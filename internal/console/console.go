// Package console serves only built console assets, never arbitrary source files.
package console

import (
	"errors"
	"io/fs"
	"net/http"
	"os"
	"strings"
)

func New(directory string) (http.Handler, error) {
	root := os.DirFS(directory)
	if _, err := fs.Stat(root, "index.html"); err != nil {
		return nil, errors.New("console build missing; build web/ before enabling browser login")
	}
	files := http.FileServerFS(root)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" {
			http.Error(w, "method not allowed", 405)
			return
		}
		if r.URL.Path != "/" && (!strings.HasPrefix(r.URL.Path, "/assets/") || strings.Contains(r.URL.Path, "..")) {
			http.NotFound(w, r)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" {
			name = "index.html"
		}
		stat, err := fs.Stat(root, name)
		if err != nil || stat.IsDir() {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if name == "index.html" {
			w.Header().Set("Cache-Control", "no-store")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		files.ServeHTTP(w, r)
	}), nil
}
