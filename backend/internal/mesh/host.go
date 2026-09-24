package mesh

import (
	"crypto/rand"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// Process-side constants (architecture §4.1 / §9.7).
const (
	// DefaultHeartbeatInterval is how often a live node refreshes its record.
	DefaultHeartbeatInterval = 30 * time.Second
	// DefaultHeartbeatTTL is the age after which readers call a record stale.
	DefaultHeartbeatTTL = 90 * time.Second
	// maxWriteFailures is the consecutive-failure budget before the host stops
	// writing (no retry storms; the chat keeps running — §4.1 / §4.7).
	maxWriteFailures = 3
)

// ChatNodeCapabilities is what an aicli chat node advertises once its loopback
// control plane is up (architecture §3.1).
var ChatNodeCapabilities = []string{
	"manifest", "screen", "status", "events", "invoke", "input", "sessions", "mesh",
}

// BaseCapabilities is what every mesh participant advertises even without a
// loopback server: it is discoverable, but cannot be called.
var BaseCapabilities = []string{"mesh"}

// HostConfig describes the process that owns the node record. Every field is
// optional; the zero value produces a `chat` node started from the CLI.
type HostConfig struct {
	// Kind is the node type: chat / runtime-server / other.
	Kind string
	// Origin answers "who started this process": cli / web / resume / mesh.
	Origin string
	// SpawnedBy is the parent node id when another node spawned us.
	SpawnedBy string
	// Version / BuildTime / Exe are build identity fields.
	Version   string
	BuildTime string
	Exe       string
	// WorkspacePath / WorkspaceName describe the workspace (a filter dimension
	// only — never a permission boundary).
	WorkspacePath string
	WorkspaceName string
	// Capabilities defaults to BaseCapabilities.
	Capabilities []string
	// ParentPID defaults to os.Getppid().
	ParentPID int

	// HeartbeatInterval / HeartbeatTTL default to the process defaults.
	HeartbeatInterval time.Duration
	HeartbeatTTL      time.Duration

	// Now overrides the clock (tests).
	Now func() time.Time
	// Warn receives degraded-mode warnings; nil means "write to stderr".
	Warn func(format string, args ...any)
}

// Host is the process-side mesh integration: it owns this process's node
// record, heartbeat, journal and exit cleanup.
//
// Every write is best-effort: a failure never propagates into the chat path
// (MN1). After maxWriteFailures consecutive failures the host stops writing
// and keeps only its in-memory state.
type Host struct {
	mu     sync.Mutex
	cfg    HostConfig
	paths  Paths
	nodeID string
	record NodeRecord
	// journal is the node's event log; nil when the mesh root is unavailable.
	journal *Journal

	started  bool
	closed   bool
	writeOff bool
	failures int
	// Session lease state (architecture §3.3 / §4.4): the lease this process
	// holds for its active session, if any.
	leasePurpose string
	leaseKey     string
	leaseHeld    bool
	leaseLost    bool
	stopCh       chan struct{}
	doneCh       chan struct{}
	closeOnce    sync.Once
}

// NewHost builds the host for this process. It never fails: an unresolvable
// mesh root yields an inert host (fail-closed) and a warning.
func NewHost(cfg HostConfig) *Host {
	if cfg.Kind == "" {
		cfg.Kind = "chat"
	}
	if cfg.Origin == "" {
		cfg.Origin = "cli"
	}
	if cfg.HeartbeatInterval <= 0 {
		cfg.HeartbeatInterval = DefaultHeartbeatInterval
	}
	if cfg.HeartbeatTTL <= 0 {
		cfg.HeartbeatTTL = DefaultHeartbeatTTL
	}
	if cfg.Now == nil {
		cfg.Now = NowUTC
	}
	if cfg.ParentPID == 0 {
		cfg.ParentPID = os.Getppid()
	}
	if len(cfg.Capabilities) == 0 {
		cfg.Capabilities = append([]string(nil), BaseCapabilities...)
	}
	return &Host{cfg: cfg, paths: ResolvePaths()}
}

// Paths returns the resolved mesh layout of this host.
func (h *Host) Paths() Paths {
	if h == nil {
		return Paths{}
	}
	return h.paths
}

// NodeID returns this process's node id ("" before Start on a degraded host).
func (h *Host) NodeID() string {
	if h == nil {
		return ""
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.nodeID
}

// Journal returns the node journal (nil when the mesh root is unavailable).
func (h *Host) Journal() *Journal {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.journal
}

// Start writes the initial node record, opens the journal and launches the
// heartbeat. The returned error is informational only: callers log it as a
// warning and continue running the chat.
func (h *Host) Start() error {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	if h.started {
		h.mu.Unlock()
		return nil
	}
	h.started = true
	if !h.paths.Enabled() {
		h.mu.Unlock()
		h.warn("no mesh directory resolved (no home); this node will not appear on the mesh")
		return nil
	}
	if err := h.paths.EnsureDirs(); err != nil {
		h.warn("cannot prepare mesh directories: %v", err)
	}
	h.nodeID = h.reserveNodeIDLocked()
	now := h.cfg.Now()
	h.record = NodeRecord{
		SchemaVersion: SchemaVersion,
		NodeID:        h.nodeID,
		PID:           os.Getpid(),
		Kind:          h.cfg.Kind,
		Process: ProcessInfo{
			StartedAt: now,
			Exe:       h.cfg.Exe,
			Version:   h.cfg.Version,
			BuildTime: h.cfg.BuildTime,
			ParentPID: h.cfg.ParentPID,
			Origin:    h.cfg.Origin,
			SpawnedBy: h.cfg.SpawnedBy,
		},
		Capabilities: append([]string(nil), h.cfg.Capabilities...),
		Liveness: LivenessInfo{
			StartedAt:       now,
			UpdatedAt:       now,
			HeartbeatAt:     now,
			HeartbeatTTLSec: int(h.cfg.HeartbeatTTL / time.Second),
			State:           string(NodeStateLive),
		},
	}
	if h.cfg.WorkspacePath != "" {
		h.record.Workspace = &WorkspaceInfo{Path: h.cfg.WorkspacePath, Name: h.cfg.WorkspaceName}
	}
	h.journal = OpenJournal(h.paths, h.nodeID, h.cfg.Now)
	h.stopCh = make(chan struct{})
	h.doneCh = make(chan struct{})
	writeErr := h.writeRecordLocked()
	h.mu.Unlock()

	if writeErr != nil {
		h.warn("cannot write node record: %v", writeErr)
	} else {
		h.journal.Append(JournalNodeStarted, "", map[string]any{
			"kind": h.cfg.Kind,
			"pid":  os.Getpid(),
		})
	}
	go h.heartbeatLoop()
	return writeErr
}

// SetEndpoint records the loopback control-plane address and the write token
// once the server is listening. Capabilities grow to the full chat set: the
// node becomes callable.
func (h *Host) SetEndpoint(endpoint EndpointInfo, auth AuthInfo) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	if endpoint.Scheme == "" {
		endpoint.Scheme = "http"
	}
	if endpoint.Host == "" {
		endpoint.Host = "127.0.0.1"
	}
	if endpoint.BaseURL == "" && endpoint.Port > 0 {
		endpoint.BaseURL = fmt.Sprintf("%s://%s:%d", endpoint.Scheme, endpoint.Host, endpoint.Port)
	}
	if endpoint.WebBaseURL == "" && endpoint.BaseURL != "" {
		endpoint.WebBaseURL = endpoint.BaseURL + "/web"
	}
	if endpoint.ManifestURL == "" && endpoint.BaseURL != "" {
		endpoint.ManifestURL = endpoint.BaseURL + "/debug/endpoints"
	}
	h.record.Endpoint = &endpoint
	if auth.Mode != "" || auth.Token != "" {
		h.record.Auth = &auth
	}
	h.journal.SetSecrets(auth.Token)
	h.record.Capabilities = withLoopbackCapabilities(h.record.Capabilities)
	h.touchLocked()
	if err := h.writeRecordLocked(); err != nil {
		h.warn("cannot update node endpoint: %v", err)
	}
}

// SetSession records the active session (nil clears it). Called on session
// activation / resume / switch.
func (h *Host) SetSession(session *SessionInfo) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	previousID := ""
	if h.record.Session != nil {
		previousID = h.record.Session.ID
	}
	if session == nil || strings.TrimSpace(session.ID) == "" {
		if h.record.Session == nil {
			return
		}
		h.record.Session = nil
		h.touchLocked()
		_ = h.writeRecordLocked()
		h.journal.Append(JournalSessionDeactivated, previousID, nil)
		h.releaseSessionLeaseLocked()
		return
	}
	copied := *session
	copied.ID = strings.TrimSpace(copied.ID)
	previous := h.record.Session
	sameSession := previous != nil && previous.ID == copied.ID
	if sameSession && strings.TrimSpace(previous.Title) == strings.TrimSpace(copied.Title) {
		// Re-sync of an already published session (goal reconcile, tool session
		// context ...): nothing to write, nothing to journal.
		return
	}
	if sameSession {
		// Title refresh of the same session: keep activation time and the
		// busy/turn state owned by the turn hooks.
		copied.ActivatedAt = previous.ActivatedAt
		copied.Busy = previous.Busy
		copied.TurnID = previous.TurnID
	}
	if copied.ActivatedAt.IsZero() {
		copied.ActivatedAt = h.cfg.Now()
	}
	h.record.Session = &copied
	h.touchLocked()
	if err := h.writeRecordLocked(); err != nil {
		h.warn("cannot update node session: %v", err)
	}
	if !sameSession {
		h.journal.Append(JournalSessionActivated, copied.ID, map[string]any{"title": copied.Title})
		h.acquireSessionLeaseLocked(copied.ID)
	}
}

