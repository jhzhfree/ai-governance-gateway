package gatewaybridge

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jhzhfree/ai-governance-gateway/internal/admission"
	"github.com/jhzhfree/ai-governance-gateway/internal/budget"
	"github.com/jhzhfree/ai-governance-gateway/internal/circuit"
	"github.com/jhzhfree/ai-governance-gateway/internal/completion"
	"github.com/jhzhfree/ai-governance-gateway/internal/contentaudit"
	"github.com/jhzhfree/ai-governance-gateway/internal/control"
	"github.com/jhzhfree/ai-governance-gateway/internal/database"
	recorder "github.com/jhzhfree/ai-governance-gateway/internal/execution"
	"github.com/jhzhfree/ai-governance-gateway/internal/gateway"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
	adapter "github.com/jhzhfree/ai-governance-gateway/modules/engine/adapters/bifrost"
	"github.com/jhzhfree/ai-governance-gateway/modules/engine/execution"
)

func clusterAudit(db *pgxpool.Pool) *contentaudit.Service {
	key := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", 32)))
	ring, err := contentaudit.ParseKeys(`{"synthetic":"`+key+`"}`, "synthetic")
	if err != nil {
		panic(err)
	}
	return contentaudit.New(db, ring)
}
func clusterChild(t *testing.T) {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()
	cfg, err := pgxpool.ParseConfig(os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal("invalid test database")
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = os.Getenv("AIGW_CLUSTER_SCHEMA")
	db, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal("child database unavailable")
	}
	defer db.Close()
	if err := database.Check(ctx, db); err != nil {
		t.Fatal(err)
	}
	journal := completion.New(db)
	source := provider.NewManager(db, func(string) string { return "synthetic-cluster-key" }, nil, identity.Principal{})
	engines, err := newPool(ctx, PoolConfig{4, 4}, func(ctx context.Context, r *provider.Route, n int) (ownedBinding, error) {
		b, err := newSupplier(ctx, r, n, func(ctx context.Context, cfg adapter.Config) (execution.Executor, error) {
			cfg.BaseURL = os.Getenv("AIGW_CLUSTER_UPSTREAM")
			cfg.TestLoopback = true
			return adapter.New(ctx, cfg)
		})
		if err != nil {
			return nil, err
		}
		b.binding.journal = journal
		return b, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	managed := newManaged(source, engines)
	defer managed.Close()
	if !managed.Ready() {
		t.Fatal("child engine not ready")
	}
	store := control.NewWithCatalog(db, managed)
	handler := &gateway.Handler{Completions: journal, Providers: managed, Executor: managed, Ledger: budget.New(db), Authorize: store.ResolveKey, Attempts: recorder.New(db), Admission: admission.New(db), Circuits: circuit.New(db), Content: clusterAudit(db), DeviceProof: store.VerifyDeviceRequest}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: time.Second}
	ended := make(chan error, 1)
	go func() { ended <- server.Serve(listener) }()
	recoveryDone := make(chan struct{})
	go func() {
		defer close(recoveryDone)
		tick := time.NewTicker(100 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				_, _ = journal.Recover(ctx, time.Now().Add(-3*time.Minute))
			}
		}
	}()
	fmt.Println("cluster-ready http://" + listener.Addr().String())
	<-ctx.Done()
	_ = server.Close()
	<-ended
	<-recoveryDone
}

type clusterNode struct {
	cmd  *exec.Cmd
	url  string
	once sync.Once
	err  error
}

