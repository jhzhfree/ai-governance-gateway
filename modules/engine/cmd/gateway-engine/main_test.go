package main

import (
	"context"
	"net/http"
	"testing"

	"github.com/jhzhfree/ai-governance-gateway/internal/completion"
	"github.com/jhzhfree/ai-governance-gateway/internal/gatewayapp"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
)

func TestEngineEntrypointFactoryReadinessAndPublication(t *testing.T) {
	db, _, _ := testutil.Database(t)
	ctx := context.Background()
	admin := identity.Principal{Issuer: "synthetic", Subject: "owner"}
	source := testutil.MainlandManager(t, db, admin, http.NotFoundHandler())
	if engine, err := createEngine(ctx, source, gatewayapp.EngineConfig{}); err == nil || engine != nil {
		t.Fatal("invalid limits must return a nil engine")
	}
	if engine, err := createEngine(ctx, source, gatewayapp.EngineConfig{MaxBindings: 8, MaxConcurrent: 2}); err == nil || engine != nil {
		t.Fatal("product factory accepted missing completion journal")
	}
	engine, err := createEngine(ctx, source, gatewayapp.EngineConfig{MaxBindings: 8, MaxConcurrent: 2, Completions: completion.New(db)})
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	if engine.Ready() {
		t.Fatal("unconfigured engine ready")
	}
	cfg := testutil.MainlandConfig()
	for i := range cfg.Models {
		cfg.Models[i].MaxOutputTokens = 32
	}
	if err := source.Publish(ctx, admin, 0, cfg); err != nil {
		t.Fatal(err)
	}
	if !engine.Ready() || len(engine.Models()) != 4 {
		t.Fatal("published providers did not become ready")
	}
	engine.Close()
	if engine.Ready() {
		t.Fatal("closed engine ready")
	}
}
