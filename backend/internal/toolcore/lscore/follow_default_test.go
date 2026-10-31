package lscore

// follow_default_test.go pins ls's half of the shared default: with no follow argument, the
// answer comes from the TARGET's platform, not this process's. A Windows client driving a
// POSIX backend must not inherit the host's junction-following habit and descend directory
// links the local tool would refuse.

import (
	"context"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// linkTreeAccess resolves links the plain tree fake cannot: ReadDir still reports the link
// entry (so a non-following walk stops at it), Stat reports the TARGET's shape, and
// EvalLinkPath answers the target's identity.
type linkTreeAccess struct {
	*treeAccess
	links map[string]string
}

func (a *linkTreeAccess) Stat(ctx context.Context, p string) (fs.FileInfo, error) {
	n := filepath.ToSlash(p)
	if target, ok := a.links[n]; ok {
		return a.treeAccess.Stat(ctx, target)
	}
	return a.treeAccess.Stat(ctx, n)
}

func (a *linkTreeAccess) ReadDir(ctx context.Context, p string) ([]fs.DirEntry, error) {
	n := filepath.ToSlash(p)
	if target, ok := a.links[n]; ok {
		return a.treeAccess.ReadDir(ctx, target)
	}
	return a.treeAccess.ReadDir(ctx, n)
}

func (a *linkTreeAccess) EvalLinkPath(_ context.Context, p string) (string, error) {
	n := filepath.ToSlash(p)
	if target, ok := a.links[n]; ok {
		return target, nil
	}
	return n, nil
}

type posixTreeAccess struct{ *linkTreeAccess }

func (posixTreeAccess) PathSeparator() rune { return '/' }

type windowsTreeAccess struct{ *linkTreeAccess }

func (windowsTreeAccess) PathSeparator() rune { return '\\' }

func TestLsFollowDefaultComesFromTheTargetNotTheHost(t *testing.T) {
	const root, out = "/ls/default", "/ls/out"
	base := &linkTreeAccess{
		treeAccess: &treeAccess{
			roots: map[string]treeInfo{root: {name: "default", dir: true}, out: {name: "out", dir: true}},
			nodes: map[string][]fs.DirEntry{
				root: {treeEntry{treeInfo{name: "inner.txt"}}, treeEntry{treeInfo{name: "lnk", link: true}}},
				out:  {treeEntry{treeInfo{name: "g.txt"}}},
			},
		},
		links: map[string]string{root + "/lnk": out},
	}

	posix := runLs(t, root, posixTreeAccess{base}, map[string]any{"path": ".", "depth": 3})
	if strings.Contains(posix.Content, "g.txt") {
		t.Fatalf("a POSIX target followed a directory link by default: %s", posix.Content)
	}

	windows := runLs(t, root, windowsTreeAccess{base}, map[string]any{"path": ".", "depth": 3})
	if !strings.Contains(windows.Content, "g.txt") {
		t.Fatalf("a Windows-style target must keep following junctions by default: %s", windows.Content)
	}
}
