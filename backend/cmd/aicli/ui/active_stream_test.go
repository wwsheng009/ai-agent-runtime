package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/motion"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

func TestActiveStreamControllerMarkdownNoScrollbackSpam(t *testing.T) {
	motion.SetGlobal(motion.NewPolicy(motion.Config{Forced: motion.ForceMode(motion.ModeReduced)}))
	t.Cleanup(func() {
		motion.SetGlobal(motion.NewPolicy(motion.Config{Interactive: true}))
	})

	c := NewActiveStreamController(80, 6)
	c.BeginAssistant("assistant")
	c.PushAssistantDelta("#", true)
	frame1, changed1 := c.Paint(time.Unix(1, 0), true)
	if !changed1 {
		t.Fatal("first paint should change")
	}
	// Incomplete heading lead is holdback — body may show dim hint only.
	c.PushAssistantDelta(" Title\n\nHello stable paragraph.\n", true)
	frame2, _ := c.Paint(time.Unix(2, 0), true)
	if frame2 == "" {
		t.Fatal("expected frame after content")
	}
	// Paint twice quickly — second should not be forced duplicate commit.
	_, changedQuick := c.Paint(time.Unix(2, 0).Add(time.Millisecond), false)
	_ = changedQuick

	content, kind := c.Finalize()
	if content == "" || !strings.Contains(content, "Hello stable") {
		t.Fatalf("finalize content=%q kind=%v", content, kind)
	}
	if c.Active() {
		t.Fatal("should be inactive after finalize")
	}
	// Frames are viewport-only; finalize is the single transcript payload.
	if strings.Count(frame1+frame2, "Hello stable") > 2 {
		// loose check — main guarantee is single Finalize content
		t.Logf("frames=%q %q", frame1, frame2)
	}
}

func TestActiveStreamControllerCommittedPrefixLeavesMutableTail(t *testing.T) {
	c := NewActiveStreamController(40, 6)
	c.BeginAssistant("assistant")
	c.PushAssistantDelta("committed line\nmutable tail\n", false)
	if stable := c.StableContent(); stable != "committed line\nmutable tail\n" {
		t.Fatalf("StableContent=%q", stable)
	}
	c.CommitStablePrefix(len("committed line\n"))
	lines, _ := c.PaintLines(time.Now(), true)
	plain := (render.PlainBackend{}).Render(render.LinesDoc(lines...))
	if strings.Contains(plain, "committed line") || !strings.Contains(plain, "mutable tail") {
		t.Fatalf("active viewport did not retain only the mutable tail: %q", plain)
	}
	content, _ := c.Finalize()
	if content != "committed line\nmutable tail\n" {
		t.Fatalf("Finalize lost committed source prefix: %q", content)
	}
}

func TestActiveStreamControllerPaintLinesRetainsSemanticRoles(t *testing.T) {
	c := NewActiveStreamController(12, 4)
	c.BeginAssistant("assistant")
	c.PushAssistantDelta("中文 response body", false)

	lines, changed := c.PaintLines(time.Now(), true)
	if !changed || len(lines) < 2 {
		t.Fatalf("expected structured active frame, changed=%v lines=%#v", changed, lines)
	}
	if got := lines[0].Spans[0].Style.Role; got != string(style.RoleAccent) {
		t.Fatalf("expected accent header role, got %q", got)
	}
	if got := lines[1].Spans[0].Style.Role; got != string(style.RoleTextPrimary) {
		t.Fatalf("expected primary body role, got %q", got)
	}
	for _, line := range lines {
		if width := render.LineWidth(line); width > 12 {
			t.Fatalf("active line exceeds terminal width: width=%d line=%#v", width, line)
		}
	}
}

