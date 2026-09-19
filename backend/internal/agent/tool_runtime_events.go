package agent

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/protocol"
	"github.com/wwsheng009/ai-agent-runtime/internal/output"
	"github.com/wwsheng009/ai-agent-runtime/internal/pathdisplay"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolprotocol"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

var preferredToolArgPreviewKeys = []string{
	"command",
	"path",
	"file_path",
	"pattern",
	"query",
	"q",
	"url",
	"prompt",
	"name",
	"title",
	"task_id",
	"team_id",
	"id",
	"message",
	"input",
	"content",
}

func toolRequestedEventPayload(call types.ToolCall, step int, traceID string, extra map[string]interface{}) map[string]interface{} {
	payload := map[string]interface{}{
		"tool_call_id": call.ID,
		"logical_tool": strings.TrimSpace(call.Name),
		"step":         step,
		"trace_id":     traceID,
	}
	if preview := summarizeToolCallArgs(call.Name, call.Args); preview != "" {
		payload["arg_preview"] = preview
	}
	if commandText := summarizeShellToolCommand(call.Name, call.Args); commandText != "" {
		payload["command_text"] = commandText
	}
	copyToolExecutionDirectory(payload, call.Args)
	copyToolDisplayFilePath(payload, call.Args)
	mergeToolEventPayload(payload, extra)
	return payload
}

func toolCompletedEventPayload(result toolExecutionResult, step int, traceID string, extra map[string]interface{}) map[string]interface{} {
	payload := map[string]interface{}{
		"tool_call_id": result.Call.ID,
		"logical_tool": strings.TrimSpace(result.Call.Name),
		"step":         step,
		"error":        result.Error,
		"trace_id":     traceID,
	}
	if preview := summarizeToolCallArgs(result.Call.Name, result.Call.Args); preview != "" {
		payload["arg_preview"] = preview
	}
	if commandText := summarizeShellToolCommand(result.Call.Name, result.Call.Args); commandText != "" {
		payload["command_text"] = commandText
	}
	copyToolExecutionDirectory(payload, result.Call.Args)
	copyToolDisplayFilePath(payload, result.Call.Args)
	if summaryLines := summarizeToolExecutionLines(result); len(summaryLines) > 0 {
		payload["summary"] = strings.Join(summaryLines, "\n")
		payload["summary_lines"] = append([]string(nil), summaryLines...)
	}
	if output := editingToolRenderOutput(result); output != "" {
		payload["render_output"] = output
		payload["render_output_format"] = "markdown"
		payload["render_output_untruncated"] = true
	} else if output := shellDiffToolRenderOutput(result); output != "" {
		payload["render_output"] = output
		payload["render_output_format"] = "diff"
		payload["render_output_untruncated"] = true
	}
	if result.Envelope != nil {
		if source := toolresult.SourceFromMetadata(result.Envelope.Metadata); source != "" {
			payload[toolresult.SourceKey] = source
		}
		if kind := toolresult.KindFromMetadata(result.Envelope.Metadata); kind != "" {
			payload[toolresult.MetadataKey] = kind
		}
		copyToolShellMetadata(payload, result.Envelope.Metadata)
		copyToolReliabilityMetadata(payload, result.Envelope.Metadata)
		copyToolArtifactFlowMetadata(payload, result.Envelope)
	}
	// Promote disposition contracts onto tool.completed / chat-log payloads so
	// offline analyzers can distinguish success vs empty vs partial vs failed
	// without parsing summary text. Generic: driven by envelope metadata +
	// toolresult.Diagnose, never tool-name special cases.
	promoteToolDispositionToPayload(payload, result)
	// 事件载荷的 duration_ms 被实时标题（bridge 编码）与事件日志/重放投影共同
	// 当作工具调用耗时展示。工具自身上报的执行耗时（tool_metadata.duration_ms）
	// 比 bridge 的墙钟回退更权威且可复现：载荷缺失时在此提升，使 live 与
	// replay 使用同一口径（bridge 对已有 duration_ms 不再覆盖）。
	if intValue(payload["duration_ms"]) <= 0 && result.Envelope != nil {
		if nested, ok := result.Envelope.Metadata["tool_metadata"].(map[string]interface{}); ok {
			if durationMs := intValue(nested["duration_ms"]); durationMs > 0 {
				payload["duration_ms"] = durationMs
			}
		}
	}
	// Nested portable wire view for hosts that prefer toolprotocol.Result shape.
	attachProtocolResultToPayload(payload, result)
	mergeToolEventPayload(payload, extra)
	return payload
}

func editingToolRenderOutput(result toolExecutionResult) string {
	toolName := strings.TrimSpace(result.Call.Name)
	output := strings.TrimSpace(extractToolTextOutput(result.Output))
	switch toolName {
	case "edit", "apply_patch":
		if output != "" {
			return output
		}
	case "write", "append_write", "multiedit":
		if output != "" {
			return ""
		}
	default:
		return ""
	}
	if strings.TrimSpace(result.Error) != "" {
		return ""
	}
	return toolresult.MutationSummary(toolMetadataFromEnvelope(result.Envelope))
}

func shellDiffToolRenderOutput(result toolExecutionResult) string {
	if !runtimepolicy.IsShellLikeToolName(strings.TrimSpace(result.Call.Name)) ||
		!runtimeexecutor.IsGitDiffCommand(shellCommandText(result.Call.Args)) ||
		strings.TrimSpace(result.Error) != "" {
		return ""
	}
	if result.Envelope != nil {
		metadata := result.Envelope.Metadata
		if complete, ok := metadataBoolValue(metadata, "output_capture_complete"); ok && !complete {
			return ""
		}
		if limited, _ := metadataBoolValue(metadata, "capture_limit_reached"); limited {
			return ""
		}
	}
	output := strings.TrimSpace(extractToolTextOutput(result.Output))
	if !runtimeexecutor.LooksLikeUnifiedDiffOutput(output) {
		return ""
	}
	return output
}