func (n *clusterNode) stop(kill bool) error {
	n.once.Do(func() {
		if kill {
			_ = n.cmd.Process.Kill()
		} else {
			_ = n.cmd.Process.Signal(syscall.SIGTERM)
		}
		n.err = n.cmd.Wait()
	})
	return n.err
}
func startClusterNode(t *testing.T, ctx context.Context, schema, upstream string) *clusterNode {
	t.Helper()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMultiProcessGovernanceCrashRecovery$", "-test.timeout=60s")
	cmd.Env = append(os.Environ(), "AIGW_CLUSTER_CHILD=1", "AIGW_CLUSTER_SCHEMA="+schema, "AIGW_CLUSTER_UPSTREAM="+upstream)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	node := &clusterNode{cmd: cmd}
	t.Cleanup(func() { node.stop(true) })
	ready := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if strings.HasPrefix(scanner.Text(), "cluster-ready ") {
				ready <- strings.TrimPrefix(scanner.Text(), "cluster-ready ")
				return
			}
		}
		ready <- ""
	}()
	select {
	case node.url = <-ready:
		if node.url == "" {
			t.Fatal("child exited before readiness")
		}
	case <-ctx.Done():
		t.Fatal("child readiness timeout")
	}
	return node
}
func clusterRequest(t *testing.T, ctx context.Context, node *clusterNode, key string, stream bool) *http.Response {
	t.Helper()
	raw := fmt.Sprintf(`{"model":"deepseek/fixture","stream":%t,"max_tokens":32,"messages":[{"role":"user","content":"synthetic"}]}`, stream)
	r, _ := http.NewRequestWithContext(ctx, "POST", node.url+"/v1/chat/completions", strings.NewReader(raw))
	r.Header.Set("Authorization", "Bearer "+key)
	response, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	return response
}
func TestMultiProcessGovernanceCrashRecovery(t *testing.T) {
	if os.Getenv("AIGW_CLUSTER_CHILD") == "1" {
		clusterChild(t)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	db, ledger, _ := testutil.Database(t)
	cfg := testutil.MainlandConfig()
	cfg.Models = cfg.Models[3:]
	cfg.Models[0].MaxOutputTokens = 32
	actor := identity.Principal{Issuer: "synthetic", Subject: "owner"}
	source := provider.NewManager(db, func(string) string { return "synthetic-cluster-key" }, nil, actor)
	if err := source.Publish(ctx, actor, 0, cfg); err != nil {
		t.Fatal(err)
	}
	store := control.NewWithCatalog(db, source)
	org, err := store.CreateOrganizationWithCurrency(ctx, actor, "cluster", "2026-11", provider.Currency, 10000)
	if err != nil {
		t.Fatal(err)
	}
	makeProject := func(name string, limit int64) (control.Project, string) {
		p, e := store.CreateProject(ctx, actor, org.ID, name, limit)
		if e != nil {
			t.Fatal(e)
		}
		if e := store.SetModels(ctx, actor, org.ID, p.ID, []string{"deepseek/fixture"}); e != nil {
			t.Fatal(e)
		}
		k, e := store.CreateKey(ctx, actor, org.ID, p.ID, "fixture", []string{"deepseek/fixture"}, time.Hour)
		if e != nil {
			t.Fatal(e)
		}
		return p, k.Secret
	}
	project, key := makeProject("held", 500)
	healthy, healthyKey := makeProject("healthy", 5000)
	audit := clusterAudit(db)
	if err := pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		if err := admission.Update(ctx, tx, org.ID, project.ID, admission.Policy{RequestsPerMinute: 60, MaxConcurrent: 1}); err != nil {
			return err
		}
		for _, p := range []control.Project{project, healthy} {
			if err := audit.SetPolicy(ctx, tx, org.ID, p.ID, contentaudit.Policy{Enabled: true, RetentionDays: 1, MaxBytes: 1024, ConsentRef: "synthetic"}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer synthetic-cluster-key" {
			t.Error("supplier contract changed")
		}
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `"stream":true`) {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"synthetic\"},\"finish_reason\":null}]}\n\n")
			http.NewResponseController(w).Flush()
			ticker := time.NewTicker(200 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-r.Context().Done():
					return
				case <-ticker.C:
					fmt.Fprint(w, ": keepalive\n\n")
					if http.NewResponseController(w).Flush() != nil {
						return
					}
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"synthetic"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`)
	}))
	// Cleanup is LIFO: stop child processes before waiting for upstream handlers.
	t.Cleanup(upstream.Close)
	a := startClusterNode(t, ctx, db.Config().ConnConfig.RuntimeParams["search_path"], upstream.URL)
	b := startClusterNode(t, ctx, db.Config().ConnConfig.RuntimeParams["search_path"], upstream.URL)
	if a.cmd.Process.Pid == b.cmd.Process.Pid {
		t.Fatal("not distinct gateway processes")
	}
	stream := clusterRequest(t, ctx, a, key, true)
	t.Cleanup(func() { stream.Body.Close() })
	if stream.StatusCode != 200 {
		t.Fatal("stream did not start")
	}
	id := stream.Header.Get("X-Request-ID")
	reader := bufio.NewReader(stream.Body)
	line, err := reader.ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "data: ") {
		t.Fatal("first durable stream chunk missing")
	}
	deny := func(code string) {
		t.Helper()
		r := clusterRequest(t, ctx, b, key, false)
		body, _ := io.ReadAll(r.Body)
		r.Body.Close()
		if r.StatusCode != 429 || !strings.Contains(string(body), code) {
			t.Fatalf("expected shared denial %s; status=%d", code, r.StatusCode)
		}
	}
	deny("project_concurrency_limited")
	a.stop(true)
	rest, err := io.ReadAll(reader)
	stream.Body.Close()
	if err == nil || strings.Contains(string(rest), "[DONE]") {
		t.Fatal("killed process produced a completed stream")
	}
	deny("project_concurrency_limited")
	// Advance ONLY disposable fixture timestamps; production leases remain 180s.
	if _, err := db.Exec(ctx, `UPDATE admission_leases SET expires_at=clock_timestamp()-interval '1 second' WHERE request_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	deny("budget_exceeded")
	if n, err := ledger.Recover(ctx, time.Now().Add(time.Second)); err != nil || n != 1 {
		t.Fatal("crashed dispatch did not become uncertain")
	}
	reservation, err := ledger.Get(ctx, id)
	if err != nil || reservation.State != "uncertain" || reservation.Actual != nil || reservation.Amount != 384 {
		t.Fatal("crash released or charged unknown usage")
	}
	if _, err := db.Exec(ctx, `UPDATE content_audit_checkpoints SET lease_until=clock_timestamp()-interval '1 second' WHERE request_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if n, err := audit.Recover(ctx, 10); err != nil || n != 1 {
		t.Fatal("crashed audit checkpoint not recovered", err)
	}
	var state string
	var status int
	if err := db.QueryRow(ctx, `SELECT state,status FROM content_audit_records WHERE request_id=$1`, id).Scan(&state, &status); err != nil || state != "incomplete" || status != 0 {
		t.Fatal("crashed audit claimed completion")
	}
	response := clusterRequest(t, ctx, b, healthyKey, false)
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal("surviving node failed healthy project")
	}
	settled, err := ledger.Get(ctx, response.Header.Get("X-Request-ID"))
	if err != nil || settled.State != "settled" || settled.Actual == nil || *settled.Actual != 14 {
		t.Fatal("surviving node settlement failed")
	}
	if calls.Load() != 2 {
		t.Fatal("denial/recovery replayed inference")
	}
	var held, spent int64
	if err := db.QueryRow(ctx, `SELECT held,spent FROM budget_accounts WHERE organization_id=$1 AND project_id=''`, org.ID).Scan(&held, &spent); err != nil || held != 384 || spent != 14 {
		t.Fatal("shared account conservation failed")
	}
	checkpoint, err := audit.Verify(ctx, org.ID)
	if err != nil || !checkpoint.Valid {
		t.Fatal("cross-process audit chain invalid", err)
	}
	if err := b.stop(false); err != nil {
		t.Fatal("surviving child failed clean shutdown", err)
	}
}
