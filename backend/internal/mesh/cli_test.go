package mesh

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// runCLI executes one command line with captured stdio.
func runCLI(t *testing.T, cli *CLI, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	local := *cli
	local.Stdout = &stdout
	local.Stderr = &stderr
	code := local.Run(args)
	return code, stdout.String(), stderr.String()
}

// testCLI wires a CLI to a temp mesh layout and a fake clock.
func testCLI(paths Paths, clock *fakeClock) *CLI {
	return &CLI{Version: "test-version", Now: clock.Now, Paths: &paths}
}

// testCLIPaths is testMeshPaths plus the directory materialisation the CLI
// relies on (ResolvePaths only computes names).
func testCLIPaths(t *testing.T) Paths {
	t.Helper()
	paths := testMeshPaths(t)
	if err := paths.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}
	return paths
}

// seedLiveNode writes a live record (own pid, fresh heartbeat) with an endpoint
// and a write token, the way a chat process publishes itself.
func seedLiveNode(t *testing.T, paths Paths, now time.Time, nodeID, sessionID, workspace string) NodeRecord {
	t.Helper()
	record := viewNodeRecord(nodeID, os.Getpid(), now.Add(-5*time.Second), sessionID, workspace)
	record.Endpoint = &EndpointInfo{
		Scheme:      "http",
		Host:        "127.0.0.1",
		Port:        55124,
		Loopback:    true,
		BaseURL:     "http://127.0.0.1:55124",
		WebBaseURL:  "http://127.0.0.1:55124/web",
		ManifestURL: "http://127.0.0.1:55124/debug/endpoints",
	}
	record.Auth = &AuthInfo{Mode: "loopback-dev", Required: true, Token: "0f3a-secret-token", TokenSource: "random"}
	writeViewNode(t, paths, record)
	return record
}

func decodeJSON[T any](t *testing.T, payload string) T {
	t.Helper()
	var out T
	if err := json.Unmarshal([]byte(payload), &out); err != nil {
		t.Fatalf("decode JSON: %v\n%s", err, payload)
	}
	return out
}

// ---------------------------------------------------------------------------
// ls
// ---------------------------------------------------------------------------

func TestCLILsJSONSchemaAndRedaction(t *testing.T) {
	paths := testCLIPaths(t)
	clock := newFakeClock()
	now := clock.Now()
	cli := testCLI(paths, clock)

	seedLiveNode(t, paths, now, "node-1000-20260924T100000Z", "session_live", `E:\ws\a`)
	dead := viewNodeRecord("node-2000-20260924T090000Z", deadPID(t), now.Add(-time.Hour), "session_dead", `E:\ws\b`)
	writeViewNode(t, paths, dead)

	code, stdout, stderr := runCLI(t, cli, "ls", "--json")
	if code != ExitOK {
		t.Fatalf("ls --json exit = %d (stderr %q)", code, stderr)
	}
	view := decodeJSON[MeshView](t, stdout)
	if view.SchemaVersion != SchemaVersion {
		t.Fatalf("schema_version = %d, want %d", view.SchemaVersion, SchemaVersion)
	}
	if view.Counts.Live != 1 || view.Counts.Stale != 1 {
		t.Fatalf("counts = %+v, want live=1 stale=1", view.Counts)
	}
	// 默认只列在线：stale 档案不进 nodes[]，但 counts 仍是全量普查。
	if len(view.Nodes) != 1 {
		t.Fatalf("default ls listed %d nodes, want 1 (online only)", len(view.Nodes))
	}
	if view.Filter == nil || view.Filter.State != FilterStateLive {
		t.Fatalf("default filter echo = %+v, want state=live", view.Filter)
	}

	code, stdout, stderr = runCLI(t, cli, "ls", "-a", "--json")
	if code != ExitOK {
		t.Fatalf("ls -a --json exit = %d (stderr %q)", code, stderr)
	}
	view = decodeJSON[MeshView](t, stdout)
	if len(view.Nodes) != 2 {
		t.Fatalf("ls -a nodes = %d, want 2", len(view.Nodes))
	}
	if view.Filter != nil {
		t.Fatalf("ls -a filter echo = %+v, want none", view.Filter)
	}
	live := nodeByName(t, view, "node-1000-20260924T100000Z")
	if live.Auth == nil || live.Auth.TokenHint != "0f3a…" {
		t.Fatalf("token hint = %+v, want 0f3a…", live.Auth)
	}
	if live.Auth.Token != "" {
		t.Fatalf("ls must never reveal the token, got %q", live.Auth.Token)
	}
	if live.Endpoint == nil || live.Endpoint.BaseURL != "http://127.0.0.1:55124" {
		t.Fatalf("endpoint = %+v", live.Endpoint)
	}
	if live.Ownership != OwnershipPeer {
		t.Fatalf("ownership = %q, want peer (the CLI is not a node)", live.Ownership)
	}
	if strings.Contains(stdout, "0f3a-secret-token") {
		t.Fatalf("raw token leaked into ls output:\n%s", stdout)
	}
}

