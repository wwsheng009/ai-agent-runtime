package commands

import (
	"archive/zip"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

func TestHandleDebugCommandExportsArchive(t *testing.T) {
	outputDir := t.TempDir()
	sessionDir := t.TempDir()
	logger := NewChatLogger("openai", "openai", "gpt-test", false, "")
	if err := logger.SetLogDir(t.TempDir()); err != nil {
		t.Fatalf("set log dir: %v", err)
	}

	runtimeSession := runtimechat.NewSession("tester")
	runtimeSession.ID = "session-debug"
	sessionPath := filepath.Join(sessionDir, "session-debug.json")
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
	imageDir := filepath.Join(logger.SessionDirPath(), "generated-images")
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
		"debug_log_file/debug.log",
		"runtime_http_artifact_dir/001_request_gateway_client.json",
		"local_shell_artifact_dir/001_git.txt",
		"generated_image_artifact_dir/image.png",
	} {
		if !containsString(names, expected) {
			t.Fatalf("expected zip to contain %q, got %#v", expected, names)
		}
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
