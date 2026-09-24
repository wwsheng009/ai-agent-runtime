package mesh

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeRawBinding writes a binding file verbatim so the tolerant-reader rules
// can be exercised (corrupt JSON, unknown schema, out-of-range port ...).
func writeRawBinding(t *testing.T, paths Paths, sessionID, body string) {
	t.Helper()
	path := paths.BindingPath(sessionID)
	if path == "" {
		t.Fatalf("BindingPath(%q) returned empty", sessionID)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestBindingRoundTrip(t *testing.T) {
	paths := testPaths(t)
	const sessionID = "session_20260924072950_ltYRU9tG"

	if err := TouchBinding(paths, BindingUpdate{
		SessionID:     sessionID,
		Host:          "127.0.0.1",
		Port:          55124,
		NodeID:        "node-8124-20260924T073012Z",
		WorkspacePath: `E:\projects\ai\ai-agent-runtime`,
	}); err != nil {
		t.Fatalf("TouchBinding() error = %v", err)
	}

	if got, want := paths.BindingPath(sessionID), filepath.Join(paths.Root, "bindings", sessionID+".json"); got != want {
		t.Fatalf("BindingPath() = %q, want %q", got, want)
	}

	binding, ok := LoadBinding(paths, sessionID)
	if !ok {
		t.Fatal("LoadBinding() ok = false after TouchBinding")
	}
	if binding.SessionID != sessionID {
		t.Fatalf("SessionID = %q, want %q", binding.SessionID, sessionID)
	}
	if binding.Preferred == nil || binding.Preferred.Port != 55124 || binding.Preferred.Host != "127.0.0.1" {
		t.Fatalf("Preferred = %+v, want 127.0.0.1:55124", binding.Preferred)
	}
	if binding.LastNodeID != "node-8124-20260924T073012Z" {
		t.Fatalf("LastNodeID = %q", binding.LastNodeID)
	}
	if binding.LastWorkspacePath != `E:\projects\ai\ai-agent-runtime` {
		t.Fatalf("LastWorkspacePath = %q", binding.LastWorkspacePath)
	}
	if binding.UpdatedAt.IsZero() {
		t.Fatal("UpdatedAt must be set")
	}

	raw, err := os.ReadFile(paths.BindingPath(sessionID))
	if err != nil {
		t.Fatalf("read binding: %v", err)
	}
	// 红线 M7：绑定是偏好记录，永远不含令牌。
	if strings.Contains(strings.ToLower(string(raw)), "token") {
		t.Fatalf("binding must never mention a token:\n%s", raw)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("binding is not valid JSON: %v", err)
	}
	if version, _ := decoded["schema_version"].(float64); int(version) != SchemaVersion {
		t.Fatalf("schema_version = %v, want %d", decoded["schema_version"], SchemaVersion)
	}
}

func TestLoadBindingRejectsUnusableFiles(t *testing.T) {
	paths := testPaths(t)

	// 缺失
	if _, ok := LoadBinding(paths, "session_missing"); ok {
		t.Fatal("LoadBinding() ok = true for a missing file")
	}

	// 损坏的 JSON
	writeRawBinding(t, paths, "session_corrupt", "{not json")
	if _, ok := LoadBinding(paths, "session_corrupt"); ok {
		t.Fatal("LoadBinding() ok = true for corrupt JSON")
	}

	// 未知 schema 版本：新版本写的绑定不参与本版本的粘性端口
	writeRawBinding(t, paths, "session_future",
		`{"schema_version":99,"session_id":"session_future","preferred":{"port":1234}}`)
	if _, ok := LoadBinding(paths, "session_future"); ok {
		t.Fatal("LoadBinding() ok = true for an unknown schema version")
	}

	// 端口越界
	writeRawBinding(t, paths, "session_badport",
		`{"schema_version":2,"session_id":"session_badport","preferred":{"port":70000}}`)
	if _, ok := LoadBinding(paths, "session_badport"); ok {
		t.Fatal("LoadBinding() ok = true for an out-of-range port")
	}

	// 没有 preferred 段（只有"上次服务者"信息）：对粘性端口不可用
	writeRawBinding(t, paths, "session_noaddr",
		`{"schema_version":2,"session_id":"session_noaddr","last_node_id":"node-1"}`)
	if _, ok := LoadBinding(paths, "session_noaddr"); ok {
		t.Fatal("LoadBinding() ok = true without a preferred address")
	}

	// 非法会话 ID
	if _, ok := LoadBinding(paths, ".."); ok {
		t.Fatal("LoadBinding(\"..\") ok = true")
	}
}

func TestBindingSanitizesSessionID(t *testing.T) {
	paths := testPaths(t)

	if err := SaveBinding(paths, SessionBinding{SessionID: "..", Preferred: &BindingAddr{Port: 8080}}); err == nil {
		t.Fatal("SaveBinding(\"..\") should fail")
	}
	if err := SaveBinding(paths, SessionBinding{SessionID: "", Preferred: &BindingAddr{Port: 8080}}); err == nil {
		t.Fatal("SaveBinding(\"\") should fail")
	}
	if err := SaveBinding(paths, SessionBinding{SessionID: "session_ok", Preferred: &BindingAddr{Port: 70000}}); err == nil {
		t.Fatal("SaveBinding() should reject out-of-range ports")
	}

	// 路径穿越被规整到 bindings 目录内（文件名替换非法字符）。
	if err := TouchBinding(paths, BindingUpdate{SessionID: "../escape", Host: "127.0.0.1", Port: 8123}); err != nil {
		t.Fatalf("TouchBinding(\"../escape\") error = %v", err)
	}
	if got := filepath.Dir(paths.BindingPath("../escape")); got != paths.Bindings {
		t.Fatalf("sanitized session id escaped the bindings dir: %s", got)
	}
	if _, ok := LoadBinding(paths, "../escape"); !ok {
		t.Fatal("LoadBinding() should read the sanitized binding back")
	}
}

// TestTouchBindingRefreshesServerAndAddress 覆盖 sticky 端口的写侧语义：
// 同一会话再次成为活动会话时，绑定整体替换为最新的地址与服务者。
func TestTouchBindingRefreshesServerAndAddress(t *testing.T) {
	paths := testPaths(t)
	const sessionID = "session_touch"

	if err := TouchBinding(paths, BindingUpdate{SessionID: sessionID, Host: "127.0.0.1", Port: 6100, NodeID: "node-a"}); err != nil {
		t.Fatalf("first TouchBinding() error = %v", err)
	}
	first, ok := LoadBinding(paths, sessionID)
	if !ok {
		t.Fatal("first LoadBinding() ok = false")
	}

	if err := TouchBinding(paths, BindingUpdate{
		SessionID: sessionID, Host: "127.0.0.1", Port: 6200,
		NodeID: "node-b", WorkspacePath: `E:\work\other`,
	}); err != nil {
		t.Fatalf("second TouchBinding() error = %v", err)
	}
	second, ok := LoadBinding(paths, sessionID)
	if !ok {
		t.Fatal("second LoadBinding() ok = false")
	}
	if second.Preferred.Port != 6200 {
		t.Fatalf("Preferred.Port = %d, want 6200 (latest server wins)", second.Preferred.Port)
	}
	if second.LastNodeID != "node-b" || second.LastWorkspacePath != `E:\work\other` {
		t.Fatalf("last server = %q / %q, want node-b / E:\\work\\other", second.LastNodeID, second.LastWorkspacePath)
	}
	if second.UpdatedAt.Before(first.UpdatedAt) {
		t.Fatalf("UpdatedAt went backwards: %v -> %v", first.UpdatedAt, second.UpdatedAt)
	}
}

// TestBindingFailsClosedWithoutMeshRoot 守住 §2.4 的「不做 CWD 回退」：
// 没有网格根目录时读不到、写不报错、也绝不落盘到当前目录。
func TestBindingFailsClosedWithoutMeshRoot(t *testing.T) {
	paths := Paths{}

	if _, ok := LoadBinding(paths, "session_x"); ok {
		t.Fatal("LoadBinding() ok = true without a mesh root")
	}
	if path := paths.BindingPath("session_x"); path != "" {
		t.Fatalf("BindingPath() = %q, want empty without a mesh root", path)
	}
	if err := TouchBinding(paths, BindingUpdate{SessionID: "session_x", Port: 6100}); err != nil {
		t.Fatalf("TouchBinding() must be a silent no-op without a mesh root, got %v", err)
	}
	if err := SaveBinding(paths, SessionBinding{SessionID: "session_x", Preferred: &BindingAddr{Port: 6100}}); err != nil {
		t.Fatalf("SaveBinding() must be a silent no-op without a mesh root, got %v", err)
	}
}

func TestProcessEndpointLifecycle(t *testing.T) {
	ClearProcessEndpoint()
	t.Cleanup(ClearProcessEndpoint)

	if _, _, ok := ProcessEndpoint(); ok {
		t.Fatal("ProcessEndpoint() ok = true before the server is up")
	}

	// 非法端口不得建立状态（失败的监听不能覆盖可用地址）。
	SetProcessEndpoint("127.0.0.1", 0)
	if _, _, ok := ProcessEndpoint(); ok {
		t.Fatal("invalid port must not record an endpoint")
	}

	SetProcessEndpoint("127.0.0.1", 45871)
	host, port, ok := ProcessEndpoint()
	if !ok || host != "127.0.0.1" || port != 45871 {
		t.Fatalf("ProcessEndpoint() = (%q,%d,%v), want (127.0.0.1,45871,true)", host, port, ok)
	}

	ClearProcessEndpoint()
	if _, _, ok := ProcessEndpoint(); ok {
		t.Fatal("ProcessEndpoint() ok = true after ClearProcessEndpoint")
	}
}
