package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/capability"
	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
)

// handleCommand 处理命令
func handleCommand(session *ChatSession, command string, noInteractive bool) bool {
	// 首先检查 /shell 和 /cmd 命令（带参数）
	cmdLower := strings.ToLower(strings.TrimSpace(command))
	if strings.HasPrefix(cmdLower, "/shell") || strings.HasPrefix(cmdLower, "/cmd") {
		argument := extractCommandArgument(command)
		if strings.TrimSpace(argument) == "" {
			fmt.Println("错误: 需要指定 shell 命令")
			fmt.Println("用法: /shell [--output-bytes-cap <bytes> | --disable-output-cap] <命令>")
			fmt.Println("      /cmd   [--output-bytes-cap <bytes> | --disable-output-cap] <命令>")
			return false
		}
		result, err := executeShellCommandDetailed(session, argument)
		if err != nil {
			fmt.Printf("错误: %v\n", err)
			return false
		}
		// 将命令输出作为消息发送给 AI
		aiInput := buildShellCommandAIInput(result)
		response, err := sendMessage(session, aiInput)
		if err != nil {
			fmt.Printf("错误: %v\n", err)
			return false
		}
		if !session.Stream && response != "" {
			fmt.Printf("助手> %s\n", response)
		}
		return false
	}
	if strings.HasPrefix(cmdLower, "/function ") || strings.HasPrefix(cmdLower, "/describe ") {
		name, jsonOutput := extractCommandArgumentOptions(command)
		if name == "" {
			fmt.Println("错误: 需要指定 function 名称")
			fmt.Println("用法: /function <name> [--json] 或 /describe <name> [--json]")
			return false
		}
		fmt.Println(formatFunctionDescriptor(session, name, jsonOutput))
		return false
	}
	if strings.HasPrefix(cmdLower, "/functions ") || strings.HasPrefix(cmdLower, "/catalog ") {
		prompt, jsonOutput := extractCommandArgumentOptions(command)
		if prompt == "" && jsonOutput {
			fmt.Println(formatFunctionCatalogSummary(session, true))
			return false
		}
		if prompt == "" {
			fmt.Println("错误: 需要提供 prompt 预览最终暴露集合")
			fmt.Println("用法: /functions <prompt> [--json] 或 /catalog <prompt> [--json]")
			return false
		}
		fmt.Println(formatFunctionExposurePreview(session, prompt, jsonOutput))
		return false
	}
	if strings.HasPrefix(cmdLower, "/call ") || strings.HasPrefix(cmdLower, "/tool ") {
		return handleDirectFunctionCommand(session, command)
	}
	if strings.HasPrefix(cmdLower, "/skill ") {
		return handleDirectSkillCommand(session, command)
	}
	if strings.HasPrefix(cmdLower, "/sessions ") {
		filter := session.SessionFilter
		filter.Query = strings.TrimSpace(extractCommandArgument(command))
		if err := printChatSessionSummaries(session.SessionManager, session.SessionUserID, currentRuntimeSessionID(session), filter); err != nil {
			fmt.Printf("错误: %v\n", err)
		}
		return false
	}
	if strings.HasPrefix(cmdLower, "/load ") {
		sessionID := extractCommandArgument(command)
		if strings.TrimSpace(sessionID) == "" {
			fmt.Println("错误: 需要指定会话 ID")
			fmt.Println("用法: /load <session-id>")
			return false
		}
		if err := loadRuntimeConversation(session, sessionID); err != nil {
			fmt.Printf("错误: %v\n", err)
			return false
		}
		fmt.Println("会话已加载")
		printCurrentRuntimeSession(session)
		if hasVisibleChatHistory(session) {
			fmt.Println()
			printVisibleChatHistory(session, "已加载历史会话")
		}
		return false
	}
	if strings.HasPrefix(cmdLower, "/title ") {
		title := extractCommandArgument(command)
		if strings.TrimSpace(title) == "" {
			fmt.Println("错误: 需要指定会话标题")
			fmt.Println("用法: /title <title>")
			return false
		}
		if session.RuntimeSession == nil {
			fmt.Println("错误: 当前没有可更新的会话")
			return false
		}
		session.RuntimeSession.UpdateTitle(title)
		if err := syncRuntimeSessionFromChat(session); err != nil {
			fmt.Printf("错误: %v\n", err)
			return false
		}
		fmt.Println("会话标题已更新")
		return false
	}
	if strings.HasPrefix(cmdLower, "/image") {
		return handleImageCommand(session, command)
	}
	if strings.HasPrefix(cmdLower, "/queue") {
		return handleQueueCommand(session, command)
	}
	if strings.HasPrefix(cmdLower, "/compact") {
		return handleCompactCommand(session, command)
	}
	if commandMatches(cmdLower, "/model") {
		return handleModelCommand(session, command, noInteractive)
	}
	if commandMatches(cmdLower, "/permission-mode") || commandMatches(cmdLower, "/mode") {
		return handlePermissionModeCommand(session, command)
	}
	if strings.HasPrefix(cmdLower, "/approval-reuse") {
		return handleApprovalReuseCommand(session, command)
	}

	// 处理其他命令
	cmd := cmdLower
	switch cmd {
	case "/exit", "/quit", "/q":
		fmt.Println("再见！")
		return true

	case "/clear", "/cls":
		session.Messages = []map[string]interface{}{}
		session.MsgCount = 0
		session.TurnRequestCount = 0
		ensureChatSystemPromptMessage(session)
		if err := syncRuntimeSessionFromChat(session); err != nil {
			fmt.Printf("错误: %v\n", err)
			return false
		}
		fmt.Println("当前会话历史已清空")

	case "/new":
		if err := createNewRuntimeConversation(session, ""); err != nil {
			fmt.Printf("错误: %v\n", err)
			return false
		}
		fmt.Println("已创建新会话")
		printCurrentRuntimeSession(session)

	case "/stream", "/s":
		session.Stream = true
		warnIfChatSessionSyncFails(session, "toggle stream", syncRuntimeSessionFromChat(session))
		fmt.Println("提示: 已切换到流式模式")

	case "/normal", "/n":
		session.Stream = false
		warnIfChatSessionSyncFails(session, "toggle normal mode", syncRuntimeSessionFromChat(session))
		fmt.Println("提示: 已切换到普通模式")

	case "/history", "/h":
		if printVisibleChatHistory(session, "对话历史") == 0 {
			fmt.Println("当前会话暂无历史消息")
		}

	case "/functions", "/catalog":
		fmt.Println(formatFunctionCatalogSummary(session, false))

	case "/session":
		if session.RuntimeSession == nil {
			fmt.Println("当前没有持久化会话")
			return false
		}
		printCurrentRuntimeSession(session)

	case "/compact":
		return handleCompactCommand(session, command)

	case "/sessions":
		if err := printChatSessionSummaries(session.SessionManager, session.SessionUserID, currentRuntimeSessionID(session), session.SessionFilter); err != nil {
			fmt.Printf("错误: %v\n", err)
		}

	case "/resume":
		if err := resumeLatestRuntimeConversation(session); err != nil {
			fmt.Printf("错误: %v\n", err)
			return false
		}
		fmt.Println("已恢复最近会话")
		printCurrentRuntimeSession(session)
		if hasVisibleChatHistory(session) {
			fmt.Println()
			printVisibleChatHistory(session, "已加载历史会话")
		}

	case "/yolo":
		if session == nil {
			fmt.Println("错误: 当前没有活动会话")
			return false
		}
		session.PermissionMode = "bypass_permissions"
		if session.ActiveTeam != nil {
			session.ActiveTeam.PermissionMode = session.PermissionMode
		}
		warnIfChatSessionSyncFails(session, "toggle permission mode", syncRuntimeSessionFromChat(session))
		fmt.Println("提示: 已切换到 permission-mode=bypass_permissions（等价于 --yolo）")

	case "/image":
		handleImageCommand(session, "/image")

	case "/help", "/?":
		fmt.Println()
		fmt.Println("可用命令:")
		fmt.Println("  /exit, /quit, /q   - 退出聊天")
		fmt.Println("  /clear, /cls       - 清空当前会话历史")
		fmt.Println("  /new               - 创建新会话")
		fmt.Println("  /stream, /s        - 切换到流式模式")
		fmt.Println("  /normal, /n        - 切换到普通模式")
		fmt.Println("  /history, /h       - 显示对话历史")
		fmt.Println("  /session           - 显示当前会话信息")
		fmt.Println("  /compact [mode]    - 手动触发会话压缩（auto|local|remote）")
		fmt.Println("  /sessions          - 按当前筛选条件列出可恢复会话")
		fmt.Println("  /sessions <query>  - 按关键字筛选会话")
		fmt.Println("  /load <id>         - 加载指定会话")
		fmt.Println("  /resume            - 恢复最近会话")
		fmt.Println("  /title <text>      - 更新当前会话标题")
		fmt.Println("  /model [name]      - 查看或切换模型，并在支持时调整 thinking_effort")
		fmt.Println("  /image [path]      - 查看/添加图片附件（支持 PNG/JPEG/GIF/WebP）")
		fmt.Println("  /image clear       - 清空待发送图片附件")
		fmt.Println("  /queue             - 查看当前排队输入状态")
		fmt.Println("  /queue clear       - 清空当前排队输入")
		fmt.Println("  /permission-mode [mode] - 查看或切换权限模式（default|accept_edits|plan|bypass_permissions）")
		fmt.Println("  /mode [mode]       - /permission-mode 的别名")
		fmt.Println("  /approval-reuse [mode]  - 查看或切换审批复用策略（off|session_readonly_shell|team_readonly_shell）")
		fmt.Println("  /yolo              - 快速切换到 permission-mode=bypass_permissions")
		fmt.Println("  /functions, /catalog - 显示当前已加载的 builtin tools 和 skill functions")
		fmt.Println("  /functions --json  - 以 JSON 输出当前 catalog")
		fmt.Println("  /functions <prompt>  - 预览该 prompt 下最终暴露给模型的 functions")
		fmt.Println("  /functions <prompt> --json - 以 JSON 输出 exposure report")
		fmt.Println("  /function <name>   - 显示单个 function 的详情")
		fmt.Println("  /function <name> --json - 以 JSON 输出单个 function descriptor")
		fmt.Println("  /call <name> [args-json] - 直接执行指定 function（tool 或 skill function）")
		fmt.Println("  /tool <name> [args-json] - /call 的别名，便于直接执行 tool")
		fmt.Println("  /skill <name> <prompt> - 直接执行指定 skill，例如 /skill imagegen 帮我生成一张图片")
		fmt.Println("  /help, /?          - 显示此帮助")
		fmt.Println()
		fmt.Println("Shell 命令:")
		fmt.Println("  ![--output-bytes-cap <bytes> | --disable-output-cap] <命令>")
		fmt.Println("                    - 执行 shell 命令并分享输出给 AI")
		fmt.Println("  /shell [--output-bytes-cap <bytes> | --disable-output-cap] <命令>")
		fmt.Println("  /cmd   [--output-bytes-cap <bytes> | --disable-output-cap] <命令>")
		fmt.Println("                    - 执行 shell 命令并分享输出给 AI")
		fmt.Println("                      例如: !git status --short")
		fmt.Println("                            /shell --output-bytes-cap 1048576 git diff --stat")
		fmt.Println("                            /cmd --disable-output-cap git diff HEAD -- README.md")
		fmt.Println("                      安全保护: 超时 30s；终端实时输出完整显示，分享给 AI 的 capture 默认保留 256KB，可通过上述参数覆盖")
		fmt.Println()

	default:
		fmt.Printf("错误: 未知命令: %s\n", cmd)
		fmt.Println("输入 /help 查看可用命令")
	}

	return false
}

