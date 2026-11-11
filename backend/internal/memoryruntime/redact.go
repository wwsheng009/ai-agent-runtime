package memoryruntime

import "github.com/wwsheng009/ai-agent-runtime/internal/memoryredact"

func DetectSecret(text string) string {
	return memoryredact.DetectSecret(text)
}

// RedactForPrompt removes secret-shaped spans before the transcript reaches an
// extraction model. It returns only rule names, never raw secret values.
func RedactForPrompt(text string) (string, []string) {
	return memoryredact.Redact(text)
}

func containsInstructionLikeMemory(text string) bool {
	return memoryredact.ContainsInstructionLikeText(text)
}
