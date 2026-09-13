package agentcontrol

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// MaxThreadsUnlimited is the explicit opt-in value for agents.maxThreads that
// disables the active-thread quota (plan P2-8 方案 1). Zero no longer means
// "unlimited": like every other agents.* knob it means "unset, fall back to the
// built-in default", so an incomplete config can never silently drop the quota.
const MaxThreadsUnlimited = -1

// ThreadLimitNextAction is the machine-readable recovery instruction attached
// to every over-limit diagnostic (plan P2-8 方案 2 / P1-7).
const ThreadLimitNextAction = "reuse_existing_child_or_close_idle"

// defaultThreadOccupantLimit caps how many children are spelled out in the
// over-limit diagnostic before it degrades to "+N more".
const defaultThreadOccupantLimit = 4

// ResolveMaxThreads maps the configured agents.maxThreads onto the effective
// quota: raw < 0 is the explicit unlimited opt-in, raw == 0 falls back to
// defaultMax (never unlimited), and raw > 0 is the quota itself.
func ResolveMaxThreads(raw, defaultMax int) (limit int, unlimited bool) {
	if raw < 0 {
		return 0, true
	}
	if raw == 0 {
		if defaultMax <= 0 {
			return 0, true
		}
		return defaultMax, false
	}
	return raw, false
}

// HoldsQuota reports whether a durable identity row still counts against
// maxThreads. Terminal rows (closed/stale, or explicitly closed) do not, which
// matches the SQL filter used by ReserveAgentControlAgentSpawn.
func (r AgentRecord) HoldsQuota() bool {
	if r.ClosedAt != nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(r.Status)) {
	case AgentStatusClosed, AgentStatusStale:
		return false
	default:
		return true
	}
}

// QuotaChildren filters a registry listing down to the rows that hold active
// thread quota: every non-root identity that is not already terminal.
func QuotaChildren(records []AgentRecord) []AgentRecord {
	children := make([]AgentRecord, 0, len(records))
	for _, record := range records {
		if record.AgentPath == "/root" || strings.EqualFold(record.AgentType, AgentTypeRoot) {
			continue
		}
		if !record.HoldsQuota() {
			continue
		}
		children = append(children, record)
	}
	return children
}

// Reclaim reasons. Missing/terminal containers are reclaimed by default because
// the identity can no longer be routed to; idle eviction is opt-in.
const (
	ReclaimReasonSessionMissing  = "session_missing"
	ReclaimReasonSessionTerminal = "session_terminal"
	ReclaimReasonIdleTimeout     = "idle_timeout"
)

// ReclaimObservation is the host-observed liveness of one quota-holding child.
// Hosts fill the session-side facts (container lookup, actor state, last
// activity); the registry facts come straight from the durable record.
type ReclaimObservation struct {
	AgentID           string    `json:"agent_id,omitempty"`
	AgentPath         string    `json:"agent_path,omitempty"`
	SessionID         string    `json:"session_id,omitempty"`
	Status            string    `json:"status,omitempty"`
	RegistryUpdatedAt time.Time `json:"registry_updated_at,omitempty"`

	// SessionMissing marks a child whose execution container is gone.
	SessionMissing bool `json:"session_missing,omitempty"`
	// SessionTerminal marks a container in a terminal lifecycle state
	// (closed/archived). A stopped-but-resumable session is not terminal.
	SessionTerminal bool `json:"session_terminal,omitempty"`
	// SessionBusy marks a child that must never be interrupted: running turns,
	// pending approvals/questions/tools, active background jobs, rewinding.
	SessionBusy bool `json:"session_busy,omitempty"`
	// SessionIdleSince is the last observed activity of the container. Zero
	// means "unknown", which disables idle eviction for this child.
	SessionIdleSince time.Time `json:"session_idle_since,omitempty"`
}

// ReclaimPolicy is the host-resolved eviction policy (plan P2-8 方案 3).
type ReclaimPolicy struct {
	// IdleTimeout enables idle eviction when > 0: a child whose container has
	// been idle for at least this long becomes reclaimable. Zero keeps the
	// default conservative behaviour of only reclaiming provably terminal
	// containers.
	IdleTimeout time.Duration `json:"idle_timeout,omitempty"`
	// Now is the evaluation instant; zero means time.Now().UTC().
	Now time.Time `json:"-"`
}