func handlePermissionModeCommand(session *ChatSession, command string) bool {
	if session == nil {
		fmt.Println("错误: 当前没有活动会话")
		return false
	}
	value := extractCommandArgument(command)
	if strings.TrimSpace(value) == "" {
		fmt.Printf("当前 permission-mode: %s\n", session.PermissionMode)
		return false
	}
	mode, err := parseChatPermissionMode(value, false)
	if err != nil {
		fmt.Printf("错误: %v\n", err)
		return false
	}
	session.PermissionMode = mode
	if session.ActiveTeam != nil {
		session.ActiveTeam.PermissionMode = mode
	}
	warnIfChatSessionSyncFails(session, "toggle permission mode", syncRuntimeSessionFromChat(session))
	fmt.Printf("提示: 已切换到 permission-mode=%s\n", mode)
	return false
}

func handleImageCommand(session *ChatSession, command string) bool {
	if session == nil {
		fmt.Println("错误: 当前没有活动会话")
		return false
	}
	arg := strings.TrimSpace(extractCommandArgument(command))
	if arg == "" {
		// /image with no argument: list current attachments
		if len(session.ImagePaths) == 0 {
			fmt.Println("当前无待发送图片附件")
		} else {
			fmt.Printf("待发送图片附件 (%d):\n", len(session.ImagePaths))
			for i, path := range session.ImagePaths {
				fmt.Printf("  [%d] %s\n", i+1, path)
			}
		}
		return false
	}
	if arg == "clear" {
		count := len(session.ImagePaths)
		session.ImagePaths = nil
		fmt.Printf("已清空 %d 个待发送图片附件\n", count)
		return false
	}
	// /image <path>: add image path
	path := arg
	session.ImagePaths = append(session.ImagePaths, path)
	fmt.Printf("已添加图片附件: %s (当前共 %d 个)\n", path, len(session.ImagePaths))
	return false
}

