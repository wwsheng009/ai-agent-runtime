package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/functions"
	"github.com/wwsheng009/ai-agent-runtime/internal/acp"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/manager"
	logpkg "github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
	runtimetools "github.com/wwsheng009/ai-agent-runtime/internal/tools"
)

// acpMCPMode 控制 ACP 会话的 MCP 装配策略。
//
// ACP v1 允许客户端在 session/new、session/load、session/resume 的 mcpServers
// 字段下发 MCP 服务器。这个开关决定「本地配置链」与「客户端下发」两条来源如何
// 组合，默认 merge（两者都连）。
type acpMCPMode string

const (
	// acpMCPModeMerge 同时装配本地配置链与会话级客户端下发（默认）。
	acpMCPModeMerge acpMCPMode = "merge"
	// acpMCPModeLocal 只连本地配置链，忽略客户端下发（等同旧行为）。
	acpMCPModeLocal acpMCPMode = "local"
	// acpMCPModeClient 只连客户端下发，不初始化本地配置链。
	acpMCPModeClient acpMCPMode = "client"
	// acpMCPModeOff 两条来源都关闭（MCP 工具面为空）。
	acpMCPModeOff acpMCPMode = "off"
)

// acpMCPFirstPromptReadyTimeout 是首轮 prompt 前等待会话级 MCP 建连的上界。
//
// 等待只发生一次：超时后不再阻塞后续 turn，迟到的工具仍会在下一个 prompt
// 边界被增量登记（见 acpSessionMCP.prepareForPrompt）。
const acpMCPFirstPromptReadyTimeout = 2 * time.Second

// acpMCPLogPrefix 是会话级 MCP 诊断/审计日志的稳定前缀，便于过滤。
const acpMCPLogPrefix = "acp.mcp"

func parseACPMCPMode(raw string) (acpMCPMode, error) {
	trimmed := strings.ToLower(strings.TrimSpace(raw))
	if trimmed == "" {
		return acpMCPModeMerge, nil
	}
	switch mode := acpMCPMode(trimmed); mode {
	case acpMCPModeMerge, acpMCPModeLocal, acpMCPModeClient, acpMCPModeOff:
		return mode, nil
	default:
		return "", fmt.Errorf("invalid ACP MCP mode %q (want merge|local|client|off)", raw)
	}
}

// allowsLocal 表示该模式是否装配本地配置链（进程级 MCPManagerInstance）。
func (m acpMCPMode) allowsLocal() bool {
	return m.normalized() == acpMCPModeMerge || m.normalized() == acpMCPModeLocal
}

// allowsClient 表示该模式是否接受客户端下发的 mcpServers。
func (m acpMCPMode) allowsClient() bool {
	return m.normalized() == acpMCPModeMerge || m.normalized() == acpMCPModeClient
}

// normalized 把零值收敛到默认模式：所有未显式配置 ACP MCP 的入口（chat、TUI、
// exec、单元测试）都必须保持旧行为，即 merge。
func (m acpMCPMode) normalized() acpMCPMode {
	if strings.TrimSpace(string(m)) == "" {
		return acpMCPModeMerge
	}
	return acpMCPMode(strings.ToLower(strings.TrimSpace(string(m))))
}

// acpMCPSessionLabel 返回用于诊断/审计的会话标识。
//
// ACP 的 sessionId 在 bootstrap 之后才确定，而 MCP 装配发生在 bootstrap 内部，
// 因此这里使用同一份运行时会话 id（ACP 侧 resolvedID 正是由它派生的）。
func acpMCPSessionLabel(session *ChatSession) string {
	if session == nil {
		return "session"
	}
	if id := strings.TrimSpace(currentRuntimeSessionID(session)); id != "" {
		return id
	}
	return "session"
}

// registeredMCPToolNames 把已登记的工具名折算成 acpSessionMCP 的初始集合。
func registeredMCPToolNames(descs []runtimetools.ToolDescriptor) map[string]struct{} {
	names := make(map[string]struct{}, len(descs))
	for _, desc := range descs {
		if name := strings.TrimSpace(desc.Name); name != "" {
			names[name] = struct{}{}
		}
	}
	return names
}

