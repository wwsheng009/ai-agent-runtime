package mesh

import (
	"bytes"
	"encoding/json"
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
	if len(view.Nodes) != 2 {
		t.Fatalf("nodes = %d, want 2", len(view.Nodes))
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

	code, stdout, stderr := runCLI(t, cli, "ls", "--live", "--json")
	if code != ExitOK {
		t.Fatalf("ls --live exit = %d (stderr %q)", code, stderr)
	}
	view := decodeJSON[MeshView](t, stdout)
	if len(view.Nodes) != 2 {
		t.Fatalf("--live listed %d nodes, want 2 (stale hidden)", len(view.Nodes))
	}
	if view.Counts.Live != 2 || view.Counts.Stale != 1 {
		t.Fatalf("counts = %+v, want the full census (live=2 stale=1)", view.Counts)
	}
	if view.Filter == nil || view.Filter.State != FilterStateLive {
		t.Fatalf("filter echo = %+v, want state=live", view.Filter)
	}

	code, stdout, stderr = runCLI(t, cli, "ls", "--workspace", `E:\ws\a`, "--json")
	if code != ExitOK {
		t.Fatalf("ls --workspace exit = %d (stderr %q)", code, stderr)
	}
	view = decodeJSON[MeshView](t, stdout)
	if len(view.Nodes) != 2 {
		t.Fatalf("--workspace listed %d nodes, want 2 (live + stale in E:\\ws\\a)", len(view.Nodes))
	}
	if view.Counts.Live != 2 || view.Counts.Stale != 1 {
		t.Fatalf("counts = %+v, want the full census", view.Counts)
	}
	if view.Filter == nil || len(view.Filter.Workspace) != 1 {
		t.Fatalf("filter echo = %+v", view.Filter)
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
