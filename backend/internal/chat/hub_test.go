package chat

import (
	"sync/atomic"
	"testing"
	"time"
)

func newHubTestActor(id string, status SessionStatus, stopped *atomic.Int32) *SessionActor {
	return &SessionActor{
		id:    id,
		cmdCh: make(chan Command, 1),
		stop:  make(chan struct{}),
		done:  make(chan struct{}),
		state: &RuntimeState{SessionID: id, Status: status},
		onStop: func() {
			if stopped != nil {
				stopped.Add(1)
			}
		},
	}
}

func TestBoundedSessionHubEvictsOldestIdleActor(t *testing.T) {
	var stopped atomic.Int32
	hub := NewBoundedSessionHub(func(id string) (*SessionActor, error) {
		return newHubTestActor(id, SessionIdle, &stopped), nil
	}, SessionHubOptions{MaxActors: 2})
	t.Cleanup(hub.StopAll)
	if _, err := hub.GetOrCreate("oldest"); err != nil {
		t.Fatal(err)
	}
	if _, err := hub.GetOrCreate("newer"); err != nil {
		t.Fatal(err)
	}
	hub.mu.Lock()
	hub.lastAccess["oldest"] = time.Now().Add(-2 * time.Hour)
	hub.lastAccess["newer"] = time.Now().Add(-time.Hour)
	hub.mu.Unlock()
	if _, err := hub.GetOrCreate("newest"); err != nil {
		t.Fatal(err)
	}
	if _, ok := hub.Get("oldest"); ok {
		t.Fatal("expected oldest idle actor to be evicted")
	}
	if _, ok := hub.Get("newer"); !ok {
		t.Fatal("expected newer actor to remain")
	}
	if stopped.Load() != 1 {
		t.Fatalf("expected one stopped actor, got %d", stopped.Load())
	}
}

func TestBoundedSessionHubProtectsRunningActor(t *testing.T) {
	hub := NewBoundedSessionHub(func(id string) (*SessionActor, error) {
		status := SessionIdle
		if id == "running" {
			status = SessionRunning
		}
		return newHubTestActor(id, status, nil), nil
	}, SessionHubOptions{MaxActors: 1})
	t.Cleanup(hub.StopAll)
	if _, err := hub.GetOrCreate("running"); err != nil {
		t.Fatal(err)
	}
	if _, err := hub.GetOrCreate("idle"); err != nil {
		t.Fatal(err)
	}
	if _, ok := hub.Get("running"); !ok {
		t.Fatal("running actor must not be evicted")
	}
}

func TestBoundedSessionHubEvictsExpiredIdleActorsAndStopsWorker(t *testing.T) {
	var stopped atomic.Int32
	hub := NewBoundedSessionHub(func(id string) (*SessionActor, error) {
		return newHubTestActor(id, SessionIdle, &stopped), nil
	}, SessionHubOptions{MaxActors: 8, IdleTTL: time.Minute, SweepInterval: time.Hour})
	if _, err := hub.GetOrCreate("expired"); err != nil {
		t.Fatal(err)
	}
	hub.mu.Lock()
	hub.lastAccess["expired"] = time.Now().Add(-2 * time.Minute)
	hub.mu.Unlock()
	hub.evictIdle(time.Now())
	if _, ok := hub.Get("expired"); ok {
		t.Fatal("expected expired idle actor to be evicted")
	}
	hub.StopAll()
	select {
	case <-hub.stopSweep:
	default:
		t.Fatal("expected StopAll to close the sweep worker signal")
	}
	if stopped.Load() != 1 {
		t.Fatalf("expected expired actor to stop exactly once, got %d", stopped.Load())
	}
}

func TestSessionHubGetOrCreateReplacesStoppedActor(t *testing.T) {
	var created atomic.Int32
	hub := NewSessionHub(func(id string) (*SessionActor, error) {
		created.Add(1)
		return newHubTestActor(id, SessionIdle, nil), nil
	})
	t.Cleanup(hub.StopAll)

	first, err := hub.GetOrCreate("session-1")
	if err != nil {
		t.Fatal(err)
	}
	first.Stop()

	second, err := hub.GetOrCreate("session-1")
	if err != nil {
		t.Fatal(err)
	}
	if second == first {
		t.Fatal("expected the hub to replace the stopped actor")
	}
	if created.Load() != 2 {
		t.Fatalf("expected two actor factory calls, got %d", created.Load())
	}
}

func TestBoundedSessionHubCreatesFreshActorAfterIdleSweep(t *testing.T) {
	var created atomic.Int32
	hub := NewBoundedSessionHub(func(id string) (*SessionActor, error) {
		created.Add(1)
		return newHubTestActor(id, SessionIdle, nil), nil
	}, SessionHubOptions{MaxActors: 8, IdleTTL: time.Minute, SweepInterval: time.Hour})
	t.Cleanup(hub.StopAll)

	first, err := hub.GetOrCreate("session-1")
	if err != nil {
		t.Fatal(err)
	}
	hub.mu.Lock()
	hub.lastAccess["session-1"] = time.Now().Add(-2 * time.Minute)
	hub.mu.Unlock()
	hub.evictIdle(time.Now())

	second, err := hub.GetOrCreate("session-1")
	if err != nil {
		t.Fatal(err)
	}
	if second == first {
		t.Fatal("expected a fresh actor after idle eviction")
	}
	if created.Load() != 2 {
		t.Fatalf("expected two actor factory calls, got %d", created.Load())
	}
}
