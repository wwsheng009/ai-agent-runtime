package chat

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
)

// TestHandleApproveToolForwardsDecisionOptions pins the §4.8 plumbing: the
// remember scope / feedback a host UI sends with approve_tool must reach the
// policy engine's waiter as an ApprovalResponse (the engine then routes the
// grant and puts the feedback into the deny reason).
func TestHandleApproveToolForwardsDecisionOptions(t *testing.T) {
	ctx := context.Background()
	// The harness leaves the actor in waiting_approval on "approval_late".
	actor, _, _, _, _ := newTerminalApprovalTestActor(t, ctx, "")

	const requestID = "approval_late"
	waiter := actor.registerApprovalWaiter(requestID)
	defer actor.unregisterApprovalWaiter(requestID)

	reply := make(chan error, 1)
	actor.handleApproveTool(ApproveTool{
		Ctx:             ctx,
		RequestID:       requestID,
		Allow:           true,
		RememberScope:   " PROJECT ",
		RememberPattern: "path:docs/a.md",
		Feedback:        "只改这一个文件",
		Reply:           reply,
	})
	require.NoError(t, <-reply)

	select {
	case response := <-waiter:
		assert.True(t, response.Allowed)
		assert.Equal(t, "PROJECT", response.RememberScope, "the actor trims, the engine normalizes case")
		assert.True(t, response.Remember, "an explicit scope must set Remember")
		assert.Equal(t, "path:docs/a.md", response.RememberPattern)
		assert.Equal(t, "只改这一个文件", response.Feedback)
	default:
		t.Fatal("the policy waiter did not receive the approval response")
	}
}

func TestApproveToolDecisionNormalizedRememberScope(t *testing.T) {
	cases := map[string]string{
		"":         "once",
		"once":     "once",
		"session":  "session",
		"PROJECT":  "project",
		" project": "project",
		"global":   "once", // unknown scope never widens to a durable grant
	}
	for input, want := range cases {
		assert.Equal(t, want, ApproveToolDecision{RememberScope: input}.NormalizedRememberScope(), "input=%q", input)
	}
}

func TestRequestApprovalExpiryRecordsOutcome(t *testing.T) {
	ctx := context.Background()
	actor, _, _, _, _ := newTerminalApprovalTestActor(t, ctx, "")
	const requestID = "approval-expiry-outcome"

	_, err := actor.RequestApproval(ctx, runtimepolicy.ApprovalRequest{
		ID:         requestID,
		ToolCallID: "call-expiry-outcome",
		ToolName:   "shell",
		ExpiresAt:  time.Now().Add(20 * time.Millisecond),
	})
	require.Error(t, err)
	require.True(t, runtimeerrors.Is(err, runtimeerrors.ErrApprovalExpired))

	resolution, resumed := actor.ApprovalOutcome(requestID)
	require.Equal(t, ApprovalResolutionExpired, resolution,
		"the timeout path must record expired so a later decision cannot report a zero-value allow")
	require.False(t, resumed)
}

func TestRetirePendingInteractionClosesParkedApproval(t *testing.T) {
	ctx := context.Background()
	actor, _, _, _, _ := newTerminalApprovalTestActor(t, ctx, "")

	require.NoError(t, actor.RetirePendingInteraction(ctx, "agent_session_closed"))
	resolution, resumed := actor.ApprovalOutcome("approval_late")
	require.Equal(t, ApprovalResolutionRunTerminated, resolution)
	require.False(t, resumed)

	state := actor.StateForInspection()
	require.NotNil(t, state)
	require.False(t, state.HasPendingInteraction())
	require.Equal(t, SessionStopped, state.Status)
}
