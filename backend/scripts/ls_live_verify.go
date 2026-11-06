//go:build ignore

// Command ls_live_verify drives the real aicli-remote-tools server on a target
// machine and proves that the tree walk behind `ls` still answers exactly as the
// pre-seam filepath.WalkDir traversal did.
//
// Why a second driver instead of trusting the unit differential: the unit test ran
// on Windows, where two things cannot be created honestly - a directory whose
// contents are truly unreadable (chmod 000 as an ordinary user) and symlinks. Both
// are the cases this change could break silently: a walk that follows a symlinked
// root climbs out of the authorized tree, and a walk that turns an unreadable
// subdirectory into an error makes one hole fail the whole listing.
//
// The expectation is computed on the target's own disk by the OLD algorithm
// (filepath.WalkDir with the original callback), then rendered through the shared
// renderer, and compared byte for byte with what the server answered. The server's
// answer and the expectation therefore come from two different implementations of
// the same contract, which is the only kind of comparison that can find a drift.
//
// Usage (run on the target):
//
//	ls_live_verify -server ./aicli-remote-tools -workspace /home/me/aicli-ls-live
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/lscore"
)

type nopWriteCloser struct{ io.Writer }

func (n nopWriteCloser) Close() error { return nil }

// cleanupFuncs run on every exit path, including a failed check. They exist because
// a fixture that fails halfway must not leave the target holding an unreadable
// directory: os.Exit skips deferred functions, so the first version of this driver
// abandoned its chmod 000 fixture on the machine it was verifying.
var cleanupFuncs []func()

func onCleanup(fn func()) { cleanupFuncs = append(cleanupFuncs, fn) }

func fail(check string, err error) {
	fmt.Printf("FAIL %s: %v\n", check, err)
	runCleanups()
	os.Exit(1)
}

// runCleanups empties the hook list, so a hook can never run twice.
func runCleanups() {
	for index := len(cleanupFuncs) - 1; index >= 0; index-- {
		cleanupFuncs[index]()
	}
	cleanupFuncs = nil
}

