package deviceproof

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
	"time"
)

func TestDPoPProofBindsKeyMethodTargetTokenAndTime(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	now := time.Now()
	htu := "https://gateway.synthetic.invalid/v1/models"
	token := "synthetic-access-token"
	proof, err := SignAt(key, "GET", htu, token, now, rand.Text())
	if err != nil {
		t.Fatal(err)
	}
	claims, err := Verify(proof, pub, "GET", htu, token, now)
	if err != nil || claims.ID == "" {
		t.Fatal("valid proof rejected")
	}
	for _, tc := range []struct {
		key                ed25519.PublicKey
		method, url, token string
		at                 time.Time
	}{{other, "GET", htu, token, now}, {pub, "POST", htu, token, now}, {pub, "GET", htu + "/other", token, now}, {pub, "GET", htu, "other", now}, {pub, "GET", htu, token, now.Add(61 * time.Second)}, {pub, "GET", htu, token, now.Add(-31 * time.Second)}} {
		if _, err := Verify(proof, tc.key, tc.method, tc.url, tc.token, tc.at); err == nil {
			t.Fatal("unbound proof accepted")
		}
	}
	if _, err := Verify(strings.Repeat("x", 9000), pub, "GET", htu, token, now); err == nil {
		t.Fatal("unbounded proof accepted")
	}
	issuance, err := Sign(key, "POST", htu, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(issuance, pub, "POST", htu, "", time.Now()); err != nil {
		t.Fatal("issuance proof rejected")
	}
	if _, err := Verify(issuance, pub, "POST", htu, token, time.Now()); err == nil {
		t.Fatal("issuance proof reused as resource proof")
	}
}
