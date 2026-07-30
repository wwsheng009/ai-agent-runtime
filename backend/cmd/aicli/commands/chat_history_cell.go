package commands

import (
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
)

// historyCellKind classifies a transcript block. Kind will drive tool-chain
// merging and spacing as the cell model grows (P3b/P4); today it is carried so
// the transcript has a single, inspectable data model that mirrors Codex's typed
// HistoryCell.
type historyCellKind int

const (
	historyCellUser historyCellKind = iota
	historyCellAssistant
	historyCellTool
	historyCellSupplement
	historyCellReasoning
	historyCellHeader
)

// historyCell is the retained-source view of one transcript block. It is the Go
// counterpart of Codex's HistoryCell trait (display_lines / kind): the concrete
// type keeps its own source, so the same block can be re-rendered at a new width
// (resize reflow, P4.3) instead of mutating already-committed scrollback.
//
// Introduced incrementally: P4.1 routes the user-echo block through it; later
// sub-steps migrate assistant/tool/supplement blocks and add a retained
// transcript store plus width-aware reflow.
type historyCell interface {
	Kind() historyCellKind
	// DisplayLines returns writeLine-ready rows (block terminator stripped,
	// internal blank rows kept, CR dropped) for the given output width. width is
	// reserved for wrap-aware cells; source-only cells that do not wrap here may
	// ignore it.
	DisplayLines(width int) []string
}

// userMessageCell renders a submitted or replayed user message. Its source is
// the raw user input, so the rendered rows can be rebuilt at any width.
type userMessageCell struct {
	source string
}

func newUserMessageCell(source string) userMessageCell {
	return userMessageCell{source: source}
}

func (userMessageCell) Kind() historyCellKind { return historyCellUser }

// DisplayLines mirrors the legacy writeCompleteBlockLocked(FormatUserMessage)
// pipeline exactly: normalizeWriteLines turns the pre-styled block into
// writeLine-ready rows. User messages are not wrapped at this layer, so width is
// ignored for now.
func (c userMessageCell) DisplayLines(int) []string {
	return normalizeWriteLines(ui.FormatUserMessage(c.source))
}

// assistantMessageCell renders a finalized assistant turn. It holds the
// formatter-rendered body (post-markdown ANSI); DisplayLines applies the shared
// assistant framing exactly like the legacy writeCompleteBlockLocked path.
// Re-rendering the raw markdown source at a new width is deferred to P4.3.
type assistantMessageCell struct {
	body string
}

func newAssistantMessageCell(body string) assistantMessageCell {
	return assistantMessageCell{body: body}
}

func (assistantMessageCell) Kind() historyCellKind { return historyCellAssistant }

func (c assistantMessageCell) DisplayLines(int) []string {
	return normalizeWriteLines(ui.FormatAssistantRendered(c.body))
}

// supplementLineCell renders one async/supplement line (tool feedback, warnings,
// reasoning summaries, team/system notices). It mirrors the legacy
// writeCompleteBlockLocked(FormatAssistantSupplementBlock) pipeline.
type supplementLineCell struct {
	line string
}

func newSupplementLineCell(line string) supplementLineCell {
	return supplementLineCell{line: line}
}

func (supplementLineCell) Kind() historyCellKind { return historyCellSupplement }

func (c supplementLineCell) DisplayLines(int) []string {
	return normalizeWriteLines(ui.FormatAssistantSupplementBlock(c.line))
}

// asyncDocumentCell renders a typed timeline/tool/info document. It keeps the
// structured render.Document as its source so a width-aware backend can re-emit
// it later; DisplayLines matches the legacy writeCompleteBlockLocked(RenderDocumentANSI).
type asyncDocumentCell struct {
	doc render.Document
}

func newAsyncDocumentCell(doc render.Document) asyncDocumentCell {
	return asyncDocumentCell{doc: doc}
}

func (asyncDocumentCell) Kind() historyCellKind { return historyCellTool }

func (c asyncDocumentCell) DisplayLines(int) []string {
	return normalizeWriteLines(ui.RenderDocumentANSI(c.doc))
}
