package searchcore

// S5 of docs/plan/searchcore-io-seam-migration-20261028.md: the traversal itself.
//
// These tests carry the weight of the claim that walkViaAccess is a copy of
// filepath.WalkDir and not an approximation. Traversal order is not cosmetic here: the
// fetch limit, the paging offset and the early SkipAll are all decided in visit order, so
// a re-ordered walk silently changes which files a caller is shown - which is exactly what
// the ls migration's differential test was built to catch, and the reason this step is not
// allowed to ship on "the existing tests still pass".

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/localio"
)

// walkPolicy turns one visited entry into the decision a caller would make, so the
// differential covers the SkipDir/SkipAll/error paths and not only the happy one.
type walkPolicy struct {
	name string
	// decide returns the callback's value for one entry. A nil decide means "nil".
	decide func(path string, entry fs.DirEntry, err error, visitIndex int) error
}

func buildWalkFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	mkdir := func(rel string) string {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(full, 0o755); err != nil {
			t.Fatal(err)
		}
		return full
	}
	touch := func(rel, body string) string {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return full
	}
	// Names chosen so that creation order differs from lexical order, and so that a
	// SkipDir on one file must hide only the siblings that follow it.
	touch("z.txt", "z\n")
	touch("a.txt", "a\n")
	mkdir("empty-dir")
	touch("nested/a-first.txt", "1\n")
	touch("nested/middle/b.txt", "2\n")
	touch("nested/z-last.txt", "3\n")
	touch(".hidden/c.txt", "3\n")
	linkTarget := touch("a.txt", "a\n")
	if err := os.Symlink(linkTarget, filepath.Join(root, "link-to-file.txt")); err != nil {
		t.Fatalf("this differential needs symlink support: %v", err)
	}
	if err := os.Symlink(filepath.Join(root, "nested"), filepath.Join(root, "link-to-dir")); err != nil {
		t.Fatalf("this differential needs symlink support: %v", err)
	}
	if err := os.Symlink(filepath.Join(root, "nowhere"), filepath.Join(root, "broken-link")); err != nil {
		t.Fatalf("this differential needs symlink support: %v", err)
	}
	return root
}

// describe renders one visit the way both walks record it, so a divergence says which
// entry differs instead of only that two slices were unequal.
func describe(path string, entry fs.DirEntry, err error) string {
	kind := "file"
	name := ""
	switch {
	case entry == nil:
		kind = "nil-entry"
	case entry.IsDir():
		kind = "dir"
	case entry.Type()&fs.ModeSymlink != 0:
		kind = "link"
	}
	if entry != nil {
		name = entry.Name()
	}
	if err != nil {
		return path + "|" + kind + "|" + name + "|err"
	}
	return path + "|" + kind + "|" + name
}

func (p walkPolicy) run(root string, walk func(func(string, fs.DirEntry, error) error) error) ([]string, error) {
	t := []string{}
	index := 0
	err := walk(func(path string, entry fs.DirEntry, err error) error {
		index++
		t = append(t, describe(path, entry, err))
		if p.decide == nil {
			return nil
		}
		return p.decide(path, entry, err, index)
	})
	return t, err
}

