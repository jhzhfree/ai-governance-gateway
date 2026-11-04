package contentaudit_test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jhzhfree/ai-governance-gateway/internal/contentaudit"
	"github.com/jhzhfree/ai-governance-gateway/internal/control"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
	"testing"
)

func TestContentCaptureRoundTrip(t *testing.T) {
	pool, _, _ := testutil.Database(t)
	ctx := context.Background()
	store := control.New(pool)
	actor := identity.Principal{Issuer: "https://synthetic.invalid", Subject: "owner"}
	org, err := store.CreateOrganization(ctx, actor, "synthetic", "2026-11", 100)
	if err != nil {
		t.Fatal(err)
	}
	project, err := store.CreateProject(ctx, actor, org.ID, "synthetic", 100)
	if err != nil {
		t.Fatal(err)
	}
	service := testutil.ContentAudit(t, pool)
	err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		return service.SetPolicy(ctx, tx, org.ID, project.ID, contentaudit.Policy{Enabled: true, RetentionDays: 1, MaxBytes: 1024, ConsentRef: "synthetic"})
	})
	if err != nil {
		t.Fatal("policy", err)
	}
	capture, err := service.Begin(ctx, contentaudit.Meta{RequestID: "synthetic-capture", Organization: org.ID, Project: project.ID, Model: "mock-text"}, []byte("synthetic request"))
	if err != nil {
		t.Fatal("begin", err)
	}
	if capture == nil {
		t.Fatal("enabled policy skipped")
	}
	capture.Observe([]byte("synthetic response"))
	if err := capture.Finish(ctx, 200, "complete"); err != nil {
		t.Fatal("finish", err)
	}
	err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		meta, err := contentaudit.ScanMeta(tx.QueryRow(ctx, `SELECT `+contentaudit.Columns+` FROM content_audit_records WHERE request_id='synthetic-capture'`))
		if err != nil {
			return err
		}
		detail, err := service.Decrypt(ctx, tx, meta)
		if err == nil && (detail.Request.Content != "synthetic request" || detail.Response == nil || detail.Response.Content != "synthetic response") {
			t.Fatal("content changed")
		}
		return err
	})
	if err != nil {
		t.Fatal("decrypt", err)
	}
	if contentaudit.New(pool, nil).Ready(ctx) {
		t.Fatal("missing keys reported ready")
	}
	if _, err := contentaudit.New(pool, nil).Begin(ctx, contentaudit.Meta{RequestID: "without-key", Organization: org.ID, Project: project.ID, Model: "mock-text"}, []byte("synthetic")); err == nil {
		t.Fatal("enabled policy silently bypassed missing keys")
	}
	interrupted, err := service.Begin(ctx, contentaudit.Meta{RequestID: "crash-window", Organization: org.ID, Project: project.ID, Model: "mock-text"}, []byte("durable request"))
	if err != nil {
		t.Fatal(err)
	}
	interrupted.Observe([]byte("partial response"))
	var state string
	if err := pool.QueryRow(ctx, `SELECT state FROM content_audit_records WHERE request_id='crash-window'`).Scan(&state); err != nil || state != "capturing" {
		t.Fatal("unfinished capture claimed complete")
	}
	if err := interrupted.Finish(ctx, 200, "incomplete"); err != nil {
		t.Fatal(err)
	}

}

func TestContentKeyVersionRotationAndBoundedCapture(t *testing.T) {
	pool, _, _ := testutil.Database(t)
	ctx := context.Background()
	store := control.New(pool)
	owner := identity.Principal{Issuer: "https://synthetic.invalid", Subject: "owner"}
	org, err := store.CreateOrganization(ctx, owner, "rotation", "2026-11", 100)
	if err != nil {
		t.Fatal(err)
	}
	project, err := store.CreateProject(ctx, owner, org.ID, "rotation", 100)
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{}
	for _, id := range []string{"v1", "v2"} {
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			t.Fatal(err)
		}
		values[id] = base64.StdEncoding.EncodeToString(key)
	}
	data, _ := json.Marshal(values)
	ring1, err := contentaudit.ParseKeys(string(data), "v1")
	if err != nil {
		t.Fatal(err)
	}
	first := contentaudit.New(pool, ring1)
	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		return first.SetPolicy(ctx, tx, org.ID, project.ID, contentaudit.Policy{Enabled: true, RetentionDays: 1, MaxBytes: 1024, ConsentRef: "synthetic"})
	}); err != nil {
		t.Fatal(err)
	}
	capture, err := first.Begin(ctx, contentaudit.Meta{RequestID: "old-version", Organization: org.ID, Project: project.ID, Model: "mock-text"}, []byte("synthetic old content"))
	if err != nil {
		t.Fatal(err)
	}
	capture.Observe([]byte("response"))
	if err := capture.Finish(ctx, 200, "complete"); err != nil {
		t.Fatal(err)
	}
	ring2, err := contentaudit.ParseKeys(string(data), "v2")
	if err != nil {
		t.Fatal(err)
	}
	second := contentaudit.New(pool, ring2)
	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		meta, err := contentaudit.ScanMeta(tx.QueryRow(ctx, `SELECT `+contentaudit.Columns+` FROM content_audit_records WHERE request_id='old-version'`))
		if err != nil {
			return err
		}
		detail, err := second.Decrypt(ctx, tx, meta)
		if err == nil && detail.Request.Content != "synthetic old content" {
			t.Fatal("rotation changed old content")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var active []*contentaudit.Capture
	for i := 0; i < 16; i++ {
		id := fmt.Sprintf("bounded-%d", i)
		c, err := second.Begin(ctx, contentaudit.Meta{RequestID: id, Organization: org.ID, Project: project.ID, Model: "mock-text"}, []byte("synthetic"))
		if err != nil {
			t.Fatal(err)
		}
		active = append(active, c)
	}
	if _, err := second.Begin(ctx, contentaudit.Meta{RequestID: "overflow", Organization: org.ID, Project: project.ID, Model: "mock-text"}, []byte("synthetic")); err == nil {
		t.Fatal("unbounded audit buffers")
	}
	for _, c := range active {
		if err := c.Finish(ctx, 503, "incomplete"); err != nil {
			t.Fatal(err)
		}
	}
	proof, err := second.Verify(ctx, org.ID)
	if err != nil || !proof.Valid {
		t.Fatal("rotated captures failed integrity", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE content_audit_payloads SET nonce=decode(repeat('00',12),'hex') WHERE request_id='old-version' AND direction='response'`); err != nil {
		t.Fatal(err)
	}
	proof, err = second.Verify(ctx, org.ID)
	if err != nil || proof.Valid {
		t.Fatal("retained nonce corruption undetected", err)
	}
}
