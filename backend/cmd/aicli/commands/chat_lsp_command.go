package commands

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimelsp "github.com/wwsheng009/ai-agent-runtime/internal/lsp"
	runtimetools "github.com/wwsheng009/ai-agent-runtime/internal/tools"
)

// chat_lsp_command.go 实现 chat/TUI 内的 /lsp 命令族：
//
//   - 只读观测：status/list/servers（池状态、生命周期、诊断配置）；
//   - 按需诊断：diagnostics <file>（W7 工具的用户侧等价入口）；
//   - 手动恢复：restart/start（docs/lsp 02 §2.2 L2「手动重启仍可用」）。
//
// 命令只读取会话工具管理器上的 LSP 池（ChatToolManager）：池缺失或未启用
// 一律按「LSP 未启用」降级输出，不视为错误（docs/lsp A11：LSP 可能不存在，
// 编辑与命令本身不受影响）。
//
// 忙时语义（见 chat_runtime_command_registry.go 的注册声明）：
//   - 只读子命令 inline+read：忙时立即执行（无 Phase B 效应）；
//   - restart/start queue+live：忙时排队到回合结束后执行，不打断在途编辑。

const chatLSPCommandUsage = `用法:
  /lsp                      显示 LSP 状态（等价 /lsp status）
  /lsp status               显示 LSP 池状态：开关、工作区、server 生命周期与诊断配置
  /lsp list | servers       同 /lsp status
  /lsp diagnostics <file>   读取指定文件当前诊断（只读；有界等待后按降级语义返回）
  /lsp baseline [--days N]  归因会话日志出 §4.3 基线报告（默认最近 14 天；--since/--root 可选）
  /lsp restart [name]       重启指定 server；缺省重启全部已配置 server
  /lsp start [name]         启动（预热）指定 server；缺省启动全部（已就绪者跳过）
  /lsp help                 显示本帮助`

const (
	// chatLSPServerTimeout 是单个 server 启动/重启的等待上限：覆盖 initialize
	// 握手（默认 startupTimeout=15s），超时按失败返回但不中断会话。
	chatLSPServerTimeout = 30 * time.Second
	// chatLSPDiagnosticsTimeout 是 /lsp diagnostics 的整体兜底上限；实际等待
	// 仍受 diagnostics.waitMs 约束（A10），这里只覆盖读文件与调度开销。
	chatLSPDiagnosticsTimeout = 10 * time.Second
)

// executeStructuredLSPCommand 把 /lsp 结果投影为统一的命令单元。
//
// 只读长文档（status/list/servers/diagnostics）按 §5.1 判据进副屏
// （ScreenDocument）：超过内联行预算且当前具备副屏能力时才开屏，短输出
// （未覆盖文件的说明、空诊断）与无副屏场景保持主屏内联；restart/start
// 回执、help 用法与参数错误一律留在主屏内联单元格。
func executeStructuredLSPCommand(session *ChatSession, command string) CommandResult {
	text := chatLSPCommandText(session, command)
	id, title, ok := chatLSPReadOnlyScreenIdentity(command)
	if !ok || !chatLSPPoolEnabled(session) {
		return commandTextResult(text)
	}
	doc := chatScreenTextDoc(text)
	if !chatScreenShouldOpenForDocument(session, doc) {
		return commandTextResult(text)
	}
	return chatScreenDocResult(chatScreenLSPReadOnlySpec(id, title, text))
}

// chatLSPReadOnlyScreenIdentity 判定 /lsp 的只读长文档变体并给出副屏身份
// （与 chatLSPCommandText 的别名表保持一致）；restart/start、help 与错误
// 输出不进入副屏。
func chatLSPReadOnlyScreenIdentity(command string) (string, string, bool) {
	args := splitChatCommandFields(extractCommandArgument(command))
	if len(args) == 0 {
		return "lsp.status", "LSP 服务器状态", true
	}
	switch strings.ToLower(args[0]) {
	case "status", "list", "ls", "servers", "show":
		return "lsp.status", "LSP 服务器状态", true
	case "diagnostics", "diag", "check":
		return "lsp.diagnostics", "LSP 诊断", true
	case "baseline":
		return "lsp.baseline", "LSP 基线报告", true
	default:
		return "", "", false
	}
}