// SetBusy flips the session busy flag (turn start / end). Low frequency on
// purpose: no debouncing, just one rewrite per transition.
func (h *Host) SetBusy(busy bool, turnID string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || h.record.Session == nil {
		return
	}
	turnID = strings.TrimSpace(turnID)
	if h.record.Session.Busy == busy && h.record.Session.TurnID == turnID {
		return
	}
	h.record.Session.Busy = busy
	h.record.Session.TurnID = turnID
	h.touchLocked()
	if err := h.writeRecordLocked(); err != nil {
		h.warn("cannot update node busy state: %v", err)
	}
	h.journal.Append(JournalBusyChanged, h.record.Session.ID, map[string]any{
		"busy":    busy,
		"turn_id": turnID,
	})
}

// SessionLeaseStatus reports the session lease this host holds.
type SessionLeaseStatus struct {
	Purpose string
	Key     string
	Held    bool
	// Lost is true when another node took the lease over: this process keeps
	// running but no longer owns the session (architecture §4.4).
	Lost bool
}

// acquireSessionLeaseLocked takes the `session-<sid>` lease (architecture §3.3
// / §4.1). A refusal or an unusable lease directory degrades to "no mutual
// exclusion": the chat keeps running and the journal records why (MN1 / §4.7).
func (h *Host) acquireSessionLeaseLocked(sessionID string) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || !h.paths.Enabled() || h.nodeID == "" {
		return
	}
	if h.leaseHeld && h.leaseKey == sessionID && !h.leaseLost {
		return
	}
	if h.leaseHeld {
		h.releaseSessionLeaseLocked()
	}
	owner := LeaseOwner{NodeID: h.nodeID, PID: os.Getpid()}
	outcome := Acquire(h.paths, LeasePurposeSession, sessionID, owner, AcquireOptions{
		Now: h.cfg.Now(),
		TTL: h.cfg.HeartbeatTTL,
	})
	detail := map[string]any{
		"purpose": LeasePurposeSession,
		"key":     sessionID,
		"ttl_sec": int(h.cfg.HeartbeatTTL / time.Second),
	}
	switch {
	case outcome.Acquired:
		h.leasePurpose = LeasePurposeSession
		h.leaseKey = sessionID
		h.leaseHeld = true
		h.leaseLost = false
		if outcome.Reclaimed {
			h.journal.Append(JournalLeaseReclaimed, sessionID, detail)
		}
		h.journal.Append(JournalLeaseAcquired, sessionID, detail)
	case outcome.Reason == LeaseReasonHeld && outcome.Holder != nil:
		// Another live node already serves this session. The mesh never takes
		// over implicitly (§4.4): we keep running and say so in the journal.
		h.leaseHeld = false
		detail["reason"] = LeaseReasonHeld
		detail["holder_node_id"] = outcome.Holder.OwnerNodeID
		detail["holder_pid"] = outcome.Holder.OwnerPID
		h.journal.Append(JournalLeaseDegraded, sessionID, detail)
	default:
		h.leaseHeld = false
		reason := outcome.Reason
		if reason == "" {
			reason = LeaseReasonDegraded
		}
		detail["reason"] = reason
		h.journal.Append(JournalLeaseDegraded, sessionID, detail)
	}
}

