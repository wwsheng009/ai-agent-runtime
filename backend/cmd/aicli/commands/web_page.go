package commands

import (
	"bytes"
	"embed"
	"html"
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
	if name == "index.html" {
		data = chatWebInjectAuthToken(data)
	}
	w.Header().Set("Content-Type", webAssetContentType(name))
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Write(data)
}

// chatWebAuthFetchWrapperScript 在页面最早期（head 内联脚本，先于 ES 模块
// 执行）包装 window.fetch：对同源 /web/api/* 的状态变更请求自动附加
// X-AICLI-Token。EventSource（SSE）无法设置请求头，只读 GET 因此不要求令牌。
const chatWebAuthFetchWrapperScript = `<script>
(function () {
  var meta = document.querySelector('meta[name="aicli-web-token"]');
  if (!meta || !meta.content || typeof window.fetch !== 'function') { return; }
  var token = meta.content;
  var original = window.fetch.bind(window);
  window.fetch = function (input, init) {
    init = init || {};
    var url = typeof input === 'string' ? input : (input && input.url) || '';
    var apiPath = url.indexOf('/web/api/') === 0 ||
      (window.location.origin && url.indexOf(window.location.origin + '/web/api/') === 0);
    var method = (init.method || (input && input.method) || 'GET').toUpperCase();
    if (apiPath && method !== 'GET' && method !== 'HEAD') {
      var headers = new Headers(init.headers || (input && input.headers) || {});
      headers.set('X-AICLI-Token', token);
      init.headers = headers;
    }
    return original(input, init);
  };
})();
</script>
`

// chatWebInjectAuthToken 在 index.html 的 </head> 前注入写令牌 meta 与
// fetch 包装脚本；令牌未生成（同包单测等场景）或占位缺失时原样返回。
func chatWebInjectAuthToken(indexHTML []byte) []byte {
	token := ChatWebAuthToken()
	if token == "" || !bytes.Contains(indexHTML, []byte("</head>")) {
		return indexHTML
	}
	snippet := "<meta name=\"aicli-web-token\" content=\"" + html.EscapeString(token) + "\">\n" +
		chatWebAuthFetchWrapperScript + "</head>"
	return bytes.Replace(indexHTML, []byte("</head>"), []byte(snippet), 1)
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
