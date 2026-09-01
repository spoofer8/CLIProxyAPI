package helps

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

// WireAPIResponses is the openai-compatibility wire-api value selecting the
// OpenAI Responses API for upstream requests.
const WireAPIResponses = "responses"

// OpenAICompatUsesResponsesAPI reports whether the provider forwards requests
// to the upstream Responses API instead of Chat Completions. Any other value,
// including the empty default, keeps the Chat Completions wire format.
func OpenAICompatUsesResponsesAPI(compat *config.OpenAICompatibility) bool {
	return compat != nil && strings.EqualFold(strings.TrimSpace(compat.WireAPI), WireAPIResponses)
}
