package commands

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/functions"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	runtimelsp "github.com/wwsheng009/ai-agent-runtime/internal/lsp"
	logpkg "github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
	"github.com/wwsheng009/ai-agent-runtime/internal/projectscan"
	runtimetools "github.com/wwsheng009/ai-agent-runtime/internal/tools"
)

// 启动期 LSP 自动装配（项目类型扫描 → 工作区 runtime.yaml → 启用 LSP）。
//
// 时序（不阻塞启动关键路径）：
//
//	启动线程: 建 toolManager（此时 LSP 未配置，池为空）
//	          └─ go startChatLSPBootstrap
//	后台扫描: projectscan.Scan（有界、只读）
//	          → 过滤掉本机不存在的服务器二进制
//	          → agentconfig.EnsureWorkspaceRuntimeLSP 写入 <workspace>/.aicli/runtime.yaml
//	            （仅在没有任何层显式配置 lsp.enabled 时）
//	          → 重新加载生效配置（层合并；工作区不在层栈时只叠加 lsp 段）
//	          → toolManager.EnableLSPFromConfig（注册表自带锁，可跨 goroutine）
//	          → 标记 pending，等待 turn 边界
//	turn 边界: prepareLateLSPToolSurface 把新工具函数登记进会话目录并让稳定工具面失效。
//
// 为什么工具函数必须等到 turn 边界：aicliFunctionCatalog 没有内部锁，写它只能发生
// 在会话的 turn goroutine 上（与 ACP MCP 刷新器同一条规则，§4.7 R1 / D5）。

// chatLSPBootstrapTimeout 是后台扫描 + 配置写入 + 重载的总时限。超时即放弃本轮
// 自动检测；显式配置的 LSP 不受影响。
const chatLSPBootstrapTimeout = 12 * time.Second

// chatLSPLateEnable 承载「LSP 已挂载、工具函数待登记」这一个状态。
//
// 并发契约：
//   - attach 由后台扫描 goroutine 调用（只碰 toolManager，注册表线程安全）；
//   - apply 只在 turn 边界调用（会话 turn goroutine），因此 catalog/registered 的
//     读写不需要额外锁；pending/applied 用 mu 保护以覆盖 attach 与 apply 的交接。
type chatLSPLateEnable struct {
	toolManager *runtimetools.Manager
	// invalidateSurface 让本会话的稳定工具面缓存失效，使新工具在下一个 turn
	// 进入模型工具面（与 MCP 刷新器同一条通道）。
	invalidateSurface func() int

	mu         sync.Mutex
	pending    bool
	applied    bool
	registered map[string]struct{}
}

// attach 把配置里的 LSP 池挂到工具管理器上；成功时标记待登记。
func (l *chatLSPLateEnable) attach(config *runtimecfg.RuntimeConfig) bool {
	if l == nil || l.toolManager == nil {
		return false
	}
	if !l.toolManager.EnableLSPFromConfig(config) {
		return false
	}
	l.mu.Lock()
	l.pending = true
	l.mu.Unlock()
	return true
}

// apply 在 turn 边界增量登记 LSP 工具函数，返回新增数量。
func (l *chatLSPLateEnable) apply(session *ChatSession) int {
	if l == nil || session == nil {
		return 0
	}
	l.mu.Lock()
	if !l.pending || l.applied {
		l.mu.Unlock()
		return 0
	}
	l.applied = true
	catalog := session.FunctionCatalog
	manager := l.toolManager
	registered := l.registered
	if registered == nil {
		registered = make(map[string]struct{})
		l.registered = registered
	}
	l.mu.Unlock()

	if catalog == nil || manager == nil {
		return 0
	}
	added := 0
	for _, desc := range manager.ListTools() {
		name := strings.TrimSpace(desc.Name)
		if name == "" {
			continue
		}
		if _, exists := registered[name]; exists {
			continue
		}
		registered[name] = struct{}{}
		catalog.RegisterBuiltinToolFunction(functions.NewRuntimeToolFunction(manager, desc), desc)
		added++
	}
	if added > 0 && l.invalidateSurface != nil {
		l.invalidateSurface()
	}
	return added
}

// prepareLateLSPToolSurface 在 turn 边界登记迟到的 LSP 工具（幂等，非阻塞）。
func prepareLateLSPToolSurface(session *ChatSession) {
	if session == nil || session.ChatLSPLateEnable == nil {
		return
	}
	if added := session.ChatLSPLateEnable.apply(session); added > 0 {
		logpkg.Infof("AICLI LSP tool surface refreshed: %d tool(s) registered at turn boundary", added)
	}
}

