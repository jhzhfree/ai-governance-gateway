package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jhzhfree/ai-governance-gateway/internal/contentaudit"
	"github.com/jhzhfree/ai-governance-gateway/internal/control"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
)

func TestRecoveryOperatorCommandUsesKeysAndEmitsOnlyCounts(t *testing.T) {
	pool, _, _ := testutil.Database(t)
	ctx := context.Background()
	dsn := os.Getenv("TEST_DATABASE_URL")
	schema := pool.Config().ConnConfig.RuntimeParams["search_path"]
	if parsed, err := url.Parse(dsn); err == nil && (parsed.Scheme == "postgres" || parsed.Scheme == "postgresql") {
		q := parsed.Query()
		q.Set("search_path", schema)
		parsed.RawQuery = q.Encode()
		dsn = parsed.String()
	} else {
		dsn += " search_path='" + schema + "'"
	}
	t.Setenv("AIGW_DATABASE_URL", dsn)
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(map[string]string{"fixture": base64.StdEncoding.EncodeToString(key)})
	ring, err := contentaudit.ParseKeys(string(encoded), "fixture")
	if err != nil {
		t.Fatal(err)
	}
	service := contentaudit.New(pool, ring)
	store := control.New(pool)
	actor := identity.Principal{Issuer: "https://synthetic.invalid", Subject: "operator"}
	org, err := store.CreateOrganization(ctx, actor, "operator fixture", "2026-11", 100)
	if err != nil {
		t.Fatal(err)
	}
	project, err := store.CreateProject(ctx, actor, org.ID, "fixture", 100)
	if err != nil {
		t.Fatal(err)
	}
	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		return service.SetPolicy(ctx, tx, org.ID, project.ID, contentaudit.Policy{Enabled: true, MaxBytes: 1024, RetentionDays: 1, ConsentRef: "synthetic"})
	}); err != nil {
		t.Fatal(err)
	}
	capture, err := service.Begin(ctx, contentaudit.Meta{RequestID: "operator-recovery", Organization: org.ID, Project: project.ID, Model: "mock-text"}, []byte("synthetic request"))
	if err != nil {
		t.Fatal(err)
	}
	if err := capture.Checkpoint(ctx, []byte("sensitive-synthetic-output")); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE content_audit_checkpoints SET lease_until=clock_timestamp()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AIGW_CONTENT_AUDIT_KEYS", "")
	t.Setenv("AIGW_CONTENT_AUDIT_ACTIVE_KEY", "")
	var out bytes.Buffer
	if execute([]string{"recover", "1"}, &out) == nil || out.Len() != 0 {
		t.Fatal("missing keys yielded success")
	}
	t.Setenv("AIGW_CONTENT_AUDIT_KEYS", string(encoded))
	t.Setenv("AIGW_CONTENT_AUDIT_ACTIVE_KEY", "fixture")
	for _, args := range [][]string{{"recover", "0"}, {"recover", "101"}, {"recover", "invalid"}, {"recover", "1", "extra"}} {
		out.Reset()
		if execute(args, &out) == nil || out.Len() != 0 {
			t.Fatal("invalid recover limit accepted")
		}
	}
	if err := execute([]string{"recover", "1"}, &out); err != nil || out.String() != "{\"recovered\":1}\n" {
		t.Fatal("operator recovery failed or exposed payload")
	}
	out.Reset()
	if err := execute([]string{"recover"}, &out); err != nil || out.String() != "{\"recovered\":0}\n" {
		t.Fatal("operator retry not idempotent")
	}
	out.Reset()
	if err := execute([]string{"verify", org.ID}, &out); err != nil {
		t.Fatal("recovered chain did not verify")
	}
	if err := capture.Finish(ctx, 200, "complete"); err == nil {
		t.Fatal("operator recovery did not fence old capture")
	}
}
