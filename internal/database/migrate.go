// Package database owns append-only PostgreSQL migrations and schema readiness.
package database

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrUnmanaged   = errors.New("schema has no migration history; run migrate up")
	ErrPending     = errors.New("database migrations pending; run migrate up")
	ErrNewer       = errors.New("database version is newer than this binary")
	ErrHistory     = errors.New("invalid migration history")
	ErrChecksum    = errors.New("applied migration checksum differs from embedded SQL")
	ErrSchemaDrift = errors.New("managed schema differs from its recorded structure")
	ErrLegacy      = errors.New("legacy schema does not match a complete supported prototype stage")
	ErrSearchPath  = errors.New("search_path must select a non-system application schema")
)

//go:embed migrations/*.sql
var files embed.FS

type migration struct {
	version             int
	name, sql, checksum string
	tables              []string
}

// New migrations that create tables must register them here. Versions 1-3 are
// the only supported unversioned prototype snapshots; never expand that boundary.
const legacyBaselineVersion = 3

var managedTables = map[int][]string{
	1: {"budget_accounts", "budget_reservations", "budget_outbox"},
	2: {"organizations", "organization_members", "projects", "project_members", "project_models", "project_keys", "audit_events"},
	3: {"browser_login_flows", "browser_sessions"},
	7: {"inference_attempts"},
	8: {"project_limits", "admission_events", "admission_leases"},
}

func plan() ([]migration, error) {
	entries, err := fs.ReadDir(files, "migrations")
	if err != nil {
		return nil, err
	}
	result := []migration{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		parts := strings.SplitN(entry.Name(), "_", 2)
		if len(parts) != 2 {
			return nil, ErrHistory
		}
		version, err := strconv.Atoi(parts[0])
		if err != nil || version != len(result)+1 {
			return nil, ErrHistory
		}
		data, err := files.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(data)
		result = append(result, migration{version: version, name: entry.Name(), sql: string(data), checksum: hex.EncodeToString(sum[:]), tables: managedTables[version]})
	}
	if len(result) < legacyBaselineVersion {
		return nil, ErrHistory
	}
	return result, nil
}

type Applied struct {
	Version    int       `json:"version"`
	Name       string    `json:"name"`
	Checksum   string    `json:"checksum"`
	SchemaHash string    `json:"schema_hash"`
	Adopted    bool      `json:"adopted"`
	AppliedAt  time.Time `json:"applied_at"`
}
type Report struct {
	Schema         string    `json:"schema"`
	Managed        bool      `json:"managed"`
	Ready          bool      `json:"ready"`
	CurrentVersion int       `json:"current_version"`
	LatestVersion  int       `json:"latest_version"`
	Pending        []string  `json:"pending"`
	Applied        []Applied `json:"applied"`
}

// operationError prevents SQL errors from exposing row values or connection strings.
type operationError struct {
	version int
	cause   error
}

