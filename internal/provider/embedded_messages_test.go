package provider_test

import (
	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
	"testing"
)

func TestEmbeddedMessagesOperationSnapshot(t *testing.T) {
	cfg := testutil.MainlandConfig()
	cfg.Models = cfg.Models[3:]
	cfg.Models[0].MaxOutputTokens = 32
	cfg.Models[0].CachePricing = &provider.CachePricing{ReadPerMillion: 500000}
	getenv := func(string) string { return "synthetic" }
	chat, err := provider.NewEmbedded(cfg, getenv, nil)
	if err != nil {
		t.Fatal(err)
	}
	chatRoute, _ := chat.Find("deepseek/fixture")
	cfg.Models[0].API = "messages"
	native, err := provider.NewEmbedded(cfg, getenv, nil)
	if err != nil {
		t.Fatal(err)
	}
	r, _ := native.Find("deepseek/fixture")
	if r.API() != "messages" || r.ExecutionSettings().API != "messages" || !r.UsesNativeMessages() || r.UsesReadOnlyCache() || r.PriceVersion() == chatRoute.PriceVersion() {
		t.Fatal("native operation lost from execution snapshot or identity")
	}
	cfg.Models[0].AllowMessagesStream = true
	streaming, err := provider.NewEmbedded(cfg, getenv, nil)
	if err != nil {
		t.Fatal(err)
	}
	live, _ := streaming.Find("deepseek/fixture")
	if !live.AllowsMessagesStream() || live.PriceVersion() == r.PriceVersion() || r.AllowsMessagesStream() {
		t.Fatal("stream review not immutable")
	}
	cfg.Models[0].API = ""
	if _, err := provider.NewEmbedded(cfg, getenv, nil); err == nil {
		t.Fatal("Chat route borrowed native early-usage review")
	}
	cfg.Models[0].API = "messages"
	cfg.Models[0].AllowMessagesStream = false

	if _, err := provider.New(cfg, getenv, nil); err == nil {
		t.Fatal("legacy transport inherited native operation")
	}
	cfg.Models[0].UpstreamModel = "claude-alias"
	if _, err := provider.NewEmbedded(cfg, getenv, nil); err == nil {
		t.Fatal("supplier model auto-remapping accepted")
	}
	cfg.Models[0].UpstreamModel = "fixture-model"
	cfg.Models[0].CachePricing = nil
	if _, err := provider.NewEmbedded(cfg, getenv, nil); err == nil {
		t.Fatal("unreviewed cache tariff accepted")
	}
}
