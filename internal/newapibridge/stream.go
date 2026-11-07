package newapibridge

import (
	"bufio"
	"io"
	"net/http"
	"strings"
)

// verifiedStream releases content immediately, but withholds DONE until the
// authenticated peer has closed the body with a durable completion trailer.
// EOF, cancellation, truncation and stripped trailers never become success.
type verifiedStream struct {
	body     io.ReadCloser
	response *http.Response
	limit    *io.LimitedReader
	scanner  *bufio.Scanner
	pending  []byte
	done     bool
}

func newVerifiedStream(response *http.Response) *verifiedStream {
	limit := &io.LimitedReader{R: response.Body, N: (4 << 20) + 1}
	scanner := bufio.NewScanner(limit)
	scanner.Buffer(make([]byte, 4096), 64<<10)
	return &verifiedStream{body: response.Body, response: response, limit: limit, scanner: scanner}
}
func (s *verifiedStream) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if len(s.pending) > 0 {
		n := copy(p, s.pending)
		s.pending = s.pending[n:]
		return n, nil
	}
	if s.done {
		return 0, io.EOF
	}
	if !s.scanner.Scan() {
		return 0, ErrUnavailable
	}
	line := s.scanner.Text()
	if s.limit.N <= 0 {
		return 0, ErrUnavailable
	}
	if strings.HasPrefix(line, "data:") && strings.TrimSpace(strings.TrimPrefix(line, "data:")) == "[DONE]" {
		for s.scanner.Scan() {
			tail := s.scanner.Text()
			if tail != "" && !strings.HasPrefix(tail, ":") {
				return 0, ErrUnavailable
			}
		}
		values := s.response.Trailer.Values("X-Aigw-Completion")
		if s.scanner.Err() != nil || s.limit.N <= 0 || len(values) != 1 || values[0] != "completed" {
			return 0, ErrUnavailable
		}
		s.done = true
		s.pending = []byte("data: [DONE]\n\n")
	} else {
		s.pending = []byte(line + "\n")
	}
	n := copy(p, s.pending)
	s.pending = s.pending[n:]
	return n, nil
}
func (s *verifiedStream) Close() error { return s.body.Close() }
