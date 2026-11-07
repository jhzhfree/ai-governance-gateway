package newapibridge

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strconv"
)

// CheckChannelRevisions gates new inference only. Recovery of immutable old
// bindings must run independently even while current channel configuration drifts.
func (e *Executor) CheckChannelRevisions(ctx context.Context) error {
	return e.checkChannelRevisions(ctx, e.revisions)
}

func (e *Executor) checkChannelRevisions(ctx context.Context, revisions map[int]string) error {
	ids := make([]int, 0, len(revisions))
	for id := range revisions {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	for _, id := range ids {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.origin+"/internal/governance/v1/channels/"+strconv.Itoa(id)+"/revision", nil)
		if err != nil {
			return ErrUnavailable
		}
		req.Header.Set("Authorization", "Bearer "+e.token)
		res, err := e.recoveryClient.Do(req)
		if err != nil {
			return ErrUnavailable
		}
		raw, readErr := io.ReadAll(io.LimitReader(res.Body, 4097))
		res.Body.Close()
		if readErr != nil || len(raw) > 4096 || res.StatusCode != 200 || len(res.Header.Values("X-Aigw-Config-Version")) != 1 || res.Header.Get("X-Aigw-Config-Version") != "1" {
			return ErrConfigurationDrift
		}
		var value struct {
			ChannelID int    `json:"channel_id"`
			Revision  string `json:"revision"`
			Enabled   bool   `json:"enabled"`
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&value) != nil || decoder.Decode(&struct{}{}) != io.EOF || value.ChannelID != id || !value.Enabled || value.Revision != revisions[id] {
			return ErrConfigurationDrift
		}
	}
	return nil
}
