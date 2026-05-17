package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/capability"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolnames"
)

type directFunctionInvokeReport struct {
	RequestedName string                 `json:"requested_name"`
	FunctionName  string                 `json:"function_name"`
	Output        string                 `json:"output,omitempty"`
	Metadata      map[string]interface{} `json:"metadata,omitempty"`
}

func handleDirectFunctionCommand(session *ChatSession, command string) bool {
	if session == nil {
		fmt.Println("错误: 当前没有活动会话")
		return false
	}

	payload, jsonOutput := extractCommandArgumentOptions(command)
	jsonOutput = jsonOutput || shouldUseSessionJSONCommandOutput(session)
	requestedName, rawArgs := splitCommandNameAndRemainder(payload)
	if requestedName == "" {
		fmt.Println(formatCommandError("错误: 需要指定 function 名称\n用法: /call <name> [args-json] 或 /tool <name> [args-json]", jsonOutput))
		return false
	}

	resolvedName, isSkill, err := resolveDirectCallableFunctionName(session, requestedName, false)
	if err != nil {
		fmt.Println(formatCommandError("错误: "+err.Error(), jsonOutput))
		return false
	}
	args, err := parseDirectFunctionArgs(rawArgs, isSkill, resolvedName)
	if err != nil {
		fmt.Println(formatCommandError("错误: "+err.Error(), jsonOutput))
		return false
	}

	report, err := executeDirectFunction(session, requestedName, resolvedName, args)
	if err != nil {
		fmt.Println(formatCommandError("错误: "+err.Error(), jsonOutput))
		return false
	}
	fmt.Println(formatDirectFunctionInvokeReport(report, jsonOutput))
	return false
}

func handleDirectSkillCommand(session *ChatSession, command string) bool {
	if session == nil {
		fmt.Println("错误: 当前没有活动会话")
		return false
	}

	payload, jsonOutput := extractCommandArgumentOptions(command)
	jsonOutput = jsonOutput || shouldUseSessionJSONCommandOutput(session)
	requestedName, rawPrompt := splitCommandNameAndRemainder(payload)
	if requestedName == "" {
		fmt.Println(formatCommandError("错误: 需要指定 skill 名称\n用法: /skill <name> <prompt> 或 /skill <name> {\"prompt\":\"...\"}", jsonOutput))
		return false
	}

	resolvedName, _, err := resolveDirectCallableFunctionName(session, requestedName, true)
	if err != nil {
		fmt.Println(formatCommandError("错误: "+err.Error(), jsonOutput))
		return false
	}
	args, err := parseDirectFunctionArgs(rawPrompt, true, resolvedName)
	if err != nil {
		fmt.Println(formatCommandError("错误: "+err.Error(), jsonOutput))
		return false
	}

	renderDirectSkillInvocationStarted(session, command, requestedName, resolvedName, args, jsonOutput)
	report, err := executeDirectFunction(session, requestedName, resolvedName, args)
	if err != nil {
		fmt.Println(formatCommandError("错误: "+err.Error(), jsonOutput))
		return false
	}
	fmt.Println(formatDirectFunctionInvokeReport(report, jsonOutput))
	return false
}

func shouldUseSessionJSONCommandOutput(session *ChatSession) bool {
	return session != nil && session.NoInteractive && session.JSONOutput
}

func splitCommandNameAndRemainder(payload string) (string, string) {
	payload = strings.TrimSpace(payload)
	if payload == "" {
		return "", ""
	}
	if idx := strings.IndexAny(payload, " \t"); idx >= 0 {
		return strings.TrimSpace(payload[:idx]), strings.TrimSpace(payload[idx+1:])
	}
	return payload, ""
}

func resolveDirectCallableFunctionName(session *ChatSession, requestedName string, preferSkill bool) (string, bool, error) {
	catalog := ensureFunctionCatalog(session)
	if catalog == nil || catalog.Registry() == nil {
		return "", false, fmt.Errorf("function catalog 未初始化")
	}

	normalized := strings.TrimSpace(requestedName)
	if normalized == "" {
		return "", false, fmt.Errorf("function 名称不能为空")
	}
	normalized = toolnames.CanonicalOpenAIImageGenerateToolName(normalized)

	if preferSkill {
		if resolvedName, isSkill, err := resolveSkillCallableReference(catalog, normalized); err != nil {
			return "", false, err
		} else if resolvedName != "" {
			return resolvedName, isSkill, nil
		}
	}

	if resolvedName, isSkill, ok := resolveExactCallableFunctionName(catalog, normalized); ok {
		return resolvedName, isSkill, nil
	}

	if !preferSkill {
		if resolvedName, isSkill, err := resolveSkillCallableReference(catalog, normalized); err != nil {
			return "", false, err
		} else if resolvedName != "" {
			return resolvedName, isSkill, nil
		}
	}

	if preferSkill {
		return "", false, fmt.Errorf("未找到 skill: %s", requestedName)
	}
	return "", false, fmt.Errorf("未找到 function: %s", requestedName)
}

