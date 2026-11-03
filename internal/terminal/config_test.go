package terminal

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type memoryKeyring struct {
	values map[string]string
	fail   bool
}

func (k *memoryKeyring) Get(s, u string) (string, error) {
	if k.fail {
		return "", fmt.Errorf("unavailable")
	}
	v, ok := k.values[s+u]
	if !ok {
		return "", fmt.Errorf("not found")
	}
	return v, nil
}
func (k *memoryKeyring) Set(s, u, v string) error {
	if k.fail {
		return fmt.Errorf("unavailable")
	}
	if k.values == nil {
		k.values = map[string]string{}
	}
	k.values[s+u] = v
	return nil
}
func (k *memoryKeyring) Delete(s, u string) error {
	if k.fail {
		return fmt.Errorf("unavailable")
	}
	delete(k.values, s+u)
	return nil
}
func localState(t *testing.T) State {
	t.Helper()
	return State{Path: filepath.Join(t.TempDir(), "private", "config.json"), Keyring: &memoryKeyring{}}
}
func testConfig() Config {
	return Config{Version: 1, Server: "https://gateway.example", Issuer: "https://idp.example/realm", ClientID: "native", Audience: "aigw-management", CredentialStore: "file"}
}
func testCredential(c Config) Credential {
	return Credential{Server: c.Server, Issuer: c.Issuer, ClientID: c.ClientID, Audience: c.Audience, AccessToken: "synthetic-private-token", ExpiresAt: time.Now().Add(time.Hour)}
}

func TestPrivateCredentialFilesAndRedaction(t *testing.T) {
	state := localState(t)
	cfg := testConfig()
	credential := testCredential(cfg)
	if err := state.Save(cfg); err != nil {
		t.Fatal(err)
	}
	if err := state.SaveCredential(cfg, credential); err != nil {
		t.Fatal(err)
	}
	directory, err := os.Stat(filepath.Dir(state.Path))
	if err != nil || directory.Mode().Perm() != 0700 {
		t.Fatal("configuration directory permissions invalid")
	}
	for _, path := range []string{state.Path, state.Path + ".credentials.json"} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal("private file permissions invalid")
		}
	}
	content, err := os.ReadFile(state.Path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(content), credential.AccessToken) {
		t.Fatal("config contains token")
	}
	saved, exists, err := state.Load()
	if err != nil || !exists || !saved.sameBinding(cfg) {
		t.Fatal("config roundtrip failed")
	}
	loaded, err := state.Credential(saved)
	if err != nil || loaded.AccessToken != credential.AccessToken {
		t.Fatal("credential roundtrip failed")
	}
	for _, rendered := range []string{fmt.Sprint(credential), fmt.Sprintf("%+v", credential), fmt.Sprintf("%#v", credential)} {
		if strings.Contains(rendered, credential.AccessToken) {
			t.Fatal("credential formatter exposed token")
		}
	}
	if err := state.DeleteCredential(saved); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Credential(saved); err == nil {
		t.Fatal("deleted credential still usable")
	}
}
func TestKeyringFailureDoesNotSilentlyWriteFiles(t *testing.T) {
	state := localState(t)
	cfg := testConfig()
	cfg.CredentialStore = "keyring"
	state.Keyring = &memoryKeyring{fail: true}
	if err := state.SaveCredential(cfg, testCredential(cfg)); err == nil {
		t.Fatal("keyring failure ignored")
	}
	if _, err := os.Stat(state.Path + ".credentials.json"); !os.IsNotExist(err) {
		t.Fatal("keyring failure created fallback file")
	}
	state.Keyring = &memoryKeyring{}
	if err := state.SaveCredential(cfg, testCredential(cfg)); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Credential(cfg); err != nil {
		t.Fatal(err)
	}
	other := cfg
	other.Server = "https://other.example"
	if _, err := state.Credential(other); err == nil {
		t.Fatal("keyring credential crossed server binding")
	}
}
func TestUnsafeFilesAndCredentialBindingsAreRejected(t *testing.T) {
	t.Run("public_directory", func(t *testing.T) {
		s := localState(t)
		if err := os.MkdirAll(filepath.Dir(s.Path), 0755); err != nil {
			t.Fatal(err)
		}
		// The runner may use umask 077; explicitly establish the unsafe mode.
		if err := os.Chmod(filepath.Dir(s.Path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := s.Save(testConfig()); err == nil {
			t.Fatal("shared directory accepted")
		}
	})
	t.Run("public_file", func(t *testing.T) {
		s := localState(t)
		if err := s.Save(testConfig()); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(s.Path, 0644); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.Load(); err == nil {
			t.Fatal("public config accepted")
		}
	})
	t.Run("symlink", func(t *testing.T) {
		s := localState(t)
		if err := s.Save(testConfig()); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(s.Path); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(t.TempDir(), "outside")
		if err := os.WriteFile(target, []byte("preserve"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, s.Path); err != nil {
			t.Fatal(err)
		}
		if err := s.Save(testConfig()); err == nil {
			t.Fatal("symlink overwritten")
		}
		if _, _, err := s.Load(); err == nil {
			t.Fatal("symlink read")
		}
		value, _ := os.ReadFile(target)
		if string(value) != "preserve" {
			t.Fatal("symlink target modified")
		}
	})
	t.Run("binding_and_expiry", func(t *testing.T) {
		s := localState(t)
		cfg := testConfig()
		cred := testCredential(cfg)
		if err := s.SaveCredential(cfg, cred); err != nil {
			t.Fatal(err)
		}
		cfg.Audience = "other"
		if _, err := s.Credential(cfg); err == nil {
			t.Fatal("file binding ignored")
		}
		cfg = testConfig()
		cred.ExpiresAt = time.Now().Add(-time.Minute)
		if err := s.SaveCredential(cfg, cred); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Credential(cfg); err == nil {
			t.Fatal("expired credential accepted")
		}
	})
}
func TestSecureAddressDefaults(t *testing.T) {
	for _, address := range []string{"http://public.example", "https://user:password@gateway.example", "https://gateway.example/path", "https://gateway.example?token=secret"} {
		if _, err := canonicalURL(address, true); err == nil {
			t.Fatal("unsafe address accepted")
		}
	}
	if _, err := canonicalURL("http://127.0.0.1:8080", false); err == nil {
		t.Fatal("insecure loopback enabled implicitly")
	}
	if _, err := canonicalURL("http://127.0.0.1:8080", true); err != nil {
		t.Fatal(err)
	}
	if _, err := issuerURL("https://idp.example/realms/team", false); err != nil {
		t.Fatal(err)
	}
}
