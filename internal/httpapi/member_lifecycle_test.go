package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jhzhfree/ai-governance-gateway/internal/control"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
)

func lifecycleBody(f fixture, subject string, revision int64, enabled bool, role string) any {
	body := map[string]any{"target": identity.Principal{Issuer: f.issuer.URL, Subject: subject}, "expected_revision": revision, "enabled": enabled, "reason": "synthetic lifecycle review"}
	if role != "" {
		body["restore_role"] = role
	}
	return body
}
func TestMemberLifecycleRevokesScopedCredentialsAndExplicitRestore(t *testing.T) {
	f := providerSetup(t, testutil.SupplierFixture(t))
	o, p, _ := providerProject(f)
	other, otherProject, _ := providerProject(f)
	f.member(o, "bob", "developer")
	f.member(other, "bob", "developer")
	for _, pair := range []struct {
		o control.Organization
		p control.Project
	}{{o, p}, {other, otherProject}} {
		f.request(f.token("alice"), "PUT", projectPath(pair.o, pair.p)+"/members", map[string]string{"issuer": f.issuer.URL, "subject": "bob", "role": "developer"}, 204)
	}
	issue := func(org control.Organization, project control.Project) control.IssuedKey {
		return parse[control.IssuedKey](t, f.request(f.token("bob"), "POST", projectPath(org, project)+"/keys", map[string]any{"name": "lifecycle", "models": []string{"aliyun/fixture"}, "ttl_seconds": 3600}, 201))
	}
	key := issue(o, p)
	otherKey := issue(other, otherProject)
	enableContent(f, o, p, 65536)
	grantAuditor(f, o, p, "bob", true)
	f.request(key.Secret, "POST", "/v1/chat/completions", realPayload("aliyun/fixture", false), 200)
	records := parse[contentPage](t, f.request(f.token("bob"), "GET", projectPath(o, p)+"/content-audit/records", nil, 200))
	transfer := parse[control.OwnershipTransfer](t, f.request(f.token("alice"), "POST", orgPath(o)+"/ownership-transfer", handoffBody(f, "bob", "developer"), 201))
	before := parse[control.Usage](t, f.request(f.token("alice"), "GET", projectPath(o, p)+"/usage", nil, 200))
	path := orgPath(o) + "/members/enabled"
	event := parse[control.MemberLifecycleEvent](t, f.request(f.token("alice"), "PUT", path, lifecycleBody(f, "bob", 0, false, ""), 200))
	if event.Revision != 1 || event.Enabled || event.RevokedKeys != 1 || event.RemovedProjectGrants != 1 || event.RevokedAuditors != 1 || event.CanceledTransfers != 1 {
		t.Fatal("offboarding incomplete")
	}
	if after := parse[control.Usage](t, f.request(f.token("alice"), "GET", projectPath(o, p)+"/usage", nil, 200)); after != before {
		t.Fatal("offboarding changed budget")
	}
	visible := parse[struct {
		Data []control.Organization `json:"data"`
	}](t, f.request(f.token("bob"), "GET", "/api/v1/organizations", nil, 200))
	if len(visible.Data) != 1 || visible.Data[0].ID != other.ID {
		t.Fatal("disabled organization remained visible or other organization disappeared")
	}
	f.request(f.token("bob"), "GET", projectPath(o, p)+"/usage", nil, 404)
	f.request(f.token("bob"), "POST", projectPath(o, p)+"/keys", map[string]any{"name": "late", "models": []string{"aliyun/fixture"}, "ttl_seconds": 3600}, 404)
	f.request(key.Secret, "GET", "/v1/models", nil, 401)
	f.request(key.Secret, "POST", "/v1/chat/completions", realPayload("aliyun/fixture", false), 401)
	f.request(otherKey.Secret, "GET", "/v1/models", nil, 200)
	f.request(f.token("bob"), "POST", projectPath(o, p)+"/content-audit/records/"+records.Data[0].RequestID+"/reveal", map[string]string{"reason": "disabled audit reader"}, 403)
	f.request(f.token("bob"), "POST", orgPath(o)+"/ownership-transfer/"+transfer.ID+"/accept", nil, 404)
	f.request(f.token("alice"), "PUT", orgPath(o)+"/members", map[string]string{"issuer": f.issuer.URL, "subject": "bob", "role": "admin"}, 409)
	f.request(f.token("alice"), "PUT", projectPath(o, p)+"/content-audit/auditors", map[string]any{"issuer": f.issuer.URL, "subject": "bob", "active": true}, 409)
	f.request(f.token("alice"), "PUT", path, lifecycleBody(f, "bob", 0, false, ""), 409)
	restored := parse[control.MemberLifecycleEvent](t, f.request(f.token("alice"), "PUT", path, lifecycleBody(f, "bob", 1, true, "developer"), 200))
	if !restored.Enabled || restored.NewRole != "developer" || restored.RevokedKeys != 0 {
		t.Fatal("restore role incorrect")
	}
	f.request(key.Secret, "GET", "/v1/models", nil, 401)
	f.request(f.token("bob"), "GET", projectPath(o, p)+"/usage", nil, 404)
	f.request(f.token("bob"), "POST", projectPath(o, p)+"/content-audit/records/"+records.Data[0].RequestID+"/reveal", map[string]string{"reason": "old audit grant must not revive"}, 403)
	f.request(f.token("alice"), "PUT", projectPath(o, p)+"/members", map[string]string{"issuer": f.issuer.URL, "subject": "bob", "role": "developer"}, 204)
	fresh := issue(o, p)
	f.request(fresh.Secret, "GET", "/v1/models", nil, 200)
	history := parse[struct {
		Data []control.MemberLifecycleEvent `json:"data"`
	}](t, f.request(f.token("alice"), "GET", orgPath(o)+"/members/lifecycle?issuer="+url.QueryEscape(f.issuer.URL)+"&subject=bob", nil, 200))
	if len(history.Data) != 2 || history.Data[0].Enabled || !history.Data[1].Enabled {
		t.Fatal("lifecycle history missing")
	}
	orgs := parse[struct {
		Data []control.Organization `json:"data"`
	}](t, f.request(f.token("bob"), "GET", "/api/v1/organizations", nil, 200))
	if len(orgs.Data) != 2 {
		t.Fatal("other organization state damaged")
	}
}
func TestMemberLifecycleAuditFailureRollsBackAndOwnerProtected(t *testing.T) {
	f := setup(t)
	o := f.org("alice")
	p := f.project("alice", o)
	f.member(o, "bob", "admin")
	key := f.key("bob", o, p)
	path := orgPath(o) + "/members/enabled"
	ctx := context.Background()
	f.request(f.token("alice"), "PUT", path, lifecycleBody(f, "alice", 0, false, ""), 400)
	f.request(f.token("bob"), "PUT", path, lifecycleBody(f, "alice", 0, false, ""), 403)
	f.request(f.token("outsider"), "PUT", path, lifecycleBody(f, "bob", 0, false, ""), 404)
	if _, err := f.pool.Exec(ctx, `ALTER TABLE audit_events ADD CONSTRAINT reject_lifecycle CHECK(action<>'organization.member_disabled') NOT VALID`); err != nil {
		t.Fatal(err)
	}
	f.request(f.token("alice"), "PUT", path, lifecycleBody(f, "bob", 0, false, ""), 503)
	f.request(key.Secret, "GET", "/v1/models", nil, 200)
	f.request(f.token("bob"), "GET", projectPath(o, p)+"/usage", nil, 200)
	if _, err := f.pool.Exec(ctx, `ALTER TABLE audit_events DROP CONSTRAINT reject_lifecycle`); err != nil {
		t.Fatal(err)
	}
	f.request(f.token("alice"), "PUT", path, lifecycleBody(f, "bob", 0, false, ""), 200)
	if _, err := f.pool.Exec(ctx, `ALTER TABLE audit_events ADD CONSTRAINT reject_restore CHECK(action<>'organization.member_enabled') NOT VALID`); err != nil {
		t.Fatal(err)
	}
	f.request(f.token("alice"), "PUT", path, lifecycleBody(f, "bob", 1, true, "viewer"), 503)
	f.request(f.token("bob"), "GET", projectPath(o, p)+"/usage", nil, 404)
	if _, err := f.pool.Exec(ctx, `UPDATE organization_members SET enabled=false WHERE organization_id=$1 AND role='owner'`, o.ID); err == nil {
		t.Fatal("database allowed inactive organization owner")
	}
}
func TestMemberLifecycleFencesAlreadyWaitingKeyIssuance(t *testing.T) {
	f := setup(t)
	o := f.org("alice")
	p := f.project("alice", o)
	f.member(o, "bob", "admin")
	ctx := context.Background()
	// Hold the authoritative row so both possible waiters must recheck its newest
	// version. The disabled flag is changed while the new issuance waits FOR SHARE.
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `UPDATE organization_members SET enabled=false,lifecycle_revision=1 WHERE organization_id=$1 AND subject='bob'`, o.ID); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		close(started)
		_, err := f.store.CreateKey(ctx, identity.Principal{Issuer: f.issuer.URL, Subject: "bob"}, o.ID, p.ID, "late issuance", []string{"mock-text"}, time.Hour)
		finished <- err
	}()
	<-started
	waitForMemberBlocker(t, f, tx.Conn().PgConn().PID())
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-finished; !errors.Is(err, control.ErrNotFound) {
		t.Fatal("late credential issuance bypassed disabled member")
	}
	var count int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM project_keys WHERE organization_id=$1 AND creator_subject='bob' AND revoked_at IS NULL`, o.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("late live key escaped")
	}
	// The writer-first case is the real lifecycle API tested separately; the
	// row-lock race must reject the formerly authenticated principal itself.
}

func waitForMemberBlocker(t *testing.T, f fixture, pid uint32) uint32 {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var waiter uint32
		err := f.pool.QueryRow(context.Background(), `SELECT pid FROM pg_stat_activity WHERE $1::integer=ANY(pg_blocking_pids(pid)) LIMIT 1`, pid).Scan(&waiter)
		if err == nil {
			return waiter
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("expected database lock waiter not observed")
	return 0
}
func TestMemberLifecycleRevokesKeyThatCommitsBeforeOffboarding(t *testing.T) {
	f := setup(t)
	o := f.org("alice")
	p := f.project("alice", o)
	f.member(o, "bob", "admin")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	lock, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback(context.Background())
	if _, err := lock.Exec(ctx, `LOCK TABLE audit_events IN SHARE MODE`); err != nil {
		t.Fatal(err)
	}
	type result struct {
		key control.IssuedKey
		err error
	}
	issued := make(chan result, 1)
	go func() {
		key, err := f.store.CreateKey(ctx, identity.Principal{Issuer: f.issuer.URL, Subject: "bob"}, o.ID, p.ID, "racing key", []string{"mock-text"}, time.Hour)
		issued <- result{key, err}
	}()
	keyPID := waitForMemberBlocker(t, f, lock.Conn().PgConn().PID())
	disabled := make(chan error, 1)
	go func() {
		_, err := f.store.SetMemberEnabled(ctx, identity.Principal{Issuer: f.issuer.URL, Subject: "alice"}, o.ID, identity.Principal{Issuer: f.issuer.URL, Subject: "bob"}, 0, false, "", "offboarding races key issuance")
		disabled <- err
	}()
	waitForMemberBlocker(t, f, keyPID)
	if err := lock.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	key := <-issued
	if key.err != nil {
		t.Fatal(key.err)
	}
	if err := <-disabled; err != nil {
		t.Fatal(err)
	}
	f.request(key.key.Secret, "GET", "/v1/models", nil, 401)
}
func TestMemberLifecycleAllowsAlreadyAuthorizedInferenceToSettle(t *testing.T) {
	arrived, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	healthy := testutil.SupplierFixture(t)
	f := providerSetup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(arrived)
		select {
		case <-release:
			healthy.ServeHTTP(w, r)
		case <-r.Context().Done():
		}
	}))
	o, p, _ := providerProject(f)
	f.member(o, "bob", "admin")
	key := parse[control.IssuedKey](t, f.request(f.token("bob"), "POST", projectPath(o, p)+"/keys", map[string]any{"name": "in-flight", "models": []string{"aliyun/fixture"}, "ttl_seconds": 3600}, 201))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	body, _ := json.Marshal(realPayload("aliyun/fixture", false))
	req, err := http.NewRequestWithContext(ctx, "POST", f.server.URL+"/v1/chat/completions", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+key.Secret)
	finished := make(chan error, 1)
	go func() {
		res, err := f.server.Client().Do(req)
		if err == nil {
			defer res.Body.Close()
			if res.StatusCode != 200 {
				err = errors.New("in-flight request did not complete")
			}
		}
		finished <- err
	}()
	select {
	case <-arrived:
	case <-ctx.Done():
		t.Fatal("supplier was not dispatched")
	}
	f.request(f.token("alice"), "PUT", orgPath(o)+"/members/enabled", lifecycleBody(f, "bob", 0, false, ""), 200)
	f.request(key.Secret, "GET", "/v1/models", nil, 401)
	close(release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	requests := parse[struct {
		Data []control.RequestRecord `json:"data"`
	}](t, f.request(f.token("alice"), "GET", projectPath(o, p)+"/requests", nil, 200))
	if len(requests.Data) != 1 || requests.Data[0].State != "settled" || requests.Data[0].Actual == nil {
		t.Fatal("in-flight settlement lost or refunded")
	}
}
