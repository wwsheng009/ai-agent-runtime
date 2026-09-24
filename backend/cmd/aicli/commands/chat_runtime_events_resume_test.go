package commands

import (
	"bufio"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
)

// newResumeTestBridge builds a bridge that has already finished one foreground
// run: the turn is retired, runActive is cleared — exactly the state
// session_20260924161011_sSHP4F7o was in when the user answered a question that
// had been asked before the drain timeout finalized the run.
func newResumeTestBridge(t *testing.T) *chatRuntimeEventBridge {
	t.Helper()
	bridge := newChatRuntimeEventBridge(&ChatSession{})
	bridge.setPrimarySessionID("session-resume")
	bridge.renderMu.Lock()
	bridge.runStarted = true
	bridge.runEpoch = 3
	bridge.activeTurnID = "turn-1"
	bridge.retiredTurnIDs = map[string]struct{}{"turn-1": {}}
	bridge.retiredTurnOrder = []string{"turn-1"}
	bridge.runActive = false
	bridge.renderMu.Unlock()
	return bridge
}

func resumedTurnEvent(eventType string) runtimeevents.Event {
	return runtimeevents.Event{
		Type:      eventType,
		SessionID: "session-resume",
		Payload: map[string]interface{}{
			"question_id": "q-1",
			"turn_id":     "turn-1",
		},
	}
}

// TestChatRuntimeEventBridge_ResumedQuestionAnswerNeedsExplicitResume pins the
// silent-loss path from the real session: after EndRun the resumed execution of
// a still-pending question carries the retired turn id, so every one of its
// events was dropped with reason="event turn does not match active run" — the
// model kept working while the page showed nothing, which the user read as "the
// answer never reached the server". Flagging the turn alone is not enough; the
// run epoch must actually be reopened.
func TestChatRuntimeEventBridge_ResumedQuestionAnswerNeedsExplicitResume(t *testing.T) {
	bridge := newResumeTestBridge(t)
	event := resumedTurnEvent("question_answered")

	require.True(t, bridge.shouldSuppressMismatchedPrimaryTurnEvent(event),
		"a retired turn must stay suppressed while no resume is expected")

	bridge.expectResumedTurnAfterAnswer("q-1")
	require.Equal(t, "turn-1", bridge.resumeTurnID,
		"the answer must flag the most recently finished turn as resumable")
	require.True(t, bridge.shouldSuppressMismatchedPrimaryTurnEvent(event),
		"flagging alone must not let a retired turn mutate the viewport")
}

// TestChatRuntimeEventBridge_ResumedTurnAdoptsRunEpoch pins the fix: the first
// event of the flagged turn reopens a run epoch (and un-retires the turn), so
// the rest of the resumed timeline — LLM continuation, assistant message —
// reaches the render pipeline instead of being dropped.
func TestChatRuntimeEventBridge_ResumedTurnAdoptsRunEpoch(t *testing.T) {
	bridge := newResumeTestBridge(t)
	bridge.expectResumedTurnAfterAnswer("q-1")

	event := resumedTurnEvent("question_answered")
	bridge.maybeAdoptResumedPrimaryTurn(event)

	require.True(t, bridge.isRunActive(), "the resumed turn must own a run epoch")
	require.Equal(t, "turn-1", bridge.activeTurnID)
	require.Equal(t, "turn-1", bridge.adoptedTurnID, "the resumed run closes on its own session_end")
	require.Empty(t, bridge.resumeTurnID, "the resume expectation is consumed once")
	_, retired := bridge.retiredTurnIDs["turn-1"]
	require.False(t, retired, "the resumed turn must no longer be retired")
	require.Empty(t, bridge.retiredTurnOrder, "retire ordering must not keep the revived turn")

	require.False(t, bridge.shouldSuppressMismatchedPrimaryTurnEvent(resumedTurnEvent("assistant_message")),
		"the resumed timeline must render")
	require.False(t, bridge.shouldSuppressMismatchedPrimaryTurnEvent(resumedTurnEvent("tool.completed")),
		"tool boundaries of the resumed turn must render too")
	require.True(t, bridge.shouldEndAdoptedRun(resumedTurnEvent("session_end")),
		"the adopted run must close on the resumed turn's session_end")
}

