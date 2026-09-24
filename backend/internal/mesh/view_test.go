package mesh

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// viewNodeRecord builds a node record the way a chat process would publish it.
func viewNodeRecord(nodeID string, pid int, heartbeat time.Time, sessionID, workspacePath string) NodeRecord {
	record := NodeRecord{
		SchemaVersion: SchemaVersion,
		NodeID:        nodeID,
		PID:           pid,
		Kind:          "chat",
		Process:       ProcessInfo{StartedAt: heartbeat.Add(-time.Minute), Origin: "cli"},
		Capabilities:  []string{"mesh"},
		Liveness: LivenessInfo{
			StartedAt:       heartbeat.Add(-time.Minute),
			UpdatedAt:       heartbeat,
			HeartbeatAt:     heartbeat,
			HeartbeatTTLSec: int(DefaultHeartbeatTTL / time.Second),
			State:           string(NodeStateLive),
		},
	}
	if sessionID != "" {
		record.Session = &SessionInfo{ID: sessionID, Title: "title-" + sessionID, ActivatedAt: heartbeat}
	}
	if workspacePath != "" {
		record.Workspace = &WorkspaceInfo{Path: workspacePath, Name: filepath.Base(workspacePath)}
	}
	return record
}

func writeViewNode(t *testing.T, paths Paths, record NodeRecord) {
	t.Helper()
	if err := WriteNodeRecord(paths, record); err != nil {
		t.Fatalf("write node record %s: %v", record.NodeID, err)
	}
}

func nodeByName(t *testing.T, view MeshView, nodeID string) NodeView {
	t.Helper()
	for _, node := range view.Nodes {
		if node.NodeID == nodeID {
			return node
		}
	}
	t.Fatalf("node %s missing from view (%+v)", nodeID, view.Nodes)
	return NodeView{}
}

func snapshotTree(t *testing.T, root string) []string {
	t.Helper()
	var entries []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		entries = append(entries, fmt.Sprintf("%s|%d|%d", path, info.Size(), info.ModTime().UnixNano()))
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	sort.Strings(entries)
	return entries
}

func TestViewRefinesNodeStateAndSorts(t *testing.T) {
	paths := testPaths(t)
	now := NowUTC()
	writeViewNode(t, paths, viewNodeRecord("node-live", os.Getpid(), now, "session_live", `E:\ws\one`))
	writeViewNode(t, paths, viewNodeRecord("node-slow", os.Getpid(), now.Add(-10*time.Minute), "session_slow", `E:\ws\one`))
	writeViewNode(t, paths, viewNodeRecord("node-dead", deadPID(t), now, "session_dead", `E:\ws\two`))
	garbage := filepath.Join(paths.Nodes, "node-broken.json")
	if err := os.WriteFile(garbage, []byte("{not json\n"), 0o600); err != nil {
		t.Fatalf("seed broken record: %v", err)
	}

	view := BuildView(paths, ViewOptions{Now: now, SelfNodeID: "node-live"})
	if len(view.Nodes) != 4 {
		t.Fatalf("nodes = %d, want 4 (%+v)", len(view.Nodes), view.Nodes)
	}
	if got := view.Nodes[0].NodeID; got != "node-live" {
		t.Fatalf("first node = %s, want the live one", got)
	}
	if got := view.Nodes[len(view.Nodes)-1].State; got != NodeStateUnknown {
		t.Fatalf("last node state = %s, want unknown", got)
	}
	if node := nodeByName(t, view, "node-live"); node.State != NodeStateLive || node.AgeSec != 0 {
		t.Fatalf("live node = %+v", node)
	}
	if node := nodeByName(t, view, "node-slow"); node.State != NodeStateStale {
		t.Fatalf("node with an old heartbeat must be stale: %+v", node)
	}
	if node := nodeByName(t, view, "node-dead"); node.State != NodeStateStale {
		t.Fatalf("node with a dead pid must be stale: %+v", node)
	}
	broken := nodeByName(t, view, "node-broken")
	if broken.State != NodeStateUnknown || broken.Err == "" || broken.Record != nil {
		t.Fatalf("broken record view = %+v", broken)
	}
	if view.Counts.Live != 1 || view.Counts.Stale != 2 || view.Counts.Unknown != 1 {
		t.Fatalf("counts = %+v", view.Counts)
	}
}

