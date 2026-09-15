package skills

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// 外部传入的会话 ID 只有在规范化下保持不变时才能按原字符串读回（见
// chat.IsAddressableSessionID）。带路径分隔符的 ID 会落一条“列表可见、打开即
// 404”的孤儿记录——线上真实出现过 "/root/p26s3b" 和 "<nil>" 两条，前端因此
// 反复报 404。控制器必须在落库前拒绝，并且不留下任何半成品会话。
func TestSessionAgentController_SpawnRejectsUnaddressableSessionID(t *testing.T) {
	ctx := context.Background()
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	defer sessionManager.Stop()
	handler.SetSessionManager(sessionManager)

	root, err := sessionManager.Create(ctx, "user-unaddressable-id")
	require.NoError(t, err)
	controller := handler.getAgentSessionController()
	require.NotNil(t, controller)

	for _, id := range []string{"/root/p26s3b", `dir\child`, "p26s3b/", "<nil>"} {
		_, spawnErr := controller.Spawn(ctx, root.ID, toolbroker.SpawnAgentArgs{ID: id})
		require.Error(t, spawnErr, "id %q must be rejected", id)
		require.Contains(t, spawnErr.Error(), "invalid session id", "id %q", id)

		_, getErr := sessionManager.Get(ctx, id)
		require.Error(t, getErr, "id %q must not exist after rejection", id)
	}
}

// 对照用例：规范化下不变的 ID（含 agent 派生会话命名）必须继续可用，
// 否则这次加固会误伤真实的多 agent 工作流。
func TestSessionAgentController_SpawnAcceptsAddressableSessionID(t *testing.T) {
	ctx := context.Background()
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	defer sessionManager.Stop()
	handler.SetSessionManager(sessionManager)

	root, err := sessionManager.Create(ctx, "user-addressable-id")
	require.NoError(t, err)
	controller := handler.getAgentSessionController()
	require.NotNil(t, controller)

	// 不注入 hub/配额，spawn 会在更靠后的阶段失败；这里只断言它越过了 ID 校验，
	// 即错误不再是 "invalid session id"。
	_, spawnErr := controller.Spawn(ctx, root.ID, toolbroker.SpawnAgentArgs{ID: "note-a"})
	if spawnErr != nil {
		require.NotContains(t, spawnErr.Error(), "invalid session id")
	}
}
