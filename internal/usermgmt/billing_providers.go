package usermgmt

import (
	"net/url"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
)

// ConfigureBillingProviders records endpoint-proven vendors, never guesses from
// custom group names or model aliases. Each reload replaces the immutable map.
func (r *Runtime) ConfigureBillingProviders(cfg *config.Config) {
	if r == nil {
		return
	}
	providers := make(map[string]string)
	if cfg != nil {
		for _, provider := range cfg.OpenAICompatibility {
			if provider.Disabled {
				continue
			}
			vendor := billingVendorFromEndpoint(provider.BaseURL)
			if vendor == "" {
				continue
			}
			name := strings.ToLower(strings.TrimSpace(provider.Name))
			providers[name] = vendor
			providers[util.OpenAICompatibleProviderKey(name)] = vendor
		}
	}
	r.mu.Lock()
	r.billingProviders = providers
	r.mu.Unlock()
}

func billingVendorFromEndpoint(endpoint string) string {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return ""
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	switch {
	case strings.HasSuffix(host, ".openai.azure.com"), strings.HasSuffix(host, ".services.ai.azure.com"), strings.HasSuffix(host, ".models.ai.azure.com"):
		return "azure"
	case host == "api.openai.com":
		return "openai"
	case host == "api.anthropic.com":
		return "anthropic"
	case host == "generativelanguage.googleapis.com", host == "aiplatform.googleapis.com", strings.HasSuffix(host, "-aiplatform.googleapis.com"):
		return "google"
	default:
		return ""
	}
}

func (r *Runtime) billingProviderCandidates(provider, _ string) []string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	result := []string{provider}
	if r == nil {
		return result
	}
	r.mu.RLock()
	vendor := r.billingProviders[provider]
	r.mu.RUnlock()
	if vendor != "" && vendor != provider {
		result = append(result, vendor)
	}
	return result
}
