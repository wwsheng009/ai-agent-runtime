package main

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed console
var consoleFS embed.FS

// consoleHandler serves the embedded lightweight console (docs/gateway/07-console.md).
// Unknown non-API paths fall back to index.html; API/tunnel/proxy prefixes stay
// 404s so the console never masks a missing API route.
func consoleHandler() http.Handler {
	sub, err := fs.Sub(consoleFS, "console")
	if err != nil {
		return http.NotFoundHandler()
	}
	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.NotFound(w, r)
			return
		}
		raw := r.URL.Path
		if strings.Contains(raw, "..") {
			http.NotFound(w, r)
			return
		}
		cleaned := path.Clean("/" + raw)
		if hasReservedPrefix(cleaned) {
			http.NotFound(w, r)
			return
		}
		name := strings.TrimPrefix(cleaned, "/")
		if name == "" || !isRegularConsoleFile(sub, name) {
			name = "index.html"
		}
		servePath := "/" + name
		if name == "index.html" {
			// FileServer redirects /index.html -> ./, so serve the directory
			// index via "/" instead.
			servePath = "/"
			w.Header().Set("Cache-Control", "no-cache")
		}
		r2 := r.Clone(r.Context())
		r2.URL.Path = servePath
		fileServer.ServeHTTP(w, r2)
	})
}

// hasReservedPrefix reports whether the path belongs to the API, tunnel or
// reverse-proxy namespaces (never served by the console).
func hasReservedPrefix(cleaned string) bool {
	for _, prefix := range []string{"/api", "/gw", "/n"} {
		if cleaned == prefix || strings.HasPrefix(cleaned, prefix+"/") {
			return true
		}
	}
	return false
}

func isRegularConsoleFile(fsys fs.FS, name string) bool {
	info, err := fs.Stat(fsys, name)
	return err == nil && info.Mode().IsRegular()
}
