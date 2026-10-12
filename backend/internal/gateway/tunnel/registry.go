// Package tunnel implements the gateway server side of the reverse tunnel: the
// /gw/agent WebSocket endpoint, the HELLO/WELCOME/REGISTER handshake, the
// in-memory node registry and the heartbeat loop. Reverse-proxy stream
// forwarding (STREAM_*) lands in a later step.
//
// Design: docs/gateway/03-protocol.md.
package tunnel

import (
	"sort"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/wire"
)

// Node is a registered agent node.
type Node struct {
	NodeID        string                `json:"node_id"`
	Kind          string                `json:"kind,omitempty"`
	Name          string                `json:"name,omitempty"`
	DeviceName    string                `json:"device_name,omitempty"`
	Version       string                `json:"version,omitempty"`
	OS            string                `json:"os,omitempty"`
	Arch          string                `json:"arch,omitempty"`
	Capabilities  []string              `json:"capabilities,omitempty"`
	Routes        []string              `json:"routes,omitempty"`
	Sessions      []wire.SessionSummary `json:"sessions,omitempty"`
	RemoteAddr    string                `json:"remote_addr,omitempty"`
	RegisteredAt  time.Time             `json:"registered_at"`
	LastHeartbeat time.Time             `json:"last_heartbeat"`
	// Stale marks a node restored from the persisted registry snapshot: the
	// entry carries the last-known sessions/attention, but no live connection
	// exists (proxy requests fail closed with 502). A fresh REGISTER clears it.
	Stale bool `json:"stale,omitempty"`
}

// Registry is an in-memory node registry. Live connections are not persisted;
// the registry only answers "which nodes are registered right now".
type Registry struct {
	mu    sync.RWMutex
	nodes map[string]*Node
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry {
	return &Registry{nodes: make(map[string]*Node)}
}

// Upsert stores (or replaces) a node record.
func (r *Registry) Upsert(node *Node) {
	if r == nil || node == nil || node.NodeID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *node
	r.nodes[node.NodeID] = &cp
}

// Remove deletes a node record.
func (r *Registry) Remove(nodeID string) {
	if r == nil || nodeID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.nodes, nodeID)
}

// RemoveStale deletes every offline (stale snapshot) node in one pass and
// returns the removed node IDs in stable (sorted) order. Live entries are
// never touched: removing them would race the next heartbeat sync and only
// re-add the entry. Backs the console's one-click "clear offline snapshots".
func (r *Registry) RemoveStale() []string {
	removed := make([]string, 0)
	if r == nil {
		return removed
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, node := range r.nodes {
		if node.Stale {
			removed = append(removed, id)
			delete(r.nodes, id)
		}
	}
	sort.Strings(removed)
	return removed
}

// RemoveSession deletes one session entry from a node, reporting whether the
// node and session existed. Used by the console "remove" action to clear dead
// entries (typically stale snapshot sessions that will never resolve).
func (r *Registry) RemoveSession(nodeID, sessionID string) bool {
	if r == nil || nodeID == "" || sessionID == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	node, ok := r.nodes[nodeID]
	if !ok {
		return false
	}
	out := node.Sessions[:0]
	found := false
	for _, s := range node.Sessions {
		if s.SessionID == sessionID {
			found = true
			continue
		}
		out = append(out, s)
	}
	if !found {
		return false
	}
	node.Sessions = out
	return true
}

// Get returns a copy of the node record.
func (r *Registry) Get(nodeID string) (*Node, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	node, ok := r.nodes[nodeID]
	if !ok {
		return nil, false
	}
	cp := *node
	return &cp, true
}

// List returns a stable (node_id sorted) copy of all node records.
func (r *Registry) List() []*Node {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Node, 0, len(r.nodes))
	for _, node := range r.nodes {
		cp := *node
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].NodeID < out[j].NodeID })
	return out
}

// Count returns the number of registered nodes.
func (r *Registry) Count() int {
	if r == nil {
		return 0
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.nodes)
}

// CountLive returns the number of nodes with a live connection (non-stale).
func (r *Registry) CountLive() int {
	if r == nil {
		return 0
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	n := 0
	for _, node := range r.nodes {
		if !node.Stale {
			n++
		}
	}
	return n
}

// Restore seeds the registry from a persisted snapshot (gateway restart). Every
// restored node is marked Stale: it is last-known state, not a live agent. A
// reconnect with the same NodeID replaces the entry and clears the flag.
func (r *Registry) Restore(nodes []*Node) int {
	if r == nil {
		return 0
	}
	restored := 0
	for _, node := range nodes {
		if node == nil || node.NodeID == "" {
			continue
		}
		cp := *node
		cp.Stale = true
		r.mu.Lock()
		r.nodes[cp.NodeID] = &cp
		r.mu.Unlock()
		restored++
	}
	return restored
}
