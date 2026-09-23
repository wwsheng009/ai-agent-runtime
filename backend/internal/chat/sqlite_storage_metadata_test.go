package chat

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// 诊断审计与 P2-9 对账只需要「存在性 + 状态」。LoadMetadata 必须只读 sessions
// 单行：会话库连接池恒为单连接，用完整 Load 反序列化整段历史会让启动期历史分页
// 与 /web/api/status 快照相互饿死（实测 goroutine 停在 database/sql.(*DB).conn）。
func TestSQLiteSessionStorageLoadMetadataSkipsPromptHistory(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteSessionStorage(t, nil)

	session := NewSession("metadata-user")
	session.AddMessage(*types.NewUserMessage("第一条"))
	session.AddMessage(*types.NewUserMessage("第二条"))
	require.NoError(t, store.Save(ctx, session))

	meta, err := store.LoadMetadata(ctx, session.ID)
	require.NoError(t, err)
	require.Equal(t, session.ID, meta.ID)
	require.Equal(t, session.State, meta.State)
	require.False(t, meta.HistoryLoaded, "元数据读取不得声称已装载历史")
	require.Empty(t, meta.History)

	// 强证明：删掉消息投影表后元数据读取仍必须成功（说明它不触碰历史），
	// 而完整 Load 必须失败。
	_, err = store.db.ExecContext(ctx, `DROP TABLE session_prompt_messages`)
	require.NoError(t, err)

	meta, err = store.LoadMetadata(ctx, session.ID)
	require.NoError(t, err)
	require.Equal(t, session.ID, meta.ID)

	_, err = store.Load(ctx, session.ID)
	require.Error(t, err, "完整 Load 依赖消息投影表，删表后必须报错")
}

func TestSQLiteSessionStorageLoadMetadataMissingSession(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteSessionStorage(t, nil)

	_, err := store.LoadMetadata(ctx, "missing-session")
	require.ErrorIs(t, err, ErrSessionNotFound)

	_, err = store.LoadMetadata(ctx, "")
	require.ErrorIs(t, err, ErrInvalidSession)
}
