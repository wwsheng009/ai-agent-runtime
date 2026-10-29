package lscore

// ls reached zero direct os.* calls in Phase 2.5a, and these tests say what that
// zero does and does not mean. It means the existence check and the file/dir
// decision now come from the accessor the Runtime supplies. It does NOT mean the
// tree walk is backend-neutral: ReadTree still uses filepath.WalkDir, which the
// driftguard walker counter measures at one call for this package and which is
// Phase 2.5b's job to route through Access.ReadDir.

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
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
	statCalls int
	info      fs.FileInfo
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

func (a *absentAccess) Open(context.Context, string) (toolcore.FileHandle, error) {
	return nil, absentError{path: "open is not used by ls"}
}

func (a *absentAccess) ReadDir(context.Context, string) ([]fs.DirEntry, error) {
	return nil, absentError{path: "the listing still comes from the host tree"}
}

type seamRuntime struct {
	*fakeRuntime
	access toolcore.Access
}

func (r seamRuntime) FileAccess() toolcore.Access { return r.access }

func runLs(t *testing.T, root string, access toolcore.Access, args map[string]any) *toolcore.Result {
	t.Helper()
	handler := NewHandler(toolcore.Definition{Name: "ls"})
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
