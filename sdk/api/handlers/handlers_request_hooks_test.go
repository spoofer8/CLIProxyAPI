package handlers

import (
	"context"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

type quotaHookError struct{}

func (quotaHookError) Error() string        { return "quota exceeded" }
func (quotaHookError) StatusCode() int      { return http.StatusTooManyRequests }
func (quotaHookError) ResponseBody() []byte { return []byte(`{"error":{"code":"quota_exceeded"}}`) }

func TestExecutionGuardsRunBeforeRoutingOnEveryCall(t *testing.T) {
	var checks, leases int
	ctx := sdkaccess.WithRequestHooks(context.Background(), sdkaccess.RequestHooks{
		Begin: func(context.Context) (func(), error) {
			leases++
			return func() { leases-- }, nil
		},
		Check: func(context.Context) error { checks++; return quotaHookError{} },
	})
	handler := &BaseAPIHandler{Cfg: &config.SDKConfig{}}
	for range 2 {
		_, _, executeError := handler.ExecuteWithAuthManager(ctx, "openai", "missing-model", nil, "")
		_, _, countError := handler.ExecuteCountWithAuthManager(ctx, "openai", "missing-model", nil, "")
		_, _, streamErrors := handler.ExecuteStreamWithAuthManager(ctx, "openai", "missing-model", nil, "")
		streamError := <-streamErrors
		for _, result := range []*interfaces.ErrorMessage{executeError, countError, streamError} {
			if result == nil || result.StatusCode != http.StatusTooManyRequests || !result.DirectResponse || string(result.Body) != string((quotaHookError{}).ResponseBody()) {
				t.Fatalf("execution did not preserve the admission error: %+v", result)
			}
		}
	}
	if checks != 6 || leases != 0 {
		t.Fatalf("guard calls=%d live leases=%d, want 6 and 0", checks, leases)
	}
}
