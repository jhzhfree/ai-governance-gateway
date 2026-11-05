package budget

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
)

func lockDepartmentAttribution(ctx context.Context, tx pgx.Tx, org string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared(hashtextextended($1,0))`, "aigw:departments:"+org)
	return err
}

// Called only for a new reservation after lockDepartmentAttribution. The shared
// lock covers admission and capture: no current-tree backfill or replay.
func captureDepartment(ctx context.Context, tx pgx.Tx, q Quote) error {

	var path []byte
	var depth int
	var root bool
	err := tx.QueryRow(ctx, `WITH RECURSIVE lineage(id,name,parent_id,depth,seen) AS (
 SELECT d.id,d.name,d.parent_id,1,ARRAY[d.id] FROM department_projects b JOIN departments d ON d.organization_id=b.organization_id AND d.id=b.department_id WHERE b.organization_id=$1 AND b.project_id=$2
 UNION ALL SELECT d.id,d.name,d.parent_id,l.depth+1,l.seen||d.id FROM lineage l JOIN departments d ON d.organization_id=$1 AND d.id=l.parent_id WHERE l.depth<9 AND NOT d.id=ANY(l.seen)
 ) SELECT coalesce(jsonb_agg(jsonb_build_object('id',id,'name',name,'parent_id',coalesce(parent_id,'')) ORDER BY depth DESC),'[]'::jsonb),coalesce(max(depth),0),coalesce(bool_or(parent_id IS NULL),false) FROM lineage`, q.Organization, q.Project).Scan(&path, &depth, &root)
	if err != nil {
		return err
	}
	if depth > 8 || (depth > 0 && !root) {
		return errors.New("invalid department attribution tree")
	}
	_, err = tx.Exec(ctx, `INSERT INTO request_department_attributions(request_id,configuration_revision,department_path) VALUES($1,coalesce((SELECT revision FROM department_configurations WHERE organization_id=$2),0),$3)`, q.RequestID, q.Organization, path)
	return err
}
