package searchcore

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func symlinkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
}

func TestGlobFollowsNestedDirectoryLink(t *testing.T) {
	base := t.TempDir()
	outside := filepath.Join(base, "outside")
	root := filepath.Join(base, "root")
	for _, dir := range []string{outside, root} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(outside, "one.go"), []byte("package one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	symlinkOrSkip(t, outside, filepath.Join(root, "linked"))

	plain, err := GlobWithOptions(context.Background(), GlobOptions{
		Base: root, Pattern: "**/*.go", Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plain.Matches) != 0 {
		t.Fatalf("follow=false reached the linked tree: %v", plain.Matches)
	}

	followed, err := GlobWithOptions(context.Background(), GlobOptions{
		Base: root, Pattern: "**/*.go", Limit: 10, Follow: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(followed.Matches) != 1 || filepath.Base(followed.Matches[0]) != "one.go" {
		t.Fatalf("followed glob matches = %v, want linked/one.go", followed.Matches)
	}
	if got := filepath.ToSlash(followed.Matches[0]); !strings.HasSuffix(got, "linked/one.go") {
		t.Fatalf("followed path = %q, want the link's own path spelling", got)
	}
}

func TestGlobFollowsSymlinkedBase(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "one.go"), []byte("package one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "linked-root")
	symlinkOrSkip(t, target, link)

	result, err := GlobWithOptions(context.Background(), GlobOptions{
		Base: link, Pattern: "**/*.go", Limit: 10, Follow: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Matches) != 1 || filepath.Base(result.Matches[0]) != "one.go" {
		t.Fatalf("linked-base glob matches = %v, want one.go", result.Matches)
	}
}

// The explicit base is the path the caller named, so it is resolved even when
// follow=false. follow only governs links discovered below it; the walker must
// agree with ripgrep, which follows an explicit path argument.
func TestGlobReadsSymlinkedBaseWithoutFollow(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "one.go"), []byte("package one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "linked-root")
	symlinkOrSkip(t, target, link)

	result, err := GlobWithOptions(context.Background(), GlobOptions{
		Base: link, Pattern: "**/*.go", Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Matches) != 1 || filepath.Base(result.Matches[0]) != "one.go" {
		t.Fatalf("follow=false linked-base glob matches = %v, want one.go", result.Matches)
	}
	if got := filepath.ToSlash(result.Matches[0]); !strings.HasPrefix(got, filepath.ToSlash(link)) {
		t.Fatalf("match path = %q, want it spelled under the named link root", got)
	}
}

func TestGlobFollowTerminatesOnLinkCycle(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "top.go"), []byte("package top\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	symlinkOrSkip(t, root, filepath.Join(root, "loop"))

	result, err := GlobWithOptions(context.Background(), GlobOptions{
		Base: root, Pattern: "**/*.go", Limit: 10, Follow: true,
	})
	if err != nil {
		t.Fatalf("cycle glob: %v", err)
	}
	if len(result.Matches) != 1 || filepath.Base(result.Matches[0]) != "top.go" {
		t.Fatalf("cycle glob matches = %v, want only top.go", result.Matches)
	}
}

func TestRipgrepGlobAsksForFollow(t *testing.T) {
	root := t.TempDir()
	var args []string
	engine := Ripgrep{
		Path: "rg",
		Stream: func(_ context.Context, got []string, _ string, onLine func(line []byte) bool) error {
			args = append([]string(nil), got...)
			onLine([]byte("one.go"))
			return nil
		},
	}
	if _, used, err := engine.Glob(context.Background(), GlobOptions{
		Base: root, Pattern: "*.go", Limit: 10, Follow: true,
	}); err != nil || !used {
		t.Fatalf("ripgrep glob: used=%v err=%v", used, err)
	}
	if !slices.Contains(args, "--follow") {
		t.Fatalf("glob did not pass --follow to ripgrep: %v", args)
	}
}
