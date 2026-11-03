package database

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

type objectDefinition struct {
	Kind       string
	Name       string
	Definition string
}
type tableDefinition struct {
	Name    string
	Objects []objectDefinition
}

// Definitions intentionally exclude data, sequence counters, owner and comments.
// Setting the search path makes same-schema FK names comparable to a scratch
// schema; explicit qualifiers in index definitions are normalized below.
func catalog(ctx context.Context, tx pgx.Tx, schema string, names []string) ([]tableDefinition, error) {
	if err := searchPath(ctx, tx, schema); err != nil {
		return nil, err
	}
	var quoted string
	if err := tx.QueryRow(ctx, `SELECT pg_catalog.quote_ident($1)`, schema).Scan(&quoted); err != nil {
		return nil, err
	}
	names = append([]string(nil), names...)
	sort.Strings(names)
	result := make([]tableDefinition, 0, len(names))
	for _, name := range names {
		var oid uint32
		err := tx.QueryRow(ctx, `SELECT c.oid FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname=$2 AND c.relkind='r'`, schema, name).Scan(&oid)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrSchemaDrift
		}
		if err != nil {
			return nil, err
		}
		rows, err := tx.Query(ctx, `
SELECT 'relation' AS kind, c.relname AS name,
       c.relpersistence::text||'|'||c.relrowsecurity::text||'|'||c.relforcerowsecurity::text AS definition
FROM pg_catalog.pg_class c WHERE c.oid=$1
UNION ALL
SELECT 'column', a.attnum::text||':'||a.attname,
       pg_catalog.format_type(a.atttypid,a.atttypmod)||'|'||a.attnotnull::text||'|'||a.attidentity::text||'|'||a.attgenerated::text||'|'||COALESCE(pg_catalog.pg_get_expr(d.adbin,d.adrelid,true),'')
FROM pg_catalog.pg_attribute a LEFT JOIN pg_catalog.pg_attrdef d ON d.adrelid=a.attrelid AND d.adnum=a.attnum
WHERE a.attrelid=$1 AND a.attnum>0 AND NOT a.attisdropped
UNION ALL
SELECT 'constraint', c.conname,
       c.contype::text||'|'||pg_catalog.pg_get_constraintdef(c.oid,true)||'|'||c.condeferrable::text||'|'||c.condeferred::text||'|'||c.convalidated::text
FROM pg_catalog.pg_constraint c WHERE c.conrelid=$1
UNION ALL
SELECT 'index', c.relname,
       pg_catalog.pg_get_indexdef(i.indexrelid,0,true)||'|'||i.indisvalid::text||'|'||i.indisready::text
FROM pg_catalog.pg_index i JOIN pg_catalog.pg_class c ON c.oid=i.indexrelid WHERE i.indrelid=$1
UNION ALL
SELECT 'trigger', t.tgname, pg_catalog.pg_get_triggerdef(t.oid,true)||'|'||t.tgenabled::text
FROM pg_catalog.pg_trigger t WHERE t.tgrelid=$1 AND NOT t.tgisinternal
UNION ALL
SELECT 'policy', p.polname, p.polcmd::text||'|'||p.polpermissive::text||'|'||p.polroles::text||'|'||COALESCE(pg_catalog.pg_get_expr(p.polqual,p.polrelid,true),'')||'|'||COALESCE(pg_catalog.pg_get_expr(p.polwithcheck,p.polrelid,true),'')
FROM pg_catalog.pg_policy p WHERE p.polrelid=$1
ORDER BY kind,name`, oid)
		if err != nil {
			return nil, err
		}
		table := tableDefinition{Name: name, Objects: []objectDefinition{}}
		for rows.Next() {
			var item objectDefinition
			if err := rows.Scan(&item.Kind, &item.Name, &item.Definition); err != nil {
				rows.Close()
				return nil, err
			}
			item.Definition = strings.ReplaceAll(item.Definition, quoted+".", "@schema.")
			table.Objects = append(table.Objects, item)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		result = append(result, table)
	}
	return result, nil
}
func fingerprint(ctx context.Context, tx pgx.Tx, schema string, names []string) (string, error) {
	objects, err := catalog(ctx, tx, schema, names)
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(objects)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
func searchPath(ctx context.Context, tx pgx.Tx, schema string) error {
	_, err := tx.Exec(ctx, `SELECT pg_catalog.set_config('search_path',$1,true)`, pgx.Identifier{schema}.Sanitize())
	return err
}
