package provider_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
)

type snapshotBarrier struct {
	once          sync.Once
	read, proceed chan struct{}
}

func (b *snapshotBarrier) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if data.SQL == "SELECT configuration FROM provider_configuration WHERE singleton=true" {
		return context.WithValue(ctx, barrierKey{}, true)
	}
	return ctx
}

type barrierKey struct{}

func (b *snapshotBarrier) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryEndData) {
	if ctx.Value(barrierKey{}) == true {
		b.once.Do(func() {
			close(b.read)
			select {
			case <-b.proceed:
			case <-ctx.Done():
			}
		})
	}
}
func TestExecutionSnapshotCannotMixConfigurationAndCredentials(t *testing.T) {
	pool, _, _ := testutil.Database(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	keys, _ := json.Marshal(map[string]string{"fixture": base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", 32)))})
	getter := func(name string) string {
		switch name {
		case "AIGW_PROVIDER_ENCRYPTION_KEYS":
			return string(keys)
		case "AIGW_PROVIDER_ENCRYPTION_ACTIVE_KEY":
			return "fixture"
		}
		return "synthetic-env"
	}
	admin := identity.Principal{Issuer: "synthetic", Subject: "admin"}
	writer := provider.NewManager(pool, getter, nil, admin)
	cfg := testutil.MainlandConfig()
	cfg.Models = cfg.Models[:1]
	if err := writer.SetCredential(ctx, admin, "aliyun", 0, "synthetic-old"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Publish(ctx, admin, 0, cfg); err != nil {
		t.Fatal(err)
	}
	barrier := &snapshotBarrier{read: make(chan struct{}), proceed: make(chan struct{})}
	pc := pool.Config()
	pc.ConnConfig.Tracer = barrier
	pc.MaxConns = 1
	readerPool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		t.Fatal(err)
	}
	defer readerPool.Close()
	reader := provider.NewManager(readerPool, getter, nil, admin)
	result := make(chan *provider.Catalog, 1)
	fail := make(chan error, 1)
	go func() { c, e := reader.ExecutionSnapshot(ctx); result <- c; fail <- e }()
	select {
	case <-barrier.read:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := writer.SetCredential(ctx, admin, "aliyun", 1, "synthetic-new"); err != nil {
		t.Fatal(err)
	}
	cfg.Models[0].UpstreamModel = "new-model"
	if err := writer.Publish(ctx, admin, 1, cfg); err != nil {
		t.Fatal(err)
	}
	close(barrier.proceed)
	old := <-result
	if err := <-fail; err != nil {
		t.Fatal(err)
	}
	r, _ := old.Find("aliyun/fixture")
	s := r.ExecutionSettings()
	if s.Model != "fixture-model" || s.APIKey != "synthetic-old" {
		t.Fatal("mixed database snapshots")
	}
	fresh, err := reader.ExecutionSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	r, _ = fresh.Find("aliyun/fixture")
	s = r.ExecutionSettings()
	if s.Model != "new-model" || s.APIKey != "synthetic-new" {
		t.Fatal("next snapshot did not observe committed changes")
	}
	if len(fresh.ExecutionRoutes()) != 1 {
		t.Fatal("invalid execution route set")
	}
}
