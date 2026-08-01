package supervision

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Action-related sentinel errors (doc 6.6 constraints 1-2).
var (
	// ErrActionConflict is returned when expected_version no longer matches
	// (state changed concurrently). Callers must re-read the snapshot instead
	// of blindly retrying the mutation.
	ErrActionConflict = errors.New("supervision: action conflict: state changed")
	// ErrActionNotAllowed is returned when the requested action is not in the
	// server-computed allowed_actions for the target's current state.
	ErrActionNotAllowed = errors.New("supervision: action not allowed for current state")
	// ErrActionInvalid is returned for structurally invalid requests.
	ErrActionInvalid = errors.New("supervision: invalid action request")
	// ErrActionNotFound is returned when the action record does not exist.
	ErrActionNotFound = errors.New("supervision: action not found")
	// ErrActionNotActionable is returned when a state transition is not
	// permitted (e.g. executing a rejected action).
	ErrActionNotActionable = errors.New("supervision: action not actionable in current status")
)

// NodeActionResult is one node's outcome in a cascade action
// (doc 6.6 cancel_subtree: per-node results, partially_completed on partial
// failure).
type NodeActionResult struct {
	Kind   SubjectKind  `json:"kind,omitempty"`
	ID     string       `json:"id,omitempty"`
	Status ActionStatus `json:"status,omitempty"`
	Result string       `json:"result,omitempty"`
}

// ActionResult is the terminal outcome returned by an ActionExecutor.
type ActionResult struct {
	Status       ActionStatus       `json:"status,omitempty"`
	Result       string             `json:"result,omitempty"`
	ResultDetail string             `json:"result_detail,omitempty"`
	NodeResults  []NodeActionResult `json:"node_results,omitempty"`
}

// ActionExecutor executes a concrete control action against the runtime
// (agentcontrol, team orchestrator or other executor). The supervision
// package stays decoupled from those packages by receiving an implementation.
type ActionExecutor interface {
	// Execute performs the side effects for an accepted action and returns
	// the terminal status (completed / partially_completed / failed).
	Execute(ctx context.Context, a ActionRecord) (ActionResult, error)
}

// ScopeAuthorizer verifies that the requester may control the target within
// the given root scope (doc 6.6 constraint 6: parent/lead only controls its
// own root scope; cross-team control needs explicit authorization).
type ScopeAuthorizer interface {
	Authorize(ctx context.Context, rootScopeID, requestedByKind, requestedByID, targetKind, targetID string) error
}

// ActionRequest is the caller-facing input to RequestAction. The service
// re-validates allowed_actions server-side; passing an action name never
// implies authorization (doc 6.6 constraint 2).
type ActionRequest struct {
	RootScopeID          string
	RequestedByKind      string
	RequestedByID        string
	TargetKind           SubjectKind
	TargetID             string
	Action               ActionKind
	CascadeMode          CascadeMode
	Reason               string
	ExpectedVersion      int64
	ExpectedFencingToken string
}

// ActionService is the durable control plane entry (doc 6.6). Every request,
// acceptance, execution and result is persisted; status transitions are
// CAS-guarded.
type ActionService struct {
	store      Store
	evaluator  Evaluator
	executor   ActionExecutor
	authorizer ScopeAuthorizer
	now        func() time.Time
}

// NewActionService wires the durable action service. executor is required for
// mutation actions; authorizer may be nil (then no scope authorization is
// enforced, which is only acceptable for trusted internal callers).
func NewActionService(store Store, executor ActionExecutor, authorizer ScopeAuthorizer) *ActionService {
	svc := &ActionService{
		store:      store,
		evaluator:  Evaluator{},
		executor:   executor,
		authorizer: authorizer,
		now:        timeNow,
	}
	if executor == nil {
		svc.executor = noopExecutor{}
	}
	return svc
}