func TestWalkViaAccessMatchesWalkDirReference(t *testing.T) {
	root := buildWalkFixture(t)
	policies := []walkPolicy{
		{name: "visit-everything"},
		{name: "skip-the-nested-dir", decide: func(path string, entry fs.DirEntry, _ error, _ int) error {
			if entry != nil && entry.IsDir() && entry.Name() == "nested" {
				return filepath.SkipDir
			}
			return nil
		}},
		// The subtle one: SkipDir returned for a NON-directory must stop the remaining
		// siblings of that directory, which is what the stdlib's loop does with `break`.
		{name: "skip-rest-after-one-file", decide: func(path string, entry fs.DirEntry, _ error, _ int) error {
			if entry != nil && !entry.IsDir() && filepath.Base(path) == "a-first.txt" {
				return filepath.SkipDir
			}
			return nil
		}},
		{name: "stop-at-first-file", decide: func(path string, entry fs.DirEntry, _ error, _ int) error {
			if entry != nil && !entry.IsDir() {
				return filepath.SkipAll
			}
			return nil
		}},
		{name: "error-propagates", decide: func(path string, entry fs.DirEntry, _ error, _ int) error {
			if filepath.Base(path) == "b.txt" {
				return errors.New("stop here")
			}
			return nil
		}},
	}

	for _, policy := range policies {
		t.Run(policy.name, func(t *testing.T) {
			oracle, oracleErr := policy.run(root, func(fn func(string, fs.DirEntry, error) error) error {
				return filepath.WalkDir(root, fn)
			})

			viaSeam, seamErr := policy.run(root, func(fn func(string, fs.DirEntry, error) error) error {
				return walkViaAccess(context.Background(), localio.Access, root, fn)
			})

			if !reflect.DeepEqual(oracle, viaSeam) || !reflect.DeepEqual(oracleErr, seamErr) {
				t.Fatalf("walkViaAccess diverged from filepath.WalkDir\n oracle (%d): %v\n  seam  (%d): %v\n oracleErr=%v seamErr=%v",
					len(oracle), oracle, len(viaSeam), viaSeam, oracleErr, seamErr)
			}

			// The same walk against an accessor whose listing is NOT sorted. os.ReadDir
			// sorted for us before the seam, so this is the only way to prove the walker
			// sorts by name itself instead of inheriting the host's courtesy.
			viaUnsorted, unsortedErr := policy.run(root, func(fn func(string, fs.DirEntry, error) error) error {
				return walkViaAccess(context.Background(), reverseReadDirAccess{localio.Access}, root, fn)
			})
			if !reflect.DeepEqual(oracle, viaUnsorted) || !reflect.DeepEqual(oracleErr, unsortedErr) {
				t.Fatalf("a remote listing that is not pre-sorted changed the visit order\n oracle: %v\n  got: %v",
					oracle, viaUnsorted)
			}
		})
	}
}

// reverseReadDirAccess hands back each directory in reverse name order, which is what an
// unsorted remote listing looks like. A real transport makes no ordering promise, so the
// walker must not depend on one.
type reverseReadDirAccess struct{ inner toolcore.Access }

func (a reverseReadDirAccess) Stat(ctx context.Context, path string) (fs.FileInfo, error) {
	return a.inner.Stat(ctx, path)
}

func (a reverseReadDirAccess) Lstat(ctx context.Context, path string) (fs.FileInfo, error) {
	return a.inner.Lstat(ctx, path)
}

func (a reverseReadDirAccess) Open(ctx context.Context, path string) (toolcore.FileHandle, error) {
	return a.inner.Open(ctx, path)
}

func (a reverseReadDirAccess) ReadDir(ctx context.Context, path string) ([]fs.DirEntry, error) {
	entries, err := a.inner.ReadDir(ctx, path)
	if err != nil {
		return nil, err
	}
	reversed := append([]fs.DirEntry(nil), entries...)
	sort.Slice(reversed, func(i, j int) bool { return reversed[i].Name() > reversed[j].Name() })
	return reversed, nil
}

// unreadableAccess fails exactly one directory listing, which is the branch the host tree
// cannot be asked to produce on demand.
type unreadableAccess struct {
	toolcore.Access
	bad string
}

func (a unreadableAccess) ReadDir(ctx context.Context, path string) ([]fs.DirEntry, error) {
	if path == a.bad {
		return nil, errors.New("permission denied")
	}
	return a.Access.ReadDir(ctx, path)
}

