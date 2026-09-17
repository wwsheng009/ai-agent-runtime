package commands

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
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
//     X-AICLI-Token（或 ?token=），令牌来源（按优先级）：
//     - --web-token 显式指定（固定令牌，适合 CI/服务化管理）；
//     - AICLI_WEB_TOKEN 环境变量（同上，flag 优先）；
//     - 未指定时进程启动时随机生成（默认，重启即轮换）：
//     - 终端启动行打印（供脚本/外部 Agent 使用）；
//     - 页面注入 meta 并自动附加（供内置 Web 客户端使用）。
//     - GET /web/api/token 显式读取（机器可读的稳定入口，供不便解析
//     启动行的脚本；关于页也直接显示，便于人工复制）。
//
// 只读 GET（含 SSE 事件流）不要求令牌：EventSource 无法设置请求头，且
// 读操作已被 Host/Origin 校验限制在本机同源范围内。
//
// 关于「令牌可被只读 GET 读取是否削弱防护」：不会。令牌挡的是浏览器
// 跨站写请求与 DNS rebinding（二者都带 Origin 或非回环 Host，被上面两层
// 拦下），而不是同机其他进程——本机进程本来就能读启动行、页面 meta 与
// 进程内存。因此把读取收敛为单一显式端点只是可脚本化，不扩大信任边界；
// 该端点明确 no-store，且令牌原文不进入 /debug/endpoints 响应。
// ============================================================================

// ChatWebAuthTokenHeader 是写操作令牌的请求头名称。
const ChatWebAuthTokenHeader = "X-AICLI-Token"

// ChatWebAuthTokenEnv 是预设写令牌的环境变量名（--web-token 优先于它）。
const ChatWebAuthTokenEnv = "AICLI_WEB_TOKEN"

// 令牌来源标识：用于启动行提示，便于操作者判断"重启是否还会轮换"。
const (
	chatWebAuthTokenSourceRandom = "random"
	chatWebAuthTokenSourceFlag   = "--web-token"
	chatWebAuthTokenSourceEnv    = ChatWebAuthTokenEnv
)

// 显式令牌的长度区间与字符集约束：
//   - 下限 16 字节：低于此长度可被暴力猜测，直接拒绝而不是静默接受；
//   - 上限 256 字节：避免异常输入进入常量时间比较与日志；
//   - 字符集限定 RFC 3986 unreserved（A-Za-z0-9-._~）：既安全放在请求头，
//     也能原样放进 ?token= 查询参数，不需要 URL 转义。
const (
	chatWebAuthTokenMinLen = 16
	chatWebAuthTokenMaxLen = 256
)

var (
	chatWebAuthMu          sync.Mutex
	chatWebAuthToken       string
	chatWebAuthTokenSource = chatWebAuthTokenSourceRandom
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
		chatWebAuthTokenSource = chatWebAuthTokenSourceRandom
	}
	return chatWebAuthToken
}

// ChatWebAuthToken 返回已生成的写令牌；未生成时返回空串。
func ChatWebAuthToken() string {
	chatWebAuthMu.Lock()
	defer chatWebAuthMu.Unlock()
	return chatWebAuthToken
}

// ChatWebAuthTokenSource 返回当前令牌来源：random / --web-token / AICLI_WEB_TOKEN。
// 供启动行与 /debug 提示"令牌是否会随重启轮换"。
func ChatWebAuthTokenSource() string {
	chatWebAuthMu.Lock()
	defer chatWebAuthMu.Unlock()
	return chatWebAuthTokenSource
}

// SetChatWebAuthToken 预设本进程写令牌（来自 --web-token 或 AICLI_WEB_TOKEN）。
//
// 必须在服务器开始对外服务前调用：令牌一旦生效就不可在运行中替换
// （避免"已注入页面的 meta 与服务器期望值不一致"这类半途换令牌状态）。
// 校验失败返回错误，调用方应中止启动而不是回退随机令牌——静默回退会让
// "我明明传了 token"变成难以排查的 403。
func SetChatWebAuthToken(token string) error {
	if err := validateChatWebAuthToken(token); err != nil {
		return err
	}
	chatWebAuthMu.Lock()
	defer chatWebAuthMu.Unlock()
	if chatWebAuthToken != "" && chatWebAuthToken != token {
		return fmt.Errorf("web write token already initialized")
	}
	chatWebAuthToken = token
	chatWebAuthTokenSource = chatWebAuthTokenSourceFlag
	return nil
}

// ApplyChatWebAuthToken 按优先级应用显式令牌：--web-token > AICLI_WEB_TOKEN。
// 两者都为空时不改动任何状态（保持随机生成），返回空来源。
// 返回的 source 用于启动行提示；校验失败时返回包装后的错误。
func ApplyChatWebAuthToken(flagValue string) (string, error) {
	token := strings.TrimSpace(flagValue)
	source := chatWebAuthTokenSourceFlag
	if token == "" {
		token = strings.TrimSpace(os.Getenv(ChatWebAuthTokenEnv))
		source = chatWebAuthTokenSourceEnv
	}
	if token == "" {
		return "", nil
	}
	if err := SetChatWebAuthToken(token); err != nil {
		return "", fmt.Errorf("%s: %w", source, err)
	}
	chatWebAuthMu.Lock()
	chatWebAuthTokenSource = source
	chatWebAuthMu.Unlock()
	return source, nil
}

// validateChatWebAuthToken 校验显式令牌。错误信息不回显令牌内容，
// 避免把凭证写进终端日志或 issue。
func validateChatWebAuthToken(token string) error {
	if token == "" {
		return fmt.Errorf("token must not be empty")
	}
	if token != strings.TrimSpace(token) {
		return fmt.Errorf("token must not contain surrounding whitespace")
	}
	if len(token) < chatWebAuthTokenMinLen {
		return fmt.Errorf("token too short: %d bytes, need at least %d", len(token), chatWebAuthTokenMinLen)
	}
	if len(token) > chatWebAuthTokenMaxLen {
		return fmt.Errorf("token too long: %d bytes, max %d", len(token), chatWebAuthTokenMaxLen)
	}
	for _, r := range token {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '.', r == '_', r == '~':
		default:
			return fmt.Errorf("token contains unsupported characters (allowed: A-Z a-z 0-9 - . _ ~)")
		}
	}
	return nil
}

// setChatWebAuthTokenForTest 供同包测试注入/清空令牌与来源。
func setChatWebAuthTokenForTest(token string) {
	chatWebAuthMu.Lock()
	defer chatWebAuthMu.Unlock()
	chatWebAuthToken = token
	if token == "" {
		chatWebAuthTokenSource = chatWebAuthTokenSourceRandom
		return
	}
	chatWebAuthTokenSource = chatWebAuthTokenSourceFlag
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
