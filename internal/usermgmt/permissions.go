package usermgmt

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usermgmt/store"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
)

const maxPermissions = 128

type permissionPattern struct {
	permission store.Permission
	pattern    *regexp.Regexp
}

type compiledPermissions struct {
	rules             []permissionPattern
	modelAllowlist    bool
	providerAllowlist bool
	providerRules     bool
}

func normalizePermissions(input []store.Permission) ([]store.Permission, error) {
	if len(input) > maxPermissions {
		return nil, fmt.Errorf("at most %d permission rules are allowed", maxPermissions)
	}
	result := make([]store.Permission, 0, len(input))
	seen := make(map[string]struct{}, len(input))
	for _, permission := range input {
		permission.Scope = strings.ToLower(strings.TrimSpace(permission.Scope))
		permission.Effect = strings.ToLower(strings.TrimSpace(permission.Effect))
		permission.Value = strings.TrimSpace(permission.Value)
		if permission.Effect == "" {
			permission.Effect = "allow"
		}
		if permission.Scope != "model" && permission.Scope != "provider" {
			return nil, errors.New("permission scope must be model or provider")
		}
		if permission.Effect != "allow" && permission.Effect != "deny" {
			return nil, errors.New("permission effect must be allow or deny")
		}
		if permission.Value == "" || !validText(permission.Value, 256) || strings.IndexFunc(permission.Value, unicode.IsControl) >= 0 {
			return nil, errors.New("permission value must contain 1..256 bytes without control characters")
		}
		if permission.Scope == "provider" {
			permission.Value = strings.ToLower(permission.Value)
		}
		key := permission.Scope + ":" + permission.Value
		if _, duplicate := seen[key]; duplicate {
			return nil, errors.New("duplicate permission scope and value")
		}
		seen[key] = struct{}{}
		result = append(result, permission)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Scope == result[j].Scope {
			return result[i].Value < result[j].Value
		}
		return result[i].Scope < result[j].Scope
	})
	return result, nil
}

// Only * and ? are wildcards. Everything else is literal, including brackets,
// parentheses and backslashes. RE2 gives bounded matching without backtracking.
func compilePermissionGlob(value string) (*regexp.Regexp, error) {
	var pattern strings.Builder
	pattern.WriteString("(?s)^")
	for _, char := range value {
		switch char {
		case '*':
			pattern.WriteString(".*")
		case '?':
			pattern.WriteByte('.')
		default:
			pattern.WriteString(regexp.QuoteMeta(string(char)))
		}
	}
	pattern.WriteByte('$')
	return regexp.Compile(pattern.String())
}

func compilePermissions(permissions []store.Permission) (*compiledPermissions, error) {
	normalized, errNormalize := normalizePermissions(permissions)
	if errNormalize != nil {
		return nil, errNormalize
	}
	compiled := &compiledPermissions{}
	for _, permission := range normalized {
		pattern, errPattern := compilePermissionGlob(permission.Value)
		if errPattern != nil {
			return nil, errPattern
		}
		compiled.rules = append(compiled.rules, permissionPattern{permission: permission, pattern: pattern})
		if permission.Scope == "provider" {
			compiled.providerRules = true
		}
		if permission.Effect == "allow" {
			if permission.Scope == "model" {
				compiled.modelAllowlist = true
			} else {
				compiled.providerAllowlist = true
			}
		}
	}
	return compiled, nil
}

func modelPolicyNames(models ...string) []string {
	result := make([]string, 0, len(models)*2)
	seen := make(map[string]struct{}, len(models)*2)
	for _, model := range models {
		model = strings.TrimSpace(model)
		for _, name := range []string{model, strings.TrimSpace(thinking.ParseSuffix(model).ModelName)} {
			if name == "" {
				continue
			}
			if _, exists := seen[name]; exists {
				continue
			}
			seen[name] = struct{}{}
			result = append(result, name)
		}
	}
	return result
}

func providerPolicyNames(provider string) []string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		return nil
	}
	names := []string{provider}
	if suffix, ok := strings.CutPrefix(provider, "openai-compatible-"); ok && suffix != "" {
		names = append(names, suffix)
	}
	return names
}

func matchesAny(pattern *regexp.Regexp, values []string) bool {
	for _, value := range values {
		if pattern.MatchString(value) {
			return true
		}
	}
	return false
}

