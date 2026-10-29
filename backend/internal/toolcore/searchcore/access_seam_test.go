package searchcore

// S1 of docs/plan/searchcore-io-seam-migration-20261028.md: the handler-level host
// reads (the glob search root, and the anchor decision for ignore_file/pattern_file)
// now go through the I/O seam.
//
// These tests are the difference between a seam and a decoration. A glob root is the
// one existence question that must not be answered by the machine running the
// process: if the core stats locally while the caller means the target, a directory
// that happens to share the name turns "no such path" into a successful empty search,
// and the caller has no way to know the two are unrelated.

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
)

// countingAccess answers Stat from a set the test controls, and records every
// question asked. Everything else is present only to satisfy the interface; ls and
// glob still walk the host tree here, which is S5's job, so a test that leaned on
// ReadDir would be asserting behaviour this step does not own.
type countingAccess struct {
	statCalls int
	queried   []string
	// absent lists paths this accessor insists do not exist, even though the local
	// filesystem may disagree. dirs lists the ones it agrees are directories.
	absent map[string]bool
	dirs   map[string]bool
}

type absentErr struct{ path string }

func (e absentErr) Error() string { return "remote: no such path " + e.path }
func (absentErr) NotExist() bool  { return true }

type fakeInfo struct {
	name  string
	isDir bool
}

func (i fakeInfo) Name() string { return i.name }
func (i fakeInfo) Size() int64  { return 0 }
func (i fakeInfo) Mode() fs.FileMode {
	if i.isDir {
		return fs.ModeDir | 0o755
	}
	return 0o644
}
func (i fakeInfo) ModTime() time.Time { return time.Time{} }
func (i fakeInfo) IsDir() bool        { return i.isDir }
func (i fakeInfo) Sys() any           { return nil }

func (a *countingAccess) Stat(_ context.Context, path string) (fs.FileInfo, error) {
	a.statCalls++
	a.queried = append(a.queried, path)
	if a.absent[path] {
		return nil, absentErr{path: path}
	}
	// A path is a directory only when the test says so: the default is the boring
	// one, so an assertion that "the accessor called this a file" cannot pass by
	// accident of an unset map.
	return fakeInfo{name: filepath.Base(path), isDir: a.dirs[path]}, nil
}

func (a *countingAccess) Lstat(ctx context.Context, path string) (fs.FileInfo, error) {
	return a.Stat(ctx, path)
}

func (a *countingAccess) Open(context.Context, string) (toolcore.FileHandle, error) {
	return nil, absentErr{path: "reading files is S2's scope, not this step's"}
}

func (a *countingAccess) ReadDir(context.Context, string) ([]fs.DirEntry, error) {
	return nil, absentErr{path: "directory walking is S5's scope, not this step's"}
}

// seamRuntime is a surface that supplies its own filesystem.
type seamRuntime struct {
	presentationRuntime
	access toolcore.Access
}

func (r seamRuntime) FileAccess() toolcore.Access { return r.access }

func runGlobWith(t *testing.T, root string, access toolcore.Access, args map[string]any) *toolcore.Result {
	t.Helper()
	handler := NewGlobHandler(toolcore.Definition{Name: "glob", Version: "1.0.0"})
	var runtime toolcore.Runtime = presentationRuntime{paths: presentationPaths{root: root}}
	if access != nil {
		runtime = seamRuntime{presentationRuntime: presentationRuntime{paths: presentationPaths{root: root}}, access: access}
	}
	result, err := handler.Execute(context.Background(), &toolcore.Call{Args: args, Runtime: runtime})
	if err != nil {
		t.Fatalf("glob returned a Go error, want a result: %v", err)
	}
	if result == nil {
		t.Fatal("glob returned a nil result")
	}
	return result
}

