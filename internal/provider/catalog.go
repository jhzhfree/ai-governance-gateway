// Package provider contains explicitly configured mainland supplier routes.
package provider

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

const Currency = "CNY_MICRO" // one yuan = 1,000,000 micro-yuan
const MaxAmount int64 = 9007199254740991

type Profile struct {
	ID, Region, Endpoint, KeyEnvironment string
	IncludeUsage                         bool
}

// These are fixed service entry points, not proof of provider-internal residency.
// Deployment operators must verify account contracts before enabling a route.
var profiles = map[string]Profile{
	"deepseek":   {"deepseek", "cn-mainland", "https://api.deepseek.com/chat/completions", "AIGW_DEEPSEEK_API_KEY", true},
	"aliyun":     {"aliyun", "cn-beijing", "https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions", "AIGW_ALIYUN_API_KEY", true},
	"volcengine": {"volcengine", "cn-beijing", "https://ark.cn-beijing.volces.com/api/v3/chat/completions", "AIGW_VOLCENGINE_API_KEY", true},
	"zhipu":      {"zhipu", "cn-mainland", "https://open.bigmodel.cn/api/paas/v4/chat/completions", "AIGW_ZHIPU_API_KEY", false},
}

func Profiles() []Profile {
	result := []Profile{}
	for _, id := range []string{"aliyun", "volcengine", "zhipu", "deepseek"} {
		result = append(result, profiles[id])
	}
	return result
}

type Residency struct {
	Region      string    `json:"region"`
	EvidenceRef string    `json:"evidence_ref"`
	ExpiresAt   time.Time `json:"expires_at"`
}

func (r Residency) valid(now time.Time) bool {
	return r.Region == "cn-mainland" && strings.TrimSpace(r.EvidenceRef) != "" && len(r.EvidenceRef) <= 256 && r.ExpiresAt.After(now)
}

type Model struct {
	AllowImages      bool      `json:"allow_images,omitempty"`
	NewAPIChannelID  int       `json:"newapi_channel_id,omitempty"`
	ID               string    `json:"id"`
	Provider         string    `json:"provider"`
	UpstreamModel    string    `json:"upstream_model"`
	PriceVersion     string    `json:"price_version"`
	InputPerMillion  int64     `json:"input_per_million"`
	OutputPerMillion int64     `json:"output_per_million"`
	MaxInputTokens   int64     `json:"max_input_tokens"`
	MaxOutputTokens  int64     `json:"max_output_tokens"`
	Residency        Residency `json:"residency"`
}
type Config struct {
	Version             int       `json:"version"`
	DeploymentResidency Residency `json:"deployment_residency"`
	Models              []Model   `json:"models"`
}
type ModelInfo struct {
	AllowImages      bool   `json:"allow_images,omitempty"`
	ID               string `json:"id"`
	Provider         string `json:"provider"`
	Currency         string `json:"currency"`
	Region           string `json:"region"`
	PriceVersion     string `json:"price_version"`
	InputPerMillion  int64  `json:"input_per_million"`
	OutputPerMillion int64  `json:"output_per_million"`
	MaxInputTokens   int64  `json:"max_input_tokens"`
	MaxOutputTokens  int64  `json:"max_output_tokens"`
}

func MockInfo() ModelInfo {
	return ModelInfo{ID: "mock-text", Provider: "mock", Currency: "MOCK", Region: "local", PriceVersion: "mock-v1", MaxInputTokens: 2, MaxOutputTokens: 8}
}

type Route struct {
	model       Model
	profile     Profile
	key         string
	client      *http.Client
	deployment  Residency
	fingerprint string
}

func (Route) String() string   { return "[supplier route; credentials redacted]" }
func (Route) GoString() string { return "[supplier route; credentials redacted]" }

type Catalog struct{ routes map[string]*Route }

func (Catalog) String() string   { return "[supplier catalog; credentials redacted]" }
func (Catalog) GoString() string { return "[supplier catalog; credentials redacted]" }

var upstreamPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)
var supplierPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

// ValidSupplierID validates reviewed directory identities, never credentials or endpoints.
func ValidSupplierID(id string) bool { return id != "mock" && supplierPattern.MatchString(id) }

var pricePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)
var modelPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:/-]{0,127}$`)

func ReadConfig(path string) (Config, error) {
	var cfg Config
	f, err := os.Open(path)
	if err != nil {
		return cfg, errors.New("cannot read provider configuration")
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (256<<10)+1))
	if err != nil {
		return cfg, errors.New("cannot read provider configuration")
	}
	return DecodeConfig(data)
}
func New(cfg Config, getenv func(string) string, transport *http.Client) (*Catalog, error) {
	return newCatalog(cfg, getenv, transport, true)
}

// NewGoverned creates a price/residency directory whose vendor credentials and
// physical transport live exclusively in the new-api data plane.
func NewGoverned(cfg Config) (*Catalog, error) {
	return newCatalog(cfg, func(string) string { return "" }, nil, false)
}
func newCatalog(cfg Config, getenv func(string) string, transport *http.Client, requireCredential bool) (*Catalog, error) {

	if cfg.Version != 1 || !cfg.DeploymentResidency.valid(time.Now()) || len(cfg.Models) < 1 || len(cfg.Models) > 32 || getenv == nil {
		return nil, errors.New("provider configuration requires current mainland deployment/storage residency evidence and 1–32 models")
	}
	client := &http.Client{Timeout: 120 * time.Second}
	if transport != nil {
		*client = *transport
	}
	if client.Timeout == 0 {
		client.Timeout = 120 * time.Second
	}
	client.Jar = nil
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	catalog := &Catalog{routes: map[string]*Route{}}
	for _, m := range cfg.Models {
		if (!requireCredential && m.NewAPIChannelID <= 0) || (requireCredential && (m.NewAPIChannelID != 0 || m.AllowImages)) {
			return nil, errors.New("newapi directory requires an explicit approved channel; native directories cannot specify one")
		}
		p, ok := profiles[m.Provider]
		// The data plane owns the reviewed upstream endpoint. A new-api
		// directory may name a reviewed supplier without inventing a native
		// endpoint or borrowing another supplier's identity.
		if !requireCredential && !ok && ValidSupplierID(m.Provider) {
			p = Profile{ID: m.Provider, Region: "cn-mainland"}
			ok = true
		}
		if !ok || !strings.HasPrefix(m.ID, m.Provider+"/") || !modelPattern.MatchString(m.ID) || !upstreamPattern.MatchString(m.UpstreamModel) || !pricePattern.MatchString(m.PriceVersion) || !m.Residency.valid(time.Now()) || m.MaxInputTokens < 1 || m.MaxInputTokens > 2000000 || m.MaxOutputTokens < 1 || m.MaxOutputTokens > 1000000 || m.InputPerMillion <= 0 || m.OutputPerMillion <= 0 || m.InputPerMillion > MaxAmount || m.OutputPerMillion > MaxAmount {
			return nil, errors.New("model requires a supported mainland supplier, explicit prices, token bounds and current residency evidence")
		}
		if _, exists := catalog.routes[m.ID]; exists {
			return nil, errors.New("duplicate model alias")
		}
		key := getenv(p.KeyEnvironment)
		if requireCredential && (strings.TrimSpace(key) == "" || len(key) > 8192 || strings.IndexFunc(key, func(r rune) bool { return r < 33 || r > 126 }) >= 0) {
			return nil, errors.New("required supplier credential unavailable or invalid: " + p.KeyEnvironment)
		}
		// Exclude residency review dates from the immutable price/route identity.
		canonical, _ := json.Marshal(struct {
			ID, Provider, Model, Version, Endpoint, Region, Currency string
			Input, Output, MaxInput, MaxOutput                       int64
		}{m.ID, m.Provider, m.UpstreamModel, m.PriceVersion, p.Endpoint, p.Region, Currency, m.InputPerMillion, m.OutputPerMillion, m.MaxInputTokens, m.MaxOutputTokens})
		if !requireCredential {
			if m.AllowImages {
				canonical = append(canonical, []byte("\ninline-images-v1")...)
			}
			channelBinding, _ := json.Marshal(m.NewAPIChannelID)
			canonical = append(append(canonical, '\n'), channelBinding...)
		}
		digest := sha256.Sum256(canonical)
		route := &Route{m, p, key, client, cfg.DeploymentResidency, m.ID + "@" + m.PriceVersion + "#" + hex.EncodeToString(digest[:])}
		if _, err := route.Bound(int(m.MaxOutputTokens)); err != nil {
			return nil, err
		}
		catalog.routes[m.ID] = route
	}
	return catalog, nil
}
func (c *Catalog) Find(id string) (*Route, bool) {
	if c == nil {
		return nil, false
	}
	r, ok := c.routes[id]
	return r, ok
}
func (c *Catalog) Models() []ModelInfo {
	result := []ModelInfo{}
	if c == nil {
		return result
	}
	for _, r := range c.routes {
		if r.Ready() {
			m := r.model
			result = append(result, ModelInfo{m.AllowImages, m.ID, m.Provider, Currency, r.profile.Region, r.fingerprint, m.InputPerMillion, m.OutputPerMillion, m.MaxInputTokens, m.MaxOutputTokens})
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}
func (r *Route) Ready() bool {
	return r != nil && r.deployment.valid(time.Now()) && r.model.Residency.valid(time.Now())
}
func (r *Route) PriceVersion() string { return r.fingerprint }
func (r *Route) Cost(input, output int64) (int64, error) {
	if input < 0 || output < 0 || input > r.model.MaxInputTokens || output > r.model.MaxOutputTokens {
		return 0, errors.New("supplier usage exceeds configured bounds")
	}
	in := new(big.Int).Mul(big.NewInt(input), big.NewInt(r.model.InputPerMillion))
	out := new(big.Int).Mul(big.NewInt(output), big.NewInt(r.model.OutputPerMillion))
	in.Add(in, out)
	in.Add(in, big.NewInt(999999))
	in.Quo(in, big.NewInt(1000000))
	if !in.IsInt64() || in.Int64() > MaxAmount {
		return 0, errors.New("configured price exceeds safe accounting range")
	}
	return in.Int64(), nil
}

// Bound reserves the full configured input/context bound, never a guessed tokenizer count.
func (r *Route) Bound(maxOutput int) (int64, error) {
	if maxOutput < 1 || int64(maxOutput) > r.model.MaxOutputTokens {
		return 0, errors.New("unsupported max_tokens")
	}
	return r.Cost(r.model.MaxInputTokens, int64(maxOutput))
}
func (r *Route) ValidateUsage(u *Usage, maxOutput int) (int64, error) {
	if u == nil || u.PromptTokens == nil || u.CompletionTokens == nil || u.TotalTokens == nil || *u.PromptTokens < 1 || *u.CompletionTokens < 0 || *u.CompletionTokens > int64(maxOutput) || *u.PromptTokens > r.model.MaxInputTokens || *u.TotalTokens != *u.PromptTokens+*u.CompletionTokens {
		return 0, errors.New("invalid supplier usage")
	}
	return r.Cost(*u.PromptTokens, *u.CompletionTokens)
}

// Used by DecodeConfig tests without writing credentials to disk.
func DecodeConfig(data []byte) (Config, error) {
	var c Config
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if len(data) > 256<<10 || d.Decode(&c) != nil || d.Decode(&struct{}{}) != io.EOF {
		return c, errors.New("invalid provider configuration")
	}
	return c, nil
}

func (r *Route) ReviewDeadline() time.Time {
	deadline := r.model.Residency.ExpiresAt
	if r.deployment.ExpiresAt.Before(deadline) {
		deadline = r.deployment.ExpiresAt
	}
	return deadline
}
func (c *Catalog) Ready() bool {
	if c == nil || len(c.routes) == 0 {
		return false
	}
	for _, r := range c.routes {
		if !r.Ready() {
			return false
		}
	}
	return true
}

func (r *Route) Provider() string { return r.profile.ID }
func (r *Route) Region() string   { return r.profile.Region }

// InferenceModel is the credential-safe OpenAI model directory projection.
type InferenceModel struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

// ExecutionModel exposes only the server-owned model mapping, never credentials.
func (r *Route) ExecutionModel() (alias, upstream string) { return r.model.ID, r.model.UpstreamModel }
