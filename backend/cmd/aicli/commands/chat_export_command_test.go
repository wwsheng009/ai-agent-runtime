package commands

import (
	"archive/zip"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/wwsheng009/ai-agent-runtime/internal/aiclipaths"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestHandleExportCommandFullAndBodyModes(t *testing.T) {
	outputDir := t.TempDir()
	runtimeSession := runtimechat.NewSession("tester")
	runtimeSession.ID = "session-export"
	messages := []runtimetypes.Message{
		{Role: "user", Content: "please run status", Metadata: runtimetypes.NewMetadata()},
		{
			Role:    "assistant",
			Content: "I will check.",
			ToolCalls: []runtimetypes.ToolCall{{
				ID:   "call-1",
				Name: "execute_shell_command",
				Args: map[string]interface{}{"command": "git status --short"},
			}},
			Metadata: runtimetypes.NewMetadata(),
		},
		{Role: "tool", ToolCallID: "call-1", Content: " M file.go", Metadata: runtimetypes.NewMetadata()},
		{Role: "assistant", Content: "Done.", Metadata: runtimetypes.NewMetadata()},
	}
	runtimeSession.ReplaceHistory(messages)

	session := &ChatSession{
		RuntimeSession: runtimeSession,
		Messages:       messages,
		SessionUserID:  "tester",
		NoInteractive:  true,
	}

	if quit := handleCommand(session, `/export current --full --dir "`+outputDir+`"`, false); quit {
		t.Fatal("expected /export not to exit")
	}
	fullPath := onlyFileWithExt(t, outputDir, ".json")
	fullData, err := os.ReadFile(fullPath)
	if err != nil {
		t.Fatalf("read full export: %v", err)
	}
	var envelope chatSessionExportEnvelope
	if err := json.Unmarshal(fullData, &envelope); err != nil {
		t.Fatalf("decode full export: %v\n%s", err, string(fullData))
	}
	if envelope.Stats.ToolCallCount != 1 || envelope.Stats.ToolResultCount != 1 {
		t.Fatalf("expected tool stats to be preserved, got %+v", envelope.Stats)
	}
	if envelope.Session == nil || len(envelope.Session.History) < 3 || len(envelope.Session.History[1].ToolCalls) != 1 {
		t.Fatalf("expected full export to include tool_calls, got %+v", envelope.Session)
	}

	if quit := handleCommand(session, `/export current --body --dir "`+outputDir+`"`, false); quit {
		t.Fatal("expected /export body not to exit")
	}
	bodyPath := onlyFileWithExt(t, outputDir, ".md")
	bodyData, err := os.ReadFile(bodyPath)
	if err != nil {
		t.Fatalf("read body export: %v", err)
	}
	body := string(bodyData)
	if !strings.Contains(body, "please run status") || !strings.Contains(body, "Done.") {
		t.Fatalf("expected body export to include conversation text, got:\n%s", body)
	}
	if strings.Contains(body, "execute_shell_command") || strings.Contains(body, " M file.go") {
		t.Fatalf("expected body export to omit tool details, got:\n%s", body)
	}
}

func TestExportChatSessionStreamsCompleteSQLiteCanonicalHistory(t *testing.T) {
	sessionDir := t.TempDir()
	manager, userID, _, err := newChatSessionManager(sessionDir)
	if err != nil {
		t.Fatalf("create sqlite session manager: %v", err)
	}
	t.Cleanup(manager.Stop)
	runtimeSession, err := manager.CreateSession(context.Background(), userID)
	if err != nil {
		t.Fatalf("create runtime session: %v", err)
	}
	for index := 0; index < 140; index++ {
		message := runtimetypes.NewUserMessage(fmt.Sprintf("message-%03d", index))
		if err := manager.AddMessage(context.Background(), runtimeSession.ID, *message); err != nil {
			t.Fatalf("append canonical message %d: %v", index, err)
		}
	}
	loaded, err := manager.Get(context.Background(), runtimeSession.ID)
	if err != nil {
		t.Fatalf("load bounded session: %v", err)
	}
	if len(loaded.History) >= loaded.CanonicalMessageCount {
		t.Fatalf("test requires bounded projection, got history=%d canonical=%d", len(loaded.History), loaded.CanonicalMessageCount)
	}
	outputPath := filepath.Join(t.TempDir(), "canonical-export.json")
	result, err := exportChatSession(&ChatSession{
		SessionManager: manager,
		SessionDir:     sessionDir,
		SessionUserID:  userID,
	}, chatExportOptions{Target: runtimeSession.ID, Format: chatExportFormatFull, OutputPath: outputPath})
	if err != nil {
		t.Fatalf("export canonical session: %v", err)
	}
	if result.Stats.MessageCount != 140 {
		t.Fatalf("expected 140 exported canonical messages, got %+v", result.Stats)
	}
	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read canonical export: %v", err)
	}
	var envelope chatSessionExportEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatalf("decode canonical export: %v", err)
	}
	if envelope.Session == nil || len(envelope.Session.History) != 140 {
		t.Fatalf("expected complete canonical history, got %d", len(envelope.Session.History))
	}
}

