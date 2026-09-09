package usermgmt

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/usermgmt/store"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
)

const ConfiguredAdminKeyID = "configured-main"
const configuredAdminGenerationKey = "configured_key_generation"

type configuredAdminConfig struct {
	hash       [sha256.Size]byte
	generation string
	active     bool
}

func (r *Runtime) configuredAdminSnapshot() *configuredAdminConfig {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.configuredAdmin == nil {
		return nil
	}
	copy := *r.configuredAdmin
	return &copy
}

// ConfigureMainAPIKey keeps the configured credential solely in hashed process
// memory. The account and its history never depend on the key or its generation.
func (r *Runtime) ConfigureMainAPIKey(ctx context.Context, key string) error {
	if r == nil {
		return nil
	}
	key = strings.TrimSpace(key)
	r.mu.Lock()
	old := r.configuredAdmin
	if old == nil && (key == "" || r.store == nil) {
		r.mu.Unlock()
		return nil
	}
	digest := sha256.Sum256([]byte(key))
	if old == nil || old.active != (key != "") || old.hash != digest {
		generation, errGeneration := newID()
		if errGeneration != nil {
			r.mu.Unlock()
			return errGeneration
		}
		r.configuredAdmin = &configuredAdminConfig{hash: digest, generation: generation, active: key != ""}
	}
	activeStore := r.store != nil
	r.mu.Unlock()
	if !activeStore || key == "" {
		return nil
	}
	operationCtx, cancel := r.operationContext(ctx)
	defer cancel()
	return r.withStore(false, func(db *store.Store) error {
		_, errEnsure := db.EnsureSystemAdmin(operationCtx)
		return errEnsure
	})
}

// prepareConfiguredAdmin is called before a replacement store is published.
func (r *Runtime) prepareConfiguredAdmin(ctx context.Context, db *store.Store) error {
	configured := r.configuredAdminSnapshot()
	if configured == nil || !configured.active {
		return nil
	}
	_, errEnsure := db.EnsureSystemAdmin(ctx)
	return errEnsure
}

func configuredAdminMatches(configured *configuredAdminConfig, value string) bool {
	if configured == nil || !configured.active {
		return false
	}
	digest := sha256.Sum256([]byte(value))
	return subtle.ConstantTimeCompare(configured.hash[:], digest[:]) == 1
}

func configuredAdminUnavailable() *sdkaccess.AuthError {
	return &sdkaccess.AuthError{Code: sdkaccess.AuthErrorCodeInternal, StatusCode: http.StatusServiceUnavailable, Message: "System Admin identity is temporarily unavailable"}
}

func (r *Runtime) configuredAdminResult(ctx context.Context, state *authState, configured *configuredAdminConfig, source string) (*sdkaccess.Result, *sdkaccess.AuthError) {
	if state == nil {
		return nil, configuredAdminUnavailable()
	}
	identity := &sdkaccess.Result{Provider: AccessProviderName, Principal: store.SystemAdminUserID, Metadata: map[string]string{
		UsageScopeMetadataKey: state.scopeID, "key_id": ConfiguredAdminKeyID,
		configuredAdminGenerationKey: configured.generation, "source": source,
	}}
	confirmed, errIdentity := r.IsSystemAdmin(sdkaccess.WithResult(ctx, identity))
	if errIdentity != nil || !confirmed {
		return nil, configuredAdminUnavailable()
	}
	identity.Metadata["system_admin"] = "true"
	identity.SystemAdmin = true
	identity.Metadata["role"] = "admin"
	identity.Metadata["user_email"] = store.SystemAdminEmail
	return identity, nil
}

func (r *Runtime) validateRequestIdentity(ctx context.Context, scope *usageScope, identity *sdkaccess.Result) (store.KeyIdentity, error) {
	if identity.Metadata["key_id"] != ConfiguredAdminKeyID {
		return scope.auth.validateIdentity(ctx, identity.Principal, identity.Metadata["key_id"])
	}
	configured := r.configuredAdminSnapshot()
	if configured == nil || !configured.active || identity.Principal != store.SystemAdminUserID || identity.Metadata[configuredAdminGenerationKey] != configured.generation {
		return store.KeyIdentity{}, store.ErrNotFound
	}
	user, errUser := scope.store.GetSystemAdmin(ctx)
	if errUser != nil {
		return store.KeyIdentity{}, errUser
	}
	current := r.configuredAdminSnapshot()
	if current == nil || !current.active || current.generation != configured.generation {
		return store.KeyIdentity{}, store.ErrNotFound
	}
	return store.KeyIdentity{UserID: user.ID, Email: user.Email, Role: user.Role, KeyID: ConfiguredAdminKeyID, SystemAdmin: true}, nil
}

// IsSystemAdmin confirms the persisted flag and current configured-key
// generation. Role=admin or caller-supplied metadata alone never grants bypass.
func (r *Runtime) IsSystemAdmin(ctx context.Context) (bool, error) {
	identity, ok := sdkaccess.ResultFromContext(ctx)
	if !ok || identity.Provider != AccessProviderName {
		return false, nil
	}
	if r == nil {
		return false, ErrDisabled
	}
	release, errLease := r.BeginRequest(ctx)
	if errLease != nil {
		return false, errLease
	}
	defer release()
	r.mu.RLock()
	scope := r.scopes[identity.Metadata[UsageScopeMetadataKey]]
	r.mu.RUnlock()
	if scope == nil {
		return false, errors.New("system account scope unavailable")
	}
	queryCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(scope.cleanupCtx, cancel)
	defer func() { stop(); cancel() }()
	confirmed, errIdentity := r.validateRequestIdentity(queryCtx, scope, identity)
	if errIdentity != nil {
		return false, errIdentity
	}
	return confirmed.SystemAdmin, nil
}
