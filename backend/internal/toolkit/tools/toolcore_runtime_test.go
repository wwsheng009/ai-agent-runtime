package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/searchcore"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
)

type coreRun struct {
	Content  string
	Metadata map[string]interface{}
}

func runLocalCoreGrep(t *testing.T, root string, rgAvailable bool, args map[string]interface{}) coreRun {
	t.Helper()
	tool := NewGrepTool()
	tool.SetBasePath(root)
	tool.lookPath = func(string) (string, error) {
		if rgAvailable {
			return "rg", nil
		}
		return "", os.ErrNotExist
	}
	tool.runCommand = realRunGrepCommand
	handler := searchcore.NewGrepHandler(toolcore.Definition{Name: "grep", Version: "1.0.0"})
	result, err := handler.Execute(context.Background(), &toolcore.Call{
		Args:    args,
		Runtime: tool.CoreRuntime(context.Background()),
	})
	if err != nil {
		t.Fatalf("core grep (rg=%v): %v", rgAvailable, err)
	}
	converted := toolkit.ResultToToolkit(result)
	return coreRun{Content: converted.Content, Metadata: converted.Metadata}
}

// realRunGrepCommand shells out to the actual binary, mirroring runGrepCommand
// without the budget plumbing: this test only needs a real ripgrep process.
func realRunGrepCommand(ctx context.Context, binaryPath, workingDir string, args []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, binaryPath, args...)
	cmd.Dir = workingDir
	cmd.Stdin = nil
	return cmd.Output()
}

// TestLocalCoreRuntimeUsesRipgrepThroughSearcher proves the local core runtime
// reaches the shared ripgrep engine through the tool's lookPath/runCommand
// seam: engine metadata flips to ripgrep when the binary is available, and
// back to walker when it is not. Both engines must return the same content.
func TestLocalCoreRuntimeUsesRipgrepThroughSearcher(t *testing.T) {
	root := t.TempDir()
	for name, content := range map[string]string{
		"a.txt": "needle a\n",
		"b.txt": "needle b\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	walkerRun := runLocalCoreGrep(t, root, false, map[string]interface{}{"pattern": "needle", "path": "."})
	if got := walkerRun.Metadata["engine"]; got != "walker" {
		t.Fatalf("engine = %v, want walker when rg is unavailable", got)
	}

	rgRun := runLocalCoreGrep(t, root, true, map[string]interface{}{"pattern": "needle", "path": "."})
	if got := rgRun.Metadata["engine"]; got != "ripgrep" {
		t.Fatalf("engine = %v, want ripgrep when rg is available", got)
	}
	if rgRun.Content != walkerRun.Content {
		t.Fatalf("engine outputs diverged:\nwalker:\n%s\nripgrep:\n%s", walkerRun.Content, rgRun.Content)
	}
	if !strings.Contains(rgRun.Content, "a.txt") {
		t.Fatalf("expected matches in output:\n%s", rgRun.Content)
	}
}

// TestLocalCoreRuntimeFilesWithoutMatchesAcrossEngines runs files_without
// through the real local tool entry point: the same page, the same metadata and
// the same rendering on both engines. This is the mode where rg ignores --json,
// so it is the one most likely to drift between the two implementations.
func TestLocalCoreRuntimeFilesWithoutMatchesAcrossEngines(t *testing.T) {
	root := t.TempDir()
	for name, content := range map[string]string{
		"a.txt": "needle a\n",
		"b.txt": "nothing here\n",
		"c.txt": "also nothing\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	args := map[string]interface{}{
		"pattern": "needle", "path": ".", "mode": "files_without",
	}

	walkerRun := runLocalCoreGrep(t, root, false, args)
	if got := walkerRun.Metadata["engine"]; got != "walker" {
		t.Fatalf("engine = %v, want walker when rg is unavailable", got)
	}
	rgRun := runLocalCoreGrep(t, root, true, args)
	if got := rgRun.Metadata["engine"]; got != "ripgrep" {
		t.Fatalf("engine = %v, want ripgrep when rg is available", got)
	}
	if rgRun.Content != walkerRun.Content {
		t.Fatalf("engine outputs diverged:\nwalker:\n%s\nripgrep:\n%s", walkerRun.Content, rgRun.Content)
	}
	for _, want := range []string{"b.txt", "c.txt"} {
		if !strings.Contains(rgRun.Content, want) {
			t.Fatalf("expected %q in files_without output:\n%s", want, rgRun.Content)
		}
	}
	if strings.Contains(rgRun.Content, "a.txt") {
		t.Fatalf("a.txt matches and must not be listed:\n%s", rgRun.Content)
	}
	if got := rgRun.Metadata["mode"]; got != "files_without" {
		t.Fatalf("mode = %v, want files_without", got)
	}
	if got := rgRun.Metadata["match_count"]; got != 0 {
		t.Fatalf("files_without has no match lines to count, got match_count=%v", got)
	}
}

// TestLocalToolUnrestrictedLevelUsesSharedCore proves the local entry point
// routes unrestricted_level into the shared handler rather than the legacy
// path: the engine label must be the core's ("walker"/"ripgrep"), and the
// binary file must be invisible until the top level.
func TestLocalToolUnrestrictedLevelUsesSharedCore(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"plain.txt":  "needle plain\n",
		"binary.dat": "DATA\x00needle after\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	run := func(level int) *toolkit.ToolResult {
		t.Helper()
		tool := NewGrepTool()
		tool.SetBasePath(root)
		tool.lookPath = func(string) (string, error) { return "", os.ErrNotExist }
		result, err := tool.Execute(context.Background(), map[string]interface{}{
			"pattern": "needle", "path": ".", "unrestricted_level": level,
		})
		if err != nil {
			t.Fatalf("level %d: %v", level, err)
		}
		return result
	}

	defaultRun := run(0)
	if got := defaultRun.Metadata["engine"]; got != "walker" {
		t.Fatalf("unrestricted_level must route through the shared core, got engine=%v", got)
	}
	if strings.Contains(defaultRun.Content, "binary.dat") {
		t.Fatalf("the default must skip the binary file:\n%s", defaultRun.Content)
	}

	topRun := run(3)
	if got := topRun.Metadata["unrestricted_level"]; got != 3 {
		t.Fatalf("unrestricted_level = %v, want 3", got)
	}
	if !strings.Contains(topRun.Content, "binary.dat") {
		t.Fatalf("-uuu must search the binary file:\n%s", topRun.Content)
	}
	if strings.ContainsRune(topRun.Content, 0) {
		t.Fatalf("a raw NUL reached the model-visible content:\n%q", topRun.Content)
	}
}

