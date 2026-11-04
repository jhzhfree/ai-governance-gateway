package contentaudit

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"testing"
)

func TestEnvelopeAuthenticationAndKeyValidation(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	cipher, nonce, err := seal(key, []byte("synthetic content"), []byte("org/project/request"))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := open(key, cipher, nonce, []byte("org/project/request"))
	if err != nil || string(plain) != "synthetic content" {
		t.Fatal("roundtrip failed")
	}
	if _, err := open(key, cipher, nonce, []byte("other/project/request")); err == nil {
		t.Fatal("AAD substitution accepted")
	}
	cipher[0] ^= 1
	if _, err := open(key, cipher, nonce, []byte("org/project/request")); err == nil {
		t.Fatal("ciphertext tampering accepted")
	}
	raw, _ := json.Marshal(map[string]string{"test": base64.StdEncoding.EncodeToString(key)})
	ring, err := ParseKeys(string(raw), "test")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains([]byte(fmt.Sprintf("%#v", ring)), []byte(base64.StdEncoding.EncodeToString(key))) {
		t.Fatal("key formatter leaked value")
	}
	for _, value := range []string{"{}", "bad", `{"test":"short"}`} {
		if _, err := ParseKeys(value, "test"); err == nil {
			t.Fatal("invalid key configuration accepted")
		}
	}
}
