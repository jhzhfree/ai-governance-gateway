package newapibridge_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jhzhfree/ai-governance-gateway/internal/newapibridge"
	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
)

func TestGovernedDirectoryAndRecoveryReadiness(t *testing.T) {
	pool, _, _ := testutil.Database(t)
	cfgDirectory := testutil.MainlandConfig()
	for i := range cfgDirectory.Models {
		cfgDirectory.Models[i].NewAPIChannelID = 1
	}
	directory, err := provider.NewGoverned(cfgDirectory)
	if err != nil {
		t.Fatal(err)
	}
	route, _ := directory.Find("zhipu/fixture")
	if route.ExecutionSettings().APIKey != "" {
		t.Fatal("governed directory has vendor credentials")
	}
	if _, err := route.Open(context.Background(), request()); err == nil {
		t.Fatal("credential-free route performed native execution")
	}
	var available, admissionV2 atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("service credential missing")
		}
		if available.Load() {
			w.Header().Set("X-Aigw-Events-Version", "1")
			w.Header().Set("X-Aigw-Paging-Version", "1")
			if admissionV2.Load() {
				w.Header().Set("X-Aigw-Admission-Version", "2")
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`))
	}))
	defer server.Close()
	cfg := newapibridge.Config{Origin: server.URL, ServiceToken: token, MaxConcurrent: 1, AllowLoopbackHTTP: true, Journal: newapibridge.NewJournal(pool)}
	if runtime, err := newapibridge.NewRuntime(context.Background(), cfg, directory); err == nil {
		runtime.Close()
		t.Fatal("unversioned endpoint accepted")
	}
	available.Store(true)
	if old, err := newapibridge.NewRuntime(context.Background(), cfg, directory); err == nil {
		old.Close()
		t.Fatal("old data plane without input admission accepted")
	}
	admissionV2.Store(true)
	embeddingConfig := testutil.MainlandConfig()
	for i := range embeddingConfig.Models {
		embeddingConfig.Models[i].NewAPIChannelID = 1
		embeddingConfig.Models[i].API = "embeddings"
		embeddingConfig.Models[i].MaxOutputTokens = 0
		embeddingConfig.Models[i].OutputPerMillion = 0
	}
	embeddingDirectory, err := provider.NewGoverned(embeddingConfig)
	if err != nil {
		t.Fatal(err)
	}
	if old, err := newapibridge.NewRuntime(context.Background(), cfg, embeddingDirectory); err == nil {
		old.Close()
		t.Fatal("embedding directory accepted old protocol capability")
	}
	runtime, err := newapibridge.NewRuntime(context.Background(), cfg, directory)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	if !runtime.Ready() {
		t.Fatal("authenticated healthy endpoint not ready")
	}
	available.Store(false)
	deadline := time.Now().Add(7 * time.Second)
	for runtime.Ready() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if runtime.Ready() {
		t.Fatal("recovery failure did not fail readiness")
	}
	runtime.Close()
	if runtime.Ready() {
		t.Fatal("closed runtime ready")
	}
}