// copyToolArtifactFlowMetadata promotes the plan §11.3 artifact-flow fields
// onto tool.completed payloads so session analytics can aggregate output
// sizes, truncation dimensions, archive dispositions, and pointer kinds
// without a new event pipeline. All fields are numeric/bool/short-enum.
func copyToolArtifactFlowMetadata(payload map[string]interface{}, envelope *output.Envelope) {
	if payload == nil || envelope == nil {
		return
	}
	metadata := envelope.Metadata
	if size := metadataInt64Value(metadata, "raw_bytes"); size > 0 {
		payload["output_original_bytes"] = size
	}
	if visible := len(envelope.Render()); visible > 0 {
		payload["output_model_visible_bytes"] = visible
	}
	if archivedID, ok := metadata["artifact_id"].(string); ok && strings.TrimSpace(archivedID) != "" {
		payload["artifact_id"] = strings.TrimSpace(archivedID)
		payload["artifact_archived"] = true
	} else if skipped, ok := metadata["artifact_skipped"].(string); ok && strings.TrimSpace(skipped) != "" {
		payload["artifact_skipped"] = strings.TrimSpace(skipped)
	}
}

func metadataInt64Value(metadata map[string]interface{}, key string) int64 {
	if metadata == nil {
		return 0
	}
	switch typed := metadata[key].(type) {
	case int:
		return int64(typed)
	case int32:
		return int64(typed)
	case int64:
		return typed
	case float64:
		return int64(typed)
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			return parsed
		}
	}
	return 0
}

func copyToolExecutionDirectory(payload map[string]interface{}, args map[string]interface{}) {
	if payload == nil || len(args) == 0 {
		return
	}
	if workdir := normalizeToolEventText(renderToolArgValue(args["workdir"])); workdir != "" && workdir != "<nil>" {
		payload["workdir"] = truncateToolEventText(workdir, 200)
		return
	}
	if workdir := normalizeToolEventText(renderToolArgValue(args["working_directory"])); workdir != "" && workdir != "<nil>" {
		payload["workdir"] = truncateToolEventText(workdir, 200)
		return
	}
	if cwd := normalizeToolEventText(renderToolArgValue(args["cwd"])); cwd != "" && cwd != "<nil>" {
		payload["cwd"] = truncateToolEventText(cwd, 200)
	}
}

func copyToolDisplayFilePath(payload map[string]interface{}, args map[string]interface{}) {
	if payload == nil {
		return
	}
	_, path := pathdisplay.File(args)
	if pathdisplay.NeedsOwnLine(path) {
		payload["display_file_path"] = path
	}
}

func mergeToolEventPayload(payload map[string]interface{}, extra map[string]interface{}) {
	for key, value := range extra {
		if strings.TrimSpace(key) == "" {
			continue
		}
		payload[key] = value
	}
}

func summarizeToolCallArgs(toolName string, args map[string]interface{}) string {
	if len(args) == 0 {
		return ""
	}
	if preview := summarizeSearchToolCallArgs(toolName, args); preview != "" {
		return preview
	}
	if preview := summarizeShellToolCallArgs(toolName, args); preview != "" {
		return preview
	}

	fileArgKey, filePath := pathdisplay.File(args)
	parts := make([]string, 0, len(args))
	seen := make(map[string]struct{}, len(preferredToolArgPreviewKeys))
	for _, key := range preferredToolArgPreviewKeys {
		seen[key] = struct{}{}
		if preview := formatToolArgPreview(key, args[key], fileArgKey, filePath); preview != "" {
			parts = append(parts, preview)
		}
	}

	keys := make([]string, 0, len(args))
	for key := range args {
		if _, ok := seen[key]; ok {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if toolArgPreviewRenderedSeparately(key) {
			continue
		}
		if preview := formatToolArgPreview(key, args[key], fileArgKey, filePath); preview != "" {
			parts = append(parts, preview)
		}
	}
	return truncateToolEventText(strings.Join(parts, " "), 200)
}

func toolArgPreviewRenderedSeparately(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "cwd", "workdir", "working_directory":
		return true
	default:
		return false
	}
}

func summarizeSearchToolCallArgs(toolName string, args map[string]interface{}) string {
	var keys []string
	switch strings.ToLower(strings.TrimSpace(toolName)) {
	case "glob":
		keys = []string{"pattern", "path", "case_insensitive", "ignore_case", "limit"}
	case "grep":
		keys = []string{"patterns", "pattern", "regexp", "paths", "path", "glob", "include", "type", "literal", "ignore_case"}
	default:
		return ""
	}

	parts := make([]string, 0, len(args))
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		seen[key] = struct{}{}
		if preview := formatSearchToolArgPreview(key, args[key]); preview != "" {
			parts = append(parts, preview)
		}
	}

	remainingKeys := make([]string, 0, len(args)-len(parts))
	for key := range args {
		if _, ok := seen[key]; ok || toolArgPreviewRenderedSeparately(key) {
			continue
		}
		remainingKeys = append(remainingKeys, key)
	}
	sort.Strings(remainingKeys)
	for _, key := range remainingKeys {
		if preview := formatSingleToolArgPreview(key, args[key]); preview != "" {
			parts = append(parts, preview)
		}
	}
	return truncateToolEventText(strings.Join(parts, " "), 200)
}

