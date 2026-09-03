package commands

import (
	"embed"
	"net/http"
)

// 前端静态资源（HTML/CSS/JS），由 go:embed 编译嵌入。
// 开发时直接编辑 web/ 目录下的文件，重新 go build 即可生效。
//
//go:embed web/index.html
//go:embed web/style.css
//go:embed web/app.js
var webFS embed.FS

// HandleChatWebPage 返回微型 Web 客户端页面（§4.2.1）。
// 页面引用同目录下的 style.css 和 app.js，分别由 HandleChatWebStyle 和
// HandleChatWebApp 提供。
func HandleChatWebPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data, err := webFS.ReadFile("web/index.html")
	if err != nil {
		http.Error(w, "page not found", http.StatusNotFound)
		return
	}
	w.Write(data)
}

// HandleChatWebStyle 返回 /web/style.css 静态样式表。
func HandleChatWebStyle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	data, err := webFS.ReadFile("web/style.css")
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Write(data)
}

// HandleChatWebApp 返回 /web/app.js 静态 JavaScript。
func HandleChatWebApp(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	data, err := webFS.ReadFile("web/app.js")
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Write(data)
}
