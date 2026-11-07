package gatewayapp

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jhzhfree/ai-governance-gateway/internal/database"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
)

func TestStartupAutomaticallyMigratesAndRepeats(t *testing.T) {
	pool := testutil.EmptyDatabase(t)
	ctx := context.Background()
	if err := prepareSchema(ctx, pool, config{schemaMode: "check"}); !errors.Is(err, database.ErrUnmanaged) {
		t.Fatalf("check changed empty schema: %v", err)
	}
	if err := prepareSchema(ctx, pool, config{}); err != nil {
		t.Fatal(err)
	}
	before, err := database.Status(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareSchema(ctx, pool, config{}); err != nil {
		t.Fatal(err)
	}
	after, err := database.Status(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if !after.Ready || before.CurrentVersion != after.CurrentVersion {
		t.Fatal("repeated startup changed schema version")
	}
}

func TestConcurrentStartupSerializesMigrations(t *testing.T) {
	pool := testutil.EmptyDatabase(t)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := prepareSchema(context.Background(), pool, config{}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if err := database.Check(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
}

func TestStartupUpgradePreservesExistingData(t *testing.T) {
	pool, ledger, scope := testutil.Database(t)
	ctx := context.Background()
	testutil.Accounts(t, ledger, scope, 100, 80)
	testutil.RevertSystemManagementMigration(t, pool)
	testutil.RevertDeepSeekMigration(t, pool)
	_, err := pool.Exec(ctx, `DROP TABLE device_request_bindings,device_proof_nonces,device_sessions,device_policy_events,project_device_policies,device_state_events,terminal_devices; ALTER TABLE organization_member_lifecycle DROP COLUMN revoked_devices; DELETE FROM aigw_schema_migrations WHERE version=23`)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareSchema(ctx, pool, config{schemaMode: "check"}); !errors.Is(err, database.ErrPending) {
		t.Fatalf("check must refuse pending upgrade: %v", err)
	}
	if err := prepareSchema(ctx, pool, config{}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM budget_accounts`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("accounts lost: %d %v", count, err)
	}
}

func TestStartupRejectsNewerOrTamperedDatabase(t *testing.T) {
	for _, tc := range []struct {
		name, sql string
		want      error
	}{
		{"newer", `INSERT INTO aigw_schema_migrations(version,name,checksum,schema_hash,adopted) SELECT max(version)+1,'future.sql',repeat('0',64),repeat('0',64),false FROM aigw_schema_migrations`, database.ErrNewer},
		{"checksum", `UPDATE aigw_schema_migrations SET checksum=repeat('0',64) WHERE version=1`, database.ErrChecksum},
		{"drift", `DROP INDEX budget_recovery_idx`, database.ErrSchemaDrift},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool, _, _ := testutil.Database(t)
			ctx := context.Background()
			if _, err := pool.Exec(ctx, tc.sql); err != nil {
				t.Fatal(err)
			}
			if err := prepareSchema(ctx, pool, config{}); !errors.Is(err, tc.want) {
				t.Fatalf("unsafe startup: %v", err)
			}
		})
	}
}

func TestStartupLockTimeoutDoesNotChangeSchema(t *testing.T) {
	pool := testutil.EmptyDatabase(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('aigw:migrations:'||current_schema(),0))`); err != nil {
		t.Fatal(err)
	}
	if err := prepareSchema(ctx, pool, config{schemaTimeout: 50 * time.Millisecond}); err == nil {
		t.Fatal("startup ignored migration lock timeout")
	}
	r, err := database.Status(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if r.CurrentVersion != 0 {
		t.Fatal("timed-out startup modified schema")
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if err := prepareSchema(ctx, pool, config{}); err != nil {
		t.Fatal(err)
	}
}

func TestStartupMigrationFailureRollsBackAndCanRetry(t *testing.T) {
	pool := testutil.EmptyDatabase(t)
	ctx := context.Background()
	_, err := pool.Exec(ctx, `CREATE TABLE aigw_schema_migrations (
version integer PRIMARY KEY CHECK(version>0 AND version<>2), name text NOT NULL,
checksum text NOT NULL,schema_hash text NOT NULL,adopted boolean NOT NULL,
applied_at timestamptz NOT NULL DEFAULT clock_timestamp())`)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareSchema(ctx, pool, config{}); err == nil {
		t.Fatal("startup ignored failed migration")
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM aigw_schema_migrations`).Scan(&count); err != nil || count != 0 {
		t.Fatal("failed batch partially committed history")
	}
	var created bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('budget_accounts') IS NOT NULL`).Scan(&created); err != nil || created {
		t.Fatal("failed batch left DDL behind")
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE aigw_schema_migrations DROP CONSTRAINT aigw_schema_migrations_version_check`); err != nil {
		t.Fatal(err)
	}
	if err := prepareSchema(ctx, pool, config{}); err != nil {
		t.Fatal(err)
	}
}