// TestChatRuntimeEventBridge_ResumeExpectationIgnoresForeignTurn keeps the fix
// narrow: only the flagged turn may reopen a run, and only while no run owns the
// render surface.
func TestChatRuntimeEventBridge_ResumeExpectationIgnoresForeignTurn(t *testing.T) {
	bridge := newResumeTestBridge(t)
	bridge.expectResumedTurnAfterAnswer("q-1")

	foreign := runtimeevents.Event{
		Type:      "assistant_message",
		SessionID: "session-resume",
		Payload:   map[string]interface{}{"turn_id": "turn-2"},
	}
	bridge.maybeAdoptResumedPrimaryTurn(foreign)
	require.False(t, bridge.isRunActive(), "an unrelated turn must not adopt a run")
	require.Equal(t, "turn-1", bridge.resumeTurnID)
	require.True(t, bridge.shouldSuppressMismatchedPrimaryTurnEvent(foreign))
}

// TestChatRuntimeEventDrainSnapshot_DistinguishesBacklogFromRenderLag pins the
// EndRun verdict rule: a drain timeout may only fail the run when events are
// genuinely undelivered. In the real session every turn ended as
// session_end success=false although the whole timeline had been delivered —
// the timeout only reflected the UI actor still catching up.
func TestChatRuntimeEventDrainSnapshot_DistinguishesBacklogFromRenderLag(t *testing.T) {
	settled := chatRuntimeEventDrainSnapshot{Enqueued: 120, Processed: 120}
	require.False(t, settled.undeliveredData(),
		"render-side lag must not fail the run")
	require.Equal(t, "enqueued=120 processed=120 critical_pending=0 deferred_backlog=0", settled.describe())

	unprocessed := chatRuntimeEventDrainSnapshot{Enqueued: 121, Processed: 120}
	require.True(t, unprocessed.undeliveredData(), "an unprocessed event is real backlog")

	critical := chatRuntimeEventDrainSnapshot{Enqueued: 120, Processed: 120, CriticalPending: 1}
	require.True(t, critical.undeliveredData(), "a critical event in flight is real backlog")

	deferred := chatRuntimeEventDrainSnapshot{Enqueued: 120, Processed: 120, DeferredBacklog: 2}
	require.True(t, deferred.undeliveredData(), "a deferred backlog is real backlog")

	require.False(t, (*chatRuntimeEventBridge)(nil).drainSnapshot().undeliveredData(),
		"a nil bridge reports a settled pipeline")
}

// TestChatRuntimeEventBridge_ExternalInputCaptureSkipsConsolePrompt pins the
// consumer-deadlock fix. With a micro-web/remote input capture owning the input
// face there is no console reader, so askQuestion/askApproval must never run
// from the event worker: the blocking prompt pinned processedEvents at 20 while
// enqueued grew to 42, so every later event — including the turn's own
// session_end and the continuation after the web answer — was never delivered,
// EndRun hit its drain timeout and finalized each run as failed.
func TestChatRuntimeEventBridge_ExternalInputCaptureSkipsConsolePrompt(t *testing.T) {
	session := &ChatSession{}
	session.InputQueue = newChatInputQueue(bufio.NewReader(strings.NewReader("")))
	bridge := newChatRuntimeEventBridge(session)
	bridge.writeLine = func(string) {}

	prompted := make(chan string, 2)
	bridge.askQuestion = func(prompt string, _ []string, _ bool) (string, error) {
		prompted <- "question:" + prompt
		return "", errChatInteractivePromptResolvedElsewhere
	}
	bridge.askApproval = func(*runtimechat.ApprovalRequest, []string) (chatApprovalAnswer, error) {
		prompted <- "approval"
		return chatApprovalAnswer{}, errChatInteractivePromptResolvedElsewhere
	}

	require.False(t, bridge.externalInputCaptureOwnsInput(),
		"a console-owned session must keep the legacy prompt path")
	session.InputQueue.setExternalInputCaptureActive(true)
	require.True(t, bridge.externalInputCaptureOwnsInput(),
		"an active input capture owns the input face")

	done := make(chan struct{})
	go func() {
		defer close(done)
		bridge.handleEvent(runtimeevents.Event{
			Type:      runtimechat.EventQuestionAsked,
			SessionID: "session-web-capture",
			Payload: map[string]interface{}{
				"question_id": "q-1",
				"prompt":      "继续吗?",
			},
		})
		bridge.handleEvent(runtimeevents.Event{
			Type:      runtimechat.EventApprovalRequested,
			SessionID: "session-web-capture",
			Payload: map[string]interface{}{
				"request_id": "r-1",
				"tool_name":  "shell",
				"reason":     "需要执行命令",
			},
		})
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handleEvent blocked on a console prompt although an external input capture owns the input")
	}
	select {
	case got := <-prompted:
		t.Fatalf("console prompt %q must not run while an external input capture owns the input", got)
	default:
	}
}