// chatLSPPoolEnabled 报告会话是否挂载了已启用的 LSP 池；未启用时 /lsp 的
// 输出是短提示卡（A11），不占用副屏。
func chatLSPPoolEnabled(session *ChatSession) bool {
	manager := chatLSPManager(session)
	return manager != nil && manager.LSPEnabled()
}

// chatLSPCommandText 解析并执行 /lsp 子命令，返回纯文本结果。
func chatLSPCommandText(session *ChatSession, command string) string {
	args := splitChatCommandFields(extractCommandArgument(command))
	if len(args) == 0 {
		return chatLSPStatusText(session)
	}
	switch strings.ToLower(args[0]) {
	case "help", "-h", "--help":
		return chatLSPCommandUsage
	case "status", "list", "ls", "servers", "show":
		return chatLSPStatusText(session)
	case "diagnostics", "diag", "check":
		if len(args) < 2 {
			return "错误: 需要指定文件路径\n用法: /lsp diagnostics <file>"
		}
		return chatLSPDiagnosticsText(session, args[1])
	case "baseline":
		return chatLSPBaselineText(args[1:])
	case "restart":
		return chatLSPRestartText(session, args[1:])
	case "start":
		return chatLSPStartText(session, args[1:])
	default:
		return fmt.Sprintf("错误: 未知子命令 %q\n%s", args[0], chatLSPCommandUsage)
	}
}

// chatLSPStatusText 渲染池状态：开关、工作区、诊断阈值与每个 server 的
// 生命周期记录（`lsp_servers` 工具的用户侧等价视图，只读、不触发启动）。
func chatLSPStatusText(session *ChatSession) string {
	manager := chatLSPManager(session)
	if manager == nil || !manager.LSPEnabled() {
		return chatLSPDisabledText(session)
	}
	lines := []string{"LSP 已启用"}
	if root := strings.TrimSpace(manager.LSPRoot()); root != "" {
		lines = append(lines, "工作区: "+root)
	}
	if path := chatLSPConfigPath(session); path != "" {
		lines = append(lines, "配置: "+path)
	}
	if cfg, ok := manager.LSPDiagnosticsConfig(); ok {
		lines = append(lines, fmt.Sprintf(
			"诊断: scope=%s max_items=%d max_chars=%d wait_ms=%d degrade=%s tool=%s",
			cfg.Scope, cfg.MaxItems, cfg.MaxChars, cfg.WaitMS, cfg.DegradeMode, chatLSPBoolWord(cfg.ToolEnabled)))
	}
	statuses := manager.LSPStatuses()
	if len(statuses) == 0 {
		lines = append(lines, "Server: （没有配置任何已启用的 server）")
		return strings.Join(lines, "\n")
	}
	lines = append(lines, fmt.Sprintf("Server（%d 个）:", len(statuses)))
	for _, status := range statuses {
		lines = append(lines, chatLSPServerStatusLine(status))
	}
	if hint := chatLSPRecoveryHint(statuses); hint != "" {
		lines = append(lines, hint)
	}
	return strings.Join(lines, "\n")
}

// chatLSPRecoveryHint 为崩溃/不可用成员给出可行动的手动恢复入口。第十一轮把
// "崩溃 + 重启预算耗尽"的原因结构化了，但用户看到原因后仍需知道下一步：自动
// 恢复在预算耗尽（默认每成员每 10 分钟 1 次）后会停下，二进制缺失的成员也不会
// 再自动重试——两者都能用 /lsp restart 显式重试（重启会重新检查 PATH）。
// 全健康时不产生任何输出（不制造噪音）。
func chatLSPRecoveryHint(statuses []runtimelsp.ServerStatus) string {
	names := make([]string, 0, len(statuses))
	for _, status := range statuses {
		switch status.State {
		case runtimelsp.StateCrashed, runtimelsp.StateUnavailable:
			if name := strings.TrimSpace(status.Name); name != "" {
				names = append(names, name)
			}
		}
	}
	if len(names) == 0 {
		return ""
	}
	return fmt.Sprintf(
		"提示: %s 需要恢复；`/lsp restart %s` 可手动重启（缺省重启全部；重启会重新检查二进制）。",
		strings.Join(names, "、"), names[0],
	)
}

