package output

import (
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
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
// tool_result messages sent back to the model. Text-like outputs preserve the
// full raw text. Structured outputs keep the reduced envelope summary so
// specialized reducers can preserve stable facts without dumping large JSON.
func RenderToolResultContentForModel(content interface{}, toolErr string, envelope *Envelope) string {
	if isExternalMCPToolResult(envelope) {
		return RenderFullToolResultContent(content, toolErr)
	}
	if kind := toolResultKindForModel(content, envelope); kind != "" {
		switch kind {
		case toolresult.KindText, toolresult.KindEmpty:
			return RenderFullToolResultContent(content, toolErr)
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
		return RenderFullToolResultContent(content, toolErr)
	}
	if envelope != nil {
		if summary := strings.TrimSpace(envelope.Render()); summary != "" {
			return summary
		}
	}
	return RenderFullToolResultContent(content, toolErr)
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
