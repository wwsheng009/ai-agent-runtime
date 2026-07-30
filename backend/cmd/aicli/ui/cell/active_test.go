package cell

import (
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/motion"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

func TestActiveCellAssistantDocument(t *testing.T) {
	p := motion.NewPolicy(motion.Config{Forced: motion.ForceMode(motion.ModeReduced)})
	a := ActiveCell{
		Kind:         ActiveAssistant,
		Title:        "assistant",
		Stable:       "Hello",
		Holdback:     "# draft",
		ShowActivity: true,
	}
	plain := a.FormatPlain(time.Now(), p)
	if !strings.Contains(plain, "Hello") {
		t.Fatalf("plain=%q", plain)
	}
	if !strings.Contains(plain, "assistant") {
		t.Fatalf("missing title: %q", plain)
	}
}

func TestActiveCellAssistantUsesStructuredBodyDocument(t *testing.T) {
	body := render.LinesDoc(render.Line{Spans: []render.Span{
		{Text: "func", Style: render.Style{Role: "Code.Keyword", Foreground: render.RGB(255, 0, 0)}},
		{Text: " main", Style: render.Style{Role: string(style.RoleCodeInline)}},
	}})
	a := ActiveCell{
		Kind:         ActiveAssistant,
		Title:        "assistant",
		Stable:       "raw markdown should not be projected",
		BodyDocument: &body,
	}
	doc := a.Document(time.Now(), motion.NewPolicy(motion.Config{Forced: motion.ForceMode(motion.ModeOff)}))
	plain := (render.PlainBackend{}).Render(doc)
	if !strings.Contains(plain, "func main") || strings.Contains(plain, "raw markdown") {
		t.Fatalf("unexpected structured assistant body projection: %q", plain)
	}
	foundTokenColor := false
	for _, block := range doc.Blocks {
		for _, line := range block.Lines {
			if len(line.Spans) > 0 && line.Spans[0].Style.Foreground.IsSet() {
				foundTokenColor = true
			}
		}
	}
	if !foundTokenColor {
		t.Fatalf("expected explicit token style to survive active cell composition: %#v", doc.Blocks)
	}
}

func TestRunningToolCell(t *testing.T) {
	a := RunningToolCell("view", map[string]interface{}{"path": "a.go"}, time.Now())
	if a.Kind != ActiveTool || a.Tool == nil {
		t.Fatalf("%+v", a)
	}
	plain := a.FormatPlain(time.Now(), motion.NewPolicy(motion.Config{Forced: motion.ForceMode(motion.ModeOff)}))
	if !strings.Contains(strings.ToLower(plain), "view") {
		t.Fatalf("plain=%q", plain)
	}
}

func TestRunningToolCellProgressBody(t *testing.T) {
	a := RunningToolCell("shell", nil, time.Now())
	a.Tool.Result = "45% downloading\nchunk-2"
	plain := a.FormatPlain(time.Now(), motion.NewPolicy(motion.Config{Forced: motion.ForceMode(motion.ModeOff)}))
	if !strings.Contains(strings.ToLower(plain), "shell") {
		t.Fatalf("missing tool name: %q", plain)
	}
	if !strings.Contains(plain, "45%") || !strings.Contains(plain, "chunk-2") {
		t.Fatalf("expected progress body in running tool cell, got %q", plain)
	}
}

// TestActiveCellAssistantHoldbackSeamKeepsBlockBlank pins the stable/holdback
// seam rule: a source cut on a blank line is a block boundary, so the mutable
// tail must keep the same single blank row that markdown block spacing (and
// history replay) produces. A soft line break inside one block stays tight.
func TestActiveCellAssistantHoldbackSeamKeepsBlockBlank(t *testing.T) {
	p := motion.NewPolicy(motion.Config{Forced: motion.ForceMode(motion.ModeReduced)})
	cases := []struct {
		name     string
		stable   string
		holdback string
		want     []string
	}{
		{
			name:     "block boundary inserts one blank",
			stable:   "para one.\n\n",
			holdback: "para two",
			want:     []string{"assistant", "para one.", "", "para two"},
		},
		{
			name:     "crlf block boundary inserts one blank",
			stable:   "para one.\r\n\r\n",
			holdback: "para two",
			want:     []string{"assistant", "para one.", "", "para two"},
		},
		{
			name:     "soft break stays tight",
			stable:   "line one\n",
			holdback: "line two",
			want:     []string{"assistant", "line one", "line two"},
		},
		{
			name:     "holdback only keeps no leading blank",
			stable:   "",
			holdback: "partial",
			want:     []string{"assistant", "partial"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := ActiveCell{
				Kind:     ActiveAssistant,
				Title:    "assistant",
				Stable:   tc.stable,
				Holdback: tc.holdback,
			}
			got := strings.Split(a.FormatPlain(time.Now(), p), "\n")
			if len(got) != len(tc.want) {
				t.Fatalf("row count %d != %d\ngot=%#v\nwant=%#v", len(got), len(tc.want), got, tc.want)
			}
			for i := range tc.want {
				if strings.TrimRight(got[i], " ") != tc.want[i] {
					t.Fatalf("row[%d]=%q want %q\ngot=%#v", i, got[i], tc.want[i], got)
				}
			}
		})
	}
}