func handleQueueCommand(session *ChatSession, command string) bool {
	if session == nil {
		fmt.Println("错误: 当前没有活动会话")
		return false
	}
	arg := strings.ToLower(strings.TrimSpace(extractCommandArgument(command)))
	switch arg {
	case "", "status":
		count, draining := queuedInteractiveInputState(session)
		state := fmt.Sprintf("%d pending", count)
		if draining {
			state += " (draining)"
		}
		fmt.Printf("当前 queued input: %s\n", state)
		return false
	case "clear":
		discarded := discardPendingInteractiveInput(session)
		fmt.Printf("已清空 queued input: %d\n", discarded)
		return false
	default:
		fmt.Println("错误: /queue 仅支持空参数或 clear")
		fmt.Println("用法: /queue 或 /queue clear")
		return false
	}
}

func handleCompactCommand(session *ChatSession, command string) bool {
	if session == nil {
		fmt.Println("错误: 当前没有活动会话")
		return false
	}
	mode, err := normalizeChatCompactMode(extractCommandArgument(command))
	if err != nil {
		fmt.Printf("错误: %v\n", err)
		return false
	}

	report, err := runManualChatCompact(session, mode)
	if report != nil {
		fmt.Println(formatChatCompactReport(report))
	}
	if err != nil {
		fmt.Printf("错误: %v\n", err)
	}
	return false
}

func handleApprovalReuseCommand(session *ChatSession, command string) bool {
	if session == nil {
		fmt.Println("错误: 当前没有活动会话")
		return false
	}
	value := extractCommandArgument(command)
	if strings.TrimSpace(value) == "" {
		fmt.Printf("当前 approval-reuse: %s\n", formatChatApprovalReuseMode(session.ApprovalReuseMode))
		return false
	}
	mode, err := parseChatApprovalReuseMode(value)
	if err != nil {
		fmt.Printf("错误: %v\n", err)
		return false
	}
	session.ApprovalReuseMode = mode
	warnIfChatSessionSyncFails(session, "toggle approval reuse", syncRuntimeSessionFromChat(session))
	fmt.Printf("提示: 已切换到 approval-reuse=%s\n", formatChatApprovalReuseMode(mode))
	return false
}

func commandMatches(commandLower, name string) bool {
	commandLower = strings.TrimSpace(strings.ToLower(commandLower))
	name = strings.TrimSpace(strings.ToLower(name))
	if commandLower == name {
		return true
	}
	if !strings.HasPrefix(commandLower, name) || len(commandLower) <= len(name) {
		return false
	}
	switch commandLower[len(name)] {
	case ' ', '\t':
		return true
	default:
		return false
	}
}

func extractCommandArgument(command string) string {
	command = strings.TrimSpace(command)
	if command == "" {
		return ""
	}
	if idx := strings.IndexAny(command, " \t"); idx >= 0 {
		return strings.TrimSpace(command[idx+1:])
	}
	return ""
}

func extractCommandArgumentOptions(command string) (string, bool) {
	argument := extractCommandArgument(command)
	return stripJSONOption(argument)
}

func stripJSONOption(argument string) (string, bool) {
	argument = strings.TrimSpace(argument)
	if argument == "" {
		return "", false
	}
	if argument == "--json" {
		return "", true
	}
	if strings.HasPrefix(argument, "--json ") {
		return strings.TrimSpace(argument[len("--json "):]), true
	}
	if strings.HasSuffix(argument, " --json") {
		return strings.TrimSpace(argument[:len(argument)-len(" --json")]), true
	}
	return argument, false
}

