package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadRuntimeServerStartupLogTailPrefersNewContent(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "gateway.log")
	initial := "old line 1\nold line 2\n"
	if err := os.WriteFile(logPath, []byte(initial+"new line 1\nnew line 2\n"), 0o644); err != nil {
		t.Fatalf("write log file: %v", err)
	}

	tail, returnedPath, mode, err := readRuntimeServerStartupLogTail(runtimeServerStartupLogCapture{
		Path:        logPath,
		InitialSize: int64(len(initial)),
		Existed:     true,
	}, 20)
	if err != nil {
		t.Fatalf("read tail: %v", err)
	}
	if returnedPath != logPath {
		t.Fatalf("expected path %q, got %q", logPath, returnedPath)
	}
	if mode != "new_tail" {
		t.Fatalf("expected new_tail, got %q", mode)
	}
	if strings.Contains(tail, "old line") {
		t.Fatalf("expected only new content, got %q", tail)
	}
	if !strings.Contains(tail, "new line 1") || !strings.Contains(tail, "new line 2") {
		t.Fatalf("missing new lines in %q", tail)
	}
}

func TestBuildRuntimeServerStartFailureMessageIncludesLogTail(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "gateway.log")
	content := "alpha\nbeta\ngamma\n"
	if err := os.WriteFile(logPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write log file: %v", err)
	}

	message := buildRuntimeServerStartFailureMessage("runtime-server 启动失败。", nil, runtimeServerStartupLogCapture{
		Path: logPath,
	})
	if !strings.Contains(message, "日志文件: "+logPath) {
		t.Fatalf("expected log path, got %q", message)
	}
	if !strings.Contains(message, "日志尾部:") {
		t.Fatalf("expected full tail marker, got %q", message)
	}
	if !strings.Contains(message, "alpha") || !strings.Contains(message, "gamma") {
		t.Fatalf("expected log content, got %q", message)
	}
}

func TestResolveRuntimeServerLogPathExpandsTilde(t *testing.T) {
	root := t.TempDir()
	isolateRuntimeServerHome(t, root)

	configPath := filepath.Join(root, "config.yaml")
	if err := os.WriteFile(configPath, []byte("log:\n  file_path: ~/.aicli/logs/gateway.log\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	got, err := resolveRuntimeServerLogPath(configPath, filepath.Join(root, "work"))
	if err != nil {
		t.Fatalf("resolve log path: %v", err)
	}
	expected := filepath.Join(root, "home", ".aicli", "logs", "gateway.log")
	if got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestTailTextLinesReturnsLastLinesOnly(t *testing.T) {
	got := tailTextLines("a\nb\nc\nd\n", 2)
	if got != "c\nd" {
		t.Fatalf("expected last 2 lines, got %q", got)
	}
}
