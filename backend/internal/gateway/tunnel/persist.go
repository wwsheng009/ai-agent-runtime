package tunnel

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/wire"
)

// persistPath is the default registry snapshot filename.
const persistPath = "registry.json"

// persister debounces registry snapshots to disk: OnRegistryChanged fires on
// every heartbeat and session update, so writes are collapsed into one flush
// after a quiet period. Snapshots are 0600 like the other gateway state files.
type persister struct {
	mu       sync.Mutex
	path     string
	delay    time.Duration
	timer    *time.Timer
	closed   bool
	listFunc func() []*Node
}

// NewPersister returns a debounced snapshot writer bound to the registry. dir ""
// disables persistence (tests); delay <= 0 uses the default.
func NewPersister(reg *Registry, dir string, delay time.Duration) *persister {
	if reg == nil || dir == "" {
		return nil
	}
	if delay <= 0 {
		delay = 2 * time.Second
	}
	return &persister{
		path:     filepath.Join(dir, persistPath),
		delay:    delay,
		listFunc: reg.List,
	}
}

// Schedule records that the registry changed; a flush happens once writes have
// been quiet for the debounce window (the last change wins).
func (p *persister) Schedule() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	if p.timer != nil {
		p.timer.Stop()
	}
	p.timer = time.AfterFunc(p.delay, p.flush)
}

// flush writes the current registry snapshot (best-effort: the registry is
// rebuildable, so write failures are not fatal).
func (p *persister) flush() {
	if p == nil || p.listFunc == nil {
		return
	}
	nodes := p.listFunc()
	// D3: the live-output tail is ephemeral display data. Trim it from the
	// snapshot so registry.json stays small under 1Hz streaming, without
	// clearing the live registry (List returns shallow copies whose Sessions
	// slice is shared, so the slice is copied before trimming).
	for _, node := range nodes {
		if node == nil || len(node.Sessions) == 0 {
			continue
		}
		sessions := make([]wire.SessionSummary, len(node.Sessions))
		copy(sessions, node.Sessions)
		node.Sessions = sessions
		for i := range node.Sessions {
			node.Sessions[i].StatusTail = ""
		}
	}
	data, err := json.MarshalIndent(nodes, "", "  ")
	if err != nil {
		return
	}
	if dir := filepath.Dir(p.path); dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0o700)
	}
	_ = os.WriteFile(p.path, data, 0o600)
}

// Close cancels a pending flush after forcing one final write.
func (p *persister) Close() {
	if p == nil {
		return
	}
	p.mu.Lock()
	if p.timer != nil {
		p.timer.Stop()
		p.timer = nil
	}
	p.closed = true
	p.mu.Unlock()
	p.flush()
}

// LoadSnapshot reads a persisted snapshot, returning nil when absent/corrupt
// (the registry then simply starts empty).
func LoadSnapshot(dir string) []*Node {
	if dir == "" {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(dir, persistPath))
	if err != nil {
		return nil
	}
	var nodes []*Node
	if json.Unmarshal(data, &nodes) != nil {
		return nil
	}
	out := nodes[:0]
	for _, node := range nodes {
		if node != nil && node.NodeID != "" {
			out = append(out, node)
		}
	}
	return out
}
