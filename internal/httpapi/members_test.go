package httpapi_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/jhzhfree/ai-governance-gateway/internal/control"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
)

type memberPage struct {
	Data []control.MemberRecord `json:"data"`
	Next string                 `json:"next_cursor"`
}

func TestMemberDirectoryRolesAndImmediateProjectAccess(t *testing.T) {
	f := setup(t)
	o := f.org("alice")
	p := f.project("alice", o)
	org := orgPath(o) + "/members"
	project := projectPath(o, p) + "/members"
	f.member(o, "bob", "developer")
	f.member(o, "viewer", "viewer")
	f.member(o, "admin", "admin")
	f.request(f.token("bob"), "GET", org, nil, 403)
	f.request(f.token("outsider"), "GET", org, nil, 404)
	f.request(f.token("admin"), "GET", org, nil, 200)
	page := parse[memberPage](t, f.request(f.token("alice"), "GET", project, nil, 200))
	found := false
	for _, m := range page.Data {
		if m.Subject == "bob" {
			found = true
			if m.EffectiveRole != "none" || m.ProjectRole != nil {
				t.Fatal("unassigned developer inherited access")
			}
		}
		if m.Subject == "viewer" && m.EffectiveRole != "viewer" {
			t.Fatal("viewer inheritance wrong")
		}
	}
	if !found {
		t.Fatal("organization member not visible for project grant")
	}
	f.request(f.token("bob"), "GET", projectPath(o, p), nil, 404)
	grant := map[string]any{"issuer": f.issuer.URL, "subject": "bob", "role": "developer", "expected_role": ""}
	f.request(f.token("admin"), "PUT", project, grant, 204)
	f.request(f.token("bob"), "GET", projectPath(o, p), nil, 200)
	grant["expected_role"] = "developer"
	grant["role"] = "viewer"
	f.request(f.token("alice"), "PUT", project, grant, 204)
	f.request(f.token("bob"), "POST", projectPath(o, p)+"/keys", map[string]any{"name": "forbidden", "models": []string{"mock-text"}, "ttl_seconds": 3600}, 403)
	f.request(f.token("alice"), "DELETE", project, map[string]any{"issuer": f.issuer.URL, "subject": "bob", "expected_role": "viewer"}, 204)
	f.request(f.token("bob"), "GET", projectPath(o, p), nil, 404)
	f.request(f.token("alice"), "DELETE", project, map[string]any{"issuer": f.issuer.URL, "subject": "bob", "expected_role": "viewer"}, 204)
	f.request(f.token("admin"), "PUT", org, map[string]any{"issuer": f.issuer.URL, "subject": "new", "role": "admin"}, 403)
	f.request(f.token("alice"), "PUT", org, map[string]any{"issuer": f.issuer.URL, "subject": "alice", "role": "viewer", "expected_role": "owner"}, 403)
}
func TestMemberPaginationRoleConflictsAndAuditRollback(t *testing.T) {
	f := setup(t)
	o := f.org("alice")
	p := f.project("alice", o)
	org := orgPath(o) + "/members"
	project := projectPath(o, p) + "/members"
	f.member(o, "bob", "developer")
	first := parse[memberPage](t, f.request(f.token("alice"), "GET", org+"?limit=1", nil, 200))
	if first.Next == "" {
		t.Fatal("cursor absent")
	}
	next := parse[memberPage](t, f.request(f.token("alice"), "GET", org+"?limit=1&after="+first.Next, nil, 200))
	if len(next.Data) != 1 || next.Data[0].Principal == first.Data[0].Principal {
		t.Fatal("member paging repeated row")
	}
	f.request(f.token("alice"), "GET", org+"?after=bad", nil, 400)
	f.request(f.token("alice"), "GET", org+"?limit=0", nil, 400)
	f.request(f.token("alice"), "PUT", org, map[string]any{"issuer": f.issuer.URL, "subject": "bob", "role": "viewer", "expected_role": ""}, 409)
	if _, err := f.pool.Exec(context.Background(), `ALTER TABLE audit_events ADD CONSTRAINT reject_member CHECK(action<>'project.member_changed') NOT VALID`); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"issuer": f.issuer.URL, "subject": "bob", "role": "admin", "expected_role": ""}
	f.request(f.token("alice"), "PUT", project, body, 503)
	f.request(f.token("bob"), "GET", projectPath(o, p), nil, 404)
	if _, err := f.pool.Exec(context.Background(), `ALTER TABLE audit_events DROP CONSTRAINT reject_member`); err != nil {
		t.Fatal(err)
	}
	f.request(f.token("alice"), "PUT", project, body, 204)
	if _, err := f.pool.Exec(context.Background(), `ALTER TABLE audit_events ADD CONSTRAINT reject_removal CHECK(action<>'project.member_removed') NOT VALID`); err != nil {
		t.Fatal(err)
	}
	f.request(f.token("alice"), "DELETE", project, map[string]any{"issuer": f.issuer.URL, "subject": "bob", "expected_role": "admin"}, 503)
	f.request(f.token("bob"), "GET", projectPath(o, p), nil, 200)
}
func TestConcurrentRoleComparisonPreventsLostUpdate(t *testing.T) {
	f := setup(t)
	o := f.org("alice")
	f.member(o, "bob", "developer")
	p := identity.Principal{Issuer: f.issuer.URL, Subject: "alice"}
	expected := "developer"
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, role := range []string{"admin", "viewer"} {
		wg.Add(1)
		go func(role string) {
			defer wg.Done()
			errs <- f.store.ChangeMember(context.Background(), p, o.ID, "", control.Member{Principal: identity.Principal{Issuer: f.issuer.URL, Subject: "bob"}, Role: role}, &expected)
		}(role)
	}
	wg.Wait()
	close(errs)
	successes, conflicts := 0, 0
	for err := range errs {
		if err == nil {
			successes++
		} else if errors.Is(err, control.ErrMemberConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatal("role comparison lost a concurrent update")
	}
}
