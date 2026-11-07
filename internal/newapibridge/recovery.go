package newapibridge

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
)

// ReconcileOnce acknowledges only after committing an idempotent receipt and
// ledger update together. A poison event is retained; unrelated events proceed.
func (e *Executor) ReconcileOnce(ctx context.Context) error {
	if e.journal == nil {
		return ErrInvalid
	}

	stale, err := e.journal.Stale(ctx, e.origin)
	if err != nil {
		return ErrUnavailable
	}
	failed := false
	for _, item := range stale {
		raw, _ := json.Marshal(map[string]string{"request_id": item.RequestID, "fingerprint": item.Fingerprint})
		request, err := http.NewRequestWithContext(ctx, "POST", e.origin+"/internal/governance/v1/attempts/"+item.AttemptID+"/cancel", bytes.NewReader(raw))
		if err != nil {
			failed = true
			continue
		}
		request.Header.Set("Authorization", "Bearer "+e.token)
		request.Header.Set("Content-Type", "application/json")
		response, err := e.client.Do(request)
		if err != nil {
			failed = true
			continue
		}
		response.Body.Close()
		// Conflict means dispatched/completed; absence of a completion event is not
		// refund evidence. Only the subsequent durable cancelled event settles zero.
		if response.StatusCode != 204 && response.StatusCode != 409 {
			failed = true
		}
	}
	req, err := http.NewRequestWithContext(ctx, "GET", e.origin+"/internal/governance/v1/events", nil)
	if err != nil {
		return ErrInvalid
	}
	req.Header.Set("Authorization", "Bearer "+e.token)
	res, err := e.client.Do(req)
	if err != nil {
		return ErrUnavailable
	}
	defer res.Body.Close()
	if res.StatusCode != 200 || len(res.Header.Values("X-Aigw-Events-Version")) != 1 || res.Header.Get("X-Aigw-Events-Version") != "1" {
		return ErrUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, (256<<10)+1))
	if err != nil || len(data) > 256<<10 {
		return ErrUnavailable
	}
	var events []Event
	if json.Unmarshal(data, &events) != nil || len(events) > 100 {
		return ErrInvalid
	}
	for _, event := range events {
		if err := e.journal.Apply(ctx, e.origin, event); err != nil {
			failed = true
			continue
		}
		ack, err := http.NewRequestWithContext(ctx, "POST", e.origin+"/internal/governance/v1/events/"+event.AttemptID+"/ack", nil)
		if err != nil {
			failed = true
			continue
		}
		ack.Header.Set("Authorization", "Bearer "+e.token)
		result, err := e.client.Do(ack)
		if err != nil {
			failed = true
			continue
		}
		result.Body.Close()
		if result.StatusCode != 204 {
			failed = true
		}
	}
	if failed {
		return ErrUnavailable
	}
	return nil
}