// SetExecutor replaces the runtime executor after construction. Hosts that
// assemble the executor only after the control plane exists (e.g. because the
// close adapter needs the session hub) call this once during startup; it is
// not intended for concurrent use.
func (s *ActionService) SetExecutor(executor ActionExecutor) {
	if executor == nil {
		executor = noopExecutor{}
	}
	s.executor = executor
}

// noopExecutor rejects executions when no runtime executor is wired.
type noopExecutor struct{}

func (noopExecutor) Execute(context.Context, ActionRecord) (ActionResult, error) {
	return ActionResult{Status: ActionFailed, Result: "no executor configured"}, nil
}

// RequestAction validates, authorizes, CAS-checks and persists a control
// action request (status requested). It never executes side effects.
func (s *ActionService) RequestAction(ctx context.Context, req ActionRequest) (ActionRecord, error) {
	if err := validateActionRequest(req); err != nil {
		return ActionRecord{}, err
	}
	if s.authorizer != nil {
		if err := s.authorizer.Authorize(ctx, req.RootScopeID, req.RequestedByKind, req.RequestedByID, string(req.TargetKind), req.TargetID); err != nil {
			return ActionRecord{}, fmt.Errorf("%w: %v", ErrActionNotAllowed, err)
		}
	}

	// Server-side allowed-actions re-validation (doc 6.6 constraint 2).
	latest, err := s.latestNotification(ctx, req.RootScopeID, req.TargetKind, req.TargetID)
	if err != nil {
		return ActionRecord{}, err
	}
	if latest != nil {
		if req.ExpectedVersion > 0 && latest.Version != req.ExpectedVersion {
			return ActionRecord{}, fmt.Errorf("%w: target version=%d expected=%d", ErrActionConflict, latest.Version, req.ExpectedVersion)
		}
		if req.ExpectedFencingToken != "" && latest.DiagnosticRef != req.ExpectedFencingToken {
			return ActionRecord{}, fmt.Errorf("%w: fencing token mismatch", ErrActionConflict)
		}
		allowed := s.evaluator.EvaluateAllowedActions(*latest)
		if !containsString(allowed, string(req.Action)) {
			return ActionRecord{}, fmt.Errorf("%w: action=%s allowed=[%s]", ErrActionNotAllowed, req.Action, strings.Join(allowed, ","))
		}
	} else {
		// No durable notification: only read-only inspect is permitted, or
		// acknowledge/defer on an empty state is rejected.
		if req.Action != ActionInspect {
			return ActionRecord{}, fmt.Errorf("%w: no lifecycle record for target", ErrActionNotAllowed)
		}
	}

	now := s.now().UTC()
	record := ActionRecord{
		ActionID:             "act_" + uuid.NewString(),
		RootScopeID:          strings.TrimSpace(req.RootScopeID),
		RequestedByKind:      strings.TrimSpace(req.RequestedByKind),
		RequestedByID:        strings.TrimSpace(req.RequestedByID),
		TargetKind:           req.TargetKind,
		TargetID:             strings.TrimSpace(req.TargetID),
		Action:               req.Action,
		CascadeMode:          req.CascadeMode,
		Reason:               strings.TrimSpace(req.Reason),
		ExpectedVersion:      req.ExpectedVersion,
		ExpectedFencingToken: req.ExpectedFencingToken,
		Status:               ActionRequested,
		CreatedAt:            now,
		Version:              1,
	}
	return s.store.CreateAction(ctx, record)
}

// GetAction returns a durable action record.
func (s *ActionService) GetAction(ctx context.Context, actionID string) (*ActionRecord, error) {
	record, err := s.store.GetAction(ctx, strings.TrimSpace(actionID))
	if err != nil {
		return nil, err
	}
	if record == nil {
		return nil, ErrActionNotFound
	}
	return record, nil
}

// ListActions returns durable actions matching the filter.
func (s *ActionService) ListActions(ctx context.Context, filter ActionFilter) ([]ActionRecord, error) {
	return s.store.ListActions(ctx, filter)
}

