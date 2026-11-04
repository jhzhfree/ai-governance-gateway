// Package localauth provides explicit offline accounts; it never auto-creates a user.
package localauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
	"golang.org/x/crypto/argon2"
)

var ErrInvalid = errors.New("invalid local account input")
var ErrConflict = errors.New("local account changed or protected administrator")
var ErrRateLimited = errors.New("local login rate limited")
var ErrPasswordChange = errors.New("local password change required")
var usernamePattern = regexp.MustCompile(`^[a-z][a-z0-9._-]{2,63}$`)

const TokenTTL = 2 * time.Hour

type User struct {
	ID                 string    `json:"id"`
	Username           string    `json:"username"`
	Enabled            bool      `json:"enabled"`
	Administrator      bool      `json:"administrator"`
	MustChangePassword bool      `json:"must_change_password"`
	Revision           int64     `json:"revision"`
	CreatedAt          time.Time `json:"created_at"`
}
type Service struct {
	pool   *pgxpool.Pool
	issuer string
	slots  chan struct{}
}

func New(pool *pgxpool.Pool, origin string, allowLoopback bool) (*Service, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(allowLoopback && u.Scheme == "http" && net.ParseIP(u.Hostname()).IsLoopback())) {
		return nil, ErrInvalid
	}
	return &Service{pool: pool, issuer: origin + "/auth/local", slots: make(chan struct{}, 2)}, nil
}
func (s *Service) Issuer() string { return s.issuer }
func (s *Service) Principal(user User) identity.Principal {
	return identity.Principal{Issuer: s.issuer, Subject: user.ID}
}
func Digest(value string) []byte { digest := sha256.Sum256([]byte(value)); return digest[:] }
func validPassword(password string) bool {
	return len(password) <= 256 && utf8.RuneCountInString(password) >= 12
}
func (s *Service) derive(password string, salt []byte) ([]byte, error) {
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		return nil, ErrRateLimited
	}
	return argon2.IDKey([]byte(password), salt, 3, 64*1024, 1, 32), nil
}
func (s *Service) newPassword(password string) ([]byte, []byte, error) {
	if !validPassword(password) {
		return nil, nil, ErrInvalid
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, nil, err
	}
	hash, err := s.derive(password, salt)
	return salt, hash, err
}

type scanner interface{ Scan(...any) error }

func scanUser(row scanner) (u User, err error) {
	err = row.Scan(&u.ID, &u.Username, &u.Enabled, &u.Administrator, &u.MustChangePassword, &u.Revision, &u.CreatedAt)
	return
}

const userColumns = "id,username,enabled,administrator,must_change_password,revision,created_at"

