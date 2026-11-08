package bifrost

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"github.com/jhzhfree/ai-governance-gateway/modules/engine/execution"
	"io"
	"unicode/utf8"
)

// readSSEFrames owns byte and framing limits only. A protocol handler must
// explicitly prove its terminal event; EOF is never protocol completion.
func readSSEFrames(ctx context.Context, body io.Reader, handle func(string, json.RawMessage) (bool, error)) error {
	reader := bufio.NewReaderSize(body, 4096)
	var data []byte
	name := ""
	named := false
	frameBytes, totalBytes := 0, 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
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
				if named {
					return execution.ErrUpstream
				}
				continue
			}
			data = bytes.TrimSuffix(data, []byte{'\n'})
			done, err := handle(name, data)
			if err != nil {
				return err
			}
			if done {
				return nil
			}
			data = nil
			name = ""
			named = false
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
			if named {
				return execution.ErrUpstream
			}
			name = string(value)
			named = true
		default:
			return execution.ErrUpstream
		}
	}
}