func TestExportChatSessionPreservesExistingTargetWhenCanonicalStreamFails(t *testing.T) {
	sessionDir := t.TempDir()
	manager, userID, _, err := newChatSessionManager(sessionDir)
	if err != nil {
		t.Fatalf("create sqlite session manager: %v", err)
	}
	t.Cleanup(manager.Stop)
	runtimeSession, err := manager.CreateSession(context.Background(), userID)
	if err != nil {
		t.Fatalf("create runtime session: %v", err)
	}
	largeMessage := runtimetypes.NewToolMessage("large-call", strings.Repeat("x", 600*1024))
	if err := manager.AddMessage(context.Background(), runtimeSession.ID, *largeMessage); err != nil {
		t.Fatalf("append externalized message: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(sessionDir, "session-artifacts", runtimeSession.ID)); err != nil {
		t.Fatalf("remove canonical artifact to force stream failure: %v", err)
	}
	outputPath := filepath.Join(t.TempDir(), "existing-export.json")
	const original = "existing export must survive"
	if err := os.WriteFile(outputPath, []byte(original), 0o644); err != nil {
		t.Fatalf("write existing target: %v", err)
	}
	_, err = exportChatSession(&ChatSession{
		SessionManager: manager,
		SessionDir:     sessionDir,
		SessionUserID:  userID,
	}, chatExportOptions{Target: runtimeSession.ID, Format: chatExportFormatFull, OutputPath: outputPath})
	if err == nil {
		t.Fatal("expected canonical stream failure")
	}
	data, readErr := os.ReadFile(outputPath)
	if readErr != nil {
		t.Fatalf("read preserved target: %v", readErr)
	}
	if string(data) != original {
		t.Fatalf("expected original target to remain unchanged, got %q", data)
	}
	temporaryFiles, globErr := filepath.Glob(outputPath + ".*.tmp")
	if globErr != nil {
		t.Fatalf("glob temporary export files: %v", globErr)
	}
	if len(temporaryFiles) != 0 {
		t.Fatalf("expected temporary export cleanup, got %#v", temporaryFiles)
	}
}

func TestHandleDebugCommandExportsArchive(t *testing.T) {
	outputDir := t.TempDir()
	sessionDir := t.TempDir()
	logger := NewChatLogger("openai", "openai", "gpt-test", false, "")
	if err := logger.SetLogDir(t.TempDir()); err != nil {
		t.Fatalf("set log dir: %v", err)
	}

	runtimeSession := runtimechat.NewSession("tester")
	runtimeSession.ID = "session-debug"
	sessionPath := resolveFileSessionJSONPath(sessionDir, runtimeSession.ID, runtimeSession.CreatedAt)
	if err := os.MkdirAll(filepath.Dir(sessionPath), 0o755); err != nil {
		t.Fatalf("mkdir session path: %v", err)
	}
	if err := os.WriteFile(sessionPath, []byte(`{"id":"session-debug"}`), 0o644); err != nil {
		t.Fatalf("write session file: %v", err)
	}
	if err := os.WriteFile(logger.SessionLogPath(), []byte(`{"messages":[]}`), 0o644); err != nil {
		t.Fatalf("write chat log: %v", err)
	}
	if err := os.WriteFile(logger.DebugLogPath(), []byte("[debug]\n"), 0o644); err != nil {
		t.Fatalf("write debug log: %v", err)
	}
	httpPath := filepath.Join(logger.RuntimeHTTPArtifactDir(), "001_request_gateway_client.json")
	if err := os.WriteFile(httpPath, []byte(`{"request":true}`), 0o644); err != nil {
		t.Fatalf("write http artifact: %v", err)
	}
	shellPath := filepath.Join(logger.LocalShellArtifactDir(), "001_git.txt")
	if err := os.WriteFile(shellPath, []byte("git output"), 0o644); err != nil {
		t.Fatalf("write shell artifact: %v", err)
	}
	imageDir := logger.GeneratedImagesDir()
	if err := os.MkdirAll(imageDir, 0o755); err != nil {
		t.Fatalf("mkdir image dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(imageDir, "image.png"), []byte("png"), 0o644); err != nil {
		t.Fatalf("write image artifact: %v", err)
	}

	capture := &chatRuntimeHTTPCapture{}
	capture.RecordArtifactPath("request", httpPath)
	session := &ChatSession{
		RuntimeSession:             runtimeSession,
		SessionDir:                 sessionDir,
		Logger:                     logger,
		runtimeHTTPCapture:         capture,
		lastLocalShellArtifactPath: shellPath,
	}
	if quit := handleCommand(session, `/debug export --dir "`+outputDir+`"`, false); quit {
		t.Fatal("expected /debug export not to exit")
	}
	archivePath := onlyFileWithExt(t, outputDir, ".zip")
	names := zipEntryNames(t, archivePath)
	for _, expected := range []string{
		"manifest.json",
		"session_file/session-debug.json",
		"debug_log_file/" + filepath.Base(logger.DebugLogPath()),
		"runtime_http_artifact_dir/001_request_gateway_client.json",
		"local_shell_artifact_dir/001_git.txt",
		"generated_image_artifact_dir/image.png",
	} {
		if !containsString(names, expected) {
			t.Fatalf("expected zip to contain %q, got %#v", expected, names)
		}
	}
}

func TestDebugArchiveUsesStandaloneSQLiteSnapshot(t *testing.T) {
	sessionDir := t.TempDir()
	manager, userID, _, err := newChatSessionManager(sessionDir)
	if err != nil {
		t.Fatalf("create sqlite session manager: %v", err)
	}
	t.Cleanup(manager.Stop)
	runtimeSession, err := manager.CreateSession(context.Background(), userID)
	if err != nil {
		t.Fatalf("create runtime session: %v", err)
	}
	if err := manager.AddMessage(context.Background(), runtimeSession.ID,
		*runtimetypes.NewUserMessage("committed in WAL")); err != nil {
		t.Fatalf("append runtime message: %v", err)
	}
	if err := manager.AddMessage(context.Background(), runtimeSession.ID,
		*runtimetypes.NewToolMessage("large-debug-call", strings.Repeat("artifact", 80*1024))); err != nil {
		t.Fatalf("append externalized runtime message: %v", err)
	}
	outputPath := filepath.Join(t.TempDir(), "sqlite-debug.zip")
	_, err = exportChatDebugArchive(&ChatSession{
		RuntimeSession: runtimeSession,
		SessionManager: manager,
		SessionDir:     sessionDir,
	}, chatDebugArchiveOptions{OutputPath: outputPath})
	if err != nil {
		t.Fatalf("export sqlite debug archive: %v", err)
	}

	archive, err := zip.OpenReader(outputPath)
	if err != nil {
		t.Fatalf("open sqlite debug archive: %v", err)
	}
	defer archive.Close()
	var databaseEntry *zip.File
	foundCanonicalArtifact := false
	for _, entry := range archive.File {
		if entry.Name == "session_file/"+aiclipaths.DefaultSessionHistoryFileName {
			databaseEntry = entry
		}
		if strings.HasPrefix(entry.Name, "session_file/session-artifacts/"+runtimeSession.ID+"/") &&
			strings.HasSuffix(entry.Name, ".json") {
			foundCanonicalArtifact = true
		}
	}
	if databaseEntry == nil {
		t.Fatalf("expected standalone sqlite snapshot, got %#v", zipEntryNames(t, outputPath))
	}
	if !foundCanonicalArtifact {
		t.Fatalf("expected external canonical artifact beside sqlite snapshot, got %#v", zipEntryNames(t, outputPath))
	}
	reader, err := databaseEntry.Open()
	if err != nil {
		t.Fatalf("open sqlite snapshot entry: %v", err)
	}
	snapshotPath := filepath.Join(t.TempDir(), "snapshot.sqlite")
	snapshotFile, err := os.OpenFile(snapshotPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		reader.Close()
		t.Fatalf("create extracted sqlite snapshot: %v", err)
	}
	_, copyErr := io.Copy(snapshotFile, reader)
	closeErr := snapshotFile.Close()
	readerErr := reader.Close()
	if copyErr != nil || closeErr != nil || readerErr != nil {
		t.Fatalf("extract sqlite snapshot: copy=%v close=%v reader=%v", copyErr, closeErr, readerErr)
	}

	database, err := sql.Open("sqlite3", snapshotPath)
	if err != nil {
		t.Fatalf("open extracted sqlite snapshot: %v", err)
	}
	defer database.Close()
	var messageCount int
	if err := database.QueryRow(`SELECT message_count FROM sessions WHERE id = ?`, runtimeSession.ID).Scan(&messageCount); err != nil {
		t.Fatalf("query extracted sqlite snapshot: %v", err)
	}
	if messageCount != 2 {
		t.Fatalf("expected latest committed message in snapshot, got %d", messageCount)
	}
}

func onlyFileWithExt(t *testing.T, dir, ext string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	var matches []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ext) {
			continue
		}
		matches = append(matches, filepath.Join(dir, entry.Name()))
	}
	if len(matches) != 1 {
		t.Fatalf("expected one %s file in %s, got %#v", ext, dir, matches)
	}
	return matches[0]
}