// summarizeShellToolCallArgs 让 shell 类工具的预览直接给出命令文本：批量形态的
// `commands` 列表若走通用渲染，会退化成被截断的 JSON（`commands=[{"command":"…`），
// 既不可读，也会把 200 字预算耗在结构符号上。
func summarizeShellToolCallArgs(toolName string, args map[string]interface{}) string {
	if !runtimepolicy.IsShellLikeToolName(strings.TrimSpace(toolName)) {
		return ""
	}
	command := shellCommandText(args)
	if command == "" {
		return ""
	}

	parts := []string{formatSingleToolArgPreview("command", command)}
	keys := make([]string, 0, len(args))
	for key := range args {
		if key == "command" || key == "commands" || toolArgPreviewRenderedSeparately(key) {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if preview := formatSingleToolArgPreview(key, args[key]); preview != "" {
			parts = append(parts, preview)
		}
	}
	return truncateToolEventText(strings.Join(parts, " "), 200)
}

func formatSearchToolArgPreview(key string, value interface{}) string {
	key = strings.TrimSpace(key)
	if key == "" || value == nil {
		return ""
	}
	text := normalizeToolEventText(renderSearchToolArgValue(value))
	if text == "" || text == "{}" || text == "[]" {
		return ""
	}
	return key + "=" + text
}

// renderSearchToolArgValue 渲染搜索类入参值：字符串列表（`patterns` / `paths` 的批量
// 形态）按 " | " 连接成单行。搜索入参天然是列表语义，走 JSON 渲染会把 `["a","b"]`
// 的标点带进 UI，还把 200 字预算耗在结构符号上；分隔符与前端折叠摘要统一
// （`tool-row/query-text.ts` 的 LIST_TEXT_SEPARATOR），同一份入参两条链路渲染一致。
func renderSearchToolArgValue(value interface{}) string {
	if items := searchToolArgListItems(value); len(items) > 0 {
		return strings.Join(items, " | ")
	}
	return renderToolArgValue(value)
}

// searchToolArgListItems 归一搜索类入参里的字符串列表；非列表值返回 nil，交回通用渲染。
func searchToolArgListItems(value interface{}) []string {
	var raw []interface{}
	switch typed := value.(type) {
	case []string:
		raw = make([]interface{}, 0, len(typed))
		for _, item := range typed {
			raw = append(raw, item)
		}
	case []interface{}:
		raw = typed
	default:
		return nil
	}
	items := make([]string, 0, len(raw))
	for _, item := range raw {
		text := normalizeToolEventText(renderToolArgValue(item))
		if text != "" && text != "{}" && text != "[]" {
			items = append(items, text)
		}
	}
	return items
}

func summarizeShellToolCommand(toolName string, args map[string]interface{}) string {
	if !runtimepolicy.IsShellLikeToolName(strings.TrimSpace(toolName)) || len(args) == 0 {
		return ""
	}
	command := shellCommandText(args)
	if command == "" {
		return ""
	}
	return truncateToolEventText(command, 200)
}

// shellCommandText 归一 shell 类工具的命令文本：单个 `command`，或批量 `commands`
// 列表（元素为字符串，或 `{command, workdir}` 对象——见 toolargs 的 shell 参数表）。
// 批量按 " ; " 连接成单行：这些命令是各自独立执行的，不能用 `&&` 冒充依赖关系。
func shellCommandText(args map[string]interface{}) string {
	if len(args) == 0 {
		return ""
	}
	if command := shellArgString(args["command"]); command != "" {
		return command
	}
	items := shellCommandItems(args["commands"])
	parts := make([]string, 0, len(items))
	for _, item := range items {
		if text := shellArgString(shellCommandItemValue(item)); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, " ; ")
}

// shellCommandItemValue 取批量命令项的命令文本；对象里没有命令字段时返回 nil，
// 避免把整个对象渲染成 JSON 塞进摘要。
func shellCommandItemValue(item interface{}) interface{} {
	record, ok := item.(map[string]interface{})
	if !ok {
		return item
	}
	for _, key := range []string{"command", "cmd", "script", "shell_command"} {
		if value, ok := record[key]; ok {
			return value
		}
	}
	return nil
}

func shellCommandItems(value interface{}) []interface{} {
	switch typed := value.(type) {
	case []interface{}:
		return typed
	case []map[string]interface{}:
		items := make([]interface{}, 0, len(typed))
		for _, item := range typed {
			items = append(items, item)
		}
		return items
	case []string:
		items := make([]interface{}, 0, len(typed))
		for _, item := range typed {
			items = append(items, item)
		}
		return items
	default:
		return nil
	}
}

func shellArgString(value interface{}) string {
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return normalizeToolEventText(text)
}

func formatSingleToolArgPreview(key string, value interface{}) string {
	key = strings.TrimSpace(key)
	if key == "" || value == nil {
		return ""
	}
	if sensitiveToolArgPreviewKey(key) {
		return key + "=<redacted>"
	}

	text := normalizeToolEventText(renderToolArgValue(value))
	if text == "" || text == "{}" || text == "[]" {
		return ""
	}
	return truncateToolEventText(fmt.Sprintf("%s=%s", key, text), 72)
}

func formatToolArgPreview(key string, value interface{}, fileArgKey, filePath string) string {
	if key == fileArgKey && filePath != "" {
		if pathdisplay.NeedsOwnLine(filePath) {
			return ""
		}
		value = filePath
	}
	return formatSingleToolArgPreview(key, value)
}

func sensitiveToolArgPreviewKey(key string) bool {
	normalized := strings.ToLower(strings.TrimSpace(key))
	for _, marker := range []string{
		"api_key", "apikey", "access_key", "private_key", "authorization",
		"credential", "password", "passwd", "secret", "cookie",
	} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return strings.HasSuffix(normalized, "token")
}

func renderToolArgValue(value interface{}) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case bool:
		return fmt.Sprintf("%t", typed)
	case []string:
		switch len(typed) {
		case 0:
			return ""
		case 1:
			return typed[0]
		default:
			return fmt.Sprintf("[%d]", len(typed))
		}
	case []interface{}:
		switch len(typed) {
		case 0:
			return ""
		case 1:
			return renderToolArgValue(typed[0])
		default:
			return fmt.Sprintf("[%d]", len(typed))
		}
	case map[string]interface{}:
		if len(typed) == 0 {
			return ""
		}
		for _, nestedKey := range preferredToolArgPreviewKeys {
			if preview := formatSingleToolArgPreview(keyWithPrefix("", nestedKey), typed[nestedKey]); preview != "" {
				return strings.TrimPrefix(preview, nestedKey+"=")
			}
		}
		raw, err := json.Marshal(toolresult.CompactAttemptedArgs(typed))
		if err != nil {
			return fmt.Sprintf("%v", value)
		}
		return string(raw)
	default:
		return fmt.Sprintf("%v", value)
	}
}

