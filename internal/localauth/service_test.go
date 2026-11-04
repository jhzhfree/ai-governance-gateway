package localauth_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jhzhfree/ai-governance-gateway/internal/control"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
	"github.com/jhzhfree/ai-governance-gateway/internal/localauth"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
)

func setup(t *testing.T) (*localauth.Service, context.Context) {
	t.Helper()
	pool, _, _ := testutil.Database(t)
	service, err := localauth.New(pool, "https://local.example", false)
	if err != nil {
		t.Fatal(err)
	}
	return service, context.Background()
}
func TestLocalInitializationPasswordsAndImmediateTokenRevocation(t *testing.T) {
	service, ctx := setup(t)
	initial := "synthetic-initial-passphrase"
	updated := "synthetic-updated-passphrase"
	admin, err := service.Initialize(ctx, "admin", initial)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Initialize(ctx, "other", initial); !errors.Is(err, localauth.ErrConflict) {
		t.Fatal("initializer reset existing deployment")
	}
	if _, err := service.Token(ctx, "admin", initial); !errors.Is(err, localauth.ErrPasswordChange) {
		t.Fatal("initial password issued API token")
	}
	if err := service.ChangePassword(ctx, service.Principal(admin), initial, updated); err != nil {
		t.Fatal(err)
	}
	token, err := service.Token(ctx, "admin", updated)
	if err != nil {
		t.Fatal(err)
	}
	if token.ExpiresAt.After(time.Now().Add(2*time.Hour + time.Second)) {
		t.Fatal("token outlives two-hour bound")
	}
	if p, err := service.Authenticate(ctx, token.AccessToken); err != nil || p.Subject != admin.ID {
		t.Fatal("token did not authenticate")
	}
	if err := service.RevokeUser(ctx, service.Principal(admin), admin.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Authenticate(ctx, token.AccessToken); !errors.Is(err, identity.ErrUnauthenticated) {
		t.Fatal("revoked token still authenticates")
	}
	if strings.Contains(token.String(), token.AccessToken) {
		t.Fatal("token printed in diagnostic")
	}
}
func TestLocalDisabledAccountsResetAndAdministratorProtection(t *testing.T) {
	service, ctx := setup(t)
	first := "synthetic-initial-passphrase"
	next := "synthetic-changed-passphrase"
	admin, err := service.Initialize(ctx, "admin", first)
	if err != nil {
		t.Fatal(err)
	}
	principal := service.Principal(admin)
	if err := service.ChangePassword(ctx, principal, first, next); err != nil {
		t.Fatal(err)
	}
	current, err := service.User(ctx, principal)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.SetUser(ctx, principal, admin.ID, current.Revision, false, false); !errors.Is(err, localauth.ErrConflict) {
		t.Fatal("last current administrator disabled itself")
	}
	user, err := service.Create(ctx, principal, "worker", first, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ChangePassword(ctx, service.Principal(user), first, next); err != nil {
		t.Fatal(err)
	}
	token, err := service.Token(ctx, "worker", next)
	if err != nil {
		t.Fatal(err)
	}
	user, err = service.User(ctx, service.Principal(user))
	if err != nil {
		t.Fatal(err)
	}
	if err := service.SetUser(ctx, principal, user.ID, user.Revision, false, false); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Authenticate(ctx, token.AccessToken); !errors.Is(err, identity.ErrUnauthenticated) {
		t.Fatal("disabled account kept valid token")
	}
	if _, err := service.CheckPassword(ctx, "worker", next); !errors.Is(err, identity.ErrUnauthenticated) {
		t.Fatal("disabled password login succeeded")
	}
	if _, err := service.Recover(ctx, "worker", first); !errors.Is(err, localauth.ErrInvalid) {
		t.Fatal("offline recovery promoted ordinary account")
	}
	if _, err := service.Recover(ctx, "admin", first); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Token(ctx, "admin", first); !errors.Is(err, localauth.ErrPasswordChange) {
		t.Fatal("recovered admin skipped forced change")
	}
}
func TestLocalLoginThrottleAndStaleSessionProtection(t *testing.T) {
	service, ctx := setup(t)
	password := "synthetic-initial-passphrase"
	user, err := service.Initialize(ctx, "admin", password)
	if err != nil {
		t.Fatal(err)
	}
	for range 5 {
		if _, err := service.CheckPassword(ctx, "missing", "not-the-password"); !errors.Is(err, identity.ErrUnauthenticated) {
			t.Fatal("unexpected missing-account result")
		}
	}
	if _, err := service.CheckPassword(ctx, "missing", "not-the-password"); !errors.Is(err, localauth.ErrRateLimited) {
		t.Fatal("unknown account bypassed throttle")
	}
	if err := service.ChangePassword(ctx, service.Principal(user), password, "synthetic-fresh-passphrase"); err != nil {
		t.Fatal(err)
	}
	if err := service.OpenSession(ctx, user, "stale-secret-not-to-be-used-for-any-request", time.Now().Add(time.Hour), nil); !errors.Is(err, identity.ErrUnauthenticated) {
		t.Fatal("password reset race allowed a stale login session")
	}
}

func TestLocalDisableRevokesCreatedProjectKeysAtomically(t *testing.T) {
	pool, _, _ := testutil.Database(t)
	ctx := context.Background()
	s, err := localauth.New(pool, "https://local.example", false)
	if err != nil {
		t.Fatal(err)
	}
	first := "synthetic-initial-password"
	next := "synthetic-next-password"
	admin, err := s.Initialize(ctx, "admin", first)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ChangePassword(ctx, s.Principal(admin), first, next); err != nil {
		t.Fatal(err)
	}
	worker, err := s.Create(ctx, s.Principal(admin), "worker", first, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ChangePassword(ctx, s.Principal(worker), first, next); err != nil {
		t.Fatal(err)
	}
	store := control.New(pool).WithLocalIdentity(s)
	org, err := store.CreateOrganization(ctx, s.Principal(worker), "Employee test", "2026-11", 100)
	if err != nil {
		t.Fatal(err)
	}
	project, err := store.CreateProject(ctx, s.Principal(worker), org.ID, "Employee project", 50)
	if err != nil {
		t.Fatal(err)
	}
	key, err := store.CreateKey(ctx, s.Principal(worker), org.ID, project.ID, "Employee fixture key", []string{"mock-text"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	current, err := s.User(ctx, s.Principal(worker))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE local_security_events ADD CONSTRAINT reject_disable CHECK(action<>'local.user_changed') NOT VALID`); err != nil {
		t.Fatal(err)
	}
	if err := s.SetUser(ctx, s.Principal(admin), worker.ID, current.Revision, false, false); err == nil {
		t.Fatal("disable succeeded without audit")
	}
	if _, err := store.ResolveKey(ctx, key.Secret, "mock-text"); err != nil {
		t.Fatal("failed disable revoked project key")
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE local_security_events DROP CONSTRAINT reject_disable`); err != nil {
		t.Fatal(err)
	}
	if err := s.SetUser(ctx, s.Principal(admin), worker.ID, current.Revision, false, false); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ResolveKey(ctx, key.Secret, "mock-text"); !errors.Is(err, identity.ErrUnauthenticated) {
		t.Fatal("disabled employee retained a project credential")
	}
	if _, err := store.CreateKey(ctx, s.Principal(worker), org.ID, project.ID, "late credential", []string{"mock-text"}, time.Hour); !errors.Is(err, identity.ErrForbidden) {
		t.Fatal("disabled identity issued a late project credential")
	}

}

func TestLocalPasswordResetCASAndJournalRollback(t *testing.T) {
	pool, _, _ := testutil.Database(t)
	ctx := context.Background()
	s, err := localauth.New(pool, "https://local.example", false)
	if err != nil {
		t.Fatal(err)
	}
	initial := "synthetic-initial-password"
	next := "synthetic-changed-password"
	admin, err := s.Initialize(ctx, "admin", initial)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ChangePassword(ctx, s.Principal(admin), initial, next); err != nil {
		t.Fatal(err)
	}
	user, err := s.Create(ctx, s.Principal(admin), "worker", initial, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ResetPassword(ctx, s.Principal(admin), user.ID, next, user.Revision+1); !errors.Is(err, localauth.ErrConflict) {
		t.Fatal("stale password reset overwrote credentials")
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE local_security_events ADD CONSTRAINT reject_reset CHECK(action<>'local.password_reset') NOT VALID`); err != nil {
		t.Fatal(err)
	}
	if err := s.ResetPassword(ctx, s.Principal(admin), user.ID, next, user.Revision); err == nil {
		t.Fatal("password reset succeeded without journal")
	}
	if _, err := s.CheckPassword(ctx, "worker", initial); err != nil {
		t.Fatal("failed reset replaced previous password")
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE local_security_events DROP CONSTRAINT reject_reset`); err != nil {
		t.Fatal(err)
	}
	if err := s.ResetPassword(ctx, s.Principal(admin), user.ID, next, user.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Token(ctx, "worker", next); !errors.Is(err, localauth.ErrPasswordChange) {
		t.Fatal("reset did not force password change")
	}
}