// TestGlobRootAbsenceIsTheAccessorsAnswer: the directory exists on this machine, and
// the injected accessor says it does not. The handler must believe the accessor.
func TestGlobRootAbsenceIsTheAccessorsAnswer(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("package x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	access := &countingAccess{absent: map[string]bool{root: true}}

	result := runGlobWith(t, root, access, map[string]any{"pattern": "*.go"})
	if result.Success {
		t.Fatalf("glob answered from the local filesystem although the accessor called the root absent:\n%s", result.Content)
	}
	if want := toolcore.NormalizeErrorCode(toolcore.ErrNotFound); result.ErrorCode != want {
		t.Fatalf("error code = %q, want %q", result.ErrorCode, want)
	}
	if access.statCalls != 1 {
		t.Fatalf("statCalls = %d, want exactly 1 (the root question must be asked once, of the accessor)", access.statCalls)
	}
}

// TestGlobWithoutAnAccessorStaysLocal is the other half of "no behaviour change": a
// Runtime that provides no accessor keeps getting the local filesystem, so every
// unconverted surface (and every pre-existing test) behaves exactly as before.
func TestGlobWithoutAnAccessorStaysLocal(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "b.go"), []byte("package x\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	result := runGlobWith(t, root, nil, map[string]any{"pattern": "*.go"})
	if !result.Success {
		t.Fatalf("the local default regressed: %s", result.Content)
	}
	if !strings.Contains(result.Content, "b.go") {
		t.Fatalf("glob did not list the local match:\n%s", result.Content)
	}
}

// TestGlobMissingRootStillClassifiesAsAbsence pins the classification the handler now
// shares with every other core: fs.ErrNotExist locally and NotExistError remotely both
// mean "not there", while a permission failure must not.
func TestGlobMissingRootStillClassifiesAsAbsence(t *testing.T) {
	root := t.TempDir()
	result := runGlobWith(t, root, nil, map[string]any{"pattern": "*.go", "path": "nowhere-at-all"})
	if want := toolcore.NormalizeErrorCode(toolcore.ErrNotFound); result.ErrorCode != want {
		t.Fatalf("a locally missing root must still be NOT_FOUND, got code %q (%s)", result.ErrorCode, result.Content)
	}

	liar := &countingAccess{absent: map[string]bool{}, dirs: map[string]bool{}}
	liar.absent[filepath.Join(root, "nowhere-at-all")] = true
	blocked := runGlobWith(t, root, liar, map[string]any{"pattern": "*.go", "path": "nowhere-at-all"})
	if blocked.ErrorCode != toolcore.NormalizeErrorCode(toolcore.ErrNotFound) {
		t.Fatalf("accessor absence must classify identically to local absence: %q", blocked.ErrorCode)
	}
}

// TestGrepAsksTheAccessorAboutTheAnchor covers the other two S1 sites: the decision
// "is the search root a file, so a relative pattern_file anchors to its parent" is
// made on the accessor's answer, not on this process's disk.
func TestGrepAsksTheAccessorAboutTheAnchor(t *testing.T) {
	root := t.TempDir()
	anchor := filepath.Join(root, "workspace")
	if err := os.MkdirAll(anchor, 0o755); err != nil {
		t.Fatal(err)
	}
	// The accessor claims the anchor is a FILE. The handler must then anchor the
	// relative pattern_file to its parent, which is the decision this step moved.
	access := &countingAccess{dirs: map[string]bool{}}
	runtime := seamRuntime{
		presentationRuntime: presentationRuntime{paths: presentationPaths{root: anchor}},
		access:              access,
	}
	handler := NewGrepHandler(toolcore.Definition{Name: "grep", Version: "1.0.0"})
	result, err := handler.Execute(context.Background(), &toolcore.Call{
		Args:    map[string]any{"path": ".", "pattern": "x", "pattern_file": "patterns.txt"},
		Runtime: runtime,
	})
	if err != nil {
		t.Fatalf("grep returned a Go error: %v", err)
	}
	if access.statCalls == 0 {
		t.Fatalf("the anchor decision never asked the accessor (result: %s)", result.Content)
	}
	found := false
	for _, queried := range access.queried {
		if queried == anchor {
			found = true
		}
	}
	if !found {
		t.Fatalf("the handler never asked the accessor about the search root %q, asked: %v", anchor, access.queried)
	}
}
