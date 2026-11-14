package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestPaymentDatabaseEnvironmentIsolation(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "pay.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.EnsureEnvironment(ctx, "demo"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec("INSERT INTO users(id,coins) VALUES('alice',2000)"); err != nil {
		t.Fatal(err)
	}
	if err = s.DB.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	if err = s.EnsureEnvironment(ctx, "demo"); err != nil {
		t.Fatal(err)
	}
	if err = s.EnsureEnvironment(ctx, "live:stripe_test:onramp_sandbox"); !errors.Is(err, ErrEnvironmentMismatch) {
		t.Fatal("demo database accepted for live", err)
	}
	if coins, _, err := s.Account(ctx, "alice"); err != nil || coins != 2000 {
		t.Fatal("environment rejection changed data", coins, err)
	}
}

func TestLiveSandboxAndProductionMustNotShareDatabase(t *testing.T) {
	ctx := context.Background()
	for _, scope := range []string{"live:stripe_live:onramp_sandbox", "live:stripe_test:onramp_production"} {
		t.Run(scope, func(t *testing.T) {
			s, err := Open(":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer s.DB.Close()
			if err = s.EnsureEnvironment(ctx, "live:stripe_test:onramp_sandbox"); err != nil {
				t.Fatal(err)
			}
			if err = s.EnsureEnvironment(ctx, scope); !errors.Is(err, ErrEnvironmentMismatch) {
				t.Fatal(err)
			}
		})
	}
}

func TestUnscopedDatabaseIsNotSilentlyAdopted(t *testing.T) {
	ctx := context.Background()
	for _, seed := range []string{
		"INSERT INTO users(id,coins) VALUES('alice',2000)",
		"INSERT INTO inbox(gateway,id,body_hash,snapshot,created) VALUES('stripe','event','hash','{}',1)",
		"CREATE TABLE onramp_tokens(provider TEXT,token TEXT); INSERT INTO onramp_tokens VALUES('transak','old-environment-token')",
	} {
		t.Run(seed[:20], func(t *testing.T) {
			s, err := Open(":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer s.DB.Close()
			if _, err = s.DB.Exec(seed); err != nil {
				t.Fatal(err)
			}
			if err = s.EnsureEnvironment(ctx, "demo"); !errors.Is(err, ErrUnscopedDatabase) {
				t.Fatal("unverified legacy data accepted", err)
			}
			var exists bool
			if err = s.DB.QueryRow("SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE name='payment_environment')").Scan(&exists); err != nil || exists {
				t.Fatal("failed check persisted metadata", exists, err)
			}
		})
	}
}
