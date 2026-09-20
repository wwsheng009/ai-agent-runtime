package tools

import (
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
)

// Per-tool model-visible output budgets (bytes).
//
// Every tool listed here can produce a large text payload, so it owns its own
// window: it stops at its tool-specific budget, publishes its own truncation /
// continuation metadata, and stamps toolresult.MetadataSkipRenderTruncationKey
// so the render layer (L4) never folds the payload a second time with its
// one-size-fits-all budget.
//
// The numbers follow how the payload is consumed: reading code (view/grep) and
// paging raw archived output (artifact_read) benefits from a wide window, while
// path listings and fetched previews are scanning inputs and stay smaller. They
// are intentionally independent of output.ModelToolTextByteBudget, which stays
// the backstop for tools that do not own a budget.
const (
	// viewOutputBudgetBytes: one code window, large enough for a whole function
	// or file section without a second round trip.
	viewOutputBudgetBytes = 32 * 1024
	// grepOutputBudgetBytes: one match list; wide enough for a useful first
	// pass before the model narrows the pattern.
	grepOutputBudgetBytes = 32 * 1024
	// globOutputBudgetBytes: one path result set.
	globOutputBudgetBytes = 16 * 1024
	// lsOutputBudgetBytes: one directory tree listing.
	lsOutputBudgetBytes = 16 * 1024
	// fetchOutputBudgetBytes: one fetched document body.
	fetchOutputBudgetBytes = 32 * 1024
	// artifactOutputBudgetBytes: raw archived bytes served per artifact_read page.
	artifactOutputBudgetBytes = 32 * 1024
)

// stampToolOwnsOutput declares that a tool already shaped its own payload and
// owns its final window, so the render layer (L4) must not fold it again.
// Stamping is unconditional - independent of any "truncated"/"is_truncated"
// flag - because the tool's own budget decides the payload shape on every path,
// including complete results that happen to be large.
func stampToolOwnsOutput(result *toolkit.ToolResult) *toolkit.ToolResult {
	if result == nil {
		return nil
	}
	if result.Metadata == nil {
		result.Metadata = map[string]interface{}{}
	}
	result.Metadata[toolresult.MetadataSkipRenderTruncationKey] = true
	return result
}
