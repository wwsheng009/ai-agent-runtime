package sqliteutil

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/wwsheng009/ai-agent-runtime/internal/sqlitedriver"
)

// writeWALIndexHeader 构造一个最小可用的 -shm 首部（WalIndexHdr），用于在测试中
// 伪造上一代残留的 wal-index。
func writeWALIndexHeader(t *testing.T, shmPath string, isInit bool, pageSz int, mxFrame uint32) {
	t.Helper()
	buf := make([]byte, walIndexHdrSize)
	if isInit {
		buf[12] = 1
	}
	rawPageSz := uint16(pageSz)
	if pageSz == sqliteMaxPageSize {
		// SQLite 用 1 表示 64KiB。
		rawPageSz = 1
	}
	binary.LittleEndian.PutUint16(buf[14:16], rawPageSz)
	binary.LittleEndian.PutUint32(buf[16:20], mxFrame)
	if err := os.WriteFile(shmPath, buf, 0o600); err != nil {
		t.Fatalf("write shm header: %v", err)
	}
}

func TestFilePathFromDSN(t *testing.T) {
	cases := []struct {
		name   string
		dsn    string
		want   string
		wantOK bool
	}{
		{name: "empty", dsn: "", wantOK: false},
		{name: "memory", dsn: ":memory:", wantOK: false},
		{
			name:   "memory uri",
			dsn:    "file:agent-control-agent-registry-x?mode=memory&cache=shared",
			wantOK: false,
		},
		{
			name:   "uri with pragma options",
			dsn:    "file:" + filepath.ToSlash(filepath.Join("tmp", "agent_control.sqlite")) + "?_txlock=immediate&_busy_timeout=5000",
			want:   filepath.Join("tmp", "agent_control.sqlite"),
			wantOK: true,
		},
		{
			name:   "plain path",
			dsn:    filepath.Join("tmp", "agent_control.sqlite"),
			want:   filepath.Join("tmp", "agent_control.sqlite"),
			wantOK: true,
		},
		{
			name:   "authority form is not guessed",
			dsn:    "file://host/share/db.sqlite",
			wantOK: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := FilePathFromDSN(tc.dsn)
			if ok != tc.wantOK {
				t.Fatalf("FilePathFromDSN(%q) ok=%v, want %v", tc.dsn, ok, tc.wantOK)
			}
			if ok && got != tc.want {
				t.Fatalf("FilePathFromDSN(%q) = %q, want %q", tc.dsn, got, tc.want)
			}
		})
	}
}

// TestReconcileOrphanedSidecarsRemovesSHMWithoutWAL 覆盖真实故障路径：主库被替换、
// -wal 被截断后只剩 -shm 残留。活跃 WAL 会话必然持有 -wal，因此该组合必为残留。
func TestReconcileOrphanedSidecarsRemovesSHMWithoutWAL(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "agent_control.sqlite")
	if err := os.WriteFile(dbPath, []byte("main"), 0o600); err != nil {
		t.Fatalf("write db: %v", err)
	}
	writeWALIndexHeader(t, dbPath+"-shm", true, 4096, 278)

	removed, err := ReconcileOrphanedSidecars(dbPath)
	if err != nil {
		t.Fatalf("ReconcileOrphanedSidecars: %v", err)
	}
	if !removed {
		t.Fatal("expected the orphaned -shm to be removed")
	}
	if _, err := os.Stat(dbPath + "-shm"); !os.IsNotExist(err) {
		t.Fatalf("expected -shm to be gone, stat err = %v", err)
	}
}

// TestReconcileOrphanedSidecarsRemovesOverclaimingSHM 覆盖“-wal 存在但装不下
// -shm 声称的帧数”的不一致：帧总是先落盘再更新索引，健康状态不会出现。
func TestReconcileOrphanedSidecarsRemovesOverclaimingSHM(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "agent_control.sqlite")
	if err := os.WriteFile(dbPath, []byte("main"), 0o600); err != nil {
		t.Fatalf("write db: %v", err)
	}
	const (
		pageSz  = 4096
		mxFrame = 10
	)
	writeWALIndexHeader(t, dbPath+"-shm", true, pageSz, mxFrame)
	// 真实需要的长度远大于此。
	if err := os.WriteFile(dbPath+"-wal", make([]byte, 128), 0o600); err != nil {
		t.Fatalf("write wal: %v", err)
	}

	removed, err := ReconcileOrphanedSidecars(dbPath)
	if err != nil {
		t.Fatalf("ReconcileOrphanedSidecars: %v", err)
	}
	if !removed {
		t.Fatal("expected the over-claiming -shm to be removed")
	}
}