func (p ReclaimPolicy) now() time.Time {
	if p.Now.IsZero() {
		return time.Now().UTC()
	}
	return p.Now.UTC()
}

// ReclaimDecision is one child selected for eviction.
type ReclaimDecision struct {
	AgentID   string `json:"agent_id,omitempty"`
	AgentPath string `json:"agent_path,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// EventKind is the durable wake-event kind emitted for this eviction. It keeps
// the reclaim action distinguishable from an orderly close_agent ("closed").
func (d ReclaimDecision) EventKind() string {
	reason := strings.TrimSpace(d.Reason)
	if reason == "" {
		return "reclaimed"
	}
	return "reclaimed:" + reason
}

// IdleFor reports how long the child has been idle: the session-side idle
// duration when the host knows it, otherwise the registry row age. Zero means
// unknown.
func (o ReclaimObservation) IdleFor(now time.Time) time.Duration {
	if !o.SessionIdleSince.IsZero() {
		return now.UTC().Sub(o.SessionIdleSince.UTC())
	}
	if !o.RegistryUpdatedAt.IsZero() {
		return now.UTC().Sub(o.RegistryUpdatedAt.UTC())
	}
	return 0
}

// Reclaimable returns the reclaim reason for one child, or "" when the child
// must not be touched. Running / waiting-approval / waiting-input children are
// never candidates: their container can still be routed to, so closing them
// would break a live collaboration (plan P2-8 方案 3).
func Reclaimable(observation ReclaimObservation, policy ReclaimPolicy) string {
	if strings.EqualFold(strings.TrimSpace(observation.Status), AgentStatusClosed) {
		return ""
	}
	if observation.SessionMissing {
		return ReclaimReasonSessionMissing
	}
	if observation.SessionTerminal {
		return ReclaimReasonSessionTerminal
	}
	if observation.SessionBusy {
		return ""
	}
	if policy.IdleTimeout > 0 && !observation.SessionIdleSince.IsZero() {
		if policy.now().Sub(observation.SessionIdleSince.UTC()) >= policy.IdleTimeout {
			return ReclaimReasonIdleTimeout
		}
	}
	return ""
}

// SelectReclaimable evaluates every observation and returns the eviction
// decisions in stable agent-path order.
func SelectReclaimable(observations []ReclaimObservation, policy ReclaimPolicy) []ReclaimDecision {
	decisions := make([]ReclaimDecision, 0, len(observations))
	for _, observation := range observations {
		reason := Reclaimable(observation, policy)
		if reason == "" {
			continue
		}
		decisions = append(decisions, ReclaimDecision{
			AgentID:   strings.TrimSpace(observation.AgentID),
			AgentPath: strings.TrimSpace(observation.AgentPath),
			SessionID: strings.TrimSpace(observation.SessionID),
			Reason:    reason,
		})
	}
	sort.SliceStable(decisions, func(i, j int) bool {
		return decisions[i].AgentPath < decisions[j].AgentPath
	})
	return decisions
}

// ThreadOccupant summarises one quota-holding child for diagnostics.
type ThreadOccupant struct {
	AgentPath string        `json:"agent_path,omitempty"`
	SessionID string        `json:"session_id,omitempty"`
	Status    string        `json:"status,omitempty"`
	IdleFor   time.Duration `json:"idle_for,omitempty"`
}

// ThreadOccupants projects observations into diagnostic occupants ordered by
// idleness (longest idle first: the most reclaimable child is listed first).
func ThreadOccupants(observations []ReclaimObservation, now time.Time) []ThreadOccupant {
	occupants := make([]ThreadOccupant, 0, len(observations))
	for _, observation := range observations {
		occupants = append(occupants, ThreadOccupant{
			AgentPath: strings.TrimSpace(observation.AgentPath),
			SessionID: strings.TrimSpace(observation.SessionID),
			Status:    strings.TrimSpace(observation.Status),
			IdleFor:   observation.IdleFor(now),
		})
	}
	sort.SliceStable(occupants, func(i, j int) bool {
		return occupants[i].IdleFor > occupants[j].IdleFor
	})
	return occupants
}

// FormatThreadOccupants renders at most limit occupants as
// "path=/root/a status=idle idle=4m0s" entries; extra occupants collapse into
// "+N more". It returns "" for an empty list.
func FormatThreadOccupants(occupants []ThreadOccupant, limit int) string {
	if len(occupants) == 0 {
		return ""
	}
	if limit <= 0 {
		limit = defaultThreadOccupantLimit
	}
	shown := occupants
	hidden := 0
	if len(occupants) > limit {
		shown = occupants[:limit]
		hidden = len(occupants) - limit
	}
	parts := make([]string, 0, len(shown)+1)
	for _, occupant := range shown {
		fields := make([]string, 0, 4)
		if occupant.AgentPath != "" {
			fields = append(fields, "path="+occupant.AgentPath)
		}
		if occupant.SessionID != "" {
			fields = append(fields, "session="+occupant.SessionID)
		}
		status := occupant.Status
		if status == "" {
			status = "unknown"
		}
		fields = append(fields, "status="+status)
		idle := "unknown"
		if occupant.IdleFor > 0 {
			idle = occupant.IdleFor.Round(time.Second).String()
		}
		fields = append(fields, "idle="+idle)
		parts = append(parts, strings.Join(fields, " "))
	}
	if hidden > 0 {
		parts = append(parts, fmt.Sprintf("+%d more", hidden))
	}
	return strings.Join(parts, ", ")
}

// ThreadLimitMessage renders the over-limit diagnostic: the stable
// "agent spawn thread limit reached" prefix (classification depends on it), the
// counters, any reclaim extras, the next_action recovery hint, and the current
// occupants.
func ThreadLimitMessage(limit, active int, occupants []ThreadOccupant, extras ...string) string {
	parts := []string{fmt.Sprintf("agent spawn thread limit reached: max_threads=%d active_children=%d", limit, active)}
	for _, extra := range extras {
		if trimmed := strings.TrimSpace(extra); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	message := strings.Join(parts, " ") + fmt.Sprintf(
		"; next_action=%s — reuse an existing child with send_input/wait_agent, close an idle child with close_agent, or raise agents.maxThreads (set %d for unlimited)",
		ThreadLimitNextAction, MaxThreadsUnlimited,
	)
	if summary := FormatThreadOccupants(occupants, defaultThreadOccupantLimit); summary != "" {
		message += "; occupants=[" + summary + "]"
	}
	return message
}

// AgentReclaimStore closes an agent identity subtree as a quota eviction. The
// wake event kind distinguishes an eviction (reclaimed:<reason>) from an
// orderly close_agent ("closed") so parents and /debug can explain it.
type AgentReclaimStore interface {
	ReclaimAgentControlAgentSubtree(ctx context.Context, rootSessionID string, agentPath string, reason string, reclaimedAt time.Time) (int64, error)
}

// ReclaimOutcome summarises one eviction pass. Failed counts children the store
// refused to close; they are reported (names and first error) but never abort
// the pass, because the caller re-counts the quota afterwards anyway.
type ReclaimOutcome struct {
	Decisions []ReclaimDecision `json:"decisions,omitempty"`
	// Candidates counts the children the policy selected, whether or not the
	// store was asked to close them. An observe-only pass (plan §P2-9) leaves
	// Decisions empty but still reports how many rows the next enforce pass
	// would reclaim.
	Candidates int      `json:"candidates,omitempty"`
	Reasons    []string `json:"reasons,omitempty"`
	Rows       int64    `json:"rows,omitempty"`
	Failed     int      `json:"failed,omitempty"`
	FirstError string   `json:"first_error,omitempty"`
}

// Reclaimed reports how many children were actually closed.
func (o ReclaimOutcome) Reclaimed() int {
	return len(o.Decisions)
}

// Summary renders the one-line diagnostic ("reclaimed=1 reclaimed_rows=2
// reclaim_reasons=session_terminal"), the evaluate-only form
// ("reclaim_candidates=2 reclaim_reasons=session_terminal"), or "" when nothing
// was even attempted.
func (o ReclaimOutcome) Summary() string {
	if len(o.Decisions) == 0 && o.Failed == 0 && o.Candidates == 0 {
		return ""
	}
	parts := make([]string, 0, 5)
	if len(o.Decisions) > 0 || o.Failed > 0 {
		parts = append(parts, fmt.Sprintf("reclaimed=%d", o.Reclaimed()))
	}
	if o.Candidates > 0 {
		parts = append(parts, fmt.Sprintf("reclaim_candidates=%d", o.Candidates))
	}
	if o.Rows > 0 {
		parts = append(parts, fmt.Sprintf("reclaimed_rows=%d", o.Rows))
	}
	if o.Failed > 0 {
		parts = append(parts, fmt.Sprintf("reclaim_failed=%d", o.Failed))
	}
	reasons := o.Reasons
	if len(reasons) == 0 {
		for _, decision := range o.Decisions {
			if reason := strings.TrimSpace(decision.Reason); reason != "" {
				reasons = append(reasons, reason)
			}
		}
	}
	if len(reasons) > 0 {
		parts = append(parts, "reclaim_reasons="+strings.Join(reasons, ","))
	}
	if first := strings.TrimSpace(o.FirstError); first != "" {
		parts = append(parts, "reclaim_error="+first)
	}
	return strings.Join(parts, " ")
}

// EvaluateReclaimOutcome runs the selection half of the eviction pass without a
// store: the returned outcome carries Candidates only, which is what an
// observe-mode sweep reports (plan §P2-9: automatic close stays opt-in, the
// default mode still shows what the next enforce pass would release).
func EvaluateReclaimOutcome(observations []ReclaimObservation, policy ReclaimPolicy) ReclaimOutcome {
	decisions := SelectReclaimable(observations, policy)
	if len(decisions) == 0 {
		return ReclaimOutcome{}
	}
	outcome := ReclaimOutcome{Candidates: len(decisions), Reasons: make([]string, 0, len(decisions))}
	for _, decision := range decisions {
		if reason := strings.TrimSpace(decision.Reason); reason != "" {
			outcome.Reasons = append(outcome.Reasons, reason)
		}
	}
	return outcome
}

// ReclaimAgentQuota evaluates the observations against the policy and closes
// every reclaimable subtree through the store. The eviction is deliberately
// conservative: only children whose container is missing/terminal (default) or
// idle beyond the opt-in timeout are touched, and per-child failures only
// degrade the outcome instead of aborting the pass.
func ReclaimAgentQuota(ctx context.Context, store AgentReclaimStore, rootSessionID string, observations []ReclaimObservation, policy ReclaimPolicy) (ReclaimOutcome, error) {
	outcome := ReclaimOutcome{}
	if store == nil {
		return outcome, fmt.Errorf("agent reclaim store is not initialized")
	}
	rootSessionID = strings.TrimSpace(rootSessionID)
	if rootSessionID == "" {
		return outcome, fmt.Errorf("root session id is required")
	}
	decisions := SelectReclaimable(observations, policy)
	if len(decisions) == 0 {
		return outcome, nil
	}
	now := policy.now()
	for _, decision := range decisions {
		rows, err := store.ReclaimAgentControlAgentSubtree(ctx, rootSessionID, decision.AgentPath, decision.Reason, now)
		if err != nil {
			outcome.Failed++
			if outcome.FirstError == "" {
				outcome.FirstError = err.Error()
			}
			continue
		}
		outcome.Rows += rows
		outcome.Decisions = append(outcome.Decisions, decision)
	}
	return outcome, nil
}

// QuotaRoot groups the quota-holding children of one root session. The root is
// the scope of both the eviction policy and the agent.reclaimed product event,
// so hosts never have to re-derive it while sweeping a whole registry listing.
type QuotaRoot struct {
	RootSessionID string
	Children      []AgentRecord
}

// QuotaRoots splits a registry listing into the roots that still hold quota, in
// stable root-id order (so a sweep reports the same reasons in the same
// sequence on every host).
func QuotaRoots(records []AgentRecord) []QuotaRoot {
	byRoot := make(map[string][]AgentRecord, len(records))
	roots := make([]string, 0, len(records))
	for _, child := range QuotaChildren(records) {
		root := strings.TrimSpace(child.RootSessionID)
		if root == "" {
			continue
		}
		if _, seen := byRoot[root]; !seen {
			roots = append(roots, root)
		}
		byRoot[root] = append(byRoot[root], child)
	}
	sort.Strings(roots)
	grouped := make([]QuotaRoot, 0, len(roots))
	for _, root := range roots {
		grouped = append(grouped, QuotaRoot{RootSessionID: root, Children: byRoot[root]})
	}
	return grouped
}

// ReclaimObserver is the host half of a sweep: it projects one root's durable
// children into observations by looking up the live containers
// (missing/terminal/busy/idle). Hosts own this because only they know where the
// sessions and actors live.
type ReclaimObserver func(ctx context.Context, children []AgentRecord) []ReclaimObservation

// ReclaimedSink receives every root whose subtree was actually closed, so the
// host can mirror the eviction onto the product event stream
// (agent.reclaimed) with its own source label.
type ReclaimedSink func(ctx context.Context, rootSessionID string, outcome ReclaimOutcome)

// SweepAgentQuotaReclaim runs the shared eviction sweep over a registry
// listing: per root it observes the children, then either closes what the
// policy selects (enforce) or only reports the candidate count (observe).
// Plan §P2-9 方案 3 uses it for the periodic pass, and the CLI's projection
// sweep routes its terminal closes through it as well, so "automatic close"
// shares the decision path and the reclaimed:<reason> event kind with the spawn
// gate and the manual cleanup entry.
//
// A missing store is an error only in enforce mode: an observe pass needs no
// writer. Per-root failures degrade to FirstError instead of aborting the
// sweep, mirroring ReclaimAgentQuota's per-child stance.
func SweepAgentQuotaReclaim(ctx context.Context, store AgentReclaimStore, records []AgentRecord, observe ReclaimObserver, policy ReclaimPolicy, enforce bool, onReclaimed ReclaimedSink) (ReclaimOutcome, error) {
	outcome := ReclaimOutcome{}
	if enforce && store == nil {
		return outcome, fmt.Errorf("agent reclaim store is not initialized")
	}
	roots := QuotaRoots(records)
	if len(roots) == 0 || observe == nil {
		return outcome, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if policy.Now.IsZero() {
		policy.Now = time.Now().UTC()
	}
	for _, root := range roots {
		observations := observe(ctx, root.Children)
		if len(observations) == 0 {
			continue
		}
		if !enforce {
			pass := EvaluateReclaimOutcome(observations, policy)
			outcome.Candidates += pass.Candidates
			for _, reason := range pass.Reasons {
				outcome.Reasons = AppendReclaimReason(outcome.Reasons, reason)
			}
			continue
		}
		pass, err := ReclaimAgentQuota(ctx, store, root.RootSessionID, observations, policy)
		if err != nil {
			if strings.TrimSpace(outcome.FirstError) == "" {
				outcome.FirstError = err.Error()
			}
			continue
		}
		outcome.Rows += pass.Rows
		outcome.Failed += pass.Failed
		outcome.Decisions = append(outcome.Decisions, pass.Decisions...)
		if strings.TrimSpace(outcome.FirstError) == "" {
			outcome.FirstError = pass.FirstError
		}
		if pass.Reclaimed() == 0 {
			continue
		}
		// Reasons mirror the executed decisions (not the observe-mode
		// candidates), so `/debug` renders the same reclaim_reasons=... label
		// whether the pass only evaluated or actually closed.
		for _, decision := range pass.Decisions {
			outcome.Reasons = AppendReclaimReason(outcome.Reasons, decision.Reason)
		}
		if onReclaimed != nil {
			onReclaimed(ctx, root.RootSessionID, pass)
		}
	}
	return outcome, nil
}

// AppendReclaimReason adds one eviction reason to a report list, keeping the
// `/debug` one-liner short by folding duplicates.
func AppendReclaimReason(reasons []string, reason string) []string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return reasons
	}
	for _, existing := range reasons {
		if existing == reason {
			return reasons
		}
	}
	return append(reasons, reason)
}
