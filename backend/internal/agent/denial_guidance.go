package agent

import (
	"fmt"
	"regexp"
	"strings"

	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// 模型可见的拒绝错误码（稳定文本标记，随 tool_result 进入模型上下文）。
// 机器可读事件字段仍使用 errors.ErrAgentReadOnly；这里只负责"模型读得懂"的
// 细分原因，帮助模型按类别选择出路，而不是靠猜。
const (
	DenialCodeReadOnlyTool          = "ERR_READONLY_TOOL"
	DenialCodeReadOnlyShell         = "ERR_READONLY_SHELL"
	DenialCodeReadOnlyShellCompound = "ERR_READONLY_SHELL_COMPOUND"
	DenialCodeReadOnlyShellDynamic  = "ERR_READONLY_SHELL_DYNAMIC"
	DenialCodeReadOnlyShellSecret   = "ERR_READONLY_SHELL_SECRET"
)

// 只读拒绝熔断阈值（M6）。连续拒绝达到 Escalate 阈值后，向父会话邮箱发送
// subagent.requires_write 事件并给模型强提示；达到 HardStop 阈值后终止本轮，
// 终态消息明确"goal requires write access; re-spawn with read_only=false"，
// 不再以 step=unlimited 无限空转。
const (
	ReadOnlyDenyEscalateThreshold = 3
	ReadOnlyDenyHardStopThreshold = 5
)

// renderDenialGuidance 把 read_only 策略的裸错误串升级为"原因+规则+修复"的
// 模型可读文本；非只读拒绝原样返回（向后兼容，不改变其他策略的语义）。
// 注意：文本中保留原始 reason 行（含 "read-only"），使下游
// classifyDeniedPolicy / enrichDeniedToolMetadata 的字符串分类仍然命中。
func renderDenialGuidance(toolName, reason string) string {
	if classifyDeniedPolicy(reason) != "read_only" {
		return reason
	}
	code := denialCodeForReason(reason)
	return fmt.Sprintf(
		"[TOOL_DENIED:%s] %s\nboundary: read_only (hard child boundary; approval and bypass_permissions cannot widen it)\nrule: %s\nfix: %s",
		code, toolName, reason, denialFixForCode(code))
}

// denialCodeForReason 依据策略错误串的稳定片段细分错误码。片段来自
// internal/policy/tool_policy.go 的 AllowTool / AllowToolCallWithContext 文案，
// 若上游文案调整，此处按语义（compound/dynamic/write-like/shell）继续命中。
func denialCodeForReason(reason string) string {
	lower := strings.ToLower(reason)
	switch {
	case strings.Contains(lower, "compound"):
		return DenialCodeReadOnlyShellCompound
	case strings.Contains(lower, "redirection") || strings.Contains(lower, "dynamic command"):
		return DenialCodeReadOnlyShellDynamic
	case strings.Contains(lower, "sensitive"):
		return DenialCodeReadOnlyShellSecret
	case strings.Contains(lower, "write-like tool") || strings.Contains(lower, "background command"):
		return DenialCodeReadOnlyTool
	default:
		return DenialCodeReadOnlyShell
	}
}

// denialFixForCode 给出与错误码对应的出路。写型工具被拒时直接引用共享出路文案，
// 与子代理 prompt 横幅、父代理 spawn 报告保持同源。
func denialFixForCode(code string) string {
	switch code {
	case DenialCodeReadOnlyTool:
		return "Stop attempting this tool. " + runtimepolicy.ReadOnlyEscalationPathText
	case DenialCodeReadOnlyShellCompound:
		return "Split the command: submit exactly one read-only command per shell.commands entry (chained/compound commands cannot be validated independently)."
	case DenialCodeReadOnlyShellDynamic:
		return "The read-only boundary rejects variable expansion, command substitution, and redirection to any target other than the null device (/dev/null on POSIX, NUL on Windows); submit plain single commands only."
	case DenialCodeReadOnlyShellSecret:
		return "Reading secret material (.env, keys, credential stores) is outside the read-only boundary. Report the need to the parent instead of retrying with another reader."
	default:
		return "The command is not on the read-only allow list. Use git status/diff/log/show, rg, ls/glob, Get-Content, pwd, echo, or " + runtimepolicy.ReadOnlyEscalationPathText
	}
}

// ordinalSuffix 把 n 渲染成英文序数词（1st/2nd/3rd/4th…），用于强度递增的
// 熔断提示文案。
func ordinalSuffix(n int) string {
	rem100 := n % 100
	if rem100 >= 11 && rem100 <= 13 {
		return "th"
	}
	switch n % 10 {
	case 1:
		return "st"
	case 2:
		return "nd"
	case 3:
		return "rd"
	default:
		return "th"
	}
}

// readOnlyDenyEscalationAdvisory 在连续拒绝达到 Escalate 阈值时追加到模型可见
// 文本与 subagent.requires_write 事件中，明确要求停止尝试并给出终止选项。
func readOnlyDenyEscalationAdvisory(streak int) string {
	return fmt.Sprintf(
		"This is the %d%s consecutive read-only denial in this run. STOP attempting write operations: this agent cannot mutate the workspace. Conclude with a report to the parent listing the exact change you needed, and ask the parent to apply it or re-spawn you with read_only=false.",
		streak, ordinalSuffix(streak))
}

// readOnlyDenialLimitReachedMessage 是硬停止阈值触发时的终态消息。
func readOnlyDenialLimitReachedMessage(streak, threshold int) string {
	return fmt.Sprintf(
		"Run stopped after %d consecutive read-only denials (threshold %d): the goal requires write access that this read-only child cannot obtain. Re-spawn with read_only=false or accept the read-only report.",
		streak, threshold)
}

var writeIntentPattern = regexp.MustCompile(`(?i)\b(write|writes?|editing?|create|creates?|modify|implement|apply[\s_-]?patch|append|delete|save|commit|install|scaffold|refactor|patch)\b`)

var writeIntentChineseSignals = []string{
	"写文件", "写入", "创建", "修改", "生成文件", "保存为", "删除文件", "实现功能", "打补丁", "写代码", "改动",
}

// readOnlyIntentSignals 标记「目标是只读核验」的措辞。M5 的误报形态
// （2026-09-26 真机）："复查工作区未提交改动"/"review uncommitted changes" 这类
// 目标只是核对现状，却命中弱写意图词（改动）而收到 goal_appears_to_require_writes，
// 父模型据此怀疑 read_only 设置错误。只读措辞在场且没有强写意图短语时不再告警。
var readOnlyIntentSignals = []string{
	"检查", "复查", "查看", "分析", "审查", "审计", "核对", "验证", "只读",
	"不修改", "不改动", "无需修改", "请勿修改",
	"read-only", "read only", "inspect", "review", "analyze", "analyse",
	"audit", "verify", "check", "do not modify", "do not write",
	"without modifying", "without writing", "no changes",
}

// strongWriteIntentPattern / strongWriteIntentChinesePattern 是「动词+对象」
// 级别的强写意图短语：命中即视为确实要产出/修改工件，即使目标同时提到检查、
// 验证（例如 "检查并修改 X 的代码"）。对象白名单刻意收窄——"create a report"
// 这类只产出文字、只读子代理也能完成的目标不算。
var strongWriteIntentPattern = regexp.MustCompile(`(?i)\b(?:write|create|modify|edit|implement|apply|delete|remove|save|refactor|patch)\b[\s_-]+(?:the\s+|a\s+|an\s+)?(?:files?|code|patch|patches|modules?|functions?|tests?|config(?:uration)?|scripts?|packages?|changes?)\b`)

var strongWriteIntentChinesePattern = regexp.MustCompile(`(?:写文件|写入文件|创建文件|新建文件|生成文件|删除文件|修改文件|编辑文件|修改代码|改写代码|写代码|实现功能|打补丁|保存为|重构代码|(?:修改|编辑|更新|调整)[^,。;；]{0,12}?(?:代码|文件|模块|配置))`)

// goalHasWriteIntent 对子代理 goal 做启发式写意图检测（M5）。只用于非阻断的
// route_warnings 提示，允许少量误报；命中并不改变 spawn 行为。
func goalHasWriteIntent(goal string) bool {
	if strongWriteIntentPattern.MatchString(goal) || strongWriteIntentChinesePattern.MatchString(goal) {
		return true
	}
	if goalHasReadOnlyIntent(goal) {
		return false
	}
	if writeIntentPattern.MatchString(goal) {
		return true
	}
	lower := strings.ToLower(goal)
	for _, signal := range writeIntentChineseSignals {
		if strings.Contains(lower, signal) {
			return true
		}
	}
	return false
}

func goalHasReadOnlyIntent(goal string) bool {
	lower := strings.ToLower(goal)
	for _, signal := range readOnlyIntentSignals {
		if strings.Contains(lower, signal) {
			return true
		}
	}
	return false
}

// writeIntentRouteWarning 是 M5 追加到 route_warnings 的固定文案，父模型在
// spawn 工具结果里即时可见，可在决策点纠正 read_only 与 goal 的不匹配。
func writeIntentRouteWarning() string {
	return "goal_appears_to_require_writes: read_only=true will strip all write-like tools from this child and deny them at execution; set read_only=false for writer tasks, or narrow the goal to read-only analysis and report findings back to the parent"
}

// shellIntentPattern / shellIntentChineseSignals 检测「目标确实要跑命令」的措辞
// （运行/执行 + 命令、测试、构建、git 等）。与 M5 写意图一样只用于非阻断的
// route_warnings，允许少量误报；命中不改变 spawn 行为。2026-09-30 真机教训：
// 派发时白名单漏掉 shell，子代理整轮在猜工具名（tool not found: commands）。
var shellIntentPattern = regexp.MustCompile(`(?i)\b(?:run|runs|running|execute|executes?|invoke|launch)\b[^.\n]{0,48}?\b(?:shell|commands?|scripts?|tests?|test suite|build|compile|lint|go test|npm|pnpm|yarn|cargo|pytest|make|docker|git)\b|\b(?:go test|go build|npm (?:install|ci|test|run)|pnpm\b|cargo (?:build|test|run)|pytest\b|git (?:status|diff|log|show))\b`)

var shellIntentChineseSignals = []string{
	"运行命令", "执行命令", "跑命令", "运行 shell", "执行 shell",
	"运行测试", "跑测试", "执行测试", "运行 go test", "跑 go test", "执行 go test",
	"运行构建", "执行构建", "构建项目", "编译项目",
}

// goalHasShellIntent 对子代理 goal 做启发式命令执行意图检测。命中且派发出的
// 工具面/能力面没有 shell 时，spawn 结果会带 goal_appears_to_require_commands
// 告警，让父模型在决策点补上 shell 白名单，而不是等子代理白跑一轮。
func goalHasShellIntent(goal string) bool {
	if shellIntentPattern.MatchString(goal) {
		return true
	}
	lower := strings.ToLower(goal)
	for _, signal := range shellIntentChineseSignals {
		if strings.Contains(lower, signal) {
			return true
		}
	}
	return false
}

// mutatingShellIntentPattern / mutatingShellChineseSignals 检测只读 shell 无法
// 放行的命令（构建/测试/安装类）。read_only=true 的子代理只接受逐条分类的
// 只读命令，这类目标需要 read_only=false。
var mutatingShellIntentPattern = regexp.MustCompile(`(?i)\b(?:go test|go build|go run|npm (?:install|ci|test|run)|pnpm\b|yarn\b|cargo (?:build|test|run)|pytest\b|mvn\b|gradle\b|docker\b|pip install|run the build|build the (?:project|code|binary|module)|compile the (?:project|code|module))\b`)

var mutatingShellChineseSignals = []string{
	"运行测试", "跑测试", "执行测试", "运行 go test", "跑 go test", "执行 go test",
	"运行构建", "执行构建", "构建项目", "编译项目", "安装依赖",
}

func goalNeedsMutatingShell(goal string) bool {
	if mutatingShellIntentPattern.MatchString(goal) {
		return true
	}
	lower := strings.ToLower(goal)
	for _, signal := range mutatingShellChineseSignals {
		if strings.Contains(lower, signal) {
			return true
		}
	}
	return false
}

// shellSurfaceRouteWarning 在子代理工具面/能力面完全没有 shell 时追加到
// route_warnings，父模型在 spawn 结果里即时可见并可纠正。
func shellSurfaceRouteWarning() string {
	return "goal_appears_to_require_commands: this child's resolved tool surface has no usable shell tool (tools_whitelist omitted \"shell\", or the parent policy does not grant exec_shell); include \"shell\" in tools_whitelist and keep read_only=false for tasks that need general shell syntax, or narrow the goal to what the granted tools can do"
}

// readOnlyShellRouteWarning 在 read_only=true 但目标需要构建/测试类命令时追加。
func readOnlyShellRouteWarning() string {
	return "goal_appears_to_require_mutating_shell: read_only=true only permits individually classified read-only commands (git status/diff/log/show, rg, ls, pwd, ...); build/test/install commands such as go test will be hard-denied; set read_only=false for this child, or narrow the goal to static inspection"
}

// enrichReadOnlyToolDescriptions 在只读子代理的模型可见工具面上，给 shell 工具
// description 动态追加 READ-ONLY MODE 预告，让模型第一轮就知道 shell 边界，
// 而不是逐个命令试错后被拒。非只读策略原样返回。
func enrichReadOnlyToolDescriptions(tools []types.ToolDefinition, policy *runtimepolicy.ToolExecutionPolicy) []types.ToolDefinition {
	if policy == nil || !policy.ReadOnly {
		return tools
	}
	const readOnlyShellModeNote = "\n\nREAD-ONLY MODE: only read-only commands are allowed (git status/diff/log/show, rg, ls/glob, Get-Content, Select-Object, pwd, echo). Write, mutating, compound, or file-redirecting commands are hard-denied; only redirection to the null device (2>/dev/null on POSIX, 2>NUL on Windows) stays allowed. If you need to change files, return the change to the parent instead."
	changed := false
	out := make([]types.ToolDefinition, 0, len(tools))
	for _, def := range tools {
		if (def.Name == "shell" || def.Name == "bash") && !strings.Contains(def.Description, "READ-ONLY MODE") {
			def.Description += readOnlyShellModeNote
			changed = true
		}
		out = append(out, def)
	}
	if !changed {
		return tools
	}
	return out
}
