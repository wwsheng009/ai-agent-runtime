package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// presenceRegistry tracks which sessions a viewer is actively watching
// (docs/gateway/08-notifications.md §5.4；多源模型见
// docs/bots/10-presence-signals-design.md §3.2). Entries expire per source so a
// closed tab or a stopped agent heartbeat stops suppressing notifications
// without an explicit "leave" call. The `tail` flag is console-only: the P1-b
// watch gate that enables live-output tail pushes for sessions a console is
// actually watching.
type presenceRegistry struct {
	mu      sync.Mutex
	ttl     time.Duration // console 源 TTL（--presence-ttl）
	hostTTL time.Duration // host 源 TTL（main 按心跳周期设置；心跳停止即失效）
	seen    map[string]presenceEntry
}

// presenceEntry 是某会话的在场登记：按**来源**分别记到期时间（多源并存，任一
// 有效即在场）；tail 仅 console 源使用。
type presenceEntry struct {
	sources map[string]time.Time
	tail    bool
}

// 登记表内置来源名（--presence-sources 取值）；宿主上报（心跳 Viewing）用 host。
const (
	presenceSourceConsole = "console"
	presenceSourceHost    = "host"
)

func newPresenceRegistry(ttl time.Duration) *presenceRegistry {
	if ttl <= 0 {
		ttl = 90 * time.Second
	}
	return &presenceRegistry{ttl: ttl, hostTTL: 90 * time.Second, seen: make(map[string]presenceEntry)}
}

func presenceKey(nodeID, sessionID string) string {
	return nodeID + "|" + sessionID
}

// Touch records that a console viewer is watching nodeID/sessionID right now;
// tail additionally requests live-output pushes for that session.
func (p *presenceRegistry) Touch(nodeID, sessionID string, tail bool) {
	p.TouchSource(presenceSourceConsole, nodeID, sessionID, tail)
}

// TouchSource 按来源续期在场条目（多源并存；docs/bots/10 §3.2）。tail 仅对
// console 源生效（host 源不参与 watch/unwatch 控制面）。
func (p *presenceRegistry) TouchSource(source, nodeID, sessionID string, tail bool) {
	if p == nil || nodeID == "" || source == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	p.pruneLocked(now)
	key := presenceKey(nodeID, sessionID)
	entry := p.seen[key]
	if entry.sources == nil {
		entry.sources = make(map[string]time.Time, 2)
	}
	entry.sources[source] = now.Add(p.ttlFor(source))
	if source == presenceSourceConsole {
		entry.tail = tail
	}
	p.seen[key] = entry
}

// ttlFor 返回某来源条目的 TTL。
func (p *presenceRegistry) ttlFor(source string) time.Duration {
	if source == presenceSourceHost && p.hostTTL > 0 {
		return p.hostTTL
	}
	return p.ttl
}

// Active reports whether any source is currently watching nodeID/sessionID.
func (p *presenceRegistry) Active(nodeID, sessionID string) bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	p.pruneLocked(now)
	entry, ok := p.seen[presenceKey(nodeID, sessionID)]
	if !ok {
		return false
	}
	for _, expiry := range entry.sources {
		if expiry.After(now) {
			return true
		}
	}
	return false
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
		if !entry.tail || !entry.sources[presenceSourceConsole].After(now) {
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

// Release drops the console presence source so notifications stop being
// suppressed immediately instead of waiting for the TTL（host 源不受影响，由
// 心跳 TTL 自管）。An empty sessionID releases every session of the node; an
// empty nodeID releases everything.
func (p *presenceRegistry) Release(nodeID, sessionID string) int {
	if p == nil {
		return 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	released := 0
	releaseKey := func(key string) {
		entry, ok := p.seen[key]
		if !ok {
			return
		}
		if _, had := entry.sources[presenceSourceConsole]; !had {
			return
		}
		released++
		delete(entry.sources, presenceSourceConsole)
		entry.tail = false
		if len(entry.sources) == 0 {
			delete(p.seen, key)
			return
		}
		p.seen[key] = entry
	}
	switch {
	case nodeID == "":
		for key := range p.seen {
			releaseKey(key)
		}
	case sessionID == "":
		prefix := nodeID + "|"
		for key := range p.seen {
			if strings.HasPrefix(key, prefix) {
				releaseKey(key)
			}
		}
	default:
		releaseKey(presenceKey(nodeID, sessionID))
	}
	return released
}

func (p *presenceRegistry) pruneLocked(now time.Time) {
	for key, entry := range p.seen {
		live := false
		for _, expiry := range entry.sources {
			if expiry.After(now) {
				live = true
				break
			}
		}
		if !live {
			delete(p.seen, key)
		}
	}
}

// presenceSourceEnabled 解析 --presence-sources 词表（docs/bots/10 §3.2）。
func presenceSourceEnabled(spec, source string) bool {
	for _, part := range strings.Split(spec, ",") {
		if strings.TrimSpace(part) == source {
			return true
		}
	}
	return false
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
