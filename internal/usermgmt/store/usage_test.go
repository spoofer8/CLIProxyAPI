package store

import (
	"context"
	"errors"
	"math"
	"sync"
	"testing"
)

func TestPostgresMonthlyUsageConcurrentUpsert(t *testing.T) {
	dsn := integrationDSN(t)
	ctx := context.Background()
	db, errOpen := Open(ctx, Config{DSN: dsn})
	if errOpen != nil {
		t.Fatal(errOpen)
	}
	t.Cleanup(func() {
		if errClose := db.Close(); errClose != nil {
			t.Error(errClose)
		}
	})
	if _, errCreate := db.CreateUser(ctx, User{ID: "usage-user", Email: "usage@example.com", Role: "user", Status: "active"}); errCreate != nil {
		t.Fatal(errCreate)
	}
	var workers sync.WaitGroup
	for range 16 {
		workers.Go(func() {
			for range 20 {
				if errWrite := db.IncrementUsage(ctx, UsageIncrement{UserID: "usage-user", Period: "2026-09", InputTokens: 2, OutputTokens: 3, TotalTokens: 5}); errWrite != nil {
					t.Error(errWrite)
					return
				}
			}
		})
	}
	workers.Wait()
	monthly, errGet := db.GetMonthlyUsage(ctx, "usage-user", "2026-09")
	if errGet != nil || monthly.RequestCount != 320 || monthly.InputTokens != 640 || monthly.OutputTokens != 960 || monthly.TotalTokens != 1600 {
		t.Fatalf("concurrent increments lost updates: %+v (error %v)", monthly, errGet)
	}
	nextMonth, errNext := db.GetMonthlyUsage(ctx, "usage-user", "2026-10")
	if errNext != nil || nextMonth.TotalTokens != 0 || nextMonth.RequestCount != 0 {
		t.Fatal("new calendar month did not start at zero")
	}
}

func TestPostgresUsageRejectsOverflowWithoutPartialWrites(t *testing.T) {
	db, errOpen := Open(context.Background(), Config{DSN: integrationDSN(t)})
	if errOpen != nil {
		t.Fatal(errOpen)
	}
	t.Cleanup(func() {
		if errClose := db.Close(); errClose != nil {
			t.Error(errClose)
		}
	})
	ctx := context.Background()
	if _, errCreate := db.CreateUser(ctx, User{ID: "usage-user", Email: "usage@example.com", Role: "user", Status: "active"}); errCreate != nil {
		t.Fatal(errCreate)
	}
	if errWrite := db.IncrementUsage(ctx, UsageIncrement{UserID: "usage-user", Period: "2026-09", TotalTokens: math.MaxInt64}); errWrite != nil {
		t.Fatal(errWrite)
	}
	for _, invalid := range []UsageIncrement{
		{UserID: "usage-user", Period: "2026-09", InputTokens: 1, TotalTokens: 1},
		{UserID: "usage-user", Period: "2026-09", OutputTokens: -1},
		{UserID: "usage-user", Period: "2026-09", InputTokens: math.MaxInt64, OutputTokens: 1},
		{UserID: "usage-user", Period: "invalid"},
	} {
		if errWrite := db.IncrementUsage(ctx, invalid); !errors.Is(errWrite, ErrInvalidUsage) {
			t.Fatalf("expected safe overflow/invalid rejection, got %v", errWrite)
		}
	}
	monthly, errGet := db.GetMonthlyUsage(ctx, "usage-user", "2026-09")
	if errGet != nil || monthly.TotalTokens != math.MaxInt64 || monthly.InputTokens != 0 || monthly.RequestCount != 1 {
		t.Fatal("overflow caused a partial counter update")
	}
}
