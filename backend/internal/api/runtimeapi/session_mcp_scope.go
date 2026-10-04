package runtimeapi

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"sort"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

// 会话级 MCP 覆盖（runtime-server 版）
//
// 语义与 CLI `/mcp enable|disable <name> --session`、chat web
// `POST /web/api/mcps/{name}/enable|disable?scope=session` 对齐：
//   - disable：只从"本会话"的工具面/执行面撤销该 server 的工具，不写配置文件、
//     不影响全局连接与其他会话；
//   - enable：清除本会话的停用覆盖（恢复全局可见性）。
//
// 覆盖存放在会话元数据（Metadata.Context）中随会话持久化；工具列表与工具执行
// 两条链路统一经 sessionScopedMCPManager 过滤。包装器在每次调用时动态读取覆盖
// （而不是构造期快照），因此在驻 actor 无需重建即可看到启停变更。
const sessionMCPDisabledContextKey = "runtime_mcp_session_disabled"

var (
	// errRuntimeSessionMCPNotFound 表示目标 server 不在（全局）配置中。
	errRuntimeSessionMCPNotFound = stderrors.New("session mcp not found")
	// errRuntimeSessionMCPTempUnsupported 表示对全局停用的 server 请求会话级启用：
	// runtime-server 暂不支持会话私有临时连接（CLI 的 ScopedManager 方案）。
	errRuntimeSessionMCPTempUnsupported = stderrors.New(
		"runtime-server 暂不支持会话级临时连接：请先启用该 MCP（全局：POST /api/runtime/mcps/{name}/enable；工作区：POST /api/runtime/sessions/{id}/runtime/mcps/{name}/enable?scope=workspace）")
)

// sessionMCPDisabledNames 读取会话的停用清单（兼容 []string / []interface{} /
// JSON 字符串三种持久化形态），去重排序后返回；无覆盖时返回 nil。
func sessionMCPDisabledNames(session *chat.Session) []string {
	if session == nil {
		return nil
	}
	raw, ok := session.GetContext(sessionMCPDisabledContextKey)
	if !ok || raw == nil {
		return nil
	}
	names := make([]string, 0, 4)
	switch value := raw.(type) {
	case []string:
		names = append(names, value...)
	case []interface{}:
		for _, item := range value {
			if text, ok := item.(string); ok {
				names = append(names, text)
			}
		}
	case string:
		var parsed []string
		if err := json.Unmarshal([]byte(value), &parsed); err == nil {
			names = append(names, parsed...)
		}
	}
	cleaned := make([]string, 0, len(names))
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		cleaned = append(cleaned, name)
	}
	if len(cleaned) == 0 {
		return nil
	}
	sort.Strings(cleaned)
	return cleaned
}

// setSessionMCPDisabledNames 写回停用清单（空清单写空数组，保持键存在语义简单）。
func setSessionMCPDisabledNames(session *chat.Session, names []string) {
	if session == nil {
		return
	}
	if len(names) == 0 {
		session.SetContext(sessionMCPDisabledContextKey, []string{})
		return
	}
	sorted := append([]string(nil), names...)
	sort.Strings(sorted)
	session.SetContext(sessionMCPDisabledContextKey, sorted)
}

// sessionByID 读取会话；失败返回 nil（调用方按"无会话"处理）。
func (h *Handler) sessionByID(ctx context.Context, sessionID string) *chat.Session {
	if h == nil || h.sessionManager == nil {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	queryCtx, cancel := context.WithTimeout(ctx, sessionStoreQueryTimeout)
	defer cancel()
	session, err := h.sessionManager.Get(queryCtx, sessionID)
	if err != nil {
		return nil
	}
	return session
}

// sessionScopedMCPManager 是某会话的 skill.MCPManager 视图：按会话级覆盖过滤
// ListTools / FindTool / CallTool。next 为共享/全局 manager（或 profile adapter），
// 组合而非包装其生命周期。
type sessionScopedMCPManager struct {
	handler   *Handler
	sessionID string
	next      skill.MCPManager
}

var _ skill.MCPManager = (*sessionScopedMCPManager)(nil)

// sessionScopedMCPSurface 给 next 叠加会话级覆盖视图；sessionID 为空或 next 为
// nil 时原样返回（保持无会话链路行为不变）。
func (h *Handler) sessionScopedMCPSurface(ctx context.Context, sessionID string, next skill.MCPManager) skill.MCPManager {
	if h == nil || next == nil {
		return next
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return next
	}
	return &sessionScopedMCPManager{handler: h, sessionID: sessionID, next: next}
}

func (m *sessionScopedMCPManager) disabledSet() map[string]struct{} {
	if m == nil || m.handler == nil {
		return nil
	}
	names := sessionMCPDisabledNames(m.handler.sessionByID(context.Background(), m.sessionID))
	if len(names) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(names))
	for _, name := range names {
		set[name] = struct{}{}
	}
	return set
}