func keyWithPrefix(prefix, key string) string {
	if strings.TrimSpace(prefix) == "" {
		return strings.TrimSpace(key)
	}
	if strings.TrimSpace(key) == "" {
		return strings.TrimSpace(prefix)
	}
	return strings.TrimSpace(prefix) + "." + strings.TrimSpace(key)
}

func summarizeToolExecutionLines(result toolExecutionResult) []string {
	toolName := firstNonEmptyToolRuntimeValue(result.Call.Name)
	if lines := summarizeToolTextLines(extractToolTextOutput(result.Output), toolName); len(lines) > 0 {
		return lines
	}
	errText := normalizeToolEventText(result.Error)
	if result.Envelope != nil {
		envelopeToolName := firstNonEmptyToolRuntimeValue(result.Call.Name, result.Envelope.ToolName)
		if lines := summarizeToolTextLines(result.Envelope.Summary, envelopeToolName); len(lines) > 0 {
			if errText == "" || !isGenericToolFailureSummary(result.Envelope.Summary, firstNonEmptyToolRuntimeValue(result.Call.Name, result.Envelope.ToolName)) {
				return lines
			}
		}
	}
	if errText != "" {
		return []string{truncateToolEventText("failed: "+errText, 120)}
	}
	if summary := summarizeToolMetadata(toolMetadataFromEnvelope(result.Envelope)); summary != "" {
		return []string{summary}
	}
	return nil
}

func isGenericToolFailureSummary(summary, toolName string) bool {
	normalized := strings.ToLower(normalizeToolEventText(summary))
	if normalized == "" {
		return false
	}
	if normalized == "tool returned no output." {
		return true
	}
	if strings.HasPrefix(normalized, "tool ") && strings.HasSuffix(normalized, " failed before producing output.") {
		return true
	}
	if strings.TrimSpace(toolName) == "" {
		return false
	}
	expected := strings.ToLower(normalizeToolEventText(fmt.Sprintf("Tool %s failed before producing output.", strings.TrimSpace(toolName))))
	return normalized == expected
}

func firstNonEmptyToolRuntimeValue(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func toolMetadataFromEnvelope(envelope *output.Envelope) map[string]interface{} {
	if envelope == nil || len(envelope.Metadata) == 0 {
		return nil
	}
	raw, ok := envelope.Metadata["tool_metadata"].(map[string]interface{})
	if !ok || len(raw) == 0 {
		return nil
	}
	return raw
}

func copyToolShellMetadata(payload map[string]interface{}, metadata map[string]interface{}) {
	if payload == nil || len(metadata) == 0 {
		return
	}
	if nested, ok := metadata["tool_metadata"].(map[string]interface{}); ok {
		copyToolShellMetadata(payload, nested)
	}
	for _, key := range []string{
		"shell_type",
		"shell_path",
		"shell_display",
		"raw_output_artifact_path",
		"raw_output_artifact_error",
	} {
		value, _ := metadata[key].(string)
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			payload[key] = trimmed
		}
	}
}

