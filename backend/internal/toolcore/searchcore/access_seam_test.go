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
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/localio"
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
	mod   time.Time
}

func (i fakeInfo) Name() string { return i.name }
func (i fakeInfo) Size() int64  { return i.size }
func (i fakeInfo) Mode() fs.FileMode {
	if i.isDir {
		return fs.ModeDir | 0o755
	}
	return 0o644
}
func (i fakeInfo) ModTime() time.Time { return i.mod }
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
	files map[string][]byte
	times map[string]time.Time
	// dirs declares the paths this accessor calls directories. The ignore work needs
	// it: a directory that exists on the host but is not in `files` would otherwise be
	// reported absent, so a test about scope could pass or fail on an accident of the
	// fixture instead of on the answer the accessor actually gave.
	dirs      map[string]bool
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
		if a.dirs[path] {
			return fakeInfo{name: filepath.Base(path), isDir: true}, nil
		}
		return nil, absentErr{path: path}
	}
	return fakeInfo{name: filepath.Base(path), size: int64(len(body)), mod: a.times[path]}, nil
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

func (a *virtualAccess) ReadDir(_ context.Context, dir string) ([]fs.DirEntry, error) {
	// S5: enumeration belongs to the accessor too. The listing is derived from the same
	// `files`/`dirs` maps the rest of the fake answers from, so a tree can be described
	// once and the search will only ever see it through this method.
	//
	// The entries come back in REVERSE name order on purpose. os.ReadDir sorts, so a fake
	// that also sorted could never expose a missing sort in the walker - and traversal
	// order is paging order in this package, so the sort is load-bearing, not cosmetic.
	prefix := dir + string(filepath.Separator)
	byName := map[string]fs.DirEntry{}
	for path, body := range a.files {
		if !strings.HasPrefix(path, prefix) {
			continue
		}
		rest := strings.TrimPrefix(path, prefix)
		if rest == "" {
			continue
		}
		if idx := strings.IndexByte(rest, filepath.Separator); idx >= 0 {
			name := rest[:idx]
			if _, ok := byName[name]; !ok {
				byName[name] = fs.FileInfoToDirEntry(fakeInfo{name: name, isDir: true})
			}
			continue
		}
		if a.dirs[path] {
			byName[rest] = fs.FileInfoToDirEntry(fakeInfo{name: rest, isDir: true})
			continue
		}
		byName[rest] = fs.FileInfoToDirEntry(fakeInfo{name: rest, size: int64(len(body)), mod: a.times[path]})
	}
	for path := range a.dirs {
		if !strings.HasPrefix(path, prefix) {
			continue
		}
		rest := strings.TrimPrefix(path, prefix)
		if rest == "" || strings.ContainsRune(rest, filepath.Separator) {
			continue
		}
		byName[rest] = fs.FileInfoToDirEntry(fakeInfo{name: rest, isDir: true})
	}
	entries := make([]fs.DirEntry, 0, len(byName))
	for _, entry := range byName {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() > entries[j].Name() })
	return entries, nil
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
	}, dirs: map[string]bool{root: true}}

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
	}, dirs: map[string]bool{root: true}}

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
	}, dirs: map[string]bool{root: true}}

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

