package terminal

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	types "github.com/jhzhfree/ai-governance-gateway/api/clienttypes"
	"github.com/jhzhfree/ai-governance-gateway/internal/control"
	"github.com/jhzhfree/ai-governance-gateway/internal/httpapi"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
)

func TestBillingCLIAndTUIReadSameScopedEvidence(t *testing.T) {
	pool, _, _ := testutil.Database(t)
	ctx := context.Background()
	catalog, _ := testutil.MainlandCatalog(t, testutil.SupplierFixture(t))
	store := control.NewWithCatalog(pool, catalog)
	issuer := testutil.NewIssuer(t)
	auth, err := identity.NewOIDC(ctx, issuer.URL, "aigw-management")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(httpapi.New(store, auth))
	defer server.Close()
	owner := identity.Principal{Issuer: issuer.URL, Subject: "alice"}
	org, err := store.CreateOrganizationWithCurrency(ctx, owner, "Billing team", "2026-11", "CNY_MICRO", 1000)
	if err != nil {
		t.Fatal(err)
	}
	project, err := store.CreateProject(ctx, owner, org.ID, "Billing app", 500)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.ImportStatement(ctx, owner, org.ID, project.ID, control.ImportInput{Provider: "aliyun", AccountRef: "account", EvidenceRef: "final-invoice", CSV: "record_id,request_id,period,currency,amount,input_tokens,output_tokens\ncli-bill,,2026-11,CNY_MICRO,10,0,0\n"})
	if err != nil {
		t.Fatal(err)
	}
	state := localState(t)
	cfg := Config{Version: 1, Server: server.URL, Issuer: issuer.URL, ClientID: "native-client", Audience: "aigw-management", AllowLoopbackHTTP: true, CredentialStore: "file", Organization: org.ID, Project: project.ID}
	if err := state.Save(cfg); err != nil {
		t.Fatal(err)
	}
	f := apiFixture{server: server, issuer: issuer, config: cfg, state: state, token: issuer.Token(t, "alice", nil)}
	code, out, _ := f.execute(t, f.token, "billing", "list", "--output", "json")
	var page types.BillingPage
	if code != 0 || json.Unmarshal([]byte(out), &page) != nil || len(page.Data) != 1 || page.Data[0].RecordId != "cli-bill" {
		t.Fatal("CLI evidence mismatch")
	}
	client, err := NewClient(server.URL, f.token, true, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	m := NewModel(ctx, client, cfg, nil, true)
	defer m.stop()
	m = complete(t, m, m.Init())
	next, cmd := m.Update(keyPress('b'))
	m = complete(t, next.(Model), cmd)
	if m.screen != billingScreen || !strings.Contains(m.View().Content, "cli-bill") {
		t.Fatal("TUI evidence mismatch")
	}
	code, _, _ = f.execute(t, issuer.Token(t, "outsider", nil), "billing", "list")
	if code == 0 {
		t.Fatal("CLI leaked another project statement")
	}
}
