package mesh

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeClock is a controllable, race-free clock for the host tests.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t.Truncate(time.Second)
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// testMeshPaths points AICLI_MESH_DIR at a fresh temp dir and returns the
// resolved layout.
func testMeshPaths(t *testing.T) Paths {
	t.Helper()
	t.Setenv(EnvMeshDir, t.TempDir())
	paths := ResolvePaths()
	if !paths.Enabled() {
		t.Fatalf("expected an enabled mesh layout, got %+v", paths)
	}
	return paths
}

func readRecord(t *testing.T, paths Paths, nodeID string) *NodeRecord {
	t.Helper()
	file := ReadNodeFile(paths.NodePath(nodeID))
	if file.Err != nil {
		t.Fatalf("read node record %s: %v", nodeID, file.Err)
	}
	if file.Record == nil {
		t.Fatalf("node record %s is missing", nodeID)
	}
	return file.Record
}

func journalKinds(t *testing.T, paths Paths, nodeID string) []string {
	t.Helper()
	entries, err := ReadJournalEntries(paths.JournalPath(nodeID), 0)
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	kinds := make([]string, 0, len(entries))
	for _, entry := range entries {
		kinds = append(kinds, entry.Kind)
	}
	return kinds
}

func countKind(kinds []string, kind string) int {
	count := 0
	for _, candidate := range kinds {
		if candidate == kind {
			count++
		}
	}
	return count
}

func TestHostStartPublishesRecordAndJournal(t *testing.T) {
	paths := testMeshPaths(t)
	clock := newFakeClock()
	host := NewHost(HostConfig{
		Kind:          "chat",
		Origin:        "cli",
		Version:       "test-version",
		BuildTime:     "test-build",
		Exe:           "aicli.exe",
		WorkspacePath: "/tmp/ws",
		WorkspaceName: "ws",
		Now:           clock.Now,
	})
	if err := host.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	nodeID := host.NodeID()
	if !strings.HasPrefix(nodeID, "node-") {
		t.Fatalf("unexpected node id %q", nodeID)
	}

	record := readRecord(t, paths, nodeID)
	if record.SchemaVersion != SchemaVersion {
		t.Fatalf("schema_version = %d, want %d", record.SchemaVersion, SchemaVersion)
	}
	if record.NodeID != nodeID {
		t.Fatalf("node_id = %q, want %q", record.NodeID, nodeID)
	}
	if record.Kind != "chat" {
		t.Fatalf("kind = %q", record.Kind)
	}
	if record.Process.Origin != "cli" {
		t.Fatalf("origin = %q", record.Process.Origin)
	}
	if record.Process.Exe != "aicli.exe" || record.Process.Version != "test-version" {
		t.Fatalf("process identity not published: %+v", record.Process)
	}
	if record.Workspace == nil || record.Workspace.Name != "ws" {
		t.Fatalf("workspace not published: %+v", record.Workspace)
	}
	if record.Liveness.State != string(NodeStateLive) {
		t.Fatalf("liveness.state = %q", record.Liveness.State)
	}
	if record.Liveness.HeartbeatTTLSec != int(DefaultHeartbeatTTL/time.Second) {
		t.Fatalf("heartbeat_ttl_sec = %d", record.Liveness.HeartbeatTTLSec)
	}
	if !record.Liveness.HeartbeatAt.Equal(clock.Now()) {
		t.Fatalf("heartbeat_at = %v, want %v", record.Liveness.HeartbeatAt, clock.Now())
	}
	// Before the loopback server listens there is no endpoint and the node is
	// not callable — only discoverable.
	if record.Endpoint != nil {
		t.Fatalf("endpoint must be absent before the loopback server starts: %+v", record.Endpoint)
	}
	if record.Session != nil {
		t.Fatalf("session must be absent before the first turn: %+v", record.Session)
	}
	if len(record.Capabilities) != 1 || record.Capabilities[0] != "mesh" {
		t.Fatalf("capabilities = %v, want [mesh]", record.Capabilities)
	}

	entries, err := ReadJournalEntries(paths.JournalPath(nodeID), 0)
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	if len(entries) != 1 || entries[0].Kind != JournalNodeStarted {
		t.Fatalf("journal = %+v, want a single node.started", entries)
	}
	if entries[0].NodeID != nodeID || entries[0].Seq != 1 {
		t.Fatalf("journal entry identity = %+v", entries[0])
	}

	host.Close()
	if _, err := os.Stat(paths.NodePath(nodeID)); !os.IsNotExist(err) {
		t.Fatalf("node record must be deleted on close (stat err = %v)", err)
	}
	kinds := journalKinds(t, paths, nodeID)
	if len(kinds) != 2 || kinds[1] != JournalNodeStopped {
		t.Fatalf("journal kinds after close = %v", kinds)
	}

	// Close is idempotent and never panics on a second call.
	host.Close()
}

