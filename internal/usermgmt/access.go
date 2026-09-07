package usermgmt

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"

	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
)

const AccessProviderName = "user"

// AccessProvider returns a provider scoped to this runtime. It must be attached
// to the owning access.Manager ahead of its ordinary providers, never globally.
func (r *Runtime) AccessProvider() sdkaccess.Provider {
	if r == nil {
		return nil
	}
	if active, _ := r.Snapshot(); active == nil {
		return nil
	}
	return &userAccessProvider{runtime: r}
}

type userAccessProvider struct{ runtime *Runtime }

func (p *userAccessProvider) Identifier() string { return AccessProviderName }

func (p *userAccessProvider) Authenticate(ctx context.Context, request *http.Request) (*sdkaccess.Result, *sdkaccess.AuthError) {
	if p == nil || p.runtime == nil {
		return nil, sdkaccess.NewNotHandledError()
	}
	p.runtime.mu.RLock()
	state := p.runtime.auth
	p.runtime.mu.RUnlock()
	if state == nil {
		return nil, sdkaccess.NewNotHandledError()
	}
	if request == nil {
		return nil, sdkaccess.NewNoCredentialsError()
	}
	if ctx == nil {
		ctx = request.Context()
	}
	// Match config_access exactly: raw Authorization is accepted, Bearer is
	// case-insensitive, and candidates are tried in the same five-source order.
	authorization := request.Header.Get("Authorization")
	if parts := strings.SplitN(authorization, " ", 2); len(parts) == 2 && strings.EqualFold(parts[0], "bearer") {
		authorization = strings.TrimSpace(parts[1])
	}
	queryKey, queryToken := "", ""
	if request.URL != nil {
		queryKey = request.URL.Query().Get("key")
		queryToken = request.URL.Query().Get("auth_token")
	}
	candidates := []struct{ value, source string }{
		{authorization, "authorization"},
		{request.Header.Get("X-Goog-Api-Key"), "x-goog-api-key"},
		{request.Header.Get("X-Api-Key"), "x-api-key"},
		{queryKey, "query-key"},
		{queryToken, "query-auth-token"},
	}
	present := request.Header.Get("Authorization") != ""
	for _, candidate := range candidates {
		if candidate.value == "" {
			continue
		}
		present = true
		// Legacy keys cannot have been issued by this service. Avoid a DB lookup
		// for them, so their fallback remains independent of database outages.
		if !strings.HasPrefix(candidate.value, "sk-cpa-") {
			continue
		}
		digest := sha256.Sum256([]byte(candidate.value))
		identity, errLookup := state.authenticate(ctx, hex.EncodeToString(digest[:]))
		if errLookup != nil {
			continue
		}
		return &sdkaccess.Result{
			Provider: AccessProviderName, Principal: identity.UserID,
			Metadata: map[string]string{
				"user_email": identity.Email, "key_id": identity.KeyID,
				"role": identity.Role, "source": candidate.source,
			},
		}, nil
	}
	if !present {
		return nil, sdkaccess.NewNoCredentialsError()
	}
	// InvalidCredential allows the manager to continue to legacy config keys.
	return nil, sdkaccess.NewInvalidCredentialError()
}
