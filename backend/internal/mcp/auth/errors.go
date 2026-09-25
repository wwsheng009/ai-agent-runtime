// Package auth 实现 MCP 远程服务器的 OAuth 2.0（Authorization Code + PKCE）
// 登录、令牌持久化与自动刷新。
//
// 设计要点（见 docs/analysis/commandcode-mcp-design-borrowing-20260925.md §4.4）：
//   - 授权码 + PKCE(S256)，公共客户端默认走动态客户端注册（RFC 7591）；
//   - 发现链：401 WWW-Authenticate → RFC 9728 受保护资源 metadata → RFC 8414 授权服务器 metadata；
//   - 令牌存 ~/.aicli/mcp-tokens.json（0600），按 server 名索引并校验 URL 未变；
//   - 浏览器打不开时打印可复制 URL，并支持手动粘贴回调 URL / code（win7compat 构建始终走该路径）；
//   - 任何失败都给出可行动提示：改用 headers 手动配置静态凭证。
package auth

import (
	"errors"
	"fmt"
)

var (
	// ErrNotAuthenticated 表示尚未完成 OAuth 授权（无可用令牌）。
	ErrNotAuthenticated = errors.New("尚未完成 OAuth 授权")
	// ErrRefreshFailed 表示刷新令牌失败，通常需要重新授权。
	ErrRefreshFailed = errors.New("刷新 OAuth 令牌失败")
	// ErrUnsupported 表示当前构建/环境不支持自动 OAuth（应回退到 headers 手动配置）。
	ErrUnsupported = errors.New("当前环境不支持自动 OAuth")
)

// NeedsAuthError 表示远端以 401/403 拒绝了当前凭证。
type NeedsAuthError struct {
	Server string
	Reason string
	Status int
}

func (e *NeedsAuthError) Error() string {
	if e == nil {
		return "需要认证"
	}
	reason := e.Reason
	if reason == "" {
		reason = "访问令牌被拒绝"
	}
	if e.Server != "" {
		return fmt.Sprintf("%s (MCP: %s)", reason, e.Server)
	}
	return reason
}

func (e *NeedsAuthError) Unwrap() error { return ErrNotAuthenticated }

// IsNeedsAuth 判断错误链是否代表「需要重新认证」。
func IsNeedsAuth(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, ErrNotAuthenticated) || errors.Is(err, ErrRefreshFailed)
}

// manualFallbackHint 统一的兜底提示：自动 OAuth 不可用时如何手动配置。
const manualFallbackHint = "可在 MCP 配置的 headers 中手动设置 Authorization（如 `headers: { Authorization: \"Bearer <token>\" }`）"
