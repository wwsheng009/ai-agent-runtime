package remotetools_test

// The M2 file surface: every migrated tool must work through the shared server
// (profile gating, prefix, audit) while doing its I/O on the target, and the
// mutation tools must hold the jail line the bridge established — protected
// roots refuse, and a move destination is checked as hard as its source.

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/wwsheng009/ai-agent-runtime/internal/remotetools"
)

// connectEnvDServerWithProfile starts one envd server at the given tier and
// returns a live MCP session against it.
func connectEnvDServerWithProfile(t *testing.T, stub *envdStub, profile remotetools.Profile) (*mcp.ClientSession, context.Context, *remotetools.Server) {
	t.Helper()
	cfg := newEnvDConfig(stub.server.URL)
	cfg.Profile = profile
	server, err := remotetools.NewServer(cfg, "envd")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { server.Close() })
	session, ctx := connectEnvDServer(t, server)
	return session, ctx, server
}

func callEnvDExpectError(t *testing.T, ctx context.Context, session *mcp.ClientSession, name string, args map[string]interface{}) string {
	t.Helper()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	if !result.IsError {
		t.Fatalf("%s succeeded, want a refusal: %s", name, textContent(result))
	}
	return textContent(result)
}

func TestEnvDLsListsOneLevel(t *testing.T) {
	stub := newEnvdStub(t, map[string]string{
		"/srv/app/README.md":     "hello\n",
		"/srv/app/src/main.go":   "package main\n",
		"/srv/app/src/deep/x.go": "package deep\n",
	}, nil)

	session, ctx, _ := connectEnvDServerWithProfile(t, stub, remotetools.ProfileReadonly)
	got := callTool(t, ctx, session, "remote_envd__ls", map[string]interface{}{"path": "."})
	if !strings.Contains(got, "README.md") {
		t.Fatalf("ls missing the direct child: %s", got)
	}
	// A subdirectory is an entry like any other: a listing that skipped it would
	// make every walk blind to the nested tree below it.
	if !strings.Contains(got, "src/") {
		t.Fatalf("ls missing the subdirectory entry: %s", got)
	}
	// One level only: nested entries belong to a deeper call, not this listing.
	if strings.Contains(got, "main.go") || strings.Contains(got, "deep") {
		t.Fatalf("ls leaked deeper levels: %s", got)
	}

	// One level down through the per-call workdir, which the prefix wrapper adds
	// to every path-taking tool: the listing anchors there, not at the root.
	anchored := callTool(t, ctx, session, "remote_envd__ls", map[string]interface{}{"path": ".", "workdir": "src"})
	if !strings.Contains(anchored, "main.go") {
		t.Fatalf("workdir listing missed the anchored child: %s", anchored)
	}
	if strings.Contains(anchored, "README.md") {
		t.Fatalf("workdir was ignored; the listing still starts at the workspace root: %s", anchored)
	}
}

func TestEnvDGlobWalksTheTargetThroughTheSeam(t *testing.T) {
	stub := newEnvdStub(t, map[string]string{
		"/srv/app/src/main.go": "package main\n",
		"/srv/app/top.go":      "package top\n",
		"/srv/app/README.md":   "hello\n",
	}, nil)
	session, ctx, _ := connectEnvDServerWithProfile(t, stub, remotetools.ProfileReadonly)

	// The shared glob semantics, converged with the local tool: `**/*.go` reaches
	// into subdirectories...
	got := callTool(t, ctx, session, "remote_envd__glob", map[string]interface{}{
		"path": ".", "pattern": "**/*.go",
	})
	// The shared render, relative to the search root, and reached by walking the
	// target through the accessor - no remote find runs any more.
	if !strings.Contains(got, "src/main.go") {
		t.Fatalf("recursive glob missed the nested .go file: %s", got)
	}
	if strings.Contains(got, "README.md") {
		t.Fatalf("glob matched outside the pattern: %s", got)
	}

	// ...while a bare `*.go` stays at the search root's first level, exactly like
	// the local tool. (The retired find matched by basename at any depth; the
	// merge converged on the local rule, so callers reaching into subdirectories
	// spell it `**/*.go`.)
	shallow := callTool(t, ctx, session, "remote_envd__glob", map[string]interface{}{
		"path": ".", "pattern": "*.go",
	})
	// Exact, not substring: labels must stay workspace-relative, and an absolute
	// prefix would still "contain" the relative path.
	if strings.TrimSpace(shallow) != "top.go" {
		t.Fatalf("bare glob = %q, want the workspace-relative label %q", shallow, "top.go")
	}
	if strings.Contains(shallow, "src/main.go") {
		t.Fatalf("bare *.go must stay at depth 1 on this transport too: %s", shallow)
	}

	// The jail still applies to the search root.
	refused := callEnvDExpectError(t, ctx, session, "remote_envd__glob", map[string]interface{}{
		"path": "../etc", "pattern": "passwd",
	})
	if !strings.Contains(refused, "outside the workspace") {
		t.Fatalf("glob escape text = %q", refused)
	}
}

