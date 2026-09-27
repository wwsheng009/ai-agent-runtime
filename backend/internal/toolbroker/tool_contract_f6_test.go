package toolbroker

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// F6：close_agent 必须接受 spawn_subagents 的 batch_id——批次收敛由宿主把
// batch id 展开为全部子会话，模型侧一次调用即可收敛整批。
func TestBrokerCloseAgentAcceptsBatchID(t *testing.T) {
	controller := &fakeAgentSessionController{}
	broker := &Broker{AgentSessions: controller}

	_, meta, err := broker.Execute(context.Background(), "parent-session", ToolCloseAgent, map[string]interface{}{
		"batch_id": "batch_727ab892152acd39",
	})
	require.NoError(t, err)
	require.Equal(t, "batch_727ab892152acd39", controller.lastClose,
		"the host resolves the batch id to its child sessions")
	require.Equal(t, "batch_727ab892152acd39", meta["session_id"])

	// 定义面必须文档化 batch_id（模型只能看见 schema 里存在的参数）。
	found := false
	for _, definition := range broker.Definitions() {
		if definition.Name != ToolCloseAgent {
			continue
		}
		properties, _ := definition.Parameters["properties"].(map[string]interface{})
		_, found = properties["batch_id"]
	}
	require.True(t, found, "close_agent must document batch_id")
}