func TestCLILsFiltersNeverShrinkCounts(t *testing.T) {
	paths := testCLIPaths(t)
	clock := newFakeClock()
	now := clock.Now()
	cli := testCLI(paths, clock)

	seedLiveNode(t, paths, now, "node-1000-20260924T100000Z", "session_a", `E:\ws\a`)
	seedLiveNode(t, paths, now, "node-1001-20260924T100001Z", "session_b", `E:\ws\b`)
	writeViewNode(t, paths, viewNodeRecord("node-2000-20260924T090000Z", deadPID(t), now.Add(-time.Hour), "session_c", `E:\ws\a`))

	code, stdout, stderr := runCLI(t, cli, "ls", "--json")
	if code != ExitOK {
		t.Fatalf("ls exit = %d (stderr %q)", code, stderr)
	}
	view := decodeJSON[MeshView](t, stdout)
	if len(view.Nodes) != 2 {
		t.Fatalf("default ls listed %d nodes, want 2 (stale hidden)", len(view.Nodes))
	}
	if view.Counts.Live != 2 || view.Counts.Stale != 1 {
		t.Fatalf("counts = %+v, want the full census (live=2 stale=1)", view.Counts)
	}

	code, stdout, stderr = runCLI(t, cli, "ls", "-a", "--json")
	if code != ExitOK {
		t.Fatalf("ls -a exit = %d (stderr %q)", code, stderr)
	}
	view = decodeJSON[MeshView](t, stdout)
	if len(view.Nodes) != 3 {
		t.Fatalf("ls -a listed %d nodes, want 3", len(view.Nodes))
	}

	code, stdout, stderr = runCLI(t, cli, "ls", "--live", "--json")
	if code != ExitOK {
		t.Fatalf("ls --live exit = %d (stderr %q)", code, stderr)
	}
	view = decodeJSON[MeshView](t, stdout)
	if len(view.Nodes) != 2 {
		t.Fatalf("--live listed %d nodes, want 2 (stale hidden)", len(view.Nodes))
	}
	if view.Counts.Live != 2 || view.Counts.Stale != 1 {
		t.Fatalf("counts = %+v, want the full census (live=2 stale=1)", view.Counts)
	}
	if view.Filter == nil || view.Filter.State != FilterStateLive {
		t.Fatalf("filter echo = %+v, want state=live", view.Filter)
	}

	code, stdout, stderr = runCLI(t, cli, "ls", "-a", "--workspace", `E:\ws\a`, "--json")
	if code != ExitOK {
		t.Fatalf("ls -a --workspace exit = %d (stderr %q)", code, stderr)
	}
	view = decodeJSON[MeshView](t, stdout)
	if len(view.Nodes) != 2 {
		t.Fatalf("ls -a --workspace listed %d nodes, want 2 (live + stale in E:\\ws\\a)", len(view.Nodes))
	}
	if view.Counts.Live != 2 || view.Counts.Stale != 1 {
		t.Fatalf("counts = %+v, want the full census", view.Counts)
	}
	if view.Filter == nil || len(view.Filter.Workspace) != 1 {
		t.Fatalf("filter echo = %+v", view.Filter)
	}

	// 默认 + --workspace：工作区过滤仍叠加在「只看在线」之上。
	code, stdout, stderr = runCLI(t, cli, "ls", "--workspace", `E:\ws\a`, "--json")
	if code != ExitOK {
		t.Fatalf("ls --workspace exit = %d (stderr %q)", code, stderr)
	}
	view = decodeJSON[MeshView](t, stdout)
	if len(view.Nodes) != 1 {
		t.Fatalf("ls --workspace listed %d nodes, want 1 (online only in E:\\ws\\a)", len(view.Nodes))
	}
	if view.Filter == nil || view.Filter.State != FilterStateLive || len(view.Filter.Workspace) != 1 {
		t.Fatalf("filter echo = %+v, want state=live + workspace", view.Filter)
	}
}

func TestCLILsOnlineDefaultAllFlagAndConflict(t *testing.T) {
	paths := testCLIPaths(t)
	clock := newFakeClock()
	now := clock.Now()
	cli := testCLI(paths, clock)

	seedLiveNode(t, paths, now, "node-1000-20260924T100000Z", "session_live", `E:\ws\a`)
	writeViewNode(t, paths, viewNodeRecord("node-2000-20260924T090000Z", deadPID(t), now.Add(-time.Hour), "session_dead", `E:\ws\b`))

	// 人类可读的默认输出：只有在线行，并明说隐藏了多少（counts 仍全量）。
	code, stdout, stderr := runCLI(t, cli, "ls")
	if code != ExitOK {
		t.Fatalf("ls exit = %d (stderr %q)", code, stderr)
	}
	if !strings.Contains(stdout, "node-1000-20260924T100000Z") {
		t.Fatalf("default ls must list the live node:\n%s", stdout)
	}
	if strings.Contains(stdout, "node-2000-20260924T090000Z") {
		t.Fatalf("default ls must hide the stale node:\n%s", stdout)
	}
	if !strings.Contains(stdout, "共 2 个节点") || !strings.Contains(stdout, "已隐藏 1 个节点") {
		t.Fatalf("default ls summary must keep the full census and say what is hidden:\n%s", stdout)
	}

	// --all 与 -a 等价：全量列出，且不再提示隐藏。
	code, stdout, stderr = runCLI(t, cli, "ls", "--all")
	if code != ExitOK {
		t.Fatalf("ls --all exit = %d (stderr %q)", code, stderr)
	}
	if !strings.Contains(stdout, "node-1000-20260924T100000Z") || !strings.Contains(stdout, "node-2000-20260924T090000Z") {
		t.Fatalf("ls --all must list every node:\n%s", stdout)
	}
	if strings.Contains(stdout, "已隐藏") {
		t.Fatalf("ls --all hides nothing and must not claim otherwise:\n%s", stdout)
	}

	// --live 已是默认行为（保留兼容），与 -a 同时出现属于自相矛盾。
	if code, _, _ = runCLI(t, cli, "ls", "--live"); code != ExitOK {
		t.Fatalf("ls --live exit = %d, want %d", code, ExitOK)
	}
	code, _, stderr = runCLI(t, cli, "ls", "-a", "--live")
	if code != ExitUsage || !strings.Contains(stderr, "互斥") {
		t.Fatalf("ls -a --live = %d (stderr %q), want usage error", code, stderr)
	}
}

