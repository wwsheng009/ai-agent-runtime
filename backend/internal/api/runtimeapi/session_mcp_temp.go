package runtimeapi

// 会话级 MCP「临时启用」（runtime-server 版）
//
// 背景：配置里 enabled=false 的 server 无法出现在会话工具面（base manager 不建连）。
// 会话面板既有的「本会话停用」只做收窄；本文件补齐对称能力——「本会话启用」：
//   - 用会话生效配置的**内存快照**（目标 server 强制 enabled=true）起一套会话私有
//     manager，只在本会话工具面暴露；不写配置文件、不影响全局连接与其他会话；
//   - 回收：会话内再停用、或服务关闭（CloseSessionMCPTempRuntimes）；名称集合变化
//     时重建（旧连接先关闭，不泄漏）。
//
// 内核与 CLI chat `/mcp enable <name> --session` 同源
// （cmd/aicli/commands/chat_mcp_session_scope.go 的 ScopedManager 方案）：
// manager.ScopedManager.LoadConfigFromConfig + AsyncManager.StartAsync。

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	mcpconfig "github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
	mcpmanager "github.com/wwsheng009/ai-agent-runtime/internal/mcp/manager"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

// sessionMCPEnabledContextKey 是「本会话临时启用」名单的会话元数据键
// （常量宿主在 internal/chat，见 chat.SessionMCPEnabledContextKey）。
const sessionMCPEnabledContextKey = chat.SessionMCPEnabledContextKey

// sessionMCPTempReadyTimeout 是临时连接启动后等待首轮建连的有界时长；
// 超时不报错——迟到工具在后续 ListTools/FindTool 里自然可见。
const sessionMCPTempReadyTimeout = 2 * time.Second

// errRuntimeSessionMCPTempFailed 表示会话级临时启用建连失败（映射 503）。
var errRuntimeSessionMCPTempFailed = fmt.Errorf("session MCP temp runtime failed")

// parseSessionMCPNamesJSON 解析字符串形态的名称清单（历史持久化形态）。
func parseSessionMCPNamesJSON(raw string) []string {
	var parsed []string
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil
	}
	return parsed
}

