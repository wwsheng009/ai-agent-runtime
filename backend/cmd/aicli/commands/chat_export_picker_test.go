package commands

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
)

func TestExecuteStructuredExportCommandExplicitCurrentFull(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	sessionDir := t.TempDir()
	manager, userID, _, err := newChatSessionManager(sessionDir)
	if err != nil {
		t.Fatalf("create session manager: %v", err)
	}
	t.Cleanup(manager.Stop)
	runtimeSession, err := manager.CreateSession(context.Background(), userID)
	if err != nil {
		t.Fatalf("create runtime session: %v", err)
	}

	outputPath := filepath.Join(t.TempDir(), "export.json")
	session := &ChatSession{
		Model:          "gpt-test",
		RuntimeSession: runtimeSession,
		SessionManager: manager,
		SessionDir:     sessionDir,
		SessionUserID:  userID,
	}
	result, handled := executeStructuredExportCommand(session, "/export current --full --output "+outputPath)
	if !handled {
		t.Fatal("/export current --full was not handled by the structured executor")
	}
	if result.Screen != nil {
		t.Fatalf("explicit /export must not open the picker, got %#v", result.Screen)
	}
	text := strings.TrimSpace(ui.RenderDocumentPlain(result.Document()))
	if !strings.Contains(text, "会话已导出") {
		t.Fatalf("export result document missing success line, got:\n%s", text)
	}
	if !strings.Contains(text, "Output File:") {
		t.Fatalf("export result document missing output file, got:\n%s", text)
	}
	if _, err := os.Stat(outputPath); err != nil {
		t.Fatalf("export file was not written: %v", err)
	}
}

func TestExecuteStructuredExportCommandInvalidArgsReportUsage(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	session := &ChatSession{}
	result, handled := executeStructuredExportCommand(session, "/export --bogus-flag")
	if !handled {
		t.Fatal("invalid /export args must be handled by the structured executor")
	}
	if result.Screen != nil {
		t.Fatalf("invalid args must not open the picker, got %#v", result.Screen)
	}
	text := strings.TrimSpace(ui.RenderDocumentPlain(result.Document()))
	if !strings.Contains(text, "用法: /export") {
		t.Fatalf("invalid args must report usage, got:\n%s", text)
	}
}

func TestExecuteStructuredExportCommandBareWithDirDegradesToCurrent(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	sessionDir := t.TempDir()
	manager, userID, _, err := newChatSessionManager(sessionDir)
	if err != nil {
		t.Fatalf("create session manager: %v", err)
	}
	t.Cleanup(manager.Stop)
	runtimeSession, err := manager.CreateSession(context.Background(), userID)
	if err != nil {
		t.Fatalf("create runtime session: %v", err)
	}

	outDir := t.TempDir()
	session := &ChatSession{
		Model:          "gpt-test",
		RuntimeSession: runtimeSession,
		SessionManager: manager,
		SessionDir:     sessionDir,
		SessionUserID:  userID,
	}
	// Bare /export with an explicit --dir but no explicit target degrades to
	// exporting the current session (no picker surface in this test session).
	result, handled := executeStructuredExportCommand(session, "/export --dir "+outDir)
	if !handled {
		t.Fatal("bare /export with --dir was not handled by the structured executor")
	}
	if result.Screen != nil {
		t.Fatalf("bare /export without a picker-capable surface must not open the picker, got %#v", result.Screen)
	}
	text := strings.TrimSpace(ui.RenderDocumentPlain(result.Document()))
	if !strings.Contains(text, "会话已导出") {
		t.Fatalf("degraded bare /export must export the current session, got:\n%s", text)
	}
}

func TestCanOpenChatExportPickerRequiresUnifiedSurface(t *testing.T) {
	if canOpenChatExportPicker(nil) {
		t.Fatal("nil session must not open the export picker")
	}
	session := &ChatSession{}
	if canOpenChatExportPicker(session) {
		t.Fatal("bare session without interaction/surface must not open the export picker")
	}
}