func copyToolReliabilityMetadata(payload map[string]interface{}, metadata map[string]interface{}) {
	if payload == nil || len(metadata) == 0 {
		return
	}
	if nested, ok := metadata["tool_metadata"].(map[string]interface{}); ok {
		copyToolReliabilityMetadata(payload, nested)
	}
	for _, key := range []string{
		toolresult.MetadataOKKey,
		toolresult.MetadataErrorCodeKey,
		"error_message",
		"engine",
		"execution_backend",
		"backend_command",
		"backend_path",
		"backend_source",
		toolresult.MetadataRetryableKey,
		toolresult.MetadataNextActionKey,
		toolresult.MetadataPolicyKey,
		toolresult.MetadataPolicySource,
		toolresult.MetadataOverridable,
		toolresult.MetadataOutcomeKey,
		toolresult.MetadataEmptyResultKey,
		toolresult.MetadataRequestedCountKey,
		toolresult.MetadataFailedCountKey,
		toolresult.MetadataSucceededCountKey,
		toolresult.MetadataPartialFailureKey,
		toolresult.MetadataFailedItemsKey,
		toolresult.MetadataPathCandidatesKey,
		toolresult.MetadataAttemptedArgsKey,
		"failure_class",
		"file_path",
		"suggested_view_offset",
		"suggested_view_limit",
		"current_snippet",
		"current_snippet_start_line",
		"path_auto_healed",
		"original_path",
		"resolved_path",
		"timeout_requested_ms",
		"timeout_effective_ms",
		"timeout_source",
		"timeout_ms",
		"cancel_source",
		"output_capture_complete",
		"capture_limit_reached",
		"output_capture_limit_disabled",
		"output_capture_limit_bytes",
		"retained_output_bytes",
		"omitted_output_bytes",
	} {
		if value, ok := metadata[key]; ok && value != nil {
			payload[key] = value
		}
	}
	logicalTool := strings.ToLower(strings.TrimSpace(fmt.Sprint(payload["logical_tool"])))
	if backend := strings.TrimSpace(fmt.Sprint(payload["execution_backend"])); (backend == "" || backend == "<nil>") && (logicalTool == "grep" || logicalTool == "glob") {
		if engine := strings.TrimSpace(fmt.Sprint(payload["engine"])); engine != "" && engine != "<nil>" {
			payload["execution_backend"] = engine
		}
	}
}

// promoteToolDispositionToPayload stamps the stable toolresult disposition
// contract onto a tool.completed event payload. Chat-log export stores this
// payload as tool_result.content.result, so offline efficiency reports can read
// outcome/empty_result/failed_items without relying on model-facing history text.
func promoteToolDispositionToPayload(payload map[string]interface{}, result toolExecutionResult) {
	if payload == nil {
		return
	}
	var metadata map[string]interface{}
	if result.Envelope != nil {
		metadata = result.Envelope.Metadata
	}
	toolName := firstNonEmptyToolRuntimeValue(result.Call.Name)
	toolCallID := firstNonEmptyToolRuntimeValue(result.Call.ID)
	if result.Envelope != nil {
		if toolName == "" {
			toolName = strings.TrimSpace(result.Envelope.ToolName)
		}
		if toolCallID == "" {
			toolCallID = strings.TrimSpace(result.Envelope.ToolCallID)
		}
	}
	diagnostic := toolresult.Diagnose(toolName, toolCallID, result.Error, metadata)

	payload[toolresult.MetadataOKKey] = diagnostic.OK

	outcome := toolresult.NormalizeOutcome(diagnostic.Outcome)
	if outcome == "" {
		if diagnostic.OK {
			outcome = toolresult.OutcomeSuccess
		} else {
			outcome = toolresult.OutcomeFailed
		}
	}
	payload[toolresult.MetadataOutcomeKey] = outcome

	if diagnostic.EmptyResult || outcome == toolresult.OutcomeEmpty {
		payload[toolresult.MetadataEmptyResultKey] = true
	}
	if code := strings.TrimSpace(diagnostic.ErrorCode); code != "" {
		payload[toolresult.MetadataErrorCodeKey] = code
	}
	if !diagnostic.OK {
		payload[toolresult.MetadataRetryableKey] = diagnostic.Retryable
	}
	if next := strings.TrimSpace(diagnostic.NextAction); next != "" {
		payload[toolresult.MetadataNextActionKey] = next
	}
	if diagnostic.RequestedCount > 0 {
		payload[toolresult.MetadataRequestedCountKey] = diagnostic.RequestedCount
	}
	if diagnostic.FailedCount > 0 {
		payload[toolresult.MetadataFailedCountKey] = diagnostic.FailedCount
	}
	if diagnostic.SucceededCount > 0 {
		payload[toolresult.MetadataSucceededCountKey] = diagnostic.SucceededCount
	}
	if diagnostic.PartialFailure || outcome == toolresult.OutcomePartial {
		payload[toolresult.MetadataPartialFailureKey] = true
	}

	items := diagnostic.FailedItems
	if len(items) == 0 {
		items = toolresult.ExtractFailedItems(metadata)
	}
	if len(items) > 0 {
		rows := make([]map[string]interface{}, 0, len(items))
		for _, item := range items {
			row := toolresult.FailedItemMap(item.Index, item.Path, item.Ref, item.Error)
			if row == nil {
				continue
			}
			rows = append(rows, row)
		}
		if len(rows) > 0 {
			payload[toolresult.MetadataFailedItemsKey] = rows
		}
	}

	// Recovery hints stay compact and generic when present.
	if candidates := diagnostic.PathCandidates; len(candidates) > 0 {
		payload[toolresult.MetadataPathCandidatesKey] = append([]string(nil), candidates...)
	} else if existing := toolresult.ExtractPathCandidates(metadata); len(existing) > 0 {
		payload[toolresult.MetadataPathCandidatesKey] = append([]string(nil), existing...)
	}
	if len(diagnostic.AttemptedArgs) > 0 {
		payload[toolresult.MetadataAttemptedArgsKey] = diagnostic.AttemptedArgs
	} else if existing := toolresult.ExtractAttemptedArgs(metadata); len(existing) > 0 {
		// Only surface attempted_args for recovery-relevant dispositions.
		if !diagnostic.OK || diagnostic.EmptyResult || outcome == toolresult.OutcomeEmpty || outcome == toolresult.OutcomePartial {
			payload[toolresult.MetadataAttemptedArgsKey] = existing
		}
	}
	// STALE recovery fields: prefer diagnostic (may rehydrate from error body),
	// then fall back to envelope metadata. Never overwrite values already set
	// by copyToolReliabilityMetadata unless they are empty.
	if path := strings.TrimSpace(diagnostic.FilePath); path != "" {
		if existing, _ := payload["file_path"].(string); strings.TrimSpace(existing) == "" {
			payload["file_path"] = path
		}
	}
	if diagnostic.SuggestedViewOffset != nil {
		if _, exists := payload["suggested_view_offset"]; !exists {
			payload["suggested_view_offset"] = *diagnostic.SuggestedViewOffset
		}
	}
	if diagnostic.SuggestedViewLimit != nil {
		if _, exists := payload["suggested_view_limit"]; !exists {
			payload["suggested_view_limit"] = *diagnostic.SuggestedViewLimit
		}
	}
	if snippet := diagnostic.CurrentSnippet; snippet != "" {
		if existing, _ := payload["current_snippet"].(string); existing == "" {
			payload["current_snippet"] = snippet
		}
	}
	if diagnostic.CurrentSnippetStartLine != nil {
		if _, exists := payload["current_snippet_start_line"]; !exists {
			payload["current_snippet_start_line"] = *diagnostic.CurrentSnippetStartLine
		}
	}
}