func TestCLILsSortAndUsageErrors(t *testing.T) {
	paths := testCLIPaths(t)
	clock := newFakeClock()
	now := clock.Now()
	cli := testCLI(paths, clock)

	seedLiveNode(t, paths, now, "node-1000-20260924T100000Z", "session_b", `E:\ws\b`)
	seedLiveNode(t, paths, now, "node-1001-20260924T100001Z", "session_a", `E:\ws\a`)

	code, stdout, stderr := runCLI(t, cli, "ls", "--sort", "session")
	if code != ExitOK {
		t.Fatalf("ls --sort session exit = %d (stderr %q)", code, stderr)
	}
	if strings.Index(stdout, "session_a") > strings.Index(stdout, "session_b") {
		t.Fatalf("--sort session did not order by session id:\n%s", stdout)
	}
	if !strings.Contains(stdout, "共 2 个节点") {
		t.Fatalf("human summary missing:\n%s", stdout)
	}

	code, stdout, stderr = runCLI(t, cli, "ls", "--sort", "bogus")
	if code != ExitUsage {
		t.Fatalf("ls --sort bogus exit = %d, want %d (stdout %q)", code, ExitUsage, stdout)
	}
	if !strings.Contains(stderr, "--sort") {
		t.Fatalf("stderr should explain --sort: %q", stderr)
	}

	if code, _, _ = runCLI(t, cli, "ls", "extra"); code != ExitUsage {
		t.Fatalf("ls extra exit = %d, want %d", code, ExitUsage)
	}
	if code, _, _ = runCLI(t, cli, "ls", "--nope"); code != ExitUsage {
		t.Fatalf("ls --nope exit = %d, want %d", code, ExitUsage)
	}
}

// ---------------------------------------------------------------------------
// show
// ---------------------------------------------------------------------------

func TestCLIShowResolvesTargetsAndRendersDetails(t *testing.T) {
	paths := testCLIPaths(t)
	clock := newFakeClock()
	now := clock.Now()
	cli := testCLI(paths, clock)

	nodeID := "node-1000-20260924T100000Z"
	sessionID := "session_20260924072950_ltYRU9tG"
	seedLiveNode(t, paths, now, nodeID, sessionID, `E:\ws\a`)
	if err := SaveBinding(paths, SessionBinding{
		SchemaVersion: SchemaVersion,
		SessionID:     sessionID,
		Preferred:     &BindingAddr{Host: "127.0.0.1", Port: 55124},
		LastNodeID:    nodeID,
		UpdatedAt:     now,
	}); err != nil {
		t.Fatalf("SaveBinding: %v", err)
	}
	writeRawLease(t, paths, Lease{
		Purpose:     LeasePurposeSession,
		Key:         sessionID,
		OwnerNodeID: nodeID,
		OwnerPID:    os.Getpid(),
		AcquiredAt:  now.Add(-time.Minute),
		RenewedAt:   now.Add(-time.Second),
		TTLSec:      DefaultLeaseTTLSec,
	})

	// node id prefix
	code, stdout, stderr := runCLI(t, cli, "show", "node-1000")
	if code != ExitOK {
		t.Fatalf("show prefix exit = %d (stderr %q)", code, stderr)
	}
	for _, want := range []string{nodeID, sessionID, "127.0.0.1:55124", "0f3a…", "live"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("show output missing %q:\n%s", want, stdout)
		}
	}

	// pid: and session prefix must resolve to the same node
	for _, ref := range []string{"pid:" + strconv.Itoa(os.Getpid()), "session_20260924072950"} {
		code, stdout, stderr = runCLI(t, cli, "show", ref, "--json")
		if code != ExitOK {
			t.Fatalf("show %s exit = %d (stderr %q)", ref, code, stderr)
		}
		result := decodeJSON[showResult](t, stdout)
		if result.Node == nil || result.Node.NodeID != nodeID {
			t.Fatalf("show %s resolved to %+v", ref, result.Node)
		}
		if result.Node.Binding == nil || result.Node.Binding.Preferred.Port != 55124 {
			t.Fatalf("show %s lost the binding: %+v", ref, result.Node.Binding)
		}
		if len(result.Leases) != 1 || result.Leases[0].Key != sessionID || !result.Leases[0].OwnerAlive {
			t.Fatalf("show %s leases = %+v", ref, result.Leases)
		}
	}

	// unknown target, missing target
	if code, _, stderr = runCLI(t, cli, "show", "does-not-exist"); code != ExitNotFound {
		t.Fatalf("show unknown exit = %d, want %d (stderr %q)", code, ExitNotFound, stderr)
	}
	if code, _, _ = runCLI(t, cli, "show"); code != ExitUsage {
		t.Fatalf("show without target exit = %d, want %d", code, ExitUsage)
	}
}

