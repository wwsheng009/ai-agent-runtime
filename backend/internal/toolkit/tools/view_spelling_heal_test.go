package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestViewHealsUnicodeSpellingCandidate: a request whose only difference from
// the on-disk name is an invisible code point must resolve to the real file,
// with the repair stated in the result (analysis §3.7).
func TestViewHealsUnicodeSpellingCandidate(t *testing.T) {
	t.Setenv("AICLI_VIEW_DEDUP", "")
	root := t.TempDir()
	name := "report\u202Ffinal.txt"
	if err := os.WriteFile(filepath.Join(root, name), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	tool := newViewToolAt(t, root)
	result := executeViewParams(t, tool, context.Background(), map[string]interface{}{
		"file_path": "report final.txt",
	})
	if !strings.Contains(result.Content, "1: hello") {
		t.Fatalf("expected the healed file content, got %q", result.Content)
	}
	if result.Metadata["path_auto_healed"] != true {
		t.Fatalf("expected path_auto_healed metadata, got %#v", result.Metadata)
	}
	resolved, _ := result.Metadata["resolved_path"].(string)
	if !strings.Contains(resolved, "\u202F") {
		t.Fatalf("expected the resolved path to keep the disk spelling, got %q", resolved)
	}
	if !strings.Contains(result.Content, "Unicode") {
		t.Fatalf("expected the repair note, got %q", result.Content)
	}
}

// TestViewSpellingAmbiguousCandidates: two folded-name candidates must not be
// auto-picked; the error lists them so the model can choose.
func TestViewSpellingAmbiguousCandidates(t *testing.T) {
	t.Setenv("AICLI_VIEW_DEDUP", "")
	root := t.TempDir()
	for _, name := range []string{"don\u2019t.txt", "don\u2018t.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("x\n"), 0o644); err != nil {
			t.Fatalf("seed %q: %v", name, err)
		}
	}
	tool := newViewToolAt(t, root)
	result, err := tool.Execute(context.Background(), map[string]interface{}{"file_path": "don't.txt"})
	if err != nil {
		t.Fatalf("unexpected tool error: %v", err)
	}
	if result == nil || result.Success {
		t.Fatalf("ambiguous spelling must not be auto-healed, got %+v", result)
	}
	candidates, _ := result.Metadata["path_candidates"].([]string)
	if len(candidates) != 2 {
		t.Fatalf("expected both folded-name candidates, got %#v", result.Metadata["path_candidates"])
	}
	if result.Metadata["path_auto_heal"] != "ambiguous" {
		t.Fatalf("expected ambiguous marker, got %#v", result.Metadata)
	}
}
