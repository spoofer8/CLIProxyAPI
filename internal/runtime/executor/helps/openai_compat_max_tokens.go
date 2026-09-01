package helps

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ShouldPromoteMaxTokensForCompat reports whether the provider requires the
// max_completion_tokens parameter in place of the legacy max_tokens.
func ShouldPromoteMaxTokensForCompat(compat *config.OpenAICompatibility) bool {
	return compat != nil && compat.UseMaxCompletionTokens
}

// PromoteMaxTokens renames max_tokens to max_completion_tokens. When both are
// present the existing max_completion_tokens wins and max_tokens is dropped,
// because upstreams that need this rename reject max_tokens outright.
func PromoteMaxTokens(payload []byte) []byte {
	maxTokens := gjson.GetBytes(payload, "max_tokens")
	if !maxTokens.Exists() {
		return payload
	}
	out := payload
	if !gjson.GetBytes(payload, "max_completion_tokens").Exists() {
		updated, err := sjson.SetRawBytes(out, "max_completion_tokens", []byte(maxTokens.Raw))
		if err != nil {
			return payload
		}
		out = updated
	}
	updated, err := sjson.DeleteBytes(out, "max_tokens")
	if err != nil {
		return payload
	}
	return updated
}

// OpenAICompatForRequestURL returns the configured openai-compatibility provider
// that requestURL is addressed to, matching on base URL prefix. It lets callers
// holding only an outgoing URL -- such as the management api-call proxy backing
// the Web UI connectivity test -- apply the same request compatibility rules the
// executor applies, so both paths agree on what a provider accepts.
func OpenAICompatForRequestURL(cfg *config.Config, requestURL string) *config.OpenAICompatibility {
	if cfg == nil {
		return nil
	}
	requestURL = strings.TrimSpace(requestURL)
	if requestURL == "" {
		return nil
	}
	var match *config.OpenAICompatibility
	longest := 0
	for i := range cfg.OpenAICompatibility {
		compat := &cfg.OpenAICompatibility[i]
		if compat.Disabled {
			continue
		}
		base := strings.TrimSuffix(strings.TrimSpace(compat.BaseURL), "/")
		if base == "" || !strings.HasPrefix(requestURL, base) {
			continue
		}
		if len(base) > longest {
			match, longest = compat, len(base)
		}
	}
	return match
}
