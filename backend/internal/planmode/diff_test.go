package planmode

import (
	"errors"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/planstore"
)

func diffFixture(t *testing.T) (*planstore.Store, planstore.Record) {
	t.Helper()
	store := planstore.NewStore(t.TempDir())
	record, err := store.Record(planstore.RecordOptions{
		SessionID:   "session-1",
		ProjectPath: "/work/demo",
		PlanPath:    "docs/plan.md",
	})
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	for index, body := range []string{
		"# Plan\n\nstep one\nstep two\n",
		"# Plan\n\nstep one\nstep two revised\nstep three\n",
	} {
		decision := "enter"
		status := planstore.StatusPending
		if index > 0 {
			decision = "request_changes"
		}
		record, err = store.Snapshot(planstore.SnapshotOptions{
			ID:       record.ID,
			Decision: decision,
			Source:   "user",
			Notes:    "round notes",
			Content:  []byte(body),
			Status:   status,
		})
		if err != nil {
			t.Fatalf("snapshot %d: %v", index, err)
		}
	}
	return store, record
}

func TestUnifiedDiffIdenticalAndEmptyOld(t *testing.T) {
	identical := UnifiedDiff("a\nb\n", "a\nb\n", DiffOptions{})
	if !identical.Identical || identical.Text != "" || identical.Added != 0 {
		t.Fatalf("expected an identical result, got %+v", identical)
	}

	added := UnifiedDiff("", "first\nsecond\n", DiffOptions{})
	if added.Identical || added.Added != 2 || added.Removed != 0 {
		t.Fatalf("unexpected empty-old result: %+v", added)
	}
	for _, expected := range []string{"@@ -1,0 +1,2 @@", "+first", "+second"} {
		if !strings.Contains(added.Text, expected) {
			t.Fatalf("diff missing %q:\n%s", expected, added.Text)
		}
	}
}

func TestUnifiedDiffChangeWithContext(t *testing.T) {
	oldText := "line1\nline2\nline3\nline4\nline5\n"
	newText := "line1\nline2\nLINE3\nline4\nline5\n"
	result := UnifiedDiff(oldText, newText, DiffOptions{})
	if result.Added != 1 || result.Removed != 1 || result.Truncated || result.Coarse {
		t.Fatalf("unexpected counts: %+v", result)
	}
	if !strings.Contains(result.Text, "@@ -1,5 +1,5 @@") {
		t.Fatalf("unexpected hunk header:\n%s", result.Text)
	}
	for _, expected := range []string{" line1\n line2\n-line3\n+LINE3\n line4"} {
		if !strings.Contains(result.Text, expected) {
			t.Fatalf("diff missing %q:\n%s", expected, result.Text)
		}
	}
}

func TestUnifiedDiffSplitsDistantHunks(t *testing.T) {
	oldLines := make([]string, 0, 40)
	newLines := make([]string, 0, 40)
	for index := 0; index < 40; index++ {
		line := "line" + string(rune('a'+index%26)) + string(rune('0'+index/26))
		oldLines = append(oldLines, line)
		if index == 2 || index == 30 {
			newLines = append(newLines, line+"-changed")
			continue
		}
		newLines = append(newLines, line)
	}
	result := UnifiedDiff(strings.Join(oldLines, "\n")+"\n", strings.Join(newLines, "\n")+"\n", DiffOptions{})
	if count := strings.Count(result.Text, "@@ -"); count != 2 {
		t.Fatalf("expected two hunks, got %d:\n%s", count, result.Text)
	}
	if result.Added != 2 || result.Removed != 2 {
		t.Fatalf("unexpected counts: %+v", result)
	}
}

func TestUnifiedDiffBoundsAndCoarseFallback(t *testing.T) {
	oldLines := make([]string, 0, 900)
	newLines := make([]string, 0, 900)
	for index := 0; index < 900; index++ {
		oldLines = append(oldLines, "old-"+itoa(index))
		newLines = append(newLines, "new-"+itoa(index))
	}
	result := UnifiedDiff(strings.Join(oldLines, "\n")+"\n", strings.Join(newLines, "\n")+"\n", DiffOptions{
		MaxLines: 20,
	})
	if !result.Coarse {
		t.Fatalf("expected the coarse fallback for a full rewrite: %+v", result)
	}
	if !result.Truncated {
		t.Fatalf("expected truncation at 20 lines: %+v", result)
	}
	if !strings.Contains(result.Text, "diff 已截断") {
		t.Fatalf("expected a truncation note:\n%s", result.Text)
	}
	if result.Added != 900 || result.Removed != 900 {
		t.Fatalf("counts must stay accurate for the rendered part: %+v", result)
	}
}

func TestUnifiedDiffNoNewlineMarker(t *testing.T) {
	result := UnifiedDiff("a\nb", "a\nc", DiffOptions{})
	if !strings.Contains(result.Text, `\ No newline at end of file`) {
		t.Fatalf("expected the no-newline marker:\n%s", result.Text)
	}
}

func TestDiffArchivedVersionsLabelsRounds(t *testing.T) {
	store, record := diffFixture(t)

	result, err := DiffArchivedVersions(DiffVersionsOptions{Store: store, RecordID: record.ID})
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if result.Identical || result.Added != 2 || result.Removed != 1 {
		t.Fatalf("unexpected diff result: %+v", result)
	}
	for _, expected := range []string{"--- v1 enter (user, ", "+++ v2 request_changes (user, ", "-step two", "+step two revised", "+step three"} {
		if !strings.Contains(result.Text, expected) {
			t.Fatalf("diff missing %q:\n%s", expected, result.Text)
		}
	}

	// Explicit and derived ranges agree here: v1 -> v2 is the only pair.
	explicit, err := DiffArchivedVersions(DiffVersionsOptions{Store: store, RecordID: record.ID, From: 1, To: 2})
	if err != nil {
		t.Fatalf("explicit diff: %v", err)
	}
	if explicit.Text != result.Text {
		t.Fatalf("explicit range should match the derived one:\n%s\n%s", explicit.Text, result.Text)
	}

	// Same version on both sides is identical, not an error.
	same, err := DiffArchivedVersions(DiffVersionsOptions{Store: store, RecordID: record.ID, From: 2, To: 2})
	if err != nil {
		t.Fatalf("same-version diff: %v", err)
	}
	if !same.Identical {
		t.Fatalf("expected v2 vs v2 to be identical: %+v", same)
	}
}

func TestDiffArchivedVersionsErrors(t *testing.T) {
	store, record := diffFixture(t)

	if _, err := DiffArchivedVersions(DiffVersionsOptions{Store: store, RecordID: "demo/ghost"}); !errors.Is(err, planstore.ErrNotFound) {
		t.Fatalf("expected not-found, got %v", err)
	}
	if _, err := DiffArchivedVersions(DiffVersionsOptions{Store: store}); err == nil {
		t.Fatal("expected an empty-id error")
	}
	if _, err := DiffArchivedVersions(DiffVersionsOptions{Store: store, RecordID: record.ID, From: 9, To: 9}); err == nil {
		t.Fatal("expected an error for a version outside the retention window")
	}

	unsnapshotted, err := store.Record(planstore.RecordOptions{
		SessionID:   "session-2",
		ProjectPath: "/work/demo",
		PlanPath:    "docs/other.md",
	})
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if _, err := DiffArchivedVersions(DiffVersionsOptions{Store: store, RecordID: unsnapshotted.ID}); err == nil ||
		!strings.Contains(err.Error(), "no snapshot") {
		t.Fatalf("expected a no-snapshot error, got %v", err)
	}
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := make([]byte, 0, 4)
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}
