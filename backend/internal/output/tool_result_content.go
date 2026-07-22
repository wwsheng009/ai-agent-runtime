package output

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
)

const (
	modelToolTextByteBudget      = 12 * 1024
	modelToolTextMarkerReserve   = 160
	modelToolTextMinSegmentBytes = 1024
	modelArtifactNoticePrefix    = "Full raw output artifact: "
)

// RenderFullToolResultContent builds the full tool_result text that should be
// sent back to the model. It preserves the original tool output instead of the
// reduced envelope summary used for CLI/event rendering.
func RenderFullToolResultContent(content interface{}, toolErr string) string {
	rawText := strings.TrimSpace(stringify(content))
	toolErr = strings.TrimSpace(toolErr)

	switch {
	case rawText == "" && toolErr == "":
		return "Tool returned no output."
	case rawText == "":
		return "Tool execution failed: " + toolErr
	case toolErr == "":
		return rawText
	default:
		return "Tool execution failed: " + toolErr + "\n" + rawText
	}
}

// RenderToolResultContentForModel returns the content that should be written to
// tool_result messages sent back to the model. Internal text-like outputs keep
// full raw text when small, but large payloads are truncated before entering
// history. Structured outputs keep the reduced envelope summary so specialized
// reducers can preserve stable facts without dumping large JSON.
func RenderToolResultContentForModel(content interface{}, toolErr string, envelope *Envelope) string {
	body := renderToolResultBodyForModel(content, toolErr, envelope)
	effectiveErr := strings.TrimSpace(toolErr)
	if effectiveErr == "" && envelope != nil {
		effectiveErr = strings.TrimSpace(envelope.Error)
	}
	diagnostic := toolresult.Diagnose(envelopeToolName(envelope), envelopeToolCallID(envelope), effectiveErr, envelopeMetadata(envelope))
	if envelope != nil {
		if strings.TrimSpace(envelope.ErrorCode) != "" {
			diagnostic.ErrorCode = strings.TrimSpace(envelope.ErrorCode)
			diagnostic.Retryable = envelope.Retryable
		}
		if strings.TrimSpace(envelope.NextAction) != "" {
			diagnostic.NextAction = strings.TrimSpace(envelope.NextAction)
		}
	}
	return renderToolResultContract(body, diagnostic)
}

func renderToolResultBodyForModel(content interface{}, toolErr string, envelope *Envelope) string {
	if isEditingToolResult(envelope) {
		return renderToolTextForModelHistory(content, toolErr, envelope)
	}
	if isExternalMCPToolResult(envelope) {
		return renderToolTextForModelHistory(content, toolErr, envelope)
	}
	if isCollaborationResult(envelope) || isBackgroundTaskResult(envelope) {
		return renderToolTextForModelHistory(content, toolErr, envelope)
	}
	if modelSummaryPreferred(envelope) {
		return appendToolArtifactNotice(envelope.Render(), modelArtifactNotice(envelope))
	}
	if isTaskOutputToolResult(envelope) {
		return renderToolTextForModelHistory(content, toolErr, envelope)
	}
	if kind := toolResultKindForModel(content, envelope); kind != "" {
		switch kind {
		case toolresult.KindText, toolresult.KindEmpty:
			return renderToolTextForModelHistory(content, toolErr, envelope)
		case toolresult.KindStructured, toolresult.KindBinary:
			if envelope != nil {
				if summary := strings.TrimSpace(envelope.Render()); summary != "" {
					return summary
				}
			}
			return RenderFullToolResultContent(content, toolErr)
		}
	}
	if isTextLikeToolResult(content) {
		return renderToolTextForModelHistory(content, toolErr, envelope)
	}
	if envelope != nil {
		if summary := strings.TrimSpace(envelope.Render()); summary != "" {
			return summary
		}
	}
	return RenderFullToolResultContent(content, toolErr)
}

func envelopeToolName(envelope *Envelope) string {
	if envelope == nil {
		return ""
	}
	return envelope.ToolName
}

func envelopeToolCallID(envelope *Envelope) string {
	if envelope == nil {
		return ""
	}
	return envelope.ToolCallID
}

