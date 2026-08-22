package ui

import (
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/formatter"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/cell"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/markdown"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/motion"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/renderengine"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/syntax"
)

const (
	activeHighlightMaxBytes = 64 * 1024
	activeHighlightMaxLines = 2000
	activeHighlightBudget   = 80 * time.Millisecond
)

// ActiveStreamController owns the in-progress assistant/tool viewport.
//
// Scrollback stays clean: deltas update an in-memory BufferBackend and only
// coalesced frames are exposed via Paint. Finalize returns the content that
// should be committed once to the transcript.
type ActiveStreamController struct {
	mu sync.Mutex

	// Scheduler is a compatibility injection point. Production controllers use
	// the RenderEngine FrameClock; tests may still provide render.FrameScheduler
	// while callers migrate to the shared frame abstraction.
	Scheduler   renderengine.FrameGate
	Buffer      *render.BufferBackend
	Policy      motion.Policy
	Highlighter syntax.Highlighter

	cell       cell.ActiveCell
	md         markdown.StreamCollector
	prev       []string
	prevStyled []render.Line
	active     bool
	markdown   bool
	committed  int

	// markdownCache is the shared RenderCache (阶段 D §4.6). nil falls back
	// to the process-wide renderengine.SharedRenderCache. Tests inject a
	// private instance to assert hit/miss accounting.
	markdownCache *renderengine.RenderCache

	markdownFrameDoc   render.Document
	markdownFrameHold  string
	markdownFrameTitle string
}

// ActiveStreamSourceSnapshot is a read-only migration view of the controller's
// semantic source. It intentionally exposes byte boundaries, not rendered rows
// or BufferBackend state. Tool display is omitted from Source because a running
// tool is an overlay and must not be mistaken for finalized transcript source.
type ActiveStreamSourceSnapshot struct {
	Active       bool
	Kind         cell.ActiveKind
	Source       string
	StableEnd    int
	CommittedEnd int
}

// SourceSnapshot provides the only supported read path for a future bridge to
// construct an ActiveCellState update. It does not advance committed progress
// and does not expose the mutable cell pointer or terminal frame cache.
func (c *ActiveStreamController) SourceSnapshot() ActiveStreamSourceSnapshot {
	if c == nil {
		return ActiveStreamSourceSnapshot{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.active {
		return ActiveStreamSourceSnapshot{}
	}
	snapshot := ActiveStreamSourceSnapshot{
		Active:       true,
		Kind:         c.cell.Kind,
		CommittedEnd: c.committed,
	}
	if c.cell.Kind != cell.ActiveAssistant {
		return snapshot
	}
	if c.markdown {
		snapshot.Source = c.cell.Body
		snapshot.StableEnd = len(c.md.Stable())
	} else {
		// Source and all ledger ranges use the same semantic byte coordinates.
		// Ordinary assistant presentation no longer bakes event chrome into the
		// source, so shadow replacement and history handoff compare raw content.
		snapshot.Source = c.cell.Body
		snapshot.StableEnd = len(c.cell.Stable)
	}
	return snapshot
}

// NewActiveStreamController builds a controller with 30 FPS coalescing.
func NewActiveStreamController(width, height int) *ActiveStreamController {
	if width <= 0 {
		width = 80
	}
	if height <= 0 {
		height = ActiveBandMinRows
	}
	c := &ActiveStreamController{
		Scheduler:   renderengine.NewFrameClock(render.DefaultMaxFPS),
		Buffer:      &render.BufferBackend{Width: width, Height: height},
		Policy:      motion.Global(),
		Highlighter: newActiveStreamHighlighter(),
	}
	return c
}

// SetRenderCache adopts the RenderEngine cache for markdown document reuse.
// A nil cache keeps the historical process-wide fallback used by standalone
// controllers and tests.
func (c *ActiveStreamController) SetRenderCache(cache *renderengine.RenderCache) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.markdownCache = cache
	c.mu.Unlock()
}

// Active reports whether a stream is in progress.
func (c *ActiveStreamController) Active() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.active
}

// IsToolActive reports whether the in-progress cell is a running tool.
func (c *ActiveStreamController) IsToolActive() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.active && c.cell.Kind == cell.ActiveTool
}

