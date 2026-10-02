package usageanalytics

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"

	// 迁移测试要手工造一张 v7 老表，必须自带驱动注册。
	_ "github.com/wwsheng009/ai-agent-runtime/internal/sqlitedriver"
)

// publishSubagentCompleted 发一条 subagent.completed 事件。
func publishSubagentCompleted(t *testing.T, bus *runtimeevents.Bus, payload map[string]interface{}, at time.Time) {
	t.Helper()
	bus.Publish(runtimeevents.Event{
		Type:      EventSubagentCompleted,
		SessionID: "session-batch",
		Payload:   payload,
		Timestamp: at,
	})
}

// TestSubagentCompletionBatchIDKeepsBatchesSeparate 锁定 v8 迁移的根因修复：
// subagent_id 在模型省略 id 时按批次内序号合成（subagent_1…），同一父会话的
// 第二个 spawn_subagents 批次会复用同一批 id。旧主键
// (subagent_id, parent_session_id) 会把两次独立运行并成一行——真机证据是
// /web/api/analysis/subagents 对两个批次只返回 1 行，token 取 MAX 而非 SUM、
// read_only 被后到者覆盖。主键加入 batch_id 后两次运行必须各占一行。
func TestSubagentCompletionBatchIDKeepsBatchesSeparate(t *testing.T) {
	store, err := Open(Config{Path: filepath.Join(t.TempDir(), "usage_analytics.sqlite")})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = store.Close() }()

	bus := runtimeevents.NewBus()
	collector := newCollector(store, nil, nil)
	collector.subscribe(bus)
	defer collector.close()

	at := time.Date(2026, 10, 2, 12, 54, 0, 0, time.UTC)
	// 批次 A：只读 explore，任务书却要求用 rg/ls —— 真事故形态。
	publishSubagentCompleted(t, bus, map[string]interface{}{
		"subagent_id":       "subagent_1",
		"parent_session_id": "session-batch",
		"batch_id":          "batch_A",
		"child_session_id":  "subagent_subagent_1_aaa",
		"task_type":         "explore",
		"success":           false,
		"status":            "failed",
		"error_code":        "UPSTREAM_ERROR",
		"failure_category":  "tool_error",
		"read_only":         true,
		"usage_total_tokens": int64(1_882_910),
		"duration_ms":       int64(460_211),
	}, at)
	// 批次 B：同一父会话、同一个合成 id，但 read_only 未声明（= 写任务）。
	publishSubagentCompleted(t, bus, map[string]interface{}{
		"subagent_id":       "subagent_1",
		"parent_session_id": "session-batch",
		"batch_id":          "batch_B",
		"child_session_id":  "subagent_subagent_1_bbb",
		"task_type":         "implement",
		"success":           false,
		"status":            "failed",
		"error_code":        "UPSTREAM_ERROR",
		"failure_category":  "tool_error",
		"usage_total_tokens": int64(4_000),
		"duration_ms":       int64(1_200),
	}, at.Add(time.Second))

	stats, err := store.SubagentStats(SubagentStatsQuery{SessionID: "session-batch"})
	if err != nil {
		t.Fatalf("SubagentStats: %v", err)
	}
	if len(stats.Subagents) != 2 {
		t.Fatalf("两个批次必须各自成行，实际 %d 行: %+v", len(stats.Subagents), stats.Subagents)
	}

	byBatch := map[string]SubagentStat{}
	for _, stat := range stats.Subagents {
		byBatch[stat.BatchID] = stat
	}
	a, okA := byBatch["batch_A"]
	b, okB := byBatch["batch_B"]
	if !okA || !okB {
		t.Fatalf("缺少批次维度，batch_id 未落库: %+v", stats.Subagents)
	}
	if a.ChildSessionID != "subagent_subagent_1_aaa" || b.ChildSessionID != "subagent_subagent_1_bbb" {
		t.Fatalf("child_session_id 串行错位: A=%q B=%q", a.ChildSessionID, b.ChildSessionID)
	}
	if !a.ReadOnly || b.ReadOnly {
		t.Fatalf("read_only 被跨批次覆盖: A=%v B=%v", a.ReadOnly, b.ReadOnly)
	}
	// token 必须是各批次自己的值（旧合并语义下两条都会显示 1_882_910）。
	if a.UsageTotalTokens != 1_882_910 || b.UsageTotalTokens != 4_000 {
		t.Fatalf("usage_total_tokens 跨批次串行: A=%d B=%d", a.UsageTotalTokens, b.UsageTotalTokens)
	}
	// duration_ms 同批次应各自保留（旧语义下后到者会覆盖前者）。
	if a.DurationMS != 460_211 || b.DurationMS != 1_200 {
		t.Fatalf("duration_ms 跨批次串行: A=%d B=%d", a.DurationMS, b.DurationMS)
	}
}

