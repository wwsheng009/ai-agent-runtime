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

func TestAgentDepthRoundTripAndNormalization(t *testing.T) {
	ctx := WithAgentDepth(context.Background(), 2)
	if got := AgentDepth(ctx); got != 2 {
		t.Fatalf("expected agent depth 2, got %d", got)
	}
	if got := AgentDepth(WithAgentDepth(ctx, -1)); got != 0 {
		t.Fatalf("expected negative agent depth to normalize to zero, got %d", got)
	}
	if got := AgentDepth(nil); got != 0 {
		t.Fatalf("expected nil context to report root depth, got %d", got)
	}
}