// TestHostRecordAdvertisesStopCapability 锁死治理开关的档案投影（§5.7 / §9.2）：
// --mesh-allow-stop=true 必须在档案里声明 CapabilityStop，否则不经目标 HTTP 层
// 的调用方（aicli-mesh stop 本地编排）无从判定「谁能停我」；默认（false）不得
// 声明——那等于默认放行停止。
func TestHostRecordAdvertisesStopCapability(t *testing.T) {
	onPaths := testMeshPaths(t)
	on := NewHost(HostConfig{Now: newFakeClock().Now, HeartbeatInterval: time.Hour, StopAllowed: true})
	if err := on.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer on.Close()
	if record := readRecord(t, onPaths, on.NodeID()); !HasCapability(record.Capabilities, CapabilityStop) {
		t.Fatalf("capabilities = %v, want to contain %q", record.Capabilities, CapabilityStop)
	}

	offPaths := testMeshPaths(t)
	off := NewHost(HostConfig{Now: newFakeClock().Now, HeartbeatInterval: time.Hour})
	if err := off.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer off.Close()
	if record := readRecord(t, offPaths, off.NodeID()); HasCapability(record.Capabilities, CapabilityStop) {
		t.Fatalf("capabilities = %v, want no %q（默认关闭）", record.Capabilities, CapabilityStop)
	}
}

func TestHostEndpointPromotesCapabilities(t *testing.T) {
	paths := testMeshPaths(t)
	host := NewHost(HostConfig{Now: newFakeClock().Now})
	if err := host.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer host.Close()
	nodeID := host.NodeID()

	host.SetEndpoint(
		EndpointInfo{Host: "127.0.0.1", Port: 55124, Loopback: true},
		AuthInfo{Mode: "loopback-dev", Required: false, Token: "super-secret-token-value"},
	)

	record := readRecord(t, paths, nodeID)
	if record.Endpoint == nil || record.Endpoint.Port != 55124 {
		t.Fatalf("endpoint = %+v", record.Endpoint)
	}
	if record.Endpoint.BaseURL != "http://127.0.0.1:55124" {
		t.Fatalf("base_url = %q", record.Endpoint.BaseURL)
	}
	if record.Endpoint.WebBaseURL != "http://127.0.0.1:55124/web" {
		t.Fatalf("web_base_url = %q", record.Endpoint.WebBaseURL)
	}
	if record.Endpoint.ManifestURL != "http://127.0.0.1:55124/debug/endpoints" {
		t.Fatalf("manifest_url = %q", record.Endpoint.ManifestURL)
	}
	if record.Auth == nil || record.Auth.Token != "super-secret-token-value" {
		t.Fatalf("auth = %+v", record.Auth)
	}
	for _, want := range []string{"invoke", "manifest", "mesh"} {
		found := false
		for _, capability := range record.Capabilities {
			if capability == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("capability %q missing after the loopback server started: %v", want, record.Capabilities)
		}
	}
	// The write token is a secret: it must never reach the journal.
	journal, err := os.ReadFile(paths.JournalPath(nodeID))
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	if strings.Contains(string(journal), "super-secret-token-value") {
		t.Fatalf("journal leaked the write token: %s", journal)
	}
}