// TestSubagentCompletionSameBatchStillMergesAttempts 保证加入 batch_id 没有
// 破坏同一次运行的多次 attempt 合并：中间尝试（intermediate_attempt）与终态
// 属于同一批次，必须并成一行并由 MAX(attempt) 收敛。
func TestSubagentCompletionSameBatchStillMergesAttempts(t *testing.T) {
	store, err := Open(Config{Path: filepath.Join(t.TempDir(), "usage_analytics.sqlite")})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = store.Close() }()

	bus := runtimeevents.NewBus()
	collector := newCollector(store, nil, nil)
	collector.subscribe(bus)
	defer collector.close()

	at := time.Date(2026, 10, 2, 12, 54, 0, 0, time.UTC)
	publishSubagentCompleted(t, bus, map[string]interface{}{
		"subagent_id":          "subagent_1",
		"parent_session_id":    "session-batch",
		"batch_id":             "batch_A",
		"attempt":              1,
		"max_attempts":         2,
		"intermediate_attempt": true,
		"success":              false,
		"status":               "failed",
	}, at)
	publishSubagentCompleted(t, bus, map[string]interface{}{
		"subagent_id":       "subagent_1",
		"parent_session_id": "session-batch",
		"batch_id":          "batch_A",
		"attempt":           2,
		"max_attempts":      2,
		"success":           false,
		"status":            "failed",
	}, at.Add(time.Second))

	stats, err := store.SubagentStats(SubagentStatsQuery{SessionID: "session-batch"})
	if err != nil {
		t.Fatalf("SubagentStats: %v", err)
	}
	if len(stats.Subagents) != 1 {
		t.Fatalf("同批次多次 attempt 必须并成一行，实际 %d 行", len(stats.Subagents))
	}
	stat := stats.Subagents[0]
	if stat.Attempt != 2 || stat.MaxAttempts != 2 {
		t.Fatalf("attempt 未收敛到最终尝试: %+v", stat)
	}
	if stat.ConflictCount != 1 {
		t.Fatalf("中间尝试应计入 conflict_count，实际 %d", stat.ConflictCount)
	}
}

// usageSubagentsV7DDL 是 v8 之前的表结构：没有 batch_id，主键只到父会话。
// 迁移测试用它造一张真实老库，锁定重建逻辑不会丢行。
const usageSubagentsV7DDL = `CREATE TABLE usage_subagents (
  subagent_id             TEXT NOT NULL DEFAULT '',
  parent_session_id       TEXT NOT NULL DEFAULT '',
  child_session_id        TEXT NOT NULL DEFAULT '',
  role                    TEXT NOT NULL DEFAULT '',
  task_type               TEXT NOT NULL DEFAULT '',
  task_subject            TEXT NOT NULL DEFAULT '',
  read_only               INTEGER NOT NULL DEFAULT 0,
  success                 INTEGER,
  completion_reason       TEXT NOT NULL DEFAULT '',
  failure_category        TEXT NOT NULL DEFAULT '',
  error_code              TEXT NOT NULL DEFAULT '',
  attempt                 INTEGER NOT NULL DEFAULT 1,
  max_attempts            INTEGER NOT NULL DEFAULT 1,
  retry_reason            TEXT NOT NULL DEFAULT '',
  id_synthesized          INTEGER NOT NULL DEFAULT 0,
  duration_ms             INTEGER NOT NULL DEFAULT 0,
  started_at_unix_nano    INTEGER NOT NULL DEFAULT 0,
  completed_at_unix_nano  INTEGER NOT NULL DEFAULT 0,
  usage_total_tokens      INTEGER NOT NULL DEFAULT 0,
  budget_tokens           INTEGER NOT NULL DEFAULT 0,
  source                  TEXT NOT NULL DEFAULT '',
  conflict_count          INTEGER NOT NULL DEFAULT 0,
  record_json             BLOB,
  PRIMARY KEY (subagent_id, parent_session_id)
)`

