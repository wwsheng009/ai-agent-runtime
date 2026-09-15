package supervision

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// P2-12 方案 3 的宿主侧共享实现。
//
// 在它出现之前，「确认 / 延后 / 收敛 / 取消」这套动作只有 HTTP 宿主与
// CLI 的 /debug supervision 两条入口：模型（父 agent）在自身回合内既看不到
// 监督快照，也无法收敛通知，失败类 critical 行只能靠人看、靠宿主命令改。
// 本服务把「scope 校验 + allowed_actions 复核 + expected_version CAS」集中
// 在一处，让模型工具入口（toolbroker）与既有宿主命令复用同一口径，避免出现
// 第二套判定标准。
type LocalControlService struct {
	store   Store
	actions *ActionService
	now     func() time.Time
}

// NewLocalControlService wires the durable store and (optional) action service.
// A nil action service still serves snapshot/ack/defer/resolve; control actions
// then fail loudly ("no executor configured") instead of pretending success.
func NewLocalControlService(store Store, actions *ActionService) *LocalControlService {
	svc := &LocalControlService{
		store:   store,
		actions: actions,
		now:     timeNow,
	}
	if svc.actions == nil && store != nil {
		svc.actions = NewActionService(store, nil, nil)
	}
	return svc
}

// Store exposes the durable store for host adapters that need raw reads.
func (s *LocalControlService) Store() Store {
	if s == nil {
		return nil
	}
	return s.store
}

// Actions exposes the durable action service (nil when unwired).
func (s *LocalControlService) Actions() *ActionService {
	if s == nil {
		return nil
	}
	return s.actions
}

// LocalSnapshotRequest is the input of a scoped supervision snapshot. (The
// name is distinct from the SnapshotProvider's own SnapshotRequest in
// snapshot.go, which describes a different, provider-facing payload.)
type LocalSnapshotRequest struct {
	// RootScopeID is the caller's own root scope (session id or team id).
	RootScopeID string
	// TargetParentSessionID / TargetParentTeamID narrow the digest to the rows
	// addressed at this parent (same fields the preflight hook uses).
	TargetParentSessionID string
	TargetParentTeamID    string
	// AfterSeq is the last lifecycle sequence the caller already processed.
	AfterSeq int64
	// IncludeResolved adds items resolved after AfterSeq.
	IncludeResolved bool
	// Limit caps the number of injected items (unresolved_digest_limit).
	Limit int
	// SubjectPresence optionally downgrades rows whose subject no longer
	// exists in the control plane (P2-12 stale 判定).
	SubjectPresence SubjectPresenceFunc
	// HostCapabilities optionally narrows the announced action set to the
	// channels this host actually wired (P2-12 方案 1). nil = undeclared.
	HostCapabilities *HostCapabilities
}

// Snapshot returns the deterministic preflight digest for one root scope. It
// never mutates decision state: acknowledging is an explicit, audited call.
func (s *LocalControlService) Snapshot(ctx context.Context, req LocalSnapshotRequest) (*Digest, error) {
	if s == nil || s.store == nil {
		return nil, fmt.Errorf("supervision store is required")
	}
	scope := strings.TrimSpace(req.RootScopeID)
	if scope == "" {
		return nil, fmt.Errorf("%w: supervision scope is required", ErrActionInvalid)
	}
	return BuildDigest(ctx, s.store, DigestRequest{
		RootScopeID:           scope,
		TargetParentSessionID: strings.TrimSpace(req.TargetParentSessionID),
		TargetParentTeamID:    strings.TrimSpace(req.TargetParentTeamID),
		AfterSeq:              req.AfterSeq,
		Limit:                 req.Limit,
		IncludeResolvedSince:  req.IncludeResolved,
		SubjectPresence:       req.SubjectPresence,
		HostCapabilities:      req.HostCapabilities,
	})
}

// LifecycleDecisionRequest is the shared input of ack / defer / resolve.
type LifecycleDecisionRequest struct {
	NotificationID string
	// Scopes are the root scopes the caller owns. A notification outside them
	// is rejected instead of silently mutated.
	Scopes []string
	// ExpectedVersion is the optimistic-concurrency guard. HasExpectedVersion
	// false means "use the version just read"; the store still CAS-checks it.
	ExpectedVersion    int64
	HasExpectedVersion bool
	// Note is the audit note required by acknowledge.
	Note string
	// Reason is the audit reason required by defer.
	Reason string
	// Until is the defer deadline (must be in the future).
	Until time.Time
	// Resolution is the target resolution state for resolve.
	Resolution ResolutionState
}

