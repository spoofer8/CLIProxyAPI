package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

type finalModelDenied struct{}

func (finalModelDenied) Error() string   { return "final model denied" }
func (finalModelDenied) StatusCode() int { return http.StatusForbidden }
func (finalModelDenied) ResponseBody() []byte {
	return []byte(`{"error":{"type":"permission_error","code":"model_not_permitted"}}`)
}

func finalModelPolicyContext(provider string) context.Context {
	ctx := sdkaccess.WithPolicyTarget(context.Background(), sdkaccess.PolicyTarget{
		RequestedModel: "visible-alias", ResolvedModel: "visible-alias", ExecutionModel: "allowed-model", Provider: provider,
	})
	return sdkaccess.WithRequestHooks(ctx, sdkaccess.RequestHooks{Authorize: func(_ context.Context, target sdkaccess.PolicyTarget) error {
		if target.PayloadModel == "blocked-model" {
			return finalModelDenied{}
		}
		return nil
	}})
}

func modelOverrideConfig() *config.Config {
	return &config.Config{Payload: config.PayloadConfig{Override: []config.PayloadRule{{
		Models: []config.PayloadModelRule{{Name: "*"}}, Params: map[string]any{"model": "blocked-model"},
	}}}}
}

func TestOpenAICompatFinalPayloadOverrideCannotBypassPolicy(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		if gjson.GetBytes(body, "model").String() != "blocked-model" {
			t.Error("fixture did not apply its model override")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"test","object":"chat.completion","model":"blocked-model","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer server.Close()
	auth := &cliproxyauth.Auth{ID: "test-credential", Provider: "openai-compatible-test", Attributes: map[string]string{"base_url": server.URL + "/v1", "api_key": "local-test-key"}}
	executor := NewOpenAICompatExecutor(auth.Provider, modelOverrideConfig())
	req := cliproxyexecutor.Request{Model: "allowed-model", Payload: []byte(`{"model":"visible-alias","messages":[{"role":"user","content":"hello"}]}`)}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")}
	ctx := finalModelPolicyContext(auth.Provider)
	_, errExecute := executor.Execute(ctx, auth, req, opts)
	if !sdkaccess.IsPolicyError(errExecute) || calls.Load() != 0 {
		t.Fatalf("nonstream payload override bypassed policy: calls=%d error=%v", calls.Load(), errExecute)
	}
	opts.Stream = true
	_, errStream := executor.ExecuteStream(ctx, auth, req, opts)
	if !sdkaccess.IsPolicyError(errStream) || calls.Load() != 0 {
		t.Fatalf("stream payload override bypassed policy: calls=%d error=%v", calls.Load(), errStream)
	}
	opts.Stream = false
	if _, errLegacy := executor.Execute(context.Background(), auth, req, opts); errLegacy != nil || calls.Load() != 1 {
		t.Fatalf("legacy payload override behavior changed: calls=%d error=%v", calls.Load(), errLegacy)
	}
}

func TestSharedWebsocketSendChecksFinalFrameBeforeConnection(t *testing.T) {
	ctx := finalModelPolicyContext("codex")
	for _, frame := range []string{
		`{"type":"response.create","model":"blocked-model"}`,
		`{"type":"response.create","model":"allowed-model","model":"blocked-model"}`,
	} {
		errSend := writeCodexWebsocketMessage(ctx, nil, nil, []byte(frame))
		if !sdkaccess.IsPolicyError(errSend) {
			t.Fatal("Codex/xAI final frame reached connection handling before permission check")
		}
	}
	errLegacy := writeCodexWebsocketMessage(context.Background(), nil, nil, []byte(`{"model":"blocked-model"}`))
	if errLegacy == nil || sdkaccess.IsPolicyError(errLegacy) || !strings.Contains(errLegacy.Error(), "conn is nil") {
		t.Fatal("legacy websocket send gained policy rejection")
	}
}

func TestAIStudioFinalPayloadOverrideRejectedBeforeRelay(t *testing.T) {
	executor := NewAIStudioExecutor(modelOverrideConfig(), "aistudio", nil)
	auth := &cliproxyauth.Auth{ID: "test-credential", Provider: "aistudio"}
	req := cliproxyexecutor.Request{Model: "allowed-model", Payload: []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`)}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("gemini")}
	ctx := finalModelPolicyContext("aistudio")
	_, errExecute := executor.Execute(ctx, auth, req, opts)
	if !sdkaccess.IsPolicyError(errExecute) {
		t.Fatalf("AIStudio nonstream reached relay with forbidden final model: %v", errExecute)
	}
	opts.Stream = true
	_, errStream := executor.ExecuteStream(ctx, auth, req, opts)
	if !sdkaccess.IsPolicyError(errStream) {
		t.Fatalf("AIStudio stream reached relay with forbidden final model: %v", errStream)
	}
	opts.Stream = false
	_, errCount := executor.CountTokens(ctx, auth, req, opts)
	if !sdkaccess.IsPolicyError(errCount) {
		t.Fatalf("AIStudio token count reached relay with forbidden final model: %v", errCount)
	}
}