func zipEntryNames(t *testing.T, path string) []string {
	t.Helper()
	reader, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	defer reader.Close()
	names := make([]string, 0, len(reader.File))
	for _, file := range reader.File {
		names = append(names, file.Name)
	}
	return names
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func TestUniqueChatArtifactPathAvoidsSameSecondOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session_20260913_101112_full.json")
	if err := os.WriteFile(path, []byte("first"), 0o644); err != nil {
		t.Fatalf("write first export: %v", err)
	}

	// 同秒内第二次导出必须换名，否则 rename/O_TRUNC 会静默顶掉上一份产物。
	second := uniqueChatArtifactPath(path)
	if want := filepath.Join(dir, "session_20260913_101112_full-2.json"); second != want {
		t.Fatalf("second export path = %s, want %s", second, want)
	}
	if err := os.WriteFile(second, []byte("second"), 0o644); err != nil {
		t.Fatalf("write second export: %v", err)
	}
	third := uniqueChatArtifactPath(path)
	if want := filepath.Join(dir, "session_20260913_101112_full-3.json"); third != want {
		t.Fatalf("third export path = %s, want %s", third, want)
	}
	first, err := os.ReadFile(path)
	if err != nil || string(first) != "first" {
		t.Fatalf("first export was overwritten: %q %v", first, err)
	}

	// 无冲突时保持默认命名原样，空路径直接透传。
	fresh := filepath.Join(dir, "fresh.json")
	if got := uniqueChatArtifactPath(fresh); got != fresh {
		t.Fatalf("fresh path = %s, want %s", got, fresh)
	}
	if got := uniqueChatArtifactPath(""); got != "" {
		t.Fatalf("empty path = %q", got)
	}
}

