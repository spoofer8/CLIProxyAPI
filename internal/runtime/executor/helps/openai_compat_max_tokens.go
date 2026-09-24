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

// ShouldUseMaxCompletionTokensForModel reports whether the resolved model under an
// OpenAI compatibility configuration has use-max-completion-tokens set to true.
func ShouldUseMaxCompletionTokensForModel(compat *config.OpenAICompatibility, upstreamModel, requestedModel string) bool {
	if compat == nil {
		return false
	}
	if useMCT, matched := openAICompatibilityModelUsesMaxCompletionTokens(compat.Models, upstreamModel); matched {
		return useMCT || compat.UseMaxCompletionTokens
	}
	useMCT, _ := openAICompatibilityModelUsesMaxCompletionTokens(compat.Models, requestedModel)
	return useMCT || compat.UseMaxCompletionTokens
}

func openAICompatibilityModelUsesMaxCompletionTokens(models []config.OpenAICompatibilityModel, model string) (bool, bool) {
	model = normalizeOpenAICompatibilityModelName(model)
	if model == "" {
		return false, false
	}

	for i := range models {
		if strings.EqualFold(model, normalizeOpenAICompatibilityModelName(models[i].Name)) {
			return models[i].UseMaxCompletionTokens, true
		}
	}

	for i := range models {
		if strings.EqualFold(model, normalizeOpenAICompatibilityModelName(models[i].Alias)) {
			return models[i].UseMaxCompletionTokens, true
		}
	}
	return false, false
}

// NormalizeOpenAIMaxTokens normalizes max_tokens and max_completion_tokens according to the model's preference.
// When useMaxCompletionTokens is true, it ensures max_completion_tokens is set and max_tokens is removed.
// When useMaxCompletionTokens is false, it ensures max_tokens is used and max_completion_tokens is removed.
func NormalizeOpenAIMaxTokens(payload []byte, useMaxCompletionTokens bool) []byte {
	if len(payload) == 0 {
		return payload
	}

	hasMaxTokens := gjson.GetBytes(payload, "max_tokens").Exists()
	hasMaxCompletionTokens := gjson.GetBytes(payload, "max_completion_tokens").Exists()
	if !hasMaxTokens && !hasMaxCompletionTokens {
		return payload
	}

	if useMaxCompletionTokens {
		if hasMaxTokens && !hasMaxCompletionTokens {
			val := gjson.GetBytes(payload, "max_tokens")
			if val.Raw != "" {
				payload, _ = sjson.SetRawBytes(payload, "max_completion_tokens", []byte(val.Raw))
			} else {
				payload, _ = sjson.SetBytes(payload, "max_completion_tokens", val.Value())
			}
		}
		if hasMaxTokens {
			payload, _ = sjson.DeleteBytes(payload, "max_tokens")
		}
	} else {
		if hasMaxCompletionTokens && !hasMaxTokens {
			val := gjson.GetBytes(payload, "max_completion_tokens")
			if val.Raw != "" {
				payload, _ = sjson.SetRawBytes(payload, "max_tokens", []byte(val.Raw))
			} else {
				payload, _ = sjson.SetBytes(payload, "max_tokens", val.Value())
			}
		}
		if hasMaxCompletionTokens {
			payload, _ = sjson.DeleteBytes(payload, "max_completion_tokens")
		}
	}
	return payload
}
