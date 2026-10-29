package localio

// A "move, not a rewrite" is a claim, and a claim needs a test. These cases run
// the seam and the os.* calls it replaced side by side over the same files: any
// difference in shape or error behaviour would mean Phase 2.5a changed semantics
// while pretending not to.

import (
	"context"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
)

func TestStatMatchesOsStatExactly(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "note.txt")
	if err := os.WriteFile(path, []byte("hello\n"), 0o640); err != nil {
		t.Fatal(err)
	}

	want, wantErr := os.Stat(path)
	got, gotErr := Access.Stat(context.Background(), path)
	if (wantErr == nil) != (gotErr == nil) {
		t.Fatalf("error presence differs: os=%v seam=%v", wantErr, gotErr)
	}
	if wantErr != nil {
		t.Fatalf("os.Stat failed: %v", wantErr)
	}
	if got.Name() != want.Name() || got.Size() != want.Size() ||
		got.Mode() != want.Mode() || got.IsDir() != want.IsDir() ||
		!got.ModTime().Equal(want.ModTime()) {
		t.Fatalf("seam Stat = %v/%d/%v/%v, os.Stat = %v/%d/%v/%v",
			got.Name(), got.Size(), got.Mode(), got.IsDir(),
			want.Name(), want.Size(), want.Mode(), want.IsDir())
	}
}

func TestOpenHandleStreamsAndSeeksLikeTheFileItReplace(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "window.txt")
	body := "line one\nline two\nline three\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	file, err := Access.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("seam Open: %v", err)
	}
	defer file.Close()

	// The view core's probe pattern: peek four bytes, rewind, then read the whole
	// file. A handle that could not seek would either swallow the head or force a
	// re-open, and the window would start on the wrong line.
	head := make([]byte, 4)
	if _, err := io.ReadFull(file, head); err != nil {
		t.Fatalf("peek head: %v", err)
	}
	if string(head) != body[:4] {
		t.Fatalf("head = %q, want %q", head, body[:4])
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		t.Fatalf("rewind: %v", err)
	}
	rest, err := io.ReadAll(file)
	if err != nil {
		t.Fatalf("read all: %v", err)
	}
	if string(rest) != body {
		t.Fatalf("after rewind = %q, want %q", rest, body)
	}

	info, err := file.Stat()
	if err != nil {
		t.Fatalf("handle Stat: %v", err)
	}
	if info.Size() != int64(len(body)) {
		t.Fatalf("handle size = %d, want %d", info.Size(), len(body))
	}
}

func TestReadDirMatchesOsReadDir(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}

	want, wantErr := os.ReadDir(root)
	got, gotErr := Access.ReadDir(context.Background(), root)
	if wantErr != nil || gotErr != nil {
		t.Fatalf("errors: os=%v seam=%v", wantErr, gotErr)
	}
	if len(got) != len(want) {
		t.Fatalf("entry count = %d, want %d", len(got), len(want))
	}
	for index := range want {
		if got[index].Name() != want[index].Name() || got[index].IsDir() != want[index].IsDir() {
			t.Fatalf("entry %d: seam = %s(dir=%v), os = %s(dir=%v)", index,
				got[index].Name(), got[index].IsDir(), want[index].Name(), want[index].IsDir())
		}
	}
}

func TestMissingPathIsAbsenceUnderBothVocabularies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nope.txt")

	_, seamErr := Access.Stat(context.Background(), path)
	if seamErr == nil {
		t.Fatal("Stat on a missing path must fail")
	}
	if !toolcore.IsNotExist(seamErr) {
		t.Fatalf("seam error is not classified as absence: %v", seamErr)
	}
	// The pre-seam predicate the cores used to call. If these two ever disagree,
	// an error message changes during a refactor that promised not to change one.
	if !os.IsNotExist(seamErr) {
		t.Fatalf("os.IsNotExist disagrees with the seam classifier: %v", seamErr)
	}

	_, dirErr := Access.ReadDir(context.Background(), path)
	if !toolcore.IsNotExist(dirErr) {
		t.Fatalf("ReadDir absence not classified: %v", dirErr)
	}
	_, openErr := Access.Open(context.Background(), path)
	if !toolcore.IsNotExist(openErr) {
		t.Fatalf("Open absence not classified: %v", openErr)
	}
}

// plainRuntime is a surface that has not been converted to the seam. It answers
// every toolcore.Runtime method but offers no accessor, and it must keep getting
// the local filesystem: that construction is why unconverted surfaces need no
// change in this phase.
type plainRuntime struct{}

func (plainRuntime) Paths() toolcore.PathResolver            { return nil }
func (plainRuntime) Permissions() toolcore.PermissionChecker { return nil }
func (plainRuntime) Commands() toolcore.CommandPolicy        { return nil }
func (plainRuntime) Env() toolcore.EnvProvider               { return nil }
func (plainRuntime) Output() toolcore.OutputShaper           { return nil }
func (plainRuntime) Artifacts() toolcore.ArtifactSink        { return nil }
func (plainRuntime) Sessions() toolcore.SessionState         { return nil }
func (plainRuntime) Clock() toolcore.Clock                   { return nil }
func (plainRuntime) IDs() toolcore.IDGenerator               { return nil }

// remoteRuntime is the optional extension implemented.
type remoteRuntime struct {
	plainRuntime
	access toolcore.Access
}

func (r remoteRuntime) FileAccess() toolcore.Access { return r.access }

// stubAccess is enough to prove identity: For must hand back exactly what the
// Runtime offered, not a copy or the local default.
type stubAccess struct{}

func (stubAccess) Stat(context.Context, string) (fs.FileInfo, error)         { return nil, nil }
func (stubAccess) Open(context.Context, string) (toolcore.FileHandle, error) { return nil, nil }
func (stubAccess) ReadDir(context.Context, string) ([]fs.DirEntry, error)    { return nil, nil }

func TestForResolvesTheInjectedAccessorAndDefaultsToLocal(t *testing.T) {
	stub := stubAccess{}

	tests := []struct {
		name   string
		rt     toolcore.Runtime
		want   toolcore.Access
		sameAs toolcore.Access
	}{
		{"nil runtime falls back to local", nil, Access, Access},
		{"runtime without the provider falls back to local", plainRuntime{}, Access, Access},
		{"runtime with a provider wins", remoteRuntime{access: stub}, stub, stub},
		{"provider returning nil falls back to local", remoteRuntime{access: nil}, Access, Access},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := For(test.rt)
			if got == nil {
				t.Fatal("For returned nil; a core would panic on the first read")
			}
			if anyAccess(got) != anyAccess(test.sameAs) {
				t.Fatalf("For = %T, want %T", got, test.sameAs)
			}
		})
	}
}

// anyAccess puts two different interface values on the same footing so the
// comparison above can be an identity check.
func anyAccess(access toolcore.Access) interface{} { return access }

// TestLocalHandleSatisfiesSeamWithoutWrapper is the compile-time half of the
// "this is a move" claim, stated as a test so it is reported on failure rather
// than silently drifting.
func TestLocalHandleSatisfiesSeamWithoutWrapper(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "typed.txt")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := Access.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, isOsFile := file.(*os.File); !isOsFile {
		t.Fatalf("the local handle is %T, not *os.File: the move added a wrapper", file)
	}
	var _ fs.FileInfo = func() fs.FileInfo {
		info, err := file.Stat()
		if err != nil {
			t.Fatalf("handle Stat: %v", err)
		}
		return info
	}()
}
