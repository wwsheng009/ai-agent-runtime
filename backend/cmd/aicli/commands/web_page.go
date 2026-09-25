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
		// 非回环模式：下发导航 cookie，让 F5 / 新标签页 / 标签页恢复这类
		// 「浏览器自己发起、带不了请求头也读不到 sessionStorage」的导航也能通过
		// 鉴权（web_auth.go::SetChatWebPageAuthCookie；回环模式为空操作，
		// cookie 只用于页面本身，不参与 API 与写方法）。
		SetChatWebPageAuthCookie(w)
	}
	w.Header().Set("Content-Type", webAssetContentType(name))
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Write(data)
}

// chatWebAuthFetchWrapperScript 在页面最早期（head 内联脚本，先于 ES 模块
// 执行）包装 window.fetch：对同源 /web/api/* 的请求自动附加 X-AICLI-Token。
// 在回环模式下只对状态变更方法附加；在非回环模式（--web-host 0.0.0.0）下
// 对所有方法（含 GET）附加，确保局域网访问也受令牌保护。
// 令牌取值顺序与 js/util.js::webAuthToken 一致：meta（当前进程注入的权威值）
// 优先，sessionStorage 仅在 meta 缺失时兜底。
// EventSource（SSE）无法设置请求头，sse.js 单独处理其 URL。
const chatWebAuthFetchWrapperScript = `<script>
(function () {
  var TOKEN_STORAGE_KEY = 'aicli-web-token';
  // §7.3 窗口深链：/web?token=<t>&session=<id>（aicli-mesh open / mesh/spawn 产出）。
  // 这段必须在内联脚本最前面、且在 ES 模块之前执行：模块顶层就会发起第一个
  // fetch（loadSessions），那时令牌得已经在 sessionStorage 里。
  // 令牌只允许在地址栏出现这一次：立刻转存并 history.replaceState 抹掉，
  // 避免留在地址栏/历史记录（M7：令牌不落 localStorage/DOM）。
  try {
    var deepParams = new URLSearchParams(window.location.search);
    var deepToken = deepParams.get('token');
    if (deepToken && String(deepToken).trim()) {
      sessionStorage.setItem(TOKEN_STORAGE_KEY, String(deepToken).trim());
    }
    if (deepToken !== null) {
      deepParams.delete('token');
      var deepQuery = deepParams.toString();
      window.history.replaceState(null, '',
        window.location.pathname + (deepQuery ? '?' + deepQuery : '') + window.location.hash);
    }
    var deepSession = deepParams.get('session');
    if (deepSession) { window.__aicli_deep_link_session = String(deepSession); }
  } catch (e) { /* 存储不可用/老浏览器：退回 meta 标签路径 */ }
  // 令牌取值顺序（2026-09 修订，与 js/util.js::webAuthToken 一致）：
  //   1. <meta name="aicli-web-token">：由**当前**进程注入，是权威值，且 meta
  //      就在本脚本之前，head 阶段即可读到；
  //   2. sessionStorage：同源缓存，只在 meta 缺失时兜底——进程重启换了随机
  //      令牌后，缓存里可能是上一个进程的过期值。
  // 顺序反了会怎样：ES 模块顶层的第一个写请求会带旧令牌 → 403。
  function readMetaToken() {
    var meta = document.querySelector('meta[name="aicli-web-token"]');
    return meta && meta.content ? String(meta.content).trim() : '';
  }
  function getAICLIToken() {
    var token = readMetaToken();
    if (token) {
      try { sessionStorage.setItem(TOKEN_STORAGE_KEY, token); } catch (e) { /* 存储不可用 */ }
      return token;
    }
    try {
      var cached = sessionStorage.getItem(TOKEN_STORAGE_KEY);
      if (cached) { return String(cached).trim(); }
    } catch (e) { /* 隐私模式下 sessionStorage 可能抛错 */ }
    return '';
  }
  // head 阶段同步把 meta 令牌写进 sessionStorage：ES 模块顶层（loadSessions 等）
  // 可能在 DOMContentLoaded 之前就发请求，不能等事件回调再落缓存。
  getAICLIToken();
  // 「关于」页的链接改写放在 DOMContentLoaded：那时 DOM 才完整。
  window.addEventListener('DOMContentLoaded', function () {
    // 非回环模式下：为关于页的所有外部链接附加 ?token=，
    // 这样在新标签页打开时也能通过查询参数携带令牌。
    if (!window.__aicli_non_loopback) { return; }
    var token = getAICLIToken();
    if (!token) { return; }
    // /web/api/* 和 /debug/* 链接需要 token；/web/ 静态资源无需 token。
    var links = document.querySelectorAll('a[href^="/web/api/"], a[href^="/debug/"]');
    for (var i = 0; i < links.length; i++) {
      (function (link) {
        var href = link.getAttribute('href');
        if (!href || href.indexOf('token=') !== -1) { return; }
        var sep = href.indexOf('?') !== -1 ? '&' : '?';
        link.setAttribute('href', href + sep + 'token=' + encodeURIComponent(token));
      })(links[i]);
    }
  });
  if (typeof window.fetch !== 'function') { return; }
  var original = window.fetch.bind(window);
  window.fetch = function (input, init) {
    init = init || {};
    var url = typeof input === 'string' ? input : (input && input.url) || '';
    var apiPath = url.indexOf('/web/api/') === 0 ||
      (window.location.origin && url.indexOf(window.location.origin + '/web/api/') === 0);
    var method = (init.method || (input && input.method) || 'GET').toUpperCase();
    // ?token= 查询参数优先于 Header；fetch 时若 URL 已含 token 则不重复附加。
    var hasQueryParam = url.indexOf('?token=') !== -1 || url.indexOf('&token=') !== -1;
    if (apiPath && !hasQueryParam &&
        (method !== 'GET' && method !== 'HEAD' || window.__aicli_non_loopback)) {
      var token = getAICLIToken();
      if (token) {
        var headers = new Headers(init.headers || (input && input.headers) || {});
        headers.set('X-AICLI-Token', token);
        init.headers = headers;
      }
    }
    return original(input, init);
  };
})();
</script>
`

// chatWebInjectAuthToken 在 index.html 的 </head> 前注入写令牌 meta、
// 非回环模式标志与 fetch 包装脚本；令牌未生成（同包单测等场景）或占位
// 缺失时原样返回。
func chatWebInjectAuthToken(indexHTML []byte) []byte {
	token := ChatWebAuthToken()
	if token == "" || !bytes.Contains(indexHTML, []byte("</head>")) {
		return indexHTML
	}
	snippet := "<meta name=\"aicli-web-token\" content=\"" + html.EscapeString(token) + "\">\n"
	if !IsChatWebLoopbackMode() {
		// 非回环模式：告知前端 JS 将令牌附加到所有 API 请求（含 GET/SSE）
		snippet += "<script>window.__aicli_non_loopback = true;</script>\n"
	}
	snippet += chatWebAuthFetchWrapperScript + "</head>"
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