func TestCLIShowReportsAmbiguousTargets(t *testing.T) {
	paths := testCLIPaths(t)
	clock := newFakeClock()
	now := clock.Now()
	cli := testCLI(paths, clock)

	seedLiveNode(t, paths, now, "node-1000-20260924T100000Z", "session_a", `E:\ws\a`)
	seedLiveNode(t, paths, now, "node-1001-20260924T100001Z", "session_b", `E:\ws\b`)

	code, _, stderr := runCLI(t, cli, "show", "node-100")
	if code != ExitNotFound {
		t.Fatalf("ambiguous target exit = %d, want %d", code, ExitNotFound)
	}
	for _, want := range []string{"无法唯一确定", "node-1000-20260924T100000Z", "node-1001-20260924T100001Z", "pid:"} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("ambiguity message missing %q: %q", want, stderr)
		}
	}
}

// ---------------------------------------------------------------------------
// url
// ---------------------------------------------------------------------------

func TestCLIURLTokenPolicy(t *testing.T) {
	paths := testCLIPaths(t)
	clock := newFakeClock()
	now := clock.Now()
	cli := testCLI(paths, clock)

	nodeID := "node-1000-20260924T100000Z"
	seedLiveNode(t, paths, now, nodeID, "session_a", `E:\ws\a`)
	writeViewNode(t, paths, viewNodeRecord("node-2000-20260924T090000Z", os.Getpid(), now.Add(-5*time.Second), "session_tui", ""))

	// default: no token, web path
	code, stdout, stderr := runCLI(t, cli, "url", nodeID)
	if code != ExitOK {
		t.Fatalf("url exit = %d (stderr %q)", code, stderr)
	}
	if got := strings.TrimSpace(stdout); got != "http://127.0.0.1:55124/web" {
		t.Fatalf("url = %q, want the web base URL without a token", got)
	}
	if strings.Contains(stdout, "token") {
		t.Fatalf("url leaked a token by default: %q", stdout)
	}

	// --path overrides the path
	code, stdout, _ = runCLI(t, cli, "url", nodeID, "--path", "/debug/endpoints")
	if code != ExitOK {
		t.Fatalf("url --path exit = %d", code)
	}
	if got := strings.TrimSpace(stdout); got != "http://127.0.0.1:55124/debug/endpoints" {
		t.Fatalf("url --path = %q", got)
	}

	// --with-token is the explicit reveal path
	code, stdout, _ = runCLI(t, cli, "url", nodeID, "--with-token")
	if code != ExitOK {
		t.Fatalf("url --with-token exit = %d", code)
	}
	if got := strings.TrimSpace(stdout); got != "http://127.0.0.1:55124/web?token=0f3a-secret-token" {
		t.Fatalf("url --with-token = %q", got)
	}

	code, stdout, _ = runCLI(t, cli, "url", nodeID, "--json")
	if code != ExitOK {
		t.Fatalf("url --json exit = %d", code)
	}
	result := decodeJSON[urlResult](t, stdout)
	if result.WithToken || result.TokenSource != "random" || result.NodeID != nodeID {
		t.Fatalf("url --json = %+v", result)
	}

	// a node without an endpoint is unreachable, not "not found"
	code, _, stderr = runCLI(t, cli, "url", "session_tui")
	if code != ExitUnreachable {
		t.Fatalf("url on a TUI node exit = %d, want %d (stderr %q)", code, ExitUnreachable, stderr)
	}
}

// ---------------------------------------------------------------------------
// gc
// ---------------------------------------------------------------------------

