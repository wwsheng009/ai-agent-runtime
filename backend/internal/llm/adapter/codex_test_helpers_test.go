package adapter

// convertMessagesToCodexInput is the test-only convenience form of the
// option-aware production entry point.
func (a *CodexAdapter) convertMessagesToCodexInput(messages []map[string]interface{}) []map[string]interface{} {
	return a.convertMessagesToCodexInputWithOptions(messages, codexInputOptions{})
}
