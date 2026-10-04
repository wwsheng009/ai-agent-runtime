package commands

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/functions"
	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	mcpconfig "github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/manager"
	runtimetools "github.com/wwsheng009/ai-agent-runtime/internal/tools"
)

// chat_mcp_session_scope.go 实现「仅本会话」的 MCP 启停覆盖：
//
//   - 覆盖表（ChatSession.MCPSessionOverrides）只驻留内存，/mcp enable|disable
//     <name> --session 驱动，绝不写 mcp.yaml；
//   - 全局启用的 server：停用=仅从本会话工具面撤销并隐藏（不动连接与全局配置）；
//   - 全局停用的 server：启用=会话私有 manager 的临时连接（内存配置快照，
//     不落盘；会话结束或 /new 时回收）；
//   - 所有路径都做「撤销已注册函数 + 失效稳定工具面」：只挡新注册不撤销已注册
//     会造成假开关（对齐 per-skill 启停的收口方式）。
//
// 语义边界：会话级覆盖只能收窄或恢复全局可见面，不能绕过 profile 的 server
// 选择裁剪（被 profile 排除的 server 不因 --session 启用而旁路）。

// sessionMCPTempReadyTimeout 是临时连接启动后等待首轮建连的有界时长。
// 超时不报错：迟到工具由 turn 边界的 refreshSessionScopedMCP 增量补登记。
const sessionMCPTempReadyTimeout = 3 * time.Second

// 测试注入缝：默认走真实的分层配置读取与 manager 构造。
var (
	sessionMCPConfigLookupFrom   = loadMCPConfigLayeredFrom
	sessionMCPTempManagerFactory = func() manager.Manager { return manager.NewManager() }
)

// sessionMCPOverrideLookup 读取会话覆盖；第二个返回值表示是否存在覆盖。
func sessionMCPOverrideLookup(session *ChatSession, name string) (enabled bool, ok bool) {
	if session == nil || len(session.MCPSessionOverrides) == 0 {
		return false, false
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return false, false
	}
	if enabled, exists := session.MCPSessionOverrides[name]; exists {
		return enabled, true
	}
	for key, value := range session.MCPSessionOverrides {
		if strings.EqualFold(strings.TrimSpace(key), name) {
			return value, true
		}
	}
	return false, false
}

// sessionMCPOverrideDisabled 报告该 server 是否被本会话显式停用。
func sessionMCPOverrideDisabled(session *ChatSession, name string) bool {
	enabled, ok := sessionMCPOverrideLookup(session, name)
	return ok && !enabled
}

