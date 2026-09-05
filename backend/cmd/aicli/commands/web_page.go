package commands

import (
	"embed"
	"net/http"
	"path"
	"strings"
)

// 前端静态资源（HTML/CSS/JS），由 go:embed 编译嵌入。
// 开发时直接编辑 web/ 目录下的文件，重新 go build 即可生效。
//
//go:embed web
var webFS embed.FS

// HandleChatWebPage 伺服微型 Web 客户端的全部静态资源（§4.2.1）。
// ChatWebPath（"/web/"）是子树路由：按文件名返回 web/ 下的嵌入文件，
// index.html 为页面入口，其余（style.css、app.js、js/*.js ES 模块）按
// 扩展名给 Content-Type；未命中返回 404。/web/api/* 由更长的精确路由
// 优先接管（net/http ServeMux 最长模式匹配），不会走到这里。
func HandleChatWebPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/web/")
	name = strings.TrimPrefix(name, "/")
	if name == "" {
		name = "index.html"
	}
	// 只允许相对子路径内的普通文件名，拒绝目录穿越与绝对路径。
	if strings.Contains(name, "..") || strings.HasPrefix(name, "/") || path.IsAbs(name) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	data, err := webFS.ReadFile("web/" + name)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", webAssetContentType(name))
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Write(data)
}

// webAssetContentType 按扩展名返回静态资源的 Content-Type。
// 不用 mime.TypeByExtension：Windows 上会读注册表，类型不可控；
// ES 模块要求 JS 的 MIME 为 text/javascript 等已知 JS 类型。
func webAssetContentType(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".html":
		return "text/html; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8"
	case ".json":
		return "application/json; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".png":
		return "image/png"
	case ".ico":
		return "image/x-icon"
	default:
		return "application/octet-stream"
	}
}
