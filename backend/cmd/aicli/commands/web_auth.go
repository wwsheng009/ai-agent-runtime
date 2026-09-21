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
//   1. Host 必须是回环地址（挡 DNS rebinding / 反向代理转发）；
//   2. 携带 Origin 的请求必须与请求 Host 同源（挡浏览器跨站写请求）；
//   3. 状态变更方法（POST/PUT/PATCH/DELETE）必须携带写令牌
//      X-AICLI-Token（或 ?token=），令牌来源（按优先级）：
//      - --web-token 显式指定（固定令牌，适合 CI/服务化管理）；
//      - AICLI_WEB_TOKEN 环境变量（同上，flag 优先）；
//      - 未指定时进程启动时随机生成（默认，重启即轮换）：
//      - 终端启动行打印（供脚本/外部 Agent 使用）；
//      - 页面注入 meta 并自动附加（供内置 Web 客户端使用）。
//      - GET /web/api/token 显式读取（机器可读的稳定入口，供不便解析
//      启动行的脚本；关于页也直接显示，便于人工复制）。
//
// 只读 GET（含 SSE 事件流）不要求令牌：EventSource 无法设置请求头，且
// 读操作已被 Host/Origin 校验限制在本机同源范围内。
//
// 关于「令牌可被只读 GET 读取是否削弱防护」：不会。令牌挡的是浏览器
// 跨站写请求与 DNS rebinding（二者都带 Origin 或非回环 Host，被上面两层
// 拦下），而不是同机其他进程——本机进程本来就能读启动行、页面 meta 与
// 进程内存。因此把读取收敛为单一显式端点只是可脚本化，不扩大信任边界；
// 该端点明确 no-store，且令牌原文不进入 /debug/endpoints 响应。
//
// --- 非 loopback 模式 (--web-host 0.0.0.0 / 非回环地址) ---
//
// 当 --web-host 显式指定为非回环地址（如 0.0.0.0）时，服务器监听本机
// 所有网络接口，可被局域网访问。此时 Host/Origin 无法限定在回环范围内，
// 因此鉴权模型升级为「所有请求都必须携带写令牌（X-AICLI-Token 或
// ?token=）」：GET/HEAD/SSE 事件流 likewise 需要令牌。Origin 校验在
// 非回环模式下自动放宽（跨域访问是合法场景）。这确保仅拥有令牌的人能
// 访问，避免暴露调试端点到局域网上的非授权用户。
// ============================================================================
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
	// chatWebLoopbackMode 控制鉴权模式：true=回环模式（Host/Origin+写令牌），
	// false=非回环模式（所有请求需令牌，放宽 Host/Origin）。默认 true，
	// 当 --web-host 指定非回环地址（如 0.0.0.0）时由 startPprofServer 设为 false。
	chatWebLoopbackMode = true
	// chatWebDevMode 开启开发模式：仅在回环模式下生效，跳过对写令牌的校验
	//（POST/PUT/DELETE 等），便于本机开发无需携带 --web-token。
	chatWebDevMode = false
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

// IsChatWebLoopbackMode 返回当前鉴权模式：true=回环模式，false=非回环模式。
// 非回环模式下所有请求（含 GET/SSE）都需要令牌，Host/Origin 校验被放宽。
func IsChatWebLoopbackMode() bool {
	chatWebAuthMu.Lock()
	defer chatWebAuthMu.Unlock()
	return chatWebLoopbackMode
}

// ChatWebTokenQueryParam 返回非回环模式下的 token 查询参数后缀（?token=xxxxx），
// 回环模式下返回空串。供 URL 构造处调用，便于在 --web-host 0.0.0.0 时生成
// 带令牌的访问地址（如 http://host:port/web?token=xxxxx）。
func ChatWebTokenQueryParam() string {
	if chatWebLoopbackMode {
		return ""
	}
	token := ChatWebAuthToken()
	if token == "" {
		return ""
	}
	return "?token=" + token
}

// ChatWebLocalAddresses 返回本机非回环网络接口的 IP 地址列表（IPv4 优先）。
// 用于在 --web-host 0.0.0.0 时，向用户展示可用于局域网访问的实际 IP 地址
// （0.0.0.0 在浏览器中不可直接访问）。返回的地址已过滤回环、链路本地与未运行接口。
func ChatWebLocalAddresses() []string {
	var addrs []string
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 {
			continue // 接口未启用
		}
		if iface.Flags&net.FlagLoopback != 0 {
			continue // 跳过回环接口
		}
		ifaceAddrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range ifaceAddrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil {
				continue
			}
			// 优先收集 IPv4 地址，其次收集 IPv6 地址（跳过链路本地）
			if ip4 := ip.To4(); ip4 != nil {
				if ip4.IsLoopback() || ip4.IsLinkLocalUnicast() {
					continue
				}
				addrs = append(addrs, ip4.String())
			} else if ip.IsLoopback() || ip.IsLinkLocalUnicast() {
				continue
			} else {
				addrs = append(addrs, "["+ip.String()+"]")
			}
		}
	}
	return addrs
}