// TestWalkViaAccessReportsAReadDirErrorLikeWalkDir pins the stdlib's "second call": an
// unreadable directory is reported to the callback twice - once as the entry itself, once
// with the listing error - and the walk then continues with the next sibling. The shape of
// that sequence is what makes a permission problem cost one subtree instead of the search.
func TestWalkViaAccessReportsAReadDirErrorLikeWalkDir(t *testing.T) {
	root := buildWalkFixture(t)
	bad := filepath.Join(root, "nested")
	access := unreadableAccess{Access: localio.Access, bad: bad}

	var visits []string
	err := walkViaAccess(context.Background(), access, root, func(path string, entry fs.DirEntry, err error) error {
		visits = append(visits, describe(path, entry, err))
		return nil
	})
	if err != nil {
		t.Fatalf("an unreadable subtree must not fail the walk: %v", err)
	}

	var reportedErr, reachedAfter int
	for _, visit := range visits {
		if strings.HasPrefix(visit, bad) && strings.HasSuffix(visit, "|err") {
			reportedErr++
		}
		if strings.HasPrefix(visit, filepath.Join(root, "z.txt")) {
			reachedAfter++
		}
	}
	if reportedErr != 1 {
		t.Fatalf("the listing error must be reported for that directory exactly once, got %d in %v", reportedErr, visits)
	}
	if reachedAfter == 0 {
		t.Fatalf("the walk abandoned the whole tree after one unreadable directory: %v", visits)
	}
	// The directory itself is visited twice: as its own entry, then with the error.
	firstAsEntry := 0
	for _, visit := range visits {
		if strings.HasPrefix(visit, bad) && !strings.HasSuffix(visit, "|err") {
			firstAsEntry++
		}
	}
	if firstAsEntry != 1 {
		t.Fatalf("the directory must also be reported as an entry once, got %d in %v", firstAsEntry, visits)
	}

	// The three dispositions of the second call, each pinned against the stdlib's text:
	//
	//   - SkipDir on a directory whose listing failed means "that subtree is done", and
	//     since the listing already failed the visit sequence is the SAME as swallowing
	//     the error. Asserting the equality is the point: an implementation that treated
	//     SkipDir as a fatal error would abort here instead.
	var skipped []string
	skipErr := walkViaAccess(context.Background(), access, root, func(path string, entry fs.DirEntry, err error) error {
		skipped = append(skipped, describe(path, entry, err))
		if err != nil && entry != nil && entry.IsDir() {
			return filepath.SkipDir
		}
		return nil
	})
	if skipErr != nil {
		t.Fatalf("SkipDir on a listing error must not surface as a failure: %v", skipErr)
	}
	if !reflect.DeepEqual(visits, skipped) {
		t.Fatalf("a failed listing is already finished; SkipDir must not change what is visited\n swallow: %v\n skipdir: %v", visits, skipped)
	}

	//   - a real error from that same callback aborts the walk and propagates, which is
	//     what makes the other two branches a policy choice rather than an accident.
	var stopped []string
	abortErr := walkViaAccess(context.Background(), access, root, func(path string, entry fs.DirEntry, err error) error {
		stopped = append(stopped, describe(path, entry, err))
		if err != nil {
			return err
		}
		return nil
	})
	if abortErr == nil {
		t.Fatal("a callback error on a failed listing must abort the walk")
	}
	if len(stopped) >= len(visits) {
		t.Fatalf("the abort must stop earlier than completing the tree (%d vs %d visits)", len(stopped), len(visits))
	}
}

// TestGlobEnumeratesATreeTheHostDoesNotHave is the strongest single statement of this
// step: the directory does not exist on this machine at all, and every path below it comes
// from the accessor. Listing it is only possible through the seam.
func TestGlobEnumeratesATreeTheHostDoesNotHave(t *testing.T) {
	hostDir := t.TempDir()
	remoteRoot := filepath.Join(hostDir, "checkout")
	deep := filepath.Join(remoteRoot, "src", "pkg")
	access := &virtualAccess{
		files: map[string][]byte{
			filepath.Join(deep, "one.go"):          []byte("package one\n"),
			filepath.Join(deep, "two.go"):          []byte("package two\n"),
			filepath.Join(remoteRoot, "README.md"): []byte("# hi\n"),
		},
		dirs: map[string]bool{remoteRoot: true, deep: true, filepath.Join(remoteRoot, "src"): true},
	}

	result, err := GlobWithOptions(context.Background(), GlobOptions{
		Base: remoteRoot, Pattern: "**/*.go", Limit: 10, Access: access,
	})
	if err != nil {
		t.Fatalf("glob over an accessor-only tree: %v", err)
	}
	if len(result.Matches) != 2 {
		t.Fatalf("matches = %d (%v), want the two files the accessor listed", len(result.Matches), result.Matches)
	}

	// Control: the same call without an accessor looks at this machine, where the tree is
	// genuinely absent, and must find nothing.
	local, err := GlobWithOptions(context.Background(), GlobOptions{
		Base: remoteRoot, Pattern: "**/*.go", Limit: 10,
	})
	if err != nil {
		t.Fatalf("local glob: %v", err)
	}
	if len(local.Matches) != 0 {
		t.Fatalf("the control is wrong: that path does not exist here, got %v", local.Matches)
	}
}

