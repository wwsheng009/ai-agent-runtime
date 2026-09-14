package commands

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

func TestSuccessfulSendFreezesWorkedSummaryAtAPICompletion(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	executor := &fakeChatExecutor{output: "final assistant response"}
	session := &ChatSession{
		ChatExecutor: executor,
		cancelCtx:    context.Background(),
	}
	coord := newChatInteractionCoordinator(session)
	t.Cleanup(coord.Shutdown)
	session.Interaction = coord
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(80, 24)
	session.Surface = surface

	frameText := func() string {
		var text strings.Builder
		for _, row := range surface.ComposedFrameForTest() {
			for _, cell := range row {
				if !cell.Cont {
					text.WriteString(cell.Text)
				}
			}
			text.WriteByte('\n')
		}
		return text.String()
	}

	captureSurfaceStdout(t, func() {
		coord.SetWriter(os.Stdout)
		coord.SetSurface(surface)
		coord.PrintPrompt()
		coord.SetPromptInput("hello")
		coord.ResetPromptState()

		// This is the production order: commit the submitted user row first;
		// sendMessage then owns the single Waiting transition.
		renderSubmittedUserInputEcho(session, "hello")
		response, err := sendMessage(session, "hello")
		if err != nil {
			t.Fatalf("sendMessage: %v", err)
		}

		coord.waitUIActorIdle()
		afterAPI := frameText()
		if !strings.Contains(afterAPI, "Worked for ") {
			t.Fatalf("API completion did not freeze the work summary:\n%s", afterAPI)
		}
		if !strings.Contains(afterAPI, ">") {
			t.Fatalf("ready composer prompt missing after API completion:\n%s", afterAPI)
		}
		if state := coord.currentSurfaceStateForTest(); state != "Ready" {
			t.Fatalf("state after API completion=%q, want Ready", state)
		}

		finishSuccessfulChatSend(session, response, false)
		coord.waitUIActorIdle()
		afterFinalize := frameText()
		if !strings.Contains(afterFinalize, "final assistant response") {
			t.Fatalf("final response was not committed before activity cleared:\n%s", afterFinalize)
		}
		if !strings.Contains(afterFinalize, ">") {
			t.Fatalf("ready composer prompt missing after finalization:\n%s", afterFinalize)
		}
		if !strings.Contains(afterFinalize, "Worked for ") {
			t.Fatalf("completed activity summary missing after successful finalization:\n%s", afterFinalize)
		}
		if strings.Contains(afterFinalize, "Analyzing") {
			t.Fatalf("live activity should be replaced by the completion summary:\n%s", afterFinalize)
		}
		if state := coord.currentSurfaceStateForTest(); state != "Ready" {
			t.Fatalf("state after successful finalization=%q, want Ready", state)
		}
	})
}

func TestNextSendReplacesCompletedSummaryWithLiveActivity(t *testing.T) {
	coord := newChatInteractionCoordinator(&ChatSession{})
	t.Cleanup(coord.Shutdown)
	coord.StartWaiting()
	coord.mu.Lock()
	coord.dynamicStatusStarted = time.Now().Add(-5 * time.Second)
	coord.mu.Unlock()
	coord.CompleteWaiting()

	coord.mu.Lock()
	if !coord.dynamicStatusCompleted || coord.dynamicStatusCompletedElapsed < 5*time.Second {
		coord.mu.Unlock()
		t.Fatal("expected a frozen completion summary")
	}
	coord.mu.Unlock()

	coord.StartWaiting()
	coord.mu.Lock()
	defer coord.mu.Unlock()
	if coord.dynamicStatusCompleted {
		t.Fatal("new send must replace the prior completion summary")
	}
	if coord.dynamicStatusStarted.IsZero() {
		t.Fatal("new send must start a fresh live activity clock")
	}
}

func TestFailedSendClearsDynamicStatusWithoutSuccessfulFinalizer(t *testing.T) {
	executor := &fakeChatExecutor{err: errors.New("request failed")}
	session := &ChatSession{
		ChatExecutor: executor,
		cancelCtx:    context.Background(),
	}
	coord := newChatInteractionCoordinator(session)
	t.Cleanup(coord.Shutdown)
	session.Interaction = coord

	if _, err := sendMessage(session, "hello"); err == nil {
		t.Fatal("expected send failure")
	}
	if state := coord.currentSurfaceStateForTest(); state != "Ready" {
		t.Fatalf("failed send must release Waiting without finalizer, state=%q", state)
	}
	coord.mu.Lock()
	defer coord.mu.Unlock()
	if coord.dynamicStatusCompleted {
		t.Fatal("failed send must not publish a successful Worked for summary")
	}
}

