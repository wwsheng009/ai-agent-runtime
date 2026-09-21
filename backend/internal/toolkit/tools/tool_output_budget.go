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
	// shellOutputBudgetBytes: one shell command's model-visible window.
	//
	// Shell output is the one large payload whose *full* text must survive into
	// the archive (there is no offset/limit continuation for a command; the
	// omitted tail is recovered with artifact_read). The shell therefore keeps
	// its captured output intact, declares this budget via
	// toolresult.MetadataModelVisibleBudgetKey, and lets the render layer (L4)
	// fold head-only beyond it. Declaring the budget (instead of folding here)
	// is what keeps the archived record complete: the gateway archives the
	// tool's content before the render layer folds it.
	shellOutputBudgetBytes = 32 * 1024
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

// stampToolOwnsOutputWithBudget stamps a tool that owns its window AND declares
// that window as its model-visible budget.
//
// The declared window is what keeps the rest of the pipeline aligned with the
// tool's real contract: the gateway's archive tiering floor (a result the model
// can read end to end needs no archived record) and the render-layer fold
// budget both follow it, so neither falls back to a fraction of, or the whole,
// one-size-fits-all backstop.
func stampToolOwnsOutputWithBudget(result *toolkit.ToolResult, bytes int) *toolkit.ToolResult {
	return declareModelVisibleBudget(stampToolOwnsOutput(result), bytes)
}

// declareModelVisibleBudget attaches a declared model-visible budget to a
// result without folding its content.
//
// Unlike stampToolOwnsOutput this does NOT opt out of render-layer folding: the
// tool keeps its payload intact (so the archive and artifact_read keep the full
// output) and the render layer folds head-only at the declared budget, emitting
// the raw-output pointer for the omitted tail. Used by shell output, where the
// capture limit bounds memory but the model window is the shell's own budget.
func declareModelVisibleBudget(result *toolkit.ToolResult, bytes int) *toolkit.ToolResult {
	if result == nil || bytes <= 0 {
		return result
	}
	if result.Metadata == nil {
		result.Metadata = map[string]interface{}{}
	}
	result.Metadata[toolresult.MetadataModelVisibleBudgetKey] = bytes
	return result
}
