package knowledge

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// TestLockRecordPIDReadsRecordWithoutStalenessGate 钉住"诊断口径"与"仲裁口径"
// 的分工：锁超龄（> maxLockAge）时 LockHolderPID=0（不判 writer），但锁文件
// 仍记录着 pid，LockRecordPID 必须能读出来——现场（Windows 下老会话占用锁、
// 新二进制无法接管）的指引文案依赖它。
func TestLockRecordPIDReadsRecordWithoutStalenessGate(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Mode = ModeOn
	cfg.Workspace = t.TempDir()

	if got := LockRecordPID(cfg); got != 0 {
		t.Fatalf("无锁文件时 LockRecordPID=%d, want 0", got)
	}

	lockPath := cfg.storePath() + ".lock"
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"pid":` + strconv.Itoa(os.Getpid()) + `,"host":"test","started_at_unix_ms":1,"knowledge_version":1}`
	if err := os.WriteFile(lockPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	// 超龄但 pid 存活：仲裁视为陈旧（不可判 writer），诊断仍可读。
	old := time.Now().Add(-3 * time.Hour)
	if err := os.Chtimes(lockPath, old, old); err != nil {
		t.Fatal(err)
	}
	if got := LockHolderPID(cfg); got != 0 {
		t.Fatalf("超龄锁的 LockHolderPID=%d, want 0（仲裁口径不判 writer）", got)
	}
	if got := LockRecordPID(cfg); got != os.Getpid() {
		t.Fatalf("LockRecordPID=%d, want %d（诊断口径必须给出占用者）", got, os.Getpid())
	}

	// 内容不可解析时两个口径都返回 0。
	if err := os.WriteFile(lockPath, []byte("not-json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := LockRecordPID(cfg); got != 0 {
		t.Fatalf("损坏锁文件 LockRecordPID=%d, want 0", got)
	}
}
