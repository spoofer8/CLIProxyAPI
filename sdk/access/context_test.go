package access

import (
	"context"
	"testing"
)

func TestResultContextDoesNotRetainMutableIdentity(t *testing.T) {
	original := &Result{Provider: "user", Principal: "user-id", Metadata: map[string]string{"role": "user"}}
	ctx := WithResult(nil, original)
	original.Principal, original.Metadata["role"] = "other-user", "admin"
	result, ok := ResultFromContext(ctx)
	if !ok || result.Principal != "user-id" || result.Metadata["role"] != "user" {
		t.Fatal("provider mutation changed the stored identity")
	}
	result.Principal, result.Metadata["role"] = "other-user", "admin"
	again, _ := ResultFromContext(ctx)
	if again.Principal != "user-id" || again.Metadata["role"] != "user" {
		t.Fatal("consumer mutation changed another consumer's identity")
	}
	if _, ok := ResultFromContext(WithResult(ctx, nil)); ok {
		t.Fatal("nil result did not clear inherited identity")
	}
	if _, ok := ResultFromContext(context.Background()); ok {
		t.Fatal("unauthenticated context contains an identity")
	}
}
