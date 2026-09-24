package usageledger

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/model/entity"
)

// FR-13：usageledger 的 profile 维度——正例断言写入，反例断言"不猜"。
// 使用服务同包测试直接驱动 onRequestFinished，避免依赖 EventBus 时序。

func newLedgerProfileTestStore(t *testing.T) *SQLiteStore {
	t.Helper()
	store, err := NewSQLiteStore(&Config{
		Driver: "sqlite",
		DSN:    filepath.Join(t.TempDir(), "ledger-profile.db"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, store.Close())
	})
	return store
}

func ledgerProfileTestEvent(sessionID string) runtimeevents.Event {
	return runtimeevents.Event{
		Type:      "llm.request.finished",
		SessionID: sessionID,
		TraceID:   "trace-ledger-profile",
		Payload: map[string]interface{}{
			"llm_request_id":          "req-ledger-profile",
			"success":                 true,
			"model":                   "model-x",
			"provider":                "provider-x",
			"usage_prompt_tokens":     int64(12),
			"usage_completion_tokens": int64(3),
			"usage_total_tokens":      int64(15),
			"step":                    int64(1),
		},
		Timestamp: time.Now().UTC(),
	}
}

func singleLedgerProfileRecord(t *testing.T, store *SQLiteStore) *entity.TokenUsageHistory {
	t.Helper()
	records, err := store.GetSince(time.Time{}, 10)
	require.NoError(t, err)
	require.Len(t, records, 1)
	return records[0]
}

func TestServiceRecordsProfileFromLookup(t *testing.T) {
	store := newLedgerProfileTestStore(t)
	var askedSessionID string
	service := NewService(store, WithProfileLookup(func(sessionID string) string {
		askedSessionID = sessionID
		return "coding"
	}))
	require.NotNil(t, service)

	service.onRequestFinished(ledgerProfileTestEvent("sess-1"))

	record := singleLedgerProfileRecord(t, store)
	require.Equal(t, "sess-1", askedSessionID)
	require.Equal(t, "coding", record.Metadata["profile"])
	// 既有元数据键不受影响。
	require.Equal(t, "llm_runtime", record.Metadata["subsystem"])
	require.Equal(t, "sess-1", record.Metadata["session_id"])
}

// 反证 1：未注入 lookup 的旧接线（NewService(store)）不得写入 profile 键。
func TestServiceOmitsProfileWithoutLookup(t *testing.T) {
	store := newLedgerProfileTestStore(t)
	service := NewService(store)
	require.NotNil(t, service)

	service.onRequestFinished(ledgerProfileTestEvent("sess-1"))

	record := singleLedgerProfileRecord(t, store)
	_, hasProfile := record.Metadata["profile"]
	require.False(t, hasProfile, "未注入 lookup 时不得写入 profile 键")
}

// 反证 2：lookup 解析为空（未绑定 / 读不到会话）时同样不写键，而不是写空串。
func TestServiceOmitsProfileWhenLookupEmpty(t *testing.T) {
	store := newLedgerProfileTestStore(t)
	service := NewService(store, WithProfileLookup(func(string) string { return "   " }))
	require.NotNil(t, service)

	service.onRequestFinished(ledgerProfileTestEvent("sess-1"))

	record := singleLedgerProfileRecord(t, store)
	_, hasProfile := record.Metadata["profile"]
	require.False(t, hasProfile, "解析为空时不得写入 profile 键")
}