func (e *operationError) Error() string {
	var pg *pgconn.PgError
	if errors.As(e.cause, &pg) {
		return fmt.Sprintf("migration %d database operation failed (SQLSTATE %s)", e.version, pg.Code)
	}
	return fmt.Sprintf("migration %d database operation failed", e.version)
}
func (e *operationError) Unwrap() error { return e.cause }
func safe(version int, err error) error {
	if err == nil {
		return nil
	}
	for _, known := range []error{ErrUnmanaged, ErrPending, ErrNewer, ErrHistory, ErrChecksum, ErrSchemaDrift, ErrLegacy, ErrSearchPath, context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, known) {
			return fmt.Errorf("migration %d: %w", version, known)
		}
	}
	return &operationError{version: version, cause: err}
}
func currentSchema(ctx context.Context, tx pgx.Tx) (string, error) {
	var schema string
	if err := tx.QueryRow(ctx, `SELECT COALESCE(pg_catalog.current_schema(),'')`).Scan(&schema); err != nil {
		return "", err
	}
	if schema == "" || strings.HasPrefix(schema, "pg_") || schema == "information_schema" {
		return "", ErrSearchPath
	}
	return schema, nil
}
func tablesThrough(migrations []migration, version int) []string {
	var names []string
	for _, m := range migrations {
		if m.version <= version {
			names = append(names, m.tables...)
		}
	}
	return names
}
func inspect(ctx context.Context, tx pgx.Tx, schema string, migrations []migration) (r Report, err error) {
	r = Report{Schema: schema, LatestVersion: len(migrations), Pending: []string{}, Applied: []Applied{}}
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT pg_catalog.to_regclass($1) IS NOT NULL`, pgx.Identifier{schema, "aigw_schema_migrations"}.Sanitize()).Scan(&exists); err != nil {
		return
	}
	r.Managed = exists
	if exists {
		if err = searchPath(ctx, tx, schema); err != nil {
			return
		}
		var rows pgx.Rows
		rows, err = tx.Query(ctx, `SELECT version,name,checksum,schema_hash,adopted,applied_at FROM aigw_schema_migrations ORDER BY version`)
		if err != nil {
			return
		}
		for rows.Next() {
			var a Applied
			if e := rows.Scan(&a.Version, &a.Name, &a.Checksum, &a.SchemaHash, &a.Adopted, &a.AppliedAt); e != nil {
				rows.Close()
				return r, e
			}
			r.Applied = append(r.Applied, a)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return
		}
		for index, a := range r.Applied {
			if a.Version > len(migrations) {
				return r, ErrNewer
			}
			if a.Version != index+1 {
				return r, ErrHistory
			}
			m := migrations[index]
			if a.Checksum != m.checksum || a.Name != m.name {
				return r, ErrChecksum
			}
			decoded, e := hex.DecodeString(a.SchemaHash)
			if e != nil || len(decoded) != 32 {
				return r, ErrHistory
			}
			r.CurrentVersion = a.Version
		}
		if r.CurrentVersion > 0 {
			var actual string
			actual, err = fingerprint(ctx, tx, schema, tablesThrough(migrations, r.CurrentVersion))
			if err != nil {
				return
			}
			if actual != r.Applied[len(r.Applied)-1].SchemaHash {
				return r, ErrSchemaDrift
			}
		}
	}
	for _, m := range migrations {
		if m.version > r.CurrentVersion {
			r.Pending = append(r.Pending, m.name)
		}
	}
	r.Ready = r.Managed && len(r.Pending) == 0
	return
}

// Status is read-only: it never creates a ledger, scratch schema or table.
func Status(ctx context.Context, pool *pgxpool.Pool) (r Report, err error) {
	migrations, err := plan()
	if err != nil {
		return r, err
	}
	err = pgx.BeginTxFunc(ctx, pool, pgx.TxOptions{AccessMode: pgx.ReadOnly, IsoLevel: pgx.RepeatableRead}, func(tx pgx.Tx) error {
		schema, e := currentSchema(ctx, tx)
		if e != nil {
			return e
		}
		r, e = inspect(ctx, tx, schema, migrations)
		return e
	})
	return r, safe(r.CurrentVersion, err)
}
func Check(ctx context.Context, pool *pgxpool.Pool) error {
	r, err := Status(ctx, pool)
	if err != nil {
		return err
	}
	if !r.Managed {
		return ErrUnmanaged
	}
	if !r.Ready {
		return ErrPending
	}
	return nil
}

const ledgerDDL = `CREATE TABLE IF NOT EXISTS aigw_schema_migrations (
 version integer PRIMARY KEY CHECK(version>0),name text NOT NULL,checksum text NOT NULL,
 schema_hash text NOT NULL,adopted boolean NOT NULL,applied_at timestamptz NOT NULL DEFAULT clock_timestamp()
)`

// Migrate serializes all up-migrations for this schema and applies the entire
// pending batch atomically. No down/force/repair bypass is provided.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	migrations, err := plan()
	if err != nil {
		return err
	}
	version := 0
	err = pgx.BeginTxFunc(ctx, pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		schema, err := currentSchema(ctx, tx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT pg_catalog.pg_advisory_xact_lock(pg_catalog.hashtextextended($1,0))`, "aigw:migrations:"+schema); err != nil {
			return err
		}
		r, err := inspect(ctx, tx, schema, migrations)
		if err != nil {
			return err
		}
		version = r.CurrentVersion
		if r.Ready {
			return nil
		}
		adopt := map[int]bool{}
		if r.CurrentVersion == 0 {
			adopt, err = legacy(ctx, tx, schema, migrations)
			if err != nil {
				return err
			}
		}
		if err := searchPath(ctx, tx, schema); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, ledgerDDL); err != nil {
			return err
		}
		for _, m := range migrations {
			if m.version <= r.CurrentVersion {
				continue
			}
			version = m.version
			if !adopt[m.version] {
				if _, err := tx.Exec(ctx, m.sql); err != nil {
					return err
				}
			}
			hash, err := fingerprint(ctx, tx, schema, tablesThrough(migrations, m.version))
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO aigw_schema_migrations(version,name,checksum,schema_hash,adopted) VALUES($1,$2,$3,$4,$5)`, m.version, m.name, m.checksum, hash, adopt[m.version]); err != nil {
				return err
			}
		}
		return nil
	})
	return safe(version, err)
}

func legacy(ctx context.Context, tx pgx.Tx, schema string, migrations []migration) (map[int]bool, error) {
	adopt := map[int]bool{}
	for _, m := range migrations {
		if m.version > legacyBaselineVersion {
			continue
		}
		count := 0
		for _, name := range m.tables {
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname=$2)`, schema, name).Scan(&exists); err != nil {
				return nil, err
			}
			if exists {
				count++
			}
		}
		if count != 0 && count != len(m.tables) {
			return nil, ErrLegacy
		}
		if count > 0 {
			adopt[m.version] = true
		}
	}
	if len(adopt) == 0 {
		return adopt, nil
	}
	scratch := "aigw_expected_" + strings.ToLower(rand.Text())
	if _, err := tx.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{scratch}.Sanitize()); err != nil {
		return nil, err
	}
	if err := searchPath(ctx, tx, scratch); err != nil {
		return nil, err
	}
	for _, m := range migrations {
		if m.version <= legacyBaselineVersion {
			if _, err := tx.Exec(ctx, m.sql); err != nil {
				return nil, err
			}
		}
	}
	for _, m := range migrations {
		if !adopt[m.version] {
			continue
		}
		expected, err := fingerprint(ctx, tx, scratch, m.tables)
		if err != nil {
			return nil, err
		}
		actual, err := fingerprint(ctx, tx, schema, m.tables)
		if err != nil {
			return nil, err
		}
		if actual != expected {
			return nil, ErrLegacy
		}
	}
	if err := searchPath(ctx, tx, schema); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, "DROP SCHEMA "+pgx.Identifier{scratch}.Sanitize()+" CASCADE"); err != nil {
		return nil, err
	}
	return adopt, nil
}
