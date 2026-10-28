package wire

import "strings"

// ============================================================================
// HEARTBEAT_ACK Pending 里的「窗口动作」词汇表（控制台 → 节点执行）。
//
// 放在 wire 层而不是 tunnel/agent 任一侧：这是两端共用的字符串契约，一处定义、
// 两侧引用，才不会漂移出「gateway 发 window:close，agent 只认 win:close」这类
// 静默失配。旧 agent 收到不认识的 Pending 动作一律忽略（control.go 的前向兼容
// 契约），所以 gateway 与 agent 可以不同步升级。
// ============================================================================

// WindowActionPrefix 是 Pending 里窗口动作的固定前缀。
const WindowActionPrefix = "window:"

// 窗口动作取值（closed set）。
const (
	// WindowActionOpenTerminal 在节点所在机器上开一个新的终端窗口并恢复该会话。
	WindowActionOpenTerminal = "open-terminal"
	// WindowActionResume 恢复该会话（无活窗口时与 open-terminal 同径）。
	WindowActionResume = "resume"
	// WindowActionNew 新开一个会话窗口（不带会话 ID）。
	WindowActionNew = "new"
	// WindowActionClose 关闭该会话在本机的窗口进程。
	WindowActionClose = "close"
)

// ValidWindowAction 报告 kind 是否在词汇表内。
func ValidWindowAction(kind string) bool {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case WindowActionOpenTerminal, WindowActionResume, WindowActionNew, WindowActionClose:
		return true
	}
	return false
}

// WindowActionName 渲染 Pending 里的动作字符串。会话级动作带 session_id；
// WindowActionNew 不带（新会话还没有 ID）。
func WindowActionName(kind, sessionID string) string {
	kind = strings.ToLower(strings.TrimSpace(kind))
	sessionID = strings.TrimSpace(sessionID)
	if kind == WindowActionNew || sessionID == "" {
		return WindowActionPrefix + kind
	}
	return WindowActionPrefix + kind + ":" + sessionID
}

// ParseWindowAction 解析一条 Pending 动作。ok=false 表示这不是窗口动作或形态
// 不完整（调用方按「未知动作忽略」处理）。
func ParseWindowAction(pending string) (kind, sessionID string, ok bool) {
	trimmed := strings.TrimSpace(pending)
	if !strings.HasPrefix(trimmed, WindowActionPrefix) {
		return "", "", false
	}
	body := strings.TrimPrefix(trimmed, WindowActionPrefix)
	// 会话 ID 里不含冒号（session_<时间>_<随机>），所以第一个冒号就是分界。
	if index := strings.IndexByte(body, ':'); index >= 0 {
		kind, sessionID = body[:index], body[index+1:]
	} else {
		kind = body
	}
	kind = strings.ToLower(strings.TrimSpace(kind))
	sessionID = strings.TrimSpace(sessionID)
	if !ValidWindowAction(kind) {
		return "", "", false
	}
	if kind != WindowActionNew && sessionID == "" {
		return "", "", false
	}
	return kind, sessionID, true
}
