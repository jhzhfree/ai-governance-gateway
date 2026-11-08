package testutil

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jhzhfree/ai-governance-gateway/internal/budget"
	"github.com/jhzhfree/ai-governance-gateway/internal/completion"
	"github.com/jhzhfree/ai-governance-gateway/internal/execution"
	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
)

// SeedEmbeddedCompletion creates real durable accounting states, without an
// upstream request. It deliberately leaves client attempts unfinished to model
// a killed request process independently of supplier evidence and recovery.
func SeedEmbeddedCompletion(t *testing.T, pool *pgxpool.Pool, scope budget.Scope, id, state string) {
	t.Helper()
	ctx := context.Background()
	cfg := MainlandConfig()
	for i := range cfg.Models {
		cfg.Models[i].MaxOutputTokens = 32
	}
	catalog, err := provider.New(cfg, func(string) string { return "synthetic-metadata-fixture" }, nil)
	if err != nil {
		t.Fatal(err)
	}
	route, _ := catalog.Find("aliyun/fixture")
	req := provider.Request{Model: "aliyun/fixture", MaxTokens: 32, Messages: []provider.Message{{Role: "user", Content: "synthetic metadata fixture"}}}
	amount, err := route.Bound(32)
	if err != nil {
		t.Fatal(err)
	}
	ledger := budget.New(pool)
	if _, err := ledger.Reserve(ctx, budget.Quote{Scope: scope, RequestID: id, PriceVersion: route.PriceVersion(), Amount: amount}); err != nil {
		t.Fatal(err)
	}
	attempt := "attempt-" + id
	if err := execution.New(pool).Begin(ctx, execution.Start{AttemptID: attempt, RequestID: id, Model: req.Model, Provider: route.Provider(), Region: route.Region()}); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Dispatch(ctx, id); err != nil {
		t.Fatal(err)
	}
	journal := completion.New(pool)
	if err := journal.Bind(ctx, route, req, id, attempt); err != nil {
		t.Fatal(err)
	}
	if state != "awaiting_usage" {
		if err := journal.Record(ctx, id, attempt, completion.RouteDigest(route), []byte(`{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}`)); err != nil {
			t.Fatal(err)
		}
	}
	switch state {
	case "receipted":
		err = journal.Settle(ctx, id, attempt, 14)
	case "recorded", "awaiting_usage":
		err = ledger.MarkUncertain(ctx, id)
	default:
		t.Fatal("invalid fixture completion state")
	}
	if err != nil {
		t.Fatal(err)
	}
}
