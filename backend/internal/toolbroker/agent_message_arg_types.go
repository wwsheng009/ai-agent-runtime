package toolbroker

import "fmt"

// Argument kinds for the message tools that address an existing child session
// (send_message, followup_task, send_input).
//
// These tools read their keys with direct type assertions too. For send_input
// that is not harmless: `interrupt: "true"` (a string, as models often emit for
// boolean-looking flags) was dropped, so the call delivered a message that did
// not interrupt the child while the caller believed it had. A `target`/`id`
// given as a number was dropped as well, and the call then failed later with a
// less actionable "session" error instead of naming the offending argument.
//
// The message tools are not fail-closed on unknown keys, so this only checks the
// known keys and leaves other arguments alone.
var agentMessageToolStringKeys = map[string][]string{
	ToolSendMessage:  {"id", "message", "session_id", "target"},
	ToolFollowupTask: {"id", "message", "session_id", "target"},
	ToolSendInput:    {"id", "message", "session_id"},
}

func validateAgentMessageArgTypes(toolName string, args map[string]interface{}) error {
	for _, key := range agentMessageToolStringKeys[toolName] {
		value, exists := args[key]
		if !exists || value == nil {
			continue
		}
		if _, ok := value.(string); ok {
			continue
		}
		return fmt.Errorf("%s argument %q must be a %s, got %T (%v); omit it to use the runtime default",
			toolName, key, toolArgFieldString, value, value)
	}
	if toolName != ToolSendInput {
		return nil
	}
	if value, exists := args["interrupt"]; exists && value != nil {
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("%s argument %q must be a %s, got %T (%v); pass true to interrupt the child before delivering the prompt, or omit it",
				toolName, "interrupt", toolArgFieldBool, value, value)
		}
	}
	return nil
}