// TestGrepEnumeratesATreeTheHostDoesNotHave is the grep counterpart, and it also pins that
// the ignore rules travel with the same listing: the tree carries its own .gitignore, which
// only exists on the target.
func TestGrepEnumeratesATreeTheHostDoesNotHave(t *testing.T) {
	hostDir := t.TempDir()
	remoteRoot := filepath.Join(hostDir, "checkout")
	access := &virtualAccess{
		files: map[string][]byte{
			filepath.Join(remoteRoot, ".gitignore"):         []byte("vendor\n"),
			filepath.Join(remoteRoot, "keep.txt"):           []byte("needle here\n"),
			filepath.Join(remoteRoot, "vendor", "skip.txt"): []byte("needle too\n"),
		},
		dirs: map[string]bool{
			remoteRoot:                          true,
			filepath.Join(remoteRoot, "vendor"): true,
		},
	}

	result, err := Grep(context.Background(), GrepOptions{
		Pattern: "needle", Path: remoteRoot, Limit: 10, Access: access,
	})
	if err != nil {
		t.Fatalf("grep over an accessor-only tree: %v", err)
	}
	if len(result.Matches) != 1 {
		t.Fatalf("matches = %d (%v), want only keep.txt", len(result.Matches), result.Matches)
	}
	if filepath.Base(result.Matches[0].File) != "keep.txt" {
		t.Fatalf("the target's own .gitignore was not honoured: %v", result.Matches[0].File)
	}

	local, err := Grep(context.Background(), GrepOptions{
		Pattern: "needle", Path: remoteRoot, Limit: 10,
	})
	if err != nil {
		t.Fatalf("local grep: %v", err)
	}
	if len(local.Matches) != 0 {
		t.Fatalf("the control is wrong: that tree is not on this machine, got %v", local.Matches)
	}
}

// --- the engine's shape answers ---

// The ripgrep engine runs where the caller put it, but it decides its own argv from the
// shape of the paths it is handed. Those are the caller's paths: answering from this
// machine means a remote directory that is a file here gets searched with the wrong
// program shape, and the difference shows up as a silently different result rather than an
// error.

func TestRipgrepGlobAsksTheAccessorAboutTheBase(t *testing.T) {
	hostDir := t.TempDir()
	base := filepath.Join(hostDir, "checkout")
	// Here: a plain file. On the target: the directory the caller means.
	writeHostFile(t, base, "not a directory here\n")
	access := &virtualAccess{
		files: map[string][]byte{filepath.Join(base, "one.go"): []byte("package one\n")},
		dirs:  map[string]bool{base: true},
	}

	invoked := false
	engine := Ripgrep{
		Path: "rg",
		Stream: func(_ context.Context, _ []string, _ string, _ func(line []byte) bool) error {
			invoked = true
			return nil
		},
	}
	result, used, err := engine.Glob(context.Background(), GlobOptions{
		Base: base, Pattern: "*.go", Limit: 10, Access: access,
	})
	if err != nil {
		t.Fatalf("engine glob: %v", err)
	}
	if !used || !invoked {
		t.Fatalf("the engine declined a base the accessor called a directory; it decided from this machine's file (used=%v invoked=%v %v)",
			used, invoked, result.Matches)
	}

	// Control: the same base with no accessor is a file here, and a file base is exactly
	// the shape the engine hands back to the walker.
	localResult, localUsed, err := engine.Glob(context.Background(), GlobOptions{
		Base: base, Pattern: "*.go", Limit: 10,
	})
	if err != nil {
		t.Fatalf("local engine glob: %v", err)
	}
	if localUsed || len(localResult.Matches) != 0 {
		t.Fatalf("the control is wrong: a file base must be declined, got used=%v %v", localUsed, localResult.Matches)
	}
}

