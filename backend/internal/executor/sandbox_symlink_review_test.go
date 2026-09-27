package executor

import (
	"os"
	"path/filepath"
	"runtime"
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

// TestCheckPermissionResolvesDanglingSymlinkTarget pins the 2026-09-27 finding
// H1: a leaf link whose target does not exist yet made filepath.EvalSymlinks
// give up, so the authorization fell back to the lexical leaf and write then
// created the file outside the allowlist through the link.
func TestCheckPermissionResolvesDanglingSymlinkTarget(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(root, "link.txt")
	if err := os.Symlink(filepath.Join(outside, "new.txt"), link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(outside, "new.txt")); !os.IsNotExist(err) {
		t.Fatalf("fixture target must not exist yet: %v", err)
	}
	sandbox := NewSandbox(&SandboxConfig{Enabled: true, AllowedPaths: []string{root}})
	if err := sandbox.CheckPermission(OpWrite, link); err == nil {
		t.Fatal("a dangling symlink escaping the allowlist must be denied")
	}

	// A dangling relative link that stays inside the allowlist must still work.
	insideLink := filepath.Join(root, "inside-link.txt")
	if err := os.Symlink("created-later.txt", insideLink); err != nil {
		t.Fatal(err)
	}
	if err := sandbox.CheckPermission(OpWrite, insideLink); err != nil {
		t.Fatalf("a dangling link inside the allowlist must stay writable: %v", err)
	}
}

// TestCheckPermissionSymlinkDotDot pins finding H2: authorization must resolve
// "link/.." in kernel order (follow the link first, then go up), otherwise the
// checked path and the opened path differ. Unix-only: Windows path semantics do
// not apply.
func TestCheckPermissionSymlinkDotDot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix kernel path semantics")
	}
	root := t.TempDir()
	outside := t.TempDir()
	sub := filepath.Join(outside, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	jump := filepath.Join(root, "jump")
	if err := os.Symlink(sub, jump); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "secret.txt"), []byte("PUBLIC"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	sandbox := NewSandbox(&SandboxConfig{Enabled: true, AllowedPaths: []string{root}})
	// String concatenation (not filepath.Join) keeps the ".." in the request:
	// Join would clean it away before the policy ever sees the path.
	spelled := jump + "/../secret.txt"
	if err := sandbox.CheckPermission(OpRead, spelled); err == nil {
		t.Fatal("a link/.. spelling that reaches outside the allowlist must be denied")
	}
}

// TestCheckPermissionLinkedPolicyRoots pins finding H3: when the request path
// itself needs no resolution, a symlinked DeniedPaths / ReadOnlyPaths root must
// still be compared against the physical target.
func TestCheckPermissionLinkedPolicyRoots(t *testing.T) {
	root := t.TempDir()
	physical := filepath.Join(root, "physical")
	if err := os.MkdirAll(physical, 0o755); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(physical, "secret.txt")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(physical, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	denied := NewSandbox(&SandboxConfig{Enabled: true, AllowedPaths: []string{root}, DeniedPaths: []string{alias}})
	if err := denied.CheckPermission(OpRead, secret); err == nil {
		t.Fatal("reading the physical target of a linked DeniedPaths root must be denied")
	}

	readOnly := NewSandbox(&SandboxConfig{Enabled: true, AllowedPaths: []string{root}, ReadOnlyPaths: []string{alias}})
	if err := readOnly.CheckPermission(OpWrite, secret); err == nil {
		t.Fatal("writing the physical target of a linked ReadOnlyPaths root must be denied")
	}
	if err := readOnly.CheckPermission(OpRead, secret); err != nil {
		t.Fatalf("reads outside the linked read-only root must stay allowed: %v", err)
	}
}