func (p *compiledPermissions) allows(target sdkaccess.PolicyTarget, deferProvider bool) bool {
	if p == nil || len(p.rules) == 0 {
		return true
	}
	visible := modelPolicyNames(target.RequestedModel, target.ResolvedModel)
	denyModels := []string{target.RequestedModel, target.ResolvedModel, target.ExecutionModel, target.PayloadModel}
	denyModels = append(denyModels, target.DenyModels...)
	allModels := modelPolicyNames(denyModels...)
	providers := providerPolicyNames(target.Provider)
	if p.providerRules && !deferProvider && len(providers) == 0 {
		return false
	}
	modelAllowed, providerAllowed := !p.modelAllowlist, !p.providerAllowlist || deferProvider
	for _, rule := range p.rules {
		if rule.permission.Scope == "provider" {
			if deferProvider {
				continue
			}
			if matchesAny(rule.pattern, providers) {
				if rule.permission.Effect == "deny" {
					return false
				}
				providerAllowed = true
			}
			continue
		}
		if rule.permission.Effect == "deny" {
			if matchesAny(rule.pattern, allModels) {
				return false
			}
		} else if matchesAny(rule.pattern, visible) {
			modelAllowed = true
		}
	}
	return modelAllowed && providerAllowed
}

func permissionDenied(model string) error {
	return quotaError(http.StatusForbidden, fmt.Sprintf("Model '%s' is not permitted for this account", model), "permission_error", "model_not_permitted")
}

func (r *Runtime) permissionsForContext(ctx context.Context) (*compiledPermissions, error) {
	identity, exists := sdkaccess.ResultFromContext(ctx)
	if !exists || identity.Provider != AccessProviderName {
		return nil, nil
	}
	if r == nil {
		return nil, unavailableScopeError()
	}
	release, errLease := r.BeginRequest(ctx)
	if errLease != nil {
		return nil, unavailableScopeError()
	}
	defer release()
	r.mu.RLock()
	scope := r.scopes[identity.Metadata[UsageScopeMetadataKey]]
	r.mu.RUnlock()
	if scope == nil {
		return nil, unavailableScopeError()
	}
	queryCtx, cancel := context.WithCancel(ctx)
	stopCleanup := context.AfterFunc(scope.cleanupCtx, cancel)
	defer func() { stopCleanup(); cancel() }()
	if _, errIdentity := scope.auth.validateIdentity(queryCtx, identity.Principal, identity.Metadata["key_id"]); errIdentity != nil {
		return nil, unavailableScopeError()
	}
	permissions, errPermissions := scope.permissions.get(queryCtx, identity.Principal)
	if errPermissions != nil {
		return nil, quotaError(http.StatusServiceUnavailable, "Account permissions are temporarily unavailable", "server_error", "permissions_unavailable")
	}
	return permissions, nil
}

// CheckPermissions is the final authority after provider selection and plugin
// rewrites. Physical deployment names are checked for denies, never allowlists.
func (r *Runtime) CheckPermissions(ctx context.Context, target sdkaccess.PolicyTarget) error {
	permissions, errPermissions := r.permissionsForContext(ctx)
	if errPermissions != nil {
		return errPermissions
	}
	if !permissions.allows(target, false) {
		return permissionDenied(target.RequestedModel)
	}
	return nil
}

// FilterProviders retains permitted fallback candidates in their original order.
// Home is a routing dispatcher; its actual provider is checked at final execution.
func (r *Runtime) FilterProviders(ctx context.Context, requested, resolved string, providers []string) ([]string, error) {
	permissions, errPermissions := r.permissionsForContext(ctx)
	if errPermissions != nil {
		return nil, errPermissions
	}
	if permissions == nil || len(permissions.rules) == 0 {
		return append([]string(nil), providers...), nil
	}
	if len(providers) == 0 {
		if !permissions.allows(sdkaccess.PolicyTarget{RequestedModel: requested, ResolvedModel: resolved}, true) {
			return nil, permissionDenied(requested)
		}
		return nil, nil
	}
	allowed := make([]string, 0, len(providers))
	for _, provider := range providers {
		target := sdkaccess.PolicyTarget{RequestedModel: requested, ResolvedModel: resolved, Provider: provider}
		if permissions.allows(target, strings.EqualFold(strings.TrimSpace(provider), "home")) {
			allowed = append(allowed, provider)
		}
	}
	if len(allowed) == 0 {
		return nil, permissionDenied(requested)
	}
	return allowed, nil
}

// ModelAllowed filters advertised visible model IDs. Unknown provider metadata
// cannot satisfy a provider allowlist. Query errors remain errors, not empty sets.
func (r *Runtime) ModelAllowed(ctx context.Context, model string, providers []string) (bool, error) {
	permissions, errPermissions := r.permissionsForContext(ctx)
	if errPermissions != nil {
		return false, errPermissions
	}
	if len(providers) == 0 {
		providers = []string{""}
	}
	for _, provider := range providers {
		if permissions.allows(sdkaccess.PolicyTarget{RequestedModel: model, ResolvedModel: model, ExecutionModel: model, Provider: provider}, false) {
			return true, nil
		}
	}
	return false, nil
}
