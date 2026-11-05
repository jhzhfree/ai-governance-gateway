package control

import (
	"context"
	"github.com/jackc/pgx/v5"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
	"testing"
)

func TestDepartmentRepeatableReadCannotReuseRevokedAuthorization(t *testing.T) {
	pool, _, _ := testutil.Database(t)
	s := New(pool)
	ctx := context.Background()
	owner := identity.Principal{Issuer: "https://synthetic.invalid", Subject: "owner"}
	member := identity.Principal{Issuer: owner.Issuer, Subject: "member"}
	o, err := s.CreateOrganization(ctx, owner, "snapshot guard", "2026-11", 1000)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.CreateProject(ctx, owner, o.ID, "project", 500)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ChangeMember(ctx, owner, o.ID, "", Member{Principal: member, Role: "developer"}, nil); err != nil {
		t.Fatal(err)
	}
	c, err := s.SetDepartments(ctx, owner, o.ID, DepartmentConfiguration{Departments: []Department{{ID: "root", Name: "部门"}}, Grants: []DepartmentGrant{{DepartmentID: "root", Principal: member, Role: "developer"}}, Projects: []DepartmentProject{{DepartmentID: "root", ProjectID: p.ID}}}, "initial")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT count(*) FROM department_configurations`); err != nil {
		t.Fatal(err)
	}
	c.Grants = []DepartmentGrant{}
	if _, err := s.SetDepartments(ctx, owner, o.ID, c, "revoke after old snapshot"); err != nil {
		t.Fatal(err)
	}
	if role, err := projectRole(ctx, tx, member, o.ID, p.ID); err == nil {
		t.Fatalf("revoked authorization reused from old snapshot: %s", role)
	}
}
