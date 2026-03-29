package tools

import (
	"context"
	stderrors "errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	runtimeerrors "github.com/ai-gateway/ai-agent-runtime/internal/errors"
	runtimeexecutor "github.com/ai-gateway/ai-agent-runtime/internal/executor"
	"github.com/ai-gateway/ai-agent-runtime/internal/toolkit"
)

type sandboxAwareTool interface {
	toolkit.Tool
	SetSandbox(*runtimeexecutor.Sandbox)
}

func TestFileTools_RespectSandboxPathPolicy(t *testing.T) {
	root := t.TempDir()
	allowedDir := filepath.Join(root, "allowed")
	deniedDir := filepath.Join(root, "denied")
	mustMkdirAll(t, allowedDir)
	mustMkdirAll(t, deniedDir)

	allowedFile := filepath.Join(allowedDir, "sample.txt")
	deniedFile := filepath.Join(deniedDir, "sample.txt")
	mustWriteFile(t, allowedFile, "line1\nline2\n")
	mustWriteFile(t, deniedFile, "secret\n")

	sandbox := runtimeexecutor.NewSandbox(&runtimeexecutor.SandboxConfig{
		AllowedPaths: []string{allowedDir},
		DeniedPaths:  []string{deniedDir},
	})

	tests := []struct {
		name   string
		tool   sandboxAwareTool
		params map[string]interface{}
	}{
		{
			name: "view denied file",
			tool: NewViewTool(),
			params: map[string]interface{}{
				"file_path": deniedFile,
			},
		},
		{
			name: "write denied file",
			tool: NewWriteTool(),
			params: map[string]interface{}{
				"file_path": deniedFile,
				"content":   "x",
			},
		},
		{
			name: "edit denied file",
			tool: NewEditTool(),
			params: map[string]interface{}{
				"file_path":  deniedFile,
				"old_string": "secret",
				"new_string": "public",
			},
		},
		{
			name: "multiedit denied file",
			tool: NewMultieditTool(),
			params: map[string]interface{}{
				"file_path": deniedFile,
				"edits": []interface{}{
					map[string]interface{}{"old_string": "secret", "new_string": "public"},
				},
			},
		},
		{
			name: "glob denied path",
			tool: NewGlobTool(),
			params: map[string]interface{}{
				"path":    deniedDir,
				"pattern": "*.txt",
			},
		},
		{
			name: "grep denied path",
			tool: NewGrepTool(),
			params: map[string]interface{}{
				"path":    deniedDir,
				"pattern": "secret",
			},
		},
		{
			name: "ls denied path",
			tool: NewLsTool(),
			params: map[string]interface{}{
				"path": deniedDir,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.tool.SetSandbox(sandbox)
			result, err := tt.tool.Execute(context.Background(), tt.params)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.Success {
				t.Fatalf("expected sandbox denial, got success: %s", result.Content)
			}
			if result.Error == nil || !strings.Contains(strings.ToLower(result.Error.Error()), "sandbox") {
				t.Fatalf("expected sandbox error, got: %v", result.Error)
			}
			var runtimeErr *runtimeerrors.RuntimeError
			if !stderrors.As(result.Error, &runtimeErr) {
				t.Fatalf("expected runtime error, got %T %v", result.Error, result.Error)
			}
			if runtimeErr.Code != runtimeerrors.ErrAgentPermission {
				t.Fatalf("expected AGENT_PERMISSION, got %s", runtimeErr.Code)
			}
		})
	}

	allowedGlob := NewGlobTool()
	allowedGlob.SetSandbox(sandbox)
	result, err := allowedGlob.Execute(context.Background(), map[string]interface{}{
		"path":    allowedDir,
		"pattern": "*.txt",
	})
	if err != nil {
		t.Fatalf("unexpected error for allowed glob: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected allowed glob to succeed, got: %v", result.Error)
	}
}

func TestGlobTool_RejectsTraversalPatternUnderSandbox(t *testing.T) {
	root := t.TempDir()
	mustMkdirAll(t, root)

	tool := NewGlobTool()
	tool.SetSandbox(runtimeexecutor.NewSandbox(&runtimeexecutor.SandboxConfig{
		AllowedPaths: []string{root},
	}))

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"path":    root,
		"pattern": "../*.txt",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatal("expected traversal pattern to be rejected")
	}
	if result.Error == nil || !strings.Contains(strings.ToLower(result.Error.Error()), "sandbox") {
		t.Fatalf("expected sandbox traversal error, got: %v", result.Error)
	}
}

