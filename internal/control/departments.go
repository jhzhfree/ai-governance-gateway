package control

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
)

type Department struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	ParentID string `json:"parent_id"`
}
type DepartmentGrant struct {
	DepartmentID string `json:"department_id"`
	identity.Principal
	Role string `json:"role"`
}
type DepartmentProject struct {
	ProjectID    string `json:"project_id"`
	DepartmentID string `json:"department_id"`
}
type DepartmentConfiguration struct {
	Revision    int64               `json:"revision"`
	Departments []Department        `json:"departments"`
	Grants      []DepartmentGrant   `json:"grants"`
	Projects    []DepartmentProject `json:"projects"`
}
type DepartmentVersion struct {
	Revision  int64              `json:"revision"`
	Actor     identity.Principal `json:"actor"`
	Reason    string             `json:"reason"`
	CreatedAt time.Time          `json:"created_at"`
}

var departmentID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`)

// Department readers keep a stable authorization configuration for their transaction.
// Writers first acquire memberLock; no department writer upgrades organization member rows.
func departmentLock(ctx context.Context, tx pgx.Tx, org string, write bool) error {
	fn := "pg_advisory_xact_lock_shared"
	if write {
		fn = "pg_advisory_xact_lock"
	}
	_, err := tx.Exec(ctx, `SELECT `+fn+`(hashtextextended($1,0))`, "aigw:departments:"+org)
	return err
}
func emptyDepartments() DepartmentConfiguration {
	return DepartmentConfiguration{Departments: []Department{}, Grants: []DepartmentGrant{}, Projects: []DepartmentProject{}}
}
func loadDepartments(ctx context.Context, tx pgx.Tx, org string) (v DepartmentConfiguration, err error) {
	v = emptyDepartments()
	err = tx.QueryRow(ctx, `SELECT revision FROM department_configurations WHERE organization_id=$1`, org).Scan(&v.Revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, nil
	}
	if err != nil {
		return
	}
	rows, err := tx.Query(ctx, `SELECT id,name,coalesce(parent_id,'') FROM departments WHERE organization_id=$1 ORDER BY id`, org)
	if err != nil {
		return v, err
	}
	for rows.Next() {
		var d Department
		if err = rows.Scan(&d.ID, &d.Name, &d.ParentID); err != nil {
			rows.Close()
			return
		}
		v.Departments = append(v.Departments, d)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return
	}
	rows, err = tx.Query(ctx, `SELECT department_id,issuer,subject,role FROM department_members WHERE organization_id=$1 ORDER BY department_id,issuer,subject`, org)
	if err != nil {
		return
	}
	for rows.Next() {
		var g DepartmentGrant
		if err = rows.Scan(&g.DepartmentID, &g.Issuer, &g.Subject, &g.Role); err != nil {
			rows.Close()
			return
		}
		v.Grants = append(v.Grants, g)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return
	}
	rows, err = tx.Query(ctx, `SELECT project_id,department_id FROM department_projects WHERE organization_id=$1 ORDER BY project_id`, org)
	if err != nil {
		return
	}
	for rows.Next() {
		var p DepartmentProject
		if err = rows.Scan(&p.ProjectID, &p.DepartmentID); err != nil {
			rows.Close()
			return
		}
		v.Projects = append(v.Projects, p)
	}
	err = rows.Err()
	rows.Close()
	return
}
func (s *Store) Departments(ctx context.Context, p identity.Principal, org string) (v DepartmentConfiguration, err error) {
	err = transaction(ctx, s.pool, func(tx pgx.Tx) error {
		if err := orgAdmin(ctx, tx, p, org); err != nil {
			return err
		}
		if err := departmentLock(ctx, tx, org, false); err != nil {
			return err
		}
		v, err = loadDepartments(ctx, tx, org)
		return err
	})
	return
}
func validDepartments(v DepartmentConfiguration) bool {
	if v.Revision < 0 || v.Revision >= MaxAPIAmount || len(v.Departments) > 100 || len(v.Grants) > 1000 || len(v.Projects) > 1000 || v.Departments == nil || v.Grants == nil || v.Projects == nil {
		return false
	}
	nodes := map[string]Department{}
	for _, d := range v.Departments {
		if !departmentID.MatchString(d.ID) || strings.TrimSpace(d.Name) != d.Name || utf8.RuneCountInString(d.Name) < 1 || utf8.RuneCountInString(d.Name) > 80 {
			return false
		}
		if _, ok := nodes[d.ID]; ok {
			return false
		}
		nodes[d.ID] = d
	}
	for _, d := range v.Departments {
		seen := map[string]bool{}
		id := d.ID
		depth := 0
		for id != "" {
			node, ok := nodes[id]
			depth++
			if !ok || seen[id] || depth > 8 {
				return false
			}
			seen[id] = true
			id = node.ParentID
		}
	}
	grants := map[struct{ Department, Issuer, Subject string }]bool{}
	for _, g := range v.Grants {
		key := struct{ Department, Issuer, Subject string }{g.DepartmentID, g.Issuer, g.Subject}
		if _, ok := nodes[g.DepartmentID]; !ok {
			return false
		}
		if !validMember(Member{Principal: g.Principal, Role: g.Role}) || grants[key] {
			return false
		}
		grants[key] = true
	}
	projects := map[string]bool{}
	for _, p := range v.Projects {
		if _, ok := nodes[p.DepartmentID]; !ok {
			return false
		}
		if !departmentID.MatchString(p.ProjectID) || projects[p.ProjectID] {
			return false
		}
		projects[p.ProjectID] = true
	}
	return true
}
func recordDepartments(ctx context.Context, tx pgx.Tx, p identity.Principal, org, reason string) (DepartmentConfiguration, error) {
	if _, err := tx.Exec(ctx, `INSERT INTO department_configurations(organization_id,revision) VALUES($1,1) ON CONFLICT(organization_id) DO UPDATE SET revision=department_configurations.revision+1`, org); err != nil {
		return DepartmentConfiguration{}, err
	}
	v, err := loadDepartments(ctx, tx, org)
	if err != nil {
		return v, err
	}
	if v.Revision > MaxAPIAmount {
		return v, ErrInvalid
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return v, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO department_versions(organization_id,revision,actor_issuer,actor_subject,reason,configuration) VALUES($1,$2,$3,$4,$5,$6)`, org, v.Revision, p.Issuer, p.Subject, reason, raw); err != nil {
		return v, err
	}
	return v, audit(ctx, tx, org, p, "organization.departments_changed", org)
}
func (s *Store) SetDepartments(ctx context.Context, p identity.Principal, org string, v DepartmentConfiguration, reason string) (out DepartmentConfiguration, err error) {
	if !validDepartments(v) || strings.TrimSpace(reason) != reason || utf8.RuneCountInString(reason) < 1 || utf8.RuneCountInString(reason) > 256 {
		return out, ErrInvalid
	}
	err = transaction(ctx, s.pool, func(tx pgx.Tx) error {
		if err := memberLock(ctx, tx, org); err != nil {
			return err
		}
		if err := orgAdmin(ctx, tx, p, org); err != nil {
			return err
		}
		if err := departmentLock(ctx, tx, org, true); err != nil {
			return err
		}
		var revision int64
		err := tx.QueryRow(ctx, `SELECT revision FROM department_configurations WHERE organization_id=$1`, org).Scan(&revision)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if revision != v.Revision {
			return ErrMemberConflict
		}
		for _, g := range v.Grants {
			var enabled bool
			err := tx.QueryRow(ctx, `SELECT enabled FROM organization_members WHERE organization_id=$1 AND issuer=$2 AND subject=$3`, org, g.Issuer, g.Subject).Scan(&enabled)
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			if err != nil {
				return err
			}
			if !enabled {
				return ErrMemberConflict
			}
		}
		for _, p := range v.Projects {
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM projects WHERE organization_id=$1 AND id=$2)`, org, p.ProjectID).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				return ErrNotFound
			}
		}
		// FK constraints are deferred for parent replacement; children are cleared first.
		for _, table := range []string{"department_members", "department_projects", "departments"} {
			if _, err := tx.Exec(ctx, `DELETE FROM `+table+` WHERE organization_id=$1`, org); err != nil {
				return err
			}
		}
		for _, d := range v.Departments {
			if _, err := tx.Exec(ctx, `INSERT INTO departments(organization_id,id,name,parent_id) VALUES($1,$2,$3,nullif($4,''))`, org, d.ID, d.Name, d.ParentID); err != nil {
				return err
			}
		}
		for _, g := range v.Grants {
			if _, err := tx.Exec(ctx, `INSERT INTO department_members(organization_id,department_id,issuer,subject,role) VALUES($1,$2,$3,$4,$5)`, org, g.DepartmentID, g.Issuer, g.Subject, g.Role); err != nil {
				return err
			}
		}
		for _, p := range v.Projects {
			if _, err := tx.Exec(ctx, `INSERT INTO department_projects(organization_id,project_id,department_id) VALUES($1,$2,$3)`, org, p.ProjectID, p.DepartmentID); err != nil {
				return err
			}
		}
		out, err = recordDepartments(ctx, tx, p, org, reason)
		return err
	})
	return
}

// Ancestor membership is inherited downwards only. Direct project grants and
// department grants combine by strongest role, under existing organization ceilings.
const departmentAncestors = `WITH RECURSIVE ancestors(id,parent_id) AS (
 SELECT d.id,d.parent_id FROM departments d JOIN department_projects b ON b.organization_id=d.organization_id AND b.department_id=d.id WHERE b.organization_id=$1 AND b.project_id=$2
 UNION SELECT d.id,d.parent_id FROM departments d JOIN ancestors a ON d.id=a.parent_id WHERE d.organization_id=$1
) `

func departmentRole(ctx context.Context, tx pgx.Tx, p identity.Principal, org, project string) (string, error) {
	var role string
	err := tx.QueryRow(ctx, departmentAncestors+`SELECT coalesce((SELECT m.role FROM department_members m JOIN ancestors a ON a.id=m.department_id WHERE m.organization_id=$1 AND m.issuer=$3 AND m.subject=$4 ORDER BY CASE m.role WHEN 'admin' THEN 3 WHEN 'developer' THEN 2 ELSE 1 END DESC LIMIT 1),'')`, org, project, p.Issuer, p.Subject).Scan(&role)
	return role, err
}
func strongerRole(a, b string) string {
	rank := map[string]int{"": 0, "none": 0, "viewer": 1, "developer": 2, "admin": 3}
	if rank[b] > rank[a] {
		return b
	}
	return a
}
func (s *Store) DepartmentHistory(ctx context.Context, p identity.Principal, org string, after int64) (items []DepartmentVersion, err error) {
	items = []DepartmentVersion{}
	if after < 0 {
		return items, ErrInvalid
	}
	err = transaction(ctx, s.pool, func(tx pgx.Tx) error {
		if err := orgAdmin(ctx, tx, p, org); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT revision,actor_issuer,actor_subject,reason,created_at FROM department_versions WHERE organization_id=$1 AND revision>$2 ORDER BY revision LIMIT 51`, org, after)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v DepartmentVersion
			if err := rows.Scan(&v.Revision, &v.Actor.Issuer, &v.Actor.Subject, &v.Reason, &v.CreatedAt); err != nil {
				return err
			}
			items = append(items, v)
		}
		return rows.Err()
	})
	return
}