type aicliFunctionDescriptorReport struct {
	FunctionName string                 `json:"function_name"`
	Descriptor   *capability.Descriptor `json:"descriptor,omitempty"`
}

type aicliFunctionCatalogReport struct {
	Stats   aicliFunctionCatalogStats       `json:"stats"`
	Builtin []aicliFunctionDescriptorReport `json:"builtin,omitempty"`
	Skills  []aicliFunctionDescriptorReport `json:"skills,omitempty"`
}

func formatFunctionCatalogSummary(session *ChatSession, jsonOutput bool) string {
	catalog := ensureFunctionCatalog(session)
	if catalog == nil {
		return formatCommandError("Function Catalog: 未初始化", jsonOutput)
	}

	report := buildFunctionCatalogReport(catalog)
	if jsonOutput {
		return marshalIndentedJSON(report)
	}

	stats := report.Stats
	descriptors := append([]aicliFunctionDescriptorReport(nil), report.Builtin...)
	descriptors = append(descriptors, report.Skills...)
	lines := []string{
		fmt.Sprintf("Function Catalog: total=%d builtin=%d skills=%d", stats.TotalFunctions, stats.BuiltinTools, stats.SkillFunctions),
	}
	if len(descriptors) == 0 {
		lines = append(lines, "Functions: <none>")
		return strings.Join(lines, "\n")
	}

	builtinLines := make([]string, 0)
	skillLines := make([]string, 0)
	for _, item := range descriptors {
		descriptor := item.Descriptor
		if descriptor == nil {
			continue
		}
		line := fmt.Sprintf("  - %s [%s]", item.FunctionName, descriptor.Kind)
		if source, _ := descriptor.Metadata["source"].(string); source != "" {
			line += " source=" + source
		}
		if skillPath, _ := descriptor.Metadata["skill_path"].(string); skillPath != "" {
			line += " path=" + skillPath
		}
		if descriptor.Category != "" {
			line += " category=" + descriptor.Category
		}
		if descriptor.Description != "" {
			line += " :: " + descriptor.Description
		}
		if descriptor.Kind == "skill" || strings.HasPrefix(descriptor.Name, skillFunctionPrefix) {
			skillLines = append(skillLines, line)
		} else {
			builtinLines = append(builtinLines, line)
		}
	}
	sort.Strings(builtinLines)
	sort.Strings(skillLines)

	lines = append(lines, "Builtin Tools:")
	if len(builtinLines) == 0 {
		lines = append(lines, "  <none>")
	} else {
		lines = append(lines, builtinLines...)
	}

	lines = append(lines, "Skill Functions:")
	if len(skillLines) == 0 {
		lines = append(lines, "  <none>")
	} else {
		lines = append(lines, skillLines...)
	}

	return strings.Join(lines, "\n")
}

func buildFunctionCatalogReport(catalog *aicliFunctionCatalog) *aicliFunctionCatalogReport {
	if catalog == nil {
		return nil
	}

	report := &aicliFunctionCatalogReport{
		Stats: catalog.Stats(),
	}
	for _, descriptor := range catalog.Descriptors() {
		if descriptor == nil {
			continue
		}
		item := aicliFunctionDescriptorReport{
			FunctionName: descriptorDisplayName(descriptor),
			Descriptor:   descriptor,
		}
		if descriptor.Kind == "skill" || strings.HasPrefix(item.FunctionName, skillFunctionPrefix) {
			report.Skills = append(report.Skills, item)
			continue
		}
		report.Builtin = append(report.Builtin, item)
	}
	return report
}

func formatFunctionExposurePreview(session *ChatSession, prompt string, jsonOutput bool) string {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return formatCommandError("错误: prompt 不能为空", jsonOutput)
	}

	catalog := ensureFunctionCatalog(session)
	if catalog == nil {
		return formatCommandError("Function Catalog: 未初始化", jsonOutput)
	}

	report := buildFunctionExposureReportForPrompt(session, prompt)
	if report == nil {
		return formatCommandError("Function Exposure Preview: 无可用 functions", jsonOutput)
	}
	if jsonOutput {
		return marshalIndentedJSON(report)
	}

	lines := []string{
		"Function Exposure Preview:",
		"Prompt: " + report.Prompt,
		fmt.Sprintf("Mode: %s", report.Mode),
		fmt.Sprintf("Include Builtin: %t", report.IncludeBuiltin),
		"Builtin Exposed: " + formatPreviewList(report.BuiltinFunctions),
		"Skill Exposed: " + formatPreviewList(report.SkillFunctions),
		"Final Functions: " + formatPreviewList(report.FinalFunctionNames),
	}
	if len(report.ExplicitMentions) > 0 {
		lines = append(lines, "Explicit Mentions: "+strings.Join(report.ExplicitMentions, ", "))
	}
	if len(report.PreviouslyCalled) > 0 {
		lines = append(lines, "Previously Called: "+strings.Join(report.PreviouslyCalled, ", "))
	}
	lines = append(lines, "Routed Skills: "+formatPreviewList(report.RoutedSkills))
	if len(report.Candidates) > 0 {
		candidateParts := make([]string, 0, len(report.Candidates))
		for _, candidate := range report.Candidates {
			if candidate.FunctionName == "" {
				continue
			}
			part := fmt.Sprintf("%s(score=%.3f matched_by=%s)", candidate.FunctionName, candidate.Score, candidate.MatchedBy)
			candidateParts = append(candidateParts, part)
		}
		lines = append(lines, "Candidates: "+formatPreviewList(candidateParts))
	}

	return strings.Join(lines, "\n")
}