func main() {
	server := flag.String("server", "", "aicli-remote-tools binary to drive")
	workspace := flag.String("workspace", "", "absolute workspace on this machine")
	prefix := flag.String("prefix", "lsx", "tool prefix")
	simulateFailure := flag.Bool("simulate-failure", false, "fail right after building the fixture, to prove cleanup runs on the exit path")
	flag.Parse()
	if *server == "" || *workspace == "" {
		fail("usage", fmt.Errorf("-server and -workspace are required"))
	}
	// Cleanup is deferred for the success path as well as driven by fail() for the
	// error path. Both halves are required: os.Exit skips deferred functions, and a
	// plain defer would never run when a check fails - the first version of this
	// driver left its chmod 000 fixture on the target exactly because it only had
	// one of the two.
	defer runCleanups()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	fixture, err := buildFixture(*workspace)
	if err != nil {
		fail("build the fixture tree", err)
	}
	if *simulateFailure {
		// Proves the exit path cleans up: a verification driver that leaves an
		// unreadable directory behind on failure is worse than one that is never run.
		fail("simulated failure (-simulate-failure)", fmt.Errorf("deliberate, to exercise cleanup"))
	}

	run := serve(ctx, *server, *workspace, *prefix)
	onCleanup(func() { _ = run.session.Close() })

	tools, err := run.session.ListTools(ctx, nil)
	if err != nil {
		fail("tools/list", err)
	}
	lsName := *prefix + "__list_dir"
	found := false
	for _, tool := range tools.Tools {
		if tool.Name == lsName {
			found = true
		}
	}
	if !found {
		fail("list_dir is advertised", fmt.Errorf("tool %q missing from %d advertised tools", lsName, len(tools.Tools)))
	}
	fmt.Printf("PASS list_dir advertised by the local-backend server (%d tools under prefix %q)\n", len(tools.Tools), *prefix)

	// The matrix: every depth and limit pair, compared byte for byte against the
	// reference walk of the same disk.
	rel := filepath.ToSlash(strings.TrimPrefix(fixture, *workspace+"/"))
	for _, target := range []string{rel, rel + "/m", rel + "/m/deep"} {
		abs := filepath.Join(*workspace, filepath.FromSlash(target))
		// depth 0 is not reachable through the server: the tool schema declares a
		// minimum of 1 and the handler rejects anything below it. The core's own
		// clamp for maxDepth < 1 is exercised by the unit differential, which calls
		// ReadTreeVia directly.
		for _, depth := range []int{1, 2, 3, 5, 10} {
			for _, limit := range []int{1, 2, 3, 7, 200} {
				name := fmt.Sprintf("ls %s depth=%d limit=%d", target, depth, limit)
				got, callErr := run.call(ctx, lsName, map[string]interface{}{"path": target, "depth": depth, "limit": limit})
				if callErr != nil {
					fail(name, callErr)
				}
				wantListing, wantErr := referenceTree(abs, depth, limit)
				if wantErr != nil {
					fail(name+" (reference walk)", wantErr)
				}
				want, _, _ := lscore.RenderTree(target, wantListing, lscore.OutputBudgetBytes)
				if got != want {
					fail(name, fmt.Errorf("server answered:\n%s\nreference answered:\n%s", firstChars(got, 900), firstChars(want, 900)))
				}
			}
		}
		fmt.Printf("PASS %s: %d depth/limit answers match the pre-seam walk byte for byte\n", target, 25)
	}

	// The unreadable subdirectory: one hole must not fail the listing, and its
	// contents must not appear. This is the case Windows could not construct.
	lockedListing, lockedErr := referenceTree(filepath.Join(fixture), 3, 200)
	if lockedErr != nil {
		fail("reference walk over the locked tree", lockedErr)
	}
	got, callErr := run.call(ctx, lsName, map[string]interface{}{"path": rel, "depth": 3, "limit": 200})
	if callErr != nil {
		fail("ls over a tree containing an unreadable directory", callErr)
	}
	if !strings.Contains(got, "locked/") {
		fail("denied directory is still listed", fmt.Errorf("output lacks it:\n%s", firstChars(got, 600)))
	}
	if strings.Contains(got, "locked/secret.txt") {
		fail("denied directory contents stayed off the listing", fmt.Errorf("they leaked:\n%s", firstChars(got, 600)))
	}
	for _, entry := range lockedListing.Entries {
		if strings.HasPrefix(entry.RelPath, "locked/") {
			fail("reference must not read a denied directory either", fmt.Errorf("reference has %q", entry.RelPath))
		}
	}
	if !strings.Contains(got, "📁 locked/") {
		fail("denied directory keeps its type marker", fmt.Errorf("output:\n%s", firstChars(got, 600)))
	}
	fmt.Printf("PASS unreadable subdirectory: listed as a hole, contents absent, call still succeeded\n")

	// A symlinked root must not be climbed: the authorized tree stops at the link.
	// The handler stats (follows) to decide "is a directory", then the walk lstats
	// (does not follow) to decide "may I descend" - and the answer is no.
	symRoot := rel + "-via-link"
	gotLink, linkErr := run.call(ctx, lsName, map[string]interface{}{"path": symRoot, "depth": 2, "limit": 50})
	if linkErr != nil {
		fail("ls on a symlinked root", linkErr)
	}
	if !strings.Contains(gotLink, "(空目录)") {
		fail("symlinked root is not descended", fmt.Errorf("the walk followed the link:\n%s", firstChars(gotLink, 600)))
	}
	if strings.Contains(gotLink, "a.txt") {
		fail("symlinked root leaks the target's contents", fmt.Errorf("output:\n%s", firstChars(gotLink, 600)))
	}
	fmt.Printf("PASS symlinked root: not followed, empty listing without an error\n")

	// A symlink *inside* the tree is reported as an entry, not descended.
	if !strings.Contains(got, "link-to-m") {
		fail("symlink inside the tree is reported", fmt.Errorf("output:\n%s", firstChars(got, 600)))
	}
	if strings.Contains(got, "link-to-m/") {
		fail("symlink inside the tree is not marked as a directory", fmt.Errorf("it was treated as one:\n%s", firstChars(got, 600)))
	}
	fmt.Printf("PASS symlink inside the tree: reported as an entry and not descended\n")

	// A dangling link is the case where Stat and Lstat disagree; the listing must
	// still describe it rather than fail.
	if !strings.Contains(got, "dangling") {
		fail("dangling link is reported", fmt.Errorf("output:\n%s", firstChars(got, 600)))
	}
	fmt.Printf("PASS dangling link: described as an entry, not swallowed as absence\n")

	// ls on a file is the single-entry shape, and its bytes come from a real stat.
	filePath := rel + "/a.txt"
	info, statErr := os.Stat(filepath.Join(*workspace, filepath.FromSlash(filePath)))
	if statErr != nil {
		fail("stat the file target", statErr)
	}
	gotFile, fileErr := run.call(ctx, lsName, map[string]interface{}{"path": filePath})
	if fileErr != nil {
		fail("ls on a file", fileErr)
	}
	wantFile := fmt.Sprintf("%s (%d bytes, %s)", filePath, info.Size(), info.Mode().String())
	if gotFile != wantFile {
		fail("ls on a file", fmt.Errorf("got %q, want %q", firstChars(gotFile, 200), wantFile))
	}
	fmt.Printf("PASS ls on a file: one-entry shape from a real stat (%d bytes)\n", info.Size())

	// A missing path is a refusal, not an empty listing - the seam version returns
	// the error instead of the old walk's silent swallow, and ls reaches it through
	// the handler's own existence check.
	_, missingErr := run.call(ctx, lsName, map[string]interface{}{"path": rel + "/no-such-dir"})
	if missingErr == nil {
		fail("ls on a missing path", fmt.Errorf("it succeeded; a caller cannot tell empty from gone"))
	}
	fmt.Printf("PASS ls on a missing path refuses: %v\n", firstChars(missingErr.Error(), 120))

	fmt.Println("ALL LS CHECKS PASSED")
}

