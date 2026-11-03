// Package control implements project governance and project-key authorization.
package control

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jhzhfree/ai-governance-gateway/internal/budget"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
)

var (
	ErrNotFound = errors.New("not found")
	ErrInvalid  = errors.New("invalid input")
)

// JSON numbers must remain exact in future JavaScript clients. The ledger uses
// int64 internally; this development API caps amounts at Number.MAX_SAFE_INTEGER.
const MaxAPIAmount int64 = 9007199254740991

type Store struct{ pool *pgxpool.Pool }

func New(p *pgxpool.Pool) *Store { return &Store{pool: p} }
func validName(v string) bool    { return strings.TrimSpace(v) == v && len(v) > 0 && len(v) <= 100 }
func validPrincipal(p identity.Principal) bool {
	return p.Issuer != "" && p.Subject != "" && len(p.Issuer) <= 2048 && len(p.Subject) <= 256
}
func transaction(ctx context.Context, p *pgxpool.Pool, fn func(pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, p, fn)
}
func audit(ctx context.Context, tx pgx.Tx, org string, p identity.Principal, action, resource string) error {
	_, err := tx.Exec(ctx, `INSERT INTO audit_events(organization_id,actor_issuer,actor_subject,action,resource_id) VALUES($1,$2,$3,$4,$5)`, org, p.Issuer, p.Subject, action, resource)
	return err
}

func orgRole(ctx context.Context, tx pgx.Tx, p identity.Principal, org string) (string, error) {
	var role string
	err := tx.QueryRow(ctx, `SELECT role FROM organization_members WHERE organization_id=$1 AND issuer=$2 AND subject=$3 FOR SHARE`, org, p.Issuer, p.Subject).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return role, err
}
func orgAdmin(ctx context.Context, tx pgx.Tx, p identity.Principal, org string) error {
	role, err := orgRole(ctx, tx, p, org)
	if err != nil {
		return err
	}
	if role != "owner" && role != "admin" {
		return identity.ErrForbidden
	}
	return nil
}