// TestEnvDGrepDeclinesCapabilitiesTheTargetCannotProvide pins the capability
// direction of the swap: the retired script silently ignored parameters it did
// not understand, which turned "I asked for this guarantee" into "you got
// something else". The shared core refuses instead, and the message names the
// missing capability together with the way out.
func TestEnvDGrepDeclinesCapabilitiesTheTargetCannotProvide(t *testing.T) {
	stub := newEnvdStub(t, map[string]string{"/srv/app/README.md": "hello envd\n"}, nil)
	session, ctx, _ := connectEnvDServerWithProfile(t, stub, remotetools.ProfileReadonly)

	// envd's wire carries no device id, so the filesystem boundary cannot be
	// known: inferring it from path shape would cross a mount the caller excluded.
	refused := callEnvDExpectError(t, ctx, session, "remote_envd__grep", map[string]interface{}{
		"path": ".", "pattern": "hello", "one_file_system": true,
	})
	if !strings.Contains(refused, "one_file_system") || !strings.Contains(refused, "FilesystemIdentifier") {
		t.Fatalf("one_file_system refusal = %q", refused)
	}

	// Access and creation times are not on the wire either: no order may be
	// invented from this machine's clock.
	refusedSort := callEnvDExpectError(t, ctx, session, "remote_envd__grep", map[string]interface{}{
		"path": ".", "pattern": "hello", "sort": "accessed",
	})
	if !strings.Contains(refusedSort, "sort=accessed") {
		t.Fatalf("sort=accessed refusal = %q", refusedSort)
	}

	// The same call without the unanswerable guarantee still works: the refusal
	// is scoped to the parameter, not the tool.
	ok := callTool(t, ctx, session, "remote_envd__grep", map[string]interface{}{
		"path": ".", "pattern": "hello",
	})
	if !strings.Contains(ok, "hello envd") {
		t.Fatalf("dropping one_file_system must still search: %q", ok)
	}
}

// TestEnvDGrepSkipsTheTargetUsersUnreadableFiles pins the gate the live target
// caught missing: envd's /files endpoint reads as the daemon process (measured:
// root), so without the target-user check a search through the shared kernel would
// return the contents of a file remote_x__shell cannot open. Measured on a live
// 0.2.11 before envdGuardedAccess existed: a 0600 root file inside the workspace
// came back through grep while view of the same file was already refused.
func TestEnvDGrepSkipsTheTargetUsersUnreadableFiles(t *testing.T) {
	stub := newEnvdStub(t, map[string]string{
		"/srv/app/readable.txt": "needle readable\n",
		"/srv/app/canary.txt":   "needle secret\n",
	}, nil)
	// The gate's fast path only probes when the mode says denial is possible; the
	// fixture must model that, or the test would pass without asking the kernel.
	stub.pinMode("/srv/app/canary.txt", 0o600)
	stub.markUnreadable("/srv/app/canary.txt")
	session, ctx, _ := connectEnvDServerWithProfile(t, stub, remotetools.ProfileReadonly)

	got := callTool(t, ctx, session, "remote_envd__grep", map[string]interface{}{
		"path": ".", "pattern": "needle",
	})
	if !strings.Contains(got, "readable.txt") {
		t.Fatalf("the readable file was not searched: %s", got)
	}
	if strings.Contains(got, "secret") || strings.Contains(got, "canary.txt") {
		t.Fatalf("the search served a file the target user cannot open: %s", got)
	}
	if stub.readableChecks.Load() == 0 {
		t.Fatal("the read gate never asked the target kernel")
	}

	// Control: the bespoke view of the same file is refused by the same verdict
	// through an independent path, so this test cannot pass merely because the file
	// was absent.
	refused := callEnvDExpectError(t, ctx, session, "remote_envd__view", map[string]interface{}{"file_path": "canary.txt"})
	if !strings.Contains(refused, "permission denied") {
		t.Fatalf("view refusal = %q", refused)
	}
}

