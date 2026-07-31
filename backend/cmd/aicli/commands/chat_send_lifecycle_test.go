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
