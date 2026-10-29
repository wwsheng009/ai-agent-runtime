package viewcore

// These tests are the difference between a seam and a decoration. Every case
// injects an accessor that serves files no local path can reach, so if the shared
// handler still opened the host filesystem somewhere, the read would fail on a
// missing file instead of returning the virtual bytes - and the test would say so.
// The pre-existing view and ls unit tests were left untouched to prove the move
// changed no behaviour; these prove the move actually wired something up.

import (
	"bytes"
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

// --- a filesystem that exists only inside this test process ---

type seamInfo struct {
	name string
	size int64
	mode fs.FileMode
}

func (i seamInfo) Name() string       { return i.name }
func (i seamInfo) Size() int64        { return i.size }
func (i seamInfo) Mode() fs.FileMode  { return i.mode }
func (i seamInfo) ModTime() time.Time { return time.Time{} }
func (i seamInfo) IsDir() bool        { return i.mode.IsDir() }
func (i seamInfo) Sys() any           { return nil }

type seamFile struct {
	info seamInfo
	data []byte
}

func newSeamFile(name string, data []byte) seamFile {
	return seamFile{info: seamInfo{name: name, size: int64(len(data)), mode: 0o644}, data: data}
}

func newSeamDir(name string) seamFile {
	return seamFile{info: seamInfo{name: name, mode: fs.ModeDir | 0o755}}
}

// seamAccess serves an in-memory tree and records what the core asked for.
type seamAccess struct {
	files    map[string]seamFile
	children map[string][]seamFile

	statCalls    int
	openCalls    int
	readDirCalls int
	opened       []*seamHandle
}

// seamAbsent is how a transport says "not there": its own error type, not
// fs.ErrNotExist. toolcore.IsNotExist must still classify it, or a remote miss
// would surface as a generic argument error and the model would get no recovery
// hint.
type seamAbsent struct{ path string }

func (e seamAbsent) Error() string { return fmt.Sprintf("remote: no such path %s", e.path) }
func (seamAbsent) NotExist() bool  { return true }

func (a *seamAccess) Stat(_ context.Context, path string) (fs.FileInfo, error) {
	a.statCalls++
	entry, ok := a.files[path]
	if !ok {
		return nil, seamAbsent{path: path}
	}
	return entry.info, nil
}

// Lstat is present because the seam requires it; view never descends, so the local
// and linked forms are the same answer here.
func (a *seamAccess) Lstat(_ context.Context, path string) (fs.FileInfo, error) {
	return a.Stat(context.Background(), path)
}

func (a *seamAccess) Open(_ context.Context, path string) (toolcore.FileHandle, error) {
	a.openCalls++
	entry, ok := a.files[path]
	if !ok {
		return nil, seamAbsent{path: path}
	}
	if entry.info.IsDir() {
		return nil, fmt.Errorf("%s is a directory, which the seam refuses to open", path)
	}
	handle := &seamHandle{Reader: bytes.NewReader(entry.data), info: entry.info}
	a.opened = append(a.opened, handle)
	return handle, nil
}

func (a *seamAccess) ReadDir(_ context.Context, path string) ([]fs.DirEntry, error) {
	a.readDirCalls++
	entries, ok := a.children[path]
	if !ok {
		return nil, seamAbsent{path: path}
	}
	out := make([]fs.DirEntry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, seamDirEntry{info: entry.info})
	}
	return out, nil
}

// seamHandle records whether the core closed it. A remote handle is a live
// connection, and a leak is a resource bug that a byte-for-byte local comparison
// can never show.
type seamHandle struct {
	*bytes.Reader
	info   fs.FileInfo
	closed bool
}

func (h *seamHandle) Stat() (fs.FileInfo, error) { return h.info, nil }

func (h *seamHandle) Close() error {
	h.closed = true
	return nil
}

type seamDirEntry struct{ info fs.FileInfo }

func (e seamDirEntry) Name() string               { return e.info.Name() }
func (e seamDirEntry) IsDir() bool                { return e.info.IsDir() }
func (e seamDirEntry) Type() fs.FileMode          { return e.info.Mode().Type() }
func (e seamDirEntry) Info() (fs.FileInfo, error) { return e.info, nil }

// seamRuntime is a surface that owns its own filesystem: the same handler, a
// different machine underneath.
type seamRuntime struct {
	*viewBenchRuntime
	access *seamAccess
}

func (r seamRuntime) FileAccess() toolcore.Access { return r.access }

