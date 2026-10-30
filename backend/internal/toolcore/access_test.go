package toolcore

// The seam's only real policy is what an accessor's error means: a shared core
// asks "is this path absent?" and must get the same answer for a syscall and for a
// transport that reports absence in its own way. If that classification lived in
// each core, every backend would need its own copy of "not found", and a
// permission error would eventually be misread as a missing file - which makes a
// tool lie about the reason it failed.

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// remoteAbsent is the shape a non-local accessor contributes: an error carrying
// its own notion of absence.
type remoteAbsent struct{ path string }

func (e remoteAbsent) Error() string { return fmt.Sprintf("rpc: no such file %s", e.path) }
func (remoteAbsent) NotExist() bool  { return true }

type remoteDenied struct{ path string }

func (e remoteDenied) Error() string { return fmt.Sprintf("rpc: permission denied %s", e.path) }

func TestIsNotExistClassifiesEveryAbsenceShape(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "definitely-not-here.txt")
	_, statErr := os.Stat(missing)
	if statErr == nil {
		t.Fatal("fixture is expected to be absent")
	}

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"bare fs.ErrNotExist", fs.ErrNotExist, true},
		{"os.Stat on a missing path", statErr, true},
		{"wrapped fs.ErrNotExist", fmt.Errorf("context: %w", fs.ErrNotExist), true},
		{"accessor absence", remoteAbsent{path: "a.txt"}, true},
		{"wrapped accessor absence", fmt.Errorf("read window: %w", remoteAbsent{path: "a.txt"}), true},
		// The one that decides whether a failure message is honest: a file that
		// exists but cannot be opened must never be reported as missing, or the
		// core tells the model to go look for a file that is right there.
		{"permission denied", fs.ErrPermission, false},
		{"accessor error without the hook", remoteDenied{path: "a.txt"}, false},
		{"unrelated error", errors.New("connection reset"), false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := IsNotExist(test.err); got != test.want {
				t.Fatalf("IsNotExist(%v) = %v, want %v", test.err, got, test.want)
			}
		})
	}
}

// TestLocalAbsenceStillMatchesOsIsNotExist pins the equivalence that makes the
// Phase 2.5a move a move: for host errors, the seam's classifier and the os.*
// predicate the cores used before must never disagree.
func TestLocalAbsenceStillMatchesOsIsNotExist(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "gone.txt")
	_, statErr := os.Stat(missing)

	// A directory the caller cannot traverse is not an absent path. On platforms
	// where the check cannot be made to fail (running as admin or root) the error
	// is nil and the case is skipped rather than faked.
	blocked := filepath.Join(root, "blocked", "inner.txt")
	deniedErr := func() error {
		if err := os.MkdirAll(filepath.Join(root, "blocked"), 0o755); err != nil {
			return nil
		}
		if err := os.Chmod(filepath.Join(root, "blocked"), 0o000); err != nil {
			return nil
		}
		t.Cleanup(func() { _ = os.Chmod(filepath.Join(root, "blocked"), 0o755) })
		_, err := os.Stat(blocked)
		return err
	}()

	if IsNotExist(statErr) != os.IsNotExist(statErr) {
		t.Fatalf("seam and os predicate disagree on a missing path: %v vs %v",
			IsNotExist(statErr), os.IsNotExist(statErr))
	}
	if deniedErr == nil {
		t.Skip("cannot construct a permission failure on this platform")
	}
	if os.IsNotExist(deniedErr) {
		t.Skipf("platform reports the blocked path as absent: %v", deniedErr)
	}
	if IsNotExist(deniedErr) {
		t.Fatalf("a blocked path is classified as absent: %v", deniedErr)
	}
}

// errno mapping (ENOENT, ERROR_FILE_NOT_FOUND) is what makes
// errors.Is(err, fs.ErrNotExist) true for the host errors above; the equivalence
// test pins it so a future refactor cannot quietly widen absence into "any error".
// styleStubAccess satisfies Access without answering anything: JoinTargetPath is a pure
// decision about spelling, and a fake that could be called would only invite a test to
// depend on I/O that the function under test never performs.
type styleStubAccess struct{}

func (styleStubAccess) Stat(context.Context, string) (fs.FileInfo, error) {
	return nil, errors.New("unused")
}
func (styleStubAccess) Lstat(context.Context, string) (fs.FileInfo, error) {
	return nil, errors.New("unused")
}
func (styleStubAccess) Open(context.Context, string) (FileHandle, error) {
	return nil, errors.New("unused")
}
func (styleStubAccess) ReadDir(context.Context, string) ([]fs.DirEntry, error) {
	return nil, errors.New("unused")
}

// slashStyleStub is a POSIX target: the case that motivated the capability.
type slashStyleStub struct{ styleStubAccess }

func (slashStyleStub) PathSeparator() rune { return '/' }

// TestJoinTargetPathFollowsTheAccessorsConvention pins the single place that decides how a
// core spells a child path.
//
// Both halves matter. The default must stay this process's own convention, because a local
// walk cannot change just because the seam exists - and the spelling is not cosmetic: the
// path a core builds is echoed back in results, so a local listing that suddenly used
// slashes on Windows would be a visible regression. The POSIX half matters because the
// failure it prevents is invisible: a Windows client walking a Linux target with the host's
// separator asks the daemon for "\srv\work\a.txt", gets "not found" for every entry, and
// reports an empty result rather than an error.
func TestJoinTargetPathFollowsTheAccessorsConvention(t *testing.T) {
	const dir, name = "/srv/work", "a.txt"

	hostConvention := filepath.Join(dir, name)
	if got := JoinTargetPath(nil, dir, name); got != hostConvention {
		t.Fatalf("no accessor must keep this process's convention: got %q, want %q", got, hostConvention)
	}
	if got := JoinTargetPath(styleStubAccess{}, dir, name); got != hostConvention {
		t.Fatalf("an accessor without the capability must keep this process's convention: got %q, want %q", got, hostConvention)
	}
	if got := JoinTargetPath(slashStyleStub{}, dir, name); got != "/srv/work/a.txt" {
		t.Fatalf("a POSIX target must get slashes on every host: got %q", got)
	}

	// On this host the two conventions differ; asserting that keeps the capability from
	// becoming decorative if someone ever makes filepath.Join slash-shaped on Windows.
	if runtime.GOOS == "windows" {
		posix := JoinTargetPath(slashStyleStub{}, dir, name)
		if posix == hostConvention {
			t.Fatalf("the capability changed nothing on Windows: both spellings are %q", posix)
		}
	}
}
