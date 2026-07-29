package cell

import (
	"fmt"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/motion"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

// ActiveKind classifies the live (in-progress) cell above the prompt.
type ActiveKind int

const (
	ActiveNone ActiveKind = iota
	ActiveAssistant
	ActiveReasoning
	ActiveTool
	ActiveStatus
)

// ActiveCell is the mutable in-progress viewport model.
// It is rendered into a BufferBackend and only committed to scrollback on Finalize.
type ActiveCell struct {
	Kind     ActiveKind
	Title    string
	Body     string // raw / plain accumulating content
	Stable   string // markdown-stable portion when applicable
	Holdback string
	// BodyDocument is an optional render-ready projection of Stable. It is
	// transient viewport state only; Body remains the source used at finalize.
	BodyDocument *render.Document
	Tool         *ToolCell
	Status       style.RunState
	// Activity uses MotionPolicy for a fixed-width marker.
	ShowActivity bool
	UpdatedAt    time.Time
}

// Document builds a structured view of the active cell (not yet transcript).
func (a ActiveCell) Document(now time.Time, policy motion.Policy) render.Document {
	if a.Kind == ActiveNone {
		return render.Document{}
	}
	if policy == nil {
		policy = motion.Global()
	}
	var lines []render.Line

	marker := ""
	if a.ShowActivity {
		marker = policy.ActivityFrame(now)
		if marker == "" {
			marker = "•"
		}
	}

	switch a.Kind {
	case ActiveAssistant:
		return a.assistantDocument(marker)

	case ActiveReasoning:
		header := "reasoning"
		if marker != "" {
			header = marker + " " + header
		}
		lines = append(lines, render.Line{Spans: []render.Span{
			{Text: header, Style: render.Style{Role: string(style.RoleReasoning)}},
		}})
		for _, row := range strings.Split(trimRightNewlines(a.Body), "\n") {
			lines = append(lines, render.Line{Spans: []render.Span{
				{Text: "  " + row, Style: render.Style{Role: string(style.RoleTextSecondary)}},
			}})
		}

	case ActiveTool:
		if a.Tool != nil {
			return a.Tool.Document()
		}
		header := a.Title
		if header == "" {
			header = "tool"
		}
		if marker != "" {
			header = marker + " " + header
		}
		lines = append(lines, render.Line{Spans: []render.Span{
			{Text: header, Style: render.Style{Role: string(style.RoleTool), Bold: true}},
		}})
		if a.Body != "" {
			lines = append(lines, render.Line{Spans: []render.Span{
				{Text: a.Body, Style: render.Style{Role: string(style.RoleTextMuted), Dim: true}},
			}})
		}

	case ActiveStatus:
		text := a.Title
		if text == "" {
			text = string(a.Status)
		}
		if marker != "" {
			text = marker + " " + text
		}
		role := style.RoleInfo
		switch a.Status {
		case style.RunStreaming, style.RunRunning, style.RunWorking:
			role = style.RoleTool
		case style.RunError:
			role = style.RoleError
		case style.RunReady:
			role = style.RoleSuccess
		}
		lines = append(lines, render.Line{Spans: []render.Span{
			{Text: text, Style: render.Style{Role: string(role)}},
		}})
	}

	if len(lines) == 0 {
		return render.Document{}
	}
	return render.Document{Blocks: []render.Block{{
		Kind:  render.BlockCustom,
		Lines: lines,
	}}}
}

func (a ActiveCell) assistantDocument(marker string) render.Document {
	title := a.Title
	if title == "" {
		title = "assistant"
	}
	header := title
	if marker != "" {
		header = marker + " " + title
	}
	blocks := []render.Block{{
		Kind: render.BlockCustom,
		Lines: []render.Line{{Spans: []render.Span{{
			Text:  header,
			Style: render.Style{Role: string(style.RoleAccent), Bold: true},
		}}}},
	}}

	body := a.Stable
	if body == "" {
		body = a.Body
	}
	if a.BodyDocument != nil {
		blocks = append(blocks, a.BodyDocument.Blocks...)
	} else {
		bodyLines := make([]render.Line, 0, strings.Count(body, "\n")+1)
		for _, row := range strings.Split(trimRightNewlines(body), "\n") {
			bodyLines = append(bodyLines, render.Line{Spans: []render.Span{{
				Text:  row,
				Style: render.Style{Role: string(style.RoleTextPrimary)},
			}}})
		}
		blocks = append(blocks, render.Block{Kind: render.BlockParagraph, Lines: bodyLines})
	}

	if a.Holdback != "" && strings.TrimSpace(a.Holdback) != strings.TrimSpace(body) {
		// Keep complete mutable-tail rows visible. Open code fences and tables
		// can span many lines; flattening them into one 48-cell hint hid the
		// exact content users most often need to inspect while it is streaming.
		holdback := trimRightNewlines(a.Holdback)
		holdbackLines := make([]render.Line, 0, strings.Count(holdback, "\n")+1)
		for _, row := range strings.Split(holdback, "\n") {
			holdbackLines = append(holdbackLines, render.Line{Spans: []render.Span{{
				Text:  row,
				Style: render.Style{Role: string(style.RoleTextMuted), Dim: true},
			}}})
		}
		blocks = append(blocks, render.Block{Kind: render.BlockCustom, Lines: holdbackLines})
	}
	return render.Document{Blocks: blocks}
}

// FormatPlain renders without ANSI.
func (a ActiveCell) FormatPlain(now time.Time, policy motion.Policy) string {
	return render.PlainBackend{}.Render(a.Document(now, policy))
}

// RunningToolCell builds an in-progress tool active cell.
func RunningToolCell(name string, args map[string]interface{}, now time.Time) ActiveCell {
	tc := ToolCell{
		FunctionName: name,
		Arguments:    args,
		Status:       StatusRunning,
	}
	return ActiveCell{
		Kind:         ActiveTool,
		Title:        fmt.Sprintf("tool %s", name),
		Tool:         &tc,
		ShowActivity: true,
		UpdatedAt:    now,
	}
}

func trimRightNewlines(s string) string {
	return strings.TrimRight(s, "\r\n")
}
