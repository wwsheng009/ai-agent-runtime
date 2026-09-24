package mesh

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testPaths(t *testing.T) Paths {
	t.Helper()
	paths := pathsFor(filepath.Join(t.TempDir(), "mesh"), PathSourceEnv)
	if err := paths.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs() = %v", err)
	}
	return paths
}

func sampleNodeRecord() NodeRecord {
	started := time.Date(2026, 9, 24, 7, 30, 12, 0, time.UTC)
	return NodeRecord{
		SchemaVersion: SchemaVersion,
		NodeID:        "node-8124-20260924T073012Z",
		PID:           8124,
		Kind:          "chat",
		Process: ProcessInfo{
			StartedAt: started,
			Exe:       `E:\projects\ai\ai-agent-runtime\backend\dist\aicli.exe`,
			Version:   "1.4.2",
			BuildTime: "2026-09-20T11:02:33Z",
			ParentPID: 4200,
			Origin:    "cli",
		},
		Endpoint: &EndpointInfo{
			Scheme:      "http",
			Host:        "127.0.0.1",
			Port:        55124,
			Loopback:    true,
			BaseURL:     "http://127.0.0.1:55124",
			WebBaseURL:  "http://127.0.0.1:55124/web",
			ManifestURL: "http://127.0.0.1:55124/debug/endpoints",
		},
		Auth: &AuthInfo{Mode: "loopback-dev", Required: false, Token: "0f3a-secret", TokenSource: "random"},
		Session: &SessionInfo{
			ID:          "session_20260924072950_ltYRU9tG",
			Title:       "优化会话切换",
			Busy:        true,
			TurnID:      "turn_1",
			ActivatedAt: started.Add(2 * time.Second),
		},
		Workspace:    &WorkspaceInfo{Path: `E:\projects\ai\ai-agent-runtime`, Name: "ai-agent-runtime"},
		Capabilities: []string{"manifest", "screen", "status", "events", "invoke", "input", "sessions", "mesh"},
		Liveness: LivenessInfo{
			StartedAt:       started,
			UpdatedAt:       started.Add(90 * time.Second),
			HeartbeatAt:     started.Add(90 * time.Second),
			HeartbeatTTLSec: 90,
			State:           "live",
		},
	}
}

func TestNodeRecordRoundTrip(t *testing.T) {
	paths := testPaths(t)
	record := sampleNodeRecord()

	if err := WriteNodeRecord(paths, record); err != nil {
		t.Fatalf("WriteNodeRecord() = %v", err)
	}
	file := ReadNodeFile(paths.NodePath(record.NodeID))
	if file.Err != nil {
		t.Fatalf("ReadNodeFile() err = %v", file.Err)
	}
	if file.State != NodeStateLive {
		t.Fatalf("State = %q, want %q", file.State, NodeStateLive)
	}
	got := *file.Record
	if got.NodeID != record.NodeID || got.PID != record.PID || got.Kind != record.Kind {
		t.Fatalf("identity mismatch: %+v", got)
	}
	if got.Endpoint == nil || got.Endpoint.Port != record.Endpoint.Port || got.Endpoint.WebBaseURL != record.Endpoint.WebBaseURL {
		t.Fatalf("endpoint mismatch: %+v", got.Endpoint)
	}
	if got.Auth == nil || got.Auth.Token != record.Auth.Token || got.Auth.Mode != record.Auth.Mode {
		t.Fatalf("auth mismatch: %+v", got.Auth)
	}
	if got.Session == nil || got.Session.ID != record.Session.ID || !got.Session.Busy || got.Session.TurnID != "turn_1" {
		t.Fatalf("session mismatch: %+v", got.Session)
	}
	if got.Workspace == nil || got.Workspace.Path != record.Workspace.Path {
		t.Fatalf("workspace mismatch: %+v", got.Workspace)
	}
	if got.Liveness.HeartbeatTTLSec != 90 || !got.Liveness.HeartbeatAt.Equal(record.Liveness.HeartbeatAt) {
		t.Fatalf("liveness mismatch: %+v", got.Liveness)
	}
	if len(got.Capabilities) != len(record.Capabilities) {
		t.Fatalf("capabilities mismatch: %v", got.Capabilities)
	}
}