// renewSessionLeaseLocked refreshes the lease from the heartbeat and notices a
// takeover (architecture §4.4).
func (h *Host) renewSessionLeaseLocked(now time.Time) {
	if !h.leaseHeld || h.leaseLost || h.leaseKey == "" {
		return
	}
	outcome := Renew(h.paths, h.leasePurpose, h.leaseKey, LeaseOwner{NodeID: h.nodeID, PID: os.Getpid()}, now, h.cfg.HeartbeatTTL)
	if !outcome.Lost {
		return
	}
	h.leaseHeld = false
	h.leaseLost = true
	h.journal.Append(JournalLeaseDegraded, h.leaseKey, map[string]any{
		"purpose": h.leasePurpose,
		"key":     h.leaseKey,
		"reason":  "taken-over",
	})
}

// releaseSessionLeaseLocked drops the session lease on deactivation or exit.
func (h *Host) releaseSessionLeaseLocked() {
	if !h.leaseHeld || h.leaseKey == "" {
		h.leaseHeld, h.leaseLost = false, false
		return
	}
	key, purpose := h.leaseKey, h.leasePurpose
	if Release(h.paths, purpose, key, LeaseOwner{NodeID: h.nodeID, PID: os.Getpid()}) {
		h.journal.Append(JournalLeaseReleased, key, map[string]any{"purpose": purpose, "key": key})
	}
	h.leaseHeld, h.leaseLost, h.leaseKey, h.leasePurpose = false, false, "", ""
}