// AcceptAction moves requested -> accepted (CAS). For cascade operations it
// first marks the root as canceling/closing (doc 6.6 constraint 3: prevent
// concurrent descendant spawns before propagation).
func (s *ActionService) AcceptAction(ctx context.Context, actionID string) (ActionRecord, error) {
	record, err := s.store.GetAction(ctx, strings.TrimSpace(actionID))
	if err != nil {
		return ActionRecord{}, err
	}
	if record == nil {
		return ActionRecord{}, ErrActionNotFound
	}
	if record.Status != ActionRequested {
		return ActionRecord{}, fmt.Errorf("%w: status=%s", ErrActionNotActionable, record.Status)
	}
	now := s.now().UTC()
	accepted := *record
	accepted.Status = ActionAccepted
	accepted.StartedAt = &now
	accepted.Version = record.Version
	ok, err := s.store.UpdateActionStatus(ctx, accepted, record.Version)
	if err != nil {
		return ActionRecord{}, err
	}
	if !ok {
		return ActionRecord{}, ErrActionConflict
	}

	// Cascade guard: freeze the root before touching descendants.
	if record.CascadeMode == CascadeDescendants {
		if err := s.markRootTransitioning(ctx, *record); err != nil {
			return ActionRecord{}, err
		}
	}
	return accepted, nil
}

// ExecuteAction runs the executor and durably records the terminal outcome
// (doc 6.6 constraint 7: close/cancel must produce a resolution notification,
// never just a tool return value).
func (s *ActionService) ExecuteAction(ctx context.Context, actionID string) (ActionRecord, error) {
	record, err := s.store.GetAction(ctx, strings.TrimSpace(actionID))
	if err != nil {
		return ActionRecord{}, err
	}
	if record == nil {
		return ActionRecord{}, ErrActionNotFound
	}
	if record.Status != ActionAccepted && record.Status != ActionRequested {
		return ActionRecord{}, fmt.Errorf("%w: status=%s", ErrActionNotActionable, record.Status)
	}

	now := s.now().UTC()
	executing := *record
	executing.Status = ActionExecuting
	executing.StartedAt = &now
	executing.Version = record.Version
	ok, err := s.store.UpdateActionStatus(ctx, executing, record.Version)
	if err != nil {
		return ActionRecord{}, err
	}
	if !ok {
		return ActionRecord{}, ErrActionConflict
	}

	// The first CAS bumped version; re-read so the terminal transition uses
	// the fresh version instead of the stale pre-executing one.
	fresh, err := s.store.GetAction(ctx, record.ActionID)
	if err != nil {
		return ActionRecord{}, err
	}
	if fresh == nil {
		return ActionRecord{}, ErrActionNotFound
	}

	result, execErr := s.executor.Execute(ctx, executing)

	terminal := *fresh
	terminal.FinishedAt = s.ptrTime(s.now().UTC())
	switch {
	case execErr != nil:
		terminal.Status = ActionFailed
		terminal.Result = execErr.Error()
	case result.Status == "":
		terminal.Status = ActionCompleted
		terminal.Result = result.Result
	default:
		terminal.Status = result.Status
		terminal.Result = result.Result
		terminal.ResultDetail = result.ResultDetail
	}
	ok, err = s.store.UpdateActionStatus(ctx, terminal, fresh.Version)
	if err != nil {
		return ActionRecord{}, err
	}
	if !ok {
		return ActionRecord{}, ErrActionConflict
	}

	// Rule 7: every mutation produces a terminal lifecycle record. Failed and
	// rejected outcomes deliberately leave the source notification unresolved,
	// while completed/partial outcomes close it through the same helper.
	if isMutationAction(terminal.Action) {
		if err := s.emitResolutionNotification(ctx, terminal, result.NodeResults); err != nil {
			return ActionRecord{}, err
		}
	}
	return terminal, nil
}