func setSessionMCPOverride(session *ChatSession, name string, enabled bool) {
	if session == nil {
		return
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	if session.MCPSessionOverrides == nil {
		session.MCPSessionOverrides = map[string]bool{}
	}
	session.MCPSessionOverrides[name] = enabled
}

func deleteSessionMCPOverride(session *ChatSession, name string) {
	if session == nil || len(session.MCPSessionOverrides) == 0 {
		return
	}
	name = strings.TrimSpace(name)
	delete(session.MCPSessionOverrides, name)
	for key := range session.MCPSessionOverrides {
		if strings.EqualFold(strings.TrimSpace(key), name) {
			delete(session.MCPSessionOverrides, key)
		}
	}
}

// sessionMCPLookupServer 在合并配置中定位 server（精确优先，大小写不敏感兜底）。
func sessionMCPLookupServer(cfg *mcpconfig.Config, name string) (string, *mcpconfig.MCPConfig, bool) {
	if cfg == nil {
		return "", nil, false
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "", nil, false
	}
	if server, ok := cfg.MCPServers[name]; ok {
		copied := server
		return name, &copied, true
	}
	for key, server := range cfg.MCPServers {
		if strings.EqualFold(strings.TrimSpace(key), name) {
			copied := server
			return key, &copied, true
		}
	}
	return "", nil, false
}

// sessionMCPGlobalEnabled 报告 server 在全局层是否处于启用态：
// 以运行中 manager 的状态为准，manager 不可用/未知时回退配置。
func sessionMCPGlobalEnabled(name string, serverCfg *mcpconfig.MCPConfig) bool {
	if MCPManagerInstance != nil {
		if status, err := MCPManagerInstance.GetMCPStatus(name); err == nil && status != nil {
			return status.Enabled
		}
	}
	if serverCfg != nil {
		return serverCfg.IsEnabled()
	}
	return false
}

// mcpServerAllowedByProfile 按会话绑定的 profile 选择（只收窄）判断 server 是否
// 可被会话级启用：allowlist 存在时必须在名单内；denylist 命中即排除。
func mcpServerAllowedByProfile(session *ChatSession, name string) bool {
	if session == nil {
		return true
	}
	key := strings.ToLower(strings.TrimSpace(name))
	if key == "" {
		return true
	}
	if use := session.ProfileMCPSelection.UseServers; len(use) > 0 {
		allowed := false
		for _, item := range use {
			if strings.EqualFold(strings.TrimSpace(item), key) {
				allowed = true
				break
			}
		}
		if !allowed {
			return false
		}
	}
	for _, item := range session.ProfileMCPSelection.ExcludeServers {
		if strings.EqualFold(strings.TrimSpace(item), key) {
			return false
		}
	}
	return true
}

// mcpFunctionServer 返回函数所属的 MCP server 名：空串表示非 MCP 函数。
// 覆盖两种注册形态：直注册的 *MCPFunction 与 runtime-tool 包装
// （metadata.mcp_name，见 internal/tools 的 withMCPIdentity）。
func mcpFunctionServer(fn functions.Function) string {
	switch value := fn.(type) {
	case *MCPFunction:
		if value != nil {
			return strings.TrimSpace(value.mcpName)
		}
	case *functions.RuntimeToolFunction:
		if value != nil {
			if metadata := value.DefinitionMetadata(); metadata != nil {
				if name, ok := metadata["mcp_name"].(string); ok {
					return strings.TrimSpace(name)
				}
			}
		}
	}
	return ""
}

// mcpNameFromDescriptor 从 runtime tool descriptor 的元数据读取 mcp_name。
func mcpNameFromDescriptor(desc runtimetools.ToolDescriptor) string {
	if desc.Metadata == nil {
		return ""
	}
	name, _ := desc.Metadata["mcp_name"].(string)
	return strings.TrimSpace(name)
}

// pruneSessionMCPFunctions 撤销属于该 server 的全部已注册函数（registry + catalog），
// 返回撤销数量。调用方需保证会话级覆盖已经落成（否则会与选择面过滤互相打架）。
func pruneSessionMCPFunctions(session *ChatSession, server string) int {
	if session == nil || session.FunctionCatalog == nil {
		return 0
	}
	server = strings.TrimSpace(server)
	if server == "" {
		return 0
	}
	catalog := session.FunctionCatalog
	names := map[string]struct{}{}
	if catalog.registry != nil {
		for _, fn := range catalog.registry.List() {
			if fn == nil {
				continue
			}
			if strings.EqualFold(mcpFunctionServer(fn), server) {
				names[strings.TrimSpace(fn.Name())] = struct{}{}
			}
		}
	}
	for name, entry := range catalog.entries {
		if entry == nil || entry.fn == nil {
			continue
		}
		if strings.EqualFold(mcpFunctionServer(entry.fn), server) {
			names[name] = struct{}{}
		}
	}
	removed := 0
	for name := range names {
		if name == "" {
			continue
		}
		if catalog.RemoveFunction(name) {
			removed++
			continue
		}
		if catalog.registry != nil && catalog.registry.Unregister(name) {
			removed++
		}
	}
	return removed
}

// retractSessionFunctions 撤销指定函数名集合（用于临时连接重建后的清理）。
func retractSessionFunctions(session *ChatSession, names map[string]struct{}) int {
	if session == nil || session.FunctionCatalog == nil {
		return 0
	}
	removed := 0
	for name := range names {
		if session.FunctionCatalog.RemoveFunction(name) {
			removed++
		}
	}
	return removed
}

// refreshSessionScopedMCP 应用本会话覆盖并登记临时连接的迟到工具。
// 返回发生变化的登记数（>0 时需要失效稳定工具面）。
func refreshSessionScopedMCP(session *ChatSession) int {
	if session == nil || session.FunctionCatalog == nil {
		return 0
	}
	changed := 0
	if temp := session.mcpSessionTemp; temp != nil {
		changed += temp.registerNewTools(session)
	}
	for name, enabled := range session.MCPSessionOverrides {
		if enabled {
			continue
		}
		changed += pruneSessionMCPFunctions(session, name)
	}
	if changed > 0 {
		resetStableSharedToolSurface(session)
	}
	return changed
}

// sessionMCPStateLabel 渲染 /mcp list|status 的会话级状态标注（无覆盖返回空串）。
func sessionMCPStateLabel(session *ChatSession, name string) string {
	if session == nil {
		return ""
	}
	if temp := session.mcpSessionTemp; temp != nil && temp.hasServer(name) {
		return "本会话: 临时启用（--session，未落盘）"
	}
	if sessionMCPOverrideDisabled(session, name) {
		return "本会话: 已停用（--session 恢复）"
	}
	return ""
}

// chatMCPSessionToggleText 执行 /mcp enable|disable <name> --session。
// 返回 (回执文本, 是否已修改会话状态)。
func chatMCPSessionToggleText(session *ChatSession, rawName string, enabled bool) (string, bool) {
	if session == nil {
		return "错误: 会话级启停需要活动会话", false
	}
	name := strings.TrimSpace(rawName)
	if name == "" {
		return "错误: 需要指定 MCP 名称\n用法: /mcp " + sessionMCPToggleSubcommand(enabled) + " <name> --session", false
	}
	result, err := sessionMCPConfigLookupFrom(chatSessionWorkspaceDir(session))
	if err != nil || result == nil || result.Config == nil {
		if err == nil {
			err = fmt.Errorf("MCP 配置不可用")
		}
		return "错误: 读取 MCP 配置失败: " + err.Error(), false
	}
	canonical, serverCfg, ok := sessionMCPLookupServer(result.Config, name)
	if !ok {
		return fmt.Sprintf("错误: MCP %q 不存在", name), false
	}
	if !mcpServerAllowedByProfile(session, canonical) {
		return fmt.Sprintf("MCP %q 被当前 profile 选择排除；会话级启用不绕过 profile 裁剪", canonical), false
	}
	globalEnabled := sessionMCPGlobalEnabled(canonical, serverCfg)
	temp := session.mcpSessionTemp

	switch {
	case enabled && temp != nil && temp.hasServer(canonical):
		return fmt.Sprintf("MCP %q 已是本会话临时启用状态", canonical), false

	case enabled && globalEnabled:
		deleteSessionMCPOverride(session, canonical)
		refreshChatMCPTools(session)
		resetStableSharedToolSurface(session)
		return fmt.Sprintf("✓ 已在本次会话恢复启用 MCP %q（仅本会话；配置文件未改动）", canonical), true

	case enabled:
		// 全局停用：会话私有临时连接（内存配置快照，不写配置文件）。
		if temp == nil {
			temp = &sessionMCPTempRuntime{
				servers:    map[string]mcpconfig.MCPConfig{},
				registered: map[string]struct{}{},
			}
			session.mcpSessionTemp = temp
		}
		serverCopy := *serverCfg
		serverCopy.Enabled = true
		serverCopy.Disabled = false
		temp.servers[canonical] = serverCopy
		if err := temp.connect(context.Background(), session); err != nil {
			delete(temp.servers, canonical)
			if len(temp.servers) == 0 {
				temp.close()
				session.mcpSessionTemp = nil
			}
			return fmt.Sprintf("错误: 会话内临时启动 MCP %q 失败: %v", canonical, err), false
		}
		setSessionMCPOverride(session, canonical, true)
		resetStableSharedToolSurface(session)
		return fmt.Sprintf("✓ 已在本次会话临时启用 MCP %q（仅本会话连接，未写入配置文件；会话结束即回收）", canonical), true

	case temp != nil && temp.hasServer(canonical):
		// 停用本会话临时启用的 server：回收临时连接。
		delete(temp.servers, canonical)
		if len(temp.servers) == 0 {
			retractSessionFunctions(session, temp.registered)
			temp.close()
			session.mcpSessionTemp = nil
		} else if err := temp.connect(context.Background(), session); err != nil {
			return fmt.Sprintf("错误: 回收会话临时 MCP %q 失败: %v", canonical, err), false
		}
		deleteSessionMCPOverride(session, canonical)
		refreshChatMCPTools(session)
		resetStableSharedToolSurface(session)
		return fmt.Sprintf("✓ 已在本次会话停用 MCP %q（临时连接已回收；全局配置未改动）", canonical), true

	case !globalEnabled:
		deleteSessionMCPOverride(session, canonical)
		refreshSessionScopedMCP(session)
		return fmt.Sprintf("MCP %q 当前已是停用状态（全局与本会话）", canonical), false

	default:
		setSessionMCPOverride(session, canonical, false)
		refreshChatMCPTools(session)
		resetStableSharedToolSurface(session)
		return fmt.Sprintf("✓ 已在本次会话停用 MCP %q（仅本会话；全局连接与配置文件未改动）", canonical), true
	}
}

func sessionMCPToggleSubcommand(enabled bool) string {
	if enabled {
		return "enable"
	}
	return "disable"
}

// resetSessionScopedMCP 在新对话（/new）时清空会话级覆盖并回收临时连接：
// 临时启用的函数必须从目录撤销，避免新对话继续暴露旧会话的临时工具。
func resetSessionScopedMCP(session *ChatSession) {
	if session == nil {
		return
	}
	if temp := session.mcpSessionTemp; temp != nil {
		retractSessionFunctions(session, temp.registered)
		temp.close()
		session.mcpSessionTemp = nil
	}
	session.MCPSessionOverrides = nil
	resetStableSharedToolSurface(session)
}

// closeSessionScopedMCP 在会话关闭时回收临时连接（幂等）。
func closeSessionScopedMCP(session *ChatSession) {
	if session == nil {
		return
	}
	if temp := session.mcpSessionTemp; temp != nil {
		temp.close()
		session.mcpSessionTemp = nil
	}
	session.MCPSessionOverrides = nil
}

// sessionMCPTempRuntime 是「全局停用、仅本会话临时启用」的私有运行时：
// 用内存配置快照复用 manager 的建连/工具加载/回收机制，不产生任何配置文件写入。
type sessionMCPTempRuntime struct {
	manager     manager.Manager
	toolManager *runtimetools.Manager
	servers     map[string]mcpconfig.MCPConfig
	registered  map[string]struct{}
	closed      bool
}

func (t *sessionMCPTempRuntime) hasServer(name string) bool {
	if t == nil {
		return false
	}
	_, ok := t.servers[strings.TrimSpace(name)]
	return ok
}

// connect 按当前 servers 快照重建临时连接并登记工具（幂等重建）。
func (t *sessionMCPTempRuntime) connect(ctx context.Context, session *ChatSession) error {
	if t == nil {
		return nil
	}
	previous := t.registered
	t.registered = map[string]struct{}{}
	t.closed = false
	if t.manager != nil {
		_ = t.manager.Stop()
		t.manager = nil
		t.toolManager = nil
	}
	if len(t.servers) == 0 {
		retractSessionFunctions(session, previous)
		return nil
	}

	cfg := &mcpconfig.Config{MCPServers: make(map[string]mcpconfig.MCPConfig, len(t.servers))}
	for name, server := range t.servers {
		cfg.MCPServers[name] = server
	}
	mgr := sessionMCPTempManagerFactory()
	scoped, ok := mgr.(manager.ScopedManager)
	if !ok || scoped == nil {
		t.registered = previous
		return fmt.Errorf("当前 MCP manager 不支持内存配置快照（ScopedManager）")
	}
	if err := scoped.LoadConfigFromConfig(cfg); err != nil {
		t.registered = previous
		return err
	}
	// 建连使用与命令上下文解耦的 ctx：命令返回后后台建连仍需继续。
	startCtx := context.WithoutCancel(ctxOrBackground(ctx))
	if async, ok := mgr.(manager.AsyncManager); ok && async != nil {
		if err := async.StartAsync(startCtx); err != nil {
			_ = mgr.Stop()
			t.registered = previous
			return err
		}
	} else {
		go func() {
			_ = mgr.Start(startCtx)
		}()
	}
	t.manager = mgr
	readyCtx, cancel := context.WithTimeout(startCtx, sessionMCPTempReadyTimeout)
	waitMCPReady(readyCtx, currentRuntimeSessionID(session), "session-temp", mgr)
	cancel()
	t.toolManager = runtimetools.NewDefaultManagerWithRuntimeConfig(mgr, loadRuntimeToolConfig(agentconfig.GetGlobalConfig(), session))
	t.registerNewTools(session)
	for name := range previous {
		if _, kept := t.registered[name]; kept {
			continue
		}
		if session != nil && session.FunctionCatalog != nil {
			session.FunctionCatalog.RemoveFunction(name)
		}
	}
	return nil
}

// registerNewTools 把临时连接上"新出现的"工具增量登记进会话目录，返回新增数量。
func (t *sessionMCPTempRuntime) registerNewTools(session *ChatSession) int {
	if t == nil || t.toolManager == nil || t.closed || session == nil || session.FunctionCatalog == nil {
		return 0
	}
	added := 0
	for _, desc := range t.toolManager.ListTools() {
		server := mcpNameFromDescriptor(desc)
		if server == "" {
			continue
		}
		if _, wanted := t.servers[server]; !wanted {
			continue
		}
		if sessionMCPOverrideDisabled(session, server) {
			continue
		}
		name := strings.TrimSpace(desc.Name)
		if name == "" {
			continue
		}
		if _, exists := t.registered[name]; exists {
			continue
		}
		t.registered[name] = struct{}{}
		session.FunctionCatalog.RegisterBuiltinToolFunction(functions.NewRuntimeToolFunction(t.toolManager, desc), desc)
		added++
	}
	return added
}

// close 停止临时连接（幂等）；调用方负责会话结束场景，不再撤销函数。
func (t *sessionMCPTempRuntime) close() {
	if t == nil || t.closed {
		return
	}
	t.closed = true
	if t.manager != nil {
		_ = t.manager.Stop()
		t.manager = nil
	}
	t.toolManager = nil
	t.registered = map[string]struct{}{}
	t.servers = map[string]mcpconfig.MCPConfig{}
}
