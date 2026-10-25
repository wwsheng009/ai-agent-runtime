package searchcore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// contextSeparatorFixture lays out two files that each contribute one context
// group, which is the case a separator sits between.
func contextSeparatorFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range map[string]string{"a.txt": "one\nTARGET\n", "b.txt": "two\nTARGET\n"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// TestGrepContextSeparatorIsRenderedByTheSharedRenderer pins the separator
// contract: it is produced by the renderer every engine shares, so a custom
// string, the default "--" and "nothing at all" all mean the same thing on the
// walker and on ripgrep. The metadata reports the value that actually printed.
func TestGrepContextSeparatorIsRenderedByTheSharedRenderer(t *testing.T) {
	root := contextSeparatorFixture(t)

	cases := []struct {
		name          string
		args          map[string]any
		wantSeparator string
		wantMeta      any
	}{
		{
			name:          "default",
			args:          map[string]any{"pattern": "TARGET", "context": 1},
			wantSeparator: "--",
			wantMeta:      "--",
		},
		{
			name:          "custom string",
			args:          map[string]any{"pattern": "TARGET", "context": 1, "context_separator": "~~~"},
			wantSeparator: "~~~",
			wantMeta:      "~~~",
		},
		{
			name:          "blank keeps the default",
			args:          map[string]any{"pattern": "TARGET", "context": 1, "context_separator": "   "},
			wantSeparator: "--",
			wantMeta:      "--",
		},
		{
			name: "no_context_separator wins over a value",
			args: map[string]any{
				"pattern": "TARGET", "context": 1, "context_separator": "~~~", "no_context_separator": true,
			},
			wantSeparator: "",
			wantMeta:      "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := runGrepHandler(t, root, tc.args)
			lines := strings.Split(result.Content, "\n")

			found := false
			for _, line := range lines {
				if tc.wantSeparator != "" && line == tc.wantSeparator {
					found = true
				}
				if tc.wantSeparator == "" && (line == "--" || line == "~~~") {
					t.Fatalf("no separator may be printed when it is disabled:\n%s", result.Content)
				}
			}
			if tc.wantSeparator != "" && !found {
				t.Fatalf("the separator %q must stand on its own line:\n%s", tc.wantSeparator, result.Content)
			}
			if result.Metadata["context_separator"] != tc.wantMeta {
				t.Fatalf("metadata.context_separator = %#v, want %#v",
					result.Metadata["context_separator"], tc.wantMeta)
			}
			// Both groups must still be there: disabling the separator joins
			// them, it does not drop a file.
			for _, want := range []string{"a.txt", "b.txt"} {
				if !strings.Contains(result.Content, want) {
					t.Fatalf("%s must still be listed:\n%s", want, result.Content)
				}
			}
		})
	}
}

// TestGrepHeadLimitIsThePageSizeSpelling pins the paging contract for the local
// spelling: it is the same concept as limit, it wins when both are present, and
// an explicit 0 means "no page cap" up to the safety ceiling - never a silent
// switch back to the default page.
func TestGrepHeadLimitIsThePageSizeSpelling(t *testing.T) {
	root := t.TempDir()
	var content strings.Builder
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		content.Reset()
		content.WriteString("needle\n")
		if err := os.WriteFile(filepath.Join(root, name), []byte(content.String()), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("page size", func(t *testing.T) {
		result := runGrepHandler(t, root, map[string]any{"pattern": "needle", "head_limit": 1})
		if got := strings.Count(result.Content, "needle"); got != 1 {
			t.Fatalf("head_limit 1 must return one match, got %d:\n%s", got, result.Content)
		}
		if result.Metadata["has_more"] != true {
			t.Fatalf("two more files matched, so has_more must be true: %#v", result.Metadata)
		}
		if result.Metadata["next_offset"] != 1 {
			t.Fatalf("next_offset = %#v, want 1", result.Metadata["next_offset"])
		}
		if result.Metadata["head_limit_explicit"] != true || result.Metadata["head_limit"] != 1 {
			t.Fatalf("head_limit metadata must state the effective page size: %#v", result.Metadata)
		}
	})

	t.Run("head_limit wins over limit", func(t *testing.T) {
		result := runGrepHandler(t, root, map[string]any{"pattern": "needle", "limit": 5, "head_limit": 1})
		if got := strings.Count(result.Content, "needle"); got != 1 {
			t.Fatalf("the local spelling must win, got %d matches:\n%s", got, result.Content)
		}
	})

	t.Run("explicit zero means no page cap", func(t *testing.T) {
		result := runGrepHandler(t, root, map[string]any{"pattern": "needle", "head_limit": 0})
		if got := strings.Count(result.Content, "needle"); got != 3 {
			t.Fatalf("head_limit 0 must not fall back to the default page, got %d matches:\n%s", got, result.Content)
		}
		if result.Metadata["limit"] != GrepUnlimitedPageSize {
			t.Fatalf("limit = %#v, want the safety ceiling %d", result.Metadata["limit"], GrepUnlimitedPageSize)
		}
	})

	t.Run("the safety ceiling bounds both spellings", func(t *testing.T) {
		big := runGrepHandler(t, root, map[string]any{"pattern": "needle", "head_limit": 9999})
		if big.Metadata["limit"] != GrepUnlimitedPageSize {
			t.Fatalf("head_limit must be clamped to the ceiling, got %#v", big.Metadata["limit"])
		}
		core := runGrepHandler(t, root, map[string]any{"pattern": "needle", "limit": 9999})
		if core.Metadata["limit"] != GrepUnlimitedPageSize {
			t.Fatalf("limit must be clamped to the same ceiling, got %#v", core.Metadata["limit"])
		}
	})
}