// attachProtocolResultToPayload nests a compact toolprotocol.Result wire view
// under "protocol_result". Flat disposition fields stay authoritative for
// existing chat-log / offline analyzers; this is additive for SSE/hosts/ACP.
func attachProtocolResultToPayload(payload map[string]interface{}, result toolExecutionResult) {
	if payload == nil {
		return
	}
	var metadata map[string]interface{}
	if result.Envelope != nil {
		metadata = result.Envelope.Metadata
	}
	toolName := firstNonEmptyToolRuntimeValue(result.Call.Name)
	toolCallID := firstNonEmptyToolRuntimeValue(result.Call.ID)
	if result.Envelope != nil {
		if toolName == "" {
			toolName = strings.TrimSpace(result.Envelope.ToolName)
		}
		if toolCallID == "" {
			toolCallID = strings.TrimSpace(result.Envelope.ToolCallID)
		}
	}
	// Truncate raw text for ResultFromParts summary/content derivation only;
	// EventMap omits full content blocks so the nested object stays compact.
	content := extractToolTextOutput(result.Output)
	if len(content) > 4096 {
		content = content[:4096]
	}
	wire := toolprotocol.ResultFromParts(toolName, toolCallID, content, result.Error, metadata)
	// todo_snapshot is tool-scoped: opt it in for this result instead of adding
	// it to the shared thin allowlist that every tool filters through.
	scopedMetadataKeys := make([]string, 0, 1)
	if strings.EqualFold(strings.TrimSpace(toolName), "todos") {
		attachTodoSnapshotToProtocolResult(wire, metadata)
		if wire.Metadata[todoSnapshotMetadataKey] != nil {
			scopedMetadataKeys = append(scopedMetadataKeys, todoSnapshotMetadataKey)
		}
	}
	payload["protocol_result"] = wire.EventMapWithMetadataKeys(scopedMetadataKeys...)
}

// todoSnapshotMetadataKey is the tool-scoped protocol_result.metadata key that
// carries the trimmed todos snapshot. It is deliberately kept out of
// toolprotocol's shared thin allowlist and opted in per result instead.
const todoSnapshotMetadataKey = "todo_snapshot"

// todoSnapshotItem is the frontend-facing projection of one todos tool item.
// The producer stores toolkit/tools.TodoItem values; this mirrors only the
// fields hosts render, so the agent package never imports the tool package.
type todoSnapshotItem struct {
	Content    string `json:"content"`
	Status     string `json:"status"`
	ActiveForm string `json:"active_form"`
}

// attachTodoSnapshotToProtocolResult adds a frontend-facing, trimmed copy of the
// todos tool snapshot onto protocol_result.metadata.todo_snapshot. It never mutates
// the producer metadata and only runs for the `todos` tool (tool-scoped whitelist).
//
// wire.Metadata is the cloned map built by toolprotocol.ResultFromParts, so
// writing there is local to the wire object and invisible to the payload map.
// The key is attached only when at least one valid item survives filtering.
//
// The producer keys are resolved through todoSnapshotSourceBag: the live agent
// path nests tool-authored metadata under "tool_metadata"
// (recordToolExecutionOutcome), while direct callers keep them flat.
func attachTodoSnapshotToProtocolResult(wire toolprotocol.Result, metadata map[string]interface{}) {
	if len(metadata) == 0 || wire.Metadata == nil {
		return
	}
	bag := todoSnapshotSourceBag(metadata)
	if len(bag) == 0 {
		return
	}
	raw, ok := bag["todos"]
	if !ok || raw == nil {
		return
	}
	// Convert structurally instead of importing the producer type: the snapshot
	// may arrive as []tools.TodoItem, []interface{} or JSON-decoded maps.
	encoded, err := json.Marshal(raw)
	if err != nil {
		return
	}
	var items []todoSnapshotItem
	if err := json.Unmarshal(encoded, &items); err != nil {
		return
	}
	rows := make([]map[string]interface{}, 0, len(items))
	for _, item := range items {
		content := strings.TrimSpace(item.Content)
		if content == "" {
			continue
		}
		status := normalizeTodoSnapshotStatus(item.Status)
		if status == "" {
			continue
		}
		rows = append(rows, map[string]interface{}{
			"content":     content,
			"status":      status,
			"active_form": strings.TrimSpace(item.ActiveForm),
		})
	}
	if len(rows) == 0 {
		return
	}
	snapshot := map[string]interface{}{"items": rows}
	if sessionID := todoSnapshotOwnerID(bag, metadata, "session_id"); sessionID != "" {
		snapshot["session_id"] = sessionID
	}
	if goalID := todoSnapshotOwnerID(bag, metadata, "goal_id"); goalID != "" {
		snapshot["goal_id"] = goalID
	}
	wire.Metadata[todoSnapshotMetadataKey] = snapshot
}

