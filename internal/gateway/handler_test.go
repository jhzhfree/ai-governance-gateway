package gateway_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jhzhfree/ai-governance-gateway/internal/budget"
	"github.com/jhzhfree/ai-governance-gateway/internal/gateway"
	"github.com/jhzhfree/ai-governance-gateway/internal/mockprovider"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
)

const payload = `{"model":"mock-text","stream":true,"max_tokens":8,"messages":[{"role":"user","content":"hello"}]}`

type fixture struct {
	store  *budget.Store
	server *httptest.Server
	done   chan struct{}
}

func setup(t *testing.T, provider http.Handler, wrap func(gateway.Ledger) gateway.Ledger, limit int64) fixture {
	t.Helper()
	_, s, scope := testutil.Database(t)
	testutil.Accounts(t, s, scope, limit, limit)
	up := httptest.NewServer(provider)
	t.Cleanup(up.Close)
	var ledger gateway.Ledger = s
	if wrap != nil {
		ledger = wrap(ledger)
	}
	h := &gateway.Handler{Ledger: ledger, Authorize: func(context.Context, string, string) (budget.Scope, error) { return scope, nil }, Client: &http.Client{Timeout: 6 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}, Upstream: up.URL + "/v1/chat/completions"}
	done := make(chan struct{}, 10)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { defer func() { done <- struct{}{} }(); h.ServeHTTP(w, r) }))
	t.Cleanup(server.Close)
	return fixture{store: s, server: server, done: done}
}

func (f fixture) wait(t *testing.T) {
	t.Helper()
	select {
	case <-f.done:
	case <-time.After(8 * time.Second):
		t.Fatal("gateway did not terminate")
	}
}

func (f fixture) request(t *testing.T, body string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest("POST", f.server.URL+"/v1/chat/completions", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer test-fixture")
	resp, err := f.server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	f.wait(t)
	return resp, string(b)
}

func (f fixture) assertState(t *testing.T, id, state string, actual *int64) {
	t.Helper()
	r, err := f.store.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if r.State != state {
		t.Fatalf("state=%s want %s", r.State, state)
	}
	if actual != nil && (r.Actual == nil || *r.Actual != *actual) {
		t.Fatalf("actual=%v want %d", r.Actual, *actual)
	}
}

func TestCompleteSSESettlesBeforeDone(t *testing.T) {
	f := setup(t, http.HandlerFunc(mockprovider.Handler), nil, 100)
	resp, body := f.request(t, payload)
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/event-stream" || !strings.HasSuffix(body, "data: [DONE]\n\n") {
		t.Fatalf("invalid response %d %s", resp.StatusCode, body)
	}
	actual := int64(4)
	f.assertState(t, resp.Header.Get("X-Request-ID"), "settled", &actual)
}

func TestClientDisconnectCancelsUpstreamAndRetainsHold(t *testing.T) {
	upstreamCanceled := make(chan struct{})
	f := setup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"first\"}}]}\n\n")
		_ = http.NewResponseController(w).Flush()
		<-r.Context().Done()
		close(upstreamCanceled)
	}), nil, 100)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", f.server.URL+"/v1/chat/completions", strings.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer test-fixture")
	resp, err := f.server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	if err != nil || !strings.Contains(line, "first") {
		resp.Body.Close()
		t.Fatal("first chunk did not arrive before upstream finished")
	}
	cancel()
	resp.Body.Close()
	select {
	case <-upstreamCanceled:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream did not receive cancellation")
	}
	f.wait(t)
	f.assertState(t, resp.Header.Get("X-Request-ID"), "uncertain", nil)
	r, err := f.store.Get(context.Background(), resp.Header.Get("X-Request-ID"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Amount != 10 || r.Actual != nil {
		t.Fatal("uncertain request lost conservative reservation")
	}
}

func TestFragmentedMultilineSSEPreservesFraming(t *testing.T) {
	events := "data: {\r\ndata: \"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\r\n\r\n" +
		"data: {\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":1,\"total_tokens\":3}}\n\ndata: [DONE]\n\n"
	f := setup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for start := 0; start < len(events); start += 7 {
			if _, err := io.WriteString(w, events[start:min(start+7, len(events))]); err != nil {
				return
			}
			if err := http.NewResponseController(w).Flush(); err != nil {
				return
			}
		}
	}), nil, 100)
	resp, body := f.request(t, payload)
	if !strings.Contains(body, "data: {\ndata: \"choices\"") || !strings.Contains(body, "data: [DONE]") {
		t.Fatalf("broken SSE framing: %s", body)
	}
	actual := int64(3)
	f.assertState(t, resp.Header.Get("X-Request-ID"), "settled", &actual)
}

func TestInvalidOrIncompleteUpstreamNeverProducesDone(t *testing.T) {
	valid := `data: {"choices":[{"delta":{"content":"first"}}]}` + "\n\n"
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"truncated", valid, 200},
		{"missing_usage", valid + "data: [DONE]\n\n", 200},
		{"malformed", "data: not-json\n\n", 200},
		{"null_event", "data: null\n\n", 200},
		{"upstream_error", "data: {\"error\":{\"message\":\"sensitive-upstream-secret\"}}\n\n", 200},
		{"oversized", "data: " + strings.Repeat("x", 70<<10) + "\n\n", 200},
		{"excess_usage", valid + "data: {\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":99,\"total_tokens\":101}}\n\ndata: [DONE]\n\n", 200},
		{"http_error", "sensitive-upstream-secret", 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}), nil, 100)
			resp, body := f.request(t, payload)
			if strings.Contains(body, "[DONE]") || strings.Contains(body, "sensitive-upstream-secret") {
				t.Fatalf("false success or leaked upstream error: %s", body)
			}
			f.assertState(t, resp.Header.Get("X-Request-ID"), "uncertain", nil)
		})
	}
}

type failedSettlement struct{ gateway.Ledger }

func (f failedSettlement) Settle(context.Context, string, int64) error {
	return errors.New("simulated unavailable database")
}

func TestSettlementFailureDoesNotEmitDone(t *testing.T) {
	f := setup(t, http.HandlerFunc(mockprovider.Handler), func(l gateway.Ledger) gateway.Ledger { return failedSettlement{l} }, 100)
	resp, body := f.request(t, payload)
	if strings.Contains(body, "[DONE]") {
		t.Fatal("acknowledged success without durable settlement")
	}
	if !strings.Contains(body, "settlement_failed") {
		t.Fatal("settlement error was misclassified")
	}
	f.assertState(t, resp.Header.Get("X-Request-ID"), "uncertain", nil)
}

func TestBudgetDenialDoesNotDispatch(t *testing.T) {
	f := setup(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("upstream called after budget denial") }), nil, 9)
	resp, body := f.request(t, payload)
	if resp.StatusCode != 429 || !strings.Contains(body, "budget_exceeded") {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
}

func TestUnsupportedRequestsAreRejectedBeforeDispatch(t *testing.T) {
	f := setup(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("unsupported request dispatched") }), nil, 100)
	for _, body := range []string{
		strings.Replace(payload, `"mock-text"`, `"real-model"`, 1),
		strings.Replace(payload, `"stream":true`, `"stream":false`, 1),
		strings.TrimSuffix(payload, "}") + `,"tools":[]}`,
		payload + " {}",
	} {
		resp, _ := f.request(t, body)
		if resp.StatusCode != 400 {
			t.Fatalf("unsupported request status=%d", resp.StatusCode)
		}
	}
}
