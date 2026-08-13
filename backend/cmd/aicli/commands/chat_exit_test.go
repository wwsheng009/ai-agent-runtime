package commands

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestChatInterruptExitStateFirstInterruptThenSecondExits(t *testing.T) {
	state := &chatInterruptExitState{}
	var shouldExit atomic.Bool
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	session := &ChatSession{
		cancelCtx:  ctx,
		cancelFunc: cancel,
	}

	now := time.Now()
	if state.handleInterruptSignal(session, &shouldExit, now) {
		t.Fatal("first interrupt while busy should not request exit")
	}
	if shouldExit.Load() {
		t.Fatal("first interrupt should leave exit flag unset")
	}
	if !session.IsInterrupted() {
		t.Fatal("first interrupt should mark the session interrupted")
	}

	if !state.handleInterruptSignal(session, &shouldExit, now.Add(time.Second)) {
		t.Fatal("second interrupt inside the window should request exit")
	}
	if !shouldExit.Load() {
		t.Fatal("second interrupt should set the exit flag")
	}
}

func TestChatInterruptExitStateWindowExpiryResets(t *testing.T) {
	state := &chatInterruptExitState{}
	var shouldExit atomic.Bool
	session := &ChatSession{}

	now := time.Now()
	if state.handleInterruptSignal(session, &shouldExit, now) {
		t.Fatal("first interrupt should not request exit")
	}
	if state.handleInterruptSignal(session, &shouldExit, now.Add(3*time.Second)) {
		t.Fatal("interrupt outside the window should not request exit")
	}
	if shouldExit.Load() {
		t.Fatal("expired window should reset the exit request")
	}
}

func TestChatInterruptExitStateIdleUnifiedSessionExitsImmediately(t *testing.T) {
	state := &chatInterruptExitState{}
	var shouldExit atomic.Bool
	session := &ChatSession{}
	session.Interaction = newChatInteractionCoordinator(session)

	if !state.handleInterruptSignal(session, &shouldExit, time.Now()) {
		t.Fatal("Ctrl+C while Ready should exit immediately")
	}
	if !shouldExit.Load() {
		t.Fatal("idle exit should set the exit flag")
	}
}

func TestChatInterruptExitStateStoppingExitsImmediately(t *testing.T) {
	state := &chatInterruptExitState{}
	var shouldExit atomic.Bool
	session := &ChatSession{}
	session.Interaction = newChatInteractionCoordinator(session)
	session.Interaction.SetAgentStage(chatAgentStageStopping)

	if !state.handleInterruptSignal(session, &shouldExit, time.Now()) {
		t.Fatal("Ctrl+C while Stopping should request exit without another interrupt")
	}
	if !shouldExit.Load() {
		t.Fatal("Stopping exit should set the exit flag")
	}
}

func TestChatInterruptExitStateFirstInterruptThenStoppingSecondExits(t *testing.T) {
	state := &chatInterruptExitState{}
	var shouldExit atomic.Bool
	session := &ChatSession{}
	session.Interaction = newChatInteractionCoordinator(session)
	session.Interaction.StartWaiting()

	now := time.Now()
	if state.handleInterruptSignal(session, &shouldExit, now) {
		t.Fatal("first interrupt while busy should not request exit")
	}
	session.Interaction.SetAgentStage(chatAgentStageStopping)
	if !state.handleInterruptSignal(session, &shouldExit, now.Add(5*time.Second)) {
		t.Fatal("Ctrl+C while Stopping should exit even outside the two-press window")
	}
	if !shouldExit.Load() {
		t.Fatal("Stopping exit should set the exit flag")
	}
}

func TestChatInterruptExitStateExitRequestKeepsInterrupting(t *testing.T) {
	state := &chatInterruptExitState{}
	var shouldExit atomic.Bool
	shouldExit.Store(true)
	session := &ChatSession{}

	if !state.handleInterruptSignal(session, &shouldExit, time.Now()) {
		t.Fatal("signal after exit request should keep reporting exit")
	}
	if !shouldExit.Load() {
		t.Fatal("exit flag must stay set")
	}
}