// todoSnapshotSourceBag returns the metadata bag that actually carries the todos
// tool result keys.
//
// The live execution path stores the tool result metadata under
// metadata["tool_metadata"] (see recordToolExecutionOutcome) instead of flattening
// it, so reading only the top level silently loses the snapshot and leaves the
// frontend task panel with just the text summary. Nested wins when it carries the
// key; flat stays supported for direct callers and older payloads.
func todoSnapshotSourceBag(metadata map[string]interface{}) map[string]interface{} {
	if nested, ok := metadata["tool_metadata"].(map[string]interface{}); ok && len(nested) > 0 {
		if _, hasTodos := nested["todos"]; hasTodos {
			return nested
		}
	}
	if _, hasTodos := metadata["todos"]; hasTodos {
		return metadata
	}
	return nil
}

// todoSnapshotOwnerID prefers the owner id from the resolved bag and falls back
// to the flat envelope metadata (nested bag may omit an id the host set flat).
func todoSnapshotOwnerID(bag, metadata map[string]interface{}, key string) string {
	if id := todoSnapshotMetadataString(bag[key]); id != "" {
		return id
	}
	if bag == nil {
		return ""
	}
	return todoSnapshotMetadataString(metadata[key])
}

// normalizeTodoSnapshotStatus canonicalizes a todo status, returning "" for
// anything outside the pending / in_progress / completed contract.
func normalizeTodoSnapshotStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "pending":
		return "pending"
	case "in_progress":
		return "in_progress"
	case "completed":
		return "completed"
	default:
		return ""
	}
}

// todoSnapshotMetadataString extracts a trimmed string owner id.
func todoSnapshotMetadataString(value interface{}) string {
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(text)
}

func summarizeToolMetadata(metadata map[string]interface{}) string {
	if len(metadata) == 0 {
		return ""
	}
	if summary := toolresult.MutationSummary(metadata); summary != "" {
		return summary
	}

	if fileCount, fileOK := intToolMetadataValue(metadata["file_count"]); fileOK {
		dirCount, dirOK := intToolMetadataValue(metadata["dir_count"])
		parts := []string{formatCountSummary(fileCount, "file", "files")}
		if dirOK {
			parts = append(parts, formatCountSummary(dirCount, "dir", "dirs"))
		}
		if truncatedToolMetadata(metadata["truncated"]) {
			parts = append(parts, "truncated")
		}
		return strings.Join(parts, ", ")
	}

	if matchCount, ok := intToolMetadataValue(metadata["match_count"]); ok {
		parts := []string{formatCountSummary(matchCount, "match", "matches")}
		if truncatedToolMetadata(metadata["truncated"]) {
			parts = append(parts, "truncated")
		}
		return strings.Join(parts, ", ")
	}

	if linesRead, ok := intToolMetadataValue(metadata["lines_read"]); ok {
		parts := []string{formatCountSummary(linesRead, "line", "lines")}
		if truncatedToolMetadata(metadata["is_truncated"]) {
			parts = append(parts, "truncated")
		}
		return strings.Join(parts, ", ")
	}

	if total, ok := intToolMetadataValue(metadata["total"]); ok {
		parts := []string{formatCountSummary(total, "item", "items")}
		if truncatedToolMetadata(metadata["truncated"]) {
			parts = append(parts, "truncated")
		}
		return strings.Join(parts, ", ")
	}

	return ""
}

func truncatedToolMetadata(value interface{}) bool {
	boolean, ok := value.(bool)
	return ok && boolean
}

func intToolMetadataValue(value interface{}) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int32:
		return int(typed), true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	default:
		return 0, false
	}
}

func formatCountSummary(count int, singular, plural string) string {
	if count == 1 {
		return fmt.Sprintf("1 %s", singular)
	}
	return fmt.Sprintf("%d %s", count, plural)
}

func extractToolTextOutput(value interface{}) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []byte:
		return string(typed)
	case fmt.Stringer:
		return typed.String()
	case *protocol.CallToolResult:
		return callToolResultText(typed)
	case []protocol.Content:
		return contentListText(typed)
	case *toolprotocol.Result:
		if typed == nil {
			return ""
		}
		return contentBlockListText(typed.Content)
	case []toolprotocol.ContentBlock:
		return contentBlockListText(typed)
	case map[string]interface{}:
		return mapContentText(typed)
	case []interface{}:
		return sliceContentText(typed)
	default:
		return ""
	}
}

// callToolResultText 提取 MCP CallToolResult 的文本内容（多个 text block 按序拼接）。
func callToolResultText(result *protocol.CallToolResult) string {
	if result == nil {
		return ""
	}
	return contentListText(result.Content)
}

// contentListText 拼接 MCP Content 列表中的 text 内容。
func contentListText(contents []protocol.Content) string {
	if len(contents) == 0 {
		return ""
	}
	var b strings.Builder
	for _, content := range contents {
		if content.Type == "text" {
			b.WriteString(content.Text)
		}
	}
	return b.String()
}

