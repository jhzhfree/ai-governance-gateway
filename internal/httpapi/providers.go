package httpapi

import (
	"errors"
	"github.com/jhzhfree/ai-governance-gateway/internal/control"
	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
	"net/http"
)

func (h *Handler) providerConfiguration(w http.ResponseWriter, r *http.Request) {
	v, err := h.Store.ProviderConfiguration(r.Context(), principal(r))
	if err != nil {
		failure(w, r, err)
		return
	}
	send(w, 200, v)
}
func (h *Handler) publishProviders(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ExpectedRevision *int64          `json:"expected_revision"`
		Configuration    provider.Config `json:"configuration"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.ExpectedRevision == nil {
		failure(w, r, control.ErrInvalid)
		return
	}
	err := h.Store.PublishProviders(r.Context(), principal(r), *body.ExpectedRevision, body.Configuration)
	if errors.Is(err, provider.ErrConfigurationInvalid) {
		err = control.ErrInvalid
	}

	if err != nil {
		failure(w, r, err)
		return
	}
	h.providerConfiguration(w, r)
}