func (s *Service) User(ctx context.Context, p identity.Principal) (User, error) {
	if p.Issuer != s.issuer {
		return User{}, identity.ErrUnauthenticated
	}
	u, err := scanUser(s.pool.QueryRow(ctx, `SELECT `+userColumns+` FROM local_users WHERE id=$1`, p.Subject))
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !u.Enabled {
		return User{}, identity.ErrUnauthenticated
	}
	return u, err
}
func (s *Service) admin(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, p identity.Principal) error {
	if p.Issuer != s.issuer {
		return identity.ErrForbidden
	}
	var allowed bool
	if err := q.QueryRow(ctx, `SELECT enabled AND administrator AND NOT must_change_password FROM local_users WHERE id=$1 FOR SHARE`, p.Subject).Scan(&allowed); errors.Is(err, pgx.ErrNoRows) {
		return identity.ErrForbidden
	} else if err != nil {
		return err
	}
	if !allowed {
		return identity.ErrForbidden
	}
	return nil
}
func (s *Service) IsAdmin(ctx context.Context, p identity.Principal) bool {
	return s.admin(ctx, s.pool, p) == nil
}
func securityEvent(ctx context.Context, tx pgx.Tx, p identity.Principal, action, target string) error {
	_, err := tx.Exec(ctx, `INSERT INTO local_security_events(actor_issuer,actor_subject,action,target_id) VALUES($1,$2,$3,$4)`, p.Issuer, p.Subject, action, target)
	return err
}
func (s *Service) create(ctx context.Context, p identity.Principal, username, password string, administrator, initial bool) (u User, err error) {
	username = strings.ToLower(strings.TrimSpace(username))
	if !usernamePattern.MatchString(username) {
		return u, ErrInvalid
	}
	if !initial {
		if err := s.admin(ctx, s.pool, p); err != nil {
			return u, err
		}
	}
	salt, hash, err := s.newPassword(password)
	if err != nil {
		return u, err
	}
	u = User{ID: rand.Text(), Username: username, Enabled: true, Administrator: administrator, MustChangePassword: true, Revision: 1}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(817349160150)`); err != nil {
			return err
		}
		if initial {
			var n int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM local_users`).Scan(&n); err != nil {
				return err
			}
			if n != 0 {
				return ErrConflict
			}
		} else if err := s.admin(ctx, tx, p); err != nil {
			return err
		}
		// Conflict is checked without exposing the username through database errors.
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM local_users WHERE username=$1)`, username).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return ErrConflict
		}
		if err := tx.QueryRow(ctx, `INSERT INTO local_users(id,username,password_salt,password_hash,administrator) VALUES($1,$2,$3,$4,$5) RETURNING created_at`, u.ID, u.Username, salt, hash, u.Administrator).Scan(&u.CreatedAt); err != nil {
			return err
		}
		return securityEvent(ctx, tx, p, "local.user_created", u.ID)
	})
	return
}
func (s *Service) Initialize(ctx context.Context, username, password string) (User, error) {
	return s.create(ctx, identity.Principal{Issuer: "system:local-auth", Subject: "offline-initializer"}, username, password, true, true)
}
func (s *Service) Create(ctx context.Context, p identity.Principal, username, password string, administrator bool) (User, error) {
	return s.create(ctx, p, username, password, administrator, false)
}
func (s *Service) CheckPassword(ctx context.Context, username, password string) (u User, err error) {
	username = strings.ToLower(strings.TrimSpace(username))
	if !usernamePattern.MatchString(username) || len(password) > 256 || password == "" {
		return u, identity.ErrUnauthenticated
	}
	var attempts int
	if err = s.pool.QueryRow(ctx, `INSERT INTO local_auth_throttle(digest,window_start,attempts) VALUES($1,clock_timestamp(),1) ON CONFLICT(digest) DO UPDATE SET attempts=CASE WHEN local_auth_throttle.window_start<clock_timestamp()-interval '1 minute' THEN 1 ELSE least(local_auth_throttle.attempts+1,1000000) END,window_start=CASE WHEN local_auth_throttle.window_start<clock_timestamp()-interval '1 minute' THEN clock_timestamp() ELSE local_auth_throttle.window_start END RETURNING attempts`, Digest("local-login:"+username)).Scan(&attempts); err != nil {
		return u, err
	}
	if attempts > 5 {
		return u, ErrRateLimited
	}
	var salt, hash []byte
	err = s.pool.QueryRow(ctx, `SELECT `+userColumns+`,password_salt,password_hash FROM local_users WHERE username=$1`, username).Scan(&u.ID, &u.Username, &u.Enabled, &u.Administrator, &u.MustChangePassword, &u.Revision, &u.CreatedAt, &salt, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		salt = make([]byte, 16)
		hash = make([]byte, 32)
	} else if err != nil {
		return User{}, err
	}
	actual, err := s.derive(password, salt)
	if err != nil {
		return User{}, err
	}
	if subtle.ConstantTimeCompare(hash, actual) != 1 || u.ID == "" || !u.Enabled {
		return User{}, identity.ErrUnauthenticated
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM local_auth_throttle WHERE digest=$1`, Digest("local-login:"+username)); err != nil {
		return User{}, err
	}
	return u, nil
}
func (s *Service) lockUser(ctx context.Context, tx pgx.Tx, user User) error {
	var enabled bool
	var revision int64
	if err := tx.QueryRow(ctx, `SELECT enabled,revision FROM local_users WHERE id=$1 FOR SHARE`, user.ID).Scan(&enabled, &revision); err != nil {
		return err
	}
	if !enabled || revision != user.Revision {
		return identity.ErrUnauthenticated
	}
	return nil
}
func (s *Service) OpenSession(ctx context.Context, user User, secret string, expires time.Time, oldDigest []byte) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := s.lockUser(ctx, tx, user); err != nil {
			return err
		}
		if len(oldDigest) > 0 {
			if _, err := tx.Exec(ctx, `DELETE FROM browser_sessions WHERE digest=$1`, oldDigest); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO browser_sessions(digest,issuer,subject,expires_at) VALUES($1,$2,$3,$4)`, Digest(secret), s.issuer, user.ID, expires); err != nil {
			return err
		}
		return securityEvent(ctx, tx, s.Principal(user), "local.browser_login", user.ID)
	})
}

type Token struct {
	AccessToken string             `json:"access_token"`
	ExpiresAt   time.Time          `json:"expires_at"`
	User        identity.Principal `json:"user"`
}

func (Token) String() string   { return "[local token redacted]" }
func (Token) GoString() string { return "[local token redacted]" }
func (s *Service) Token(ctx context.Context, username, password string) (v Token, err error) {
	u, err := s.CheckPassword(ctx, username, password)
	if err != nil {
		return v, err
	}
	if u.MustChangePassword {
		return v, ErrPasswordChange
	}
	v = Token{AccessToken: "aigw_local_" + rand.Text() + rand.Text(), ExpiresAt: time.Now().Add(TokenTTL), User: s.Principal(u)}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := s.lockUser(ctx, tx, u); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO local_access_tokens(digest,user_id,expires_at) VALUES($1,$2,$3)`, Digest(v.AccessToken), u.ID, v.ExpiresAt); err != nil {
			return err
		}
		return securityEvent(ctx, tx, v.User, "local.token_issued", u.ID)
	})
	if err != nil {
		return Token{}, err
	}
	return
}
func (s *Service) Authenticate(ctx context.Context, token string) (p identity.Principal, err error) {
	if !strings.HasPrefix(token, "aigw_local_") || len(token) > 256 {
		return p, identity.ErrUnauthenticated
	}
	p.Issuer = s.issuer
	err = s.pool.QueryRow(ctx, `SELECT u.id FROM local_access_tokens t JOIN local_users u ON u.id=t.user_id WHERE t.digest=$1 AND t.expires_at>clock_timestamp() AND u.enabled AND NOT u.must_change_password`, Digest(token)).Scan(&p.Subject)
	if errors.Is(err, pgx.ErrNoRows) {
		err = identity.ErrUnauthenticated
	}
	return
}
func (s *Service) ChangePassword(ctx context.Context, p identity.Principal, current, next string) error {
	user, err := s.User(ctx, p)
	if err != nil {
		return err
	}
	verified, err := s.CheckPassword(ctx, user.Username, current)
	if err != nil {
		return err
	}
	if current == next {
		return ErrInvalid
	}
	salt, hash, err := s.newPassword(next)
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var revision int64
		if err := tx.QueryRow(ctx, `SELECT revision FROM local_users WHERE id=$1 FOR UPDATE`, user.ID).Scan(&revision); err != nil {
			return err
		}
		if revision != verified.Revision {
			return ErrConflict
		}
		if _, err := tx.Exec(ctx, `UPDATE local_users SET password_salt=$2,password_hash=$3,must_change_password=false,revision=revision+1 WHERE id=$1`, user.ID, salt, hash); err != nil {
			return err
		}
		if err := s.revoke(ctx, tx, user.ID); err != nil {
			return err
		}
		return securityEvent(ctx, tx, p, "local.password_changed", user.ID)
	})
}
func (s *Service) revoke(ctx context.Context, tx pgx.Tx, id string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM local_access_tokens WHERE user_id=$1`, id); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `DELETE FROM browser_sessions WHERE issuer=$1 AND subject=$2`, s.issuer, id)
	return err
}
func (s *Service) SetUser(ctx context.Context, p identity.Principal, id string, expected int64, enabled, administrator bool) error {
	if expected < 1 || expected >= 9007199254740991 {
		return ErrInvalid
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(817349160150)`); err != nil {
			return err
		}
		if err := s.admin(ctx, tx, p); err != nil {
			return err
		}
		if id == p.Subject && (!enabled || !administrator) {
			return ErrConflict
		}
		var revision int64
		if err := tx.QueryRow(ctx, `SELECT revision FROM local_users WHERE id=$1 FOR UPDATE`, id).Scan(&revision); errors.Is(err, pgx.ErrNoRows) {
			return ErrInvalid
		} else if err != nil {
			return err
		}
		if revision != expected {
			return ErrConflict
		}
		if _, err := tx.Exec(ctx, `UPDATE local_users SET enabled=$2,administrator=$3,revision=revision+1 WHERE id=$1`, id, enabled, administrator); err != nil {
			return err
		}
		if !enabled {
			if err := s.revoke(ctx, tx, id); err != nil {
				return err
			}

			if _, err := tx.Exec(ctx, `WITH revoked AS (UPDATE project_keys SET revoked_at=clock_timestamp() WHERE creator_issuer=$1 AND creator_subject=$2 AND revoked_at IS NULL RETURNING id,organization_id) INSERT INTO audit_events(organization_id,actor_issuer,actor_subject,action,resource_id) SELECT organization_id,$3,$4,'project.key_revoked',id FROM revoked`, s.issuer, id, p.Issuer, p.Subject); err != nil {
				return err
			}
		}
		return securityEvent(ctx, tx, p, "local.user_changed", id)
	})
}
func (s *Service) ResetPassword(ctx context.Context, p identity.Principal, id, next string, expected int64) error {
	if expected < 1 || expected >= 9007199254740991 {
		return ErrInvalid
	}
	if err := s.admin(ctx, s.pool, p); err != nil {
		return err
	}
	salt, hash, err := s.newPassword(next)
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(817349160150)`); err != nil {
			return err
		}
		if err := s.admin(ctx, tx, p); err != nil {
			return err
		}
		var revision int64
		if err := tx.QueryRow(ctx, `SELECT revision FROM local_users WHERE id=$1 FOR UPDATE`, id).Scan(&revision); err != nil {
			return ErrInvalid
		}
		if revision != expected {
			return ErrConflict
		}
		if _, err := tx.Exec(ctx, `UPDATE local_users SET password_salt=$2,password_hash=$3,must_change_password=true,revision=revision+1 WHERE id=$1`, id, salt, hash); err != nil {
			return err
		}
		if err := s.revoke(ctx, tx, id); err != nil {
			return err
		}
		return securityEvent(ctx, tx, p, "local.password_reset", id)
	})
}
func (s *Service) RevokeUser(ctx context.Context, p identity.Principal, id string) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(817349160150)`); err != nil {
			return err
		}
		if err := s.admin(ctx, tx, p); err != nil {
			return err
		}
		var target string
		if err := tx.QueryRow(ctx, `SELECT id FROM local_users WHERE id=$1 FOR UPDATE`, id).Scan(&target); err != nil {
			return ErrInvalid
		}
		if err := s.revoke(ctx, tx, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE local_users SET revision=revision+1 WHERE id=$1`, id); err != nil {
			return err
		}
		return securityEvent(ctx, tx, p, "local.sessions_revoked", id)
	})
}
func (s *Service) Users(ctx context.Context, p identity.Principal, after string) (items []User, err error) {
	items = []User{}
	if len(after) > 128 {
		return nil, ErrInvalid
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := s.admin(ctx, tx, p); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT `+userColumns+` FROM local_users WHERE id>$1 ORDER BY id LIMIT 101`, after)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			u, err := scanUser(rows)
			if err != nil {
				return err
			}
			items = append(items, u)
		}
		return rows.Err()
	})
	return
}
func (s *Service) Maintenance(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM local_access_tokens WHERE digest IN(SELECT digest FROM local_access_tokens WHERE expires_at<=clock_timestamp() LIMIT 1000)`)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `DELETE FROM local_auth_throttle WHERE digest IN(SELECT digest FROM local_auth_throttle WHERE window_start<clock_timestamp()-interval '1 hour' LIMIT 1000)`)
	return err
}

// Recover is an offline DB-operator action; no HTTP route exposes it.
func (s *Service) Recover(ctx context.Context, username, next string) (User, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	salt, hash, err := s.newPassword(next)
	if err != nil {
		return User{}, err
	}
	var user User
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(817349160150)`); err != nil {
			return err
		}
		var e error
		user, e = scanUser(tx.QueryRow(ctx, `SELECT `+userColumns+` FROM local_users WHERE username=$1 AND administrator FOR UPDATE`, username))
		if e != nil {
			return ErrInvalid
		}
		if _, err := tx.Exec(ctx, `UPDATE local_users SET enabled=true,password_salt=$2,password_hash=$3,must_change_password=true,revision=revision+1 WHERE id=$1`, user.ID, salt, hash); err != nil {
			return err
		}
		if err := s.revoke(ctx, tx, user.ID); err != nil {
			return err
		}
		return securityEvent(ctx, tx, identity.Principal{Issuer: "system:local-auth", Subject: "offline-recovery"}, "local.admin_recovered", user.ID)
	})
	return user, err
}