func TestParseChatExportOptionsMarkdownToolFormats(t *testing.T) {
	cases := []struct {
		argument string
		format   chatExportFormat
	}{
		{"current --tools", chatExportFormatMarkdownTools},
		{"current md-tools", chatExportFormatMarkdownTools},
		{"current --format tool-calls", chatExportFormatMarkdownTools},
		{"current --trace", chatExportFormatMarkdownTrace},
		{"current --mode=md-trace", chatExportFormatMarkdownTrace},
		{"current --format=full-md", chatExportFormatMarkdownTrace},
	}
	for _, tc := range cases {
		opts, err := parseChatExportOptions(tc.argument)
		if err != nil {
			t.Fatalf("parse %q: %v", tc.argument, err)
		}
		if opts.Format != tc.format {
			t.Fatalf("parse %q format = %q, want %q", tc.argument, opts.Format, tc.format)
		}
		if !opts.ExplicitFormat {
			t.Fatalf("parse %q must mark the format explicit", tc.argument)
		}
		if !opts.ExplicitTarget || opts.Target != "current" {
			t.Fatalf("parse %q target = %q explicit=%v", tc.argument, opts.Target, opts.ExplicitTarget)
		}
	}
	if _, err := parseChatExportOptions("current --format bogus"); err == nil {
		t.Fatal("expected unknown format error")
	}
	if _, err := parseChatExportOptions("current --bogus"); err == nil {
		t.Fatal("expected unknown option error")
	}
}

