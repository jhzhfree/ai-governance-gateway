package database_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"reflect"
	"strings"
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
	if initial.Managed || initial.Ready || initial.CurrentVersion != 0 || len(initial.Pending) != 35 {
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
	if !before.Ready || before.CurrentVersion != 35 || len(before.Applied) != 35 {
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
	org := control.Organization{ID: "legacy-org", Name: "Legacy organization", Period: "2026-11", Currency: "MOCK", Role: "owner"}
	project := control.Project{ID: "legacy-project", OrganizationID: org.ID, Name: "Legacy project", Role: "admin"}
	key := control.IssuedKey{Secret: "aigw_pk_synthetic_legacy_migration_secret"}
	keyDigest := sha256.Sum256([]byte(key.Secret))
	execSQL(t, pool, `INSERT INTO organizations(id,name,period_id,currency) VALUES($1,$2,$3,$4)`, org.ID, org.Name, org.Period, org.Currency)
	execSQL(t, pool, `INSERT INTO organization_members VALUES($1,$2,$3,'owner')`, org.ID, p.Issuer, p.Subject)
	execSQL(t, pool, `INSERT INTO projects VALUES($1,$2,$3)`, org.ID, project.ID, project.Name)
	execSQL(t, pool, `INSERT INTO budget_accounts(organization_id,project_id,period_id,currency,limit_amount) VALUES($1,'',$2,$3,100),($1,$4,$2,$3,50)`, org.ID, org.Period, org.Currency, project.ID)
	execSQL(t, pool, `INSERT INTO project_models VALUES($1,$2,'mock-text')`, org.ID, project.ID)
	execSQL(t, pool, `INSERT INTO project_keys(id,organization_id,project_id,name,digest,prefix,models,creator_issuer,creator_subject,expires_at) VALUES('legacy-key',$1,$2,'Legacy key',$3,$4,ARRAY['mock-text'],$5,$6,clock_timestamp()+interval '1 hour')`, org.ID, project.ID, keyDigest[:], key.Secret[:16], p.Issuer, p.Subject)
	execSQL(t, pool, `INSERT INTO audit_events(organization_id,actor_issuer,actor_subject,action,resource_id) SELECT $1,$2,$3,action,'legacy' FROM unnest(ARRAY['organization.created','project.created','project.key_created']) action`, org.ID, p.Issuer, p.Subject)
	ledger := budget.New(pool)
	// Prepare an immutable legacy-format snapshot directly. New ledger code must
	// require the current schema rather than silently skip missing alert tables.
	execSQL(t, pool, `WITH accounts AS (
 UPDATE budget_accounts SET spent=4,held=0 WHERE organization_id=$1 AND project_id IN ('',$2) AND period_id=$3 AND currency=$4 RETURNING project_id),
 reservation AS (INSERT INTO budget_reservations(request_id,organization_id,project_id,period_id,currency,price_version,reserved,actual,state)
 SELECT 'legacy-request',$1,$2,$3,$4,'mock-v1',10,4,'settled' WHERE (SELECT count(*) FROM accounts)=2 RETURNING request_id)
 INSERT INTO budget_outbox(request_id,event_type) SELECT request_id,unnest(ARRAY['reserved','dispatched','settled']) FROM reservation`, org.ID, project.ID, org.Period, org.Currency)
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
	if len(r.Applied) != 35 || !r.Ready {
		t.Fatal("concurrent migration history invalid")
	}
}

func TestMigrationBatchRollsBackDDLAndHistory(t *testing.T) {
	pool := testutil.EmptyDatabase(t)
	ctx := context.Background()
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	testutil.RevertSystemManagementMigration(t, pool)
	testutil.RevertDeepSeekMigration(t, pool)
	execSQL(t, pool, `DROP TABLE device_request_bindings,device_proof_nonces,device_sessions,device_policy_events,project_device_policies,device_state_events,terminal_devices; ALTER TABLE organization_member_lifecycle DROP COLUMN revoked_devices; DELETE FROM aigw_schema_migrations WHERE version=23; DROP TABLE budget_approval_requests,budget_approval_policies,budget_approval_policy_versions; DELETE FROM aigw_schema_migrations WHERE version=22; DROP TABLE request_department_attributions; DELETE FROM aigw_schema_migrations WHERE version=21; DROP TABLE department_versions,department_projects,department_members,departments,department_configurations; ALTER TABLE organization_member_lifecycle DROP COLUMN removed_department_grants; DELETE FROM aigw_schema_migrations WHERE version=20; DROP TABLE organization_member_lifecycle; DROP INDEX project_keys_creator_live; ALTER TABLE organization_members DROP CONSTRAINT organization_owner_enabled; ALTER TABLE organization_members DROP COLUMN lifecycle_revision,DROP COLUMN enabled; DELETE FROM aigw_schema_migrations WHERE version=19; DROP TABLE organization_ownership_transfers; DROP INDEX organization_single_owner; DELETE FROM aigw_schema_migrations WHERE version=18; DROP TABLE content_audit_checkpoint_parts,content_audit_checkpoints; DELETE FROM aigw_schema_migrations WHERE version=17; DROP TABLE budget_alert_policy_versions,budget_alerts,budget_alert_policies; DELETE FROM aigw_schema_migrations WHERE version=16; DROP TABLE local_security_events,local_auth_throttle,local_access_tokens,local_users; DELETE FROM aigw_schema_migrations WHERE version=15; DROP TABLE keyword_policy_versions,keyword_matches,keyword_policies,budget_period_changes,budget_cycles,budget_limit_changes; DELETE FROM aigw_schema_migrations WHERE version=14; DROP TABLE provider_credential_events,provider_credentials; DELETE FROM aigw_schema_migrations WHERE version=13; DROP TABLE provider_configuration_events,provider_configuration; DELETE FROM aigw_schema_migrations WHERE version=12; DROP TABLE content_audit_access_log,content_audit_chain,content_audit_heads,content_audit_payloads,content_audit_records,content_audit_deks,content_audit_auditors,content_audit_policies; DELETE FROM aigw_schema_migrations WHERE version=11`)
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
		{"newer", `INSERT INTO aigw_schema_migrations(version,name,checksum,schema_hash,adopted) SELECT max(version)+1,'future.sql',repeat('0',64),repeat('0',64),false FROM aigw_schema_migrations`, database.ErrNewer},
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
	testutil.RevertSystemManagementMigration(t, pool)
	testutil.RevertDeepSeekMigration(t, pool)
	execSQL(t, pool, `DROP TABLE device_request_bindings,device_proof_nonces,device_sessions,device_policy_events,project_device_policies,device_state_events,terminal_devices; ALTER TABLE organization_member_lifecycle DROP COLUMN revoked_devices; DELETE FROM aigw_schema_migrations WHERE version=23; DROP TABLE budget_approval_requests,budget_approval_policies,budget_approval_policy_versions; DELETE FROM aigw_schema_migrations WHERE version=22; DROP TABLE request_department_attributions; DELETE FROM aigw_schema_migrations WHERE version=21; DROP TABLE department_versions,department_projects,department_members,departments,department_configurations; ALTER TABLE organization_member_lifecycle DROP COLUMN removed_department_grants; DELETE FROM aigw_schema_migrations WHERE version=20; DROP TABLE organization_member_lifecycle; DROP INDEX project_keys_creator_live; ALTER TABLE organization_members DROP CONSTRAINT organization_owner_enabled; ALTER TABLE organization_members DROP COLUMN lifecycle_revision,DROP COLUMN enabled; DELETE FROM aigw_schema_migrations WHERE version=19; DROP TABLE organization_ownership_transfers; DROP INDEX organization_single_owner; DELETE FROM aigw_schema_migrations WHERE version=18; DROP TABLE content_audit_checkpoint_parts,content_audit_checkpoints; DELETE FROM aigw_schema_migrations WHERE version=17; DROP TABLE budget_alert_policy_versions,budget_alerts,budget_alert_policies; DELETE FROM aigw_schema_migrations WHERE version=16; DROP TABLE local_security_events,local_auth_throttle,local_access_tokens,local_users; DELETE FROM aigw_schema_migrations WHERE version=15; DROP TABLE keyword_policy_versions,keyword_matches,keyword_policies,budget_period_changes,budget_cycles,budget_limit_changes; DELETE FROM aigw_schema_migrations WHERE version=14; DROP TABLE provider_credential_events,provider_credentials; DELETE FROM aigw_schema_migrations WHERE version=13; DROP TABLE provider_configuration_events,provider_configuration; DELETE FROM aigw_schema_migrations WHERE version=12; DROP TABLE content_audit_access_log,content_audit_chain,content_audit_heads,content_audit_payloads,content_audit_records,content_audit_deks,content_audit_auditors,content_audit_policies; DELETE FROM aigw_schema_migrations WHERE version=11`)
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
	testutil.RevertSystemManagementMigration(t, pool)
	testutil.RevertDeepSeekMigration(t, pool)
	execSQL(t, pool, `DROP TABLE device_request_bindings,device_proof_nonces,device_sessions,device_policy_events,project_device_policies,device_state_events,terminal_devices; ALTER TABLE organization_member_lifecycle DROP COLUMN revoked_devices; DELETE FROM aigw_schema_migrations WHERE version=23; DROP TABLE budget_approval_requests,budget_approval_policies,budget_approval_policy_versions; DELETE FROM aigw_schema_migrations WHERE version=22; DROP TABLE request_department_attributions; DELETE FROM aigw_schema_migrations WHERE version=21; DROP TABLE department_versions,department_projects,department_members,departments,department_configurations; ALTER TABLE organization_member_lifecycle DROP COLUMN removed_department_grants; DELETE FROM aigw_schema_migrations WHERE version=20; DROP TABLE organization_member_lifecycle; DROP INDEX project_keys_creator_live; ALTER TABLE organization_members DROP CONSTRAINT organization_owner_enabled; ALTER TABLE organization_members DROP COLUMN lifecycle_revision,DROP COLUMN enabled; DELETE FROM aigw_schema_migrations WHERE version=19; DROP TABLE organization_ownership_transfers; DROP INDEX organization_single_owner; DELETE FROM aigw_schema_migrations WHERE version=18; DROP TABLE content_audit_checkpoint_parts,content_audit_checkpoints; DELETE FROM aigw_schema_migrations WHERE version=17; DROP TABLE budget_alert_policy_versions,budget_alerts,budget_alert_policies; DELETE FROM aigw_schema_migrations WHERE version=16; DROP TABLE local_security_events,local_auth_throttle,local_access_tokens,local_users; DELETE FROM aigw_schema_migrations WHERE version=15; DROP TABLE keyword_policy_versions,keyword_matches,keyword_policies,budget_period_changes,budget_cycles,budget_limit_changes; DELETE FROM aigw_schema_migrations WHERE version=14; DROP TABLE provider_credential_events,provider_credentials; DELETE FROM aigw_schema_migrations WHERE version=13; DROP TABLE provider_configuration_events,provider_configuration; DELETE FROM aigw_schema_migrations WHERE version=12; DROP TABLE content_audit_access_log,content_audit_chain,content_audit_heads,content_audit_payloads,content_audit_records,content_audit_deks,content_audit_auditors,content_audit_policies; DELETE FROM aigw_schema_migrations WHERE version=11`)
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
	testutil.RevertSystemManagementMigration(t, pool)
	testutil.RevertDeepSeekMigration(t, pool)
	execSQL(t, pool, `DROP TABLE device_request_bindings,device_proof_nonces,device_sessions,device_policy_events,project_device_policies,device_state_events,terminal_devices; ALTER TABLE organization_member_lifecycle DROP COLUMN revoked_devices; DELETE FROM aigw_schema_migrations WHERE version=23; DROP TABLE budget_approval_requests,budget_approval_policies,budget_approval_policy_versions; DELETE FROM aigw_schema_migrations WHERE version=22; DROP TABLE request_department_attributions; DELETE FROM aigw_schema_migrations WHERE version=21; DROP TABLE department_versions,department_projects,department_members,departments,department_configurations; ALTER TABLE organization_member_lifecycle DROP COLUMN removed_department_grants; DELETE FROM aigw_schema_migrations WHERE version=20; DROP TABLE organization_member_lifecycle; DROP INDEX project_keys_creator_live; ALTER TABLE organization_members DROP CONSTRAINT organization_owner_enabled; ALTER TABLE organization_members DROP COLUMN lifecycle_revision,DROP COLUMN enabled; DELETE FROM aigw_schema_migrations WHERE version=19; DROP TABLE organization_ownership_transfers; DROP INDEX organization_single_owner; DELETE FROM aigw_schema_migrations WHERE version=18; DROP TABLE content_audit_checkpoint_parts,content_audit_checkpoints; DELETE FROM aigw_schema_migrations WHERE version=17; DROP TABLE budget_alert_policy_versions,budget_alerts,budget_alert_policies; DELETE FROM aigw_schema_migrations WHERE version=16; DROP TABLE local_security_events,local_auth_throttle,local_access_tokens,local_users; DELETE FROM aigw_schema_migrations WHERE version=15; DROP TABLE keyword_policy_versions,keyword_matches,keyword_policies,budget_period_changes,budget_cycles,budget_limit_changes; DELETE FROM aigw_schema_migrations WHERE version=14; DROP TABLE provider_credential_events,provider_credentials; DELETE FROM aigw_schema_migrations WHERE version=13; DROP TABLE provider_configuration_events,provider_configuration; DELETE FROM aigw_schema_migrations WHERE version=12; DROP TABLE content_audit_access_log,content_audit_chain,content_audit_heads,content_audit_payloads,content_audit_records,content_audit_deks,content_audit_auditors,content_audit_policies; DELETE FROM aigw_schema_migrations WHERE version=11`)
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
	testutil.RevertSystemManagementMigration(t, pool)
	testutil.RevertDeepSeekMigration(t, pool)
	execSQL(t, pool, `DROP TABLE device_request_bindings,device_proof_nonces,device_sessions,device_policy_events,project_device_policies,device_state_events,terminal_devices; ALTER TABLE organization_member_lifecycle DROP COLUMN revoked_devices; DELETE FROM aigw_schema_migrations WHERE version=23; DROP TABLE budget_approval_requests,budget_approval_policies,budget_approval_policy_versions; DELETE FROM aigw_schema_migrations WHERE version=22; DROP TABLE request_department_attributions; DELETE FROM aigw_schema_migrations WHERE version=21; DROP TABLE department_versions,department_projects,department_members,departments,department_configurations; ALTER TABLE organization_member_lifecycle DROP COLUMN removed_department_grants; DELETE FROM aigw_schema_migrations WHERE version=20; DROP TABLE organization_member_lifecycle; DROP INDEX project_keys_creator_live; ALTER TABLE organization_members DROP CONSTRAINT organization_owner_enabled; ALTER TABLE organization_members DROP COLUMN lifecycle_revision,DROP COLUMN enabled; DELETE FROM aigw_schema_migrations WHERE version=19; DROP TABLE organization_ownership_transfers; DROP INDEX organization_single_owner; DELETE FROM aigw_schema_migrations WHERE version=18; DROP TABLE content_audit_checkpoint_parts,content_audit_checkpoints; DELETE FROM aigw_schema_migrations WHERE version=17; DROP TABLE budget_alert_policy_versions,budget_alerts,budget_alert_policies; DELETE FROM aigw_schema_migrations WHERE version=16; DROP TABLE local_security_events,local_auth_throttle,local_access_tokens,local_users; DELETE FROM aigw_schema_migrations WHERE version=15; DROP TABLE keyword_policy_versions,keyword_matches,keyword_policies,budget_period_changes,budget_cycles,budget_limit_changes; DELETE FROM aigw_schema_migrations WHERE version=14; DROP TABLE provider_credential_events,provider_credentials; DELETE FROM aigw_schema_migrations WHERE version=13; DROP TABLE provider_configuration_events,provider_configuration; DELETE FROM aigw_schema_migrations WHERE version=12; DROP TABLE content_audit_access_log,content_audit_chain,content_audit_heads,content_audit_payloads,content_audit_records,content_audit_deks,content_audit_auditors,content_audit_policies; DELETE FROM aigw_schema_migrations WHERE version=11`)
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
	testutil.RevertSystemManagementMigration(t, pool)
	testutil.RevertDeepSeekMigration(t, pool)
	execSQL(t, pool, `DROP TABLE device_request_bindings,device_proof_nonces,device_sessions,device_policy_events,project_device_policies,device_state_events,terminal_devices; ALTER TABLE organization_member_lifecycle DROP COLUMN revoked_devices; DELETE FROM aigw_schema_migrations WHERE version=23; DROP TABLE budget_approval_requests,budget_approval_policies,budget_approval_policy_versions; DELETE FROM aigw_schema_migrations WHERE version=22; DROP TABLE request_department_attributions; DELETE FROM aigw_schema_migrations WHERE version=21; DROP TABLE department_versions,department_projects,department_members,departments,department_configurations; ALTER TABLE organization_member_lifecycle DROP COLUMN removed_department_grants; DELETE FROM aigw_schema_migrations WHERE version=20; DROP TABLE organization_member_lifecycle; DROP INDEX project_keys_creator_live; ALTER TABLE organization_members DROP CONSTRAINT organization_owner_enabled; ALTER TABLE organization_members DROP COLUMN lifecycle_revision,DROP COLUMN enabled; DELETE FROM aigw_schema_migrations WHERE version=19; DROP TABLE organization_ownership_transfers; DROP INDEX organization_single_owner; DELETE FROM aigw_schema_migrations WHERE version=18; DROP TABLE content_audit_checkpoint_parts,content_audit_checkpoints; DELETE FROM aigw_schema_migrations WHERE version=17; DROP TABLE budget_alert_policy_versions,budget_alerts,budget_alert_policies; DELETE FROM aigw_schema_migrations WHERE version=16; DROP TABLE local_security_events,local_auth_throttle,local_access_tokens,local_users; DELETE FROM aigw_schema_migrations WHERE version=15; DROP TABLE keyword_policy_versions,keyword_matches,keyword_policies,budget_period_changes,budget_cycles,budget_limit_changes; DELETE FROM aigw_schema_migrations WHERE version=14; DROP TABLE provider_credential_events,provider_credentials; DELETE FROM aigw_schema_migrations WHERE version=13; DROP TABLE provider_configuration_events,provider_configuration; DELETE FROM aigw_schema_migrations WHERE version=12; DROP TABLE content_audit_access_log,content_audit_chain,content_audit_heads,content_audit_payloads,content_audit_records,content_audit_deks,content_audit_auditors,content_audit_policies; DELETE FROM aigw_schema_migrations WHERE version=11`)
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
	testutil.RevertSystemManagementMigration(t, pool)
	testutil.RevertDeepSeekMigration(t, pool)
	execSQL(t, pool, `DROP TABLE device_request_bindings,device_proof_nonces,device_sessions,device_policy_events,project_device_policies,device_state_events,terminal_devices; ALTER TABLE organization_member_lifecycle DROP COLUMN revoked_devices; DELETE FROM aigw_schema_migrations WHERE version=23; DROP TABLE budget_approval_requests,budget_approval_policies,budget_approval_policy_versions; DELETE FROM aigw_schema_migrations WHERE version=22; DROP TABLE request_department_attributions; DELETE FROM aigw_schema_migrations WHERE version=21; DROP TABLE department_versions,department_projects,department_members,departments,department_configurations; ALTER TABLE organization_member_lifecycle DROP COLUMN removed_department_grants; DELETE FROM aigw_schema_migrations WHERE version=20; DROP TABLE organization_member_lifecycle; DROP INDEX project_keys_creator_live; ALTER TABLE organization_members DROP CONSTRAINT organization_owner_enabled; ALTER TABLE organization_members DROP COLUMN lifecycle_revision,DROP COLUMN enabled; DELETE FROM aigw_schema_migrations WHERE version=19; DROP TABLE organization_ownership_transfers; DROP INDEX organization_single_owner; DELETE FROM aigw_schema_migrations WHERE version=18; DROP TABLE content_audit_checkpoint_parts,content_audit_checkpoints; DELETE FROM aigw_schema_migrations WHERE version=17; DROP TABLE budget_alert_policy_versions,budget_alerts,budget_alert_policies; DELETE FROM aigw_schema_migrations WHERE version=16; DROP TABLE local_security_events,local_auth_throttle,local_access_tokens,local_users; DELETE FROM aigw_schema_migrations WHERE version=15; DROP TABLE keyword_policy_versions,keyword_matches,keyword_policies,budget_period_changes,budget_cycles,budget_limit_changes; DELETE FROM aigw_schema_migrations WHERE version=14; DROP TABLE provider_credential_events,provider_credentials; DELETE FROM aigw_schema_migrations WHERE version=13; DROP TABLE provider_configuration_events,provider_configuration; DELETE FROM aigw_schema_migrations WHERE version=12; DROP TABLE content_audit_access_log,content_audit_chain,content_audit_heads,content_audit_payloads,content_audit_records,content_audit_deks,content_audit_auditors,content_audit_policies; DELETE FROM aigw_schema_migrations WHERE version=11`)
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
	testutil.RevertSystemManagementMigration(t, pool)
	testutil.RevertDeepSeekMigration(t, pool)
	execSQL(t, pool, `DROP TABLE device_request_bindings,device_proof_nonces,device_sessions,device_policy_events,project_device_policies,device_state_events,terminal_devices; ALTER TABLE organization_member_lifecycle DROP COLUMN revoked_devices; DELETE FROM aigw_schema_migrations WHERE version=23; DROP TABLE budget_approval_requests,budget_approval_policies,budget_approval_policy_versions; DELETE FROM aigw_schema_migrations WHERE version=22; DROP TABLE request_department_attributions; DELETE FROM aigw_schema_migrations WHERE version=21; DROP TABLE department_versions,department_projects,department_members,departments,department_configurations; ALTER TABLE organization_member_lifecycle DROP COLUMN removed_department_grants; DELETE FROM aigw_schema_migrations WHERE version=20; DROP TABLE organization_member_lifecycle; DROP INDEX project_keys_creator_live; ALTER TABLE organization_members DROP CONSTRAINT organization_owner_enabled; ALTER TABLE organization_members DROP COLUMN lifecycle_revision,DROP COLUMN enabled; DELETE FROM aigw_schema_migrations WHERE version=19; DROP TABLE organization_ownership_transfers; DROP INDEX organization_single_owner; DELETE FROM aigw_schema_migrations WHERE version=18; DROP TABLE content_audit_checkpoint_parts,content_audit_checkpoints; DELETE FROM aigw_schema_migrations WHERE version=17; DROP TABLE budget_alert_policy_versions,budget_alerts,budget_alert_policies; DELETE FROM aigw_schema_migrations WHERE version=16; DROP TABLE local_security_events,local_auth_throttle,local_access_tokens,local_users; DELETE FROM aigw_schema_migrations WHERE version=15; DROP TABLE keyword_policy_versions,keyword_matches,keyword_policies,budget_period_changes,budget_cycles,budget_limit_changes; DELETE FROM aigw_schema_migrations WHERE version=14; DROP TABLE provider_credential_events,provider_credentials; DELETE FROM aigw_schema_migrations WHERE version=13; DROP TABLE provider_configuration_events,provider_configuration; DELETE FROM aigw_schema_migrations WHERE version=12; DROP TABLE content_audit_access_log,content_audit_chain,content_audit_heads,content_audit_payloads,content_audit_records,content_audit_deks,content_audit_auditors,content_audit_policies; DELETE FROM aigw_schema_migrations WHERE version=11; ALTER TABLE aigw_schema_migrations ADD CONSTRAINT reject_v11 CHECK(version<>11)`)
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
	testutil.RevertSystemManagementMigration(t, pool)
	testutil.RevertDeepSeekMigration(t, pool)
	execSQL(t, pool, `DROP TABLE device_request_bindings,device_proof_nonces,device_sessions,device_policy_events,project_device_policies,device_state_events,terminal_devices; ALTER TABLE organization_member_lifecycle DROP COLUMN revoked_devices; DELETE FROM aigw_schema_migrations WHERE version=23; DROP TABLE budget_approval_requests,budget_approval_policies,budget_approval_policy_versions; DELETE FROM aigw_schema_migrations WHERE version=22; DROP TABLE request_department_attributions; DELETE FROM aigw_schema_migrations WHERE version=21; DROP TABLE department_versions,department_projects,department_members,departments,department_configurations; ALTER TABLE organization_member_lifecycle DROP COLUMN removed_department_grants; DELETE FROM aigw_schema_migrations WHERE version=20; DROP TABLE organization_member_lifecycle; DROP INDEX project_keys_creator_live; ALTER TABLE organization_members DROP CONSTRAINT organization_owner_enabled; ALTER TABLE organization_members DROP COLUMN lifecycle_revision,DROP COLUMN enabled; DELETE FROM aigw_schema_migrations WHERE version=19; DROP TABLE organization_ownership_transfers; DROP INDEX organization_single_owner; DELETE FROM aigw_schema_migrations WHERE version=18; DROP TABLE content_audit_checkpoint_parts,content_audit_checkpoints; DELETE FROM aigw_schema_migrations WHERE version=17; DROP TABLE budget_alert_policy_versions,budget_alerts,budget_alert_policies; DELETE FROM aigw_schema_migrations WHERE version=16; DROP TABLE local_security_events,local_auth_throttle,local_access_tokens,local_users; DELETE FROM aigw_schema_migrations WHERE version=15; DROP TABLE keyword_policy_versions,keyword_matches,keyword_policies,budget_period_changes,budget_cycles,budget_limit_changes; DELETE FROM aigw_schema_migrations WHERE version=14; DROP TABLE provider_credential_events,provider_credentials; DELETE FROM aigw_schema_migrations WHERE version=13; DROP TABLE provider_configuration_events,provider_configuration; DELETE FROM aigw_schema_migrations WHERE version=12; ALTER TABLE aigw_schema_migrations ADD CONSTRAINT reject_v12 CHECK(version<>12)`)
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
	testutil.RevertSystemManagementMigration(t, pool)
	testutil.RevertDeepSeekMigration(t, pool)
	execSQL(t, pool, `DROP TABLE device_request_bindings,device_proof_nonces,device_sessions,device_policy_events,project_device_policies,device_state_events,terminal_devices; ALTER TABLE organization_member_lifecycle DROP COLUMN revoked_devices; DELETE FROM aigw_schema_migrations WHERE version=23; DROP TABLE budget_approval_requests,budget_approval_policies,budget_approval_policy_versions; DELETE FROM aigw_schema_migrations WHERE version=22; DROP TABLE request_department_attributions; DELETE FROM aigw_schema_migrations WHERE version=21; DROP TABLE department_versions,department_projects,department_members,departments,department_configurations; ALTER TABLE organization_member_lifecycle DROP COLUMN removed_department_grants; DELETE FROM aigw_schema_migrations WHERE version=20; DROP TABLE organization_member_lifecycle; DROP INDEX project_keys_creator_live; ALTER TABLE organization_members DROP CONSTRAINT organization_owner_enabled; ALTER TABLE organization_members DROP COLUMN lifecycle_revision,DROP COLUMN enabled; DELETE FROM aigw_schema_migrations WHERE version=19; DROP TABLE organization_ownership_transfers; DROP INDEX organization_single_owner; DELETE FROM aigw_schema_migrations WHERE version=18; DROP TABLE content_audit_checkpoint_parts,content_audit_checkpoints; DELETE FROM aigw_schema_migrations WHERE version=17; DROP TABLE budget_alert_policy_versions,budget_alerts,budget_alert_policies; DELETE FROM aigw_schema_migrations WHERE version=16; DROP TABLE local_security_events,local_auth_throttle,local_access_tokens,local_users; DELETE FROM aigw_schema_migrations WHERE version=15; DROP TABLE keyword_policy_versions,keyword_matches,keyword_policies,budget_period_changes,budget_cycles,budget_limit_changes; DELETE FROM aigw_schema_migrations WHERE version=14; DROP TABLE provider_credential_events,provider_credentials; DELETE FROM aigw_schema_migrations WHERE version=13; ALTER TABLE aigw_schema_migrations ADD CONSTRAINT reject_v13 CHECK(version<>13)`)
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

func TestBudgetKeywordMigrationRollback(t *testing.T) {
	ctx := context.Background()
	pool := testutil.EmptyDatabase(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	testutil.RevertSystemManagementMigration(t, pool)
	testutil.RevertDeepSeekMigration(t, pool)
	execSQL(t, pool, `DROP TABLE device_request_bindings,device_proof_nonces,device_sessions,device_policy_events,project_device_policies,device_state_events,terminal_devices; ALTER TABLE organization_member_lifecycle DROP COLUMN revoked_devices; DELETE FROM aigw_schema_migrations WHERE version=23; DROP TABLE budget_approval_requests,budget_approval_policies,budget_approval_policy_versions; DELETE FROM aigw_schema_migrations WHERE version=22; DROP TABLE request_department_attributions; DELETE FROM aigw_schema_migrations WHERE version=21; DROP TABLE department_versions,department_projects,department_members,departments,department_configurations; ALTER TABLE organization_member_lifecycle DROP COLUMN removed_department_grants; DELETE FROM aigw_schema_migrations WHERE version=20; DROP TABLE organization_member_lifecycle; DROP INDEX project_keys_creator_live; ALTER TABLE organization_members DROP CONSTRAINT organization_owner_enabled; ALTER TABLE organization_members DROP COLUMN lifecycle_revision,DROP COLUMN enabled; DELETE FROM aigw_schema_migrations WHERE version=19; DROP TABLE organization_ownership_transfers; DROP INDEX organization_single_owner; DELETE FROM aigw_schema_migrations WHERE version=18; DROP TABLE content_audit_checkpoint_parts,content_audit_checkpoints; DELETE FROM aigw_schema_migrations WHERE version=17; DROP TABLE budget_alert_policy_versions,budget_alerts,budget_alert_policies; DELETE FROM aigw_schema_migrations WHERE version=16; DROP TABLE local_security_events,local_auth_throttle,local_access_tokens,local_users; DELETE FROM aigw_schema_migrations WHERE version=15; DROP TABLE keyword_policy_versions,keyword_matches,keyword_policies,budget_period_changes,budget_cycles,budget_limit_changes; DELETE FROM aigw_schema_migrations WHERE version=14; ALTER TABLE aigw_schema_migrations ADD CONSTRAINT reject_v14 CHECK(version<>14)`)
	if database.Migrate(ctx, pool) == nil {
		t.Fatal("migration should fail")
	}
	if relation(t, pool, "keyword_policies") || relation(t, pool, "budget_cycles") {
		t.Fatal("failed migration leaked tables")
	}
	execSQL(t, pool, `ALTER TABLE aigw_schema_migrations DROP CONSTRAINT reject_v14`)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := database.Check(ctx, pool); err != nil {
		t.Fatal(err)
	}
}

func TestLocalIdentityMigrationRollback(t *testing.T) {
	ctx := context.Background()
	pool := testutil.EmptyDatabase(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	testutil.RevertSystemManagementMigration(t, pool)
	testutil.RevertDeepSeekMigration(t, pool)
	execSQL(t, pool, `DROP TABLE device_request_bindings,device_proof_nonces,device_sessions,device_policy_events,project_device_policies,device_state_events,terminal_devices; ALTER TABLE organization_member_lifecycle DROP COLUMN revoked_devices; DELETE FROM aigw_schema_migrations WHERE version=23; DROP TABLE budget_approval_requests,budget_approval_policies,budget_approval_policy_versions; DELETE FROM aigw_schema_migrations WHERE version=22; DROP TABLE request_department_attributions; DELETE FROM aigw_schema_migrations WHERE version=21; DROP TABLE department_versions,department_projects,department_members,departments,department_configurations; ALTER TABLE organization_member_lifecycle DROP COLUMN removed_department_grants; DELETE FROM aigw_schema_migrations WHERE version=20; DROP TABLE organization_member_lifecycle; DROP INDEX project_keys_creator_live; ALTER TABLE organization_members DROP CONSTRAINT organization_owner_enabled; ALTER TABLE organization_members DROP COLUMN lifecycle_revision,DROP COLUMN enabled; DELETE FROM aigw_schema_migrations WHERE version=19; DROP TABLE organization_ownership_transfers; DROP INDEX organization_single_owner; DELETE FROM aigw_schema_migrations WHERE version=18; DROP TABLE content_audit_checkpoint_parts,content_audit_checkpoints; DELETE FROM aigw_schema_migrations WHERE version=17; DROP TABLE budget_alert_policy_versions,budget_alerts,budget_alert_policies; DELETE FROM aigw_schema_migrations WHERE version=16; DROP TABLE local_security_events,local_auth_throttle,local_access_tokens,local_users; DELETE FROM aigw_schema_migrations WHERE version=15; ALTER TABLE aigw_schema_migrations ADD CONSTRAINT reject_v15 CHECK(version<>15)`)
	if database.Migrate(ctx, pool) == nil {
		t.Fatal("migration should fail")
	}
	if relation(t, pool, "local_users") {
		t.Fatal("failed migration leaked identity tables")
	}
	execSQL(t, pool, `ALTER TABLE aigw_schema_migrations DROP CONSTRAINT reject_v15`)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := database.Check(ctx, pool); err != nil {
		t.Fatal(err)
	}
}

func TestBudgetAlertMigrationRollbackAndHeldBudgetPreservation(t *testing.T) {
	pool, ledger, scope := testutil.Database(t)
	ctx := context.Background()
	testutil.Accounts(t, ledger, scope, 100, 100)
	if _, err := ledger.Reserve(ctx, budget.Quote{Scope: scope, RequestID: "before-alerts", PriceVersion: "synthetic", Amount: 10}); err != nil {
		t.Fatal(err)
	}
	testutil.RevertSystemManagementMigration(t, pool)
	testutil.RevertDeepSeekMigration(t, pool)
	execSQL(t, pool, `DROP TABLE device_request_bindings,device_proof_nonces,device_sessions,device_policy_events,project_device_policies,device_state_events,terminal_devices; ALTER TABLE organization_member_lifecycle DROP COLUMN revoked_devices; DELETE FROM aigw_schema_migrations WHERE version=23; DROP TABLE budget_approval_requests,budget_approval_policies,budget_approval_policy_versions; DELETE FROM aigw_schema_migrations WHERE version=22; DROP TABLE request_department_attributions; DELETE FROM aigw_schema_migrations WHERE version=21; DROP TABLE department_versions,department_projects,department_members,departments,department_configurations; ALTER TABLE organization_member_lifecycle DROP COLUMN removed_department_grants; DELETE FROM aigw_schema_migrations WHERE version=20; DROP TABLE organization_member_lifecycle; DROP INDEX project_keys_creator_live; ALTER TABLE organization_members DROP CONSTRAINT organization_owner_enabled; ALTER TABLE organization_members DROP COLUMN lifecycle_revision,DROP COLUMN enabled; DELETE FROM aigw_schema_migrations WHERE version=19; DROP TABLE organization_ownership_transfers; DROP INDEX organization_single_owner; DELETE FROM aigw_schema_migrations WHERE version=18; DROP TABLE content_audit_checkpoint_parts,content_audit_checkpoints; DELETE FROM aigw_schema_migrations WHERE version=17; DROP TABLE budget_alert_policy_versions,budget_alerts,budget_alert_policies; DELETE FROM aigw_schema_migrations WHERE version=16; ALTER TABLE aigw_schema_migrations ADD CONSTRAINT reject_v16 CHECK(version<>16)`)
	if database.Migrate(ctx, pool) == nil {
		t.Fatal("expected migration failure")
	}
	if relation(t, pool, "budget_alerts") || status(t, pool).CurrentVersion != 15 {
		t.Fatal("failed stage leaked schema")
	}
	execSQL(t, pool, `ALTER TABLE aigw_schema_migrations DROP CONSTRAINT reject_v16`)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := database.Check(ctx, pool); err != nil {
		t.Fatal(err)
	}
	r, err := ledger.Get(ctx, "before-alerts")
	if err != nil || r.State != "reserved" || r.Amount != 10 {
		t.Fatal("migration changed held budget")
	}
}

func TestAuditCheckpointMigrationRollbackPreservesHolds(t *testing.T) {
	pool, ledger, scope := testutil.Database(t)
	ctx := context.Background()
	testutil.Accounts(t, ledger, scope, 100, 100)
	if _, err := ledger.Reserve(ctx, budget.Quote{Scope: scope, RequestID: "before-checkpoint", PriceVersion: "synthetic", Amount: 10}); err != nil {
		t.Fatal(err)
	}
	testutil.RevertSystemManagementMigration(t, pool)
	testutil.RevertDeepSeekMigration(t, pool)
	execSQL(t, pool, `DROP TABLE device_request_bindings,device_proof_nonces,device_sessions,device_policy_events,project_device_policies,device_state_events,terminal_devices; ALTER TABLE organization_member_lifecycle DROP COLUMN revoked_devices; DELETE FROM aigw_schema_migrations WHERE version=23; DROP TABLE budget_approval_requests,budget_approval_policies,budget_approval_policy_versions; DELETE FROM aigw_schema_migrations WHERE version=22; DROP TABLE request_department_attributions; DELETE FROM aigw_schema_migrations WHERE version=21; DROP TABLE department_versions,department_projects,department_members,departments,department_configurations; ALTER TABLE organization_member_lifecycle DROP COLUMN removed_department_grants; DELETE FROM aigw_schema_migrations WHERE version=20; DROP TABLE organization_member_lifecycle; DROP INDEX project_keys_creator_live; ALTER TABLE organization_members DROP CONSTRAINT organization_owner_enabled; ALTER TABLE organization_members DROP COLUMN lifecycle_revision,DROP COLUMN enabled; DELETE FROM aigw_schema_migrations WHERE version=19; DROP TABLE organization_ownership_transfers; DROP INDEX organization_single_owner; DELETE FROM aigw_schema_migrations WHERE version=18; DROP TABLE content_audit_checkpoint_parts,content_audit_checkpoints; DELETE FROM aigw_schema_migrations WHERE version=17; ALTER TABLE aigw_schema_migrations ADD CONSTRAINT reject_v17 CHECK(version<>17)`)
	if database.Migrate(ctx, pool) == nil {
		t.Fatal("expected migration rollback")
	}
	if relation(t, pool, "content_audit_checkpoints") || status(t, pool).CurrentVersion != 16 {
		t.Fatal("failed checkpoint migration leaked schema")
	}
	execSQL(t, pool, `ALTER TABLE aigw_schema_migrations DROP CONSTRAINT reject_v17`)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := database.Check(ctx, pool); err != nil {
		t.Fatal(err)
	}
	r, err := ledger.Get(ctx, "before-checkpoint")
	if err != nil || r.State != "reserved" || r.Amount != 10 {
		t.Fatal("checkpoint upgrade modified finance")
	}
}

func TestOwnershipMigrationRollbackPreservesSingleOwnerAndData(t *testing.T) {
	pool, ledger, scope := testutil.Database(t)
	ctx := context.Background()
	testutil.Accounts(t, ledger, scope, 100, 100)
	if _, err := ledger.Reserve(ctx, budget.Quote{Scope: scope, RequestID: "before-ownership", PriceVersion: "synthetic", Amount: 10}); err != nil {
		t.Fatal(err)
	}
	testutil.RevertSystemManagementMigration(t, pool)
	testutil.RevertDeepSeekMigration(t, pool)
	execSQL(t, pool, `DROP TABLE device_request_bindings,device_proof_nonces,device_sessions,device_policy_events,project_device_policies,device_state_events,terminal_devices; ALTER TABLE organization_member_lifecycle DROP COLUMN revoked_devices; DELETE FROM aigw_schema_migrations WHERE version=23; DROP TABLE budget_approval_requests,budget_approval_policies,budget_approval_policy_versions; DELETE FROM aigw_schema_migrations WHERE version=22; DROP TABLE request_department_attributions; DELETE FROM aigw_schema_migrations WHERE version=21; DROP TABLE department_versions,department_projects,department_members,departments,department_configurations; ALTER TABLE organization_member_lifecycle DROP COLUMN removed_department_grants; DELETE FROM aigw_schema_migrations WHERE version=20; DROP TABLE organization_member_lifecycle; DROP INDEX project_keys_creator_live; ALTER TABLE organization_members DROP CONSTRAINT organization_owner_enabled; ALTER TABLE organization_members DROP COLUMN lifecycle_revision,DROP COLUMN enabled; DELETE FROM aigw_schema_migrations WHERE version=19; DROP TABLE organization_ownership_transfers; DROP INDEX organization_single_owner; DELETE FROM aigw_schema_migrations WHERE version=18; ALTER TABLE aigw_schema_migrations ADD CONSTRAINT reject_v18 CHECK(version<>18)`)
	if database.Migrate(ctx, pool) == nil {
		t.Fatal("expected failed migration")
	}
	if relation(t, pool, "organization_ownership_transfers") || relation(t, pool, "organization_single_owner") || status(t, pool).CurrentVersion != 17 {
		t.Fatal("failed handoff migration leaked schema")
	}
	execSQL(t, pool, `ALTER TABLE aigw_schema_migrations DROP CONSTRAINT reject_v18`)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := database.Check(ctx, pool); err != nil {
		t.Fatal(err)
	}
	r, err := ledger.Get(ctx, "before-ownership")
	if err != nil || r.State != "reserved" || r.Amount != 10 {
		t.Fatal("handoff migration modified finance")
	}
}

func TestOwnershipUpgradeRejectsAmbiguousLegacyOwners(t *testing.T) {
	pool, _, _ := testutil.Database(t)
	ctx := context.Background()
	store := control.New(pool)
	actor := identity.Principal{Issuer: "https://synthetic.invalid", Subject: "alice"}
	org, err := store.CreateOrganization(ctx, actor, "ambiguous legacy", "2026-11", 100)
	if err != nil {
		t.Fatal(err)
	}
	testutil.RevertSystemManagementMigration(t, pool)
	testutil.RevertDeepSeekMigration(t, pool)
	execSQL(t, pool, `DROP TABLE device_request_bindings,device_proof_nonces,device_sessions,device_policy_events,project_device_policies,device_state_events,terminal_devices; ALTER TABLE organization_member_lifecycle DROP COLUMN revoked_devices; DELETE FROM aigw_schema_migrations WHERE version=23; DROP TABLE budget_approval_requests,budget_approval_policies,budget_approval_policy_versions; DELETE FROM aigw_schema_migrations WHERE version=22; DROP TABLE request_department_attributions; DELETE FROM aigw_schema_migrations WHERE version=21; DROP TABLE department_versions,department_projects,department_members,departments,department_configurations; ALTER TABLE organization_member_lifecycle DROP COLUMN removed_department_grants; DELETE FROM aigw_schema_migrations WHERE version=20; DROP TABLE organization_member_lifecycle; DROP INDEX project_keys_creator_live; ALTER TABLE organization_members DROP CONSTRAINT organization_owner_enabled; ALTER TABLE organization_members DROP COLUMN lifecycle_revision,DROP COLUMN enabled; DELETE FROM aigw_schema_migrations WHERE version=19; DROP TABLE organization_ownership_transfers; DROP INDEX organization_single_owner; DELETE FROM aigw_schema_migrations WHERE version=18`)
	execSQL(t, pool, `INSERT INTO organization_members(organization_id,issuer,subject,role) VALUES($1,$2,'second-owner','owner')`, org.ID, actor.Issuer)
	if database.Migrate(ctx, pool) == nil {
		t.Fatal("ambiguous owners silently accepted")
	}
	if relation(t, pool, "organization_ownership_transfers") || status(t, pool).CurrentVersion != 17 {
		t.Fatal("invalid ownership upgrade partially applied")
	}
	var owners int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM organization_members WHERE organization_id=$1 AND role='owner'`, org.ID).Scan(&owners); err != nil || owners != 2 {
		t.Fatal("migration silently chose an owner")
	}
}

func TestMemberLifecycleMigrationRollbackPreservesStateAndBudget(t *testing.T) {
	pool, ledger, scope := testutil.Database(t)
	ctx := context.Background()
	testutil.Accounts(t, ledger, scope, 100, 100)
	if _, err := ledger.Reserve(ctx, budget.Quote{Scope: scope, RequestID: "before-lifecycle", PriceVersion: "synthetic", Amount: 10}); err != nil {
		t.Fatal(err)
	}
	testutil.RevertSystemManagementMigration(t, pool)
	testutil.RevertDeepSeekMigration(t, pool)
	execSQL(t, pool, `DROP TABLE device_request_bindings,device_proof_nonces,device_sessions,device_policy_events,project_device_policies,device_state_events,terminal_devices; ALTER TABLE organization_member_lifecycle DROP COLUMN revoked_devices; DELETE FROM aigw_schema_migrations WHERE version=23; DROP TABLE budget_approval_requests,budget_approval_policies,budget_approval_policy_versions; DELETE FROM aigw_schema_migrations WHERE version=22; DROP TABLE request_department_attributions; DELETE FROM aigw_schema_migrations WHERE version=21; DROP TABLE department_versions,department_projects,department_members,departments,department_configurations; ALTER TABLE organization_member_lifecycle DROP COLUMN removed_department_grants; DELETE FROM aigw_schema_migrations WHERE version=20; DROP TABLE organization_member_lifecycle; DROP INDEX project_keys_creator_live; ALTER TABLE organization_members DROP CONSTRAINT organization_owner_enabled; ALTER TABLE organization_members DROP COLUMN lifecycle_revision,DROP COLUMN enabled; DELETE FROM aigw_schema_migrations WHERE version=19; ALTER TABLE aigw_schema_migrations ADD CONSTRAINT reject_v19 CHECK(version<>19)`)
	if database.Migrate(ctx, pool) == nil {
		t.Fatal("expected failed lifecycle upgrade")
	}
	if relation(t, pool, "organization_member_lifecycle") || status(t, pool).CurrentVersion != 18 {
		t.Fatal("failed lifecycle stage leaked schema")
	}
	execSQL(t, pool, `ALTER TABLE aigw_schema_migrations DROP CONSTRAINT reject_v19`)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := database.Check(ctx, pool); err != nil {
		t.Fatal(err)
	}
	r, err := ledger.Get(ctx, "before-lifecycle")
	if err != nil || r.State != "reserved" || r.Amount != 10 {
		t.Fatal("lifecycle migration modified budget")
	}
}

func TestDepartmentMigrationRollbackPreservesHeldBudget(t *testing.T) {
	pool, ledger, scope := testutil.Database(t)
	ctx := context.Background()
	testutil.Accounts(t, ledger, scope, 100, 100)
	if _, err := ledger.Reserve(ctx, budget.Quote{Scope: scope, RequestID: "before-departments", PriceVersion: "synthetic", Amount: 10}); err != nil {
		t.Fatal(err)
	}
	testutil.RevertSystemManagementMigration(t, pool)
	testutil.RevertDeepSeekMigration(t, pool)
	execSQL(t, pool, `DROP TABLE device_request_bindings,device_proof_nonces,device_sessions,device_policy_events,project_device_policies,device_state_events,terminal_devices; ALTER TABLE organization_member_lifecycle DROP COLUMN revoked_devices; DELETE FROM aigw_schema_migrations WHERE version=23; DROP TABLE budget_approval_requests,budget_approval_policies,budget_approval_policy_versions; DELETE FROM aigw_schema_migrations WHERE version=22; DROP TABLE request_department_attributions; DELETE FROM aigw_schema_migrations WHERE version=21; DROP TABLE department_versions,department_projects,department_members,departments,department_configurations; ALTER TABLE organization_member_lifecycle DROP COLUMN removed_department_grants; DELETE FROM aigw_schema_migrations WHERE version=20; ALTER TABLE aigw_schema_migrations ADD CONSTRAINT reject_v20 CHECK(version<>20)`)
	if database.Migrate(ctx, pool) == nil {
		t.Fatal("expected failed department upgrade")
	}
	if relation(t, pool, "departments") || status(t, pool).CurrentVersion != 19 {
		t.Fatal("failed department migration leaked schema")
	}
	execSQL(t, pool, `ALTER TABLE aigw_schema_migrations DROP CONSTRAINT reject_v20`)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := database.Check(ctx, pool); err != nil {
		t.Fatal(err)
	}
	r, err := ledger.Get(ctx, "before-departments")
	if err != nil || r.State != "reserved" || r.Amount != 10 {
		t.Fatal("department migration changed held budget")
	}
}

func TestDepartmentAttributionMigrationPreservesHistoricalUnknownAndHeldBudget(t *testing.T) {
	pool, ledger, scope := testutil.Database(t)
	ctx := context.Background()
	testutil.Accounts(t, ledger, scope, 100, 100)
	if _, err := ledger.Reserve(ctx, budget.Quote{Scope: scope, RequestID: "before-department-cost", PriceVersion: "synthetic", Amount: 10}); err != nil {
		t.Fatal(err)
	}
	testutil.RevertSystemManagementMigration(t, pool)
	testutil.RevertDeepSeekMigration(t, pool)
	execSQL(t, pool, `DROP TABLE device_request_bindings,device_proof_nonces,device_sessions,device_policy_events,project_device_policies,device_state_events,terminal_devices; ALTER TABLE organization_member_lifecycle DROP COLUMN revoked_devices; DELETE FROM aigw_schema_migrations WHERE version=23; DROP TABLE budget_approval_requests,budget_approval_policies,budget_approval_policy_versions; DELETE FROM aigw_schema_migrations WHERE version=22; DROP TABLE request_department_attributions; DELETE FROM aigw_schema_migrations WHERE version=21; ALTER TABLE aigw_schema_migrations ADD CONSTRAINT reject_v21 CHECK(version<>21)`)
	if database.Migrate(ctx, pool) == nil {
		t.Fatal("expected failed attribution migration")
	}
	if relation(t, pool, "request_department_attributions") || status(t, pool).CurrentVersion != 20 {
		t.Fatal("failed attribution migration leaked state")
	}
	execSQL(t, pool, `ALTER TABLE aigw_schema_migrations DROP CONSTRAINT reject_v21`)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := database.Check(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM request_department_attributions`).Scan(&count); err != nil || count != 0 {
		t.Fatal("migration invented historical attribution")
	}
	r, err := ledger.Get(ctx, "before-department-cost")
	if err != nil || r.State != "reserved" || r.Amount != 10 {
		t.Fatal("attribution upgrade rewrote finance")
	}
}

func TestBudgetApprovalMigrationRollbackPreservesBalance(t *testing.T) {
	pool, ledger, scope := testutil.Database(t)
	ctx := context.Background()
	testutil.Accounts(t, ledger, scope, 100, 100)
	if _, err := ledger.Reserve(ctx, budget.Quote{Scope: scope, RequestID: "before-approval", PriceVersion: "synthetic", Amount: 10}); err != nil {
		t.Fatal(err)
	}
	testutil.RevertSystemManagementMigration(t, pool)
	testutil.RevertDeepSeekMigration(t, pool)
	execSQL(t, pool, `DROP TABLE device_request_bindings,device_proof_nonces,device_sessions,device_policy_events,project_device_policies,device_state_events,terminal_devices; ALTER TABLE organization_member_lifecycle DROP COLUMN revoked_devices; DELETE FROM aigw_schema_migrations WHERE version=23; DROP TABLE budget_approval_requests,budget_approval_policies,budget_approval_policy_versions; DELETE FROM aigw_schema_migrations WHERE version=22; ALTER TABLE aigw_schema_migrations ADD CONSTRAINT reject_v22 CHECK(version<>22)`)
	if database.Migrate(ctx, pool) == nil {
		t.Fatal("expected approval migration failure")
	}
	if relation(t, pool, "budget_approval_requests") || status(t, pool).CurrentVersion != 21 {
		t.Fatal("failed approval migration leaked schema")
	}
	execSQL(t, pool, `ALTER TABLE aigw_schema_migrations DROP CONSTRAINT reject_v22`)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := database.Check(ctx, pool); err != nil {
		t.Fatal(err)
	}
	r, err := ledger.Get(ctx, "before-approval")
	if err != nil || r.Amount != 10 || r.State != "reserved" {
		t.Fatal("approval migration rewrote ledger")
	}
}

func TestDeviceAdmissionMigrationRollbackPreservesLedger(t *testing.T) {
	pool, ledger, scope := testutil.Database(t)
	ctx := context.Background()
	testutil.Accounts(t, ledger, scope, 100, 100)
	if _, err := ledger.Reserve(ctx, budget.Quote{Scope: scope, RequestID: "before-device", PriceVersion: "synthetic", Amount: 10}); err != nil {
		t.Fatal(err)
	}
	testutil.RevertSystemManagementMigration(t, pool)
	testutil.RevertDeepSeekMigration(t, pool)
	execSQL(t, pool, `DROP TABLE device_request_bindings,device_proof_nonces,device_sessions,device_policy_events,project_device_policies,device_state_events,terminal_devices; ALTER TABLE organization_member_lifecycle DROP COLUMN revoked_devices; DELETE FROM aigw_schema_migrations WHERE version=23; ALTER TABLE aigw_schema_migrations ADD CONSTRAINT reject_v23 CHECK(version<>23)`)
	if database.Migrate(ctx, pool) == nil {
		t.Fatal("expected failed device migration")
	}
	if relation(t, pool, "terminal_devices") || status(t, pool).CurrentVersion != 22 {
		t.Fatal("failed device migration leaked tables")
	}
	execSQL(t, pool, `ALTER TABLE aigw_schema_migrations DROP CONSTRAINT reject_v23`)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := database.Check(ctx, pool); err != nil {
		t.Fatal(err)
	}
	r, err := ledger.Get(ctx, "before-device")
	if err != nil || r.State != "reserved" || r.Amount != 10 {
		t.Fatal("device migration modified old finance")
	}
}

func TestDeepSeekMigrationRollbackAndUpgradePreserveLedger(t *testing.T) {
	pool, ledger, scope := testutil.Database(t)
	ctx := context.Background()
	testutil.Accounts(t, ledger, scope, 100, 100)
	if _, err := ledger.Reserve(ctx, budget.Quote{Scope: scope, RequestID: "before-deepseek", PriceVersion: "synthetic", Amount: 10}); err != nil {
		t.Fatal(err)
	}
	testutil.RevertSystemManagementMigration(t, pool)
	testutil.RevertDeepSeekMigration(t, pool)
	if !errors.Is(database.Check(ctx, pool), database.ErrPending) {
		t.Fatal("v23 must require upgrade")
	}
	execSQL(t, pool, `ALTER TABLE aigw_schema_migrations ADD CONSTRAINT reject_v24 CHECK(version<>24)`)
	if database.Migrate(ctx, pool) == nil {
		t.Fatal("expected migration failure")
	}
	if status(t, pool).CurrentVersion != 23 {
		t.Fatal("failed upgrade changed history")
	}
	var allowed bool
	if err := pool.QueryRow(ctx, `SELECT position('deepseek' in pg_get_constraintdef(oid))>0 FROM pg_constraint WHERE conrelid='provider_credentials'::regclass AND conname='provider_credentials_provider_check'`).Scan(&allowed); err != nil || allowed {
		t.Fatal("failed upgrade leaked constraint changes")
	}
	execSQL(t, pool, `ALTER TABLE aigw_schema_migrations DROP CONSTRAINT reject_v24`)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := database.Check(ctx, pool); err != nil {
		t.Fatal(err)
	}
	r, err := ledger.Get(ctx, "before-deepseek")
	if err != nil || r.State != "reserved" || r.Amount != 10 {
		t.Fatal("upgrade changed existing budget")
	}
}

func TestSystemManagementMigrationUpgradeAndRollback(t *testing.T) {
	pool := testutil.EmptyDatabase(t)
	ctx := context.Background()
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	testutil.RevertSystemManagementMigration(t, pool)
	execSQL(t, pool, `ALTER TABLE aigw_schema_migrations ADD CONSTRAINT reject_v25 CHECK(version<>25)`)
	if err := database.Migrate(ctx, pool); err == nil {
		t.Fatal("failed migration accepted")
	}
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('system_management_events') IS NOT NULL`).Scan(&exists); err != nil || exists {
		t.Fatal("DDL did not roll back")
	}
	execSQL(t, pool, `ALTER TABLE aigw_schema_migrations DROP CONSTRAINT reject_v25`)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := database.Check(ctx, pool); err != nil {
		t.Fatal(err)
	}
	execSQL(t, pool, `ALTER TABLE system_management_events DROP COLUMN reason`)
	if err := database.Check(ctx, pool); err == nil {
		t.Fatal("system event schema drift accepted")
	}
}

func TestEmbeddedCompletionUpgradeRollbackAndReadiness(t *testing.T) {
	pool, ledger, scope := testutil.Database(t)
	ctx := context.Background()
	testutil.Accounts(t, ledger, scope, 100, 100)
	if _, err := ledger.Reserve(ctx, budget.Quote{Scope: scope, RequestID: "before-completion", PriceVersion: "old-price", Amount: 10}); err != nil {
		t.Fatal(err)
	}
	testutil.RevertEmbeddedCompletionMigration(t, pool)
	execSQL(t, pool, `ALTER TABLE aigw_schema_migrations ADD CONSTRAINT reject_v32 CHECK(version<>32)`)
	if err := database.Migrate(ctx, pool); err == nil {
		t.Fatal("failed completion migration accepted")
	}
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('engine_execution_bindings') IS NOT NULL`).Scan(&exists); err != nil || exists {
		t.Fatal("completion DDL did not roll back")
	}
	execSQL(t, pool, `ALTER TABLE aigw_schema_migrations DROP CONSTRAINT reject_v32`)
	for range 2 {
		if err := database.Migrate(ctx, pool); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.Check(ctx, pool); err != nil {
		t.Fatal(err)
	}
	r, err := ledger.Get(ctx, "before-completion")
	if err != nil || r.State != "reserved" || r.Amount != 10 {
		t.Fatal("historical hold changed")
	}
	execSQL(t, pool, `ALTER TABLE engine_completion_events DROP COLUMN usage_sha256`)
	if err := database.Check(ctx, pool); err == nil {
		t.Fatal("evidence schema drift accepted")
	}
}

func TestNewAPIUpgradeRollbackAndReadiness(t *testing.T) {
	pool, ledger, scope := testutil.Database(t)
	ctx := context.Background()
	testutil.Accounts(t, ledger, scope, 100, 100)
	if _, err := ledger.Reserve(ctx, budget.Quote{Scope: scope, RequestID: "before-newapi", PriceVersion: "old-price", Amount: 10}); err != nil {
		t.Fatal(err)
	}
	testutil.RevertNewAPIMigration(t, pool)
	execSQL(t, pool, `ALTER TABLE aigw_schema_migrations ADD CONSTRAINT reject_v26 CHECK(version<>26)`)
	if err := database.Migrate(ctx, pool); err == nil {
		t.Fatal("failed migration accepted")
	}
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('newapi_execution_bindings') IS NOT NULL`).Scan(&exists); err != nil || exists {
		t.Fatal("DDL did not roll back")
	}
	execSQL(t, pool, `ALTER TABLE aigw_schema_migrations DROP CONSTRAINT reject_v26`)
	for range 2 {
		if err := database.Migrate(ctx, pool); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.Check(ctx, pool); err != nil {
		t.Fatal(err)
	}
	reservation, err := ledger.Get(ctx, "before-newapi")
	if err != nil || reservation.Amount != 10 || reservation.State != "reserved" {
		t.Fatal("upgrade changed historical hold")
	}
	execSQL(t, pool, `ALTER TABLE newapi_execution_receipts DROP COLUMN actual`)
	if err := database.Check(ctx, pool); err == nil {
		t.Fatal("receipt schema drift accepted")
	}
}

func TestGovernedSupplierUpgradeRollbackAndDrift(t *testing.T) {
	pool, ledger, scope := testutil.Database(t)
	ctx := context.Background()
	testutil.Accounts(t, ledger, scope, 100, 100)
	if _, err := ledger.Reserve(ctx, budget.Quote{Scope: scope, RequestID: "supplier-upgrade", PriceVersion: "old-price", Amount: 10}); err != nil {
		t.Fatal(err)
	}
	testutil.RevertGovernedSuppliersMigration(t, pool)
	execSQL(t, pool, `ALTER TABLE aigw_schema_migrations ADD CONSTRAINT reject_v27 CHECK(version<>27)`)
	if err := database.Migrate(ctx, pool); err == nil {
		t.Fatal("failed supplier upgrade accepted")
	}
	var definition string
	if err := pool.QueryRow(ctx, `SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conrelid='inference_attempts'::regclass AND conname='inference_attempts_provider_check'`).Scan(&definition); err != nil || !strings.Contains(definition, "deepseek") {
		t.Fatal("failed upgrade did not restore old constraint")
	}
	execSQL(t, pool, `ALTER TABLE aigw_schema_migrations DROP CONSTRAINT reject_v27`)
	for range 2 {
		if err := database.Migrate(ctx, pool); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.Check(ctx, pool); err != nil {
		t.Fatal(err)
	}
	execSQL(t, pool, `INSERT INTO inference_attempts(attempt_id,request_id,model,provider,region,stream) VALUES('supplier-attempt','supplier-upgrade','reviewed-compatible/fixture','reviewed-compatible','cn-mainland',false)`)
	reservation, err := ledger.Get(ctx, "supplier-upgrade")
	if err != nil || reservation.State != "reserved" || reservation.Amount != 10 {
		t.Fatal("supplier upgrade altered existing hold")
	}
	if _, err := pool.Exec(ctx, `UPDATE inference_attempts SET provider='https://not-an-identifier' WHERE attempt_id='supplier-attempt'`); err == nil {
		t.Fatal("invalid supplier identity accepted")
	}
	execSQL(t, pool, `ALTER TABLE inference_attempts DROP CONSTRAINT inference_attempts_provider_check`)
	if err := database.Check(ctx, pool); err == nil {
		t.Fatal("supplier constraint drift accepted")
	}
}

func TestEmbeddingUpgradeRollbackAndReadiness(t *testing.T) {
	pool, _, _ := testutil.Database(t)
	ctx := context.Background()
	testutil.RevertEmbeddingMigration(t, pool)
	execSQL(t, pool, `ALTER TABLE aigw_schema_migrations ADD CONSTRAINT reject_v28 CHECK(version<>28)`)
	if database.Migrate(ctx, pool) == nil {
		t.Fatal("failed embedding upgrade accepted")
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_constraint WHERE conrelid='newapi_execution_bindings'::regclass AND conname='newapi_execution_bindings_output_pair_check'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("failed upgrade left new constraint")
	}
	execSQL(t, pool, `ALTER TABLE aigw_schema_migrations DROP CONSTRAINT reject_v28`)
	for range 2 {
		if err := database.Migrate(ctx, pool); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.Check(ctx, pool); err != nil {
		t.Fatal(err)
	}
	execSQL(t, pool, `ALTER TABLE newapi_execution_bindings DROP CONSTRAINT newapi_execution_bindings_output_pair_check`)
	if database.Check(ctx, pool) == nil {
		t.Fatal("missing input-only billing constraint accepted")
	}
}

func TestCacheTariffUpgradeRollbackAndReadiness(t *testing.T) {
	pool, _, _ := testutil.Database(t)
	ctx := context.Background()
	testutil.RevertCacheTariffMigration(t, pool)
	execSQL(t, pool, `ALTER TABLE aigw_schema_migrations ADD CONSTRAINT reject_v29 CHECK(version<>29)`)
	if database.Migrate(ctx, pool) == nil {
		t.Fatal("failed cache tariff upgrade accepted")
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_constraint WHERE conrelid='newapi_execution_bindings'::regclass AND conname='newapi_cache_tariff_check'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("failed upgrade left new constraint")
	}
	execSQL(t, pool, `ALTER TABLE aigw_schema_migrations DROP CONSTRAINT reject_v29`)
	for range 2 {
		if err := database.Migrate(ctx, pool); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.Check(ctx, pool); err != nil {
		t.Fatal(err)
	}
	execSQL(t, pool, `ALTER TABLE newapi_execution_bindings DROP CONSTRAINT newapi_cache_tariff_check`)
	if database.Check(ctx, pool) == nil {
		t.Fatal("missing cache tariff constraint accepted")
	}
}

func TestCacheTTLUpgradeRollbackAndReadiness(t *testing.T) {
	pool, _, _ := testutil.Database(t)
	ctx := context.Background()
	testutil.RevertCacheTTLMigration(t, pool)
	execSQL(t, pool, `ALTER TABLE aigw_schema_migrations ADD CONSTRAINT reject_v30 CHECK(version<>30)`)
	if database.Migrate(ctx, pool) == nil {
		t.Fatal("failed cache tariff upgrade accepted")
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_constraint WHERE conrelid='newapi_execution_bindings'::regclass AND conname='newapi_cache_ttl_tariff_check'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("failed upgrade left new constraint")
	}
	execSQL(t, pool, `ALTER TABLE aigw_schema_migrations DROP CONSTRAINT reject_v30`)
	for range 2 {
		if err := database.Migrate(ctx, pool); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.Check(ctx, pool); err != nil {
		t.Fatal(err)
	}
	execSQL(t, pool, `ALTER TABLE newapi_execution_bindings DROP CONSTRAINT newapi_cache_ttl_tariff_check`)
	if database.Check(ctx, pool) == nil {
		t.Fatal("missing cache tariff constraint accepted")
	}
}

func TestChannelManagementUpgradeRollbackAndReadiness(t *testing.T) {
	pool, ledger, scope := testutil.Database(t)
	ctx := context.Background()
	testutil.Accounts(t, ledger, scope, 100, 100)
	testutil.RevertChannelManagementMigration(t, pool)
	execSQL(t, pool, `ALTER TABLE aigw_schema_migrations ADD CONSTRAINT reject_v31 CHECK(version<>31)`)
	if database.Migrate(ctx, pool) == nil {
		t.Fatal("failed channel management upgrade accepted")
	}
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('newapi_channel_operations') IS NOT NULL`).Scan(&exists); err != nil || exists {
		t.Fatal("failed migration left administration tables")
	}
	execSQL(t, pool, `ALTER TABLE aigw_schema_migrations DROP CONSTRAINT reject_v31`)
	for range 2 {
		if err := database.Migrate(ctx, pool); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.Check(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var accounts int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM budget_accounts`).Scan(&accounts); err != nil || accounts != 2 {
		t.Fatal("channel migration changed existing accounts")
	}
	execSQL(t, pool, `ALTER TABLE newapi_channel_operations DROP COLUMN data_plane_origin`)
	if database.Check(ctx, pool) == nil {
		t.Fatal("administration schema drift accepted")
	}
}

func TestEmbeddedEmbeddingsUpgradePreservesReceiptsAndRollsBack(t *testing.T) {
	pool, ledger, scope := testutil.Database(t)
	ctx := context.Background()
	scope.Currency = "CNY_MICRO"
	testutil.Accounts(t, ledger, scope, 10000, 10000)
	testutil.SeedEmbeddedCompletion(t, pool, scope, "before-embedding", "receipted")
	testutil.RevertEmbeddedEmbeddingsMigration(t, pool)
	before := status(t, pool)
	if before.CurrentVersion != 32 || before.Ready || len(before.Pending) != 3 {
		t.Fatal("released schema not detected")
	}
	execSQL(t, pool, `ALTER TABLE aigw_schema_migrations ADD CONSTRAINT reject_v33 CHECK(version<>33)`)
	if err := database.Migrate(ctx, pool); err == nil {
		t.Fatal("failed migration accepted")
	}
	var constraint string
	if err := pool.QueryRow(ctx, `SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conrelid='engine_execution_bindings'::regclass AND conname='engine_execution_bindings_protocol_check'`).Scan(&constraint); err != nil || strings.Contains(constraint, "embeddings_v1") {
		t.Fatal("partial schema committed", err)
	}
	execSQL(t, pool, `ALTER TABLE aigw_schema_migrations DROP CONSTRAINT reject_v33`)
	for range 2 {
		if err := database.Migrate(ctx, pool); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.Check(ctx, pool); err != nil {
		t.Fatal(err)
	}
	r, err := ledger.Get(ctx, "before-embedding")
	if err != nil || r.Actual == nil || *r.Actual != 14 || r.State != "settled" {
		t.Fatal("historical receipt changed")
	}
	var receipts int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM engine_completion_receipts WHERE actual=14`).Scan(&receipts); err != nil || receipts != 1 {
		t.Fatal("receipt lost")
	}
	execSQL(t, pool, `ALTER TABLE engine_execution_bindings DROP CONSTRAINT engine_execution_bindings_protocol_limits`)
	if err := database.Check(ctx, pool); err == nil {
		t.Fatal("protocol constraint drift accepted")
	}
}

func TestEmbeddedCacheUpgradeRollbackAndReadiness(t *testing.T) {
	pool, ledger, scope := testutil.Database(t)
	ctx := context.Background()
	scope.Currency = "CNY_MICRO"
	testutil.Accounts(t, ledger, scope, 10000, 10000)
	testutil.SeedEmbeddedCompletion(t, pool, scope, "before-cache-chat", "receipted")
	testutil.SeedEmbeddedEmbeddingCompletion(t, pool, scope, "before-cache-embedding", "receipted")
	testutil.RevertEmbeddedCacheMigration(t, pool)
	before := status(t, pool)
	if before.CurrentVersion != 33 || before.Ready || len(before.Pending) != 2 {
		t.Fatal("v33 not detected")
	}
	execSQL(t, pool, `ALTER TABLE aigw_schema_migrations ADD CONSTRAINT reject_v34 CHECK(version<>34)`)
	if err := database.Migrate(ctx, pool); err == nil {
		t.Fatal("failed migration accepted")
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_attribute WHERE attrelid='engine_execution_bindings'::regclass AND attname='cache_read_price' AND NOT attisdropped`).Scan(&count); err != nil || count != 0 {
		t.Fatal("partial upgrade committed", err)
	}
	execSQL(t, pool, `ALTER TABLE aigw_schema_migrations DROP CONSTRAINT reject_v34`)
	for range 2 {
		if err := database.Migrate(ctx, pool); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.Check(ctx, pool); err != nil {
		t.Fatal(err)
	}
	for id, amount := range map[string]int64{"before-cache-chat": 14, "before-cache-embedding": 6} {
		r, err := ledger.Get(ctx, id)
		if err != nil || r.Actual == nil || *r.Actual != amount || r.State != "settled" {
			t.Fatal("historical receipt changed", err)
		}
	}
	testutil.SeedEmbeddedCacheCompletion(t, pool, scope, "after-cache-upgrade", "receipted")
	execSQL(t, pool, `ALTER TABLE engine_completion_events DROP CONSTRAINT engine_completion_events_cache_counts`)
	if err := database.Check(ctx, pool); err == nil {
		t.Fatal("cache constraint drift accepted")
	}
}

func TestNativeMessagesUpgradeRollbackAndReadiness(t *testing.T) {
	pool, ledger, scope := testutil.Database(t)
	ctx := context.Background()
	scope.Currency = "CNY_MICRO"
	testutil.Accounts(t, ledger, scope, 10000, 10000)
	testutil.SeedEmbeddedCacheCompletion(t, pool, scope, "before-native-messages", "receipted")
	testutil.RevertEmbeddedMessagesMigration(t, pool)
	before := status(t, pool)
	if before.CurrentVersion != 34 || before.Ready || len(before.Pending) != 1 {
		t.Fatal("v34 not detected")
	}
	execSQL(t, pool, `ALTER TABLE aigw_schema_migrations ADD CONSTRAINT reject_v35 CHECK(version<>35)`)
	if err := database.Migrate(ctx, pool); err == nil {
		t.Fatal("failed migration accepted")
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_attribute WHERE attrelid='engine_completion_events'::regclass AND attname='cache_write_tokens' AND NOT attisdropped`).Scan(&count); err != nil || count != 0 {
		t.Fatal("partial upgrade committed", err)
	}
	execSQL(t, pool, `ALTER TABLE aigw_schema_migrations DROP CONSTRAINT reject_v35`)
	for range 2 {
		if err := database.Migrate(ctx, pool); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.Check(ctx, pool); err != nil {
		t.Fatal(err)
	}
	old, err := ledger.Get(ctx, "before-native-messages")
	if err != nil || old.Actual == nil || *old.Actual != 11 {
		t.Fatal("old cache receipt changed")
	}
	testutil.SeedEmbeddedMessagesCompletion(t, pool, scope, "after-native-messages", "receipted")
	var write *int64
	if err := pool.QueryRow(ctx, `SELECT cache_write_tokens FROM engine_completion_events WHERE attempt_id='attempt-before-native-messages'`).Scan(&write); err != nil || write != nil {
		t.Fatal("old absent write count fabricated", err)
	}
	if err := pool.QueryRow(ctx, `SELECT cache_write_tokens FROM engine_completion_events WHERE attempt_id='attempt-after-native-messages'`).Scan(&write); err != nil || write == nil || *write != 0 {
		t.Fatal("original explicit zero lost", err)
	}
	execSQL(t, pool, `ALTER TABLE engine_completion_events DROP CONSTRAINT engine_completion_events_cache_writes`)
	if err := database.Check(ctx, pool); err == nil {
		t.Fatal("write count constraint drift accepted")
	}
}