// contentBlockListText 拼接 toolprotocol ContentBlock 列表中的 text 内容。
func contentBlockListText(blocks []toolprotocol.ContentBlock) string {
	if len(blocks) == 0 {
		return ""
	}
	var b strings.Builder
	for _, block := range blocks {
		if block.Type == "text" {
			b.WriteString(block.Text)
		}
	}
	return b.String()
}

// mapContentText 从 JSON 解码形态的 MCP 结果（map 带 "content" 数组）中提取 text。
// 只识别内容块列表形状，避免从任意 map 中误取字段。
func mapContentText(m map[string]interface{}) string {
	if m == nil {
		return ""
	}
	content, ok := m["content"]
	if !ok {
		return ""
	}
	switch typed := content.(type) {
	case []interface{}:
		return sliceContentText(typed)
	case []protocol.Content:
		return contentListText(typed)
	}
	return ""
}

// sliceContentText 从 []interface{} 中提取 text 类型内容块。
func sliceContentText(items []interface{}) string {
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	for _, item := range items {
		switch typed := item.(type) {
		case map[string]interface{}:
			if t, _ := typed["type"].(string); t == "text" {
				if text, _ := typed["text"].(string); text != "" {
					b.WriteString(text)
				}
			}
		case protocol.Content:
			if typed.Type == "text" {
				b.WriteString(typed.Text)
			}
		case *protocol.Content:
			if typed != nil && typed.Type == "text" {
				b.WriteString(typed.Text)
			}
		}
	}
	return b.String()
}

func summarizeToolTextLines(text string, toolName string) []string {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
	if text == "" {
		return nil
	}

	lines := make([]string, 0, 6)
	textLimit := toolEventSummaryTextLimit(toolName)
	for _, rawLine := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(rawLine)
		if trimmed == "" {
			continue
		}
		if strings.EqualFold(trimmed, "Metadata:") {
			break
		}
		if strings.HasPrefix(trimmed, "artifact_refs:") {
			continue
		}
		normalized := truncateToolEventText(normalizeToolEventText(trimmed), textLimit)
		if normalized == "" {
			continue
		}
		if len(lines) == 0 || lines[len(lines)-1] != normalized {
			lines = append(lines, normalized)
		}
	}
	if len(lines) == 0 {
		return nil
	}
	if len(lines) == 1 {
		if split := splitToolSummaryLine(lines[0]); len(split) > 1 {
			lines = split
		}
	}
	return selectToolSummaryLines(lines, toolName)
}

func splitToolSummaryLine(line string) []string {
	replacements := []struct {
		old string
		new string
	}{
		{old: ". Fields:", new: ".\nFields:"},
		{old: ". Keys:", new: ".\nKeys:"},
		{old: ". Summary:", new: ".\nSummary:"},
	}
	for _, replacement := range replacements {
		line = strings.ReplaceAll(line, replacement.old, replacement.new)
	}

	segments := strings.Split(line, "\n")
	out := make([]string, 0, len(segments))
	for _, segment := range segments {
		for _, clause := range strings.Split(segment, ". ") {
			clause = truncateToolEventText(normalizeToolEventText(clause), 120)
			if clause == "" {
				continue
			}
			out = append(out, clause)
			if len(out) == 3 {
				return out
			}
		}
	}
	return out
}

func selectToolSummaryLines(lines []string, toolName string) []string {
	if len(lines) == 0 {
		return nil
	}
	if limit := toolEventSummaryLineLimit(toolName); limit > 3 {
		if len(lines) <= limit {
			return lines
		}
		return lines[:limit]
	}
	if looksLikeDirectorySummary(lines) {
		return buildDirectorySummaryLines(lines)
	}
	if len(lines) <= 3 {
		return lines
	}
	if looksLikeSummaryTrailer(lines[len(lines)-1]) {
		return []string{lines[0], lines[1], lines[len(lines)-1]}
	}
	return lines[:3]
}

func toolEventSummaryLineLimit(toolName string) int {
	if strings.EqualFold(strings.TrimSpace(toolName), "todos") {
		return 32
	}
	return 3
}

func toolEventSummaryTextLimit(toolName string) int {
	if strings.EqualFold(strings.TrimSpace(toolName), "todos") {
		return 240
	}
	return 120
}

func looksLikeDirectorySummary(lines []string) bool {
	return len(lines) >= 2 &&
		strings.HasPrefix(lines[0], "目录:") &&
		strings.HasPrefix(lines[len(lines)-1], "统计:")
}

func buildDirectorySummaryLines(lines []string) []string {
	if len(lines) <= 2 {
		return lines
	}

	middle := lines[1 : len(lines)-1]
	if len(middle) > 3 {
		middle = middle[:3]
	}

	summary := []string{lines[0]}
	if joined := truncateToolEventText(strings.Join(middle, " · "), 120); joined != "" {
		summary = append(summary, joined)
	}
	summary = append(summary, lines[len(lines)-1])
	return summary
}

func looksLikeSummaryTrailer(line string) bool {
	line = strings.TrimSpace(line)
	switch {
	case strings.HasPrefix(line, "统计:"):
		return true
	case strings.HasPrefix(line, "("):
		return true
	case strings.Contains(line, "截断"):
		return true
	case strings.Contains(strings.ToLower(line), "truncated"):
		return true
	default:
		return false
	}
}

func normalizeToolEventText(text string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
}

func truncateToolEventText(text string, limit int) string {
	text = normalizeToolEventText(text)
	if limit <= 0 {
		return text
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	if limit <= 3 {
		return string(runes[:limit])
	}
	return strings.TrimSpace(string(runes[:limit-3])) + "..."
}
