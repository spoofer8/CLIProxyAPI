package helps

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestOpenAICompatUsesResponsesAPI(t *testing.T) {
	for _, tc := range []struct {
		name string
		wire string
		want bool
	}{
		{"default is chat completions", "", false},
		{"explicit chat", "chat", false},
		{"responses", "responses", true},
		{"responses is case and space insensitive", "  Responses ", true},
		{"unknown value falls back to chat", "grpc", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := OpenAICompatUsesResponsesAPI(&config.OpenAICompatibility{WireAPI: tc.wire})
			if got != tc.want {
				t.Fatalf("OpenAICompatUsesResponsesAPI(%q) = %v, want %v", tc.wire, got, tc.want)
			}
		})
	}
	if OpenAICompatUsesResponsesAPI(nil) {
		t.Fatal("nil compat must not use responses")
	}
}
