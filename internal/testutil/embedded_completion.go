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
	seedEmbeddedCompletion(t, pool, scope, id, state, "chat")
}
func SeedEmbeddedEmbeddingCompletion(t testing.TB, pool *pgxpool.Pool, scope budget.Scope, id, state string) {
	t.Helper()
	seedEmbeddedCompletion(t, pool, scope, id, state, "embeddings")
}
func seedEmbeddedCompletion(t testing.TB, pool *pgxpool.Pool, scope budget.Scope, id, state string, protocol string) {
	t.Helper()
	ctx := context.Background()
	nativeStream := protocol == "messages_stream"
	if nativeStream {
		protocol = "messages"
	}
	embedding := protocol == "embeddings"
	cached := protocol == "cache" || protocol == "messages"
	cfg := MainlandConfig()
	for i := range cfg.Models {
		cfg.Models[i].MaxOutputTokens = 32
	}
	newCatalog := provider.New
	if embedding {
		cfg.Models = cfg.Models[:1]
		cfg.Models[0].API = "embeddings"
		cfg.Models[0].MaxOutputTokens = 0
		cfg.Models[0].OutputPerMillion = 0
		newCatalog = provider.NewEmbedded
	}
	alias := "aliyun/fixture"
	if cached {
		cfg.Models = cfg.Models[:1]
		cfg.Models[0].ID, cfg.Models[0].Provider = "deepseek/fixture", "deepseek"
		cfg.Models[0].CachePricing = &provider.CachePricing{ReadPerMillion: 500000}
		alias = "deepseek/fixture"
		newCatalog = provider.NewEmbedded
	}
	if protocol == "messages" {
		cfg.Models[0].API = "messages"
		cfg.Models[0].AllowMessagesStream = nativeStream
	}
	catalog, err := newCatalog(cfg, func(string) string { return "synthetic-metadata-fixture" }, nil)
	if err != nil {
		t.Fatal(err)
	}
	route, _ := catalog.Find(alias)
	req := provider.Request{Model: alias, MaxTokens: 32, Stream: nativeStream, MessagesStream: nativeStream, Messages: []provider.Message{{Role: "user", Content: "synthetic metadata fixture"}}}
	if embedding {
		req = provider.Request{API: "embeddings", Model: alias, Input: []string{"synthetic metadata fixture"}}
	}
	amount, err := route.Bound(req.MaxTokens)
	if err != nil {
		t.Fatal(err)
	}
	ledger := budget.New(pool)
	if _, err := ledger.Reserve(ctx, budget.Quote{Scope: scope, RequestID: id, PriceVersion: route.PriceVersion(), Amount: amount}); err != nil {
		t.Fatal(err)
	}
	attempt := "attempt-" + id
	if err := execution.New(pool).Begin(ctx, execution.Start{AttemptID: attempt, RequestID: id, Model: req.Model, Provider: route.Provider(), Region: route.Region(), Stream: req.Stream}); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Dispatch(ctx, id); err != nil {
		t.Fatal(err)
	}
	journal := completion.New(pool)
	if err := journal.Bind(ctx, route, req, id, attempt); err != nil {
		t.Fatal(err)
	}
	usage := `{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}`
	actual := int64(14)
	if embedding {
		usage = `{ "prompt_tokens":3, "total_tokens":3 }`
		actual = 6
	}
	if cached {
		usage = `{ "prompt_tokens":3, "completion_tokens":2, "total_tokens":5, "prompt_cache_hit_tokens":2, "prompt_cache_miss_tokens":1 }`
		actual = 11
	}
	if protocol == "messages" {
		usage = `{ "input_tokens":1, "output_tokens":2, "cache_read_input_tokens":2, "cache_creation_input_tokens":0 }`
	}
	if state != "awaiting_usage" {
		if err := journal.Record(ctx, id, attempt, completion.RouteDigest(route), []byte(usage)); err != nil {
			t.Fatal(err)
		}
	}
	switch state {
	case "receipted":
		err = journal.Settle(ctx, id, attempt, actual)
	case "recorded", "awaiting_usage":
		err = ledger.MarkUncertain(ctx, id)
	default:
		t.Fatal("invalid fixture completion state")
	}
	if err != nil {
		t.Fatal(err)
	}
}

func SeedEmbeddedCacheCompletion(t testing.TB, pool *pgxpool.Pool, scope budget.Scope, id, state string) {
	t.Helper()
	seedEmbeddedCompletion(t, pool, scope, id, state, "cache")
}

func SeedEmbeddedMessagesCompletion(t testing.TB, pool *pgxpool.Pool, scope budget.Scope, id, state string) {
	t.Helper()
	seedEmbeddedCompletion(t, pool, scope, id, state, "messages")
}

func SeedEmbeddedMessagesStreamCompletion(t testing.TB, pool *pgxpool.Pool, scope budget.Scope, id, state string) {
	t.Helper()
	seedEmbeddedCompletion(t, pool, scope, id, state, "messages_stream")
}
