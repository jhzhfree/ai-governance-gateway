package main

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/jhzhfree/ai-governance-gateway/internal/database"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
)

func TestReadinessRequiresVersionAndStructure(t *testing.T) {
	pool := testutil.EmptyDatabase(t)
	handler := readiness(pool)
	check := func(want int) {
		t.Helper()
		w := httptest.NewRecorder()
		handler(w, httptest.NewRequest("GET", "/health/ready", nil))
		if w.Code != want {
			t.Fatalf("readiness=%d want %d", w.Code, want)
		}
	}
	check(503)
	if err := database.Migrate(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	check(204)
	if _, err := pool.Exec(context.Background(), `DROP INDEX budget_recovery_idx`); err != nil {
		t.Fatal(err)
	}
	check(503)
}
