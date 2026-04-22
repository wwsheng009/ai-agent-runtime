package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"sort"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/capability"
)

// handleCommand 处理命令
func handleCommand(session *ChatSession, command string, noInteractive bool) bool {
	// 首先检查 /shell 和 /cmd 命令（带参数）
	cmdLower := strings.ToLower(strings.TrimSpace(command))
	if strings.HasPrefix(cmdLower, "/shell") || strings.HasPrefix(cmdLower, "/cmd") {
		// 提取命令名称和参数
		parts := strings.Fields(command)
		if len(parts) < 2 {
			fmt.Println("错误: 需要指定 shell 命令")
			fmt.Println("用法: /shell <命令> 或 /cmd <命令>")
			return false
		}
		// 组合命令参数
		shellCmd := strings.Join(parts[1:], " ")
		output, err := executeShellCommand(session, shellCmd)
		if err != nil {
			fmt.Printf("错误: %v\n", err)
			return false
		}
		// 将命令输出作为消息发送给 AI
		aiInput := fmt.Sprintf("我执行了命令: %s，输出如下：\n%s", shellCmd, output)
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
	if strings.HasPrefix(cmdLower, "/queue") {
		return handleQueueCommand(session, command)
	}
	if strings.HasPrefix(cmdLower, "/permission-mode") || strings.HasPrefix(cmdLower, "/mode") {
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
		fmt.Println("  /sessions          - 按当前筛选条件列出可恢复会话")
		fmt.Println("  /sessions <query>  - 按关键字筛选会话")
		fmt.Println("  /load <id>         - 加载指定会话")
		fmt.Println("  /resume            - 恢复最近会话")
		fmt.Println("  /title <text>      - 更新当前会话标题")
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
		fmt.Println("  /help, /?          - 显示此帮助")
		fmt.Println()
		fmt.Println("Shell 命令:")
		fmt.Println("  !<命令>            - 执行 shell 命令并分享输出给 AI")
		fmt.Println("  /shell <命令>, /cmd <命令>  - 执行 shell 命令并分享输出给 AI")
		fmt.Println("                      例如: !dir, /shell dir, /cmd cd")
		fmt.Println("                      安全保护: 超时 30s，最多 1000 行/100KB 输出")
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

	descriptor := catalog.Descriptor(name)
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

// executeShellCommand 执行 shell 命令
func executeShellCommand(session *ChatSession, cmdStr string) (string, error) {
	// 移除 ! 前缀
	cmdStr = strings.TrimPrefix(strings.TrimSpace(cmdStr), "!")
	if cmdStr == "" {
		return "", fmt.Errorf("命令为空")
	}

	cfg := ShellCommandConfig{
		Timeout:       DefaultShellTimeout,
		MaxLines:      DefaultShellMaxLines,
		MaxOutputSize: DefaultShellMaxOutputSize,
	}

	// 检查危险命令
	if isDangerousCommand(cmdStr) {
		fmt.Printf("\n警告: 检测到可能危险的命令: %s\n", cmdStr)
		fmt.Print("确认执行? (yes/no): ")

		var confirm string
		_, err := fmt.Scanln(&confirm)
		if err != nil || strings.ToLower(confirm) != "yes" {
			return "", fmt.Errorf("命令已取消")
		}
	}

	// 根据操作系统选择 shell
	var shellCmd []string
	if runtime.GOOS == "windows" {
		shellCmd = []string{"cmd", "/c", cmdStr}
	} else {
		shellCmd = []string{"sh", "-c", cmdStr}
	}

	fmt.Printf("\n执行命令: %s\n", cmdStr)
	fmt.Println("--- 输出 ---")

	// 创建带超时和中断的 context
	ctx, cancel := context.WithTimeout(session.cancelCtx, cfg.Timeout)
	defer cancel()

	// 执行命令
	cmd := exec.CommandContext(ctx, shellCmd[0], shellCmd[1:]...)

	// 启动 Goroutine 实时输出命令结果（支持长时间运行的命令）
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("创建标准输出管道失败: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return "", fmt.Errorf("创建标准错误管道失败: %w", err)
	}

	// 启动命令
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("启动命令失败: %w", err)
	}

	// 实时读取输出
	outputChan := make(chan string, 100)
	doneChan := make(chan struct{})

	go func() {
		defer close(doneChan)
		defer close(outputChan) // 确保关闭输出通道
		buf := make([]byte, 1024)

		// 合并 stdout 和 stderr
		reader := io.MultiReader(stdoutPipe, stderrPipe)
		for {
			n, err := reader.Read(buf)
			if n > 0 {
				chunk := string(buf[:n])
				outputChan <- chunk
			}
			if err != nil {
				break
			}
		}
	}()

	// 实时打印输出
	totalOutput := &strings.Builder{}
	for {
		select {
		case chunk, ok := <-outputChan:
			if !ok {
				// 输出通道关闭，等待 goroutine 完成
				<-doneChan
				fmt.Println()
				goto commandDone
			}
			fmt.Print(chunk)
			totalOutput.WriteString(chunk)

		case <-ctx.Done():
			// 检查是否是用户中断
			if session.IsInterrupted() {
				cmd.Process.Kill()
				<-doneChan
				fmt.Println("\n[已中断] 命令执行已停止")
				return totalOutput.String(), fmt.Errorf("用户中断")
			}
			// 超时
			cmd.Process.Kill()
			<-doneChan
			fmt.Println("\n[超时] 命令执行超时")
			return totalOutput.String(), fmt.Errorf("命令执行超时（超过 %v）", cfg.Timeout)
		}
	}

commandDone: // 等待命令完成
	// 检查命令执行状态
	outputStr := totalOutput.String()

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
			friendlyHint = "提示: Windows 下请使用 `cd` 查看当前目录，或 `echo %cd%`"

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
			return "", fmt.Errorf("命令执行失败: %w\n%s", err, friendlyHint)
		}
		return "", fmt.Errorf("命令执行失败: %w", err)
	}

	// 命令执行成功
	if outputStr == "" {
		return "", fmt.Errorf("命令执行成功，但没有输出")
	}

	fmt.Println("--- 完成 ---")
	fmt.Println()

	return outputStr, nil
}