// startChatLSPBootstrap 启动后台项目扫描。它立即返回；只有确实需要自动检测的
// 会话才会真的起 goroutine：
//   - LSP 已在启动配置里启用（构造期已挂池）→ 不需要；
//   - 会话使用显式单文件 runtime.yaml（非 .aicli 层）→ 尊重显式选择；
//   - 工作区未被信任（project-scope 门控）→ 不做项目级写入。
func startChatLSPBootstrap(session *ChatSession, toolManager *runtimetools.Manager, runtimeConfig *runtimecfg.RuntimeConfig, configPath string) {
	if session == nil || toolManager == nil {
		return
	}
	if runtimeConfig != nil && runtimeConfig.LSP.Enabled {
		logpkg.Debugf("AICLI LSP auto-scan skipped: lsp already enabled by config")
		return
	}
	trimmedConfigPath := strings.TrimSpace(configPath)
	if trimmedConfigPath != "" && !chatRuntimeConfigLayerMatch(trimmedConfigPath) {
		logpkg.Debugf("AICLI LSP auto-scan skipped: explicit runtime config %s is not an .aicli layer", trimmedConfigPath)
		return
	}
	workspaceRoot := strings.TrimSpace(resolveLocalWorkspacePath(runtimeConfig, session))
	if workspaceRoot == "" {
		return
	}
	if !sessionProjectScopeAllowed(session) {
		logpkg.Debugf("AICLI LSP auto-scan skipped: workspace %s is not trusted for project-scope changes", workspaceRoot)
		return
	}

	late := &chatLSPLateEnable{
		toolManager:       toolManager,
		registered:        registeredMCPToolNames(toolManager.ListTools()),
		invalidateSurface: func() int { return invalidateACPSessionToolSurface(session) },
	}
	session.ChatLSPLateEnable = late

	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				logpkg.Warnf("AICLI LSP auto-scan panicked: %v", recovered)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), chatLSPBootstrapTimeout)
		defer cancel()
		runChatLSPBootstrap(ctx, late, workspaceRoot, trimmedConfigPath, runtimeConfig)
	}()
}

// runChatLSPBootstrap 是后台扫描的主体（可单测：不依赖会话与 UI）。
func runChatLSPBootstrap(ctx context.Context, late *chatLSPLateEnable, workspaceRoot, configPath string, base *runtimecfg.RuntimeConfig) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return
		}
	}
	result, err := projectscan.Scan(workspaceRoot, projectscan.Options{MaxDepth: 3, MaxDirs: 256, MaxEntries: 4096})
	if err != nil {
		logpkg.Debugf("AICLI LSP auto-scan: scan %s failed: %v", workspaceRoot, err)
		return
	}
	if result == nil || len(result.Types) == 0 {
		logpkg.Debugf("AICLI LSP auto-scan: no common project type detected under %s", workspaceRoot)
		return
	}
	serverNames := lspServerNamesForProjectTypes(result.Types)
	if len(serverNames) == 0 {
		logpkg.Debugf("AICLI LSP auto-scan: detected types %v have no built-in LSP preset", result.Types)
		return
	}
	workspacePath := agentconfig.WorkspaceRuntimeConfigPath(workspaceRoot)
	if workspacePath == "" {
		return
	}
	// 显式决定优先：任意存在的层或工作区文件显式声明 lsp.enabled（true/false）时
	// 一律不写——自动检测只补「从未配置过」的场景。
	if agentconfig.RuntimeConfigExplicitKeyInLayers("lsp", "enabled") ||
		agentconfig.RuntimeConfigExplicitKey(workspacePath, "lsp", "enabled") {
		logpkg.Debugf("AICLI LSP auto-scan: lsp.enabled already declared in config; keeping the explicit choice")
		return
	}

	specs := runtimelsp.PresetServersNamed(serverNames)
	runnable, missing := filterRunnableServerSpecs(specs)
	if len(missing) > 0 {
		logpkg.Infof("AICLI LSP auto-scan: %s detected but not installed; skipped (%s)",
			strings.Join(missing, ", "), strings.Join(serverNames, ", "))
	}
	if len(runnable) == 0 {
		return
	}

	path, changed, err := agentconfig.EnsureWorkspaceRuntimeLSP(workspaceRoot, runnable)
	if err != nil {
		logpkg.Warnf("AICLI LSP auto-scan: persist workspace runtime config %s failed: %v", workspacePath, err)
		return
	}
	if !changed {
		logpkg.Debugf("AICLI LSP auto-scan: %s left unchanged", path)
		return
	}
	logpkg.Infof("AICLI LSP auto-enabled from project scan: %s (servers=%s)", path,
		strings.Join(serverSpecNames(runnable), ", "))

	effective := reloadChatLSPConfig(configPath, workspacePath, base)
	if effective == nil || !effective.LSP.Enabled {
		logpkg.Debugf("AICLI LSP auto-scan: effective config does not enable lsp; nothing attached")
		return
	}
	if late != nil && late.attach(effective) {
		logpkg.Infof("AICLI LSP pool attached from scan result; tool functions register at the next turn boundary")
	}
}

