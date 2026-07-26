package toolprotocol

import (
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
)

// OutputKind is the content shape of a tool result payload.
type OutputKind string

const (
	OutputKindText       OutputKind = toolresult.KindText
	OutputKindStructured OutputKind = toolresult.KindStructured
	OutputKindBinary     OutputKind = toolresult.KindBinary
	OutputKindEmpty      OutputKind = toolresult.KindEmpty
)

// NormalizeOutputKind mirrors toolresult.NormalizeKind.
func NormalizeOutputKind(value string) OutputKind {
	return OutputKind(toolresult.NormalizeKind(value))
}

// ContentBlock is one portable content unit in a tool result.
// Type is text | structured | binary | resource (MCP-compatible subset).
type ContentBlock struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	MIMEType string `json:"mime_type,omitempty"`
	URI      string `json:"uri,omitempty"`
}

// Result is the stable tool execution result wire object.
// It unifies toolkit/broker/MCP outcomes for SSE, CLI, and future ACP hosts.
type Result struct {
	ToolID     ToolID                 `json:"tool_id"`
	CallID     CallID                 `json:"call_id,omitempty"`
	OK         bool                   `json:"ok"`
	Outcome    string                 `json:"outcome,omitempty"`
	OutputKind OutputKind             `json:"output_kind,omitempty"`
	Content    []ContentBlock         `json:"content,omitempty"`
	Summary    string                 `json:"summary,omitempty"`
	Error      *Error                 `json:"error,omitempty"`
	Metadata   map[string]interface{} `json:"metadata,omitempty"`
	Source     string                 `json:"source,omitempty"`
}

// ResultFromParts builds a Result from common agent/gateway fields.
func ResultFromParts(toolName, toolCallID, content, toolErr string, metadata map[string]interface{}) Result {
	diag := toolresult.Diagnose(toolName, toolCallID, toolErr, metadata)
	result := Result{
		ToolID:   ToolID(strings.TrimSpace(toolName)),
		CallID:   NormalizeCallID(toolCallID),
		OK:       diag.OK,
		Outcome:  toolresult.NormalizeOutcome(diag.Outcome),
		Metadata: cloneMap(metadata),
		Source:   toolresult.SourceFromMetadata(metadata),
	}
	if result.Outcome == "" {
		if diag.OK {
			if diag.EmptyResult {
				result.Outcome = toolresult.OutcomeEmpty
			} else {
				result.Outcome = toolresult.OutcomeSuccess
			}
		} else {
			result.Outcome = toolresult.OutcomeFailed
		}
	}
	if kind := toolresult.KindFromMetadata(metadata); kind != "" {
		result.OutputKind = OutputKind(kind)
	} else if strings.TrimSpace(content) == "" && diag.OK {
		result.OutputKind = OutputKindEmpty
	} else if strings.TrimSpace(content) != "" {
		result.OutputKind = OutputKindText
	}
	if text := strings.TrimSpace(content); text != "" {
		result.Content = []ContentBlock{{Type: "text", Text: content}}
		result.Summary = truncateSummary(text, 240)
	}
	if wireErr := ErrorFromDiagnostic(diag, toolErr); wireErr != nil {
		result.Error = wireErr
		result.OK = false
		if result.Outcome == "" || result.Outcome == toolresult.OutcomeSuccess {
			result.Outcome = toolresult.OutcomeFailed
		}
	}
	return result
}

// Map returns a JSON-friendly full wire representation of Result.
// Prefer EventMap for tool.completed payloads to avoid large content blobs.
func (r Result) Map() map[string]interface{} {
	out := r.EventMap()
	if len(r.Content) > 0 {
		blocks := make([]map[string]interface{}, 0, len(r.Content))
		for _, block := range r.Content {
			item := map[string]interface{}{"type": firstNonEmpty(block.Type, "text")}
			if text := strings.TrimSpace(block.Text); text != "" {
				item["text"] = block.Text
			}
			if data := strings.TrimSpace(block.Data); data != "" {
				item["data"] = block.Data
			}
			if mime := strings.TrimSpace(block.MIMEType); mime != "" {
				item["mime_type"] = mime
			}
			if uri := strings.TrimSpace(block.URI); uri != "" {
				item["uri"] = uri
			}
			blocks = append(blocks, item)
		}
		out["content"] = blocks
	}
	return out
}