func sessionMCPNameDisabled(disabled map[string]struct{}, name string) bool {
	if len(disabled) == 0 {
		return false
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	_, exists := disabled[name]
	return exists
}

func (m *sessionScopedMCPManager) ListTools() []skill.ToolInfo {
	if m == nil || m.next == nil {
		return nil
	}
	infos := m.next.ListTools()
	disabled := m.disabledSet()
	if len(disabled) == 0 {
		return infos
	}
	out := make([]skill.ToolInfo, 0, len(infos))
	for _, info := range infos {
		if sessionMCPNameDisabled(disabled, info.MCPName) {
			continue
		}
		out = append(out, info)
	}
	return out
}

func (m *sessionScopedMCPManager) FindTool(toolName string) (skill.ToolInfo, error) {
	if m == nil || m.next == nil {
		return skill.ToolInfo{}, fmt.Errorf("MCP manager unavailable")
	}
	info, err := m.next.FindTool(toolName)
	if err != nil {
		return info, err
	}
	if sessionMCPNameDisabled(m.disabledSet(), info.MCPName) {
		return skill.ToolInfo{}, fmt.Errorf("MCP server %q is disabled for this session", strings.TrimSpace(info.MCPName))
	}
	return info, nil
}

func (m *sessionScopedMCPManager) CallTool(ctx interface{}, mcpName, toolName string, args map[string]interface{}) (interface{}, error) {
	if m == nil || m.next == nil {
		return nil, fmt.Errorf("MCP manager unavailable")
	}
	if sessionMCPNameDisabled(m.disabledSet(), mcpName) {
		return nil, fmt.Errorf("MCP server %q is disabled for this session", strings.TrimSpace(mcpName))
	}
	return m.next.CallTool(ctx, mcpName, toolName, args)
}

// runtimeMCPServerState 报告 server 是否存在与其全局启用状态：优先管理面配置，
// 回退运行时 manager 状态。返回 (false, false) 表示不存在（或不可判定）。
func (h *Handler) runtimeMCPServerState(ctx context.Context, name string) (bool, bool) {
	if h == nil {
		return false, false
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return false, false
	}
	if h.mcpAdmin != nil {
		if ctx == nil {
			ctx = context.Background()
		}
		queryCtx, cancel := context.WithTimeout(ctx, sessionStoreQueryTimeout)
		defer cancel()
		cfg, err := h.mcpAdmin.Get(queryCtx, name)
		if err == nil && cfg != nil {
			enabled := cfg.Enabled
			if cfg.Disabled {
				enabled = false
			}
			return true, enabled
		}
		return false, false
	}
	if manager := h.runtimeMCPManager(); manager != nil {
		if reader, ok := manager.(mcpStatusReader); ok && reader != nil {
			for _, status := range reader.ListMCPs() {
				if status == nil {
					continue
				}
				if strings.EqualFold(strings.TrimSpace(status.Name), name) {
					return true, status.Enabled
				}
			}
		}
	}
	return false, false
}

// runtimeSessionMCPToggleResult 是会话级启停的落库结果。
type runtimeSessionMCPToggleResult struct {
	Changed bool
	State   string // "disabled" / ""（enabled）
	Message string
}

// applyRuntimeSessionMCPToggle 应用一次会话级启停并持久化到会话元数据。
// 返回 errRuntimeSessionMCPNotFound / errRuntimeSessionMCPTempUnsupported 供上层映射状态码。
func (h *Handler) applyRuntimeSessionMCPToggle(ctx context.Context, sessionID, name string, enabled bool) (runtimeSessionMCPToggleResult, error) {
	result := runtimeSessionMCPToggleResult{}
	if h == nil || h.sessionManager == nil {
		return result, fmt.Errorf("session manager unavailable")
	}
	name = strings.TrimSpace(name)
	session := h.sessionByID(ctx, sessionID)
	if session == nil {
		return result, fmt.Errorf("%w: session %q", errRuntimeSessionMCPNotFound, sessionID)
	}

	exists, globallyEnabled := h.sessionEffectiveMCPServerState(ctx, session, name)
	if !exists {
		return result, fmt.Errorf("%w: MCP %q", errRuntimeSessionMCPNotFound, name)
	}

	names := sessionMCPDisabledNames(session)
	has := false
	for _, item := range names {
		if item == name {
			has = true
			break
		}
	}

	if enabled {
		if !globallyEnabled {
			return result, errRuntimeSessionMCPTempUnsupported
		}
		result.State = ""
		if has {
			kept := make([]string, 0, len(names))
			for _, item := range names {
				if item != name {
					kept = append(kept, item)
				}
			}
			setSessionMCPDisabledNames(session, kept)
			result.Changed = true
			result.Message = fmt.Sprintf("已恢复 MCP %q 的会话级启用", name)
		} else {
			result.Message = fmt.Sprintf("MCP %q 全局已启用，本会话无需恢复", name)
		}
	} else {
		result.State = "disabled"
		if has {
			result.Message = fmt.Sprintf("MCP %q 在当前会话已是停用状态", name)
		} else {
			setSessionMCPDisabledNames(session, append(names, name))
			result.Changed = true
			result.Message = fmt.Sprintf("已在当前会话停用 MCP %q（全局连接与其他会话不受影响）", name)
		}
	}

	if !result.Changed {
		return result, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	updateCtx, cancel := context.WithTimeout(ctx, sessionStoreQueryTimeout)
	defer cancel()
	if err := h.sessionManager.Update(updateCtx, session); err != nil {
		return result, err
	}
	h.invalidateSessionMCPRuntimeSurface(sessionID)
	return result, nil
}

// invalidateSessionMCPRuntimeSurface 让会话级 MCP 变更在下一个 turn 边界生效：
// 清持久化稳定工具面缓存，并让在驻 actor 丢弃其冻结工具前缀（在途 turn 由
// turn 内快照保护，不被打断）。
func (h *Handler) invalidateSessionMCPRuntimeSurface(sessionID string) {
	if h == nil {
		return
	}
	h.invalidateSessionRuntimeToolSurfaces()
	hub := h.peekSessionHub()
	sessionID = strings.TrimSpace(sessionID)
	if hub == nil || sessionID == "" {
		return
	}
	actor, ok := hub.Get(sessionID)
	if !ok || actor == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), sessionStoreQueryTimeout)
	defer cancel()
	_ = actor.InvalidateStableToolSurface(ctx)
}
