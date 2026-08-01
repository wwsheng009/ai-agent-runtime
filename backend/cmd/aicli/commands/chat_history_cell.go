package commands

import (
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	runtimechatcore "github.com/wwsheng009/ai-agent-runtime/internal/chatcore"
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
	historyCellCommand
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
	// internal blank rows kept, CR dropped) for the given output width. When
	// width > 0, long visual rows are broken with render.Wrap so the owned
	// viewport can reflow from source without immediate-mode padding.
	DisplayLines(width int) []string
}

// widthAwareDisplayLines turns a pre-styled block into writeLine-ready rows and,
// when width > 0, wraps each visual row with render.Wrap (grapheme-aware, CJK
// safe). width <= 0 keeps the legacy normalize-only path so existing
// commitHistoryCellLocked(..., 0) call sites stay byte-identical.
//
// Rows that already fit width are returned unchanged (no re-encode) so short
// content stays byte-identical to the pre-P5.4 normalizeWriteLines path.
func widthAwareDisplayLines(rendered string, width int) []string {
	lines := normalizeWriteLines(rendered)
	if width <= 0 || len(lines) == 0 {
		return lines
	}
	out := make([]string, 0, len(lines))
	backend := render.ANSIBackend{}
	for _, row := range lines {
		if row == "" {
			out = append(out, "")
			continue
		}
		parsed := render.ANSIToLines(row)
		needsWrap := false
		for _, line := range parsed {
			if render.LineWidth(line) > width {
				needsWrap = true
				break
			}
		}
		if !needsWrap {
			// Preserve original ANSI bytes when no wrap is required.
			out = append(out, row)
			continue
		}
		for _, line := range parsed {
			for _, wrapped := range render.Wrap(line, width, render.WrapOptions{BreakWord: true}) {
				encoded := backend.RenderLines(render.LinesDoc(wrapped))
				if len(encoded) == 0 {
					out = append(out, "")
					continue
				}
				out = append(out, encoded...)
			}
		}
	}
	return out
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
// pipeline, then applies width-aware wrap when width > 0.
func (c userMessageCell) DisplayLines(width int) []string {
	return widthAwareDisplayLines(ui.FormatUserMessage(c.source), width)
}

// assistantMessageCell renders a finalized assistant turn. It holds the
// formatter-rendered body (post-markdown ANSI); DisplayLines applies the shared
// assistant framing exactly like the legacy writeCompleteBlockLocked path, then
// width-aware wrap when width > 0.
type assistantMessageCell struct {
	body string
}

func newAssistantMessageCell(body string) assistantMessageCell {
	return assistantMessageCell{body: body}
}

func (assistantMessageCell) Kind() historyCellKind { return historyCellAssistant }

func (c assistantMessageCell) DisplayLines(width int) []string {
	return widthAwareDisplayLines(ui.FormatAssistantRendered(c.body), width)
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

func (c supplementLineCell) DisplayLines(width int) []string {
	return widthAwareDisplayLines(ui.FormatAssistantSupplementBlock(c.line), width)
}

// asyncDocumentCell renders a typed timeline/tool/info document. It keeps the
// structured render.Document as its source so a width-aware backend can re-emit
// it later; DisplayLines matches the legacy writeCompleteBlockLocked(RenderDocumentANSI)
// then applies width-aware wrap.
type asyncDocumentCell struct {
	doc render.Document
}

func newAsyncDocumentCell(doc render.Document) asyncDocumentCell {
	return asyncDocumentCell{doc: doc}
}

func (asyncDocumentCell) Kind() historyCellKind { return historyCellTool }

func (c asyncDocumentCell) DisplayLines(width int) []string {
	return widthAwareDisplayLines(ui.RenderDocumentANSI(c.doc), width)
}

// commandResultCell is the retained projection of one structured local command.
// Its identity is allocated once by the coordinator; all RenderBlocks from the
// command are already merged into doc, so one command produces one atomic cell.
type commandResultCell struct {
	id       string
	sequence uint64
	doc      render.Document
}

func newCommandResultCell(id string, sequence uint64, doc render.Document) commandResultCell {
	return commandResultCell{id: id, sequence: sequence, doc: doc}
}

func (commandResultCell) Kind() historyCellKind { return historyCellCommand }

func (c commandResultCell) DisplayLines(width int) []string {
	return widthAwareDisplayLines(ui.RenderDocumentANSI(c.doc), width)
}

// toolChainCell represents one tool invocation as a *single dense cell* in the
// owned viewport. Live Running is redrawn in place (no gap). When the tool
// completes, one final insertHistoryLines commits the Completed state (with
// leading empty line if needed). The cell holds the ChatEvent so DisplayLines
// reuses the compact transcript path byte-for-byte.
type toolChainCell struct {
	event runtimechatcore.ChatEvent
}

func newToolChainCell(name string, args map[string]interface{}, _ time.Time) toolChainCell {
	return toolChainCell{event: runtimechatcore.ChatEvent{
		Type:      runtimechatcore.EventTool,
		Stage:     "tool_requested",
		ToolName:  name,
		Arguments: args,
	}}
}

func newToolChainCellFromEvent(event runtimechatcore.ChatEvent) toolChainCell {
	return toolChainCell{event: event}
}

func (c toolChainCell) withCompleted(result string, metadata map[string]interface{}) toolChainCell {
	next := c.event
	next.Stage = "tool_result"
	next.Output = result
	if metadata != nil {
		next.Metadata = metadata
	}
	return toolChainCell{event: next}
}

func (toolChainCell) Kind() historyCellKind { return historyCellTool }

func (c toolChainCell) DisplayLines(width int) []string {
	line := renderSharedChatToolEvent(c.event)
	if strings.TrimSpace(line) == "" {
		return nil
	}
	return widthAwareDisplayLines(ui.FormatAssistantSupplementBlock(line), width)
}

// assistantStreamCell is the P5.4 unification of assistantTurnTranscript into
// the historyCell model. It holds the raw source so DisplayLines can re-render
// at any width (real line breaks) instead of immediate-mode padding.
//
// Live streaming still owns emission cursors on assistantTurnTranscript
// (EmittedEnd/EnqueuedEnd/Blocks); this cell is the retained-source view used
// when a turn is committed as history or reflowed at a new width. Immediate-
// mode divergence flags (EmittedDiverged / NeedsConsolidation) were removed in
// P5.4-S3; residualAfterEmittedPrefix remains the behavioral residual path.
type assistantStreamCell struct {
	source   string
	markdown bool
	// optional formatter for width-aware markdown reflow; nil falls back to plain.
	formatFn func(source string, width int) string
}

func newAssistantStreamCell(source string, markdown bool) assistantStreamCell {
	return assistantStreamCell{source: source, markdown: markdown}
}

func newAssistantStreamCellWithFormatter(source string, markdown bool, formatFn func(string, int) string) assistantStreamCell {
	return assistantStreamCell{source: source, markdown: markdown, formatFn: formatFn}
}

func (assistantStreamCell) Kind() historyCellKind { return historyCellAssistant }

func (c assistantStreamCell) DisplayLines(width int) []string {
	if c.source == "" {
		return nil
	}
	formatted := c.source
	if c.formatFn != nil {
		formatted = c.formatFn(c.source, width)
	} else if c.markdown {
		// No formatter supplied: keep source as-is (plain path parity).
		formatted = c.source
	}
	return widthAwareDisplayLines(ui.FormatAssistantRendered(formatted), width)
}
