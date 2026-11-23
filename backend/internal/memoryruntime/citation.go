package memoryruntime

import (
	"context"

	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
)

type CitationResult = memorystore.CitationResult
type AnswerExposureInput = memorystore.AnswerExposureInput

// ParseMemoryCitations strips hidden citation blocks from the user-visible
// answer and extracts only explicitly cited ids that are present in the
// caller-provided allowlist.
func ParseMemoryCitations(answer string, allowedIDs []string) CitationResult {
	return memorystore.ParseMemoryCitations(answer, allowedIDs)
}

// RecordAnswerExposure applies the citation contract without blocking the
// answer path. Ordinary cited/read ids update ordinary memory usage; Core IDs
// are routed to their dedicated usage ledger. Other allowed injected ids are
// recorded as injected_only exposure. When parsing fails, every injected ID is
// recorded as injected_only.
func RecordAnswerExposure(ctx context.Context, store *memorystore.Store, input AnswerExposureInput) (CitationResult, error) {
	if store == nil {
		return CitationResult{CleanText: input.Answer}, nil
	}
	return store.RecordAnswerExposure(ctx, input)
}
