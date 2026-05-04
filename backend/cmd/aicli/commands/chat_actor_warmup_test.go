package commands

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
)

func TestStartChatActorWarmupCreatesActorAsync(t *testing.T) {
	var calls int32
	hub := runtimechat.NewSessionHub(func(sessionID string) (*runtimechat.SessionActor, error) {
		atomic.AddInt32(&calls, 1)
		return &runtimechat.SessionActor{}, nil
	})
	session := &ChatSession{
		RuntimeSession:   &runtimechat.Session{ID: "session-1"},
		LocalRuntimeHost: &localChatRuntimeHost{SessionHub: hub},
	}

	startChatActorWarmup(session)
	warmup := currentChatActorWarmup(session, "session-1")
	if warmup == nil {
		t.Fatal("expected actor warmup")
	}
	actor, err := warmup.wait(context.Background())
	if err != nil {
		t.Fatalf("warmup wait returned error: %v", err)
	}
	if actor == nil {
		t.Fatal("expected warmed actor")
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("expected one actor factory call, got %d", calls)
	}
	if _, ok := hub.Get("session-1"); !ok {
		t.Fatal("expected warmed actor to be stored in hub")
	}
}

func TestChatActorForSessionWaitsForWarmupWithoutCreatingAgain(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var calls int32
	hub := runtimechat.NewSessionHub(func(sessionID string) (*runtimechat.SessionActor, error) {
		atomic.AddInt32(&calls, 1)
		close(started)
		<-release
		return &runtimechat.SessionActor{}, nil
	})
	session := &ChatSession{
		RuntimeSession:   &runtimechat.Session{ID: "session-1"},
		LocalRuntimeHost: &localChatRuntimeHost{SessionHub: hub},
	}

	startChatActorWarmup(session)
	<-started

	type actorResult struct {
		actor *runtimechat.SessionActor
		err   error
	}
	resultCh := make(chan actorResult, 1)
	go func() {
		actor, err := chatActorForSession(context.Background(), session)
		resultCh <- actorResult{actor: actor, err: err}
	}()

	select {
	case result := <-resultCh:
		t.Fatalf("chatActorForSession returned before warmup completed: %+v", result)
	case <-time.After(25 * time.Millisecond):
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("expected chatActorForSession to wait on warmup without a second factory call, got %d", calls)
	}

	close(release)
	result := <-resultCh
	if result.err != nil {
		t.Fatalf("chatActorForSession returned error: %v", result.err)
	}
	if result.actor == nil {
		t.Fatal("expected warmed actor")
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("expected one actor factory call after warmup, got %d", calls)
	}
}

func TestChatActorForSessionFallsBackWhenWarmupMissing(t *testing.T) {
	var calls int32
	hub := runtimechat.NewSessionHub(func(sessionID string) (*runtimechat.SessionActor, error) {
		atomic.AddInt32(&calls, 1)
		return &runtimechat.SessionActor{}, nil
	})
	session := &ChatSession{
		RuntimeSession:   &runtimechat.Session{ID: "session-1"},
		LocalRuntimeHost: &localChatRuntimeHost{SessionHub: hub},
	}

	actor, err := chatActorForSession(context.Background(), session)
	if err != nil {
		t.Fatalf("chatActorForSession returned error: %v", err)
	}
	if actor == nil {
		t.Fatal("expected actor")
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("expected one fallback factory call, got %d", calls)
	}
}
