package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentEnableStreaming(t *testing.T) {
	t.Run("nil agent safe", func(t *testing.T) {
		var a *Agent
		a.EnableStreaming() // must not panic
	})

	t.Run("nil config safe", func(t *testing.T) {
		a := &Agent{}
		a.EnableStreaming() // must not panic
	})

	t.Run("sets stream option true", func(t *testing.T) {
		a := &Agent{config: &Config{Options: map[string]interface{}{}}}
		a.EnableStreaming()
		v, ok := a.config.Options["stream"]
		if !ok || !boolValue(v) {
			t.Fatalf("stream option not enabled: %#v", a.config.Options)
		}
	})

	t.Run("creates nil options map", func(t *testing.T) {
		a := &Agent{config: &Config{}}
		a.EnableStreaming()
		if a.config.Options == nil {
			t.Fatal("Options map was not created")
		}
		v, ok := a.config.Options["stream"]
		if !ok || !boolValue(v) {
			t.Fatalf("stream option not enabled: %#v", a.config.Options)
		}
	})

	t.Run("idempotent", func(t *testing.T) {
		a := &Agent{config: &Config{Options: map[string]interface{}{"stream": true}}}
		a.EnableStreaming()
		a.EnableStreaming()
		if !boolValue(a.config.Options["stream"]) {
			t.Fatalf("stream option lost: %#v", a.config.Options)
		}
	})
}

func TestNewAgentWithLLM_DefersWorkspaceScanUntilBuild(t *testing.T) {
	tmpDir := t.TempDir()
	a := NewAgentWithLLM(&Config{
		Name:  "workspace-lazy-test",
		Model: "test-model",
		Options: map[string]interface{}{
			"workspace_path": tmpDir,
		},
	}, nil, nil)
	if a == nil {
		t.Fatal("expected agent")
	}

	ctxMgr := a.GetContextManager()
	if ctxMgr == nil || ctxMgr.Workspace == nil {
		t.Fatal("expected workspace context builder")
	}

	file := filepath.Join(tmpDir, "main.go")
	if err := os.WriteFile(file, []byte("package demo\nfunc SearchDocs() {}\n"), 0644); err != nil {
		t.Fatalf("write file failed: %v", err)
	}

	ctx := ctxMgr.Workspace.Build("SearchDocs")
	if ctx == nil || ctx.Summary == "" {
		t.Fatalf("expected workspace context summary after lazy scan, got %+v", ctx)
	}
	found := false
	for _, gotFile := range ctx.Files {
		if strings.EqualFold(filepath.Clean(gotFile), filepath.Clean(file)) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected lazy scan to include %s, got %v", file, ctx.Files)
	}
}

func TestNewAgentWithLLM_AttachesProjectMemory(t *testing.T) {
	tmpDir := t.TempDir()
	a := NewAgentWithLLM(&Config{
		Name:  "project-memory-test",
		Model: "test-model",
		Options: map[string]interface{}{
			"workspace_path": tmpDir,
		},
	}, nil, nil)
	if a == nil {
		t.Fatal("expected agent")
	}
	ctxMgr := a.GetContextManager()
	if ctxMgr == nil || ctxMgr.ProjectMemory == nil {
		t.Fatal("expected project memory store attached from workspace_path")
	}

	// Disable path.
	disabled := NewAgentWithLLM(&Config{
		Name:  "project-memory-disabled",
		Model: "test-model",
		Options: map[string]interface{}{
			"workspace_path":          tmpDir,
			"context_project_memory":  false,
		},
	}, nil, nil)
	if disabled == nil {
		t.Fatal("expected agent")
	}
	if disabled.GetContextManager() != nil && disabled.GetContextManager().ProjectMemory != nil {
		t.Fatal("expected project memory to be disabled")
	}
}