func renderToolResultContract(body string, diagnostic toolresult.Diagnostic) string {
	if diagnostic.OK {
		return body
	}
	type modelContract struct {
		OK         bool   `json:"ok"`
		ToolName   string `json:"tool_name,omitempty"`
		ToolCallID string `json:"tool_call_id,omitempty"`
		ErrorCode  string `json:"error_code,omitempty"`
		Retryable  *bool  `json:"retryable,omitempty"`
		NextAction string `json:"next_action,omitempty"`
	}
	contractValue := modelContract{
		OK:         diagnostic.OK,
		ToolName:   diagnostic.ToolName,
		ToolCallID: diagnostic.ToolCallID,
		ErrorCode:  diagnostic.ErrorCode,
		NextAction: diagnostic.NextAction,
	}
	if !diagnostic.OK {
		contractValue.Retryable = &diagnostic.Retryable
	}
	contract, err := json.Marshal(contractValue)
	if err != nil {
		return body
	}
	header := "Runtime tool result contract: " + string(contract)
	body = strings.TrimSpace(body)
	if body == "" {
		return header
	}
	separator := "\n\n"
	remaining := modelToolTextByteBudget - len(header) - len(separator)
	if remaining <= 0 {
		return safePrefixByBytes(header, modelToolTextByteBudget)
	}
	if len(body) > remaining {
		body = formatTruncatedToolTextForModel(body, remaining)
	}
	return header + separator + body
}

func modelSummaryPreferred(envelope *Envelope) bool {
	if envelope == nil || strings.TrimSpace(envelope.Summary) == "" {
		return false
	}
	preferred, _ := envelope.Metadata["model_summary_preferred"].(bool)
	return preferred
}

func isTaskOutputToolResult(envelope *Envelope) bool {
	if envelope == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(envelope.ToolName), "task_output")
}

func isCollaborationResult(envelope *Envelope) bool {
	if envelope == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(envelope.ToolName)) {
	case "wait_agent", "read_agent_events", "list_agents", "spawn_agent", "send_message", "followup_task",
		"send_input", "resolve_agent_approval", "close_agent", "resume_agent":
		return true
	default:
		return false
	}
}

func isBackgroundTaskResult(envelope *Envelope) bool {
	if envelope == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(envelope.ToolName)) {
	case "background_task":
		return true
	default:
		return false
	}
}

func isEditingToolResult(envelope *Envelope) bool {
	if envelope == nil {
		return false
	}
	switch strings.TrimSpace(envelope.ToolName) {
	case "edit", "apply_patch":
		return true
	default:
		return false
	}
}

func toolResultKindForModel(content interface{}, envelope *Envelope) string {
	if envelope != nil {
		if kind := toolresult.KindFromMetadata(envelope.Metadata); kind != "" {
			return kind
		}
	}
	if isTextLikeToolResult(content) {
		if strings.TrimSpace(stringify(content)) == "" {
			return toolresult.KindEmpty
		}
		return toolresult.KindText
	}
	if content != nil {
		return toolresult.KindStructured
	}
	return ""
}

func isExternalMCPToolResult(envelope *Envelope) bool {
	if envelope == nil {
		return false
	}
	mcpName := strings.TrimSpace(metadataString(envelope.Metadata, "mcp_name"))
	if mcpName == "" {
		return false
	}
	return !strings.EqualFold(mcpName, "toolkit")
}

func metadataString(metadata map[string]interface{}, key string) string {
	if len(metadata) == 0 {
		return ""
	}
	if value, ok := metadata[key].(string); ok {
		return value
	}
	if nested, ok := metadata["tool_metadata"].(map[string]interface{}); ok {
		if value, ok := nested[key].(string); ok {
			return value
		}
	}
	return ""
}

func isTextLikeToolResult(content interface{}) bool {
	switch content.(type) {
	case nil:
		return true
	case string:
		return true
	case []byte:
		return true
	case fmt.Stringer:
		return true
	default:
		return false
	}
}

func renderToolTextForModelHistory(content interface{}, toolErr string, envelope *Envelope) string {
	full := RenderFullToolResultContent(content, toolErr)
	if strings.TrimSpace(toolErr) == "" && strings.TrimSpace(stringify(content)) == "" {
		if summary := toolresult.MutationSummary(envelopeMetadata(envelope)); summary != "" {
			full = summary
		}
	}
	notice := modelArtifactNotice(envelope)
	if strings.TrimSpace(full) == "" {
		return appendToolArtifactNotice(full, notice)
	}
	withNotice := appendToolArtifactNotice(full, notice)
	if len(withNotice) <= modelToolTextByteBudget {
		return withNotice
	}
	if notice == "" {
		return formatTruncatedToolTextForModel(full, modelToolTextByteBudget)
	}
	bodyBudget := modelToolTextByteBudget - len(notice) - len("\n\n")
	if bodyBudget <= 0 {
		return safePrefixByBytes(notice, modelToolTextByteBudget)
	}
	return appendToolArtifactNotice(formatTruncatedToolTextForModel(full, bodyBudget), notice)
}

