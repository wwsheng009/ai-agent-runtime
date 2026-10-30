package remotetools

// envd_core_integration_test.go is the offline gate in front of wiring the envd accessor
// into the remote runtime's FileAccess.
//
// The contract tests in envd_access_test.go prove each Access method behaves on its own.
// They do not prove that a shared core can *run* on the adapter: a core asks for a root,
// enumerates it, filters, pages, and renders, and any one of those steps can reach for
// something the adapter answers differently than the core expects. So this file drives the
// real handlers - searchcore's grep and glob, lscore's ls - with a runtime whose only
// interesting method is FileAccess(), over a tree that exists on no machine but the fake
// envd transport.
//
// What it does NOT prove, and must not be read as proving: anything about the wire. The
// fake answers in the shapes envdclient documents, so a wrong assumption about what a live
// daemon actually sends stays invisible here - that is what scripts/envd_seam_access_verify.go
// is for. Offline and live gates answer different questions; both are required.

import (
	"context"
	"path"
	"strconv"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/envdclient"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/lscore"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/searchcore"
)

// envdCorePaths resolves target paths with POSIX rules, because the target is a Linux
// daemon: using filepath here would silently produce backslashes on a Windows test host and
// the fake tree would stop being found. That is not a test convenience - it is the same
// reason the adapter itself uses path, not filepath, for anything it sends to envd.
type envdCorePaths struct{ root string }

func (p envdCorePaths) Root() string { return p.root }

func (p envdCorePaths) Resolve(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "." {
		return p.root, nil
	}
	if strings.HasPrefix(raw, "/") {
		return path.Clean(raw), nil
	}
	return path.Join(p.root, raw), nil
}

func (p envdCorePaths) Relative(abs string) string {
	abs = path.Clean(abs)
	if abs == p.root {
		return "."
	}
	if strings.HasPrefix(abs, p.root+"/") {
		return strings.TrimPrefix(abs, p.root+"/")
	}
	return abs
}

// envdCoreRuntime is the smallest Runtime that can carry an accessor: everything the
// handlers do not need answers nil, and the one method that matters hands the cores the
// envd adapter. A nil accessor models today's wiring (nothing provides one), which is the
// control case below.
type envdCoreRuntime struct {
	paths  toolcore.PathResolver
	access toolcore.Access
}

func (r envdCoreRuntime) FileAccess() toolcore.Access             { return r.access }
func (r envdCoreRuntime) Paths() toolcore.PathResolver            { return r.paths }
func (r envdCoreRuntime) Permissions() toolcore.PermissionChecker { return nil }
func (r envdCoreRuntime) Commands() toolcore.CommandPolicy        { return nil }
func (r envdCoreRuntime) Env() toolcore.EnvProvider               { return nil }
func (r envdCoreRuntime) Output() toolcore.OutputShaper           { return nil }
func (r envdCoreRuntime) Artifacts() toolcore.ArtifactSink        { return nil }
func (r envdCoreRuntime) Sessions() toolcore.SessionState         { return nil }
func (r envdCoreRuntime) Clock() toolcore.Clock                   { return nil }
func (r envdCoreRuntime) IDs() toolcore.IDGenerator               { return nil }

// envdIntegrationTree describes a target that exists only for the fake transport.
func envdIntegrationTree() *fakeEnvdFiles {
	fake := &fakeEnvdFiles{
		entries: map[string]envdclient.Entry{
			"/srv/work":               fakeDirEntry("/srv/work"),
			"/srv/work/alpha.txt":     fakeFileEntry("/srv/work/alpha.txt", int64(len("needle alpha\n"))),
			"/srv/work/beta.log":      fakeFileEntry("/srv/work/beta.log", int64(len("nothing here\n"))),
			"/srv/work/pkg":           fakeDirEntry("/srv/work/pkg"),
			"/srv/work/pkg/gamma.txt": fakeFileEntry("/srv/work/pkg/gamma.txt", int64(len("needle gamma\n"))),
		},
		listing: map[string][]string{
			"/srv/work":     {"beta.log", "pkg", "alpha.txt"},
			"/srv/work/pkg": {"gamma.txt"},
		},
		content: map[string][]byte{
			"/srv/work/alpha.txt":     []byte("needle alpha\n"),
			"/srv/work/beta.log":      []byte("nothing here\n"),
			"/srv/work/pkg/gamma.txt": []byte("needle gamma\n"),
		},
	}
	return fake
}