// TestLateCompleteWaitingDoesNotFreezeSummaryWhenInternalRunTookOver covers
// the production interleaving behind the "Worked for 28m 41s but still
// running" report: sendMessage's CompleteWaiting is deferred until after the
// executor returns, while the actor turn gate is released at executor return.
// A supervision auto-wake parked on that gate therefore begins its internal
// run first, and the late CompleteWaiting must not freeze the foreground
// completion summary over the running internal turn.
func TestLateCompleteWaitingDoesNotFreezeSummaryWhenInternalRunTookOver(t *testing.T) {
	coord := newChatInteractionCoordinator(&ChatSession{})
	t.Cleanup(coord.Shutdown)
	coord.StartWaiting()
	coord.mu.Lock()
	coord.dynamicStatusStarted = time.Now().Add(-5 * time.Second)
	coord.mu.Unlock()

	// The wake turn wins the gate and owns the composer state machine:
	// bridge.BeginRunKind(chatRunKindInternal) resets the run state and marks
	// the composer as planning.
	coord.ResetRunStateKind(chatRunKindInternal)
	coord.SetAgentStage(chatAgentStagePlanning)
	// sendMessage now reaches its deferred CompleteWaiting.
	coord.CompleteWaiting()

	coord.mu.Lock()
	completed := coord.dynamicStatusCompleted
	waiting := coord.waitingActive
	coord.mu.Unlock()
	if completed {
		t.Fatal("late CompleteWaiting must not freeze the foreground summary over a running internal turn")
	}
	if waiting {
		t.Fatal("CompleteWaiting must still release the foreground waiting flag")
	}
	if state := coord.currentSurfaceStateForTest(); state == "Ready" {
		t.Fatalf("internal run must keep the surface in a running state, got %q", state)
	}
}

// TestInternalRunStartDropsFrozenForegroundSummary covers the mirrored
// interleaving: the foreground turn froze its summary first, then the
// supervision wake starts. The internal run must clear the summary instead of
// painting it for the whole wake turn.
func TestInternalRunStartDropsFrozenForegroundSummary(t *testing.T) {
	coord := newChatInteractionCoordinator(&ChatSession{})
	t.Cleanup(coord.Shutdown)
	coord.StartWaiting()
	coord.mu.Lock()
	coord.dynamicStatusStarted = time.Now().Add(-5 * time.Second)
	coord.mu.Unlock()
	coord.CompleteWaiting()

	coord.mu.Lock()
	frozen := coord.dynamicStatusCompleted
	coord.mu.Unlock()
	if !frozen {
		t.Fatal("precondition: foreground completion must freeze the Worked for summary")
	}

	coord.ResetRunStateKind(chatRunKindInternal)

	coord.mu.Lock()
	defer coord.mu.Unlock()
	if coord.dynamicStatusCompleted {
		t.Fatal("internal run must drop the inherited Worked for summary")
	}
}

// TestForegroundSummaryFreezesAgainAfterInternalRunCompletes guards the
// run-ownership snapshot against over-matching: once the internal run has
// started and finished, the next foreground send must freeze its own summary
// normally.
func TestForegroundSummaryFreezesAgainAfterInternalRunCompletes(t *testing.T) {
	coord := newChatInteractionCoordinator(&ChatSession{})
	t.Cleanup(coord.Shutdown)
	coord.StartWaiting()
	coord.ResetRunStateKind(chatRunKindInternal)
	coord.CompleteWaiting()

	coord.StartWaiting()
	coord.mu.Lock()
	coord.dynamicStatusStarted = time.Now().Add(-5 * time.Second)
	coord.mu.Unlock()
	coord.CompleteWaiting()

	coord.mu.Lock()
	defer coord.mu.Unlock()
	if !coord.dynamicStatusCompleted || coord.dynamicStatusCompletedElapsed < 5*time.Second {
		t.Fatal("foreground turn after an internal run must still freeze its own Worked for summary")
	}
}
