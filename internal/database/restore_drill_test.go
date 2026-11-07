//go:build restore_e2e

package database_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jhzhfree/ai-governance-gateway/internal/budget"
	"github.com/jhzhfree/ai-governance-gateway/internal/database"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
)

// Only randomly created fixture schemas are renamed/restored. Never accepts a
// caller-supplied schema, uses --clean, or drops an existing application schema.
func TestLogicalBackupRestore(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Fatal("dedicated TEST_DATABASE_URL required")
	}
	for _, tool := range []string{"pg_dump", "pg_restore"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("%s required", tool)
		}
	}
	for _, previous := range []bool{false, true} {
		t.Run(fmt.Sprintf("previous_%t", previous), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			pool, store, scope := testutil.Database(t)
			testutil.Accounts(t, store, scope, 10000, 10000)
			for _, id := range []string{"settled", "reserved", "dispatched", "uncertain"} {
				if _, err := store.Reserve(ctx, budget.Quote{Scope: scope, RequestID: id, PriceVersion: "synthetic-v1", Amount: 100}); err != nil {
					t.Fatal(err)
				}
				if id != "reserved" {
					if err := store.Dispatch(ctx, id); err != nil {
						t.Fatal(err)
					}
				}
				if id == "settled" {
					if err := store.Settle(ctx, id, 40); err != nil {
						t.Fatal(err)
					}
				}
				if id == "uncertain" {
					if err := store.MarkUncertain(ctx, id); err != nil {
						t.Fatal(err)
					}
				}
			}
			execSQL(t, pool, `INSERT INTO provider_credentials(provider,revision,key_id,nonce,ciphertext) VALUES('deepseek',1,'synthetic-key',decode(repeat('01',12),'hex'),decode(repeat('02',32),'hex'))`)
			if previous {
				testutil.RevertSystemManagementMigration(t, pool)
			} else {
				execSQL(t, pool, `INSERT INTO system_management_events(operation_id,node,actor_issuer,actor_subject,action,phase,reason,payload) VALUES('fixture-operation','fixture-node','fixture-issuer','fixture-subject','apply','requested','restore rehearsal','{"fixture":true}')`)
			}

			if !previous {
				execSQL(t, pool, `INSERT INTO inference_attempts(attempt_id,request_id,model,provider,region,stream) VALUES('restore_attempt_12345','settled','reviewed-compatible/fixture','reviewed-compatible','cn-mainland',false)`)
				execSQL(t, pool, `INSERT INTO newapi_execution_bindings(attempt_id,request_id,backend,model,channel_id,fingerprint,price_version,input_price,output_price,max_input,max_output,recovery_complete) VALUES('restore_attempt_12345','settled','https://synthetic.invalid','fixture-model',1,repeat('a',64),'synthetic-v1',2000000,4000000,128,8,true)`)
				execSQL(t, pool, `INSERT INTO newapi_execution_receipts(attempt_id,state,input_tokens,output_tokens,actual) VALUES('restore_attempt_12345','completed',4,8,40)`)
			}
			var schema string
			if err := pool.QueryRow(ctx, "SELECT current_schema()").Scan(&schema); err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(schema, "poc_") {
				t.Fatal("not a generated fixture schema")
			}
			before := fixtureDigest(t, ctx, pool, schema)
			archive := filepath.Join(t.TempDir(), "fixture.dump")
			f, err := os.OpenFile(archive, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				t.Fatal(err)
			}
			f.Close()
			runPG := func(tool string, args ...string) error {
				cmd := exec.CommandContext(ctx, tool, args...)
				// Credentials stay in the child environment, never argv or test output.
				cmd.Env = restoreEnvironment(t)
				if err := cmd.Run(); err != nil {
					return fmt.Errorf("%s failed (diagnostics suppressed to protect connection data)", tool)
				}
				return nil
			}
			if err := runPG("pg_dump", "--no-password", "--format=custom", "--strict-names", "--schema="+pgx.Identifier{schema}.Sanitize(), "--file="+archive); err != nil {
				t.Fatal(err)
			}
			saved := schema + "_source"
			execSQL(t, pool, "ALTER SCHEMA "+pgx.Identifier{schema}.Sanitize()+" RENAME TO "+pgx.Identifier{saved}.Sanitize())
			t.Cleanup(func() { execSQL(t, pool, "DROP SCHEMA "+pgx.Identifier{saved}.Sanitize()+" CASCADE") })
			data, err := os.ReadFile(archive)
			if err != nil {
				t.Fatal(err)
			}
			broken := filepath.Join(filepath.Dir(archive), "truncated.dump")
			if err := os.WriteFile(broken, data[:len(data)/2], 0600); err != nil {
				t.Fatal(err)
			}
			restore := func(file string) error {
				return runPG("pg_restore", "--no-password", "--exit-on-error", "--single-transaction", "--no-owner", "--no-acl", "--dbname=", file)
			}
			// An empty --dbname makes libpq use PGDATABASE without putting its value in argv.
			if err := restore(broken); err == nil {
				t.Fatal("truncated archive unexpectedly restored")
			}
			var absent bool
			if err := pool.QueryRow(ctx, "SELECT to_regnamespace($1) IS NULL", schema).Scan(&absent); err != nil || !absent {
				t.Fatal("failed restore left partial schema")
			}
			if err := restore(archive); err != nil {
				t.Fatal(err)
			}
			// Discard prepared plans that may still refer to the preserved source OIDs.
			pool.Reset()
			if after := fixtureDigest(t, ctx, pool, schema); after != before {
				t.Fatal("restored tables or sequences differ")
			}
			if previous {
				if err := database.Check(ctx, pool); !errors.Is(err, database.ErrPending) {
					t.Fatal("previous backup not recognized as pending")
				}
			}
			if err := database.Migrate(ctx, pool); err != nil {
				t.Fatal(err)
			}
			if err := database.Check(ctx, pool); err != nil {
				t.Fatal(err)
			}
			if n, err := store.Recover(ctx, time.Now().Add(time.Minute)); err != nil || n != 1 {
				t.Fatalf("recovery: count=%d error=%v", n, err)
			}
			for id, want := range map[string]string{"reserved": "reserved", "dispatched": "uncertain", "uncertain": "uncertain", "settled": "settled"} {
				var state string
				if err := pool.QueryRow(ctx, "SELECT state FROM budget_reservations WHERE request_id=$1", id).Scan(&state); err != nil || state != want {
					t.Fatal("restored reservation changed incorrectly")
				}
			}
			var spent, held int64
			if err := pool.QueryRow(ctx, "SELECT spent,held FROM budget_accounts WHERE project_id=$1", scope.Project).Scan(&spent, &held); err != nil || spent != 40 || held != 300 {
				t.Fatal("restored holds/spend not preserved")
			}
			var id int64
			if err := pool.QueryRow(ctx, `INSERT INTO system_management_events(operation_id,node,actor_issuer,actor_subject,action,phase,reason,payload) VALUES('after-restore','fixture-node','fixture-issuer','fixture-subject','apply','requested','restored identity sequence','{}') RETURNING id`).Scan(&id); err != nil || (!previous && id <= 1) {
				t.Fatal("restored identity sequence cannot continue")
			}
		})
	}
}