// TestReconcileOrphanedSidecarsKeepsConsistentPair 是防误删的关键回归：自洽的
// -wal/-shm 必须原样保留。
func TestReconcileOrphanedSidecarsKeepsConsistentPair(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "agent_control.sqlite")
	if err := os.WriteFile(dbPath, []byte("main"), 0o600); err != nil {
		t.Fatalf("write db: %v", err)
	}
	const (
		pageSz  = 4096
		mxFrame = 10
	)
	writeWALIndexHeader(t, dbPath+"-shm", true, pageSz, mxFrame)
	required := walHeaderSize + mxFrame*(walFrameHdrSize+pageSz)
	if err := os.WriteFile(dbPath+"-wal", make([]byte, required), 0o600); err != nil {
		t.Fatalf("write wal: %v", err)
	}

	removed, err := ReconcileOrphanedSidecars(dbPath)
	if err != nil {
		t.Fatalf("ReconcileOrphanedSidecars: %v", err)
	}
	if removed {
		t.Fatal("a self-consistent -wal/-shm pair must never be removed")
	}
	if _, err := os.Stat(dbPath + "-shm"); err != nil {
		t.Fatalf("expected -shm to survive, stat err = %v", err)
	}
}

// TestReconcileOrphanedSidecarsConservativeCases 覆盖必须“不作为”的场景：
// 缺少 -shm、空 -shm、未初始化的 -shm，以及 mxFrame=0 的空 WAL。
func TestReconcileOrphanedSidecarsConservativeCases(t *testing.T) {
	t.Run("no shm", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "db.sqlite")
		if err := os.WriteFile(dbPath, []byte("main"), 0o600); err != nil {
			t.Fatalf("write db: %v", err)
		}
		removed, err := ReconcileOrphanedSidecars(dbPath)
		if err != nil || removed {
			t.Fatalf("expected no-op, got removed=%v err=%v", removed, err)
		}
	})

	t.Run("empty shm", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "db.sqlite")
		if err := os.WriteFile(dbPath, []byte("main"), 0o600); err != nil {
			t.Fatalf("write db: %v", err)
		}
		if err := os.WriteFile(dbPath+"-shm", nil, 0o600); err != nil {
			t.Fatalf("write shm: %v", err)
		}
		removed, err := ReconcileOrphanedSidecars(dbPath)
		if err != nil || removed {
			t.Fatalf("expected no-op for an empty -shm, got removed=%v err=%v", removed, err)
		}
	})

	t.Run("uninitialized shm", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "db.sqlite")
		if err := os.WriteFile(dbPath, []byte("main"), 0o600); err != nil {
			t.Fatalf("write db: %v", err)
		}
		// isInit=0 的索引 SQLite 会自行重建，无需也不应代删。
		writeWALIndexHeader(t, dbPath+"-shm", false, 4096, 99)
		if err := os.WriteFile(dbPath+"-wal", make([]byte, 64), 0o600); err != nil {
			t.Fatalf("write wal: %v", err)
		}
		removed, err := ReconcileOrphanedSidecars(dbPath)
		if err != nil || removed {
			t.Fatalf("expected no-op for an uninitialized -shm, got removed=%v err=%v", removed, err)
		}
	})

	t.Run("mxFrame zero with empty wal", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "db.sqlite")
		if err := os.WriteFile(dbPath, []byte("main"), 0o600); err != nil {
			t.Fatalf("write db: %v", err)
		}
		// 健康状态：刚打开、尚无写入的 WAL 会话就是 0 字节 -wal + mxFrame=0。
		writeWALIndexHeader(t, dbPath+"-shm", true, 4096, 0)
		if err := os.WriteFile(dbPath+"-wal", nil, 0o600); err != nil {
			t.Fatalf("write wal: %v", err)
		}
		removed, err := ReconcileOrphanedSidecars(dbPath)
		if err != nil || removed {
			t.Fatalf("expected no-op for an empty WAL, got removed=%v err=%v", removed, err)
		}
	})
}