// TestUsageSubagentsBatchIDMigrationPreservesRows 锁定 v8 迁移的幂等与无损：
// 打开一张没有 batch_id 的 v7 老库，历史行必须整行搬过去（batch_id 补空串），
// 索引要跟着重建，二次打开不得再次触发。
func TestUsageSubagentsBatchIDMigrationPreservesRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage_analytics.sqlite")

	legacy, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	for _, stmt := range []string{
		usageSubagentsV7DDL,
		`CREATE INDEX IF NOT EXISTS idx_usage_subagents_session ON usage_subagents(parent_session_id, completed_at_unix_nano DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_subagents_fail ON usage_subagents(success, failure_category)`,
		`INSERT INTO usage_subagents (subagent_id, parent_session_id, child_session_id, role, task_type, read_only,
		   success, completion_reason, failure_category, error_code, usage_total_tokens, completed_at_unix_nano, source)
		 VALUES ('subagent_1','session-old','child-old','researcher','explore',1,0,'failed','tool_error','TOOL_ERROR',1882910,1756800000000000000,'scheduler')`,
	} {
		if _, err := legacy.Exec(stmt); err != nil {
			t.Fatalf("seed legacy (%s): %v", stmt, err)
		}
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close legacy db: %v", err)
	}

	store, err := Open(Config{Path: path})
	if err != nil {
		t.Fatalf("open migrated db: %v", err)
	}
	stats, err := store.SubagentStats(SubagentStatsQuery{SessionID: "session-old"})
	if err != nil {
		t.Fatalf("SubagentStats after migration: %v", err)
	}
	if len(stats.Subagents) != 1 {
		t.Fatalf("迁移后历史行丢失，实际 %d 行", len(stats.Subagents))
	}
	migrated := stats.Subagents[0]
	if migrated.ChildSessionID != "child-old" || migrated.TaskType != "explore" || migrated.Role != "researcher" {
		t.Fatalf("迁移后列错位: %+v", migrated)
	}
	if !migrated.ReadOnly || migrated.UsageTotalTokens != 1_882_910 || migrated.FailureCategory != "tool_error" {
		t.Fatalf("迁移后取值不符: %+v", migrated)
	}
	// 历史行无从恢复批次归属，补空串是唯一诚实的选择。
	if migrated.BatchID != "" {
		t.Fatalf("历史行 batch_id 应为空串，实际 %q", migrated.BatchID)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close migrated db: %v", err)
	}

	// 二次打开必须幂等：列已存在时不得再重建表（否则历史行会被清成一批空串行）。
	reopened, err := Open(Config{Path: path})
	if err != nil {
		t.Fatalf("reopen migrated db: %v", err)
	}
	defer func() { _ = reopened.Close() }()
	stats, err = reopened.SubagentStats(SubagentStatsQuery{SessionID: "session-old"})
	if err != nil {
		t.Fatalf("SubagentStats after reopen: %v", err)
	}
	if len(stats.Subagents) != 1 || stats.Subagents[0].UsageTotalTokens != 1_882_910 {
		t.Fatalf("二次打开破坏数据（迁移不幂等）: %+v", stats.Subagents)
	}
}
