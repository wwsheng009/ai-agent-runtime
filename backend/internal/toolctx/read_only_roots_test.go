package toolctx

import (
	"context"
	"reflect"
	"testing"
)

func TestReadOnlyRootsRoundTrip(t *testing.T) {
	base := context.Background()
	if got := ReadOnlyRoots(base); got != nil {
		t.Fatalf("expected nil roots without a set, got %#v", got)
	}

	ctx := WithReadOnlyRoots(base, []string{" /repo ", "", "/repo", "/repo2"})
	want := []string{"/repo", "/repo2"}
	if got := ReadOnlyRoots(ctx); !reflect.DeepEqual(got, want) {
		t.Fatalf("ReadOnlyRoots = %#v, want %#v", got, want)
	}

	// Callers must not be able to mutate the stored set through the result.
	mutated := ReadOnlyRoots(ctx)
	mutated[0] = "/mutated"
	if got := ReadOnlyRoots(ctx); got[0] != "/repo" {
		t.Fatalf("stored roots were mutated through the returned slice: %#v", got)
	}

	// An empty set leaves the context unchanged.
	if empty := WithReadOnlyRoots(base, nil); empty != base {
		t.Fatal("expected WithReadOnlyRoots(nil) to return the original context")
	}
}

// 只读根与准入根是两套独立集合：登记只读根不得顺带放行读写路径。
func TestReadOnlyRootsAreIndependentFromAllowedRoots(t *testing.T) {
	ctx := WithWorkspaceRoot(context.Background(), "/workspace")
	ctx = WithReadOnlyRoots(ctx, []string{"/repo"})

	if got := AllowedRoots(ctx); got != nil {
		t.Fatalf("AllowedRoots = %#v, want nil", got)
	}
	if got := ReadOnlyRoots(ctx); !reflect.DeepEqual(got, []string{"/repo"}) {
		t.Fatalf("ReadOnlyRoots = %#v", got)
	}
	if got := WorkspaceRoot(ctx); got != "/workspace" {
		t.Fatalf("WorkspaceRoot = %q", got)
	}
}