func formatFunctionDescriptor(session *ChatSession, name string, jsonOutput bool) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return formatCommandError("错误: function 名称不能为空", jsonOutput)
	}

	catalog := ensureFunctionCatalog(session)
	if catalog == nil {
		return formatCommandError("Function Catalog: 未初始化", jsonOutput)
	}

	resolvedName, _, err := resolveDirectCallableFunctionName(session, name, false)
	if err != nil {
		return formatCommandError("错误: "+err.Error(), jsonOutput)
	}

	descriptor := catalog.Descriptor(resolvedName)
	if descriptor == nil {
		return formatCommandError(fmt.Sprintf("错误: 未找到 function: %s", name), jsonOutput)
	}
	if jsonOutput {
		return marshalIndentedJSON(aicliFunctionDescriptorReport{
			FunctionName: descriptorDisplayName(descriptor),
			Descriptor:   descriptor,
		})
	}

	lines := []string{
		fmt.Sprintf("Function: %s", descriptorDisplayName(descriptor)),
		fmt.Sprintf("Kind: %s", descriptor.Kind),
	}
	if callableName, _ := descriptor.Metadata["function_name"].(string); callableName != "" && callableName != descriptor.Name {
		lines = append(lines, "Capability: "+descriptor.Name)
	}
	if descriptor.Description != "" {
		lines = append(lines, "Description: "+descriptor.Description)
	}
	if descriptor.Category != "" {
		lines = append(lines, "Category: "+descriptor.Category)
	}
	if len(descriptor.Capabilities) > 0 {
		lines = append(lines, "Capabilities: "+strings.Join(descriptor.Capabilities, ", "))
	}
	if len(descriptor.Labels) > 0 {
		lines = append(lines, "Labels: "+strings.Join(descriptor.Labels, ", "))
	}
	if len(descriptor.Triggers) > 0 {
		triggerParts := make([]string, 0, len(descriptor.Triggers))
		for _, trigger := range descriptor.Triggers {
			part := trigger.Type
			if len(trigger.Values) > 0 {
				part += ":" + strings.Join(trigger.Values, "|")
			}
			triggerParts = append(triggerParts, part)
		}
		lines = append(lines, "Triggers: "+strings.Join(triggerParts, ", "))
	}
	if descriptor.Source != nil {
		sourceParts := make([]string, 0, 3)
		if descriptor.Source.Layer != "" {
			sourceParts = append(sourceParts, "layer="+descriptor.Source.Layer)
		}
		if descriptor.Source.Dir != "" {
			sourceParts = append(sourceParts, "dir="+descriptor.Source.Dir)
		}
		if descriptor.Source.Path != "" {
			sourceParts = append(sourceParts, "path="+descriptor.Source.Path)
		}
		if len(sourceParts) > 0 {
			lines = append(lines, "Source: "+strings.Join(sourceParts, " "))
		}
	}
	if len(descriptor.Metadata) > 0 {
		metaKeys := make([]string, 0, len(descriptor.Metadata))
		for key := range descriptor.Metadata {
			metaKeys = append(metaKeys, key)
		}
		sort.Strings(metaKeys)
		metaParts := make([]string, 0, len(metaKeys))
		for _, key := range metaKeys {
			metaParts = append(metaParts, fmt.Sprintf("%s=%v", key, descriptor.Metadata[key]))
		}
		lines = append(lines, "Metadata: "+strings.Join(metaParts, ", "))
	}

	return strings.Join(lines, "\n")
}

func formatCommandError(message string, jsonOutput bool) string {
	if !jsonOutput {
		return message
	}
	return marshalIndentedJSON(map[string]string{
		"error": message,
	})
}

func marshalIndentedJSON(value interface{}) string {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Sprintf("{\"error\": %q}", err.Error())
	}
	return string(data)
}

func descriptorDisplayName(descriptor *capability.Descriptor) string {
	if descriptor == nil {
		return ""
	}
	if descriptor.Metadata != nil {
		if callableName, _ := descriptor.Metadata["function_name"].(string); callableName != "" {
			return callableName
		}
	}
	return descriptor.Name
}

func formatPreviewList(values []string) string {
	if len(values) == 0 {
		return "<none>"
	}
	return strings.Join(values, ", ")
}

// isDangerousCommand 检查命令是否危险
func isDangerousCommand(cmd string) bool {
	// 常见危险命令模式
	dangerousPatterns := []string{
		"rm -rf", "rm -r", "rm -f",
		"del /f", "del /s", "del /q",
		"format ",
		"shutdown ", "halt ", "reboot ",
		":(){ :|:& };:", // fork bomb
		"cat /dev/zero", "dd if=/dev/zero",
		"mkfs.",   // 文件系统格式化
		"> /dev/", // 直接写入设备
	}

	cmdLower := strings.ToLower(cmd)
	for _, pattern := range dangerousPatterns {
		if strings.Contains(cmdLower, pattern) {
			return true
		}
	}
	return false
}

// prefixPowershellUTF8ForInteractiveCommand keeps Windows shell output UTF-8 encoded.
func prefixPowershellUTF8ForInteractiveCommand(cmd *exec.Cmd) {
	if len(cmd.Args) < 3 {
		return
	}
	lastIdx := len(cmd.Args) - 1
	cmd.Args[lastIdx] = "[Console]::OutputEncoding=[System.Text.Encoding]::UTF8; " + cmd.Args[lastIdx]
}

type shellCommandResult struct {
	ExecutedCommand       string
	Output                string
	Capture               runtimeexecutor.CombinedOutputCapture
	Config                ShellCommandConfig
	RawOutputArtifactPath string
	Shell                 runtimeexecutor.Shell
}