func TestCLIGcDryRunMatchesApplyAndKeepsLiveNodes(t *testing.T) {
	paths := testCLIPaths(t)
	clock := newFakeClock()
	now := clock.Now()
	cli := testCLI(paths, clock)
	home := t.TempDir()
	t.Setenv(EnvHome, home)

	// live node: never collected, whatever the flags
	liveID := "node-1000-20260924T100000Z"
	seedLiveNode(t, paths, now, liveID, "session_live", `E:\ws\a`)
	writeRawLease(t, paths, Lease{
		Purpose:     LeasePurposeSession,
		Key:         "session_live",
		OwnerNodeID: liveID,
		OwnerPID:    os.Getpid(),
		AcquiredAt:  now.Add(-time.Minute),
		RenewedAt:   now,
		TTLSec:      DefaultLeaseTTLSec,
	})

	// dead node + its journal (old enough to collect)
	deadID := "node-2000-20260924T090000Z"
	writeViewNode(t, paths, viewNodeRecord(deadID, deadPID(t), now.Add(-time.Hour), "session_dead", `E:\ws\b`))
	journalPath := paths.JournalPath(deadID)
	if err := os.WriteFile(journalPath, []byte(`{"ts":"2026-09-24T09:00:00Z","node_id":"`+deadID+`","seq":1,"kind":"start"}`+"\n"), 0o600); err != nil {
		t.Fatalf("write journal: %v", err)
	}
	old := now.Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(journalPath, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	// expired lease owned by a dead process
	writeRawLease(t, paths, Lease{
		Purpose:     LeasePurposeSession,
		Key:         "session_dead",
		OwnerNodeID: deadID,
		OwnerPID:    deadPID(t),
		AcquiredAt:  now.Add(-time.Hour),
		RenewedAt:   now.Add(-time.Hour),
		TTLSec:      DefaultLeaseTTLSec,
	})

	// legacy directory (M8)
	legacy := filepath.Join(home, "web-ports")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatalf("mkdir legacy: %v", err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "session_x.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("write legacy file: %v", err)
	}

	code, dryRunOut, stderr := runCLI(t, cli, "gc", "--json", "--purge-legacy")
	if code != ExitOK {
		t.Fatalf("gc dry-run exit = %d (stderr %q)", code, stderr)
	}
	dryRun := decodeJSON[gcPlan](t, dryRunOut)
	if !dryRun.DryRun || dryRun.Applied {
		t.Fatalf("dry-run flags = %+v", dryRun)
	}
	kinds := map[string]int{}
	for _, action := range dryRun.Actions {
		kinds[action.Kind]++
	}
	if kinds["node-record"] != 1 || kinds["journal"] != 1 || kinds["lease"] != 1 || kinds["legacy-dir"] != 1 {
		t.Fatalf("plan kinds = %+v (actions %+v)", kinds, dryRun.Actions)
	}
	for _, action := range dryRun.Actions {
		if strings.Contains(action.Path, liveID) {
			t.Fatalf("plan touched the live node: %+v", action)
		}
	}

	code, applyOut, stderr := runCLI(t, cli, "gc", "--json", "--purge-legacy", "--apply")
	if code != ExitOK {
		t.Fatalf("gc --apply exit = %d (stderr %q)", code, stderr)
	}
	applied := decodeJSON[gcPlan](t, applyOut)
	if applied.DryRun || !applied.Applied {
		t.Fatalf("apply flags = %+v", applied)
	}
	if applied.Deleted != len(dryRun.Actions) || len(applied.Errors) != 0 {
		t.Fatalf("apply deleted=%d errors=%v, want %d and none", applied.Deleted, applied.Errors, len(dryRun.Actions))
	}
	dryRunJSON, _ := json.Marshal(dryRun.Actions)
	appliedJSON, _ := json.Marshal(applied.Actions)
	if string(dryRunJSON) != string(appliedJSON) {
		t.Fatalf("dry-run and --apply plans differ:\n%s\n%s", dryRunJSON, appliedJSON)
	}

	// everything planned is gone, the live node is untouched
	if _, err := os.Stat(paths.NodePath(deadID)); !os.IsNotExist(err) {
		t.Fatalf("dead node record still present (err %v)", err)
	}
	if _, err := os.Stat(journalPath); !os.IsNotExist(err) {
		t.Fatalf("dead node journal still present (err %v)", err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("legacy dir still present (err %v)", err)
	}
	if _, err := os.Stat(paths.NodePath(liveID)); err != nil {
		t.Fatalf("live node record disappeared: %v", err)
	}

	// a second run has nothing left to do
	code, secondOut, _ := runCLI(t, cli, "gc", "--json", "--purge-legacy")
	if code != ExitOK {
		t.Fatalf("second gc exit = %d", code)
	}
	if second := decodeJSON[gcPlan](t, secondOut); len(second.Actions) != 0 {
		t.Fatalf("second gc planned %+v, want nothing", second.Actions)
	}
}

func TestCLIGcHonoursStaleTTLAndKeepDays(t *testing.T) {
	paths := testCLIPaths(t)
	clock := newFakeClock()
	now := clock.Now()
	cli := testCLI(paths, clock)
	t.Setenv(EnvHome, t.TempDir())

	// fresh dead node: inside the 10m grace window
	freshID := "node-3000-20260924T095900Z"
	writeViewNode(t, paths, viewNodeRecord(freshID, deadPID(t), now.Add(-time.Minute), "session_fresh", ""))

	code, stdout, _ := runCLI(t, cli, "gc", "--json")
	if code != ExitOK {
		t.Fatalf("gc exit = %d", code)
	}
	if plan := decodeJSON[gcPlan](t, stdout); len(plan.Actions) != 0 {
		t.Fatalf("grace period ignored: %+v", plan.Actions)
	}

	code, stdout, _ = runCLI(t, cli, "gc", "--json", "--stale-ttl", "1s")
	if code != ExitOK {
		t.Fatalf("gc --stale-ttl exit = %d", code)
	}
	plan := decodeJSON[gcPlan](t, stdout)
	if len(plan.Actions) != 1 || plan.Actions[0].Kind != "node-record" {
		t.Fatalf("--stale-ttl 1s plan = %+v", plan.Actions)
	}

	// a journal for a node that never existed survives the default keep window
	orphan := paths.JournalPath("node-9999-20260924T000000Z")
	if err := os.WriteFile(orphan, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write orphan journal: %v", err)
	}
	// mtime 来自真实文件系统时钟，而测试时钟固定在 2026-09-24T10:00:00Z：
	// 不显式钉住 mtime，`--keep-days 0`（age < 0 视为「还没到回收时刻」）就会
	// 随真实 UTC 时刻漂移——真实时间过了 10:00 时该断言必然失败。
	orphanTime := now.Add(-time.Hour)
	if err := os.Chtimes(orphan, orphanTime, orphanTime); err != nil {
		t.Fatalf("chtimes orphan journal: %v", err)
	}
	code, stdout, _ = runCLI(t, cli, "gc", "--json")
	if code != ExitOK {
		t.Fatalf("gc exit = %d", code)
	}
	for _, action := range decodeJSON[gcPlan](t, stdout).Actions {
		if action.Kind == "journal" {
			t.Fatalf("fresh orphan journal collected too early: %+v", action)
		}
	}
	code, stdout, _ = runCLI(t, cli, "gc", "--json", "--keep-days", "0")
	if code != ExitOK {
		t.Fatalf("gc --keep-days 0 exit = %d", code)
	}
	found := false
	for _, action := range decodeJSON[gcPlan](t, stdout).Actions {
		if action.Kind == "journal" && action.Path == orphan {
			found = true
		}
	}
	if !found {
		t.Fatalf("--keep-days 0 did not collect the orphan journal")
	}
}

// ---------------------------------------------------------------------------
// doctor / version / usage
// ---------------------------------------------------------------------------

func TestCLIDoctorHealthyAndConflict(t *testing.T) {
	paths := testCLIPaths(t)
	clock := newFakeClock()
	now := clock.Now()
	cli := testCLI(paths, clock)
	t.Setenv(EnvHome, t.TempDir())

	seedLiveNode(t, paths, now, "node-1000-20260924T100000Z", "session_a", `E:\ws\a`)

	code, stdout, stderr := runCLI(t, cli, "doctor", "--json")
	if code != ExitOK {
		t.Fatalf("doctor exit = %d (stderr %q, stdout %s)", code, stderr, stdout)
	}
	report := decodeJSON[doctorReport](t, stdout)
	if report.Problems != 0 {
		t.Fatalf("healthy mesh reported problems: %+v", report.Checks)
	}
	if len(report.Checks) == 0 {
		t.Fatalf("doctor produced no checks")
	}

	// two live nodes claiming the same session: the double occupancy of §4.2
	seedLiveNode(t, paths, now, "node-1001-20260924T100001Z", "session_a", `E:\ws\a`)
	code, stdout, _ = runCLI(t, cli, "doctor", "--json")
	if code != ExitFailure {
		t.Fatalf("doctor on a conflict exit = %d, want %d", code, ExitFailure)
	}
	report = decodeJSON[doctorReport](t, stdout)
	if report.Problems == 0 {
		t.Fatalf("conflict not reported: %+v", report.Checks)
	}
	found := false
	for _, check := range report.Checks {
		if check.ID == "ownership" && check.Status == "problem" && len(check.Items) == 2 {
			found = true
		}
	}
	if !found {
		t.Fatalf("ownership check missing the two conflicting nodes: %+v", report.Checks)
	}

	code, stdout, _ = runCLI(t, cli, "doctor")
	if code != ExitFailure {
		t.Fatalf("doctor human exit = %d", code)
	}
	if !strings.Contains(stdout, "[问题]") {
		t.Fatalf("human doctor output should mark the problem:\n%s", stdout)
	}
}

// TestCLIDoctorReportsSpawnExecutable：doctor 必须说明「拉起会用哪个 aicli」及其
// 来源（多版本共存时的第一诊断项）；AICLI_BIN 指错是 problem（退出码 5），
// 绝不悄悄退回其它候选。
func TestCLIDoctorReportsSpawnExecutable(t *testing.T) {
	paths := testCLIPaths(t)
	cli := testCLI(paths, newFakeClock())
	t.Setenv(EnvHome, t.TempDir())

	fake := filepath.Join(t.TempDir(), "aicli-2x.exe")
	if err := os.WriteFile(fake, []byte("x"), 0o600); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}
	t.Setenv(SpawnExecutableEnv, fake)

	code, stdout, stderr := runCLI(t, cli, "doctor", "--json")
	if code != ExitOK {
		t.Fatalf("doctor exit = %d (stderr %q, stdout %s)", code, stderr, stdout)
	}
	check := doctorCheckByID(t, decodeJSON[doctorReport](t, stdout), "spawn-executable")
	if check.Status != "ok" {
		t.Fatalf("spawn-executable status = %q, want ok（%s）", check.Status, check.Detail)
	}
	if !strings.Contains(check.Detail, fake) || !strings.Contains(check.Detail, SpawnExecutableSourceEnv) {
		t.Fatalf("detail 应给出二进制与来源: %q", check.Detail)
	}

	// 指错的 AICLI_BIN：problem + 退出码 5，并说清是 AICLI_BIN 的问题。
	t.Setenv(SpawnExecutableEnv, filepath.Join(t.TempDir(), "absent.exe"))
	code, stdout, _ = runCLI(t, cli, "doctor", "--json")
	if code != ExitFailure {
		t.Fatalf("doctor with a broken AICLI_BIN exit = %d, want %d", code, ExitFailure)
	}
	check = doctorCheckByID(t, decodeJSON[doctorReport](t, stdout), "spawn-executable")
	if check.Status != "problem" || !strings.Contains(check.Detail, SpawnExecutableEnv) {
		t.Fatalf("check = %+v, want problem 且 detail 提到 %s", check, SpawnExecutableEnv)
	}
}

// doctorCheckByID 取出一条检查项（缺失即失败）。
func doctorCheckByID(t *testing.T, report doctorReport, id string) doctorCheck {
	t.Helper()
	for _, check := range report.Checks {
		if check.ID == id {
			return check
		}
	}
	t.Fatalf("doctor 缺少检查项 %q：%+v", id, report.Checks)
	return doctorCheck{}
}

func TestCLIVersionUsageAndExitCodes(t *testing.T) {
	paths := testCLIPaths(t)
	clock := newFakeClock()
	cli := testCLI(paths, clock)

	code, stdout, stderr := runCLI(t, cli, "version", "--json")
	if code != ExitOK {
		t.Fatalf("version --json exit = %d (stderr %q)", code, stderr)
	}
	result := decodeJSON[versionResult](t, stdout)
	if result.Name != "aicli-mesh" || result.Version != "test-version" || result.RecordSchemaVersion != SchemaVersion {
		t.Fatalf("version = %+v", result)
	}
	if result.MeshRoot != paths.Root {
		t.Fatalf("version mesh root = %q, want %q", result.MeshRoot, paths.Root)
	}

	code, stdout, _ = runCLI(t, cli, "version")
	if code != ExitOK || !strings.Contains(stdout, "aicli-mesh test-version") {
		t.Fatalf("version output = %q (exit %d)", stdout, code)
	}

	if code, _, stderr = runCLI(t, cli, "nope"); code != ExitUsage || !strings.Contains(stderr, "未知子命令") {
		t.Fatalf("unknown command exit = %d (stderr %q)", code, stderr)
	}
	if code, _, _ = runCLI(t, cli); code != ExitUsage {
		t.Fatalf("no command exit = %d, want %d", code, ExitUsage)
	}
	if code, stdout, _ = runCLI(t, cli, "help"); code != ExitOK || !strings.Contains(stdout, "用法") {
		t.Fatalf("help exit = %d (stdout %q)", code, stdout)
	}
	if code, _, _ = runCLI(t, cli, "ls", "--help"); code != ExitOK {
		t.Fatalf("ls --help exit = %d", code)
	}
}

func TestCLIWorksWithoutMeshRoot(t *testing.T) {
	// fail-closed layout: no root means "empty mesh", never a crash
	paths := Paths{Source: PathSourceUnset}
	clock := newFakeClock()
	cli := testCLI(paths, clock)

	code, stdout, stderr := runCLI(t, cli, "ls", "--json")
	if code != ExitOK {
		t.Fatalf("ls without a root exit = %d (stderr %q)", code, stderr)
	}
	view := decodeJSON[MeshView](t, stdout)
	if len(view.Nodes) != 0 || view.Counts.Live != 0 {
		t.Fatalf("view = %+v, want empty", view)
	}

	if code, _, _ = runCLI(t, cli, "gc", "--json"); code != ExitOK {
		t.Fatalf("gc without a root exit = %d", code)
	}
	if code, _, _ = runCLI(t, cli, "show", "node-1"); code != ExitNotFound {
		t.Fatalf("show without a root exit = %d, want %d", code, ExitNotFound)
	}
	if code, stdout, _ = runCLI(t, cli, "doctor", "--json"); code != ExitFailure {
		t.Fatalf("doctor without a root exit = %d, want %d", code, ExitFailure)
	} else if report := decodeJSON[doctorReport](t, stdout); report.Problems != 1 {
		t.Fatalf("doctor problems = %d, want 1 (unresolved root)", report.Problems)
	}
}

// ---------------------------------------------------------------------------
// call / send / screen（S8，架构 §5.6 / §7.3）
// ---------------------------------------------------------------------------

func TestCLICallUsageErrors(t *testing.T) {
	paths := testCLIPaths(t)
	clock := newFakeClock()
	cli := testCLI(paths, clock)

	cases := []struct {
		name string
		args []string
		want string
	}{
		{"缺 op", []string{"call", "node-1"}, "需要 <目标> <op>"},
		{"args 非 JSON", []string{"call", "node-1", "node.info", "--args", "{oops"}, "合法 JSON"},
		{"timeout 非时长", []string{"call", "node-1", "node.info", "--timeout", "nope"}, "--timeout"},
		{"screen 缺目标", []string{"screen"}, "需要 <目标>"},
		{"screen tail 非整数", []string{"screen", "node-1", "--tail", "abc"}, "--tail 需要整数"},
		{"send 缺 prompt", []string{"send", "node-1", "--allow-write"}, "需要 <目标> <prompt>"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, _, stderr := runCLI(t, cli, tc.args...)
			if code != ExitUsage {
				t.Fatalf("exit = %d, want %d (stderr %q)", code, ExitUsage, stderr)
			}
			if !strings.Contains(stderr, tc.want) {
				t.Fatalf("stderr = %q, want 含 %q", stderr, tc.want)
			}
		})
	}

	if code, stdout, _ := runCLI(t, cli, "call", "--help"); code != ExitOK || !strings.Contains(stdout, "call") {
		t.Fatalf("call --help exit = %d (stdout %q)", code, stdout)
	}
}