func TestRipgrepSingleFileFollowsTheAccessorShape(t *testing.T) {
	hostDir := t.TempDir()
	root := filepath.Join(hostDir, "one-file.txt")
	// Here: a directory. On the target: the single file the caller named.
	if err := os.MkdirAll(filepath.Join(root, "inner"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeHostFile(t, filepath.Join(root, "inner", "a.txt"), "needle\n")
	access := &virtualAccess{
		files: map[string][]byte{root: []byte("needle in the only file\n")},
	}

	engine := Ripgrep{
		Path: "rg",
		Stream: func(_ context.Context, _ []string, _ string, onLine func(line []byte) bool) error {
			onLine([]byte(root + ":1:needle in the only file"))
			return nil
		},
	}
	result, used, err := engine.Grep(context.Background(), GrepOptions{
		Pattern: "needle", Path: root, Limit: 10, Access: access,
	})
	if err != nil {
		t.Fatalf("engine grep: %v", err)
	}
	if !used {
		t.Fatal("the engine declined a search it should have answered")
	}
	if !result.SingleFile {
		t.Fatalf("SingleFile was decided by this machine's directory, not the accessor's file: %+v", result)
	}

	// Control: without an accessor the path is a directory here, and the same call must
	// report a tree search instead.
	local, localUsed, err := engine.Grep(context.Background(), GrepOptions{
		Pattern: "needle", Path: root, Limit: 10,
	})
	if err != nil {
		t.Fatalf("local engine grep: %v", err)
	}
	if !localUsed || local.SingleFile {
		t.Fatalf("the control is wrong: a directory root must not be marked single-file (used=%v %+v)", localUsed, local)
	}
}

// TestGrepSingleFileFlagFollowsTheAccessorShape covers the walker's two remaining shape
// answers - "is this root a single file" for the result flag and for the search mode -
// against the same asymmetry: a path that is a directory here and the file the caller
// named on the target. Before this step both questions were answered from the host, so a
// remote file behind a locally-existing directory name was reported as a tree search.
func TestGrepSingleFileFlagFollowsTheAccessorShape(t *testing.T) {
	hostDir := t.TempDir()
	root := filepath.Join(hostDir, "notes.txt")
	// Here it is a directory that happens to carry the same name, holding a file that
	// does not match, so a host-shaped answer is visible rather than invisible.
	writeHostFile(t, filepath.Join(root, "inner.md"), "nothing to see\n")

	access := &virtualAccess{
		files: map[string][]byte{root: []byte("needle in the named file\n")},
	}

	result, err := Grep(context.Background(), GrepOptions{
		Pattern: "needle", Path: root, Limit: 10, Access: access,
	})
	if err != nil {
		t.Fatalf("grep: %v", err)
	}
	if !result.SingleFile {
		t.Fatalf("the accessor called this root a file; the result still describes a tree search: %+v", result)
	}
	if len(result.Matches) != 1 {
		t.Fatalf("matches = %v, want the one line of the file the accessor named", result.Matches)
	}

	// Control: without an accessor this machine has a directory there, and the same call
	// searches it as a tree and finds nothing.
	local, err := Grep(context.Background(), GrepOptions{
		Pattern: "needle", Path: root, Limit: 10,
	})
	if err != nil {
		t.Fatalf("local grep: %v", err)
	}
	if local.SingleFile || len(local.Matches) != 0 {
		t.Fatalf("the control is wrong: a local directory must not be single-file, got %+v", local)
	}
}

// --- S5b: the follow-mode traversal ---

// legacyWalkGrepFollowing is a frozen snapshot of the implementation that existed before
// S5b - the same algorithm reading the host directly (os.Stat, os.ReadDir,
// filepath.EvalSymlinks). It lives here only as the oracle for the differential below.
// Keeping the old code, rather than re-deriving expectations by hand, is the only way to
// pin "the follow walker changed its source of truth and nothing else"; lscore's
// walk_differential_test.go set that precedent.
func legacyWalkGrepFollowing(root string, onEntry func(path string, entry fs.DirEntry, err error) error) error {
	rootInfo, statErr := os.Stat(root)
	if statErr != nil {
		return onEntry(root, nil, statErr)
	}
	seen := map[string]bool{}
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		seen[resolved] = true
	}
	if err := onEntry(root, fs.FileInfoToDirEntry(rootInfo), nil); err != nil {
		if err == filepath.SkipAll {
			return err
		}
		if err == filepath.SkipDir {
			return nil
		}
		return err
	}
	if !rootInfo.IsDir() {
		return nil
	}
	type dirFrame struct {
		dir     string
		entries []fs.DirEntry
		index   int
	}
	stack := []*dirFrame{{dir: root}}
	for len(stack) > 0 {
		frame := stack[len(stack)-1]
		if frame.entries == nil {
			entries, readErr := os.ReadDir(frame.dir)
			if readErr != nil {
				stack = stack[:len(stack)-1]
				continue
			}
			frame.entries = entries
		}
		if frame.index >= len(frame.entries) {
			stack = stack[:len(stack)-1]
			continue
		}
		entry := frame.entries[frame.index]
		frame.index++
		path := filepath.Join(frame.dir, entry.Name())
		isDir := entry.IsDir()
		if entry.Type()&fs.ModeSymlink != 0 {
			info, statErr := os.Stat(path)
			if statErr != nil {
				continue
			}
			if info.IsDir() {
				resolved, resolveErr := filepath.EvalSymlinks(path)
				if resolveErr != nil || seen[resolved] {
					continue
				}
				seen[resolved] = true
				isDir = true
				entry = fs.FileInfoToDirEntry(info)
			}
		}
		if err := onEntry(path, entry, nil); err != nil {
			switch {
			case err == filepath.SkipAll:
				return err
			case err == filepath.SkipDir && isDir:
				continue
			case err == filepath.SkipDir:
				frame.index = len(frame.entries)
				continue
			default:
				return err
			}
		}
		if isDir {
			stack = append(stack, &dirFrame{dir: path})
		}
	}
	return nil
}

// buildFollowFixture makes the link shapes the walker has to get right: a link to a file,
// two links to one directory (searched once), a mutual pair between two real directories
// (the cycle case), a link pointing at an ancestor, and a broken link.
func buildFollowFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	touch := func(rel, body string) string {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return full
	}
	touch("top.txt", "top\n")
	dirA := filepath.Join(root, "alpha")
	dirB := filepath.Join(root, "beta")
	for _, d := range []string{dirA, dirB} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	touch("alpha/in-alpha.txt", "a\n")
	touch("beta/in-beta.txt", "b\n")
	links := map[string]string{
		"link-to-file":  filepath.Join(root, "top.txt"),
		"link-to-alpha": dirA,
		"dup-to-alpha":  dirA,
		"link-to-beta":  dirB,
		"loop-to-root":  root,
		"broken-link":   filepath.Join(root, "nothing-here"),
	}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
			t.Fatalf("this differential needs symlink support: %v", err)
		}
	}
	// The mutual pair, which is what a plain "already visited this path" check cannot
	// terminate: each link's own path is new every time.
	if err := os.Symlink(dirB, filepath.Join(dirA, "to-beta")); err != nil {
		t.Fatalf("this differential needs symlink support: %v", err)
	}
	if err := os.Symlink(dirA, filepath.Join(dirB, "to-alpha")); err != nil {
		t.Fatalf("this differential needs symlink support: %v", err)
	}
	return root
}