// filterRunnableServerSpecs 把本机不存在的服务器过滤掉：写进配置的 enabled 池
// 应该真的可用，否则每次编辑都会退化成「服务器不可用」的提示噪音。
// LookPath 是唯一的执行环境探测，仍属于轻量扫描的一部分。
//
// lspLookPath 是测试缝：单测可替换 PATH 探测，避免依赖真实安装的服务器。
var lspLookPath = exec.LookPath

func filterRunnableServerSpecs(specs []runtimelsp.ServerSpec) ([]runtimelsp.ServerSpec, []string) {
	runnable := make([]runtimelsp.ServerSpec, 0, len(specs))
	missing := make([]string, 0, len(specs))
	for _, spec := range specs {
		if _, err := lspLookPath(spec.Command); err != nil {
			missing = append(missing, spec.Name)
			continue
		}
		runnable = append(runnable, spec)
	}
	return runnable, missing
}

// reloadChatLSPConfig 在写入工作区层之后重算生效配置：
//   - 工作区文件属于 .aicli 层栈（工作区即 CWD 的常见情形）→ 走既有分层合并
//     加载（缓存按 mtime 失效，刚写入的内容一定会被读到）；
//   - 工作区不在层栈内（CWD 与工作区不同）→ 单文件加载扫描写入的文件，只把
//     lsp 段叠加到启动配置上，其余设置仍来自会话原有来源。
func reloadChatLSPConfig(configPath, workspacePath string, base *runtimecfg.RuntimeConfig) *runtimecfg.RuntimeConfig {
	// 只有扫描写入的文件确实属于层栈时才走分层合并；否则（工作区与 CWD
	// 不同，层栈只覆盖 CWD 的 .aicli 层）合并结果看不到刚写入的 lsp 段，
	// 必须落到下面的单文件叠加，否则本次会话永远挂不上池。
	if chatRuntimeConfigLayerMatch(workspacePath) {
		if configPath != "" && chatRuntimeConfigLayerMatch(configPath) {
			if reloaded, _, err := loadCachedRuntimeConfig(configPath); err == nil && reloaded != nil {
				return reloaded
			}
		}
		if reloaded, _, err := loadCachedRuntimeConfig(workspacePath); err == nil && reloaded != nil {
			return reloaded
		}
		return base
	}
	overlay, _, err := loadSingleFileChatRuntimeConfig(workspacePath)
	if err != nil || overlay == nil {
		return base
	}
	effective := cloneRuntimeConfig(base)
	if effective == nil {
		effective = runtimecfg.DefaultRuntimeConfig()
	}
	effective.LSP = overlay.LSP
	return effective
}

// serverSpecNames 返回服务器名列表（按传入顺序）。
func serverSpecNames(specs []runtimelsp.ServerSpec) []string {
	names := make([]string, 0, len(specs))
	for _, spec := range specs {
		if name := strings.TrimSpace(spec.Name); name != "" {
			names = append(names, name)
		}
	}
	return names
}

// projectTypeLSPServers 把 projectscan 识别的项目类型映射到内置 LSP 预设
// （internal/lsp.PresetServers）的服务器名；顺序即稳定的日志/配置顺序。
// 没有内置预设的类型（java/dotnet/ruby/php）在此处自然被跳过。
var projectTypeLSPServers = []struct {
	typ    projectscan.Type
	server string
}{
	{projectscan.TypeGo, "gopls"},
	{projectscan.TypeNode, "typescript"},
	{projectscan.TypePython, "pyright"},
	{projectscan.TypeRust, "rust-analyzer"},
}

// lspServerNamesForProjectTypes 返回 detected 类型中有内置 LSP 预设的服务器名
// （按 projectTypeLSPServers 顺序，稳定且可去重）。
func lspServerNamesForProjectTypes(types []projectscan.Type) []string {
	if len(types) == 0 {
		return nil
	}
	detected := make(map[projectscan.Type]struct{}, len(types))
	for _, typ := range types {
		detected[typ] = struct{}{}
	}
	names := make([]string, 0, len(types))
	for _, mapping := range projectTypeLSPServers {
		if _, ok := detected[mapping.typ]; ok {
			names = append(names, mapping.server)
		}
	}
	return names
}
