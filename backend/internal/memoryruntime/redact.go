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

// secretBlocksMemory reports whether a record must be excluded from
// extraction/consolidation because it still contains a secret-shaped value.
// An explicit redaction.enabled=false disables this filter; the safe
// zero-value Config keeps it enabled.
func secretBlocksMemory(cfg Config, text string) bool {
	if cfg.RedactionConfigured && !cfg.RedactionEnabled {
		return false
	}
	return memoryredact.DetectSecret(text) != ""
}
