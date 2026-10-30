//go:build !windows

package searchcore

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// stubFilesystemIdentities replaces the identity seam for one test. The walk
// asks for the root first and then each subdirectory, so the stub maps a
// directory name onto a fake device id.
func stubFilesystemIdentities(t *testing.T, fn func(path string) (string, error)) {
	t.Helper()
	original := FilesystemIdentityFunc
	FilesystemIdentityFunc = fn
	t.Cleanup(func() { FilesystemIdentityFunc = original })
}

// TestGrepOneFileSystemStopsAtBoundary is the end-to-end case. The mount
// privileges needed for a real tmpfs are not available here, so the device
// boundary is injected: every directory named "mounted" reports a different
// device. That is exactly the signal filepath.WalkDir cannot see on its own.
func TestGrepOneFileSystemStopsAtBoundary(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"local.txt", "mounted/far.txt", "other/near.txt"} {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("needle "+name+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	stubFilesystemIdentities(t, func(path string) (string, error) {
		if filepath.Base(path) == "mounted" {
			return "other-device", nil
		}
		return "root-device", nil
	})

	withFlag, err := Grep(context.Background(), GrepOptions{
		Pattern: "needle", Path: root, Limit: 50, OneFileSystem: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertFileList(t, grepFileNames(withFlag), []string{"local.txt", "near.txt"})

	// Control: without the flag the same tree is searched completely.
	withoutFlag, err := Grep(context.Background(), GrepOptions{
		Pattern: "needle", Path: root, Limit: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertFileList(t, grepFileNames(withoutFlag), []string{"local.txt", "far.txt", "near.txt"})
}

// TestGrepOneFileSystemKeepsExplicitRootOnAnotherDevice pins the half of the
// contract that is easy to get wrong: the flag bounds recursion, it never
// rejects a root the caller named. The root reports a different device than
// everything else, and is still searched.
func TestGrepOneFileSystemKeepsExplicitRootOnAnotherDevice(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "explicit.txt"), []byte("needle explicit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stubFilesystemIdentities(t, func(path string) (string, error) {
		if filepath.Clean(path) == filepath.Clean(root) {
			return "root-device", nil
		}
		return "elsewhere", nil
	})

	result, err := Grep(context.Background(), GrepOptions{
		Pattern: "needle", Path: root, Limit: 50, OneFileSystem: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertFileList(t, grepFileNames(result), []string{"explicit.txt"})
}

// TestGrepOneFileSystemSingleFilesystemIsUnchanged is the control: when nothing
// crosses a boundary the flag must not alter the result.
func TestGrepOneFileSystemSingleFilesystemIsUnchanged(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"top.txt", "sub/nested.txt"} {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("needle "+name+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	stubFilesystemIdentities(t, func(string) (string, error) { return "same-device", nil })

	plain, err := Grep(context.Background(), GrepOptions{
		Pattern: "needle", Path: root, Limit: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	withFlag, err := Grep(context.Background(), GrepOptions{
		Pattern: "needle", Path: root, Limit: 50, OneFileSystem: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertFileList(t, grepFileNames(withFlag), grepFileNames(plain))
}

// TestFilesystemIdentitySeparatesDevices moved to internal/toolcore/localio/identity_test.go
// with the platform code it guards (S5c): the assertion is about how a platform reports a
// device - on Linux os.Stat answers *syscall.Stat_t, not *unix.Stat_t, and asserting only
// the latter once failed every call - and that is now localio's business, not a core's.
// What stays tested here is the core's own decision: which door the boundary question is
// asked of (see TestGrepOneFileSystemUsesTheAccessorsBoundary).