// sessionMCPContextNames 读取会话元数据里的名称清单（兼容 []string /
// []interface{} / JSON 字符串三种持久化形态），去重排序；空清单返回 nil。
func sessionMCPContextNames(session *chat.Session, key string) []string {
	if session == nil || strings.TrimSpace(key) == "" {
		return nil
	}
	names := sessionMCPNamesFromValue(session, key)
	if len(names) == 0 {
		return nil
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

func sessionMCPNamesFromValue(session *chat.Session, key string) []string {
	raw, ok := session.GetContext(key)
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
		names = append(names, parseSessionMCPNamesJSON(value)...)
	}
	return names
}

// setSessionMCPContextNames 写回名称清单（空清单写空数组，保持键存在语义简单）。
func setSessionMCPContextNames(session *chat.Session, key string, names []string) {
	if session == nil || strings.TrimSpace(key) == "" {
		return
	}
	if len(names) == 0 {
		session.SetContext(key, []string{})
		return
	}
	sorted := append([]string(nil), names...)
	sort.Strings(sorted)
	session.SetContext(key, sorted)
}

// sessionMCPEnabledNames 读取「本会话临时启用」名单。
func sessionMCPEnabledNames(session *chat.Session) []string {
	return sessionMCPContextNames(session, sessionMCPEnabledContextKey)
}

// setSessionMCPEnabledNames 写回「本会话临时启用」名单。
func setSessionMCPEnabledNames(session *chat.Session, names []string) {
	setSessionMCPContextNames(session, sessionMCPEnabledContextKey, names)
}

// sessionMCPNameInList / removeSessionMCPName 是名称清单的小工具。
func sessionMCPNameInList(names []string, name string) bool {
	for _, item := range names {
		if item == name {
			return true
		}
	}
	return false
}

func removeSessionMCPName(names []string, name string) []string {
	if len(names) == 0 {
		return names
	}
	kept := make([]string, 0, len(names))
	for _, item := range names {
		if item != name {
			kept = append(kept, item)
		}
	}
	return kept
}

// normalizeSessionMCPNames 去重排序并过滤空项。
func normalizeSessionMCPNames(names []string) []string {
	seen := make(map[string]struct{}, len(names))
	out := make([]string, 0, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func sameSessionMCPNames(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

// sessionMCPLookupServerConfig 在配置中定位 server（精确优先，大小写不敏感兜底）。
func sessionMCPLookupServerConfig(cfg *mcpconfig.Config, name string) (mcpconfig.MCPConfig, string, bool) {
	name = strings.TrimSpace(name)
	if cfg == nil || name == "" {
		return mcpconfig.MCPConfig{}, "", false
	}
	if server, ok := cfg.MCPServers[name]; ok {
		return server, name, true
	}
	for key, server := range cfg.MCPServers {
		if strings.EqualFold(strings.TrimSpace(key), name) {
			return server, key, true
		}
	}
	return mcpconfig.MCPConfig{}, "", false
}

// sessionMCPTempBuild 是一次临时连接构建的结果。
type sessionMCPTempBuild struct {
	adapter skill.MCPManager
	names   []string
	close   func()
}

// sessionMCPTempBuilder 是「临时连接构建器」的测试注入缝；默认读会话生效配置、
// 构造内存快照 manager 并建连。
var sessionMCPTempBuilder = buildSessionMCPTempRuntime

// buildSessionMCPTempRuntime 读取会话生效配置，把目标 server 快照为 enabled=true，
// 用会话私有 manager（ScopedManager + AsyncManager）建连并返回工具面适配器。
func buildSessionMCPTempRuntime(h *Handler, session *chat.Session, names []string) (*sessionMCPTempBuild, error) {
	if h == nil || session == nil || len(names) == 0 {
		return nil, fmt.Errorf("no MCP to enable for this session")
	}
	base := ""
	if h.workspaceMCP != nil {
		base = sessionWorkspaceDir(session)
	}
	explicit := ""
	if h.workspaceMCP != nil {
		explicit = strings.TrimSpace(h.workspaceMCP.cfg.ExplicitOverride)
	}
	layered, err := mcpconfig.LoadEffectiveFrom(base, explicit)
	if err != nil {
		return nil, fmt.Errorf("加载会话 MCP 配置失败: %w", err)
	}
	if layered == nil || layered.Config == nil {
		return nil, fmt.Errorf("会话 MCP 配置为空")
	}
	cfg := layered.Config
	mcpconfig.ExpandEnv(cfg)

	snapshot := &mcpconfig.Config{MCPServers: map[string]mcpconfig.MCPConfig{}}
	for _, name := range names {
		server, key, ok := sessionMCPLookupServerConfig(cfg, name)
		if !ok {
			continue
		}
		server.Enabled = true
		server.Disabled = false
		if strings.TrimSpace(server.Name) == "" {
			server.Name = strings.TrimSpace(key)
		}
		snapshot.MCPServers[strings.TrimSpace(server.Name)] = server
	}
	if len(snapshot.MCPServers) == 0 {
		return nil, fmt.Errorf("目标 MCP 不在会话配置中: %s", strings.Join(names, ", "))
	}
	snapshot.Global = cfg.Global

	manager := h.newSessionMCPTempManager()
	if manager == nil {
		return nil, fmt.Errorf("MCP manager factory unavailable")
	}
	scoped, ok := manager.(mcpmanager.ScopedManager)
	if !ok || scoped == nil {
		_ = manager.Stop()
		return nil, fmt.Errorf("当前 MCP manager 不支持内存配置快照（ScopedManager）")
	}
	if err := scoped.LoadConfigFromConfig(snapshot); err != nil {
		_ = manager.Stop()
		return nil, fmt.Errorf("加载内存配置快照失败: %w", err)
	}
	started := false
	if async, ok := manager.(mcpmanager.AsyncManager); ok && async != nil {
		if err := async.StartAsync(h.runtimeContext()); err != nil {
			_ = manager.Stop()
			return nil, fmt.Errorf("启动临时连接失败: %w", err)
		}
		started = true
		// 有界等待首轮建连，让「启用后立即查看工具」可用；超时不算失败。
		waitCtx, cancel := context.WithTimeout(h.runtimeContext(), sessionMCPTempReadyTimeout)
		_ = async.WaitReady(waitCtx)
		cancel()
	}
	if !started {
		_ = manager.Stop()
		return nil, fmt.Errorf("当前 MCP manager 不支持异步启动（AsyncManager）")
	}
	adapter := h.wrapSessionMCPTempManager(manager)
	if adapter == nil {
		_ = manager.Stop()
		return nil, fmt.Errorf("MCP 适配器构造失败")
	}
	build := &sessionMCPTempBuild{
		adapter: adapter,
		names:   normalizeSessionMCPNames(names),
		close: func() {
			if closer, ok := adapter.(interface{ Close() error }); ok && closer != nil {
				_ = closer.Close()
			}
			_ = manager.Stop()
		},
	}
	return build, nil
}

// runtimeContext 返回 handler 的常驻上下文（缺省 Background）。
func (h *Handler) runtimeContext() context.Context {
	if h != nil && h.workspaceMCP != nil && h.workspaceMCP.ctx != nil {
		return h.workspaceMCP.ctx
	}
	return context.Background()
}

// newSessionMCPTempManager 复用工作区 manager 工厂（保证与全局/工作区同一条
// 构造与观测链路），未注入时退回裸 manager。
func (h *Handler) newSessionMCPTempManager() mcpmanager.Manager {
	if h != nil && h.workspaceMCP != nil && h.workspaceMCP.cfg.NewManager != nil {
		return h.workspaceMCP.cfg.NewManager()
	}
	return mcpmanager.NewManager()
}

// wrapSessionMCPTempManager 复用工作区适配器工厂；未注入时退回 skill 适配器。
func (h *Handler) wrapSessionMCPTempManager(m mcpmanager.Manager) skill.MCPManager {
	if h != nil && h.workspaceMCP != nil && h.workspaceMCP.cfg.Wrap != nil {
		return h.workspaceMCP.cfg.Wrap(m)
	}
	return skill.NewMCPAdapter(m)
}

// ---- 会话私有运行时的存储与生命周期 ----

type sessionMCPTempEntry struct {
	build *sessionMCPTempBuild
}

// sessionMCPTempEntryFor 读取会话的临时运行时（不构建）。
func (h *Handler) sessionMCPTempEntryFor(sessionID string) *sessionMCPTempEntry {
	if h == nil {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	h.sessionMCPTempMu.Lock()
	defer h.sessionMCPTempMu.Unlock()
	if h.sessionMCPTemp == nil {
		return nil
	}
	return h.sessionMCPTemp[sessionID]
}

// setSessionMCPTempEntry 写入并返回旧条目（调用方负责关闭旧条目）。
func (h *Handler) setSessionMCPTempEntry(sessionID string, entry *sessionMCPTempEntry) *sessionMCPTempEntry {
	if h == nil {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	h.sessionMCPTempMu.Lock()
	defer h.sessionMCPTempMu.Unlock()
	if h.sessionMCPTemp == nil {
		h.sessionMCPTemp = make(map[string]*sessionMCPTempEntry)
	}
	old := h.sessionMCPTemp[sessionID]
	h.sessionMCPTemp[sessionID] = entry
	return old
}

// deleteSessionMCPTempEntry 删除并返回旧条目（调用方负责关闭）。
func (h *Handler) deleteSessionMCPTempEntry(sessionID string) *sessionMCPTempEntry {
	if h == nil {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	h.sessionMCPTempMu.Lock()
	defer h.sessionMCPTempMu.Unlock()
	if h.sessionMCPTemp == nil {
		return nil
	}
	old := h.sessionMCPTemp[sessionID]
	delete(h.sessionMCPTemp, sessionID)
	return old
}

func closeSessionMCPTempEntry(entry *sessionMCPTempEntry) {
	if entry == nil || entry.build == nil || entry.build.close == nil {
		return
	}
	entry.build.close()
}

// refreshSessionMCPTempRuntime 把会话的临时运行时对齐到 names：
// 空清单直接回收；否则重建（旧连接先行关闭）。
// 供显式启用/停用路径使用（错误会返回给调用方）；工具面的惰性路径见
// sessionMCPTempAdapter。
func (h *Handler) refreshSessionMCPTempRuntime(ctx context.Context, session *chat.Session, names []string) error {
	if h == nil || session == nil {
		return fmt.Errorf("session unavailable")
	}
	normalized := normalizeSessionMCPNames(names)
	sessionID := strings.TrimSpace(session.ID)
	if len(normalized) == 0 {
		closeSessionMCPTempEntry(h.deleteSessionMCPTempEntry(sessionID))
		return nil
	}
	build, err := sessionMCPTempBuilder(h, session, normalized)
	if err != nil {
		return fmt.Errorf("%w: %v", errRuntimeSessionMCPTempFailed, err)
	}
	old := h.setSessionMCPTempEntry(sessionID, &sessionMCPTempEntry{build: build})
	closeSessionMCPTempEntry(old)
	return nil
}

// sessionMCPTempAdapter 是工具面的惰性入口：名称集合未变化直接复用；变化时重建
// （失败静默返回 nil，显式启用路径已负责报错与提示）。
func (h *Handler) sessionMCPTempAdapter(ctx context.Context, session *chat.Session, names []string) skill.MCPManager {
	if h == nil || session == nil {
		return nil
	}
	normalized := normalizeSessionMCPNames(names)
	if len(normalized) == 0 {
		closeSessionMCPTempEntry(h.deleteSessionMCPTempEntry(strings.TrimSpace(session.ID)))
		return nil
	}
	sessionID := strings.TrimSpace(session.ID)
	if entry := h.sessionMCPTempEntryFor(sessionID); entry != nil && entry.build != nil &&
		sameSessionMCPNames(entry.build.names, normalized) {
		return entry.build.adapter
	}
	build, err := sessionMCPTempBuilder(h, session, normalized)
	if err != nil {
		return nil
	}
	old := h.setSessionMCPTempEntry(sessionID, &sessionMCPTempEntry{build: build})
	closeSessionMCPTempEntry(old)
	return build.adapter
}

// CloseSessionMCPTempRuntimes 停止全部会话级临时连接（服务关闭时调用；幂等）。
func (h *Handler) CloseSessionMCPTempRuntimes() {
	if h == nil {
		return
	}
	h.sessionMCPTempMu.Lock()
	entries := make([]*sessionMCPTempEntry, 0, len(h.sessionMCPTemp))
	for sessionID, entry := range h.sessionMCPTemp {
		entries = append(entries, entry)
		delete(h.sessionMCPTemp, sessionID)
	}
	h.sessionMCPTempMu.Unlock()
	for _, entry := range entries {
		closeSessionMCPTempEntry(entry)
	}
}

// sessionMCPTempToolEntries 返回「本会话临时启用」连接中某个 server 的工具
// 清单条目（全局/会话工具端点共用的 JSON 形态；该 server 配置停用、仅本会话
// 可见，因此 configured_enabled=false）。
func (h *Handler) sessionMCPTempToolEntries(ctx context.Context, session *chat.Session, name string) []runtimeMCPToolEntry {
	if h == nil || session == nil {
		return nil
	}
	name = strings.TrimSpace(name)
	names := sessionMCPEnabledNames(session)
	if name == "" || !sessionMCPNameInList(names, name) {
		return nil
	}
	adapter := h.sessionMCPTempAdapter(ctx, session, names)
	if adapter == nil {
		return nil
	}
	infos := adapter.ListTools()
	entries := make([]runtimeMCPToolEntry, 0, len(infos))
	for _, info := range infos {
		if !strings.EqualFold(strings.TrimSpace(info.MCPName), name) {
			continue
		}
		toolName := strings.TrimSpace(info.Name)
		if toolName == "" {
			continue
		}
		entries = append(entries, runtimeMCPToolEntry{
			Name:              toolName,
			Description:       strings.TrimSpace(info.Description),
			Enabled:           true,
			ConfiguredEnabled: false,
			Healthy:           info.Enabled,
			InputSchema:       info.InputSchema,
		})
	}
	return entries
}

// mergeSessionMCPTempToolEntries 按工具名去重合并临时连接条目（不改动既有顺序）。
func mergeSessionMCPTempToolEntries(base []runtimeMCPToolEntry, extra []runtimeMCPToolEntry) []runtimeMCPToolEntry {
	if len(extra) == 0 {
		return base
	}
	seen := make(map[string]struct{}, len(base))
	for _, tool := range base {
		seen[strings.ToLower(strings.TrimSpace(tool.Name))] = struct{}{}
	}
	for _, tool := range extra {
		key := strings.ToLower(strings.TrimSpace(tool.Name))
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		base = append(base, tool)
	}
	return base
}

// dropSessionMCPTempName 从临时启用名单移除一个 server 并回收/重建运行时，
// 用于「写配置启用」路径的清理（避免持久化启用后与临时连接重复）。
func (h *Handler) dropSessionMCPTempName(ctx context.Context, sessionID, name string) {
	if h == nil || strings.TrimSpace(name) == "" {
		return
	}
	session := h.sessionByID(ctx, sessionID)
	if session == nil {
		return
	}
	names := sessionMCPEnabledNames(session)
	if !sessionMCPNameInList(names, name) {
		return
	}
	next := removeSessionMCPName(names, name)
	if err := h.refreshSessionMCPTempRuntime(ctx, session, next); err != nil {
		return
	}
	setSessionMCPEnabledNames(session, next)
	updateCtx, cancel := context.WithTimeout(context.Background(), sessionStoreQueryTimeout)
	defer cancel()
	_ = h.sessionManager.Update(updateCtx, session)
}
