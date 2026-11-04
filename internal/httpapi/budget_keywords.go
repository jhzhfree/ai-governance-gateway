package httpapi

import (
	"github.com/jhzhfree/ai-governance-gateway/internal/control"
	"github.com/jhzhfree/ai-governance-gateway/internal/keyword"
	"net/http"
	"strconv"
	"time"
)

func (h *Handler) getBudget(w http.ResponseWriter, r *http.Request) {
	v, err := h.Store.Budget(r.Context(), principal(r), r.PathValue("org"), r.PathValue("project"))
	if err != nil {
		failure(w, r, err)
		return
	}
	send(w, 200, v)
}
func (h *Handler) setBudget(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Period   string `json:"period"`
		Expected *int64 `json:"expected_revision"`
		Limit    *int64 `json:"limit"`
		Reason   string `json:"reason"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Expected == nil || body.Limit == nil {
		failure(w, r, control.ErrInvalid)
		return
	}
	if err := h.Store.SetBudget(r.Context(), principal(r), r.PathValue("org"), r.PathValue("project"), body.Period, *body.Expected, *body.Limit, body.Reason); err != nil {
		failure(w, r, err)
		return
	}
	h.getBudget(w, r)
}
func cursorNumber(w http.ResponseWriter, r *http.Request) (int64, bool) {
	value := r.URL.Query().Get("after")
	if value == "" {
		return 0, true
	}
	v, err := strconv.ParseInt(value, 10, 64)
	if err != nil || v < 1 || v > control.MaxAPIAmount {
		failure(w, r, control.ErrInvalid)
		return 0, false
	}
	return v, true
}
func (h *Handler) budgetChanges(w http.ResponseWriter, r *http.Request) {
	after, ok := cursorNumber(w, r)
	if !ok {
		return
	}
	period := r.URL.Query().Get("period")
	if _, err := time.Parse("2006-01", period); err != nil {
		failure(w, r, control.ErrInvalid)
		return
	}
	items, err := h.Store.BudgetChanges(r.Context(), principal(r), r.PathValue("org"), r.PathValue("project"), period, after)
	if err != nil {
		failure(w, r, err)
		return
	}
	next := ""
	if len(items) > 100 {
		items = items[:100]
		next = strconv.FormatInt(items[99].Revision, 10)
	}
	send(w, 200, map[string]any{"data": items, "next_cursor": next})
}
func (h *Handler) budgetCycle(w http.ResponseWriter, r *http.Request) {
	v, err := h.Store.BudgetCycle(r.Context(), principal(r), r.PathValue("org"))
	if err != nil {
		failure(w, r, err)
		return
	}
	send(w, 200, v)
}
func (h *Handler) setBudgetCycle(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled  *bool  `json:"enabled"`
		Expected *int64 `json:"expected_revision"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Enabled == nil || body.Expected == nil {
		failure(w, r, control.ErrInvalid)
		return
	}
	if err := h.Store.SetBudgetCycle(r.Context(), principal(r), r.PathValue("org"), *body.Expected, *body.Enabled); err != nil {
		failure(w, r, err)
		return
	}
	h.budgetCycle(w, r)
}
func (h *Handler) rotateBudget(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Expected string `json:"expected_period"`
		Next     string `json:"next_period"`
	}
	if !decode(w, r, &body) {
		return
	}
	if err := h.Store.RotateBudgetPeriod(r.Context(), principal(r), r.PathValue("org"), body.Expected, body.Next); err != nil {
		failure(w, r, err)
		return
	}
	w.WriteHeader(204)
}
func (h *Handler) keywordPolicy(w http.ResponseWriter, r *http.Request) {
	v, err := h.Store.KeywordPolicy(r.Context(), principal(r), r.PathValue("org"), r.PathValue("project"))
	if err != nil {
		failure(w, r, err)
		return
	}
	send(w, 200, v)
}
func (h *Handler) setKeywordPolicy(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Rules    []keyword.Rule `json:"rules"`
		Expected *int64         `json:"expected_revision"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Expected == nil {
		failure(w, r, control.ErrInvalid)
		return
	}
	if err := h.Store.SetKeywordPolicy(r.Context(), principal(r), r.PathValue("org"), r.PathValue("project"), *body.Expected, body.Rules); err != nil {
		failure(w, r, err)
		return
	}
	h.keywordPolicy(w, r)
}
func (h *Handler) keywordMatches(w http.ResponseWriter, r *http.Request) {
	after, ok := cursorNumber(w, r)
	if !ok {
		return
	}
	items, err := h.Store.KeywordMatches(r.Context(), principal(r), r.PathValue("org"), r.PathValue("project"), after)
	if err != nil {
		failure(w, r, err)
		return
	}
	next := ""
	if len(items) > 100 {
		items = items[:100]
		next = strconv.FormatInt(items[99].ID, 10)
	}
	send(w, 200, map[string]any{"data": items, "next_cursor": next})
}
