package access

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
)

func TestPolicyTargetOwnsNestedDenyNames(t *testing.T) {
	names := []string{"nested-model"}
	ctx := WithPolicyTarget(context.Background(), PolicyTarget{RequestedModel: "outer", DenyModels: names})
	names[0] = "mutated"
	target, _ := PolicyTargetFromContext(ctx)
	if target.DenyModels[0] != "nested-model" {
		t.Fatal("context retained caller-owned deny names")
	}
	target.DenyModels[0] = "mutated-again"
	again, _ := PolicyTargetFromContext(ctx)
	if again.DenyModels[0] != "nested-model" {
		t.Fatal("reader mutated stored deny names")
	}
}

func TestPolicyErrorNormalizationDoesNotExposeTransportURL(t *testing.T) {
	policy := &PolicyError{Cause: errors.New("model denied")}
	wrapped := &url.Error{Op: "Post", URL: "https://provider.invalid/path?secret=PRIVATE", Err: policy}
	normalized := NormalizePolicyError(wrapped)
	if normalized != policy || strings.Contains(normalized.Error(), "PRIVATE") {
		t.Fatal("policy normalization retained a transport URL")
	}
	ordinary := errors.New("ordinary failure")
	if NormalizePolicyError(ordinary) != ordinary {
		t.Fatal("normalization changed unrelated errors")
	}
}
