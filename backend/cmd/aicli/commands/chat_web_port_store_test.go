package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isolateChatWebPortHome 把 ~ 指向临时目录，避免测试污染真实用户目录。
func isolateChatWebPortHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("AICLI_WEB_PORTS_DIR", "")
	resetChatWebPortRuntimeInfoForTest()
	t.Cleanup(resetChatWebPortRuntimeInfoForTest)
	return home
}

func TestChatWebPortRecordRoundTrip(t *testing.T) {
	home := isolateChatWebPortHome(t)

	const sessionID = "session_20260922173838_lsirc1la"
	if err := SaveChatWebPortRecord(sessionID, 54321, "127.0.0.1"); err != nil {
		t.Fatalf("SaveChatWebPortRecord() error = %v", err)
	}

	wantPath := filepath.Join(home, ".aicli", "web-ports", sessionID+".json")
	if got := ChatWebPortRecordPath(sessionID); got != wantPath {
		t.Fatalf("ChatWebPortRecordPath() = %q, want %q", got, wantPath)
	}
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("record file not written: %v", err)
	}
	// 原子写不应残留临时文件。
	if entries, err := os.ReadDir(filepath.Dir(wantPath)); err == nil {
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), ".tmp") {
				t.Fatalf("leftover temp file %q", entry.Name())
			}
		}
	}

	record, ok := LoadChatWebPortRecord(sessionID)
	if !ok {
		t.Fatal("LoadChatWebPortRecord() ok = false, want true")
	}
	if record.Port != 54321 {
		t.Fatalf("record.Port = %d, want 54321", record.Port)
	}
	if record.Host != "127.0.0.1" {
		t.Fatalf("record.Host = %q, want 127.0.0.1", record.Host)
	}
	if record.SessionID != sessionID {
		t.Fatalf("record.SessionID = %q, want %q", record.SessionID, sessionID)
	}
	if record.UpdatedAt.IsZero() {
		t.Fatal("record.UpdatedAt is zero")
	}
}

func TestChatWebPortRecordMissingOrCorruptIsNotAnError(t *testing.T) {
	isolateChatWebPortHome(t)

	if _, ok := LoadChatWebPortRecord("session_missing"); ok {
		t.Fatal("missing record should report ok = false")
	}

	path := ChatWebPortRecordPath("session_corrupt")
	if path == "" {
		t.Fatal("ChatWebPortRecordPath() returned empty for a valid session id")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not-json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := LoadChatWebPortRecord("session_corrupt"); ok {
		t.Fatal("corrupt record should report ok = false")
	}

	// 端口越界同样视为无效记录。
	if err := os.WriteFile(path, []byte(`{"session_id":"session_corrupt","port":70000}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := LoadChatWebPortRecord("session_corrupt"); ok {
		t.Fatal("out-of-range port should report ok = false")
	}
}

func TestChatWebPortRecordSanitizesSessionID(t *testing.T) {
	home := isolateChatWebPortHome(t)

	if got := ChatWebPortRecordPath(".."); got != "" {
		t.Fatalf("ChatWebPortRecordPath(\"..\") = %q, want empty", got)
	}
	if err := SaveChatWebPortRecord("..", 8080, "127.0.0.1"); err == nil {
		t.Fatal("SaveChatWebPortRecord(\"..\") should fail")
	}
	if err := SaveChatWebPortRecord("", 8080, "127.0.0.1"); err == nil {
		t.Fatal("SaveChatWebPortRecord(\"\") should fail")
	}
	if err := SaveChatWebPortRecord("session_ok", 70000, "127.0.0.1"); err == nil {
		t.Fatal("SaveChatWebPortRecord() should reject out-of-range ports")
	}

	// 带路径分隔符的 ID 必须被规整到同一目录内，不能逃逸。
	if err := SaveChatWebPortRecord("../escape", 8123, "127.0.0.1"); err != nil {
		t.Fatalf("SaveChatWebPortRecord() error = %v", err)
	}
	record, ok := LoadChatWebPortRecord("../escape")
	if !ok || record.Port != 8123 {
		t.Fatalf("sanitized round trip failed: record=%+v ok=%v", record, ok)
	}
	storeDir := filepath.Join(home, ".aicli", "web-ports")
	if got := filepath.Dir(ChatWebPortRecordPath("../escape")); got != storeDir {
		t.Fatalf("sanitized path dir = %q, want %q", got, storeDir)
	}
	if _, err := os.Stat(filepath.Join(home, ".aicli", "escape.json")); err == nil {
		t.Fatal("sanitized session id escaped the web-ports directory")
	}
}

func TestChatWebPortRecordDirEnvOverride(t *testing.T) {
	isolateChatWebPortHome(t)
	override := t.TempDir()
	t.Setenv("AICLI_WEB_PORTS_DIR", override)

	if err := SaveChatWebPortRecord("session_env", 6100, "127.0.0.1"); err != nil {
		t.Fatalf("SaveChatWebPortRecord() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(override, "session_env.json")); err != nil {
		t.Fatalf("record not written to override dir: %v", err)
	}
}

func TestPersistChatWebPortForSessionUsesRuntimeInfo(t *testing.T) {
	isolateChatWebPortHome(t)

	// 未启动 loopback 服务器时不得写入任何档案。
	persistChatWebPortForSession("session_no_server")
	if _, ok := LoadChatWebPortRecord("session_no_server"); ok {
		t.Fatal("no runtime listener should not create a record")
	}

	SetChatWebPortRuntimeInfo(45871, "127.0.0.1")
	persistChatWebPortForSession("session_with_server")

	record, ok := LoadChatWebPortRecord("session_with_server")
	if !ok {
		t.Fatal("expected record after SetChatWebPortRuntimeInfo")
	}
	if record.Port != 45871 || record.Host != "127.0.0.1" {
		t.Fatalf("record = %+v, want port 45871 host 127.0.0.1", record)
	}

	// 非法端口不得覆盖进程级状态。
	SetChatWebPortRuntimeInfo(0, "127.0.0.1")
	if port, _, ok := ChatWebPortRuntimeInfo(); !ok || port != 45871 {
		t.Fatalf("invalid port must not replace runtime info: port=%d ok=%v", port, ok)
	}
}