// EventMap returns a compact wire view suitable for runtime event payloads.
// Full content blocks are omitted; Summary is retained when present.
func (r Result) EventMap() map[string]interface{} {
	out := map[string]interface{}{
		"ok": r.OK,
	}
	if toolID := strings.TrimSpace(r.ToolID.String()); toolID != "" {
		out["tool_id"] = toolID
	}
	if callID := strings.TrimSpace(r.CallID.String()); callID != "" {
		out["call_id"] = callID
	}
	if outcome := strings.TrimSpace(r.Outcome); outcome != "" {
		out["outcome"] = outcome
	}
	if kind := strings.TrimSpace(string(r.OutputKind)); kind != "" {
		out["output_kind"] = kind
	}
	if summary := strings.TrimSpace(r.Summary); summary != "" {
		out["summary"] = summary
	}
	if source := strings.TrimSpace(r.Source); source != "" {
		out["source"] = source
	}
	if r.Error != nil {
		errMap := map[string]interface{}{}
		if code := strings.TrimSpace(string(r.Error.Code)); code != "" {
			errMap["code"] = code
		}
		if msg := strings.TrimSpace(r.Error.Message); msg != "" {
			errMap["message"] = msg
		}
		if r.Error.Retryable {
			errMap["retryable"] = true
		}
		if next := strings.TrimSpace(r.Error.NextAction); next != "" {
			errMap["next_action"] = next
		}
		if len(r.Error.Data) > 0 {
			errMap["data"] = cloneMap(r.Error.Data)
		}
		if len(errMap) > 0 {
			out["error"] = errMap
		}
	}
	if len(r.Metadata) > 0 {
		// Keep metadata thin: only disposition / recovery keys that hosts need.
		// Full metadata remains on the flat tool.completed payload.
		if thin := thinEventMetadata(r.Metadata); len(thin) > 0 {
			out["metadata"] = thin
		}
	}
	return out
}

func thinEventMetadata(metadata map[string]interface{}) map[string]interface{} {
	if len(metadata) == 0 {
		return nil
	}
	keys := []string{
		toolresult.MetadataOutcomeKey,
		toolresult.MetadataEmptyResultKey,
		toolresult.MetadataPartialFailureKey,
		toolresult.MetadataErrorCodeKey,
		toolresult.MetadataRetryableKey,
		toolresult.MetadataNextActionKey,
		toolresult.MetadataRequestedCountKey,
		toolresult.MetadataFailedCountKey,
		toolresult.MetadataSucceededCountKey,
		toolresult.MetadataFailedItemsKey,
		toolresult.MetadataPathCandidatesKey,
		toolresult.SourceKey,
	}
	out := make(map[string]interface{}, len(keys))
	for _, key := range keys {
		if value, ok := metadata[key]; ok && value != nil {
			out[key] = value
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// TextContent joins text content blocks for model/history rendering.
func (r Result) TextContent() string {
	if len(r.Content) == 0 {
		return ""
	}
	parts := make([]string, 0, len(r.Content))
	for _, block := range r.Content {
		if block.Type == "text" || block.Type == "" {
			if text := strings.TrimSpace(block.Text); text != "" {
				parts = append(parts, block.Text)
			}
		}
	}
	return strings.Join(parts, "\n")
}

func truncateSummary(text string, limit int) string {
	text = strings.TrimSpace(text)
	if limit <= 0 || len(text) <= limit {
		return text
	}
	if limit < 4 {
		return text[:limit]
	}
	return text[:limit-3] + "..."
}

func cloneMap(input map[string]interface{}) map[string]interface{} {
	if len(input) == 0 {
		return nil
	}
	cloned := make(map[string]interface{}, len(input))
	for key, value := range input {
		cloned[key] = value
	}
	return cloned
}