// SessionLeaseStatus reports the lease state of this host (diagnostics, and the
// `derived.lease` field of /web/api/mesh/self).
func (h *Host) SessionLeaseStatus() SessionLeaseStatus {
	if h == nil {
		return SessionLeaseStatus{}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return SessionLeaseStatus{Purpose: h.leasePurpose, Key: h.leaseKey, Held: h.leaseHeld, Lost: h.leaseLost}
}

// Close runs the normal-exit path: mark stopped, delete the record, stop the
// heartbeat. Safe to call more than once.
func (h *Host) Close() {
	if h == nil {
		return
	}
	h.mu.Lock()
	if !h.started || h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	stopCh, doneCh := h.stopCh, h.doneCh
	h.mu.Unlock()

	// A fail-closed host (no mesh root) never started a heartbeat loop.
	if stopCh != nil {
		close(stopCh)
	}
	if doneCh != nil {
		<-doneCh
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.paths.Enabled() || h.nodeID == "" {
		return
	}
	sessionID := ""
	if h.record.Session != nil {
		sessionID = h.record.Session.ID
	}
	h.releaseSessionLeaseLocked()
	h.record.Liveness.State = string(NodeStateStopped)
	h.touchLocked()
	_ = h.writeRecordLocked()
	if err := DeleteNodeRecord(h.paths, h.nodeID); err != nil {
		h.warn("cannot remove node record: %v", err)
	}
	h.journal.Append(JournalNodeStopped, sessionID, nil)
}

// WriteDisabled reports whether the host gave up writing (diagnostics/tests).
func (h *Host) WriteDisabled() bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.writeOff
}

// RecordSnapshot returns a copy of the current in-memory record.
func (h *Host) RecordSnapshot() NodeRecord {
	if h == nil {
		return NodeRecord{}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.record
}

func (h *Host) heartbeatLoop() {
	defer close(h.doneCh)
	ticker := time.NewTicker(h.cfg.HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-h.stopCh:
			return
		case <-ticker.C:
			h.heartbeatOnce()
		}
	}
}

func (h *Host) heartbeatOnce() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || h.writeOff {
		return
	}
	now := h.cfg.Now()
	h.record.Liveness.HeartbeatAt = now
	h.record.Liveness.UpdatedAt = now
	if err := h.writeRecordLocked(); err != nil {
		h.failures++
		if h.failures >= maxWriteFailures {
			h.writeOff = true
			h.warn("stopping mesh writes after %d consecutive failures: %v", h.failures, err)
		}
		return
	}
	h.failures = 0
	h.renewSessionLeaseLocked(now)
}

