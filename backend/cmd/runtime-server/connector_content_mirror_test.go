package main

// S1b（docs/bots/13 §8/§4.2）：runtime-server 宿主的内容镜像接线——
// Backend.Handle 与 runner DispatchTurn 都必须把「IM 回合活动登记」回调交给
// SubmitConnectorPrompt（L3 判据）；登记使用解析后的最终会话 id，且在提交
// 窗口内有效（内容镜像据此跳过 IM 回合，避免与回复路径双发）。

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

func TestRuntimeConnectorBackendRegistersTurnActivity(t *testing.T) {
	tracker := connectors.NewTurnActivityTracker()
	host := &fakeRuntimeConnectorHost{resolvedSession: "sess-resolved", submitReply: "ok"}
	host.activityProbe = tracker.Active
	backend := runtimeConnectorBackend{handler: host, logf: func(string, ...any) {}, turns: tracker}

	result, err := backend.Handle(context.Background(), connectors.BackendRequest{
		ConnectorID: "c1",
		Chat:        "chat-1",
		Text:        "你好",
	}, nil)
	require.NoError(t, err)
	require.Len(t, result.Reply, 1)
	require.Equal(t, "ok", result.Reply[0].Text)
	require.Equal(t, "sess-resolved", result.Session.SessionID)

	// 请求必须携带登记回调（L3）；登记窗口覆盖提交阶段（内容镜像会跳过该回合）。
	request := host.lastSubmitSnapshot()
	require.NotNil(t, request.RegisterActivity, "Handle must pass RegisterActivity for L3")
	require.True(t, host.activeDuringSubmit, "activity registration must be live during submit")
}

func TestRuntimeConnectorRunnerRegistersTurnActivity(t *testing.T) {
	tracker := connectors.NewTurnActivityTracker()
	host := &fakeRuntimeConnectorHost{resolvedSession: "sess-runner", submitReply: "ok"}
	host.activityProbe = tracker.Active
	runner := runtimeConnectorRunner{handler: host, turns: tracker}

	result, err := runner.DispatchTurn(context.Background(), connectors.DispatchRequest{
		ConnectorID: "c1",
		Text:        "派发",
		Session:     connectors.SessionRef{SessionID: "sess-bind"},
	})
	require.NoError(t, err)
	require.Len(t, result.Reply, 1)
	require.Equal(t, "sess-runner", result.Session.SessionID)

	request := host.lastSubmitSnapshot()
	require.NotNil(t, request.RegisterActivity, "runner dispatch must pass RegisterActivity for L3")
	require.True(t, host.activeDuringSubmit, "runner activity registration must be live during submit")

	// 未装配 tracker：回调为 nil（不登记，向后兼容）。
	plain := runtimeConnectorRunner{handler: host}
	_, err = plain.DispatchTurn(context.Background(), connectors.DispatchRequest{Text: "x"})
	require.NoError(t, err)
	require.Nil(t, host.lastSubmitSnapshot().RegisterActivity)
}