// ToolName returns the active tool function name when a tool cell is running.
func (c *ActiveStreamController) ToolName() string {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.active || c.cell.Kind != cell.ActiveTool {
		return ""
	}
	if c.cell.Tool != nil && c.cell.Tool.FunctionName != "" {
		return c.cell.Tool.FunctionName
	}
	return c.cell.Title
}

// ToolProgress returns the active tool progress text when a tool cell is running.
func (c *ActiveStreamController) ToolProgress() string {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.active || c.cell.Kind != cell.ActiveTool || c.cell.Tool == nil {
		return ""
	}
	return c.cell.Tool.Result
}

// ToolDisplay returns the canonical viewport-only display text for an active
// tool when one was supplied by the shared chat tool renderer.
func (c *ActiveStreamController) ToolDisplay() string {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.active || c.cell.Kind != cell.ActiveTool {
		return ""
	}
	return c.cell.Display
}

// BeginAssistant starts an assistant active cell.
func (c *ActiveStreamController) BeginAssistant(title string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.resetLocked()
	c.active = true
	c.markdown = false
	c.cell = cell.ActiveCell{
		Kind:         cell.ActiveAssistant,
		Title:        title,
		ShowActivity: true,
		UpdatedAt:    time.Now(),
		Status:       style.RunStreaming,
	}
	c.Scheduler.Request("assistant.begin")
}

// BeginTool starts a running tool active cell.
func (c *ActiveStreamController) BeginTool(name string, args map[string]interface{}) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.resetLocked()
	c.active = true
	c.cell = cell.RunningToolCell(name, args, time.Now())
	c.Scheduler.Request("tool.begin")
}

// BeginToolDisplay starts a running tool cell using an already-rendered
// canonical display. The display remains mutable viewport state and is never
// returned as assistant transcript content.
func (c *ActiveStreamController) BeginToolDisplay(name string, args map[string]interface{}, display string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.resetLocked()
	c.active = true
	c.cell = cell.RunningToolCell(name, args, time.Now())
	c.cell.Display = strings.TrimRight(display, "\r\n")
	c.Scheduler.Request("tool.begin")
}

// SetToolProgress updates an in-progress tool cell in place when the tool name
// matches; otherwise it starts a new running tool cell. Progress is shown under
// the tool header in the ActiveBand and never commits to scrollback.
func (c *ActiveStreamController) SetToolProgress(name, progress string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	name = strings.TrimSpace(name)
	if name == "" {
		name = "tool"
	}
	progress = strings.TrimSpace(progress)
	if c.active && c.cell.Kind == cell.ActiveTool && c.cell.Tool != nil && c.cell.Tool.FunctionName == name {
		if c.cell.Tool.Result == progress {
			return
		}
		c.cell.Tool.Result = progress
		c.cell.UpdatedAt = time.Now()
		c.Scheduler.Request("tool.progress")
		return
	}
	c.resetLocked()
	c.active = true
	active := cell.RunningToolCell(name, nil, time.Now())
	if progress != "" && active.Tool != nil {
		active.Tool.Result = progress
	}
	c.cell = active
	c.Scheduler.Request("tool.begin")
}