// writeRecordLocked persists the in-memory record. Heartbeats and state flips
// both go through here so the failure budget is shared.
func (h *Host) writeRecordLocked() error {
	if h.writeOff || !h.paths.Enabled() || h.nodeID == "" {
		// Fail-closed (no mesh root) or already given up: silently drop the
		// write. Dropping must never count as a failure — a process that cannot
		// join the mesh is not a degraded chat (MN1 / §4.7).
		return nil
	}
	err := WriteNodeRecord(h.paths, h.record)
	if err != nil {
		h.failures++
		if h.failures >= maxWriteFailures {
			h.writeOff = true
			h.warn("stopping mesh writes after %d consecutive failures: %v", h.failures, err)
		}
	}
	return err
}

func (h *Host) touchLocked() {
	now := h.cfg.Now()
	h.record.Liveness.UpdatedAt = now
	h.record.Liveness.HeartbeatAt = now
	if h.record.Liveness.HeartbeatTTLSec == 0 {
		h.record.Liveness.HeartbeatTTLSec = int(h.cfg.HeartbeatTTL / time.Second)
	}
	if h.record.Liveness.State == "" {
		h.record.Liveness.State = string(NodeStateLive)
	}
}

// reserveNodeIDLocked picks node-<pid>-<yyyyMMddTHHmmssZ>, appending -<rand4>
// when that file already exists (two processes with the same pid and start
// second, e.g. a fast restart in tests).
func (h *Host) reserveNodeIDLocked() string {
	base := fmt.Sprintf("node-%d-%s", os.Getpid(), h.cfg.Now().UTC().Format("20060102T150405Z"))
	candidate := base
	for attempt := 0; attempt < 8; attempt++ {
		if attempt > 0 {
			candidate = base + "-" + randomSuffix()
		}
		if path := h.paths.NodePath(candidate); path != "" {
			if _, err := os.Stat(path); os.IsNotExist(err) {
				return candidate
			}
			continue
		}
		return candidate
	}
	return base
}

func randomSuffix() string {
	var buf [2]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("%04x", time.Now().UnixNano()&0xffff)
	}
	return fmt.Sprintf("%02x%02x", buf[0], buf[1])
}

func withLoopbackCapabilities(existing []string) []string {
	seen := make(map[string]bool, len(existing)+len(ChatNodeCapabilities))
	out := make([]string, 0, len(ChatNodeCapabilities))
	for _, capability := range existing {
		if !seen[capability] {
			seen[capability] = true
			out = append(out, capability)
		}
	}
	for _, capability := range ChatNodeCapabilities {
		if !seen[capability] {
			seen[capability] = true
			out = append(out, capability)
		}
	}
	return out
}

func (h *Host) warn(format string, args ...any) {
	if h.cfg.Warn != nil {
		h.cfg.Warn(format, args...)
		return
	}
	fmt.Fprintf(os.Stderr, "Warning: mesh: "+format+"\n", args...)
}

// ---------------------------------------------------------------------------
// Process-wide current host
// ---------------------------------------------------------------------------

var (
	currentMu   sync.RWMutex
	currentHost *Host
)

// SetCurrent installs the process-wide host (nil disables mesh integration).
func SetCurrent(host *Host) {
	currentMu.Lock()
	currentHost = host
	currentMu.Unlock()
}

// Current returns the process-wide host, or nil when the mesh is disabled.
func Current() *Host {
	currentMu.RLock()
	defer currentMu.RUnlock()
	return currentHost
}

// SetCurrentSession updates the active session of the current host (no-op
// without one) — the one-liner the chat session paths call.
func SetCurrentSession(session *SessionInfo) {
	if host := Current(); host != nil {
		host.SetSession(session)
	}
}

// SetCurrentBusy flips the busy flag of the current host (no-op without one).
func SetCurrentBusy(busy bool, turnID string) {
	if host := Current(); host != nil {
		host.SetBusy(busy, turnID)
	}
}

// SetCurrentEndpoint records the loopback endpoint on the current host.
func SetCurrentEndpoint(endpoint EndpointInfo, auth AuthInfo) {
	if host := Current(); host != nil {
		host.SetEndpoint(endpoint, auth)
	}
}

// CloseCurrent runs the normal-exit path of the current host and clears it.
func CloseCurrent() {
	host := Current()
	if host == nil {
		return
	}
	host.Close()
	SetCurrent(nil)
}