func TestReconcileOrphanedSidecarsDSNSkipsMemory(t *testing.T) {
	for _, dsn := range []string{":memory:", "file:x?mode=memory&cache=shared"} {
		removed, err := ReconcileOrphanedSidecarsDSN(dsn)
		if err != nil || removed {
			t.Fatalf("dsn %q: expected no-op, got removed=%v err=%v", dsn, removed, err)
		}
	}
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read %s: %v", src, err)
	}
	if err := os.WriteFile(dst, data, 0o600); err != nil {
		t.Fatalf("write %s: %v", dst, err)
	}
}

// TestOpenFileRepairsProductionCorruptionShape 端到端复现生产故障形态：主库被替换、
// -wal 被截断为 0，而上一代的 -shm（声称有大量已提交帧）仍留在原处。修复前 SQLite
// 会采信这份陈旧 wal-index，按并不存在的帧数定位页面，首次写入即撕裂 B 树；
// 修复后 OpenFile 必须先丢弃陈旧 -shm，再正常打开并保持库健康。
func TestOpenFileRepairsProductionCorruptionShape(t *testing.T) {
	dir := t.TempDir()

	// 1) 造一个“上一代”库：WAL 模式 + 足够多的行，让 -shm 记下 mxFrame>0。
	staleDB := filepath.Join(dir, "stale.sqlite")
	seed, err := OpenFile(staleDB, true)
	if err != nil {
		t.Fatalf("open seed db: %v", err)
	}
	if _, err := seed.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY, payload TEXT)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	for i := 0; i < 400; i++ {
		if _, err := seed.Exec(`INSERT INTO t (payload) VALUES (?)`, strings.Repeat("x", 64)); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	// 连接保持打开时读取，此时 -wal/-shm 仍在磁盘上。
	staleSHM, err := os.ReadFile(staleDB + "-shm")
	if err != nil {
		t.Fatalf("read stale -shm: %v", err)
	}
	header, ok := readWALIndexHeader(staleDB + "-shm")
	if !ok || header.mxFrame == 0 {
		t.Fatalf("expected the seed -shm to claim committed frames, got ok=%v mxFrame=%d", ok, header.mxFrame)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("close seed db: %v", err)
	}

	// 2) 造一个“新一代”主库（干净关闭，无附属文件残留）。
	freshDB := filepath.Join(dir, "fresh.sqlite")
	fresh, err := OpenFile(freshDB, true)
	if err != nil {
		t.Fatalf("open fresh db: %v", err)
	}
	if _, err := fresh.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY, payload TEXT)`); err != nil {
		t.Fatalf("create fresh table: %v", err)
	}
	if _, err := fresh.Exec(`INSERT INTO t (payload) VALUES ('fresh')`); err != nil {
		t.Fatalf("insert fresh: %v", err)
	}
	if err := fresh.Close(); err != nil {
		t.Fatalf("close fresh db: %v", err)
	}

	// 3) 组装故障形态：新主库 + 上一代 -shm + 被截断为 0 的 -wal。
	victim := filepath.Join(dir, "victim.sqlite")
	copyFile(t, freshDB, victim)
	if err := os.WriteFile(victim+"-shm", staleSHM, 0o600); err != nil {
		t.Fatalf("plant stale -shm: %v", err)
	}
	if err := os.WriteFile(victim+"-wal", nil, 0o600); err != nil {
		t.Fatalf("truncate -wal: %v", err)
	}

	// 4) OpenFile 必须丢弃陈旧 -shm 后正常打开，且库保持健康、数据可读可写。
	db, err := OpenFile(victim, true)
	if err != nil {
		t.Fatalf("OpenFile on the corruption shape must succeed after repair: %v", err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	var integrity string
	if err := db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil {
		t.Fatalf("integrity_check: %v", err)
	}
	if !strings.EqualFold(strings.TrimSpace(integrity), "ok") {
		t.Fatalf("integrity_check = %q, want ok", integrity)
	}
	var payload string
	if err := db.QueryRowContext(ctx, `SELECT payload FROM t`).Scan(&payload); err != nil {
		t.Fatalf("read fresh row: %v", err)
	}
	if payload != "fresh" {
		t.Fatalf("payload = %q, want %q", payload, "fresh")
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO t (payload) VALUES ('after-repair')`); err != nil {
		t.Fatalf("write after repair must succeed: %v", err)
	}
}