func TestWalkGrepFollowingMatchesLegacyImplementation(t *testing.T) {
	root := buildFollowFixture(t)
	resolver, ok := localio.Access.(toolcore.SymlinkResolver)
	if !ok {
		t.Fatal("localio.Access must provide the link capability, or follow mode would decline on every local search")
	}

	// boundVisits keeps a broken cycle rule from crashing the test with a stack overflow.
	// An unbounded walk must fail as an assertion instead: "goroutine stack exceeds" proves
	// the same thing less precisely, hides which rule broke, and spends the package timeout
	// doing it - which is how the first version of this differential reported its evidence.
	const boundVisits = 400
	tooMany := errors.New("访问数超过上限：链接环规则没有终止遍历")
	collect := func(walk func(func(string, fs.DirEntry, error) error) error) []string {
		visits := []string{}
		err := walk(func(path string, entry fs.DirEntry, err error) error {
			visits = append(visits, describe(path, entry, err))
			if len(visits) > boundVisits {
				return tooMany
			}
			return nil
		})
		if err != nil {
			visits = append(visits, "RETURNED-ERROR: "+err.Error())
		}
		return visits
	}

	oracle := collect(func(fn func(string, fs.DirEntry, error) error) error {
		return legacyWalkGrepFollowing(root, fn)
	})
	got := collect(func(fn func(string, fs.DirEntry, error) error) error {
		return walkGrepFollowing(context.Background(), localio.Access, resolver, root, fn)
	})
	if !reflect.DeepEqual(oracle, got) {
		t.Fatalf("the accessor-driven follow walker diverged from the host one\n legacy (%d): %v\n   seam (%d): %v", len(oracle), oracle, len(got), got)
	}

	// Same tree, unsorted listing: proves the walker imposes the lexical order itself
	// instead of inheriting os.ReadDir's courtesy.
	unsorted := collect(func(fn func(string, fs.DirEntry, error) error) error {
		return walkGrepFollowing(context.Background(), reverseReadDirAccess{localio.Access}, resolver, root, fn)
	})
	if !reflect.DeepEqual(oracle, unsorted) {
		t.Fatalf("an unsorted listing changed the follow walk order\n legacy: %v\n   got: %v", oracle, unsorted)
	}

	// And the guarantee the cycle rule exists for: a mutual link pair terminates with each
	// real directory's own files reported once per distinct path, not endlessly.
	hits := 0
	for _, visit := range got {
		if strings.HasSuffix(visit, "in-alpha.txt|file|in-alpha.txt") {
			hits++
		}
	}
	if hits == 0 || hits > 4 {
		t.Fatalf("the link cycle rule produced %d visits of in-alpha.txt, want a small bounded number: %v", hits, got)
	}
}

