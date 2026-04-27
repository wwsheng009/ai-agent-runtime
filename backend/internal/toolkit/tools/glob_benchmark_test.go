package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func BenchmarkGlobTool(b *testing.B) {
	b.StopTimer()
	root := setupGlobBenchmarkTree(b)
	b.StartTimer()

	benchmarks := []struct {
		name   string
		params map[string]interface{}
	}{
		{
			name: "ExactMatch",
			params: map[string]interface{}{
				"pattern": "top-0001.go",
				"path":    root,
			},
		},
		{
			name: "TopLevelGlob",
			params: map[string]interface{}{
				"pattern": "*.go",
				"path":    root,
				"limit":   1000,
			},
		},
		{
			name: "RecursiveGlob",
			params: map[string]interface{}{
				"pattern": "**/*.go",
				"path":    root,
				"limit":   1000,
			},
		},
		{
			name: "RecursiveLimitHit",
			params: map[string]interface{}{
				"pattern": "**/*.go",
				"path":    root,
				"limit":   25,
			},
		},
	}

	for _, bm := range benchmarks {
		b.Run(bm.name, func(b *testing.B) {
			b.ReportAllocs()
			tool := NewGlobTool()
			ctx := context.Background()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				result, err := tool.Execute(ctx, bm.params)
				if err != nil {
					b.Fatalf("unexpected error: %v", err)
				}
				if !result.Success {
					b.Fatalf("unexpected failure: %v", result.Error)
				}
			}
		})
	}
}

func setupGlobBenchmarkTree(b *testing.B) string {
	b.Helper()

	root := b.TempDir()

	for i := 0; i < 200; i++ {
		ext := ".txt"
		if i%2 == 0 {
			ext = ".go"
		}
		name := filepath.Join(root, fmt.Sprintf("top-%04d%s", i, ext))
		if err := os.WriteFile(name, []byte("benchmark"), 0644); err != nil {
			b.Fatal(err)
		}
	}

	for dir := 0; dir < 8; dir++ {
		for sub := 0; sub < 4; sub++ {
			nested := filepath.Join(root, fmt.Sprintf("dir-%02d", dir), fmt.Sprintf("sub-%02d", sub))
			if err := os.MkdirAll(nested, 0755); err != nil {
				b.Fatal(err)
			}
			for file := 0; file < 25; file++ {
				ext := ".txt"
				if file%2 == 0 {
					ext = ".go"
				}
				name := filepath.Join(nested, fmt.Sprintf("file-%02d-%02d-%02d%s", dir, sub, file, ext))
				if err := os.WriteFile(name, []byte("benchmark"), 0644); err != nil {
					b.Fatal(err)
				}
			}
		}
	}

	return root
}
