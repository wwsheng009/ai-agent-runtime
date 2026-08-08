package commands

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// uniqueTranscriptRow returns the single 1-based screen row whose visible text
// contains marker, failing when the marker is missing or ambiguous. The display
// plane must place each transcript line on exactly one row.
func uniqueTranscriptRow(t *testing.T, screen *screenVT, marker string) int {
	t.Helper()
	rows := screen.RowsContaining(marker)
	if len(rows) != 1 {
		t.Fatalf("expected %q on exactly one row, got %v\n%s", marker, rows, screen.dump())
	}
	return rows[0]
}

// TestPrintVisibleChatHistory_ScreenSeparatesFinalToolCells lifts the tool-cell
// boundary contract from the content plane onto the display plane. Running is
// viewport-only (ActiveBand); history scrollback only contains final cells.
// Each cell is internally dense and adjacent cells have one separator.
func TestPrintVisibleChatHistory_ScreenSeparatesFinalToolCells(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	const width, height = 80, 24

	assistant := runtimetypes.Message{
		Role:    "assistant",
		Content: "我先查看目录。",
		ToolCalls: []runtimetypes.ToolCall{
			{ID: "call-1", Name: "ls", Args: map[string]interface{}{"path": "docs"}},
			{ID: "call-2", Name: "read_file", Args: map[string]interface{}{"path": "docs/README.md"}},
		},
		Metadata: runtimetypes.NewMetadata(),
	}
	tool1 := runtimetypes.NewToolMessage("call-1", "README.md")
	tool2 := runtimetypes.NewToolMessage("call-2", "# Docs")

	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	coord.stableCommitDelay = time.Hour
	t.Cleanup(coord.Shutdown)
	session.Interaction = coord

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(width, height)
	session.Surface = surface
	coord.SetSurface(surface)

	replaceRuntimeMessages(session, []runtimetypes.Message{
		*runtimetypes.NewUserMessage("查看 docs"),
		assistant,
		*tool1,
		*tool2,
		*runtimetypes.NewAssistantMessage("目录里有 README。"),
	})

	screen := newScreenVT(width, height)
	var count int
	stream := captureSurfaceStdout(t, func() {
		coord.SetWriter(os.Stdout)
		// Mimic the interactive submit path: a prompt is shown and then cleared
		// before already-final history is replayed into the scrollable output
		// region. This is the path that used to attach ClearPrompt scroll debt
		// to the first transcript write.
		surface.ShowPrompt("> ")
		surface.ClearPromptRows(1)
		count = printVisibleChatHistory(session, "已加载历史会话")
		surface.ShowPrompt("> ")
	})
	screen.feed(stream)

	if count != 5 {
		t.Fatalf("expected 5 visible history messages, got %d\n%s", count, screen.dump())
	}

	// Anchor every transcript line on the reconstructed screen.
	rowHeader := uniqueTranscriptRow(t, screen, "已加载历史会话 (5 条消息):")
	rowUser := uniqueTranscriptRow(t, screen, "查看 docs")
	rowIntro := uniqueTranscriptRow(t, screen, "我先查看目录。")
	rowCompLs := uniqueTranscriptRow(t, screen, "Completed ls path=docs")
	rowCompRead := uniqueTranscriptRow(t, screen, "Completed read_file path=docs/README.md")
	rowFinal := uniqueTranscriptRow(t, screen, "目录里有 README。")

	// Each tool cell is internally dense, but the next independent final cell
	// starts after one separator row.
	if got := strings.TrimSpace(screen.line(rowCompLs + 1)); got != "└  README.md" {
		t.Fatalf("expected ls output directly under Completed ls, got %q\n%s", got, screen.dump())
	}
	if rowCompRead != rowCompLs+3 {
		t.Fatalf("Completed read_file must follow one blank after the ls output: compLs=%d compRead=%d\n%s",
			rowCompLs, rowCompRead, screen.dump())
	}
	if !screen.Blank(rowCompLs + 2) {
		t.Fatalf("expected a blank row between final tool cells\n%s", screen.dump())
	}
	if got := strings.TrimSpace(screen.line(rowCompRead + 1)); got != "└  # Docs" {
		t.Fatalf("expected read_file output directly under Completed read_file, got %q\n%s", got, screen.dump())
	}

	// Single-blank separators between top-level blocks: header/user dense, then
	// one blank before the assistant intro, one blank before the first async
	// line, and one blank before the trailing assistant block.
	if rowUser != rowHeader+1 {
		t.Fatalf("header and user echo must stay dense: header=%d user=%d\n%s", rowHeader, rowUser, screen.dump())
	}
	if rowIntro != rowUser+2 {
		t.Fatalf("expected one blank between user echo and assistant intro: user=%d intro=%d\n%s",
			rowUser, rowIntro, screen.dump())
	}
	if rowCompLs != rowIntro+2 {
		t.Fatalf("expected one blank between assistant intro and first async line: intro=%d comp=%d\n%s",
			rowIntro, rowCompLs, screen.dump())
	}
	rowDocs := rowCompRead + 1
	if rowFinal != rowDocs+2 {
		t.Fatalf("expected one blank between tool block and trailing assistant: docs=%d final=%d\n%s",
			rowDocs, rowFinal, screen.dump())
	}

	// No run of two or more blank rows anywhere inside the replayed transcript.
	for row := rowHeader; row < rowFinal; row++ {
		if screen.Blank(row) && screen.Blank(row+1) {
			t.Fatalf("unexpected consecutive blank rows at %d-%d inside transcript\n%s",
				row, row+1, screen.dump())
		}
	}
}
