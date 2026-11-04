package httpapi_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/routers/legacy"
	"github.com/jhzhfree/ai-governance-gateway/api/openapi"
	"github.com/jhzhfree/ai-governance-gateway/internal/admission"
	"github.com/jhzhfree/ai-governance-gateway/internal/budget"
	"github.com/jhzhfree/ai-governance-gateway/internal/circuit"
	"github.com/jhzhfree/ai-governance-gateway/internal/control"
	"github.com/jhzhfree/ai-governance-gateway/internal/execution"
	"github.com/jhzhfree/ai-governance-gateway/internal/gateway"
	"github.com/jhzhfree/ai-governance-gateway/internal/httpapi"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
	"github.com/jhzhfree/ai-governance-gateway/internal/observe"
	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
)

func providerSetup(t *testing.T, supplier http.Handler, wrap ...func(gateway.Ledger) gateway.Ledger) fixture {
	t.Helper()
	pool, ledger, _ := testutil.Database(t)
	catalog, _ := testutil.MainlandCatalog(t, supplier)
	store := control.NewWithCatalog(pool, catalog)
	issuer := testutil.NewIssuer(t)
	auth, err := identity.NewOIDC(context.Background(), issuer.URL, "aigw-management")
	if err != nil {
		t.Fatal(err)
	}
	monitor := observe.New()
	mux := http.NewServeMux()
	mux.Handle("/api/v1/", httpapi.NewWithRuntime(store, auth, nil, monitor))
	var ledgerAPI gateway.Ledger = ledger
	if len(wrap) > 0 {
		ledgerAPI = wrap[0](ledger)
	}
	mux.Handle("/v1/", &gateway.Handler{ListModels: store.KeyModels, Monitor: monitor, Circuits: circuit.New(pool), Admission: admission.New(pool), Attempts: execution.New(pool), Providers: catalog, Ledger: ledgerAPI, Authorize: store.ResolveKey})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	doc, err := openapi3.NewLoader().LoadFromData(openapi.Document)
	if err != nil {
		t.Fatal(err)
	}
	contract, err := legacy.NewRouter(doc)
	if err != nil {
		t.Fatal(err)
	}
	return fixture{t: t, pool: pool, store: store, issuer: issuer, server: server, contract: contract}
}
func providerProject(f fixture) (control.Organization, control.Project, control.IssuedKey) {
	f.t.Helper()
	o := parse[control.Organization](f.t, f.request(f.token("alice"), "POST", "/api/v1/organizations", map[string]any{"name": "Mainland test team", "period": "2026-11", "currency": "CNY_MICRO", "limit_amount": 10000}, 201))
	p := parse[control.Project](f.t, f.request(f.token("alice"), "POST", orgPath(o)+"/projects", map[string]any{"name": "Mainland project", "limit_amount": 5000}, 201))
	models := []string{"aliyun/fixture", "volcengine/fixture", "zhipu/fixture"}
	f.request(f.token("alice"), "PUT", projectPath(o, p)+"/models", map[string]any{"models": models}, 204)
	key := parse[control.IssuedKey](f.t, f.request(f.token("alice"), "POST", projectPath(o, p)+"/keys", map[string]any{"name": "Multi-provider key", "models": models, "ttl_seconds": 3600}, 201))
	return o, p, key
}
func realPayload(model string, stream bool) map[string]any {
	return map[string]any{"model": model, "stream": stream, "max_tokens": 8, "messages": []map[string]string{{"role": "user", "content": "synthetic mainland protocol test"}}}
}
func TestThreeMainlandSuppliersThroughGovernanceAndBudget(t *testing.T) {
	f := providerSetup(t, testutil.SupplierFixture(t))
	o, p, key := providerProject(f)
	models := f.request(f.token("alice"), "GET", "/api/v1/models", nil, 200)
	if !strings.Contains(string(models), "CNY_MICRO") || strings.Contains(string(models), "synthetic-AIGW") || strings.Contains(string(models), "evidence_ref") {
		t.Fatal("model catalog invalid or exposed internal data")
	}
	for _, profile := range provider.Profiles() {
		t.Run(profile.ID, func(t *testing.T) {
			f := f
			f.t = t
			for _, stream := range []bool{false, true} {
				body := f.request(key.Secret, "POST", "/v1/chat/completions", realPayload(profile.ID+"/fixture", stream), 200)
				if stream && !strings.HasSuffix(string(body), "data: [DONE]\n\n") {
					t.Fatal("real adapter did not finish durable stream")
				}
				if !stream {
					v := parse[provider.Response](t, body)
					if v.Model != profile.ID+"/fixture" || v.Usage == nil {
						t.Fatal("normalized non-stream response invalid")
					}
				}
			}
		})
	}
	usage := parse[control.Usage](t, f.request(f.token("alice"), "GET", projectPath(o, p)+"/usage", nil, 200))
	if usage.Currency != "CNY_MICRO" || usage.Spent != 84 || usage.Held != 0 {
		t.Fatal("three-provider ledger not settled in separate integer yuan units")
	}
	records := parse[requestPage](t, f.request(f.token("alice"), "GET", projectPath(o, p)+"/requests", nil, 200))
	if len(records.Data) != 6 {
		t.Fatal("supplier requests missing from accounting history")
	}
	for _, record := range records.Data {
		if a := record.Attempt; a == nil || a.Outcome != "completed" || a.InputTokens == nil || *a.InputTokens != 3 || a.OutputTokens == nil || *a.OutputTokens != 2 || a.Region == "local" || a.DurationMS == nil || a.RequestID != record.RequestID {
			t.Fatal("real supplier metadata differs from validated execution")
		}

		if record.Reserved != 288 || record.Actual == nil || *record.Actual != 14 || !strings.Contains(record.PriceVersion, "#") {
			t.Fatal("request lost price snapshot or upper bound")
		}
	}
	f.request(f.token("alice"), "PUT", projectPath(o, p)+"/models", map[string]any{"models": []string{"unreviewed/model"}}, 400)
	f.request(f.token("alice"), "PUT", projectPath(o, p)+"/models", map[string]any{"models": []string{"aliyun/fixture"}}, 204)
	f.request(key.Secret, "POST", "/v1/chat/completions", realPayload("zhipu/fixture", false), 403)
	f.request(f.token("alice"), "DELETE", projectPath(o, p)+"/keys/"+key.ID, nil, 204)
	f.request(key.Secret, "POST", "/v1/chat/completions", realPayload("aliyun/fixture", false), 401)
}
func TestSupplierErrorsAndInvalidUsageKeepConservativeHolds(t *testing.T) {
	for _, tc := range []struct {
		name, body, media string
		stream            bool
		status            int
	}{
		{"http_error", "sensitive-upstream-secret", "application/json", false, 401},
		{"missing_usage", `{"choices":[{"index":0,"message":{"role":"assistant","content":"answer"},"finish_reason":"stop"}]}`, "application/json", false, 200},
		{"oversized_usage", `{"choices":[{"index":0,"message":{"role":"assistant","content":"answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":129,"completion_tokens":2,"total_tokens":131}}`, "application/json", false, 200},
		{"private_error", `{"error":{"message":"sensitive-upstream-secret"}}`, "application/json", false, 200},
		{"truncated_sse", "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"synthetic\"}}]}\n\n", "text/event-stream", true, 200},
		{"no_finish", "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5}}\n\ndata: [DONE]\n\n", "text/event-stream", true, 200},
		{"duplicate_usage", "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5}}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5}}\n\ndata: [DONE]\n\n", "text/event-stream", true, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := providerSetup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tc.media)
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			o, p, key := providerProject(f)
			req, _ := http.NewRequest("POST", f.server.URL+"/v1/chat/completions", strings.NewReader(mustJSON(t, realPayload("aliyun/fixture", tc.stream))))
			req.Header.Set("Authorization", "Bearer "+key.Secret)
			response, err := f.server.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			if strings.Contains(string(body), "[DONE]") || strings.Contains(string(body), "sensitive-upstream-secret") {
				t.Fatal("false supplier success or raw error leak")
			}
			reservation, err := budget.New(f.pool).Get(context.Background(), response.Header.Get("X-Request-ID"))
			if err != nil || reservation.State != "uncertain" || reservation.Actual != nil || reservation.Amount != 288 {
				t.Fatal("failure refunded or settled an uncertain supplier request")
			}
			usage := parse[control.Usage](t, f.request(f.token("alice"), "GET", projectPath(o, p)+"/usage", nil, 200))
			records := parse[requestPage](t, f.request(f.token("alice"), "GET", projectPath(o, p)+"/requests?outcome=failed", nil, 200))
			if len(records.Data) != 1 || records.Data[0].Attempt == nil || records.Data[0].Attempt.ErrorCode == "" || records.Data[0].Attempt.DurationMS == nil {
				t.Fatal("failed supplier execution missing from filtered records")
			}
			if usage.Held != 288 || usage.Spent != 0 {
				t.Fatal("uncertain hold was lost")
			}
		})
	}
}
func TestMainlandSupplierClientCancellationCancelsTLSUpstream(t *testing.T) {
	canceled := make(chan struct{})
	finished := make(chan struct{})
	f := providerSetup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(finished)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"synthetic\"}}]}\n\n")
		_ = http.NewResponseController(w).Flush()
		<-r.Context().Done()
		close(canceled)
	}))
	o, p, key := providerProject(f)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", f.server.URL+"/v1/chat/completions", strings.NewReader(mustJSON(t, realPayload("volcengine/fixture", true))))
	req.Header.Set("Authorization", "Bearer "+key.Secret)
	response, err := f.server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bufio.NewReader(response.Body).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	cancel()
	response.Body.Close()
	select {
	case <-canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("supplier request outlived client")
	}
	<-finished
	// Poll only for the independent cleanup transaction, never a supplier retry.
	deadline := time.Now().Add(3 * time.Second)
	for {
		record, err := budget.New(f.pool).Get(context.Background(), response.Header.Get("X-Request-ID"))
		if err == nil && record.State == "uncertain" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("canceled request not retained for reconciliation")
		}
		time.Sleep(10 * time.Millisecond)
	}
	record := parse[control.RequestRecord](t, f.request(f.token("alice"), "GET", projectPath(o, p)+"/requests/"+response.Header.Get("X-Request-ID"), nil, 200))
	if record.Attempt == nil || record.Attempt.Outcome != "canceled" || record.Attempt.ErrorCode != "client_canceled" || !record.Attempt.Stream {
		t.Fatal("cancellation not recorded independently from accounting")
	}

}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal("cannot encode test payload")
	}
	return string(data)
}

