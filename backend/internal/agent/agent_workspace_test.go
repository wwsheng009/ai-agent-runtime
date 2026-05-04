package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