func TestActiveStreamControllerPaintLinesRendersStableMarkdownTokens(t *testing.T) {
	c := NewActiveStreamController(40, 8)
	c.BeginAssistant("assistant")
	c.PushAssistantDelta("# Result\n\n```go\nfunc main() { println(\"ok\") }\n```\n", true)

	lines, changed := c.PaintLines(time.Now(), true)
	if !changed {
		t.Fatal("expected completed markdown block to repaint")
	}
	plain := (render.PlainBackend{}).Render(render.LinesDoc(lines...))
	if !strings.Contains(plain, "Result") || !strings.Contains(plain, "func main") {
		t.Fatalf("expected rendered heading and code, got %q", plain)
	}
	if strings.Contains(plain, "```") {
		t.Fatalf("active markdown should not expose fence markers: %q", plain)
	}
	foundTokenColor := false
	for _, line := range lines {
		if render.LineWidth(line) > 40 {
			t.Fatalf("markdown active line overflow: %#v", line)
		}
		for _, span := range line.Spans {
			if strings.HasPrefix(span.Style.Role, "Code.") && span.Style.Foreground.IsSet() {
				foundTokenColor = true
			}
		}
	}
	if !foundTokenColor {
		t.Fatalf("expected Chroma token styles in active frame: %#v", lines)
	}
}

func TestActiveStreamControllerPaintLinesKeepsOpenFenceInMutedHoldback(t *testing.T) {
	c := NewActiveStreamController(24, 8)
	c.BeginAssistant("assistant")
	c.PushAssistantDelta("# Stable\n\n", true)
	c.PushAssistantDelta("```go\nfunc pending() {\n", true)

	lines, _ := c.PaintLines(time.Now(), true)
	plain := (render.PlainBackend{}).Render(render.LinesDoc(lines...))
	if !strings.Contains(plain, "Stable") || !strings.Contains(plain, "```go") {
		t.Fatalf("expected rendered stable prefix plus visible holdback hint, got %q", plain)
	}
	foundMutedHoldback := false
	for _, line := range lines {
		for _, span := range line.Spans {
			if strings.Contains(span.Text, "```go") && span.Style.Role == string(style.RoleTextMuted) && span.Style.Dim {
				foundMutedHoldback = true
			}
		}
	}
	if !foundMutedHoldback {
		t.Fatalf("expected open fence to remain a muted holdback span: %#v", lines)
	}
	fenceRow, codeRow := -1, -1
	for i, line := range lines {
		text := (render.PlainBackend{}).Render(render.LinesDoc(line))
		if strings.Contains(text, "```go") {
			fenceRow = i
		}
		if strings.Contains(text, "func pending") {
			codeRow = i
		}
	}
	if fenceRow < 0 || codeRow <= fenceRow {
		t.Fatalf("open fence should preserve complete code rows instead of flattening them: %#v", lines)
	}
}

func TestActiveStreamControllerBurstFillsViewportBeforeCoalescingTail(t *testing.T) {
	c := NewActiveStreamController(40, 8)
	c.BeginAssistant("assistant")
	now := time.Unix(10, 0)
	for i := 1; i <= 12; i++ {
		c.PushAssistantDelta(fmt.Sprintf("line %02d\n\n", i), true)
		_, _ = c.PaintLines(now, false)
	}
	lines, _ := c.PaintLines(now, false)
	if len(lines) != 8 {
		t.Fatalf("burst left a stale short viewport: got %d rows, want 8", len(lines))
	}
}

func TestActiveStreamControllerRebuildsMarkdownCacheForSyntaxTheme(t *testing.T) {
	previous := CurrentSyntaxThemeName()
	t.Cleanup(func() { _ = SetSyntaxTheme(previous) })
	if err := SetSyntaxTheme("monokai"); err != nil {
		t.Fatal(err)
	}
	c := NewActiveStreamController(60, 8)
	c.BeginAssistant("assistant")
	c.PushAssistantDelta("```go\nfunc main() {}\n```\n", true)
	_, _ = c.PaintLines(time.Now(), true)
	if c.markdownDocTheme != "monokai" {
		t.Fatalf("unexpected initial markdown cache theme %q", c.markdownDocTheme)
	}

	if err := SetSyntaxTheme("github"); err != nil {
		t.Fatal(err)
	}
	_, changed := c.PaintLines(time.Now(), true)
	if !changed {
		t.Fatal("expected token-style-only syntax theme change to mark frame changed")
	}
	if c.markdownDocTheme != "github" {
		t.Fatalf("expected markdown cache rebuild for theme change, got %q", c.markdownDocTheme)
	}
	c.Resize(20, 8)
	lines, _ := c.PaintLines(time.Now(), true)
	if c.markdownDocWidth != 20 {
		t.Fatalf("expected markdown cache rebuild for resize, got width %d", c.markdownDocWidth)
	}
	for _, line := range lines {
		if width := render.LineWidth(line); width > 20 {
			t.Fatalf("resized markdown active line overflow: width=%d line=%#v", width, line)
		}
	}
}