func resolveExactCallableFunctionName(catalog *aicliFunctionCatalog, requestedName string) (string, bool, bool) {
	if catalog == nil || catalog.Registry() == nil {
		return "", false, false
	}

	candidates := []string{requestedName}
	if skillName := buildSkillFunctionName(requestedName); skillName != requestedName {
		candidates = append(candidates, skillName)
	}

	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if _, ok := seen[candidate]; ok {
			continue
		}
		seen[candidate] = struct{}{}
		if _, ok := catalog.Registry().Get(candidate); !ok {
			continue
		}
		isSkill := strings.HasPrefix(candidate, skillFunctionPrefix)
		if descriptor := catalog.Descriptor(candidate); descriptor != nil {
			isSkill = descriptor.Kind == "skill" || strings.HasPrefix(candidate, skillFunctionPrefix)
		}
		return candidate, isSkill, true
	}

	return "", false, false
}

func resolveSkillCallableReference(catalog *aicliFunctionCatalog, requestedName string) (string, bool, error) {
	if catalog == nil {
		return "", false, nil
	}

	normalized := normalizeSkillReferenceName(requestedName)
	if normalized == "" {
		return "", false, nil
	}
	looksLikePath := strings.ContainsAny(normalized, `/\`) ||
		strings.HasSuffix(strings.ToLower(normalized), ".md") ||
		strings.HasSuffix(strings.ToLower(normalized), ".yaml") ||
		strings.HasSuffix(strings.ToLower(normalized), ".yml")

	var pathMatches []string
	var nameMatches []string
	for _, descriptor := range catalog.Descriptors() {
		if descriptor == nil || descriptor.Kind != "skill" {
			continue
		}
		callableName := descriptorDisplayName(descriptor)
		if callableName == "" {
			continue
		}

		if looksLikePath && descriptorSourcePathMatches(descriptor, normalized) {
			pathMatches = append(pathMatches, callableName)
			continue
		}
		if strings.EqualFold(descriptor.Name, normalized) {
			nameMatches = append(nameMatches, callableName)
		}
	}

	pathMatches = uniqueStrings(pathMatches)
	if len(pathMatches) == 1 {
		return pathMatches[0], true, nil
	}
	if len(pathMatches) > 1 {
		return "", false, fmt.Errorf("skill 路径 %q 不唯一，请使用唯一 function 名称", requestedName)
	}

	nameMatches = uniqueStrings(nameMatches)
	if len(nameMatches) == 1 {
		return nameMatches[0], true, nil
	}
	if len(nameMatches) > 1 {
		return "", false, fmt.Errorf("skill 名称 %q 不唯一，请使用路径或唯一 function 名称", requestedName)
	}

	return "", false, nil
}

func normalizeSkillReferenceName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	name = strings.TrimPrefix(name, "skill://")
	return strings.TrimSpace(name)
}

func descriptorSourcePathMatches(descriptor *capability.Descriptor, requestedName string) bool {
	if descriptor == nil || descriptor.Source == nil {
		return false
	}

	sourcePath := filepath.Clean(strings.TrimSpace(descriptor.Source.Path))
	requestedName = filepath.Clean(strings.TrimSpace(requestedName))
	if sourcePath == "" || sourcePath == "." || requestedName == "" || requestedName == "." {
		return false
	}

	sourceSlash := filepath.ToSlash(sourcePath)
	requestedSlash := filepath.ToSlash(requestedName)
	if strings.EqualFold(sourceSlash, requestedSlash) {
		return true
	}
	if hasPathSuffix(sourceSlash, requestedSlash) || hasPathSuffix(requestedSlash, sourceSlash) {
		return true
	}
	return false
}

func hasPathSuffix(path, suffix string) bool {
	path = filepath.ToSlash(filepath.Clean(strings.TrimSpace(path)))
	suffix = filepath.ToSlash(filepath.Clean(strings.TrimSpace(suffix)))
	if path == "" || path == "." || suffix == "" || suffix == "." {
		return false
	}
	if strings.EqualFold(path, suffix) {
		return true
	}
	return strings.HasSuffix(path, "/"+suffix)
}

func parseDirectFunctionArgs(raw string, isSkill bool, functionName string) (map[string]interface{}, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		if isSkill {
			return nil, fmt.Errorf("skill 调用需要提供 prompt 或 JSON 参数")
		}
		return map[string]interface{}{}, nil
	}
	if strings.HasPrefix(raw, "{") {
		var args map[string]interface{}
		if err := json.Unmarshal([]byte(raw), &args); err != nil {
			return nil, fmt.Errorf("JSON 参数解析失败: %w", err)
		}
		if args == nil {
			args = map[string]interface{}{}
		}
		return args, nil
	}
	if isSkill {
		return map[string]interface{}{"prompt": raw}, nil
	}
	if toolnames.IsOpenAIImageGenerateToolName(functionName) {
		return map[string]interface{}{"prompt": raw}, nil
	}
	return nil, fmt.Errorf("非 skill function 需要 JSON object 参数，例如 {\"prompt\":\"...\"}")
}

func executeDirectFunction(session *ChatSession, requestedName, functionName string, args map[string]interface{}) (*directFunctionInvokeReport, error) {
	catalog := ensureFunctionCatalog(session)
	if catalog == nil || catalog.Registry() == nil {
		return nil, fmt.Errorf("function registry 未初始化")
	}

	ctx := context.Background()
	if session != nil {
		if session.cancelCtx != nil {
			ctx = session.cancelCtx
		}
		if session.RequestTimeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, session.RequestTimeout)
			defer cancel()
		}
	}
	ctx = generatedImageToolContext(ctx, session)

	scope, toolCallID, startedAt, shouldLog := beginDirectFunctionExecutionLog(session, requestedName, functionName, args)
	output, metadata, err := catalog.Registry().ExecuteFunctionWithMeta(ctx, functionName, args)
	if err != nil {
		finishDirectFunctionExecutionLog(session, scope, toolCallID, functionName, output, metadata, err, startedAt, shouldLog)
		return nil, err
	}
	if strings.HasPrefix(functionName, skillFunctionPrefix) {
		normalizedOutput, normalizedMeta, normalizeErr := normalizeDirectSkillCommandResult(output, metadata)
		if normalizeErr != nil {
			finishDirectFunctionExecutionLog(session, scope, toolCallID, functionName, normalizedOutput, normalizedMeta, normalizeErr, startedAt, shouldLog)
			return nil, normalizeErr
		}
		output = normalizedOutput
		metadata = normalizedMeta
	}
	finishDirectFunctionExecutionLog(session, scope, toolCallID, functionName, output, metadata, nil, startedAt, shouldLog)
	return &directFunctionInvokeReport{
		RequestedName: requestedName,
		FunctionName:  functionName,
		Output:        output,
		Metadata:      metadata,
	}, nil
}

func renderDirectSkillInvocationStarted(session *ChatSession, command, requestedName, functionName string, args map[string]interface{}, jsonOutput bool) {
	if jsonOutput || session == nil || session.NoInteractive || session.JSONOutput {
		return
	}
	lines := []string{}
	if preview := compactDirectCommandPreview(command); preview != "" {
		lines = append(lines, prefixExecutionBullet("Running "+preview))
	} else {
		lines = append(lines, prefixExecutionBullet(fmt.Sprintf("Running /skill %s", strings.TrimSpace(requestedName))))
	}
	if bridgePreview := directSkillBridgeCommandPreview(session, functionName, args); bridgePreview != "" {
		lines = append(lines, "  "+bridgePreview)
	}
	renderDirectInvocationLine(session, strings.Join(lines, "\n"))
}

func renderDirectInvocationLine(session *ChatSession, line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	if session != nil && session.Interaction != nil && !session.NoInteractive && !session.JSONOutput {
		session.Interaction.RenderAsyncLine(line)
		return
	}
	fmt.Println(line)
}

func compactDirectCommandPreview(command string) string {
	command = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(command, "\r", " "), "\n", " "))
	if command == "" {
		return ""
	}
	return truncateChatRuntimeText(command, 240)
}

func directSkillBridgeCommandPreview(session *ChatSession, functionName string, args map[string]interface{}) string {
	catalog := ensureFunctionCatalog(session)
	if catalog == nil {
		return ""
	}
	catalog.syncFromRegistry()
	entry := catalog.entries[strings.TrimSpace(functionName)]
	if entry == nil || entry.fn == nil {
		return ""
	}
	skillFn, ok := entry.fn.(*SkillFunction)
	if !ok || skillFn == nil {
		return ""
	}
	toolName := resolveDirectToolBridgeName(skillFn.summary, skillFn.skill)
	if !strings.EqualFold(toolName, aicliExecToolName) {
		return ""
	}
	toolArgs := buildDirectToolBridgeArgsFromPromptOptions(toolName, resolveSkillPrompt(args), extractMapArg(args, "options"))
	return renderAICLIExecCommandPreview(toolArgs)
}

func renderAICLIExecCommandPreview(args map[string]interface{}) string {
	if len(args) == 0 {
		return ""
	}
	parts := []string{"aicli"}
	if value := directPreviewStringArg(args, "config"); value != "" {
		parts = append(parts, "--config", quoteDirectCommandArg(value))
	}
	if directPreviewBoolArg(args, "envelope") {
		parts = append(parts, "--envelope")
	}
	parts = append(parts, "exec")
	for _, item := range []struct {
		key  string
		flag string
	}{
		{"provider", "--provider"},
		{"model", "--model"},
		{"profile", "--profile"},
		{"agent", "--agent"},
		{"log_dir", "--log-dir"},
		{"session_dir", "--session-dir"},
		{"user", "--user"},
		{"title", "--title"},
		{"output", "--output"},
		{"permission_mode", "--permission-mode"},
		{"skills_mode", "--skills-mode"},
		{"request_timeout", "--request-timeout"},
	} {
		if value := directPreviewStringArg(args, item.key); value != "" {
			parts = append(parts, item.flag, quoteDirectCommandArg(value))
		}
	}
	for _, dir := range directPreviewStringListArg(args, "skills_dir") {
		parts = append(parts, "--skills-dir", quoteDirectCommandArg(dir))
	}
	for _, item := range []struct {
		key  string
		flag string
	}{
		{"json", "--json"},
		{"disable_tools", "--disable-tools"},
		{"yolo", "--yolo"},
		{"debug_http", "--debug-http"},
		{"fail_fast", "--fail-fast"},
	} {
		if directPreviewBoolArg(args, item.key) {
			parts = append(parts, item.flag)
		}
	}
	if value := directPreviewStringArg(args, "timeout"); value != "" {
		parts = append(parts, "--timeout", quoteDirectCommandArg(value))
	} else if value := directPreviewStringArg(args, "timeout_ms"); value != "" {
		parts = append(parts, "--timeout", quoteDirectCommandArg(directPreviewTimeoutMS(value)))
	}
	return truncateChatRuntimeText(strings.Join(parts, " ")+" <prompt via stdin>", 240)
}

func directPreviewTimeoutMS(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return value
		}
	}
	return value + "ms"
}

func directPreviewStringArg(args map[string]interface{}, key string) string {
	if args == nil {
		return ""
	}
	value, ok := args[key]
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case fmt.Stringer:
		return strings.TrimSpace(typed.String())
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		return strings.TrimSpace(fmt.Sprint(typed))
	default:
		return ""
	}
}

func directPreviewBoolArg(args map[string]interface{}, key string) bool {
	if args == nil {
		return false
	}
	value, ok := args[key]
	if !ok || value == nil {
		return false
	}
	typed, ok := value.(bool)
	return ok && typed
}

func directPreviewStringListArg(args map[string]interface{}, key string) []string {
	if args == nil {
		return nil
	}
	value, ok := args[key]
	if !ok || value == nil {
		return nil
	}
	switch typed := value.(type) {
	case []string:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if item = strings.TrimSpace(item); item != "" {
				out = append(out, item)
			}
		}
		return out
	case []interface{}:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok {
				if text = strings.TrimSpace(text); text != "" {
					out = append(out, text)
				}
			}
		}
		return out
	default:
		if text := directPreviewStringArg(args, key); text != "" {
			return []string{text}
		}
		return nil
	}
}

func quoteDirectCommandArg(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return `""`
	}
	if !strings.ContainsAny(value, " \t\r\n\"") {
		return value
	}
	return `"` + strings.ReplaceAll(value, `"`, `\"`) + `"`
}

func beginDirectFunctionExecutionLog(session *ChatSession, requestedName, functionName string, args map[string]interface{}) (aicliLogScope, string, time.Time, bool) {
	startedAt := time.Now()
	if session == nil || session.Logger == nil {
		return aicliLogScope{}, "", startedAt, false
	}
	requestID := fmt.Sprintf("direct-%d", startedAt.UnixNano())
	scope := aicliLogScope{
		TurnID:    "direct",
		RequestID: requestID,
	}
	toolCallID := requestID + "-call-01"
	session.Logger.LogToolCall(scope, toolCallID, functionName, map[string]interface{}{
		"requested_name": requestedName,
		"function_name":  functionName,
		"args":           cloneFunctionSchema(args),
		"direct_command": true,
	})
	writeSessionDebugInfo(session, fmt.Sprintf("[direct-function] start requested=%q function=%q", requestedName, functionName), false)
	return scope, toolCallID, startedAt, true
}

func finishDirectFunctionExecutionLog(session *ChatSession, scope aicliLogScope, toolCallID, functionName, output string, metadata map[string]interface{}, err error, startedAt time.Time, shouldLog bool) {
	if !shouldLog || session == nil || session.Logger == nil {
		return
	}
	durationMs := time.Since(startedAt).Milliseconds()
	result := toolExecutionLogPayload(output, metadata)
	session.Logger.LogToolResult(scope, toolCallID, functionName, result, err)
	message := fmt.Sprintf("[direct-function] finish function=%q duration_ms=%d output_bytes=%d", functionName, durationMs, len(output))
	if err != nil {
		message += fmt.Sprintf(" error=%q", err.Error())
	}
	writeSessionDebugInfo(session, message, false)
	if flushErr := session.Logger.FlushSession(); flushErr != nil {
		writeSessionDebugInfo(session, fmt.Sprintf("[direct-function] flush failed function=%q error=%q", functionName, flushErr.Error()), false)
	}
}

type directSkillCommandEnvelope struct {
	Success      bool                   `json:"success"`
	Output       string                 `json:"output"`
	Error        string                 `json:"error"`
	Observations []interface{}          `json:"observations"`
	Usage        map[string]interface{} `json:"usage"`
	Skill        string                 `json:"skill"`
}

func normalizeDirectSkillCommandResult(output string, metadata map[string]interface{}) (string, map[string]interface{}, error) {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" || (!strings.HasPrefix(trimmed, "{") && !strings.HasPrefix(trimmed, "[")) {
		return output, metadata, nil
	}

	var envelope directSkillCommandEnvelope
	if err := json.Unmarshal([]byte(trimmed), &envelope); err != nil {
		return output, metadata, nil
	}

	if envelope.Error == "" && envelope.Output == "" && envelope.Skill == "" && len(envelope.Observations) == 0 && len(envelope.Usage) == 0 && !envelope.Success {
		return output, metadata, nil
	}

	if metadata == nil {
		metadata = map[string]interface{}{}
	}
	if len(envelope.Observations) > 0 {
		metadata["skill_observations"] = envelope.Observations
	}
	if len(envelope.Usage) > 0 {
		metadata["skill_usage"] = envelope.Usage
	}
	if strings.TrimSpace(envelope.Skill) != "" {
		metadata["skill"] = envelope.Skill
	}
	if strings.TrimSpace(envelope.Error) != "" || !envelope.Success {
		errMsg := strings.TrimSpace(envelope.Error)
		if errMsg == "" {
			errMsg = "skill execution failed"
		}
		return envelope.Output, metadata, fmt.Errorf("%s", errMsg)
	}
	return envelope.Output, metadata, nil
}

func formatDirectFunctionInvokeReport(report *directFunctionInvokeReport, jsonOutput bool) string {
	if report == nil {
		return formatCommandError("direct function result is empty", jsonOutput)
	}
	if jsonOutput {
		return marshalIndentedJSON(report)
	}
	if strings.TrimSpace(report.Output) != "" {
		return report.Output
	}
	if len(report.Metadata) > 0 {
		return marshalIndentedJSON(report.Metadata)
	}
	return fmt.Sprintf("Function %s 执行完成", report.FunctionName)
}
