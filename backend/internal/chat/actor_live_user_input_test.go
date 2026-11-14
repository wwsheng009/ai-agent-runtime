package chat

import (
	"errors"
	"reflect"
	"testing"
)

func TestSessionActorQueueLiveUserInputPreservesOrderAcrossRunBoundary(t *testing.T) {
	actor := &SessionActor{
		pendingLiveUserInput: []string{"earlier input"},
	}
	firstRun := &sessionRunControl{}
	actor.activeRun = firstRun

	count, err := actor.QueueLiveUserInput([]string{"  follow-up ", "", "second follow-up"})
	if err != nil {
		t.Fatalf("QueueLiveUserInput returned error: %v", err)
	}
	if count != 2 {
		t.Fatalf("QueueLiveUserInput accepted %d inputs, want 2", count)
	}

	batch := actor.liveUserInputBatch(firstRun)
	want := []string{"earlier input", "follow-up", "second follow-up"}
	if !reflect.DeepEqual(batch.Inputs, want) {
		t.Fatalf("live input batch = %v, want %v", batch.Inputs, want)
	}
	batch.Restore()
	actor.mu.Lock()
	actor.activeRun = nil
	actor.mu.Unlock()
	secondRun := &sessionRunControl{}
	actor.mu.Lock()
	actor.activeRun = secondRun
	actor.mu.Unlock()
	nextBatch := actor.liveUserInputBatch(secondRun)
	if !reflect.DeepEqual(nextBatch.Inputs, want) {
		t.Fatalf("restored input batch = %v, want %v", nextBatch.Inputs, want)
	}
}

func TestSessionActorLiveUserInputRestorePrecedesNewArrivalsAfterRunEnd(t *testing.T) {
	actor := &SessionActor{activeRun: &sessionRunControl{}}
	run := actor.activeRun
	if _, err := actor.QueueLiveUserInput([]string{"first"}); err != nil {
		t.Fatal(err)
	}
	batch := actor.liveUserInputBatch(run)
	if _, err := actor.QueueLiveUserInput([]string{"second"}); err != nil {
		t.Fatal(err)
	}
	actor.mu.Lock()
	actor.activeRun = nil
	actor.mu.Unlock()
	batch.Restore()
	actor.mu.Lock()
	actor.activeRun = &sessionRunControl{}
	next := actor.activeRun
	actor.mu.Unlock()
	if got, want := actor.liveUserInputBatch(next).Inputs, []string{"first", "second"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("restored inputs = %v, want %v", got, want)
	}
}

func TestSessionActorClearLiveUserInputInvalidatesInFlightRestore(t *testing.T) {
	actor := &SessionActor{activeRun: &sessionRunControl{}}
	if _, err := actor.QueueLiveUserInput([]string{"old"}); err != nil {
		t.Fatal(err)
	}
	batch := actor.liveUserInputBatch(actor.activeRun)
	actor.ClearLiveUserInput()
	if _, err := actor.QueueLiveUserInput([]string{"new"}); err != nil {
		t.Fatal(err)
	}
	batch.Restore()
	if got, want := actor.liveUserInputBatch(actor.activeRun).Inputs, []string{"new"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("pending inputs after clear = %v, want %v", got, want)
	}
}

func TestSessionActorQueueLiveUserInputRequiresActiveRun(t *testing.T) {
	actor := &SessionActor{}
	if _, err := actor.QueueLiveUserInput([]string{"follow-up"}); !errors.Is(err, ErrLiveUserInputUnavailable) {
		t.Fatalf("QueueLiveUserInput error = %v, want ErrLiveUserInputUnavailable", err)
	}

	run := &sessionRunControl{}
	run.finalizing.Store(true)
	actor.activeRun = run
	if _, err := actor.QueueLiveUserInput([]string{"follow-up"}); !errors.Is(err, ErrLiveUserInputUnavailable) {
		t.Fatalf("finalizing run QueueLiveUserInput error = %v, want ErrLiveUserInputUnavailable", err)
	}
}
