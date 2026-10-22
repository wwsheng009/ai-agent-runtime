package searchcore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const benchFileBytes = 1024

func benchmarkTree(b *testing.B, files int) string {
	b.Helper()
	root := b.TempDir()
	content := []byte(strings.Repeat("x", benchFileBytes-1) + "\n")
	for i := 0; i < 100; i++ {
		if err := os.MkdirAll(filepath.Join(root, fmt.Sprintf("d%03d", i)), 0o755); err != nil {
			b.Fatal(err)
		}
	}
	for i := 0; i < files; i++ {
		dir := filepath.Join(root, fmt.Sprintf("d%03d", i%100))
		path := filepath.Join(dir, fmt.Sprintf("f%05d.txt", i))
		if err := os.WriteFile(path, content, 0o644); err != nil {
			b.Fatal(err)
		}
	}
	// Warm the OS file cache and any on-access scanner outside the measured
	// window. Otherwise first-touch open latency dominates on Windows.
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		_ = data
		return nil
	}); err != nil {
		b.Fatal(err)
	}
	return root
}

func BenchmarkGlob10kNoMatch(b *testing.B) {
	root := benchmarkTree(b, 10_000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result, err := Glob(context.Background(), root, "**/*.go", 500)
		if err != nil {
			b.Fatal(err)
		}
		if len(result.Matches) != 0 {
			b.Fatalf("unexpected matches: %d", len(result.Matches))
		}
	}
}

func BenchmarkGrep10kNoMatch(b *testing.B) {
	root := benchmarkTree(b, 10_000)
	b.ReportAllocs()
	b.SetBytes(10_000 * benchFileBytes)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result, err := Grep(context.Background(), GrepOptions{
			Pattern:      "no-such-needle",
			Path:         root,
			Glob:         "*.txt",
			Limit:        200,
			MaxFileBytes: 1 << 20,
		})
		if err != nil {
			b.Fatal(err)
		}
		if len(result.Matches) != 0 {
			b.Fatalf("unexpected matches: %d", len(result.Matches))
		}
	}
}