func TestEnvDWriteThenViewRoundTrip(t *testing.T) {
	stub := newEnvdStub(t, map[string]string{}, nil)
	session, ctx, _ := connectEnvDServerWithProfile(t, stub, remotetools.ProfileStandard)

	// The shared contract: write does not create directories, mkdir does.
	callTool(t, ctx, session, "remote_envd__mkdir", map[string]interface{}{"path": "sub"})
	wrote := callTool(t, ctx, session, "remote_envd__write", map[string]interface{}{
		"path": "sub/hello.txt", "content": "abc",
	})
	// The local spelling of write's reply is shared across transports.
	if !strings.Contains(wrote, "Created sub/hello.txt (3 bytes).") {
		t.Fatalf("write result = %q, want the shared Created wording", wrote)
	}
	if _, ok := stub.content("/srv/app/sub/hello.txt"); !ok {
		t.Fatal("the stub never received the write")
	}
	got := callTool(t, ctx, session, "remote_envd__view", map[string]interface{}{"file_path": "sub/hello.txt"})
	// Numbered like the local surface. The previous expectation here was `abc`,
	// which means this test had been pinning the drift itself: two surfaces, one
	// tool name, two output shapes.
	if got != "1: abc" {
		t.Fatalf("read back = %q, want the shared numbered render %q", got, "1: abc")
	}
}

// Same-name tools must mean the same thing on both transports: the local surface
// refuses a missing parent instead of silently creating directories.
func TestEnvDWriteRefusesMissingParent(t *testing.T) {
	stub := newEnvdStub(t, map[string]string{}, nil)
	session, ctx, _ := connectEnvDServerWithProfile(t, stub, remotetools.ProfileStandard)

	before := stub.processCalls.Load()
	statBefore := stub.statCalls.Load()
	refused := callEnvDExpectError(t, ctx, session, "remote_envd__write", map[string]interface{}{
		"path": "nodir/deep.txt", "content": "x",
	})
	if !strings.Contains(refused, "parent directory does not exist") {
		t.Fatalf("missing-parent refusal = %q", refused)
	}
	// Only the jail's realpath and the parent probe may have run; the write script
	// must not have, and nothing may have landed.
	if after := stub.processCalls.Load(); after != before+1 {
		t.Fatalf("expected only the jail realpath call, got %d -> %d", before, after)
	}
	if after := stub.statCalls.Load(); after < statBefore {
		t.Fatalf("stat calls went backwards: %d -> %d", statBefore, after)
	}
	if _, ok := stub.content("/srv/app/nodir/deep.txt"); ok {
		t.Fatal("the refused write landed anyway")
	}
}

// The native channel lifted the argv ceiling: content no longer travels as one
// command-line argument, so a file larger than 1 MiB is a write, not a refusal.
// The bytes must land unchanged and no process may be spawned to do it.
func TestEnvDWriteSendsLargeContentThroughNativeChannel(t *testing.T) {
	stub := newEnvdStub(t, map[string]string{}, nil)
	session, ctx, _ := connectEnvDServerWithProfile(t, stub, remotetools.ProfileStandard)

	payload := strings.Repeat("a", (1<<20)+1)
	wrote := callTool(t, ctx, session, "remote_envd__write", map[string]interface{}{
		"path": "big.txt", "content": payload,
	})
	if !strings.Contains(wrote, "Created big.txt") || !strings.Contains(wrote, "1048577 bytes") {
		t.Fatalf("large write = %q", wrote)
	}
	landed, ok := stub.content("/srv/app/big.txt")
	if !ok {
		t.Fatal("the large write did not land")
	}
	if landed != payload {
		t.Fatalf("content changed in transit: wrote %d bytes, stored %d", len(payload), len(landed))
	}
	if stub.catCalls.Load() != 0 {
		t.Fatalf("process calls = %d, want the native channel to carry the bytes", stub.catCalls.Load())
	}
	// One documented ceiling, enforced once: with the channel available the argv
	// limit must not appear at all, and the tool must not have run a shell command
	// to test that.
	if calls := stub.writableChecks.Load(); calls != 0 {
		t.Fatalf("write gate probes = %d, want the owned-workspace fast path to answer without a spawn", calls)
	}
}

