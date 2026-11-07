package provider_test

import (
	"encoding/json"
	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
	"strings"
	"testing"
)

func TestCacheTariffBoundsRoundingAndImmutableReview(t *testing.T) {
	cfg := testutil.MainlandConfig()
	for i := range cfg.Models {
		cfg.Models[i].NewAPIChannelID = 1
		cfg.Models[i].NewAPIChannelRevision = strings.Repeat("a", 64)
		cfg.Models[i].CachePricing = &provider.CachePricing{ReadPerMillion: 500000, WritePerMillion: 3000000}
	}
	catalog, err := provider.NewGoverned(cfg)
	if err != nil {
		t.Fatal(err)
	}
	route, _ := catalog.Find("zhipu/fixture")
	bound, err := route.Bound(8)
	if err != nil || bound != 416 {
		t.Fatal("cache write worst-case not reserved")
	}
	in, out, total, read, write := int64(11), int64(3), int64(14), int64(4), int64(5)
	usage := &provider.Usage{PromptTokens: &in, CompletionTokens: &out, TotalTokens: &total, PromptTokensDetails: &provider.InputTokenDetails{CachedTokens: &read, CachedCreationTokens: &write}}
	cost, err := route.ValidateUsage(usage, 8)
	if err != nil || cost != 33 {
		t.Fatal("disjoint cache tariff incorrect")
	}
	snapshot := route.PriceSnapshot()
	snapshot.CachePricing.ReadPerMillion = 0
	cfg.Models[2].CachePricing.ReadPerMillion = 0
	cost, err = route.ValidateUsage(usage, 8)
	if err != nil || cost != 33 {
		t.Fatal("immutable tariff changed")
	}
	changed, err := provider.NewGoverned(cfg)
	if err != nil {
		t.Fatal(err)
	}
	other, _ := changed.Find("zhipu/fixture")
	if other.PriceVersion() == route.PriceVersion() {
		t.Fatal("cache tariff omitted from price version")
	}
	usage.PromptTokensDetails = nil
	if _, err := route.ValidateUsage(usage, 8); err == nil {
		t.Fatal("unknown cache usage treated as zero")
	}
	cost, err = provider.TokenCost(2, 1, 1, 0, 1, 1, &provider.CachePricing{ReadPerMillion: 1, WritePerMillion: 1})
	if err != nil || cost != 1 {
		t.Fatal("rounding applied per category instead of once")
	}
	for _, counts := range [][4]int64{{1, 0, 2, 0}, {1, 0, 1, 1}, {-1, 0, 0, 0}, {1, 0, -1, 0}, {1 << 40, 0, 0, 0}} {
		if _, err := provider.TokenCost(counts[0], counts[1], counts[2], counts[3], 1, 1, &provider.CachePricing{}); err == nil {
			t.Fatal("invalid usage accepted")
		}
	}
	if _, err := provider.TokenCost(2000000, 1000000, 0, 0, provider.MaxAmount, provider.MaxAmount, nil); err == nil {
		t.Fatal("unsafe amount accepted")
	}
}

func TestCacheTariffJSONRequiresExplicitPrices(t *testing.T) {
	for _, raw := range []string{`{}`, `{"read_per_million":0}`, `{"read_per_million":null,"write_per_million":0}`, `{"read_per_million":0,"write_per_million":0,"extra":1}`} {
		var price provider.CachePricing
		if json.Unmarshal([]byte(raw), &price) == nil {
			t.Fatal("incomplete tariff became a free cache tier")
		}
	}
	var price provider.CachePricing
	if err := json.Unmarshal([]byte(`{"read_per_million":0,"write_per_million":0}`), &price); err != nil {
		t.Fatal(err)
	}
}

