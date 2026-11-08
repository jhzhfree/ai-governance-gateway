package provider_test

import (
	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
	"testing"
)

func TestEmbeddedReadTariffIsolationSnapshotAndRounding(t *testing.T) {
	cfg := testutil.MainlandConfig()
	cfg.Models = cfg.Models[3:]
	cfg.Models[0].MaxOutputTokens = 32
	cfg.Models[0].CachePricing = &provider.CachePricing{ReadPerMillion: 500001}
	getenv := func(string) string { return "synthetic-cache-key" }
	c, err := provider.NewEmbedded(cfg, getenv, nil)
	if err != nil {
		t.Fatal(err)
	}
	r, _ := c.Find("deepseek/fixture")
	in, out, total, hit := int64(3), int64(2), int64(5), int64(2)
	u := &provider.Usage{PromptTokens: &in, CompletionTokens: &out, TotalTokens: &total, PromptTokensDetails: &provider.InputTokenDetails{CachedTokens: &hit}}
	if got, err := r.ValidateUsage(u, 32); err != nil || got != 12 {
		t.Fatal("expected one final ceil(11.000002)", got, err)
	}
	cfg.Models[0].CachePricing.ReadPerMillion = 0
	if got, err := r.ValidateUsage(u, 32); err != nil || got != 12 {
		t.Fatal("caller mutated snapshot", err)
	}
	changed, err := provider.NewEmbedded(cfg, getenv, nil)
	if err != nil {
		t.Fatal(err)
	}
	other, _ := changed.Find("deepseek/fixture")
	if other.PriceVersion() == r.PriceVersion() {
		t.Fatal("cache rate omitted from identity")
	}
	zero := int64(0)
	u.PromptTokensDetails.CachedCreationTokens = &zero
	if _, err := r.ValidateUsage(u, 32); err == nil {
		t.Fatal("synthetic write category accepted")
	}
	u.PromptTokensDetails = nil
	if _, err := r.ValidateUsage(u, 32); err == nil {
		t.Fatal("missing cache counts became free")
	}
	if _, err := provider.New(cfg, getenv, nil); err == nil {
		t.Fatal("legacy native contract silently broadened")
	}
	cfg.Models[0].CachePricing.WritePerMillion = 1
	if _, err := provider.NewEmbedded(cfg, getenv, nil); err == nil {
		t.Fatal("unreviewed write tariff accepted")
	}
	cfg.Models[0].CachePricing.WritePerMillion = 0
	cfg.Models[0].ID = "aliyun/fixture"
	cfg.Models[0].Provider = "aliyun"
	if _, err := provider.NewEmbedded(cfg, getenv, nil); err == nil {
		t.Fatal("other supplier borrowed DeepSeek evidence")
	}
}
