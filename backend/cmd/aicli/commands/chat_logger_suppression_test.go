package commands

import (
	"io"
	"os"
	"strings"
	"testing"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	logpkg "github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
)

func TestSuppressChatConsoleLoggerRoutesLogsToFileOnly(t *testing.T) {
	tempFile, err := os.CreateTemp("", "aicli-chat-suppression-*.log")
	if err != nil {
		t.Fatalf("create temp log file: %v", err)
	}
	filePath := tempFile.Name()
	if err := tempFile.Close(); err != nil {
		t.Fatalf("close temp log file: %v", err)
	}
	if err := os.Remove(filePath); err != nil {
		t.Fatalf("remove temp log file: %v", err)
	}

	originalStdout := os.Stdout
	originalStderr := os.Stderr
	stdoutReader, stdoutWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdout pipe: %v", err)
	}
	stderrReader, stderrWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stderr pipe: %v", err)
	}

	os.Stdout = stdoutWriter
	os.Stderr = stderrWriter

	restoreLogger := suppressChatConsoleLogger(&config.Config{
		Log: logpkg.LogConfig{
			Level:         "info",
			Format:        "json",
			Output:        "stdout",
			FilePath:      filePath,
			EnableConsole: true,
		},
	})
	if restoreLogger == nil {
		t.Fatal("expected chat console logger suppression to be applied")
	}
	defer restoreLogger()
	defer func() {
		os.Stdout = originalStdout
		os.Stderr = originalStderr
	}()

	logpkg.Info("chat logger suppression test", logpkg.String("scope", "chat"))
	if err := logpkg.Sync(); err != nil {
		t.Fatalf("sync logger: %v", err)
	}

	if err := stdoutWriter.Close(); err != nil {
		t.Fatalf("close stdout writer: %v", err)
	}
	if err := stderrWriter.Close(); err != nil {
		t.Fatalf("close stderr writer: %v", err)
	}

	stdoutData, err := io.ReadAll(stdoutReader)
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	stderrData, err := io.ReadAll(stderrReader)
	if err != nil {
		t.Fatalf("read stderr: %v", err)
	}

	if strings.Contains(string(stdoutData), "chat logger suppression test") {
		t.Fatalf("expected no chat log on stdout, got %q", string(stdoutData))
	}
	if strings.Contains(string(stderrData), "chat logger suppression test") {
		t.Fatalf("expected no chat log on stderr, got %q", string(stderrData))
	}

	fileData, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("read file log: %v", err)
	}
	if !strings.Contains(string(fileData), "chat logger suppression test") {
		t.Fatalf("expected file log to contain chat message, got %q", string(fileData))
	}
	if !strings.Contains(string(fileData), "\"scope\":\"chat\"") {
		t.Fatalf("expected file log to contain structured field, got %q", string(fileData))
	}
}