// On the fallback the old guarantee still holds, and it still holds BEFORE the
// command runs: a refusal must not leave a half-written file behind.
func TestEnvDWriteRejectsOversizedContentOnProcessFallback(t *testing.T) {
	stub := newEnvdStub(t, map[string]string{}, nil)
	stub.disableFiles()
	session, ctx, _ := connectEnvDServerWithProfile(t, stub, remotetools.ProfileStandard)

	before := stub.processCalls.Load()
	refused := callEnvDExpectError(t, ctx, session, "remote_envd__write", map[string]interface{}{
		"path": "big.txt", "content": strings.Repeat("a", (1<<20)+1),
	})
	if !strings.Contains(refused, "write limit") {
		t.Fatalf("oversized write text = %q", refused)
	}
	if _, ok := stub.content("/srv/app/big.txt"); ok {
		t.Fatal("the refused write landed anyway")
	}
	if after := stub.processCalls.Load(); after != before {
		t.Fatalf("oversized write still reached the target: %d -> %d", before, after)
	}
}

func TestEnvDMkdirReportsExistingDirectoryRatherThanFailing(t *testing.T) {
	stub := newEnvdStub(t, map[string]string{}, nil)
	session, ctx, _ := connectEnvDServerWithProfile(t, stub, remotetools.ProfileStandard)

	first := callTool(t, ctx, session, "remote_envd__mkdir", map[string]interface{}{"path": "build"})
	if !strings.Contains(first, "created /srv/app/build") {
		t.Fatalf("first mkdir = %q", first)
	}
	second := callTool(t, ctx, session, "remote_envd__mkdir", map[string]interface{}{"path": "build"})
	if !strings.Contains(second, "already exists") {
		t.Fatalf("second mkdir = %q, want the already-exists report", second)
	}
	_, _, created := stub.history()
	if len(created) != 1 {
		t.Fatalf("created = %v, want exactly one new directory", created)
	}
}

func TestEnvDRemoveRefusesProtectedRoot(t *testing.T) {
	stub := newEnvdStub(t, map[string]string{"/srv/app/keep.txt": "x\n"}, nil)
	session, ctx, _ := connectEnvDServerWithProfile(t, stub, remotetools.ProfileStandard)

	refused := callEnvDExpectError(t, ctx, session, "remote_envd__remove", map[string]interface{}{"path": "."})
	if !strings.Contains(refused, "protected root") {
		t.Fatalf("protected root refusal = %q", refused)
	}
	removed, _, _ := stub.history()
	if len(removed) != 0 {
		t.Fatalf("Remove RPC happened anyway: %v", removed)
	}

	gone := callTool(t, ctx, session, "remote_envd__remove", map[string]interface{}{"path": "keep.txt"})
	if !strings.Contains(gone, "removed /srv/app/keep.txt") {
		t.Fatalf("remove = %q", gone)
	}
}

func TestEnvDMoveChecksBothEnds(t *testing.T) {
	stub := newEnvdStub(t, map[string]string{"/srv/app/a.txt": "body\n"}, nil)
	session, ctx, _ := connectEnvDServerWithProfile(t, stub, remotetools.ProfileStandard)

	// A destination outside the jail must fail even though the source is fine.
	refused := callEnvDExpectError(t, ctx, session, "remote_envd__move", map[string]interface{}{
		"source": "a.txt", "destination": "/etc/passwd",
	})
	if !strings.Contains(refused, "destination") || !strings.Contains(refused, "outside the workspace") {
		t.Fatalf("move destination refusal = %q", refused)
	}
	if _, moved, _ := stub.history(); len(moved) != 0 {
		t.Fatalf("Move RPC happened anyway: %v", moved)
	}

	done := callTool(t, ctx, session, "remote_envd__move", map[string]interface{}{
		"source": "a.txt", "destination": "b.txt",
	})
	if !strings.Contains(done, "moved /srv/app/a.txt -> /srv/app/b.txt") {
		t.Fatalf("move = %q", done)
	}
	if _, ok := stub.content("/srv/app/b.txt"); !ok {
		t.Fatal("the stub never saw the destination")
	}
}

func TestEnvDHostMetricsDegradesMissingFields(t *testing.T) {
	stub := newEnvdStub(t, map[string]string{}, nil)
	session, ctx, _ := connectEnvDServerWithProfile(t, stub, remotetools.ProfileReadonly)

	got := callTool(t, ctx, session, "remote_envd__host_metrics", map[string]interface{}{})
	if !strings.Contains(got, "cpus: 4") {
		t.Fatalf("metrics missing cpu count: %s", got)
	}
	// The stub /metrics body has no `available`; 0 MiB would read as "no memory
	// free" rather than "not reported".
	if !strings.Contains(got, "memory available: n/a") {
		t.Fatalf("metrics should degrade a missing field to n/a: %s", got)
	}
}
