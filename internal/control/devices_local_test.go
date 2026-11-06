package control_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"github.com/jhzhfree/ai-governance-gateway/internal/control"
	"github.com/jhzhfree/ai-governance-gateway/internal/deviceproof"
	"github.com/jhzhfree/ai-governance-gateway/internal/localauth"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
	"net/http"
	"testing"
)

func TestDeviceLocalAccountRevocationAndRestoreDoNotRevive(t *testing.T) {
	pool, _, _ := testutil.Database(t)
	ctx := context.Background()
	origin := "https://local.synthetic.invalid"
	local, err := localauth.New(pool, origin, false)
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
	owner := local.Principal(worker)
	if err := local.ChangePassword(ctx, owner, "synthetic-initial-passphrase", "synthetic-updated-passphrase"); err != nil {
		t.Fatal(err)
	}
	store := control.New(pool).WithLocalIdentity(local).WithDeviceOrigin(origin)
	org, err := store.CreateOrganization(ctx, actor, "device local", "2026-11", 100)
	if err != nil {
		t.Fatal(err)
	}
	project, err := store.CreateProject(ctx, actor, org.ID, "device app", 50)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutOrganizationMember(ctx, actor, org.ID, control.Member{Principal: owner, Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	d, err := store.RegisterDevice(ctx, owner, org.ID, "Linux", deviceproof.Public(pub))
	if err != nil {
		t.Fatal(err)
	}
	d, err = store.ChangeDevice(ctx, actor, org.ID, d.ID, 0, "active", "manual confirmation")
	if err != nil {
		t.Fatal(err)
	}
	issue := func() control.IssuedDeviceSession {
		proof, _ := deviceproof.Sign(key, "POST", origin+"/api/v1/organizations/"+org.ID+"/projects/"+project.ID+"/devices/"+d.ID+"/sessions", "")
		v, err := store.IssueDeviceSession(ctx, owner, org.ID, project.ID, d.ID, proof, []string{"mock-text"})
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	verify := func(v control.IssuedDeviceSession) error {
		r, _ := http.NewRequest("GET", origin+"/v1/models", nil)
		proof, _ := deviceproof.Sign(key, "GET", origin+"/v1/models", v.Token)
		r.Header.Set("DPoP", proof)
		_, err := store.VerifyDeviceRequest(ctx, r, v.Token)
		return err
	}
	session := issue()
	if err := verify(session); err != nil {
		t.Fatal(err)
	}
	if err := local.RevokeUser(ctx, actor, owner.Subject); err != nil {
		t.Fatal(err)
	}
	if err := verify(session); err == nil {
		t.Fatal("account revocation left device session active")
	}
	next := issue()
	user, err := local.User(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	if err := local.SetUser(ctx, actor, owner.Subject, user.Revision, false, false); err != nil {
		t.Fatal(err)
	}
	users, err := local.Users(ctx, actor, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range users {
		if u.ID == owner.Subject {
			user = u
		}
	}
	if err := local.SetUser(ctx, actor, owner.Subject, user.Revision, true, false); err != nil {
		t.Fatal(err)
	}
	if err := verify(next); err == nil {
		t.Fatal("restoring local user revived device")
	}
	devices, err := store.Devices(ctx, owner, org.ID, "")
	if err != nil || len(devices) != 1 || devices[0].State != "revoked" {
		t.Fatal("local disable did not permanently revoke device")
	}
}