func runHandler(t *testing.T, handler toolcore.Handler, args map[string]any, runtime toolcore.Runtime) *toolcore.Result {
	t.Helper()
	result, err := handler.Execute(context.Background(), &toolcore.Call{Args: args, Runtime: runtime})
	if err != nil {
		t.Fatalf("%s returned a Go error: %v", handler.Definition().Name, err)
	}
	if result == nil {
		t.Fatalf("%s returned a nil result", handler.Definition().Name)
	}
	return result
}

func TestSharedGrepRunsOnTheEnvdAccessor(t *testing.T) {
	fake := envdIntegrationTree()
	access := &envdAccess{api: fake}
	runtime := envdCoreRuntime{paths: envdCorePaths{root: "/srv/work"}, access: access}

	handler := searchcore.NewGrepHandler(toolcore.Definition{Name: "grep", Version: "1.0.0"})
	result := runHandler(t, handler, map[string]any{"pattern": "needle", "path": "."}, runtime)
	if !result.Success {
		t.Fatalf("grep against the accessor failed: %s", result.Content)
	}
	for _, want := range []string{"alpha.txt", "gamma.txt"} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("grep output is missing %s (code=%q len=%d metadata=%v statCalls=%v listCalls=%v):\n%s",
				want, result.ErrorCode, len(result.Content), result.Metadata, fake.statCalls, fake.listCalls, result.Content)
		}
	}
	if strings.Contains(result.Content, "beta.log") {
		t.Fatalf("grep matched a file with no match:\n%s", result.Content)
	}
	// The core must have asked the target, not this machine: without the read windows and
	// the stats below, the matches could only have come from somewhere else.
	if len(fake.windows) == 0 || len(fake.statCalls) == 0 {
		t.Fatalf("the core never used the accessor (windows=%v stats=%v)", fake.windows, fake.statCalls)
	}

	// Control: the same call with no accessor asks localio, this machine has no /srv/work,
	// and the result must therefore differ. Without this half, the assertions above could
	// be passing because the core ignored the accessor and found the tree anyway.
	control := runHandler(t, handler, map[string]any{"pattern": "needle", "path": "."},
		envdCoreRuntime{paths: envdCorePaths{root: "/srv/work"}, access: nil})
	if control.Success && strings.Contains(control.Content, "alpha.txt") {
		t.Fatalf("the control is wrong: a tree that exists for no one locally was searched anyway:\n%s", control.Content)
	}
}

func TestSharedGlobRunsOnTheEnvdAccessor(t *testing.T) {
	fake := envdIntegrationTree()
	access := &envdAccess{api: fake}
	runtime := envdCoreRuntime{paths: envdCorePaths{root: "/srv/work"}, access: access}

	handler := searchcore.NewGlobHandler(toolcore.Definition{Name: "glob", Version: "1.0.0"})
	result := runHandler(t, handler, map[string]any{"pattern": "**/*.txt", "path": "."}, runtime)
	if !result.Success {
		t.Fatalf("glob against the accessor failed: %s", result.Content)
	}
	for _, want := range []string{"alpha.txt", "gamma.txt"} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("glob output is missing %s:\n%s", want, result.Content)
		}
	}
	if strings.Contains(result.Content, "beta.log") {
		t.Fatalf("glob listed a file the pattern excludes:\n%s", result.Content)
	}

	// A pattern that matches only the nested file pins that the walk descended the fake's
	// own directory structure rather than a single level.
	nested := runHandler(t, handler, map[string]any{"pattern": "pkg/*.txt", "path": "."}, runtime)
	if !nested.Success || !strings.Contains(nested.Content, "gamma.txt") || strings.Contains(nested.Content, "alpha") {
		t.Fatalf("nested pattern did not walk the accessor's tree:\n%s", nested.Content)
	}
}