func TestCLICallLocalRefusalsExitCodes(t *testing.T) {
	paths := testCLIPaths(t)
	clock := newFakeClock()
	cli := testCLI(paths, clock)

	// 未知 op：本地拒绝，error → ExitFailure。
	code, _, stderr := runCLI(t, cli, "call", "node-1", "shell.exec")
	if code != ExitFailure {
		t.Fatalf("未知 op exit = %d, want %d (stderr %q)", code, ExitFailure, stderr)
	}
	if !strings.Contains(stderr, CallCodeUnknownOp) {
		t.Fatalf("stderr 应带原因码: %q", stderr)
	}

	// 写操作无 --allow-write：refused → ExitRefused。
	code, _, stderr = runCLI(t, cli, "call", "node-1", "invoke", "--args", `{"prompt":"hi"}`)
	if code != ExitRefused {
		t.Fatalf("invoke 无 allow-write exit = %d, want %d (stderr %q)", code, ExitRefused, stderr)
	}
	if !strings.Contains(stderr, CallCodeWriteNotAllowed) {
		t.Fatalf("stderr 应带原因码: %q", stderr)
	}

	// send 是 invoke 的固定写法：同样无隐式放行。
	code, _, stderr = runCLI(t, cli, "send", "node-1", "你好")
	if code != ExitRefused || !strings.Contains(stderr, "--allow-write") {
		t.Fatalf("send 无 allow-write exit = %d (stderr %q)", code, stderr)
	}

	// 目标不存在：not_found → ExitNotFound。
	code, _, stderr = runCLI(t, cli, "call", "node-missing", "node.info")
	if code != ExitNotFound {
		t.Fatalf("未知目标 exit = %d, want %d (stderr %q)", code, ExitNotFound, stderr)
	}
	if !strings.Contains(stderr, CallCodeTargetNotFound) {
		t.Fatalf("stderr 应带原因码: %q", stderr)
	}
}

