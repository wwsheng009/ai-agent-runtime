package chat

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
)

// TestSessionActorLoopConfigPropagatesOnProgress pins the actor passthrough for
// P0-1: a configured OnProgress callback must be injected into every run's
// ReAct config, and a nil callback must leave the loop config untouched.
func TestSessionActorLoopConfigPropagatesOnProgress(t *testing.T) {
	var got []string
	actor := &SessionActor{onProgress: func(kind string) {
		got = append(got, kind)
	}}
	cfg := actor.historyCheckpointLoopConfig(nil, nil, nil)
	require.NotNil(t, cfg)
	require.NotNil(t, cfg.OnProgress, "OnProgress 必须透传到 run 级 ReAct 配置")
	cfg.OnProgress(context.Background(), "llm_response")
	cfg.OnProgress(context.Background(), "iteration_end")
	require.Equal(t, []string{"llm_response", "iteration_end"}, got)

	plain := &SessionActor{}
	plainCfg := plain.historyCheckpointLoopConfig(nil, nil, nil)
	require.NotNil(t, plainCfg)
	require.Nil(t, plainCfg.OnProgress, "未配置回调时不得注入进度钩子")
}

// TestSessionActorStateTransitionProgressKinds pins the P0-1 §5.4 state
// transition kinds: entering/leaving an approval or input wait emits exactly
// one tick each, resolving a missing waiter emits nothing, and interrupting a
// live run emits one "interrupt" tick.
func TestSessionActorStateTransitionProgressKinds(t *testing.T) {
	var got []string
	actor := &SessionActor{
		onProgress:      func(kind string) { got = append(got, kind) },
		approvalWaiters: make(map[string]chan runtimepolicy.ApprovalResponse),
		questionWaiters: make(map[string]chan string),
	}
	_ = actor.registerApprovalWaiter("approval-1")
	require.True(t, actor.resolveApproval("approval-1", runtimepolicy.ApprovalResponse{Allowed: true}))
	require.False(t, actor.resolveApproval("approval-missing", runtimepolicy.ApprovalResponse{}))
	_ = actor.registerQuestionWaiter("question-1")
	require.True(t, actor.resolveQuestion("question-1", "yes"))
	require.False(t, actor.resolveQuestion("question-missing", "no"))

	actor.activeRun = &sessionRunControl{}
	require.NotNil(t, actor.interruptActiveSessionRun())

	require.Equal(t, []string{
		"approval_requested", "approval_resolved",
		"input_requested", "input_resolved",
		"interrupt",
	}, got)
}
