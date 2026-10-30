package localio

// The filesystem-boundary guard moved here with the implementation it guards. It belongs in
// this package for the same reason the host calls do: the assertion is about how a *platform*
// reports a device, and no core can test that without reaching through the seam.

import (
	"context"
	"os"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
)

// TestFilesystemIdentitySeparatesDevices is the guard for the bug that made the old local
// implementation unusable: os.Stat returns *syscall.Stat_t on Linux, not *unix.Stat_t, so
// asserting only the latter failed every call and one_file_system could never run.
// /dev/shm and /tmp are different devices on mainstream Linux and need no privileges.
//
// On Windows the second half skips (there is no /dev/shm), and the first half still proves
// the moved function answers; the Windows branch is a path-string volume name with no type
// assertion to get wrong.
func TestFilesystemIdentitySeparatesDevices(t *testing.T) {
	primary := t.TempDir()
	id, err := FilesystemIdentity(primary)
	if err != nil {
		t.Fatalf("FilesystemIdentity(%s): %v", primary, err)
	}
	if id == "" {
		t.Fatal("FilesystemIdentity returned an empty filesystem id")
	}

	// The accessor's answer for the same path must be the same answer, because one core
	// decision - whether a directory is still inside the root's filesystem - cannot be
	// allowed to differ depending on which door was knocked on.
	capability, ok := Access.(toolcore.FilesystemIdentifier)
	if !ok {
		t.Fatal("the local accessor must provide the filesystem capability, or one_file_system would decline on every local search")
	}
	viaSeam, err := capability.FilesystemIdentity(context.Background(), primary)
	if err != nil {
		t.Fatalf("accessor FilesystemIdentity: %v", err)
	}
	if viaSeam != id {
		t.Fatalf("accessor answer %q differs from the host function %q", viaSeam, id)
	}

	other, statErr := os.Stat("/dev/shm")
	if statErr != nil || !other.IsDir() {
		t.Skip("/dev/shm is not available")
	}
	otherID, err := FilesystemIdentity("/dev/shm")
	if err != nil {
		t.Fatalf("FilesystemIdentity(/dev/shm): %v", err)
	}
	if otherID == id {
		t.Skipf("/dev/shm shares the filesystem id %q with the temp dir", id)
	}
}
