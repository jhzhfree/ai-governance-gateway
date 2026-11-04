package control

import (
	"context"
	"crypto/sha256"
	"errors"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
)

// KeyModels returns only the live key/project/configuration intersection.
func (s *Store) KeyModels(ctx context.Context, secret string) (items []provider.InferenceModel, err error) {
	items = []provider.InferenceModel{}
	if !strings.HasPrefix(secret, "aigw_pk_") || len(secret) > 256 {
		return nil, identity.ErrUnauthenticated
	}
	digest := sha256.Sum256([]byte(secret))
	var allowed []string
	var currency string
	err = s.pool.QueryRow(ctx, `SELECT o.currency,ARRAY(SELECT m.model FROM project_models m WHERE m.organization_id=k.organization_id AND m.project_id=k.project_id AND m.model=ANY(k.models) ORDER BY m.model) FROM project_keys k JOIN organizations o ON o.id=k.organization_id WHERE k.digest=$1 AND k.revoked_at IS NULL AND k.expires_at>clock_timestamp()`, digest[:]).Scan(&currency, &allowed)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, identity.ErrUnauthenticated
	}
	if err != nil {
		return nil, err
	}
	enabled := map[string]bool{}
	for _, model := range allowed {
		enabled[model] = true
	}
	for _, model := range s.Models() {
		if enabled[model.ID] && model.Currency == currency {
			items = append(items, provider.InferenceModel{ID: model.ID, Object: "model", Created: 0, OwnedBy: model.Provider})
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return
}
