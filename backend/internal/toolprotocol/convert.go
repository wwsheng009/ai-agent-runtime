package toolprotocol

import (
	"encoding/base64"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
)

// FromToolkitResult converts a toolkit.ToolResult into the protocol Result wire.
// toolName/toolCallID may be empty when not known at the toolkit boundary.
func FromToolkitResult(toolName, toolCallID string, result *toolkit.ToolResult) Result {
	if result == nil {
		return Result{
			ToolID:     ToolID(strings.TrimSpace(toolName)),
			CallID:     NormalizeCallID(toolCallID),
			OK:         false,
			Outcome:    toolresult.OutcomeFailed,
			OutputKind: OutputKindEmpty,
			Error: &Error{
				Code:    ErrorCodeExecution,
				Message: "nil tool result",
			},
		}
	}

	metadata := result.MetadataWithOutputKind()
	toolErr := ""
	if result.Error != nil {
		toolErr = result.Error.Error()
	}
	if !result.Success && toolErr == "" {
		toolErr = "tool execution failed"
	}

	content := strings.TrimSpace(result.Content)
	wire := ResultFromParts(toolName, toolCallID, content, toolErr, metadata)
	wire.OutputKind = OutputKind(result.NormalizedOutputKind())
	wire.Source = firstNonEmpty(wire.Source, toolresult.SourceToolkit)

	if len(result.Data) > 0 {
		block := ContentBlock{
			Type:     "binary",
			Data:     base64.StdEncoding.EncodeToString(result.Data),
			MIMEType: strings.TrimSpace(result.MIMEType),
		}
		if wire.Content == nil {
			wire.Content = []ContentBlock{block}
		} else {
			wire.Content = append(wire.Content, block)
		}
		if wire.OutputKind == "" || wire.OutputKind == OutputKindText || wire.OutputKind == OutputKindEmpty {
			wire.OutputKind = OutputKindBinary
		}
	} else if content != "" && len(wire.Content) == 0 {
		wire.Content = []ContentBlock{{Type: "text", Text: result.Content}}
	}

	if wire.OK && result.Success && wire.Outcome == "" {
		wire.Outcome = toolresult.OutcomeSuccess
	}
	if !result.Success {
		wire.OK = false
		if wire.Outcome == "" || wire.Outcome == toolresult.OutcomeSuccess {
			wire.Outcome = toolresult.OutcomeFailed
		}
	}
	return wire
}

// ToToolkitResult converts a protocol Result back to toolkit.ToolResult.
// Round-trip is best-effort: binary data is base64-decoded when present.
func ToToolkitResult(result Result) *toolkit.ToolResult {
	out := &toolkit.ToolResult{
		Success:    result.OK && (result.Error == nil),
		OutputKind: string(NormalizeOutputKind(string(result.OutputKind))),
		Content:    result.TextContent(),
		Metadata:   cloneMap(result.Metadata),
	}
	if out.OutputKind == "" {
		if strings.TrimSpace(out.Content) == "" && len(result.Content) == 0 {
			out.OutputKind = toolresult.KindEmpty
		} else {
			out.OutputKind = toolresult.KindText
		}
	}
	for _, block := range result.Content {
		switch strings.ToLower(strings.TrimSpace(block.Type)) {
		case "binary", "image":
			if raw := strings.TrimSpace(block.Data); raw != "" {
				if decoded, err := base64.StdEncoding.DecodeString(raw); err == nil {
					out.Data = decoded
					out.MIMEType = strings.TrimSpace(block.MIMEType)
					if out.OutputKind == "" || out.OutputKind == toolresult.KindText || out.OutputKind == toolresult.KindEmpty {
						out.OutputKind = toolresult.KindBinary
					}
				}
			}
		}
	}
	if result.Error != nil {
		out.Success = false
		msg := strings.TrimSpace(result.Error.Message)
		if msg == "" {
			msg = "tool execution failed"
		}
		out.Error = errorString(msg)
		if out.Metadata == nil {
			out.Metadata = map[string]interface{}{}
		}
		if result.Error.Code != "" {
			out.Metadata[toolresult.MetadataErrorCodeKey] = string(result.Error.Code)
		}
		if result.Error.NextAction != "" {
			out.Metadata[toolresult.MetadataNextActionKey] = result.Error.NextAction
		}
		out.Metadata[toolresult.MetadataRetryableKey] = result.Error.Retryable
	}
	if result.Source != "" {
		out.Metadata = toolresult.WithSource(out.Metadata, result.Source)
	}
	if result.Outcome != "" {
		if out.Metadata == nil {
			out.Metadata = map[string]interface{}{}
		}
		out.Metadata[toolresult.MetadataOutcomeKey] = result.Outcome
	}
	return out
}

type errorString string

func (e errorString) Error() string { return string(e) }

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
