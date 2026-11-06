package control

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jhzhfree/ai-governance-gateway/internal/budget"
	"github.com/jhzhfree/ai-governance-gateway/internal/deviceproof"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

var ErrDeviceRequired = fmt.Errorf("project requires device admission: %w", identity.ErrForbidden)
var ErrDeviceUnavailable = errors.New("device admission origin is unavailable")

type Device struct {
	ID        string             `json:"id"`
	Owner     identity.Principal `json:"owner"`
	Name      string             `json:"name"`
	PublicKey string             `json:"public_key"`
	State     string             `json:"state"`
	Revision  int64              `json:"revision"`
	CreatedAt time.Time          `json:"created_at"`
}
type DevicePolicy struct {
	Required  bool  `json:"required"`
	Revision  int64 `json:"revision"`
	Available bool  `json:"available"`
}
type DeviceSession struct {
	ID        string     `json:"id"`
	DeviceID  string     `json:"device_id"`
	ProjectID string     `json:"project_id"`
	Models    []string   `json:"models"`
	ExpiresAt time.Time  `json:"expires_at"`
	RevokedAt *time.Time `json:"revoked_at"`
}
type IssuedDeviceSession struct {
	DeviceSession
	Token     string `json:"token"`
	TokenType string `json:"token_type"`
}
type DeviceRequest struct {
	RequestID string    `json:"request_id"`
	SessionID string    `json:"session_id"`
	ProjectID string    `json:"project_id"`
	CreatedAt time.Time `json:"created_at"`
}

const deviceColumns = `id,owner_issuer,owner_subject,name,public_key,state,revision,created_at`

