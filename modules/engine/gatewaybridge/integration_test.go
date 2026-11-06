package gatewaybridge_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jhzhfree/ai-governance-gateway/internal/admission"
	"github.com/jhzhfree/ai-governance-gateway/internal/circuit"
	"github.com/jhzhfree/ai-governance-gateway/internal/contentaudit"
	"github.com/jhzhfree/ai-governance-gateway/internal/control"
	recorder "github.com/jhzhfree/ai-governance-gateway/internal/execution"
	"github.com/jhzhfree/ai-governance-gateway/internal/gateway"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
	adapter "github.com/jhzhfree/ai-governance-gateway/modules/engine/adapters/bifrost"
	"github.com/jhzhfree/ai-governance-gateway/modules/engine/gatewaybridge"
)

func TestGovernedBifrostExecution(t *testing.T) {
	for _, mode := range []string{"json", "stream", "missing usage", "duplicate usage", "upstream failure", "revoked key", "budget denial", "device denial", "audit unavailable", "small limit", "unauthorized model", "model permission removed", "malformed stream", "oversized stream", "usage then disconnect"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			pool, ledger, _ := testutil.Database(t)
			cfg := testutil.MainlandConfig()
			for i := range cfg.Models {
				cfg.Models[i].MaxOutputTokens = 32
			}
			catalog, err := provider.New(cfg, func(string) string { return "synthetic-unused-native-key" }, &http.Client{Transport: forbiddenTransport{t}})
			if err != nil {
				t.Fatal(err)
			}
			route, _ := catalog.Find("aliyun/fixture")
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer synthetic-engine-key" {
					t.Error("unexpected endpoint or credential")
				}
				var wire map[string]json.RawMessage
				if json.NewDecoder(r.Body).Decode(&wire) != nil || string(wire["model"]) != `"fixture-model"` {
					t.Error("model mapping failed")
				}
				if mode == "upstream failure" {
					w.WriteHeader(503)
					_, _ = io.WriteString(w, "synthetic-secret-error")
					return
				}
				usage := `,"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}`
				if mode == "missing usage" {
					usage = ""
				}
				if mode == "stream" || mode == "missing usage" || mode == "duplicate usage" || mode == "malformed stream" || mode == "oversized stream" || mode == "usage then disconnect" {
					w.Header().Set("Content-Type", "text/event-stream")
					if mode == "malformed stream" {
						_, _ = fmt.Fprint(w, "data: {bad JSON}\n\n")
					}
					if mode == "oversized stream" {
						_, _ = fmt.Fprintf(w, "data: {\"choices\":[],\"hidden\":\"%s\"}\n\n", strings.Repeat("x", (1<<20)+1))
					}
					_, _ = fmt.Fprint(w, "data: {\"id\":\"fixture\",\"object\":\"chat.completion.chunk\",\"model\":\"fixture-model\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"synthetic reply\"},\"finish_reason\":null}]}\n\n")
					_, _ = fmt.Fprint(w, "data: {\"id\":\"fixture\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
					if usage != "" {
						_, _ = fmt.Fprintf(w, "data: {\"id\":\"fixture\",\"choices\":[]%s}\n\n", usage)
					}
					if mode == "duplicate usage" {
						_, _ = fmt.Fprintf(w, "data: {\"id\":\"fixture\",\"choices\":[]%s}\n\n", usage)
					}
					if mode != "usage then disconnect" {
						_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
					}
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(w, `{"id":"fixture","object":"chat.completion","model":"fixture-model","choices":[{"index":0,"message":{"role":"assistant","content":"synthetic reply"},"finish_reason":"stop"}]%s}`, usage)
			}))
			defer upstream.Close()
			engine, err := adapter.New(ctx, adapter.Config{BaseURL: upstream.URL, APIKey: "synthetic-engine-key", Model: "fixture-model", MaxOutputTokens: 32, TestLoopback: true})
			if err != nil {
				t.Fatal(err)
			}
			defer engine.Close()
			binding, err := gatewaybridge.New(route, engine)
			if err != nil {
				t.Fatal(err)
			}
			store := control.NewWithCatalog(pool, catalog)
			actor := identity.Principal{Issuer: "https://synthetic.invalid", Subject: "owner"}
			org, err := store.CreateOrganizationWithCurrency(ctx, actor, "engine fixture", "2026-11", provider.Currency, 10000)
			if err != nil {
				t.Fatal(err)
			}
			limit := int64(10000)
			if mode == "budget denial" {
				limit = 1
			}
			project, err := store.CreateProject(ctx, actor, org.ID, "fixture", limit)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.SetModels(ctx, actor, org.ID, project.ID, []string{"aliyun/fixture"}); err != nil {
				t.Fatal(err)
			}
			key, err := store.CreateKey(ctx, actor, org.ID, project.ID, "fixture", []string{"aliyun/fixture"}, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "revoked key" {
				if err := store.RevokeKey(ctx, actor, org.ID, project.ID, key.Key.ID); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "model permission removed" {
				if err := store.SetModels(ctx, actor, org.ID, project.ID, []string{}); err != nil {
					t.Fatal(err)
				}
			}
			audit := testutil.ContentAudit(t, pool)
			if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
				return audit.SetPolicy(ctx, tx, org.ID, project.ID, contentaudit.Policy{Enabled: true, RetentionDays: 1, MaxBytes: 8192, ConsentRef: "synthetic"})
			}); err != nil {
				t.Fatal(err)
			}
			h := &gateway.Handler{Providers: catalog, Executor: binding, Ledger: ledger, Authorize: store.ResolveKey, Attempts: recorder.New(pool), Admission: admission.New(pool), Circuits: circuit.New(pool), Content: audit, DeviceProof: store.VerifyDeviceRequest}
			if mode == "audit unavailable" {
				h.Content = contentaudit.New(pool, nil)
			}
			stream := mode == "stream" || mode == "missing usage" || mode == "duplicate usage" || mode == "malformed stream" || mode == "oversized stream" || mode == "usage then disconnect"
			maxTokens := 32
			if mode == "small limit" {
				maxTokens = 8
			}
			model := "aliyun/fixture"
			if mode == "unauthorized model" {
				model = "zhipu/fixture"
			}
			r := httptest.NewRequest("POST", "http://gateway.invalid/v1/chat/completions", strings.NewReader(fmt.Sprintf(`{"model":%q,"stream":%t,"max_tokens":%d,"messages":[{"role":"user","content":"synthetic question"}]}`, model, stream, maxTokens)))
			r.Header.Set("Authorization", "Bearer "+key.Secret)
			if mode == "device denial" {
				r.Header.Set("Authorization", "DPoP aigw_dt_invalid")
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			id := w.Header().Get("X-Request-ID")
			successful := mode == "json" || mode == "stream"
			dispatched := successful || mode == "missing usage" || mode == "duplicate usage" || mode == "upstream failure" || mode == "malformed stream" || mode == "oversized stream" || mode == "usage then disconnect"
			if dispatched {
				if calls.Load() != 1 {
					t.Fatalf("physical attempts = %d", calls.Load())
				}
				reservation, err := ledger.Get(ctx, id)
				if err != nil {
					t.Fatal(err)
				}
				wantState := "uncertain"
				if successful {
					wantState = "settled"
				}
				if reservation.State != wantState {
					t.Fatalf("ledger state %s, want %s; HTTP %d", reservation.State, wantState, w.Code)
				}
				if successful && (reservation.Actual == nil || *reservation.Actual != 14) {
					t.Fatal("wrong settled cost")
				}
				if !successful && reservation.Actual != nil {
					t.Fatal("unknown usage was charged")
				}
				var attemptID, requestID, outcome string
				if err := pool.QueryRow(ctx, `SELECT attempt_id,request_id,outcome FROM inference_attempts WHERE request_id=$1`, id).Scan(&attemptID, &requestID, &outcome); err != nil {
					t.Fatal(err)
				}
				if requestID != id || attemptID == "" || (successful && outcome != "completed") || (!successful && outcome != "failed") {
					t.Fatal("attempt correlation/outcome invalid")
				}
			} else {
				if calls.Load() != 0 {
					t.Fatal("denied request reached engine")
				}
				var count int
				if err := pool.QueryRow(ctx, `SELECT count(*) FROM budget_reservations WHERE request_id=$1`, id).Scan(&count); err != nil || count != 0 {
					t.Fatal("denied request created a reservation", err)
				}
				if w.Code < 400 {
					t.Fatal("denied request succeeded")
				}
			}
			if successful {
				if w.Code != 200 || !strings.Contains(w.Body.String(), "synthetic reply") || strings.Contains(w.Body.String(), "fixture-model") {
					t.Fatalf("normalized response invalid: status %d", w.Code)
				}
				if stream && !strings.HasSuffix(w.Body.String(), "data: [DONE]\n\n") {
					t.Fatal("successful stream not sealed")
				}
				err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
					meta, err := contentaudit.ScanMeta(tx.QueryRow(ctx, `SELECT `+contentaudit.Columns+` FROM content_audit_records WHERE request_id=$1`, id))
					if err != nil {
						return err
					}
					detail, err := audit.Decrypt(ctx, tx, meta)
					if err == nil && (!strings.Contains(detail.Request.Content, "synthetic question") || detail.Response == nil || !strings.Contains(detail.Response.Content, "synthetic reply")) {
						t.Error("audit content not correlated")
					}
					return err
				})
				if err != nil {
					t.Fatal(err)
				}
			} else if strings.Contains(w.Body.String(), "synthetic-secret-error") || (stream && strings.Contains(w.Body.String(), "[DONE]")) {
				t.Fatal("error disclosed or incomplete stream marked done")
			}
		})
	}
}

type forbiddenTransport struct{ t *testing.T }

func (f forbiddenTransport) RoundTrip(*http.Request) (*http.Response, error) {
	f.t.Error("native fallback must not execute")
	return nil, fmt.Errorf("forbidden native dispatch")
}
