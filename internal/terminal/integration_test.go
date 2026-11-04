package terminal

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	types "github.com/jhzhfree/ai-governance-gateway/api/clienttypes"
	"github.com/jhzhfree/ai-governance-gateway/internal/budget"
	"github.com/jhzhfree/ai-governance-gateway/internal/control"
	"github.com/jhzhfree/ai-governance-gateway/internal/execution"
	"github.com/jhzhfree/ai-governance-gateway/internal/httpapi"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
)

type apiFixture struct {
	server   *httptest.Server
	issuer   *testutil.Issuer
	config   Config
	state    State
	org      control.Organization
	project  control.Project
	store    *control.Store
	ledger   *budget.Store
	recorder *execution.Store
	token    string
}

func fixture(t *testing.T) apiFixture {
	t.Helper()
	pool, ledger, _ := testutil.Database(t)
	store := control.New(pool)
	issuer := testutil.NewIssuer(t)
	auth, err := identity.NewOIDC(context.Background(), issuer.URL, "aigw-management")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(httpapi.New(store, auth))
	t.Cleanup(server.Close)
	user := identity.Principal{Issuer: issuer.URL, Subject: "alice"}
	org, err := store.CreateOrganization(context.Background(), user, "CLI team", "2026-11", 100)
	if err != nil {
		t.Fatal(err)
	}
	project, err := store.CreateProject(context.Background(), user, org.ID, "Terminal app", 50)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateKey(context.Background(), user, org.ID, project.ID, "Metadata key", []string{"mock-text"}, time.Hour); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Version: 1, Server: server.URL, Issuer: issuer.URL, ClientID: "native-client", Audience: "aigw-management", AllowLoopbackHTTP: true, CredentialStore: "file"}
	state := localState(t)
	if err := state.Save(cfg); err != nil {
		t.Fatal(err)
	}
	return apiFixture{server, issuer, cfg, state, org, project, store, ledger, execution.New(pool), issuer.Token(t, "alice", nil)}
}
func (f apiFixture) execute(t *testing.T, token string, args ...string) (int, string, string) {
	t.Helper()
	var out, stderr bytes.Buffer
	options := Options{In: strings.NewReader(""), Out: &out, Err: &stderr, Getenv: func(k string) string {
		if k == "AIGW_TOKEN" {
			return token
		}
		return ""
	}, Keyring: f.state.Keyring, HTTPClient: f.server.Client()}
	args = append([]string{"--config", f.state.Path}, args...)
	code := Execute(context.Background(), options, args)
	if token != "" && (strings.Contains(out.String(), token) || strings.Contains(stderr.String(), token)) {
		t.Fatal("CLI output exposed bearer token")
	}
	return code, out.String(), stderr.String()
}
func TestCLIQueriesContextAndJSONContract(t *testing.T) {
	f := fixture(t)
	code, out, _ := f.execute(t, f.token, "organizations", "list", "--output", "json", "--limit", "1")
	if code != 0 {
		t.Fatal("organization query failed")
	}
	decoder := json.NewDecoder(strings.NewReader(out))
	var organizations types.OrganizationPage
	if err := decoder.Decode(&organizations); err != nil || len(organizations.Data) != 1 || organizations.Data[0].Id != f.org.ID {
		t.Fatal("CLI JSON differs from management contract")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		t.Fatal("stdout contains extra text")
	}
	code, _, _ = f.execute(t, f.token, "context", "use", "--org", f.org.ID, "--project", f.project.ID, "--output", "json")
	if code != 0 {
		t.Fatal("context selection failed")
	}
	saved, _, err := f.state.Load()
	if err != nil || saved.Organization != f.org.ID || saved.Project != f.project.ID {
		t.Fatal("context not persisted")
	}
	code, out, _ = f.execute(t, f.token, "usage", "--output", "json")
	if code != 0 {
		t.Fatal("usage failed")
	}
	var usage types.Usage
	if json.Unmarshal([]byte(out), &usage) != nil || usage.LimitAmount != 50 || usage.Currency != "MOCK" {
		t.Fatal("usage contract mismatch")
	}
	code, out, _ = f.execute(t, f.token, "keys", "list", "--output", "json")
	if code != 0 {
		t.Fatal("keys failed")
	}
	var keys types.KeyPage
	if json.Unmarshal([]byte(out), &keys) != nil || len(keys.Data) != 1 || strings.Contains(out, `"secret"`) {
		t.Fatal("key metadata invalid")
	}
	code, out, _ = f.execute(t, f.token, "projects", "list", "--output", "json")
	if code != 0 {
		t.Fatal("projects failed")
	}
	var projects types.ProjectPage
	if json.Unmarshal([]byte(out), &projects) != nil || len(projects.Data) != 1 {
		t.Fatal("projects contract mismatch")
	}
	code, out, _ = f.execute(t, f.token, "audit", "--output", "json")
	if code != 0 {
		t.Fatal("audit failed")
	}
	var audit types.AuditPage
	if json.Unmarshal([]byte(out), &audit) != nil || len(audit.Data) != 3 {
		t.Fatal("audit contract mismatch")
	}
}
func TestCLIRefusesScopeEscalationAndCredentialForwarding(t *testing.T) {
	f := fixture(t)
	otherOwner := identity.Principal{Issuer: f.issuer.URL, Subject: "bob"}
	other, err := f.store.CreateOrganization(context.Background(), otherOwner, "Private organization", "2026-11", 100)
	if err != nil {
		t.Fatal(err)
	}
	if code, _, _ := f.execute(t, f.token, "context", "use", "--org", other.ID); code == 0 {
		t.Fatal("inaccessible context accepted")
	}
	cfg, _, err := f.state.Load()
	if err != nil || cfg.Organization != "" {
		t.Fatal("failed context write changed config")
	}
	credential := testCredential(f.config)
	credential.AccessToken = f.token
	if err := f.state.SaveCredential(f.config, credential); err != nil {
		t.Fatal(err)
	}
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("stored credential sent to different server") }))
	defer destination.Close()
	if code, _, _ := f.execute(t, "", "--server", destination.URL, "organizations", "list"); code != 2 {
		t.Fatalf("server change exit=%d want 2", code)
	}
	if code, out, stderr := f.execute(t, "", "tui"); code != 2 || out != "" || stderr == "" {
		t.Fatal("non-interactive TUI did not reject safely")
	}
	if code, out, stderr := f.execute(t, "", "unknown-command"); code != 2 || out != "" || strings.Contains(stderr, "unknown-command") {
		t.Fatal("invalid command leaked untrusted argument or wrong exit status")
	}
}
func TestDeviceFlowLoginPersistenceAndLogout(t *testing.T) {
	f := fixture(t)
	f.issuer.AutoApproveDevices.Store(true)
	code, out, stderr := f.execute(t, "", "login", "--server", f.server.URL, "--issuer", f.issuer.URL, "--client-id", "native-client", "--credential-store", "file", "--allow-loopback-http", "--timeout", "15s", "--output", "json")
	if code != 0 {
		t.Fatal("device login failed: ", SafeText(stderr))
	}
	if !strings.Contains(stderr, "输入验证码") || !strings.Contains(out, `"configured":true`) {
		t.Fatal("missing device challenge or clean status JSON")
	}
	cfg, _, err := f.state.Load()
	if err != nil {
		t.Fatal(err)
	}
	credential, err := f.state.Credential(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, credential.AccessToken) || strings.Contains(stderr, credential.AccessToken) {
		t.Fatal("login output leaked token")
	}
	if f.issuer.DevicePolls.Load() < 2 {
		t.Fatal("authorization_pending was not exercised")
	}
	if code, _, _ := f.execute(t, "", "organizations", "list"); code != 0 {
		t.Fatal("persisted login unusable")
	}
	if code, out, _ := f.execute(t, "", "auth", "status", "--output", "json"); code != 0 || strings.Contains(out, credential.AccessToken) {
		t.Fatal("unsafe local auth status")
	}
	if code, _, _ := f.execute(t, "", "logout"); code != 0 {
		t.Fatal("logout failed")
	}
	if code, out, _ := f.execute(t, "", "organizations", "list"); code != 3 || out != "" {
		t.Fatal("logout did not remove credential")
	}
}
func TestDeviceFlowValidationAndCancellation(t *testing.T) {
	for _, fault := range []string{"denied", "wrong_audience", "expired", "negative_interval"} {
		t.Run(fault, func(t *testing.T) {
			f := fixture(t)
			f.issuer.AutoApproveDevices.Store(true)
			f.issuer.SetDeviceFault(fault)
			if code, _, _ := f.execute(t, "", "login", "--server", f.server.URL, "--issuer", f.issuer.URL, "--client-id", "native-client", "--credential-store", "file", "--allow-loopback-http", "--timeout", "10s"); code == 0 {
				t.Fatal("invalid device grant accepted")
			}
			if _, err := f.state.Credential(f.config); err == nil {
				t.Fatal("failed grant persisted credential")
			}
		})
	}
	f := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	_, err := DeviceLogin(ctx, f.config, f.server.Client(), func(challenge Challenge) error {
		response, err := http.PostForm(challenge.VerificationURI, url.Values{"user_code": {challenge.UserCode}})
		if err != nil {
			return err
		}
		response.Body.Close()
		cancel()
		return nil
	})
	if err != context.Canceled {
		t.Fatal("device wait did not respect cancellation")
	}
}

