package agentcontrol

import (
	"context"
	"strings"
	"time"
)

const (
	// AgentTypeRoot identifies the foreground/root session node.
	AgentTypeRoot = "root"
	// AgentTypeChild identifies a lightweight spawn_agent child.
	AgentTypeChild = "child"
	// AgentTypeTeamTeammate identifies a spawn_team teammate session.
	AgentTypeTeamTeammate = "team_teammate"

	// AgentStatusActive is the default open identity state.
	AgentStatusActive = "active"
	// AgentStatusClosed marks an agent identity that should no longer receive
	// routing or target resolution traffic.
	AgentStatusClosed = "closed"
)

// AgentRecord is the storage-neutral AgentControl identity graph row. It is
// intentionally separate from chat session state: sessions remain execution
// containers, while AgentRecord is the durable control-plane identity.
type AgentRecord struct {
	Seq             int64      `json:"seq,omitempty"`
	AgentID         string     `json:"agent_id,omitempty"`
	RootSessionID   string     `json:"root_session_id,omitempty"`
	ParentAgentID   string     `json:"parent_agent_id,omitempty"`
	ParentSessionID string     `json:"parent_session_id,omitempty"`
	SessionID       string     `json:"session_id,omitempty"`
	AgentPath       string     `json:"agent_path,omitempty"`
	Depth           int        `json:"depth,omitempty"`
	AgentType       string     `json:"agent_type,omitempty"`
	Nickname        string     `json:"nickname,omitempty"`
	Workflow        string     `json:"workflow,omitempty"`
	TeamID          string     `json:"team_id,omitempty"`
	TeammateID      string     `json:"teammate_id,omitempty"`
	Status          string     `json:"status,omitempty"`
	CreatedAt       time.Time  `json:"created_at,omitempty"`
	UpdatedAt       time.Time  `json:"updated_at,omitempty"`
	ClosedAt        *time.Time `json:"closed_at,omitempty"`
}

// Normalize returns a stable AgentRecord shape for storage and comparison.
func (r AgentRecord) Normalize() AgentRecord {
	r.AgentID = strings.TrimSpace(r.AgentID)
	r.RootSessionID = strings.TrimSpace(r.RootSessionID)
	r.ParentAgentID = strings.TrimSpace(r.ParentAgentID)
	r.ParentSessionID = strings.TrimSpace(r.ParentSessionID)
	r.SessionID = strings.TrimSpace(r.SessionID)
	r.AgentPath = normalizeAgentPath(r.AgentPath)
	r.AgentType = strings.TrimSpace(r.AgentType)
	r.Nickname = strings.TrimSpace(r.Nickname)
	r.Workflow = strings.TrimSpace(r.Workflow)
	r.TeamID = strings.TrimSpace(r.TeamID)
	r.TeammateID = strings.TrimSpace(r.TeammateID)
	r.Status = strings.TrimSpace(r.Status)
	if r.Status == "" {
		r.Status = AgentStatusActive
	}
	if r.Depth < 0 {
		r.Depth = 0
	}
	return r
}

// Closed reports whether the durable identity is terminal.
func (r AgentRecord) Closed() bool {
	return r.ClosedAt != nil || strings.EqualFold(strings.TrimSpace(r.Status), AgentStatusClosed)
}

// AgentFilter describes reads from a durable AgentControl identity registry.
type AgentFilter struct {
	AgentID         string
	RootSessionID   string
	ParentAgentID   string
	ParentSessionID string
	SessionID       string
	AgentPath       string
	PathPrefix      string
	Workflow        string
	TeamID          string
	TeammateID      string
	IncludeClosed   bool
	AfterSeq        int64
	Limit           int
}

// AgentWakeFilter identifies agent identity graph wake streams.
type AgentWakeFilter struct {
	RootSessionID string
	ParentAgentID string
	SessionID     string
	AgentPath     string
	PathPrefix    string
	Workflow      string
	TeamID        string
	TeammateID    string
}

// Normalize trims agent wake filter fields.
func (f AgentWakeFilter) Normalize() AgentWakeFilter {
	f.RootSessionID = strings.TrimSpace(f.RootSessionID)
	f.ParentAgentID = strings.TrimSpace(f.ParentAgentID)
	f.SessionID = strings.TrimSpace(f.SessionID)
	f.AgentPath = normalizeAgentPath(f.AgentPath)
	f.PathPrefix = normalizeAgentPath(f.PathPrefix)
	f.Workflow = strings.TrimSpace(f.Workflow)
	f.TeamID = strings.TrimSpace(f.TeamID)
	f.TeammateID = strings.TrimSpace(f.TeammateID)
	return f
}

