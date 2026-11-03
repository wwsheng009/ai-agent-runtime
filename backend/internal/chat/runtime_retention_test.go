package chat

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	cacheanalytics "github.com/wwsheng009/ai-agent-runtime/internal/cacheanalytics"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

func newRetentionTestStore(t *testing.T, cfg RuntimeStoreConfig) *SQLiteRuntimeStore {
	t.Helper()
	if strings.TrimSpace(cfg.Path) == "" && strings.TrimSpace(cfg.DSN) == "" {
		cfg.Path = filepath.Join(t.TempDir(), "runtime.sqlite")
	}
	store, err := NewSQLiteRuntimeStore(&cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestSQLiteRuntimeStorePrunesToolReceiptsByCount(t *testing.T) {
	ctx := context.Background()
	store := newRetentionTestStore(t, RuntimeStoreConfig{
		ToolReceiptRetention: 3,
		ToolReceiptMaxBytes:  64 << 20,
	})
	base := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	for index := 1; index <= 6; index++ {
		require.NoError(t, store.SaveToolReceipt(ctx, ToolExecutionReceipt{
			SessionID:   "bounded-receipt-count",
			ToolCallID:  "tool-" + string(rune('0'+index)),
			ToolName:    "shell",
			MessageJSON: []byte(`{"content":"ok"}`),
			CreatedAt:   base.Add(time.Duration(index) * time.Second),
		}))
	}

	receipts, err := store.ListToolReceipts(ctx, "bounded-receipt-count", 0)
	require.NoError(t, err)
	require.Len(t, receipts, 3)
	require.Equal(t, "tool-6", receipts[0].ToolCallID)
	require.Equal(t, "tool-4", receipts[2].ToolCallID)
	oldest, err := store.GetToolReceipt(ctx, "bounded-receipt-count", "tool-1")
	require.NoError(t, err)
	require.Nil(t, oldest)
}

func TestSQLiteRuntimeStorePrunesToolReceiptsByBytesAndKeepsNewest(t *testing.T) {
	ctx := context.Background()
	store := newRetentionTestStore(t, RuntimeStoreConfig{
		ToolReceiptRetention: 100,
		ToolReceiptMaxBytes:  250,
	})
	base := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	for index := 1; index <= 3; index++ {
		require.NoError(t, store.SaveToolReceipt(ctx, ToolExecutionReceipt{
			SessionID:   "bounded-receipt-bytes",
			ToolCallID:  "tool-" + string(rune('0'+index)),
			ToolName:    "view",
			MessageJSON: []byte(strings.Repeat("x", 100)),
			CreatedAt:   base.Add(time.Duration(index) * time.Second),
		}))
	}

	receipts, err := store.ListToolReceipts(ctx, "bounded-receipt-bytes", 0)
	require.NoError(t, err)
	require.Len(t, receipts, 2, "byte budget 250 must retain the newest two 100-byte receipts")
	require.Equal(t, "tool-3", receipts[0].ToolCallID)
	require.Equal(t, "tool-2", receipts[1].ToolCallID)

	// 单条超过字节预算时仍必须保留当前回执，否则崩溃恢复会丢失现场。
	oversized := strings.Repeat("y", 400)
	require.NoError(t, store.SaveToolReceipt(ctx, ToolExecutionReceipt{
		SessionID:   "bounded-receipt-oversized",
		ToolCallID:  "tool-oversized",
		ToolName:    "apply_patch",
		MessageJSON: []byte(oversized),
		CreatedAt:   base,
	}))
	receipts, err = store.ListToolReceipts(ctx, "bounded-receipt-oversized", 0)
	require.NoError(t, err)
	require.Len(t, receipts, 1)
	require.Equal(t, oversized, string(receipts[0].MessageJSON))
}

func TestSQLiteRuntimeStorePrunesCacheRequestsBySession(t *testing.T) {
	store := newRetentionTestStore(t, RuntimeStoreConfig{CacheRequestRetention: 3})
	base := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	for index := 1; index <= 6; index++ {
		require.NoError(t, store.SaveRequest(cacheanalytics.CacheRequestRecord{
			SchemaVersion: cacheanalytics.SchemaVersion,
			LLMRequestID:  "req-" + string(rune('0'+index)),
			SessionID:     "cache-retention",
			StartedAt:     base.Add(time.Duration(index) * time.Minute),
			Status:        cacheanalytics.RequestStatusSuccess,
			CacheStatus:   cacheanalytics.CacheStatusNotReported,
		}))
	}
	require.NoError(t, store.SaveRequest(cacheanalytics.CacheRequestRecord{
		SchemaVersion: cacheanalytics.SchemaVersion,
		LLMRequestID:  "other-session",
		SessionID:     "cache-retention-other",
		StartedAt:     base,
		Status:        cacheanalytics.RequestStatusSuccess,
		CacheStatus:   cacheanalytics.CacheStatusNotReported,
	}))

	records, err := store.LoadSessionRequests("cache-retention")
	require.NoError(t, err)
	require.Len(t, records, 3)
	require.Equal(t, "req-4", records[0].LLMRequestID)
	require.Equal(t, "req-5", records[1].LLMRequestID)
	require.Equal(t, "req-6", records[2].LLMRequestID)

	other, err := store.LoadSessionRequests("cache-retention-other")
	require.NoError(t, err)
	require.Len(t, other, 1, "retention must be scoped to one session")
}

func TestAppendEventsCompactsStreamingPayloadAliases(t *testing.T) {
	ctx := context.Background()
	store := newRetentionTestStore(t, RuntimeStoreConfig{})
	_, err := store.AppendEvents(ctx, []runtimeevents.Event{
		{
			Type:      "assistant.reasoning",
			SessionID: "stream-compact",
			Payload: map[string]interface{}{
				"text": "step", "content": "step", "delta": "step",
				"reasoning": map[string]interface{}{"format": "stream_delta", "summary": "step"},
				"step":      1,
			},
		},
		{
			Type:      "assistant_delta",
			SessionID: "stream-compact",
			Payload: map[string]interface{}{
				"delta": "answer", "content": "answer", "text": "answer",
				"step": 1,
			},
		},
	})
	require.NoError(t, err)

	events, err := store.ListEvents(ctx, "stream-compact", 0, 0)
	require.NoError(t, err)
	require.Len(t, events, 2)

	reasoning := events[0].Payload
	require.Equal(t, "step", reasoning["text"])
	require.NotContains(t, reasoning, "content")
	require.NotContains(t, reasoning, "delta")
	require.Equal(t, "step", reasoning["reasoning"].(map[string]interface{})["summary"])

	delta := events[1].Payload
	require.Equal(t, "answer", delta["delta"])
	require.NotContains(t, delta, "content")
	require.NotContains(t, delta, "text")
}

func TestRuntimeStoreDropsRedundantSessionEventsIndex(t *testing.T) {
	store := newRetentionTestStore(t, RuntimeStoreConfig{})
	require.NoError(t, store.ensure())
	rows, err := store.db.Query(`
		SELECT name FROM sqlite_master
		WHERE type='index' AND tbl_name='session_events'
		ORDER BY name
	`)
	require.NoError(t, err)
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		names = append(names, name)
	}
	require.NoError(t, rows.Err())
	require.NotContains(t, names, "idx_session_events_session_seq")
	require.NotEmpty(t, names, "UNIQUE(session_id, seq) must still provide an index")

	// 历史库可能仍有旧显式索引；离线清理也要把它移除。
	_, err = store.db.Exec(`CREATE INDEX idx_session_events_session_seq ON session_events(session_id, seq)`)
	require.NoError(t, err)
	stats, err := PruneRuntimeRetention(context.Background(), store.db, RuntimeRetentionOptions{})
	require.NoError(t, err)
	require.True(t, stats.RedundantSessionEventsIndexDropped)
	var indexCount int
	require.NoError(t, store.db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_session_events_session_seq'`,
	).Scan(&indexCount))
	require.Zero(t, indexCount)
}

