package usageanalytics

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// TestStoreMigratesV2Idempotent 锁定方案 §5.1 / D6：schema v2 三表建立、
// user_version=2、重复 Open 幂等。
func TestStoreMigratesV2Idempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage_analytics.sqlite")
	store, err := Open(Config{Path: path})
	if err != nil {
		t.Fatalf("第一次 Open 失败: %v", err)
	}
	assertSchemaV2(t, store)
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	store, err = Open(Config{Path: path})
	if err != nil {
		t.Fatalf("第二次 Open 必须幂等: %v", err)
	}
	defer store.Close()
	assertSchemaV2(t, store)
}

// TestReadOnlyOpenSkipsMigration 锁定方案 §4 批次 1.1：只读打开不得执行
// 写入型迁移（库缺失走 empty 语义；已有库的 user_version 保持原样）。
func TestReadOnlyOpenSkipsMigration(t *testing.T) {
	dir := t.TempDir()

	// 1) 库不存在：只读打开走 empty，且不得创建文件。
	missing := filepath.Join(dir, "missing.sqlite")
	emptyStore := openReadOnly(missing, 0)
	if !emptyStore.Empty() {
		t.Fatalf("库缺失时只读打开应标记 empty")
	}
	if _, statErr := os.Stat(missing); !os.IsNotExist(statErr) {
		t.Fatalf("只读打开不得创建库文件，stat err=%v", statErr)
	}

	// 2) 造一个只含 v1 表、user_version=0 的库：只读打开不得改写版本。
	legacyPath := filepath.Join(dir, "legacy.sqlite")
	db, err := sql.Open("sqlite3", legacyPath)
	if err != nil {
		t.Fatalf("打开临时库: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE usage_requests (
		llm_request_id TEXT PRIMARY KEY,
		session_id TEXT,
		trace_id TEXT,
		step INTEGER,
		provider TEXT,
		model TEXT,
		started_at_unix_nano INTEGER
	)`); err != nil {
		t.Fatalf("建 v1 表: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("关闭临时库: %v", err)
	}

	readOnlyStore := openReadOnly(legacyPath, 0)
	defer readOnlyStore.Close()
	if readOnlyStore.Empty() {
		t.Fatalf("既有库只读打开不应标记 empty")
	}
	if version := userVersion(t, readOnlyStore); version != 0 {
		t.Fatalf("只读打开不得写入迁移，user_version 期望 0，实际 %d", version)
	}
	if rows, ok, err := readOnlyStore.query(`SELECT name FROM sqlite_master WHERE type='table' AND name='usage_tool_calls'`); err != nil {
		t.Fatalf("查询 sqlite_master: %v", err)
	} else if ok && rows.Next() {
		_ = rows.Close()
		t.Fatalf("只读打开不得建 v2 表")
	} else if ok {
		_ = rows.Close()
	}

	// 3) 可写打开同一库：迁移补齐 v2 表并写版本号。
	writable, err := Open(Config{Path: legacyPath})
	if err != nil {
		t.Fatalf("可写 Open: %v", err)
	}
	defer writable.Close()
	assertSchemaV2(t, writable)
}

func assertSchemaV2(t *testing.T, store *Store) {
	t.Helper()
	if version := userVersion(t, store); version < 2 {
		t.Fatalf("user_version 期望 >= 2（v3 迁移后为 3），实际 %d", version)
	}
	for _, table := range []string{"usage_tool_calls", "usage_subagents", "usage_turns"} {
		rows, ok, err := store.query(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table)
		if err != nil || !ok {
			t.Fatalf("查询 sqlite_master(%s) 失败: ok=%v err=%v", table, ok, err)
		}
		found := rows.Next()
		_ = rows.Close()
		if !found {
			t.Fatalf("schema v2 缺少表 %s", table)
		}
	}
}

func userVersion(t *testing.T, store *Store) int {
	t.Helper()
	if store == nil || store.db == nil {
		return -1
	}
	var version int
	if err := store.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatalf("读取 user_version: %v", err)
	}
	return version
}