func defaultShellCommandConfig() ShellCommandConfig {
	return ShellCommandConfig{
		Timeout:        DefaultShellTimeout,
		MaxLines:       DefaultShellMaxLines,
		MaxOutputSize:  DefaultShellMaxOutputSize,
		OutputBytesCap: runtimeexecutor.DefaultRetainedOutputBytes,
	}
}

func shellCommandCaptureLimit(cfg ShellCommandConfig) int {
	if cfg.DisableOutputCap {
		return runtimeexecutor.DisableRetainedOutputLimit
	}
	if cfg.OutputBytesCap > 0 {
		return cfg.OutputBytesCap
	}
	if cfg.MaxOutputSize > 0 {
		return cfg.MaxOutputSize
	}
	return runtimeexecutor.DefaultRetainedOutputBytes
}

func parseShellCommandInvocation(raw string) (string, ShellCommandConfig, error) {
	cfg := defaultShellCommandConfig()
	explicitOutputCap := false
	command := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(raw), "!"))
	for {
		command = strings.TrimSpace(command)
		switch {
		case strings.HasPrefix(command, "--disable-output-cap"):
			if len(command) > len("--disable-output-cap") && !isShellCommandOptionSeparator(command[len("--disable-output-cap")]) {
				return "", cfg, fmt.Errorf("无法解析 shell 选项: %s", command)
			}
			cfg.DisableOutputCap = true
			command = strings.TrimSpace(command[len("--disable-output-cap"):])
		case strings.HasPrefix(command, "--output-bytes-cap="):
			token, remaining := splitLeadingShellCommandToken(command)
			value := strings.TrimSpace(token[len("--output-bytes-cap="):])
			if value == "" {
				return "", cfg, fmt.Errorf("output-bytes-cap 需要正整数值")
			}
			parsed, err := strconv.Atoi(value)
			if err != nil || parsed <= 0 {
				return "", cfg, fmt.Errorf("output-bytes-cap 需要正整数值，收到 %q", value)
			}
			cfg.OutputBytesCap = parsed
			explicitOutputCap = true
			command = remaining
		case strings.HasPrefix(command, "--output-bytes-cap"):
			if len(command) > len("--output-bytes-cap") && !isShellCommandOptionSeparator(command[len("--output-bytes-cap")]) {
				return "", cfg, fmt.Errorf("无法解析 shell 选项: %s", command)
			}
			valueToken, remaining, err := consumeRequiredShellCommandValue(command[len("--output-bytes-cap"):])
			if err != nil {
				return "", cfg, err
			}
			parsed, parseErr := strconv.Atoi(valueToken)
			if parseErr != nil || parsed <= 0 {
				return "", cfg, fmt.Errorf("output-bytes-cap 需要正整数值，收到 %q", valueToken)
			}
			cfg.OutputBytesCap = parsed
			explicitOutputCap = true
			command = remaining
		default:
			if cfg.DisableOutputCap && explicitOutputCap {
				return "", cfg, fmt.Errorf("output-bytes-cap 不能与 disable-output-cap 同时设置")
			}
			if strings.TrimSpace(command) == "" {
				return "", cfg, fmt.Errorf("需要在 shell 选项后提供要执行的命令")
			}
			return strings.TrimSpace(command), cfg, nil
		}
	}
}

func isShellCommandOptionSeparator(ch byte) bool {
	return ch == ' ' || ch == '\t'
}

func splitLeadingShellCommandToken(command string) (string, string) {
	command = strings.TrimSpace(command)
	if command == "" {
		return "", ""
	}
	if idx := strings.IndexAny(command, " \t"); idx >= 0 {
		return command[:idx], strings.TrimSpace(command[idx+1:])
	}
	return command, ""
}

func consumeRequiredShellCommandValue(rest string) (string, string, error) {
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return "", "", fmt.Errorf("output-bytes-cap 需要正整数值")
	}
	value, remaining := splitLeadingShellCommandToken(rest)
	if value == "" {
		return "", "", fmt.Errorf("output-bytes-cap 需要正整数值")
	}
	return value, remaining, nil
}

func buildShellCommandAIInput(result shellCommandResult) string {
	lines := []string{
		fmt.Sprintf("我执行了命令: %s", result.ExecutedCommand),
	}
	if metadata := result.Shell.Metadata(); len(metadata) > 0 {
		lines = append(lines, fmt.Sprintf("实际执行 Shell: %s", result.Shell.String()))
	}
	lines = append(lines,
		fmt.Sprintf("命令输出捕获状态: %s", shellCommandCaptureStatusLine(result)),
	)
	if result.Capture.Truncated && strings.TrimSpace(result.RawOutputArtifactPath) != "" {
		lines = append(lines, fmt.Sprintf("完整原始输出已旁路保存: %s", result.RawOutputArtifactPath))
	}
	lines = append(lines, "输出如下：", result.Output)
	return strings.Join(lines, "\n")
}

func shellCommandCaptureStatusLine(result shellCommandResult) string {
	retainedBytes := result.Capture.RetainedBytes
	if retainedBytes == 0 && result.Output != "" {
		retainedBytes = len(result.Output)
	}
	totalBytes := result.Capture.TotalBytes
	if totalBytes == 0 && result.Output != "" {
		totalBytes = len(result.Output)
	}
	limitValue := "disabled"
	if !result.Capture.CaptureLimitDisabled {
		limitBytes := result.Capture.CaptureLimitBytes
		if limitBytes <= 0 {
			limitBytes = shellCommandCaptureLimit(result.Config)
		}
		limitValue = fmt.Sprintf("%dB", limitBytes)
	}
	parts := []string{
		fmt.Sprintf("complete=%t", !result.Capture.Truncated),
		fmt.Sprintf("limit=%s", limitValue),
		fmt.Sprintf("retained=%dB", retainedBytes),
		fmt.Sprintf("total=%dB", totalBytes),
	}
	if result.Capture.OmittedBytes > 0 {
		parts = append(parts, fmt.Sprintf("omitted=%dB", result.Capture.OmittedBytes))
	}
	return strings.Join(parts, "; ")
}

