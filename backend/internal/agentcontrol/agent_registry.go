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
	// AgentStatusStale marks an abandoned identity whose execution container or
	// lease can no longer support routing. Stale is terminal, but remains
	// distinct from an orderly close for diagnostics and consistency audits.
	AgentStatusStale = "stale"
	// AgentStatusClosed marks an agent identity that should no longer receive
	// routing or target resolution traffic.
	AgentStatusClosed = "closed"
)

// AgentRecord is the storage-neutral AgentControl identity graph row. It is
// intentionally separate from chat session state: sessions remain execution
// containers, while AgentRecord is the durable control-plane identity.
type AgentRecord struct {
	Seq                      int64      `json:"seq,omitempty"`
	AgentID                  string     `json:"agent_id,omitempty"`
	RootSessionID            string     `json:"root_session_id,omitempty"`
	ParentAgentID            string     `json:"parent_agent_id,omitempty"`
	ParentSessionID          string     `json:"parent_session_id,omitempty"`
	SessionID                string     `json:"session_id,omitempty"`
	AgentPath                string     `json:"agent_path,omitempty"`
	Depth                    int        `json:"depth,omitempty"`
	AgentType                string     `json:"agent_type,omitempty"`
	Nickname                 string     `json:"nickname,omitempty"`
	Workflow                 string     `json:"workflow,omitempty"`
	TeamID                   string     `json:"team_id,omitempty"`
	TeammateID               string     `json:"teammate_id,omitempty"`
	Provider                 string     `json:"provider,omitempty"`
	Model                    string     `json:"model,omitempty"`
	ReasoningEffort          string     `json:"reasoning_effort,omitempty"`
	Difficulty               string     `json:"difficulty,omitempty"`
	DifficultySource         string     `json:"difficulty_source,omitempty"`
	DifficultyRationale      string     `json:"difficulty_rationale,omitempty"`
	RouteSource              string     `json:"route_source,omitempty"`
	RouteWarnings            []string   `json:"route_warnings,omitempty"`
	FallbackUsed             bool       `json:"fallback_used,omitempty"`
	FallbackReason           string     `json:"fallback_reason,omitempty"`
	RequestedProvider        string     `json:"requested_provider,omitempty"`
	EffectiveProvider        string     `json:"effective_provider,omitempty"`
	RequestedModel           string     `json:"requested_model,omitempty"`
	EffectiveModel           string     `json:"effective_model,omitempty"`
	RequestedReasoningEffort string     `json:"requested_reasoning_effort,omitempty"`
	EffectiveReasoningEffort string     `json:"effective_reasoning_effort,omitempty"`
	RequestedPermissionMode  string     `json:"requested_permission_mode,omitempty"`
	EffectivePermissionMode  string     `json:"effective_permission_mode,omitempty"`
	Status                   string     `json:"status,omitempty"`
	CreatedAt                time.Time  `json:"created_at,omitempty"`
	UpdatedAt                time.Time  `json:"updated_at,omitempty"`
	ClosedAt                 *time.Time `json:"closed_at,omitempty"`
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
	r.Provider = strings.TrimSpace(r.Provider)
	r.Model = strings.TrimSpace(r.Model)
	r.ReasoningEffort = strings.TrimSpace(r.ReasoningEffort)
	r.Difficulty = strings.TrimSpace(r.Difficulty)
	r.DifficultySource = strings.TrimSpace(r.DifficultySource)
	r.DifficultyRationale = strings.TrimSpace(r.DifficultyRationale)
	r.RouteSource = strings.TrimSpace(r.RouteSource)
	r.RouteWarnings = trimAgentRecordStrings(r.RouteWarnings)
	r.FallbackReason = strings.TrimSpace(r.FallbackReason)
	r.RequestedProvider = strings.TrimSpace(r.RequestedProvider)
	r.EffectiveProvider = strings.TrimSpace(r.EffectiveProvider)
	r.RequestedModel = strings.TrimSpace(r.RequestedModel)
	r.EffectiveModel = strings.TrimSpace(r.EffectiveModel)
	r.RequestedReasoningEffort = strings.TrimSpace(r.RequestedReasoningEffort)
	r.EffectiveReasoningEffort = strings.TrimSpace(r.EffectiveReasoningEffort)
	r.RequestedPermissionMode = strings.TrimSpace(r.RequestedPermissionMode)
	r.EffectivePermissionMode = strings.TrimSpace(r.EffectivePermissionMode)
	r.Status = strings.TrimSpace(r.Status)
	if r.Status == "" {
		r.Status = AgentStatusActive
	}
	if r.Depth < 0 {
		r.Depth = 0
	}
	return r
}

func trimAgentRecordStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			result = append(result, value)
		}
	}
	return result
}

// Closed reports whether the durable identity is terminal.
func (r AgentRecord) Closed() bool {
	status := strings.TrimSpace(r.Status)
	return r.ClosedAt != nil ||
		strings.EqualFold(status, AgentStatusClosed) ||
		strings.EqualFold(status, AgentStatusStale)
}