type rejectSupplierSettlement struct{ gateway.Ledger }

func (rejectSupplierSettlement) Settle(context.Context, string, int64) error {
	return errors.New("synthetic database unavailable")
}
func TestSupplierSettlementFailureNeverAcknowledgesSuccess(t *testing.T) {
	f := providerSetup(t, testutil.SupplierFixture(t), func(ledger gateway.Ledger) gateway.Ledger { return rejectSupplierSettlement{ledger} })
	o, p, key := providerProject(f)
	for _, stream := range []bool{false, true} {
		want := 503
		if stream {
			want = 200
		}
		body := f.request(key.Secret, "POST", "/v1/chat/completions", realPayload("aliyun/fixture", stream), want)
		if strings.Contains(string(body), "[DONE]") || !strings.Contains(string(body), "settlement_failed") {
			t.Fatal("supplier success acknowledged without durable settlement")
		}
	}
	usage := parse[control.Usage](t, f.request(f.token("alice"), "GET", projectPath(o, p)+"/usage", nil, 200))
	if usage.Held != 576 || usage.Spent != 0 {
		t.Fatal("failed settlement lost holds")
	}
}

func TestRealModelsCannotBeGrantedAgainstHistoricalMockCurrency(t *testing.T) {
	f := providerSetup(t, testutil.SupplierFixture(t))
	legacy := control.New(f.pool)
	principal := identity.Principal{Issuer: f.issuer.URL, Subject: "alice"}
	org, err := legacy.CreateOrganization(context.Background(), principal, "Historical MOCK team", "2026-11", 1000)
	if err != nil {
		t.Fatal(err)
	}
	project, err := legacy.CreateProject(context.Background(), principal, org.ID, "Historical app", 1000)
	if err != nil {
		t.Fatal(err)
	}
	f.request(f.token("alice"), "PUT", projectPath(org, project)+"/models", map[string]any{"models": []string{"aliyun/fixture"}}, 400)
	f.request(f.token("alice"), "POST", projectPath(org, project)+"/keys", map[string]any{"name": "Invalid currency grant", "models": []string{"aliyun/fixture"}, "ttl_seconds": 3600}, 403)
	usage := parse[control.Usage](t, f.request(f.token("alice"), "GET", projectPath(org, project)+"/usage", nil, 200))
	if usage.Currency != "MOCK" || usage.Held != 0 || usage.Spent != 0 {
		t.Fatal("model grant changed historical currency or accounting")
	}
}