func scanDevice(row interface{ Scan(...any) error }) (v Device, err error) {
	err = row.Scan(&v.ID, &v.Owner.Issuer, &v.Owner.Subject, &v.Name, &v.PublicKey, &v.State, &v.Revision, &v.CreatedAt)
	return
}
func (s *Store) WithDeviceOrigin(origin string) *Store {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		s.deviceOrigin = ""
		return s
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || (ip != nil && ip.IsLoopback()))) {
		s.deviceOrigin = ""
		return s
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Path = ""
	s.deviceOrigin = u.String()
	return s
}
func (s *Store) RegisterDevice(ctx context.Context, p identity.Principal, org, name, public string) (v Device, err error) {
	if _, err := deviceproof.ParsePublic(public); err != nil || strings.TrimSpace(name) != name || utf8.RuneCountInString(name) < 1 || utf8.RuneCountInString(name) > 80 {
		return v, ErrInvalid
	}
	err = transaction(ctx, s.pool, func(tx pgx.Tx) error {
		if err := memberLock(ctx, tx, org); err != nil {
			return err
		}
		if err := s.ownershipActor(ctx, tx, p); err != nil {
			return err
		}
		if _, err := orgRole(ctx, tx, p, org); err != nil {
			return err
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM terminal_devices WHERE organization_id=$1 AND public_key=$2)`, org, public).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return ErrMemberConflict
		}
		var err error
		v, err = scanDevice(tx.QueryRow(ctx, `INSERT INTO terminal_devices(id,organization_id,owner_issuer,owner_subject,name,public_key) VALUES($1,$2,$3,$4,$5,$6) RETURNING `+deviceColumns, rand.Text(), org, p.Issuer, p.Subject, name, public))
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO device_state_events(device_id,revision,state,actor_issuer,actor_subject,reason) VALUES($1,0,'pending',$2,$3,'设备公钥登记，等待管理员确认')`, v.ID, p.Issuer, p.Subject); err != nil {
			return err
		}
		return audit(ctx, tx, org, p, "device.registered", v.ID)
	})
	return
}
func deviceAccess(ctx context.Context, tx pgx.Tx, p identity.Principal, org, id string) (v Device, err error) {
	role, err := orgRole(ctx, tx, p, org)
	if err != nil {
		return v, err
	}
	v, err = scanDevice(tx.QueryRow(ctx, `SELECT `+deviceColumns+` FROM terminal_devices WHERE organization_id=$1 AND id=$2`, org, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return v, ErrNotFound
	}
	if err != nil {
		return v, err
	}
	if role != "owner" && role != "admin" && v.Owner != p {
		return Device{}, ErrNotFound
	}
	return v, nil
}
func (s *Store) Devices(ctx context.Context, p identity.Principal, org, after string) (items []Device, err error) {
	items = []Device{}
	if len(after) > 128 {
		return items, ErrInvalid
	}
	err = transaction(ctx, s.pool, func(tx pgx.Tx) error {
		role, err := orgRole(ctx, tx, p, org)
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT `+deviceColumns+` FROM terminal_devices WHERE organization_id=$1 AND id>$2 AND($3 IN ('owner','admin') OR(owner_issuer=$4 AND owner_subject=$5)) ORDER BY id LIMIT 51`, org, after, role, p.Issuer, p.Subject)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			v, err := scanDevice(rows)
			if err != nil {
				return err
			}
			items = append(items, v)
		}
		return rows.Err()
	})
	return
}
func (s *Store) ChangeDevice(ctx context.Context, p identity.Principal, org, id string, revision int64, state, reason string) (v Device, err error) {
	if revision < 0 || revision >= MaxAPIAmount || (state != "active" && state != "revoked") || !validApprovalReason(reason) {
		return v, ErrInvalid
	}
	err = transaction(ctx, s.pool, func(tx pgx.Tx) error {
		if err := memberLock(ctx, tx, org); err != nil {
			return err
		}
		if err := s.ownershipActor(ctx, tx, p); err != nil {
			return err
		}
		var err error
		v, err = deviceAccess(ctx, tx, p, org, id)
		if err != nil {
			return err
		}
		if state == "active" {
			if err := orgAdmin(ctx, tx, p, org); err != nil {
				return err
			}
			if err := s.ownershipActor(ctx, tx, v.Owner); err != nil {
				return err
			}
			if _, err := orgRole(ctx, tx, v.Owner, org); err != nil {
				return err
			}
		}
		if v.Revision != revision || v.State == "revoked" || (state == "active" && v.State != "pending") {
			return ErrMemberConflict
		}
		v, err = scanDevice(tx.QueryRow(ctx, `UPDATE terminal_devices SET state=$3,revision=revision+1 WHERE organization_id=$1 AND id=$2 AND revision=$4 AND state<>'revoked' RETURNING `+deviceColumns, org, id, state, revision))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrMemberConflict
		}
		if err != nil {
			return err
		}
		if state == "revoked" {
			if _, err := tx.Exec(ctx, `UPDATE device_sessions SET revoked_at=coalesce(revoked_at,clock_timestamp()) WHERE device_id=$1`, id); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO device_state_events(device_id,revision,state,actor_issuer,actor_subject,reason) VALUES($1,$2,$3,$4,$5,$6)`, id, v.Revision, state, p.Issuer, p.Subject, reason); err != nil {
			return err
		}
		return audit(ctx, tx, org, p, "device."+state, id)
	})
	return
}
func (s *Store) DevicePolicy(ctx context.Context, p identity.Principal, org, project string) (v DevicePolicy, err error) {
	v.Available = s.deviceOrigin != ""
	err = transaction(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := projectRole(ctx, tx, p, org, project); err != nil {
			return err
		}
		err := tx.QueryRow(ctx, `SELECT required,revision FROM project_device_policies WHERE organization_id=$1 AND project_id=$2`, org, project).Scan(&v.Required, &v.Revision)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	})
	return
}
func (s *Store) SetDevicePolicy(ctx context.Context, p identity.Principal, org, project string, revision int64, required bool, reason string) (v DevicePolicy, err error) {
	if revision < 0 || revision >= MaxAPIAmount || !validApprovalReason(reason) {
		return v, ErrInvalid
	}
	if required && s.deviceOrigin == "" {
		return v, ErrDeviceUnavailable
	}
	err = transaction(ctx, s.pool, func(tx pgx.Tx) error {
		if err := memberLock(ctx, tx, org); err != nil {
			return err
		}
		if err := s.ownershipActor(ctx, tx, p); err != nil {
			return err
		}
		if err := orgAdmin(ctx, tx, p, org); err != nil {
			return err
		}
		if _, err := projectRole(ctx, tx, p, org, project); err != nil {
			return err
		}
		var current int64
		err := tx.QueryRow(ctx, `SELECT revision FROM project_device_policies WHERE organization_id=$1 AND project_id=$2`, org, project).Scan(&current)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if current != revision {
			return ErrMemberConflict
		}
		v = DevicePolicy{Required: required, Revision: revision + 1, Available: s.deviceOrigin != ""}
		if _, err := tx.Exec(ctx, `INSERT INTO project_device_policies(organization_id,project_id,required,revision) VALUES($1,$2,$3,$4) ON CONFLICT(organization_id,project_id) DO UPDATE SET required=excluded.required,revision=excluded.revision`, org, project, required, v.Revision); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO device_policy_events(organization_id,project_id,revision,required,actor_issuer,actor_subject,reason) VALUES($1,$2,$3,$4,$5,$6,$7)`, org, project, v.Revision, required, p.Issuer, p.Subject, reason); err != nil {
			return err
		}
		return audit(ctx, tx, org, p, "device.policy_changed", project)
	})
	return
}
func requireDevice(ctx context.Context, tx interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, org, project string) error {
	var required bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM project_device_policies WHERE organization_id=$1 AND project_id=$2 AND required)`, org, project).Scan(&required); err != nil {
		return err
	}
	if required {
		return ErrDeviceRequired
	}
	return nil
}
func consumeDeviceProof(ctx context.Context, tx pgx.Tx, deviceID, public, raw, method, htu, token string) error {
	key, err := deviceproof.ParsePublic(public)
	if err != nil {
		return identity.ErrUnauthenticated
	}
	var now time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return err
	}
	claims, err := deviceproof.Verify(raw, key, method, htu, token, now)
	if err != nil {
		return identity.ErrUnauthenticated
	}
	tag, err := tx.Exec(ctx, `INSERT INTO device_proof_nonces(device_id,nonce,expires_at) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, deviceID, claims.ID, time.Unix(claims.IssuedAt+61, 0))
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return identity.ErrUnauthenticated
	}
	return nil
}
func (s *Store) IssueDeviceSession(ctx context.Context, p identity.Principal, org, project, id, proof string, models []string) (v IssuedDeviceSession, err error) {
	if s.deviceOrigin == "" {
		return v, ErrDeviceUnavailable
	}
	if !s.validModels(models, false) {
		return v, ErrInvalid
	}
	err = transaction(ctx, s.pool, func(tx pgx.Tx) error {
		if err := memberLock(ctx, tx, org); err != nil {
			return err
		}
		if err := s.ownershipActor(ctx, tx, p); err != nil {
			return err
		}
		role, err := projectRole(ctx, tx, p, org, project)
		if err != nil {
			return err
		}
		if role == "viewer" {
			return identity.ErrForbidden
		}
		d, err := deviceAccess(ctx, tx, p, org, id)
		if err != nil {
			return err
		}
		if d.Owner != p || d.State != "active" {
			return identity.ErrForbidden
		}
		if err := consumeDeviceProof(ctx, tx, id, d.PublicKey, proof, "POST", s.deviceOrigin+"/api/v1/organizations/"+url.PathEscape(org)+"/projects/"+url.PathEscape(project)+"/devices/"+url.PathEscape(id)+"/sessions", ""); err != nil {
			return err
		}
		var allowed []string
		if err := tx.QueryRow(ctx, `SELECT ARRAY(SELECT model FROM project_models WHERE organization_id=$1 AND project_id=$2)`, org, project).Scan(&allowed); err != nil {
			return err
		}
		for _, m := range models {
			found := false
			for _, a := range allowed {
				if m == a {
					found = true
					break
				}
			}
			if !found {
				return identity.ErrForbidden
			}
		}
		random := make([]byte, 32)
		if _, err := rand.Read(random); err != nil {
			return err
		}
		v.Token = "aigw_dt_" + base64.RawURLEncoding.EncodeToString(random)
		v.TokenType = "DPoP"
		digest := sha256.Sum256([]byte(v.Token))
		v.DeviceSession = DeviceSession{ID: rand.Text(), DeviceID: id, ProjectID: project, Models: models}
		if err := tx.QueryRow(ctx, `INSERT INTO device_sessions(id,device_id,organization_id,project_id,digest,models) VALUES($1,$2,$3,$4,$5,$6) RETURNING expires_at`, v.ID, id, org, project, digest[:], models).Scan(&v.ExpiresAt); err != nil {
			return err
		}
		return audit(ctx, tx, org, p, "device.session_issued", v.ID)
	})
	return
}

// liveDeviceSession locks the member, device, then session in the same order as
// member offboarding and device revocation. No device lock precedes a role lock.
func (s *Store) liveDeviceSession(ctx context.Context, tx pgx.Tx, token string, want *deviceproof.Binding) (scope budget.Scope, models []string, public string, b deviceproof.Binding, err error) {
	digest := sha256.Sum256([]byte(token))
	var owner identity.Principal
	err = tx.QueryRow(ctx, `SELECT s.id,s.organization_id,s.project_id,d.id,d.owner_issuer,d.owner_subject,o.period_id,o.currency FROM device_sessions s JOIN terminal_devices d ON d.id=s.device_id AND d.organization_id=s.organization_id JOIN organizations o ON o.id=s.organization_id WHERE s.digest=$1 AND s.revoked_at IS NULL AND s.expires_at>clock_timestamp() AND d.state='active'`, digest[:]).Scan(&b.SessionID, &b.Organization, &b.Project, &b.DeviceID, &owner.Issuer, &owner.Subject, &scope.Period, &scope.Currency)
	if errors.Is(err, pgx.ErrNoRows) {
		err = identity.ErrUnauthenticated
	}
	if err != nil {
		return
	}
	if want != nil && *want != b {
		err = identity.ErrUnauthenticated
		return
	}
	scope.Organization = b.Organization
	scope.Project = b.Project
	if err = s.ownershipActor(ctx, tx, owner); err != nil {
		return
	}
	var role string
	role, err = projectRole(ctx, tx, owner, b.Organization, b.Project)
	if err != nil {
		err = identity.ErrForbidden
		return
	}
	if role == "viewer" {
		err = identity.ErrForbidden
		return
	}
	err = tx.QueryRow(ctx, `SELECT public_key FROM terminal_devices WHERE organization_id=$1 AND id=$2 AND state='active' FOR SHARE`, b.Organization, b.DeviceID).Scan(&public)
	if errors.Is(err, pgx.ErrNoRows) {
		err = identity.ErrUnauthenticated
	}
	if err != nil {
		return
	}
	err = tx.QueryRow(ctx, `SELECT models FROM device_sessions WHERE id=$1 AND digest=$2 AND revoked_at IS NULL AND expires_at>clock_timestamp() FOR SHARE`, b.SessionID, digest[:]).Scan(&models)
	if errors.Is(err, pgx.ErrNoRows) {
		err = identity.ErrUnauthenticated
	}
	return
}

// VerifyDeviceRequest only accepts DPoP tokens, never untrusted device headers.
func (s *Store) VerifyDeviceRequest(ctx context.Context, r *http.Request, token string) (out context.Context, err error) {
	if !strings.HasPrefix(token, "aigw_dt_") {
		return ctx, nil
	}
	if s.deviceOrigin == "" || len(token) > 256 || len(r.Header.Values("DPoP")) != 1 {
		return nil, identity.ErrUnauthenticated
	}
	var binding deviceproof.Binding
	bounded, stop := context.WithTimeout(ctx, 3*time.Second)
	defer stop()
	err = transaction(bounded, s.pool, func(tx pgx.Tx) error {
		ctx := bounded
		_, _, public, b, err := s.liveDeviceSession(ctx, tx, token, nil)
		if err != nil {
			return err
		}
		binding = b
		return consumeDeviceProof(ctx, tx, b.DeviceID, public, r.Header.Get("DPoP"), r.Method, s.deviceOrigin+r.URL.EscapedPath(), token)
	})
	if err != nil {
		return nil, err
	}
	return deviceproof.WithBinding(ctx, binding), nil
}
func (s *Store) resolveDevice(ctx context.Context, token, model string) (scope budget.Scope, models []string, err error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	b, ok := deviceproof.FromContext(ctx)
	if !ok || len(token) > 256 {
		return scope, nil, identity.ErrUnauthenticated
	}
	err = transaction(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		scope, models, _, _, err = s.liveDeviceSession(ctx, tx, token, &b)
		if err != nil {
			return err
		}
		var current []string
		if err := tx.QueryRow(ctx, `SELECT ARRAY(SELECT model FROM project_models WHERE organization_id=$1 AND project_id=$2 AND model=ANY($3))`, scope.Organization, scope.Project, models).Scan(&current); err != nil {
			return err
		}
		models = current
		if model != "" {
			for _, m := range models {
				if m == model {
					return nil
				}
			}
			return identity.ErrForbidden
		}
		return nil
	})
	return
}
func (s *Store) deviceModels(ctx context.Context, token string) (items []provider.InferenceModel, err error) {
	scope, allowed, err := s.resolveDevice(ctx, token, "")
	if err != nil {
		return nil, err
	}
	items = []provider.InferenceModel{}
	for _, m := range s.Models() {
		for _, a := range allowed {
			if m.ID == a && m.Currency == scope.Currency {
				items = append(items, provider.InferenceModel{ID: m.ID, Object: "model", OwnedBy: m.Provider})
				break
			}
		}
	}
	return
}
func (s *Store) DeviceSessions(ctx context.Context, p identity.Principal, org, id, after string) (items []DeviceSession, err error) {
	items = []DeviceSession{}
	err = transaction(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := deviceAccess(ctx, tx, p, org, id); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT id,device_id,project_id,models,expires_at,revoked_at FROM device_sessions WHERE device_id=$1 AND id>$2 ORDER BY id LIMIT 51`, id, after)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v DeviceSession
			if err := rows.Scan(&v.ID, &v.DeviceID, &v.ProjectID, &v.Models, &v.ExpiresAt, &v.RevokedAt); err != nil {
				return err
			}
			items = append(items, v)
		}
		return rows.Err()
	})
	return
}
func (s *Store) RevokeDeviceSession(ctx context.Context, p identity.Principal, org, id, session, reason string) error {
	if !validApprovalReason(reason) {
		return ErrInvalid
	}
	return transaction(ctx, s.pool, func(tx pgx.Tx) error {
		if err := memberLock(ctx, tx, org); err != nil {
			return err
		}
		if err := s.ownershipActor(ctx, tx, p); err != nil {
			return err
		}
		if _, err := deviceAccess(ctx, tx, p, org, id); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE device_sessions SET revoked_at=clock_timestamp() WHERE device_id=$1 AND id=$2 AND revoked_at IS NULL`, id, session)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrMemberConflict
		}
		return audit(ctx, tx, org, p, "device.session_revoked", session)
	})
}
func (s *Store) DeviceRequests(ctx context.Context, p identity.Principal, org, id, after string) (items []DeviceRequest, err error) {
	items = []DeviceRequest{}
	err = transaction(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := deviceAccess(ctx, tx, p, org, id); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT b.request_id,b.session_id,r.project_id,b.created_at FROM device_request_bindings b JOIN budget_reservations r USING(request_id) WHERE b.device_id=$1 AND r.organization_id=$2 AND b.request_id>$3 ORDER BY b.request_id LIMIT 51`, id, org, after)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v DeviceRequest
			if err := rows.Scan(&v.RequestID, &v.SessionID, &v.ProjectID, &v.CreatedAt); err != nil {
				return err
			}
			items = append(items, v)
		}
		return rows.Err()
	})
	return
}
func (s *Store) PruneDeviceProofs(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM device_proof_nonces WHERE(device_id,nonce) IN(SELECT device_id,nonce FROM device_proof_nonces WHERE expires_at<clock_timestamp() ORDER BY expires_at LIMIT 10000)`)
	return err
}

type DeviceEvent struct {
	Revision  int64              `json:"revision"`
	State     string             `json:"state"`
	Actor     identity.Principal `json:"actor"`
	Reason    string             `json:"reason"`
	CreatedAt time.Time          `json:"created_at"`
}

func (s *Store) DeviceEvents(ctx context.Context, p identity.Principal, org, id string, after int64) (items []DeviceEvent, err error) {
	items = []DeviceEvent{}
	err = transaction(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := deviceAccess(ctx, tx, p, org, id); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT revision,state,actor_issuer,actor_subject,reason,created_at FROM device_state_events WHERE device_id=$1 AND (revision>$2 OR($2=0 AND revision=0)) ORDER BY revision LIMIT 51`, id, after)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v DeviceEvent
			if err := rows.Scan(&v.Revision, &v.State, &v.Actor.Issuer, &v.Actor.Subject, &v.Reason, &v.CreatedAt); err != nil {
				return err
			}
			items = append(items, v)
		}
		return rows.Err()
	})
	return
}

