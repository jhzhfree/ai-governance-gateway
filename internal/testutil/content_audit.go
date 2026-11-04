package testutil

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jhzhfree/ai-governance-gateway/internal/contentaudit"
	"testing"
)

func ContentAudit(t testing.TB, pool *pgxpool.Pool) *contentaudit.Service {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal("synthetic audit key unavailable")
	}
	data, _ := json.Marshal(map[string]string{"test-v1": base64.StdEncoding.EncodeToString(key)})
	ring, err := contentaudit.ParseKeys(string(data), "test-v1")
	if err != nil {
		t.Fatal("synthetic audit key invalid")
	}
	return contentaudit.New(pool, ring)
}