func logLocalShellCommandDebug(session *ChatSession, result shellCommandResult, err error) {
	if session == nil {
		return
	}
	writeSessionDebugInfo(session, shellCommandDebugLine(result, err), false)
}

func shellCommandDebugLine(result shellCommandResult, err error) string {
	status := shellCommandCaptureStatusLine(result)
	shellSuffix := ""
	if metadata := result.Shell.Metadata(); len(metadata) > 0 {
		shellType, _ := metadata["shell_type"].(string)
		shellPath, _ := metadata["shell_path"].(string)
		shellSuffix = fmt.Sprintf(" shell_type=%q shell_path=%q", shellType, shellPath)
	}
	artifactSuffix := ""
	if strings.TrimSpace(result.RawOutputArtifactPath) != "" {
		artifactSuffix = fmt.Sprintf(" artifact=%q", result.RawOutputArtifactPath)
	}
	if err != nil {
		return fmt.Sprintf("[shell-debug] local command=%q%s success=false capture={%s}%s error=%q", result.ExecutedCommand, shellSuffix, status, artifactSuffix, err.Error())
	}
	return fmt.Sprintf("[shell-debug] local command=%q%s success=true capture={%s}%s", result.ExecutedCommand, shellSuffix, status, artifactSuffix)
}

func executeShellCommand(session *ChatSession, cmdStr string) (string, error) {
	result, err := executeShellCommandDetailed(session, cmdStr)
	if err != nil {
		return "", err
	}
	return result.Output, nil
}

