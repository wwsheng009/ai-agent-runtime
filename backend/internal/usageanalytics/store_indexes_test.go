package usageanalytics

import (
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// 锁定查询关键索引：工具时间窗、请求分页排序、大小写不敏感 provider/model
// 过滤都必须随 Open 的迁移创建，避免新装与老库升级后索引集合分叉。
func TestOpenCreatesAnalyticsQueryIndexes(t *testing.T) {
	store, err := Open(Config{Path: filepath.Join(t.TempDir(), "usage.sqlite")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()
	var walLimit int
	if err := store.db.QueryRow("PRAGMA journal_size_limit").Scan(&walLimit); err != nil {
		t.Fatalf("read journal_size_limit: %v", err)
	}
	if walLimit != defaultWALSizeLimit {
		t.Fatalf("journal_size_limit = %d, want %d", walLimit, defaultWALSizeLimit)
	}
	rows, err := store.readDB().Query(`SELECT name FROM sqlite_master WHERE type = 'index'`)
	if err != nil {
		t.Fatalf("list indexes: %v", err)
	}
	defer rows.Close()
	got := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan index: %v", err)
		}
		got[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate indexes: %v", err)
	}
	for _, want := range []string{
		"idx_usage_requests_time_step_id",
		"idx_usage_requests_provider_lower",
		"idx_usage_requests_model_lower",
		"idx_usage_tool_calls_started",
		"idx_usage_tool_calls_tool_started",
		"idx_usage_tool_calls_time_group",
		"idx_usage_subagents_completed",
		"idx_usage_subagents_started",
		"idx_usage_turns_started",
	} {
		if !got[want] {
			t.Errorf("missing index %s; indexes=%s", want, strings.Join(sortedKeys(got), ","))
		}
	}
}

func sortedKeys(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
