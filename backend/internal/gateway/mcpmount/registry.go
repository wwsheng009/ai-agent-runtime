package mcpmount

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/wire"
)

// Record is one mount as the gateway knows it. It is deliberately free of
// secrets: the upstream address stays on the node, and the external credential
// lives in the token store as a hash.
type Record struct {
	Slug string `json:"slug"`
	// MountID is the node's stable mount identity (mount identity + spec id). It
	// survives reconnects; NodeID does not.
	MountID    string `json:"mount_id"`
	NodeID     string `json:"node_id,omitempty"`
	NodeStable string `json:"node_stable,omitempty"`
	Kind       string `json:"kind,omitempty"`
	Name       string `json:"name,omitempty"`
	// LocalPath is the node's own endpoint path (derived from its upstream URL).
	// The gateway publishes it under its namespace and uses it to verify the
	// node's route allowlist at REGISTER time.
	LocalPath string            `json:"local_path,omitempty"`
	Labels    *wire.MountLabels `json:"labels,omitempty"`
	Limits    wire.MountLimits  `json:"limits,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
	LastSeen  time.Time         `json:"last_seen,omitempty"`
	// Disabled is the operator's emergency stop. It survives reconnects: a node
	// must not be able to re-enable a mount by dialing again.
	Disabled bool `json:"disabled,omitempty"`
	// Stale marks a record restored from disk or left behind by a disconnected
	// node: the URL stays reserved (clients get 502, never 404) but no live
	// connection serves it.
	Stale bool `json:"stale,omitempty"`
}

// SlugConflictError reports that a slug is already held by a different mount.
// It is a hard error on purpose: silently renaming a published path would make
// a client's configured URL drift with no visible event.
type SlugConflictError struct {
	Slug            string
	ExistingMountID string
	IncomingMountID string
}

func (e *SlugConflictError) Error() string {
	return fmt.Sprintf("mount slug %q is already held by mount %q (incoming %q)", e.Slug, e.ExistingMountID, e.IncomingMountID)
}

// Registry is the in-memory slug -> mount table.
type Registry struct {
	mu     sync.RWMutex
	bySlug map[string]*Record
	byID   map[string]*Record
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry {
	return &Registry{
		bySlug: make(map[string]*Record),
		byID:   make(map[string]*Record),
	}
}

// Upsert stores (or refreshes) a mount record.
//
// Re-registering the same MountID is idempotent and clears Stale; a different
// MountID claiming an existing slug is rejected. If the same MountID reappears
// under a new slug (operator pinned a different path), the old slug is released
// — that is the one case where a public URL intentionally changes.
func (r *Registry) Upsert(record *Record) error {
	if r == nil || record == nil {
		return fmt.Errorf("nil mount record")
	}
	slug := strings.TrimSpace(record.Slug)
	mountID := strings.TrimSpace(record.MountID)
	if slug == "" || mountID == "" {
		return fmt.Errorf("mount record needs both slug and mount id")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if existing, ok := r.bySlug[slug]; ok && strings.TrimSpace(existing.MountID) != mountID {
		return &SlugConflictError{Slug: slug, ExistingMountID: existing.MountID, IncomingMountID: mountID}
	}

	now := time.Now().UTC()
	cp := *record
	cp.Slug = slug
	cp.MountID = mountID
	cp.Stale = false
	if cp.UpdatedAt.IsZero() {
		cp.UpdatedAt = now
	}
	if cp.LastSeen.IsZero() {
		cp.LastSeen = now
	}

	// Release a previous slug held by this same mount (pin change).
	if previous, ok := r.byID[mountID]; ok && previous.Slug != slug {
		delete(r.bySlug, previous.Slug)
	}

	if previous, ok := r.bySlug[slug]; ok {
		// Preserve creation time and operator intent across reconnects.
		cp.CreatedAt = previous.CreatedAt
		cp.Disabled = previous.Disabled
	} else if cp.CreatedAt.IsZero() {
		cp.CreatedAt = now
	}

	r.bySlug[slug] = &cp
	r.byID[mountID] = &cp
	return nil
}

// Get returns a copy of the record for a slug.
func (r *Registry) Get(slug string) (*Record, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	record, ok := r.bySlug[strings.TrimSpace(slug)]
	if !ok {
		return nil, false
	}
	cp := *record
	return &cp, true
}

// GetByMountID returns a copy of the record for a stable mount identity.
func (r *Registry) GetByMountID(mountID string) (*Record, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	record, ok := r.byID[strings.TrimSpace(mountID)]
	if !ok {
		return nil, false
	}
	cp := *record
	return &cp, true
}

// List returns a slug-sorted copy of every record, so console output and
// snapshots are stable across map iteration order.
func (r *Registry) List() []*Record {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Record, 0, len(r.bySlug))
	for _, record := range r.bySlug {
		cp := *record
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slug < out[j].Slug })
	return out
}

// Count returns the number of registered mounts, including stale and disabled.
func (r *Registry) Count() int {
	if r == nil {
		return 0
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.bySlug)
}

// CountLive returns the number of mounts currently backed by a live node.
func (r *Registry) CountLive() int {
	if r == nil {
		return 0
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	live := 0
	for _, record := range r.bySlug {
		if !record.Stale && !record.Disabled {
			live++
		}
	}
	return live
}

// Remove deletes a mount record entirely. Callers should refuse to remove a
// live mount: deleting it turns every configured client URL into a 404.
func (r *Registry) Remove(slug string) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	slug = strings.TrimSpace(slug)
	record, ok := r.bySlug[slug]
	if !ok {
		return false
	}
	delete(r.bySlug, slug)
	delete(r.byID, record.MountID)
	return true
}

// SetDisabled toggles the operator emergency stop and reports whether the mount
// existed.
func (r *Registry) SetDisabled(slug string, disabled bool) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	record, ok := r.bySlug[strings.TrimSpace(slug)]
	if !ok {
		return false
	}
	record.Disabled = disabled
	record.UpdatedAt = time.Now().UTC()
	return true
}

// MarkStale marks a mount as no longer served by a live connection. The record
// stays so the public path remains reserved (502, not 404).
func (r *Registry) MarkStale(slug string) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	record, ok := r.bySlug[strings.TrimSpace(slug)]
	if !ok {
		return false
	}
	record.Stale = true
	record.UpdatedAt = time.Now().UTC()
	return true
}

// Bind points a mount at a live node connection and clears Stale.
func (r *Registry) Bind(slug, nodeID string, seen time.Time) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	record, ok := r.bySlug[strings.TrimSpace(slug)]
	if !ok {
		return false
	}
	record.NodeID = strings.TrimSpace(nodeID)
	record.Stale = false
	if seen.IsZero() {
		seen = time.Now().UTC()
	}
	record.LastSeen = seen
	record.UpdatedAt = seen
	return true
}

// RemoveStale deletes every stale record and returns the removed slugs in
// sorted order (route stability for the console/API response).
func (r *Registry) RemoveStale() []string {
	if r == nil {
		return []string{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	removed := make([]string, 0)
	for slug, record := range r.bySlug {
		if !record.Stale {
			continue
		}
		removed = append(removed, slug)
		delete(r.bySlug, slug)
		delete(r.byID, record.MountID)
	}
	sort.Strings(removed)
	return removed
}

// Restore seeds the registry from a persisted snapshot. Every restored mount is
// marked stale: it is last-known routing state, not a live endpoint. A
// reconnecting node with the same MountID flips it back to live.
func (r *Registry) Restore(records []*Record) int {
	if r == nil {
		return 0
	}
	restored := 0
	for _, record := range records {
		if record == nil || strings.TrimSpace(record.Slug) == "" || strings.TrimSpace(record.MountID) == "" {
			continue
		}
		cp := *record
		cp.Stale = true
		r.mu.Lock()
		r.bySlug[cp.Slug] = &cp
		r.byID[cp.MountID] = &cp
		r.mu.Unlock()
		restored++
	}
	return restored
}
