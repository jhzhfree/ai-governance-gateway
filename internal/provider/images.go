package provider

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"strings"
)

type ContentPart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *ImageURL `json:"image_url,omitempty"`
}
type ImageURL struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}
type ImageMetadata struct {
	Message int    `json:"message"`
	Part    int    `json:"part"`
	MIME    string `json:"mime"`
	SHA256  string `json:"sha256"`
	Bytes   int    `json:"bytes"`
	Width   int    `json:"width"`
	Height  int    `json:"height"`
}

// The string field remains convenient for text-only callers. Custom decoding
// preserves content arrays and keeps unknown-field rejection at this boundary.
func (m *Message) UnmarshalJSON(raw []byte) error {
	var wire struct {
		Role       string          `json:"role"`
		Content    json.RawMessage `json:"content"`
		ToolCalls  []ToolCall      `json:"tool_calls,omitempty"`
		ToolCallID string          `json:"tool_call_id,omitempty"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return err
	}
	*m = Message{Role: wire.Role, ToolCalls: wire.ToolCalls, ToolCallID: wire.ToolCallID}
	if len(wire.Content) == 0 || bytes.Equal(wire.Content, []byte("null")) {
		return nil
	}
	if len(bytes.TrimSpace(wire.Content)) > 0 && bytes.TrimSpace(wire.Content)[0] == '[' {
		decoder = json.NewDecoder(bytes.NewReader(wire.Content))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&m.Parts); err != nil {
			return err
		}
		if len(m.Parts) == 0 {
			return errors.New("empty content array")
		}
		return nil
	}
	return json.Unmarshal(wire.Content, &m.Content)
}
func (m Message) MarshalJSON() ([]byte, error) {
	var content any = m.Content
	if m.Parts != nil {
		content = m.Parts
	}
	return json.Marshal(struct {
		Role       string     `json:"role"`
		Content    any        `json:"content"`
		ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
		ToolCallID string     `json:"tool_call_id,omitempty"`
	}{m.Role, content, m.ToolCalls, m.ToolCallID})
}
func (r Request) HasParts() bool {
	for _, m := range r.Messages {
		if m.Parts != nil {
			return true
		}
	}
	return false
}
func (r Request) HasImages() bool {
	for _, m := range r.Messages {
		for _, p := range m.Parts {
			if p.Type == "image_url" {
				return true
			}
		}
	}
	return false
}
func (r *Route) AllowsImages() bool { return r != nil && r.model.AllowImages }

// No URL fetching, HTML/SVG decoding or full pixel allocation occurs here.
// Header dimensions are checked; provider-side decoding can still reject an image.
func (r Request) ImageMetadata() ([]ImageMetadata, error) {
	invalid := errors.New("unsupported or oversized inline image input")
	result := []ImageMetadata{}
	totalBytes, totalPixels := 0, int64(0)
	for mi, m := range r.Messages {
		if m.Parts == nil {
			continue
		}
		if m.Role != "user" || m.Content != "" || len(m.Parts) < 1 || len(m.Parts) > 16 || len(m.ToolCalls) > 0 || m.ToolCallID != "" {
			return nil, invalid
		}
		for pi, part := range m.Parts {
			switch part.Type {
			case "text":
				if part.Text == "" || len(part.Text) > 16<<10 || part.ImageURL != nil {
					return nil, invalid
				}
			case "image_url":
				if part.Text != "" || part.ImageURL == nil || len(result) >= 4 {
					return nil, invalid
				}
				source := part.ImageURL
				if source.Detail != "" && source.Detail != "auto" && source.Detail != "low" && source.Detail != "high" {
					return nil, invalid
				}
				header, encoded, ok := strings.Cut(source.URL, ",")
				mime := ""
				switch header {
				case "data:image/png;base64":
					mime = "image/png"
				case "data:image/jpeg;base64":
					mime = "image/jpeg"
				default:
					return nil, invalid
				}
				if !ok || len(encoded) > base64.StdEncoding.EncodedLen(256<<10) || strings.ContainsAny(encoded, "\r\n\t ") {
					return nil, invalid
				}
				raw, err := base64.StdEncoding.Strict().DecodeString(encoded)
				if err != nil || len(raw) == 0 || len(raw) > 256<<10 {
					return nil, invalid
				}
				cfg, format, err := image.DecodeConfig(bytes.NewReader(raw))
				if err != nil || "image/"+format != mime || cfg.Width < 1 || cfg.Height < 1 || cfg.Width > 4096 || cfg.Height > 4096 {
					return nil, invalid
				}
				totalBytes += len(raw)
				totalPixels += int64(cfg.Width) * int64(cfg.Height)
				if totalBytes > 512<<10 || totalPixels > 16*1024*1024 {
					return nil, invalid
				}
				sum := sha256.Sum256(raw)
				result = append(result, ImageMetadata{mi, pi, mime, hex.EncodeToString(sum[:]), len(raw), cfg.Width, cfg.Height})
			default:
				return nil, invalid
			}
		}
	}
	return result, nil
}
