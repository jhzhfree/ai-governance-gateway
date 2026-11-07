package gateway

import "net/http"

// A marker only: errors are encoded through the current audit writer, so the
// audited bytes remain the bytes actually delivered to the client.
type messagesProtocolWriter struct{ http.ResponseWriter }

func (w *messagesProtocolWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func messagesProtocol(w http.ResponseWriter) bool {
	for {
		if _, ok := w.(*messagesProtocolWriter); ok {
			return true
		}
		u, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return false
		}
		w = u.Unwrap()
	}
}
func messagesErrorType(status int) string {
	switch status {
	case 400, 404, 405, 413:
		return "invalid_request_error"
	case 401:
		return "authentication_error"
	case 403:
		return "permission_error"
	case 429:
		return "rate_limit_error"
	case 503:
		return "overloaded_error"
	default:
		return "api_error"
	}
}

// Accept exactly one credential representation. Native vendor keys are never
// selected by caller headers; both forms carry the enterprise project key.
func messagesHeaders(r *http.Request) (*http.Request, bool) {
	versions := r.Header.Values("Anthropic-Version")
	if len(versions) > 1 || (len(versions) == 1 && versions[0] != "2023-06-01") || len(r.Header.Values("Anthropic-Beta")) != 0 {
		return r, false
	}
	keys := r.Header.Values("X-Api-Key")
	if len(keys) == 0 {
		return r, true
	}
	if len(keys) != 1 || keys[0] == "" || len(r.Header.Values("Authorization")) != 0 {
		return r, false
	}
	clone := r.Clone(r.Context())
	clone.Header.Del("X-Api-Key")
	clone.Header.Set("Authorization", "Bearer "+keys[0])
	return clone, true
}