// TestNodeRecordWireShape locks the JSON contract from architecture §3.1: the
// CLI, the HTTP views and the E2E assertions all read these key paths.
func TestNodeRecordWireShape(t *testing.T) {
	data, err := json.Marshal(sampleNodeRecord())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if raw["schema_version"] != float64(SchemaVersion) {
		t.Fatalf("schema_version = %v, want %d", raw["schema_version"], SchemaVersion)
	}
	for _, key := range []string{"node_id", "pid", "kind", "process", "endpoint", "auth", "session", "workspace", "capabilities", "liveness"} {
		if _, ok := raw[key]; !ok {
			t.Fatalf("missing top-level key %q in %s", key, string(data))
		}
	}
	process, _ := raw["process"].(map[string]any)
	for _, key := range []string{"started_at", "exe", "version", "build_time", "parent_pid", "origin", "spawned_by"} {
		if _, ok := process[key]; !ok {
			t.Fatalf("missing process.%s", key)
		}
	}
	endpoint, _ := raw["endpoint"].(map[string]any)
	for _, key := range []string{"scheme", "host", "port", "loopback", "base_url", "web_base_url", "manifest_url"} {
		if _, ok := endpoint[key]; !ok {
			t.Fatalf("missing endpoint.%s", key)
		}
	}
	auth, _ := raw["auth"].(map[string]any)
	for _, key := range []string{"mode", "required", "token", "token_source"} {
		if _, ok := auth[key]; !ok {
			t.Fatalf("missing auth.%s", key)
		}
	}
	session, _ := raw["session"].(map[string]any)
	for _, key := range []string{"id", "title", "busy", "turn_id", "activated_at"} {
		if _, ok := session[key]; !ok {
			t.Fatalf("missing session.%s", key)
		}
	}
	liveness, _ := raw["liveness"].(map[string]any)
	for _, key := range []string{"started_at", "updated_at", "heartbeat_at", "heartbeat_ttl_sec", "state"} {
		if _, ok := liveness[key]; !ok {
			t.Fatalf("missing liveness.%s", key)
		}
	}
	// Timestamps are UTC RFC3339 with a Z suffix (no local offsets).
	if ts, _ := process["started_at"].(string); !strings.HasSuffix(ts, "Z") {
		t.Fatalf("process.started_at = %q, want a UTC (Z) timestamp", ts)
	}
}

// TestNodeRecordOmitsEmptyEndpoint covers the "node without loopback" shape:
// the whole endpoint section must disappear, not appear as nulls.
func TestNodeRecordOmitsEmptyEndpoint(t *testing.T) {
	record := sampleNodeRecord()
	record.Endpoint = nil
	record.Auth = nil
	record.Session = nil

	data, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"endpoint", "auth", "session"} {
		if _, ok := raw[key]; ok {
			t.Fatalf("expected %q to be omitted, got %s", key, string(data))
		}
	}
}

func TestWriteNodeRecordLeavesNoTempFiles(t *testing.T) {
	paths := testPaths(t)
	record := sampleNodeRecord()
	if err := WriteNodeRecord(paths, record); err != nil {
		t.Fatalf("WriteNodeRecord() = %v", err)
	}
	// Rewrite: the replace path must not leave temp files behind either.
	record.Session.Busy = false
	if err := WriteNodeRecord(paths, record); err != nil {
		t.Fatalf("second WriteNodeRecord() = %v", err)
	}
	entries, err := os.ReadDir(paths.Nodes)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != record.NodeID+".json" {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("nodes dir = %v, want only %s.json", names, record.NodeID)
	}
	file := ReadNodeFile(paths.NodePath(record.NodeID))
	if file.Record == nil || file.Record.Session.Busy {
		t.Fatalf("rewrite did not land: %+v", file.Record)
	}
}

