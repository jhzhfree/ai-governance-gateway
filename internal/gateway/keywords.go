package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jhzhfree/ai-governance-gateway/internal/budget"
	"github.com/jhzhfree/ai-governance-gateway/internal/keyword"
	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
)

func (h *Handler) beginKeywords(w http.ResponseWriter, r *http.Request, id string, scope budget.Scope, req Request) (*keyword.Session, bool) {
	if h.Keywords == nil {
		return nil, true
	}
	session, err := h.Keywords.Begin(r.Context(), scope.Organization, scope.Project, id, req.Stream)
	if err == nil {
		for _, m := range req.Messages {
			_, err = session.Process(r.Context(), "input", m.Content)
			for _, part := range m.Parts {
				if err == nil && part.Type == "text" {
					_, err = session.Process(r.Context(), "input", part.Text)
				}
			}
			if err == nil && len(m.ToolCalls) > 0 {
				raw, _ := json.Marshal(m.ToolCalls)
				_, err = keywordJSON(r.Context(), session, "input", raw)
				for _, call := range m.ToolCalls {
					if err == nil {
						_, err = keywordJSON(r.Context(), session, "input", []byte(call.Function.Arguments))
					}
				}
			}
			if err != nil {
				break
			}
		}
		if err == nil && len(req.Tools) > 0 {
			raw, _ := json.Marshal(req.Tools)
			_, err = keywordJSON(r.Context(), session, "input", raw)
		}
	}
	if err != nil {
		status, code := keywordProblem(err)
		problem(w, status, code, id)
		return nil, false
	}
	return session, true
}
func keywordProblem(err error) (int, string) {
	if errors.Is(err, keyword.ErrBlocked) {
		return 403, "keyword_blocked"
	}
	if errors.Is(err, keyword.ErrStreamPolicy) {
		return 400, "stream_keyword_policy_unsupported"
	}
	return 503, "keyword_unavailable"
}
func keywordResponse(ctx context.Context, session *keyword.Session, response *provider.Response, stream bool) error {
	if session == nil {
		return nil
	}
	for i := range response.Choices {
		choice := &response.Choices[i]
		delta := choice.Message
		if stream {
			delta = choice.Delta
		}
		if delta == nil {
			continue
		}
		for j := range delta.ToolCalls {
			call := &delta.ToolCalls[j]
			name, err := session.Process(ctx, "output", call.Function.Name)
			if err != nil {
				return err
			}
			if name != call.Function.Name {
				return keyword.ErrBlocked
			}
			arguments, err := keywordJSON(ctx, session, "output", []byte(call.Function.Arguments))
			if err != nil {
				return err
			}
			call.Function.Arguments = string(arguments)
		}
		if stream {
			if delta.Content != nil {
				if err := session.Stream(ctx, "content", *delta.Content); err != nil {
					return err
				}
			}
			if err := session.Stream(ctx, "reasoning", delta.ReasoningContent); err != nil {
				return err
			}
		} else {
			if delta.Content != nil {
				value, err := session.Process(ctx, "output", *delta.Content)
				if err != nil {
					return err
				}
				delta.Content = &value
			}
			if delta.ReasoningContent != "" {
				value, err := session.Process(ctx, "output", delta.ReasoningContent)
				if err != nil {
					return err
				}
				delta.ReasoningContent = value
			}
		}
	}
	return nil
}
func keywordFinishContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 3*time.Second)
}

// Inspect decoded JSON strings so escaped tool arguments cannot bypass text rules.
// Redaction changes string values, never function names or object keys.
func keywordJSON(ctx context.Context, session *keyword.Session, direction string, raw []byte) ([]byte, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return nil, keyword.ErrBlocked
	}
	var visit func(any, int) (any, error)
	visit = func(v any, depth int) (any, error) {
		if depth > 32 {
			return nil, keyword.ErrBlocked
		}
		switch x := v.(type) {
		case string:
			out, err := session.Process(ctx, direction, x)
			if err != nil {
				return nil, err
			}
			if direction == "input" && out != x {
				return nil, keyword.ErrBlocked
			}
			return out, nil
		case []any:
			for i, child := range x {
				out, err := visit(child, depth+1)
				if err != nil {
					return nil, err
				}
				x[i] = out
			}
		case map[string]any:
			for key, child := range x {
				out, err := session.Process(ctx, direction, key)
				if err != nil {
					return nil, err
				}
				if out != key {
					return nil, keyword.ErrBlocked
				}
				next, err := visit(child, depth+1)
				if err != nil {
					return nil, err
				}
				x[key] = next
			}
		}
		return v, nil
	}
	result, err := visit(value, 0)
	if err != nil {
		return nil, err
	}
	return json.Marshal(result)
}
