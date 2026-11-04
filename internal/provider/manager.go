package provider

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
)

var ErrConfigurationConflict = errors.New("provider configuration revision conflict")
var ErrConfigurationInvalid = errors.New("provider configuration invalid or required credential unavailable")

// Routing supplies immutable route snapshots; a published change never mutates an in-flight route.
type Routing interface {
	Find(string) (*Route, bool)
	Models() []ModelInfo
	Ready() bool
}

type SupplierStatus struct {
	ID                   string `json:"id"`
	Region               string `json:"region"`
	Endpoint             string `json:"endpoint"`
	KeyEnvironment       string `json:"key_environment"`
	CredentialConfigured bool   `json:"credential_configured"`
}
type ConfigurationStatus struct {
	Managed       bool             `json:"managed"`
	CanManage     bool             `json:"can_manage"`
	Revision      int64            `json:"revision"`
	Ready         bool             `json:"ready"`
	Suppliers     []SupplierStatus `json:"suppliers"`
	Models        []ModelInfo      `json:"models"`
	Configuration *Config          `json:"configuration,omitempty"`
}

// Manager reads the database on every acquisition: no stale local cache or secret persistence.
// Only deployment-bound OIDC principals can publish a platform-wide configuration.
type Manager struct {
	pool          *pgxpool.Pool
	getenv        func(string) string
	transport     *http.Client
	administrator identity.Principal
}

func NewManager(pool *pgxpool.Pool, getenv func(string) string, transport *http.Client, administrator identity.Principal) *Manager {
	return &Manager{pool: pool, getenv: getenv, transport: transport, administrator: administrator}
}
func (m *Manager) CanManage(p identity.Principal) bool {
	return m != nil && m.administrator.Issuer != "" && m.administrator.Subject != "" && m.administrator == p
}
func (m *Manager) read(ctx context.Context) (cfg Config, revision int64, err error) {
	var raw []byte
	err = m.pool.QueryRow(ctx, `SELECT configuration,revision FROM provider_configuration WHERE singleton=true`).Scan(&raw, &revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return Config{}, 0, nil
	}
	if err != nil {
		return cfg, 0, err
	}
	cfg, err = DecodeConfig(raw)
	return
}
func (m *Manager) snapshot() *Catalog {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cfg, rev, err := m.read(ctx)
	if err != nil || rev == 0 {
		return nil
	}
	c, err := New(cfg, m.getenv, m.transport)
	if err != nil {
		return nil
	}
	return c
}
func (m *Manager) Find(id string) (*Route, bool) { return m.snapshot().Find(id) }
func (m *Manager) Models() []ModelInfo           { return m.snapshot().Models() }
func (m *Manager) Ready() bool                   { return m.snapshot().Ready() }
func (m *Manager) Status(ctx context.Context, p identity.Principal) (ConfigurationStatus, error) {
	cfg, rev, err := m.read(ctx)
	if err != nil {
		return ConfigurationStatus{}, err
	}
	status := ConfigurationStatus{Managed: true, CanManage: m.CanManage(p), Revision: rev, Models: []ModelInfo{}, Suppliers: []SupplierStatus{}}
	for _, profile := range Profiles() {
		status.Suppliers = append(status.Suppliers, SupplierStatus{profile.ID, profile.Region, profile.Endpoint, profile.KeyEnvironment, strings.TrimSpace(m.getenv(profile.KeyEnvironment)) != ""})
	}
	if rev > 0 {
		if c, e := New(cfg, m.getenv, m.transport); e == nil {
			status.Ready = c.Ready()
			status.Models = c.Models()
		}
	}
	if status.CanManage && rev > 0 {
		status.Configuration = &cfg
	}
	return status, nil
}
func (m *Manager) Publish(ctx context.Context, p identity.Principal, expected int64, cfg Config) error {
	if !m.CanManage(p) {
		return identity.ErrForbidden
	}
	if expected < 0 || expected >= MaxAmount {
		return ErrConfigurationInvalid
	}
	if _, err := New(cfg, m.getenv, m.transport); err != nil {
		return ErrConfigurationInvalid
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return ErrConfigurationInvalid
	}
	digest := sha256.Sum256(raw)
	return pgx.BeginFunc(ctx, m.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(817349160125)`); err != nil {
			return err
		}
		var revision int64
		err := tx.QueryRow(ctx, `SELECT revision FROM provider_configuration WHERE singleton=true FOR UPDATE`).Scan(&revision)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if revision != expected {
			return ErrConfigurationConflict
		}
		if _, err := tx.Exec(ctx, `INSERT INTO provider_configuration(singleton,revision,configuration) VALUES(true,$1,$2) ON CONFLICT(singleton) DO UPDATE SET revision=EXCLUDED.revision,configuration=EXCLUDED.configuration,updated_at=now()`, revision+1, raw); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO provider_configuration_events(revision,actor_issuer,actor_subject,digest,configuration) VALUES($1,$2,$3,$4,$5)`, revision+1, p.Issuer, p.Subject, digest[:], raw)
		return err
	})
}

// ModelsIn reuses a control transaction's connection instead of acquiring a
// nested pool connection. This keeps authorization usable with a one-slot pool.
func (m *Manager) ModelsIn(ctx context.Context, tx pgx.Tx) []ModelInfo {
	if tx == nil {
		return m.Models()
	}
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT configuration FROM provider_configuration WHERE singleton=true`).Scan(&raw); err != nil {
		return []ModelInfo{}
	}
	cfg, err := DecodeConfig(raw)
	if err != nil {
		return []ModelInfo{}
	}
	catalog, err := New(cfg, m.getenv, m.transport)
	if err != nil {
		return []ModelInfo{}
	}
	return catalog.Models()
}
