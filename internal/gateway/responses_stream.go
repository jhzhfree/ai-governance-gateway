package gateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jhzhfree/ai-governance-gateway/internal/provider"
)

// responsesStreamWriter translates only normalized, governed Chat frames. The
// caller retains admission, original evidence, billing and completion authority.
type responsesStreamWriter struct {
	http.ResponseWriter
	request       provider.Request
	id            string
	created       int64
	sequence      int
	started, done bool
	text          strings.Builder
	reason        *string
	usage         *provider.Usage
}

func (w *responsesStreamWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *responsesStreamWriter) frame(kind string, fields map[string]any) ([]byte, error) {
	fields["type"] = kind
	fields["sequence_number"] = w.sequence
	data, err := json.Marshal(fields)
	if err != nil || len(data) > 2<<20 {
		return nil, errors.New("responses event exceeds boundary")
	}
	return []byte("event: " + kind + "\ndata: " + string(data) + "\n\n"), nil
}
func (w *responsesStreamWriter) emit(kind string, fields map[string]any) error {
	data, err := w.frame(kind, fields)
	if err != nil {
		return err
	}
	if n, writeErr := w.ResponseWriter.Write(data); writeErr != nil {
		return writeErr
	} else if n != len(data) {
		return io.ErrShortWrite
	}
	w.sequence++
	return nil
}

type responsesEvent struct {
	kind   string
	fields map[string]any
}

// Initial metadata exists together before the first text delta. Persist and
// write it once, without delaying deltas or combining the sealed terminal event.
func (w *responsesStreamWriter) emitInitial(events []responsesEvent) error {
	if len(events) != 4 || w.started || w.sequence != 0 {
		return errors.New("invalid responses initialization")
	}
	var batch []byte
	for _, event := range events {
		frame, err := w.frame(event.kind, event.fields)
		if err != nil || len(batch)+len(frame) > 64<<10 {
			w.sequence = 0
			return errors.New("responses initialization exceeds boundary")
		}
		batch = append(batch, frame...)
		w.sequence++
	}
	n, err := w.ResponseWriter.Write(batch)
	if err != nil || n != len(batch) {
		w.sequence = 0
		if err != nil {
			return err
		}
		return io.ErrShortWrite
	}
	return nil
}

func (w *responsesStreamWriter) Write(data []byte) (int, error) {
	if !strings.HasPrefix(w.Header().Get("Content-Type"), "text/event-stream") {
		return w.ResponseWriter.Write(data)
	}
	if w.done {
		return 0, errors.New("responses stream already terminated")
	}
	if !bytes.HasPrefix(data, []byte("data: ")) || !bytes.HasSuffix(data, []byte("\n\n")) {
		return 0, errors.New("invalid normalized stream frame")
	}
	raw := bytes.TrimSuffix(bytes.TrimPrefix(data, []byte("data: ")), []byte("\n\n"))
	if bytes.Equal(raw, []byte("[DONE]")) {
		value := provider.Response{ID: "chatcmpl-" + w.id, Model: w.request.Model, Usage: w.usage, Choices: []provider.Choice{{Message: &provider.Delta{Role: "assistant", Content: new(w.text.String())}, FinishReason: w.reason}}}
		result, err := provider.PublicResponses(value, w.request)
		if err != nil {
			return 0, err
		}
		result.CreatedAt = w.created
		item := result.Output[0]
		for _, event := range []struct {
			kind   string
			fields map[string]any
		}{
			{"response.output_text.done", map[string]any{"item_id": item.ID, "output_index": 0, "content_index": 0, "text": w.text.String(), "logprobs": []any{}}},
			{"response.content_part.done", map[string]any{"item_id": item.ID, "output_index": 0, "content_index": 0, "part": item.Content[0]}},
			{"response.output_item.done", map[string]any{"output_index": 0, "item": item}},
		} {
			if err := w.emit(event.kind, event.fields); err != nil {
				return 0, err
			}
		}
		kind := "response.completed"
		if result.Status == "incomplete" {
			kind = "response.incomplete"
		}
		terminal, err := w.frame(kind, map[string]any{"response": result})
		if err != nil {
			return 0, err
		}
		failure, err := w.frame("error", map[string]any{"code": "content_audit_unavailable", "message": "content_audit_unavailable", "param": nil})
		if err != nil {
			return 0, err
		}
		var written int
		if audit, ok := w.ResponseWriter.(interface {
			writeTerminal([]byte, []byte) (int, error)
		}); ok {
			written, err = audit.writeTerminal(terminal, failure)
		} else {
			written, err = w.ResponseWriter.Write(terminal)
		}
		if err != nil {
			return 0, err
		}
		if written != len(terminal) {
			return 0, io.ErrShortWrite
		}
		w.sequence++
		w.done = true
		return len(data), nil
	}
	var value provider.Response
	if json.Unmarshal(raw, &value) != nil {
		return 0, errors.New("invalid normalized event")
	}
	if len(value.Error) > 0 {
		var failure struct {
			Code string `json:"code"`
		}
		if json.Unmarshal(value.Error, &failure) != nil || failure.Code == "" {
			return 0, errors.New("invalid stream failure")
		}
		if err := w.emit("error", map[string]any{"code": failure.Code, "message": failure.Code, "param": nil}); err != nil {
			return 0, err
		}
		w.done = true
		return len(data), nil
	}
	if !w.started {
		w.created = time.Now().Unix()
		initial := map[string]any{"id": "resp_" + w.id, "object": "response", "created_at": w.created, "status": "in_progress", "error": nil, "incomplete_details": nil, "model": w.request.Model, "store": false, "background": false, "max_output_tokens": w.request.MaxTokens, "output": []any{}, "usage": nil}
		if err := w.emitInitial([]responsesEvent{
			{"response.created", map[string]any{"response": initial}},
			{"response.in_progress", map[string]any{"response": initial}},
			{"response.output_item.added", map[string]any{"output_index": 0, "item": map[string]any{"id": "msg_" + w.id, "type": "message", "role": "assistant", "status": "in_progress", "content": []any{}}}},
			{"response.content_part.added", map[string]any{"item_id": "msg_" + w.id, "output_index": 0, "content_index": 0, "part": provider.ResponsesText{Type: "output_text", Text: "", Annotations: []any{}}}},
		}); err != nil {
			return 0, err
		}
		w.started = true
	}
	for _, choice := range value.Choices {
		if choice.FinishReason != nil {
			w.reason = choice.FinishReason
		}
		if choice.Delta != nil && choice.Delta.Content != nil {
			delta := *choice.Delta.Content
			if w.text.Len()+len(delta) > 1<<20 {
				return 0, errors.New("responses text exceeds boundary")
			}
			w.text.WriteString(delta)
			if delta != "" {
				if err := w.emit("response.output_text.delta", map[string]any{"item_id": "msg_" + w.id, "output_index": 0, "content_index": 0, "delta": delta, "logprobs": []any{}}); err != nil {
					return 0, err
				}
			}
		}
	}
	if value.Usage != nil {
		w.usage = value.Usage
	}
	return len(data), nil
}