// PushAssistantDelta appends text. When asMarkdown is true, only the stable
// prefix is shown in the active body; holdback stays dimmed.
// Returns newly-stable text suitable for optional live transcript append
// (empty when nothing new is stable).
func (c *ActiveStreamController) PushAssistantDelta(delta string, asMarkdown bool) (newlyStable string) {
	if c == nil || delta == "" {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.active {
		c.active = true
		c.cell = cell.ActiveCell{
			Kind:         cell.ActiveAssistant,
			Title:        "assistant",
			ShowActivity: true,
			Status:       style.RunStreaming,
		}
	}
	if asMarkdown && !c.markdown && c.cell.Body != "" {
		// The stream classifier may upgrade from plain text after seeing more
		// context. Seed the Markdown collector so the active viewport and final
		// payload retain the already-received prefix.
		_ = c.md.SetContent(c.cell.Body)
	}
	c.markdown = c.markdown || asMarkdown
	c.cell.UpdatedAt = time.Now()
	if c.markdown {
		newlyStable = c.md.Push(delta)
		c.cell.Body = c.md.Raw()
		c.cell.Stable = c.md.Stable()
		c.cell.Holdback = c.md.Holdback()
	} else {
		c.cell.Body += delta
		c.cell.Stable = c.cell.Body
		c.cell.Holdback = ""
		newlyStable = delta
	}
	c.Scheduler.Request("assistant.delta")
	return newlyStable
}

// StableContent returns the append-only source prefix that is safe to move
// from the mutable viewport into scrollback.
func (c *ActiveStreamController) StableContent() string {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.active || c.cell.Kind != cell.ActiveAssistant {
		return ""
	}
	if c.markdown {
		return c.md.Stable()
	}
	return c.cell.Stable
}

// CommitStablePrefix hides an absolute source prefix from the mutable cell.
// Finalize still returns the complete raw source for transcript ownership and
// history persistence.
func (c *ActiveStreamController) CommitStablePrefix(offset int) {
	if c == nil || offset <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	stableLen := len(c.cell.Stable)
	if c.markdown {
		stableLen = len(c.md.Stable())
	}
	if offset <= c.committed || offset > stableLen {
		return
	}
	c.committed = offset
	c.Scheduler.Request("assistant.commit")
}

// SetAssistantSnapshot replaces content (for snapshot-style streams).
func (c *ActiveStreamController) SetAssistantSnapshot(content string, asMarkdown bool) (newlyStable string) {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.active {
		c.active = true
		c.cell = cell.ActiveCell{
			Kind:         cell.ActiveAssistant,
			ShowActivity: true,
			Status:       style.RunStreaming,
		}
	}
	c.markdown = asMarkdown
	c.cell.UpdatedAt = time.Now()
	if asMarkdown {
		newlyStable = c.md.SetContent(content)
		c.cell.Body = c.md.Raw()
		c.cell.Stable = c.md.Stable()
		c.cell.Holdback = c.md.Holdback()
	} else {
		prev := c.cell.Body
		c.cell.Body = content
		c.cell.Stable = content
		if strings.HasPrefix(content, prev) {
			newlyStable = content[len(prev):]
		} else {
			newlyStable = content
		}
	}
	c.Scheduler.Request("assistant.snapshot")
	return newlyStable
}

// Paint coalesces and materializes the active buffer.
// changed is true when visible lines differ from the previous paint.
// frame is the plain multi-line active region (no transcript commit).
func (c *ActiveStreamController) Paint(now time.Time, force bool) (frame string, changed bool) {
	if c == nil {
		return "", false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_, changed = c.paintLinesLocked(now, force)
	return strings.Join(c.Buffer.Lines, "\n"), changed
}

// PaintLines materializes the active frame without flattening semantic spans.
// The returned lines are safe structured data; terminal encoding remains the
// responsibility of the fixed-bottom surface.
func (c *ActiveStreamController) PaintLines(now time.Time, force bool) (lines []render.Line, changed bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.paintLinesLocked(now, force)
}

// NextFrameDelay reports when the controller should be polled again. Pending
// content uses the scheduler's FPS deadline; animated activity uses the motion
// cadence. Callers should stop polling as soon as the active cell is finalized.
func (c *ActiveStreamController) NextFrameDelay(now time.Time) (time.Duration, bool) {
	if c == nil {
		return 0, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.active {
		return 0, false
	}
	delay, needed := c.Scheduler.NextDelay(now)
	if c.Policy == nil || !c.Policy.NeedsNextFrame() {
		return delay, needed
	}
	motionDelay := c.Policy.Interval()
	if motionDelay <= 0 {
		return delay, needed
	}
	if !needed || motionDelay < delay {
		return motionDelay, true
	}
	return delay, true
}

func (c *ActiveStreamController) paintLinesLocked(now time.Time, force bool) ([]render.Line, bool) {
	if !c.active {
		return nil, false
	}
	emitted := false
	if force {
		c.Scheduler.Request("force")
		emitted = c.Scheduler.ForceConsume(now)
	} else {
		// Always try consume; Request may have been set by Push.
		emitted = c.Scheduler.Consume(now)
		if !emitted && c.Policy != nil && c.Policy.NeedsNextFrame() {
			// Motion-only refresh still needs a coalesced slot.
			c.Scheduler.Request("motion")
			emitted = c.Scheduler.Consume(now)
		}
	}
	if !emitted && !force {
		// Still allow reading current buffer without advancing FPS credit when forced path unused.
		// While the viewport is still filling, do not let FPS coalescing strand a
		// short stale frame after a burst of deltas. Once full, content-only tail
		// changes remain coalesced normally.
		if len(c.prev) > 0 && !c.shouldGrowViewportLocked() {
			return c.Buffer.StyledSnapshot(), false
		}
	}
	doc := c.activeDocumentLocked(now)
	_ = c.Buffer.Render(doc)
	diffs := c.Buffer.Diff(c.prev)
	styled := c.Buffer.StyledSnapshot()
	changed := len(diffs) > 0 || !render.LinesEqual(c.prevStyled, styled)
	c.prev = c.Buffer.Snapshot()
	c.prevStyled = styled
	return c.Buffer.StyledSnapshot(), changed
}

func (c *ActiveStreamController) shouldGrowViewportLocked() bool {
	if c.Buffer == nil || c.Buffer.Height <= 0 || len(c.prev) >= c.Buffer.Height {
		return false
	}
	content := c.cell.Body
	if c.markdown {
		content = c.cell.Stable
	}
	content = activeSourceSuffix(content, c.committed)
	if c.cell.Holdback != "" {
		content += c.cell.Holdback
	}
	rows := 1 // active-cell header
	width := c.Buffer.Width
	if width <= 0 {
		width = 80
	}
	for _, line := range strings.Split(content, "\n") {
		lineRows := 1
		if cells := render.Width(line); cells > width {
			lineRows = (cells + width - 1) / width
		}
		rows += lineRows
		if rows > len(c.prev) {
			return true
		}
	}
	return rows > len(c.prev)
}

func (c *ActiveStreamController) activeDocumentLocked(now time.Time) render.Document {
	active := c.cell
	if c.markdown && active.Kind == cell.ActiveAssistant {
		active.Stable = activeSourceSuffix(active.Stable, c.committed)
		width := 80
		if c.Buffer != nil && c.Buffer.Width > 0 {
			width = c.Buffer.Width
		}
		syntaxTheme := CurrentResolvedSyntaxThemeName()
		if c.Highlighter == nil {
			c.Highlighter = newActiveStreamHighlighter()
		}
		// 阶段 D：band 与 scrollback 走同一条 Formatter 路径，正文文档由
		// 共享 RenderCache 内容寻址（hash+width+theme+mode）。缓存未命中
		// 等价于旧实现的 bodyChanged（源码/宽度/主题任一变化）。
		bodyDoc, hit := c.bandFormatter(width, syntaxTheme).FormatDocumentCached(active.Stable)
		if !hit || c.markdownFrameDoc.LineCount() == 0 || c.markdownFrameHold != active.Holdback || c.markdownFrameTitle != active.Title {
			active.BodyDocument = &bodyDoc
			c.markdownFrameDoc = active.Document(now, c.Policy)
			c.markdownFrameHold = active.Holdback
			c.markdownFrameTitle = active.Title
		} else {
			updateActiveAssistantHeader(&c.markdownFrameDoc, activeAssistantHeader(active, now, c.Policy))
		}
		return c.markdownFrameDoc
	}
	if active.Kind == cell.ActiveAssistant && c.committed > 0 {
		active.Body = activeSourceSuffix(active.Body, c.committed)
		active.Stable = activeSourceSuffix(active.Stable, c.committed)
	}
	return active.Document(now, c.Policy)
}

// bandFormatter builds the single shared Formatter render path for the live
// ActiveBand. Band-specific differences (highlighter throttling, holdback
// hygiene) stay as formatter options; the RenderCache key keeps mode "band"
// so band frames and scrollback replay share documents when options match.
func (c *ActiveStreamController) bandFormatter(width int, syntaxTheme string) *formatter.MarkdownFormatter {
	f := formatter.NewMarkdownFormatter(true)
	f.Width = width
	f.SyntaxTheme = syntaxTheme
	f.Highlighter = c.Highlighter
	f.AssistantBody = true
	f.HideHighlightFallback = true
	f.TrustMarkdown = true
	// ActiveBandBodyOptions historically used a zero-value ThemeContext; keep
	// that contract so cached documents stay identical to the pre-stage-D path.
	f.ThemeContextProvider = bandThemeContextProvider
	f.Cache = c.markdownCache
	return f
}

// bandThemeContextProvider is package-level so per-frame formatter builds do
// not allocate a fresh closure.
var bandThemeContextProvider = func() style.ThemeContext { return style.ThemeContext{} }

func activeSourceSuffix(source string, committed int) string {
	if committed <= 0 {
		return source
	}
	if committed >= len(source) {
		return ""
	}
	return source[committed:]
}

func newActiveBandHighlighter() syntax.Highlighter {
	highlighter := syntax.NewChromaHighlighter()
	highlighter.Limits = syntax.Limits{
		MaxBytes: activeHighlightMaxBytes,
		MaxLines: activeHighlightMaxLines,
	}
	highlighter.Budget = activeHighlightBudget
	return highlighter
}

func newActiveStreamHighlighter() syntax.Highlighter {
	return newActiveBandHighlighter()
}

func activeAssistantHeader(active cell.ActiveCell, now time.Time, policy motion.Policy) string {
	title := active.Title
	if title == "" {
		title = "assistant"
	}
	if !active.ShowActivity {
		return title
	}
	if policy == nil {
		policy = motion.Global()
	}
	marker := policy.ActivityFrame(now)
	if marker == "" {
		marker = "•"
	}
	return marker + " " + title
}

func updateActiveAssistantHeader(doc *render.Document, header string) {
	if doc == nil || len(doc.Blocks) == 0 || len(doc.Blocks[0].Lines) == 0 || len(doc.Blocks[0].Lines[0].Spans) == 0 {
		return
	}
	doc.Blocks[0].Lines[0].Spans[0].Text = header
}

// NoticeLine returns a single-line status hint for the prompt notice area.
func (c *ActiveStreamController) NoticeLine(now time.Time) string {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.active {
		return ""
	}
	marker := "•"
	if c.Policy != nil {
		marker = c.Policy.ActivityFrame(now)
	}
	switch c.cell.Kind {
	case cell.ActiveTool:
		name := c.cell.Title
		if c.cell.Tool != nil {
			name = c.cell.Tool.FunctionName
		}
		return strings.TrimSpace(marker + " running " + name)
	case cell.ActiveReasoning:
		return strings.TrimSpace(marker + " reasoning")
	default:
		return strings.TrimSpace(marker + " streaming…")
	}
}

// Finalize ends the active stream and returns full content for one transcript write.
func (c *ActiveStreamController) Finalize() (content string, kind cell.ActiveKind) {
	if c == nil {
		return "", cell.ActiveNone
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.active {
		return "", cell.ActiveNone
	}
	kind = c.cell.Kind
	if c.markdown {
		_ = c.md.Finalize()
		content = c.md.Raw()
	} else {
		content = c.cell.Body
	}
	c.resetLocked()
	return content, kind
}

// Cancel drops the active cell without returning content.
func (c *ActiveStreamController) Cancel() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.resetLocked()
}

// Resize updates buffer geometry and requests a forced redraw on next Paint(force).
func (c *ActiveStreamController) Resize(width, height int) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if width > 0 {
		c.Buffer.Width = width
	}
	if height > 0 {
		c.Buffer.Height = height
	}
	c.Scheduler.Request("resize")
}

// SetViewport applies viewport geometry and only requests a redraw when the
// size actually changed, so callers can sync it on every streaming frame.
func (c *ActiveStreamController) SetViewport(width, height int) {
	if c == nil {
		return
	}
	c.mu.Lock()
	if c.Buffer == nil {
		c.mu.Unlock()
		return
	}
	changed := false
	if width > 0 && c.Buffer.Width != width {
		c.Buffer.Width = width
		changed = true
	}
	if height > 0 && c.Buffer.Height != height {
		c.Buffer.Height = height
		changed = true
	}
	c.mu.Unlock()
	if changed {
		c.Scheduler.Request("resize")
	}
}

// ViewportRows reports the current active viewport height in rows.
func (c *ActiveStreamController) ViewportRows() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Buffer == nil {
		return 0
	}
	return c.Buffer.Height
}

func (c *ActiveStreamController) resetLocked() {
	c.active = false
	c.markdown = false
	c.committed = 0
	c.cell = cell.ActiveCell{}
	c.md.Reset()
	c.prev = nil
	c.prevStyled = nil
	c.markdownFrameDoc = render.Document{}
	c.markdownFrameHold = ""
	c.markdownFrameTitle = ""
	if c.Buffer != nil {
		c.Buffer.Lines = nil
		c.Buffer.StyledLines = nil
	}
}
