package webui

import (
	"bytes"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

type spaHandler struct {
	assets     fs.FS
	fileServer http.Handler
}

func newSPAHandler(assets fs.FS) http.Handler {
	return &spaHandler{
		assets:     assets,
		fileServer: http.FileServer(http.FS(assets)),
	}
}

func (h *spaHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.NotFound(w, r)
		return
	}

	requestedPath := cleanRequestPath(r.URL.Path)
	if requestedPath == "" {
		requestedPath = "index.html"
	}

	if isRegularFile(h.assets, requestedPath) {
		h.serveFile(w, r, requestedPath)
		return
	}

	// Unknown API routes must stay API 404s instead of receiving the SPA shell.
	if requestedPath == "api" || strings.HasPrefix(requestedPath, "api/") {
		http.NotFound(w, r)
		return
	}

	if !isRegularFile(h.assets, "index.html") {
		http.NotFound(w, r)
		return
	}
	h.serveFile(w, r, "index.html")
}

func cleanRequestPath(requestPath string) string {
	cleaned := path.Clean("/" + requestPath)
	if cleaned == "/" || cleaned == "." {
		return ""
	}
	return strings.TrimPrefix(cleaned, "/")
}

func isRegularFile(assets fs.FS, name string) bool {
	info, err := fs.Stat(assets, name)
	return err == nil && info.Mode().IsRegular()
}

func (h *spaHandler) serveFile(w http.ResponseWriter, r *http.Request, name string) {
	if name == "index.html" {
		w.Header().Set("Cache-Control", "no-cache")
		contents, err := fs.ReadFile(h.assets, name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		info, err := fs.Stat(h.assets, name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		http.ServeContent(w, r, name, info.ModTime(), bytes.NewReader(contents))
		return
	} else if strings.HasPrefix(name, "assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}

	request := r.Clone(r.Context())
	request.URL.Path = "/" + name
	request.URL.RawPath = ""
	h.fileServer.ServeHTTP(w, request)
}