// Caller holds memberLock and the target member row for update. Revoked keys
// and devices never revive when a member is subsequently restored.
func revokeMemberDevices(ctx context.Context, tx pgx.Tx, p identity.Principal, org string, target identity.Principal, reason string) (int64, error) {
	var count int64
	err := tx.QueryRow(ctx, `WITH changed AS(UPDATE terminal_devices SET state='revoked',revision=revision+1 WHERE organization_id=$1 AND owner_issuer=$2 AND owner_subject=$3 AND state<>'revoked' RETURNING id,revision),
 events AS(INSERT INTO device_state_events(device_id,revision,state,actor_issuer,actor_subject,reason) SELECT id,revision,'revoked',$4,$5,$6 FROM changed),
 logged AS(INSERT INTO audit_events(organization_id,actor_issuer,actor_subject,action,resource_id) SELECT $1,$4,$5,'device.revoked',id FROM changed RETURNING id) SELECT count(*) FROM logged`, org, target.Issuer, target.Subject, p.Issuer, p.Subject, reason).Scan(&count)
	if err != nil {
		return 0, err
	}
	_, err = tx.Exec(ctx, `UPDATE device_sessions s SET revoked_at=clock_timestamp() FROM terminal_devices d WHERE s.device_id=d.id AND d.organization_id=$1 AND d.owner_issuer=$2 AND d.owner_subject=$3 AND s.revoked_at IS NULL`, org, target.Issuer, target.Subject)
	return count, err
}

func (s *Store) DeviceAdmissionReady(ctx context.Context) bool {
	if s.deviceOrigin != "" {
		return true
	}
	var ready bool
	err := s.pool.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM project_device_policies WHERE required)`).Scan(&ready)
	return err == nil && ready
}
