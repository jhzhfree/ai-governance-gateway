package testutil

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// NewAPIProcess starts only the explicitly configured, synthetic native fixture.
// Production binaries and vendor credentials are never used by this helper.
func NewAPIProcess(t testing.TB, upstream string) string {
	t.Helper()
	binary := os.Getenv("TEST_NEWAPI_SERVER_BINARY")
	if binary == "" {
		t.Skip("pinned new-api integration fixture binary required")
	}
	root := t.TempDir()
	cmd := exec.Command(binary)
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOVERNANCE_FIXTURE_DIR="+root, "GOVERNANCE_FIXTURE_UPSTREAM="+upstream, "GOVERNANCE_FIXTURE_PROTOCOL=openai", "GOVERNANCE_FIXTURE_MULTIKEY=")
	if cmd.Start() != nil {
		t.Fatal("native fixture failed to start")
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if t.Failed() {
			raw := output.String()
			if len(raw) > 5000 {
				raw = raw[len(raw)-5000:]
			}
			t.Log(raw)
		}
	})
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if raw, err := os.ReadFile(filepath.Join(root, "address")); err == nil && len(raw) > 0 {
			return string(raw)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("native fixture readiness timeout")
	return ""
}
