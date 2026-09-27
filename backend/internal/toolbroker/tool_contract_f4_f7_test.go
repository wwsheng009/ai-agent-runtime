package toolbroker

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// F7：等待预算必须"未耗尽也可读"——宿主在每个等待段后都会盖章（stamp），
// 模型才能看到剩余额度，而不是等到 suspend 判据出现才知道。
func TestStampAgentWaitBudgetExposesObservedBudget(t *testing.T) {
	stamped := StampAgentWaitBudget(&AgentWaitResult{}, 2, 6)
	require.Equal(t, 2, stamped.WaitBudgetConsecutive)
	require.Equal(t, 6, stamped.WaitBudgetLimit)
	require.False(t, stamped.WaitBudgetExhausted, "stamping must not change the verdict")

	// limit<=0（预算未装配 / 无挂起 turn）与 nil 都是 no-op。
	require.Nil(t, StampAgentWaitBudget(nil, 2, 6))
	untouched := StampAgentWaitBudget(&AgentWaitResult{}, 3, 0)
	require.Zero(t, untouched.WaitBudgetLimit)

	// 耗尽路径同时携带计数与 suspend 判据。
	exhausted := SuspendAgentWaitResultForBudget(&AgentWaitResult{TimedOut: true, PendingCount: 1}, 6, 6)
	require.True(t, exhausted.WaitBudgetExhausted)
	require.Equal(t, 6, exhausted.WaitBudgetConsecutive)
	require.Equal(t, 6, exhausted.WaitBudgetLimit)
	require.True(t, strings.HasPrefix(exhausted.NextAction, "suspend:"))
}

// F7：超时返回的 next_action 必须指向只读巡检原语（read_agent_events
// view=tool_progress / subagent_status），让"再等一次"之前先拿到便宜证据。
func TestFinalizeAgentWaitResultTimedOutNamesCheapProbes(t *testing.T) {
	pending := FinalizeAgentWaitResult(&AgentWaitResult{
		TimedOut:     true,
		PendingCount: 1,
		PendingIDs:   []string{"child-1"},
	}, time.Now().Add(-time.Second))
	require.True(t, strings.HasPrefix(pending.NextAction, "continue_independent_work_before_waiting_again"))
	require.Contains(t, pending.NextAction, "read_agent_events(view=tool_progress")
	require.Contains(t, pending.NextAction, "subagent_status")

	plain := FinalizeAgentWaitResult(&AgentWaitResult{TimedOut: true}, time.Now().Add(-time.Second))
	require.Contains(t, plain.NextAction, "read_agent_events")
	require.Contains(t, plain.NextAction, "subagent_status")
}

// F4：send_message 的回执必须显式说明"不会开新 turn"；目标空闲时还要给出
// followup_task / send_input 的可执行替代，避免把 delivered 误读成"会被处理"。
func TestBrokerSendMessageSummaryStatesNoTurn(t *testing.T) {
	broker := &Broker{AgentSessions: idleSendMessageController{
		fakeAgentSessionController: &fakeAgentSessionController{},
	}}

	_, meta, err := broker.Execute(context.Background(), "parent-session", ToolSendMessage, map[string]interface{}{
		"target":  "child-1",
		"message": "note only",
	})
	require.NoError(t, err)
	require.Equal(t, false, meta["turn_started"])
	require.Equal(t, true, meta["no_turn_expected"], "an idle child cannot consume the message on its own")
	require.Contains(t, meta["next_action"], "followup_task")

	_, followMeta, err := broker.Execute(context.Background(), "parent-session", ToolFollowupTask, map[string]interface{}{
		"target":  "child-1",
		"message": "start now",
	})
	require.NoError(t, err)
	require.NotContains(t, followMeta, "turn_started",
		"followup_task starts a turn; the no-turn receipt is send_message-only")
}

// idleSendMessageController overrides only SendMessage: the fake base type
// supplies the rest of the controller surface.
type idleSendMessageController struct {
	*fakeAgentSessionController
}

func (c idleSendMessageController) SendMessage(ctx context.Context, fromSessionID string, args AgentMessageArgs) (*AgentMessageResult, error) {
	return &AgentMessageResult{
		TargetSessionID: "child-1",
		Delivered:       true,
		Queued:          true,
		Status:          &AgentStatusResult{ID: "child-1", SessionID: "child-1", Status: "idle"},
	}, nil
}
