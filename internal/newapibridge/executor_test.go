package newapibridge_test

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

	"github.com/jhzhfree/ai-governance-gateway/internal/budget"
	"github.com/jhzhfree/ai-governance-gateway/internal/execution"
	"github.com/jhzhfree/ai-governance-gateway/internal/gateway"
	"github.com/jhzhfree/ai-governance-gateway/internal/newapibridge"
	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
)

const token = "synthetic-internal-token"
const reply = `{"id":"synthetic","object":"chat.completion","model":"backend-model","choices":[{"index":0,"message":{"role":"assistant","content":"synthetic reply"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`

func evidence(w http.ResponseWriter, r *http.Request) {
	for k, v := range map[string]string{"X-Aigw-Admission-Version": "2", "X-Aigw-Input-Limit": r.Header.Get("X-Aigw-Max-Input-Tokens"), "X-Aigw-Evidence-Version": "1", "X-Aigw-Request-Id": r.Header.Get("X-Aigw-Request-Id"), "X-Aigw-Attempt-Id": r.Header.Get("X-Aigw-Attempt-Id"), "X-Aigw-Usage-Source": "upstream", "X-Aigw-Physical-Attempts": "1"} {
		w.Header().Set(k, v)
	}
}
func catalog(t *testing.T) *provider.Catalog {
	t.Helper()
	cfg := testutil.MainlandConfig()
	for i := range cfg.Models {
		cfg.Models[i].NewAPIChannelID = 1
		cfg.Models[i].AllowedTools = []string{"lookup"}
	}
	c, e := provider.NewGoverned(cfg)
	if e != nil {
		t.Fatal(e)
	}
	return c
}
func executor(t *testing.T, url string, max int) *newapibridge.Executor {
	t.Helper()
	e, err := newapibridge.New(newapibridge.Config{Origin: url, ServiceToken: token, Models: map[string]string{"zhipu/fixture": "backend-model"}, MaxConcurrent: max, AllowLoopbackHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Close)
	return e
}
func request() gateway.Request {
	return gateway.Request{Model: "zhipu/fixture", MaxTokens: 8, Messages: []provider.Message{{Role: "user", Content: "synthetic"}}}
}
func TestIsolationEvidenceAndNoRedirectRetry(t *testing.T) {
	for _, mode := range []string{"valid", "missing", "wrong-attempt", "duplicate", "estimated", "retry", "input-limit", "redirect"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path != "/internal/governance/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer "+token || r.Header.Get("Cookie") != "" {
					t.Error("credential or path isolation failed")
				}
				var body provider.Request
				if json.NewDecoder(r.Body).Decode(&body) != nil || body.Model != "backend-model" {
					t.Error("model mapping failed")
				}
				if mode != "missing" {
					evidence(w, r)
				}
				switch mode {
				case "wrong-attempt":
					w.Header().Set("X-Aigw-Attempt-Id", "wrong")
				case "duplicate":
					w.Header().Add("X-Aigw-Usage-Source", "upstream")
				case "estimated":
					w.Header().Set("X-Aigw-Usage-Source", "estimated")
				case "input-limit":
					w.Header().Set("X-Aigw-Input-Limit", "1")
				case "retry":
					w.Header().Set("X-Aigw-Physical-Attempts", "2")
				case "redirect":
					w.Header().Set("Location", "/v1/chat/completions")
					w.WriteHeader(307)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, reply)
			}))
			defer server.Close()
			e := executor(t, server.URL, 1)
			route, _ := catalog(t).Find("zhipu/fixture")
			res, err := e.Open(context.Background(), route, request(), "request-1234567890", "attempt-1234567890")
			if mode == "valid" {
				if err != nil {
					t.Fatal(err)
				}
				b, _ := io.ReadAll(res.Body)
				res.Body.Close()
				if string(b) != reply {
					t.Fatal("body altered")
				}
			} else if err != newapibridge.ErrUnavailable {
				t.Fatal("invalid evidence accepted")
			}
			if calls.Load() != 1 {
				t.Fatal("request retried")
			}
		})
	}
}
func TestCloseCancelsStreamAndCapacity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		evidence(w, r)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	e := executor(t, server.URL, 1)
	route, _ := catalog(t).Find("zhipu/fixture")
	req := request()
	req.Stream = true
	res, err := e.Open(context.Background(), route, req, "request-1234567890", "attempt-1234567890")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Open(context.Background(), route, req, "request-1234567891", "attempt-1234567891"); err != newapibridge.ErrUnavailable {
		t.Fatal("unbounded concurrency")
	}
	done := make(chan struct{})
	go func() { e.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("close did not cancel stream")
	}
	res.Body.Close()
	if e.Validate(route, req) == nil {
		t.Fatal("closed executor accepted request")
	}
}
func TestOriginAndCredentialValidation(t *testing.T) {
	for _, origin := range []string{"http://example.com", "http://localhost", "https://user:pass@example.com", "https://example.com/path", "https://example.com?key=x", "https://example.com?", "https://example.com#", "https://example.com/%2f", "file:///tmp/socket"} {
		if e, err := newapibridge.New(newapibridge.Config{Origin: origin, ServiceToken: token, Models: map[string]string{"zhipu/fixture": "model"}, MaxConcurrent: 1, AllowLoopbackHTTP: true}); err == nil {
			e.Close()
			t.Fatal("unsafe origin accepted")
		}
	}
	c := newapibridge.Config{ServiceToken: token}
	b, _ := json.Marshal(c)
	if strings.Contains(string(b), token) || strings.Contains(fmt.Sprintf("%+v %#v", c, c), token) {
		t.Fatal("secret exposed")
	}
}
func TestGovernanceSettlementRequiresEvidence(t *testing.T) {
	t.Run("budget-denied", func(t *testing.T) {
		pool, ledger, scope := testutil.Database(t)
		scope.Currency = provider.Currency
		testutil.Accounts(t, ledger, scope, 1, 1)
		var calls atomic.Int32
		backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(500) }))
		defer backend.Close()
		handler := &gateway.Handler{Executor: executor(t, backend.URL, 1), Providers: catalog(t), Ledger: ledger, Attempts: execution.New(pool), Admission: testutil.PassAdmission{}, Circuits: testutil.PassCircuit{}, Authorize: func(context.Context, string, string) (budget.Scope, error) { return scope, nil }}
		raw, _ := json.Marshal(request())
		r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(string(raw)))
		r.Header.Set("Authorization", "Bearer synthetic-project-key")
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, r)
		if out.Code != 429 || calls.Load() != 0 {
			t.Fatal("budget rejection contacted backend")
		}
		var n int
		if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM budget_reservations").Scan(&n); err != nil || n != 0 {
			t.Fatal("rejected budget created reservation")
		}
	})

	for _, valid := range []bool{false, true} {
		t.Run(fmt.Sprint(valid), func(t *testing.T) {
			pool, ledger, scope := testutil.Database(t)
			scope.Currency = provider.Currency
			testutil.Accounts(t, ledger, scope, 10000, 10000)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if valid {
					evidence(w, r)
				}
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, reply)
			}))
			defer server.Close()
			e := executor(t, server.URL, 2)
			handler := &gateway.Handler{Executor: e, Providers: catalog(t), Ledger: ledger, Attempts: execution.New(pool), Admission: testutil.PassAdmission{}, Circuits: testutil.PassCircuit{}, Authorize: func(context.Context, string, string) (budget.Scope, error) { return scope, nil }}
			raw, _ := json.Marshal(request())
			r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(string(raw)))
			r.Header.Set("Authorization", "Bearer synthetic-public-project-key")
			r.Header.Set("Cookie", "must-not-forward")
			out := httptest.NewRecorder()
			handler.ServeHTTP(out, r)
			var state string
			var held, spent int64
			if err := pool.QueryRow(context.Background(), "SELECT state FROM budget_reservations").Scan(&state); err != nil {
				t.Fatal(err)
			}
			if err := pool.QueryRow(context.Background(), "SELECT held,spent FROM budget_accounts WHERE project_id=$1", scope.Project).Scan(&held, &spent); err != nil {
				t.Fatal(err)
			}
			if valid {
				if out.Code != 200 || state != "settled" || held != 0 || spent != 16 {
					t.Fatalf("valid result: status=%d state=%s held=%d spent=%d", out.Code, state, held, spent)
				}
			} else if out.Code != 502 || state != "uncertain" || held != 288 || spent != 0 {
				t.Fatalf("missing evidence released funds: status=%d state=%s held=%d spent=%d", out.Code, state, held, spent)
			}
		})
	}
}