// chatLSPDisabledText 是「LSP 不存在」的降级输出：给出配置入口与自动检测
// 事实，不报错、不阻断（A11）。
func chatLSPDisabledText(session *ChatSession) string {
	lines := []string{
		"LSP 未启用",
		"当前会话没有挂载语言服务器池；编辑工具不会追加诊断（编辑本身不受影响）。",
	}
	if path := chatLSPConfigPath(session); path != "" {
		lines = append(lines, "当前 runtime 配置: "+path)
	}
	lines = append(lines, "启用方式: 在 runtime 配置中设置 lsp.enabled: true；工作区首次自动检测识别到项目类型时也会写入 <workspace>/.aicli/runtime.yaml。")
	return strings.Join(lines, "\n")
}

// chatLSPDiagnosticsText 读取指定文件的当前诊断（W7 口径）：归属判定不过
// 则明确说明未发起请求；超时/缺失按降级语义返回文本。
func chatLSPDiagnosticsText(session *ChatSession, path string) string {
	manager := chatLSPManager(session)
	if manager == nil || !manager.LSPEnabled() {
		return chatLSPDisabledText(session)
	}
	target := strings.TrimSpace(path)
	if target == "" {
		return "错误: 需要指定文件路径\n用法: /lsp diagnostics <file>"
	}
	ctx, cancel := context.WithTimeout(context.Background(), chatLSPDiagnosticsTimeout)
	defer cancel()
	text, handled, err := manager.LSPReport(ctx, target)
	if err != nil {
		return "错误: 读取诊断失败: " + err.Error()
	}
	if !handled {
		return fmt.Sprintf("LSP 未覆盖该文件: %s\n没有已启用的 server 声明该文件类型（按扩展名/文件名判定；未发出通知也未读取诊断）。", target)
	}
	if trimmed := strings.TrimRight(text, "\n"); trimmed != "" {
		return trimmed
	}
	return "该文件没有诊断。"
}

// chatLSPRestartText 手动重启指定 server；缺省重启全部（L2 恢复入口）。
func chatLSPRestartText(session *ChatSession, args []string) string {
	manager := chatLSPManager(session)
	if manager == nil || !manager.LSPEnabled() {
		return chatLSPDisabledText(session)
	}
	names := chatLSPTargetNames(manager, args)
	if len(names) == 0 {
		return "没有配置任何已启用的 LSP server"
	}
	lines := make([]string, 0, len(names))
	for _, name := range names {
		ctx, cancel := context.WithTimeout(context.Background(), chatLSPServerTimeout)
		err := manager.LSPRestart(ctx, name)
		cancel()
		if err != nil {
			lines = append(lines, fmt.Sprintf("✗ 重启 %s 失败: %v", name, err))
			continue
		}
		lines = append(lines, "✓ 已重启 "+name)
	}
	return strings.Join(lines, "\n")
}

// chatLSPStartText 手动启动（预热）指定 server；缺省启动全部，已就绪者跳过。
func chatLSPStartText(session *ChatSession, args []string) string {
	manager := chatLSPManager(session)
	if manager == nil || !manager.LSPEnabled() {
		return chatLSPDisabledText(session)
	}
	names := chatLSPTargetNames(manager, args)
	if len(names) == 0 {
		return "没有配置任何已启用的 LSP server"
	}
	lines := make([]string, 0, len(names))
	for _, name := range names {
		if state, ok := chatLSPServerState(manager, name); ok && state == runtimelsp.StateReady {
			lines = append(lines, "● 已就绪，跳过 "+name)
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), chatLSPServerTimeout)
		err := manager.LSPStart(ctx, name)
		cancel()
		if err != nil {
			lines = append(lines, fmt.Sprintf("✗ 启动 %s 失败: %v", name, err))
			continue
		}
		lines = append(lines, "✓ 已启动 "+name)
	}
	return strings.Join(lines, "\n")
}

func chatLSPManager(session *ChatSession) *runtimetools.Manager {
	if session == nil {
		return nil
	}
	return session.ChatToolManager
}

