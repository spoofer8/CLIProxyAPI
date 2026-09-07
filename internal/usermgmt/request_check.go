package usermgmt

import (
	"context"

	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
)

// CheckRequest revalidates the current user/key state before checking quota.
// It runs for each execution/frame, so a prior WebSocket authentication does not
// survive an administrative revocation or disable when quota enforcement is off.
func (r *Runtime) CheckRequest(ctx context.Context) error {
	identity, exists := sdkaccess.ResultFromContext(ctx)
	if !exists || identity.Provider != AccessProviderName {
		return nil
	}
	if r == nil {
		return unavailableScopeError()
	}
	release, errLease := r.BeginRequest(ctx)
	if errLease != nil {
		return unavailableScopeError()
	}
	defer release()
	r.mu.RLock()
	scope := r.scopes[identity.Metadata[UsageScopeMetadataKey]]
	r.mu.RUnlock()
	if scope == nil {
		return unavailableScopeError()
	}
	queryCtx, cancel := context.WithCancel(ctx)
	stopCleanup := context.AfterFunc(scope.cleanupCtx, cancel)
	defer func() { stopCleanup(); cancel() }()
	if _, errIdentity := scope.auth.validateIdentity(queryCtx, identity.Principal, identity.Metadata["key_id"]); errIdentity != nil {
		return unavailableScopeError()
	}
	if quotaErr := r.CheckQuota(ctx); quotaErr != nil {
		return quotaErr
	}
	return nil
}