func TestCacheTTLPricesEvidenceBoundsAndSnapshots(t *testing.T) {
	cfg := testutil.MainlandConfig()
	for i := range cfg.Models {
		cfg.Models[i].NewAPIChannelID = 1
		cfg.Models[i].NewAPIChannelRevision = strings.Repeat("a", 64)
		cfg.Models[i].CachePricing = &provider.CachePricing{ReadPerMillion: 500000, WritePerMillion: 9000000, WriteTTL: &provider.CacheWriteTTL{FiveMinutePerMillion: 3000000, OneHourPerMillion: 6000000}}
	}
	catalog, err := provider.NewGoverned(cfg)
	if err != nil {
		t.Fatal(err)
	}
	route, _ := catalog.Find("zhipu/fixture")
	if bound, err := route.Bound(8); err != nil || bound != 800 || route.TariffVersion() != 2 {
		t.Fatal("duration bound or negotiation wrong")
	}
	ttl := &provider.CacheWriteUsage{FiveMinuteTokens: new(int64(3)), OneHourTokens: new(int64(2))}
	usage := &provider.Usage{PromptTokens: new(int64(11)), CompletionTokens: new(int64(3)), TotalTokens: new(int64(14)), PromptTokensDetails: &provider.InputTokenDetails{CachedTokens: new(int64(4)), CachedCreationTokens: new(int64(5)), CachedCreationTokensDetails: ttl}}
	if cost, err := route.ValidateUsage(usage, 8); err != nil || cost != 39 {
		t.Fatal("duration cost wrong")
	}
	snapshot := route.PriceSnapshot()
	snapshot.CachePricing.WriteTTL.OneHourPerMillion = 0
	cfg.Models[2].CachePricing.WriteTTL.OneHourPerMillion = 0
	if cost, err := route.ValidateUsage(usage, 8); err != nil || cost != 39 {
		t.Fatal("nested immutable price mutated")
	}
	changed, err := provider.NewGoverned(cfg)
	if err != nil {
		t.Fatal(err)
	}
	other, _ := changed.Find("zhipu/fixture")
	if other.PriceVersion() == route.PriceVersion() {
		t.Fatal("duration rate absent from fingerprint")
	}
	for _, invalid := range []*provider.CacheWriteUsage{nil, {}, {FiveMinuteTokens: new(int64(3))}, {FiveMinuteTokens: new(int64(-1)), OneHourTokens: new(int64(6))}, {FiveMinuteTokens: new(int64(4)), OneHourTokens: new(int64(2))}, {FiveMinuteTokens: new(int64(1 << 62)), OneHourTokens: new(int64(1 << 62))}} {
		usage.PromptTokensDetails.CachedCreationTokensDetails = invalid
		if _, err := route.ValidateUsage(usage, 8); err == nil {
			t.Fatal("missing or contradictory durations billed")
		}
	}
	zeroPrice := &provider.CachePricing{WriteTTL: &provider.CacheWriteTTL{FiveMinutePerMillion: 1, OneHourPerMillion: 1}, ReadPerMillion: 1}
	if cost, err := provider.TokenCostDetailed(4, 1, 1, 2, 1, 1, zeroPrice, &provider.CacheWriteUsage{FiveMinuteTokens: new(int64(1)), OneHourTokens: new(int64(1))}); err != nil || cost != 1 {
		t.Fatal("duration rounding not applied once")
	}
	if _, err := provider.TokenCost(4, 1, 1, 2, 1, 1, zeroPrice); err == nil {
		t.Fatal("legacy arithmetic bypassed evidence")
	}
	for _, raw := range []string{`{}`, `{"five_minute_per_million":0}`, `{"five_minute_per_million":0,"one_hour_per_million":null}`, `{"five_minute_per_million":-1,"one_hour_per_million":0}`, `{"five_minute_per_million":0,"one_hour_per_million":9007199254740992}`, `{"five_minute_per_million":0,"one_hour_per_million":0,"extra":1}`} {
		var p provider.CachePricing
		if json.Unmarshal([]byte(`{"read_per_million":0,"write_per_million":0,"write_ttl":`+raw+`}`), &p) == nil {
			t.Fatal("implicit or invalid duration rate accepted")
		}
	}
}
