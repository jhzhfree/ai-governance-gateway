package httpapi

import (
	"github.com/jhzhfree/ai-governance-gateway/internal/control"
	"net/http"
	"strconv"
)

func (h *Handler) approvalPolicy(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		v, err := h.Store.BudgetApprovalPolicy(r.Context(), principal(r), r.PathValue("org"))
		if err != nil {
			failure(w, r, err)
			return
		}
		send(w, 200, v)
		return
	}
	var body struct {
		Enabled  *bool  `json:"enabled"`
		Revision *int64 `json:"expected_revision"`
		Reason   string `json:"reason"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Enabled == nil || body.Revision == nil {
		failure(w, r, control.ErrInvalid)
		return
	}
	v, err := h.Store.SetBudgetApprovalPolicy(r.Context(), principal(r), r.PathValue("org"), *body.Revision, *body.Enabled, body.Reason)
	if err != nil {
		failure(w, r, err)
		return
	}
	send(w, 200, v)
}
func (h *Handler) budgetApprovals(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		after, _, ok := page(w, r)
		if !ok {
			return
		}
		items, err := h.Store.BudgetApprovals(r.Context(), principal(r), r.PathValue("org"), r.PathValue("project"), after)
		if err != nil {
			failure(w, r, err)
			return
		}
		list(w, items, 50, func(v control.BudgetApproval) string { return v.ID })
		return
	}
	var body struct {
		Period   string `json:"period"`
		Revision *int64 `json:"expected_revision"`
		Limit    *int64 `json:"limit"`
		Reason   string `json:"reason"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Revision == nil || body.Limit == nil {
		failure(w, r, control.ErrInvalid)
		return
	}
	v, err := h.Store.RequestBudgetApproval(r.Context(), principal(r), r.PathValue("org"), r.PathValue("project"), body.Period, *body.Revision, *body.Limit, body.Reason)
	if err != nil {
		failure(w, r, err)
		return
	}
	send(w, 201, v)
}
func (h *Handler) decideBudgetApproval(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Reason string `json:"reason"`
	}
	if !decode(w, r, &body) {
		return
	}
	v, err := h.Store.DecideBudgetApproval(r.Context(), principal(r), r.PathValue("org"), r.PathValue("project"), r.PathValue("approval"), r.PathValue("action"), body.Reason)
	if err != nil {
		failure(w, r, err)
		return
	}
	send(w, 200, v)
}

func (h *Handler) approvalPolicyChanges(w http.ResponseWriter, r *http.Request) {
	after, ok := cursorNumber(w, r)
	if !ok {
		return
	}
	items, err := h.Store.BudgetApprovalPolicyChanges(r.Context(), principal(r), r.PathValue("org"), after)
	if err != nil {
		failure(w, r, err)
		return
	}
	list(w, items, 50, func(v control.BudgetApprovalPolicyChange) string { return strconv.FormatInt(v.Revision, 10) })
}
