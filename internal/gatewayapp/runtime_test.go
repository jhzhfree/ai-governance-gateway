package gatewayapp

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jhzhfree/ai-governance-gateway/internal/budget"
	"github.com/jhzhfree/ai-governance-gateway/internal/control"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
)

type runtimeFixture struct {
	*provider.Catalog
	ready            atomic.Bool
	opened, closed   atomic.Int32
	started, release chan struct{}
}

func (f *runtimeFixture) Ready() bool                                           { return f.ready.Load() && f.Catalog.Ready() }
func (f *runtimeFixture) ModelsIn(context.Context, pgx.Tx) []provider.ModelInfo { return f.Models() }
func (f *runtimeFixture) Validate(r *provider.Route, req provider.Request) error {
	if !r.Ready() || !provider.ValidateRequest(req) {
		return errors.New("invalid fixture request")
	}
	return nil
}
func (f *runtimeFixture) Open(ctx context.Context, _ *provider.Route, _ provider.Request, _, _ string) (*http.Response, error) {
	f.opened.Add(1)
	close(f.started)
	select {
	case <-f.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"choices":[{"index":0,"message":{"role":"assistant","content":"synthetic"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`))}, nil
}
func (f *runtimeFixture) Close() { f.closed.Add(1) }
func appConfig(t *testing.T, dsn string, l net.Listener) config {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("synthetic application fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"AIGW_CONTENT_AUDIT_KEYS", "AIGW_CONTENT_AUDIT_ACTIVE_KEY", "AIGW_METRICS_TOKEN"} {
		t.Setenv(name, "")
	}
	return config{database: dsn, listen: l.Addr().String(), authMode: "local", browserOrigin: "http://" + l.Addr().String(), allowLoopbackHTTP: true, webDir: dir, executionEngine: "bifrost", engineLimits: EngineConfig{4, 2}}
}
func fixtureDSN(t *testing.T, original, searchPath string) string {
	t.Helper()
	u, err := url.Parse(original)
	if err != nil {
		t.Fatal("invalid fixture DSN")
	}
	q := u.Query()
	q.Set("search_path", searchPath)
	u.RawQuery = q.Encode()
	return u.String()
}
func TestApplicationPairsEngineReadinessAndGracefulShutdown(t *testing.T) {
	db, _, _ := testutil.Database(t)
	dsn := fixtureDSN(t, os.Getenv("TEST_DATABASE_URL"), db.Config().ConnConfig.RuntimeParams["search_path"])
	catalog, _ := testutil.MainlandCatalog(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("native fallback dispatched") }))
	engine := &runtimeFixture{Catalog: catalog, started: make(chan struct{}), release: make(chan struct{})}
	engine.ready.Store(true)
	store := control.NewWithCatalog(db, engine)
	actor := identity.Principal{Issuer: "synthetic", Subject: "owner"}
	ctx := context.Background()
	org, err := store.CreateOrganizationWithCurrency(ctx, actor, "runtime", "2026-11", provider.Currency, 10000)
	if err != nil {
		t.Fatal(err)
	}
	project, err := store.CreateProject(ctx, actor, org.ID, "runtime", 5000)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetModels(ctx, actor, org.ID, project.ID, []string{"aliyun/fixture"}); err != nil {
		t.Fatal(err)
	}
	key, err := store.CreateKey(ctx, actor, org.ID, project.ID, "runtime", []string{"aliyun/fixture"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	c := appConfig(t, dsn, listener)
	life, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	resourceCreated := make(chan context.Context, 1)
	go func() {
		done <- run(life, c, func(resource context.Context, _ *provider.Manager, limits EngineConfig) (RuntimeEngine, error) {
			resourceCreated <- resource
			if limits != c.engineLimits {
				t.Error("pool limits lost")
			}
			return engine, nil
		}, listener)
	}()
	client := &http.Client{Timeout: 3 * time.Second}
	base := "http://" + listener.Addr().String()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		response, e := client.Get(base + "/health/live")
		if e == nil {
			response.Body.Close()
			if response.StatusCode == 204 {
				break
			}
		}
		select {
		case err := <-done:
			t.Fatalf("application exited: %v", err)
		case <-deadline.C:
			t.Fatal("listener did not start")
		case <-tick.C:
		}
	}
	check := func(path string, want int) {
		t.Helper()
		response, e := client.Get(base + path)
		if e != nil {
			t.Fatal(e)
		}
		response.Body.Close()
		if response.StatusCode != want {
			t.Fatalf("%s status %d", path, response.StatusCode)
		}
	}
	check("/health/ready", 204)
	engine.ready.Store(false)
	check("/health/ready", 503)
	check("/health/live", 204)
	engine.ready.Store(true)
	request, _ := http.NewRequest("POST", base+"/v1/chat/completions", strings.NewReader(`{"model":"aliyun/fixture","max_tokens":8,"messages":[{"role":"user","content":"synthetic"}]}`))
	request.Header.Set("Authorization", "Bearer "+key.Secret)
	completed := make(chan *http.Response, 1)
	failed := make(chan error, 1)
	go func() {
		r, e := client.Do(request)
		if e != nil {
			failed <- e
			return
		}
		completed <- r
	}()
	select {
	case <-engine.started:
	case err := <-failed:
		t.Fatal(err)
	case <-time.After(3 * time.Second):
		t.Fatal("governed request did not dispatch")
	}
	resource := <-resourceCreated
	cancel() // Signal stops new admission but must not cancel this in-flight call.
	if resource.Err() != nil {
		t.Fatal("signal prematurely canceled inference resources")
	}
	close(engine.release)
	var response *http.Response
	select {
	case response = <-completed:
	case err := <-failed:
		t.Fatal(err)
	case <-time.After(3 * time.Second):
		t.Fatal("request did not drain")
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("drain failed: %d", response.StatusCode)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("application did not stop")
	}
	if resource.Err() == nil {
		t.Fatal("shutdown left inference resource context alive")
	}
	if engine.opened.Load() != 1 || engine.closed.Load() != 1 {
		t.Fatal("engine not selected or reclaimed exactly once")
	}
	reservation, err := budget.New(db).Get(ctx, response.Header.Get("X-Request-ID"))
	if err != nil || reservation.State != "settled" || reservation.Actual == nil || *reservation.Actual != 14 {
		t.Fatal("runtime lost governed settlement")
	}
}
func TestApplicationClosesEngineOnLaterInitializationFailure(t *testing.T) {
	db, _, _ := testutil.Database(t)
	dsn := fixtureDSN(t, os.Getenv("TEST_DATABASE_URL"), db.Config().ConnConfig.RuntimeParams["search_path"])
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	c := appConfig(t, dsn, listener)
	c.browserOrigin = "http://not-a-loopback.example"
	engine := &runtimeFixture{}
	err = run(context.Background(), c, func(context.Context, *provider.Manager, EngineConfig) (RuntimeEngine, error) { return engine, nil }, listener)
	if err == nil || engine.closed.Load() != 1 {
		t.Fatal("startup failure leaked engine")
	}
}
func TestEngineSelectionFailsBeforeDatabaseAccess(t *testing.T) {
	c := config{database: "invalid", executionEngine: "bifrost"}
	if err := run(context.Background(), c, nil, nil); err == nil || !strings.Contains(err.Error(), "unavailable in this binary") {
		t.Fatal("native binary silently selected fallback")
	}
	valid := config{database: "unused", listen: "127.0.0.1:8080", authMode: "local", browserOrigin: "https://gateway.example", executionEngine: "bifrost", engineLimits: EngineConfig{32, 32}}
	for _, change := range []func(*config){func(c *config) { c.executionEngine = "typo" }, func(c *config) { c.mockOnly = true }, func(c *config) { c.providerConfig = "example.json" }, func(c *config) { c.engineLimits.MaxBindings = 0 }, func(c *config) { c.engineLimits.MaxConcurrent = 1025 }} {
		c := valid
		change(&c)
		if c.validate() == nil {
			t.Fatal("invalid engine configuration accepted")
		}
	}
}