func TestViewOwnershipOwnerPeerAndNone(t *testing.T) {
	paths := testPaths(t)
	now := NowUTC()
	writeViewNode(t, paths, viewNodeRecord("node-a", os.Getpid(), now, "session_a", `E:\ws\one`))
	writeViewNode(t, paths, viewNodeRecord("node-b", os.Getpid(), now, "session_b", `E:\ws\one`))
	writeViewNode(t, paths, viewNodeRecord("node-idle", os.Getpid(), now, "", `E:\ws\one`))

	view := BuildView(paths, ViewOptions{Now: now, SelfNodeID: "node-a"})
	if got := nodeByName(t, view, "node-a").Ownership; got != OwnershipOwner {
		t.Fatalf("self ownership = %s, want owner", got)
	}
	if got := nodeByName(t, view, "node-b").Ownership; got != OwnershipPeer {
		t.Fatalf("peer ownership = %s, want peer", got)
	}
	if got := nodeByName(t, view, "node-idle").Ownership; got != OwnershipNone {
		t.Fatalf("session-less ownership = %s, want none", got)
	}
	if view.Counts.Conflict != 0 {
		t.Fatalf("counts = %+v, want no conflict", view.Counts)
	}
	if view.Self == nil || view.Self.SessionID != "session_a" {
		t.Fatalf("self = %+v", view.Self)
	}
}

func TestViewConflictOnTwoLiveClaimants(t *testing.T) {
	paths := testPaths(t)
	now := NowUTC()
	writeViewNode(t, paths, viewNodeRecord("node-a", os.Getpid(), now, "session_shared", `E:\ws\one`))
	writeViewNode(t, paths, viewNodeRecord("node-b", os.Getpid(), now.Add(-time.Second), "session_shared", `E:\ws\two`))

	view := BuildView(paths, ViewOptions{Now: now, SelfNodeID: "node-a"})
	for _, nodeID := range []string{"node-a", "node-b"} {
		if got := nodeByName(t, view, nodeID).Ownership; got != OwnershipConflict {
			t.Fatalf("%s ownership = %s, want conflict", nodeID, got)
		}
	}
	if view.Counts.Conflict != 2 {
		t.Fatalf("counts = %+v, want two conflicting nodes", view.Counts)
	}
}

func TestViewConflictIgnoresNonLiveClaimants(t *testing.T) {
	paths := testPaths(t)
	now := NowUTC()
	writeViewNode(t, paths, viewNodeRecord("node-a", os.Getpid(), now, "session_shared", `E:\ws\one`))
	// pid alive but the heartbeat lapsed: not a live claimant (§4.2 rule 1).
	writeViewNode(t, paths, viewNodeRecord("node-slow", os.Getpid(), now.Add(-10*time.Minute), "session_shared", `E:\ws\one`))

	view := BuildView(paths, ViewOptions{Now: now, SelfNodeID: "node-a"})
	if got := nodeByName(t, view, "node-a").Ownership; got != OwnershipOwner {
		t.Fatalf("live claimant ownership = %s, want owner", got)
	}
	if got := nodeByName(t, view, "node-slow").Ownership; got != OwnershipNone {
		t.Fatalf("stale claimant ownership = %s, want none", got)
	}
	if view.Counts.Conflict != 0 {
		t.Fatalf("counts = %+v, want no conflict", view.Counts)
	}
}

func TestViewConflictIsFullScopeAcrossWorkspaces(t *testing.T) {
	paths := testPaths(t)
	now := NowUTC()
	writeViewNode(t, paths, viewNodeRecord("node-a", os.Getpid(), now, "session_shared", `E:\ws\one`))
	writeViewNode(t, paths, viewNodeRecord("node-b", os.Getpid(), now, "session_shared", `E:\ws\two`))

	view := BuildView(paths, ViewOptions{Now: now, SelfNodeID: "node-a"})
	if view.Counts.Conflict != 2 {
		t.Fatalf("counts = %+v: workspace must not narrow conflict detection", view.Counts)
	}
	if len(view.Workspaces) != 2 {
		t.Fatalf("workspaces = %+v, want two groups", view.Workspaces)
	}
	for _, group := range view.Workspaces {
		if group.Nodes != 1 {
			t.Fatalf("workspace group = %+v", group)
		}
	}
}