func TestWriteNodeRecordRequiresNodeID(t *testing.T) {
	paths := testPaths(t)
	record := sampleNodeRecord()
	record.NodeID = "  "
	if err := WriteNodeRecord(paths, record); err == nil {
		t.Fatal("WriteNodeRecord with empty node id = nil, want error")
	}
	entries, _ := os.ReadDir(paths.Nodes)
	if len(entries) != 0 {
		t.Fatalf("nodes dir not empty: %v", entries)
	}
}

// TestWriteNodeRecordFailClosedWithoutRoot pins the no-home contract: writing
// must be a silent no-op instead of creating a CWD-relative directory.
func TestWriteNodeRecordFailClosedWithoutRoot(t *testing.T) {
	clearHomeEnv(t)
	paths := ResolvePaths()
	if paths.Enabled() {
		t.Fatalf("test env resolved a mesh root: %q", paths.Root)
	}
	if err := WriteNodeRecord(paths, sampleNodeRecord()); err != nil {
		t.Fatalf("WriteNodeRecord on fail-closed layout = %v, want nil", err)
	}
	if err := DeleteNodeRecord(paths, "node-1"); err != nil {
		t.Fatalf("DeleteNodeRecord on fail-closed layout = %v, want nil", err)
	}
	if files := ListNodeFiles(paths); len(files) != 0 {
		t.Fatalf("ListNodeFiles on fail-closed layout = %v, want empty", files)
	}
}

