package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGrepTool_BuiltinWalkerHandlesLinesBeyondReaderCaps pins the shared
// bounded line reader in every builtin grep mode. The first long line exceeds
// bufio.Scanner's default token limit; the second exceeds viewcore's reader
// cap. Neither may hide or renumber the following matching lines.
func TestGrepTool_BuiltinWalkerHandlesLinesBeyondReaderCaps(t *testing.T) {
	tmpDir := t.TempDir()
	longFile := filepath.Join(tmpDir, "long.txt")
	content := strings.Repeat("x", 100*1024) + "\n" +
		"needle-after-scanner-limit\n" +
		strings.Repeat("y", 300*1024) + "\n" +
		"needle-after-reader-cap\n"
	if err := os.WriteFile(longFile, []byte(content), 0o644); err != nil {
		t.Fatalf("write long file: %v", err)
	}

	cases := []struct {
		name       string
		params     map[string]interface{}
		wantParts  []string
		wantAbsent []string
	}{
		{
			name: "content",
			params: map[string]interface{}{
				"pattern": "needle-after",
				"path":    longFile,
			},
			wantParts: []string{
				"long.txt:2: needle-after-scanner-limit",
				"long.txt:4: needle-after-reader-cap",
			},
		},
		{
			name: "files",
			params: map[string]interface{}{
				"pattern": "needle-after",
				"path":    longFile,
				"mode":    "files",
			},
			wantParts: []string{"long.txt"},
		},
		{
			name: "count",
			params: map[string]interface{}{
				"pattern": "needle-after",
				"path":    longFile,
				"mode":    "count",
			},
			wantParts: []string{"long.txt:2"},
		},
		{
			name: "files without match",
			params: map[string]interface{}{
				"pattern":             "needle-after",
				"path":                longFile,
				"files_without_match": true,
			},
			wantAbsent: []string{"long.txt"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tool := NewGrepTool()
			tool.lookPath = func(string) (string, error) {
				return "", os.ErrNotExist
			}

			result, err := tool.Execute(context.Background(), tc.params)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !result.Success {
				t.Fatalf("expected success, got %v", result.Error)
			}
			if result.Metadata["engine"] != "builtin" {
				t.Fatalf("expected builtin engine, got %#v", result.Metadata["engine"])
			}
			for _, want := range tc.wantParts {
				if !strings.Contains(result.Content, want) {
					t.Fatalf("expected %q in content, got %q", want, result.Content)
				}
			}
			for _, absent := range tc.wantAbsent {
				if strings.Contains(result.Content, absent) {
					t.Fatalf("expected %q to be absent from content, got %q", absent, result.Content)
				}
			}
		})
	}
}