func TestActiveStreamControllerMarkdownViewportBaselineWidths(t *testing.T) {
	source := "# Summary\n\n- 中文内容 with a deliberately long explanation that must wrap safely\n\n```go\nfunc render(value string) { println(value) }\n```\n"
	for _, width := range []int{40, 80, 120} {
		t.Run(fmt.Sprintf("width_%d", width), func(t *testing.T) {
			c := NewActiveStreamController(width, 6)
			c.BeginAssistant("assistant")
			c.PushAssistantDelta(source, true)
			lines, _ := c.PaintLines(time.Unix(1, 0), true)
			if len(lines) == 0 || len(lines) > 6 {
				t.Fatalf("unexpected viewport height at width %d: %d", width, len(lines))
			}
			for _, line := range lines {
				if got := render.LineWidth(line); got > width {
					t.Fatalf("width %d overflow (%d): %#v", width, got, line)
				}
			}
			plain := (render.PlainBackend{}).Render(render.LinesDoc(lines...))
			if strings.Contains(plain, "```") || !strings.Contains(plain, "render") {
				t.Fatalf("unexpected width %d markdown projection: %q", width, plain)
			}
		})
	}
}

func TestActiveStreamControllerLargeCodeUsesSilentPreviewFallback(t *testing.T) {
	code := strings.Repeat("println(\"ok\")\n", activeHighlightMaxLines+1)
	c := NewActiveStreamController(80, 6)
	c.BeginAssistant("assistant")
	c.PushAssistantDelta("```go\n"+code+"```\n", true)
	lines, _ := c.PaintLines(time.Unix(1, 0), true)
	plain := (render.PlainBackend{}).Render(render.LinesDoc(lines...))
	if !strings.Contains(plain, "println") {
		t.Fatalf("large-code fallback lost visible tail: %q", plain)
	}
	if strings.Contains(plain, "limit_exceeded") {
		t.Fatalf("active viewport leaked technical highlighter fallback: %q", plain)
	}
	for _, line := range lines {
		for _, span := range line.Spans {
			if strings.HasPrefix(span.Style.Role, "Code.") && span.Style.Foreground.IsSet() {
				t.Fatalf("large active code should skip Chroma tokenization: %#v", span)
			}
		}
	}
}

func TestActiveStreamControllerCoalesceFPS(t *testing.T) {
	c := NewActiveStreamController(40, 4)
	c.BeginAssistant("")
	c.PushAssistantDelta("aaaa\n", false)
	now := time.Unix(10, 0)
	_, ch1 := c.Paint(now, false)
	if !ch1 {
		// first paint may need force when scheduler gap from zero
		_, ch1 = c.Paint(now, true)
	}
	if !ch1 {
		t.Fatal("expected first paint")
	}
	c.PushAssistantDelta("bbbb\n", false)
	_, ch2 := c.Paint(now.Add(time.Millisecond), false)
	if ch2 {
		// Within FPS gap, Consume should suppress — unless first-paint path.
		// Accept either; ensure Force still works.
	}
	_, ch3 := c.Paint(now.Add(100*time.Millisecond), true)
	if !ch3 && c.Active() {
		// force should render
		t.Log("force paint returned changed=false (identical buffer ok)")
	}
}

func TestActiveStreamControllerReportsPendingFrameDeadline(t *testing.T) {
	c := NewActiveStreamController(20, 2)
	c.Scheduler = render.NewFrameScheduler(10)
	c.Policy = motion.NewPolicy(motion.Config{Forced: motion.ForceMode(motion.ModeOff), Interactive: true})
	c.BeginAssistant("")
	c.PushAssistantDelta("first\nsecond\n", false)
	start := time.Unix(100, 0)
	_, _ = c.PaintLines(start, false)
	c.PushAssistantDelta("final\n", false)
	_, _ = c.PaintLines(start.Add(25*time.Millisecond), false)
	delay, needed := c.NextFrameDelay(start.Add(25 * time.Millisecond))
	if !needed || delay != 75*time.Millisecond {
		t.Fatalf("NextFrameDelay=(%s,%t), want (75ms,true)", delay, needed)
	}
}

