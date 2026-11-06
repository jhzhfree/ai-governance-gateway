package httpapi_test

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jhzhfree/ai-governance-gateway/internal/admission"
	"github.com/jhzhfree/ai-governance-gateway/internal/budget"
	"github.com/jhzhfree/ai-governance-gateway/internal/circuit"
	"github.com/jhzhfree/ai-governance-gateway/internal/control"
	"github.com/jhzhfree/ai-governance-gateway/internal/execution"
	"github.com/jhzhfree/ai-governance-gateway/internal/gateway"
	"github.com/jhzhfree/ai-governance-gateway/internal/httpapi"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
	"github.com/jhzhfree/ai-governance-gateway/internal/keyword"
	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
)

func managedSetup(t *testing.T, suppliers ...http.Handler) (fixture, *provider.Manager) {
	f := providerSetup(t, testutil.SupplierFixture(t))
	supplier := testutil.SupplierFixture(t)
	if len(suppliers) > 0 {
		supplier = suppliers[0]
	}
	manager := testutil.MainlandManager(t, f.pool, identity.Principal{Issuer: f.issuer.URL, Subject: "alice"}, supplier)
	*f.store = *control.NewWithCatalog(f.pool, manager).WithProviderManager(manager)
	auth, err := identity.NewOIDC(context.Background(), f.issuer.URL, "aigw-management")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/api/v1/", httpapi.New(f.store, auth))
	mux.Handle("/v1/", &gateway.Handler{DeviceProof: f.store.VerifyDeviceRequest, Keywords: keyword.New(f.pool), Providers: manager, Ledger: budget.New(f.pool), Admission: admission.New(f.pool), Circuits: circuit.New(f.pool), Attempts: execution.New(f.pool), Authorize: f.store.ResolveKey, ListModels: f.store.KeyModels})
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f, manager
}
func TestPublishedProviderConfigurationDrivesRealProtocol(t *testing.T) {
	f, _ := managedSetup(t)
	initial := parse[provider.ConfigurationStatus](t, f.request(f.token("alice"), "GET", "/api/v1/providers", nil, 200))
	if initial.Ready || initial.Revision != 0 || !initial.CanManage || len(initial.Models) != 0 {
		t.Fatal("empty deployment fabricated models")
	}
	cfg := testutil.MainlandConfig()
	body := map[string]any{"expected_revision": 0, "configuration": cfg}
	f.request("", "PUT", "/api/v1/providers", body, 401)
	f.request(f.token("bob"), "PUT", "/api/v1/providers", body, 403)
	f.request(f.token("alice"), "PUT", "/api/v1/providers", body, 200)
	o, p, key := providerProject(f)
	for _, profile := range provider.Profiles() {
		for _, stream := range []bool{false, true} {
			f.request(key.Secret, "POST", "/v1/chat/completions", realPayload(profile.ID+"/fixture", stream), 200)
		}
	}
	// An organization owner is not automatically a platform administrator.
	f.request(f.token("bob"), "POST", "/api/v1/organizations", map[string]any{"name": "Other owner", "period": "2026-11", "currency": "CNY_MICRO", "limit_amount": 1000}, 201)
	f.request(f.token("bob"), "PUT", "/api/v1/providers", map[string]any{"expected_revision": 1, "configuration": cfg}, 403)
	data := f.request(f.token("bob"), "GET", "/api/v1/providers", nil, 200)
	if strings.Contains(string(data), "evidence_ref") || strings.Contains(string(data), "synthetic-AIGW") {
		t.Fatal("private config or credential exposed")
	}
	f.request(f.token("alice"), "PUT", "/api/v1/providers", body, 409)
	cfg.Models = cfg.Models[:1]
	f.request(f.token("alice"), "PUT", "/api/v1/providers", map[string]any{"expected_revision": 1, "configuration": cfg}, 200)
	models := string(f.request(key.Secret, "GET", "/v1/models", nil, 200))
	if strings.Contains(models, "volcengine/") {
		t.Fatal("removed route still advertised")
	}
	f.request(key.Secret, "POST", "/v1/chat/completions", realPayload("volcengine/fixture", false), 400)
	f.request(key.Secret, "POST", "/v1/chat/completions", realPayload("aliyun/fixture", false), 200)
	usage := parse[control.Usage](t, f.request(f.token("alice"), "GET", projectPath(o, p)+"/usage", nil, 200))
	if usage.Spent <= 0 {
		t.Fatal("real protocol was not accounted")
	}
}
func TestProviderPublicationIsCASAndAtomicWithAudit(t *testing.T) {
	f, m := managedSetup(t)
	ctx := context.Background()
	admin := identity.Principal{Issuer: f.issuer.URL, Subject: "alice"}
	cfg := testutil.MainlandConfig()
	var wg sync.WaitGroup
	out := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); out <- m.Publish(ctx, admin, 0, cfg) }()
	}
	wg.Wait()
	close(out)
	success, conflicts := 0, 0
	for err := range out {
		if err == nil {
			success++
		} else if errors.Is(err, provider.ErrConfigurationConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflicts != 1 {
		t.Fatal("publication lost CAS")
	}
	_, err := f.pool.Exec(ctx, `ALTER TABLE provider_configuration_events ADD CONSTRAINT block_publish CHECK(revision<2)`)
	if err != nil {
		t.Fatal(err)
	}
	if m.Publish(ctx, admin, 1, cfg) == nil {
		t.Fatal("publication succeeded without audit")
	}
	status, err := m.Status(ctx, admin)
	if err != nil || status.Revision != 1 {
		t.Fatal("failed audit changed live config")
	}
	var n int
	if err = f.pool.QueryRow(ctx, `SELECT count(*) FROM provider_configuration_events`).Scan(&n); err != nil || n != 1 {
		t.Fatal("audit count changed")
	}
	cfg.DeploymentResidency.EvidenceRef = ""
	if !errors.Is(m.Publish(ctx, admin, 1, cfg), provider.ErrConfigurationInvalid) {
		t.Fatal("unreviewed residency accepted")
	}
	missing := provider.NewManager(f.pool, func(string) string { return "" }, nil, admin)
	if missing.Ready() || len(missing.Models()) != 0 {
		t.Fatal("missing credential fell back to fake model")
	}
	if _, ok := missing.Find("aliyun/fixture"); ok {
		t.Fatal("missing key dispatched")
	}
	if !errors.Is(missing.Publish(ctx, admin, 1, testutil.MainlandConfig()), provider.ErrConfigurationInvalid) {
		t.Fatal("missing key accepted")
	}
}

func TestManagedCatalogDoesNotAcquireNestedPoolConnection(t *testing.T) {
	f, m := managedSetup(t)
	ctx := context.Background()
	admin := identity.Principal{Issuer: f.issuer.URL, Subject: "alice"}
	if err := m.Publish(ctx, admin, 0, testutil.MainlandConfig()); err != nil {
		t.Fatal(err)
	}
	org, project, _ := providerProject(f)
	cfg := f.pool.Config()
	cfg.MaxConns = 1
	single, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer single.Close()
	manager := provider.NewManager(single, func(name string) string { return "synthetic-" + name }, nil, admin)
	store := control.NewWithCatalog(single, manager)
	limited, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := store.SetModels(limited, admin, org.ID, project.ID, []string{"aliyun/fixture"}); err != nil {
		t.Fatal("authorization acquired a nested pool connection", err)
	}
	tx, err := single.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if len(manager.ModelsIn(limited, tx)) != 4 {
		t.Fatal("transaction-scoped catalog unavailable")
	}
}