// RejectAction records a rejection (e.g. validation failed inside executor).
func (s *ActionService) RejectAction(ctx context.Context, actionID, reason string) (ActionRecord, error) {
	record, err := s.store.GetAction(ctx, strings.TrimSpace(actionID))
	if err != nil {
		return ActionRecord{}, err
	}
	if record == nil {
		return ActionRecord{}, ErrActionNotFound
	}
	now := s.now().UTC()
	rejected := *record
	rejected.Status = ActionRejected
	rejected.Result = reason
	rejected.FinishedAt = &now
	rejected.Version = record.Version
	ok, err := s.store.UpdateActionStatus(ctx, rejected, record.Version)
	if err != nil {
		return ActionRecord{}, err
	}
	if !ok {
		return ActionRecord{}, ErrActionConflict
	}
	if isMutationAction(rejected.Action) {
		if err := s.emitResolutionNotification(ctx, rejected, nil); err != nil {
			return ActionRecord{}, err
		}
	}
	return rejected, nil
}

// latestNotification finds the highest-event_seq notification for a target.
func (s *ActionService) latestNotification(ctx context.Context, rootScopeID string, kind SubjectKind, id string) (*Notification, error) {
	list, err := s.store.ListNotifications(ctx, NotificationFilter{
		RootScopeID:     rootScopeID,
		SubjectKind:     kind,
		SubjectID:       id,
		IncludeResolved: true,
	})
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, nil
	}
	latest := list[0]
	for _, n := range list[1:] {
		if n.EventSeq > latest.EventSeq {
			latest = n
		}
	}
	return &latest, nil
}

// markRootTransitioning freezes a subtree root before cascade propagation:
// it refreshes the root notification with canceling/closing state, which
// bumps version and therefore invalidates stale parent mutation requests.
func (s *ActionService) markRootTransitioning(ctx context.Context, record ActionRecord) error {
	latest, err := s.latestNotification(ctx, record.RootScopeID, record.TargetKind, record.TargetID)
	if err != nil {
		return err
	}
	state := SupervisionCanceling
	if record.Action == ActionClose {
		state = SupervisionTerminated
	}
	if latest == nil {
		latest = &Notification{
			NotificationID:    "notif_" + uuid.NewString(),
			RootScopeID:       record.RootScopeID,
			SubjectKind:       record.TargetKind,
			SubjectID:         record.TargetID,
			SubjectVersion:    0,
			EventType:         "cascade_fence",
			Severity:          SeverityWarning,
			SupervisionState:  state,
			Reason:            "cascade " + string(record.Action) + " accepted; spawning frozen",
			DecisionState:     DecisionActioned,
			ResolutionState:   ResolutionUnresolved,
			RecommendedAction: "inspect_cancel_result",
			AllowedActions:    []string{string(ActionInspect)},
			AutoActionID:      record.ActionID,
		}
	} else {
		latest.SupervisionState = state
		latest.Reason = "cascade " + string(record.Action) + " accepted; spawning frozen: " + record.Reason
		latest.DecisionState = DecisionActioned
		latest.AutoActionID = record.ActionID
		latest.RecommendedAction = "inspect_cancel_result"
		latest.AllowedActions = []string{string(ActionInspect)}
	}
	_, err = s.store.UpsertNotification(ctx, *latest)
	return err
}

