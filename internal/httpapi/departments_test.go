package httpapi_test

import (
	"context"
	"encoding/json"
	"net/url"
	"testing"
	"time"

	"github.com/jhzhfree/ai-governance-gateway/internal/control"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
)

func departmentFixture(f fixture, o control.Organization, p control.Project) control.DepartmentConfiguration {
	return control.DepartmentConfiguration{Departments: []control.Department{{ID: "engineering", Name: "研发", ParentID: ""}, {ID: "platform", Name: "平台", ParentID: "engineering"}, {ID: "sales", Name: "销售", ParentID: ""}}, Grants: []control.DepartmentGrant{{DepartmentID: "engineering", Principal: identity.Principal{Issuer: f.issuer.URL, Subject: "bob"}, Role: "developer"}}, Projects: []control.DepartmentProject{{ProjectID: p.ID, DepartmentID: "platform"}}}
}
func putDepartments(f fixture, o control.Organization, c control.DepartmentConfiguration, status int) control.DepartmentConfiguration {
	result := f.request(f.token("alice"), "PUT", orgPath(o)+"/departments", map[string]any{"configuration": c, "reason": "synthetic department review"}, status)
	if status != 200 {
		return c
	}
	return parse[control.DepartmentConfiguration](f.t, result)
}
func TestDepartmentInheritanceVisibilityAndDirectGrants(t *testing.T) {
	f := setup(t)
	o := f.org("alice")
	p := f.project("alice", o)
	sibling := f.project("alice", o)
	f.member(o, "bob", "developer")
	c := departmentFixture(f, o, p)
	c = putDepartments(f, o, c, 200)
	f.request(f.token("bob"), "GET", projectPath(o, p)+"/usage", nil, 200)
	f.request(f.token("bob"), "GET", projectPath(o, sibling)+"/usage", nil, 404)
	detail := parse[control.ProjectDetails](t, f.request(f.token("bob"), "GET", projectPath(o, p), nil, 200))
	if detail.Role != "developer" {
		t.Fatal("ancestor grant not inherited")
	}
	page := parse[struct {
		Data []control.Project `json:"data"`
	}](t, f.request(f.token("bob"), "GET", orgPath(o)+"/projects?limit=1", nil, 200))
	if len(page.Data) != 1 || page.Data[0].ID != p.ID || page.Data[0].Role != "developer" {
		t.Fatal("project discovery disagrees with authorization")
	}
	f.request(f.token("bob"), "GET", orgPath(o)+"/departments", nil, 403)
	c.Grants[0].Role = "viewer"
	c = putDepartments(f, o, c, 200)
	f.request(f.token("bob"), "POST", projectPath(o, p)+"/keys", map[string]any{"name": "refused", "models": []string{"mock-text"}, "ttl_seconds": 3600}, 403)
	f.request(f.token("alice"), "PUT", projectPath(o, p)+"/members", map[string]string{"issuer": f.issuer.URL, "subject": "bob", "role": "developer"}, 204)
	detail = parse[control.ProjectDetails](t, f.request(f.token("bob"), "GET", projectPath(o, p), nil, 200))
	if detail.Role != "developer" {
		t.Fatal("direct grant lost")
	}
	c.Grants[0].Role = "admin"
	c = putDepartments(f, o, c, 200)
	// Department admin is scoped to the project, never organization membership administration or plaintext audit.
	f.request(f.token("bob"), "GET", projectPath(o, p)+"/keys", nil, 200)
	f.request(f.token("bob"), "PUT", orgPath(o)+"/members", map[string]string{"issuer": f.issuer.URL, "subject": "charlie", "role": "admin"}, 403)
	f.request(f.token("bob"), "GET", projectPath(o, p)+"/content-audit/records", nil, 403)
	members := parse[struct {
		Data []control.MemberRecord `json:"data"`
	}](t, f.request(f.token("alice"), "GET", projectPath(o, p)+"/members", nil, 200))
	for _, m := range members.Data {
		if m.Subject == "bob" && m.EffectiveRole != "admin" {
			t.Fatal("directory has stale effective role")
		}
	}
	f.member(o, "bob", "viewer")
	detail = parse[control.ProjectDetails](t, f.request(f.token("bob"), "GET", projectPath(o, p), nil, 200))
	if detail.Role != "viewer" {
		t.Fatal("organization viewer ceiling bypassed")
	}
	// Moving a project to a sibling does not inherit the old subtree's grants.
	f.member(o, "bob", "developer")
	f.request(f.token("alice"), "DELETE", projectPath(o, p)+"/members", map[string]string{"issuer": f.issuer.URL, "subject": "bob", "expected_role": "developer"}, 204)
	c.Projects[0].DepartmentID = "sales"
	putDepartments(f, o, c, 200)
	f.request(f.token("bob"), "GET", projectPath(o, p)+"/usage", nil, 404)
}
func TestDepartmentValidationIsolationAndAuditRollback(t *testing.T) {
	f := setup(t)
	o := f.org("alice")
	p := f.project("alice", o)
	f.member(o, "bob", "developer")
	c := putDepartments(f, o, departmentFixture(f, o, p), 200)
	copyConfig := func() control.DepartmentConfiguration {
		raw, _ := json.Marshal(c)
		var v control.DepartmentConfiguration
		_ = json.Unmarshal(raw, &v)
		return v
	}
	cycle := copyConfig()
	cycle.Departments[0].ParentID = "platform"
	putDepartments(f, o, cycle, 400)
	duplicate := copyConfig()
	duplicate.Grants = append(duplicate.Grants, duplicate.Grants[0])
	putDepartments(f, o, duplicate, 400)
	missing := copyConfig()
	missing.Departments[0].ParentID = "missing"
	putDepartments(f, o, missing, 400)
	deep := copyConfig()
	deep.Departments = []control.Department{}
	for i, id := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"} {
		parent := ""
		if i > 0 {
			parent = string(rune('a' + i - 1))
		}
		deep.Departments = append(deep.Departments, control.Department{ID: id, Name: id, ParentID: parent})
	}
	deep.Grants = []control.DepartmentGrant{}
	deep.Projects = []control.DepartmentProject{}
	putDepartments(f, o, deep, 400)
	stale := copyConfig()
	stale.Revision = 0
	putDepartments(f, o, stale, 409)
	other := f.org("alice")
	otherProject := f.project("alice", other)
	foreign := copyConfig()
	foreign.Projects[0].ProjectID = otherProject.ID
	putDepartments(f, o, foreign, 404)
	outsider := copyConfig()
	outsider.Grants[0].Subject = "outsider"
	putDepartments(f, o, outsider, 404)
	if _, err := f.pool.Exec(context.Background(), `ALTER TABLE audit_events ADD CONSTRAINT reject_departments CHECK(action<>'organization.departments_changed') NOT VALID`); err != nil {
		t.Fatal(err)
	}
	changed := copyConfig()
	changed.Projects = []control.DepartmentProject{}
	putDepartments(f, o, changed, 503)
	actual := parse[control.DepartmentConfiguration](t, f.request(f.token("alice"), "GET", orgPath(o)+"/departments", nil, 200))
	if actual.Revision != 1 || len(actual.Projects) != 1 {
		t.Fatal("audit failure leaked configuration")
	}
	f.request(f.token("bob"), "GET", projectPath(o, p)+"/usage", nil, 200)
	history := parse[struct {
		Data []control.DepartmentVersion `json:"data"`
	}](t, f.request(f.token("alice"), "GET", orgPath(o)+"/departments/history", nil, 200))
	if len(history.Data) != 1 {
		t.Fatal("failed update leaked history")
	}
}
func TestDepartmentOffboardingRemovesGrantsAndFencesStaleRestore(t *testing.T) {
	f := setup(t)
	o := f.org("alice")
	p := f.project("alice", o)
	f.member(o, "bob", "developer")
	c := putDepartments(f, o, departmentFixture(f, o, p), 200)
	key := f.key("bob", o, p)
	event := parse[control.MemberLifecycleEvent](t, f.request(f.token("alice"), "PUT", orgPath(o)+"/members/enabled", lifecycleBody(f, "bob", 0, false, ""), 200))
	if event.RemovedDepartmentGrants != 1 {
		t.Fatal("offboarding did not count department grant")
	}
	current := parse[control.DepartmentConfiguration](t, f.request(f.token("alice"), "GET", orgPath(o)+"/departments", nil, 200))
	if current.Revision != c.Revision+1 || len(current.Grants) != 0 {
		t.Fatal("offboarding failed to advance department revision")
	}
	putDepartments(f, o, c, 409)
	c.Revision = current.Revision
	putDepartments(f, o, c, 409)
	f.request(key.Secret, "GET", "/v1/models", nil, 401)
	f.request(f.token("alice"), "PUT", orgPath(o)+"/members/enabled", lifecycleBody(f, "bob", 1, true, "developer"), 200)
	f.request(f.token("bob"), "GET", projectPath(o, p)+"/usage", nil, 404)
	path := orgPath(o) + "/members/lifecycle?issuer=" + url.QueryEscape(f.issuer.URL) + "&subject=bob"
	history := parse[struct {
		Data []control.MemberLifecycleEvent `json:"data"`
	}](t, f.request(f.token("alice"), "GET", path, nil, 200))
	if history.Data[0].RemovedDepartmentGrants != 1 {
		t.Fatal("offboarding history lost department count")
	}
}
func TestDepartmentAuthorizationBlocksConcurrentConfigurationWrite(t *testing.T) {
	f := setup(t)
	o := f.org("alice")
	p := f.project("alice", o)
	f.member(o, "bob", "developer")
	c := putDepartments(f, o, departmentFixture(f, o, p), 200)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(ctx, `LOCK TABLE audit_events IN SHARE MODE`); err != nil {
		t.Fatal(err)
	}
	type keyResult struct {
		key control.IssuedKey
		err error
	}
	issued := make(chan keyResult, 1)
	go func() {
		key, err := f.store.CreateKey(ctx, identity.Principal{Issuer: f.issuer.URL, Subject: "bob"}, o.ID, p.ID, "department race", []string{"mock-text"}, time.Hour)
		issued <- keyResult{key, err}
	}()
	keyPID := waitForMemberBlocker(t, f, tx.Conn().PgConn().PID())
	c.Projects = []control.DepartmentProject{}
	done := make(chan error, 1)
	go func() {
		_, err := f.store.SetDepartments(ctx, identity.Principal{Issuer: f.issuer.URL, Subject: "alice"}, o.ID, c, "concurrent authorization test")
		done <- err
	}()
	waitForMemberBlocker(t, f, keyPID)
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	key := <-issued
	if key.err != nil {
		t.Fatal(key.err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	f.request(f.token("bob"), "GET", projectPath(o, p)+"/usage", nil, 404)
	// A department edit follows the existing project-role semantics: issued keys remain independent.
	f.request(key.key.Secret, "GET", "/v1/models", nil, 200)
	f.request(f.token("bob"), "POST", projectPath(o, p)+"/keys", map[string]any{"name": "after revoke", "models": []string{"mock-text"}, "ttl_seconds": 3600}, 404)
}
