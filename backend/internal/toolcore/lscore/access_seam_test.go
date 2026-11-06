package lscore

// ls reached zero direct os.* calls in Phase 2.5a, and these tests say what that
// zero does and does not mean. It means the existence check and the file/dir
// decision now come from the accessor the Runtime supplies. It does NOT mean the
// tree walk is backend-neutral: ReadTree still uses filepath.WalkDir, which the
// driftguard walker counter measures at one call for this package and which is
// Phase 2.5b's job to route through Access.ReadDir.

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
)

type stubInfo struct {
	name  string
	size  int64
	isDir bool
}

func (i stubInfo) Name() string { return i.name }
func (i stubInfo) Size() int64  { return i.size }
func (i stubInfo) Mode() fs.FileMode {
	if i.isDir {
		return fs.ModeDir | 0o755
	}
	return 0o644
}
func (i stubInfo) ModTime() time.Time { return time.Time{} }
func (i stubInfo) IsDir() bool        { return i.isDir }
func (i stubInfo) Sys() any           { return nil }

// absentAccess answers every question with "not there" and records that it was
// asked. Opening and listing are unreachable in these cases, so returning the same
// absence error keeps the fake honest about a path it has never seen.
type absentAccess struct {
	statCalls  int
	lstatCalls int
	info       fs.FileInfo
}

type absentError struct{ path string }

func (e absentError) Error() string { return "remote: no such path " + e.path }
func (absentError) NotExist() bool  { return true }

func (a *absentAccess) Stat(_ context.Context, path string) (fs.FileInfo, error) {
	a.statCalls++
	if a.info == nil {
		return nil, absentError{path: path}
	}
	return a.info, nil
}

// Lstat counts separately from Stat on purpose: the existence check uses Stat (it
// follows, as it always did) while the walk uses Lstat (it must not follow). If
// those two counters were interchangeable, a regression that swapped one for the
// other would be invisible.
func (a *absentAccess) Lstat(_ context.Context, path string) (fs.FileInfo, error) {
	a.lstatCalls++
	if a.info == nil {
		return nil, absentError{path: path}
	}
	return a.info, nil
}

func (a *absentAccess) Open(context.Context, string) (toolcore.FileHandle, error) {
	return nil, absentError{path: "open is not used by ls"}
}

func (a *absentAccess) ReadDir(context.Context, string) ([]fs.DirEntry, error) {
	return nil, absentError{path: "the listing still comes from the host tree"}
}

// --- the tree walk through the seam ---

// treeInfo is a file or directory node of the virtual tree.
type treeInfo struct {
	name string
	dir  bool
	link bool
}

func (i treeInfo) Name() string { return i.name }
func (i treeInfo) Size() int64  { return 0 }
func (i treeInfo) Mode() fs.FileMode {
	switch {
	case i.link:
		return fs.ModeSymlink
	case i.dir:
		return fs.ModeDir | 0o755
	default:
		return 0o644
	}
}
func (i treeInfo) ModTime() time.Time { return time.Time{} }
func (i treeInfo) IsDir() bool        { return i.dir && !i.link }
func (i treeInfo) Sys() any           { return nil }

type treeEntry struct{ info treeInfo }

func (e treeEntry) Name() string               { return e.info.name }
func (e treeEntry) IsDir() bool                { return e.info.IsDir() }
func (e treeEntry) Type() fs.FileMode          { return e.info.Mode().Type() }
func (e treeEntry) Info() (fs.FileInfo, error) { return e.info, nil }

// treeAccess serves a virtual directory tree. denied marks directories whose
// contents cannot be read, which is the case a listing must survive without
// turning into an error.
type treeAccess struct {
	nodes    map[string][]fs.DirEntry
	roots    map[string]treeInfo
	visited  []string
	denyRead map[string]bool
}

func (a *treeAccess) Stat(_ context.Context, path string) (fs.FileInfo, error) {
	if info, ok := a.roots[path]; ok {
		return info, nil
	}
	return nil, absentError{path: path}
}

func (a *treeAccess) Lstat(_ context.Context, path string) (fs.FileInfo, error) {
	return a.Stat(context.Background(), path)
}

func (a *treeAccess) Open(context.Context, string) (toolcore.FileHandle, error) {
	return nil, absentError{path: "ls does not open files"}
}

func (a *treeAccess) ReadDir(_ context.Context, path string) ([]fs.DirEntry, error) {
	// The virtual tree is keyed with "/" while the walk builds absolute paths with
	// filepath.Join, which uses "\" on Windows. Normalising here keeps the fixture
	// about the tree, not about the host's separator.
	path = filepath.ToSlash(path)
	a.visited = append(a.visited, path)
	if a.denyRead[path] {
		return nil, absentError{path: "denied to this user: " + path}
	}
	entries, ok := a.nodes[path]
	if !ok {
		return nil, absentError{path: path}
	}
	return entries, nil
}

