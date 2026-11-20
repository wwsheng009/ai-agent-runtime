package adapter

import (
	"fmt"
	"strings"
)

// ValidateRequest checks the tool-call trace after it has been converted to the
// exact Responses input shape used by BuildRequest. BuildRequest intentionally
// keeps its historical, error-free interface; request boundaries should call
// this method before building or sending a Codex request.
func (a *CodexAdapter) ValidateRequest(config RequestConfig) error {
	stripReasoningItems, _ := requestMetadataBool(config.Metadata, codexStripReasoningClientStateMetadataKey)
	_, input := a.buildCodexInstructionsAndInput(config.Messages, stripReasoningItems)
	return ValidateCodexToolCallTrace(input)
}

// ValidateCodexToolCallTrace verifies that every Responses tool call has
// exactly one matching output of the corresponding kind. It deliberately
// validates the final input rather than the source chat messages, because
// replay items and converted chat-style calls share the same wire contract.
func ValidateCodexToolCallTrace(input []map[string]interface{}) error {
	calls := make(map[string]string)
	outputs := make(map[string]string)

	for index, item := range input {
		if len(item) == 0 {
			return fmt.Errorf("codex_tool_trace_invalid: input[%d] is empty", index)
		}

		itemType := strings.ToLower(strings.TrimSpace(asCodexString(item["type"])))
		switch itemType {
		case "function_call", "custom_tool_call":
			callID := strings.TrimSpace(asCodexString(item["call_id"]))
			if callID == "" {
				return fmt.Errorf("codex_tool_trace_invalid: %s input[%d] is missing call_id", itemType, index)
			}
			if previous, exists := calls[callID]; exists {
				return fmt.Errorf("codex_tool_trace_invalid: duplicate tool call_id %q (%s and %s)", callID, previous, itemType)
			}
			calls[callID] = itemType

		case "function_call_output", "custom_tool_call_output":
			callID := strings.TrimSpace(asCodexString(item["call_id"]))
			if callID == "" {
				return fmt.Errorf("codex_tool_trace_invalid: %s input[%d] is missing call_id", itemType, index)
			}
			if previous, exists := outputs[callID]; exists {
				return fmt.Errorf("codex_tool_trace_invalid: duplicate tool output call_id %q (%s and %s)", callID, previous, itemType)
			}
			outputs[callID] = itemType
		}
	}

	for callID, callType := range calls {
		outputType, exists := outputs[callID]
		if !exists {
			return fmt.Errorf("tool_output_missing: A tool call is missing its matching output (call_id=%q)", callID)
		}
		expectedOutputType := "function_call_output"
		if callType == "custom_tool_call" {
			expectedOutputType = "custom_tool_call_output"
		}
		if outputType != expectedOutputType {
			return fmt.Errorf("codex_tool_trace_invalid: tool call_id %q has type %s but output has type %s", callID, callType, outputType)
		}
	}

	for callID := range outputs {
		if _, exists := calls[callID]; !exists {
			return fmt.Errorf("codex_tool_trace_invalid: output call_id %q has no matching tool call", callID)
		}
	}

	return nil
}
