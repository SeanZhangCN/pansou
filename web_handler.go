package main

import (
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// newAppHandler serves the built frontend and API from one origin. fnOS forwards
// /app/pansou requests to the Unix Socket; direct root paths aid local testing.
func newAppHandler(api http.Handler, frontendDir, gatewayPrefix string) http.Handler {
	prefix := strings.TrimRight(gatewayPrefix, "/")
	files := http.FileServer(http.Dir(frontendDir))
	index := filepath.Join(frontendDir, "index.html")

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		urlPath := r.URL.Path
		if prefix != "" {
			switch {
			case urlPath == prefix:
				urlPath = "/"
			case strings.HasPrefix(urlPath, prefix+"/"):
				urlPath = strings.TrimPrefix(urlPath, prefix)
			}
		}
		if urlPath != path.Clean("/"+strings.TrimPrefix(urlPath, "/")) {
			http.NotFound(w, r)
			return
		}

		req := r.Clone(r.Context())
		req.URL.Path = urlPath
		req.URL.RawPath = ""
		if strings.HasPrefix(urlPath, "/api/") {
			api.ServeHTTP(w, req)
			return
		}
		if urlPath == "/" {
			http.ServeFile(w, req, index)
			return
		}

		name := filepath.Join(frontendDir, filepath.FromSlash(strings.TrimPrefix(urlPath, "/")))
		if info, err := os.Stat(name); err == nil && !info.IsDir() {
			files.ServeHTTP(w, req)
			return
		}
		if filepath.Ext(urlPath) == "" {
			http.ServeFile(w, req, index)
			return
		}
		http.NotFound(w, req)
	})
}