func TestViewJoinsBindings(t *testing.T) {
	paths := testPaths(t)
	now := NowUTC()
	writeViewNode(t, paths, viewNodeRecord("node-a", os.Getpid(), now, "session_a", `E:\ws\one`))
	writeViewNode(t, paths, viewNodeRecord("node-dead", deadPID(t), now, "session_gone", `E:\ws\one`))
	if err := TouchBinding(paths, BindingUpdate{
		SessionID:     "session_a",
		Host:          "127.0.0.1",
		Port:          55124,
		NodeID:        "node-a",
		WorkspacePath: `E:\ws\one`,
	}); err != nil {
		t.Fatalf("TouchBinding(session_a): %v", err)
	}
	if err := TouchBinding(paths, BindingUpdate{
		SessionID: "session_gone",
		Host:      "127.0.0.1",
		Port:      55125,
		NodeID:    "node-dead",
	}); err != nil {
		t.Fatalf("TouchBinding(session_gone): %v", err)
	}

	view := BuildView(paths, ViewOptions{Now: now})
	live := nodeByName(t, view, "node-a")
	if live.Binding == nil || live.Binding.Preferred == nil || live.Binding.Preferred.Port != 55124 {
		t.Fatalf("live binding = %+v", live.Binding)
	}
	// The binding of a dead node is exactly the "last served at" story (§3.2):
	// it survives the node.
	dead := nodeByName(t, view, "node-dead")
	if dead.State != NodeStateStale || dead.Binding == nil || dead.Binding.Preferred.Port != 55125 {
		t.Fatalf("dead node view = %+v", dead)
	}
}

func TestViewWorkspaceGroupsIncludeNodesWithoutWorkspace(t *testing.T) {
	paths := testPaths(t)
	now := NowUTC()
	writeViewNode(t, paths, viewNodeRecord("node-a", os.Getpid(), now, "", `E:\ws\one`))
	writeViewNode(t, paths, viewNodeRecord("node-b", os.Getpid(), now, "", `E:\ws\two`))
	writeViewNode(t, paths, viewNodeRecord("node-c", os.Getpid(), now, "", ""))

	view := BuildView(paths, ViewOptions{Now: now})
	if len(view.Workspaces) != 3 {
		t.Fatalf("workspaces = %+v, want three groups", view.Workspaces)
	}
	if view.Workspaces[0].Path != "" || view.Workspaces[0].Name != "(无工作区)" || view.Workspaces[0].Nodes != 1 {
		t.Fatalf("first group = %+v", view.Workspaces[0])
	}
}

func TestViewProbeDisabledIsSkipped(t *testing.T) {
	paths := testPaths(t)
	now := NowUTC()
	record := viewNodeRecord("node-a", os.Getpid(), now, "", "")
	record.Endpoint = &EndpointInfo{BaseURL: "http://127.0.0.1:1", Loopback: true}
	writeViewNode(t, paths, record)

	view := BuildView(paths, ViewOptions{Now: now})
	if got := nodeByName(t, view, "node-a").Reachability; got != ReachabilitySkipped {
		t.Fatalf("reachability without --probe = %s, want skipped", got)
	}
}

