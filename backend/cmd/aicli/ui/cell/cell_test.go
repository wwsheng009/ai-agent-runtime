package cell

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

func TestLegacyTimelineParserTags(t *testing.T) {
	cases := []struct {
		in   string
		kind TimelineKind
	}{
		{"[tool] view path", TimelineTool},
		{"[tool done] shell", TimelineTool},
		{"[approval] allow", TimelineApproval},
		{"[reasoning] think", TimelineReasoning},
		{"• [task] started", TimelineTask},
		{"failed: boom", TimelineNotice},
	}
	for _, tc := range cases {
		ev, ok := LegacyTimelineParser(tc.in)
		if !ok {
			t.Fatalf("parse failed for %q", tc.in)
		}
		if ev.Kind != tc.kind {
			t.Fatalf("%q kind=%v want %v", tc.in, ev.Kind, tc.kind)
		}
	}
}

func TestTimelineEventDocumentToolDonePrefix(t *testing.T) {
	ev, ok := LegacyTimelineParser("[tool done] shell exit 0")
	if !ok {
		t.Fatal("parse failed")
	}
	plain := ev.FormatPlain()
	if plain != "[tool done] shell exit 0" {
		t.Fatalf("Document plain=%q", plain)
	}
	running, ok := LegacyTimelineParser("[tool] view")
	if !ok {
		t.Fatal("parse running failed")
	}
	if got := running.FormatPlain(); got != "[tool] view" {
		t.Fatalf("running plain=%q", got)
	}
	// Bullet notices now go through Document (preserves marker).
	bullet, ok := LegacyTimelineParser("• Edited file.go")
	if !ok {
		t.Fatal("bullet parse failed")
	}
	if got := bullet.FormatPlain(); got != "• Edited file.go" {
		t.Fatalf("bullet Document plain=%q", got)
	}
	bullet2, ok := LegacyTimelineParser("• [task] started")
	if !ok {
		t.Fatal("bullet bracket parse failed")
	}
	if got := bullet2.FormatPlain(); got != "• [task] started" {
		t.Fatalf("bullet bracket Document plain=%q", got)
	}
}

func TestTimelineEventDocumentCustomTagAndErrorRole(t *testing.T) {
	event := TimelineEvent{
		Kind:   TimelineTeam,
		Status: StatusError,
		Tag:    "[team summary]",
		Title:  "failed team-1",
	}
	doc := event.Document()
	if got := event.FormatPlain(); got != "[team summary] failed team-1" {
		t.Fatalf("plain=%q", got)
	}
	if got := doc.Blocks[0].Lines[0].Spans[0].Style.Role; got != string(style.RoleError) {
		t.Fatalf("tag role=%q want=%q", got, style.RoleError)
	}
}

func TestTimelineEventDocumentPreservesDetailLines(t *testing.T) {
	event := TimelineEvent{
		Kind:               TimelineTool,
		Status:             StatusRunning,
		Marker:             "• ",
		SuppressKindPrefix: true,
		Title:              "Running shell",
		Details:            []string{"  cwd: C:/work", "  backend: local"},
	}
	want := "• Running shell\n  cwd: C:/work\n  backend: local"
	if got := event.FormatPlain(); got != want {
		t.Fatalf("plain=%q want=%q", got, want)
	}
	if got := event.Document().Blocks[0].Lines[1].Spans[0].Style.Role; got != string(style.RoleTextMuted) {
		t.Fatalf("detail role=%q", got)
	}
}

func TestToolCellStripsControlsInPreview(t *testing.T) {
	tc := ToolCell{
		FunctionName: "shell",
		Arguments:    map[string]interface{}{"command": "ls"},
		Status:       StatusSuccess,
		Result:       "ok\x1b[2J\x1b]0;pwned\x07done\nline2",
		Preview: PreviewOptions{
			MaxLines:  6,
			HeadLines: 4,
			TailLines: 2,
			MaxBytes:  1024,
		},
	}
	plain := tc.FormatPlain()
	if strings.Contains(plain, "\x1b") || strings.Contains(plain, "pwned") {
		t.Fatalf("controls leaked: %q", plain)
	}
	if !strings.Contains(plain, "ok") || !strings.Contains(plain, "done") {
		t.Fatalf("lost text: %q", plain)
	}
	if !strings.Contains(plain, "✓") || !strings.Contains(plain, "Shell") {
		t.Fatalf("missing header: %q", plain)
	}
}

func TestBuildPreviewHeadTail(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 20; i++ {
		b.WriteString(strings.Repeat("x", 3))
		b.WriteByte('\n')
	}
	res := BuildPreview(b.String(), PreviewOptions{
		MaxLines:  6,
		HeadLines: 3,
		TailLines: 2,
		MaxBytes:  0,
	})
	if res.OmittedLines <= 0 {
		t.Fatalf("expected omission, got %+v", res)
	}
	plain := render.PlainBackend{}.Render(render.Document{
		Blocks: []render.Block{{Lines: res.Lines}},
	})
	if !strings.Contains(plain, "omitted") {
		t.Fatalf("missing omission marker: %q", plain)
	}
	// No raw CSI
	if strings.Contains(plain, "\x1b") {
		t.Fatalf("ESC in preview: %q", plain)
	}
}

func TestBuildPreviewUsesTerminalCellWidth(t *testing.T) {
	res := BuildPreview("中文中文abc", PreviewOptions{
		MaxLines:     2,
		HeadLines:    1,
		MaxLineWidth: 6,
	})
	if len(res.Lines) != 1 {
		t.Fatalf("lines=%d", len(res.Lines))
	}
	if width := render.LineWidth(res.Lines[0]); width > 6 {
		t.Fatalf("preview width=%d: %+v", width, res.Lines[0])
	}
}

func TestBuildPreviewPreservesAllowedSGRStyles(t *testing.T) {
	res := BuildPreview("\x1b[31mred\x1b[0m\nplain", PreviewOptions{
		MaxLines:     4,
		HeadLines:    2,
		MaxLineWidth: 20,
		AllowANSI:    true,
	})
	if len(res.Lines) != 2 {
		t.Fatalf("lines=%d: %+v", len(res.Lines), res.Lines)
	}
	if len(res.Lines[0].Spans) == 0 || !res.Lines[0].Spans[0].Style.Foreground.IsSet() {
		t.Fatalf("SGR style was lost: %+v", res.Lines[0])
	}
}

func TestArgSummaryStableOrder(t *testing.T) {
	a := formatArgSummary(map[string]interface{}{"z": "1", "a": "2", "m": "3"})
	b := formatArgSummary(map[string]interface{}{"m": "3", "z": "1", "a": "2"})
	if a != b {
		t.Fatalf("unstable arg order: %q vs %q", a, b)
	}
}

func TestToolCellRendersLongFilePathOnOwnLine(t *testing.T) {
	base := t.TempDir()
	filename := strings.Repeat("very-long-file-name-", 4) + "component.generated.tsx"
	absPath := filepath.Join(base, "apps", "portal-modern", "src", filename)
	wantPath := filepath.Join("apps", "portal-modern", "src", filename)

	got := (ToolCell{
		FunctionName: "view",
		Arguments: map[string]interface{}{
			"file_path": absPath,
			"workdir":   base,
			"limit":     20,
		},
		Status: StatusRunning,
	}).FormatPlain()

	if !strings.Contains(got, "\n  file_path: "+wantPath) {
		t.Fatalf("long file path was not moved to its own line: %q", got)
	}
	if !strings.Contains(got, filename) || strings.Contains(got, filename[:30]+"…") {
		t.Fatalf("full filename was not preserved: %q", got)
	}
}