func envelopeMetadata(envelope *Envelope) map[string]interface{} {
	if envelope == nil {
		return nil
	}
	return envelope.Metadata
}

func modelArtifactNotice(envelope *Envelope) string {
	if envelope == nil {
		return ""
	}
	if artifactID := strings.TrimSpace(metadataString(envelope.Metadata, "artifact_id")); artifactID != "" {
		return "Full raw output artifact_id: " + artifactID
	}
	if len(envelope.ArtifactIDs) > 0 && strings.TrimSpace(envelope.ArtifactIDs[0]) != "" {
		return "Full raw output artifact_id: " + strings.TrimSpace(envelope.ArtifactIDs[0])
	}
	path := strings.TrimSpace(metadataString(envelope.Metadata, "raw_output_artifact_path"))
	if path != "" {
		return modelArtifactNoticePrefix + path
	}
	return ""
}

func appendToolArtifactNotice(body string, notice string) string {
	body = strings.TrimSpace(body)
	notice = strings.TrimSpace(notice)
	switch {
	case body == "":
		return notice
	case notice == "":
		return body
	default:
		return body + "\n\n" + notice
	}
}

func formatTruncatedToolTextForModel(content string, budget int) string {
	content = strings.TrimSpace(strings.ReplaceAll(content, "\r\n", "\n"))
	if content == "" || budget <= 0 || len(content) <= budget {
		return content
	}

	totalLines := countTextLines(content)
	totalBytes := len(content)
	header := fmt.Sprintf("Total output lines: %d\nTotal output bytes: %d\n\n", totalLines, totalBytes)

	headTailBudget := budget - len(header) - modelToolTextMarkerReserve
	if headTailBudget < modelToolTextMinSegmentBytes*2 {
		headTailBudget = modelToolTextMinSegmentBytes * 2
	}
	if headTailBudget >= totalBytes {
		headTailBudget = totalBytes - 1
	}
	if headTailBudget <= 0 {
		return safePrefixByBytes(content, budget)
	}

	headBudget := headTailBudget * 2 / 3
	tailBudget := headTailBudget - headBudget
	if headBudget < modelToolTextMinSegmentBytes {
		headBudget = modelToolTextMinSegmentBytes
		tailBudget = headTailBudget - headBudget
	}
	if tailBudget < modelToolTextMinSegmentBytes {
		tailBudget = modelToolTextMinSegmentBytes
		headBudget = headTailBudget - tailBudget
	}
	if headBudget <= 0 {
		headBudget = headTailBudget / 2
	}
	if tailBudget <= 0 {
		tailBudget = headTailBudget - headBudget
	}

	head := safePrefixByBytes(content, headBudget)
	tail := safeSuffixByBytes(content, tailBudget)
	if len(head)+len(tail) >= totalBytes {
		if budget <= modelToolTextMarkerReserve {
			return safePrefixByBytes(content, budget)
		}
		bodyBudget := budget - modelToolTextMarkerReserve
		if bodyBudget <= 0 {
			bodyBudget = budget
		}
		head = safePrefixByBytes(content, bodyBudget*2/3)
		tail = safeSuffixByBytes(content, bodyBudget/3)
	}

	omittedBytes := totalBytes - len(head) - len(tail)
	if omittedBytes < 0 {
		omittedBytes = 0
	}
	marker := fmt.Sprintf("\n\n[output truncated for history safety: omitted %d bytes from the middle]\n\n", omittedBytes)
	return header + head + marker + tail
}

func countTextLines(text string) int {
	if strings.TrimSpace(text) == "" {
		return 0
	}
	return strings.Count(text, "\n") + 1
}

func safePrefixByBytes(text string, maxBytes int) string {
	if maxBytes <= 0 || text == "" {
		return ""
	}
	if len(text) <= maxBytes {
		return text
	}
	index := 0
	for index < len(text) {
		_, size := utf8.DecodeRuneInString(text[index:])
		if size <= 0 || index+size > maxBytes {
			break
		}
		index += size
	}
	return text[:index]
}

func safeSuffixByBytes(text string, maxBytes int) string {
	if maxBytes <= 0 || text == "" {
		return ""
	}
	if len(text) <= maxBytes {
		return text
	}
	start := len(text)
	used := 0
	for start > 0 {
		_, size := utf8.DecodeLastRuneInString(text[:start])
		if size <= 0 || used+size > maxBytes {
			break
		}
		start -= size
		used += size
	}
	return text[start:]
}
