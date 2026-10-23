package searchcore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func grepMaxCountFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range map[string]string{
		"a.txt": "hit a1\nhit a2\nhit a3\n",
		"b.txt": "hit b1\nhit b2\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func matchLabels(result GrepResult) []string {
	out := make([]string, 0, len(result.Matches))
	for _, match := range result.Matches {
		out = append(out, fmt.Sprintf("%s:%d:%s", filepath.Base(match.File), match.Line, match.Text))
	}
	return out
}

// TestGrepMaxCountPerFile pins the shared per-file cap: matching lines stop at
// the cap in every file, an explicit 0 suppresses the file entirely (the rg -m 0
// contract), and an unset cap means no limit.
func TestGrepMaxCountPerFile(t *testing.T) {
	root := grepMaxCountFixture(t)

	cases := []struct {
		name string
		opts GrepOptions
		want []string
	}{
		{
			name: "unset keeps every match",
			opts: GrepOptions{Pattern: "hit"},
			want: []string{"a.txt:1:hit a1", "a.txt:2:hit a2", "a.txt:3:hit a3", "b.txt:1:hit b1", "b.txt:2:hit b2"},
		},
		{
			name: "cap one keeps the first match of each file",
			opts: GrepOptions{Pattern: "hit", MaxCount: 1, MaxCountSet: true},
			want: []string{"a.txt:1:hit a1", "b.txt:1:hit b1"},
		},
		{
			name: "cap two",
			opts: GrepOptions{Pattern: "hit", MaxCount: 2, MaxCountSet: true},
			want: []string{"a.txt:1:hit a1", "a.txt:2:hit a2", "b.txt:1:hit b1", "b.txt:2:hit b2"},
		},
		{
			name: "explicit zero suppresses every file",
			opts: GrepOptions{Pattern: "hit", MaxCount: 0, MaxCountSet: true},
			want: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := tc.opts
			opts.Path = root
			opts.Limit = 50
			result, err := Grep(context.Background(), opts)
			if err != nil {
				t.Fatalf("grep: %v", err)
			}
			assertFileList(t, matchLabels(result), tc.want)
			if result.Truncated {
				t.Fatalf("a per-file cap is not page truncation, got %+v", result)
			}
		})
	}
}

func TestGrepMaxCountAppliesToCountAndFilesModes(t *testing.T) {
	root := grepMaxCountFixture(t)

	counts, err := Grep(context.Background(), GrepOptions{
		Pattern: "hit", Path: root, Mode: GrepModeCount, Limit: 50,
		MaxCount: 1, MaxCountSet: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertFileList(t, summarizeNames(counts), []string{"a.txt:1", "b.txt:1"})

	files, err := Grep(context.Background(), GrepOptions{
		Pattern: "hit", Path: root, Mode: GrepModeFiles, Limit: 50,
		MaxCount: 0, MaxCountSet: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(files.Files) != 0 {
		t.Fatalf("max_count=0 must suppress every file in files mode, got %+v", files.Files)
	}
}
