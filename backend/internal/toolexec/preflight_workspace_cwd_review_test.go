package toolexec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPathCandidatesDoNotScanProcessCwdUnderWorkspace pins finding H15: with a
// bound workspace, a missing relative path must not fall back to ranking the
// process CWD — that surfaced out-of-workspace file names as candidates while
// presenting them as workspace-relative.
func TestPathCandidatesDoNotScanProcessCwdUnderWorkspace(t *testing.T) {
	outside := t.TempDir()
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(outside, "keys"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "keys", "client.key"), []byte("k"), 0o600); err != nil {
		t.Fatal(err)
	}

	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(outside); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })

	healed, hints, _ := uniqueHighConfidencePathCandidate("keys/client", PreflightRequest{WorkspaceRoot: workspace})
	if healed != "" {
		t.Fatalf("no candidate exists inside the workspace, got healed=%q", healed)
	}
	for _, hint := range hints {
		if strings.Contains(hint, "client.key") {
			t.Fatalf("out-of-workspace candidate leaked into hints: %#v", hints)
		}
	}

	// Without a workspace root relative paths stay CWD-anchored, so the same
	// lookup may still offer the nearby sibling.
	healed, hints, _ = uniqueHighConfidencePathCandidate("keys/client", PreflightRequest{})
	if healed == "" && len(hints) == 0 {
		t.Fatalf("CWD-anchored candidates must keep working without a workspace root")
	}
}