// Acknowledge marks a notification acknowledged after re-validating that the
// current state allows it. Convergence (`unresolved` -> decided) is why this
// exists: a critical row the parent has actually handled must stop being
// re-injected every turn (N9).
func (s *LocalControlService) Acknowledge(ctx context.Context, req LifecycleDecisionRequest) (*Notification, error) {
	record, err := s.loadScopedNotification(ctx, req)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.Note) == "" {
		return nil, fmt.Errorf("%w: acknowledge requires an audit note", ErrActionInvalid)
	}
	if err := s.requireAllowed(record, ActionAcknowledge); err != nil {
		return nil, err
	}
	version, err := s.resolveVersion(record, req)
	if err != nil {
		return nil, err
	}
	ok, err := s.store.AcknowledgeNotification(ctx, record.NotificationID, s.now().UTC(), version)
	if err != nil {
		return nil, fmt.Errorf("acknowledge notification %s: %w", record.NotificationID, err)
	}
	if !ok {
		return nil, conflictError(record, version)
	}
	return s.reload(ctx, record)
}

// Defer postpones a notification until a deadline. Before the deadline it is
// out of the preflight view; afterwards it re-enters automatically (the row is
// never silently dropped).
func (s *LocalControlService) Defer(ctx context.Context, req LifecycleDecisionRequest) (*Notification, error) {
	record, err := s.loadScopedNotification(ctx, req)
	if err != nil {
		return nil, err
	}
	until := req.Until.UTC()
	if until.IsZero() {
		return nil, fmt.Errorf("%w: defer requires an until deadline", ErrActionInvalid)
	}
	if !until.After(s.now().UTC()) {
		return nil, fmt.Errorf("%w: defer deadline %s is not in the future", ErrActionInvalid, until.Format(time.RFC3339))
	}
	if strings.TrimSpace(req.Reason) == "" {
		return nil, fmt.Errorf("%w: defer requires an audit reason", ErrActionInvalid)
	}
	if err := s.requireAllowed(record, ActionDefer); err != nil {
		return nil, err
	}
	version, err := s.resolveVersion(record, req)
	if err != nil {
		return nil, err
	}
	ok, err := s.store.DeferNotification(ctx, record.NotificationID, until, strings.TrimSpace(req.Reason), version)
	if err != nil {
		return nil, fmt.Errorf("defer notification %s: %w", record.NotificationID, err)
	}
	if !ok {
		return nil, conflictError(record, version)
	}
	return s.reload(ctx, record)
}

// Resolve closes the notification's resolution state (closed / recovered /
// failed). This is the terminal convergence for a row whose subject is gone.
func (s *LocalControlService) Resolve(ctx context.Context, req LifecycleDecisionRequest) (*Notification, error) {
	record, err := s.loadScopedNotification(ctx, req)
	if err != nil {
		return nil, err
	}
	resolution := ResolutionState(strings.TrimSpace(string(req.Resolution)))
	switch resolution {
	case ResolutionClosed, ResolutionRecovered, ResolutionFailed:
	default:
		return nil, fmt.Errorf("%w: unsupported resolution %q (want closed|recovered|failed)", ErrActionInvalid, req.Resolution)
	}
	version, err := s.resolveVersion(record, req)
	if err != nil {
		return nil, err
	}
	ok, err := s.store.ResolveNotification(ctx, record.NotificationID, resolution, s.now().UTC(), version)
	if err != nil {
		return nil, fmt.Errorf("resolve notification %s: %w", record.NotificationID, err)
	}
	if !ok {
		return nil, conflictError(record, version)
	}
	return s.reload(ctx, record)
}

// ControlRequest is the shared input of a descendant control action (cancel /
// close / retry / reassign). The target is always taken from the notification
// row, so a caller can never address a subject outside its own scope.
type ControlRequest struct {
	NotificationID string
	// Scopes are the root scopes the caller owns.
	Scopes []string
	// RequestedByID identifies the caller in the durable action audit trail.
	RequestedByID string
	// Action is the requested control action (validated against the durable
	// notification's server-computed allowed_actions).
	Action ActionKind
	// Reason is the audit reason; required for every mutating action.
	Reason string
	// CascadeMode selects single-target vs subtree propagation.
	CascadeMode CascadeMode
	// ExpectedVersion guards against acting on a state the caller never saw.
	ExpectedVersion    int64
	HasExpectedVersion bool
}