// TestGrepPatternFileComesThroughTheAccessor is S2b: the pattern file is read on the
// machine the search runs against. The disk copy asks for a word that appears nowhere,
// the accessor's copy asks for the word that is in the haystack. Zero matches would
// mean the read bypassed the seam; the control case below shows the disk copy really
// does produce zero, so a pass here can only come from the accessor.
func TestGrepPatternFileComesThroughTheAccessor(t *testing.T) {
	root := t.TempDir()
	// The pattern file lives OUTSIDE the searched tree: a pattern file inside the
	// tree would match its own literal text, and the control below would pass for
	// the wrong reason. (It did, on the first run of this test.)
	tree := filepath.Join(root, "tree")
	if err := os.MkdirAll(tree, 0o755); err != nil {
		t.Fatal(err)
	}
	haystack := filepath.Join(tree, "haystack.txt")
	if err := os.WriteFile(haystack, []byte("needle in the haystack\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	patternFile := filepath.Join(root, "patterns.txt")
	if err := os.WriteFile(patternFile, []byte("absent-word\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Control: reading the pattern file from disk finds nothing.
	fromDisk, err := Grep(context.Background(), GrepOptions{Path: tree, Limit: 10, PatternFiles: []string{patternFile}})
	if err != nil {
		t.Fatalf("control grep: %v", err)
	}
	if len(fromDisk.Matches) != 0 {
		t.Fatalf("the control is wrong: the on-disk pattern file should match nothing, got %v", fromDisk.Matches)
	}

	access := &virtualAccess{files: map[string][]byte{
		patternFile: []byte("needle\n"),
		haystack:    []byte("needle in the haystack\n"),
	}, dirs: map[string]bool{tree: true}}
	result, err := Grep(context.Background(), GrepOptions{
		Path: tree, Limit: 10, PatternFiles: []string{patternFile}, Access: access,
	})
	if err != nil {
		t.Fatalf("grep with the accessor: %v", err)
	}
	if len(result.Matches) != 1 {
		t.Fatalf("matches = %d, want 1: the pattern file was not read through the accessor", len(result.Matches))
	}
	if !strings.Contains(result.Matches[0].Text, "needle in the haystack") {
		t.Fatalf("unexpected match text: %q", result.Matches[0].Text)
	}
	sawPatternFile := false
	for _, opened := range access.opened {
		if opened == patternFile {
			sawPatternFile = true
		}
	}
	if !sawPatternFile {
		t.Fatalf("the accessor never served the pattern file it was supposed to read: %v", access.opened)
	}
}

// TestGrepContextLinesComeFromTheSameSource is S2c, end to end through the handler.
// A context line is a neighbour of a match, so it must be re-read on the machine that
// produced the match: the disk copy here carries a different neighbour than the
// accessor's copy. Seeing DISK-BELOW would mean the render layer re-opened the file
// locally and printed a line that does not exist in the file it searched.
func TestGrepContextLinesComeFromTheSameSource(t *testing.T) {
	root := t.TempDir()
	hay := filepath.Join(root, "hay.txt")
	if err := os.WriteFile(hay, []byte("needle\nDISK-BELOW\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	access := &virtualAccess{files: map[string][]byte{
		hay: []byte("needle\nREMOTE-BELOW\n"),
	}, dirs: map[string]bool{root: true}}

	handler := NewGrepHandler(toolcore.Definition{Name: "grep", Version: "1.0.0"})
	runtime := seamRuntime{
		presentationRuntime: presentationRuntime{paths: presentationPaths{root: root}},
		access:              access,
	}
	result, err := handler.Execute(context.Background(), &toolcore.Call{
		Args:    map[string]any{"pattern": "needle", "path": ".", "context": 1},
		Runtime: runtime,
	})
	if err != nil {
		t.Fatalf("grep returned a Go error: %v", err)
	}
	if !result.Success {
		t.Fatalf("grep failed: %s", result.Content)
	}
	if !strings.Contains(result.Content, "REMOTE-BELOW") {
		t.Fatalf("the context line did not come from the accessor:\n%s", result.Content)
	}
	if strings.Contains(result.Content, "DISK-BELOW") {
		t.Fatalf("the context line was re-read from disk:\n%s", result.Content)
	}
	// One open for the search itself and one for the context re-read: without the
	// second, "REMOTE-BELOW" could only have come from somewhere else.
	if access.openCalls < 2 {
		t.Fatalf("opens = %d, want at least 2 (search + context re-read through the seam)", access.openCalls)
	}
}

// TestExpandGrepContextNilOpenerMeansLocal pins the convention the whole seam relies
// on: nil is not "no context lines", it is "this machine's filesystem", which is what
// every caller did before the seam existed.
func TestExpandGrepContextNilOpenerMeansLocal(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "near.txt")
	if err := os.WriteFile(path, []byte("one\nneedle\nthree\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	matches := []GrepMatch{{File: path, Line: 2, Text: "needle"}}

	local := ExpandGrepContext(matches, 1, 1, false, false, nil)
	if len(local) != 3 {
		t.Fatalf("nil opener produced %d lines, want 3 (one, needle, three): %v", len(local), local)
	}
	if local[0].Text != "one" || local[2].Text != "three" {
		t.Fatalf("unexpected local neighbours: %q / %q", local[0].Text, local[2].Text)
	}

	// The same call with an opener that serves different neighbours must show those
	// instead, which is what proves the parameter is honoured rather than ignored.
	other := &virtualAccess{files: map[string][]byte{path: []byte("L1\nneedle\nL3\n")}}
	remote := ExpandGrepContext(matches, 1, 1, false, false, func(p string) (toolcore.FileHandle, error) {
		return other.Open(context.Background(), p)
	})
	if len(remote) != 3 || remote[0].Text != "L1" || remote[2].Text != "L3" {
		t.Fatalf("opener was not honoured: %v", remote)
	}
}

// TestGlobOrderFollowsTheAccessorsTimes is S3 for glob. Every glob call orders
// matches newest-first, so the timestamps must belong to the machine being listed.
// The disk is set up with the opposite order, and the control case proves the two
// sources really do disagree - otherwise this test would pass while reading nothing.
func TestGlobOrderFollowsTheAccessorsTimes(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "a.txt")
	second := filepath.Join(root, "b.txt")
	for _, path := range []string{first, second} {
		if err := os.WriteFile(path, []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Unix(1_600_000_000, 0)
	newer := time.Unix(1_700_000_000, 0)
	// Disk: b is the newer file.
	if err := os.Chtimes(first, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(second, newer, newer); err != nil {
		t.Fatal(err)
	}
	options := GlobOptions{Base: root, Pattern: "*.txt", Limit: 10, SortByModTime: true}

	local, err := GlobWithOptions(context.Background(), options)
	if err != nil {
		t.Fatalf("local glob: %v", err)
	}
	if len(local.Matches) != 2 || filepath.Base(local.Matches[0]) != "b.txt" {
		t.Fatalf("the control is wrong: disk order should put b.txt first, got %v", local.Matches)
	}

	// The accessor insists a is the newer file.
	access := &virtualAccess{
		files: map[string][]byte{first: []byte("x\n"), second: []byte("x\n")},
		times: map[string]time.Time{first: newer, second: old},
		dirs:  map[string]bool{root: true},
	}
	options.Access = access
	remote, err := GlobWithOptions(context.Background(), options)
	if err != nil {
		t.Fatalf("glob with the accessor: %v", err)
	}
	if len(remote.Matches) != 2 || filepath.Base(remote.Matches[0]) != "a.txt" {
		t.Fatalf("glob ordered by this machine's mtimes instead of the accessor's: %v", remote.Matches)
	}
}

// TestGrepSortFollowsTheAccessorsTimes is S3 for grep's --sort=modified. Note the
// boundary this exposes: sort=accessed and sort=created read platform-specific
// Stat_t fields through FileInfo.Sys(), which an accessor over another transport
// cannot honestly supply - those two must stay declined for remote sources rather
// than approximated. Only "modified" is exercised here.
func TestGrepSortFollowsTheAccessorsTimes(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "f1.txt")
	second := filepath.Join(root, "f2.txt")
	for _, path := range []string{first, second} {
		if err := os.WriteFile(path, []byte("needle\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Unix(1_600_000_000, 0)
	newer := time.Unix(1_700_000_000, 0)
	if err := os.Chtimes(first, newer, newer); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(second, old, old); err != nil {
		t.Fatal(err)
	}

	// SortSet is load-bearing: a key alone is not a request. The first draft of this
	// test omitted it, so no sorting happened and the "control" passed for the wrong
	// reason - the assertion below only became meaningful once the sort actually ran.
	// rg's `--sort=modified` is ascending - oldest file first - and the core keeps
	// that meaning (`--sortr` is the reverse). The first draft of this test asserted
	// newest-first and so "failed" against correct behaviour; the comparator in
	// grep_sort.go settled it.
	shared := GrepOptions{Path: root, Limit: 10, Pattern: "needle", SortKey: "modified", SortSet: true}
	local, err := Grep(context.Background(), shared)
	if err != nil {
		t.Fatalf("local grep: %v", err)
	}
	if len(local.Matches) != 2 || filepath.Base(local.Matches[0].File) != "f2.txt" {
		t.Fatalf("the control is wrong: ascending by disk mtime should put the older f2.txt first, got %v", local.Matches)
	}

	access := &virtualAccess{
		files: map[string][]byte{
			first:  []byte("needle\n"),
			second: []byte("needle\n"),
		},
		// The accessor's view flips the ages: f1 is the older file there, so the
		// ascending order must start with f1 while disk starts with f2.
		times: map[string]time.Time{first: old, second: newer},
		dirs:  map[string]bool{root: true},
	}
	shared.Access = access
	remote, err := Grep(context.Background(), shared)
	if err != nil {
		t.Fatalf("grep with the accessor: %v", err)
	}
	if len(remote.Matches) != 2 || filepath.Base(remote.Matches[0].File) != "f1.txt" {
		t.Fatalf("grep sorted by this machine's mtimes instead of the accessor's: %v", remote.Matches)
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

// --- S4: the ignore rule set is discovered on the machine that holds the files ---

// writeHostFile puts one file on the disk, which is what makes the injection honest:
// the same path holds different bytes for the accessor, so a rule that arrived from the
// host is visible in the answer rather than invisible in a passing test.
func writeHostFile(t *testing.T, filePath, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestGrepIgnoreRulesComeFromTheAccessorsBytes: the host .gitignore hides keep.txt, the
// accessor's .gitignore hides ignored.txt. Which file the search reports is therefore a
// direct read of which machine supplied the rule.
//
// The control is not decoration. Without it, a fixture whose rules simply never applied
// would produce the same "accessor" answer for the wrong reason, and the test would pass
// while proving nothing.
func TestGrepIgnoreRulesComeFromTheAccessorsBytes(t *testing.T) {
	root := t.TempDir()
	writeHostFile(t, filepath.Join(root, ".gitignore"), "keep.txt\n")
	writeHostFile(t, filepath.Join(root, "ignored.txt"), "needle\n")
	writeHostFile(t, filepath.Join(root, "keep.txt"), "needle\n")

	shared := GrepOptions{Pattern: "needle", Limit: 10}
	control := grepFilterMatches(t, root, shared)
	assertFileList(t, control, []string{"ignored.txt"})

	access := &virtualAccess{
		files: map[string][]byte{
			filepath.Join(root, ".gitignore"):  []byte("ignored.txt\n"),
			filepath.Join(root, "ignored.txt"): []byte("needle\n"),
			filepath.Join(root, "keep.txt"):    []byte("needle\n"),
		},
		dirs: map[string]bool{root: true},
	}
	shared.Access = access
	got := grepFilterMatches(t, root, shared)
	if strings.Join(got, ",") == strings.Join(control, ",") {
		t.Fatalf("the accessor changed nothing: the rule still came from the host (%v)", got)
	}
	assertFileList(t, got, []string{"keep.txt"})
}

// TestGrepExplicitIgnoreFileIsReadThroughTheAccessor pins the other entry point: an
// ignore_file the caller named is a rule set, and a remote search must read the target's
// copy of it. Before this step the file was opened on the host even when the search was
// pointed elsewhere, so the caller's own promise was answered by the wrong machine.
func TestGrepExplicitIgnoreFileIsReadThroughTheAccessor(t *testing.T) {
	root := t.TempDir()
	custom := filepath.Join(root, "custom.ignore")
	writeHostFile(t, custom, "keep.txt\n")
	writeHostFile(t, filepath.Join(root, "ignored.txt"), "needle\n")
	writeHostFile(t, filepath.Join(root, "keep.txt"), "needle\n")

	shared := GrepOptions{Pattern: "needle", Limit: 10, IgnoreFiles: []string{custom}}
	assertFileList(t, grepFilterMatches(t, root, shared), []string{"ignored.txt"})

	access := &virtualAccess{
		files: map[string][]byte{
			custom:                             []byte("ignored.txt\n"),
			filepath.Join(root, "ignored.txt"): []byte("needle\n"),
			filepath.Join(root, "keep.txt"):    []byte("needle\n"),
		},
		dirs: map[string]bool{root: true},
	}
	shared.Access = access
	assertFileList(t, grepFilterMatches(t, root, shared), []string{"keep.txt"})
	found := false
	for _, path := range access.opened {
		if path == custom {
			found = true
		}
	}
	if !found {
		t.Fatalf("the explicit ignore file was never opened through the accessor: %v", access.opened)
	}
}

// TestValidateGrepIgnoreFilesAsksTheAccessor is the promise half: naming an ignore_file
// means its rules apply, so the check for "can this file be read" has to be answered by
// the machine the search is going to read. Two cases matter and both are silent failures
// if wrong: a remote-only file that this machine would call missing, and a local file the
// target does not have.
func TestValidateGrepIgnoreFilesAsksTheAccessor(t *testing.T) {
	root := t.TempDir()
	remoteOnly := filepath.Join(root, "remote-only.ignore")
	localOnly := filepath.Join(root, "local-only.ignore")
	writeHostFile(t, localOnly, "keep.txt\n")

	access := &virtualAccess{
		files: map[string][]byte{remoteOnly: []byte("ignored.txt\n")},
		dirs:  map[string]bool{filepath.Join(root, "dir.ignore"): true},
	}

	t.Run("a file the accessor has passes even though the host lacks it", func(t *testing.T) {
		if err := ValidateGrepIgnoreFiles(context.Background(), GrepOptions{
			IgnoreFiles: []string{remoteOnly}, Access: access,
		}); err != nil {
			t.Fatalf("validation answered from the host: %v", err)
		}
		if access.openCalls == 0 {
			t.Fatal("validation never asked the accessor")
		}
	})

	t.Run("a file only the host has fails", func(t *testing.T) {
		err := ValidateGrepIgnoreFiles(context.Background(), GrepOptions{
			IgnoreFiles: []string{localOnly}, Access: access,
		})
		if err == nil {
			t.Fatal("the accessor says this file is absent; promising its rules would be a lie")
		}
		if !strings.Contains(err.Error(), "ignore_file") {
			t.Fatalf("the error must name the parameter: %v", err)
		}
	})

	t.Run("no_messages turns the accessor's refusal into a skip", func(t *testing.T) {
		if err := ValidateGrepIgnoreFiles(context.Background(), GrepOptions{
			IgnoreFiles: []string{localOnly}, NoMessages: true, Access: access,
		}); err != nil {
			t.Fatalf("no_messages must skip: %v", err)
		}
	})

	t.Run("a directory by the accessor's answer fails", func(t *testing.T) {
		if err := ValidateGrepIgnoreFiles(context.Background(), GrepOptions{
			IgnoreFiles: []string{filepath.Join(root, "dir.ignore")}, Access: access,
		}); err == nil {
			t.Fatal("a path the accessor calls a directory must not be promised as a rule file")
		}
	})
}

// TestRemoteAccessorDoesNotReadTheHostHomeGitignore is the Q3 decision, and it is the
// reason the ratchet number alone is not the whole argument: HOME is a fact about the
// machine running the tool, so applying it to files held elsewhere would silently hide
// matches the target's own user never ignored.
//
// The control here is what makes the fixture trustworthy: without an accessor the same
// global file must take effect, proving the host rule really is in place and that the
// remote case is declining it rather than failing to read it.
func TestRemoteAccessorDoesNotReadTheHostHomeGitignore(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)
	t.Setenv("USERPROFILE", tempHome)
	writeHostFile(t, filepath.Join(tempHome, ".config", "git", "ignore"), "global.txt\n")

	root := t.TempDir()
	writeHostFile(t, filepath.Join(root, "global.txt"), "needle\n")
	writeHostFile(t, filepath.Join(root, "keep.txt"), "needle\n")

	shared := GrepOptions{Pattern: "needle", Limit: 10}
	assertFileList(t, grepFilterMatches(t, root, shared), []string{"keep.txt"})

	access := &virtualAccess{
		files: map[string][]byte{
			filepath.Join(root, "global.txt"): []byte("needle\n"),
			filepath.Join(root, "keep.txt"):   []byte("needle\n"),
			// The target also holds a file at the *host's* HOME path. That is the
			// realistic collision - both sides are Linux, both have
			// /home/<name>/.config/git/ignore - and it is what makes skipping the
			// host's global rules observable instead of accidentally correct.
			// Without this entry the remote case would pass even if the code asked
			// for the host's global file, because the read would go to the accessor
			// and find nothing there: two safeguards, one test, and no evidence that
			// the intended one is doing any work.
			filepath.Join(tempHome, ".config", "git", "ignore"): []byte("global.txt\n"),
		},
		dirs: map[string]bool{root: true},
	}
	shared.Access = access
	// The host's global file hides global.txt, so a search that consulted it would answer
	// with only keep.txt. Reporting global.txt is the proof that the host's HOME was left
	// alone: HOME names this machine, and these bytes are not this machine's files.
	assertFileList(t, grepFilterMatches(t, root, shared), []string{"global.txt", "keep.txt"})
}

// TestLocalAccessorStillReadsTheHostHomeGitignore keeps the other side of the default:
// handing the core localio.Access explicitly is the same machine, so the host's global
// rules must still apply. If this ever failed, a plain local search would have quietly
// stopped honouring its own ~/.config/git/ignore.
func TestLocalAccessorStillReadsTheHostHomeGitignore(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)
	t.Setenv("USERPROFILE", tempHome)
	writeHostFile(t, filepath.Join(tempHome, ".config", "git", "ignore"), "global.txt\n")

	root := t.TempDir()
	writeHostFile(t, filepath.Join(root, "global.txt"), "needle\n")
	writeHostFile(t, filepath.Join(root, "keep.txt"), "needle\n")

	shared := GrepOptions{Pattern: "needle", Limit: 10, Access: localio.Access}
	assertFileList(t, grepFilterMatches(t, root, shared), []string{"keep.txt"})
}

// TestGrepIgnoreScopeFollowsTheAccessorsRootShape pins the Stat that chooses which
// directory the search is scoped to. Now that the traversal answers from the accessor,
// this question must agree with it: a path that is a directory on the target and a plain
// file here is the ordinary shape of "same path, different machine", and choosing the
// scope from the host answer makes the search consult the parent of a tree the caller
// never named - rules leaking in from outside the root.
//
// The fixture is asymmetric on purpose, and its first draft was wrong in a way only a
// mutation exposed: when both sides agreed the root was a directory, baseRules already
// loads the ancestors it can reach, so the two scope answers produced the same rule set and
// the test stayed green against a host-side Stat. Here the parent's rule can only reach the
// match through the root's shape question, and no_ignore_parent is what shuts the other
// door.
func TestGrepIgnoreScopeFollowsTheAccessorsRootShape(t *testing.T) {
	root := filepath.Join(t.TempDir(), "app")
	parent := filepath.Dir(filepath.Clean(root))
	hit := filepath.Join(root, "src", "hit.txt")
	// On this machine the search root is a file with unrelated bytes.
	writeHostFile(t, root, "this path is a file here\n")

	access := &virtualAccess{
		files: map[string][]byte{
			hit: []byte("needle\n"),
			// The parent carries a rule that hides the match by base name. It may
			// only reach the search if the scope is chosen from the host answer.
			filepath.Join(parent, ".gitignore"): []byte("hit.txt\n"),
		},
		dirs: map[string]bool{root: true, filepath.Join(root, "src"): true},
	}

	shared := GrepOptions{Pattern: "needle", Limit: 10, NoIgnoreParent: true}
	shared.Access = access
	assertFileList(t, grepFilterMatches(t, root, shared), []string{"src/hit.txt"})

	// Control: with no accessor this machine has a file at the root, so there is no tree
	// to search and nothing is reported. The two sources must differ, or the assertion
	// above is passing on an inert fixture.
	assertFileList(t, grepFilterMatches(t, root, GrepOptions{
		Pattern: "needle", Limit: 10, NoIgnoreParent: true,
	}), nil)
}

// TestGrepRulesForFollowsTheAccessorsDirectoryShape is a regression pin for the target's
// own directory rules, and it is NOT the evidence S4 hoped for. The mutation that answers
// this candidate's shape from the host (os.Stat on absPath) leaves the result unchanged,
// and the reason is structural rather than a flaw in the fixture:
//
//	rulesFor walks from `dir` up to the search root, so choosing dir = absPath adds exactly
//	one more directory level, dirRules(absPath). Those rule files carry scope = absPath, and
//	they are matched against absPath itself, whose relative path is "." - which the matcher
//	skips. Children of absPath reach dirRules(absPath) through their own parent either way.
//	So the shape answer at a candidate cannot change that candidate's verdict, and no fixture
//	can make it observable.
//
// The Stat still moved, for a different reason than evidence: one search must have one
// answerer for the same question, and a future change to the chain would otherwise make
// this line quietly load-bearing. This test keeps the behaviour pinned in the meantime -
// the target's .gitignore inside a directory that is a file here must hide its own match.
func TestGrepRulesForFollowsTheAccessorsDirectoryShape(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "pkg")
	writeHostFile(t, sub, "not a directory here\n")

	access := &virtualAccess{
		files: map[string][]byte{
			sub:                              []byte("not a directory here\n"),
			filepath.Join(sub, ".gitignore"): []byte("hit.txt\n"),
			filepath.Join(sub, "hit.txt"):    []byte("needle\n"),
		},
		dirs: map[string]bool{root: true, sub: true},
	}

	assertFileList(t, grepFilterMatches(t, root, GrepOptions{
		Pattern: "needle", Limit: 10, Access: access,
	}), nil)

	// And the same tree without the target's rule file must report the match, proving the
	// zero above is the rule file doing work rather than the candidate never being reached.
	unruled := &virtualAccess{
		files: map[string][]byte{
			sub:                           []byte("not a directory here\n"),
			filepath.Join(sub, "hit.txt"): []byte("needle\n"),
		},
		dirs: map[string]bool{root: true, sub: true},
	}
	assertFileList(t, grepFilterMatches(t, root, GrepOptions{
		Pattern: "needle", Limit: 10, Access: unruled,
	}), []string{"pkg/hit.txt"})
}
