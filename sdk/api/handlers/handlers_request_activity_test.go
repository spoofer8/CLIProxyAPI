package handlers

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
)

type activityBudgetFailure struct{}

func (activityBudgetFailure) Error() string   { return "budget exhausted" }
func (activityBudgetFailure) StatusCode() int { return 429 }
func (activityBudgetFailure) ResponseBody() []byte {
	return []byte(`{"error":{"code":"budget_exceeded","available_at":"2026-09-10T00:00:00Z"}}`)
}
func (activityBudgetFailure) ResponseHeaders() http.Header {
	return http.Header{"Retry-After": []string{"3600"}}
}
func (activityBudgetFailure) DirectResponse() bool { return true }

func TestRequestActivityBudgetHeadersSurvivePolicyAndHTTP(t *testing.T) {
	wrapped := fmt.Errorf("transport: %w", &sdkaccess.PolicyError{Cause: activityBudgetFailure{}})
	for _, message := range []*interfaces.ErrorMessage{requestHookError(wrapped), executionErrorMessage(wrapped)} {
		if message.StatusCode != 429 || message.Headers.Get("Retry-After") != "3600" {
			t.Fatal("SDK lost budget retry headers")
		}
		handler := &BaseAPIHandler{}
		engine := gin.New()
		engine.GET("/", func(c *gin.Context) { handler.WriteErrorResponse(c, message) })
		recorder := httptest.NewRecorder()
		engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
		if recorder.Code != 429 || recorder.Header().Get("Retry-After") != "3600" {
			t.Fatal("HTTP response lost retry header")
		}
	}
}
