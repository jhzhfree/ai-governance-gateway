package httpapi_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/jhzhfree/ai-governance-gateway/internal/control"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
)

type pendingOwnership struct {
	Transfer *control.OwnershipTransfer `json:"transfer"`
}

func handoffBody(f fixture, subject, role string) any {
	return map[string]any{"target": identity.Principal{Issuer: f.issuer.URL, Subject: subject}, "expected_role": role, "reason": "synthetic ownership handoff"}
}
func TestOwnershipRecipientConfirmationAndImmediateRoles(t *testing.T) {
	f := setup(t)
	o := f.org("alice")
	p := f.project("alice", o)
	key := f.key("alice", o, p)
	f.member(o, "bob", "developer")
	f.member(o, "observer", "viewer")
	base := orgPath(o) + "/ownership-transfer"
	f.request(f.token("outsider"), "GET", base, nil, 404)
	f.request(f.token("alice"), "POST", base, handoffBody(f, "alice", "admin"), 400)
	f.request(f.token("bob"), "POST", base, handoffBody(f, "observer", "viewer"), 403)
	f.request(f.token("alice"), "POST", base, handoffBody(f, "bob", "viewer"), 409)
	transfer := parse[control.OwnershipTransfer](t, f.request(f.token("alice"), "POST", base, handoffBody(f, "bob", "developer"), 201))
	f.request(f.token("alice"), "POST", base, handoffBody(f, "observer", "viewer"), 409)
	if parse[pendingOwnership](t, f.request(f.token("observer"), "GET", base, nil, 200)).Transfer != nil {
		t.Fatal("handoff exposed to unrelated member")
	}
	if parse[pendingOwnership](t, f.request(f.token("bob"), "GET", base, nil, 200)).Transfer == nil {
		t.Fatal("recipient cannot see request")
	}
	f.request(f.token("alice"), "POST", base+"/"+transfer.ID+"/accept", nil, 403)
	f.request(f.token("bob"), "POST", base+"/"+transfer.ID+"/cancel", nil, 403)
	f.request(f.token("bob"), "POST", base+"/"+transfer.ID+"/accept", nil, 204)
	f.request(f.token("bob"), "POST", base+"/"+transfer.ID+"/accept", nil, 409)
	f.request(f.token("alice"), "PUT", orgPath(o)+"/members", map[string]string{"issuer": f.issuer.URL, "subject": "observer", "role": "admin"}, 403)
	f.request(f.token("bob"), "PUT", orgPath(o)+"/members", map[string]string{"issuer": f.issuer.URL, "subject": "observer", "role": "admin"}, 204)
	rows := parse[struct {
		Data []control.MemberRecord `json:"data"`
	}](t, f.request(f.token("bob"), "GET", orgPath(o)+"/members", nil, 200)).Data
	owners := 0
	for _, m := range rows {
		if m.Role == "owner" {
			owners++
			if m.Subject != "bob" {
				t.Fatal("wrong owner")
			}
		}
		if m.Subject == "alice" && m.Role != "admin" {
			t.Fatal("former owner lost admin continuity")
		}
	}
	if owners != 1 {
		t.Fatal("ownership invariant broken")
	}
	f.request(key.Secret, "GET", "/v1/models", nil, 200)
	if parse[pendingOwnership](t, f.request(f.token("bob"), "GET", base, nil, 200)).Transfer != nil {
		t.Fatal("completed handoff still pending")
	}
}
func TestOwnershipExpiryRoleChangeAndAuditRollback(t *testing.T) {
	f := setup(t)
	o := f.org("alice")
	f.member(o, "bob", "developer")
	base := orgPath(o) + "/ownership-transfer"
	ctx := context.Background()
	v := parse[control.OwnershipTransfer](t, f.request(f.token("alice"), "POST", base, handoffBody(f, "bob", "developer"), 201))
	f.member(o, "bob", "admin")
	f.request(f.token("bob"), "POST", base+"/"+v.ID+"/accept", nil, 409)
	f.request(f.token("alice"), "POST", base+"/"+v.ID+"/cancel", nil, 204)
	v = parse[control.OwnershipTransfer](t, f.request(f.token("alice"), "POST", base, handoffBody(f, "bob", "admin"), 201))
	if _, err := f.pool.Exec(ctx, `UPDATE organization_ownership_transfers SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, v.ID); err != nil {
		t.Fatal(err)
	}
	f.request(f.token("bob"), "POST", base+"/"+v.ID+"/accept", nil, 409)
	if parse[pendingOwnership](t, f.request(f.token("bob"), "GET", base, nil, 200)).Transfer != nil {
		t.Fatal("expired handoff shown active")
	}
	v = parse[control.OwnershipTransfer](t, f.request(f.token("alice"), "POST", base, handoffBody(f, "bob", "admin"), 201))
	if _, err := f.pool.Exec(ctx, `ALTER TABLE audit_events ADD CONSTRAINT reject_handoff CHECK(action<>'organization.ownership_accepted') NOT VALID`); err != nil {
		t.Fatal(err)
	}
	f.request(f.token("bob"), "POST", base+"/"+v.ID+"/accept", nil, 503)
	var owner string
	if err := f.pool.QueryRow(ctx, `SELECT subject FROM organization_members WHERE organization_id=$1 AND role='owner'`, o.ID).Scan(&owner); err != nil || owner != "alice" {
		t.Fatal("audit failure transferred ownership")
	}
	if parse[pendingOwnership](t, f.request(f.token("alice"), "GET", base, nil, 200)).Transfer == nil {
		t.Fatal("audit failure consumed request")
	}
	if _, err := f.pool.Exec(ctx, `ALTER TABLE audit_events DROP CONSTRAINT reject_handoff`); err != nil {
		t.Fatal(err)
	}
	f.request(f.token("bob"), "POST", base+"/"+v.ID+"/accept", nil, 204)
}
func TestOwnershipConcurrentDecisionAndDatabaseInvariant(t *testing.T) {
	f := setup(t)
	o := f.org("alice")
	f.member(o, "bob", "developer")
	ctx := context.Background()
	v := parse[control.OwnershipTransfer](t, f.request(f.token("alice"), "POST", orgPath(o)+"/ownership-transfer", handoffBody(f, "bob", "developer"), 201))
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	start := make(chan struct{})
	for _, item := range []struct {
		who    string
		accept bool
	}{{"alice", false}, {"bob", true}} {
		wg.Add(1)
		go func(who string, accept bool) {
			defer wg.Done()
			<-start
			errs <- f.store.FinishOwnership(ctx, identity.Principal{Issuer: f.issuer.URL, Subject: who}, o.ID, v.ID, accept)
		}(item.who, item.accept)
	}
	close(start)
	wg.Wait()
	close(errs)
	success, conflict := 0, 0
	for err := range errs {
		if err == nil {
			success++
		} else if errors.Is(err, control.ErrMemberConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal("concurrent decision not serialized")
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO organization_members(organization_id,issuer,subject,role) VALUES($1,$2,'duplicate-owner','owner')`, o.ID, f.issuer.URL); err == nil {
		t.Fatal("second owner accepted by database")
	}
}
