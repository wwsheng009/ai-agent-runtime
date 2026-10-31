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
		// Console shell assets must revalidate: CDN edges (Cloudflare) cache
		// *.js/*.css for hours by default, which would otherwise delay a
		// release until the TTL expires. Versioned ?v= URLs in index.html are
		// the primary mechanism; this header is defense in depth.
		// 外壳与模块（console/js/*.js）都必须可重新验证：CDN 边缘默认按扩展名
		// 缓存 *.js 数小时，靠 ?v= 换 URL 破缓存；再叠一层 no-cache 兜底，
		// 避免"部署成功但页面还是旧逻辑"。
		if name == "index.html" || name == "app.js" || name == "style.css" || name == "sw.js" ||
			strings.HasPrefix(name, "js/") {
			w.Header().Set("Cache-Control", "no-cache")
		}
		if name == "index.html" {
			// FileServer redirects /index.html -> ./, so serve the directory
			// index via "/" instead.
			servePath = "/"
		}
		if name == "manifest.webmanifest" {
			// Go's extension MIME table is unreliable on Windows; set it here.
			w.Header().Set("Content-Type", "application/manifest+json")
		}
		r2 := r.Clone(r.Context())
		r2.URL.Path = servePath
		fileServer.ServeHTTP(w, r2)
	})
}

// hasReservedPrefix reports whether the path belongs to the API, tunnel or
// reverse-proxy namespaces (never served by the console).
func hasReservedPrefix(cleaned string) bool {
	for _, prefix := range []string{"/api", "/gw", "/n", "/m"} {
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