func TestWriteAndEditTools_RespectReadOnlyPaths(t *testing.T) {
	root := t.TempDir()
	readOnlyDir := filepath.Join(root, "readonly")
	mustMkdirAll(t, readOnlyDir)
	target := filepath.Join(readOnlyDir, "sample.txt")
	mustWriteFile(t, target, "before\n")

	sandbox := runtimeexecutor.NewSandbox(&runtimeexecutor.SandboxConfig{
		AllowedPaths:  []string{root},
		ReadOnlyPaths: []string{readOnlyDir},
	})

	writeTool := NewWriteTool()
	writeTool.SetSandbox(sandbox)
	writeResult, err := writeTool.Execute(context.Background(), map[string]interface{}{
		"file_path": target,
		"content":   "after\n",
	})
	if err != nil {
		t.Fatalf("unexpected write error: %v", err)
	}
	if writeResult.Success {
		t.Fatal("expected write to readonly path to fail")
	}

	editTool := NewEditTool()
	editTool.SetSandbox(sandbox)
	editResult, err := editTool.Execute(context.Background(), map[string]interface{}{
		"file_path":  target,
		"old_string": "before",
		"new_string": "after",
	})
	if err != nil {
		t.Fatalf("unexpected edit error: %v", err)
	}
	if editResult.Success {
		t.Fatal("expected edit to readonly path to fail")
	}
}

func TestBashTool_RespectsSandboxCommandPolicy(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	sandbox := runtimeexecutor.NewSandbox(&runtimeexecutor.SandboxConfig{
		AllowedPaths:     []string{wd},
		AllowedCommands:  []string{"echo"},
		DeniedCommands:   []string{"whoami"},
		EnvWhitelist:     []string{"PATH", "SystemRoot", "ComSpec"},
		MaxExecutionTime: 2 * time.Second,
	})

	tool := NewBashTool()
	tool.SetSandbox(sandbox)

	allowedResult, err := tool.Execute(context.Background(), map[string]interface{}{
		"command": "echo sandbox-ok",
	})
	if err != nil {
		t.Fatalf("unexpected error for allowed command: %v", err)
	}
	if !allowedResult.Success {
		t.Fatalf("expected allowed command to succeed, got: %v", allowedResult.Error)
	}

	deniedResult, err := tool.Execute(context.Background(), map[string]interface{}{
		"command": "whoami",
	})
	if err != nil {
		t.Fatalf("unexpected error for denied command: %v", err)
	}
	if deniedResult.Success {
		t.Fatal("expected denied command to fail")
	}
	if deniedResult.Error == nil || !strings.Contains(strings.ToLower(deniedResult.Error.Error()), "sandbox") {
		t.Fatalf("expected sandbox command error, got: %v", deniedResult.Error)
	}
}

func TestBashTool_ReadOnlySandboxBlocksShellLauncher(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	sandbox := runtimeexecutor.NewSandbox(&runtimeexecutor.SandboxConfig{
		AllowedPaths:    []string{wd},
		AllowedCommands: []string{"echo"},
		DeniedCommands:  []string{"sh", "cmd", "powershell", "pwsh"},
		EnvWhitelist:    []string{"PATH", "SystemRoot", "ComSpec"},
	})

	tool := NewBashTool()
	tool.SetSandbox(sandbox)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"command": "echo sandbox-ok",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatal("expected read-only shell launcher to be blocked")
	}
	if result.Error == nil || !strings.Contains(strings.ToLower(result.Error.Error()), "shell launcher") {
		t.Fatalf("expected shell launcher denial, got: %v", result.Error)
	}
}

func TestDownloadTool_RespectsSandboxWritePolicy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("sandbox-download"))
	}))
	defer server.Close()

	root := t.TempDir()
	allowedDir := filepath.Join(root, "allowed")
	deniedDir := filepath.Join(root, "denied")
	mustMkdirAll(t, allowedDir)
	mustMkdirAll(t, deniedDir)

	sandbox := runtimeexecutor.NewSandbox(&runtimeexecutor.SandboxConfig{
		AllowedPaths: []string{allowedDir},
		DeniedPaths:  []string{deniedDir},
	})

	tool := NewDownloadTool()
	tool.SetSandbox(sandbox)

	deniedPath := filepath.Join(deniedDir, "sample.txt")
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"url":       server.URL,
		"file_path": deniedPath,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatal("expected denied download path to fail")
	}
	if result.Error == nil || !strings.Contains(strings.ToLower(result.Error.Error()), "sandbox") {
		t.Fatalf("expected sandbox denial, got: %v", result.Error)
	}

	allowedPath := filepath.Join(allowedDir, "sample.txt")
	allowedResult, err := tool.Execute(context.Background(), map[string]interface{}{
		"url":       server.URL,
		"file_path": allowedPath,
	})
	if err != nil {
		t.Fatalf("unexpected error on allowed path: %v", err)
	}
	if !allowedResult.Success {
		t.Fatalf("expected allowed download to succeed, got: %v", allowedResult.Error)
	}
}

