package commands

import (
	"context"
	"testing"
)

// E4（P2-6）：cleanup 结果诚实性——只有真正完成清理才清 Stopping；
// 超时/未完成时保留 Stopping 并追加可重试提示。

func TestFinishInterruptCleanupUIKeepsStoppingOnIncompleteOutcome(t *testing.T) {
	session := &ChatSession{}
	coord := newTestChatInteractionCoordinator(t, session)
	session.Interaction = coord
	coord.SetAgentStage(chatAgentStageStopping)

	session.finishInterruptCleanupUIAfter(chatInterruptCleanupOutcome{
		stopped:      false,
		actors:       2,
		failedActors: 1,
	})

	if got := coord.AgentStage(); got != chatAgentStageStopping {
		t.Fatalf("incomplete cleanup must keep Stopping, got stage %v", got)
	}
}

func TestFinishInterruptCleanupUIClearsStoppingOnCompleteOutcome(t *testing.T) {
	session := &ChatSession{}
	coord := newTestChatInteractionCoordinator(t, session)
	session.Interaction = coord
	coord.SetAgentStage(chatAgentStageStopping)

	session.finishInterruptCleanupUIAfter(chatInterruptCleanupOutcome{stopped: true})

	if got := coord.AgentStage(); got != chatAgentStageIdle {
		t.Fatalf("completed cleanup must return to Idle, got stage %v", got)
	}
}

func TestInterruptActiveRunsNilHostReportsStopped(t *testing.T) {
	var host *localChatRuntimeHost
	outcome := host.interruptActiveRuns(context.Background(), "session-1", "", "")
	if !outcome.stopped {
		t.Fatalf("nil host has nothing to stop and must report stopped, got %#v", outcome)
	}
	if outcome.actors != 0 || outcome.failedActors != 0 {
		t.Fatalf("nil host must not report actors, got %#v", outcome)
	}
}