func TestChatExportFormatOptionsMenuOrder(t *testing.T) {
	options := chatExportFormatOptions()
	want := []chatExportFormat{
		chatExportFormatFull,
		chatExportFormatBody,
		chatExportFormatMarkdownTools,
		chatExportFormatMarkdownTrace,
	}
	if len(options) != len(want) {
		t.Fatalf("format options = %d, want %d", len(options), len(want))
	}
	for index, format := range want {
		if options[index].Format != format {
			t.Fatalf("options[%d] = %q, want %q", index, options[index].Format, format)
		}
		if strings.TrimSpace(options[index].MenuLabel) == "" || strings.TrimSpace(options[index].PickerDetail) == "" {
			t.Fatalf("options[%d] must have both a menu label and a picker detail", index)
		}
		if mapped, ok := chatExportFormatByMenuChoice(strconv.Itoa(index+1), options); !ok || mapped != format {
			t.Fatalf("menu choice %d mapped to %q ok=%v", index+1, mapped, ok)
		}
	}
	if _, ok := chatExportFormatByMenuChoice(strconv.Itoa(len(options)+1), options); ok {
		t.Fatal("the cancel number must not map to a format")
	}
	if chatExportFormatReportsToolStats(chatExportFormatBody) {
		t.Fatal("body export must keep the compact result summary")
	}
	for _, format := range []chatExportFormat{chatExportFormatFull, chatExportFormatMarkdownTools, chatExportFormatMarkdownTrace} {
		if !chatExportFormatReportsToolStats(format) {
			t.Fatalf("%q must report tool stats", format)
		}
	}
}