func TestSharedLsRunsOnTheEnvdAccessor(t *testing.T) {
	fake := envdIntegrationTree()
	access := &envdAccess{api: fake}
	runtime := envdCoreRuntime{paths: envdCorePaths{root: "/srv/work"}, access: access}

	handler := lscore.NewHandler(toolcore.Definition{Name: "ls", Version: "1.0.0"})
	result := runHandler(t, handler, map[string]any{"path": "."}, runtime)
	if !result.Success {
		t.Fatalf("ls against the accessor failed: %s", result.Content)
	}
	// ls returns one level by default: the three children, not the nested file.
	for _, want := range []string{"alpha.txt", "beta.log", "pkg"} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("ls output is missing %s:\n%s", want, result.Content)
		}
	}
	if strings.Contains(result.Content, "gamma.txt") {
		t.Fatalf("a one-level listing descended into a subdirectory:\n%s", result.Content)
	}

	// A recursive listing is the case that needs the target's path spelling, because the
	// walk builds the child path itself: with the host's separator the accessor is asked
	// for "\srv\work\pkg\gamma.txt" and the descent silently finds nothing.
	deep := runHandler(t, handler, map[string]any{"path": ".", "depth": 3}, runtime)
	if !deep.Success {
		t.Fatalf("recursive ls failed: %s", deep.Content)
	}
	if !strings.Contains(deep.Content, "gamma.txt") {
		t.Fatalf("a recursive listing did not descend the accessor's tree:\n%s", deep.Content)
	}
}

// TestSharedGrepPagesTheAccessorsTree pins the property a remote core depends on most: the
// page is filled by the target's own enumeration order, sorted by the core, and a second
// page continues rather than repeating. One-level fakes cannot show that, so this tree has
// enough files to page.
func TestSharedGrepPagesTheAccessorsTree(t *testing.T) {
	fake := &fakeEnvdFiles{entries: map[string]envdclient.Entry{"/srv/work": fakeDirEntry("/srv/work")}, listing: map[string][]string{}, content: map[string][]byte{}}
	names := make([]string, 0, 12)
	for i := 0; i < 12; i++ {
		name := "f" + strconv.Itoa(i) + ".txt"
		names = append(names, name)
		fake.entries[path.Join("/srv/work", name)] = fakeFileEntry(path.Join("/srv/work", name), int64(len("needle\n")))
		fake.content[path.Join("/srv/work", name)] = []byte("needle\n")
	}
	// Reverse order on purpose: the adapter promises no ordering, so a core that trusted it
	// would page in the target's order while a local search pages lexically.
	for i, j := 0, len(names)-1; i < j; i, j = i+1, j-1 {
		names[i], names[j] = names[j], names[i]
	}
	fake.listing["/srv/work"] = names

	access := &envdAccess{api: fake}
	runtime := envdCoreRuntime{paths: envdCorePaths{root: "/srv/work"}, access: access}
	handler := searchcore.NewGrepHandler(toolcore.Definition{Name: "grep", Version: "1.0.0"})

	first := runHandler(t, handler, map[string]any{"pattern": "needle", "path": ".", "limit": 5}, runtime)
	if !first.Success {
		t.Fatalf("first page failed: %s", first.Content)
	}
	if !strings.Contains(first.Content, "f0.txt") {
		t.Fatalf("the first page did not start at the lexical head, so the core did not sort the target's raw order:\n%s", first.Content)
	}
	second := runHandler(t, handler, map[string]any{"pattern": "needle", "path": ".", "limit": 5, "offset": 5}, runtime)
	if !second.Success {
		t.Fatalf("second page failed: %s", second.Content)
	}
	if strings.Contains(second.Content, "f0.txt") {
		t.Fatalf("page two repeated page one's first entry:\n%s", second.Content)
	}
	if !strings.Contains(second.Content, "f5.txt") {
		t.Fatalf("page two did not continue the sequence:\n%s", second.Content)
	}
}

