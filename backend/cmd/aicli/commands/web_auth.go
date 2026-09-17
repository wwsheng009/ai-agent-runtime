package commands

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ============================================================================
// 本机 loopback Web 端点鉴权（P0 安全加固）
//
// 威胁模型：aicli --pprof 暴露的 /web/* 与 /debug/* 默认无鉴权，任何本机
// 进程都能调用；更关键的是浏览器可被第三方页面利用发起「简单请求」CSRF
// （text/plain body 无预检），以及 DNS rebinding 读取响应。三层防护：
//
//  1. Host 必须是回环地址（挡 DNS rebinding / 反向代理转发）；
//  2. 携带 Origin 的请求必须与请求 Host 同源（挡浏览器跨站写请求）；
//  3. 状态变更方法（POST/PUT/PATCH/DELETE）必须携带写令牌
//     X-AICLI-Token（或 ?token=），令牌进程启动时随机生成：
//     - 终端启动行打印（供脚本/外部 Agent 使用）；
//     - 页面注入 meta 并自动附加（供内置 Web 客户端使用）。
//
// 只读 GET（含 SSE 事件流）不要求令牌：EventSource 无法设置请求头，且
// 读操作已被 Host/Origin 校验限制在本机同源范围内。
// ============================================================================

// ChatWebAuthTokenHeader 是写操作令牌的请求头名称。
const ChatWebAuthTokenHeader = "X-AICLI-Token"

var (
	chatWebAuthMu    sync.Mutex
	chatWebAuthToken string
)

// EnsureChatWebAuthToken 返回当前进程的 Web 写令牌；首次调用时随机生成。
// 生成失败（crypto/rand 不可用，极罕见）时退回时间派生值，保证非空。
func EnsureChatWebAuthToken() string {
	chatWebAuthMu.Lock()
	defer chatWebAuthMu.Unlock()
	if chatWebAuthToken == "" {
		buf := make([]byte, 16)
		if _, err := rand.Read(buf); err != nil {
			chatWebAuthToken = strconv.FormatInt(time.Now().UnixNano(), 16)
		} else {
			chatWebAuthToken = hex.EncodeToString(buf)
		}
	}
	return chatWebAuthToken
}

// ChatWebAuthToken 返回已生成的写令牌；未生成时返回空串。
func ChatWebAuthToken() string {
	chatWebAuthMu.Lock()
	defer chatWebAuthMu.Unlock()
	return chatWebAuthToken
}

// setChatWebAuthTokenForTest 供同包测试注入/清空令牌。
func setChatWebAuthTokenForTest(token string) {
	chatWebAuthMu.Lock()
	defer chatWebAuthMu.Unlock()
	chatWebAuthToken = token
}

// ChatWebAuthGuard 包装 HTTP 处理器，实施 Host/Origin/写令牌校验。
// 校验失败返回 403 JSON（status=forbidden + reason）。
func ChatWebAuthGuard(next http.HandlerFunc) http.HandlerFunc {
	if next == nil {
		return nil
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if reason := chatWebAuthRejectReason(r); reason != "" {
			writeWebAPIJSON(w, http.StatusForbidden, map[string]string{
				"status": "forbidden",
				"reason": reason,
			})
			return
		}
		next(w, r)
	}
}

// chatWebAuthRejectReason 返回拒绝原因；空串表示放行。
func chatWebAuthRejectReason(r *http.Request) string {
	if r == nil {
		return "invalid request"
	}
	if !ChatWebHostIsLoopback(r.Host) {
		return "non-loopback Host header rejected"
	}
	if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != "" {
		if !chatWebOriginMatchesHost(origin, r.Host) {
			return "cross-origin request rejected"
		}
	}
	if chatWebMethodRequiresToken(r.Method) && !chatWebTokenValid(r) {
		return "missing or invalid " + ChatWebAuthTokenHeader
	}
	return ""
}

// ChatWebHostIsLoopback 判断 Host（可带端口）是否为回环地址。
func ChatWebHostIsLoopback(hostport string) bool {
	name := chatWebHostName(hostport)
	if name == "" {
		return false
	}
	if name == "localhost" {
		return true
	}
	if ip := net.ParseIP(name); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

func chatWebHostName(hostport string) string {
	h := strings.TrimSpace(hostport)
	if h == "" {
		return ""
	}
	if strings.HasPrefix(h, "[") {
		if idx := strings.Index(h, "]"); idx > 0 {
			return strings.ToLower(h[1:idx])
		}
		return strings.ToLower(strings.Trim(h, "[]"))
	}
	if host, _, err := net.SplitHostPort(h); err == nil {
		return strings.ToLower(host)
	}
	return strings.ToLower(h)
}

// chatWebOriginMatchesHost 判断 Origin 是否与请求 Host 同源（scheme 固定
// http，比较 host:port；页面经 http://127.0.0.1:port 或 http://localhost:port
// 打开时二者一致）。
func chatWebOriginMatchesHost(origin, host string) bool {
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" {
		return false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return false
	}
	return strings.EqualFold(parsed.Host, strings.TrimSpace(host))
}

// chatWebMethodRequiresToken 判断方法是否为状态变更（需要写令牌）。
func chatWebMethodRequiresToken(method string) bool {
	switch strings.ToUpper(strings.TrimSpace(method)) {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	default:
		return true
	}
}

// chatWebTokenValid 校验请求携带的写令牌（请求头或 ?token= 查询参数）。
// 令牌未初始化时放行（同包单测直接调用处理器、未启动 HTTP 服务器的场景）。
func chatWebTokenValid(r *http.Request) bool {
	expected := ChatWebAuthToken()
	if expected == "" {
		return true
	}
	provided := strings.TrimSpace(r.Header.Get(ChatWebAuthTokenHeader))
	if provided == "" {
		provided = strings.TrimSpace(r.URL.Query().Get("token"))
	}
	if provided == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) == 1
}
