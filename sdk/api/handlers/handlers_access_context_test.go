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
}
