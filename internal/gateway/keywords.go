package gateway

import (
	"context"
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
			if err != nil {
				break
			}
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
