package gateway_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jhzhfree/ai-governance-gateway/internal/admission"
	"github.com/jhzhfree/ai-governance-gateway/internal/budget"
	"github.com/jhzhfree/ai-governance-gateway/internal/circuit"
	"github.com/jhzhfree/ai-governance-gateway/internal/execution"
	"github.com/jhzhfree/ai-governance-gateway/internal/gateway"
	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type classifiedGate struct {
	outcome   circuit.Outcome
	completed bool
}

func (g *classifiedGate) Acquire(context.Context, string) (circuit.Permit, error) { return g, nil }
func (g *classifiedGate) Complete(_ context.Context, o circuit.Outcome) error {
	g.outcome = o
	g.completed = true
	return nil
}

type protocolLedger struct{ reserved, uncertain int }

func (l *protocolLedger) Reserve(context.Context, budget.Quote) (bool, error) {
	l.reserved++
	return true, nil
}
func (l *protocolLedger) Dispatch(context.Context, string) error      { return nil }
func (l *protocolLedger) MarkUncertain(context.Context, string) error { l.uncertain++; return nil }
func (l *protocolLedger) Settle(context.Context, string, int64) error { return nil }

type protocolRecorder struct{}

func (protocolRecorder) Begin(context.Context, execution.Start) error           { return nil }
func (protocolRecorder) Finish(context.Context, string, execution.Result) error { return nil }
func TestTransportTimeoutAndClientCancellationClassifyWithoutReplay(t *testing.T) {
	if circuit.ProbeLease <= admission.MaxRequestDuration+12*time.Second {
		t.Fatal("probe lease must cover handler and detached cleanup")
	}
	for _, kind := range []string{"timeout", "client_canceled"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			client := &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
				calls++
				if kind == "client_canceled" {
					cancel()
					return nil, context.Canceled
				}
				return nil, context.DeadlineExceeded
			})}
			catalog, err := provider.New(testutil.MainlandConfig(), func(string) string { return "synthetic-key" }, client)
			if err != nil {
				t.Fatal(err)
			}
			gate := &classifiedGate{}
			ledger := &protocolLedger{}
			h := &gateway.Handler{Admission: testutil.PassAdmission{}, Circuits: gate, Providers: catalog, Ledger: ledger, Attempts: protocolRecorder{}, Authorize: func(context.Context, string, string) (budget.Scope, error) {
				return budget.Scope{Organization: "org", Project: "project", Period: "2026-11", Currency: "CNY_MICRO"}, nil
			}}
			r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"aliyun/fixture","max_tokens":8,"messages":[{"role":"user","content":"synthetic"}]}`)).WithContext(ctx)
			r.Header.Set("Authorization", "Bearer synthetic-project-key")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			want := circuit.Failure
			if kind == "client_canceled" {
				want = circuit.Neutral
			}
			if w.Code != 502 || calls != 1 || ledger.reserved != 1 || ledger.uncertain != 1 || !gate.completed || gate.outcome != want {
				t.Fatal("wrong fault classification, budget mutation or replay")
			}
		})
	}
}