func chatLSPConfigPath(session *ChatSession) string {
	if session == nil {
		return ""
	}
	if path := strings.TrimSpace(session.RuntimeConfigPath); path != "" {
		return path
	}
	// 普通 TUI 会话（未走 profile 投影）没有已解析路径：按 LSP 配置的实际
	// 来源回退到工作区层 .aicli/runtime.yaml（项目层通常写在这里），再退到
	// 用户层；resolveGlobalRuntimeConfigPath 只返回真实存在的文件。
	if manager := chatLSPManager(session); manager != nil {
		if path := agentconfig.WorkspaceRuntimeConfigPath(manager.LSPRoot()); path != "" {
			if _, err := os.Stat(path); err == nil {
				return path
			}
		}
	}
	return resolveGlobalRuntimeConfigPath(session.Config)
}

// chatLSPTargetNames 解析目标 server：显式名称原样返回；缺省取池中全部
// 已配置成员（按配置顺序）。
func chatLSPTargetNames(manager *runtimetools.Manager, args []string) []string {
	if len(args) > 0 {
		if name := strings.TrimSpace(args[0]); name != "" {
			return []string{name}
		}
	}
	statuses := manager.LSPStatuses()
	names := make([]string, 0, len(statuses))
	for _, status := range statuses {
		if name := strings.TrimSpace(status.Name); name != "" {
			names = append(names, name)
		}
	}
	return names
}

func chatLSPServerState(manager *runtimetools.Manager, name string) (runtimelsp.ServerState, bool) {
	trimmed := strings.TrimSpace(name)
	for _, status := range manager.LSPStatuses() {
		if strings.EqualFold(strings.TrimSpace(status.Name), trimmed) {
			return status.State, true
		}
	}
	return "", false
}

func chatLSPServerStatusLine(status runtimelsp.ServerStatus) string {
	name := strings.TrimSpace(status.Name)
	if name == "" {
		name = "(未命名)"
	}
	parts := []string{chatLSPServerStateMarker(status.State), name}
	if language := strings.TrimSpace(status.Language); language != "" {
		parts = append(parts, "["+language+"]")
	}
	parts = append(parts, string(status.State))
	if status.PID > 0 {
		parts = append(parts, fmt.Sprintf("pid=%d", status.PID))
	}
	if status.Restarts > 0 {
		parts = append(parts, fmt.Sprintf("restarts=%d", status.Restarts))
	}
	// 冷启动延迟（启动→首个发布）：跨会话基线已采集（§4.3），这里让单会话也能
	// 直接判读"这次为什么慢"；未发布（0）时不显示，避免噪音。
	if status.FirstPublishMS > 0 {
		parts = append(parts, fmt.Sprintf("first_publish=%dms", status.FirstPublishMS))
	}
	line := strings.Join(parts, " ")
	reason := strings.TrimSpace(status.Reason)
	if reason == "" {
		reason = strings.TrimSpace(status.LastError)
	}
	if reason != "" {
		line += " · " + reason
	}
	// 懒启动成员在首次使用前无法从注册表得知二进制是否存在：这里做一次
	// 轻量 LookPath 探测（与自动扫描 filterRunnableServerSpecs 共用测试缝，
	// 见 chat_lsp_bootstrap.go），把「首次使用必然降级」提前暴露在状态页，
	// 避免用户等到第一次编辑才知道该 server 装不上。
	if status.State == runtimelsp.StateStarting {
		if command := strings.TrimSpace(status.Command); command != "" {
			if _, err := lspLookPath(command); err != nil {
				line += fmt.Sprintf("（未找到可执行文件 %s，首次使用将降级）", command)
			}
		}
	}
	return line
}

func chatLSPServerStateMarker(state runtimelsp.ServerState) string {
	switch state {
	case runtimelsp.StateReady:
		return "●"
	case runtimelsp.StateStarting:
		return "◐"
	case runtimelsp.StateCrashed, runtimelsp.StateUnavailable:
		return "!"
	case runtimelsp.StateStopped:
		return "○"
	default:
		return "○"
	}
}

func chatLSPBoolWord(enabled bool) string {
	if enabled {
		return "on"
	}
	return "off"
}