// referenceTree is the traversal that lived in lscore before the seam landed,
// copied verbatim so the expectation is the old implementation's answer and not a
// restatement of the new one.
func referenceTree(path string, maxDepth, limit int) (lscore.TreeListing, error) {
	if maxDepth < 1 {
		maxDepth = 1
	}
	if maxDepth > lscore.MaxDepth {
		maxDepth = lscore.MaxDepth
	}
	if limit < 1 {
		limit = lscore.DefaultTreeLimit
	}
	listing := lscore.TreeListing{}
	walkErr := filepath.WalkDir(path, func(walkPath string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		relPath, relErr := filepath.Rel(path, walkPath)
		if relErr != nil {
			relPath = walkPath
		}
		if relPath == "." {
			return nil
		}
		normalized := filepath.ToSlash(relPath)
		depth := strings.Count(normalized, "/")
		if depth >= maxDepth {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		item := lscore.TreeEntry{RelPath: normalized, Depth: depth, Dir: entry.IsDir()}
		listing.Entries = append(listing.Entries, item)
		if item.Dir {
			listing.Dirs++
		} else {
			listing.Files++
		}
		if len(listing.Entries) >= limit {
			listing.Truncated = true
			listing.LimitHit = true
			return filepath.SkipAll
		}
		return nil
	})
	if walkErr != nil {
		return lscore.TreeListing{}, walkErr
	}
	sort.Slice(listing.Entries, func(i, j int) bool {
		if listing.Entries[i].RelPath == listing.Entries[j].RelPath {
			if listing.Entries[i].Dir == listing.Entries[j].Dir {
				return listing.Entries[i].Depth < listing.Entries[j].Depth
			}
			return listing.Entries[i].Dir && !listing.Entries[j].Dir
		}
		return listing.Entries[i].RelPath < listing.Entries[j].RelPath
	})
	listing.Total = len(listing.Entries)
	return listing, nil
}

func buildFixture(workspace string) (string, error) {
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		return "", err
	}
	fixture, err := os.MkdirTemp(workspace, "ls-live-")
	if err != nil {
		return "", err
	}
	// Registered here rather than in the caller: a fixture that fails halfway must
	// still be cleaned, and the workspace itself is left alone because the operator
	// pointed us at it - emptying a directory we did not create is not our call.
	onCleanup(func() {
		_ = os.Chmod(filepath.Join(fixture, "locked"), 0o755)
		_ = os.RemoveAll(fixture)
		_ = os.Remove(fixture + "-via-link")
	})
	mkdir := func(rel string, mode os.FileMode) error {
		return os.MkdirAll(filepath.Join(fixture, filepath.FromSlash(rel)), mode)
	}
	touch := func(rel string) error {
		path := filepath.Join(fixture, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		return os.WriteFile(path, []byte("payload\n"), 0o644)
	}
	for _, dir := range []string{"m", "m/deep", "z", "empty"} {
		if err := mkdir(dir, 0o755); err != nil {
			return "", err
		}
	}
	if err := mkdir("locked", 0o755); err != nil {
		return "", err
	}
	for _, file := range []string{"A-caps.txt", "a.txt", "m/b.txt", "m/deep/c.txt", "m/deep/z.txt", "z/last.txt", "locked/secret.txt"} {
		if err := touch(file); err != nil {
			return "", err
		}
	}
	// Symlinks: the interesting cases. The link to a directory must not be climbed,
	// and the dangling one must still be described.
	if err := os.Symlink(filepath.Join(fixture, "m"), filepath.Join(fixture, "link-to-m")); err != nil {
		return "", fmt.Errorf("the target must allow symlinks for this check: %w", err)
	}
	if err := os.Symlink(filepath.Join(fixture, "nowhere"), filepath.Join(fixture, "dangling")); err != nil {
		return "", fmt.Errorf("the target must allow symlinks for this check: %w", err)
	}
	if err := os.Symlink(fixture, fixture+"-via-link"); err != nil {
		return "", fmt.Errorf("the target must allow symlinks for this check: %w", err)
	}
	// Now the directory becomes unreadable for this user. Doing it last means the
	// fixtures above are already in place, and the deferred cleanup restores it.
	if err := os.Chmod(filepath.Join(fixture, "locked"), 0o000); err != nil {
		return "", fmt.Errorf("the target must run as an unprivileged user: %w", err)
	}
	if uid := os.Geteuid(); uid == 0 {
		return "", fmt.Errorf("refusing to run as root: a chmod 000 directory is not a barrier for uid 0")
	}
	return fixture, nil
}

type lsRun struct {
	cmd     *exec.Cmd
	session *mcp.ClientSession
}

func serve(ctx context.Context, binary, workspace, prefix string) *lsRun {
	args := []string{
		"serve", "--stdio", "--backend", "local",
		"--workspace", workspace,
		"--profile", "standard",
		"--tool-prefix", prefix,
	}
	cmd := exec.Command(binary, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		fail("stdin pipe", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		fail("stdout pipe", err)
	}
	stderr := &strings.Builder{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		fail("start the local-backend server", err)
	}
	// Registered before the connect attempt: if the MCP handshake fails, fail()
	// exits the process and a deferred close would never run - leaving a server
	// child alive on the machine that was only supposed to be measured.
	onCleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
	})
	client := mcp.NewClient(&mcp.Implementation{Name: "ls-live-verify", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, &mcp.IOTransport{
		Reader: io.NopCloser(stdout),
		Writer: nopWriteCloser{stdin},
	}, nil)
	if err != nil {
		fail("mcp connect", fmt.Errorf("%w (server stderr: %s)", err, stderr.String()))
	}
	return &lsRun{cmd: cmd, session: session}
}

func (r *lsRun) call(ctx context.Context, tool string, args map[string]interface{}) (string, error) {
	result, err := r.session.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		return "", fmt.Errorf("%w", err)
	}
	text := textOf(result)
	if result.IsError {
		return text, fmt.Errorf("%s", strings.TrimSpace(text))
	}
	return text, nil
}

func textOf(result *mcp.CallToolResult) string {
	if result == nil {
		return ""
	}
	var parts []string
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			parts = append(parts, text.Text)
		}
	}
	return strings.Join(parts, "")
}

func firstChars(text string, count int) string {
	if len(text) <= count {
		return text
	}
	return text[:count] + "..."
}
