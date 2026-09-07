package helps

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestUsageReporterPrefersImmutableAccessPrincipal(t *testing.T) {
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginCtx.Set("userApiKey", "reused-context-secret")
	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	ctx = sdkaccess.WithResult(ctx, &sdkaccess.Result{Provider: "user", Principal: "original-user"})
	reporter := NewUsageReporter(ctx, "openai", "model", nil)
	ginCtx.Set("userApiKey", "different-secret")
	if key := APIKeyFromContext(ctx); key != "original-user" {
		t.Fatalf("usage read mutable Gin identity: %q", key)
	}
	if record := reporter.buildRecord(usage.Detail{TotalTokens: 7}, false); record.APIKey != "original-user" {
		t.Fatalf("usage record was attributed to %q", record.APIKey)
	}
	legacyCtx := context.WithValue(context.Background(), "gin", ginCtx)
	if key := APIKeyFromContext(legacyCtx); key != "different-secret" {
		t.Fatal("legacy SDK callers lost their Gin-only principal")
	}
	for _, provider := range []string{"config-access", "home"} {
		legacyCtx := sdkaccess.WithResult(legacyCtx, &sdkaccess.Result{Provider: provider, Principal: "initial-frontend-identity"})
		if key := APIKeyFromContext(legacyCtx); key != "different-secret" {
			t.Fatalf("provider %q lost its later Gin usage-identity override", provider)
		}
		if record := NewUsageReporter(legacyCtx, "openai", "model", nil).buildRecord(usage.Detail{}, false); record.APIKey != "different-secret" {
			t.Fatalf("provider %q usage record ignored the later Gin identity", provider)
		}
	}
}