func TestHandleExportCommandMarkdownToolFormats(t *testing.T) {
	runtimeSession := runtimechat.NewSession("tester")
	runtimeSession.ID = "session-export-md"
	messages := []runtimetypes.Message{
		{Role: "user", Content: "please run status", Metadata: runtimetypes.NewMetadata()},
		{
			Role:    "assistant",
			Content: "I will check.",
			ToolCalls: []runtimetypes.ToolCall{{
				ID:   "call-1",
				Name: "execute_shell_command",
				Args: map[string]interface{}{"command": "git status --short"},
			}},
			Metadata: runtimetypes.NewMetadata(),
		},
		{Role: "tool", ToolCallID: "call-1", Content: " M file.go", Metadata: runtimetypes.NewMetadata()},
		{Role: "assistant", Content: "Done.", Metadata: runtimetypes.NewMetadata()},
	}
	runtimeSession.ReplaceHistory(messages)
	session := &ChatSession{
		RuntimeSession: runtimeSession,
		Messages:       messages,
		SessionUserID:  "tester",
		NoInteractive:  true,
	}

	toolsDir := t.TempDir()
	if quit := handleCommand(session, `/export current --tools --dir "`+toolsDir+`"`, false); quit {
		t.Fatal("expected /export --tools not to exit")
	}
	toolsPath := onlyFileWithExt(t, toolsDir, ".md")
	if !strings.HasSuffix(toolsPath, "_md-tools.md") {
		t.Fatalf("expected md-tools filename token, got %s", toolsPath)
	}
	toolsData, err := os.ReadFile(toolsPath)
	if err != nil {
		t.Fatalf("read md-tools export: %v", err)
	}
	toolsDoc := string(toolsData)
	for _, want := range []string{
		"please run status", "Done.", "#### Tool Calls", "**execute_shell_command**", "(`call-1`)",
		"Input:", `"command": "git status --short"`,
	} {
		if !strings.Contains(toolsDoc, want) {
			t.Fatalf("md-tools export missing %q, got:\n%s", want, toolsDoc)
		}
	}
	if strings.Contains(toolsDoc, " M file.go") {
		t.Fatalf("md-tools export must not include tool output, got:\n%s", toolsDoc)
	}

	traceDir := t.TempDir()
	if quit := handleCommand(session, `/export current --format=md-trace --dir "`+traceDir+`"`, false); quit {
		t.Fatal("expected /export --format=md-trace not to exit")
	}
	tracePath := onlyFileWithExt(t, traceDir, ".md")
	traceData, err := os.ReadFile(tracePath)
	if err != nil {
		t.Fatalf("read md-trace export: %v", err)
	}
	traceDoc := string(traceData)
	for _, want := range []string{
		"#### Tool Calls", "**execute_shell_command**", "Input:", "Output:", " M file.go",
		`"command": "git status --short"`,
	} {
		if !strings.Contains(traceDoc, want) {
			t.Fatalf("md-trace export missing %q, got:\n%s", want, traceDoc)
		}
	}
	if strings.Contains(traceDoc, "## Unmatched Tool Results") {
		t.Fatalf("a matched tool result must not fall back to the unmatched section, got:\n%s", traceDoc)
	}
}

func TestExportChatSessionMarkdownTraceUnmatchedToolResults(t *testing.T) {
	runtimeSession := runtimechat.NewSession("tester")
	runtimeSession.ID = "session-export-orphan"
	messages := []runtimetypes.Message{
		{Role: "user", Content: "hello", Metadata: runtimetypes.NewMetadata()},
		{Role: "tool", ToolCallID: "call-orphan", Content: "orphan output", Metadata: runtimetypes.NewMetadata()},
	}
	runtimeSession.ReplaceHistory(messages)
	outputPath := filepath.Join(t.TempDir(), "orphan.md")
	result, err := exportChatSession(&ChatSession{
		RuntimeSession: runtimeSession,
		Messages:       messages,
	}, chatExportOptions{Target: "current", Format: chatExportFormatMarkdownTrace, OutputPath: outputPath})
	if err != nil {
		t.Fatalf("export md-trace with orphan result: %v", err)
	}
	if result.Stats.ToolResultCount != 1 {
		t.Fatalf("expected orphan tool result to stay in stats, got %+v", result.Stats)
	}
	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read md-trace export: %v", err)
	}
	doc := string(data)
	for _, want := range []string{"## Unmatched Tool Results", "call-orphan", "orphan output"} {
		if !strings.Contains(doc, want) {
			t.Fatalf("orphan tool result export missing %q, got:\n%s", want, doc)
		}
	}
}

func TestChatExportCodeFenceAndToolOutputTruncation(t *testing.T) {
	if got := chatExportCodeFence("plain"); got != "```" {
		t.Fatalf("plain fence = %q", got)
	}
	if got := chatExportCodeFence("a```b"); got != "````" {
		t.Fatalf("nested fence = %q", got)
	}
	long := strings.Repeat("x", chatExportMarkdownMaxToolOutput+10)
	truncated := truncateChatExportToolOutput(long)
	if !strings.Contains(truncated, "truncated: 10 bytes omitted") {
		t.Fatalf("expected truncation marker, got tail: %q", truncated[len(truncated)-64:])
	}
	multibyte := strings.Repeat("测", chatExportMarkdownMaxToolOutput)
	cut := truncateChatExportToolOutput(multibyte)
	if !utf8.ValidString(cut) {
		t.Fatal("truncated tool output must stay valid UTF-8")
	}
	if !strings.Contains(cut, "bytes omitted") {
		t.Fatalf("expected multibyte truncation marker, got tail: %q", cut[len(cut)-64:])
	}
}
