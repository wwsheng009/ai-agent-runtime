package toolctx

import (
	"context"
	"reflect"
	"testing"
)

func TestAllowedRootsRoundTrip(t *testing.T) {
	base := context.Background()
	if got := AllowedRoots(base); got != nil {
		t.Fatalf("expected nil roots without a set, got %#v", got)
	}

	ctx := WithAllowedRoots(base, []string{" /a ", "", "/a", "/b"})
	want := []string{"/a", "/b"}
	if got := AllowedRoots(ctx); !reflect.DeepEqual(got, want) {
		t.Fatalf("AllowedRoots = %#v, want %#v", got, want)
	}

	// Callers must not be able to mutate the stored set through the result.
	mutated := AllowedRoots(ctx)
	mutated[0] = "/mutated"
	if got := AllowedRoots(ctx); got[0] != "/a" {
		t.Fatalf("stored roots were mutated through the returned slice: %#v", got)
	}

	// An empty set leaves the context unchanged.
	if empty := WithAllowedRoots(base, nil); empty != base {
		t.Fatal("expected WithAllowedRoots(nil) to return the original context")
	}
}

func TestAllowedRootsCoexistWithWorkspaceRoot(t *testing.T) {
	ctx := WithWorkspaceRoot(context.Background(), "/workspace")
	ctx = WithAllowedRoots(ctx, []string{"/external"})
	if got := WorkspaceRoot(ctx); got != "/workspace" {
		t.Fatalf("WorkspaceRoot = %q", got)
	}
	if got := AllowedRoots(ctx); !reflect.DeepEqual(got, []string{"/external"}) {
		t.Fatalf("AllowedRoots = %#v", got)
	}
}
