package searchcore

// follow_default_test.go pins where the "no follow argument" default comes from: the TARGET
// platform, not this process's. Before this was fixed, the shared handler read
// runtime.GOOS - so a Windows client driving the envd backend descended directory links by
// default, exactly the divergence the F2 work surfaced on a real target. The POSIX half of
// this test is red on a Windows host if the default ever goes back to the process OS; the
// Windows-styled half is red on a POSIX host for the opposite reason.

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
)

// backslashAccess is a Windows-style target: the same described tree, spelled the way a
// junction-following host spells it. posixAccess (seam_walk_test.go) is its POSIX twin.
type backslashAccess struct{ *linkFixtureAccess }

func (backslashAccess) PathSeparator() rune { return '\\' }

type posixFixtureAccess struct{ *linkFixtureAccess }

func (posixFixtureAccess) PathSeparator() rune { return '/' }

// linkFixtureAccess is a spelling-tolerant described tree: every path is keyed by its
// slash-normalised form and resolved through the links map (whole path and prefix), so the
// same fixture serves a POSIX-style and a Windows-style accessor without either
// convention leaking into the other.
type linkFixtureAccess struct {
	files map[string][]byte
	dirs  map[string]bool
	links map[string]string
}

func (a *linkFixtureAccess) norm(p string) string { return path.Clean(filepath.ToSlash(p)) }

func (a *linkFixtureAccess) resolve(p string) string {
	p = a.norm(p)
	for step := 0; step < 16; step++ {
		changed := false
		for link, target := range a.links {
			switch {
			case p == link:
				p, changed = target, true
			case strings.HasPrefix(p, link+"/"):
				p, changed = target+strings.TrimPrefix(p, link), true
			}
			if changed {
				break
			}
		}
		if !changed {
			return p
		}
	}
	return p
}

func (a *linkFixtureAccess) Stat(_ context.Context, p string) (fs.FileInfo, error) {
	r := a.resolve(p)
	if body, ok := a.files[r]; ok {
		return fixtureInfo{name: path.Base(r), size: int64(len(body))}, nil
	}
	if a.dirs[r] {
		return fixtureInfo{name: path.Base(r), dir: true}, nil
	}
	return nil, fs.ErrNotExist
}

func (a *linkFixtureAccess) Lstat(ctx context.Context, p string) (fs.FileInfo, error) {
	if n := a.norm(p); a.isLink(n) {
		return fixtureInfo{name: path.Base(n), link: true}, nil
	}
	return a.Stat(ctx, p)
}

func (a *linkFixtureAccess) isLink(p string) bool {
	_, ok := a.links[p]
	return ok
}

func (a *linkFixtureAccess) Open(_ context.Context, p string) (toolcore.FileHandle, error) {
	body, ok := a.files[a.resolve(p)]
	if !ok {
		return nil, errors.New("not a file readable through the seam")
	}
	return &fixtureHandle{Reader: bytes.NewReader(body)}, nil
}

func (a *linkFixtureAccess) ReadDir(_ context.Context, dir string) ([]fs.DirEntry, error) {
	d := a.resolve(dir)
	byName := map[string]fs.DirEntry{}
	for p, body := range a.files {
		if path.Dir(p) == d {
			byName[path.Base(p)] = fs.FileInfoToDirEntry(fixtureInfo{name: path.Base(p), size: int64(len(body))})
		}
	}
	for p := range a.dirs {
		if p != d && path.Dir(p) == d {
			byName[path.Base(p)] = fs.FileInfoToDirEntry(fixtureInfo{name: path.Base(p), dir: true})
		}
	}
	for p := range a.links {
		if path.Dir(p) == d {
			byName[path.Base(p)] = fs.FileInfoToDirEntry(fixtureInfo{name: path.Base(p), link: true})
		}
	}
	entries := make([]fs.DirEntry, 0, len(byName))
	for _, entry := range byName {
		entries = append(entries, entry)
	}
	return entries, nil
}

func (a *linkFixtureAccess) EvalLinkPath(_ context.Context, p string) (string, error) {
	n := a.norm(p)
	if !a.isLink(n) {
		return n, nil
	}
	r := a.resolve(n)
	if _, ok := a.files[r]; !ok && !a.dirs[r] {
		return "", fs.ErrNotExist
	}
	return r, nil
}

type fixtureInfo struct {
	name string
	size int64
	dir  bool
	link bool
}

func (i fixtureInfo) Name() string { return i.name }
func (i fixtureInfo) Size() int64  { return i.size }
func (i fixtureInfo) Mode() fs.FileMode {
	switch {
	case i.link:
		return fs.ModeSymlink | 0o777
	case i.dir:
		return fs.ModeDir | 0o755
	default:
		return 0o644
	}
}
func (i fixtureInfo) ModTime() time.Time { return time.Time{} }
func (i fixtureInfo) IsDir() bool        { return i.dir && !i.link }
func (i fixtureInfo) Sys() any           { return nil }

type fixtureHandle struct{ *bytes.Reader }

func (fixtureHandle) Close() error                 { return nil }
func (h fixtureHandle) Stat() (fs.FileInfo, error) { return nil, errors.New("unused") }

func runGrepOnAccess(t *testing.T, root string, access toolcore.Access, args map[string]any) *toolcore.Result {
	t.Helper()
	handler := NewGrepHandler(toolcore.Definition{Name: "grep", Version: "1.0.0"})
	runtime := seamRuntime{
		presentationRuntime: presentationRuntime{paths: presentationPaths{root: root}},
		access:              access,
	}
	result, err := handler.Execute(context.Background(), &toolcore.Call{Args: args, Runtime: runtime})
	if err != nil {
		t.Fatalf("grep returned a Go error, want a result: %v", err)
	}
	if result == nil {
		t.Fatal("grep returned a nil result")
	}
	return result
}

func TestGrepFollowDefaultComesFromTheTargetNotTheHost(t *testing.T) {
	const root = "/srv/follow"
	base := &linkFixtureAccess{
		files: map[string][]byte{
			root + "/inner.txt":     []byte("needle inner\n"),
			"/srv/follow-out/g.txt": []byte("needle out\n"),
		},
		dirs:  map[string]bool{root: true, "/srv/follow-out": true},
		links: map[string]string{root + "/lnk": "/srv/follow-out"},
	}

	posix := runGrepOnAccess(t, root, posixFixtureAccess{base}, map[string]any{"pattern": "needle", "path": "."})
	if body := posix.Content; !strings.Contains(body, "inner.txt") {
		t.Fatalf("the in-root file must still be searched: %s", body)
	} else if strings.Contains(body, "g.txt") {
		t.Fatalf("a POSIX target followed a directory link by default: %s (meta=%v)", posix.Metadata, posix.Metadata)
	}
	if posix.Metadata["follow"] != false {
		t.Fatalf("metadata.follow = %#v, want false for a POSIX target", posix.Metadata["follow"])
	}

	windows := runGrepOnAccess(t, root, backslashAccess{base}, map[string]any{"pattern": "needle", "path": "."})
	if !strings.Contains(windows.Content, "g.txt") {
		t.Fatalf("a Windows-style target must keep following junctions by default: %s", windows.Content)
	}
	if windows.Metadata["follow"] != true {
		t.Fatalf("metadata.follow = %#v, want true for a Windows-style target", windows.Metadata["follow"])
	}

	explicit := runGrepOnAccess(t, root, posixFixtureAccess{base}, map[string]any{"pattern": "needle", "path": ".", "follow": true})
	if !strings.Contains(explicit.Content, "g.txt") {
		t.Fatalf("explicit follow=true must still win on a POSIX target: %s", explicit.Content)
	}
}
