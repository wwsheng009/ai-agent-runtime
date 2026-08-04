package commands

import (
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render/encoding"
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
	// ID 返回信息块身份。编码器接入前由构造器分配过渡 ID（cell-N）；
	// 终态由统一编码器分配（Item.ID，见 render-model-spec §5.1）。
	ID() string
	// Seq 返回提交序号（单调，仅追加语义）。
	Seq() uint64
	// Status 返回信息块生命周期状态（pending/running/completed/failed/canceled）。
	Status() encoding.ItemStatus
	// CauseID 返回父信息块身份（并行工具输出 → 工具调用；空表示无父）。
	CauseID() string
	// DisplayLines returns writeLine-ready rows (block terminator stripped,
	// internal blank rows kept, CR dropped) for the given output width. When
	// width > 0, long visual rows are broken with render.Wrap so the owned
	// viewport can reflow from source without immediate-mode padding.
	DisplayLines(width int) []string
}

// cellIdentity 是 historyCell 的过渡身份（render-model-spec §5.1）：
// 编码器接入前由构造器分配临时 ID/Seq（cell-N），终态由统一编码器
// 分配 Item.ID/Seq/CauseID。嵌入后方法提升到外层 cell 类型。
type cellIdentity struct {
	id      string
	seq     uint64
	status  encoding.ItemStatus
	causeID string
}

func (c cellIdentity) ID() string                  { return c.id }
func (c cellIdentity) Seq() uint64                 { return c.seq }
func (c cellIdentity) Status() encoding.ItemStatus { return c.status }
func (c cellIdentity) CauseID() string             { return c.causeID }

// withID 覆盖身份（commandResultCell 使用 coordinator 分配的 id/sequence）。
func (c cellIdentity) withID(id string, seq uint64) cellIdentity {
	c.id = id
	c.seq = seq
	return c
}

// withStatus 覆盖状态（可变 cell：toolChainCell running → completed）。
func (c cellIdentity) withStatus(status encoding.ItemStatus) cellIdentity {
	c.status = status
	return c
}

var cellIdentityCounter atomic.Uint64

// newCellIdentity 分配过渡身份：ID 为 cell-N（全局单调），Seq 同源递增。
// 默认状态 completed（已提交历史的 cell 均为终态；可变 cell 由构造器覆盖）。
func newCellIdentity(causeID string) cellIdentity {
	n := cellIdentityCounter.Add(1)
	return cellIdentity{
		id:      fmt.Sprintf("cell-%d", n),
		seq:     n,
		status:  encoding.StatusCompleted,
		causeID: causeID,
	}
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
	cellIdentity
	source string
}

func newUserMessageCell(source string) userMessageCell {
	return userMessageCell{cellIdentity: newCellIdentity(""), source: source}
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
	cellIdentity
	body string
}

func newAssistantMessageCell(body string) assistantMessageCell {
	return assistantMessageCell{cellIdentity: newCellIdentity(""), body: body}
}

func (assistantMessageCell) Kind() historyCellKind { return historyCellAssistant }

func (c assistantMessageCell) DisplayLines(width int) []string {
	return widthAwareDisplayLines(ui.FormatAssistantRendered(c.body), width)
}

// supplementLineCell renders one async/supplement line (tool feedback, warnings,
// reasoning summaries, team/system notices). It mirrors the legacy
// writeCompleteBlockLocked(FormatAssistantSupplementBlock) pipeline.
type supplementLineCell struct {
	cellIdentity
	line string
}

func newSupplementLineCell(line string) supplementLineCell {
	return supplementLineCell{cellIdentity: newCellIdentity(""), line: line}
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
	cellIdentity
	doc render.Document
}

func newAsyncDocumentCell(doc render.Document) asyncDocumentCell {
	return asyncDocumentCell{cellIdentity: newCellIdentity(""), doc: doc}
}

func (asyncDocumentCell) Kind() historyCellKind { return historyCellTool }

func (c asyncDocumentCell) DisplayLines(width int) []string {
	return widthAwareDisplayLines(ui.RenderDocumentANSI(c.doc), width)
}

// commandResultCell is the retained projection of one structured local command.
// Its identity is allocated once by the coordinator; all RenderBlocks from the
// command are already merged into doc, so one command produces one atomic cell.
type commandResultCell struct {
	cellIdentity
	doc render.Document
}

func newCommandResultCell(id string, sequence uint64, doc render.Document) commandResultCell {
	return commandResultCell{
		cellIdentity: newCellIdentity("").withID(id, sequence),
		doc:          doc,
	}
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
	cellIdentity
	event runtimechatcore.ChatEvent
}

func newToolChainCell(name string, args map[string]interface{}, _ time.Time) toolChainCell {
	return toolChainCell{
		cellIdentity: newCellIdentity("").withStatus(encoding.StatusRunning),
		event: runtimechatcore.ChatEvent{
			Type:      runtimechatcore.EventTool,
			Stage:     "tool_requested",
			ToolName:  name,
			Arguments: args,
		},
	}
}

func newToolChainCellFromEvent(event runtimechatcore.ChatEvent) toolChainCell {
	return toolChainCell{cellIdentity: newCellIdentity("").withStatus(encoding.StatusRunning), event: event}
}

func (c toolChainCell) withCompleted(result string, metadata map[string]interface{}) toolChainCell {
	c.event.Stage = "tool_result"
	c.event.Output = result
	if metadata != nil {
		c.event.Metadata = metadata
	}
	c.status = encoding.StatusCompleted
	return c
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
	cellIdentity
	source              string
	markdown            bool
	trailingDisplayLine string
	// optional formatter for width-aware markdown reflow; nil falls back to plain.
	formatFn func(source string, width int) string
}

func newAssistantStreamCell(source string, markdown bool) assistantStreamCell {
	return assistantStreamCell{cellIdentity: newCellIdentity(""), source: source, markdown: markdown}
}

func newAssistantStreamCellWithFormatter(source string, markdown bool, formatFn func(string, int) string) assistantStreamCell {
	return assistantStreamCell{cellIdentity: newCellIdentity(""), source: source, markdown: markdown, formatFn: formatFn}
}

// withTrailingDisplayLine attaches display-only policy output to the same
// semantic cell. It is deliberately not appended by an output call site: live
// finalization and one-shot replay must derive the identical tail from this
// cell at the same width.
func (c assistantStreamCell) withTrailingDisplayLine(line string) assistantStreamCell {
	c.trailingDisplayLine = line
	return c
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
	if c.trailingDisplayLine != "" {
		if formatted != "" && !strings.HasSuffix(formatted, "\n") {
			formatted += "\n"
		}
		formatted += c.trailingDisplayLine
	}
	return widthAwareDisplayLines(ui.FormatAssistantRendered(formatted), width)
}