func TestCLICallJSONOutputAndRequestShape(t *testing.T) {
	paths := testCLIPaths(t)
	clock := newFakeClock()
	now := clock.Now()
	cli := testCLI(paths, clock)

	capture := &callCapture{}
	server := callEnvelopeServer(t, capture, http.StatusOK, CallEnvelope{
		SchemaVersion: SchemaVersion,
		Status:        CallStatusOK,
		NodeID:        "node-target-20260924T100000Z",
		Op:            "node.info",
		ElapsedMs:     2,
		Result:        json.RawMessage(`{"node_id":"node-target-20260924T100000Z","state":"live"}`),
	})
	seedCallableNode(t, paths, now, "node-target-20260924T100000Z", server.URL, "token-1")

	code, stdout, stderr := runCLI(t, cli, "call", "node-target-20260924T100000Z", "node.info", "--json")
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, stderr)
	}
	out := decodeJSON[callJSONResult](t, stdout)
	if out.SchemaVersion != SchemaVersion || out.Status != CallStatusOK {
		t.Fatalf("--json 输出 = %+v", out)
	}
	if out.NodeID != "node-target-20260924T100000Z" || out.Op != "node.info" {
		t.Fatalf("node/op = %q/%q", out.NodeID, out.Op)
	}
	if len(out.Result) == 0 {
		t.Fatal("--json 必须带 result（调用方原样透传）")
	}

	// CLI 不是节点：调用方头为空，但请求形状与令牌照旧。
	got := capture.snapshot()
	if got.path != ChatWebMeshCallPath || got.method != http.MethodPost {
		t.Fatalf("请求 = %s %s", got.method, got.path)
	}
	if got.token != "token-1" {
		t.Fatalf("令牌头 = %q", got.token)
	}
	if got.caller != "" {
		t.Fatalf("CLI 调用不应带调用方 node_id: %q", got.caller)
	}
	body := capture.requestBody(t)
	if body.Op != "node.info" || body.Target != "node-target-20260924T100000Z" {
		t.Fatalf("请求体 = %+v", body)
	}

	// 人读输出：只读调用打印 pretty JSON（result 原样）。
	code, stdout, _ = runCLI(t, cli, "call", "node-target-20260924T100000Z", "node.info")
	if code != ExitOK || !strings.Contains(stdout, "node-target-20260924T100000Z") {
		t.Fatalf("人读输出 exit = %d (stdout %q)", code, stdout)
	}
}