// TestLocalToolOneFileSystemUsesSharedCore proves one_file_system routes into
// the shared handler. The boundary itself is already covered by the core tests;
// what matters here is that the local entry point does not fall back to legacy.
func TestLocalToolOneFileSystemUsesSharedCore(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("needle\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewGrepTool()
	tool.SetBasePath(root)
	tool.lookPath = func(string) (string, error) { return "", os.ErrNotExist }
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern": "needle", "path": ".", "one_file_system": true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got %v", result.Error)
	}
	if got := result.Metadata["engine"]; got != "walker" {
		t.Fatalf("one_file_system must route through the shared core, got engine=%v", got)
	}
	if got := result.Metadata["one_file_system"]; got != true {
		t.Fatalf("one_file_system metadata = %v, want true", got)
	}
	if !strings.Contains(result.Content, "a.txt") {
		t.Fatalf("expected the root file in the page:\n%s", result.Content)
	}
}

// TestLocalToolNoMessagesUsesSharedCore pins the local behaviour of the pair:
// an explicit ignore_file that cannot be read fails the call, and no_messages
// turns that into a silent skip. Both cases must run through the shared core,
// not the legacy path.
func TestLocalToolNoMessagesUsesSharedCore(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("needle\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(root, "absent.ignore")

	run := func(params map[string]interface{}) *toolkit.ToolResult {
		t.Helper()
		tool := NewGrepTool()
		tool.SetBasePath(root)
		tool.lookPath = func(string) (string, error) { return "", os.ErrNotExist }
		result, err := tool.Execute(context.Background(), params)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		return result
	}

	failing := run(map[string]interface{}{
		"pattern": "needle", "path": ".", "ignore_file": missing,
	})
	if failing.Success {
		t.Fatalf("a missing explicit ignore_file must fail:\n%s", failing.Content)
	}
	if failing.Error == nil || !strings.Contains(failing.Error.Error(), "ignore_file") {
		t.Fatalf("the error must name ignore_file, got %v", failing.Error)
	}

	skipped := run(map[string]interface{}{
		"pattern": "needle", "path": ".", "ignore_file": missing, "no_messages": true,
	})
	if !skipped.Success {
		t.Fatalf("no_messages must skip the unreadable file: %v", skipped.Error)
	}
	if got := skipped.Metadata["engine"]; got != "walker" {
		t.Fatalf("no_messages must route through the shared core, got engine=%v", got)
	}
	if got := skipped.Metadata["no_messages"]; got != true {
		t.Fatalf("no_messages metadata = %v, want true", got)
	}
	if !strings.Contains(skipped.Content, "a.txt") {
		t.Fatalf("the search must still run:\n%s", skipped.Content)
	}
}