func TestStreamingCompletionEvidenceKeepsUnknownFunds(t *testing.T) {
	for _, mode := range []string{"complete", "no-done", "no-usage", "tool-complete", "tool-no-proof"} {
		t.Run(mode, func(t *testing.T) {
			pool, ledger, scope := testutil.Database(t)
			scope.Currency = provider.Currency
			testutil.Accounts(t, ledger, scope, 10000, 10000)
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				evidence(w, r)
				if strings.HasPrefix(mode, "tool-") {
					w.Header().Set("X-Aigw-Evidence-Version", "2")
					w.Header().Set("Trailer", "X-Aigw-Completion")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"synthetic\"},\"finish_reason\":null}]}\n\n")
				if strings.HasPrefix(mode, "tool-") {
					io.WriteString(w, "data: "+`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_verified","type":"function","function":{"name":"lookup","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`+"\n\n")
				} else {
					io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
				}
				if mode != "no-usage" {
					io.WriteString(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5}}\n\n")
				}
				if mode != "no-done" {
					io.WriteString(w, "data: [DONE]\n\n")
				}
				if mode == "tool-complete" {
					w.Header().Set("X-Aigw-Completion", "completed")
				}
			}))
			defer backend.Close()
			handler := &gateway.Handler{Executor: executor(t, backend.URL, 2), Providers: catalog(t), Ledger: ledger, Attempts: execution.New(pool), Admission: testutil.PassAdmission{}, Circuits: testutil.PassCircuit{}, Authorize: func(context.Context, string, string) (budget.Scope, error) { return scope, nil }}
			req := request()
			req.Stream = true
			if strings.HasPrefix(mode, "tool-") {
				req.Tools = []provider.Tool{{Type: "function", Function: provider.FunctionDefinition{Name: "lookup", Parameters: json.RawMessage(`{"type":"object"}`)}}}
			}
			raw, _ := json.Marshal(req)
			r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(string(raw)))
			r.Header.Set("Authorization", "Bearer synthetic-project-key")
			out := httptest.NewRecorder()
			handler.ServeHTTP(out, r)
			if mode == "tool-no-proof" && strings.Contains(out.Body.String(), "call_verified") {
				t.Fatal("executable call released without completion proof")
			}
			if mode == "tool-complete" && !strings.Contains(out.Body.String(), "call_verified") {
				t.Fatal("verified call not delivered")
			}
			var state string
			var held, spent int64
			if err := pool.QueryRow(context.Background(), "SELECT state FROM budget_reservations").Scan(&state); err != nil {
				t.Fatal(err)
			}
			if err := pool.QueryRow(context.Background(), "SELECT held,spent FROM budget_accounts WHERE project_id=$1", scope.Project).Scan(&held, &spent); err != nil {
				t.Fatal(err)
			}
			if mode == "complete" || mode == "tool-complete" {
				if state != "settled" || held != 0 || spent != 14 {
					t.Fatalf("stream settlement %s %d %d", state, held, spent)
				}
			} else if state != "uncertain" || held != 288 || spent != 0 {
				t.Fatalf("incomplete stream settled %s %d %d", state, held, spent)
			}
		})
	}
}

func TestUnreviewedImageCapabilityDoesNotDispatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("unreviewed image dispatched") }))
	defer server.Close()
	e := executor(t, server.URL, 1)
	route, _ := catalog(t).Find("zhipu/fixture")
	req := request()
	req.Messages = []provider.Message{{Role: "user", Parts: []provider.ContentPart{{Type: "image_url", ImageURL: &provider.ImageURL{URL: testutil.InlinePNG}}}}}
	if _, err := e.Open(context.Background(), route, req, "ImageRequest123456", "ImageAttempt123456"); err != newapibridge.ErrInvalid {
		t.Fatal("image capability review bypassed")
	}
}
