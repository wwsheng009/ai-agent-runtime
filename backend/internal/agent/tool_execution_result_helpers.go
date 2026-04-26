package agent

// recordToolExecutionOutcome preserves tool output/metadata even when the tool
// also returns an execution error. This keeps stderr-like diagnostic text
// available to runtime events, chat history, and model-visible tool messages.
func recordToolExecutionOutcome(result *toolExecutionResult, metadata map[string]interface{}, rawOutput interface{}, rawMeta map[string]interface{}, execErr error) {
	if result == nil {
		return
	}
	result.Output = rawOutput
	if len(rawMeta) > 0 && metadata != nil {
		metadata["tool_metadata"] = cloneInterfaceMap(rawMeta)
	}
	if execErr != nil {
		result.Error = execErr.Error()
	}
}