// acpMCPClientPlan 是一次 session/new|load|resume 的客户端下发快照。
type acpMCPClientPlan struct {
	Servers []acp.McpServer
	Issues  []acp.MCPDecodeIssue
}

// newACPMCPClientPlan 逐条容错解析 mcpServers；任何解析失败都降级为诊断。
func newACPMCPClientPlan(raw []byte) *acpMCPClientPlan {
	servers, issues := acp.DecodeMCPServers(raw)
	if len(servers) == 0 && len(issues) == 0 {
		return nil
	}
	return &acpMCPClientPlan{Servers: servers, Issues: issues}
}

// acpMCPPlanContextKey 承载「本次请求」的客户端下发快照。
//
// 快照必须按请求传递：host 级 opts 在多个会话间共享，把 per-session 数据挂上去
// 会产生竞态。NewSession/LoadSession/ResumeSession 从请求里解析后放入 ctx，
// bootstrap 路径再取出并绑定到该会话自己的 chat 选项上。
type acpMCPPlanContextKey struct{}

func withACPMCPClientPlan(ctx context.Context, plan *acpMCPClientPlan) context.Context {
	if plan == nil {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, acpMCPPlanContextKey{}, plan)
}

func acpMCPClientPlanFromContext(ctx context.Context) *acpMCPClientPlan {
	if ctx == nil {
		return nil
	}
	plan, _ := ctx.Value(acpMCPPlanContextKey{}).(*acpMCPClientPlan)
	return plan
}

// acpMCPServerPlan 是一个通过全部门控、可直接转成运行时配置的客户端 server。
type acpMCPServerPlan struct {
	// Name 是去重后的 manager 名称（同时也是 config.MCPServers 的键）。
	Name string
	// ClientName 是客户端给出的原始名称，用于诊断。
	ClientName string
	Server     acp.McpServer
	Config     config.MCPConfig
}

// planACPSessionMCP 把客户端下发映射成运行时 MCP 配置，并给出全部跳过原因。
//
// 纯函数：不建连、不写日志，便于测试门控矩阵（模式 / 能力位 / 工作区信任 /
// 传输支持度）。诊断行只包含 server 名与原因，不含 env/headers 的值。
func planACPSessionMCP(
	plan *acpMCPClientPlan,
	mode acpMCPMode,
	caps acp.MCPCapabilities,
	trusted bool,
	cwd string,
) ([]acpMCPServerPlan, []string) {
	if plan == nil || len(plan.Servers) == 0 {
		return nil, nil
	}
	if !mode.allowsClient() {
		return nil, []string{fmt.Sprintf(
			"ignored %d client-provided MCP server(s): mode=%s disables client servers",
			len(plan.Servers), mode)}
	}
	if !trusted {
		names := make([]string, 0, len(plan.Servers))
		for _, server := range plan.Servers {
			names = append(names, server.Name)
		}
		sort.Strings(names)
		return nil, []string{fmt.Sprintf(
			"refused %d client-provided MCP server(s) in an untrusted workspace (%s): project-scope MCP execution requires folder trust",
			len(plan.Servers), strings.Join(names, ", "))}
	}

	planned := make([]acpMCPServerPlan, 0, len(plan.Servers))
	var diagnostics []string
	used := make(map[string]struct{}, len(plan.Servers))
	for _, server := range plan.Servers {
		kind := server.TransportKind()
		if supported, reason := acpMCPServerSupported(server, caps); !supported {
			diagnostics = append(diagnostics, fmt.Sprintf(
				"skipped MCP server %q: %s", server.Name, reason))
			continue
		}
		mapped, err := acpMCPServerToConfig(server, cwd)
		if err != nil {
			diagnostics = append(diagnostics, fmt.Sprintf(
				"skipped MCP server %q: %v", server.Name, err))
			continue
		}
		name := uniqueMCPServerName(server.Name, used)
		mapped.Name = name
		planned = append(planned, acpMCPServerPlan{
			Name:       name,
			ClientName: server.Name,
			Server:     server,
			Config:     mapped,
		})
		_ = kind
	}
	return planned, diagnostics
}

