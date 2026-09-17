package commands

import (
	"net/http"
	"strings"
)

// ============================================================================
// GET /web/api/token —— 读取本进程 Web 写令牌（P2 可观测性）
//
// 定位：令牌的**唯一 HTTP 读取面**（另外两个来源是进程启动行与页面 meta
// 注入，关于页直接显示 meta 值）。只读 GET 因此不要求令牌本身（自举），
// 安全性完全由 ChatWebAuthGuard 的两层校验承担：
//
//   - 跨站页面发起的 fetch 带 Origin → 与 Host 不同源，403；
//   - <img>/<script> 之类无 Origin 的"简单请求"读不到 JSON 响应体（CORS 不放开，
//     且响应不是可执行脚本）；
//   - DNS rebinding 场景 Host 非回环 → 403。
//
// 也就是说：该端点没有放宽威胁模型（令牌防的是浏览器跨站写请求，不是同机
// 其他进程——本机进程本来就能读启动行与页面 meta），只是把"本机同源可读"
// 做成显式的、可脚本化的契约，便于脚本/外部 Agent 免解析启动行。
//
// 令牌默认每个进程随机生成一次（16 字节 hex）、重启即轮换；也可由
// --web-token / AICLI_WEB_TOKEN 显式指定（固定不轮换，响应里的 source
// 字段会写明来源，脚本据此决定能否跨重启复用）。本端点不写日志、不落盘、
// 明确 no-store，且不进入 /debug/endpoints 的 JSON（避免清单被转发/
// 贴进 issue 时连带泄露令牌原文）。
// ============================================================================

// ChatWebAPITokenPath 是写令牌读取端点路径。
const ChatWebAPITokenPath = "/web/api/token"

// chatWebTokenResponse 是 /web/api/token 的响应体。
type chatWebTokenResponse struct {
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
	// Header 是携带令牌的请求头名（X-AICLI-Token），Token 为令牌原文。
	Header string `json:"header,omitempty"`
	Token  string `json:"token,omitempty"`
	// QueryParam 是等价的查询参数名（无法自定义请求头的工具可用）。
	QueryParam string `json:"query_param,omitempty"`
	// Source 是令牌来源：random（每进程随机、重启轮换）/ --web-token /
	// AICLI_WEB_TOKEN（后两者为显式固定值，重启不轮换）。
	Source string `json:"source,omitempty"`
	// Hint 说明适用范围与生命周期。
	Hint string `json:"hint,omitempty"`
}

// HandleChatWebAPIToken 返回当前进程的 Web 写令牌（仅 GET）。
func HandleChatWebAPIToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeWebAPIJSON(w, http.StatusMethodNotAllowed, chatWebTokenResponse{
			Status: "error",
			Reason: "method not allowed: use GET " + ChatWebAPITokenPath,
		})
		return
	}
	// 令牌属于进程内动态秘密，任何缓存层都不得保留。
	w.Header().Set("Cache-Control", "no-store")
	token := strings.TrimSpace(ChatWebAuthToken())
	if token == "" {
		writeWebAPIJSON(w, http.StatusServiceUnavailable, chatWebTokenResponse{
			Status: "error",
			Reason: "web write token not initialized (loopback server not started)",
		})
		return
	}
	source := ChatWebAuthTokenSource()
	hint := "状态变更方法（POST/PUT/PATCH/DELETE）需携带 " + ChatWebAuthTokenHeader +
		" 请求头或 ?token= 查询参数；令牌每进程随机生成、重启即轮换，仅本机回环同源可读"
	if source != "" && source != chatWebAuthTokenSourceRandom {
		hint = "状态变更方法（POST/PUT/PATCH/DELETE）需携带 " + ChatWebAuthTokenHeader +
			" 请求头或 ?token= 查询参数；令牌由 " + source + " 显式指定（固定值、重启不轮换），仅本机回环同源可读"
	}
	writeWebAPIJSON(w, http.StatusOK, chatWebTokenResponse{
		Status:     "ok",
		Header:     ChatWebAuthTokenHeader,
		Token:      token,
		QueryParam: "token",
		Source:     source,
		Hint:       hint,
	})
}
