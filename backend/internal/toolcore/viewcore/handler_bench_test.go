package viewcore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
)

type viewBenchPaths struct {
	root string
}

func (p viewBenchPaths) Root() string { return p.root }

func (p viewBenchPaths) Resolve(raw string) (string, error) {
	if raw == "" {
		return p.root, nil
	}
	if filepath.IsAbs(raw) {
		return raw, nil
	}
	return filepath.Join(p.root, raw), nil
}

func (p viewBenchPaths) Relative(abs string) string {
	rel, err := filepath.Rel(p.root, abs)
	if err != nil {
		return abs
	}
	return filepath.ToSlash(rel)
}

type viewBenchRuntime struct {
	paths toolcore.PathResolver
}

func (r *viewBenchRuntime) Paths() toolcore.PathResolver            { return r.paths }
func (r *viewBenchRuntime) Permissions() toolcore.PermissionChecker { return nil }
func (r *viewBenchRuntime) Commands() toolcore.CommandPolicy        { return nil }
func (r *viewBenchRuntime) Env() toolcore.EnvProvider               { return nil }
func (r *viewBenchRuntime) Output() toolcore.OutputShaper           { return nil }
func (r *viewBenchRuntime) Artifacts() toolcore.ArtifactSink        { return nil }
func (r *viewBenchRuntime) Sessions() toolcore.SessionState         { return nil }
func (r *viewBenchRuntime) Clock() toolcore.Clock                   { return nil }
func (r *viewBenchRuntime) IDs() toolcore.IDGenerator               { return nil }

func setupViewBenchFixture(b *testing.B) (string, string, int) {
	b.Helper()
	root := b.TempDir()
	var builder strings.Builder
	payload := strings.Repeat("x", 1024)
	for i := 0; i < 20_000; i++ {
		fmt.Fprintf(&builder, "line %04d: %s\n", i, payload)
	}
	content := builder.String()
	path := filepath.Join(root, "large.txt")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		b.Fatalf("write fixture: %v", err)
	}
	return root, path, len(content)
}

// BenchmarkViewCoreDirect is the baseline: the shared core functions called
// with an already-resolved path.
func BenchmarkViewCoreDirect(b *testing.B) {
	_, path, size := setupViewBenchFixture(b)
	b.ReportAllocs()
	b.SetBytes(int64(size))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		offset, limit := Normalize(0, MaxLimit)
		window, err := Read(path, offset, limit, 0)
		if err != nil {
			b.Fatal(err)
		}
		if body := Render("large.txt", window); body == "" {
			b.Fatal("empty render")
		}
		_ = Metadata("large.txt", window)
	}
}

// BenchmarkViewHandlerRuntime measures the same fixture through the
// surface-independent Handler + injected Runtime (path resolution, permission
// check, special-file guard, arg parsing included).
func BenchmarkViewHandlerRuntime(b *testing.B) {
	root, path, size := setupViewBenchFixture(b)
	handler := NewHandler(toolcore.Definition{Name: "view", Version: "1.0.0"})
	runtime := &viewBenchRuntime{paths: viewBenchPaths{root: root}}
	ctx := context.Background()
	b.ReportAllocs()
	b.SetBytes(int64(size))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result, err := handler.Execute(ctx, &toolcore.Call{
			Args:    map[string]any{"file_path": path, "limit": MaxLimit},
			Runtime: runtime,
		})
		if err != nil || result == nil || !result.Success {
			b.Fatalf("handler failed: %v (%+v)", err, result)
		}
	}
}
