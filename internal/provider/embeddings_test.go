package provider_test

import (
	"encoding/json"
	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
	"strings"
	"testing"
)

func TestEmbeddingContractAndInputOnlyPrice(t *testing.T) {
	for _, raw := range []string{`"synthetic"`, `["one","two"]`} {
		if _, err := (provider.EmbeddingRequest{Model: "m", Input: json.RawMessage(raw)}).Request(); err != nil {
			t.Fatal(err)
		}
	}
	for _, raw := range []string{`null`, `""`, `[]`, `[1,2]`, `["one",2]`, `["one",null]`, `"` + strings.Repeat("x", 16385) + `"`} {
		if _, err := (provider.EmbeddingRequest{Model: "m", Input: json.RawMessage(raw)}).Request(); err == nil {
			t.Fatal("invalid input accepted")
		}
	}
	cfg := testutil.MainlandConfig()
	for i := range cfg.Models {
		cfg.Models[i].API = "embeddings"
		cfg.Models[i].NewAPIChannelID = 1
		cfg.Models[i].MaxOutputTokens = 0
		cfg.Models[i].OutputPerMillion = 0
	}
	c, err := provider.NewGoverned(cfg)
	if err != nil {
		t.Fatal(err)
	}
	route, _ := c.Find(cfg.Models[0].ID)
	if _, err := route.Bound(0); err != nil {
		t.Fatal(err)
	}
	if _, err := route.Bound(1); err == nil {
		t.Fatal("embedding reserved generated tokens")
	}
	if _, err := route.Cost(2, 1); err == nil {
		t.Fatal("embedding accepted generated token charge")
	}
	cfg.Models[0].OutputPerMillion = 1
	if _, err := provider.NewGoverned(cfg); err == nil {
		t.Fatal("mixed input-only tariff accepted")
	}
}
func TestEmbeddingResponseShapeAndEvidence(t *testing.T) {
	req, _ := (provider.EmbeddingRequest{Model: "alias", Input: json.RawMessage(`["one","two"]`)}).Request()
	const raw = `{"object":"list","model":"upstream","data":[{"object":"embedding","index":1,"embedding":[0.3,0.4]},{"object":"embedding","index":0,"embedding":[0.1,0.2]}],"usage":{"prompt_tokens":2,"total_tokens":2}}`
	value, usage, err := provider.NormalizeEmbedding([]byte(raw), req)
	if err != nil || value.Model != "alias" || value.Data[0].Index != 0 || usage == nil || *usage.CompletionTokens != 0 {
		t.Fatal("valid embedding normalization failed")
	}
	for _, invalid := range []string{
		strings.Replace(raw, `"total_tokens":2`, `"total_tokens":3`, 1), strings.Replace(raw, `"prompt_tokens":2`, `"prompt_tokens":null`, 1),
		strings.Replace(raw, `"index":1`, `"index":0`, 1), strings.Replace(raw, `"index":1,`, ``, 1), strings.Replace(raw, `[0.3,0.4]`, `[0.3]`, 1),
		strings.Replace(raw, `[0.3,0.4]`, `"base64"`, 1), strings.Replace(raw, `"prompt_tokens":2`, `"completion_tokens":1,"prompt_tokens":2`, 1),
	} {
		if _, _, err := provider.NormalizeEmbedding([]byte(invalid), req); err == nil {
			t.Fatal("invalid vector or usage accepted")
		}
	}
	dims := 3
	req.Dimensions = &dims
	if _, _, err := provider.NormalizeEmbedding([]byte(raw), req); err == nil {
		t.Fatal("wrong dimensions accepted")
	}
}
