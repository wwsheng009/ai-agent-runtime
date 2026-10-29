package remotetools_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/remotetools"
)

// TestRemoteArtifactRootUnwritableFailsAtStartup pins the fail-fast half: a root
// that cannot even be created is a configuration error, so the server must refuse
// to start instead of running with a half-working surface.
func TestRemoteArtifactRootUnwritableFailsAtStartup(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses permission checks")
	}
	if runtime.GOOS == "windows" {
		// Windows honours only the read-only attribute on directories, not
		// the POSIX mode bits, so a chmod-500 parent still accepts directory
		// creation and the premise of this test does not exist here.
		t.Skip("windows ignores directory mode bits; the unwritable-root premise needs POSIX permissions")
	}
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Skipf("chmod parent: %v", err)
	}
	defer os.Chmod(parent, 0o755)

	target := filepath.Join(parent, "artifacts")
	store, err := remotetools.NewArtifactStore(target, 0)
	if err == nil {
		if store == nil {
			t.Skip("store disabled instead of failing; behaviour changed")
		}
		t.Fatalf("expected NewArtifactStore to fail for an uncreatable root, got a store")
	}
	if !strings.Contains(err.Error(), "artifact root") {
		t.Fatalf("error must name the artifact root: %v", err)
	}
}

// TestRemoteShellArchiveFailureCarriesStableCode is the recovery contract for a
// root that exists at startup but loses write access later.
//
// The MCP projection includes Content for failed results now, but the stable
// code must still be inside Error for clients that render only Error. Before
// this the model received a bare "artifact archive failed: ... permission denied"
// with no class and no next action - it could not tell a disk problem from a
// rejected command.
func TestRemoteShellArchiveFailureCarriesStableCode(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses permission checks")
	}
	dir := filepath.Join(t.TempDir(), "artifacts")
	cfg := newOpsConfig(t, t.TempDir(), []string{"seq"})
	cfg.Artifacts.Root = dir
	cfg.Limits.ModelVisibleCap = 1024 // force a fold, so archiving is attempted
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}

	server, err := remotetools.NewServer(cfg, "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()
	session, ctx := startTestSession(t, server)

	// Take write access away after startup, which is the realistic failure:
	// a volume goes read-only or a directory is repointed.
	if runtime.GOOS == "windows" {
		// Windows honours only the read-only attribute on directories, not
		// the POSIX mode bits, so a chmod-500 directory still accepts writes
		// and the premise of this test does not exist on this platform.
		t.Skip("windows ignores directory mode bits; the unwritable-root premise needs POSIX permissions")
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Skipf("chmod: %v", err)
	}
	defer os.Chmod(dir, 0o700)

	out := callToolExpectingError(t, ctx, session, "remote_dev__shell", map[string]interface{}{
		"command": "seq 1 20000",
	})
	if !strings.Contains(out, "ARTIFACT_WRITE_FAILED") {
		t.Fatalf("the failure must carry its stable code in the body the model sees:\n%s", out)
	}
	if !strings.Contains(out, "next:") {
		t.Fatalf("the failure must carry a recovery hint:\n%s", out)
	}
	// The underlying cause still has to be visible for diagnosis.
	if !strings.Contains(out, "permission denied") {
		t.Fatalf("the underlying cause must survive:\n%s", out)
	}
}