func TestHostSessionAndBusyLifecycle(t *testing.T) {
	paths := testMeshPaths(t)
	clock := newFakeClock()
	host := NewHost(HostConfig{Now: clock.Now})
	if err := host.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer host.Close()
	nodeID := host.NodeID()

	host.SetSession(&SessionInfo{ID: "sess-1", Title: "first"})
	host.SetBusy(true, "turn-0001")

	record := readRecord(t, paths, nodeID)
	if record.Session == nil || record.Session.ID != "sess-1" {
		t.Fatalf("session = %+v", record.Session)
	}
	if !record.Session.Busy || record.Session.TurnID != "turn-0001" {
		t.Fatalf("busy flip not published: %+v", record.Session)
	}
	if !record.Session.ActivatedAt.Equal(clock.Now()) {
		t.Fatalf("activated_at = %v", record.Session.ActivatedAt)
	}
	activatedAt := record.Session.ActivatedAt

	// Re-syncing the same session (goal reconcile / tool session context) must
	// neither reset activated_at nor the busy flag nor spam the journal.
	clock.Advance(7 * time.Second)
	host.SetSession(&SessionInfo{ID: "sess-1", Title: "first"})
	record = readRecord(t, paths, nodeID)
	if !record.Session.ActivatedAt.Equal(activatedAt) {
		t.Fatalf("resync must keep activated_at, got %v", record.Session.ActivatedAt)
	}
	if !record.Session.Busy || record.Session.TurnID != "turn-0001" {
		t.Fatalf("resync must keep the busy state: %+v", record.Session)
	}
	if got := countKind(journalKinds(t, paths, nodeID), JournalSessionActivated); got != 1 {
		t.Fatalf("session.activated count = %d, want 1", got)
	}

	// A title refresh rewrites the record without a new journal entry.
	host.SetSession(&SessionInfo{ID: "sess-1", Title: "renamed"})
	record = readRecord(t, paths, nodeID)
	if record.Session.Title != "renamed" {
		t.Fatalf("title = %q", record.Session.Title)
	}
	if got := countKind(journalKinds(t, paths, nodeID), JournalSessionActivated); got != 1 {
		t.Fatalf("title refresh must not re-journal session.activated (count = %d)", got)
	}

	host.SetBusy(false, "")
	record = readRecord(t, paths, nodeID)
	if record.Session.Busy {
		t.Fatalf("busy must be false after the turn ended: %+v", record.Session)
	}

	// Switching sessions journals a new activation and keeps the new id.
	host.SetSession(&SessionInfo{ID: "sess-2", Title: "second"})
	record = readRecord(t, paths, nodeID)
	if record.Session.ID != "sess-2" || record.Session.Busy {
		t.Fatalf("session switch not published: %+v", record.Session)
	}
	if got := countKind(journalKinds(t, paths, nodeID), JournalSessionActivated); got != 2 {
		t.Fatalf("session.activated count = %d, want 2", got)
	}

	// Busy without an active session is ignored (never a stray busy flag).
	host.SetSession(nil)
	host.SetBusy(true, "turn-0003")
	record = readRecord(t, paths, nodeID)
	if record.Session != nil {
		t.Fatalf("session must be cleared: %+v", record.Session)
	}
	if got := countKind(journalKinds(t, paths, nodeID), JournalSessionDeactivated); got != 1 {
		t.Fatalf("session.deactivated count = %d, want 1", got)
	}
}

