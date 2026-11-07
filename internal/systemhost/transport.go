package systemhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"time"
)

func Handler(agent Agent) http.Handler {
	mux := http.NewServeMux()
	send := func(w http.ResponseWriter, v any, e error) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		status := 200
		if e != nil {
			status = 503
			if errors.Is(e, ErrInvalid) {
				status = 400
			}
			if errors.Is(e, ErrConflict) {
				status = 409
			}
			v = map[string]string{"error": http.StatusText(status)}
		}
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(v)
	}
	decode := func(w http.ResponseWriter, r *http.Request, v any) bool {
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
		d.DisallowUnknownFields()
		if d.Decode(v) != nil || d.Decode(&struct{}{}) != io.EOF {
			send(w, nil, ErrInvalid)
			return false
		}
		return true
	}
	mux.HandleFunc("GET /snapshot", func(w http.ResponseWriter, r *http.Request) { v, e := agent.Snapshot(r.Context()); send(w, v, e) })
	mux.HandleFunc("POST /changes", func(w http.ResponseWriter, r *http.Request) {
		var c Change
		if !decode(w, r, &c) {
			return
		}
		v, e := agent.Apply(r.Context(), c)
		send(w, v, e)
	})
	mux.HandleFunc("POST /changes/{id}/{action}", func(w http.ResponseWriter, r *http.Request) {
		id, action := r.PathValue("id"), r.PathValue("action")
		if !idPattern.MatchString(id) || (action != "confirm" && action != "rollback") {
			send(w, nil, ErrInvalid)
			return
		}
		v, e := agent.Finish(r.Context(), id, action == "confirm")
		send(w, v, e)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		mux.ServeHTTP(w, r.WithContext(ctx))
	})
}

type Client struct {
	http      *http.Client
	transport *http.Transport
}

func NewClient(socket string) (*Client, error) {
	if !filepath.IsAbs(socket) || len(socket) > 100 {
		return nil, ErrInvalid
	}
	t := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}, MaxConnsPerHost: 8, IdleConnTimeout: 30 * time.Second}
	return &Client{http: &http.Client{Transport: t, Timeout: 12 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, transport: t}, nil
}
func (c *Client) Close() { c.transport.CloseIdleConnections() }
func (c *Client) call(ctx context.Context, method, path string, in, out any) error {
	var b bytes.Buffer
	if in != nil {
		if json.NewEncoder(&b).Encode(in) != nil {
			return ErrInvalid
		}
	}
	r, e := http.NewRequestWithContext(ctx, method, "http://host-agent"+path, &b)
	if e != nil {
		return ErrInvalid
	}
	r.Header.Set("Content-Type", "application/json")
	res, e := c.http.Do(r)
	if e != nil {
		return ErrUnavailable
	}
	defer res.Body.Close()
	switch res.StatusCode {
	case 200:
	case 400:
		return ErrInvalid
	case 409:
		return ErrConflict
	default:
		return ErrUnavailable
	}
	d := json.NewDecoder(io.LimitReader(res.Body, 1<<20))
	if d.Decode(out) != nil {
		return ErrUnavailable
	}
	return nil
}
func (c *Client) Snapshot(ctx context.Context) (v Snapshot, e error) {
	e = c.call(ctx, "GET", "/snapshot", nil, &v)
	return
}
func (c *Client) Apply(ctx context.Context, in Change) (v Result, e error) {
	e = c.call(ctx, "POST", "/changes", in, &v)
	return
}
func (c *Client) Finish(ctx context.Context, id string, confirm bool) (v Result, e error) {
	if !idPattern.MatchString(id) {
		return v, ErrInvalid
	}
	action := "rollback"
	if confirm {
		action = "confirm"
	}
	e = c.call(ctx, "POST", "/changes/"+id+"/"+action, nil, &v)
	return
}