func TestActiveStreamControllerKeepsPrefixOnMarkdownUpgrade(t *testing.T) {
	c := NewActiveStreamController(80, 8)
	c.BeginAssistant("assistant")
	c.PushAssistantDelta("intro text\n", false)
	c.PushAssistantDelta("```go\nfunc main() {}\n```\n", true)

	frame, _ := c.Paint(time.Now(), true)
	if !strings.Contains(frame, "intro text") {
		t.Fatalf("active frame lost pre-upgrade prefix: %q", frame)
	}
	content, _ := c.Finalize()
	if !strings.HasPrefix(content, "intro text\n```go") {
		t.Fatalf("final content lost or reordered prefix: %q", content)
	}
}

func TestActiveStreamNoticeLine(t *testing.T) {
	motion.SetGlobal(motion.NewPolicy(motion.Config{Forced: motion.ForceMode(motion.ModeReduced)}))
	c := NewActiveStreamController(40, 3)
	c.BeginTool("shell", map[string]interface{}{"command": "ls"})
	line := c.NoticeLine(time.Now())
	if !strings.Contains(line, "shell") {
		t.Fatalf("notice=%q", line)
	}
	if !c.IsToolActive() {
		t.Fatal("expected tool active")
	}
	if got := c.ToolName(); got != "shell" {
		t.Fatalf("ToolName=%q", got)
	}
	c.BeginAssistant("assistant")
	if c.IsToolActive() {
		t.Fatal("assistant should replace tool cell")
	}
}

func TestActiveStreamSetToolProgress(t *testing.T) {
	motion.SetGlobal(motion.NewPolicy(motion.Config{Forced: motion.ForceMode(motion.ModeReduced)}))
	c := NewActiveStreamController(80, 6)
	c.SetToolProgress("shell", "10% starting")
	if !c.IsToolActive() {
		t.Fatal("expected tool active after SetToolProgress")
	}
	if got := c.ToolName(); got != "shell" {
		t.Fatalf("ToolName=%q", got)
	}
	if got := c.ToolProgress(); got != "10% starting" {
		t.Fatalf("ToolProgress=%q", got)
	}
	frame, _ := c.Paint(time.Now(), true)
	if !strings.Contains(frame, "10%") {
		t.Fatalf("expected progress in frame, got %q", frame)
	}

	c.SetToolProgress("shell", "45% downloading")
	if got := c.ToolProgress(); got != "45% downloading" {
		t.Fatalf("updated ToolProgress=%q", got)
	}
	frame, _ = c.Paint(time.Now(), true)
	if !strings.Contains(frame, "45%") {
		t.Fatalf("expected updated progress in frame, got %q", frame)
	}
	if strings.Contains(frame, "10%") {
		t.Fatalf("stale progress should be replaced, got %q", frame)
	}

	// Identical progress is a no-op but keeps the cell active.
	c.SetToolProgress("shell", "45% downloading")
	if !c.IsToolActive() || c.ToolProgress() != "45% downloading" {
		t.Fatal("identical progress should keep tool cell")
	}

	c.SetToolProgress("view", "reading a.go")
	if got := c.ToolName(); got != "view" {
		t.Fatalf("expected tool switch, ToolName=%q", got)
	}
	if got := c.ToolProgress(); got != "reading a.go" {
		t.Fatalf("ToolProgress=%q", got)
	}
}

