package newapibridge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
)

type Channel struct {
	ID                   int      `json:"id"`
	Name                 string   `json:"name"`
	Type                 int      `json:"type"`
	BaseURL              string   `json:"base_url"`
	Models               []string `json:"models"`
	Enabled              bool     `json:"enabled"`
	CredentialConfigured bool     `json:"credential_configured"`
	KeyCount             int      `json:"key_count"`
	Revision             string   `json:"revision"`
	EditRevision         string   `json:"edit_revision"`
	Editable             bool     `json:"editable"`
}
type ChannelPage struct {
	Data       []Channel `json:"data"`
	NextCursor string    `json:"next_cursor"`
}
type ChannelWrite struct {
	OperationID      string   `json:"operation_id"`
	ExpectedRevision string   `json:"expected_revision"`
	Name             string   `json:"name"`
	Type             int      `json:"type"`
	BaseURL          string   `json:"base_url"`
	Models           []string `json:"models"`
	Enabled          *bool    `json:"enabled"`
	APIKey           *string  `json:"api_key,omitempty"`
}

func (ChannelWrite) String() string   { return "[channel write; credential redacted]" }
func (ChannelWrite) GoString() string { return "[channel write; credential redacted]" }

type ChannelReceipt struct {
	OperationID    string `json:"operation_id"`
	ActorIssuer    string `json:"actor_issuer"`
	ActorSubject   string `json:"actor_subject"`
	ChannelID      int    `json:"channel_id"`
	BeforeRevision string `json:"before_revision"`
	AfterRevision  string `json:"after_revision"`
	CreatedAt      int64  `json:"created_at"`
}
type ChannelOperation struct {
	ID           string          `json:"id"`
	ChannelID    int             `json:"channel_id"`
	ActorIssuer  string          `json:"actor_issuer"`
	ActorSubject string          `json:"actor_subject"`
	State        string          `json:"state"`
	Receipt      *ChannelReceipt `json:"receipt,omitempty"`
	CreatedAt    time.Time       `json:"created_at"`
}

type ChannelManager struct {
	pool          *pgxpool.Pool
	origin, token string
	admin         identity.Principal
	client        *http.Client
	file          *provider.Config
}

func (*ChannelManager) String() string   { return "[new-api administration; credentials redacted]" }
func (*ChannelManager) GoString() string { return "[new-api administration; credentials redacted]" }

