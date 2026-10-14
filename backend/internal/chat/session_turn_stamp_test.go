package chat

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// StampMessageTurnID 是回合身份对齐（2026-10-14）的落库侧入口：调用方已知
// turn_id 时补盖到消息上，EnsureMessageIdentity 的「已有即保留」语义保证
// 不再自造新 id——durable 历史与 runtime 事件流由此共用同一 turn 空间。
func TestStampMessageTurnIDStampsMissingTurnID(t *testing.T) {
	msg := runtimetypes.NewUserMessage("hello")
	stamped := StampMessageTurnID(msg, "turn_client_1")
	require.True(t, stamped)
	require.Equal(t, "turn_client_1", runtimetypes.TurnID(*msg))
}

func TestStampMessageTurnIDKeepsExistingTurnID(t *testing.T) {
	msg := runtimetypes.NewUserMessage("hello")
	msg.Metadata.Set(runtimetypes.MetadataKeyTurnID, "turn_existing")
	stamped := StampMessageTurnID(msg, "turn_client_1")
	require.False(t, stamped, "已带 turn_id 的消息绝不改写（补缺语义）")
	require.Equal(t, "turn_existing", runtimetypes.TurnID(*msg))
}

func TestStampMessageTurnIDIgnoresBlankTurnIDAndNilMessage(t *testing.T) {
	msg := runtimetypes.NewUserMessage("hello")
	require.False(t, StampMessageTurnID(msg, "   "))
	require.Empty(t, runtimetypes.TurnID(*msg), "空 turnID 是 no-op，不落空串")
	require.False(t, StampMessageTurnID(nil, "turn_x"))
}

func TestStampMessageTurnIDInitializesMetadata(t *testing.T) {
	msg := runtimetypes.NewUserMessage("hello")
	msg.Metadata = nil
	require.True(t, StampMessageTurnID(msg, "turn_client_1"))
	require.Equal(t, "turn_client_1", runtimetypes.TurnID(*msg))
}

// 落库链路契约：盖章后的 user 消息经 AddMessage（内部 EnsureMessageIdentity）
// 保留 caller 的 turn_id，且同回合的 assistant/tool 行继承它——历史 turn 空间
// 与调用方（事件流）一致。
func TestAddMessagePreservesStampedTurnIDAcrossTurnRows(t *testing.T) {
	session := NewSession("user-1")
	userMsg := runtimetypes.NewUserMessage("hello")
	require.True(t, StampMessageTurnID(userMsg, "turn_client_1"))
	session.AddMessage(*userMsg)
	session.AddMessage(*runtimetypes.NewAssistantMessage("working"))
	session.AddMessage(*runtimetypes.NewToolMessage("call-1", "tool out"))
	session.AddMessage(*runtimetypes.NewAssistantMessage("final answer"))

	history := session.GetMessages()
	require.Len(t, history, 4)
	for index, msg := range history {
		require.Equal(t, "turn_client_1", runtimetypes.TurnID(msg),
			"row %d (%s) must stay in the caller's turn space", index, msg.Role)
	}
	// 幂等：重复 EnsureHistoryMessageIdentities 不改写任何行。
	require.False(t, runtimetypes.EnsureHistoryMessageIdentities(history))
}

// 无盖章时保持旧行为（兼容存量路径）：EnsureMessageIdentity 自造 turn_<uuid>。
func TestAddMessageWithoutStampStillMintsTurnID(t *testing.T) {
	session := NewSession("user-1")
	session.AddMessage(*runtimetypes.NewUserMessage("hello"))
	turnID := runtimetypes.TurnID(session.GetMessages()[0])
	require.True(t, strings.HasPrefix(turnID, "turn_"), "旧行为必须保留：自造 turn_<uuid>")
}
