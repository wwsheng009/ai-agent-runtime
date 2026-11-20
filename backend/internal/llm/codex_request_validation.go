package llm

import "github.com/wwsheng009/ai-agent-runtime/internal/llm/adapter"

// validateCodexRequest checks the exact Responses input that the Codex adapter
// will build, without changing the ProtocolAdapter interface or its
// error-free BuildRequest contract.
func validateCodexRequest(protocolAdapter adapter.ProtocolAdapter, request adapter.RequestConfig) error {
	codexAdapter, ok := protocolAdapter.(*adapter.CodexAdapter)
	if !ok {
		return nil
	}
	return codexAdapter.ValidateRequest(request)
}
