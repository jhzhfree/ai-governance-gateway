package provider_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
)

func TestEstimatedInputAdmission(t *testing.T) {
	for _, req := range []provider.Request{
		{Model: "aliyun/fixture", MaxTokens: 16, Messages: []provider.Message{{Role: "user", Content: "你好"}, {Role: "assistant", Content: "ok"}}},
		{Model: "aliyun/fixture", API: "embeddings", Input: []string{"你好", "ok"}},
	} {
		estimate, err := provider.EstimatedInputTokens(req)
		if err != nil {
			t.Fatal(err)
		}
		for _, delta := range []int64{-1, 0, 1} {
			cfg := testutil.MainlandConfig()
			cfg.Models = cfg.Models[:1]
			cfg.Models[0].MaxInputTokens = estimate + delta
			cfg.Models[0].MaxOutputTokens = 16
			if req.IsEmbedding() {
				cfg.Models[0].API = "embeddings"
				cfg.Models[0].MaxOutputTokens = 0
				cfg.Models[0].OutputPerMillion = 0
			}
			catalog, err := provider.NewEmbedded(cfg, func(string) string { return "synthetic" }, nil)
			if err != nil {
				t.Fatal(err)
			}
			route, _ := catalog.Find(req.Model)
			err = route.CheckEstimatedInput(req)
			if (delta < 0) != errors.Is(err, provider.ErrEstimatedInputLimit) {
				t.Fatalf("boundary delta=%d err=%v", delta, err)
			}
		}
	}
	req := provider.Request{Model: "aliyun/fixture", MaxTokens: 16, Messages: []provider.Message{{Role: "user", Content: strings.Repeat("你", 100)}}}
	count, err := provider.EstimatedInputTokens(req)
	if err != nil || count < 300 {
		t.Fatal("UTF-8 undercount", count, err)
	}
}
