package access

import (
	"context"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
)

type localUserProvider struct{}

func (localUserProvider) Identifier() string { return "user" }
func (localUserProvider) Authenticate(context.Context, *http.Request) (*sdkaccess.Result, *sdkaccess.AuthError) {
	return nil, sdkaccess.NewNotHandledError()
}

func TestReconcilePreservesPriorityWithoutFalseRemoval(t *testing.T) {
	manager := sdkaccess.NewManager()
	manager.SetPriorityProviders([]sdkaccess.Provider{localUserProvider{}})
	for range 2 {
		changed, errApply := ApplyAccessProviders(manager, &config.Config{}, &config.Config{})
		if errApply != nil || changed {
			t.Fatalf("unchanged configuration reported a priority-provider removal: changed=%v error=%v", changed, errApply)
		}
		if providers := manager.Providers(); len(providers) != 1 || providers[0].Identifier() != "user" {
			t.Fatal("configuration reconciliation removed the priority provider")
		}
	}
	for _, provider := range sdkaccess.RegisteredProviders() {
		if provider.Identifier() == "user" {
			t.Fatal("manager-local user provider leaked into the global registry")
		}
	}
}