func TestFetchTool_RespectsSandboxNetworkPolicy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("sandbox-fetch"))
	}))
	defer server.Close()

	tool := NewFetchTool()
	tool.SetSandbox(runtimeexecutor.NewSandbox(&runtimeexecutor.SandboxConfig{
		DeniedHosts: []string{"127.0.0.1", "localhost"},
	}))

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"url": server.URL,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatal("expected fetch to be denied by sandbox network policy")
	}
	if result.Error == nil || !strings.Contains(strings.ToLower(result.Error.Error()), "sandbox") {
		t.Fatalf("expected sandbox denial, got: %v", result.Error)
	}
}

func TestWebSearchAndSourcegraph_RespectSandboxNetworkPolicy(t *testing.T) {
	sandbox := runtimeexecutor.NewSandbox(&runtimeexecutor.SandboxConfig{
		DeniedHosts: []string{"duckduckgo.com", "sourcegraph.com"},
	})

	webSearch := NewWebSearchTool()
	webSearch.SetSandbox(sandbox)
	searchResult, err := webSearch.Execute(context.Background(), map[string]interface{}{
		"query": "sandbox policy",
	})
	if err != nil {
		t.Fatalf("unexpected web_search error: %v", err)
	}
	if searchResult.Success {
		t.Fatal("expected web_search to be denied by sandbox network policy")
	}
	if searchResult.Error == nil || !strings.Contains(strings.ToLower(searchResult.Error.Error()), "sandbox") {
		t.Fatalf("expected sandbox denial, got: %v", searchResult.Error)
	}

	sourcegraph := NewSourcegraphTool()
	sourcegraph.SetSandbox(sandbox)
	sourcegraphResult, err := sourcegraph.Execute(context.Background(), map[string]interface{}{
		"query": "repo:openai/gpt",
	})
	if err != nil {
		t.Fatalf("unexpected sourcegraph error: %v", err)
	}
	if sourcegraphResult.Success {
		t.Fatal("expected sourcegraph to be denied by sandbox network policy")
	}
	if sourcegraphResult.Error == nil || !strings.Contains(strings.ToLower(sourcegraphResult.Error.Error()), "sandbox") {
		t.Fatalf("expected sandbox denial, got: %v", sourcegraphResult.Error)
	}
}

func TestTodosTool_RespectsSandboxWritePolicy(t *testing.T) {
	root := t.TempDir()
	allowedDir := filepath.Join(root, "allowed")
	deniedDir := filepath.Join(root, "denied")
	mustMkdirAll(t, allowedDir)
	mustMkdirAll(t, deniedDir)

	sandbox := runtimeexecutor.NewSandbox(&runtimeexecutor.SandboxConfig{
		AllowedPaths: []string{allowedDir},
		DeniedPaths:  []string{deniedDir},
	})

	tool := NewTodosTool()
	tool.SetSandbox(sandbox)
	tool.storage = filepath.Join(deniedDir, "todos.json")

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"todos": []interface{}{
			map[string]interface{}{
				"content":     "Task 1",
				"status":      "pending",
				"active_form": "Doing Task 1",
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected todos fallback to succeed, got: %v", result.Error)
	}
	if mode, ok := result.Metadata["storage_mode"].(string); !ok || mode != "memory" {
		t.Fatalf("expected memory storage mode, got %#v", result.Metadata["storage_mode"])
	}

	tool.storage = filepath.Join(allowedDir, "todos.json")
	allowedResult, err := tool.Execute(context.Background(), map[string]interface{}{
		"todos": []interface{}{
			map[string]interface{}{
				"content":     "Task 1",
				"status":      "pending",
				"active_form": "Doing Task 1",
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error on allowed path: %v", err)
	}
	if !allowedResult.Success {
		t.Fatalf("expected todos write to allowed path to succeed, got: %v", allowedResult.Error)
	}
	if mode, ok := allowedResult.Metadata["storage_mode"].(string); !ok || mode != "file" {
		t.Fatalf("expected file storage mode, got %#v", allowedResult.Metadata["storage_mode"])
	}
}

func mustMkdirAll(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
