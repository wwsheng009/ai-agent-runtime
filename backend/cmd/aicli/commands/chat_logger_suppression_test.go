package commands

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	logpkg "github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
)

func TestConfigureAICLILoggerForCLIForcesFileOnlyOutput(t *testing.T) {
	enabled := true
	cfg := &config.Config{
		Log: logpkg.LogConfig{
			Level:         "debug",
			Format:        "json",
			Output:        "stdout",
			FilePath:      "gateway.log",
			EnableConsole: true,
		},
		AICLI: &config.AICLIConfig{
			Log: &config.AICLILogConfig{
				Enabled:  &enabled,
				FilePath: "aicli.log",
			},
		},
	}

	ConfigureAICLILoggerForCLI(cfg, "")

	if cfg.Log.Enabled == nil || !*cfg.Log.Enabled {
		t.Fatal("expected aicli log enabled override to be applied")
	}
	if cfg.Log.FilePath != "aicli.log" {
		t.Fatalf("expected aicli log file path override, got %q", cfg.Log.FilePath)
	}
	if cfg.Log.Output != "file" {
		t.Fatalf("expected file-only output, got %q", cfg.Log.Output)
	}
	if cfg.Log.EnableConsole {
		t.Fatal("expected console logging to be disabled")
	}
}

func TestConfigureAICLILoggerForCLICommandLineLogFileWins(t *testing.T) {
	cfg := &config.Config{
		Log: logpkg.LogConfig{Output: "stdout", FilePath: "gateway.log"},
		AICLI: &config.AICLIConfig{
			Log: &config.AICLILogConfig{FilePath: "aicli.log"},
		},
	}

	ConfigureAICLILoggerForCLI(cfg, "override.log")

	if cfg.Log.FilePath != "override.log" {
		t.Fatalf("expected command line log file override, got %q", cfg.Log.FilePath)
	}
	if cfg.Log.Output != "file" || cfg.Log.EnableConsole {
		t.Fatalf("expected file-only logging, got output=%q enable_console=%v", cfg.Log.Output, cfg.Log.EnableConsole)
	}
}

func TestConfigureAICLILoggerForCLIUsesDefaultLogFile(t *testing.T) {
	cfg := &config.Config{
		Log: logpkg.LogConfig{Output: "stdout"},
	}

	ConfigureAICLILoggerForCLI(cfg, "")

	if !strings.HasSuffix(filepath.ToSlash(cfg.Log.FilePath), "/.aicli/logs/aicli.log") {
		t.Fatalf("expected default aicli log path, got %q", cfg.Log.FilePath)
	}
	if cfg.Log.Output != "file" || cfg.Log.EnableConsole {
		t.Fatalf("expected file-only logging, got output=%q enable_console=%v", cfg.Log.Output, cfg.Log.EnableConsole)
	}
}

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
	}, nil)
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

func TestSuppressChatConsoleLoggerCreatesFallbackFileWhenFilePathMissing(t *testing.T) {
	logDir := t.TempDir()

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
	defer func() {
		os.Stdout = originalStdout
		os.Stderr = originalStderr
	}()

	restoreLogger := suppressChatConsoleLogger(&config.Config{
		Log: logpkg.LogConfig{
			Level:         "info",
			Format:        "json",
			Output:        "stdout",
			FilePath:      "",
			EnableConsole: true,
		},
	}, &chatCommandOptions{LogDir: logDir})
	if restoreLogger == nil {
		t.Fatal("expected chat console logger suppression to be applied with fallback file path")
	}
	defer restoreLogger()

	logpkg.Info("chat logger fallback test", logpkg.String("scope", "chat"))
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

	if strings.Contains(string(stdoutData), "chat logger fallback test") {
		t.Fatalf("expected no chat log on stdout, got %q", string(stdoutData))
	}
	if strings.Contains(string(stderrData), "chat logger fallback test") {
		t.Fatalf("expected no chat log on stderr, got %q", string(stderrData))
	}

	fallbackFiles, err := filepath.Glob(filepath.Join(logDir, "chat-console-*.log"))
	if err != nil {
		t.Fatalf("glob fallback log files: %v", err)
	}
	if len(fallbackFiles) != 1 {
		t.Fatalf("expected one fallback log file, got %d: %v", len(fallbackFiles), fallbackFiles)
	}

	fileData, err := os.ReadFile(fallbackFiles[0])
	if err != nil {
		t.Fatalf("read fallback log file: %v", err)
	}
	if !strings.Contains(string(fileData), "chat logger fallback test") {
		t.Fatalf("expected fallback file log to contain chat message, got %q", string(fileData))
	}
	if !strings.Contains(string(fileData), "\"scope\":\"chat\"") {
		t.Fatalf("expected fallback file log to contain structured field, got %q", string(fileData))
	}
}