// noResolverAccess hides a capability that the value inside it has. Embedding the interface
// type, rather than the concrete one, is what makes the method set shrink - which is how a
// backend that cannot resolve links is represented without writing a second fake.
type noResolverAccess struct{ toolcore.Access }

func TestGrepFollowNeedsTheLinkCapabilityOnlyWhenThereAreLinks(t *testing.T) {
	hostDir := t.TempDir()
	remote := filepath.Join(hostDir, "repo")
	real := filepath.Join(remote, "src")
	linked := filepath.Join(remote, "current")

	plain := &virtualAccess{
		files: map[string][]byte{filepath.Join(real, "one.txt"): []byte("needle one\n")},
		dirs:  map[string]bool{remote: true, real: true},
	}

	// A link-free tree is answered by a resolver-less accessor with no decline at all. This
	// is the case my first version of the gate broke: the handler turns follow on by default
	// on Windows, so refusing up front would have denied every ordinary search.
	capabilityless := noResolverAccess{plain}
	result, err := Grep(context.Background(), GrepOptions{
		Pattern: "needle", Path: remote, Limit: 10, Follow: true, Access: capabilityless,
	})
	if err != nil {
		t.Fatalf("a tree without links must not need the link capability: %v", err)
	}
	if len(result.Matches) != 1 {
		t.Fatalf("matches = %v, want the one file in the accessor's tree", result.Matches)
	}

	// The same accessor, the same options, but the tree now contains a link to a directory:
	// the walk must decline instead of dropping the cycle check or guessing an identity.
	withLink := &virtualAccess{
		files: map[string][]byte{filepath.Join(real, "one.txt"): []byte("needle one\n")},
		dirs:  map[string]bool{remote: true, real: true},
		links: map[string]string{linked: real},
	}
	_, err = Grep(context.Background(), GrepOptions{
		Pattern: "needle", Path: remote, Limit: 10, Follow: true, Access: noResolverAccess{withLink},
	})
	if err == nil {
		t.Fatal("following a link to a directory without a resolver must decline, not search on without cycle protection")
	}
	if !strings.Contains(err.Error(), "SymlinkResolver") {
		t.Fatalf("the refusal must name the missing capability: %v", err)
	}
}

// TestGrepFollowReachesAMatchThatOnlyExistsThroughALink is the injection half: the tree
// exists only for the accessor, and the reported path is reached through a link that this
// machine has never heard of. Getting it therefore needs enumeration, link detection, the
// target's shape and the resolver all answered by the same non-local source.
func TestGrepFollowReachesAMatchThatOnlyExistsThroughALink(t *testing.T) {
	hostDir := t.TempDir()
	remote := filepath.Join(hostDir, "repo")
	real := filepath.Join(remote, "src")
	linked := filepath.Join(remote, "current")
	second := filepath.Join(remote, "mirror")

	access := &virtualAccess{
		files: map[string][]byte{filepath.Join(real, "one.txt"): []byte("needle one\n")},
		dirs:  map[string]bool{remote: true, real: true},
		links: map[string]string{linked: real, second: real},
	}

	result, err := Grep(context.Background(), GrepOptions{
		Pattern: "needle", Path: remote, Limit: 10, Follow: true, Access: access,
	})
	if err != nil {
		t.Fatalf("follow with the accessor's links: %v", err)
	}
	found := map[string]bool{}
	for _, match := range result.Matches {
		rel, relErr := filepath.Rel(remote, match.File)
		if relErr != nil {
			t.Fatalf("match outside the root: %v", match.File)
		}
		found[filepath.ToSlash(rel)] = true
	}
	// src/one.txt is reached directly and once through each link; two links to one
	// directory are searched once, so exactly one link path joins the real one.
	if len(found) != 2 || !found["src/one.txt"] {
		t.Fatalf("follow did not use the accessor's links: %v", found)
	}
	if !found["current/one.txt"] && !found["mirror/one.txt"] {
		t.Fatalf("expected one link path alongside the real one, got %v", found)
	}

	// Control: the same call with no accessor searches this machine, where the tree is
	// absent, and must find nothing.
	local, err := Grep(context.Background(), GrepOptions{
		Pattern: "needle", Path: remote, Limit: 10, Follow: true,
	})
	if err != nil {
		t.Fatalf("local follow grep: %v", err)
	}
	if len(local.Matches) != 0 {
		t.Fatalf("the control is wrong: that tree is not on this machine, got %v", local.Matches)
	}
}

