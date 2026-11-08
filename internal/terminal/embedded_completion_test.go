package terminal

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	types "github.com/jhzhfree/ai-governance-gateway/api/clienttypes"
	"github.com/jhzhfree/ai-governance-gateway/internal/budget"
	"github.com/jhzhfree/ai-governance-gateway/internal/control"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
)

func TestTerminalCompletionEvidencePreservesClientOutcome(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	catalog, _ := testutil.MainlandCatalog(t, testutil.SupplierFixture(t))
	store := control.NewWithCatalog(f.pool, catalog)
	actor := identity.Principal{Issuer: f.issuer.URL, Subject: "alice"}
	org, err := store.CreateOrganizationWithCurrency(ctx, actor, "terminal evidence", "2026-11", provider.Currency, 10000)
	if err != nil {
		t.Fatal(err)
	}
	project, err := store.CreateProject(ctx, actor, org.ID, "receipt", 5000)
	if err != nil {
		t.Fatal(err)
	}
	id := "terminal-evidence-receipted"
	testutil.SeedEmbeddedCompletion(t, f.pool, budget.Scope{Organization: org.ID, Project: project.ID, Period: org.Period, Currency: org.Currency}, id, "receipted")
	cfg := f.config
	cfg.Organization, cfg.Project = org.ID, project.ID
	if err := f.state.Save(cfg); err != nil {
		t.Fatal(err)
	}
	code, out, _ := f.execute(t, f.token, "requests", "get", "--request-id", id, "--output", "json")
	var record types.RequestRecord
	if code != 0 || json.Unmarshal([]byte(out), &record) != nil || record.EmbeddedCompletion == nil || record.EmbeddedCompletion.State != types.Receipted || record.Attempt == nil || record.Attempt.Outcome != types.ExecutionAttemptOutcomeUnfinished {
		t.Fatal("CLI lost independent completion evidence")
	}
	code, out, _ = f.execute(t, f.token, "requests", "get", "--request-id", id)
	if code != 0 || !strings.Contains(out, "已生成结算回执") || !strings.Contains(out, "未结束") || !strings.Contains(out, "14 CNY_MICRO") {
		t.Fatal("CLI table conflated posting and delivery")
	}
	client, err := NewClient(cfg.Server, f.token, true, f.server.Client())
	if err != nil {
		t.Fatal(err)
	}
	model := NewModel(ctx, client, cfg, nil, true)
	defer model.stop()
	model = complete(t, model, model.Init())
	model, cmd := update(t, model, keyPress('t'))
	model = complete(t, model, cmd)
	model, cmd = update(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	model = complete(t, model, cmd)
	if model.record == nil || model.record.EmbeddedCompletion == nil || model.record.EmbeddedCompletion.State != types.Receipted || model.record.Attempt.Outcome != types.ExecutionAttemptOutcomeUnfinished {
		t.Fatal("TUI differs from the scoped request API")
	}
	var n int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM budget_outbox WHERE request_id=$1 AND event_type='settled'`, id).Scan(&n); err != nil || n != 1 {
		t.Fatal("read-only terminal duplicated settlement")
	}
}
