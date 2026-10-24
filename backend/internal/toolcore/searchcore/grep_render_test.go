package searchcore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeRenderPaths renders display paths like the workspace resolver: relative
// slash paths inside the root, absolute outside.
type fakeRenderPaths struct{ root string }

func (p fakeRenderPaths) Root() string { return p.root }
func (p fakeRenderPaths) Resolve(raw string) (string, error) {
	if raw == "" {
		return p.root, nil
	}
	if filepath.IsAbs(raw) {
		return filepath.Clean(raw), nil
	}
	return filepath.Join(p.root, raw), nil
}
func (p fakeRenderPaths) Relative(abs string) string {
	rel, err := filepath.Rel(p.root, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return abs
	}
	return filepath.ToSlash(rel)
}

func renderFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("alpha needle\nbeta\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "nested", "b.txt"), []byte("needle nested\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// TestGrepRenderContentShapes pins the page renderer's content shapes: the
// local tool's `path:line: text` (with the space), fragments carrying the
// column, and the single-file root showing only the file name.
func TestGrepRenderContentShapes(t *testing.T) {
	root := renderFixture(t)
	paths := fakeRenderPaths{root: root}

	dir, err := Grep(context.Background(), GrepOptions{Pattern: "needle", Path: root, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	page := renderGrepPage(grepRenderInputs{paths: paths, result: &dir, options: GrepOptions{Limit: 10}, mode: GrepModeContent, roots: []string{root}})
	lines := strings.Split(page.content, "\n")
	if lines[0] != "a.txt:1: alpha needle" {
		t.Fatalf("content line shape = %q, want %q", lines[0], "a.txt:1: alpha needle")
	}
	if lines[1] != "nested/b.txt:1: needle nested" {
		t.Fatalf("nested line = %q", lines[1])
	}

	single, err := Grep(context.Background(), GrepOptions{Pattern: "needle", Path: filepath.Join(root, "a.txt")})
	if err != nil {
		t.Fatal(err)
	}
	page = renderGrepPage(grepRenderInputs{paths: paths, result: &single, options: GrepOptions{}, mode: GrepModeContent, roots: []string{filepath.Join(root, "a.txt")}})
	if page.content != "a.txt:1: alpha needle" {
		t.Fatalf("single-file display = %q, want the bare file name", page.content)
	}
}

func TestGrepRenderFragmentsAndContext(t *testing.T) {
	root := renderFixture(t)
	paths := fakeRenderPaths{root: root}

	frag, err := Grep(context.Background(), GrepOptions{Pattern: "needle", Path: root, OnlyMatching: true, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	page := renderGrepPage(grepRenderInputs{paths: paths, result: &frag, options: GrepOptions{Limit: 10}, mode: GrepModeContent, roots: []string{root}})
	if !strings.Contains(page.content, "a.txt:1:7: needle") {
		t.Fatalf("fragment shape with column:\n%s", page.content)
	}

	ctx2, err := Grep(context.Background(), GrepOptions{Pattern: "needle", Path: root, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	page = renderGrepPage(grepRenderInputs{paths: paths, result: &ctx2, options: GrepOptions{Limit: 10}, mode: GrepModeContent, roots: []string{root}, contextAfter: 1})
	if !strings.Contains(page.content, "a.txt>1: alpha needle") {
		t.Fatalf("context match marker:\n%s", page.content)
	}
	// after=1 展示第 2 行作为相邻上下文行（空格前缀、无 > 标记）。
	if !strings.Contains(page.content, "a.txt 2: beta") {
		t.Fatalf("context neighbour shape:\n%s", page.content)
	}
}

// TestGrepRenderEmptyAndPaging pins the empty-page wording and the paging
// notice: 未找到匹配的内容 and the Chinese page hint with next_offset (the
// word 截断 must never appear - models quote it back).
func TestGrepRenderEmptyAndPaging(t *testing.T) {
	root := renderFixture(t)
	paths := fakeRenderPaths{root: root}

	empty, err := Grep(context.Background(), GrepOptions{Pattern: "nothing-matches-this", Path: root})
	if err != nil {
		t.Fatal(err)
	}
	page := renderGrepPage(grepRenderInputs{paths: paths, result: &empty, options: GrepOptions{}, mode: GrepModeContent, roots: []string{root}})
	if page.content != "未找到匹配的内容" {
		t.Fatalf("empty page wording = %q", page.content)
	}

	paged, err := Grep(context.Background(), GrepOptions{Pattern: "needle", Path: root, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	page = renderGrepPage(grepRenderInputs{paths: paths, result: &paged, options: GrepOptions{Limit: 1}, mode: GrepModeContent, roots: []string{root}})
	if !strings.Contains(page.content, "next_offset=1") || !strings.Contains(page.content, "已分页") {
		t.Fatalf("paging notice missing:\n%s", page.content)
	}
	if strings.Contains(page.content, "截断") {
		t.Fatalf("the paging notice must not use the word 截断:\n%s", page.content)
	}
}

func TestGrepRenderFilesAndCountShapes(t *testing.T) {
	root := renderFixture(t)
	paths := fakeRenderPaths{root: root}

	files, err := Grep(context.Background(), GrepOptions{Pattern: "needle", Path: root, Mode: GrepModeFiles, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	page := renderGrepPage(grepRenderInputs{paths: paths, result: &files, options: GrepOptions{Limit: 10}, mode: GrepModeFiles, roots: []string{root}})
	if page.content != "a.txt\nnested/b.txt" {
		t.Fatalf("files shape = %q", page.content)
	}

	counts, err := Grep(context.Background(), GrepOptions{Pattern: "needle", Path: root, Mode: GrepModeCount, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	page = renderGrepPage(grepRenderInputs{paths: paths, result: &counts, options: GrepOptions{Limit: 10}, mode: GrepModeCount, roots: []string{root}})
	if page.content != "a.txt:1\nnested/b.txt:1" {
		t.Fatalf("count shape = %q", page.content)
	}
}