// AgentWakeEvent is emitted when durable AgentControl identity rows change.
// Seq is the durable agent registry row id, so consumers can combine watch
// notifications with ListAgentControlAgents(AfterSeq) for catch-up.
type AgentWakeEvent struct {
	Seq             int64     `json:"seq,omitempty"`
	AgentID         string    `json:"agent_id,omitempty"`
	RootSessionID   string    `json:"root_session_id,omitempty"`
	ParentAgentID   string    `json:"parent_agent_id,omitempty"`
	ParentSessionID string    `json:"parent_session_id,omitempty"`
	SessionID       string    `json:"session_id,omitempty"`
	AgentPath       string    `json:"agent_path,omitempty"`
	Depth           int       `json:"depth,omitempty"`
	AgentType       string    `json:"agent_type,omitempty"`
	Workflow        string    `json:"workflow,omitempty"`
	TeamID          string    `json:"team_id,omitempty"`
	TeammateID      string    `json:"teammate_id,omitempty"`
	Status          string    `json:"status,omitempty"`
	EventKind       string    `json:"event_kind,omitempty"`
	CreatedAt       time.Time `json:"created_at,omitempty"`
}

// Normalize trims agent wake event fields.
func (e AgentWakeEvent) Normalize() AgentWakeEvent {
	e.AgentID = strings.TrimSpace(e.AgentID)
	e.RootSessionID = strings.TrimSpace(e.RootSessionID)
	e.ParentAgentID = strings.TrimSpace(e.ParentAgentID)
	e.ParentSessionID = strings.TrimSpace(e.ParentSessionID)
	e.SessionID = strings.TrimSpace(e.SessionID)
	e.AgentPath = normalizeAgentPath(e.AgentPath)
	e.AgentType = strings.TrimSpace(e.AgentType)
	e.Workflow = strings.TrimSpace(e.Workflow)
	e.TeamID = strings.TrimSpace(e.TeamID)
	e.TeammateID = strings.TrimSpace(e.TeammateID)
	e.Status = strings.TrimSpace(e.Status)
	e.EventKind = strings.TrimSpace(e.EventKind)
	return e
}

// Normalize returns a trimmed AgentFilter.
func (f AgentFilter) Normalize() AgentFilter {
	f.AgentID = strings.TrimSpace(f.AgentID)
	f.RootSessionID = strings.TrimSpace(f.RootSessionID)
	f.ParentAgentID = strings.TrimSpace(f.ParentAgentID)
	f.ParentSessionID = strings.TrimSpace(f.ParentSessionID)
	f.SessionID = strings.TrimSpace(f.SessionID)
	f.AgentPath = normalizeAgentPath(f.AgentPath)
	f.PathPrefix = normalizeAgentPath(f.PathPrefix)
	f.Workflow = strings.TrimSpace(f.Workflow)
	f.TeamID = strings.TrimSpace(f.TeamID)
	f.TeammateID = strings.TrimSpace(f.TeammateID)
	if f.AfterSeq < 0 {
		f.AfterSeq = 0
	}
	return f
}

// AgentRegistryReader exposes durable AgentControl identity graph reads.
type AgentRegistryReader interface {
	ListAgentControlAgents(ctx context.Context, filter AgentFilter) ([]AgentRecord, error)
}

// AgentRegistryWriter exposes durable AgentControl identity graph writes.
type AgentRegistryWriter interface {
	UpsertAgentControlAgent(ctx context.Context, record AgentRecord) (AgentRecord, error)
	CloseAgentControlAgentSubtree(ctx context.Context, rootSessionID string, agentPath string, closedAt time.Time) (int64, error)
}

// AgentSpawnReservationStore optionally supports an atomic spawn reservation.
// Stores that implement it can enforce cross-process active-thread limits in
// the same transaction that creates the durable child identity row.
type AgentSpawnReservationStore interface {
	ReserveAgentControlAgentSpawn(ctx context.Context, root AgentRecord, child AgentRecord, maxThreads int) (AgentRecord, error)
}

// AgentWakeWatcher exposes AgentControl identity graph wake notifications.
type AgentWakeWatcher interface {
	WatchAgentControlAgentWake(ctx context.Context, filter AgentWakeFilter) (<-chan AgentWakeEvent, func())
}

// AgentWakeSequencer exposes the durable high-water mark for identity graph
// changes.
type AgentWakeSequencer interface {
	LastAgentControlAgentWakeSeq(ctx context.Context, filter AgentWakeFilter) (int64, error)
}

// AgentWakeSource combines agent graph watch and sequence reads.
type AgentWakeSource interface {
	AgentWakeWatcher
	AgentWakeSequencer
}

// AgentRegistryStore is the combined durable AgentControl identity registry
// surface. It is the identity-graph counterpart to GlobalMailboxRegistryStore.
type AgentRegistryStore interface {
	AgentRegistryReader
	AgentRegistryWriter
	Close() error
}

func normalizeAgentPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	path = "/" + strings.Trim(path, "/")
	if path == "/" {
		return "/root"
	}
	return path
}

// AgentPathMatchesPrefix reports whether path is exactly prefix or is inside
// prefix as a path subtree. It intentionally does not treat sibling names such
// as /root/child-10 as descendants of /root/child.
func AgentPathMatchesPrefix(path string, prefix string) bool {
	path = normalizeAgentPath(path)
	prefix = strings.TrimRight(normalizeAgentPath(prefix), "/")
	if path == "" || prefix == "" {
		return false
	}
	return strings.EqualFold(path, prefix) || strings.HasPrefix(strings.ToLower(path), strings.ToLower(prefix)+"/")
}
