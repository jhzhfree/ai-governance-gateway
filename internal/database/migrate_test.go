package database_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jhzhfree/ai-governance-gateway/internal/budget"
	"github.com/jhzhfree/ai-governance-gateway/internal/control"
	"github.com/jhzhfree/ai-governance-gateway/internal/database"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
)

func execSQL(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatal("test SQL failed: ", err)
	}
}
func legacy(t *testing.T, pool *pgxpool.Pool, parts ...string) {
	t.Helper()
	for _, part := range parts {
		sql, err := os.ReadFile("testdata/legacy_" + part + ".sql")
		if err != nil {
			t.Fatal(err)
		}
		execSQL(t, pool, string(sql))
	}
}
func relation(t *testing.T, pool *pgxpool.Pool, name string) bool {
	t.Helper()
	var exists bool
	if err := pool.QueryRow(context.Background(), `SELECT pg_catalog.to_regclass($1) IS NOT NULL`, name).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	return exists
}
func status(t *testing.T, pool *pgxpool.Pool) database.Report {
	t.Helper()
	r, err := database.Status(context.Background(), pool)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestFreshSchemaAndReadOnlyStatus(t *testing.T) {
	pool := testutil.EmptyDatabase(t)
	ctx := context.Background()
	initial := status(t, pool)
	if initial.Managed || initial.Ready || initial.CurrentVersion != 0 || len(initial.Pending) != 13 {
		t.Fatal("unexpected unmanaged status")
	}
	if relation(t, pool, "aigw_schema_migrations") {
		t.Fatal("status created migration ledger")
	}
	if err := database.Check(ctx, pool); !errors.Is(err, database.ErrUnmanaged) {
		t.Fatalf("unmanaged check: %v", err)
	}
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	before := status(t, pool)
	if !before.Ready || before.CurrentVersion != 13 || len(before.Applied) != 13 {
		t.Fatal("fresh migration incomplete")
	}
	for _, a := range before.Applied {
		if a.Adopted {
			t.Fatal("new schema marked adopted")
		}
	}
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if after := status(t, pool); !reflect.DeepEqual(before, after) {
		t.Fatal("repeated migration changed history")
	}
	if err := database.Check(ctx, pool); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyUpgradePreservesGovernanceAndLedgerData(t *testing.T) {
	pool := testutil.EmptyDatabase(t)
	legacy(t, pool, "budget", "control", "browser")
	ctx := context.Background()
	p := identity.Principal{Issuer: "https://test-idp.example", Subject: "owner"}
	store := control.New(pool)
	org, err := store.CreateOrganization(ctx, p, "Legacy organization", "2026-11", 100)
	if err != nil {
		t.Fatal(err)
	}
	project, err := store.CreateProject(ctx, p, org.ID, "Legacy project", 50)
	if err != nil {
		t.Fatal(err)
	}
	key, err := store.CreateKey(ctx, p, org.ID, project.ID, "Legacy key", []string{"mock-text"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ledger := budget.New(pool)
	scope := budget.Scope{Organization: org.ID, Project: project.ID, Period: org.Period, Currency: org.Currency}
	if _, err := ledger.Reserve(ctx, budget.Quote{Scope: scope, RequestID: "legacy-request", PriceVersion: "mock-v1", Amount: 10}); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Dispatch(ctx, "legacy-request"); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Settle(ctx, "legacy-request", 4); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("synthetic-migration-fixture"))
	execSQL(t, pool, `INSERT INTO browser_sessions(digest,issuer,subject,expires_at) VALUES($1,$2,$3,clock_timestamp()+interval '1 hour')`, digest[:], p.Issuer, p.Subject)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	r := status(t, pool)
	for i, a := range r.Applied {
		if a.Adopted != (i < 3) {
			t.Fatal("legacy adoption record incorrect")
		}
	}
	if _, err := store.ResolveKey(ctx, key.Secret, "mock-text"); err != nil {
		t.Fatal("existing key no longer authorizes")
	}
	u, err := store.Usage(ctx, p, org.ID, project.ID)
	if err != nil || u.Spent != 4 || u.Held != 0 {
		t.Fatal("budget data changed")
	}
	reservation, err := ledger.Get(ctx, "legacy-request")
	if err != nil || reservation.State != "settled" || reservation.Actual == nil || *reservation.Actual != 4 {
		t.Fatal("reservation changed")
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM browser_sessions WHERE digest=$1`, digest[:]).Scan(&count); err != nil || count != 1 {
		t.Fatal("browser session lost")
	}
	audit, err := store.ListAudit(ctx, p, org.ID, 0, 100)
	if err != nil || len(audit) != 3 {
		t.Fatal("audit history changed")
	}
	if !relation(t, pool, "budget_project_history_idx") || !relation(t, pool, "budget_recovery_idx") || !relation(t, pool, "browser_login_flows_expiry_idx") {
		t.Fatal("new migration indexes missing")
	}
}

func TestBudgetOnlyLegacyStageCanUpgrade(t *testing.T) {
	pool := testutil.EmptyDatabase(t)
	legacy(t, pool, "budget")
	execSQL(t, pool, `INSERT INTO budget_accounts(organization_id,project_id,period_id,currency,limit_amount,spent) VALUES('legacy','project','2026-11','MOCK',100,8)`)
	if err := database.Migrate(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	r := status(t, pool)
	if !r.Applied[0].Adopted || r.Applied[1].Adopted || r.Applied[2].Adopted {
		t.Fatal("unexpected adoption stages")
	}
	var spent int
	if err := pool.QueryRow(context.Background(), `SELECT spent FROM budget_accounts WHERE organization_id='legacy'`).Scan(&spent); err != nil || spent != 8 {
		t.Fatal("legacy account overwritten")
	}
}

func TestLegacyMismatchIsNotStampedOrRepaired(t *testing.T) {
	for _, change := range []struct{ name, sql string }{
		{"partial_stage", `DROP TABLE budget_outbox`},
		{"changed_constraint", `ALTER TABLE budget_accounts DROP CONSTRAINT budget_accounts_check`},
		{"extra_column", `ALTER TABLE budget_accounts ADD COLUMN unexpected text`},
		{"changed_default", `ALTER TABLE budget_accounts ALTER COLUMN spent SET DEFAULT 10`},
	} {
		t.Run(change.name, func(t *testing.T) {
			pool := testutil.EmptyDatabase(t)
			legacy(t, pool, "budget")
			execSQL(t, pool, change.sql)
			if err := database.Migrate(context.Background(), pool); !errors.Is(err, database.ErrLegacy) {
				t.Fatalf("mismatch error=%v", err)
			}
			if relation(t, pool, "aigw_schema_migrations") || relation(t, pool, "organizations") {
				t.Fatal("mismatch caused partial upgrade")
			}
		})
	}
}

func TestConcurrentMigrationsRunExactlyOnce(t *testing.T) {
	pool := testutil.EmptyDatabase(t)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if err := database.Migrate(context.Background(), pool); err != nil {
				t.Error(err)
			}
		}()
	}
	close(start)
	wg.Wait()
	r := status(t, pool)
	if len(r.Applied) != 13 || !r.Ready {
		t.Fatal("concurrent migration history invalid")
	}
}

func TestMigrationBatchRollsBackDDLAndHistory(t *testing.T) {
	pool := testutil.EmptyDatabase(t)
	ctx := context.Background()
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	execSQL(t, pool, `DROP TABLE provider_credential_events,provider_credentials; DELETE FROM aigw_schema_migrations WHERE version=13; DROP TABLE provider_configuration_events,provider_configuration; DELETE FROM aigw_schema_migrations WHERE version=12; DROP TABLE content_audit_access_log,content_audit_chain,content_audit_heads,content_audit_payloads,content_audit_records,content_audit_deks,content_audit_auditors,content_audit_policies; DELETE FROM aigw_schema_migrations WHERE version=11`)
	execSQL(t, pool, `DROP TABLE billing_records,billing_imports; DELETE FROM aigw_schema_migrations WHERE version=10`)
	execSQL(t, pool, `DROP TABLE supplier_circuits; DELETE FROM aigw_schema_migrations WHERE version=9`)
	execSQL(t, pool, `DROP TABLE admission_leases,admission_events,project_limits; DELETE FROM aigw_schema_migrations WHERE version=8`)
	execSQL(t, pool, `DROP TABLE inference_attempts; DELETE FROM aigw_schema_migrations WHERE version=7`)
	execSQL(t, pool, `ALTER TABLE organizations DROP CONSTRAINT organizations_currency_check; ALTER TABLE organizations ADD CONSTRAINT organizations_currency_check CHECK(currency='MOCK'); ALTER TABLE project_models DROP CONSTRAINT project_models_model_check; ALTER TABLE project_models ADD CONSTRAINT project_models_model_check CHECK(model='mock-text'); DELETE FROM aigw_schema_migrations WHERE version=6`)
	execSQL(t, pool, `DROP INDEX budget_project_history_idx; DROP INDEX budget_recovery_idx; DROP INDEX browser_login_flows_expiry_idx; DELETE FROM aigw_schema_migrations WHERE version>=4; ALTER TABLE aigw_schema_migrations ADD CONSTRAINT reject_v4 CHECK(version<>4)`)
	if err := database.Check(ctx, pool); !errors.Is(err, database.ErrPending) {
		t.Fatalf("pending check: %v", err)
	}
	if err := database.Migrate(ctx, pool); err == nil {
		t.Fatal("expected ledger insertion failure")
	}
	if relation(t, pool, "budget_project_history_idx") || relation(t, pool, "budget_recovery_idx") || relation(t, pool, "browser_login_flows_expiry_idx") {
		t.Fatal("failed migration left index DDL committed")
	}
	r := status(t, pool)
	if r.CurrentVersion != 3 || len(r.Applied) != 3 {
		t.Fatal("failed migration left history committed")
	}
	execSQL(t, pool, `ALTER TABLE aigw_schema_migrations DROP CONSTRAINT reject_v4`)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := database.Check(ctx, pool); err != nil {
		t.Fatal(err)
	}
}

func TestHistoryAndSchemaDriftFailClosed(t *testing.T) {
	for _, change := range []struct {
		name, sql string
		want      error
	}{
		{"checksum", `UPDATE aigw_schema_migrations SET checksum=repeat('0',64) WHERE version=1`, database.ErrChecksum},
		{"gap", `DELETE FROM aigw_schema_migrations WHERE version=2`, database.ErrHistory},
		{"newer", `INSERT INTO aigw_schema_migrations(version,name,checksum,schema_hash,adopted) VALUES(14,'0014_future.sql',repeat('0',64),repeat('0',64),false)`, database.ErrNewer},
		{"missing_index", `DROP INDEX budget_recovery_idx`, database.ErrSchemaDrift},
		{"extra_column", `ALTER TABLE browser_sessions ADD COLUMN unexpected text`, database.ErrSchemaDrift},
		{"missing_table", `DROP TABLE browser_sessions`, database.ErrSchemaDrift},
	} {
		t.Run(change.name, func(t *testing.T) {
			pool := testutil.EmptyDatabase(t)
			ctx := context.Background()
			if err := database.Migrate(ctx, pool); err != nil {
				t.Fatal(err)
			}
			execSQL(t, pool, change.sql)
			if err := database.Check(ctx, pool); !errors.Is(err, change.want) {
				t.Fatalf("check=%v want %v", err, change.want)
			}
			if err := database.Migrate(ctx, pool); !errors.Is(err, change.want) {
				t.Fatalf("migration=%v want %v", err, change.want)
			}
		})
	}
}

func TestCanceledAndLockWaitingMigrationsLeaveNoChanges(t *testing.T) {
	pool := testutil.EmptyDatabase(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := database.Migrate(ctx, pool); err == nil {
		t.Fatal("canceled migration succeeded")
	}
	if relation(t, pool, "aigw_schema_migrations") {
		t.Fatal("canceled migration wrote ledger")
	}
	lock, err := pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback(context.Background())
	if _, err := lock.Exec(context.Background(), `SELECT pg_catalog.pg_advisory_xact_lock(pg_catalog.hashtextextended('aigw:migrations:'||current_schema(),0))`); err != nil {
		t.Fatal(err)
	}
	waiting, stop := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer stop()
	if err := database.Migrate(waiting, pool); err == nil {
		t.Fatal("migration ignored lock timeout")
	}
	if relation(t, pool, "aigw_schema_migrations") {
		t.Fatal("blocked migration changed schema")
	}
	if err := lock.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
}

func TestRequestHistoryIndexUpgradePreservesData(t *testing.T) {
	pool, ledger, scope := testutil.Database(t)
	testutil.Accounts(t, ledger, scope, 100, 100)
	if _, err := ledger.Reserve(context.Background(), budget.Quote{Scope: scope, RequestID: "history-upgrade", PriceVersion: "mock-v1", Amount: 10}); err != nil {
		t.Fatal(err)
	}
	execSQL(t, pool, `DROP TABLE provider_credential_events,provider_credentials; DELETE FROM aigw_schema_migrations WHERE version=13; DROP TABLE provider_configuration_events,provider_configuration; DELETE FROM aigw_schema_migrations WHERE version=12; DROP TABLE content_audit_access_log,content_audit_chain,content_audit_heads,content_audit_payloads,content_audit_records,content_audit_deks,content_audit_auditors,content_audit_policies; DELETE FROM aigw_schema_migrations WHERE version=11`)
	execSQL(t, pool, `DROP TABLE billing_records,billing_imports; DELETE FROM aigw_schema_migrations WHERE version=10`)
	execSQL(t, pool, `DROP TABLE supplier_circuits; DELETE FROM aigw_schema_migrations WHERE version=9`)
	execSQL(t, pool, `DROP TABLE admission_leases,admission_events,project_limits; DELETE FROM aigw_schema_migrations WHERE version=8`)
	execSQL(t, pool, `DROP TABLE inference_attempts; DELETE FROM aigw_schema_migrations WHERE version=7`)
	execSQL(t, pool, `ALTER TABLE organizations DROP CONSTRAINT organizations_currency_check; ALTER TABLE organizations ADD CONSTRAINT organizations_currency_check CHECK(currency='MOCK'); ALTER TABLE project_models DROP CONSTRAINT project_models_model_check; ALTER TABLE project_models ADD CONSTRAINT project_models_model_check CHECK(model='mock-text'); DELETE FROM aigw_schema_migrations WHERE version=6`)
	execSQL(t, pool, `DROP INDEX budget_project_history_idx; DELETE FROM aigw_schema_migrations WHERE version=5`)
	if err := database.Check(context.Background(), pool); !errors.Is(err, database.ErrPending) {
		t.Fatal("v4 database did not require migration")
	}
	if err := database.Migrate(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	if err := database.Check(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	record, err := ledger.Get(context.Background(), "history-upgrade")
	if err != nil || record.Amount != 10 || record.State != "reserved" {
		t.Fatal("index migration changed existing hold")
	}
	if !relation(t, pool, "budget_project_history_idx") {
		t.Fatal("history index missing")
	}
}

func TestMainlandCurrencyMigrationRollsBackAndPreservesMockLedger(t *testing.T) {
	pool, ledger, scope := testutil.Database(t)
	testutil.Accounts(t, ledger, scope, 100, 100)
	if _, err := ledger.Reserve(context.Background(), budget.Quote{Scope: scope, RequestID: "mock-before-currencies", PriceVersion: "mock-v1", Amount: 10}); err != nil {
		t.Fatal(err)
	}
	execSQL(t, pool, `DROP TABLE provider_credential_events,provider_credentials; DELETE FROM aigw_schema_migrations WHERE version=13; DROP TABLE provider_configuration_events,provider_configuration; DELETE FROM aigw_schema_migrations WHERE version=12; DROP TABLE content_audit_access_log,content_audit_chain,content_audit_heads,content_audit_payloads,content_audit_records,content_audit_deks,content_audit_auditors,content_audit_policies; DELETE FROM aigw_schema_migrations WHERE version=11`)
	execSQL(t, pool, `DROP TABLE billing_records,billing_imports; DELETE FROM aigw_schema_migrations WHERE version=10`)
	execSQL(t, pool, `DROP TABLE supplier_circuits; DELETE FROM aigw_schema_migrations WHERE version=9`)
	execSQL(t, pool, `DROP TABLE admission_leases,admission_events,project_limits; DELETE FROM aigw_schema_migrations WHERE version=8`)
	execSQL(t, pool, `DROP TABLE inference_attempts; DELETE FROM aigw_schema_migrations WHERE version=7`)
	execSQL(t, pool, `ALTER TABLE organizations DROP CONSTRAINT organizations_currency_check; ALTER TABLE organizations ADD CONSTRAINT organizations_currency_check CHECK(currency='MOCK'); ALTER TABLE project_models DROP CONSTRAINT project_models_model_check; ALTER TABLE project_models ADD CONSTRAINT project_models_model_check CHECK(model='mock-text'); DELETE FROM aigw_schema_migrations WHERE version=6; ALTER TABLE aigw_schema_migrations ADD CONSTRAINT reject_v6 CHECK(version<>6)`)
	if err := database.Migrate(context.Background(), pool); err == nil {
		t.Fatal("expected migration history failure")
	}
	if _, err := pool.Exec(context.Background(), `INSERT INTO organizations(id,name,period_id,currency) VALUES('cny-org','Currency fixture','2026-11','CNY_MICRO')`); err == nil {
		t.Fatal("failed migration committed widened currency constraint")
	}
	execSQL(t, pool, `ALTER TABLE aigw_schema_migrations DROP CONSTRAINT reject_v6`)
	if err := database.Migrate(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	execSQL(t, pool, `INSERT INTO organizations(id,name,period_id,currency) VALUES('cny-org','Currency fixture','2026-11','CNY_MICRO')`)
	record, err := ledger.Get(context.Background(), "mock-before-currencies")
	if err != nil || record.Currency != "MOCK" || record.Amount != 10 || record.State != "reserved" {
		t.Fatal("currency migration converted or reset a historical MOCK account")
	}
	if err := database.Check(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
}

func TestAttemptMigrationRollbackAndHistoricalRecords(t *testing.T) {
	pool, ledger, scope := testutil.Database(t)
	testutil.Accounts(t, ledger, scope, 100, 100)
	ctx := context.Background()
	if _, err := ledger.Reserve(ctx, budget.Quote{Scope: scope, RequestID: "before-attempts", PriceVersion: "mock-v1", Amount: 10}); err != nil {
		t.Fatal(err)
	}
	execSQL(t, pool, `DROP TABLE provider_credential_events,provider_credentials; DELETE FROM aigw_schema_migrations WHERE version=13; DROP TABLE provider_configuration_events,provider_configuration; DELETE FROM aigw_schema_migrations WHERE version=12; DROP TABLE content_audit_access_log,content_audit_chain,content_audit_heads,content_audit_payloads,content_audit_records,content_audit_deks,content_audit_auditors,content_audit_policies; DELETE FROM aigw_schema_migrations WHERE version=11`)
	execSQL(t, pool, `DROP TABLE billing_records,billing_imports; DELETE FROM aigw_schema_migrations WHERE version=10`)
	execSQL(t, pool, `DROP TABLE supplier_circuits; DELETE FROM aigw_schema_migrations WHERE version=9`)
	execSQL(t, pool, `DROP TABLE admission_leases,admission_events,project_limits; DELETE FROM aigw_schema_migrations WHERE version=8`)
	execSQL(t, pool, `DROP TABLE inference_attempts; DELETE FROM aigw_schema_migrations WHERE version=7; ALTER TABLE aigw_schema_migrations ADD CONSTRAINT reject_v7 CHECK(version<>7)`)
	if err := database.Check(ctx, pool); !errors.Is(err, database.ErrPending) {
		t.Fatal("old database did not require metadata migration")
	}
	if err := database.Migrate(ctx, pool); err == nil {
		t.Fatal("expected migration history failure")
	}
	if relation(t, pool, "inference_attempts") {
		t.Fatal("failed migration left metadata table committed")
	}
	execSQL(t, pool, `ALTER TABLE aigw_schema_migrations DROP CONSTRAINT reject_v7`)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM inference_attempts`).Scan(&count); err != nil || count != 0 {
		t.Fatal("migration fabricated historical execution data")
	}
	record, err := ledger.Get(ctx, "before-attempts")
	if err != nil || record.Amount != 10 || record.State != "reserved" {
		t.Fatal("metadata migration changed historical accounting")
	}
	execSQL(t, pool, `DROP INDEX inference_attempts_outcome_idx`)
	if err := database.Check(ctx, pool); !errors.Is(err, database.ErrSchemaDrift) {
		t.Fatal("new metadata table/index not managed by readiness")
	}
}

func TestAdmissionMigrationRollsBackAndPreservesHeldBudget(t *testing.T) {
	pool, ledger, scope := testutil.Database(t)
	ctx := context.Background()
	testutil.Accounts(t, ledger, scope, 100, 100)
	if _, err := ledger.Reserve(ctx, budget.Quote{Scope: scope, RequestID: "before-admission", PriceVersion: "mock-v1", Amount: 10}); err != nil {
		t.Fatal(err)
	}
	execSQL(t, pool, `DROP TABLE provider_credential_events,provider_credentials; DELETE FROM aigw_schema_migrations WHERE version=13; DROP TABLE provider_configuration_events,provider_configuration; DELETE FROM aigw_schema_migrations WHERE version=12; DROP TABLE content_audit_access_log,content_audit_chain,content_audit_heads,content_audit_payloads,content_audit_records,content_audit_deks,content_audit_auditors,content_audit_policies; DELETE FROM aigw_schema_migrations WHERE version=11`)
	execSQL(t, pool, `DROP TABLE billing_records,billing_imports; DELETE FROM aigw_schema_migrations WHERE version=10`)
	execSQL(t, pool, `DROP TABLE supplier_circuits; DELETE FROM aigw_schema_migrations WHERE version=9`)
	execSQL(t, pool, `DROP TABLE admission_leases,admission_events,project_limits; DELETE FROM aigw_schema_migrations WHERE version=8; ALTER TABLE aigw_schema_migrations ADD CONSTRAINT reject_v8 CHECK(version<>8)`)
	if err := database.Migrate(ctx, pool); err == nil {
		t.Fatal("expected migration history failure")
	}
	for _, table := range []string{"project_limits", "admission_events", "admission_leases"} {
		if relation(t, pool, table) {
			t.Fatal("failed migration left admission table")
		}
	}
	if status(t, pool).CurrentVersion != 7 {
		t.Fatal("failed migration changed history")
	}
	execSQL(t, pool, `ALTER TABLE aigw_schema_migrations DROP CONSTRAINT reject_v8`)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := database.Check(ctx, pool); err != nil {
		t.Fatal(err)
	}
	record, err := ledger.Get(ctx, "before-admission")
	if err != nil || record.State != "reserved" || record.Amount != 10 || record.Actual != nil {
		t.Fatal("migration changed existing budget hold")
	}
}

func TestCircuitMigrationRollbackAndExistingHoldPreservation(t *testing.T) {
	pool, ledger, scope := testutil.Database(t)
	ctx := context.Background()
	testutil.Accounts(t, ledger, scope, 100, 100)
	if _, err := ledger.Reserve(ctx, budget.Quote{Scope: scope, RequestID: "before-circuit", PriceVersion: "mock-v1", Amount: 10}); err != nil {
		t.Fatal(err)
	}
	execSQL(t, pool, `DROP TABLE provider_credential_events,provider_credentials; DELETE FROM aigw_schema_migrations WHERE version=13; DROP TABLE provider_configuration_events,provider_configuration; DELETE FROM aigw_schema_migrations WHERE version=12; DROP TABLE content_audit_access_log,content_audit_chain,content_audit_heads,content_audit_payloads,content_audit_records,content_audit_deks,content_audit_auditors,content_audit_policies; DELETE FROM aigw_schema_migrations WHERE version=11`)
	execSQL(t, pool, `DROP TABLE billing_records,billing_imports; DELETE FROM aigw_schema_migrations WHERE version=10`)
	execSQL(t, pool, `DROP TABLE supplier_circuits; DELETE FROM aigw_schema_migrations WHERE version=9; ALTER TABLE aigw_schema_migrations ADD CONSTRAINT reject_v9 CHECK(version<>9)`)
	if err := database.Migrate(ctx, pool); err == nil {
		t.Fatal("expected failed migration")
	}
	if relation(t, pool, "supplier_circuits") || status(t, pool).CurrentVersion != 8 {
		t.Fatal("failed migration changed schema or history")
	}
	execSQL(t, pool, `ALTER TABLE aigw_schema_migrations DROP CONSTRAINT reject_v9`)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := database.Check(ctx, pool); err != nil {
		t.Fatal(err)
	}
	r, err := ledger.Get(ctx, "before-circuit")
	if err != nil || r.State != "reserved" || r.Amount != 10 {
		t.Fatal("migration changed held budget")
	}
}

func TestBillingMigrationRollbackAndHoldPreservation(t *testing.T) {
	pool, ledger, scope := testutil.Database(t)
	ctx := context.Background()
	testutil.Accounts(t, ledger, scope, 100, 100)
	if _, err := ledger.Reserve(ctx, budget.Quote{Scope: scope, RequestID: "before-billing", PriceVersion: "mock-v1", Amount: 10}); err != nil {
		t.Fatal(err)
	}
	execSQL(t, pool, `DROP TABLE provider_credential_events,provider_credentials; DELETE FROM aigw_schema_migrations WHERE version=13; DROP TABLE provider_configuration_events,provider_configuration; DELETE FROM aigw_schema_migrations WHERE version=12; DROP TABLE content_audit_access_log,content_audit_chain,content_audit_heads,content_audit_payloads,content_audit_records,content_audit_deks,content_audit_auditors,content_audit_policies; DELETE FROM aigw_schema_migrations WHERE version=11`)
	execSQL(t, pool, `DROP TABLE billing_records,billing_imports; DELETE FROM aigw_schema_migrations WHERE version=10; ALTER TABLE aigw_schema_migrations ADD CONSTRAINT reject_v10 CHECK(version<>10)`)
	if err := database.Migrate(ctx, pool); err == nil {
		t.Fatal("expected failed migration")
	}
	if relation(t, pool, "billing_records") || relation(t, pool, "billing_imports") || status(t, pool).CurrentVersion != 9 {
		t.Fatal("failed migration changed schema")
	}
	execSQL(t, pool, `ALTER TABLE aigw_schema_migrations DROP CONSTRAINT reject_v10`)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := database.Check(ctx, pool); err != nil {
		t.Fatal(err)
	}
	r, err := ledger.Get(ctx, "before-billing")
	if err != nil || r.State != "reserved" || r.Amount != 10 {
		t.Fatal("billing migration changed hold")
	}
}

func TestContentAuditMigrationRollbackPreservesBudget(t *testing.T) {
	pool, ledger, scope := testutil.Database(t)
	ctx := context.Background()
	testutil.Accounts(t, ledger, scope, 100, 100)
	if _, err := ledger.Reserve(ctx, budget.Quote{Scope: scope, RequestID: "before-content", PriceVersion: "mock-v1", Amount: 10}); err != nil {
		t.Fatal(err)
	}
	execSQL(t, pool, `DROP TABLE provider_credential_events,provider_credentials; DELETE FROM aigw_schema_migrations WHERE version=13; DROP TABLE provider_configuration_events,provider_configuration; DELETE FROM aigw_schema_migrations WHERE version=12; DROP TABLE content_audit_access_log,content_audit_chain,content_audit_heads,content_audit_payloads,content_audit_records,content_audit_deks,content_audit_auditors,content_audit_policies; DELETE FROM aigw_schema_migrations WHERE version=11; ALTER TABLE aigw_schema_migrations ADD CONSTRAINT reject_v11 CHECK(version<>11)`)
	if err := database.Migrate(ctx, pool); err == nil {
		t.Fatal("expected failed content migration")
	}
	if relation(t, pool, "content_audit_records") || status(t, pool).CurrentVersion != 10 {
		t.Fatal("failed content migration partially committed")
	}
	execSQL(t, pool, `ALTER TABLE aigw_schema_migrations DROP CONSTRAINT reject_v11`)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := database.Check(ctx, pool); err != nil {
		t.Fatal(err)
	}
	record, err := ledger.Get(ctx, "before-content")
	if err != nil || record.State != "reserved" || record.Amount != 10 {
		t.Fatal("content migration changed budget")
	}
}

func TestContentPartitionDetachFailsReadiness(t *testing.T) {
	pool, _, _ := testutil.Database(t)
	execSQL(t, pool, `ALTER TABLE content_audit_payloads DETACH PARTITION content_audit_payloads_default`)
	if err := database.Check(context.Background(), pool); !errors.Is(err, database.ErrSchemaDrift) {
		t.Fatal("partition binding drift not detected", err)
	}
}

func TestProviderConfigurationMigrationPreservesPriorAudit(t *testing.T) {
	ctx := context.Background()
	pool := testutil.EmptyDatabase(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	execSQL(t, pool, `DROP TABLE provider_credential_events,provider_credentials; DELETE FROM aigw_schema_migrations WHERE version=13; DROP TABLE provider_configuration_events,provider_configuration; DELETE FROM aigw_schema_migrations WHERE version=12; ALTER TABLE aigw_schema_migrations ADD CONSTRAINT reject_v12 CHECK(version<>12)`)
	if database.Migrate(ctx, pool) == nil {
		t.Fatal("migration should fail atomically")
	}
	var table *string
	if err := pool.QueryRow(ctx, `SELECT to_regclass('provider_configuration')::text`).Scan(&table); err != nil || table != nil {
		t.Fatal("failed migration left tables")
	}
	execSQL(t, pool, `ALTER TABLE aigw_schema_migrations DROP CONSTRAINT reject_v12`)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := database.Check(ctx, pool); err != nil {
		t.Fatal(err)
	}
}

func TestProviderCredentialMigrationRollback(t *testing.T) {
	ctx := context.Background()
	pool := testutil.EmptyDatabase(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	execSQL(t, pool, `DROP TABLE provider_credential_events,provider_credentials; DELETE FROM aigw_schema_migrations WHERE version=13; ALTER TABLE aigw_schema_migrations ADD CONSTRAINT reject_v13 CHECK(version<>13)`)
	if database.Migrate(ctx, pool) == nil {
		t.Fatal("migration should fail")
	}
	if relation(t, pool, "provider_credentials") {
		t.Fatal("failed migration left credential table")
	}
	execSQL(t, pool, `ALTER TABLE aigw_schema_migrations DROP CONSTRAINT reject_v13`)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := database.Check(ctx, pool); err != nil {
		t.Fatal(err)
	}
}
