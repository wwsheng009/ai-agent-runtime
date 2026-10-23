package remotetools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRemoteGrepContextIsEngineIndependent runs one context query through the
// handler twice — once with the resident ripgrep engine, once with the pure-Go
// walker — and requires identical content and metadata. Context is produced by
// the shared expander in searchcore, so rg never receives --context and the
// engines cannot render different windows.
func TestRemoteGrepContextIsEngineIndependent(t *testing.T) {
	cfg, workspace := newSearchFixture(t)
	content := "one\ntwo\nMATCH three\nfour\nfive\nsix\nMATCH seven\neight\n"
	if err := os.WriteFile(filepath.Join(cfg.Workspace, "ctx.txt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	args := map[string]interface{}{"pattern": "MATCH", "path": "ctx.txt", "context": 1}
	walker := newRemoteGrepTool(cfg, workspace)
	walkerResult, err := walker.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if !walkerResult.Success {
		t.Fatalf("walker grep failed: %+v", walkerResult)
	}

	for _, want := range []string{
		"ctx.txt-2-two",
		"ctx.txt:3:MATCH three",
		"ctx.txt-4-four",
		"--",
		"ctx.txt-6-six",
		"ctx.txt:7:MATCH seven",
		"ctx.txt-8-eight",
	} {
		if !strings.Contains(walkerResult.Content, want) {
			t.Fatalf("walker output missing %q:\n%s", want, walkerResult.Content)
		}
	}
	if got := walkerResult.Metadata["context_lines"]; got != 6 {
		t.Fatalf("context_lines = %v, want 6:\n%s", got, walkerResult.Content)
	}

	rg := resolveRipgrepRunner()
	if !rg.available() {
		t.Skip("ripgrep is not available")
	}
	engineTool := newRemoteGrepTool(cfg, workspace)
	engineTool.rg = rg
	engineResult, err := engineTool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if engineResult.Content != walkerResult.Content {
		t.Fatalf("engine rendering diverged:\nwalker:\n%s\nripgrep:\n%s", walkerResult.Content, engineResult.Content)
	}
	for _, key := range []string{"matches", "context_before", "context_after", "context_lines"} {
		if engineResult.Metadata[key] != walkerResult.Metadata[key] {
			t.Fatalf("metadata[%q]: ripgrep=%v walker=%v", key, engineResult.Metadata[key], walkerResult.Metadata[key])
		}
	}
	if got := engineResult.Metadata["engine"]; got != "ripgrep" {
		t.Fatalf("engine = %v, want ripgrep", got)
	}
}

// TestRemoteGrepContextClampsInput pins the shared cap: a caller cannot force
// an unbounded re-read through before/after context.
func TestRemoteGrepContextClampsInput(t *testing.T) {
	cfg, workspace := newSearchFixture(t)
	content := ""
	for i := 1; i <= 30; i++ {
		content += "line\n"
	}
	content += "MATCH\n"
	if err := os.WriteFile(filepath.Join(cfg.Workspace, "long.txt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := newRemoteGrepTool(cfg, workspace)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern": "MATCH", "path": "long.txt", "before_context": 500,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Metadata["context_before"]; got != 10 {
		t.Fatalf("context_before = %v, want clamped 10", got)
	}
	if got := result.Metadata["context_lines"]; got != 11 {
		t.Fatalf("context_lines = %v, want 11", got)
	}
}
