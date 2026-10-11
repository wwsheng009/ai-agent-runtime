package usageledger

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/model/entity"
)

func TestSQLiteStore_CreateAndGetSince(t *testing.T) {
	store, err := NewSQLiteStore(&Config{
		Driver: "sqlite",
		DSN:    filepath.Join(t.TempDir(), "ledger.db"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, store.Close())
	})

	older := &entity.TokenUsageHistory{
		RequestID:    "req-old",
		ModelID:      "model-a",
		ProviderID:   "provider-a",
		InputTokens:  10,
		OutputTokens: 5,
		MessageCount: 1,
		MaxTokens:    50,
		Success:      true,
		StatusCode:   200,
		Metadata: entity.JSONMap{
			"skill":      "search",
			"entrypoint": "execute",
		},
		CreatedAt: entity.Time(time.Now().UTC().Add(-2 * time.Hour)),
	}
	require.NoError(t, store.Create(older))

	newer := &entity.TokenUsageHistory{
		RequestID:    "req-new",
		ModelID:      "model-b",
		ProviderID:   "provider-b",
		InputTokens:  20,
		OutputTokens: 8,
		MessageCount: 2,
		MaxTokens:    60,
		Success:      false,
		StatusCode:   429,
		Metadata: entity.JSONMap{
			"skill":      "plan",
			"entrypoint": "agent_chat",
		},
		CreatedAt: entity.Time(time.Now().UTC().Add(-30 * time.Minute)),
	}
	require.NoError(t, store.Create(newer))

	records, err := store.GetSince(time.Time{}, 10)
	require.NoError(t, err)
	require.Len(t, records, 2)
	require.Equal(t, "req-new", records[0].RequestID)
	require.Equal(t, "req-old", records[1].RequestID)
	require.Equal(t, "plan", records[0].Metadata["skill"])
	require.False(t, records[0].Success)

	filtered, err := store.GetSince(time.Now().UTC().Add(-1*time.Hour), 10)
	require.NoError(t, err)
	require.Len(t, filtered, 1)
	require.Equal(t, "req-new", filtered[0].RequestID)
	require.Equal(t, 28, filtered[0].TotalTokens)
}

// TestExpandHomeDSN 锁定默认 DSN 的用户目录锚定语义：只有 "~" 前缀会被展开，
// 其余 DSN（相对路径、:memory:、file: URI）逐字保留，避免改写既有落点。
func TestExpandHomeDSN(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err)

	require.Equal(t,
		filepath.Join(home, ".aicli", "data", "usage_ledger.db"),
		expandHomeDSN("~/.aicli/data/usage_ledger.db"),
	)
	require.Equal(t, home, expandHomeDSN("~"))
	require.Equal(t, "data/gateway.db", expandHomeDSN("data/gateway.db"))
	require.Equal(t, "./data/gateway.db", expandHomeDSN("./data/gateway.db"))
	require.Equal(t, ":memory:", expandHomeDSN(":memory:"))
	require.Equal(t, "file:ledger.db?cache=shared", expandHomeDSN("file:ledger.db?cache=shared"))
}
