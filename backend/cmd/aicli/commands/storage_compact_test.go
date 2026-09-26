package commands

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/aiclipaths"
	_ "github.com/wwsheng009/ai-agent-runtime/internal/sqlitedriver"
)

// storageTestSeedDB 造一个「有 freelist 的胖库」：auto_vacuum 取调用方指定值，
// 写入 ~4MiB 数据后删掉大部分行——空闲页留在 freelist（INCREMENTAL 模式下
// 不会归还给文件系统），正是离线压缩要回收的对象。
func storageTestSeedDB(t *testing.T, path string, autoVacuum string) {
	t.Helper()
	db, err := sql.Open("sqlite3", path)
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, storageExec(t.Context(), db, "PRAGMA busy_timeout=5000"))
	if autoVacuum != "" {
		require.NoError(t, storageExec(t.Context(), db, "PRAGMA auto_vacuum="+autoVacuum))
	}
	require.NoError(t, storageExec(t.Context(), db, `CREATE TABLE payload (id INTEGER PRIMARY KEY, body BLOB)`))
	const rows = 64
	for index := 0; index < rows; index++ {
		_, err := db.ExecContext(t.Context(), `INSERT INTO payload (body) VALUES (?)`, bytes.Repeat([]byte("x"), 64<<10))
		require.NoError(t, err)
	}
	require.NoError(t, storageExec(t.Context(), db, `DELETE FROM payload WHERE id > 4`))
}

func storageTestOpen(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", path)
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func storageTestRun(t *testing.T, args ...string) (string, int) {
	t.Helper()
	code := statsTestCaptureExit(t)
	var buf bytes.Buffer
	cmd := NewStorageCommand()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs(args)
	require.NoError(t, cmd.Execute(), "输出: %s", buf.String())
	return buf.String(), *code
}

type storageCompactJSON struct {
	Reports []storageCompactReport `json:"reports"`
	Summary map[string]int         `json:"summary"`
}

func TestStorageCompactReclaimsFreePagesAndDisablesAutoVacuum(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.sqlite")
	storageTestSeedDB(t, path, "INCREMENTAL")

	before := storageTestOpen(t, path)
	var freePages, autoVacuum int64
	require.NoError(t, before.QueryRow("PRAGMA freelist_count").Scan(&freePages))
	require.NoError(t, before.QueryRow("PRAGMA auto_vacuum").Scan(&autoVacuum))
	require.Greater(t, freePages, int64(0), "fixture 必须留下空闲页")
	require.Equal(t, int64(2), autoVacuum, "fixture 必须处于 INCREMENTAL 模式")
	require.NoError(t, before.Close())

	out, code := storageTestRun(t, "compact", "--db", path, "--json")
	require.Equal(t, statsExitOK, code, out)

	var payload storageCompactJSON
	require.NoError(t, json.Unmarshal([]byte(out), &payload), out)
	require.Len(t, payload.Reports, 1)
	report := payload.Reports[0]
	require.Equal(t, "compacted", report.Status, out)
	require.Equal(t, "ok", report.IntegrityBefore, out)
	require.Equal(t, "ok", report.IntegrityAfter, out)
	require.Greater(t, report.FreePagesBefore, int64(0))
	require.Greater(t, report.BytesReclaimed, int64(0))
	require.Equal(t, int64(0), report.AutoVacuumAfter, "压缩必须把历史库的 auto_vacuum 转成 NONE")
	require.Equal(t, 1, payload.Summary["compacted"])

	after := storageTestOpen(t, path)
	t.Cleanup(func() { _ = after.Close() })
	var remaining, rows int64
	require.NoError(t, after.QueryRow("PRAGMA freelist_count").Scan(&remaining))
	require.NoError(t, after.QueryRow("PRAGMA auto_vacuum").Scan(&autoVacuum))
	require.NoError(t, after.QueryRow(`SELECT COUNT(*) FROM payload`).Scan(&rows))
	require.Equal(t, int64(0), remaining, "压缩后不应再有空闲页")
	require.Equal(t, int64(0), autoVacuum, "压缩后 auto_vacuum 应为 NONE")
	require.Equal(t, int64(4), rows, "数据必须完整保留")
	var integrity string
	require.NoError(t, after.QueryRow("PRAGMA integrity_check").Scan(&integrity))
	require.Equal(t, "ok", integrity)
}

func TestStorageCompactDryRunDoesNotModify(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.sqlite")
	storageTestSeedDB(t, path, "NONE")
	before := storageTestOpen(t, path)
	var freePages int64
	require.NoError(t, before.QueryRow("PRAGMA freelist_count").Scan(&freePages))
	require.Greater(t, freePages, int64(0))
	require.NoError(t, before.Close())

	out, code := storageTestRun(t, "compact", "--db", path, "--dry-run", "--json")
	require.Equal(t, statsExitOK, code, out)
	var payload storageCompactJSON
	require.NoError(t, json.Unmarshal([]byte(out), &payload), out)
	require.Equal(t, "planned", payload.Reports[0].Status, out)

	after := storageTestOpen(t, path)
	t.Cleanup(func() { _ = after.Close() })
	var remaining int64
	require.NoError(t, after.QueryRow("PRAGMA freelist_count").Scan(&remaining))
	require.Equal(t, freePages, remaining, "dry-run 不得改动库")
}

func TestStorageCompactRefusesWhileWriterHoldsLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.sqlite")
	storageTestSeedDB(t, path, "NONE")

	holder := storageTestOpen(t, path)
	t.Cleanup(func() { _ = holder.Close() })
	_, err := holder.Exec("BEGIN IMMEDIATE")
	require.NoError(t, err)
	_, err = holder.Exec(`INSERT INTO payload (body) VALUES ('pending')`)
	require.NoError(t, err)
	defer func() { _, _ = holder.Exec("ROLLBACK") }()

	out, code := storageTestRun(t, "compact", "--db", path, "--busy-timeout", "1", "--json")
	require.Equal(t, statsExitDeterministic, code, out)
	var payload storageCompactJSON
	require.NoError(t, json.Unmarshal([]byte(out), &payload), out)
	require.Equal(t, "in_use", payload.Reports[0].Status, out)
	require.Contains(t, payload.Reports[0].Error, "占用", out)
}

func TestStorageCompactSkipsAbsentFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "runtime.sqlite")
	out, code := storageTestRun(t, "compact", "--db", path, "--json")
	require.Equal(t, statsExitOK, code, out)
	var payload storageCompactJSON
	require.NoError(t, json.Unmarshal([]byte(out), &payload), out)
	require.Equal(t, "absent", payload.Reports[0].Status, out)
	_, statErr := filepath.Glob(path)
	require.NoError(t, statErr)
	require.NoFileExists(t, path, "缺失的库不得被创建")
}

func TestStorageCompactRejectsUnknownTarget(t *testing.T) {
	out, code := storageTestRun(t, "compact", "--target", "nope")
	require.Equal(t, statsExitUsage, code, out)
	require.Contains(t, out, "未知 --target")
}

func TestStorageCommandSurface(t *testing.T) {
	cmd := NewStorageCommand()
	child, _, err := cmd.Find([]string{"compact"})
	require.NoError(t, err)
	require.Equal(t, "compact", child.Name())
}

// TestStorageResolveTargetsDefaults 固定各目标的默认库文件名：这些路径必须与
// 运行时（sessionruntime.ResolvePaths CLI-local + aiclipaths 默认）一致，
// 否则离线压缩会打开错的库（甚至不存在）。
func TestStorageResolveTargetsDefaults(t *testing.T) {
	all, err := storageResolveTargets(storageCompactOptions{target: "all"})
	require.NoError(t, err)
	byName := map[string]string{}
	for _, target := range all {
		byName[target.Name] = filepath.Base(target.Path)
	}
	require.Equal(t, "session_runtime.sqlite", byName["runtime"])
	require.Equal(t, aiclipaths.DefaultSessionHistoryFileName, byName["history"])
	require.Equal(t, "artifacts.sqlite", byName["artifacts"])
	require.Equal(t, "usage_analytics.sqlite", byName["analytics"])
	require.Equal(t, "team_store.sqlite", byName["team"])
	require.Equal(t, "agent_control.sqlite", byName["agent-control"])
	require.Equal(t, "background.sqlite", byName["background"])

	history, err := storageResolveTargets(storageCompactOptions{target: "history"})
	require.NoError(t, err)
	require.Len(t, history, 1)
	require.Equal(t, "history", history[0].Name)
}
