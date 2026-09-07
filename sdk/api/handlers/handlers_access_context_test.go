package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestExecutionContextPreservesAccessAfterRequestAndGinReuse(t *testing.T) {
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	requestCtx = sdkaccess.WithResult(requestCtx, &sdkaccess.Result{Provider: "user", Principal: "first-user", Metadata: map[string]string{"key_id": "first-key"}})
	guardCalled := false
	requestCtx = sdkaccess.WithRequestHooks(requestCtx, sdkaccess.RequestHooks{Check: func(context.Context) error { guardCalled = true; return nil }})
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(requestCtx)
	handler := &BaseAPIHandler{Cfg: &config.SDKConfig{}}
	executionCtx, cancel := handler.GetContextWithCancel(nil, ginCtx, context.Background())
	defer cancel()
	cancelRequest()
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ginCtx.Set("userApiKey", "second-user")
	ginCtx.Set("accessMetadata", map[string]string{"key_id": "second-key"})
	result, ok := sdkaccess.ResultFromContext(executionCtx)
	if !ok || result.Principal != "first-user" || result.Metadata["key_id"] != "first-key" {
		t.Fatalf("execution context lost the original request identity: %+v", result)
	}
	hooks, ok := sdkaccess.RequestHooksFromContext(executionCtx)
	if !ok || hooks.Check == nil {
		t.Fatal("execution context did not carry the request guard")
	}
	if errCheck := hooks.Check(executionCtx); errCheck != nil || !guardCalled {
		t.Fatal("execution context lost the original request guard")
	}
}