// Hash canonical row JSON in deterministic order, including bytea, timestamps,
// migration history and every fixture table; never print its contents.
func fixtureDigest(t *testing.T, ctx context.Context, pool *pgxpool.Pool, schema string) [32]byte {
	t.Helper()
	rows, err := pool.Query(ctx, `SELECT c.relname,c.relkind::text FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relkind IN ('r','p','S') ORDER BY c.relname`, schema)
	if err != nil {
		t.Fatal(err)
	}
	type relation struct{ name, kind string }
	var rels []relation
	for rows.Next() {
		var r relation
		if err := rows.Scan(&r.name, &r.kind); err != nil {
			t.Fatal(err)
		}
		rels = append(rels, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	h := sha256.New()
	for _, r := range rels {
		fmt.Fprintf(h, "%d:%s:%s\n", len(r.name), r.name, r.kind)
		from := pgx.Identifier{schema, r.name}.Sanitize()
		query := "SELECT to_jsonb(t)::text FROM " + from + " t ORDER BY 1"
		if r.kind == "S" {
			query = "SELECT jsonb_build_array(last_value,is_called)::text FROM " + from
		}
		rows, err := pool.Query(ctx, query)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var value string
			if err := rows.Scan(&value); err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(h, "%d:%s\n", len(value), value)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
	}
	var digest [32]byte
	copy(digest[:], h.Sum(nil))
	return digest
}

// libpq does not expand a URI stored in PGDATABASE; provide its fields through
// child-only environment variables. Reject unsupported URI options explicitly.
func restoreEnvironment(t *testing.T) []string {
	t.Helper()
	u, err := url.Parse(os.Getenv("TEST_DATABASE_URL"))
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Hostname() == "" || u.User == nil {
		t.Fatal("restore drill requires a PostgreSQL URI with host and user")
	}
	password, _ := u.User.Password()
	values := map[string]string{"PGHOST": u.Hostname(), "PGPORT": u.Port(), "PGUSER": u.User.Username(), "PGPASSWORD": password, "PGDATABASE": strings.TrimPrefix(u.Path, "/"), "PGSSLMODE": "prefer"}
	if values["PGPORT"] == "" {
		values["PGPORT"] = "5432"
	}
	options := map[string]string{"sslmode": "PGSSLMODE", "sslrootcert": "PGSSLROOTCERT", "sslcert": "PGSSLCERT", "sslkey": "PGSSLKEY", "connect_timeout": "PGCONNECT_TIMEOUT", "application_name": "PGAPPNAME", "target_session_attrs": "PGTARGETSESSIONATTRS", "channel_binding": "PGCHANNELBINDING"}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		t.Fatal("invalid test database URI options")
	}
	for key, v := range query {
		env, ok := options[key]
		if !ok || len(v) != 1 {
			t.Fatal("unsupported or repeated test database URI option")
		}
		values[env] = v[0]
	}
	// Do not inherit libpq service/database overrides from the operator's shell.
	var result []string
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "PG") && !strings.HasPrefix(entry, "TEST_DATABASE_URL=") {
			result = append(result, entry)
		}
	}
	for key, value := range values {
		result = append(result, key+"="+value)
	}
	return result
}
