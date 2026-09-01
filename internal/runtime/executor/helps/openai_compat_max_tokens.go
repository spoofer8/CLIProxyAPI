package helps

import (
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
