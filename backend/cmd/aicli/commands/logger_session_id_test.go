package commands

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/aiclipaths"
)

// TestNewChatLogSessionIDUsesSessionFormat 锁定 chat-logs 会话目录名格式：
// session_YYYYMMDDHHMMSS_<8 位字母数字>，替代旧的
// YYYYMMDD_HHMMSS_mmm_<hex> 日期式 ID。
func TestNewChatLogSessionIDUsesSessionFormat(t *testing.T) {
	id := newChatLogSessionID()
	if !regexp.MustCompile(`^session_\d{14}_[0-9A-Za-z]{8}$`).MatchString(id) {
		t.Fatalf("unexpected chat log session id: %q", id)
	}

	logger := NewChatLogger("provider", "openai", "model", false, "")
	logger.sessionID = id
	logger.logDir = t.TempDir()
	logger.sessionLog.SessionID = id
	// 强制 partitionAt 走「从会话 ID 解析时间戳」的分支，验证新格式可解析。
	logger.sessionLog.StartTime = time.Time{}

	dir := logger.SessionDirPath()
	if got := filepath.Base(dir); got != id {
		t.Fatalf("session dir base = %q, want %q", got, id)
	}
	stamp, ok := aiclipaths.ParseTimestampedSessionIDTime(id)
	if !ok {
		t.Fatalf("new chat log session id must stay parseable: %q", id)
	}
	want := filepath.Join(logger.logDir,
		stamp.Local().Format("2006"), stamp.Local().Format("01"), stamp.Local().Format("02"), id)
	if dir != want {
		t.Fatalf("session dir = %q, want %q", dir, want)
	}
}

// TestChatLoggerAdoptsRuntimeSessionIDAsDirectoryName 锁定核心契约：
// chat-logs 会话目录名就是运行时会话 ID，而不是另行生成的 chat log ID。
func TestChatLoggerAdoptsRuntimeSessionIDAsDirectoryName(t *testing.T) {
	logDir := t.TempDir()
	logger := NewChatLogger("provider", "openai", "model", false, "")
	if err := logger.SetLogDir(logDir); err != nil {
		t.Fatalf("SetLogDir: %v", err)
	}

	provisionalDir := logger.SessionDirPath()
	if provisionalDir == "" {
		t.Fatal("provisional session dir must not be empty")
	}

	runtimeID := "session_20260920131211_8fR4UAFV"
	logger.SetRuntimeSessionMetadata(runtimeID, "runtime session")

	if got := logger.sessionID; got != runtimeID {
		t.Fatalf("logger sessionID = %q, want %q", got, runtimeID)
	}
	if got := logger.sessionLog.SessionID; got != runtimeID {
		t.Fatalf("sessionLog.SessionID = %q, want %q", got, runtimeID)
	}
	if got := logger.sessionLog.RuntimeSessionID; got != runtimeID {
		t.Fatalf("sessionLog.RuntimeSessionID = %q, want %q", got, runtimeID)
	}

	dir := logger.SessionDirPath()
	if got := filepath.Base(dir); got != runtimeID {
		t.Fatalf("session dir base = %q, want %q", got, runtimeID)
	}
	want := filepath.Join(logDir, "2026", "09", "20", runtimeID)
	if dir != want {
		t.Fatalf("session dir = %q, want %q", dir, want)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("adopted session dir must exist: %v", err)
	}
	if _, err := os.Stat(provisionalDir); !os.IsNotExist(err) {
		t.Fatalf("provisional dir %q should be pruned, stat err = %v", provisionalDir, err)
	}
}

// TestChatLoggerRotateSessionWithIDUsesGivenID 锁定 /new 行为：轮转后的目录名
// 直接使用传入的运行时会话 ID，同时旧会话目录保留在磁盘上。
func TestChatLoggerRotateSessionWithIDUsesGivenID(t *testing.T) {
	logger := NewChatLogger("provider", "openai", "model", true, "")
	if err := logger.SetLogDir(t.TempDir()); err != nil {
		t.Fatalf("SetLogDir: %v", err)
	}

	logger.LogRequest(aicliLogScope{TurnID: "turn-1", RequestID: "req-1"}, map[string]string{"hello": "world"})
	if err := logger.FlushSession(); err != nil {
		t.Fatalf("FlushSession: %v", err)
	}
	previousDir := logger.SessionDirPath()

	nextID := "session_20260920131211_8fR4UAFV"
	if err := logger.RotateSessionWithID(nextID); err != nil {
		t.Fatalf("RotateSessionWithID: %v", err)
	}

	if got := logger.sessionID; got != nextID {
		t.Fatalf("rotated sessionID = %q, want %q", got, nextID)
	}
	if got := logger.sessionLog.SessionID; got != nextID {
		t.Fatalf("rotated sessionLog.SessionID = %q, want %q", got, nextID)
	}
	if got := filepath.Base(logger.SessionDirPath()); got != nextID {
		t.Fatalf("rotated dir base = %q, want %q", got, nextID)
	}
	if _, err := os.Stat(previousDir); err != nil {
		t.Fatalf("previous session dir must survive rotation: %v", err)
	}
}