// TestSharedGrepHonoursTheTargetsIgnoreRules is the second half of the path-spelling
// problem, and it is the half that stays invisible longest.
//
// The walk's child paths were fixed by JoinTargetPath, but the ignore subsystem does its own
// path arithmetic - it builds each candidate rule file's name, walks the ancestor chain, and
// computes a path relative to a rule's scope - all with the host's rules. On a Windows client
// the candidate for the target's .gitignore is spelled "\srv\work\.gitignore", the daemon has
// no such path, and the caller's own rules are silently not applied. Nothing reports an
// error: the search just returns files the target's repository excludes.
func TestSharedGrepHonoursTheTargetsIgnoreRules(t *testing.T) {
	fake := &fakeEnvdFiles{
		entries: map[string]envdclient.Entry{
			"/srv/work":            fakeDirEntry("/srv/work"),
			"/srv/work/.gitignore": fakeFileEntry("/srv/work/.gitignore", int64(len("skip.txt\n"))),
			"/srv/work/skip.txt":   fakeFileEntry("/srv/work/skip.txt", int64(len("needle skip\n"))),
			"/srv/work/keep.txt":   fakeFileEntry("/srv/work/keep.txt", int64(len("needle keep\n"))),
		},
		listing: map[string][]string{"/srv/work": {"skip.txt", "keep.txt", ".gitignore"}},
		content: map[string][]byte{
			"/srv/work/.gitignore": []byte("skip.txt\n"),
			"/srv/work/skip.txt":   []byte("needle skip\n"),
			"/srv/work/keep.txt":   []byte("needle keep\n"),
		},
	}
	access := &envdAccess{api: fake}
	runtime := envdCoreRuntime{paths: envdCorePaths{root: "/srv/work"}, access: access}
	handler := searchcore.NewGrepHandler(toolcore.Definition{Name: "grep", Version: "1.0.0"})

	result := runHandler(t, handler, map[string]any{"pattern": "needle", "path": "."}, runtime)
	if !result.Success {
		t.Fatalf("grep with the target's ignore file failed: %s", result.Content)
	}
	if !strings.Contains(result.Content, "keep.txt") {
		t.Fatalf("the search found nothing at all:\n%s", result.Content)
	}
	if strings.Contains(result.Content, "skip.txt") {
		t.Fatalf("the target's own .gitignore was not applied (its candidate path was spelled with the host's separator):\n%s\nstatCalls=%v listCalls=%v",
			result.Content, fake.statCalls, fake.listCalls)
	}

	// Control: without an accessor the tree does not exist here, so the assertion above
	// cannot be passing because the rules came from this machine by accident.
	control := runHandler(t, handler, map[string]any{"pattern": "needle", "path": "."},
		envdCoreRuntime{paths: envdCorePaths{root: "/srv/work"}, access: nil})
	if control.Success && strings.Contains(control.Content, "keep.txt") {
		t.Fatalf("the control searched a tree that exists for no one locally:\n%s", control.Content)
	}
}

// TestSharedGrepGlobFilterUsesTheTargetsPaths is the same question one level up: the
// include/exclude glob is matched against a path relative to the search root, and that
// relative path is also computed with the host's rules.
func TestSharedGrepGlobFilterUsesTheTargetsPaths(t *testing.T) {
	access := &envdAccess{api: envdIntegrationTree()}
	runtime := envdCoreRuntime{paths: envdCorePaths{root: "/srv/work"}, access: access}
	handler := searchcore.NewGrepHandler(toolcore.Definition{Name: "grep", Version: "1.0.0"})

	// A path-aware pattern: `pkg/*.txt` must match the nested file and nothing at the root.
	result := runHandler(t, handler, map[string]any{"pattern": "needle", "path": ".", "glob": "pkg/*.txt"}, runtime)
	if !result.Success {
		t.Fatalf("glob-filtered grep failed: %s", result.Content)
	}
	if !strings.Contains(result.Content, "gamma.txt") {
		t.Fatalf("the glob filter excluded the file it names (relative path was computed with the host's rules):\n%s", result.Content)
	}
	if strings.Contains(result.Content, "alpha.txt") {
		t.Fatalf("the glob filter admitted a file outside its pattern:\n%s", result.Content)
	}
}
