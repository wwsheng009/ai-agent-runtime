package runtimeapi

import (
	"context"
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
//   - enable：对配置启用的 server 清除本会话的停用覆盖（恢复全局可见性）；
//     对配置停用的 server 走会话私有**临时连接**（内存配置快照，不写配置文件，
//     见 session_mcp_temp.go；会话结束或本会话停用后回收）。
//
// 覆盖存放在会话元数据（Metadata.Context）中随会话持久化；工具列表与工具执行
// 两条链路统一经 sessionScopedMCPManager 过滤。包装器在每次调用时动态读取覆盖
// （而不是构造期快照），因此在驻 actor 无需重建即可看到启停变更。
// 常量宿主在 internal/chat：chat actor 的回合末持久化需要按 store 最新值
// 合并同名的覆盖键（见 chat.SessionMCPDisabledContextKey 的注释）。
const sessionMCPDisabledContextKey = chat.SessionMCPDisabledContextKey

var (
	// errRuntimeSessionMCPNotFound 表示目标 server 不在（全局）配置中。
	errRuntimeSessionMCPNotFound = stderrors.New("session mcp not found")
)

// sessionMCPDisabledNames 读取会话的停用清单（兼容 []string / []interface{} /
// JSON 字符串三种持久化形态），去重排序后返回；无覆盖时返回 nil。
func sessionMCPDisabledNames(session *chat.Session) []string {
	return sessionMCPContextNames(session, sessionMCPDisabledContextKey)
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
	out := make([]skill.ToolInfo, 0, len(infos)+2)
	for _, info := range infos {
		if sessionMCPNameDisabled(disabled, info.MCPName) {
			continue
		}
		out = append(out, info)
	}
	// 合并「本会话临时启用」的私有连接工具（配置停用、仅本会话可见）。
	if temp := m.tempAdapter(); temp != nil {
		for _, info := range temp.ListTools() {
			if sessionMCPNameDisabled(disabled, info.MCPName) {
				continue
			}
			if sessionMCPToolPresent(out, info) {
				continue
			}
			out = append(out, info)
		}
	}
	return out
}

// sessionMCPToolPresent 按 (MCPName, Name) 去重（大小写不敏感）。
func sessionMCPToolPresent(infos []skill.ToolInfo, candidate skill.ToolInfo) bool {
	for _, info := range infos {
		if strings.EqualFold(strings.TrimSpace(info.MCPName), strings.TrimSpace(candidate.MCPName)) &&
			strings.EqualFold(strings.TrimSpace(info.Name), strings.TrimSpace(candidate.Name)) {
			return true
		}
	}
	return false
}

// tempAdapter 返回本会话的临时连接视图（按当前「临时启用」名单惰性构建/复用）。
func (m *sessionScopedMCPManager) tempAdapter() skill.MCPManager {
	if m == nil || m.handler == nil {
		return nil
	}
	session := m.handler.sessionByID(context.Background(), m.sessionID)
	if session == nil {
		return nil
	}
	names := sessionMCPEnabledNames(session)
	if len(names) == 0 {
		return nil
	}
	return m.handler.sessionMCPTempAdapter(context.Background(), session, names)
}

// tempServes 报告 mcpName 是否属于本会话的临时连接（大小写不敏感）。
func (m *sessionScopedMCPManager) tempServes(name string) bool {
	if m == nil || m.handler == nil {
		return false
	}
	session := m.handler.sessionByID(context.Background(), m.sessionID)
	for _, item := range sessionMCPEnabledNames(session) {
		if strings.EqualFold(strings.TrimSpace(item), strings.TrimSpace(name)) {
			return true
		}
	}
	return false
}

func (m *sessionScopedMCPManager) FindTool(toolName string) (skill.ToolInfo, error) {
	if m == nil || m.next == nil {
		return skill.ToolInfo{}, fmt.Errorf("MCP manager unavailable")
	}
	info, err := m.next.FindTool(toolName)
	if err == nil {
		if sessionMCPNameDisabled(m.disabledSet(), info.MCPName) {
			return skill.ToolInfo{}, fmt.Errorf("MCP server %q is disabled for this session", strings.TrimSpace(info.MCPName))
		}
		return info, nil
	}
	if temp := m.tempAdapter(); temp != nil {
		if tempInfo, tempErr := temp.FindTool(toolName); tempErr == nil {
			if sessionMCPNameDisabled(m.disabledSet(), tempInfo.MCPName) {
				return skill.ToolInfo{}, fmt.Errorf("MCP server %q is disabled for this session", strings.TrimSpace(tempInfo.MCPName))
			}
			return tempInfo, nil
		}
	}
	return info, err
}

func (m *sessionScopedMCPManager) CallTool(ctx interface{}, mcpName, toolName string, args map[string]interface{}) (interface{}, error) {
	if m == nil || m.next == nil {
		return nil, fmt.Errorf("MCP manager unavailable")
	}
	if sessionMCPNameDisabled(m.disabledSet(), mcpName) {
		return nil, fmt.Errorf("MCP server %q is disabled for this session", strings.TrimSpace(mcpName))
	}
	if m.tempServes(mcpName) {
		if temp := m.tempAdapter(); temp != nil {
			return temp.CallTool(ctx, mcpName, toolName, args)
		}
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

	disabledNames := sessionMCPDisabledNames(session)
	enabledNames := sessionMCPEnabledNames(session)
	hasDisabled := sessionMCPNameInList(disabledNames, name)
	hasTemp := sessionMCPNameInList(enabledNames, name)

	if enabled {
		result.State = ""
		if globallyEnabled {
			// 配置已启用：清除会话停用覆盖；顺带清理历史临时项（配置改动后的残留）。
			nextTemp := removeSessionMCPName(enabledNames, name)
			if len(nextTemp) != len(enabledNames) {
				if err := h.refreshSessionMCPTempRuntime(ctx, session, nextTemp); err != nil {
					return result, err
				}
				setSessionMCPEnabledNames(session, nextTemp)
				result.Changed = true
			}
			if hasDisabled {
				setSessionMCPDisabledNames(session, removeSessionMCPName(disabledNames, name))
				result.Changed = true
			}
			if result.Changed {
				result.Message = fmt.Sprintf("已恢复 MCP %q 的会话级启用", name)
			} else {
				result.Message = fmt.Sprintf("MCP %q 已启用，本会话无需恢复", name)
			}
			return h.finishSessionMCPToggle(ctx, session, result)
		}
		// 配置停用：本会话临时启用（内存连接，不写配置文件）。
		if hasTemp {
			result.State = "enabled"
			result.Message = fmt.Sprintf("MCP %q 已在本会话临时启用", name)
			return h.finishSessionMCPToggle(ctx, session, result)
		}
		nextTemp := append(append([]string(nil), enabledNames...), name)
		if err := h.refreshSessionMCPTempRuntime(ctx, session, nextTemp); err != nil {
			return result, err
		}
		setSessionMCPEnabledNames(session, nextTemp)
		if hasDisabled {
			setSessionMCPDisabledNames(session, removeSessionMCPName(disabledNames, name))
		}
		result.State = "enabled"
		result.Changed = true
		result.Message = fmt.Sprintf("已在本会话临时启用 MCP %q（内存连接，不写配置文件；会话结束或本会话停用后回收）", name)
		return h.finishSessionMCPToggle(ctx, session, result)
	}

	result.State = "disabled"
	switch {
	case hasTemp:
		nextTemp := removeSessionMCPName(enabledNames, name)
		if err := h.refreshSessionMCPTempRuntime(ctx, session, nextTemp); err != nil {
			return result, err
		}
		setSessionMCPEnabledNames(session, nextTemp)
		result.Changed = true
		result.Message = fmt.Sprintf("已停用 MCP %q 的本会话临时启用（连接已回收）", name)
	case hasDisabled:
		result.Message = fmt.Sprintf("MCP %q 在当前会话已是停用状态", name)
	case !globallyEnabled:
		result.Message = fmt.Sprintf("MCP %q 当前为停用状态（可在本会话临时启用）", name)
	default:
		setSessionMCPDisabledNames(session, append(disabledNames, name))
		result.Changed = true
		result.Message = fmt.Sprintf("已在当前会话停用 MCP %q（全局连接与其他会话不受影响）", name)
	}
	return h.finishSessionMCPToggle(ctx, session, result)
}

// finishSessionMCPToggle 持久化变更并失效工具面缓存；未变更直接返回。
func (h *Handler) finishSessionMCPToggle(ctx context.Context, session *chat.Session, result runtimeSessionMCPToggleResult) (runtimeSessionMCPToggleResult, error) {
	if !result.Changed {
		return result, nil
	}
	if session == nil {
		return result, fmt.Errorf("session unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	updateCtx, cancel := context.WithTimeout(ctx, sessionStoreQueryTimeout)
	defer cancel()
	if err := h.sessionManager.Update(updateCtx, session); err != nil {
		return result, err
	}
	h.invalidateSessionMCPRuntimeSurface(strings.TrimSpace(session.ID))
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
