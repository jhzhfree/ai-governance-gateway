package gateway_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jhzhfree/ai-governance-gateway/internal/budget"
	"github.com/jhzhfree/ai-governance-gateway/internal/gateway"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
)

type deniedLedger struct {
	t             *testing.T
	reserveCalled bool
}

func (l *deniedLedger) Reserve(_ context.Context, q budget.Quote) (bool, error) {
	l.reserveCalled = true
	if q.Currency != "CNY_MICRO" || q.Amount != 288 {
		l.t.Error("supplier quote invalid")
	}
	return false, budget.ErrExceeded
}
func (l *deniedLedger) Dispatch(context.Context, string) error {
	l.t.Error("dispatch after budget denial")
	return errors.New("unexpected dispatch")
}
func (l *deniedLedger) MarkUncertain(context.Context, string) error {
	l.t.Error("cleanup without reservation")
	return nil
}
func (l *deniedLedger) Settle(context.Context, string, int64) error {
	l.t.Error("settlement without reservation")
	return nil
}
func TestMainlandBudgetAndCurrencyRejectBeforeDispatch(t *testing.T) {
	catalog, _ := testutil.MainlandCatalog(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("supplier contacted after local denial") }))
	for _, currency := range []string{"MOCK", "CNY_MICRO"} {
		t.Run(currency, func(t *testing.T) {
			ledger := &deniedLedger{t: t}
			handler := &gateway.Handler{Circuits: testutil.PassCircuit{}, Admission: testutil.PassAdmission{}, Providers: catalog, Ledger: ledger, Authorize: func(context.Context, string, string) (budget.Scope, error) {
				return budget.Scope{Currency: currency}, nil
			}}
			server := httptest.NewServer(handler)
			defer server.Close()
			req, _ := http.NewRequest("POST", server.URL+"/v1/chat/completions", strings.NewReader(`{"model":"zhipu/fixture","max_tokens":8,"messages":[{"role":"user","content":"synthetic"}]}`))
			req.Header.Set("Authorization", "Bearer synthetic-project-key")
			response, err := server.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, response.Body)
			response.Body.Close()
			want := 429
			if currency == "MOCK" {
				want = 403
			}
			if response.StatusCode != want || ledger.reserveCalled != (currency == "CNY_MICRO") {
				t.Fatal("wrong-currency or budget denial dispatched upstream")
			}
		})
	}
}
