package gatewaybridge

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
	"github.com/jhzhfree/ai-governance-gateway/internal/completion"
	"github.com/jhzhfree/ai-governance-gateway/internal/contentaudit"
	"github.com/jhzhfree/ai-governance-gateway/internal/control"
	recorder "github.com/jhzhfree/ai-governance-gateway/internal/execution"
	"github.com/jhzhfree/ai-governance-gateway/internal/gateway"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
	adapter "github.com/jhzhfree/ai-governance-gateway/modules/engine/adapters/bifrost"
	"github.com/jhzhfree/ai-governance-gateway/modules/engine/execution"
)

func TestGovernedPooledBifrostExecution(t *testing.T) {
	for _, protocol := range []string{"chat", "responses", "embeddings"} {
		t.Run(protocol, func(t *testing.T) { governedPooledExecution(t, protocol, false) })
	}
}

func governedPooledExecution(t *testing.T, protocol string, cached bool) {
	native := strings.HasPrefix(protocol, "native-")
	if native {
		protocol = strings.TrimPrefix(protocol, "native-")
		cached = true
	}
	modes := []string{"json", "stream", "binding failure", "completion persistence failure", "receipt failure", "completed then cancel", "json without finish", "missing usage", "duplicate usage", "upstream failure", "revoked key", "budget denial", "device denial", "audit unavailable", "small limit", "unauthorized model", "model permission removed", "malformed stream", "oversized stream", "usage then disconnect"}
	if protocol == "responses" {
		modes = append(modes, "no journal", "no settlement store", "stored responses", "responses tools")
	}
	if protocol == "embeddings" {
		modes = []string{"json", "binding failure", "completion persistence failure", "receipt failure", "completed then cancel", "missing usage", "duplicate usage", "upstream failure", "revoked key", "budget denial", "device denial", "audit unavailable", "unauthorized model", "model permission removed", "no journal", "no settlement store"}
	}
	modes = append(modes, "oversized json", "wrong media")
	routeModel := "aliyun/fixture"
	originalUsage := `{ "prompt_tokens":3, "completion_tokens":2, "total_tokens":5 }`
	wantCost := int64(14)
	if cached {
		routeModel = "deepseek/fixture"
		wantCost = 11
		originalUsage = `{ "prompt_tokens":3, "completion_tokens":2, "total_tokens":5, "prompt_cache_hit_tokens":2, "prompt_cache_miss_tokens":1 }`
		modes = []string{"json", "stream", "missing cache", "cache mismatch", "binding failure", "completion persistence failure", "receipt failure", "completed then cancel", "missing usage", "duplicate usage", "usage then disconnect", "budget denial", "revoked key", "device denial", "audit unavailable", "model permission removed", "no journal", "no settlement store"}
		if protocol == "responses" {
			modes = []string{"json", "stream", "missing cache", "receipt failure", "no journal", "no settlement store"}
		}
	}
	replyText := "synthetic reply"
	if protocol == "embeddings" {
		wantCost = 6
		replyText = "embedding"
	}
	if native {
		originalUsage = `{ "input_tokens":1, "output_tokens":2, "cache_read_input_tokens":2, "cache_creation_input_tokens":0 }`
		modes = []string{"json", "missing cache", "missing usage", "duplicate usage", "upstream failure", "binding failure", "completion persistence failure", "receipt failure", "completed then cancel", "budget denial", "revoked key", "device denial", "audit unavailable", "model permission removed", "no journal", "no settlement store", "native stream refusal"}
		if protocol != "messages" {
			modes = []string{"json", "receipt failure", "native stream refusal"}
		}
	}
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			pool, ledger, _ := testutil.Database(t)
			journal := completion.New(pool)
			cfg := testutil.MainlandConfig()
			for i := range cfg.Models {
				cfg.Models[i].MaxOutputTokens = 32
			}
			newCatalog := provider.New
			if protocol == "embeddings" {
				cfg.Models = cfg.Models[:1]
				cfg.Models[0].API = "embeddings"
				cfg.Models[0].MaxOutputTokens = 0
				cfg.Models[0].OutputPerMillion = 0
				newCatalog = provider.NewEmbedded
			}
			if cached {
				cfg.Models = cfg.Models[3:]
				cfg.Models[0].CachePricing = &provider.CachePricing{ReadPerMillion: 500000}
				if native {
					cfg.Models[0].API = "messages"
				}
				newCatalog = provider.NewEmbedded
			}
			catalog, err := newCatalog(cfg, func(string) string { return "synthetic-route-key" }, &http.Client{Transport: forbiddenTransport{t}})
			if err != nil {
				t.Fatal(err)
			}
			route, _ := catalog.Find(routeModel)
			var calls atomic.Int32
			var cancelRequest context.CancelFunc
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				wantPath := "/compatible-mode/v1/chat/completions"
				if cached {
					wantPath = "/chat/completions"
				}
				if native {
					wantPath = "/anthropic/v1/messages"
				}
				if protocol == "embeddings" {
					wantPath = "/compatible-mode/v1/embeddings"
				}
				if r.URL.Path != wantPath || r.Header.Get("Authorization") != "Bearer synthetic-route-key" {
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
				jsonMedia := "application/json"
				if mode == "wrong media" {
					jsonMedia = "text/html"
				}
				if mode == "oversized json" {
					w.Header().Set("Content-Type", jsonMedia)
					w.(http.Flusher).Flush()
					io.WriteString(w, strings.Repeat(" ", 1<<20))
				}
				if native {
					w.Header().Set("Content-Type", jsonMedia)
					usage := `,"usage":` + originalUsage
					if mode == "missing usage" {
						usage = ""
					}
					if mode == "missing cache" {
						usage = strings.Replace(usage, `, "cache_creation_input_tokens":0`, "", 1)
					}
					if mode == "duplicate usage" {
						usage += usage
					}
					fmt.Fprintf(w, `{"id":"msg_fixture","type":"message","role":"assistant","model":"fixture-model","content":[{"type":"text","text":"synthetic reply"}],"stop_reason":"end_turn","stop_sequence":null%s}`, usage)
					return
				}
				if protocol == "embeddings" {
					w.Header().Set("Content-Type", jsonMedia)
					usage := `,"usage":{ "prompt_tokens":3, "total_tokens":3 }`
					if mode == "missing usage" {
						usage = ""
					}
					if mode == "duplicate usage" {
						usage += usage
					}
					fmt.Fprintf(w, `{"object":"list","model":"fixture-model","data":[{"object":"embedding","index":0,"embedding":[0.1,-0.2]}]%s}`, usage)
					return
				}
				usage := `,"usage":` + originalUsage
				if mode == "missing cache" {
					usage = strings.ReplaceAll(usage, `, "prompt_cache_hit_tokens":2, "prompt_cache_miss_tokens":1`, "")
				}
				if mode == "cache mismatch" {
					usage = strings.ReplaceAll(usage, `"prompt_cache_miss_tokens":1`, `"prompt_cache_miss_tokens":2`)
				}
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
				w.Header().Set("Content-Type", jsonMedia)
				if mode == "json without finish" {
					_, _ = fmt.Fprintf(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"synthetic reply"},"finish_reason":null}]%s}`, usage)
					return
				}
				_, _ = fmt.Fprintf(w, `{"id":"fixture","object":"chat.completion","model":"fixture-model","choices":[{"index":0,"message":{"role":"assistant","content":"synthetic reply"},"finish_reason":"stop"}]%s}`, usage)
			}))
			defer upstream.Close()
			enginePool, err := newPool(ctx, PoolConfig{MaxBindings: 2, MaxConcurrent: 4}, func(ctx context.Context, r *provider.Route, n int) (ownedBinding, error) {
				binding, err := newSupplier(ctx, r, n, func(ctx context.Context, cfg adapter.Config) (execution.Executor, error) {
					cfg.BaseURL = upstream.URL
					cfg.TestLoopback = true
					e, err := adapter.New(ctx, cfg)
					if err == nil && mode == "completed then cancel" {
						return terminalCancelExecutor{Executor: e, cancel: func() { cancelRequest() }}, nil
					}
					return e, err
				})
				if err != nil {
					return nil, err
				}
				if mode != "no journal" {
					binding.binding.journal = journal
				}
				return binding, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			defer enginePool.Close()
			if err := enginePool.Publish(ctx, 1, []*provider.Route{route}); err != nil {
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
			if err := store.SetModels(ctx, actor, org.ID, project.ID, []string{routeModel}); err != nil {
				t.Fatal(err)
			}
			key, err := store.CreateKey(ctx, actor, org.ID, project.ID, "fixture", []string{routeModel}, time.Hour)
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
			h := &gateway.Handler{Completions: journal, Providers: catalog, Executor: enginePool, Ledger: ledger, Authorize: store.ResolveKey, Attempts: recorder.New(pool), Admission: admission.New(pool), Circuits: circuit.New(pool), Content: audit, DeviceProof: store.VerifyDeviceRequest}
			if mode == "no settlement store" {
				h.Completions = nil
			}
			if mode == "audit unavailable" {
				h.Content = contentaudit.New(pool, nil)
			}
			stream := !native && protocol != "embeddings" && (mode == "stream" || mode == "missing usage" || mode == "duplicate usage" || mode == "malformed stream" || mode == "oversized stream" || mode == "usage then disconnect")
			if mode == "native stream refusal" {
				stream = true
			}
			maxTokens := 32
			if mode == "small limit" {
				maxTokens = 8
			}
			model := routeModel
			if mode == "unauthorized model" {
				model = "zhipu/fixture"
			}
			path := "/v1/chat/completions"
			if protocol == "messages" {
				path = "/v1/messages"
			}
			raw := fmt.Sprintf(`{"model":%q,"stream":%t,"max_tokens":%d,"messages":[{"role":"user","content":"synthetic question"}]}`, model, stream, maxTokens)
			if protocol == "responses" {
				path = "/v1/responses"
				raw = fmt.Sprintf(`{"model":%q,"stream":%t,"max_output_tokens":%d,"input":"synthetic question","store":false}`, model, stream, maxTokens)
				if mode == "stored responses" {
					raw = strings.Replace(raw, `"store":false`, `"store":true`, 1)
				}
				if mode == "responses tools" {
					raw = strings.TrimSuffix(raw, "}") + `,"tools":[]}`
				}
			}
			if protocol == "embeddings" {
				path = "/v1/embeddings"
				raw = fmt.Sprintf(`{"model":%q,"input":"synthetic question","encoding_format":"float"}`, model)
			}
			r := httptest.NewRequest("POST", "http://gateway.invalid"+path, strings.NewReader(raw))
			if mode == "completed then cancel" {
				requestCtx, cancel := context.WithCancel(r.Context())
				cancelRequest = cancel
				defer cancel()
				r = r.WithContext(requestCtx)
			}
			r.Header.Set("Authorization", "Bearer "+key.Secret)
			if mode == "device denial" {
				r.Header.Set("Authorization", "DPoP aigw_dt_invalid")
			}
			for name, table := range map[string]string{"binding failure": "engine_execution_bindings", "completion persistence failure": "engine_completion_events", "receipt failure": "engine_completion_receipts"} {
				if mode == name {
					if _, err := pool.Exec(ctx, "ALTER TABLE "+table+" ADD CONSTRAINT synthetic_failure CHECK(false)"); err != nil {
						t.Fatal(err)
					}
				}
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if enginePool.Stats().Active != 0 {
				t.Fatal("governance response leaked execution lease")
			}
			id := w.Header().Get("X-Request-ID")
			successful := mode == "json" || mode == "stream"
			dispatched := mode == "missing cache" || mode == "cache mismatch" || mode == "oversized json" || mode == "wrong media" || successful || mode == "json without finish" || mode == "missing usage" || mode == "duplicate usage" || mode == "upstream failure" || mode == "malformed stream" || mode == "oversized stream" || mode == "usage then disconnect"
			if dispatched || mode == "binding failure" || mode == "completion persistence failure" || mode == "receipt failure" || mode == "completed then cancel" {
				wantCalls := int32(1)
				if mode == "binding failure" {
					wantCalls = 0
				}
				if calls.Load() != wantCalls {
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
				var events, receipts int
				if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM engine_completion_events),(SELECT count(*) FROM engine_completion_receipts)`).Scan(&events, &receipts); err != nil {
					t.Fatal(err)
				}
				wantEvents, wantReceipts := 0, 0
				if successful {
					wantEvents, wantReceipts = 1, 1
				} else if mode == "receipt failure" || mode == "completed then cancel" {
					wantEvents = 1
				}
				if events != wantEvents || receipts != wantReceipts {
					t.Fatal("completion evidence/receipt mismatch")
				}
				if successful {
					wantRaw := originalUsage
					if protocol == "embeddings" {
						wantRaw = `{ "prompt_tokens":3, "total_tokens":3 }`
					}
					var raw []byte
					if err := pool.QueryRow(ctx, `SELECT e.raw_usage FROM engine_completion_events e JOIN engine_execution_bindings b USING(attempt_id) WHERE b.request_id=$1`, id).Scan(&raw); err != nil || string(raw) != wantRaw {
						t.Fatal("original usage byte identity lost", err)
					}
				}
				if successful && (reservation.Actual == nil || *reservation.Actual != wantCost) {
					t.Fatal("wrong settled cost")
				}
				if !successful && reservation.Actual != nil {
					t.Fatal("unknown usage was charged")
				}
				var attemptID, requestID, outcome string
				if err := pool.QueryRow(ctx, `SELECT attempt_id,request_id,outcome FROM inference_attempts WHERE request_id=$1`, id).Scan(&attemptID, &requestID, &outcome); err != nil {
					t.Fatal(err)
				}
				wantOutcome := "failed"
				if successful {
					wantOutcome = "completed"
				} else if mode == "completed then cancel" {
					wantOutcome = "canceled"
				}
				if requestID != id || attemptID == "" || outcome != wantOutcome {
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
			if mode == "completed then cancel" || mode == "receipt failure" {
				if mode == "receipt failure" {
					if _, err := pool.Exec(ctx, "ALTER TABLE engine_completion_receipts DROP CONSTRAINT synthetic_failure"); err != nil {
						t.Fatal(err)
					}
				}
				if n, err := journal.Recover(ctx, time.Now().Add(time.Second)); err != nil || n != 1 {
					t.Fatal("lost observed completion on client cancel", err)
				}
				r, err := ledger.Get(ctx, id)
				if err != nil || r.State != "settled" || r.Actual == nil || *r.Actual != wantCost || calls.Load() != 1 {
					t.Fatal("canceled delivery evidence failed recovery")
				}
			}
			if successful {
				if w.Code != 200 || !strings.Contains(w.Body.String(), replyText) || strings.Contains(w.Body.String(), "fixture-model") {
					t.Fatalf("normalized response invalid: status %d", w.Code)
				}
				if stream {
					terminal := strings.HasSuffix(w.Body.String(), "data: [DONE]\n\n")
					if protocol == "responses" {
						terminal = strings.Contains(w.Body.String(), "event: response.completed\n") && !strings.Contains(w.Body.String(), "[DONE]")
					}
					if !terminal {
						t.Fatal("successful stream not sealed")
					}
				}
				if protocol == "responses" && !stream {
					var result provider.ResponsesResult
					if json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Status != "completed" || result.Usage.InputTokens != 3 || result.Usage.OutputTokens != 2 {
						t.Fatal("invalid public Responses result")
					}
				}
				err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
					meta, err := contentaudit.ScanMeta(tx.QueryRow(ctx, `SELECT `+contentaudit.Columns+` FROM content_audit_records WHERE request_id=$1`, id))
					if err != nil {
						return err
					}
					detail, err := audit.Decrypt(ctx, tx, meta)
					if err == nil && (!strings.Contains(detail.Request.Content, "synthetic question") || detail.Response == nil || !strings.Contains(detail.Response.Content, replyText)) {
						t.Error("audit content not correlated")
					}
					return err
				})
				if err != nil {
					t.Fatal(err)
				}
			} else if strings.Contains(w.Body.String(), "synthetic-secret-error") || (stream && (strings.Contains(w.Body.String(), "[DONE]") || strings.Contains(w.Body.String(), "event: response.completed\n"))) {
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

// Cancel only after the real adapter has established terminal protocol evidence.
type terminalCancelExecutor struct {
	execution.Executor
	cancel func()
}

func (e terminalCancelExecutor) Execute(ctx context.Context, r execution.Request, emit execution.Emit) error {
	return e.Executor.Execute(ctx, r, func(c context.Context, event execution.Event) error {
		if event.Completed {
			e.cancel()
		}
		return emit(c, event)
	})
}

func TestGovernedDeepSeekCacheExecution(t *testing.T) {
	for _, protocol := range []string{"chat", "responses"} {
		t.Run(protocol, func(t *testing.T) { governedPooledExecution(t, protocol, true) })
	}
}

func TestGovernedNativeMessagesJSONExecution(t *testing.T) {
	for _, protocol := range []string{"messages", "chat", "responses"} {
		t.Run(protocol, func(t *testing.T) { governedPooledExecution(t, "native-"+protocol, true) })
	}
}