func TestPruneRuntimeRetentionCompactsLegacyInlineToolSurface(t *testing.T) {
	ctx := context.Background()
	store := newRetentionTestStore(t, RuntimeStoreConfig{})
	payload := `[{"name":"legacy_tool","description":"legacy desc"}]`
	require.NoError(t, store.ensure())
	_, err := store.db.ExecContext(ctx, `
		INSERT INTO session_runtime_state (
			session_id, status, current_turn_id, current_checkpoint_id, suspended_turn_id,
			current_run_meta_json, ambient_run_meta_json, stable_tool_surface_json, frozen_turn_tools_json,
			pending_tool_json, pending_approval_json, pending_question_json,
			last_run_terminal_reason, head_offset, active_job_ids_json, updated_at, stable_tool_surface_ref
		) VALUES ('sess-legacy-compact','idle',NULL,NULL,NULL,NULL,NULL,?,NULL,NULL,NULL,NULL,NULL,0,'[]',?,NULL)
	`, payload, time.Now().UTC().Format(time.RFC3339Nano))
	require.NoError(t, err)

	stats, err := PruneRuntimeRetention(ctx, store.db, RuntimeRetentionOptions{
		ToolReceiptRetention:  3,
		ToolReceiptMaxBytes:   1024,
		CacheRequestRetention: 3,
		EventRetention:        3,
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), stats.LegacyInlineSurfacesCompacted)
	require.Equal(t, int64(len(payload)), stats.LegacyInlineSurfaceBytesFreed)

	var ref string
	var inline []byte
	require.NoError(t, store.db.QueryRowContext(ctx, `
		SELECT stable_tool_surface_ref, stable_tool_surface_json
		FROM session_runtime_state WHERE session_id='sess-legacy-compact'
	`).Scan(&ref, &inline))
	require.NotEmpty(t, ref)
	require.Nil(t, inline)

	loaded, err := store.LoadState(ctx, "sess-legacy-compact")
	require.NoError(t, err)
	require.NotNil(t, loaded)
	require.True(t, loaded.StableToolSurfaceSet)
	require.Len(t, loaded.StableToolSurface, 1)
	require.Equal(t, "legacy_tool", loaded.StableToolSurface[0].Name)
}

