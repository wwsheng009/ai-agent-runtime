package webui

import (
	"bytes"
	"html"
	"io/fs"
	"net/http"
	"path"
	"strconv"
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
		// Base-path awareness (docs/gateway/06 §4.1): when the SPA is served
		// under a reverse-proxy mount (the gateway forwards X-Forwarded-Prefix),
		// inject a <base href> so relative asset/API URLs resolve under it.
		if prefix := forwardedPrefix(r); prefix != "" {
			contents = injectBasePath(contents, prefix)
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

// forwardedPrefix returns a safe mount prefix from X-Forwarded-Prefix, or "".
func forwardedPrefix(r *http.Request) string {
	if r == nil {
		return ""
	}
	prefix := strings.TrimSpace(r.Header.Get("X-Forwarded-Prefix"))
	if prefix == "" || !strings.HasPrefix(prefix, "/") || strings.Contains(prefix, "..") {
		return ""
	}
	return strings.TrimRight(prefix, "/")
}

// injectBasePath inserts a <base href> and a global base value into index.html.
func injectBasePath(contents []byte, prefix string) []byte {
	prefix = strings.TrimRight(prefix, "/")
	if prefix == "" {
		return contents
	}
	snippet := []byte(`<base href="` + html.EscapeString(prefix+"/") + `">` +
		`<script>window.__RUNTIME_BASE__=` + strconv.Quote(prefix) + `;</script>`)
	if bytes.Contains(contents, []byte("<head>")) {
		return bytes.Replace(contents, []byte("<head>"), append([]byte("<head>"), snippet...), 1)
	}
	return append(snippet, contents...)
}
