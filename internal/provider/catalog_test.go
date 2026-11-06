package provider_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
)

func TestMainlandProfilesAndProtocol(t *testing.T) {
	catalog, _ := testutil.MainlandCatalog(t, testutil.SupplierFixture(t))
	if len(catalog.Models()) != 4 || !catalog.Ready() {
		t.Fatal("four mainland routes not available")
	}
	for _, profile := range provider.Profiles() {
		t.Run(profile.ID, func(t *testing.T) {
			route, ok := catalog.Find(profile.ID + "/fixture")
			if !ok {
				t.Fatal("missing supplier")
			}
			for _, stream := range []bool{false, true} {
				response, err := route.Open(context.Background(), provider.Request{Model: profile.ID + "/fixture", Stream: stream, MaxTokens: 8, Messages: []provider.Message{{Role: "user", Content: "synthetic"}}})
				if err != nil {
					t.Fatal(err)
				}
				body, err := io.ReadAll(response.Body)
				response.Body.Close()
				if err != nil || response.StatusCode != 200 {
					t.Fatal("supplier fixture protocol failed")
				}
				if stream && !strings.Contains(string(body), "[DONE]") {
					t.Fatal("missing stream termination")
				}
			}
			bound, err := route.Bound(8)
			if err != nil || bound != 288 {
				t.Fatal("full input upper bound not reserved")
			}
			actual, err := route.Cost(3, 2)
			if err != nil || actual != 14 {
				t.Fatal("micro-yuan usage cost incorrect")
			}
			if strings.Contains(fmt.Sprintf("%#v %v %#v %+v", route, catalog, *route, *catalog), "synthetic-AIGW") {
				t.Fatal("credential formatting leaked")
			}
		})
	}
}
func TestConfigurationFailsClosedForResidencyAndRouting(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*provider.Config)
	}{
		{"deployment_outside_mainland", func(c *provider.Config) { c.DeploymentResidency.Region = "cn-hongkong" }},
		{"missing_evidence", func(c *provider.Config) { c.Models[0].Residency.EvidenceRef = "" }},
		{"supplier_outside_mainland", func(c *provider.Config) { c.Models[0].Residency.Region = "ap-southeast-1" }},
		{"expired_review", func(c *provider.Config) { c.Models[0].Residency.ExpiresAt = time.Now().Add(-time.Second) }},
		{"unknown_provider", func(c *provider.Config) { c.Models[0].Provider = "unreviewed" }},
		{"duplicate", func(c *provider.Config) { c.Models = append(c.Models, c.Models[0]) }},
		{"unsafe_model", func(c *provider.Config) { c.Models[0].UpstreamModel = "https://elsewhere.example" }},
		{"zero_price", func(c *provider.Config) { c.Models[0].InputPerMillion = 0 }},
		{"overflow", func(c *provider.Config) { c.Models[0].InputPerMillion = 9223372036854775807 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testutil.MainlandConfig()
			tc.edit(&cfg)
			if _, err := provider.New(cfg, func(string) string { return "synthetic" }, nil); err == nil {
				t.Fatal("unsafe provider configuration accepted")
			}
		})
	}
	cfg := testutil.MainlandConfig()
	if _, err := provider.New(cfg, func(string) string { return "" }, nil); err == nil {
		t.Fatal("missing credential accepted")
	}
	for _, data := range []string{`{"version":1,"endpoint":"https://international.example"}`, `{"version":1} {}`, strings.Repeat(" ", 256<<10) + `{}`} {
		if _, err := provider.DecodeConfig([]byte(data)); err == nil {
			t.Fatal("unknown endpoint/trailing/oversized config accepted")
		}
	}
}
func TestReviewExpiryDisablesRouteAndCatalog(t *testing.T) {
	cfg := testutil.MainlandConfig()
	cfg.Models[0].Residency.ExpiresAt = time.Now().Add(100 * time.Millisecond)
	catalog, err := provider.New(cfg, func(string) string { return "synthetic" }, nil)
	if err != nil {
		t.Fatal(err)
	}
	route, _ := catalog.Find(cfg.Models[0].ID)
	timer := time.NewTimer(150 * time.Millisecond)
	defer timer.Stop()
	<-timer.C
	if route.Ready() || catalog.Ready() || len(catalog.Models()) != 3 {
		t.Fatal("expired review still enabled")
	}
	if _, err := route.Open(context.Background(), provider.Request{}); err == nil {
		t.Fatal("expired route attempted network")
	}
}
func TestPriceFingerprintChangesAndRounding(t *testing.T) {
	cfg := testutil.MainlandConfig()
	cfg.Models[0].InputPerMillion = 1
	cfg.Models[0].OutputPerMillion = 1
	c, err := provider.New(cfg, func(string) string { return "synthetic" }, nil)
	if err != nil {
		t.Fatal(err)
	}
	route, _ := c.Find(cfg.Models[0].ID)
	cost, err := route.Cost(3, 2)
	if err != nil || cost != 1 {
		t.Fatal("fractional micro-yuan must round up once")
	}
	fingerprint := route.PriceVersion()
	cfg.Models[0].InputPerMillion++
	next, err := provider.New(cfg, func(string) string { return "synthetic" }, nil)
	if err != nil {
		t.Fatal(err)
	}
	r, _ := next.Find(cfg.Models[0].ID)
	if r.PriceVersion() == fingerprint {
		t.Fatal("price changed without changing snapshot fingerprint")
	}
	encoded, _ := json.Marshal(c.Models())
	if strings.Contains(string(encoded), "evidence_ref") || strings.Contains(string(encoded), "synthetic") {
		t.Fatal("internal residency references exposed")
	}
}
func TestRedirectDoesNotForwardSupplierCredential(t *testing.T) {
	calls := 0
	catalog, _ := testutil.MainlandCatalog(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Redirect(w, r, "https://unreviewed.example/collect", http.StatusTemporaryRedirect)
	}))
	route, _ := catalog.Find("aliyun/fixture")
	response, err := route.Open(context.Background(), provider.Request{MaxTokens: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 307 || calls != 1 {
		t.Fatal("provider followed redirect")
	}
}

func TestNormalizedStreamUsesOneGatewayIdentity(t *testing.T) {
	first, err := provider.Normalize([]byte(`{"id":"supplier-id","model":"physical-model","choices":[{"index":0,"delta":{"content":"synthetic"},"finish_reason":null}]}`), "aliyun/public", "request-fixture", true)
	if err != nil {
		t.Fatal(err)
	}
	last, err := provider.Normalize([]byte(`{"choices":[],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`), "aliyun/public", "request-fixture", true)
	if err != nil || first.ID != "chatcmpl-request-fixture" || last.ID != first.ID || first.Model != "aliyun/public" || last.Model != first.Model {
		t.Fatal("usage-only chunk changed completion identity or exposed physical routing")
	}
}
