// Package testutil provisions isolated schemas in an explicitly supplied test DB.
package testutil

import (
	"context"
	"crypto/rand"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jhzhfree/ai-governance-gateway/internal/budget"
)

func Database(t testing.TB) (*pgxpool.Pool, *budget.Store, budget.Scope) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL missing; run scripts/verify.sh for required PostgreSQL integration checks")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid TEST_DATABASE_URL")
	}
	admin, err := pgxpool.NewWithConfig(ctx, config.Copy())
	if err != nil {
		t.Fatal("test database connection failed")
	}
	schema := "poc_" + rand.Text()
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		admin.Close()
		t.Fatal("cannot create isolated test schema: ", err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = quoted
	config.MaxConns = 12
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		admin.Close()
		t.Fatal("test schema pool failed")
	}
	t.Cleanup(func() {
		pool.Close()
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.Exec(cleanup, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error("cleanup schema: ", err)
		}
		admin.Close()
	})
	store := budget.New(pool)
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	return pool, store, budget.Scope{Organization: "org-test", Project: "project-test", Period: "2026-11", Currency: "MOCK"}
}

func Accounts(t testing.TB, store *budget.Store, scope budget.Scope, orgLimit, projectLimit int64) {
	t.Helper()
	org := scope
	org.Project = ""
	if err := store.CreateAccount(context.Background(), org, orgLimit); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateAccount(context.Background(), scope, projectLimit); err != nil {
		t.Fatal(err)
	}
}