func TestPruneRuntimeRetentionCleansHistoricRows(t *testing.T) {
	ctx := context.Background()
	store := newRetentionTestStore(t, RuntimeStoreConfig{
		ToolReceiptRetention:  100,
		ToolReceiptMaxBytes:   1 << 20,
		CacheRequestRetention: 100,
		EventRetention:        100,
	})
	base := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	for index := 1; index <= 6; index++ {
		require.NoError(t, store.SaveToolReceipt(ctx, ToolExecutionReceipt{
			SessionID:   "offline-prune",
			ToolCallID:  "tool-" + string(rune('0'+index)),
			MessageJSON: []byte(strings.Repeat("z", 20)),
			CreatedAt:   base.Add(time.Duration(index) * time.Second),
		}))
		require.NoError(t, store.SaveRequest(cacheanalytics.CacheRequestRecord{
			SchemaVersion: cacheanalytics.SchemaVersion,
			LLMRequestID:  "req-" + string(rune('0'+index)),
			SessionID:     "offline-prune",
			StartedAt:     base.Add(time.Duration(index) * time.Minute),
			Status:        cacheanalytics.RequestStatusSuccess,
			CacheStatus:   cacheanalytics.CacheStatusNotReported,
		}))
		_, err := store.AppendEvent(ctx, runtimeevents.Event{
			Type: "test.offline", SessionID: "offline-prune", Payload: map[string]interface{}{"index": index},
		})
		require.NoError(t, err)
	}

	stats, err := PruneRuntimeRetention(ctx, store.db, RuntimeRetentionOptions{
		ToolReceiptRetention:  2,
		ToolReceiptMaxBytes:   1 << 20,
		CacheRequestRetention: 2,
		EventRetention:        2,
	})
	require.NoError(t, err)
	require.Equal(t, int64(4), stats.ToolReceiptsDeleted)
	require.Equal(t, int64(4), stats.CacheRequestsDeleted)
	require.Equal(t, int64(4), stats.EventsDeleted)

	receipts, err := store.ListToolReceipts(ctx, "offline-prune", 0)
	require.NoError(t, err)
	require.Len(t, receipts, 2)
	records, err := store.LoadSessionRequests("offline-prune")
	require.NoError(t, err)
	require.Len(t, records, 2)
	events, err := store.ListEvents(ctx, "offline-prune", 0, 0)
	require.NoError(t, err)
	require.Len(t, events, 2)
}
