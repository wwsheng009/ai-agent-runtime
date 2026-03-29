package chat

import (
	"fmt"
	"sync"
)

// SessionActorFactory creates a new session actor.
type SessionActorFactory func(sessionID string) (*SessionActor, error)

// SessionHub keeps a registry of active session actors.
type SessionHub struct {
	mu      sync.RWMutex
	actors  map[string]*SessionActor
	factory SessionActorFactory
}

// NewSessionHub creates a new hub with the provided factory.
func NewSessionHub(factory SessionActorFactory) *SessionHub {
	return &SessionHub{
		actors:  make(map[string]*SessionActor),
		factory: factory,
	}
}

// Get returns an actor if it exists.
func (h *SessionHub) Get(sessionID string) (*SessionActor, bool) {
	if h == nil {
		return nil, false
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	actor, ok := h.actors[sessionID]
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
	defer h.mu.Unlock()
	if actor, ok := h.actors[sessionID]; ok {
		return actor, nil
	}
	if h.factory == nil {
		return nil, fmt.Errorf("session hub factory is not configured")
	}
	actor, err := h.factory(sessionID)
	if err != nil {
		return nil, err
	}
	h.actors[sessionID] = actor
	return actor, nil
}

// Stop stops and removes an actor.
func (h *SessionHub) Stop(sessionID string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	actor := h.actors[sessionID]
	delete(h.actors, sessionID)
	h.mu.Unlock()
	if actor != nil {
		actor.Stop()
	}
}

// StopAll stops all actors managed by the hub.
func (h *SessionHub) StopAll() {
	if h == nil {
		return
	}
	h.mu.Lock()
	actors := make([]*SessionActor, 0, len(h.actors))
	for _, actor := range h.actors {
		actors = append(actors, actor)
	}
	h.actors = make(map[string]*SessionActor)
	h.mu.Unlock()
	for _, actor := range actors {
		if actor != nil {
			actor.Stop()
		}
	}
}
