package commands

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimellm "github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// P0-3a/M5 CLI 宿主三态语义矩阵（ADR-3）。与 API 宿主
// (internal/api/skills/session_agent_controller_test.go) 成对断言同一契约。

func newLocalAgentSemanticsHost(t *testing.T, v2 bool) (*localChatRuntimeHost, string) {
	t.Helper()
	manager, userID, _, err := newChatSessionManager(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(manager.Stop)
	rootSession, err := manager.Create(context.Background(), userID)
	require.NoError(t, err)
	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = teamStore.Close() })
	llmRuntime := runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{})
	host := newLocalOrchestrationTestHost(t, manager, userID, llmRuntime, teamStore)
	host.BaseSession = &ChatSession{
		RuntimeSession: rootSession,
		SessionUserID:  userID,
	}
	host.supervisionConfig = supervision.Config{MessageSemanticsV2: v2}
	return host, rootSession.ID
}

func markLocalAgentBusy(t *testing.T, host *localChatRuntimeHost, sessionID string) {
	t.Helper()
	actor, err := host.SessionHub.GetOrCreate(sessionID)
	require.NoError(t, err)
	require.NoError(t, actor.UpdateStateForTest(context.Background(), func(state *runtimechat.RuntimeState) error {
		state.Status = runtimechat.SessionRunning
		state.UpdatedAt = time.Now().UTC()
		return nil
	}))
}

func TestLocalActorRegistryV2SendInputBusyQueuesInsteadOfError(t *testing.T) {
	host, rootSessionID := newLocalAgentSemanticsHost(t, true)
	ctx := context.Background()
	registry := host.ActorRegistry
	_, err := registry.Spawn(ctx, rootSessionID, toolbroker.SpawnAgentArgs{ID: "cli-v2-sendinput-child"})
	require.NoError(t, err)
	markLocalAgentBusy(t, host, "cli-v2-sendinput-child")

	result, err := registry.SendInput(ctx, toolbroker.SendAgentInputArgs{
		ID:      "cli-v2-sendinput-child",
		Message: "queued cli input",
	})
	require.NoError(t, err, "v2 send_input(interrupt=false) on a busy child must queue instead of failing")
	require.NotNil(t, result)
	assert.True(t, result.Queued)
	assert.True(t, result.Delivered)
	assert.False(t, result.Triggered)

	events, err := host.EventStore.ListEvents(ctx, "cli-v2-sendinput-child", 0, 10)
	require.NoError(t, err)
	mailboxDelivered := false
	auditSeen := false
	for _, event := range events {
		switch event.Type {
		case runtimechat.EventMailboxReceived:
			mailboxDelivered = true
			metadata, ok := event.Payload["metadata"].(map[string]interface{})
			require.True(t, ok)
			assert.Equal(t, true, metadata["trigger_turn"])
			assert.Equal(t, runtimechat.MailboxDeliveryStatusQueued, metadata["mailbox_delivery_status"])
		case runtimechat.EventMailboxDelivery:
			auditSeen = true
			assert.Equal(t, runtimechat.MailboxDeliveryStatusQueued, event.Payload["mailbox_delivery_status"])
			assert.Equal(t, toolbroker.ToolSendInput, event.Payload["tool"])
			assert.Equal(t, "cli-v2-sendinput-child", event.Payload["target_session_id"])
		}
	}
	assert.True(t, mailboxDelivered, "busy send_input must write the durable mailbox row")
	assert.True(t, auditSeen, "busy send_input must write the delivery audit event")
}

func TestLocalActorRegistryV2SendInputApprovalFirstNextAction(t *testing.T) {
	host, rootSessionID := newLocalAgentSemanticsHost(t, true)
	ctx := context.Background()
	registry := host.ActorRegistry
	_, err := registry.Spawn(ctx, rootSessionID, toolbroker.SpawnAgentArgs{ID: "cli-v2-approval-child"})
	require.NoError(t, err)
	markLocalAgentBusy(t, host, "cli-v2-approval-child")
	actor, err := host.SessionHub.GetOrCreate("cli-v2-approval-child")
	require.NoError(t, err)
	require.NoError(t, actor.UpdateStateForTest(ctx, func(state *runtimechat.RuntimeState) error {
		state.Status = runtimechat.SessionWaitingApproval
		state.PendingApproval = &runtimechat.ApprovalRequest{ID: "apr-cli-1", Reason: "rm -rf ./tmp"}
		state.UpdatedAt = time.Now().UTC()
		return nil
	}))

	result, err := registry.SendInput(ctx, toolbroker.SendAgentInputArgs{
		ID:      "cli-v2-approval-child",
		Message: "steer while blocked on approval",
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Queued, "AC-P2-7b: 审批阻塞时 steer 必须保持排队，不得绕过闸门")
	assert.Contains(t, result.NextAction, "approval_first", "AC-P2-7b: 回执必须给出审批优先引导")
	assert.Contains(t, result.NextAction, "apr-cli-1", "引导必须点名待批的 approval id")
	assert.Contains(t, result.NextAction, "resolve_agent_approval", "引导必须指向审批解决工具")
}

func TestLocalActorRegistryWaitEndsOnCallerSteerInterrupt(t *testing.T) {
	host, rootSessionID := newLocalAgentSemanticsHost(t, true)
	registry := host.ActorRegistry
	_, err := registry.Spawn(context.Background(), rootSessionID, toolbroker.SpawnAgentArgs{ID: "cli-steer-wait-child"})
	require.NoError(t, err)
	markLocalAgentBusy(t, host, "cli-steer-wait-child")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resultCh := make(chan *toolbroker.AgentWaitResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, waitErr := registry.Wait(ctx, toolbroker.WaitAgentArgs{ID: "cli-steer-wait-child", TimeoutMs: 30000})
		if waitErr != nil {
			errCh <- waitErr
			return
		}
		resultCh <- result
	}()

	time.Sleep(150 * time.Millisecond)
	cancel()

	select {
	case waitErr := <-errCh:
		t.Fatalf("AC-P2-7e: steer 打断后等待段应返回可读结果而不是硬失败: %v", waitErr)
	case result := <-resultCh:
		require.NotNil(t, result)
		assert.True(t, result.Interrupted, "AC-P2-7e: 调用方被 steer/打断 ⇒ 等待段立即结束")
		assert.False(t, result.TimedOut, "被打断的等待不得谎报为观测窗口超时")
		assert.True(t, result.ExecutionContinues, "子代理继续运行，不被取消")
		assert.Contains(t, result.NextAction, "steer_pending")
	case <-time.After(10 * time.Second):
		t.Fatal("AC-P2-7e: steer 打断后等待段必须立即返回")
	}
}

func TestLocalActorRegistryWaitEndsOnQueuedUserInput(t *testing.T) {
	host, rootSessionID := newLocalAgentSemanticsHost(t, true)
	ctx := context.Background()
	registry := host.ActorRegistry
	_, err := registry.Spawn(ctx, rootSessionID, toolbroker.SpawnAgentArgs{ID: "cli-steer-queue-child"})
	require.NoError(t, err)
	markLocalAgentBusy(t, host, "cli-steer-queue-child")
	host.BaseSession.InputQueue = newChatInputQueue(nil)
	host.BaseSession.InputQueue.routeLine(chatQueuedInput{Text: "steer while waiting", Source: "test"})

	result, err := registry.Wait(ctx, toolbroker.WaitAgentArgs{ID: "cli-steer-queue-child", TimeoutMs: 30000})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Interrupted, "AC-P2-7e: 用户新输入（steer）必须立即结束等待段")
	assert.False(t, result.TimedOut, "被 steer 结束的等待不得谎报为观测窗口超时")
	assert.Contains(t, result.NextAction, "steer_pending")
}

