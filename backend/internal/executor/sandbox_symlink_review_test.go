package executor

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCheckPermissionResolvesSymlinkTargets pins the 2026-09-27 finding: the
// containment test compared the path as written, so a link inside an allowed
// directory could read a file outside the allowlist (or inside DeniedPaths).
// os.Stat/ReadFile follow the link, so the policy must resolve it too.
func TestCheckPermissionResolvesSymlinkTargets(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "outside.txt")
	if err := os.WriteFile(target, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	inside := filepath.Join(root, "inside.txt")
	if err := os.WriteFile(inside, []byte("inside"), 0o600); err != nil {
		t.Fatal(err)
	}
	insideLink := filepath.Join(root, "inside-link.txt")
	if err := os.Symlink(inside, insideLink); err != nil {
		t.Fatal(err)
	}

	sandbox := NewSandbox(&SandboxConfig{Enabled: true, AllowedPaths: []string{root}})
	if err := sandbox.CheckPermission(OpRead, link); err == nil {
		t.Fatal("a symlink escaping the allowlist must be denied")
	}
	if err := sandbox.CheckPermission(OpRead, inside); err != nil {
		t.Fatalf("a regular file inside the allowlist must stay readable: %v", err)
	}
	if err := sandbox.CheckPermission(OpRead, insideLink); err != nil {
		t.Fatalf("a symlink inside the allowlist must stay readable: %v", err)
	}

	// A create path goes through the symlinked parent directory: the leaf does
	// not exist yet, so only the ancestor resolution can catch the escape.
	escapeDir := filepath.Join(root, "escape")
	if err := os.Symlink(outside, escapeDir); err != nil {
		t.Fatal(err)
	}
	if err := sandbox.CheckPermission(OpWrite, filepath.Join(escapeDir, "new.txt")); err == nil {
		t.Fatal("a create path through an escaping symlinked directory must be denied")
	}
	if err := sandbox.CheckPermission(OpWrite, filepath.Join(root, "new.txt")); err != nil {
		t.Fatalf("a create path inside the allowlist must stay writable: %v", err)
	}

	deniedLink := NewSandbox(&SandboxConfig{Enabled: true, AllowedPaths: []string{root}, DeniedPaths: []string{target}})
	if err := deniedLink.CheckPermission(OpRead, link); err == nil {
		t.Fatal("a symlink resolving into DeniedPaths must be denied")
	}
}
