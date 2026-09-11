package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
)

func TestResolveWorkdirWithBasePrefersSessionRoot(t *testing.T) {
	base := t.TempDir()
	ctx := toolctx.WithWorkspaceRoot(context.Background(), base)

	got, err := resolveWorkdirWithBase(ctx, "")
	if err != nil {
		t.Fatalf("empty workdir: %v", err)
	}
	if want := filepath.Clean(base); got != want {
		t.Fatalf("expected default workdir %q, got %q", want, got)
	}

	got, err = resolveWorkdirWithBase(ctx, filepath.Join("sub", "dir"))
	if err != nil {
		t.Fatalf("relative workdir: %v", err)
	}
	if want := filepath.Clean(filepath.Join(base, "sub", "dir")); got != want {
		t.Fatalf("expected relative workdir joined to %q, got %q", want, got)
	}

	abs := filepath.Join(base, "absolute")
	got, err = resolveWorkdirWithBase(ctx, abs)
	if err != nil {
		t.Fatalf("absolute workdir: %v", err)
	}
	if got != filepath.Clean(abs) {
		t.Fatalf("expected absolute workdir %q, got %q", filepath.Clean(abs), got)
	}
}

func TestResolveWorkdirWithBaseFallsBackToProcessCWD(t *testing.T) {
	got, err := resolveWorkdirWithBase(context.Background(), "")
	if err != nil {
		t.Fatalf("no-base empty workdir: %v", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("process cwd: %v", err)
	}
	if filepath.Clean(got) != filepath.Clean(cwd) {
		t.Fatalf("expected process cwd fallback %q, got %q", cwd, got)
	}

	got, err = resolveWorkdirWithBase(nil, filepath.Join("rel", "leaf"))
	if err != nil {
		t.Fatalf("no-base relative workdir: %v", err)
	}
	if want := filepath.Clean(filepath.Join(cwd, "rel", "leaf")); got != want {
		t.Fatalf("expected relative workdir joined to cwd %q, got %q", want, got)
	}
}