// executeShellCommand 执行 shell 命令
func executeShellCommandDetailed(session *ChatSession, cmdStr string) (shellCommandResult, error) {
	executedCommand, cfg, err := parseShellCommandInvocation(cmdStr)
	if err != nil {
		return shellCommandResult{ExecutedCommand: executedCommand, Config: cfg}, err
	}

	result := shellCommandResult{
		ExecutedCommand: executedCommand,
		Config:          cfg,
	}

	// 检查危险命令
	if isDangerousCommand(executedCommand) {
		fmt.Printf("\n警告: 检测到可能危险的命令: %s\n", executedCommand)
		fmt.Print("确认执行? (yes/no): ")

		var confirm string
		_, err := fmt.Scanln(&confirm)
		if err != nil || strings.ToLower(confirm) != "yes" {
			return result, fmt.Errorf("命令已取消")
		}
	}

	artifactWriter, artifactErr := openLocalShellArtifactWriter(session, executedCommand)
	if artifactErr != nil {
		writeSessionDebugInfo(session, fmt.Sprintf("[shell-debug] local shell artifact create failed command=%q error=%q", executedCommand, artifactErr.Error()), false)
	} else if artifactWriter != nil {
		result.RawOutputArtifactPath = artifactWriter.Path()
		artifactFile := artifactWriter
		defer func() {
			if closeErr := artifactFile.Close(); closeErr != nil {
				writeSessionDebugInfo(session, fmt.Sprintf("[shell-debug] local shell artifact close failed path=%q error=%q", artifactFile.Path(), closeErr.Error()), false)
			}
		}()
	}

	// Use the same detected user shell as tool-based command execution.
	shell := runtimeexecutor.DefaultUserShell()
	result.Shell = shell
	shellCmd := shell.DeriveExecArgs(executedCommand, false)

	fmt.Printf("\n执行命令: %s\n", executedCommand)
	fmt.Println("--- 输出 ---")

	// 创建带超时和中断的 context
	ctx, cancel := context.WithTimeout(session.cancelCtx, cfg.Timeout)
	defer cancel()

	// 执行命令
	cmd := exec.CommandContext(ctx, shellCmd[0], shellCmd[1:]...)
	if shell.Type == runtimeexecutor.ShellTypePowerShell || shell.Type == runtimeexecutor.ShellTypePwsh {
		prefixPowershellUTF8ForInteractiveCommand(cmd)
	}

	// 启动 Goroutine 实时输出命令结果（支持长时间运行的命令）
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return result, fmt.Errorf("创建标准输出管道失败: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return result, fmt.Errorf("创建标准错误管道失败: %w", err)
	}

	// 启动命令
	if err := cmd.Start(); err != nil {
		return result, fmt.Errorf("启动命令失败: %w", err)
	}

	// 实时读取输出
	outputChan := make(chan []byte, 100)
	doneChan := make(chan struct{})
	captureAccumulator := runtimeexecutor.NewOutputCaptureAccumulator(shellCommandCaptureLimit(cfg))

	go func() {
		defer close(doneChan)
		defer close(outputChan) // 确保关闭输出通道
		buf := make([]byte, 1024)

		// 合并 stdout 和 stderr
		reader := io.MultiReader(stdoutPipe, stderrPipe)
		for {
			n, err := reader.Read(buf)
			if n > 0 {
				chunk := append([]byte(nil), buf[:n]...)
				outputChan <- chunk
			}
			if err != nil {
				break
			}
		}
	}()

	// 实时打印输出，同时按配置保留传递给 AI 的输出窗口
	for {
		select {
		case chunk, ok := <-outputChan:
			if !ok {
				// 输出通道关闭，等待 goroutine 完成
				<-doneChan
				fmt.Println()
				goto commandDone
			}
			fmt.Print(string(chunk))
			_, _ = captureAccumulator.Write(chunk)
			if artifactWriter != nil {
				if err := artifactWriter.WriteChunk(chunk); err != nil {
					writeSessionDebugInfo(session, fmt.Sprintf("[shell-debug] local shell artifact write failed path=%q error=%q", artifactWriter.Path(), err.Error()), false)
					_ = artifactWriter.Abort()
					artifactWriter = nil
					result.RawOutputArtifactPath = ""
					recordLocalShellArtifactPath(session, "")
				}
			}

		case <-ctx.Done():
			// 检查是否是用户中断
			if session.IsInterrupted() {
				cmd.Process.Kill()
				<-doneChan
				capture := captureAccumulator.Result()
				result.Capture = capture
				result.Output = capture.Output
				logLocalShellCommandDebug(session, result, fmt.Errorf("用户中断"))
				fmt.Println("\n[已中断] 命令执行已停止")
				return result, fmt.Errorf("用户中断")
			}
			// 超时
			cmd.Process.Kill()
			<-doneChan
			capture := captureAccumulator.Result()
			result.Capture = capture
			result.Output = capture.Output
			logLocalShellCommandDebug(session, result, fmt.Errorf("命令执行超时（超过 %v）", cfg.Timeout))
			fmt.Println("\n[超时] 命令执行超时")
			return result, fmt.Errorf("命令执行超时（超过 %v）", cfg.Timeout)
		}
	}

commandDone: // 等待命令完成
	// 检查命令执行状态
	outputCapture := captureAccumulator.Result()
	outputStr := outputCapture.Output
	result.Capture = outputCapture
	result.Output = outputStr

	if err := cmd.Wait(); err != nil {
		// 命令执行失败（返回非零状态码）
		// 针对常见错误给出友好提示
		cmdLower := strings.ToLower(cmdStr)
		cmdParts := strings.Fields(cmdStr)

		var friendlyHint string
		exitCode := -1
		if exitError, ok := err.(*exec.ExitError); ok {
			exitCode = exitError.ExitCode()
		}

		// 获取主命令名
		mainCmd := ""
		if len(cmdParts) > 0 {
			mainCmd = strings.ToLower(cmdParts[0])
		}

		switch {
		case cmdLower == "pwd" && runtime.GOOS == "windows":
			// 在 Windows 上执行 pwd（这是 Unix 命令）
			if runtimeexecutor.DefaultUserShell().Type == runtimeexecutor.ShellTypeCmd {
				friendlyHint = "提示: cmd.exe 下请使用 `cd` 或 `echo %cd%` 查看当前目录；PowerShell/pwsh 下请使用 `pwd` 或 `Get-Location`。"
			}

		case mainCmd == "ls" && runtime.GOOS == "windows":
			// 在 Windows 上执行 ls（这是 Unix 命令）
			friendlyHint = "提示: Windows 下请使用 `dir` 查看目录内容"

		case exitCode == 127:
			// Unix: exit code 127 通常表示命令未找到
			friendlyHint = "提示: 命令未找到，请检查命令拼写或确认命令是否已安装"

		case runtime.GOOS == "windows" && exitCode == 1 && len(cmdParts) > 0:
			// Windows: 检查是否是常见命令未找到
			// Windows 内置命令: dir, cd, echo, type, del, copy, move, md, rd, cls, ver 等
			windowsCommands := map[string]bool{
				"dir": true, "cd": true, "echo": true, "type": true,
				"del": true, "copy": true, "move": true, "rename": true,
				"md": true, "mkdir": true, "rd": true, "rmdir": true,
				"cls": true, "ver": true, "vol": true, "date": true,
				"time": true, "set": true, "path": true, "chdir": true,
				"help": true, "exit": true, "pause": true, "ipconfig": true,
				"ping": true, "tracert": true, "netstat": true, "tasklist": true,
				"taskkill": true, "format": true, "shutdown": true,
			}

			if !windowsCommands[mainCmd] {
				// 不是 Windows 内置命令，可能是未安装的外部命令
				friendlyHint = "提示: 命令未找到，请检查命令拼写或确认命令是否已安装"
			}

		case strings.Contains(strings.ToLower(outputStr), "permission") ||
			strings.Contains(strings.ToLower(outputStr), "access") && strings.Contains(strings.ToLower(outputStr), "denied"):
			friendlyHint = "提示: 权限不足，请检查是否有执行该命令的权限"

		case strings.Contains(outputStr, "no such file or directory") ||
			strings.Contains(outputStr, "cannot find the path"):
			friendlyHint = "提示: 文件或目录不存在"
		}

		if friendlyHint != "" {
			logLocalShellCommandDebug(session, result, fmt.Errorf("命令执行失败: %w\n%s", err, friendlyHint))
			return result, fmt.Errorf("命令执行失败: %w\n%s", err, friendlyHint)
		}
		logLocalShellCommandDebug(session, result, fmt.Errorf("命令执行失败: %w", err))
		return result, fmt.Errorf("命令执行失败: %w", err)
	}

	// 命令执行成功
	if outputStr == "" {
		logLocalShellCommandDebug(session, result, fmt.Errorf("命令执行成功，但没有输出"))
		return result, fmt.Errorf("命令执行成功，但没有输出")
	}

	if outputCapture.Truncated {
		fmt.Printf("[提示] 命令输出较大，传递给 AI 的内容已按 capture limit 截断：total=%dB retained=%dB omitted=%dB limit=%dB\n",
			outputCapture.TotalBytes, outputCapture.RetainedBytes, outputCapture.OmittedBytes, outputCapture.CaptureLimitBytes)
		if strings.TrimSpace(result.RawOutputArtifactPath) != "" {
			fmt.Printf("[提示] 完整原始输出已保存到: %s\n", resolveAbsoluteChatPath(result.RawOutputArtifactPath))
		}
	}

	fmt.Println("--- 完成 ---")
	fmt.Println()

	logLocalShellCommandDebug(session, result, nil)
	return result, nil
}
