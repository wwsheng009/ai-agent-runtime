package chat

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	_ "github.com/wwsheng009/ai-agent-runtime/internal/sqlitedriver"
)

func TestRuntimeStoreHealthProbeEnabledValues(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  bool
	}{
		{"", false},
		{"off", false},
		{"1", false},
		{"quick", true},
		{"QUICK", true},
		{" quick ", true},
	} {
		t.Setenv(RuntimeStoreHealthProbeEnv, tc.value)
		require.Equal(t, tc.want, runtimeStoreHealthProbeEnabled(), "value=%q", tc.value)
	}
}

// TestRuntimeStoreHealthProbePassesOnHealthyDatabase：开启探测后健康库正常打开。
func TestRuntimeStoreHealthProbePassesOnHealthyDatabase(t *testing.T) {
	t.Setenv(RuntimeStoreHealthProbeEnv, "quick")
	store, err := NewSQLiteRuntimeStore(&RuntimeStoreConfig{
		Path: filepath.Join(t.TempDir(), "healthy.sqlite"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	_, err = store.AppendEvent(context.Background(), runtimeevents.Event{
		Type:      "probe",
		SessionID: "s-1",
		Payload:   map[string]interface{}{"ok": true},
	})
	require.NoError(t, err)
	require.True(t, store.Opened())
}

// TestRuntimeStoreHealthProbeFailsClosedOnCorruptDatabase：开启探测后损坏库在
// 迁移前就以可见错误拒绝打开（fail-closed），不再继续写入。
func TestRuntimeStoreHealthProbeFailsClosedOnCorruptDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corrupt.sqlite")
	seedCorruptRuntimeStoreDB(t, path)
	assertRuntimeStoreDBIsCorrupt(t, path)

	t.Setenv(RuntimeStoreHealthProbeEnv, "quick")
	store, err := NewSQLiteRuntimeStore(&RuntimeStoreConfig{Path: path})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	_, err = store.AppendEvent(context.Background(), runtimeevents.Event{
		Type:      "probe",
		SessionID: "s-1",
		Payload:   map[string]interface{}{"ok": true},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "runtime store health probe")
}

// seedCorruptRuntimeStoreDB 造一个"能打开、但 quick_check 必定报错"的库：
// 先以回滚日志模式写入数据（保证内容在主文件而非 -wal），关闭后整页破坏
// 第 2 页（btree 根页）与末页。
func seedCorruptRuntimeStoreDB(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite3", path)
	require.NoError(t, err)
	var journalMode string
	require.NoError(t, db.QueryRow("PRAGMA journal_mode=DELETE").Scan(&journalMode))
	_, err = db.Exec(`CREATE TABLE payload (id INTEGER PRIMARY KEY, body BLOB)`)
	require.NoError(t, err)
	for index := 0; index < 64; index++ {
		_, err = db.Exec(`INSERT INTO payload (body) VALUES (?)`, bytes.Repeat([]byte("x"), 4096))
		require.NoError(t, err)
	}
	require.NoError(t, db.Close())

	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Greater(t, info.Size(), int64(3*4096), "损坏夹具需要至少 4 个页，实际 %d 字节", info.Size())

	garbage := bytes.Repeat([]byte{0xFF}, 4096)
	handle, err := os.OpenFile(path, os.O_WRONLY, 0)
	require.NoError(t, err)
	_, err = handle.WriteAt(garbage, 4096) // 第 2 页：btree 根页
	require.NoError(t, err)
	_, err = handle.WriteAt(garbage, info.Size()-4096) // 末页：叶子/索引页
	require.NoError(t, err)
	require.NoError(t, handle.Close())
}

// assertRuntimeStoreDBIsCorrupt 保证夹具真的坏了；否则让测试以明确的
// 前置条件失败，而不是在 store 探测断言处留下难懂的错误。
func assertRuntimeStoreDBIsCorrupt(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite3", path)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	var result string
	if err := db.QueryRow("PRAGMA quick_check").Scan(&result); err != nil {
		return // 直接报 malformed 也算损坏成立
	}
	require.NotEqual(t, "ok", strings.ToLower(strings.TrimSpace(result)), "损坏夹具未生效")
}

// TestRuntimeStoreHealthProbeDisabledByDefaultSkipsProbe 锁定"默认关闭"：
// 未设置环境变量时即使库已损坏，也不会执行 quick_check 探测——错误只能来自
// 普通打开/迁移路径，绝不能带 health probe 前缀（防止未来被静默改成默认开启）。
func TestRuntimeStoreHealthProbeDisabledByDefaultSkipsProbe(t *testing.T) {
	t.Setenv(RuntimeStoreHealthProbeEnv, "")
	require.False(t, runtimeStoreHealthProbeEnabled(), "未设置时必须关闭")

	path := filepath.Join(t.TempDir(), "corrupt.sqlite")
	seedCorruptRuntimeStoreDB(t, path)
	assertRuntimeStoreDBIsCorrupt(t, path)

	store, err := NewSQLiteRuntimeStore(&RuntimeStoreConfig{Path: path})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	// 损坏库在普通打开路径上可能失败（迁移/写入报 malformed），这不属于回归；
	// 唯一禁止的是健康探测被默认执行。
	_, appendErr := store.AppendEvent(context.Background(), runtimeevents.Event{
		Type:      "probe",
		SessionID: "s-1",
		Payload:   map[string]interface{}{"ok": true},
	})
	if appendErr != nil {
		require.NotContains(t, appendErr.Error(), "runtime store health probe",
			"未设置环境变量时不应执行健康探测: %v", appendErr)
	}
}