func TestHostHeartbeatRefreshesRecord(t *testing.T) {
	paths := testMeshPaths(t)
	clock := newFakeClock()
	host := NewHost(HostConfig{HeartbeatInterval: 15 * time.Millisecond, Now: clock.Now})
	if err := host.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer host.Close()
	nodeID := host.NodeID()
	first := readRecord(t, paths, nodeID).Liveness.HeartbeatAt

	clock.Advance(45 * time.Second)
	deadline := time.Now().Add(5 * time.Second)
	for {
		record := readRecord(t, paths, nodeID)
		if record.Liveness.HeartbeatAt.After(first) {
			if record.Liveness.UpdatedAt.Before(record.Liveness.HeartbeatAt) {
				t.Fatalf("updated_at must follow heartbeat_at: %+v", record.Liveness)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("heartbeat never refreshed the record: %+v", record.Liveness)
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Heartbeats are state-preserving writes: they never journal.
	if kinds := journalKinds(t, paths, nodeID); len(kinds) != 1 || kinds[0] != JournalNodeStarted {
		t.Fatalf("heartbeat must not journal, got %v", kinds)
	}
}

func TestHostStopsWritingAfterConsecutiveFailures(t *testing.T) {
	root := t.TempDir()
	blocker := filepath.Join(root, "blocker")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("prepare blocker: %v", err)
	}
	t.Setenv(EnvMeshDir, filepath.Join(blocker, "mesh"))

	var warnings atomic.Int64
	host := NewHost(HostConfig{
		HeartbeatInterval: 10 * time.Millisecond,
		Now:               newFakeClock().Now,
		Warn: func(string, ...any) {
			warnings.Add(1)
		},
	})
	// Start reports the failure to the caller (which only logs a warning) but
	// keeps the host alive for the heartbeat retry budget.
	if err := host.Start(); err == nil {
		t.Fatal("expected the first record write to fail")
	}

	deadline := time.Now().Add(5 * time.Second)
	for !host.WriteDisabled() {
		if time.Now().After(deadline) {
			t.Fatal("host never gave up writing after repeated failures")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if warnings.Load() == 0 {
		t.Fatal("degradation must produce at least one warning")
	}

	// The chat path stays usable: state updates are accepted and ignored.
	host.SetSession(&SessionInfo{ID: "sess-1"})
	host.SetBusy(true, "turn-0001")
	if got := host.RecordSnapshot().Session; got == nil || got.ID != "sess-1" || !got.Busy {
		t.Fatalf("in-memory state must survive write failures: %+v", got)
	}
	host.Close()
}

func TestHostWithoutMeshRootIsInert(t *testing.T) {
	t.Setenv(EnvMeshDir, "")
	t.Setenv(EnvHome, "")
	t.Setenv("USERPROFILE", "")
	t.Setenv("HOMEDRIVE", "")
	t.Setenv("HOMEPATH", "")
	t.Setenv("HOME", "")
	paths := ResolvePaths()
	if paths.Enabled() {
		t.Skipf("a home directory is still resolvable here: %+v", paths)
	}

	host := NewHost(HostConfig{Now: newFakeClock().Now})
	if err := host.Start(); err != nil {
		t.Fatalf("Start on a fail-closed layout must not error: %v", err)
	}
	if host.NodeID() != "" {
		t.Fatalf("node id = %q, want empty on a fail-closed layout", host.NodeID())
	}
	if host.Journal() != nil {
		t.Fatal("journal must be nil without a mesh root")
	}
	host.SetSession(&SessionInfo{ID: "sess-1"})
	host.SetBusy(true, "turn-0001")
	host.SetEndpoint(EndpointInfo{Host: "127.0.0.1", Port: 1}, AuthInfo{})
	host.Close()
}

func TestNodeIDReservationAvoidsCollisions(t *testing.T) {
	paths := testMeshPaths(t)
	clock := newFakeClock()
	first := NewHost(HostConfig{Now: clock.Now})
	if err := first.Start(); err != nil {
		t.Fatalf("Start first: %v", err)
	}
	defer first.Close()

	second := NewHost(HostConfig{Now: clock.Now})
	if err := second.Start(); err != nil {
		t.Fatalf("Start second: %v", err)
	}
	defer second.Close()

	if first.NodeID() == second.NodeID() {
		t.Fatalf("two processes with the same pid and start second collided on %q", first.NodeID())
	}
	for _, nodeID := range []string{first.NodeID(), second.NodeID()} {
		if _, err := os.Stat(paths.NodePath(nodeID)); err != nil {
			t.Fatalf("node record %s missing: %v", nodeID, err)
		}
	}
}

func TestJournalRedactsSecretsAndSkipsTornLines(t *testing.T) {
	paths := testMeshPaths(t)
	journal := OpenJournal(paths, "node-test", newFakeClock().Now)
	if journal == nil {
		t.Fatal("expected a journal writer")
	}
	journal.SetSecrets("super-secret-token-value")
	journal.Append(JournalCallReceived, "sess-1", map[string]any{
		"token": "super-secret-token-value",
		"url":   "http://127.0.0.1:1/web/api/turn?token=super-secret-token-value",
		"nested": map[string]any{
			"authorization": "Bearer super-secret-token-value",
		},
	})

	data, err := os.ReadFile(paths.JournalPath("node-test"))
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	if strings.Contains(string(data), "super-secret-token-value") {
		t.Fatalf("journal leaked a secret: %s", data)
	}
	if !strings.Contains(string(data), "[redacted]") {
		t.Fatalf("expected redaction markers: %s", data)
	}

	// A torn tail line (crash mid-append) must not break readers.
	file, err := os.OpenFile(paths.JournalPath("node-test"), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open journal: %v", err)
	}
	if _, err := file.WriteString("{\"ts\":\"2026-09-24T10:00:00Z\",\"node_id\""); err != nil {
		t.Fatalf("append torn line: %v", err)
	}
	_ = file.Close()

	entries, err := ReadJournalEntries(paths.JournalPath("node-test"), 0)
	if err != nil {
		t.Fatalf("read journal with a torn tail: %v", err)
	}
	if len(entries) != 1 || entries[0].Kind != JournalCallReceived {
		t.Fatalf("entries = %+v", entries)
	}
	if entries[0].SessionID != "sess-1" || entries[0].Seq != 1 {
		t.Fatalf("entry = %+v", entries[0])
	}
	if formatted := FormatJournalEntry(entries[0]); !strings.Contains(formatted, JournalCallReceived) {
		t.Fatalf("format = %q", formatted)
	}
}

func TestJournalRotatesAtSizeThreshold(t *testing.T) {
	paths := testMeshPaths(t)
	journal := OpenJournal(paths, "node-rotate", newFakeClock().Now)
	journal.maxBytes = 256
	for i := 0; i < 12; i++ {
		journal.Append(JournalPeerObserved, "sess-1", map[string]any{"peer": "node-other", "index": i})
	}
	if _, err := os.Stat(paths.JournalPath("node-rotate") + journalRotatedSuffix); err != nil {
		t.Fatalf("rotated generation missing: %v", err)
	}
	entries, err := ReadJournalEntries(paths.JournalPath("node-rotate"), 0)
	if err != nil {
		t.Fatalf("read rotated journal: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("current generation must keep the newest entries")
	}
	if entries[len(entries)-1].Seq != 12 {
		t.Fatalf("seq must stay monotonic across rotation, got %d", entries[len(entries)-1].Seq)
	}
}

func TestCurrentHostHelpersAreNilSafe(t *testing.T) {
	SetCurrent(nil)
	SetCurrentSession(&SessionInfo{ID: "sess-1"})
	SetCurrentBusy(true, "turn-0001")
	SetCurrentEndpoint(EndpointInfo{Host: "127.0.0.1", Port: 1}, AuthInfo{})
	CloseCurrent()
	if Current() != nil {
		t.Fatal("CloseCurrent must clear the process host")
	}

	paths := testMeshPaths(t)
	host := NewHost(HostConfig{Now: newFakeClock().Now})
	if err := host.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	SetCurrent(host)
	SetCurrentSession(&SessionInfo{ID: "sess-9", Title: "via helper"})
	nodeID := host.NodeID()
	if record := readRecord(t, paths, nodeID); record.Session == nil || record.Session.ID != "sess-9" {
		t.Fatalf("helper did not publish the session: %+v", record.Session)
	}
	CloseCurrent()
	if _, err := os.Stat(paths.NodePath(nodeID)); !os.IsNotExist(err) {
		t.Fatalf("CloseCurrent must delete the record (stat err = %v)", err)
	}
}