func runView(t *testing.T, root string, access *seamAccess, args map[string]any) *toolcore.Result {
	t.Helper()
	h := NewHandler(toolcore.Definition{Name: "view"})
	runtime := seamRuntime{
		viewBenchRuntime: &viewBenchRuntime{paths: viewBenchPaths{root: root}},
		access:           access,
	}
	result, err := h.Execute(context.Background(), &toolcore.Call{Args: args, Runtime: runtime})
	if err != nil {
		t.Fatalf("handler returned a Go error, want a result: %v", err)
	}
	if result == nil {
		t.Fatal("handler returned a nil result")
	}
	return result
}

// TestHandlerReadsThroughInjectedAccessor is the decisive case: this path does not
// exist on the machine running the test, so only a seam-backed read can produce a
// window for it.
func TestHandlerReadsThroughInjectedAccessor(t *testing.T) {
	root := t.TempDir()
	remotePath := filepath.Join(root, "remote.txt")
	access := &seamAccess{
		files: map[string]seamFile{
			remotePath: newSeamFile("remote.txt", []byte("first line\nsecond line\nthird line\n")),
		},
	}

	result := runView(t, root, access, map[string]any{"file_path": "remote.txt"})
	if !result.Success {
		t.Fatalf("view failed although the accessor served the file: %s", result.Content)
	}
	for _, want := range []string{"1: first line", "2: second line", "3: third line"} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("window missing %q, got:\n%s", want, result.Content)
		}
	}
	if access.statCalls != 1 || access.openCalls != 1 {
		t.Fatalf("accessor calls = stat %d open %d, want 1 and 1", access.statCalls, access.openCalls)
	}
	if len(access.opened) != 1 || !access.opened[0].closed {
		t.Fatalf("the handler did not close the handle it was given: %+v", access.opened)
	}
}

// TestHandlerNotFoundComesFromTheAccessor pins that absence is the accessor's
// answer, not the disk's: the local machine has a file at that exact path, and the
// view must still report NOT_FOUND because the injected filesystem says so.
func TestHandlerNotFoundComesFromTheAccessor(t *testing.T) {
	root := t.TempDir()
	shadow := filepath.Join(root, "shadow.txt")
	if err := os.WriteFile(shadow, []byte("local bytes that must never be read\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	access := &seamAccess{files: map[string]seamFile{}}

	result := runView(t, root, access, map[string]any{"file_path": "shadow.txt"})
	if result.Success {
		t.Fatalf("view served local bytes for a path the accessor calls absent:\n%s", result.Content)
	}
	if want := toolcore.NormalizeErrorCode(toolcore.ErrNotFound); result.ErrorCode != want {
		t.Fatalf("error code = %q, want %q", result.ErrorCode, want)
	}
	if !strings.Contains(result.Content, "路径不存在") {
		t.Fatalf("the not-found wording drifted: %q", result.Content)
	}
}

// TestHandlerDirectoryAutoListingUsesAccessor covers the auto-heal branch: the
// listing is a filesystem read, so it must describe the same machine as the stat.
func TestHandlerDirectoryAutoListingUsesAccessor(t *testing.T) {
	root := t.TempDir()
	dirPath := filepath.Join(root, "elsewhere")
	access := &seamAccess{
		files: map[string]seamFile{dirPath: newSeamDir("elsewhere")},
		children: map[string][]seamFile{
			dirPath: {newSeamFile("beta.txt", []byte("b\n")), newSeamDir("nested")},
		},
	}

	result := runView(t, root, access, map[string]any{"file_path": "elsewhere"})
	if !result.Success {
		t.Fatalf("directory auto-listing failed: %s", result.Content)
	}
	if !strings.Contains(result.Content, "路径是目录") {
		t.Fatalf("not the auto-heal answer, got:\n%s", result.Content)
	}
	for _, want := range []string{"beta.txt", "nested" + string(filepath.Separator)} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("listing missing %q, got:\n%s", want, result.Content)
		}
	}
	if access.readDirCalls != 1 {
		t.Fatalf("readDirCalls = %d, want 1 (the listing must come from the accessor)", access.readDirCalls)
	}
	if access.openCalls != 0 {
		t.Fatalf("the core opened a directory it had just been told is one: %d opens", access.openCalls)
	}
}