// emitResolutionNotification closes the loop (doc 6.6 constraint 7 / doc 6.3
// rule 8): the parent/lead learns the final result of cancel/close/retry.
func (s *ActionService) emitResolutionNotification(ctx context.Context, record ActionRecord, nodeResults []NodeActionResult) error {
	now := s.now().UTC()
	resolution := ResolutionClosed
	if record.Status == ActionPartiallyCompleted {
		resolution = ResolutionFailed
	}
	if record.Status == ActionFailed || record.Status == ActionRejected {
		resolution = ResolutionUnresolved
	}
	detail := record.Result
	if len(nodeResults) > 0 {
		var parts []string
		for _, nr := range nodeResults {
			parts = append(parts, string(nr.Kind)+":"+nr.ID+"="+string(nr.Status))
		}
		if detail == "" {
			detail = strings.Join(parts, ",")
		} else {
			detail += " [" + strings.Join(parts, ",") + "]"
		}
	}
	notification := Notification{
		NotificationID:    "notif_" + uuid.NewString(),
		RootScopeID:       record.RootScopeID,
		SubjectKind:       record.TargetKind,
		SubjectID:         record.TargetID,
		SubjectVersion:    record.ExpectedVersion,
		EventType:         "action_" + string(record.Action) + "_resolution",
		Severity:          SeverityInfo,
		SupervisionState:  SupervisionRecovered,
		Reason:            "action " + string(record.Action) + " " + string(record.Status) + ": " + detail,
		RecommendedAction: string(ActionInspect),
		AllowedActions:    []string{string(ActionInspect)},
		AutoActionID:      record.ActionID,
		DecisionState:     DecisionActioned,
		ResolutionState:   resolution,
	}
	if resolution == ResolutionUnresolved {
		notification.Severity = SeverityWarning
		notification.SupervisionState = SupervisionBlocked
		notification.RecommendedAction = string(ActionInspect)
		notification.DecisionState = DecisionUnacknowledged
	}
	if _, err := s.store.UpsertNotification(ctx, notification); err != nil {
		return err
	}
	// Resolve the original notification only after an actual successful or
	// partial terminal outcome. Failed/rejected actions must keep the source
	// visible for the next parent decision.
	if resolution == ResolutionUnresolved {
		return nil
	}
	notifications, err := s.store.ListNotifications(ctx, NotificationFilter{
		RootScopeID:     record.RootScopeID,
		SubjectKind:     record.TargetKind,
		SubjectID:       record.TargetID,
		IncludeResolved: true,
	})
	if err != nil {
		return err
	}
	for _, existing := range notifications {
		if existing.NotificationID == notification.NotificationID || existing.ResolutionState != ResolutionUnresolved {
			continue
		}
		if _, err := s.store.ResolveNotification(ctx, existing.NotificationID, resolution, now, existing.Version); err != nil {
			return err
		}
	}
	return nil
}

func isMutationAction(action ActionKind) bool {
	switch action {
	case ActionCancel, ActionClose, ActionCancelSubtree, ActionRetry, ActionReassign:
		return true
	default:
		return false
	}
}

func (s *ActionService) ptrTime(t time.Time) *time.Time {
	return &t
}

func validateActionRequest(req ActionRequest) error {
	if strings.TrimSpace(req.RootScopeID) == "" {
		return fmt.Errorf("%w: root_scope_id is required", ErrActionInvalid)
	}
	if strings.TrimSpace(req.RequestedByID) == "" {
		return fmt.Errorf("%w: requested_by_id is required", ErrActionInvalid)
	}
	if req.TargetKind == "" || strings.TrimSpace(req.TargetID) == "" {
		return fmt.Errorf("%w: target_kind and target_id are required", ErrActionInvalid)
	}
	switch req.Action {
	case ActionInspect, ActionAcknowledge, ActionDefer, ActionCancel, ActionClose, ActionCancelSubtree, ActionRetry, ActionReassign:
	default:
		return fmt.Errorf("%w: unsupported action %q", ErrActionInvalid, req.Action)
	}
	if req.Action != ActionInspect && req.Action != ActionAcknowledge && strings.TrimSpace(req.Reason) == "" {
		return fmt.Errorf("%w: reason is required for action %q", ErrActionInvalid, req.Action)
	}
	if req.CascadeMode == "" {
		req.CascadeMode = CascadeNone
	}
	switch req.CascadeMode {
	case CascadeNone, CascadeDescendants:
	default:
		return fmt.Errorf("%w: unsupported cascade_mode %q", ErrActionInvalid, req.CascadeMode)
	}
	if req.CascadeMode == CascadeDescendants && req.Action != ActionCancelSubtree && req.Action != ActionClose && req.Action != ActionRetry {
		return fmt.Errorf("%w: cascade requires cancel_subtree/close/retry", ErrActionInvalid)
	}
	return nil
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
