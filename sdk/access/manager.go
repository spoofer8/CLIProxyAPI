package access

import (
	"context"
	"net/http"
	"strings"
	"sync"
)

// Manager coordinates authentication providers.
type Manager struct {
	mu                sync.RWMutex
	providers         []Provider
	priorityProviders []Provider
}

// NewManager constructs an empty manager.
func NewManager() *Manager {
	return &Manager{}
}

// SetProviders replaces the reconciled provider list without changing priority providers.
func (m *Manager) SetProviders(providers []Provider) {
	if m == nil {
		return
	}
	cloned := make([]Provider, len(providers))
	copy(cloned, providers)
	m.mu.Lock()
	m.providers = cloned
	m.mu.Unlock()
}

// SetPriorityProviders replaces manager-local providers evaluated before configured
// and plugin providers. These providers are never added to the global registry.
func (m *Manager) SetPriorityProviders(providers []Provider) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.priorityProviders = append([]Provider(nil), providers...)
	m.mu.Unlock()
}

// ConfiguredProviders returns the list installed by SetProviders, excluding
// manager-local priority providers when comparing configuration changes.
func (m *Manager) ConfiguredProviders() []Provider {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]Provider(nil), m.providers...)
}

// Providers returns a snapshot of the active providers.
func (m *Manager) Providers() []Provider {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	snapshot := make([]Provider, 0, len(m.priorityProviders)+len(m.providers))
	priorityIDs := make(map[string]struct{}, len(m.priorityProviders))
	for _, provider := range m.priorityProviders {
		if provider == nil {
			continue
		}
		id := strings.TrimSpace(provider.Identifier())
		if _, exists := priorityIDs[id]; exists {
			continue
		}
		priorityIDs[id] = struct{}{}
		snapshot = append(snapshot, provider)
	}
	for _, provider := range m.providers {
		if provider == nil {
			continue
		}
		if _, exists := priorityIDs[strings.TrimSpace(provider.Identifier())]; !exists {
			snapshot = append(snapshot, provider)
		}
	}
	return snapshot
}

// Authenticate evaluates providers until one succeeds.
func (m *Manager) Authenticate(ctx context.Context, r *http.Request) (*Result, *AuthError) {
	if m == nil {
		return nil, nil
	}
	providers := m.Providers()
	if len(providers) == 0 {
		return nil, nil
	}

	var (
		missing bool
		invalid bool
	)

	for _, provider := range providers {
		if provider == nil {
			continue
		}
		res, authErr := provider.Authenticate(ctx, r)
		if authErr == nil {
			return res, nil
		}
		if IsAuthErrorCode(authErr, AuthErrorCodeNotHandled) {
			continue
		}
		if IsAuthErrorCode(authErr, AuthErrorCodeNoCredentials) {
			missing = true
			continue
		}
		if IsAuthErrorCode(authErr, AuthErrorCodeInvalidCredential) {
			invalid = true
			continue
		}
		return nil, authErr
	}

	if invalid {
		return nil, NewInvalidCredentialError()
	}
	if missing {
		return nil, NewNoCredentialsError()
	}
	return nil, NewNoCredentialsError()
}