// An absent administration token keeps inference working, but disables writes.
func NewChannelManager(pool *pgxpool.Pool, origin, token string, admin identity.Principal, allowLoopback bool, file *provider.Config) (*ChannelManager, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.RawPath != "" || strings.Contains(origin, "#") || u.Opaque != "" || (u.Path != "" && u.Path != "/") || (u.Scheme != "https" && !(u.Scheme == "http" && allowLoopback && net.ParseIP(u.Hostname()).IsLoopback())) {
		return nil, ErrInvalid
	}
	if token != "" && (len(token) < 16 || len(token) > 256) {
		return nil, ErrInvalid
	}
	for _, r := range token {
		if r < 33 || r > 126 {
			return nil, ErrInvalid
		}
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DisableCompression = true
	transport.MaxResponseHeaderBytes = 16 << 10
	transport.MaxConnsPerHost = 4
	return &ChannelManager{pool: pool, origin: strings.TrimSuffix(origin, "/"), token: token, admin: admin, file: file, client: &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (m *ChannelManager) Close() { m.client.CloseIdleConnections() }
func (m *ChannelManager) CanManage(p identity.Principal) bool {
	return m != nil && m.token != "" && m.admin.Issuer != "" && m.admin.Subject != "" && m.admin == p
}

func (m *ChannelManager) remote(ctx context.Context, method, path string, in, out any) (int, error) {
	if m.token == "" {
		return 0, ErrUnavailable
	}
	var body []byte
	var err error
	if in != nil {
		body, err = json.Marshal(in)
		if err != nil {
			return 0, ErrInvalid
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, m.origin+"/internal/governance-admin/v1"+path, bytes.NewReader(body))
	if err != nil {
		return 0, ErrInvalid
	}
	req.Header.Set("Authorization", "Bearer "+m.token)
	req.Header.Set("Content-Type", "application/json")
	res, err := m.client.Do(req)
	if err != nil {
		return 0, ErrUnavailable
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, (256<<10)+1))
	if err != nil || len(raw) > 256<<10 || len(res.Header.Values("X-Aigw-Channel-Management-Version")) != 1 || res.Header.Get("X-Aigw-Channel-Management-Version") != "1" {
		return 0, ErrUnavailable
	}
	if res.StatusCode != 200 {
		return res.StatusCode, ErrUnavailable
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(out) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return 0, ErrUnavailable
	}
	return 200, nil
}
func (m *ChannelManager) Channels(ctx context.Context, p identity.Principal, after string) (ChannelPage, error) {
	page := ChannelPage{Data: []Channel{}}
	if !m.CanManage(p) {
		return page, identity.ErrForbidden
	}
	path := "/channels"
	if after != "" {
		n, err := strconv.Atoi(after)
		if err != nil || n < 1 || strconv.Itoa(n) != after {
			return page, ErrInvalid
		}
		path += "/after/" + after
	}
	_, err := m.remote(ctx, "GET", path, nil, &page)
	return page, err
}

func (m *ChannelManager) WriteChannel(ctx context.Context, p identity.Principal, id int, in ChannelWrite) (ChannelOperation, error) {
	var operation ChannelOperation
	if !m.CanManage(p) {
		return operation, identity.ErrForbidden
	}
	if id < 0 || !idPattern.MatchString(in.OperationID) || in.Enabled == nil {
		return operation, ErrInvalid
	}
	// Audit intent is durable before crossing the process boundary. Never store
	// credentials or the request body, even when the remote outcome is unknown.
	fingerprint, _ := json.Marshal(struct {
		ID    int
		Actor identity.Principal
		Write ChannelWrite
	}{id, p, in})
	digest := sha256.Sum256(fingerprint) // Includes the unique operation ID; no raw secret is retained.
	requestDigest := hex.EncodeToString(digest[:])
	tag, err := m.pool.Exec(ctx, `INSERT INTO newapi_channel_operations(id,actor_issuer,actor_subject,channel_id,expected_revision,data_plane_origin,request_digest,state) VALUES($1,$2,$3,$4,$5,$6,$7,'pending') ON CONFLICT(id) DO NOTHING`, in.OperationID, p.Issuer, p.Subject, id, in.ExpectedRevision, m.origin, requestDigest)
	if err != nil {
		return operation, ErrUnavailable
	}
	if tag.RowsAffected() == 0 {
		var existing string
		if err := m.pool.QueryRow(ctx, `SELECT request_digest FROM newapi_channel_operations WHERE id=$1 AND data_plane_origin=$2`, in.OperationID, m.origin).Scan(&existing); err != nil || existing != requestDigest {
			return operation, provider.ErrConfigurationConflict
		}
		return m.Operation(ctx, p, in.OperationID, true)
	}
	input := struct {
		ChannelWrite
		ActorIssuer  string `json:"actor_issuer"`
		ActorSubject string `json:"actor_subject"`
	}{in, p.Issuer, p.Subject}
	path, method := "/channels", "POST"
	if id > 0 {
		path += "/" + strconv.Itoa(id)
		method = "PUT"
	}
	var receipt ChannelReceipt
	status, remoteErr := m.remote(ctx, method, path, input, &receipt)
	input.APIKey = nil
	if remoteErr == nil && m.validReceipt(receipt, in.OperationID, p, id) {
		if err := m.recordReceipt(ctx, receipt); err != nil {
			return operation, ErrUnavailable
		}
	} else if status == 400 || status == 404 || status == 409 {
		if _, err = m.pool.Exec(ctx, `UPDATE newapi_channel_operations SET state='rejected',updated_at=now() WHERE id=$1 AND state='pending'`, in.OperationID); err != nil {
			return operation, ErrUnavailable
		}
	}
	return m.Operation(ctx, p, in.OperationID, false)
}
func (m *ChannelManager) validReceipt(r ChannelReceipt, id string, p identity.Principal, channel int) bool {
	return r.OperationID == id && r.ActorIssuer == p.Issuer && r.ActorSubject == p.Subject && r.ChannelID > 0 && (channel == 0 || r.ChannelID == channel) && revisionPattern.MatchString(r.AfterRevision) && (r.BeforeRevision == "" || revisionPattern.MatchString(r.BeforeRevision)) && r.CreatedAt > 0
}
func (m *ChannelManager) recordReceipt(ctx context.Context, r ChannelReceipt) error {
	raw, _ := json.Marshal(r)
	tag, err := m.pool.Exec(ctx, `UPDATE newapi_channel_operations SET state='applied',channel_id=$2,receipt=$3,updated_at=now() WHERE id=$1 AND actor_issuer=$4 AND actor_subject=$5 AND data_plane_origin=$6 AND state IN ('pending','applied')`, r.OperationID, r.ChannelID, raw, r.ActorIssuer, r.ActorSubject, m.origin)
	if err != nil || tag.RowsAffected() != 1 {
		return ErrUnavailable
	}
	return nil
}
func (m *ChannelManager) Operation(ctx context.Context, p identity.Principal, id string, recover bool) (ChannelOperation, error) {
	var op ChannelOperation
	if !m.CanManage(p) {
		return op, identity.ErrForbidden
	}
	if !idPattern.MatchString(id) {
		return op, ErrInvalid
	}
	var raw []byte
	err := m.pool.QueryRow(ctx, `SELECT id,channel_id,actor_issuer,actor_subject,state,receipt,created_at FROM newapi_channel_operations WHERE id=$1 AND data_plane_origin=$2`, id, m.origin).Scan(&op.ID, &op.ChannelID, &op.ActorIssuer, &op.ActorSubject, &op.State, &raw, &op.CreatedAt)
	if err != nil {
		return op, ErrUnavailable
	}
	if len(raw) > 0 && json.Unmarshal(raw, &op.Receipt) != nil {
		return op, ErrUnavailable
	}
	if recover && op.State == "pending" {
		var receipt ChannelReceipt
		_, remoteErr := m.remote(ctx, "GET", "/changes/"+id, nil, &receipt)
		actor := identity.Principal{Issuer: op.ActorIssuer, Subject: op.ActorSubject}
		if remoteErr == nil && m.validReceipt(receipt, id, actor, op.ChannelID) {
			if err := m.recordReceipt(ctx, receipt); err != nil {
				return op, ErrUnavailable
			}
			op.State, op.ChannelID, op.Receipt = "applied", receipt.ChannelID, &receipt
		}
	}
	return op, nil
}
func (m *ChannelManager) Operations(ctx context.Context, p identity.Principal) ([]ChannelOperation, error) {
	if !m.CanManage(p) {
		return nil, identity.ErrForbidden
	}
	rows, err := m.pool.Query(ctx, `SELECT id FROM newapi_channel_operations WHERE data_plane_origin=$1 ORDER BY created_at DESC,id DESC LIMIT 50`, m.origin)
	if err != nil {
		return nil, ErrUnavailable
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if rows.Scan(&id) != nil {
			rows.Close()
			return nil, ErrUnavailable
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, ErrUnavailable
	}
	result := make([]ChannelOperation, 0, len(ids))
	for _, id := range ids {
		op, err := m.Operation(ctx, p, id, false)
		if err != nil {
			return nil, err
		}
		result = append(result, op)
	}
	return result, nil
}

func (m *ChannelManager) read(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}) (provider.Config, int64, error) {
	if m.file != nil {
		return *m.file, 0, nil
	}
	var raw []byte
	var revision int64
	var origin string
	err := q.QueryRow(ctx, `SELECT configuration,revision,data_plane_origin FROM newapi_model_configuration WHERE singleton=true`).Scan(&raw, &revision, &origin)
	if errors.Is(err, pgx.ErrNoRows) {
		return provider.Config{}, 0, nil
	}
	if err != nil {
		return provider.Config{}, 0, err
	}
	if origin != m.origin {
		return provider.Config{}, 0, provider.ErrConfigurationConflict
	}
	cfg, err := provider.DecodeConfig(raw)
	return cfg, revision, err
}
func (m *ChannelManager) Status(ctx context.Context, p identity.Principal) (provider.ConfigurationStatus, error) {
	cfg, revision, err := m.read(ctx, m.pool)
	if err != nil {
		return provider.ConfigurationStatus{}, err
	}
	status := provider.ConfigurationStatus{ExecutionEngine: "newapi", CanManageChannels: m.CanManage(p), Managed: m.file == nil, CanManage: m.CanManage(p) && m.file == nil, Revision: revision, Suppliers: []provider.SupplierStatus{}, Models: []provider.ModelInfo{}}
	if catalog, err := provider.NewGoverned(cfg); err == nil {
		status.Models = catalog.Models()
		status.Ready = catalog.Ready()
	}
	if m.CanManage(p) && len(cfg.Models) > 0 {
		status.Configuration = &cfg
	}
	return status, nil
}
func (m *ChannelManager) SetCredential(context.Context, identity.Principal, string, int64, string) error {
	return identity.ErrForbidden
}

func (m *ChannelManager) Publish(ctx context.Context, p identity.Principal, expected int64, cfg provider.Config) error {
	if !m.CanManage(p) || m.file != nil {
		return identity.ErrForbidden
	}
	if expected < 0 || expected >= provider.MaxAmount {
		return provider.ErrConfigurationInvalid
	}
	if _, err := provider.NewGoverned(cfg); err != nil {
		return provider.ErrConfigurationInvalid
	}
	checked := map[int]Channel{}
	for _, route := range cfg.Models {
		channel, ok := checked[route.NewAPIChannelID]
		if !ok {
			if _, err := m.remote(ctx, "GET", "/channels/item/"+strconv.Itoa(route.NewAPIChannelID), nil, &channel); err != nil {
				return ErrUnavailable
			}
			checked[channel.ID] = channel
		}
		if channel.ID != route.NewAPIChannelID || !channel.Enabled || !channel.Editable || channel.Revision != route.NewAPIChannelRevision {
			return provider.ErrConfigurationConflict
		}
		found := false
		for _, model := range channel.Models {
			if model == route.UpstreamModel {
				found = true
			}
		}
		if !found || (route.API == "embeddings" && channel.Type != 1) || (route.AllowImages && channel.Type == 43) || (route.AllowMessagesStream && channel.Type != 14) {
			return provider.ErrConfigurationInvalid
		}
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return provider.ErrConfigurationInvalid
	}
	return pgx.BeginFunc(ctx, m.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(817349160131)`); err != nil {
			return err
		}
		_, revision, err := m.read(ctx, tx)
		if err != nil {
			return err
		}
		if revision != expected {
			return provider.ErrConfigurationConflict
		}
		if _, err := tx.Exec(ctx, `INSERT INTO newapi_model_configuration(singleton,revision,configuration,data_plane_origin) VALUES(true,$1,$2,$3) ON CONFLICT(singleton) DO UPDATE SET revision=EXCLUDED.revision,configuration=EXCLUDED.configuration,updated_at=now()`, revision+1, raw, m.origin); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO newapi_model_configuration_events(revision,actor_issuer,actor_subject,configuration) VALUES($1,$2,$3,$4)`, revision+1, p.Issuer, p.Subject, raw)
		return err
	})
}
