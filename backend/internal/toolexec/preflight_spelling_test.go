package toolexec

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRankNearbyPathCandidatesScoresUnicodeSpelling pins the shared folding
// table in the preflight ranking: a name that differs only in invisible
// Unicode code points must score as high as a case-only mismatch so a unique
// candidate can be auto-healed (analysis §3.7).
func TestRankNearbyPathCandidatesScoresUnicodeSpelling(t *testing.T) {
	dir := t.TempDir()
	name := "report\u202Ffinal.txt"
	if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	scored := rankNearbyPathCandidates(filepath.Join(dir, "report final.txt"))
	if len(scored) == 0 {
		t.Fatalf("expected the folded-name candidate to rank")
	}
	if scored[0].score != 100 {
		t.Fatalf("expected score 100 for folded-name equality, got %d (%s)", scored[0].score, scored[0].path)
	}
	if filepath.Base(scored[0].path) != name {
		t.Fatalf("unexpected top candidate %q", scored[0].path)
	}
}

// TestUniqueHighConfidencePathCandidateHealsUnicodeSpelling verifies the
// end-to-end preflight decision: unique folded candidate -> auto-heal, no
// ambiguity flag.
func TestUniqueHighConfidencePathCandidateHealsUnicodeSpelling(t *testing.T) {
	dir := t.TempDir()
	name := "don\u2019t.txt"
	if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	healed, _, ambiguous := uniqueHighConfidencePathCandidate(
		filepath.Join(dir, "don't.txt"),
		PreflightRequest{WorkspaceRoot: dir},
	)
	if ambiguous {
		t.Fatalf("single folded-name candidate must not be ambiguous")
	}
	if healed == "" || filepath.Base(healed) != name {
		t.Fatalf("expected auto-heal to %q, got %q", name, healed)
	}
}