func TestLocalActorRegistryV1SendInputBusyKeepsLegacyError(t *testing.T) {
	host, rootSessionID := newLocalAgentSemanticsHost(t, false)
	ctx := context.Background()
	registry := host.ActorRegistry
	_, err := registry.Spawn(ctx, rootSessionID, toolbroker.SpawnAgentArgs{ID: "cli-v1-sendinput-child"})
	require.NoError(t, err)
	markLocalAgentBusy(t, host, "cli-v1-sendinput-child")

	_, err = registry.SendInput(ctx, toolbroker.SendAgentInputArgs{
		ID:      "cli-v1-sendinput-child",
		Message: "should still fail",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "busy")

	events, err := host.EventStore.ListEvents(ctx, "cli-v1-sendinput-child", 0, 10)
	require.NoError(t, err)
	for _, event := range events {
		assert.NotEqual(t, runtimechat.EventMailboxDelivery, event.Type, "v1 must not write v2 audit events")
	}
}

func TestLocalActorRegistryV2TerminalTargetReturnsSessionClosed(t *testing.T) {
	host, rootSessionID := newLocalAgentSemanticsHost(t, true)
	ctx := context.Background()
	registry := host.ActorRegistry
	_, err := registry.Spawn(ctx, rootSessionID, toolbroker.SpawnAgentArgs{ID: "cli-v2-terminal-child"})
	require.NoError(t, err)
	_, err = registry.Close(ctx, "cli-v2-terminal-child")
	require.NoError(t, err)

	_, err = registry.SendMessage(ctx, rootSessionID, toolbroker.AgentMessageArgs{
		Target:  "cli-v2-terminal-child",
		Message: "late message",
	})
	require.ErrorIs(t, err, toolbroker.ErrAgentSessionClosed)

	_, err = registry.FollowupTask(ctx, rootSessionID, toolbroker.AgentMessageArgs{
		Target:  "cli-v2-terminal-child",
		Message: "late followup",
	})
	require.ErrorIs(t, err, toolbroker.ErrAgentSessionClosed)

	_, err = registry.SendInput(ctx, toolbroker.SendAgentInputArgs{
		ID:      "cli-v2-terminal-child",
		Message: "late input",
	})
	require.ErrorIs(t, err, toolbroker.ErrAgentSessionClosed)
	assert.Contains(t, err.Error(), "next_action=inspect|finalize", "AC-P2-7d: 终态回执必须带 next_action")

	events, err := host.EventStore.ListEvents(ctx, "cli-v2-terminal-child", 0, 20)
	require.NoError(t, err)
	failures := 0
	for _, event := range events {
		if event.Type == runtimechat.EventMailboxDelivery && event.Payload["mailbox_delivery_status"] == runtimechat.MailboxDeliveryStatusFailed {
			failures++
		}
	}
	assert.GreaterOrEqual(t, failures, 1, "terminal rejection must be audited")
}

func TestLocalActorRegistryV2FollowupBusyReportsQueued(t *testing.T) {
	host, rootSessionID := newLocalAgentSemanticsHost(t, true)
	ctx := context.Background()
	registry := host.ActorRegistry
	_, err := registry.Spawn(ctx, rootSessionID, toolbroker.SpawnAgentArgs{ID: "cli-v2-followup-child"})
	require.NoError(t, err)
	markLocalAgentBusy(t, host, "cli-v2-followup-child")

	result, err := registry.FollowupTask(ctx, rootSessionID, toolbroker.AgentMessageArgs{
		Target:  "cli-v2-followup-child",
		Message: "queued cli followup",
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Delivered)
	assert.True(t, result.Queued)
	assert.False(t, result.Triggered)
}