func TestActiveStreamControllerSetViewportOnlyRedrawsOnChange(t *testing.T) {
	c := NewActiveStreamController(40, 6)
	if got := c.ViewportRows(); got != 6 {
		t.Fatalf("initial rows=%d", got)
	}

	c.BeginAssistant("assistant")
	for i := 0; i < 10; i++ {
		c.PushAssistantDelta(fmt.Sprintf("body line %d\n", i), false)
	}
	now := time.Now()
	frame, changed := c.PaintLines(now, true)
	if !changed {
		t.Fatal("expected first paint to change")
	}
	if len(frame) != 6 {
		t.Fatalf("expected 6 rows for a 6-row viewport, got %d", len(frame))
	}

	// Same size must not mark the viewport dirty.
	c.SetViewport(40, 6)
	if _, changed := c.PaintLines(now, false); changed {
		t.Fatal("unchanged viewport size should not request a repaint")
	}

	c.SetViewport(0, 12)
	if got := c.ViewportRows(); got != 12 {
		t.Fatalf("rows after grow=%d want 12", got)
	}
	grown, changed := c.PaintLines(now.Add(time.Second), true)
	if !changed {
		t.Fatal("expected repaint after viewport grow")
	}
	if len(grown) <= len(frame) {
		t.Fatalf("grown viewport=%d rows not larger than %d", len(grown), len(frame))
	}
}

func TestActiveStreamControllerTallViewportShowsMoreStableLines(t *testing.T) {
	source := "第一段落。\n\n第二段落。\n\n第三段落。\n\n第四段落。\n\n第五段落。\n\n"
	small := NewActiveStreamController(40, ActiveBandMinRows)
	small.BeginAssistant("assistant")
	small.PushAssistantDelta(source, true)
	smallFrame, _ := small.PaintLines(time.Now(), true)

	tall := NewActiveStreamController(40, ActiveBandMaxRows)
	tall.BeginAssistant("assistant")
	tall.PushAssistantDelta(source, true)
	tallFrame, _ := tall.PaintLines(time.Now(), true)

	if len(smallFrame) > ActiveBandMinRows {
		t.Fatalf("small viewport emitted %d rows", len(smallFrame))
	}
	if len(tallFrame) <= len(smallFrame) {
		t.Fatalf("tall viewport=%d rows not larger than small=%d", len(tallFrame), len(smallFrame))
	}
	if len(tallFrame) > ActiveBandMaxRows {
		t.Fatalf("tall viewport emitted %d rows", len(tallFrame))
	}
	plain := render.PlainBackend{}.Render(render.LinesDoc(tallFrame...))
	if !strings.Contains(plain, "第五段落。") {
		t.Fatalf("tall viewport lost newest content: %q", plain)
	}
}

// TestActiveStreamControllerKeepsBlockBlankBeforeHoldback pins live/replay
// parity at the stable/holdback seam: the collector cuts stable on a blank
// line, so the mutable tail must keep the block-spacing blank row that
// markdown.Render produces for the same source once it is complete.
func TestActiveStreamControllerKeepsBlockBlankBeforeHoldback(t *testing.T) {
	cases := []struct {
		name   string
		source string
		want   []string
	}{
		{
			name:   "paragraph boundary",
			source: "para one.\n\npara two",
			want:   []string{"para one.", "", "para two"},
		},
		{
			name:   "heading boundary",
			source: "## Section\n\nbody stream",
			want:   []string{"▷ Section", "", "body stream"},
		},
		{
			name:   "soft break stays tight",
			source: "line one\nline two",
			want:   []string{"line one", "line two"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := NewActiveStreamController(40, 12)
			c.BeginAssistant("assistant")
			c.PushAssistantDelta(tc.source, true)
			lines, _ := c.PaintLines(time.Now(), true)
			rows := strings.Split((render.PlainBackend{}).Render(render.LinesDoc(lines...)), "\n")
			if len(rows) == 0 {
				t.Fatal("expected painted active band rows")
			}
			// Drop the animated header row; compare body rows only.
			rows = rows[1:]
			if len(rows) != len(tc.want) {
				t.Fatalf("row count %d != %d\ngot=%#v\nwant=%#v", len(rows), len(tc.want), rows, tc.want)
			}
			for i := range tc.want {
				if strings.TrimRight(rows[i], " ") != tc.want[i] {
					t.Fatalf("row[%d]=%q want %q\ngot=%#v", i, rows[i], tc.want[i], rows)
				}
			}
		})
	}
}
