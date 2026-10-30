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
	"bytes"
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
	size  int64
	isDir bool
}

func (i fakeInfo) Name() string { return i.name }
func (i fakeInfo) Size() int64  { return i.size }
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

// --- S2a: the candidate file's bytes come from the accessor ---

// virtualAccess serves file contents the test chooses, while the host filesystem
// holds something deliberately different at the same path. That asymmetry is the
// point: if any part of the search opened the disk directly, the answer would come
// from bytes the accessor never offered, and the assertion fails loudly.
type virtualAccess struct {
	files     map[string][]byte
	openCalls int
	opened    []string
}

type virtualHandle struct {
	*bytes.Reader
	info   fs.FileInfo
	closed bool
}

func (h *virtualHandle) Stat() (fs.FileInfo, error) { return h.info, nil }

func (h *virtualHandle) Close() error {
	h.closed = true
	return nil
}

func (a *virtualAccess) Stat(_ context.Context, path string) (fs.FileInfo, error) {
	body, ok := a.files[path]
	if !ok {
		return nil, absentErr{path: path}
	}
	// The size the accessor reports is the size of the bytes it would serve, which
	// is what the walker's MaxFileBytes gate must compare against.
	return fakeInfo{name: filepath.Base(path), size: int64(len(body))}, nil
}

func (a *virtualAccess) Lstat(ctx context.Context, path string) (fs.FileInfo, error) {
	return a.Stat(ctx, path)
}

func (a *virtualAccess) Open(_ context.Context, path string) (toolcore.FileHandle, error) {
	body, ok := a.files[path]
	if !ok {
		return nil, absentErr{path: path}
	}
	a.openCalls++
	a.opened = append(a.opened, path)
	return &virtualHandle{Reader: bytes.NewReader(body), info: fakeInfo{name: filepath.Base(path), size: int64(len(body))}}, nil
}

func (a *virtualAccess) ReadDir(context.Context, string) ([]fs.DirEntry, error) {
	return nil, absentErr{path: "directory walking is S5, not this step"}
}

// TestGrepSearchesTheAccessorsBytes: the disk holds text with no match, the accessor
// holds the match. A successful search therefore proves the content came through the
// seam - which is also what the binary probe below depends on.
func TestGrepSearchesTheAccessorsBytes(t *testing.T) {
	root := t.TempDir()
	candidate := filepath.Join(root, "remote.txt")
	if err := os.WriteFile(candidate, []byte("nothing here at all\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	access := &virtualAccess{files: map[string][]byte{
		candidate: []byte("hit from the other machine\n"),
	}}

	result, err := Grep(context.Background(), GrepOptions{
		Pattern: "hit", Path: root, Limit: 10, Access: access,
	})
	if err != nil {
		t.Fatalf("grep through the accessor: %v", err)
	}
	if len(result.Matches) != 1 {
		t.Fatalf("matches = %d, want 1: the walker read the disk instead of the accessor (%v)",
			len(result.Matches), result.Matches)
	}
	if !strings.Contains(result.Matches[0].Text, "other machine") {
		t.Fatalf("match text came from the wrong source: %q", result.Matches[0].Text)
	}
	if access.openCalls == 0 {
		t.Fatal("the accessor was never asked for the file it just supplied")
	}
}

// TestGrepBinaryProbeSharesThatSameSource: a file the disk calls plain text but the
// accessor says carries a NUL must be skipped by the default rules. Classification and
// content arriving from two different machines is exactly the drift this step removes.
func TestGrepBinaryProbeSharesThatSameSource(t *testing.T) {
	root := t.TempDir()
	candidate := filepath.Join(root, "supposedly-text.txt")
	if err := os.WriteFile(candidate, []byte("needle plain text\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	access := &virtualAccess{files: map[string][]byte{
		candidate: []byte("needle\x00binary\n"),
	}}

	result, err := Grep(context.Background(), GrepOptions{
		Pattern: "needle", Path: root, Limit: 10, Access: access,
	})
	if err != nil {
		t.Fatalf("grep: %v", err)
	}
	if len(result.Matches) != 0 {
		t.Fatalf("a NUL-bearing file must be skipped by the default rules, got %v", result.Matches)
	}
	// -uuu searches binary files: the same bytes must then be searchable, which also
	// proves the probe and the read share one source.
	full, err := Grep(context.Background(), GrepOptions{
		Pattern: "needle", Path: root, Limit: 10, Unrestricted: 3, Access: access,
	})
	if err != nil {
		t.Fatalf("grep -uuu: %v", err)
	}
	if len(full.Matches) != 1 {
		t.Fatalf("-uuu matches = %d, want 1 (the accessor is the only source for both probes)", len(full.Matches))
	}
}

// TestGrepSizeGateUsesTheAccessorsSize: the host file is 19 bytes, the accessor serves
// 26. With MaxFileBytes=20 the search must skip it. A gate reading the local stat would
// search it instead, which is the quiet kind of wrong a half-moved seam produces.
func TestGrepSizeGateUsesTheAccessorsSize(t *testing.T) {
	root := t.TempDir()
	candidate := filepath.Join(root, "sized.txt")
	if err := os.WriteFile(candidate, []byte("needle small on disk\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	access := &virtualAccess{files: map[string][]byte{
		candidate: []byte("needle much larger than the gate\n"),
	}}

	result, err := Grep(context.Background(), GrepOptions{
		Pattern: "needle", Path: root, Limit: 10, MaxFileBytes: 20, Access: access,
	})
	if err != nil {
		t.Fatalf("grep: %v", err)
	}
	if len(result.Matches) != 0 {
		t.Fatalf("the size gate used the host file, not the accessor's bytes: %v", result.Matches)
	}

	// Same file, a gate that fits the accessor's size: the bytes must be searchable.
	roomy, err := Grep(context.Background(), GrepOptions{
		Pattern: "needle", Path: root, Limit: 10, MaxFileBytes: 40, Access: access,
	})
	if err != nil {
		t.Fatalf("grep with room: %v", err)
	}
	if len(roomy.Matches) != 1 || !strings.Contains(roomy.Matches[0].Text, "larger than the gate") {
		t.Fatalf("the accessor's bytes were not searched: %v", roomy.Matches)
	}
}

// TestGrepWithoutAnAccessorKeepsReadingTheDisk is the no-behaviour-change half, and it
// is why the option defaults to nil instead of forcing every caller to change at once.
func TestGrepWithoutAnAccessorKeepsReadingTheDisk(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "local.txt"), []byte("needle on disk\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := Grep(context.Background(), GrepOptions{Pattern: "needle", Path: root, Limit: 10})
	if err != nil {
		t.Fatalf("local grep: %v", err)
	}
	if len(result.Matches) != 1 || !strings.Contains(result.Matches[0].Text, "on disk") {
		t.Fatalf("the local default regressed: %v", result.Matches)
	}
}