func (*Service) String() string     { return "[local identity service; credentials redacted]" }
func (s *Service) GoString() string { return s.String() }

func (s *Service) RevokeToken(ctx context.Context, secret string) error {
	p, err := s.Authenticate(ctx, secret)
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM local_access_tokens WHERE digest=$1`, Digest(secret)); err != nil {
			return err
		}
		return securityEvent(ctx, tx, p, "local.token_revoked", p.Subject)
	})
}

type SecurityEvent struct {
	ID        int64              `json:"id"`
	Actor     identity.Principal `json:"actor"`
	Action    string             `json:"action"`
	TargetID  string             `json:"target_id"`
	CreatedAt time.Time          `json:"created_at"`
}

func (s *Service) Events(ctx context.Context, p identity.Principal, before int64) (items []SecurityEvent, err error) {
	items = []SecurityEvent{}
	if before < 0 {
		return nil, ErrInvalid
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := s.admin(ctx, tx, p); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT id,actor_issuer,actor_subject,action,target_id,created_at FROM local_security_events WHERE ($1::bigint=0 OR id<$1) ORDER BY id DESC LIMIT 101`, before)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v SecurityEvent
			if err := rows.Scan(&v.ID, &v.Actor.Issuer, &v.Actor.Subject, &v.Action, &v.TargetID, &v.CreatedAt); err != nil {
				return err
			}
			items = append(items, v)
		}
		return rows.Err()
	})
	return
}

// AuthorizeActor holds the user row through a credential-issuing transaction.
// Disable/reset must either revoke that credential afterward or precede a denial.
func (s *Service) AuthorizeActor(ctx context.Context, tx pgx.Tx, p identity.Principal) error {
	if p.Issuer != s.issuer {
		return identity.ErrForbidden
	}
	var active bool
	if err := tx.QueryRow(ctx, `SELECT enabled AND NOT must_change_password FROM local_users WHERE id=$1 FOR SHARE`, p.Subject).Scan(&active); err != nil {
		return identity.ErrForbidden
	}
	if !active {
		return identity.ErrForbidden
	}
	return nil
}
