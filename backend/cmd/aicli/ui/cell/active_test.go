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
