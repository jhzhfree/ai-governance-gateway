package bifrost

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/jhzhfree/ai-governance-gateway/modules/engine/execution"
	"github.com/maximhq/bifrost/core/network"
)

// Streaming is received directly, before any SDK parser/aggregator. The SDK's
// optional raw capture may drop malformed/oversized frames and cannot establish
// completeness. This is one physical request, never a fallback from an SDK call.
func streamClient(testLoopback bool) *http.Client {
	client := network.NewStrictSSRFSafeHTTPClient(120 * time.Second)
	if testLoopback {
		client = network.NewPrivateNetworkHTTPClient(120 * time.Second)
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return client
}

func (e *Executor) stream(ctx context.Context, req execution.Request, maxOutput int, emit execution.Emit) error {
	parent := ctx
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	var body map[string]json.RawMessage
	if json.Unmarshal(req.Body, &body) != nil {
		return execution.ErrInvalidRequest
	}
	body["stream_options"] = json.RawMessage(`{"include_usage":true}`)
	payload, err := json.Marshal(body)
	if err != nil {
		return execution.ErrInvalidRequest
	}
	upstream, err := http.NewRequestWithContext(ctx, http.MethodPost, e.streamURL, bytes.NewReader(payload))
	if err != nil {
		return execution.ErrInvalidRequest
	}
	upstream.Header.Set("Authorization", "Bearer "+e.streamKey)
	upstream.Header.Set("Content-Type", "application/json")
	upstream.Header.Set("Accept", "text/event-stream")
	upstream.Header.Set("Accept-Encoding", "identity")
	// Bound the wait for initial headers as well as each subsequent body read.
	timer := time.AfterFunc(10*time.Second, func() { cancel(context.DeadlineExceeded) })
	response, err := e.streamHTTP.Do(upstream)
	timer.Stop()
	if err != nil {
		return streamError(parent, ctx, execution.ErrUpstream)
	}
	defer response.Body.Close()
	media, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	encoding := response.Header.Get("Content-Encoding")
	if err != nil || response.StatusCode != http.StatusOK || media != "text/event-stream" || (encoding != "" && encoding != "identity") {
		return execution.ErrUpstream
	}
	read := &idleReader{Reader: response.Body, cancel: cancel}
	err = receiveStream(ctx, read, req, maxOutput, emit)
	if err != nil {
		return streamError(parent, ctx, err)
	}
	return nil
}
func streamError(parent, ctx context.Context, err error) error {
	if parent.Err() != nil {
		return parent.Err()
	}
	if errors.Is(context.Cause(ctx), context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	// Consumer errors retain identity, but transport errors are sanitized at source.
	return err
}

type idleReader struct {
	io.Reader
	cancel context.CancelCauseFunc
}

func (r *idleReader) Read(p []byte) (int, error) {
	timer := time.AfterFunc(3*time.Second, func() { r.cancel(context.DeadlineExceeded) })
	n, err := r.Reader.Read(p)
	timer.Stop()
	if err != nil && err != io.EOF {
		err = execution.ErrUpstream
	}
	return n, err
}

const maxStreamFrame = 64 << 10
const maxStreamBytes = 16 << 20

// Every byte, including comments and unknown fields, counts against limits.
// A terminal event is accepted only after a real finish_reason. EOF is never DONE.
func receiveStream(ctx context.Context, body io.Reader, req execution.Request, maxOutput int, emit execution.Emit) error {
	reader := bufio.NewReaderSize(body, 4096)
	evidence := &usageEvidence{stream: true}
	var terminalUsage *execution.Event
	var data []byte
	frameBytes, totalBytes := 0, 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		// ReadSlice bounds allocation before a delimiter; long lines are accumulated
		// only up to the frame cap, unlike Scanner limits applied after allocation.
		var line []byte
		for {
			part, err := reader.ReadSlice('\n')
			frameBytes += len(part)
			totalBytes += len(part)
			if frameBytes > maxStreamFrame || totalBytes > maxStreamBytes {
				return execution.ErrUpstream
			}
			line = append(line, part...)
			if err == bufio.ErrBufferFull {
				continue
			}
			if err != nil {
				return execution.ErrUpstream
			}
			break
		}
		line = bytes.TrimSuffix(line, []byte{'\n'})
		line = bytes.TrimSuffix(line, []byte{'\r'})
		if !utf8.Valid(line) {
			return execution.ErrUpstream
		}
		if len(line) == 0 {
			frameBytes = 0
			if len(data) == 0 {
				continue
			}
			data = bytes.TrimSuffix(data, []byte{'\n'})
			if bytes.Equal(data, []byte("[DONE]")) {
				if !evidence.finished {
					return execution.ErrUpstream
				}
				if terminalUsage != nil {
					return emit(ctx, *terminalUsage)
				}
				return nil
			}
			if err := validateStreamFrame(data, evidence.finished); err != nil {
				return err
			}
			previous := evidence.usage
			if err := evidence.observe(json.RawMessage(data), maxOutput); err != nil {
				return err
			}
			var usage *execution.Usage
			if evidence.usage != previous {
				usage = evidence.usage
			}
			// Copy because consumers may retain payload after returning.
			event := execution.Event{UpstreamStatus: 200, RequestID: req.RequestID, AttemptID: req.AttemptID, Payload: append(json.RawMessage(nil), data...), Usage: usage}
			if usage != nil {
				terminalUsage = &event
				data = nil
				continue
			}
			if err := emit(ctx, event); err != nil {
				return err
			}
			data = nil
			continue
		}
		if line[0] == ':' {
			continue
		}
		field, value, found := bytes.Cut(line, []byte{':'})
		if !found {
			value = nil
		}
		value = bytes.TrimPrefix(value, []byte{' '})
		switch string(field) {
		case "data":
			data = append(data, value...)
			data = append(data, '\n')
		case "event":
			if string(value) != "message" {
				return execution.ErrUpstream
			}
		default:
			return execution.ErrUpstream
		}
	}
}

func validateStreamFrame(data []byte, finished bool) error {
	v, ok := object(data, "id", "object", "created", "model", "choices", "usage", "system_fingerprint", "service_tier")
	if !ok {
		return execution.ErrUpstream
	}
	var choices []json.RawMessage
	if json.Unmarshal(v["choices"], &choices) != nil || choices == nil || len(choices) > 1 || (finished && len(choices) > 0) {
		return execution.ErrUpstream
	}
	if len(choices) == 0 && (len(v["usage"]) == 0 || bytes.Equal(bytes.TrimSpace(v["usage"]), []byte("null"))) {
		return execution.ErrUpstream
	}
	for _, raw := range choices {
		choice, ok := object(raw, "index", "delta", "finish_reason", "logprobs")
		if !ok {
			return execution.ErrUpstream
		}
		var index *int
		if json.Unmarshal(choice["index"], &index) != nil || index == nil || *index != 0 {
			return execution.ErrUpstream
		}
		if _, ok := object(choice["delta"], "role", "content", "tool_calls", "refusal", "annotations"); !ok {
			return execution.ErrUpstream
		}
		if raw := choice["finish_reason"]; len(raw) > 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			reason, ok := text(raw)
			if !ok || (reason != "stop" && reason != "length" && reason != "content_filter" && reason != "tool_calls") {
				return execution.ErrUpstream
			}
		}
	}
	return nil
}
