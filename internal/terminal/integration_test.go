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
	"github.com/jhzhfree/ai-governance-gateway/internal/httpapi"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
)

type apiFixture struct {
	server  *httptest.Server
	issuer  *testutil.Issuer
	config  Config
	state   State
	org     control.Organization
	project control.Project
	store   *control.Store
	ledger  *budget.Store
	token   string
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
	return apiFixture{server, issuer, cfg, state, org, project, store, ledger, issuer.Token(t, "alice", nil)}
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
