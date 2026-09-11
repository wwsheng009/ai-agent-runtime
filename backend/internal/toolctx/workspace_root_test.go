package toolctx

import (
	"context"
	"testing"
)

func TestWorkspaceRootRoundTrip(t *testing.T) {
	ctx := WithWorkspaceRoot(context.Background(), "  E:\\bound\\project  ")
	if got := WorkspaceRoot(ctx); got != `E:\bound\project` {
		t.Fatalf("expected trimmed workspace root, got %q", got)
	}
	if got := WorkspaceRoot(context.Background()); got != "" {
		t.Fatalf("expected empty workspace root without binding, got %q", got)
	}
	if got := WorkspaceRoot(nil); got != "" {
		t.Fatalf("expected nil ctx to be safe, got %q", got)
	}
}