// agentRecordsShareWakeState reports whether a write only refreshed bookkeeping
// (updated_at) and changed nothing an AgentWakeEvent consumer could observe.
//
// Host projections materialize every identity on every pass, so the store is
// asked to rewrite rows that did not change. The write itself must stay: reclaim
// reads RegistryUpdatedAt as its idle fallback (reclaim.go idleSince), so
// dropping it would freeze a heartbeat input. The wake event, however, exists to
// announce a change, so a refresh that keeps the persisted shape identical must
// not append one — otherwise the durable wake log and its in-process
// notifications grow with the reconcile cadence instead of with the identity
// graph. Compared fields are the persisted shape; updated_at, created_at and the
// row sequence are excluded because they move on every write.
func agentRecordsShareWakeState(before, after AgentRecord) bool {
	before = before.Normalize()
	after = after.Normalize()
	if before.AgentID != after.AgentID ||
		before.RootSessionID != after.RootSessionID ||
		before.ParentAgentID != after.ParentAgentID ||
		before.ParentSessionID != after.ParentSessionID ||
		before.SessionID != after.SessionID ||
		before.AgentPath != after.AgentPath ||
		before.Depth != after.Depth ||
		before.AgentType != after.AgentType ||
		before.Nickname != after.Nickname ||
		before.Workflow != after.Workflow ||
		before.TeamID != after.TeamID ||
		before.TeammateID != after.TeammateID ||
		before.Provider != after.Provider ||
		before.Model != after.Model ||
		before.ReasoningEffort != after.ReasoningEffort ||
		before.Difficulty != after.Difficulty ||
		before.DifficultySource != after.DifficultySource ||
		before.DifficultyRationale != after.DifficultyRationale ||
		before.RouteSource != after.RouteSource ||
		before.FallbackUsed != after.FallbackUsed ||
		before.FallbackReason != after.FallbackReason ||
		before.RequestedProvider != after.RequestedProvider ||
		before.EffectiveProvider != after.EffectiveProvider ||
		before.RequestedModel != after.RequestedModel ||
		before.EffectiveModel != after.EffectiveModel ||
		before.RequestedReasoningEffort != after.RequestedReasoningEffort ||
		before.EffectiveReasoningEffort != after.EffectiveReasoningEffort ||
		before.RequestedPermissionMode != after.RequestedPermissionMode ||
		before.EffectivePermissionMode != after.EffectivePermissionMode ||
		before.Status != after.Status {
		return false
	}
	if !agentRecordStringsEqual(before.RouteWarnings, after.RouteWarnings) {
		return false
	}
	return agentRecordClosedAtEqual(before.ClosedAt, after.ClosedAt)
}

// agentRecordStringsEqual compares two normalized string slices; nil and empty
// are the same state and the separator cannot appear inside a trimmed value.
func agentRecordStringsEqual(before, after []string) bool {
	if len(before) != len(after) {
		return false
	}
	for i := range before {
		if before[i] != after[i] {
			return false
		}
	}
	return true
}

// agentRecordClosedAtEqual treats a nil and a zero timestamp as the same state,
// because the store persists a zero closed_at as NULL.
func agentRecordClosedAtEqual(before, after *time.Time) bool {
	beforeSet := before != nil && !before.IsZero()
	afterSet := after != nil && !after.IsZero()
	if !beforeSet || !afterSet {
		return beforeSet == afterSet
	}
	return before.Equal(*after)
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
// Seq is the wake log's own sequence (agent_control_agent_wake_events.id), which
// is a different id space from AgentRecord.Seq (the agent_control_agents row id):
// wake ordering/catch-up uses LastAgentControlAgentWakeSeq plus this stream,
// while row catch-up uses ListAgentControlAgents(AfterSeq). The event carries the
// identity fields of the row at the moment it changed, so a watcher can either
// act on the snapshot or re-read the row by AgentID.
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

// AgentStaleMarker optionally preserves the abandoned-vs-orderly terminal
// distinction while removing the identity subtree from active routing.
type AgentStaleMarker interface {
	MarkAgentControlAgentSubtreeStale(ctx context.Context, rootSessionID string, agentPath string, staleAt time.Time) (int64, error)
}

// AgentSpawnReservationStore optionally supports an atomic spawn reservation.
// Stores that implement it can enforce cross-process active-thread limits in
// the same transaction that creates the durable child identity row.
type AgentSpawnReservationStore interface {
	ReserveAgentControlAgentSpawn(ctx context.Context, root AgentRecord, child AgentRecord, maxThreads int) (AgentRecord, error)
}

// AgentSpawnReservationReleaser optionally compensates a spawn reservation
// that never became a runnable child (actor creation or first-prompt submit
// failed). Releases move the reserved row to the stale terminal state so it
// stops counting against max_threads while retaining diagnostics. Releasing an
// unknown or already terminal row is a no-op so callers can retry safely.
type AgentSpawnReservationReleaser interface {
	ReleaseAgentControlAgentSpawn(ctx context.Context, agentID string, reason string) (AgentRecord, error)
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