// Permission reads lock membership rows until the transaction commits so role
// changes cannot race an already-authorized management write.
func projectRole(ctx context.Context, tx pgx.Tx, p identity.Principal, org, project string) (string, error) {
	role, err := orgRole(ctx, tx, p, org)
	if err != nil {
		return "", err
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM projects WHERE organization_id=$1 AND id=$2)`, org, project).Scan(&exists); err != nil {
		return "", err
	}
	if !exists {
		return "", ErrNotFound
	}
	if role == "owner" || role == "admin" {
		return "admin", nil
	}
	if role == "viewer" {
		return "viewer", nil
	}
	err = tx.QueryRow(ctx, `SELECT role FROM project_members WHERE organization_id=$1 AND project_id=$2 AND issuer=$3 AND subject=$4 FOR SHARE`, org, project, p.Issuer, p.Subject).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return role, err
}

type Organization struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Period   string `json:"period"`
	Currency string `json:"currency"`
	Role     string `json:"role"`
}
type Project struct {
	ID             string `json:"id"`
	OrganizationID string `json:"organization_id"`
	Name           string `json:"name"`
	Role           string `json:"role"`
}
type ProjectDetails struct {
	Project
	Models []string `json:"models"`
}
type Key struct {
	CanRevoke bool       `json:"can_revoke"`
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Prefix    string     `json:"prefix"`
	Models    []string   `json:"models"`
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt time.Time  `json:"expires_at"`
	RevokedAt *time.Time `json:"revoked_at"`
}
type IssuedKey struct {
	Key
	Secret string `json:"secret"`
}
type Member struct {
	identity.Principal
	Role string `json:"role"`
}
type Usage struct {
	Period   string `json:"period"`
	Currency string `json:"currency"`
	Limit    int64  `json:"limit_amount"`
	Spent    int64  `json:"spent"`
	Held     int64  `json:"held"`
}
type Audit struct {
	ID         int64              `json:"id"`
	Actor      identity.Principal `json:"actor"`
	Action     string             `json:"action"`
	ResourceID string             `json:"resource_id"`
	CreatedAt  time.Time          `json:"created_at"`
}

func (s *Store) CreateOrganization(ctx context.Context, p identity.Principal, name, period string, limit int64) (o Organization, err error) {
	if !validPrincipal(p) || !validName(name) || limit < 0 || limit > MaxAPIAmount {
		return o, ErrInvalid
	}
	if _, e := time.Parse("2006-01", period); e != nil {
		return o, ErrInvalid
	}
	o = Organization{ID: rand.Text(), Name: name, Period: period, Currency: "MOCK", Role: "owner"}
	err = transaction(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO organizations(id,name,period_id,currency) VALUES($1,$2,$3,$4)`, o.ID, o.Name, o.Period, o.Currency); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO organization_members VALUES($1,$2,$3,'owner')`, o.ID, p.Issuer, p.Subject); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO budget_accounts(organization_id,project_id,period_id,currency,limit_amount) VALUES($1,'',$2,$3,$4)`, o.ID, o.Period, o.Currency, limit); err != nil {
			return err
		}
		return audit(ctx, tx, o.ID, p, "organization.created", o.ID)
	})
	return
}
func (s *Store) ListOrganizations(ctx context.Context, p identity.Principal, after string, limit int) ([]Organization, error) {
	rows, err := s.pool.Query(ctx, `SELECT o.id,o.name,o.period_id,o.currency,m.role FROM organizations o JOIN organization_members m ON m.organization_id=o.id WHERE m.issuer=$1 AND m.subject=$2 AND o.id>$3 ORDER BY o.id LIMIT $4`, p.Issuer, p.Subject, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Organization{}
	for rows.Next() {
		var o Organization
		if err := rows.Scan(&o.ID, &o.Name, &o.Period, &o.Currency, &o.Role); err != nil {
			return nil, err
		}
		items = append(items, o)
	}
	return items, rows.Err()
}
func (s *Store) PutOrganizationMember(ctx context.Context, p identity.Principal, org string, m Member) error {
	if !validPrincipal(m.Principal) || (m.Role != "admin" && m.Role != "developer" && m.Role != "viewer") {
		return ErrInvalid
	}
	return transaction(ctx, s.pool, func(tx pgx.Tx) error {
		role, err := orgRole(ctx, tx, p, org)
		if err != nil {
			return err
		}
		if role != "owner" {
			return identity.ErrForbidden
		}
		// Owner creation/transfer/removal is deliberately outside this API.
		r, err := tx.Exec(ctx, `INSERT INTO organization_members VALUES($1,$2,$3,$4) ON CONFLICT(organization_id,issuer,subject) DO UPDATE SET role=excluded.role WHERE organization_members.role<>'owner'`, org, m.Issuer, m.Subject, m.Role)
		if err != nil {
			return err
		}
		if r.RowsAffected() != 1 {
			return identity.ErrForbidden
		}
		return audit(ctx, tx, org, p, "organization.member_changed", m.Issuer+"#"+m.Subject)
	})
}
func (s *Store) CreateProject(ctx context.Context, p identity.Principal, org, name string, limit int64) (project Project, err error) {
	if !validName(name) || limit < 0 || limit > MaxAPIAmount {
		return project, ErrInvalid
	}
	project = Project{ID: rand.Text(), OrganizationID: org, Name: name, Role: "admin"}
	err = transaction(ctx, s.pool, func(tx pgx.Tx) error {
		if err := orgAdmin(ctx, tx, p, org); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO projects VALUES($1,$2,$3)`, org, project.ID, name); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO budget_accounts(organization_id,project_id,period_id,currency,limit_amount) SELECT id,$2,period_id,currency,$3 FROM organizations WHERE id=$1`, org, project.ID, limit); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO project_models VALUES($1,$2,'mock-text')`, org, project.ID); err != nil {
			return err
		}
		return audit(ctx, tx, org, p, "project.created", project.ID)
	})
	return
}
func (s *Store) ListProjects(ctx context.Context, p identity.Principal, org, after string, limit int) (items []Project, err error) {
	items = []Project{}
	err = transaction(ctx, s.pool, func(tx pgx.Tx) error {
		role, err := orgRole(ctx, tx, p, org)
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT id,name,CASE WHEN $3 IN ('owner','admin') THEN 'admin' WHEN $3='viewer' THEN 'viewer' ELSE (SELECT role FROM project_members m WHERE m.organization_id=p.organization_id AND m.project_id=p.id AND issuer=$4 AND subject=$5) END FROM projects p WHERE organization_id=$1 AND id>$2 AND ($3<>'developer' OR EXISTS(SELECT 1 FROM project_members m WHERE m.organization_id=p.organization_id AND m.project_id=p.id AND issuer=$4 AND subject=$5)) ORDER BY id LIMIT $6`, org, after, role, p.Issuer, p.Subject, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			v := Project{OrganizationID: org}
			if err := rows.Scan(&v.ID, &v.Name, &v.Role); err != nil {
				return err
			}
			items = append(items, v)
		}
		return rows.Err()
	})
	return
}

