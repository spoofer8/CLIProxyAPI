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
	if admission, ok := r.capturedAdmission(ctx, scope); ok && admission.admitted && admission.identityValidated {
		return nil
	}
	queryCtx, cancel := context.WithCancel(ctx)
	stopCleanup := context.AfterFunc(scope.cleanupCtx, cancel)
	defer func() { stopCleanup(); cancel() }()
	confirmed, errIdentity := r.validateRequestIdentity(queryCtx, scope, identity)
	if errIdentity != nil {
		return unavailableScopeError()
	}
	if admission, exists := r.capturedAdmission(ctx, scope); exists && !admission.identityValidated {
		var permissions *compiledPermissions
		if !confirmed.SystemAdmin {
			var errPermissions error
			permissions, errPermissions = scope.permissions.get(queryCtx, identity.Principal)
			if errPermissions != nil {
				return quotaError(503, "Account permissions are temporarily unavailable", "server_error", "permissions_unavailable")
			}
		}
		r.snapshotCapturedPolicy(ctx, scope, permissions)
	}
	if quotaErr := r.CheckQuota(ctx); quotaErr != nil {
		return quotaErr
	}
	return nil
}
