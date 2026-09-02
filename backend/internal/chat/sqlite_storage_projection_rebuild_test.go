package chat

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// TestUpdateWithStaleShorterHistoryKeepsNewestInProjection 复现：CLI 退出时用
// 过期的内存投影（比当前存储投影更短/错位，缺少最新消息）调用 Update，
// 存储层不能拿这份过期历史重建 session_prompt_messages —— 否则最新一轮
// 会被从投影中丢掉，resume 后展示/上下文都会丢失最后 turn。
func TestUpdateWithStaleShorterHistoryKeepsNewestInProjection(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultPersistentSessionStorageConfig(dir)
	cfg.Path = filepath.Join(dir, "sessions.sqlite")
	cfg.ImportLegacyJSON = false
	cfg.HotHistoryMessages = 5
	store, err := NewSQLiteSessionStorage(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.CloseStorage()) })

	ctx := context.Background()
	session := &Session{
		ID:        "sess-stale-sync",
		UserID:    "tester",
		State:     StateActive,
		CreatedAt: time.Now().UTC(),
	}
	// 8 条 canonical 消息（4 轮）。
	history := make([]types.Message, 0, 8)
	for index := 0; index < 8; index++ {
		content := fmt.Sprintf("message-%02d", index)
		if index%2 == 0 {
			history = append(history, *types.NewUserMessage(content))
		} else {
			history = append(history, *types.NewAssistantMessage(content))
		}
	}
	session.ReplaceHistory(history)
	require.NoError(t, store.Save(ctx, session))

	// 加载后 History 是热投影（最新的 5 条：message-03..07）。
	loaded, err := store.Load(ctx, session.ID)
	require.NoError(t, err)
	require.True(t, loaded.HistoryLoaded)
	require.Len(t, loaded.History, 5)
	require.Equal(t, "message-07", lastMessageContent(loaded.History))
	require.Equal(t, 8, loaded.CanonicalMessageCount)

	// 模拟 CLI 退出同步：传入过期投影 [message-02..06]（缺少最新的 message-07）。
	stale := &Session{
		ID:                    session.ID,
		UserID:                "tester",
		State:                 StateActive,
		CanonicalMessageCount: 8,
		HistoryLoaded:         true,
	}
	staleHistory := make([]types.Message, 0, 5)
	for index := 2; index <= 6; index++ {
		content := fmt.Sprintf("message-%02d", index)
		if index%2 == 0 {
			staleHistory = append(staleHistory, *types.NewUserMessage(content))
		} else {
			staleHistory = append(staleHistory, *types.NewAssistantMessage(content))
		}
	}
	stale.ReplaceHistory(staleHistory)
	require.NoError(t, store.Update(ctx, stale))

	// 重新加载：投影必须仍然包含最新的 message-07（从 canonical 重建，而不是
	// 被过期的 stale 历史覆盖）。
	after, err := store.Load(ctx, session.ID)
	require.NoError(t, err)
	require.Len(t, after.History, 5)
	require.Equal(t, "message-07", lastMessageContent(after.History), "stale sync must not drop the newest turn from the projection")
}

func lastMessageContent(messages []types.Message) string {
	if len(messages) == 0 {
		return ""
	}
	return messages[len(messages)-1].Content
}
