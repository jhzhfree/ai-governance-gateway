package newapibridge_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jhzhfree/ai-governance-gateway/internal/budget"
	"github.com/jhzhfree/ai-governance-gateway/internal/execution"
	"github.com/jhzhfree/ai-governance-gateway/internal/gateway"
	"github.com/jhzhfree/ai-governance-gateway/internal/newapibridge"
	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
)

// Helper is a distinct OS process running the real enterprise handler, journal
// and recovery worker, with synthetic authorization and an isolated test schema.
func TestNewAPIControlHelper(t *testing.T) {
	if os.Getenv("AIGW_INTEGRATION_CHILD") != "1" {
		t.Skip("subprocess fixture only")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, os.Getenv("AIGW_INTEGRATION_DSN"))
	if err != nil {
		t.Fatal("fixture database unavailable")
	}
	defer pool.Close()
	var schema string
	if pool.QueryRow(ctx, "SELECT current_schema()").Scan(&schema) != nil || !strings.HasPrefix(schema, "poc_") {
		t.Fatal("isolated test schema required")
	}

	cfg := testutil.MainlandConfig()
	for i := range cfg.Models {
		cfg.Models[i].UpstreamModel = "governance-model"
		cfg.Models[i].NewAPIChannelID = 1
	}
	directory, err := provider.NewGoverned(cfg)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := newapibridge.NewRuntime(ctx, newapibridge.Config{Origin: os.Getenv("AIGW_INTEGRATION_ORIGIN"), ServiceToken: "sk-syntheticgovernancefixture", MaxConcurrent: 4, AllowLoopbackHTTP: true, Journal: newapibridge.NewJournal(pool)}, directory)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	scope := budget.Scope{Organization: "org-test", Project: "project-test", Period: "2026-11", Currency: provider.Currency}
	handler := &gateway.Handler{Executor: runtime, Providers: runtime, Ledger: budget.New(pool), Attempts: execution.New(pool), Admission: testutil.PassAdmission{}, Circuits: testutil.PassCircuit{}, Authorize: func(context.Context, string, string) (budget.Scope, error) { return scope, nil }}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if os.WriteFile(os.Getenv("AIGW_INTEGRATION_READY"), []byte("http://"+listener.Addr().String()), 0600) != nil {
		t.Fatal("fixture address unavailable")
	}
	_ = http.Serve(listener, handler)
}