func TestBuildChatExportResultDocument(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	result := &chatExportResult{
		Path:      "/tmp/out.json",
		Format:    chatExportFormatFull,
		SessionID: "session-9",
		Stats:     chatSessionExportStats{MessageCount: 3, ToolCallCount: 1, ToolResultCount: 1},
	}
	commandResult := buildChatExportResultDocument(result)
	text := strings.TrimSpace(ui.RenderDocumentPlain(commandResult.Document()))
	for _, want := range []string{"会话已导出", "Session:", "Format:", "full", "Output File:", "Messages:", "Tool Calls:"} {
		if !strings.Contains(text, want) {
			t.Fatalf("export result document missing %q, got:\n%s", want, text)
		}
	}
}

func TestChatExportShouldOpenPickerOnlyForBareExport(t *testing.T) {
	cases := []struct {
		name string
		opts chatExportOptions
		want bool
	}{
		{"bare export", chatExportOptions{}, true},
		// 解析器默认把 Format 预置为 full 但不置 ExplicitFormat：裸 /export 仍应
		// 打开选择器。
		{"default format not explicit", chatExportOptions{Format: chatExportFormatFull}, true},
		{"explicit target", chatExportOptions{Target: "current", ExplicitTarget: true}, false},
		{"explicit body format", chatExportOptions{Format: chatExportFormatBody, ExplicitFormat: true}, false},
		{"explicit tools format", chatExportOptions{Format: chatExportFormatMarkdownTools, ExplicitFormat: true}, false},
		{"explicit trace format", chatExportOptions{Format: chatExportFormatMarkdownTrace, ExplicitFormat: true}, false},
		{"explicit output path", chatExportOptions{OutputPath: "/tmp/out.md"}, false},
		{"explicit dir", chatExportOptions{OutputDir: "/tmp/exports"}, false},
	}
	for _, tc := range cases {
		if got := chatExportShouldOpenPicker(tc.opts); got != tc.want {
			t.Fatalf("%s: chatExportShouldOpenPicker()=%v want %v", tc.name, got, tc.want)
		}
	}
}

func TestExecuteStructuredExportCommandExplicitFormatKeepsFormat(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	sessionDir := t.TempDir()
	manager, userID, _, err := newChatSessionManager(sessionDir)
	if err != nil {
		t.Fatalf("create session manager: %v", err)
	}
	t.Cleanup(manager.Stop)
	runtimeSession, err := manager.CreateSession(context.Background(), userID)
	if err != nil {
		t.Fatalf("create runtime session: %v", err)
	}

	session := &ChatSession{
		Model:          "gpt-test",
		RuntimeSession: runtimeSession,
		SessionManager: manager,
		SessionDir:     sessionDir,
		SessionUserID:  userID,
	}
	// 显式 --body 但省略目标：必须直接导出当前会话正文，而不是打开选择器后
	// 被硬编码的 full 覆盖。
	result, handled := executeStructuredExportCommand(session, "/export --body")
	if !handled {
		t.Fatal("/export --body was not handled by the structured executor")
	}
	if result.Screen != nil {
		t.Fatalf("explicit --body must not open the picker, got %#v", result.Screen)
	}
	text := strings.TrimSpace(ui.RenderDocumentPlain(result.Document()))
	if !strings.Contains(text, "Format:") || !strings.Contains(text, string(chatExportFormatBody)) {
		t.Fatalf("explicit --body must export the body format, got:\n%s", text)
	}
}

func TestBuildExportSessionFullScreenItemsCurrentIsSelectable(t *testing.T) {
	current := &runtimechat.Session{ID: "current-1"}
	items, selectable := buildExportSessionFullScreenItems(nil, current, time.Now())
	if len(items) != 1 || len(selectable) != 1 {
		t.Fatalf("expected 1 current row, got items=%d selectable=%d", len(items), len(selectable))
	}
	// The current session must be selectable (non-nil) for /export.
	if selectable[0] == nil {
		t.Fatalf("current session must be selectable in the export picker, got nil")
	}
	if selectable[0].ID != "current-1" {
		t.Fatalf("expected current-1, got %q", selectable[0].ID)
	}
}