func TestReadTreeViaWalksTheInjectedTree(t *testing.T) {
	root := "/elsewhere"
	access := &treeAccess{
		roots: map[string]treeInfo{root: {name: "elsewhere", dir: true}},
		nodes: map[string][]fs.DirEntry{
			// Lexical order, because that is what os.ReadDir returns and what the
			// walk's cost shape depends on.
			root: {
				treeEntry{treeInfo{name: "a.txt"}},
				treeEntry{treeInfo{name: "locked", dir: true}},
				treeEntry{treeInfo{name: "m", dir: true}},
				treeEntry{treeInfo{name: "z.txt"}},
			},
			root + "/m": {
				treeEntry{treeInfo{name: "b.txt"}},
				treeEntry{treeInfo{name: "deep", dir: true}},
			},
			root + "/m/deep": {
				treeEntry{treeInfo{name: "c.txt"}},
			},
			// A directory the caller cannot read: it must appear, its contents must
			// not, and the listing as a whole must still succeed.
			root + "/locked": {treeEntry{treeInfo{name: "secret.txt"}}},
		},
		denyRead: map[string]bool{root + "/locked": true},
	}

	listing, err := ReadTreeVia(context.Background(), access, root, 3, 100)
	if err != nil {
		t.Fatalf("ReadTreeVia through the injected tree: %v", err)
	}
	got := make([]string, 0, len(listing.Entries))
	for _, entry := range listing.Entries {
		got = append(got, fmt.Sprintf("%s@%d%s", entry.RelPath, entry.Depth, dirMark(entry)))
	}
	// Depth counts the separators of the relative path, which is how the old walk
	// computed it: a direct child of the requested directory is depth 0, not 1.
	want := []string{
		"a.txt@0", "locked@0/", "m@0/", "m/b.txt@1", "m/deep@1/",
		"m/deep/c.txt@2", "z.txt@0",
	}
	if len(got) != len(want) {
		t.Fatalf("listing = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("entry %d = %q, want %q (full: %v)", index, got[index], want[index], got)
		}
	}
	if listing.Dirs != 3 || listing.Files != 4 || listing.Total != 7 {
		t.Fatalf("counts = dirs %d files %d total %d, want 3/4/7", listing.Dirs, listing.Files, listing.Total)
	}
	if listing.Truncated || listing.LimitHit {
		t.Fatalf("a listing under the limit must not claim truncation: %+v", listing)
	}
	// A denied directory is still *attempted* - that is how the walk learns it is
	// denied, and filepath.WalkDir paid the same attempt. What must not happen is
	// its contents reaching the listing, or the call failing because of it.
	attempts := 0
	for _, visited := range access.visited {
		if visited == root+"/locked" {
			attempts++
		}
	}
	if attempts != 1 {
		t.Fatalf("locked was attempted %d times, want exactly 1 (all visits: %v)", attempts, access.visited)
	}
	for _, entry := range listing.Entries {
		if strings.HasPrefix(entry.RelPath, "locked/") {
			t.Fatalf("contents of an unreadable directory leaked into the listing: %v", entry.RelPath)
		}
	}
}

func dirMark(entry TreeEntry) string {
	if entry.Dir {
		return "/"
	}
	return ""
}

func TestReadTreeViaDepthBoundaryDropsEntriesAtTheLine(t *testing.T) {
	root := "/tree"
	access := &treeAccess{
		roots: map[string]treeInfo{root: {name: "tree", dir: true}},
		nodes: map[string][]fs.DirEntry{
			root:              {treeEntry{treeInfo{name: "one", dir: true}}, treeEntry{treeInfo{name: "top.txt"}}},
			root + "/one":     {treeEntry{treeInfo{name: "two", dir: true}}},
			root + "/one/two": {treeEntry{treeInfo{name: "three.txt"}}},
		},
	}
	listing, err := ReadTreeVia(context.Background(), access, root, 2, 100)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(listing.Entries))
	for _, entry := range listing.Entries {
		got = append(got, entry.RelPath)
	}
	// depth=2 means: children at depth 0 and 1 are kept, so one/two (depth 1) is
	// listed but not descended - three.txt (depth 2) never appears.
	want := []string{"one", "one/two", "top.txt"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("depth boundary listing = %v, want %v", got, want)
	}
	// The directory on the boundary is still listed, and its own contents are read
	// and then dropped - which is what filepath.WalkDir did, so the syscall count is
	// unchanged. The observable rule is that nothing at depth 2 appears.
	if !strings.Contains(strings.Join(access.visited, ","), "/one/two") {
		t.Fatalf("the walk stopped one level early: %v", access.visited)
	}
	for _, entry := range listing.Entries {
		if strings.Contains(entry.RelPath, "three.txt") {
			t.Fatalf("an entry past the depth boundary was listed: %v", entry)
		}
	}
}

