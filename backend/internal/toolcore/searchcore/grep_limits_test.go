package searchcore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseGrepSize(t *testing.T) {
	cases := []struct {
		raw     string
		want    int64
		wantErr bool
	}{
		{"1024", 1024, false},
		{"10K", 10 * 1024, false},
		{"2M", 2 * 1024 * 1024, false},
		{"1G", 1024 * 1024 * 1024, false},
		{"512B", 512, false},
		{" 10k ", 10 * 1024, false},
		{"", 0, true},
		{"-1", 0, true},
		{"abc", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			got, err := ParseGrepSize(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseGrepSize(%q) must fail", tc.raw)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseGrepSize(%q): %v", tc.raw, err)
			}
			if got != tc.want {
				t.Fatalf("ParseGrepSize(%q) = %d, want %d", tc.raw, got, tc.want)
			}
		})
	}
}

func grepDepthFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for path, content := range map[string]string{
		"top.txt":          "needle top\n",
		"dir/mid.txt":      "needle mid\n",
		"dir/sub/deep.txt": "needle deep\n",
	} {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// TestGrepMaxDepth pins the shared depth contract: depth is counted from each
// search root, and an explicit 0 searches only the root's own files.
func TestGrepMaxDepth(t *testing.T) {
	root := grepDepthFixture(t)

	cases := []struct {
		name string
		opts GrepOptions
		want []string
	}{
		{
			name: "unset searches everything",
			opts: GrepOptions{Pattern: "needle"},
			want: []string{"dir/mid.txt", "dir/sub/deep.txt", "top.txt"},
		},
		{
			name: "depth zero keeps only root files",
			opts: GrepOptions{Pattern: "needle", MaxDepth: 0, MaxDepthSet: true},
			want: []string{"top.txt"},
		},
		{
			name: "depth one allows one directory down",
			opts: GrepOptions{Pattern: "needle", MaxDepth: 1, MaxDepthSet: true},
			want: []string{"dir/mid.txt", "top.txt"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertFileList(t, grepFilterMatches(t, root, tc.opts), tc.want)
		})
	}
}

func TestGrepMaxFileSize(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "small.txt"), []byte("needle small\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	big := "needle big\n" + strings.Repeat("padding line\n", 200)
	if err := os.WriteFile(filepath.Join(root, "big.txt"), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}

	unlimited := grepFilterMatches(t, root, GrepOptions{Pattern: "needle"})
	assertFileList(t, unlimited, []string{"big.txt", "small.txt"})

	limited := grepFilterMatches(t, root, GrepOptions{Pattern: "needle", MaxFileSizeBytes: 1024})
	assertFileList(t, limited, []string{"small.txt"})
}

func TestGrepDepthAndSizeErrorsAreReported(t *testing.T) {
	if _, err := ParseGrepSize("nonsense"); err == nil {
		t.Fatal("invalid size must fail")
	}
	// The handler-level rejection path is covered in the remote conformance
	// tests; here we only pin that the walker accepts the parsed value.
	if _, err := Grep(context.Background(), GrepOptions{
		Pattern: "needle", Path: t.TempDir(), MaxFileSizeBytes: 1,
	}); err != nil {
		t.Fatalf("walker must accept a parsed size: %v", err)
	}
}