// acpMCPServerSupported 判定传输是否被本 agent 的初始化能力位覆盖。
//
// 能力位「置位即承诺」：未声明支持的传输必须零连接尝试地跳过，并把原因回报给
// 用户可见的诊断通道。
func acpMCPServerSupported(server acp.McpServer, caps acp.MCPCapabilities) (bool, string) {
	switch server.TransportKind() {
	case acp.MCPTransportStdio:
		return true, ""
	case acp.MCPTransportHTTP:
		if caps.HTTP {
			return true, ""
		}
		return false, "http transport is not advertised in agentCapabilities.mcpCapabilities"
	case acp.MCPTransportSSE:
		if caps.SSE {
			return true, ""
		}
		return false, "sse transport is not advertised in agentCapabilities.mcpCapabilities"
	default:
		return false, fmt.Sprintf("unsupported transport type %q", server.Type)
	}
}

// acpMCPServerToConfig 把 ACP 条目映射成运行时 MCP 配置。
//
// 映射表（与计划 §4.4 一致）：
//   - stdio → type=stdio，WorkingDir 固定为会话 cwd（客户端下发的相对路径按会话
//     工作区解析，而不是 aicli 进程启动目录）；
//   - http  → type=streamable（运行时用 Streamable HTTP 传输）；
//   - sse   → type=sse；
//   - http/sse 的凭据走 Headers 字段（Env 是历史兼容路径，不再用于远端传输）；
//   - 远端 server 默认 trustLevel=untrusted_remote（结果按不可信输入对待）。
func acpMCPServerToConfig(server acp.McpServer, cwd string) (config.MCPConfig, error) {
	name := strings.TrimSpace(server.Name)
	if name == "" {
		return config.MCPConfig{}, errors.New(`missing required field "name"`)
	}
	switch server.TransportKind() {
	case acp.MCPTransportStdio:
		command := strings.TrimSpace(server.Command)
		if command == "" {
			return config.MCPConfig{}, errors.New(`stdio server is missing required field "command"`)
		}
		env := keyValueMap(server.Env)
		return config.MCPConfig{
			Name:       name,
			Type:       "stdio",
			Command:    command,
			Args:       append([]string(nil), server.Args...),
			Env:        env,
			WorkingDir: strings.TrimSpace(cwd),
			Enabled:    true,
			TrustLevel: config.MCPTrustLevelLocal,
		}, nil
	case acp.MCPTransportHTTP:
		url := strings.TrimSpace(server.URL)
		if url == "" {
			return config.MCPConfig{}, errors.New(`http server is missing required field "url"`)
		}
		return config.MCPConfig{
			Name:       name,
			Type:       "streamable",
			URL:        url,
			Headers:    keyValueMap(server.Headers),
			Enabled:    true,
			TrustLevel: config.MCPTrustLevelUntrusted,
		}, nil
	case acp.MCPTransportSSE:
		url := strings.TrimSpace(server.URL)
		if url == "" {
			return config.MCPConfig{}, errors.New(`sse server is missing required field "url"`)
		}
		return config.MCPConfig{
			Name:       name,
			Type:       "sse",
			URL:        url,
			Headers:    keyValueMap(server.Headers),
			Enabled:    true,
			TrustLevel: config.MCPTrustLevelUntrusted,
		}, nil
	default:
		return config.MCPConfig{}, fmt.Errorf("unsupported transport type %q", server.Type)
	}
}

