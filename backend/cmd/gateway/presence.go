package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"
)

// presenceRegistry tracks which sessions a console client is actively viewing
// (docs/gateway/08-notifications.md §5.4). Entries expire after ttl so a closed
// tab or a hidden page stops suppressing notifications without an explicit
// "leave" call.
type presenceRegistry struct {
	mu   sync.Mutex
	ttl  time.Duration
	seen map[string]time.Time
}

func newPresenceRegistry(ttl time.Duration) *presenceRegistry {
	if ttl <= 0 {
		ttl = 90 * time.Second
	}
	return &presenceRegistry{ttl: ttl, seen: make(map[string]time.Time)}
}

func presenceKey(nodeID, sessionID string) string {
	return nodeID + "|" + sessionID
}

// Touch records that a viewer is watching nodeID/sessionID right now.
func (p *presenceRegistry) Touch(nodeID, sessionID string) {
	if p == nil || nodeID == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	p.pruneLocked(now)
	p.seen[presenceKey(nodeID, sessionID)] = now.Add(p.ttl)
}

// Active reports whether a viewer is currently watching nodeID/sessionID.
func (p *presenceRegistry) Active(nodeID, sessionID string) bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	p.pruneLocked(now)
	expiry, ok := p.seen[presenceKey(nodeID, sessionID)]
	return ok && expiry.After(now)
}

// Count returns the number of live presence entries.
func (p *presenceRegistry) Count() int {
	if p == nil {
		return 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pruneLocked(time.Now())
	return len(p.seen)
}

func (p *presenceRegistry) pruneLocked(now time.Time) {
	for key, expiry := range p.seen {
		if !expiry.After(now) {
			delete(p.seen, key)
		}
	}
}

// handlePresence records a console view of a node/session so push
// notifications for it can be suppressed (docs/gateway/08-notifications.md §5.4).
func handlePresence(presence *presenceRegistry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			NodeID    string `json:"node_id"`
			SessionID string `json:"session_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.NodeID) == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "node_id is required"})
			return
		}
		presence.Touch(strings.TrimSpace(req.NodeID), strings.TrimSpace(req.SessionID))
		writeJSON(w, http.StatusOK, map[string]any{"watching": true})
	}
}
