// runtime-server 本地 backend 的会话引用形态测试（2026-10-17 真机 qq-bot1
// 双 actor 复盘）：Handle 返回的 SessionRef 必须回填本宿主 NodeID——空
// NodeID 会把绑定归属抹空，下一条消息落进假 R2a 接管（反复回写 + 「原节点
// (空) 掉线」提示）。aicli 侧同款修复见 TestLocalConnectorBackendSessionRefCarriesHostNodeID。
package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

func TestRuntimeConnectorBackendHandleFillsHostNodeID(t *testing.T) {
	host := &fakeRuntimeConnectorHost{resolvedSession: "sess-unit", submitReply: "pong"}
	backend := runtimeConnectorBackend{handler: host, logf: func(string, ...any) {}}

	result, err := backend.Handle(context.Background(), connectors.BackendRequest{
		ConnectorID: "c1",
		Chat:        "chat-1",
		Text:        "hi",
	}, nil)
	require.NoError(t, err)
	require.Equal(t, connectors.HostKindRuntimeServer, result.Session.NodeID,
		"Handle must echo the host NodeID to protect the binding ledger")
	require.Equal(t, "sess-unit", result.Session.SessionID)
	require.Len(t, result.Reply, 1)
	require.Equal(t, "pong", result.Reply[0].Text)
}