func TestReadTreeViaLimitCutsTheCollectionOrder(t *testing.T) {
	root := "/cut"
	access := &treeAccess{
		roots: map[string]treeInfo{root: {name: "cut", dir: true}},
		nodes: map[string][]fs.DirEntry{
			root: {
				treeEntry{treeInfo{name: "a", dir: true}},
				treeEntry{treeInfo{name: "b", dir: true}},
				treeEntry{treeInfo{name: "z.txt"}},
			},
			root + "/a": {treeEntry{treeInfo{name: "a1"}}, treeEntry{treeInfo{name: "a2"}}},
			root + "/b": {treeEntry{treeInfo{name: "b1"}}},
		},
	}
	listing, err := ReadTreeVia(context.Background(), access, root, 5, 3)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(listing.Entries))
	for _, entry := range listing.Entries {
		got = append(got, entry.RelPath)
	}
	// The cut lands mid-traversal, in WalkDir order: a, a/a1, a/a2 fills the limit
	// of 3, so b/* and z.txt are never collected. A version that collected the whole
	// tree and then truncated would answer {a, b, z.txt} instead.
	want := []string{"a", "a/a1", "a/a2"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("cut listing = %v, want %v", got, want)
	}
	if !listing.Truncated || !listing.LimitHit {
		t.Fatalf("a cut listing must report both flags: %+v", listing)
	}
}

// TestReadTreeViaDoesNotFollowASymlinkedRoot is the safety invariant: the root's own
// link must not be climbed, because the authorized tree stops at the link. This is
// what filepath.WalkDir did through its internal Lstat, and the seam keeps it.
func TestReadTreeViaDoesNotFollowASymlinkedRoot(t *testing.T) {
	root := "/via-link"
	access := &treeAccess{
		roots: map[string]treeInfo{root: {name: "via-link", dir: true, link: true}},
		nodes: map[string][]fs.DirEntry{
			root: {treeEntry{treeInfo{name: "outside.txt"}}},
		},
	}
	listing, err := ReadTreeVia(context.Background(), access, root, 3, 100)
	if err != nil {
		t.Fatalf("a symlinked root is not an error: %v", err)
	}
	if len(listing.Entries) != 0 {
		t.Fatalf("the walk followed a symlinked root and listed %v", listing.Entries)
	}
	if len(access.visited) != 0 {
		t.Fatalf("ReadDir was called through a symlink: %v", access.visited)
	}
}

type seamRuntime struct {
	*fakeRuntime
	access toolcore.Access
}

func (r seamRuntime) FileAccess() toolcore.Access { return r.access }

func runLs(t *testing.T, root string, access toolcore.Access, args map[string]any) *toolcore.Result {
	t.Helper()
	handler := NewHandler(toolcore.Definition{Name: "list_dir"})
	runtime := seamRuntime{fakeRuntime: &fakeRuntime{paths: fakePaths{root: root}}, access: access}
	result, err := handler.Execute(context.Background(), &toolcore.Call{Args: args, Runtime: runtime})
	if err != nil {
		t.Fatalf("ls returned a Go error, want a result: %v", err)
	}
	if result == nil {
		t.Fatal("ls returned a nil result")
	}
	return result
}

// TestExistenceCheckComesFromAccessor: the directory exists on this machine, and ls
// must still report NOT_FOUND because the accessor - the machine being described -
// says the path is absent.
func TestExistenceCheckComesFromAccessor(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "present"), 0o755); err != nil {
		t.Fatal(err)
	}
	access := &absentAccess{}

	result := runLs(t, root, access, map[string]any{"path": "present"})
	if result.Success {
		t.Fatalf("ls described the host machine instead of the accessor:\n%s", result.Content)
	}
	if want := toolcore.NormalizeErrorCode(toolcore.ErrNotFound); result.ErrorCode != want {
		t.Fatalf("error code = %q, want %q", result.ErrorCode, want)
	}
	if access.statCalls != 1 {
		t.Fatalf("statCalls = %d, want 1", access.statCalls)
	}
}

// TestFileShapeComesFromAccessor goes one step further: the accessor claims the
// path is a 42-byte file while the host has a directory at that exact path, and ls
// must publish the accessor's answer. A single os.Stat left in the handler would
// have taken the directory branch and walked the local tree.
func TestFileShapeComesFromAccessor(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "confusing")
	if err := os.MkdirAll(filepath.Join(path, "inner"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "inner", "host-only.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	access := &absentAccess{info: stubInfo{name: "confusing", size: 42}}

	result := runLs(t, root, access, map[string]any{"path": "confusing"})
	if !result.Success {
		t.Fatalf("ls refused to describe the accessor's file: %s", result.Content)
	}
	if size, _ := result.Metadata["size"].(int64); size != 42 {
		t.Fatalf("size = %v, want 42 from the accessor", result.Metadata["size"])
	}
	if isDir, _ := result.Metadata["isDir"].(bool); isDir {
		t.Fatalf("isDir = true, but the accessor reported a file: %#v", result.Metadata)
	}
	if count, _ := result.Metadata["returned_count"].(int); count != 1 {
		t.Fatalf("returned_count = %v, want 1 (one file, not a host listing)", result.Metadata["returned_count"])
	}
}