func TestViewProbeMarksOKAndUnreachable(t *testing.T) {
	paths := testPaths(t)
	now := NowUTC()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != probeHealthPath {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	closedPort := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()

	up := viewNodeRecord("node-up", os.Getpid(), now, "", "")
	up.Endpoint = &EndpointInfo{BaseURL: server.URL, Loopback: true}
	writeViewNode(t, paths, up)
	down := viewNodeRecord("node-down", os.Getpid(), now, "", "")
	down.Endpoint = &EndpointInfo{BaseURL: fmt.Sprintf("http://127.0.0.1:%d", closedPort), Loopback: true}
	writeViewNode(t, paths, down)
	plain := viewNodeRecord("node-plain", os.Getpid(), now, "", "")
	writeViewNode(t, paths, plain)

	view := BuildView(paths, ViewOptions{Now: now, Probe: true, ProbeBudget: 5 * time.Second, ProbeTimeout: time.Second})
	if got := nodeByName(t, view, "node-up").Reachability; got != ReachabilityOK {
		t.Fatalf("healthy node reachability = %s, want ok", got)
	}
	if got := nodeByName(t, view, "node-down").Reachability; got != ReachabilityUnreachable {
		t.Fatalf("closed port reachability = %s, want unreachable", got)
	}
	if got := nodeByName(t, view, "node-plain").Reachability; got != ReachabilitySkipped {
		t.Fatalf("node without endpoint reachability = %s, want skipped", got)
	}
}

func TestViewProbeBudgetMarksSlowNodesSkipped(t *testing.T) {
	paths := testPaths(t)
	now := NowUTC()
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	defer close(release)

	for _, nodeID := range []string{"node-1", "node-2", "node-3"} {
		record := viewNodeRecord(nodeID, os.Getpid(), now, "", "")
		record.Endpoint = &EndpointInfo{BaseURL: server.URL, Loopback: true}
		writeViewNode(t, paths, record)
	}

	view := BuildView(paths, ViewOptions{
		Now:          now,
		Probe:        true,
		ProbeBudget:  100 * time.Millisecond,
		ProbeTimeout: 5 * time.Second,
	})
	for _, node := range view.Nodes {
		if node.Reachability != ReachabilitySkipped {
			t.Fatalf("node %s reachability = %s, want skipped (budget ran out)", node.NodeID, node.Reachability)
		}
	}
}

func TestViewJournalTailKeepsNewestKinds(t *testing.T) {
	paths := testPaths(t)
	now := NowUTC()
	writeViewNode(t, paths, viewNodeRecord("node-a", os.Getpid(), now, "session_a", ""))
	journal := OpenJournal(paths, "node-a", NowUTC)
	for _, kind := range []string{JournalNodeStarted, JournalSessionActivated, JournalBusyChanged} {
		journal.Append(kind, "session_a", nil)
	}

	view := BuildView(paths, ViewOptions{Now: now, JournalTailLimit: 2})
	tail := nodeByName(t, view, "node-a").JournalTail
	if len(tail) != 2 || tail[0] != JournalSessionActivated || tail[1] != JournalBusyChanged {
		t.Fatalf("journal tail = %v, want [session.activated busy.changed]", tail)
	}
	if plain := BuildView(paths, ViewOptions{Now: now}); nodeByName(t, plain, "node-a").JournalTail != nil {
		t.Fatal("journal tail must stay opt-in")
	}
}

func TestViewIsReadOnly(t *testing.T) {
	paths := testPaths(t)
	now := NowUTC()
	writeViewNode(t, paths, viewNodeRecord("node-a", os.Getpid(), now, "session_a", `E:\ws\one`))
	writeViewNode(t, paths, viewNodeRecord("node-b", deadPID(t), now, "session_b", `E:\ws\two`))

	before := snapshotTree(t, paths.Root)
	_ = BuildView(paths, ViewOptions{Now: now, SelfNodeID: "node-a", JournalTailLimit: 5})
	after := snapshotTree(t, paths.Root)
	if strings.Join(before, "\n") != strings.Join(after, "\n") {
		t.Fatalf("BuildView wrote to disk:\nbefore=%v\nafter=%v", before, after)
	}
}

func TestViewRedactsTokenAndNeverMarshalsRecord(t *testing.T) {
	paths := testPaths(t)
	now := NowUTC()
	record := viewNodeRecord("node-a", os.Getpid(), now, "session_a", "")
	record.Auth = &AuthInfo{Mode: "loopback-dev", Required: false, Token: "0f3a-secret-token", TokenSource: "random"}
	writeViewNode(t, paths, record)

	view := BuildView(paths, ViewOptions{Now: now})
	node := nodeByName(t, view, "node-a")
	if node.Auth == nil || node.Auth.TokenHint != "0f3a…" {
		t.Fatalf("auth view = %+v", node.Auth)
	}
	if node.Auth.Required {
		t.Fatalf("auth view must carry the required flag: %+v", node.Auth)
	}
	if node.Record == nil {
		t.Fatal("in-process consumers still need the parsed record")
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("marshal view: %v", err)
	}
	if strings.Contains(string(encoded), "0f3a-secret-token") {
		t.Fatalf("token leaked into the view JSON: %s", encoded)
	}
	if strings.Contains(string(encoded), `"record"`) {
		t.Fatalf("the parsed record must never be marshalled: %s", encoded)
	}
}