func TestReadNodeFileToleratesGarbage(t *testing.T) {
	paths := testPaths(t)
	cases := []struct {
		name    string
		content string
	}{
		{name: "empty file", content: ""},
		{name: "truncated json", content: `{"schema_version":2,"node_id":"node-1",`},
		{name: "not json at all", content: "not json\n"},
		{name: "wrong type", content: `{"schema_version":"two"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(paths.Nodes, "node-broken-20260101T000000Z.json")
			if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
				t.Fatalf("write fixture: %v", err)
			}
			file := ReadNodeFile(path) // must not panic
			if file.State != NodeStateUnknown {
				t.Fatalf("State = %q, want %q", file.State, NodeStateUnknown)
			}
			if file.Record != nil {
				t.Fatalf("Record = %+v, want nil", file.Record)
			}
			if file.Err == nil {
				t.Fatal("Err = nil, want a parse error")
			}
			_ = os.Remove(path)
		})
	}
}

func TestReadNodeFileMissingIsUnknown(t *testing.T) {
	paths := testPaths(t)
	file := ReadNodeFile(paths.NodePath("node-1-20260101T000000Z"))
	if file.State != NodeStateUnknown || file.Record != nil || file.Err == nil {
		t.Fatalf("missing file = %+v, want unknown with error", file)
	}
}

// TestReadNodeFileUnknownSchema: an unknown major version is shown but never
// trusted (no ownership decisions, no calls) — architecture §3.6.
func TestReadNodeFileUnknownSchema(t *testing.T) {
	paths := testPaths(t)
	path := filepath.Join(paths.Nodes, "node-99-20260101T000000Z.json")
	body := `{"schema_version":99,"node_id":"node-99-20260101T000000Z","pid":99,"liveness":{"state":"live"}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	file := ReadNodeFile(path)
	if file.State != NodeStateUnknown {
		t.Fatalf("State = %q, want %q", file.State, NodeStateUnknown)
	}
	if !errors.Is(file.Err, ErrUnknownSchema) {
		t.Fatalf("Err = %v, want ErrUnknownSchema", file.Err)
	}
	if file.Record == nil || file.Record.PID != 99 {
		t.Fatalf("Record = %+v, want the parsed display copy", file.Record)
	}
}

func TestReadNodeFileDeclaredStates(t *testing.T) {
	paths := testPaths(t)
	cases := map[string]NodeState{
		"live":    NodeStateLive,
		"stale":   NodeStateStale,
		"stopped": NodeStateStopped,
		"":        NodeStateLive, // writer declared nothing -> assume live
		"bogus":   NodeStateLive,
	}
	for declared, want := range cases {
		record := sampleNodeRecord()
		record.Liveness.State = declared
		if err := WriteNodeRecord(paths, record); err != nil {
			t.Fatalf("WriteNodeRecord(%q) = %v", declared, err)
		}
		file := ReadNodeFile(paths.NodePath(record.NodeID))
		if file.State != want {
			t.Fatalf("declared %q -> State %q, want %q", declared, file.State, want)
		}
	}
}

func TestListNodeFilesSkipsNonRecords(t *testing.T) {
	paths := testPaths(t)
	first := sampleNodeRecord()
	first.NodeID = "node-100-20260101T000000Z"
	first.PID = 100
	second := sampleNodeRecord()
	second.NodeID = "node-200-20260101T000000Z"
	second.PID = 200
	for _, record := range []NodeRecord{second, first} {
		if err := WriteNodeRecord(paths, record); err != nil {
			t.Fatalf("WriteNodeRecord(%s) = %v", record.NodeID, err)
		}
	}
	// Noise that must be ignored.
	if err := os.WriteFile(filepath.Join(paths.Nodes, "notes.txt"), []byte("ignore me"), 0o600); err != nil {
		t.Fatalf("write noise: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(paths.Nodes, "nested.json"), 0o700); err != nil {
		t.Fatalf("mkdir noise: %v", err)
	}

	files := ListNodeFiles(paths)
	if len(files) != 2 {
		t.Fatalf("ListNodeFiles = %d files, want 2", len(files))
	}
	if files[0].Record.NodeID != first.NodeID || files[1].Record.NodeID != second.NodeID {
		t.Fatalf("order = %s,%s; want sorted by path", files[0].Path, files[1].Path)
	}
}

func TestListNodeFilesWithoutDir(t *testing.T) {
	paths := pathsFor(filepath.Join(t.TempDir(), "mesh-missing"), PathSourceEnv)
	if files := ListNodeFiles(paths); len(files) != 0 {
		t.Fatalf("ListNodeFiles = %v, want empty for a missing directory", files)
	}
}

func TestDeleteNodeRecordIdempotent(t *testing.T) {
	paths := testPaths(t)
	record := sampleNodeRecord()
	if err := WriteNodeRecord(paths, record); err != nil {
		t.Fatalf("WriteNodeRecord() = %v", err)
	}
	if err := DeleteNodeRecord(paths, record.NodeID); err != nil {
		t.Fatalf("DeleteNodeRecord() = %v", err)
	}
	if _, err := os.Stat(paths.NodePath(record.NodeID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("record still present: %v", err)
	}
	if err := DeleteNodeRecord(paths, record.NodeID); err != nil {
		t.Fatalf("second DeleteNodeRecord() = %v", err)
	}
}

func TestWriteFileAtomicReplacesExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "value.json")
	if err := writeFileAtomic(path, []byte("first\n"), 0o600); err != nil {
		t.Fatalf("writeFileAtomic() = %v", err)
	}
	if err := writeFileAtomic(path, []byte("second\n"), 0o600); err != nil {
		t.Fatalf("second writeFileAtomic() = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "second\n" {
		t.Fatalf("content = %q, want %q", string(data), "second\n")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("dir = %v, want only the final file", entries)
	}
}

func TestNowUTCTruncatesToSeconds(t *testing.T) {
	now := NowUTC()
	if now.Location() != time.UTC {
		t.Fatalf("location = %v, want UTC", now.Location())
	}
	if now.Nanosecond() != 0 {
		t.Fatalf("nanoseconds = %d, want 0", now.Nanosecond())
	}
}
