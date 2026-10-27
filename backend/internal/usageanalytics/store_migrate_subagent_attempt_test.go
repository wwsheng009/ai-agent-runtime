package usageanalytics

import (
	"path/filepath"
	"testing"

	// 迁移测试要手工降级表结构，必须自带驱动注册。
	_ "github.com/wwsheng009/ai-agent-runtime/internal/sqlitedriver"
)

// TestStoreMigrateAddsSubagentAttemptColumns 锁定 attempt 观测增量列的幂等迁移
// （§6.1-4 收口）：cancel_source / retry_skipped / partial_output 缺失的老库重新
// 打开时必须补齐，且新列可写可读（空值不伪造成有效语义）。
func TestStoreMigrateAddsSubagentAttemptColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage_analytics.sqlite")
	store, err := Open(Config{Path: path})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// 降级成旧形态：删掉三列。
	for _, column := range []string{"cancel_source", "retry_skipped", "partial_output"} {
		if _, err := store.db.Exec("ALTER TABLE usage_subagents DROP COLUMN " + column); err != nil {
			t.Fatalf("downgrade drop %s: %v", column, err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	reopened, err := Open(Config{Path: path})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = reopened.Close() }()
	for _, column := range []string{"cancel_source", "retry_skipped", "partial_output"} {
		has, err := reopened.hasColumn("usage_subagents", column)
		if err != nil || !has {
			t.Fatalf("迁移应补列 usage_subagents.%s（err=%v has=%v）", column, err, has)
		}
	}

	if _, err := reopened.db.Exec(
		`INSERT INTO usage_subagents (subagent_id, parent_session_id, cancel_source, retry_skipped, partial_output)
		 VALUES ('sa-1', 's-1', 'provider_timeout', 'max_attempts_reached', 1)`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	row := queryRow(t, reopened, `SELECT cancel_source, retry_skipped, partial_output FROM usage_subagents WHERE subagent_id='sa-1'`)
	if len(row) != 3 {
		t.Fatalf("row len = %d, want 3 (%v)", len(row), row)
	}
	if text, _ := row[0].(string); text != "provider_timeout" {
		t.Fatalf("cancel_source = %v", row[0])
	}
	if text, _ := row[1].(string); text != "max_attempts_reached" {
		t.Fatalf("retry_skipped = %v", row[1])
	}
	if value, _ := row[2].(int64); value != 1 {
		t.Fatalf("partial_output = %v", row[2])
	}
}
