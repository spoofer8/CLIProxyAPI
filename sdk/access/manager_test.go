package access

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

type credentialProvider struct {
	id, key, principal string
}

func (p credentialProvider) Identifier() string { return p.id }

func (p credentialProvider) Authenticate(_ context.Context, r *http.Request) (*Result, *AuthError) {
	if r.Header.Get("Authorization") != p.key {
		return nil, NewInvalidCredentialError()
	}
	return &Result{Provider: p.id, Principal: p.principal}, nil
}

func TestManagerPrioritySurvivesReconciliationAndIsLocal(t *testing.T) {
	manager, unrelated := NewManager(), NewManager()
	user := credentialProvider{id: "user", key: "user-key", principal: "user-id"}
	legacy := credentialProvider{id: "legacy", key: "legacy-key", principal: "legacy-key"}
	priority := []Provider{user}
	manager.SetPriorityProviders(priority)
	priority[0] = legacy
	manager.SetProviders([]Provider{credentialProvider{id: "user", key: "user-key", principal: "wrong"}, legacy})
	if got := manager.Providers(); len(got) != 2 || got[0].Identifier() != "user" {
		t.Fatalf("priority providers were duplicated or reordered: %v", got)
	}
	for _, key := range []string{"user-key", "legacy-key"} {
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		request.Header.Set("Authorization", key)
		result, errAuth := manager.Authenticate(context.Background(), request)
		if errAuth != nil || result == nil {
			t.Fatalf("valid credential rejected: %v", errAuth)
		}
		if key == "user-key" && result.Principal != "user-id" {
			t.Fatal("configured provider took precedence over the local user provider")
		}
	}
	manager.SetProviders([]Provider{legacy})
	if got := manager.Providers(); len(got) != 2 || got[0].Identifier() != "user" {
		t.Fatal("provider reconciliation removed the local user provider")
	}
	if got := manager.ConfiguredProviders(); len(got) != 1 || got[0].Identifier() != "legacy" {
		t.Fatal("configuration reconciliation includes a local priority provider")
	}
	if len(unrelated.Providers()) != 0 {
		t.Fatal("priority provider leaked into another manager")
	}
	manager.SetProviders(nil)
	manager.SetPriorityProviders(nil)
	if result, errAuth := manager.Authenticate(context.Background(), httptest.NewRequest(http.MethodGet, "/", nil)); result != nil || errAuth != nil {
		t.Fatal("disabling the only provider did not restore legacy unauthenticated behavior")
	}
}

func TestManagerConcurrentProviderReload(t *testing.T) {
	manager := NewManager()
	user := credentialProvider{id: "user", key: "user-key", principal: "user-id"}
	manager.SetPriorityProviders([]Provider{user})
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		for range 100 {
			manager.SetProviders([]Provider{credentialProvider{id: "legacy"}})
			manager.SetPriorityProviders([]Provider{user})
		}
	}()
	go func() {
		defer workers.Done()
		for range 100 {
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			request.Header.Set("Authorization", "user-key")
			result, errAuth := manager.Authenticate(context.Background(), request)
			if errAuth != nil || result == nil || result.Principal != "user-id" {
				t.Error("concurrent reload lost the priority provider")
				return
			}
		}
	}()
	workers.Wait()
}