func TestCLISendPrintsAssistantAndPassesArgs(t *testing.T) {
	paths := testCLIPaths(t)
	clock := newFakeClock()
	now := clock.Now()
	cli := testCLI(paths, clock)

	capture := &callCapture{}
	server := callEnvelopeServer(t, capture, http.StatusOK, CallEnvelope{
		SchemaVersion: SchemaVersion,
		Status:        CallStatusOK,
		NodeID:        "node-target-20260924T100000Z",
		Op:            "invoke",
		Result:        json.RawMessage(`{"turn_id":"turn-1","assistant":{"content":"收到"}}`),
	})
	seedCallableNode(t, paths, now, "node-target-20260924T100000Z", server.URL, "token-1")

	code, stdout, stderr := runCLI(t, cli, "send", "node-target-20260924T100000Z", "只回复两个字", "--allow-write", "--client-request-id", "mesh-7-1")
	if code != ExitOK {
		t.Fatalf("send exit = %d (stderr %q)", code, stderr)
	}
	if strings.TrimSpace(stdout) != "收到" {
		t.Fatalf("send 人读输出 = %q, want 助手正文", stdout)
	}
	body := capture.requestBody(t)
	if body.Op != "invoke" || !body.AllowWrite {
		t.Fatalf("请求体 = %+v, want invoke + allow_write", body)
	}
	if body.ClientRequestID != "mesh-7-1" {
		t.Fatalf("client_request_id = %q", body.ClientRequestID)
	}
	if !strings.Contains(string(body.Args), "只回复两个字") {
		t.Fatalf("args = %s", string(body.Args))
	}
}

func TestCLIScreenDefaultsAndTail(t *testing.T) {
	paths := testCLIPaths(t)
	clock := newFakeClock()
	now := clock.Now()
	cli := testCLI(paths, clock)

	capture := &callCapture{}
	server := callEnvelopeServer(t, capture, http.StatusOK, CallEnvelope{
		SchemaVersion: SchemaVersion,
		Status:        CallStatusOK,
		NodeID:        "node-target-20260924T100000Z",
		Op:            "screen",
		Result:        json.RawMessage(`{"text":"Debug Screen: hello"}`),
	})
	seedCallableNode(t, paths, now, "node-target-20260924T100000Z", server.URL, "")

	code, stdout, stderr := runCLI(t, cli, "screen", "node-target-20260924T100000Z", "--tail", "40")
	if code != ExitOK {
		t.Fatalf("screen exit = %d (stderr %q)", code, stderr)
	}
	if !strings.Contains(stdout, "Debug Screen: hello") {
		t.Fatalf("screen 人读输出 = %q", stdout)
	}
	var args map[string]any
	if err := json.Unmarshal(capture.requestBody(t).Args, &args); err != nil {
		t.Fatalf("args: %v", err)
	}
	if args["view"] != "tui" || args["format"] != "json" || args["tail"] != "40" {
		t.Fatalf("screen args = %v, want view=tui + format=json + tail=40", args)
	}
}
