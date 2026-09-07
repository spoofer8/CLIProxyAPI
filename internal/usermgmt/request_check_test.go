package usermgmt

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/usermgmt/store"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
)

func requireRequestUnauthorized(t *testing.T, runtime *Runtime, ctx context.Context) {
	t.Helper()
	var requestErr *QuotaError
	if errCheck := runtime.CheckRequest(ctx); !errors.As(errCheck, &requestErr) || requestErr.StatusCode() != http.StatusUnauthorized {
		t.Fatalf("existing identity should be unauthorized, got %v", errCheck)
	}
}

func TestCheckRequestInvalidatesExistingIdentityInAllQuotaModes(t *testing.T) {
	for _, enforce := range []bool{false, true} {
		name := "accounting-only"
		if enforce {
			name = "quota-enforced"
		}
		t.Run(name, func(t *testing.T) {
			runtime, engine, _ := testRuntime(t)
			user, key, plaintext := createTestIdentity(t, engine)
			ctx := usageContext(t, runtime, plaintext)
			_, cfg := runtime.Snapshot()
			cfg.Quota.Enforce = enforce
			if errApply := runtime.Apply(context.Background(), cfg); errApply != nil {
				t.Fatal(errApply)
			}
			for range 2 {
				if errCheck := runtime.CheckRequest(ctx); errCheck != nil {
					t.Fatal(errCheck)
				}
			}
			requestJSON(t, engine, http.MethodPatch, "/users/"+user.ID, `{"status":"disabled"}`, http.StatusOK)
			requireRequestUnauthorized(t, runtime, ctx)
			requestJSON(t, engine, http.MethodPatch, "/users/"+user.ID, `{"status":"active"}`, http.StatusOK)
			if errCheck := runtime.CheckRequest(ctx); errCheck != nil {
				t.Fatal(errCheck)
			}
			requestJSON(t, engine, http.MethodDelete, "/keys/"+key.ID, "", http.StatusOK)
			requireRequestUnauthorized(t, runtime, ctx)
			issued := requestJSON(t, engine, http.MethodPost, "/users/"+user.ID+"/keys", `{}`, http.StatusCreated)
			var replacement struct {
				Key string `json:"key"`
			}
			if errJSON := json.Unmarshal(issued.Body.Bytes(), &replacement); errJSON != nil {
				t.Fatal(errJSON)
			}
			replacementCtx := usageContext(t, runtime, replacement.Key)
			if errCheck := runtime.CheckRequest(replacementCtx); errCheck != nil {
				t.Fatal(errCheck)
			}
			requestJSON(t, engine, http.MethodDelete, "/users/"+user.ID, "", http.StatusOK)
			requireRequestUnauthorized(t, runtime, replacementCtx)
		})
	}
}

func TestCheckRequestKeyOwnershipAndLegacyBypass(t *testing.T) {
	runtime, engine, _ := testRuntime(t)
	_, _, plaintext := createTestIdentity(t, engine)
	ctx := usageContext(t, runtime, plaintext)
	created := requestJSON(t, engine, http.MethodPost, "/users", `{"email":"bob@example.com"}`, http.StatusCreated)
	var bob store.User
	if errJSON := json.Unmarshal(created.Body.Bytes(), &bob); errJSON != nil {
		t.Fatal(errJSON)
	}
	issued := requestJSON(t, engine, http.MethodPost, "/users/"+bob.ID+"/keys", `{}`, http.StatusCreated)
	var result struct {
		APIKey store.APIKey `json:"api_key"`
	}
	if errJSON := json.Unmarshal(issued.Body.Bytes(), &result); errJSON != nil {
		t.Fatal(errJSON)
	}
	identity, _ := sdkaccess.ResultFromContext(ctx)
	identity.Metadata["key_id"] = result.APIKey.ID
	requireRequestUnauthorized(t, runtime, sdkaccess.WithResult(context.Background(), identity))
	delete(identity.Metadata, "key_id")
	requireRequestUnauthorized(t, runtime, sdkaccess.WithResult(context.Background(), identity))
	legacy := sdkaccess.WithResult(context.Background(), &sdkaccess.Result{Provider: "config", Principal: "legacy-key"})
	if errCheck := runtime.CheckRequest(legacy); errCheck != nil {
		t.Fatal("legacy identity did not bypass user revalidation")
	}
}
