package chat

import (
	"fmt"
	"sync"
	"time"
)

// SessionActorFactory creates a new session actor.
type SessionActorFactory func(sessionID string) (*SessionActor, error)

type SessionHubOptions struct {
	MaxActors     int
	IdleTTL       time.Duration
	SweepInterval time.Duration
}

func DefaultSessionHubOptions() SessionHubOptions {
	return SessionHubOptions{
		MaxActors:     32,
		IdleTTL:       15 * time.Minute,
		SweepInterval: time.Minute,
	}
}

// SessionHub keeps a registry of active session actors.
type SessionHub struct {
	mu         sync.RWMutex
	actors     map[string]*SessionActor
	lastAccess map[string]time.Time
	factory    SessionActorFactory
	options    SessionHubOptions
	stopSweep  chan struct{}
	stopOnce   sync.Once
}

// NewSessionHub preserves the unbounded behavior used by small test and
// embedded setups. Long-running hosts should use NewBoundedSessionHub.
func NewSessionHub(factory SessionActorFactory) *SessionHub {
	return newSessionHub(factory, SessionHubOptions{})
}

func NewBoundedSessionHub(factory SessionActorFactory, configured ...SessionHubOptions) *SessionHub {
	options := DefaultSessionHubOptions()
	if len(configured) > 0 {
		options = configured[0]
	}
	if options.MaxActors <= 0 && options.IdleTTL <= 0 {
		options = DefaultSessionHubOptions()
	}
	return newSessionHub(factory, options)
}

func newSessionHub(factory SessionActorFactory, options SessionHubOptions) *SessionHub {
	hub := &SessionHub{
		actors:     make(map[string]*SessionActor),
		lastAccess: make(map[string]time.Time),
		factory:    factory,
		options:    options,
		stopSweep:  make(chan struct{}),
	}
	if options.IdleTTL > 0 {
		interval := options.SweepInterval
		if interval <= 0 || interval > options.IdleTTL {
			interval = options.IdleTTL / 2
		}
		if interval <= 0 {
			interval = time.Minute
		}
		go hub.sweepLoop(interval)
	}
	return hub
}

// Get returns an actor if it exists and refreshes its idle age.
func (h *SessionHub) Get(sessionID string) (*SessionActor, bool) {
	if h == nil {
		return nil, false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	actor, ok := h.actors[sessionID]
	if ok {
		h.lastAccess[sessionID] = time.Now()
	}
	return actor, ok
}

// GetOrCreate returns an existing actor or creates a new one.
func (h *SessionHub) GetOrCreate(sessionID string) (*SessionActor, error) {
	if h == nil {
		return nil, fmt.Errorf("session hub is nil")
	}
	if sessionID == "" {
		return nil, fmt.Errorf("session id is required")
	}
	if actor, ok := h.Get(sessionID); ok {
		return actor, nil
	}
	h.mu.Lock()
	if actor, ok := h.actors[sessionID]; ok {
		h.lastAccess[sessionID] = time.Now()
		h.mu.Unlock()
		return actor, nil
	}
	if h.factory == nil {
		h.mu.Unlock()
		return nil, fmt.Errorf("session hub factory is not configured")
	}
	actor, err := h.factory(sessionID)
	if err != nil {
		h.mu.Unlock()
		return nil, err
	}
	h.actors[sessionID] = actor
	h.lastAccess[sessionID] = time.Now()
	evicted := h.evictOverflowLocked(sessionID)
	h.mu.Unlock()
	stopActors(evicted)
	return actor, nil
}

func (h *SessionHub) evictOverflowLocked(exclude string) []*SessionActor {
	if h.options.MaxActors <= 0 || len(h.actors) <= h.options.MaxActors {
		return nil
	}
	type candidate struct {
		id     string
		access time.Time
	}
	var candidates []candidate
	for id, actor := range h.actors {
		if id == exclude || !sessionActorEvictable(actor) {
			continue
		}
		candidates = append(candidates, candidate{id: id, access: h.lastAccess[id]})
	}
	var evicted []*SessionActor
	for len(h.actors) > h.options.MaxActors && len(candidates) > 0 {
		oldest := 0
		for index := 1; index < len(candidates); index++ {
			if candidates[index].access.Before(candidates[oldest].access) {
				oldest = index
			}
		}
		id := candidates[oldest].id
		evicted = append(evicted, h.actors[id])
		delete(h.actors, id)
		delete(h.lastAccess, id)
		candidates = append(candidates[:oldest], candidates[oldest+1:]...)
	}
	return evicted
}

func sessionActorEvictable(actor *SessionActor) bool {
	if actor == nil {
		return true
	}
	state := actor.State()
	if state == nil {
		return true
	}
	if state.Status != SessionIdle && state.Status != SessionStopped {
		return false
	}
	return state.PendingTool == nil && state.PendingApproval == nil && state.PendingQuestion == nil &&
		state.CurrentTurnID == "" && len(state.ActiveJobIDs) == 0
}

func (h *SessionHub) sweepLoop(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case now := <-ticker.C:
			h.evictIdle(now)
		case <-h.stopSweep:
			return
		}
	}
}

func (h *SessionHub) evictIdle(now time.Time) {
	if h == nil || h.options.IdleTTL <= 0 {
		return
	}
	cutoff := now.Add(-h.options.IdleTTL)
	h.mu.Lock()
	var evicted []*SessionActor
	for id, actor := range h.actors {
		if h.lastAccess[id].After(cutoff) || !sessionActorEvictable(actor) {
			continue
		}
		evicted = append(evicted, actor)
		delete(h.actors, id)
		delete(h.lastAccess, id)
	}
	h.mu.Unlock()
	stopActors(evicted)
}

// Stop stops and removes an actor.
func (h *SessionHub) Stop(sessionID string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	actor := h.actors[sessionID]
	delete(h.actors, sessionID)
	delete(h.lastAccess, sessionID)
	h.mu.Unlock()
	stopActors([]*SessionActor{actor})
}

// StopAll stops all actors managed by the hub and its eviction worker.
func (h *SessionHub) StopAll() {
	if h == nil {
		return
	}
	h.stopOnce.Do(func() { close(h.stopSweep) })
	h.mu.Lock()
	actors := make([]*SessionActor, 0, len(h.actors))
	for _, actor := range h.actors {
		actors = append(actors, actor)
	}
	h.actors = make(map[string]*SessionActor)
	h.lastAccess = make(map[string]time.Time)
	h.mu.Unlock()
	stopActors(actors)
}

func stopActors(actors []*SessionActor) {
	for _, actor := range actors {
		if actor != nil {
			actor.Stop()
		}
	}
}