func TestNewAPITwoProcessRecovery(t *testing.T) {
	binary := os.Getenv("TEST_NEWAPI_SERVER_BINARY")
	if binary == "" {
		t.Skip("pinned new-api integration fixture binary required")
	}
	pool, ledger, scope := testutil.Database(t)
	scope.Currency = provider.Currency
	testutil.Accounts(t, ledger, scope, 100000, 100000)
	var calls atomic.Int32
	var block atomic.Bool
	arrived := make(chan struct{})
	blockedRelease := make(chan struct{})
	streamRelease := make(chan struct{})
	var streamOnce sync.Once
	releaseStream := func() { streamOnce.Do(func() { close(streamRelease) }) }
	defer releaseStream()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if block.Load() {
			close(arrived)
			select {
			case <-r.Context().Done():
			case <-blockedRelease:
			}
			return
		}
		var req provider.Request
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			w.WriteHeader(400)
			return
		}
		if req.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "data: {\"id\":\"synthetic\",\"model\":\"governance-model\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"synthetic\"},\"finish_reason\":null}]}\n\n")
			io.WriteString(w, "data: {\"id\":\"synthetic\",\"model\":\"governance-model\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
			w.(http.Flusher).Flush()
			select {
			case <-streamRelease:
			case <-r.Context().Done():
				return
			}
			io.WriteString(w, "data: {\"id\":\"synthetic\",\"model\":\"governance-model\",\"choices\":[],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":3,\"total_tokens\":5}}\n\ndata: [DONE]\n\n")
		} else {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, reply)
		}
	}))
	defer func() { releaseStream(); upstream.Close() }()
	defer close(blockedRelease)
	start := func(command *exec.Cmd, ready string) (string, func()) {
		t.Helper()
		_ = os.Remove(ready)
		var output bytes.Buffer
		command.Stdout = &output
		command.Stderr = &output
		if err := command.Start(); err != nil {
			t.Fatal("fixture process did not start")
		}
		var once sync.Once
		stop := func() {
			once.Do(func() {
				_ = command.Process.Kill()
				_ = command.Wait()
				if dir := os.Getenv("TEST_NEWAPI_PROCESS_LOG_DIR"); dir != "" {
					_ = os.WriteFile(filepath.Join(dir, filepath.Base(command.Path)+".log"), output.Bytes(), 0600)
				}
			})
		}
		t.Cleanup(stop)
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			data, err := os.ReadFile(ready)
			if err == nil && len(data) > 0 {
				return string(data), stop
			}
			time.Sleep(20 * time.Millisecond)
		}
		stop()
		if dir := os.Getenv("TEST_NEWAPI_PROCESS_LOG_DIR"); dir != "" {
			_ = os.WriteFile(filepath.Join(dir, "process-failure.log"), output.Bytes(), 0600)
		}
		t.Fatalf("fixture readiness timeout (exit=%d; process output withheld)", command.ProcessState.ExitCode())
		return "", stop
	}
	root := t.TempDir()
	native := exec.Command(binary)
	native.Env = append(os.Environ(), "GOVERNANCE_FIXTURE_DIR="+root, "GOVERNANCE_FIXTURE_UPSTREAM="+upstream.URL)
	nativeURL, stopNative := start(native, filepath.Join(root, "address"))
	defer stopNative()
	target, _ := url.Parse(nativeURL)
	proxy := httputil.NewSingleHostReverseProxy(target)
	var pause atomic.Bool
	captured := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	proxy.ModifyResponse = func(res *http.Response) error {
		if res.StatusCode != 200 && strings.HasSuffix(res.Request.URL.Path, "/chat/completions") {
			body, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
			res.Body.Close()
			res.Body = io.NopCloser(bytes.NewReader(body))
			t.Logf("synthetic native status=%d body=%s", res.StatusCode, body)
		}

		if pause.Load() && strings.HasSuffix(res.Request.URL.Path, "/chat/completions") && res.StatusCode == 200 {
			body, err := io.ReadAll(res.Body)
			res.Body.Close()
			if err != nil {
				return err
			}
			res.Body = io.NopCloser(bytes.NewReader(body))
			close(captured)
			<-release
		}
		return nil
	}
	faultProxy := httptest.NewServer(proxy)
	defer faultProxy.Close()

	isolatedDSN, err := url.Parse(pool.Config().ConnString())
	if err != nil {
		t.Fatal("invalid isolated fixture DSN")
	}
	query := isolatedDSN.Query()
	query.Set("search_path", pool.Config().ConnConfig.RuntimeParams["search_path"])
	isolatedDSN.RawQuery = query.Encode()
	child := func() (string, func()) {
		ready := filepath.Join(t.TempDir(), "ready")
		command := exec.Command(os.Args[0], "-test.run=^TestNewAPIControlHelper$")
		command.Env = append(os.Environ(), "AIGW_INTEGRATION_CHILD=1", "AIGW_INTEGRATION_DSN="+isolatedDSN.String(), "AIGW_INTEGRATION_ORIGIN="+faultProxy.URL, "AIGW_INTEGRATION_READY="+ready)
		return start(command, ready)
	}
	controlURL, stopControl := child()
	call := func(origin string, stream bool) (int, []byte) {
		req := request()
		req.Stream = stream
		data, _ := json.Marshal(req)
		r, _ := http.NewRequest("POST", origin+"/v1/chat/completions", bytes.NewReader(data))
		r.Header.Set("Authorization", "Bearer synthetic-project-key")
		r.Header.Set("Content-Type", "application/json")
		client := &http.Client{Timeout: 20 * time.Second}
		res, err := client.Do(r)
		if err != nil {
			return 0, nil
		}
		defer res.Body.Close()
		if stream {
			reader := bufio.NewReader(res.Body)
			first, readErr := reader.ReadString('\n')
			if readErr != nil || !strings.Contains(first, "synthetic") {
				releaseStream()
				t.Fatal("stream content did not precede upstream completion")
			}
			var settledBefore int
			if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM budget_reservations WHERE state='settled'`).Scan(&settledBefore); err != nil || settledBefore != 1 {
				releaseStream()
				t.Fatal("stream settled before completion")
			}
			releaseStream()
			tail, readErr := io.ReadAll(reader)
			if readErr != nil || !strings.Contains(string(tail), "[DONE]") {
				t.Fatalf("verified stream did not finish: %v", readErr)
			}
			return res.StatusCode, append([]byte(first), tail...)
		}
		body, _ := io.ReadAll(res.Body)
		return res.StatusCode, body
	}
	for _, stream := range []bool{false, true} {
		status, body := call(controlURL, stream)
		if status != 200 {
			t.Fatalf("real process integration rejected request: %d %s physical=%d", status, body, calls.Load())
		}
	}
	pause.Store(true)
	requestDone := make(chan struct{})
	go func() { call(controlURL, false); close(requestDone) }()
	select {
	case <-captured:
	case <-time.After(20 * time.Second):
		t.Fatal("did not reach durable result fault point")
	}
	stopControl() // SIGKILL while the successful native response is withheld.
	releaseOnce.Do(func() { close(release) })
	pause.Store(false)
	<-requestDone
	if calls.Load() != 3 {
		t.Fatal("unexpected physical attempt count before recovery")
	}
	restartedURL, stopRestarted := child()
	defer stopRestarted()
	_ = restartedURL
	var settled, receipts int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM budget_reservations WHERE state='settled'`).Scan(&settled); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM newapi_execution_receipts`).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if settled != 3 || receipts != 3 || calls.Load() != 3 {
		t.Fatalf("restart recovery mismatch: settled=%d receipts=%d physical=%d", settled, receipts, calls.Load())
	}
	var held, spent int64
	if err := pool.QueryRow(context.Background(), `SELECT held,spent FROM budget_accounts WHERE project_id=$1`, scope.Project).Scan(&held, &spent); err != nil || held != 0 || spent != 48 {
		t.Fatalf("incorrect recovered ledger: held=%d spent=%d", held, spent)
	}
	// Kill the data plane after the provider observes a request, before any
	// completion evidence exists. Restart must never replay or refund it.
	block.Store(true)
	uncertainDone := make(chan struct{})
	go func() { call(restartedURL, false); close(uncertainDone) }()
	select {
	case <-arrived:
	case <-time.After(20 * time.Second):
		t.Fatal("unknown-outcome fault point not reached")
	}
	stopNative()
	select {
	case <-uncertainDone:
	case <-time.After(20 * time.Second):
		t.Fatal("data-plane termination did not return")
	}
	restartedNative := exec.Command(binary)
	restartedNative.Env = append(os.Environ(), "GOVERNANCE_FIXTURE_DIR="+root, "GOVERNANCE_FIXTURE_UPSTREAM="+upstream.URL, "GOVERNANCE_FIXTURE_LISTEN="+target.Host)
	_, stopNativeAgain := start(restartedNative, filepath.Join(root, "address"))
	defer stopNativeAgain()
	// A recovery poll may observe this restart; it is not authority to replay.
	time.Sleep(6 * time.Second)
	var uncertain int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM budget_reservations WHERE state='uncertain'`).Scan(&uncertain); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(context.Background(), `SELECT held,spent FROM budget_accounts WHERE project_id=$1`, scope.Project).Scan(&held, &spent); err != nil {
		t.Fatal(err)
	}
	if uncertain != 1 || held != 288 || spent != 48 || calls.Load() != 4 {
		t.Fatalf("unknown outcome was replayed/refunded: uncertain=%d held=%d spent=%d physical=%d", uncertain, held, spent, calls.Load())
	}

	// An expired local binding with no data-plane dispatch can be cancelled
	// atomically; the resulting durable tombstone is the only refund authority.
	cfg := testutil.MainlandConfig()
	for i := range cfg.Models {
		cfg.Models[i].NewAPIChannelID = 1
		cfg.Models[i].UpstreamModel = "governance-model"
	}
	directory, err := provider.NewGoverned(cfg)
	if err != nil {
		t.Fatal(err)
	}
	route, _ := directory.Find("zhipu/fixture")
	const orphanRequest = "OrphanRequest123456"
	const orphanAttempt = "OrphanAttempt123456"
	if _, err := ledger.Reserve(context.Background(), budget.Quote{Scope: scope, RequestID: orphanRequest, PriceVersion: route.PriceVersion(), Amount: 288}); err != nil {
		t.Fatal(err)
	}
	if err := execution.New(pool).Begin(context.Background(), execution.Start{AttemptID: orphanAttempt, RequestID: orphanRequest, Model: "zhipu/fixture", Provider: "zhipu", Region: "cn-mainland"}); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Dispatch(context.Background(), orphanRequest); err != nil {
		t.Fatal(err)
	}
	if err := newapibridge.NewJournal(pool).Bind(context.Background(), faultProxy.URL, "governance-model", orphanRequest, orphanAttempt, strings.Repeat("a", 64), route, 8); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(), `UPDATE newapi_execution_bindings SET created_at=clock_timestamp()-interval '4 minutes' WHERE attempt_id=$1`, orphanAttempt); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		r, err := ledger.Get(context.Background(), orphanRequest)
		if err == nil && r.State == "settled" {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	orphan, err := ledger.Get(context.Background(), orphanRequest)
	if err != nil || orphan.State != "settled" || orphan.Actual == nil || *orphan.Actual != 0 {
		t.Fatal("absent attempt not reconciled from cancellation proof")
	}
	late, _ := http.NewRequest("POST", nativeURL+"/internal/governance/v1/chat/completions", strings.NewReader(`{"model":"governance-model","messages":[],"max_tokens":8}`))
	late.Header.Set("Authorization", "Bearer sk-syntheticgovernancefixture")
	late.Header.Set("X-Aigw-Request-Id", orphanRequest)
	late.Header.Set("X-Aigw-Attempt-Id", orphanAttempt)
	late.Header.Set("X-Aigw-Channel-Id", "1")
	rejected, err := http.DefaultClient.Do(late)
	if err != nil {
		t.Fatal(err)
	}
	rejected.Body.Close()
	if rejected.StatusCode != 409 || calls.Load() != 4 {
		t.Fatal("late request bypassed cancelled tombstone")
	}
	if err := pool.QueryRow(context.Background(), `SELECT held,spent FROM budget_accounts WHERE project_id=$1`, scope.Project).Scan(&held, &spent); err != nil || held != 288 || spent != 48 {
		t.Fatal("cancellation released an uncertain physical dispatch")
	}

}
