package control

import "time"

// EmbeddedCompletion is scoped accounting metadata, never raw supplier payloads.
// A receipt confirms posting, not client delivery or content-audit completeness.
type EmbeddedCompletion struct {
	Engine           string     `json:"engine"`
	Protocol         string     `json:"protocol"`
	State            string     `json:"state"`
	InputPerMillion  int64      `json:"input_per_million"`
	OutputPerMillion int64      `json:"output_per_million"`
	InputTokens      *int64     `json:"input_tokens"`
	OutputTokens     *int64     `json:"output_tokens"`
	Actual           *int64     `json:"actual"`
	RecordedAt       *time.Time `json:"recorded_at"`
	ReceivedAt       *time.Time `json:"received_at"`
}

// Prefer posted original quantities after recovery, retaining the pre-existing
// attempt semantics for requests without an embedded receipt. These one-to-one
// joins cannot multiply ledger rows or count both sources for the same request.
const settledCompletionJoins = ` LEFT JOIN engine_completion_receipts ec ON ec.attempt_id=a.attempt_id
 LEFT JOIN engine_completion_events eu ON eu.attempt_id=ec.attempt_id AND eu.usage_sha256=ec.usage_sha256 AND eu.reconciled `
const accountedInput = `COALESCE(eu.input_tokens,a.input_tokens)`
const accountedOutput = `COALESCE(eu.output_tokens,a.output_tokens)`