// TestBinaryNoteSniffsThroughAccessor keeps the split-brain honest: the size comes
// from Stat and the MIME type from a head read, and both must come from the same
// accessor. A local-only sniff would describe a file that shares the name.
func TestBinaryNoteSniffsThroughAccessor(t *testing.T) {
	root := t.TempDir()
	// A PDF signature in the head plus NUL-heavy body: the window classifies as
	// binary and the note must report the MIME the virtual bytes carry.
	body := "%PDF-1.4\n" + strings.Repeat("\x00garbage\n", 60)
	remotePath := filepath.Join(root, "doc.pdf")
	access := &seamAccess{files: map[string]seamFile{remotePath: newSeamFile("doc.pdf", []byte(body))}}

	result := runView(t, root, access, map[string]any{"file_path": "doc.pdf"})
	if !result.Success {
		t.Fatalf("binary note path failed: %s", result.Content)
	}
	if isBinary, _ := result.Metadata["is_binary"].(bool); !isBinary {
		t.Fatalf("NUL-heavy window was not flagged as binary:\n%s", result.Content)
	}
	mimeType, _ := result.Metadata["binary_mime"].(string)
	if mimeType != "application/pdf" {
		t.Fatalf("binary MIME = %q, want application/pdf (sniffed through the accessor)", mimeType)
	}
	if size, _ := result.Metadata["file_size"].(int64); size != int64(len(body)) {
		t.Fatalf("file_size = %v, want %d", result.Metadata["file_size"], len(body))
	}
	if access.openCalls < 2 {
		t.Fatalf("the MIME sniff did not go through the accessor: openCalls = %d", access.openCalls)
	}
}

// TestReadTextHonoursOptionsAccessor covers the entry point used by callers that
// hold no runtime: the accessor carried in Options must win over the disk.
func TestReadTextHonoursOptionsAccessor(t *testing.T) {
	root := t.TempDir()
	remotePath := filepath.Join(root, "virtual.txt")
	access := &seamAccess{files: map[string]seamFile{
		remotePath: newSeamFile("virtual.txt", []byte("only in memory\n")),
	}}

	content, stats, err := ReadText(remotePath, Request{Offset: 0, Limit: 10}, Options{Access: access})
	if err != nil {
		t.Fatalf("ReadText through the accessor: %v", err)
	}
	if !strings.Contains(content, "only in memory") {
		t.Fatalf("window content = %q", content)
	}
	if stats.LinesRead != 1 {
		t.Fatalf("LinesRead = %d, want 1", stats.LinesRead)
	}
	if access.openCalls != 1 {
		t.Fatalf("openCalls = %d, want 1", access.openCalls)
	}

	// The other half: with no accessor in Options the same call reads the real
	// file. This is what keeps every unconverted caller working.
	localPath := filepath.Join(root, "on-disk.txt")
	if err := os.WriteFile(localPath, []byte("on disk\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	content, _, err = ReadText(localPath, Request{Offset: 0, Limit: 10}, Options{})
	if err != nil {
		t.Fatalf("ReadText through the local default: %v", err)
	}
	if !strings.Contains(content, "on disk") {
		t.Fatalf("local default window = %q", content)
	}
}

// TestReadFileHeadKeepsTheLocalWrapper covers the four local sniffers that call
// the path-shaped entry without a runtime in hand.
func TestReadFileHeadKeepsTheLocalWrapper(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "head.bin")
	if err := os.WriteFile(path, []byte("abcdefghij"), 0o600); err != nil {
		t.Fatal(err)
	}
	head, err := ReadFileHead(path, 4)
	if err != nil {
		t.Fatalf("ReadFileHead: %v", err)
	}
	if string(head) != "abcd" {
		t.Fatalf("head = %q, want %q", head, "abcd")
	}

	remotePath := filepath.Join(root, "remote-head.bin")
	access := &seamAccess{files: map[string]seamFile{
		remotePath: newSeamFile("remote-head.bin", []byte("%PDF-1.4 virtual")),
	}}
	head, err = ReadFileHeadFromAccess(context.Background(), access, remotePath, 8)
	if err != nil {
		t.Fatalf("ReadFileHeadFromAccess: %v", err)
	}
	if string(head) != "%PDF-1.4" {
		t.Fatalf("accessor head = %q, want %q", head, "%PDF-1.4")
	}
}

// TestDirectoryPreviewKeepsTheLocalWrapper is the counterpart for the listing
// entry point the tests and the local surface call without an accessor.
func TestDirectoryPreviewKeepsTheLocalWrapper(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "shown.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	preview, err := DirectoryPreview(root)
	if err != nil {
		t.Fatalf("DirectoryPreview: %v", err)
	}
	if !strings.Contains(preview, "shown.txt") {
		t.Fatalf("local preview = %q", preview)
	}
}