// Control executes a durable control action against the notification subject.
// The flow mirrors the HTTP host: request (persisted) -> accept -> execute, so
// a crash between steps leaves an auditable requested/accepted row instead of a
// silent partial effect. It returns the terminal action record; when execution
// fails the record is still returned so the caller can report action_id.
func (s *LocalControlService) Control(ctx context.Context, req ControlRequest) (ActionRecord, error) {
	record, err := s.loadScopedNotification(ctx, LifecycleDecisionRequest{NotificationID: req.NotificationID, Scopes: req.Scopes})
	if err != nil {
		return ActionRecord{}, err
	}
	if s.actions == nil {
		return ActionRecord{}, fmt.Errorf("%w: action service is not configured", ErrActionInvalid)
	}
	action := req.Action
	switch action {
	case ActionInspect, ActionCancel, ActionClose, ActionCancelSubtree, ActionRetry, ActionReassign:
	default:
		return ActionRecord{}, fmt.Errorf("%w: unsupported control action %q", ErrActionInvalid, req.Action)
	}
	reason := strings.TrimSpace(req.Reason)
	if action != ActionInspect && reason == "" {
		return ActionRecord{}, fmt.Errorf("%w: %s requires an audit reason", ErrActionInvalid, action)
	}
	version, err := s.resolveVersion(record, LifecycleDecisionRequest{
		ExpectedVersion:    req.ExpectedVersion,
		HasExpectedVersion: req.HasExpectedVersion,
	})
	if err != nil {
		return ActionRecord{}, err
	}
	requestedBy := strings.TrimSpace(req.RequestedByID)
	if requestedBy == "" {
		requestedBy = strings.TrimSpace(record.RootScopeID)
	}
	requested, err := s.actions.RequestAction(ctx, ActionRequest{
		RootScopeID:          record.RootScopeID,
		RequestedByKind:      "agent_session",
		RequestedByID:        requestedBy,
		TargetKind:           record.SubjectKind,
		TargetID:             record.SubjectID,
		Action:               action,
		CascadeMode:          req.CascadeMode,
		Reason:               reason,
		ExpectedVersion:      version,
		ExpectedFencingToken: record.DiagnosticRef,
	})
	if err != nil {
		return ActionRecord{}, err
	}
	if action == ActionInspect {
		return requested, nil
	}
	accepted, err := s.actions.AcceptAction(ctx, requested.ActionID)
	if err != nil {
		return requested, err
	}
	executed, err := s.actions.ExecuteAction(ctx, accepted.ActionID)
	if err != nil {
		return accepted, err
	}
	return executed, nil
}

// loadScopedNotification reads a notification and enforces that it belongs to
// one of the caller's root scopes.
func (s *LocalControlService) loadScopedNotification(ctx context.Context, req LifecycleDecisionRequest) (*Notification, error) {
	if s == nil || s.store == nil {
		return nil, fmt.Errorf("supervision store is required")
	}
	notificationID := strings.TrimSpace(req.NotificationID)
	if notificationID == "" {
		return nil, fmt.Errorf("%w: notification_id is required", ErrActionInvalid)
	}
	record, err := s.store.GetNotification(ctx, notificationID)
	if err != nil {
		return nil, fmt.Errorf("load notification %s: %w", notificationID, err)
	}
	if record == nil {
		// The id is stale, fabricated (e.g. "agent_run:<subject_id>" instead of
		// the real opaque "n-<subject>-<hash>"), or belongs to another host's
		// store. Point the caller at the only reliable source of live ids so
		// the retry is a snapshot read, not another guess.
		return nil, fmt.Errorf("%w: notification %s not found (use notification_id from supervision_snapshot; ids are opaque and change per subject)",
			ErrActionNotFound, notificationID)
	}
	if !notificationInScopes(*record, req.Scopes) {
		return nil, fmt.Errorf("%w: notification %s is outside the caller scope", ErrActionNotAllowed, notificationID)
	}
	return record, nil
}

func (s *LocalControlService) requireAllowed(record *Notification, action ActionKind) error {
	if record == nil {
		return fmt.Errorf("%w: notification is required", ErrActionInvalid)
	}
	allowed := Evaluator{}.EvaluateAllowedActions(*record)
	if !AllowedAction(allowed, action) {
		return fmt.Errorf("%w: notification %s allows [%s] but %s was requested",
			ErrActionNotAllowed, record.NotificationID, strings.Join(allowed, ","), action)
	}
	return nil
}

func (s *LocalControlService) resolveVersion(record *Notification, req LifecycleDecisionRequest) (int64, error) {
	if record == nil {
		return 0, fmt.Errorf("%w: notification is required", ErrActionInvalid)
	}
	if !req.HasExpectedVersion {
		return record.Version, nil
	}
	if record.Version != req.ExpectedVersion {
		return 0, conflictError(record, req.ExpectedVersion)
	}
	return req.ExpectedVersion, nil
}

func conflictError(record *Notification, expected int64) error {
	return fmt.Errorf("%w: notification %s version=%d expected=%d (re-read the snapshot before retrying)",
		ErrActionConflict, record.NotificationID, record.Version, expected)
}

func (s *LocalControlService) reload(ctx context.Context, previous *Notification) (*Notification, error) {
	if previous == nil {
		return nil, nil
	}
	fresh, err := s.store.GetNotification(ctx, previous.NotificationID)
	if err != nil || fresh == nil {
		return previous, nil
	}
	return fresh, nil
}

// notificationInScopes reports whether the notification belongs to one of the
// given root scopes (session id or team id) as its own root or its target
// parent. The check is deliberately permissive about which of the two matches:
// both are the notification's own scope, never a foreign one.
func notificationInScopes(record Notification, scopes []string) bool {
	if len(scopes) == 0 {
		return false
	}
	owned := func(value string) bool {
		value = strings.TrimSpace(value)
		if value == "" {
			return false
		}
		for _, scope := range scopes {
			if strings.TrimSpace(scope) == value {
				return true
			}
		}
		return false
	}
	return owned(record.RootScopeID) || owned(record.TargetParentSessionID) || owned(record.TargetParentTeamID)
}
