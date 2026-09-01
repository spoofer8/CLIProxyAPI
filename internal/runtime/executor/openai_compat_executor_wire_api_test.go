package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func newWireAPIExecutor(t *testing.T, wireAPI string, handler http.HandlerFunc) (*OpenAICompatExecutor, *cliproxyauth.Auth, func()) {
	t.Helper()
	server := httptest.NewServer(handler)
	cfg := &config.Config{OpenAICompatibility: []config.OpenAICompatibility{{
		Name:                   "wire",
		BaseURL:                server.URL,
		WireAPI:                wireAPI,
		UseMaxCompletionTokens: true,
	}}}
	auth := &cliproxyauth.Auth{Provider: "openai-compatible-wire", Attributes: map[string]string{
		"source":       "config:wire[0]",
		"base_url":     server.URL,
		"api_key":      "secret",
		"compat_name":  "wire",
		"config_index": "0",
	}}
	return NewOpenAICompatExecutor("openai-compatible-wire", cfg), auth, server.Close
}

func TestOpenAICompatExecutorResponsesWireTargetsResponsesEndpoint(t *testing.T) {
	paths := make(chan string, 1)
	bodies := make(chan []byte, 1)
	executor, auth, closeServer := newWireAPIExecutor(t, "responses", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		paths <- r.URL.Path
		bodies <- body
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp_1","object":"response","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)
	})
	defer closeServer()

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "upstream-model",
		Payload: []byte(`{"model":"upstream-model","messages":[{"role":"user","content":"hi"}],"max_tokens":16}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if got := <-paths; got != "/responses" {
		t.Fatalf("path = %q, want /responses", got)
	}
	body := <-bodies
	if gjson.GetBytes(body, "max_completion_tokens").Exists() {
		t.Fatalf("max_tokens promotion must not apply to responses wire: %s", body)
	}
}

func TestOpenAICompatExecutorChatWireRemainsDefault(t *testing.T) {
	paths := make(chan string, 1)
	bodies := make(chan []byte, 1)
	executor, auth, closeServer := newWireAPIExecutor(t, "", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		paths <- r.URL.Path
		bodies <- body
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	})
	defer closeServer()

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "upstream-model",
		Payload: []byte(`{"model":"upstream-model","messages":[{"role":"user","content":"hi"}],"max_tokens":16}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if got := <-paths; got != "/chat/completions" {
		t.Fatalf("path = %q, want /chat/completions", got)
	}
	if body := <-bodies; !gjson.GetBytes(body, "max_completion_tokens").Exists() {
		t.Fatalf("chat wire should still promote max_tokens: %s", body)
	}
}

// The Responses API has no [DONE] sentinel; the stream must complete on
// response.completed instead of being reported as a truncated upstream stream.
func TestOpenAICompatExecutorResponsesWireStreamCompletesWithoutDone(t *testing.T) {
	executor, auth, closeServer := newWireAPIExecutor(t, "responses", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Errorf("stream path = %q, want /responses", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"status\":\"in_progress\",\"output\":[]}}\n\n")
		_, _ = io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":7,\"output_tokens\":3,\"total_tokens\":10}}}\n\n")
	})
	defer closeServer()

	stream, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "upstream-model",
		Payload: []byte(`{"model":"upstream-model","messages":[{"role":"user","content":"hi"}],"stream":true}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response"), Stream: true})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
	}
}
