package store

import (
	"context"
	"errors"
	"sync"
	"testing"
)

func TestPostgresPermissionsAtomicReplaceRollbackAndCascade(t *testing.T) {
	ctx := context.Background()
	db, errOpen := Open(ctx, Config{DSN: integrationDSN(t)})
	if errOpen != nil {
		t.Fatal(errOpen)
	}
	t.Cleanup(func() {
		if errClose := db.Close(); errClose != nil {
			t.Error(errClose)
		}
	})
	if _, errCreate := db.CreateUser(ctx, User{ID: "policy-user", Email: "policy@example.com", Role: "user", Status: "active"}); errCreate != nil {
		t.Fatal(errCreate)
	}
	if rules, errList := db.ListPermissions(ctx, "policy-user"); errList != nil || len(rules) != 0 {
		t.Fatal("new account did not have empty permission set")
	}
	if _, errMissing := db.ListPermissions(ctx, "missing"); !errors.Is(errMissing, ErrNotFound) {
		t.Fatal("missing account confused with default-open account")
	}
	setA := []Permission{{Scope: "model", Value: "alpha-*", Effect: "allow"}, {Scope: "provider", Value: "alpha", Effect: "allow"}}
	setB := []Permission{{Scope: "model", Value: "beta-*", Effect: "allow"}, {Scope: "provider", Value: "beta", Effect: "allow"}}
	if errReplace := db.ReplacePermissions(ctx, "policy-user", setA); errReplace != nil {
		t.Fatal(errReplace)
	}
	duplicate := []Permission{{Scope: "model", Value: "invalid", Effect: "allow"}, {Scope: "model", Value: "invalid", Effect: "deny"}}
	if errReplace := db.ReplacePermissions(ctx, "policy-user", duplicate); !errors.Is(errReplace, ErrConflict) {
		t.Fatal("duplicate replacement should fail transactionally")
	}
	if rules, errList := db.ListPermissions(ctx, "policy-user"); errList != nil || len(rules) != 2 || rules[0] != setA[0] || rules[1] != setA[1] {
		t.Fatal("failed insert deleted prior permission set")
	}
	var writers sync.WaitGroup
	for _, set := range [][]Permission{setA, setB} {
		writers.Go(func() {
			for range 20 {
				if errReplace := db.ReplacePermissions(ctx, "policy-user", set); errReplace != nil {
					t.Error(errReplace)
					return
				}
			}
		})
	}
	for range 80 {
		rules, errList := db.ListPermissions(ctx, "policy-user")
		if errList != nil {
			t.Fatal(errList)
		}
		if len(rules) != 2 || !((rules[0] == setA[0] && rules[1] == setA[1]) || (rules[0] == setB[0] && rules[1] == setB[1])) {
			t.Fatal("reader observed a partial or mixed permission replacement")
		}
	}
	writers.Wait()
	if errDelete := db.DeleteUser(ctx, "policy-user"); errDelete != nil {
		t.Fatal(errDelete)
	}
	var count int
	if errCount := db.DB().QueryRow(`SELECT count(*) FROM cpa_user_permissions`).Scan(&count); errCount != nil || count != 0 {
		t.Fatal("permissions were not deleted with account")
	}
	if errReplace := db.ReplacePermissions(ctx, "missing", nil); !errors.Is(errReplace, ErrNotFound) {
		t.Fatal("replace on missing user should not silently succeed")
	}
}
