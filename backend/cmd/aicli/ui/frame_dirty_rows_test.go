package ui

import (
	"bytes"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/renderengine"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/vt"
)

// S3 验收（P3 §2 S3）：
// 1) 稳态单行变更只物化/编码/发射该行（脏行短路 + 行物化复用的帧级效果）；
// 2) PaintTrace 回归栅栏：PaintedRows 收敛到变更行数，White/Missing 恒 0；
// 3) terminalFrameCellsWindow 的 vt.Screen 复用与「每行独立 NewScreen」逐 cell 等价。

func TestTerminalSessionDirtyRowEmitsOnlyChangedViewportRow(t *testing.T) {
	var output bytes.Buffer
	session := NewTerminalSession(&output)
	trace := renderengine.NewPaintTrace()
	trace.SetEnabled(true)
	session.screen.AttachTrace(trace)
	defer session.screen.DetachTrace()

	plan := terminalSessionPlan(1, 24, 6, 4, LeaseState{})
	if result := session.Flush(plan); result.Err != nil || !result.FullRepaint {
		t.Fatalf("initial flush = %#v", result)
	}

	next := plan
	next.Rows = append([]AppScreenRow(nil), plan.Rows...)
	// viewport = 1-based rows 5..6；只改第 5 行（prompt 行），第 6 行（status）不变。
	for frame := 0; frame < 6; frame++ {
		next.Rows[4].Text = fmt.Sprintf("row-e-%d", frame)
		before := output.Len()
		result := session.Flush(next)
		if result.Err != nil || result.FullRepaint {
			t.Fatalf("flush %d = %#v", frame, result)
		}
		emitted := output.String()[before:]
		if !strings.Contains(emitted, "\x1b[5;") {
			t.Fatalf("frame %d: changed viewport row 5 was not emitted: %q", frame, emitted)
		}
		if strings.Contains(emitted, "\x1b[6;") {
			t.Fatalf("frame %d: unchanged viewport row 6 was emitted: %q", frame, emitted)
		}
		summary := trace.LastFrame()
		if summary.PaintedRows != 1 || len(summary.White) != 0 || len(summary.Missing) != 0 {
			t.Fatalf("frame %d paint summary = %#v (want exactly one painted row, no white/missing)", frame, summary)
		}
	}
	if summary := trace.LastFrame(); summary.TotalWhite != 0 || summary.TotalMissing != 0 {
		t.Fatalf("cumulative white/missing = %d/%d, want 0/0", summary.TotalWhite, summary.TotalMissing)
	}
}

func TestTerminalFrameCellsWindowReuseMatchesFreshMaterialization(t *testing.T) {
	const width, height = 16, 4
	theme := style.BuildThemeContext(style.ThemeSelection{
		PaletteName: style.PaletteFocus,
		Mode:        style.ThemeModeDark,
	}, style.ColorProfile{ColorProfile: render.TrueColorProfile(), Background: style.BackgroundDark})

	texts := []string{"alpha", "beta", "gamma", "delta"}
	rows := make([]AppScreenRow, height)
	renderRows := make([]render.Line, height)
	for index, text := range texts {
		rows[index] = AppScreenRow{Row: index + 1, Text: text}
		renderRows[index] = render.Line{Spans: []render.Span{{Text: text}}}
	}
	// 首行带角色样式：复用实现必须保证 SGR 不泄漏到后续未着色行。
	renderRows[0] = render.Line{Spans: []render.Span{{
		Text: texts[0], Style: render.Style{Role: string(style.RoleUser)},
	}}}

	got, err := terminalFrameCellsWindow(rows, renderRows, width, height, 1, height, theme)
	if err != nil {
		t.Fatalf("window materialization = %v", err)
	}
	if len(got) != height {
		t.Fatalf("materialized %d rows, want %d", len(got), height)
	}
	// 参考语义：复用引入前每行独立 NewScreen + Feed + CellRows。
	for index := range renderRows {
		screen := vt.NewScreen(width, 2)
		screen.Feed(style.RenderDocument(render.LinesDoc(renderRows[index]), theme))
		want := screen.CellRows(1, 1)[0]
		if !reflect.DeepEqual(got[index], want) {
			t.Fatalf("row %d diverged from fresh-screen materialization:\ngot  = %#v\nwant = %#v", index+1, got[index], want)
		}
	}
	styled := false
	for _, cell := range got[0] {
		if len(cell.SGR) > 0 {
			styled = true
			break
		}
	}
	if styled {
		for _, cell := range got[1] {
			if len(cell.SGR) > 0 {
				t.Fatalf("style leaked into plain row via reused screen: %#v", cell)
			}
		}
	}
}