func TestCLIAndTUIReadSameDurableRequestLedger(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	scope := budget.Scope{Organization: f.org.ID, Project: f.project.ID, Period: f.org.Period, Currency: f.org.Currency}
	if _, err := f.ledger.Reserve(ctx, budget.Quote{Scope: scope, RequestID: "terminal-request", PriceVersion: "mock-v1", Amount: 10}); err != nil {
		t.Fatal(err)
	}
	if err := f.ledger.Dispatch(ctx, "terminal-request"); err != nil {
		t.Fatal(err)
	}
	if err := f.ledger.MarkUncertain(ctx, "terminal-request"); err != nil {
		t.Fatal(err)
	}
	cfg := f.config
	cfg.Organization = f.org.ID
	cfg.Project = f.project.ID
	if err := f.state.Save(cfg); err != nil {
		t.Fatal(err)
	}
	code, out, _ := f.execute(t, f.token, "requests", "list", "--state", "uncertain", "--output", "json")
	var page types.RequestPage
	if code != 0 || json.Unmarshal([]byte(out), &page) != nil || len(page.Data) != 1 || page.Data[0].Actual != nil {
		t.Fatal("CLI request list lost unsettled state")
	}
	code, out, _ = f.execute(t, f.token, "requests", "get", "--request-id", "terminal-request", "--output", "json")
	var record types.RequestRecord
	if code != 0 || json.Unmarshal([]byte(out), &record) != nil || record.State != types.RequestRecordStateUncertain || record.Reserved != 10 {
		t.Fatal("CLI detail differs from ledger")
	}
	if code, _, _ := f.execute(t, f.token, "requests", "list", "--state", "success"); code != 2 {
		t.Fatal("invalid state did not return usage error")
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
	if len(model.records) != 1 || model.records[0].RequestId != record.RequestId {
		t.Fatal("TUI list differs from actual API")
	}
	model, cmd = update(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	model = complete(t, model, cmd)
	if model.record == nil || model.record.Actual != nil || model.record.Reserved != record.Reserved {
		t.Fatal("TUI detail differs from actual API")
	}
}

func TestCLIModelCatalogMatchesManagementContract(t *testing.T) {
	f := fixture(t)
	code, out, _ := f.execute(t, f.token, "models", "list", "--output", "json")
	var page types.ModelPage
	if code != 0 || json.Unmarshal([]byte(out), &page) != nil || len(page.Data) != 1 || page.Data[0].Id != "mock-text" || page.Data[0].Currency != "MOCK" {
		t.Fatal("CLI model catalog differs from management API")
	}
	if strings.Contains(out, "api_key") || strings.Contains(out, "evidence_ref") {
		t.Fatal("catalog leaked internal configuration")
	}
}

func TestTerminalExecutionResultsUseSameScopedAPI(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	scope := budget.Scope{Organization: f.org.ID, Project: f.project.ID, Period: f.org.Period, Currency: f.org.Currency}
	if _, err := f.ledger.Reserve(ctx, budget.Quote{Scope: scope, RequestID: "cli-observed", PriceVersion: "mock-v1", Amount: 10}); err != nil {
		t.Fatal(err)
	}
	if err := f.recorder.Begin(ctx, execution.Start{AttemptID: "cli-attempt", RequestID: "cli-observed", Model: "mock-text", Provider: "mock", Region: "local", Stream: true}); err != nil {
		t.Fatal(err)
	}
	if err := f.ledger.Dispatch(ctx, "cli-observed"); err != nil {
		t.Fatal(err)
	}
	if err := f.ledger.MarkUncertain(ctx, "cli-observed"); err != nil {
		t.Fatal(err)
	}
	status := 503
	if err := f.recorder.Finish(ctx, "cli-attempt", execution.Result{Outcome: "failed", ErrorCode: "upstream_rejected", UpstreamStatus: &status}); err != nil {
		t.Fatal(err)
	}
	cfg := f.config
	cfg.Organization = f.org.ID
	cfg.Project = f.project.ID
	if err := f.state.Save(cfg); err != nil {
		t.Fatal(err)
	}
	code, out, _ := f.execute(t, f.token, "requests", "list", "--outcome", "failed", "--output", "json")
	var page types.RequestPage
	if code != 0 || json.Unmarshal([]byte(out), &page) != nil || len(page.Data) != 1 || page.Data[0].Attempt == nil || page.Data[0].Attempt.ErrorCode != "upstream_rejected" {
		t.Fatal("CLI failed-result filter/metadata mismatch")
	}
	code, out, _ = f.execute(t, f.token, "requests", "get", "--request-id", "cli-observed")
	if code != 0 || !strings.Contains(out, "upstream_rejected") || !strings.Contains(out, "执行失败") || !strings.Contains(out, "未采集") {
		t.Fatal("CLI detail fabricated missing token counts or lost error metadata")
	}
	if code, _, _ := f.execute(t, f.token, "requests", "list", "--outcome", "success"); code != 2 {
		t.Fatal("invalid outcome not rejected locally")
	}
	client, err := NewClient(cfg.Server, f.token, true, f.server.Client())
	if err != nil {
		t.Fatal(err)
	}
	m := NewModel(ctx, client, cfg, nil, true)
	defer m.stop()
	m = complete(t, m, m.Init())
	m, cmd := update(t, m, keyPress('t'))
	m = complete(t, m, cmd)
	m, cmd = update(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = complete(t, m, cmd)
	if m.record == nil || m.record.Attempt == nil || !strings.Contains(m.View().Content, "upstream_rejected") {
		t.Fatal("TUI did not render the same execution details")
	}

}

func TestOperationsCLIUsesScopedManagementAPI(t *testing.T) {
	f := fixture(t)
	code, out, _ := f.execute(t, f.token, "limits", "show", "--org", f.org.ID, "--project", f.project.ID, "--output", "json")
	var limits types.ProjectLimits
	if code != 0 || json.Unmarshal([]byte(out), &limits) != nil || limits.MaxConcurrent != 4 || limits.RequestsPerMinute != 60 {
		t.Fatal("CLI limits differ from API")
	}
	code, out, _ = f.execute(t, f.token, "recovery", "inspect", "--org", f.org.ID, "--project", f.project.ID, "--output", "json")
	var report types.Inspection
	if code != 0 || json.Unmarshal([]byte(out), &report) != nil || len(report.Accounts) != 2 || len(report.Data) != 0 {
		t.Fatal("CLI inspection differs from API")
	}
	code, _, _ = f.execute(t, f.token, "recovery", "inspect", "--org", f.org.ID, "--project", f.project.ID, "--older-than", "500ms")
	if code != 2 {
		t.Fatal("invalid threshold accepted")
	}
	code, _, _ = f.execute(t, f.issuer.Token(t, "outsider", nil), "recovery", "inspect", "--org", f.org.ID, "--project", f.project.ID)
	if code == 0 {
		t.Fatal("inspection exposed another tenant")
	}
}

func TestRuntimeCLIAndTUIShareScopedSnapshot(t *testing.T) {
	f := fixture(t)
	code, out, _ := f.execute(t, f.token, "runtime", "show", "--org", f.org.ID, "--project", f.project.ID, "--output", "json")
	var v types.Runtime
	if code != 0 || json.Unmarshal([]byte(out), &v) != nil || v.Instance != nil || v.HeldAmount != "0" || len(v.Models) != 1 || v.Models[0].State != "not_applicable" {
		t.Fatal("runtime CLI contract mismatch")
	}
	client, err := NewClient(f.server.URL, f.token, true, f.server.Client())
	if err != nil {
		t.Fatal(err)
	}
	cfg := f.config
	cfg.Organization = f.org.ID
	cfg.Project = f.project.ID
	m := NewModel(context.Background(), client, cfg, nil, true)
	defer m.stop()
	m = complete(t, m, m.Init())
	next, cmd := m.Update(keyPress('s'))
	m = complete(t, next.(Model), cmd)
	if m.screen != runtimeScreen || !strings.Contains(m.View().Content, "模拟供应商") {
		t.Fatal("runtime TUI did not load authorized snapshot")
	}
	next, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = complete(t, next.(Model), cmd)
	if m.screen != dashboardScreen {
		t.Fatal("runtime TUI did not return to project")
	}
	code, _, _ = f.execute(t, f.issuer.Token(t, "outsider", nil), "runtime", "show", "--org", f.org.ID, "--project", f.project.ID)
	if code == 0 {
		t.Fatal("runtime CLI exposed another tenant")
	}
}

func TestContentAuditMetadataCLIStaysReadOnlyAndScoped(t *testing.T) {
	f := fixture(t)
	code, out, _ := f.execute(t, f.token, "content-audit", "list", "--org", f.org.ID, "--project", f.project.ID, "--output", "json")
	var page types.ContentPage
	if code != 0 || json.Unmarshal([]byte(out), &page) != nil || len(page.Data) != 0 {
		t.Fatal("content metadata CLI contract failed")
	}
	code, _, _ = f.execute(t, f.issuer.Token(t, "outsider", nil), "content-audit", "list", "--org", f.org.ID, "--project", f.project.ID)
	if code == 0 {
		t.Fatal("content metadata leaked across tenant")
	}
}

func TestCostReportCLIAndTUIReadLedgerWithoutMutation(t *testing.T) {
	f := fixture(t)
	now := time.Now().UTC()
	from := now.Format("2006-01-02")
	to := now.AddDate(0, 0, 1).Format("2006-01-02")
	scope := budget.Scope{Organization: f.org.ID, Project: f.project.ID, Period: f.org.Period, Currency: f.org.Currency}
	if _, err := f.ledger.Reserve(context.Background(), budget.Quote{Scope: scope, RequestID: "terminal-cost", PriceVersion: "synthetic", Amount: 10}); err != nil {
		t.Fatal(err)
	}
	code, out, _ := f.execute(t, f.token, "cost-report", "--org", f.org.ID, "--project", f.project.ID, "--from", from, "--to", to, "--output", "json")
	var report types.CostReport
	if code != 0 || json.Unmarshal([]byte(out), &report) != nil || len(report.Rows) != 1 || report.Rows[0].OpenReserved != "10" || report.Rows[0].Model != nil || report.Rows[0].SettledAmount != "0" {
		t.Fatal("CLI report differs from ledger")
	}
	code, _, _ = f.execute(t, f.token, "cost-report", "--org", f.org.ID, "--organization", "--from", from, "--to", to)
	if code != 0 {
		t.Fatal("owner organization report denied")
	}
	code, _, _ = f.execute(t, f.issuer.Token(t, "outsider", nil), "cost-report", "--org", f.org.ID, "--project", f.project.ID, "--from", from, "--to", to)
	if code == 0 {
		t.Fatal("cross-tenant cost exposed")
	}
	code, _, _ = f.execute(t, f.token, "cost-report", "--from", from, "--to", from)
	if code != 2 {
		t.Fatal("invalid date not rejected before request")
	}
	client, err := NewClient(f.server.URL, f.token, true, f.server.Client())
	if err != nil {
		t.Fatal(err)
	}
	cfg := f.config
	cfg.Organization = f.org.ID
	cfg.Project = f.project.ID
	m := NewModel(context.Background(), client, cfg, nil, true)
	defer m.stop()
	m = complete(t, m, m.Init())
	next, cmd := m.Update(keyPress('c'))
	m = complete(t, next.(Model), cmd)
	if m.screen != costReportScreen || !strings.Contains(m.View().Content, "成本报表") || m.costReport == nil || len(m.costReport.Rows) != 1 || m.costReport.Rows[0].OpenReserved != "10" {
		t.Fatal("TUI cost report missing")
	}
	next, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = complete(t, next.(Model), cmd)
	if m.screen != dashboardScreen || m.costReport != nil {
		t.Fatal("TUI retained stale scope")
	}
	reservation, err := f.ledger.Get(context.Background(), "terminal-cost")
	if err != nil || reservation.State != "reserved" {
		t.Fatal("report mutated ledger")
	}
}

func TestBudgetAlertsCLIAndTUIStayScopedAndReadOnly(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	actor := identity.Principal{Issuer: f.issuer.URL, Subject: "alice"}
	if err := f.store.SetAlertPolicy(ctx, actor, f.org.ID, f.project.ID, control.AlertPolicy{Enabled: true, Thresholds: []int{80}}, "terminal fixture"); err != nil {
		t.Fatal(err)
	}
	scope := budget.Scope{Organization: f.org.ID, Project: f.project.ID, Period: f.org.Period, Currency: f.org.Currency}
	if _, err := f.ledger.Reserve(ctx, budget.Quote{Scope: scope, RequestID: "terminal-alert", PriceVersion: "synthetic", Amount: 40}); err != nil {
		t.Fatal(err)
	}
	code, out, _ := f.execute(t, f.token, "alerts", "--org", f.org.ID, "--project", f.project.ID, "--output", "json")
	var page types.BudgetAlertPage
	if code != 0 || json.Unmarshal([]byte(out), &page) != nil || len(page.Data) != 1 || page.Data[0].Held != "40" || page.Data[0].AcknowledgedAt != nil {
		t.Fatal("CLI did not read scoped alert")
	}
	code, _, _ = f.execute(t, f.issuer.Token(t, "outsider", nil), "alerts", "--org", f.org.ID, "--project", f.project.ID)
	if code == 0 {
		t.Fatal("CLI exposed alert across tenants")
	}
	client, err := NewClient(f.server.URL, f.token, true, f.server.Client())
	if err != nil {
		t.Fatal(err)
	}
	cfg := f.config
	cfg.Organization = f.org.ID
	cfg.Project = f.project.ID
	m := NewModel(ctx, client, cfg, nil, true)
	defer m.stop()
	m = complete(t, m, m.Init())
	next, cmd := m.Update(keyPress('a'))
	m = complete(t, next.(Model), cmd)
	if m.screen != budgetAlertsScreen || len(m.alerts) != 1 || m.alerts[0].Threshold != 80 || !strings.Contains(m.View().Content, "预算告警") {
		t.Fatal("TUI alerts not loaded")
	}
	next, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = complete(t, next.(Model), cmd)
	if m.screen != dashboardScreen || m.alerts != nil {
		t.Fatal("TUI alert state not cleared")
	}
	r, err := f.ledger.Get(ctx, "terminal-alert")
	if err != nil || r.State != "reserved" {
		t.Fatal("read-only CLI changed budget")
	}
}