// SetChatWebLoopbackMode 切换鉴权模式。仅在服务器启动前由 startPprofServer 调用：
// --web-host 指定非回环地址（如 0.0.0.0）时设为 false，回环地址时保持 true。
// 不能运行中动态切换：避免"已注入页面的 meta 与服务器期望值不一致"这类半途换模式状态。
func SetChatWebLoopbackMode(loopback bool) {
	chatWebAuthMu.Lock()
	defer chatWebAuthMu.Unlock()
	chatWebLoopbackMode = loopback
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

// setChatWebLoopbackModeForTest 供同包测试切换回环/非回环模式。
func setChatWebLoopbackModeForTest(loopback bool) {
	chatWebAuthMu.Lock()
	defer chatWebAuthMu.Unlock()
	chatWebLoopbackMode = loopback
}

// IsChatWebDevMode 返回开发模式是否开启。
func IsChatWebDevMode() bool {
	chatWebAuthMu.Lock()
	defer chatWebAuthMu.Unlock()
	return chatWebDevMode
}

// SetChatWebDevMode 开启/关闭开发模式。
// 回环模式下跳过写令牌校验（POST/PUT/DELETE 无需 token）；
// 非回环模式下（--web-host 0.0.0.0）回环 IP 始终跳过校验，
// --web-dev 仅影响回环模式下的 POST 校验行为。
func SetChatWebDevMode(dev bool) {
	chatWebAuthMu.Lock()
	defer chatWebAuthMu.Unlock()
	chatWebDevMode = dev
}

// chatWebClientIP 从请求的 RemoteAddr 提取客户端 IP（IPv4 或 IPv6）。
func chatWebClientIP(r *http.Request) string {
	if r == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return ""
	}
	return host
}

// isClientLoopbackIP 判断请求的客户端 IP 是否为回环地址（127.0.0.0/8 或 ::1）。
// 仅回环 IP 在 --web-dev + 0.0.0.0 模式下跳过令牌校验；
// 本地私有网段 IP（如 192.168.x.x）及远程 IP 均需令牌，
// 防止局域网内的其他设备越权访问。
func isClientLoopbackIP(ipStr string) bool {
	if ipStr == "" {
		return false
	}
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	// 解析 "localhost" 等名称（RemoteAddr 通常是 IP，但防御性处理）
	return false
}

// chatWebIsStaticAssetRequest 判断请求是否为静态资产（HTML/CSS/JS/图片等）。
// 在非回环模式下，浏览器加载 <link>/<script>/<img> 时无法附加令牌，
// 因此允许 GET/HEAD 对 /web/...（排除 /web/api/*）的无令牌访问。
// API 接口（/web/api/*）、调试端点（/debug/*）与观测端点仍需令牌。
func chatWebIsStaticAssetRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	p := r.URL.Path
	// index.html 页面本身不视为静态资产（其内容可能包含写令牌 meta/injection）；
	// 在非回环模式下需令牌访问（通过 ?token= 传递）。
	if p == ChatWebPath || p == strings.TrimSuffix(ChatWebPath, "/") {
		return false
	}
	// /web/style.css、/web/app.js、/web/js/*.js 等静态资源：浏览器加载时
	// 无法附加请求头，允许无令牌 GET/HEAD。
	if strings.HasPrefix(p, ChatWebPath) && !strings.HasPrefix(p, "/web/api/") {
		return true
	}
	// /favicon.ico 常被浏览器主动请求，允许无令牌获取。
	if p == "/favicon.ico" {
		return true
	}
	return false
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
	if IsChatWebLoopbackMode() {
		// 回环模式：Host 必须是回环地址，Origin 同源校验，写操作需令牌。
		// 开发模式下（--web-dev）跳过令牌校验，便于本地开发。
		if !ChatWebHostIsLoopback(r.Host) {
			return "non-loopback Host header rejected"
		}
		if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != "" {
			if !chatWebOriginMatchesHost(origin, r.Host) {
				return "cross-origin request rejected"
			}
		}
		if IsChatWebDevMode() {
			return ""
		}
		if chatWebMethodRequiresToken(r.Method) && !chatWebTokenValid(r) {
			return "missing or invalid " + ChatWebAuthTokenHeader
		}
	} else {
		// 非回环模式（--web-host 0.0.0.0 等）：
		// 默认所有方法（含 GET/HEAD/SSE）都必须携带令牌。
		// 例外 1：浏览器加载 <link>/<script>/<img> 时无法附加令牌，
		// 因此允许 GET/HEAD 的静态资产（CSS/JS/favicon）访问。
		// 例外 2：从回环 IP（127.0.0.1/localhost/[::1]）发起的请求
		// 始终跳过令牌校验（本地浏览器访问， inherently safe）；
		// 开发模式（--web-dev）下不影响 Host/Origin 校验，仍防止 DNS rebinding。
		if chatWebIsStaticAssetRequest(r) {
			return ""
		}
		if isClientLoopbackIP(chatWebClientIP(r)) {
			return ""
		}
		if !chatWebTokenValid(r) {
			return "missing or invalid " + ChatWebAuthTokenHeader + " (required on non-loopback)"
		}
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
// 回环模式下令牌未初始化时放行（同包单测直接调用处理器、未启动 HTTP 服务器的场景）；
// 非回环模式下（--web-host 0.0.0.0）即使令牌未初始化也拒绝，确保默认拒绝。
func chatWebTokenValid(r *http.Request) bool {
	expected := ChatWebAuthToken()
	if expected == "" {
		// 非回环模式下，令牌未生成视为拒绝（防御默认拒绝）；
		// 回环模式下放行，避免单测/未启动服务器时阻塞本地调试。
		if !IsChatWebLoopbackMode() {
			return false
		}
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
