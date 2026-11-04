package control_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jhzhfree/ai-governance-gateway/internal/control"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
	"github.com/jhzhfree/ai-governance-gateway/internal/localauth"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
)

func TestOwnershipLocalRecipientMustBeActiveAndInitialized(t *testing.T) {
	pool, _, _ := testutil.Database(t)
	ctx := context.Background()
	local, err := localauth.New(pool, "https://local.synthetic.invalid", false)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := local.Initialize(ctx, "admin", "synthetic-initial-passphrase")
	if err != nil {
		t.Fatal(err)
	}
	actor := local.Principal(admin)
	if err := local.ChangePassword(ctx, actor, "synthetic-initial-passphrase", "synthetic-updated-passphrase"); err != nil {
		t.Fatal(err)
	}
	worker, err := local.Create(ctx, actor, "worker", "synthetic-initial-passphrase", false)
	if err != nil {
		t.Fatal(err)
	}
	target := local.Principal(worker)
	store := control.New(pool).WithLocalIdentity(local)
	org, err := store.CreateOrganization(ctx, actor, "handoff fixture", "2026-11", 100)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RequestOwnership(ctx, actor, org.ID, target, "developer", "not in this organization"); !errors.Is(err, control.ErrNotFound) {
		t.Fatal("nonmember account status exposed")
	}
	if err := store.PutOrganizationMember(ctx, actor, org.ID, control.Member{Principal: target, Role: "developer"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RequestOwnership(ctx, actor, org.ID, target, "developer", "pending initialization"); !errors.Is(err, identity.ErrForbidden) {
		t.Fatal("uninitialized local recipient accepted")
	}
	if err := local.ChangePassword(ctx, target, "synthetic-initial-passphrase", "synthetic-updated-passphrase"); err != nil {
		t.Fatal(err)
	}
	v, err := store.RequestOwnership(ctx, actor, org.ID, target, "developer", "active recipient")
	if err != nil {
		t.Fatal(err)
	}
	current, err := local.User(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	if err := local.SetUser(ctx, actor, target.Subject, current.Revision, false, false); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishOwnership(ctx, target, org.ID, v.ID, true); !errors.Is(err, identity.ErrForbidden) {
		t.Fatal("late authenticated disabled recipient gained ownership")
	}
	users, err := local.Users(ctx, actor, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, user := range users {
		if user.ID == target.Subject {
			current = user
		}
	}
	if err := local.SetUser(ctx, actor, target.Subject, current.Revision, true, false); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishOwnership(ctx, target, org.ID, v.ID, true); err != nil {
		t.Fatal(err)
	}
}
