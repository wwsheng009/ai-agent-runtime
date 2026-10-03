package chat

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// 回归：Web 会话首轮（session_20261003222355_a4w50Tal 现场）。
//
// 时序：
//  1. 请求前 ensureSessionInstructionMessages 先把 instruction system 落 canonical
//     （seq1），prompt 投影也是该 system；
//  2. agent 循环回写的 durable 转录把 transient system 剥离，因此以 user 开头；
//  3. 该转录与 canonical 的唯一一行（instruction system）没有任何身份重叠，
//     identityAlignedAppendStart 找不到锚点，legacy 回退按 declaredDelta 跳过开头
//     的 user，只追加 assistant/tool/assistant；
//  4. prompt 投影随后按调用方历史重建（含 user），于是 /history（读 canonical）
//     永远看不到用户 prompt——Web 会话气泡空白。
//
// 修复后：无锚点且 canonical 只有 instruction 种子时，首轮 user 必须一并落 canonical。
func TestSQLiteSessionStorageFirstTurnKeepsUserAfterInstructionSeed(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteSessionStorage(t, nil)

	session := NewSession("first-turn-user")
	require.NoError(t, store.Save(ctx, session))

	// 请求前：instruction system 先落 canonical（seq1），prompt 投影同步为 system。
	session.ReplaceHistory([]types.Message{*types.NewSystemMessage("task difficulty guidance")})
	require.NoError(t, store.Update(ctx, session))

	// 回合收尾：agent 循环交回 system-stripped 的 durable 转录。
	loaded, err := store.Load(ctx, session.ID)
	require.NoError(t, err)
	toolCall := *types.NewAssistantMessage("")
	toolCall.ToolCalls = []types.ToolCall{{
		ID:   "call_1",
		Type: "function",
		Name: "shell",
		Args: map[string]interface{}{"command": "pwd"},
	}}
	loaded.ReplaceHistory([]types.Message{
		*types.NewUserMessage("pwd"),
		toolCall,
		*types.NewToolMessage("call_1", "E:\\projects\\ai\\ai-agent-runtime"),
		*types.NewAssistantMessage("Current working directory: E:\\projects\\ai\\ai-agent-runtime"),
	})
	require.NoError(t, store.Update(ctx, loaded))

	canonical := rawCanonicalContents(t, store, session.ID)
	require.Equal(t, []string{
		"system:task difficulty guidance",
		"user:pwd",
		"assistant:",
		"tool:E:\\projects\\ai\\ai-agent-runtime",
		"assistant:Current working directory: E:\\projects\\ai\\ai-agent-runtime",
	}, canonical, "首个用户消息必须随回合落 canonical，不能被 legacy 增量算术跳过")

	// /history 读取的 canonical 页必须包含 user 行（渲染层依赖它出气泡）。
	page, err := store.GetMessagePage(ctx, session.ID, 0, 50)
	require.NoError(t, err)
	require.Equal(t, []string{
		"system:task difficulty guidance",
		"user:pwd",
		"assistant:",
		"tool:E:\\projects\\ai\\ai-agent-runtime",
		"assistant:Current working directory: E:\\projects\\ai\\ai-agent-runtime",
	}, messageRolesContents(page.Messages))
}

// 回归：如果早期 checkpoint 先以 [user] 写过一次（canonical 无锚点、投影被替换为
// [user]），收尾写入也必须把 user 追加进 canonical，而不是相信投影前缀已经落库。
func TestSQLiteSessionStorageKeepsUserWhenProjectionAheadOfCanonical(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteSessionStorage(t, nil)

	session := NewSession("projection-ahead-user")
	require.NoError(t, store.Save(ctx, session))
	session.ReplaceHistory([]types.Message{*types.NewSystemMessage("task difficulty guidance")})
	require.NoError(t, store.Update(ctx, session))

	// 早期 checkpoint：system-stripped 转录只有 user。
	checkpoint, err := store.Load(ctx, session.ID)
	require.NoError(t, err)
	checkpoint.ReplaceHistory([]types.Message{*types.NewUserMessage("pwd")})
	require.NoError(t, store.Update(ctx, checkpoint))

	// 收尾：完整回合转录（user → assistant(tool_call) → tool → assistant）。
	final, err := store.Load(ctx, session.ID)
	require.NoError(t, err)
	toolCall := *types.NewAssistantMessage("")
	toolCall.ToolCalls = []types.ToolCall{{
		ID:   "call_1",
		Type: "function",
		Name: "shell",
		Args: map[string]interface{}{"command": "pwd"},
	}}
	final.ReplaceHistory([]types.Message{
		*types.NewUserMessage("pwd"),
		toolCall,
		*types.NewToolMessage("call_1", "E:\\projects\\ai\\ai-agent-runtime"),
		*types.NewAssistantMessage("Current working directory: E:\\projects\\ai\\ai-agent-runtime"),
	})
	require.NoError(t, store.Update(ctx, final))

	canonical := rawCanonicalContents(t, store, session.ID)
	require.Equal(t, []string{
		"system:task difficulty guidance",
		"user:pwd",
		"assistant:",
		"tool:E:\\projects\\ai\\ai-agent-runtime",
		"assistant:Current working directory: E:\\projects\\ai\\ai-agent-runtime",
	}, canonical, "投影先于 canonical 时，收尾写入不得跳过投影里未落库的消息")
}

func messageRolesContents(messages []types.Message) []string {
	contents := make([]string, 0, len(messages))
	for _, message := range messages {
		contents = append(contents, message.Role+":"+message.Content)
	}
	return contents
}
