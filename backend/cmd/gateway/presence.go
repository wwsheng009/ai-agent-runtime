package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// presenceRegistry tracks which sessions a console client is actively viewing
// (docs/gateway/08-notifications.md §5.4). Entries expire after ttl so a closed
// tab or a hidden page stops suppressing notifications without an explicit
// "leave" call. Entries also carry a `tail` flag: the P1-b watch gate that
// enables live-output tail pushes for sessions a console is actually watching.
type presenceRegistry struct {
	mu   sync.Mutex
	ttl  time.Duration
	seen map[string]presenceEntry
}

type presenceEntry struct {
	expiry time.Time
	tail   bool
}

func newPresenceRegistry(ttl time.Duration) *presenceRegistry {
	if ttl <= 0 {
		ttl = 90 * time.Second
	}
	return &presenceRegistry{ttl: ttl, seen: make(map[string]presenceEntry)}
}

func presenceKey(nodeID, sessionID string) string {
	return nodeID + "|" + sessionID
}

// Touch records that a viewer is watching nodeID/sessionID right now; tail
// additionally requests live-output pushes for that session.
func (p *presenceRegistry) Touch(nodeID, sessionID string, tail bool) {
	if p == nil || nodeID == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	p.pruneLocked(now)
	p.seen[presenceKey(nodeID, sessionID)] = presenceEntry{expiry: now.Add(p.ttl), tail: tail}
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
	entry, ok := p.seen[presenceKey(nodeID, sessionID)]
	return ok && entry.expiry.After(now)
}

// TailWatched returns the session IDs (sorted) with a live tail-watch entry
// for nodeID. The gateway diff-sends these to the agent as watch/unwatch
// control actions on HEARTBEAT_ACK.
func (p *presenceRegistry) TailWatched(nodeID string) []string {
	if p == nil || nodeID == "" {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	p.pruneLocked(now)
	prefix := nodeID + "|"
	var out []string
	for key, entry := range p.seen {
		if !entry.tail || !entry.expiry.After(now) {
			continue
		}
		if strings.HasPrefix(key, prefix) {
			if sessionID := strings.TrimPrefix(key, prefix); sessionID != "" {
				out = append(out, sessionID)
			}
		}
	}
	sort.Strings(out)
	return out
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

// Release drops presence entries so notifications stop being suppressed
// immediately instead of waiting for the TTL. An empty sessionID releases
// every session of the node; an empty nodeID releases everything.
func (p *presenceRegistry) Release(nodeID, sessionID string) int {
	if p == nil {
		return 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	released := 0
	switch {
	case nodeID == "":
		released = len(p.seen)
		p.seen = make(map[string]presenceEntry)
	case sessionID == "":
		prefix := nodeID + "|"
		for key := range p.seen {
			if strings.HasPrefix(key, prefix) {
				delete(p.seen, key)
				released++
			}
		}
	default:
		key := presenceKey(nodeID, sessionID)
		if _, ok := p.seen[key]; ok {
			delete(p.seen, key)
			released = 1
		}
	}
	return released
}

func (p *presenceRegistry) pruneLocked(now time.Time) {
	for key, entry := range p.seen {
		if !entry.expiry.After(now) {
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
			Tail      bool   `json:"tail"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.NodeID) == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "node_id is required"})
			return
		}
		presence.Touch(strings.TrimSpace(req.NodeID), strings.TrimSpace(req.SessionID), req.Tail)
		writeJSON(w, http.StatusOK, map[string]any{"watching": true})
	}
}

// handlePresenceRelease clears presence entries when the console page goes to
// the background or is unloaded (app.js sends this via keepalive fetch /
// pagehide). Without it a backgrounded phone keeps suppressing notifications
// for the session it last viewed until the presence TTL expires.
func handlePresenceRelease(presence *presenceRegistry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			NodeID    string `json:"node_id"`
			SessionID string `json:"session_id"`
			All       bool   `json:"all"`
		}
		// An empty body is treated as "release everything" so a bare keepalive
		// POST from pagehide still works.
		_ = json.NewDecoder(r.Body).Decode(&req)
		if !req.All && strings.TrimSpace(req.NodeID) == "" {
			// No node and no all=true: nothing to do, but treat it as success
			// (idempotent no-op) to keep the unload path simple.
			writeJSON(w, http.StatusOK, map[string]any{"released": 0})
			return
		}
		nodeID := strings.TrimSpace(req.NodeID)
		if req.All {
			nodeID = ""
		}
		released := presence.Release(nodeID, strings.TrimSpace(req.SessionID))
		writeJSON(w, http.StatusOK, map[string]any{"released": released})
	}
}
