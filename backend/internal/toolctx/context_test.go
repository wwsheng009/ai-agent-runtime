package toolctx

import (
	"context"
	"path/filepath"
	"testing"
)

func TestShellOutputArtifactDirRoundTrip(t *testing.T) {
	want := filepath.Join(t.TempDir(), "local-shell")
	ctx := WithShellOutputArtifactDir(context.Background(), want)
	if got := ShellOutputArtifactDir(ctx); got != want {
		t.Fatalf("expected shell artifact dir %q, got %q", want, got)
	}
}
