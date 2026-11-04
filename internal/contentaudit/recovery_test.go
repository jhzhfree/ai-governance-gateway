package contentaudit_test

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jhzhfree/ai-governance-gateway/internal/budget"
	"github.com/jhzhfree/ai-governance-gateway/internal/contentaudit"
	"github.com/jhzhfree/ai-governance-gateway/internal/control"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
)

type recoveryFixture struct {
	pool    *pgxpool.Pool
	service *contentaudit.Service
	ring    *contentaudit.Keyring
	keys    string
	org     control.Organization
	project control.Project
}

func newRecoveryFixture(t *testing.T) recoveryFixture {
	t.Helper()
	pool, _, _ := testutil.Database(t)
	ctx := context.Background()
	store := control.New(pool)
	owner := identity.Principal{Issuer: "https://synthetic.invalid", Subject: "operator"}
	org, err := store.CreateOrganization(ctx, owner, "recovery fixture", "2026-11", 1000)
	if err != nil {
		t.Fatal(err)
	}
	project, err := store.CreateProject(ctx, owner, org.ID, "recovery", 1000)
	if err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]string{"synthetic-v1": base64.StdEncoding.EncodeToString(key)})
	ring, err := contentaudit.ParseKeys(string(data), "synthetic-v1")
	if err != nil {
		t.Fatal(err)
	}
	service := contentaudit.New(pool, ring)
	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		return service.SetPolicy(ctx, tx, org.ID, project.ID, contentaudit.Policy{Enabled: true, RetentionDays: 1, MaxBytes: 1024, ConsentRef: "synthetic-checkpoint"})
	}); err != nil {
		t.Fatal(err)
	}
	return recoveryFixture{pool, service, ring, string(data), org, project}
}
func (f recoveryFixture) begin(t *testing.T, id string) *contentaudit.Capture {
	t.Helper()
	c, err := f.service.Begin(context.Background(), contentaudit.Meta{RequestID: id, Organization: f.org.ID, Project: f.project.ID, Model: "mock-text"}, []byte("synthetic request"))
	if err != nil || c == nil {
		t.Fatal("begin failed")
	}
	return c
}
func (f recoveryFixture) expire(t *testing.T) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(), `UPDATE content_audit_checkpoints SET lease_until=clock_timestamp()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
}
func (f recoveryFixture) detail(t *testing.T, id string) contentaudit.Detail {
	t.Helper()
	var detail contentaudit.Detail
	if err := pgx.BeginFunc(context.Background(), f.pool, func(tx pgx.Tx) error {
		meta, err := contentaudit.ScanMeta(tx.QueryRow(context.Background(), `SELECT `+contentaudit.Columns+` FROM content_audit_records WHERE request_id=$1`, id))
		if err != nil {
			return err
		}
		detail, err = f.service.Decrypt(context.Background(), tx, meta)
		return err
	}); err != nil {
		t.Fatal("decrypt recovery failed", err)
	}
	return detail
}
func TestAuditCheckpointRecoveryTruncationAndFencing(t *testing.T) {
	f := newRecoveryFixture(t)
	ctx := context.Background()
	c := f.begin(t, "bounded")
	chunks := []string{"first 合成", strings.Repeat("后续", 600), "tail"}
	all := strings.Join(chunks, "")
	for _, chunk := range chunks {
		if err := c.Checkpoint(ctx, []byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	var captured, parts, bytes int
	if err := f.pool.QueryRow(ctx, `SELECT captured_bytes,parts,(SELECT COALESCE(sum(octet_length(ciphertext)-16),0) FROM content_audit_checkpoint_parts) FROM content_audit_checkpoints WHERE request_id='bounded'`).Scan(&captured, &parts, &bytes); err != nil || captured != 1024 || parts != 2 || bytes != 1024 {
		t.Fatal("prefix journal unbounded or rewritten")
	}
	if n, err := f.service.Recover(ctx, 10); err != nil || n != 0 {
		t.Fatal("live lease recovered")
	}
	f.expire(t)
	if n, err := contentaudit.New(f.pool, nil).Recover(ctx, 10); err == nil || n != 0 {
		t.Fatal("missing key accepted")
	}
	restarted := contentaudit.New(f.pool, f.ring)
	if n, err := restarted.Recover(ctx, 10); err != nil || n != 1 {
		t.Fatal("recover failed", err)
	}
	detail := f.detail(t, "bounded")
	sum := sha256.Sum256([]byte(all))
	if detail.Meta.State != "incomplete" || detail.Meta.Status != 0 || detail.Response == nil || !detail.Response.Truncated || detail.Response.ObservedBytes != int64(len(all)) || detail.Response.SHA256 != hex.EncodeToString(sum[:]) || len(detail.Response.Content) > 1024 {
		t.Fatal("recovered content metadata invalid")
	}
	if err := c.Checkpoint(ctx, []byte("late writer")); err == nil {
		t.Fatal("recovered capture emitted more bytes")
	}
	if err := c.Finish(ctx, 200, "complete"); err == nil {
		t.Fatal("late finish overwrote recovery")
	}
	if n, err := restarted.Recover(ctx, 10); err != nil || n != 0 {
		t.Fatal("recovery not idempotent")
	}
	chain, err := f.service.Verify(ctx, f.org.ID)
	if err != nil || !chain.Valid || chain.Sequence != 2 {
		t.Fatal("recovered chain invalid")
	}
}
func TestAuditCheckpointTamperAndAtomicRecoveryRollback(t *testing.T) {
	f := newRecoveryFixture(t)
	ctx := context.Background()
	for _, id := range []string{"a-good", "b-bad"} {
		c := f.begin(t, id)
		if err := c.Checkpoint(ctx, []byte("synthetic output")); err != nil {
			t.Fatal(err)
		}
	}
	f.expire(t)
	var original []byte
	if err := f.pool.QueryRow(ctx, `SELECT proof FROM content_audit_checkpoints WHERE request_id='b-bad'`).Scan(&original); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE content_audit_checkpoints SET observed_bytes=observed_bytes+1 WHERE request_id='b-bad'`); err != nil {
		t.Fatal(err)
	}
	if n, err := f.service.Recover(ctx, 10); err == nil || n != 0 {
		t.Fatal("tampered snapshot accepted")
	}
	var count int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM content_audit_records WHERE state='capturing'`).Scan(&count); err != nil || count != 2 {
		t.Fatal("batch partially committed before corrupt record")
	}
	if _, err := f.pool.Exec(ctx, `UPDATE content_audit_checkpoints SET observed_bytes=observed_bytes-1,proof=$1 WHERE request_id='b-bad'`, original); err != nil {
		t.Fatal(err)
	}
	// Missing/reordered encrypted parts cannot be reconstructed from a digest.
	if _, err := f.pool.Exec(ctx, `UPDATE content_audit_checkpoint_parts SET nonce=set_byte(nonce,0,get_byte(nonce,0)#1) WHERE request_id='b-bad'`); err != nil {
		t.Fatal(err)
	}
	if n, err := f.service.Recover(ctx, 10); err == nil || n != 0 {
		t.Fatal("tampered journal accepted")
	}
}
func TestAuditCheckpointConcurrentRecoveryAndExpiredPurge(t *testing.T) {
	f := newRecoveryFixture(t)
	ctx := context.Background()
	for i := 0; i < 4; i++ {
		c := f.begin(t, fmt.Sprint("parallel-", i))
		if err := c.Checkpoint(ctx, []byte("synthetic partial")); err != nil {
			t.Fatal(err)
		}
	}
	f.expire(t)
	var wg sync.WaitGroup
	counts := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, err := contentaudit.New(f.pool, f.ring).Recover(ctx, 2)
			if err != nil {
				t.Error(err)
			}
			counts <- n
		}()
	}
	wg.Wait()
	close(counts)
	total := 0
	for n := range counts {
		total += n
	}
	if total != 4 {
		t.Fatal("workers repeated or skipped records")
	}
	c := f.begin(t, "expire-private")
	if err := c.Checkpoint(ctx, []byte("synthetic secret prefix")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE content_audit_records SET expires_at=clock_timestamp()-interval '1 second' WHERE request_id='expire-private'`); err != nil {
		t.Fatal(err)
	}
	if n, err := f.service.Purge(ctx, 10); err != nil || n != 1 {
		t.Fatal("expired checkpoint not purged")
	}
	var count int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM content_audit_checkpoint_parts WHERE request_id='expire-private'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("private checkpoint parts retained")
	}
	if err := c.Finish(ctx, 200, "complete"); err == nil {
		t.Fatal("purged work revived")
	}
}

// A separate OS process commits a checkpoint and is then killed, so recovery
// cannot accidentally depend on the original Capture's hash, DEK or prefix RAM.
func TestAuditCheckpointProcessKillRecovery(t *testing.T) {
	if os.Getenv("AIGW_TEST_CHECKPOINT_CHILD") == "1" {
		config, err := pgxpool.ParseConfig(os.Getenv("TEST_DATABASE_URL"))
		if err != nil {
			os.Exit(21)
		}
		config.ConnConfig.RuntimeParams["search_path"] = os.Getenv("AIGW_TEST_CHECKPOINT_SCHEMA")
		pool, err := pgxpool.NewWithConfig(context.Background(), config)
		if err != nil {
			os.Exit(22)
		}
		ring, err := contentaudit.ParseKeys(os.Getenv("AIGW_TEST_CHECKPOINT_KEYS"), "synthetic-v1")
		if err != nil {
			os.Exit(23)
		}
		service := contentaudit.New(pool, ring)
		c, err := service.Begin(context.Background(), contentaudit.Meta{RequestID: "killed-process", Organization: os.Getenv("AIGW_TEST_CHECKPOINT_ORG"), Project: os.Getenv("AIGW_TEST_CHECKPOINT_PROJECT"), Model: "mock-text"}, []byte("synthetic request"))
		if err != nil || c == nil {
			os.Exit(24)
		}
		if err := c.Checkpoint(context.Background(), []byte("durable 子进程 response")); err != nil {
			os.Exit(25)
		}
		fmt.Println("checkpoint-ready")
		select {}
	}
	f := newRecoveryFixture(t)
	ctx := context.Background()
	ledger := budget.New(f.pool)
	quote := budget.Quote{Scope: budget.Scope{Organization: f.org.ID, Project: f.project.ID, Period: f.org.Period, Currency: f.org.Currency}, RequestID: "killed-process", PriceVersion: "synthetic", Amount: 10}
	if _, err := ledger.Reserve(ctx, quote); err != nil {
		t.Fatal(err)
	}
	childCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(childCtx, os.Args[0], "-test.run=^TestAuditCheckpointProcessKillRecovery$")
	cmd.Env = append(os.Environ(), "AIGW_TEST_CHECKPOINT_CHILD=1", "AIGW_TEST_CHECKPOINT_SCHEMA="+f.pool.Config().ConnConfig.RuntimeParams["search_path"], "AIGW_TEST_CHECKPOINT_KEYS="+f.keys, "AIGW_TEST_CHECKPOINT_ORG="+f.org.ID, "AIGW_TEST_CHECKPOINT_PROJECT="+f.project.ID)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill() }()
	scan := bufio.NewScanner(stdout)
	ready := false
	for scan.Scan() {
		if scan.Text() == "checkpoint-ready" {
			ready = true
			break
		}
	}
	if !ready {
		_ = cmd.Wait()
		t.Fatal("child did not commit checkpoint")
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("child exited without being killed")
	}
	f.expire(t)
	if n, err := contentaudit.New(f.pool, f.ring).Recover(ctx, 10); err != nil || n != 1 {
		t.Fatal("post-kill recovery failed", err)
	}
	detail := f.detail(t, "killed-process")
	if detail.Response == nil || detail.Response.Content != "durable 子进程 response" || detail.Meta.State != "incomplete" {
		t.Fatal("lost committed output after process kill")
	}
	reservation, err := ledger.Get(ctx, "killed-process")
	if err != nil || reservation.State != "reserved" || reservation.Amount != 10 {
		t.Fatal("audit recovery modified finance")
	}
}