// --- S5c: the filesystem boundary behind one_file_system ---

// noIdentityAccess hides the optional filesystem capability the same way noResolverAccess
// hides the link one: embedding the interface type, not the concrete value.
type noIdentityAccess struct{ toolcore.Access }

// TestGrepOneFileSystemUsesTheAccessorsBoundary is the injection for the last host read in
// this core. The tree exists only for the accessor, and the accessor says one of its
// directories sits on another filesystem. This machine would answer "same volume" for every
// one of those paths - on Windows the host probe is a path string, on Unix a st_dev of a
// directory that is not here - so a boundary taken from the host simply would not exist and
// the search would descend into a mount the caller excluded.
func TestGrepOneFileSystemUsesTheAccessorsBoundary(t *testing.T) {
	hostDir := t.TempDir()
	remote := filepath.Join(hostDir, "repo")
	mounted := filepath.Join(remote, "mounted")

	access := &virtualAccess{
		files: map[string][]byte{
			filepath.Join(remote, "local.txt"): []byte("needle local\n"),
			filepath.Join(mounted, "far.txt"):  []byte("needle far\n"),
		},
		dirs:    map[string]bool{remote: true, mounted: true},
		devices: map[string]string{mounted: "other-filesystem"},
	}

	shared := GrepOptions{Pattern: "needle", Path: remote, Limit: 50, Access: access}

	// Control first, and it is the load-bearing half: with the flag off both sides of the
	// boundary are searched, so any absence below is the boundary doing work rather than the
	// subtree never having been enumerated.
	without, err := Grep(context.Background(), shared)
	if err != nil {
		t.Fatalf("grep without the flag: %v", err)
	}
	if names := grepFileNames(without); len(names) != 2 {
		t.Fatalf("control must search both sides of the boundary, got %v", names)
	}

	shared.OneFileSystem = true
	with, err := Grep(context.Background(), shared)
	if err != nil {
		t.Fatalf("grep with the flag: %v", err)
	}
	assertFileList(t, grepFileNames(with), []string{"local.txt"})
}

// TestGrepOneFileSystemDeclinesWithoutTheCapability pins the second half of design §2 Q2.
// A transport that cannot report a device must not have the boundary inferred for it:
// guessing from path shape would either search a mount the caller excluded or stop short of
// the tree they asked for, and either result would look completely normal.
//
// The decline is also scoped to the feature that needs the capability, which is the same
// lesson S5b learned the hard way: the same accessor, without one_file_system, still
// searches - because nothing about that tree requires knowing a device.
func TestGrepOneFileSystemDeclinesWithoutTheCapability(t *testing.T) {
	hostDir := t.TempDir()
	remote := filepath.Join(hostDir, "repo")
	mounted := filepath.Join(remote, "mounted")

	access := &virtualAccess{
		files: map[string][]byte{
			filepath.Join(remote, "local.txt"): []byte("needle local\n"),
			filepath.Join(mounted, "far.txt"):  []byte("needle far\n"),
		},
		dirs:    map[string]bool{remote: true, mounted: true},
		devices: map[string]string{mounted: "other-filesystem"},
	}
	blocked := noIdentityAccess{access}

	_, err := Grep(context.Background(), GrepOptions{
		Pattern: "needle", Path: remote, Limit: 50, Access: blocked, OneFileSystem: true,
	})
	if err == nil {
		t.Fatal("one_file_system without a filesystem capability must decline, not search across an unknown boundary")
	}
	if !strings.Contains(err.Error(), "FilesystemIdentifier") {
		t.Fatalf("the refusal must name the missing capability: %v", err)
	}

	// The workaround the message offers is real: the same accessor answers normally once
	// the caller stops asking for a guarantee the transport cannot give.
	plain, err := Grep(context.Background(), GrepOptions{
		Pattern: "needle", Path: remote, Limit: 50, Access: blocked,
	})
	if err != nil {
		t.Fatalf("dropping one_file_system must not need the capability: %v", err)
	}
	if len(grepFileNames(plain)) != 2 {
		t.Fatalf("the same tree must still be searchable in full, got %v", grepFileNames(plain))
	}
}
