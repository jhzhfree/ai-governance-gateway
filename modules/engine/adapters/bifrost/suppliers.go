package bifrost

import (
	"encoding/json"
	"errors"
	"net/url"
	"strings"
)

// These mirror the governance catalog's fixed, reviewed service entries. Tests
// compare wire paths and usage options with the governance catalog.
// A profile describes wire behavior, not evidence of real-account compatibility.
type supplierProfile struct {
	id, origin, path string
	includeUsage     bool
}

func supplier(id string) (supplierProfile, bool) {
	switch id {
	case "":
		return supplierProfile{path: "/v1/chat/completions", includeUsage: true}, true
	case "deepseek":
		return supplierProfile{id: id, origin: "https://api.deepseek.com", path: "/chat/completions", includeUsage: true}, true
	case "aliyun":
		return supplierProfile{id: id, origin: "https://dashscope.aliyuncs.com", path: "/compatible-mode/v1/chat/completions", includeUsage: true}, true
	case "volcengine":
		return supplierProfile{id: id, origin: "https://ark.cn-beijing.volces.com", path: "/api/v3/chat/completions", includeUsage: true}, true
	case "zhipu":
		return supplierProfile{id: id, origin: "https://open.bigmodel.cn", path: "/api/paas/v4/chat/completions"}, true
	default:
		return supplierProfile{}, false
	}
}
func resolveSupplier(cfg Config) (Config, supplierProfile, error) {
	p, ok := supplier(cfg.Supplier)
	if !ok {
		return cfg, p, errors.New("unsupported supplier profile")
	}
	if p.id == "" {
		return cfg, p, nil
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = p.origin
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	if cfg.TestLoopback {
		u, err := url.Parse(cfg.BaseURL)
		// New additionally requires a literal loopback address and safe URL fields.
		if err != nil || u.Path != "" {
			return cfg, p, errors.New("supplier fixture requires an origin")
		}
	} else if cfg.BaseURL != p.origin {
		return cfg, p, errors.New("supplier endpoint override refused")
	}
	return cfg, p, nil
}
func (p supplierProfile) parameters() map[string]any {
	switch p.id {
	case "aliyun":
		return map[string]any{"enable_thinking": false}
	case "volcengine", "zhipu", "deepseek":
		return map[string]any{"thinking": map[string]any{"type": "disabled"}}
	default:
		return nil
	}
}

// Configuration contains credentials; ordinary formatting must not disclose them.
func (Config) String() string   { return "[execution configuration; credentials redacted]" }
func (Config) GoString() string { return "[execution configuration; credentials redacted]" }

// wireBody is used only after validateRequest; routing and secrets are never
// body fields. Both transports use this to preserve the authorized token limit.
func (p supplierProfile) wireBody(raw json.RawMessage, stream bool) ([]byte, error) {
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, err
	}
	if stream && p.includeUsage {
		body["stream_options"] = json.RawMessage(`{"include_usage":true}`)
	}
	for name, value := range p.parameters() {
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		body[name] = encoded
	}
	return json.Marshal(body)
}
