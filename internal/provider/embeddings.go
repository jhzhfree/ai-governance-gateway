package provider

import (
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strings"
)

// EmbeddingRequest deliberately excludes token-ID inputs, remote media and
// base64 vectors. These need separate audit and measurement semantics.
type EmbeddingRequest struct {
	Model          string          `json:"model"`
	Input          json.RawMessage `json:"input"`
	EncodingFormat string          `json:"encoding_format,omitempty"`
	Dimensions     *int            `json:"dimensions,omitempty"`
}

func (r EmbeddingRequest) Request() (Request, error) {
	var inputs []string
	var single string
	if json.Unmarshal(r.Input, &single) == nil {
		inputs = []string{single}
	} else if json.Unmarshal(r.Input, &inputs) != nil {
		return Request{}, errors.New("invalid embedding input")
	}
	req := Request{API: "embeddings", Model: r.Model, Input: inputs, EncodingFormat: r.EncodingFormat, Dimensions: r.Dimensions}
	if !validEmbeddingRequest(req) {
		return Request{}, errors.New("invalid embedding input")
	}
	return req, nil
}
func (r Request) IsEmbedding() bool { return r.API == "embeddings" }
func validEmbeddingRequest(r Request) bool {
	if !r.IsEmbedding() || r.Stream || r.MaxTokens != 0 || len(r.Messages) > 0 || r.HasTools() || len(r.Input) < 1 || len(r.Input) > 16 || (r.EncodingFormat != "" && r.EncodingFormat != "float") || (r.Dimensions != nil && (*r.Dimensions < 1 || *r.Dimensions > 4096)) {
		return false
	}
	total := 0
	for _, input := range r.Input {
		if strings.TrimSpace(input) == "" || len(input) > 16<<10 {
			return false
		}
		total += len(input)
	}
	return total <= 64<<10
}
func (r *Route) API() string {
	if r.UsesNativeMessages() {
		return "messages"
	}
	if r.model.API == "embeddings" {
		return "embeddings"
	}
	return "chat"
}

type Embedding struct {
	Object string    `json:"object"`
	Index  int       `json:"index"`
	Vector []float64 `json:"embedding"`
}
type EmbeddingUsage struct {
	PromptTokens int64 `json:"prompt_tokens"`
	TotalTokens  int64 `json:"total_tokens"`
}
type EmbeddingResponse struct {
	Object string         `json:"object"`
	Model  string         `json:"model"`
	Data   []Embedding    `json:"data"`
	Usage  EmbeddingUsage `json:"usage"`
}

func NormalizeEmbedding(raw []byte, req Request) (EmbeddingResponse, *Usage, error) {
	invalid := errors.New("invalid embedding response")
	var value EmbeddingResponse
	var envelope struct {
		Data []struct {
			Index *int `json:"index"`
		} `json:"data"`
		Error json.RawMessage   `json:"error"`
		Usage map[string]*int64 `json:"usage"`
	}
	if len(raw) > 1<<20 || json.Unmarshal(raw, &value) != nil || json.Unmarshal(raw, &envelope) != nil || len(envelope.Error) > 0 || value.Model == "" || value.Object != "list" || len(value.Data) != len(req.Input) {
		return value, nil, invalid
	}
	for key, v := range envelope.Usage {
		if v == nil || (key != "prompt_tokens" && key != "total_tokens" && key != "completion_tokens") {
			return value, nil, invalid
		}
	}
	prompt, total := envelope.Usage["prompt_tokens"], envelope.Usage["total_tokens"]
	if prompt == nil || total == nil || *prompt < 1 || *total != *prompt || (envelope.Usage["completion_tokens"] != nil && *envelope.Usage["completion_tokens"] != 0) {
		return value, nil, invalid
	}
	for _, item := range envelope.Data {
		if item.Index == nil {
			return value, nil, invalid
		}
	}
	seen := map[int]bool{}
	dimensions := 0
	for _, item := range value.Data {
		if item.Object != "embedding" || item.Index < 0 || item.Index >= len(req.Input) || seen[item.Index] || len(item.Vector) < 1 || len(item.Vector) > 4096 {
			return value, nil, invalid
		}
		seen[item.Index] = true
		if dimensions == 0 {
			dimensions = len(item.Vector)
		}
		if len(item.Vector) != dimensions || (req.Dimensions != nil && dimensions != *req.Dimensions) {
			return value, nil, invalid
		}
		for _, v := range item.Vector {
			if math.IsNaN(v) || math.IsInf(v, 0) {
				return value, nil, invalid
			}
		}
	}
	sort.Slice(value.Data, func(i, j int) bool { return value.Data[i].Index < value.Data[j].Index })
	value.Model = req.Model
	zero := int64(0)
	return value, &Usage{PromptTokens: prompt, CompletionTokens: &zero, TotalTokens: total}, nil
}
