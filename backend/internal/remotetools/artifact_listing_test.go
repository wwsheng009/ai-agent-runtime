package remotetools_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/remotetools"
)

// artifactReadListed reports whether the default (non-custom) server exposes
// remote_dev__artifact_read.
func artifactReadListed(t *testing.T, cfg *remotetools.Config) bool {
	t.Helper()
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	server, err := remotetools.NewServer(cfg, "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()
	for _, name := range server.ToolNames() {
		if name == "remote_dev__artifact_read" {
			return true
		}
	}
	return false
}

// TestRemoteArtifactReadIsNotInTheDefaultSurface pins the gating rule: the tool
// is only advertised when the operator configured an artifact store.
//
// This matters because artifact_read dereferences ids produced on that target.
// Advertising it without a store would offer a tool whose every call fails with
// "artifact store is not configured" - a capability promised and not delivered.
func TestRemoteArtifactReadIsNotInTheDefaultSurface(t *testing.T) {
	cfg := newTestConfig(t, t.TempDir())
	if artifactReadListed(t, cfg) {
		t.Fatal("artifact_read must stay out of the default surface: no artifact store is configured")
	}
}

// TestRemoteArtifactReadAppearsWhenStoreIsConfigured is the other half: a
// configured store is what makes the tool real, and it must then be listed.
func TestRemoteArtifactReadAppearsWhenStoreIsConfigured(t *testing.T) {
	cfg := newTestConfig(t, t.TempDir())
	cfg.Artifacts.Root = filepath.Join(t.TempDir(), "artifacts")
	if !artifactReadListed(t, cfg) {
		t.Fatal("artifact_read must be advertised once an artifact store exists")
	}
}

// TestRemoteArtifactReadSurvivesReadOnlyGating locks the read-only contract:
// dereferencing an artifact is a read, so a read-only target must still expose
// it. Losing it here would strand every folded output an agent produced.
func TestRemoteArtifactReadSurvivesReadOnlyGating(t *testing.T) {
	cfg := newTestConfig(t, t.TempDir())
	cfg.Artifacts.Root = filepath.Join(t.TempDir(), "artifacts")
	cfg.ReadOnly = true
	if !artifactReadListed(t, cfg) {
		t.Fatal("artifact_read is a read tool and must survive the read-only filter")
	}
}

// TestRemoteArtifactReadWithoutStoreFailsCleanly documents what happens when an
// operator lists artifact_read explicitly but configures no store: the tool is
// advertised (custom profiles take the allowlist as the whole surface) and every
// call then fails with a stable, actionable code instead of a panic or a silent
// empty read.
func TestRemoteArtifactReadWithoutStoreFailsCleanly(t *testing.T) {
	root := t.TempDir()
	cfg := newTestConfig(t, root)
	cfg.Profile = remotetools.ProfileCustom
	cfg.Tools = []string{"grep", "artifact_read"}
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	server, err := remotetools.NewServer(cfg, "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()
	session, ctx := startTestSession(t, server)

	out := callToolExpectingError(t, ctx, session, "remote_dev__artifact_read", map[string]interface{}{
		"artifact_id": "art_00000000000000000000000000000000",
	})
	if !strings.Contains(out, "UNSUPPORTED") && !strings.Contains(out, "artifact") {
		t.Fatalf("expected an actionable failure naming the missing store, got:\n%s", out)
	}
}

// TestRemoteArtifactReadIsNarrowableByToolList confirms the allowlist still acts
// as a filter even when the store exists.
func TestRemoteArtifactReadIsNarrowableByToolList(t *testing.T) {
	cfg := newTestConfig(t, t.TempDir())
	cfg.Artifacts.Root = filepath.Join(t.TempDir(), "artifacts")
	cfg.Tools = []string{"grep"}
	if artifactReadListed(t, cfg) {
		t.Fatal("an explicit tool list must be able to exclude artifact_read")
	}
}