func (s *Store) GetProject(ctx context.Context, p identity.Principal, org, project string) (details ProjectDetails, err error) {
	details.Models = []string{}
	err = transaction(ctx, s.pool, func(tx pgx.Tx) error {
		role, err := projectRole(ctx, tx, p, org, project)
		if err != nil {
			return err
		}
		details.Project = Project{ID: project, OrganizationID: org, Role: role}
		if err := tx.QueryRow(ctx, `SELECT name FROM projects WHERE organization_id=$1 AND id=$2`, org, project).Scan(&details.Name); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT model FROM project_models WHERE organization_id=$1 AND project_id=$2 ORDER BY model`, org, project)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var model string
			if err := rows.Scan(&model); err != nil {
				return err
			}
			details.Models = append(details.Models, model)
		}
		return rows.Err()
	})
	return
}
func (s *Store) PutProjectMember(ctx context.Context, p identity.Principal, org, project string, m Member) error {
	if !validPrincipal(m.Principal) || (m.Role != "admin" && m.Role != "developer" && m.Role != "viewer") {
		return ErrInvalid
	}
	return transaction(ctx, s.pool, func(tx pgx.Tx) error {
		if err := orgAdmin(ctx, tx, p, org); err != nil {
			return err
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM projects WHERE organization_id=$1 AND id=$2) AND EXISTS(SELECT 1 FROM organization_members WHERE organization_id=$1 AND issuer=$3 AND subject=$4)`, org, project, m.Issuer, m.Subject).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}
		if _, err := tx.Exec(ctx, `INSERT INTO project_members VALUES($1,$2,$3,$4,$5) ON CONFLICT(organization_id,project_id,issuer,subject) DO UPDATE SET role=excluded.role`, org, project, m.Issuer, m.Subject, m.Role); err != nil {
			return err
		}
		return audit(ctx, tx, org, p, "project.member_changed", project)
	})
}
func (s *Store) SetModels(ctx context.Context, p identity.Principal, org, project string, models []string) error {
	if len(models) > 1 || (len(models) == 1 && models[0] != "mock-text") {
		return ErrInvalid
	}
	return transaction(ctx, s.pool, func(tx pgx.Tx) error {
		if err := orgAdmin(ctx, tx, p, org); err != nil {
			return err
		}
		var id string
		if err := tx.QueryRow(ctx, `SELECT id FROM projects WHERE organization_id=$1 AND id=$2 FOR UPDATE`, org, project).Scan(&id); errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM project_models WHERE organization_id=$1 AND project_id=$2`, org, project); err != nil {
			return err
		}
		for _, model := range models {
			if _, err := tx.Exec(ctx, `INSERT INTO project_models VALUES($1,$2,$3)`, org, project, model); err != nil {
				return err
			}
		}
		return audit(ctx, tx, org, p, "project.models_changed", project)
	})
}

func (s *Store) CreateKey(ctx context.Context, p identity.Principal, org, project, name string, models []string, ttl time.Duration) (issued IssuedKey, err error) {
	if !validName(name) || len(models) != 1 || models[0] != "mock-text" || ttl < time.Minute || ttl > 90*24*time.Hour {
		return issued, ErrInvalid
	}
	secret := "aigw_pk_" + rand.Text() + rand.Text()
	digest := sha256.Sum256([]byte(secret))
	issued.Secret = secret
	issued.Key = Key{ID: rand.Text(), Name: name, Prefix: secret[:16], Models: models, CanRevoke: true}
	err = transaction(ctx, s.pool, func(tx pgx.Tx) error {
		role, err := projectRole(ctx, tx, p, org, project)
		if err != nil {
			return err
		}
		if role == "viewer" {
			return identity.ErrForbidden
		}
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM project_models WHERE organization_id=$1 AND project_id=$2 AND model=ANY($3)`, org, project, models).Scan(&count); err != nil {
			return err
		}
		if count != len(models) {
			return identity.ErrForbidden
		}
		if err := tx.QueryRow(ctx, `INSERT INTO project_keys(id,organization_id,project_id,name,digest,prefix,models,creator_issuer,creator_subject,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,clock_timestamp()+$10*interval '1 second') RETURNING created_at,expires_at`, issued.ID, org, project, name, digest[:], issued.Prefix, models, p.Issuer, p.Subject, int64(ttl/time.Second)).Scan(&issued.CreatedAt, &issued.ExpiresAt); err != nil {
			return err
		}
		return audit(ctx, tx, org, p, "project.key_created", issued.ID)
	})
	if err != nil {
		return IssuedKey{}, err
	}
	return
}
func (s *Store) ListKeys(ctx context.Context, p identity.Principal, org, project, after string, limit int) (items []Key, err error) {
	items = []Key{}
	err = transaction(ctx, s.pool, func(tx pgx.Tx) error {
		role, err := projectRole(ctx, tx, p, org, project)
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT id,name,prefix,models,created_at,expires_at,revoked_at,(revoked_at IS NULL AND ($5='admin' OR ($5='developer' AND creator_issuer=$6 AND creator_subject=$7))) FROM project_keys WHERE organization_id=$1 AND project_id=$2 AND id>$3 ORDER BY id LIMIT $4`, org, project, after, limit, role, p.Issuer, p.Subject)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var k Key
			if err := rows.Scan(&k.ID, &k.Name, &k.Prefix, &k.Models, &k.CreatedAt, &k.ExpiresAt, &k.RevokedAt, &k.CanRevoke); err != nil {
				return err
			}
			items = append(items, k)
		}
		return rows.Err()
	})
	return
}
func (s *Store) RevokeKey(ctx context.Context, p identity.Principal, org, project, id string) error {
	return transaction(ctx, s.pool, func(tx pgx.Tx) error {
		role, err := projectRole(ctx, tx, p, org, project)
		if err != nil {
			return err
		}
		if role == "viewer" {
			return identity.ErrForbidden
		}
		var issuer, subject string
		var revoked *time.Time
		err = tx.QueryRow(ctx, `SELECT creator_issuer,creator_subject,revoked_at FROM project_keys WHERE organization_id=$1 AND project_id=$2 AND id=$3 FOR UPDATE`, org, project, id).Scan(&issuer, &subject, &revoked)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if role != "admin" && (issuer != p.Issuer || subject != p.Subject) {
			return identity.ErrForbidden
		}
		if revoked != nil {
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE project_keys SET revoked_at=clock_timestamp() WHERE id=$1`, id); err != nil {
			return err
		}
		return audit(ctx, tx, org, p, "project.key_revoked", id)
	})
}
func (s *Store) Usage(ctx context.Context, p identity.Principal, org, project string) (u Usage, err error) {
	err = transaction(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := projectRole(ctx, tx, p, org, project); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT a.period_id,a.currency,a.limit_amount,a.spent,a.held FROM budget_accounts a JOIN organizations o ON o.id=a.organization_id AND o.period_id=a.period_id AND o.currency=a.currency WHERE a.organization_id=$1 AND a.project_id=$2`, org, project).Scan(&u.Period, &u.Currency, &u.Limit, &u.Spent, &u.Held)
	})
	return
}
func (s *Store) ListAudit(ctx context.Context, p identity.Principal, org string, after int64, limit int) (items []Audit, err error) {
	items = []Audit{}
	err = transaction(ctx, s.pool, func(tx pgx.Tx) error {
		role, err := orgRole(ctx, tx, p, org)
		if err != nil {
			return err
		}
		if role == "developer" {
			return identity.ErrForbidden
		}
		rows, err := tx.Query(ctx, `SELECT id,actor_issuer,actor_subject,action,resource_id,created_at FROM audit_events WHERE organization_id=$1 AND id>$2 ORDER BY id LIMIT $3`, org, after, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var a Audit
			if err := rows.Scan(&a.ID, &a.Actor.Issuer, &a.Actor.Subject, &a.Action, &a.ResourceID, &a.CreatedAt); err != nil {
				return err
			}
			items = append(items, a)
		}
		return rows.Err()
	})
	return
}

// ResolveKey always reads authoritative DB state: revoke/model changes affect new
// requests immediately after commit. In-flight requests may finish normally.
func (s *Store) ResolveKey(ctx context.Context, secret, model string) (scope budget.Scope, err error) {
	if !strings.HasPrefix(secret, "aigw_pk_") || len(secret) > 256 {
		return scope, identity.ErrUnauthenticated
	}
	digest := sha256.Sum256([]byte(secret))
	var models []string
	var allowed bool
	err = s.pool.QueryRow(ctx, `SELECT k.organization_id,k.project_id,o.period_id,o.currency,k.models,
		EXISTS(SELECT 1 FROM project_models m WHERE m.organization_id=k.organization_id AND m.project_id=k.project_id AND m.model=$2)
		FROM project_keys k JOIN organizations o ON o.id=k.organization_id
		WHERE k.digest=$1 AND k.revoked_at IS NULL AND k.expires_at>clock_timestamp()`, digest[:], model).Scan(&scope.Organization, &scope.Project, &scope.Period, &scope.Currency, &models, &allowed)
	if errors.Is(err, pgx.ErrNoRows) {
		return budget.Scope{}, identity.ErrUnauthenticated
	}
	if err != nil {
		return budget.Scope{}, fmt.Errorf("resolve key: %w", err)
	}
	for _, m := range models {
		if m == model && allowed {
			return scope, nil
		}
	}
	return budget.Scope{}, identity.ErrForbidden
}