// keyValueMap 把 ACP 的 {name,value} 列表折叠成 map。
//
// 重复 name 以后者为准（与规范「列表语义、后者覆盖」一致）；空 name 被丢弃。
func keyValueMap(items []acp.MCPKeyValue) map[string]string {
	if len(items) == 0 {
		return nil
	}
	out := make(map[string]string, len(items))
	for _, item := range items {
		key := strings.TrimSpace(item.Name)
		if key == "" {
			continue
		}
		out[key] = item.Value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// uniqueMCPServerName 在会话内为 server 名去重，避免同名条目互相覆盖。
func uniqueMCPServerName(name string, used map[string]struct{}) string {
	base := sanitizeMCPServerName(name)
	candidate := base
	for suffix := 2; ; suffix++ {
		if _, exists := used[candidate]; !exists {
			used[candidate] = struct{}{}
			return candidate
		}
		candidate = fmt.Sprintf("%s-%d", base, suffix)
	}
}

// sanitizeMCPServerName 清洗客户端可控的名称：去掉空白与控制字符，空名回落为
// 占位名（名称只用于日志、配置键与状态展示，不参与任何路径拼接）。
func sanitizeMCPServerName(name string) string {
	trimmed := strings.TrimSpace(name)
	cleaned := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, trimmed)
	if cleaned == "" {
		return "client-server"
	}
	return cleaned
}

// redactMCPKeyNames 只保留凭据键名（值永不进日志）。
func redactMCPKeyNames(items []acp.MCPKeyValue) string {
	if len(items) == 0 {
		return ""
	}
	names := make([]string, 0, len(items))
	for _, item := range items {
		if name := strings.TrimSpace(item.Name); name != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}

// acpSessionMCP 持有单个 ACP 会话的 MCP 工具面刷新器与可选的会话私有运行时。
//
// 两条来源共用同一套「有界等待建连 + turn 边界增量登记」：
//   - manager：客户端通过 mcpServers 下发的 server（会话私有，close() 负责回收）；
//   - localManager：进程级本地配置链（MCPManagerInstance），close() 不回收。
//
// 生命周期：session/close、session/delete 或宿主关闭时回收会话私有子进程。
// 工具登记只发生在会话的 turn 边界（建会话时与每个 prompt 之前），避免与
// 正在执行的 turn 并发写 FunctionCatalog。
type acpSessionMCP struct {
	sessionID    string
	manager      manager.Manager
	localManager manager.Manager
	toolManager  *runtimetools.Manager
	catalog      *aicliFunctionCatalog
	// invalidateSurface 清除本会话的稳定工具面缓存（MCP 目录变化后调用）。
	// 只有 ACP 宿主会设置它：既有失效通道只覆盖 chatWebSession()。
	invalidateSurface func() int

	mu         sync.Mutex
	registered map[string]struct{}
	waited     bool
	closed     bool
	servers    []string
}

// newACPSessionMCPSurface 为「只有本地配置链」的 ACP 会话创建工具面刷新器。
//
// 本地配置链由进程级 manager 异步建连（StartAsync），工具到达时间与 session/new
// 无关；这里复用会话私有运行时同一套 turn 边界增量登记，避免用户观感上的
// 「配了却没生效」（§4.7 R1 / §4.10）。close() 不会停止进程级 manager。
func newACPSessionMCPSurface(sessionID string, catalog *aicliFunctionCatalog, localManager manager.Manager) *acpSessionMCP {
	if localManager == nil {
		return nil
	}
	return &acpSessionMCP{
		sessionID:    sessionID,
		localManager: localManager,
		catalog:      catalog,
		registered:   make(map[string]struct{}),
	}
}

// prepareForPrompt 在 turn 开始前等待首轮建连并增量登记新工具。
//
// 必须在会话的 turn goroutine 上调用（Prompt 内、sendMessage 之前）。
func (s *acpSessionMCP) prepareForPrompt(ctx context.Context) {
	if s == nil {
		return
	}
	s.waitReadyOnce(ctx, acpMCPFirstPromptReadyTimeout)
	if added := s.refreshTools(); added > 0 {
		logpkg.Infof("%s session=%s registered %d MCP tool(s) after connect", acpMCPLogPrefix, s.sessionID, added)
	}
}

// waitReadyOnce 至多等待一次首轮建连；超时不阻塞后续 turn。
func (s *acpSessionMCP) waitReadyOnce(ctx context.Context, timeout time.Duration) {
	s.mu.Lock()
	if s.waited || s.closed {
		s.mu.Unlock()
		return
	}
	s.waited = true
	sessionMgr := s.manager
	localMgr := s.localManager
	s.mu.Unlock()

	if ctx == nil {
		ctx = context.Background()
	}
	waitCtx := ctx
	cancel := func() {}
	if timeout > 0 {
		waitCtx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()

	// 两条来源都是异步建连，共用一个等待上界：超时后不阻塞后续 turn，迟到的
	// 工具仍会在下一个 prompt 边界被 refreshTools 补登记（§4.7 R1 / D5）。
	waitMCPReady(waitCtx, s.sessionID, "client", sessionMgr)
	waitMCPReady(waitCtx, s.sessionID, "local", localMgr)
}

// waitMCPReady 等待单个 manager 的异步建连完成；nil 或不支持异步时直接返回。
func waitMCPReady(ctx context.Context, sessionID, source string, mgr manager.Manager) {
	waiter, ok := mgr.(manager.AsyncManager)
	if !ok || waiter == nil {
		return
	}
	if err := waiter.WaitReady(ctx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		logpkg.Debugf("%s session=%s wait ready (%s): %v", acpMCPLogPrefix, sessionID, source, err)
	}
}

// refreshTools 把新出现的 MCP 工具登记进会话目录，返回新增数量。
func (s *acpSessionMCP) refreshTools() int {
	if s == nil || s.toolManager == nil || s.catalog == nil {
		return 0
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return 0
	}
	if s.registered == nil {
		s.registered = make(map[string]struct{})
	}
	added := 0
	for _, desc := range s.toolManager.ListTools() {
		name := strings.TrimSpace(desc.Name)
		if name == "" {
			continue
		}
		if _, exists := s.registered[name]; exists {
			continue
		}
		s.registered[name] = struct{}{}
		s.catalog.RegisterBuiltinToolFunction(functions.NewRuntimeToolFunction(s.toolManager, desc), desc)
		added++
	}
	invalidate := s.invalidateSurface
	s.mu.Unlock()

	// 目录变化后清除本会话的稳定工具面缓存，让新工具在下一个 turn 边界进入
	// 工具面（§4.7 R1）；会话首次冻结工具面发生在第一个 turn，因此这里同时
	// 覆盖「首轮之前到达」与「首轮之后到达」两种时序。
	if added > 0 && invalidate != nil {
		invalidate()
	}
	return added
}

// close 回收会话级 MCP：停止全部子进程/连接（幂等）。
func (s *acpSessionMCP) close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	mgr := s.manager
	s.mu.Unlock()
	if mgr != nil {
		if err := mgr.Stop(); err != nil {
			logpkg.Debugf("%s session=%s stop session MCP: %v", acpMCPLogPrefix, s.sessionID, err)
		}
	}
}

// summary 返回会话级 server 名称列表（用于启动日志与状态展示）。
func (s *acpSessionMCP) summary() []string {
	if s == nil {
		return nil
	}
	return append([]string(nil), s.servers...)
}

// buildACPSessionMCP 装配会话级 MCP 运行时。
//
// 返回 nil 表示该会话没有客户端下发来源可用（模式关闭、未信任、全部条目非法，
// 或没有下发）；此时不产生任何连接尝试与子进程。
func buildACPSessionMCP(
	ctx context.Context,
	sessionID string,
	session *ChatSession,
	mode acpMCPMode,
	plan *acpMCPClientPlan,
	caps acp.MCPCapabilities,
) *acpSessionMCP {
	if plan == nil || len(plan.Servers) == 0 {
		return nil
	}
	for _, issue := range plan.Issues {
		acpMCPWarnf(sessionID, "skipped client MCP entry: %s", issue.String())
	}
	cwd := strings.TrimSpace(folderTrustProjectRoot(session))
	if cwd == "" {
		if wd, err := os.Getwd(); err == nil {
			cwd = wd
		}
	}
	planned, diagnostics := planACPSessionMCP(plan, mode, caps, sessionProjectScopeAllowed(session), cwd)
	for _, line := range diagnostics {
		acpMCPWarnf(sessionID, "%s", line)
	}
	if len(planned) == 0 {
		return nil
	}

	cfg := &config.Config{MCPServers: make(map[string]config.MCPConfig, len(planned))}
	for _, item := range planned {
		cfg.MCPServers[item.Name] = item.Config
	}

	mgr := manager.NewManager()
	scoped, ok := mgr.(manager.ScopedManager)
	if !ok {
		acpMCPWarnf(sessionID, "MCP manager does not support in-memory config; client servers ignored")
		return nil
	}
	if err := scoped.LoadConfigFromConfig(cfg); err != nil {
		acpMCPWarnf(sessionID, "load client MCP config failed: %v", err)
		return nil
	}

	runtime := &acpSessionMCP{
		sessionID:   sessionID,
		manager:     mgr,
		catalog:     session.FunctionCatalog,
		registered:  make(map[string]struct{}),
		servers:     make([]string, 0, len(planned)),
		toolManager: nil,
	}
	for _, item := range planned {
		runtime.servers = append(runtime.servers, item.Name)
		acpMCPAuditf(sessionID, item, cwd)
	}
	sort.Strings(runtime.servers)

	// 建连使用与请求解耦的 ctx：session/new 的请求上下文在响应写出后即结束，
	// 直接透传会取消后台建连。会话生命周期由 close() 负责终止。
	startCtx := context.WithoutCancel(ctxOrBackground(ctx))
	if async, ok := mgr.(manager.AsyncManager); ok {
		if err := async.StartAsync(startCtx); err != nil {
			acpMCPWarnf(sessionID, "start client MCP servers failed: %v", err)
			_ = mgr.Stop()
			return nil
		}
	} else {
		go func() {
			if err := mgr.Start(startCtx); err != nil {
				acpMCPWarnf(sessionID, "start client MCP servers failed: %v", err)
			}
		}()
	}
	logpkg.Infof("%s session=%s attached %d client MCP server(s): %s",
		acpMCPLogPrefix, sessionID, len(runtime.servers), strings.Join(runtime.servers, ", "))
	return runtime
}

// acpMCPAuditf 记录一条会话级 MCP 审计日志。
//
// 审计字段：来源、会话、server 名、传输类型、会话 cwd；stdio 只记命令名与参数
// 个数（参数可能携带令牌），http/sse 只记凭据键名（值永不落盘）。
func acpMCPAuditf(sessionID string, item acpMCPServerPlan, cwd string) {
	transport := item.Server.TransportKind()
	fields := []string{
		"source=client",
		fmt.Sprintf("session=%s", sessionID),
		fmt.Sprintf("server=%s", item.Name),
		fmt.Sprintf("transport=%s", transport),
	}
	if clientName := strings.TrimSpace(item.ClientName); clientName != "" && clientName != item.Name {
		fields = append(fields, fmt.Sprintf("client_name=%s", clientName))
	}
	switch transport {
	case acp.MCPTransportStdio:
		fields = append(fields,
			fmt.Sprintf("command=%s", item.Config.Command),
			fmt.Sprintf("args=%d", len(item.Config.Args)),
			fmt.Sprintf("cwd=%s", cwd),
		)
		if keys := redactMCPKeyNames(item.Server.Env); keys != "" {
			fields = append(fields, fmt.Sprintf("env_keys=%s", keys))
		}
	default:
		fields = append(fields, fmt.Sprintf("url=%s", item.Config.URL))
		if keys := redactMCPKeyNames(item.Server.Headers); keys != "" {
			fields = append(fields, fmt.Sprintf("header_keys=%s", keys))
		}
	}
	logpkg.Infof("%s.audit %s", acpMCPLogPrefix, strings.Join(fields, " "))
}

// acpMCPWarnf 同时写结构化日志与 stderr：ACP 客户端把 agent stderr 当诊断通道，
// stdout 必须保持 NDJSON 协议帧。
func acpMCPWarnf(sessionID string, format string, args ...interface{}) {
	message := fmt.Sprintf(format, args...)
	logpkg.Warnf("%s session=%s %s", acpMCPLogPrefix, sessionID, message)
	fmt.Fprintf(os.Stderr, "[acp-mcp] session=%s %s\n", sessionID, message)
}

func ctxOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
