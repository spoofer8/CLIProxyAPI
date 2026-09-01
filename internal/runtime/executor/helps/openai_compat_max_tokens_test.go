package helps

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestPromoteMaxTokensRenamesLegacyParameter(t *testing.T) {
	out := PromoteMaxTokens([]byte(`{"model":"gpt-5.5","max_tokens":50}`))
	if got := string(out); got != `{"model":"gpt-5.5","max_completion_tokens":50}` {
		t.Fatalf("promoted payload = %s", got)
	}
}

func TestPromoteMaxTokensPrefersExistingCompletionTokens(t *testing.T) {
	out := PromoteMaxTokens([]byte(`{"max_tokens":50,"max_completion_tokens":80}`))
	if got := string(out); got != `{"max_completion_tokens":80}` {
		t.Fatalf("promoted payload = %s", got)
	}
}

func TestPromoteMaxTokensLeavesPayloadWithoutMaxTokens(t *testing.T) {
	in := `{"model":"gpt-5.5","max_completion_tokens":80}`
	if got := string(PromoteMaxTokens([]byte(in))); got != in {
		t.Fatalf("payload mutated: %s", got)
	}
}

func TestShouldPromoteMaxTokensForCompat(t *testing.T) {
	if ShouldPromoteMaxTokensForCompat(nil) {
		t.Fatal("nil compat must not promote")
	}
	if ShouldPromoteMaxTokensForCompat(&config.OpenAICompatibility{}) {
		t.Fatal("default compat must not promote")
	}
	if !ShouldPromoteMaxTokensForCompat(&config.OpenAICompatibility{UseMaxCompletionTokens: true}) {
		t.Fatal("enabled compat must promote")
	}
}
