package gatewaybridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
	adapter "github.com/jhzhfree/ai-governance-gateway/modules/engine/adapters/bifrost"
	"github.com/jhzhfree/ai-governance-gateway/modules/engine/execution"
)

func TestSupplierFactoryBindsCredentialSnapshotAndCloses(t *testing.T) {
	for _, profile := range provider.Profiles() {
		t.Run(profile.ID, func(t *testing.T) {
			cfg := testutil.MainlandConfig()
			for i := range cfg.Models {
				cfg.Models[i].MaxOutputTokens = 32
			}
			makeRoute := func(secret string) *provider.Route {
				c, err := provider.New(cfg, func(string) string { return secret }, nil)
				if err != nil {
					t.Fatal(err)
				}
				r, _ := c.Find(profile.ID + "/fixture")
				return r
			}
			oldRoute, newRoute := makeRoute("synthetic-old-key"), makeRoute("synthetic-new-key")
			if oldRoute.PriceVersion() != newRoute.PriceVersion() || oldRoute.ExecutionIdentity() == newRoute.ExecutionIdentity() {
				t.Fatal("credential rotation did not retain prices and change execution identity")
			}
			for _, route := range []*provider.Route{oldRoute, newRoute} {
				s := route.ExecutionSettings()
				j, err := json.Marshal(s)
				if err != nil || strings.Contains(string(j)+fmt.Sprintf("%v %#v", s, s), s.APIKey) {
					t.Fatal("credential snapshot leaked")
				}
			}
			var oldCalls, newCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				endpoint, _ := url.Parse(profile.Endpoint)
				if r.URL.Path != endpoint.Path {
					t.Error("factory lost supplier path")
				}
				switch r.Header.Get("Authorization") {
				case "Bearer synthetic-old-key":
					oldCalls.Add(1)
				case "Bearer synthetic-new-key":
					newCalls.Add(1)
				default:
					t.Error("factory lost credential")
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"id":"fixture","choices":[{"index":0,"message":{"role":"assistant","content":"synthetic"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`)
			}))
			defer server.Close()
			factory := func(ctx context.Context, cfg adapter.Config) (execution.Executor, error) {
				if cfg.Supplier != profile.ID || cfg.BaseURL != "" || cfg.Model != "fixture-model" || cfg.MaxOutputTokens != 32 || cfg.MaxConcurrent != 2 {
					t.Error("factory used client-derived or incorrect settings")
				}
				cfg.BaseURL = server.URL
				cfg.TestLoopback = true
				return adapter.New(ctx, cfg)
			}
			old, err := newSupplier(context.Background(), oldRoute, 2, factory)
			if err != nil {
				t.Fatal(err)
			}
			defer old.Close()
			if strings.Contains(fmt.Sprintf("%v %+v %#v %v %#v", old, old, old, old.engine, old.engine), "synthetic-old-key") {
				t.Fatal("binding formatting leaked credential")
			}
			request := provider.Request{Model: profile.ID + "/fixture", MaxTokens: 32, Messages: []provider.Message{{Role: "user", Content: "synthetic"}}}
			if err := old.Validate(newRoute, request); !errors.Is(err, execution.ErrInvalidRequest) {
				t.Fatal("stale credential accepted a new route snapshot")
			}
			if _, err := old.Open(context.Background(), newRoute, request, "r", "a"); err == nil {
				t.Fatal("stale credential dispatched")
			}
			run := func(b *SupplierBinding, route *provider.Route) {
				resp, err := b.Open(context.Background(), route, request, "r", "a")
				if err != nil {
					t.Fatal(err)
				}
				_, err = io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				if err != nil {
					t.Fatal(err)
				}
			}
			run(old, oldRoute)
			newer, err := newSupplier(context.Background(), newRoute, 2, factory)
			if err != nil {
				t.Fatal(err)
			}
			defer newer.Close()
			old.Close()
			old.Close()
			if !errors.Is(old.Validate(oldRoute, request), execution.ErrClosed) {
				t.Fatal("closed binding still accepts")
			}
			if _, err := old.Open(context.Background(), oldRoute, request, "r", "a"); !errors.Is(err, execution.ErrClosed) {
				t.Fatal("closed binding dispatched")
			}
			run(newer, newRoute)
			if oldCalls.Load() != 1 || newCalls.Load() != 1 {
				t.Fatal("credential snapshot or single-dispatch violated")
			}
		})
	}
}
