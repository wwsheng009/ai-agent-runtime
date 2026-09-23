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
	// ExtendBy / NewDeadline / ExtendWhich are the extend_deadline payload
	// (doc 6.5). Exactly one of ExtendBy / NewDeadline must be set; ExtendWhich
	// selects execution|progress|both (empty means execution).
	ExtendBy    time.Duration
	NewDeadline *time.Time
	ExtendWhich string
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
	// executorReady records whether a runtime executor able to perform mutation
	// actions is wired. Hosts read it through ExecutorReady() to declare their
	// control-action capability (P2-12 方案 1), so preflight never announces a
	// cancel/close this host could only record, never execute.
	executorReady bool
	// extensionLimits is the I5 budget applied to extend_deadline. Zero value
	// means "use the operator defaults", so an unwired service still enforces
	// the documented caps instead of extending without bound.
	extensionLimits ExtensionLimits
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
	} else {
		svc.executorReady = true
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
		s.executorReady = false
	} else {
		s.executorReady = true
	}
	s.executor = executor
}

// ExecutorReady reports whether a runtime executor able to perform mutation
// actions (cancel/close/cancel_subtree/retry/reassign) is wired. Read-only and
// bookkeeping actions (inspect/acknowledge/defer) do not depend on it.
//
// An executor that implements ExecutorReadiness answers for itself. Adapters
// that wrap an optional runtime hook are installed unconditionally (so
// bookkeeping actions and the failure wording stay identical to a wired host),
// yet must not look wired just because a value was passed: they report their
// real state through that interface.
func (s *ActionService) ExecutorReady() bool {
	if s == nil {
		return false
	}
	if probe, ok := s.executor.(ExecutorReadiness); ok {
		return probe.ExecutorReady()
	}
	return s.executorReady
}

// ExecutorReadiness lets an ActionExecutor declare whether it can perform
// mutation actions right now. Executors that do not implement it are treated as
// ready once wired, which is the default for plain adapters.
type ExecutorReadiness interface {
	ExecutorReady() bool
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
		ExtendBy:             req.ExtendBy,
		NewDeadline:          req.NewDeadline,
		ExtendWhich:          strings.TrimSpace(req.ExtendWhich),
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

	// extend_deadline is a control-plane mutation: it moves the ledger's own
	// deadlines, so it is executed here instead of through the host runtime
	// executor. Every other action keeps the host executor path.
	var result ActionResult
	var execErr error
	switch executing.Action {
	case ActionExtendDeadline:
		result, execErr = s.executeExtendDeadline(ctx, executing)
	case ActionTakeover:
		// takeover is a control-plane mutation too: it rewrites the ledger's
		// own ownership/fencing columns (§6.11), which no host executor can
		// reach.
		result, execErr = s.executeTakeover(ctx, executing)
	default:
		result, execErr = s.executor.Execute(ctx, executing)
	}

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
	case ActionCancel, ActionClose, ActionCancelSubtree, ActionRetry, ActionReassign, ActionExtendDeadline, ActionTakeover:
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
	case ActionInspect, ActionAcknowledge, ActionDefer, ActionCancel, ActionClose, ActionCancelSubtree, ActionRetry, ActionReassign, ActionExtendDeadline, ActionTakeover:
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
	if req.Action == ActionExtendDeadline {
		if err := validateExtendPayload(req); err != nil {
			return err
		}
	}
	return nil
}

// validateExtendPayload enforces the doc 6.5 request shape:
// {extend_by | new_deadline, extend_which: execution|progress|both}. The I5
// budget and the I6 irreversible-point checks need the ledger row, so they run
// at execute time (executeExtendDeadline) where the run is loaded under CAS.
func validateExtendPayload(req ActionRequest) error {
	hasExtendBy := req.ExtendBy != 0
	hasNewDeadline := req.NewDeadline != nil && !req.NewDeadline.IsZero()
	switch {
	case hasExtendBy && hasNewDeadline:
		return fmt.Errorf("%w: extend_deadline accepts extend_by or new_deadline, not both", ErrActionInvalid)
	case !hasExtendBy && !hasNewDeadline:
		return fmt.Errorf("%w: extend_deadline requires extend_by or new_deadline", ErrActionInvalid)
	case hasExtendBy && req.ExtendBy < 0:
		return fmt.Errorf("%w: extend_by must be positive", ErrActionInvalid)
	}
	if _, err := normalizeExtendWhich(req.ExtendWhich); err != nil {
		return err
	}
	return nil
}

// extendWhichExecution / extendWhichProgress / extendWhichBoth are the
// extend_which values (doc 6.5). An empty value means "execution", which keeps
// the common case (move the run's own deadline) short for the caller.
const (
	extendWhichExecution = "execution"
	extendWhichProgress  = "progress"
	extendWhichBoth      = "both"
)

func normalizeExtendWhich(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", extendWhichExecution:
		return extendWhichExecution, nil
	case extendWhichProgress:
		return extendWhichProgress, nil
	case extendWhichBoth:
		return extendWhichBoth, nil
	default:
		return "", fmt.Errorf("%w: unsupported extend_which %q (want execution|progress|both)", ErrActionInvalid, raw)
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

// --- extend_deadline (doc 6.5 / change #2) ---

const (
	// HintExtensionBudgetExhausted is the stable next_action hint for an
	// extension rejected by the I5 caps: the budget is spent, so the parent must
	// end or replace the obligation instead of extending it again.
	HintExtensionBudgetExhausted = "extend_budget_exhausted_use_cancel_or_reassign"
	// HintExtensionIrreversible is the stable next_action hint for an extension
	// rejected at an I6 irreversible point (cancel requested/canceling/terminal):
	// the run is already ending, so only retry/reassign can continue the work.
	HintExtensionIrreversible = "extend_rejected_irreversible_use_retry_or_reassign"
	// HintExtensionTargetMissing is the stable next_action hint when the
	// notification subject has no execution-run row to extend.
	HintExtensionTargetMissing = "extend_requires_agent_run_subject"
)

// ExtensionLimits is the I5 budget for extend_deadline (doc 6.5): at most
// MaxExtensions calls per obligation, each at most MaxExtensionPerCall × the
// original budget, and at most MaxExtensionTotal × the original budget in
// total. A non-positive value falls back to the operator default, so a
// partially wired service can never extend without bound.
type ExtensionLimits struct {
	MaxExtensions       int
	MaxExtensionPerCall float64
	MaxExtensionTotal   float64
}

// DefaultExtensionLimits returns the doc 6.5 / Q4 defaults (3 calls, 1× per
// call, 4× in total).
func DefaultExtensionLimits() ExtensionLimits {
	cfg := DefaultConfig()
	return ExtensionLimits{
		MaxExtensions:       cfg.MaxExtensions,
		MaxExtensionPerCall: cfg.MaxExtensionPerCall,
		MaxExtensionTotal:   cfg.MaxExtensionTotal,
	}
}

// ExtensionLimitsFromConfig maps the operator config onto the I5 budget so a
// host wires the same numbers the supervisor's ladder uses.
func ExtensionLimitsFromConfig(cfg Config) ExtensionLimits {
	resolved := cfg.WithDefaults()
	return ExtensionLimits{
		MaxExtensions:       resolved.MaxExtensions,
		MaxExtensionPerCall: resolved.MaxExtensionPerCall,
		MaxExtensionTotal:   resolved.MaxExtensionTotal,
	}
}

// SetExtensionLimits overrides the I5 budget (hosts call it during assembly
// with the operator config). Non-positive fields keep the defaults.
func (s *ActionService) SetExtensionLimits(limits ExtensionLimits) {
	if s == nil {
		return
	}
	s.extensionLimits = limits
}

func (s *ActionService) effectiveExtensionLimits() ExtensionLimits {
	defaults := DefaultExtensionLimits()
	if s == nil {
		return defaults
	}
	limits := s.extensionLimits
	if limits.MaxExtensions <= 0 {
		limits.MaxExtensions = defaults.MaxExtensions
	}
	if limits.MaxExtensionPerCall <= 0 {
		limits.MaxExtensionPerCall = defaults.MaxExtensionPerCall
	}
	if limits.MaxExtensionTotal <= 0 {
		limits.MaxExtensionTotal = defaults.MaxExtensionTotal
	}
	return limits
}

// executeExtendDeadline applies one extension: load the run, reject the I6
// irreversible points, enforce the I5 budget, CAS the new deadlines and
// counters, then project the visible "已延长 ×N, +时长" lifecycle event.
func (s *ActionService) executeExtendDeadline(ctx context.Context, record ActionRecord) (ActionResult, error) {
	run, err := s.loadExtensionRun(ctx, record)
	if err != nil {
		return ActionResult{}, err
	}
	now := s.now().UTC()
	if err := ensureExtendable(*run); err != nil {
		return ActionResult{}, err
	}
	// §6.11 ownership boundary: while another session holds a live owner lease
	// on this run, extending it is a cross-owner mutation and must go through
	// the explicit, audited takeover action first.
	if err := ensureRunMutationAuthorized(run, record.RequestedByID, string(ActionExtendDeadline), now); err != nil {
		return ActionResult{}, err
	}
	which, err := normalizeExtendWhich(record.ExtendWhich)
	if err != nil {
		return ActionResult{}, err
	}

	limits := s.effectiveExtensionLimits()
	if run.ExtensionCount >= limits.MaxExtensions {
		return ActionResult{}, fmt.Errorf(
			"%w: extension budget exhausted for run %s (calls=%d/%d); next_action=%s",
			ErrActionInvalid, run.RunID, run.ExtensionCount, limits.MaxExtensions, HintExtensionBudgetExhausted)
	}
	budget := extensionBudget(*run)

	updated := *run
	var deltas []time.Duration
	var parts []string
	if which == extendWhichExecution || which == extendWhichBoth {
		target, delta, err := extensionTarget(run.ExecutionDeadlineAt, now, record.ExtendBy, record.NewDeadline)
		if err != nil {
			return ActionResult{}, err
		}
		updated.ExecutionDeadlineAt = &target
		deltas = append(deltas, delta)
		parts = append(parts, "execution_deadline_at="+target.Format(time.RFC3339))
	}
	if which == extendWhichProgress || which == extendWhichBoth {
		target, delta, err := extensionTarget(run.ProgressDeadlineAt, now, record.ExtendBy, record.NewDeadline)
		if err != nil {
			return ActionResult{}, err
		}
		updated.ProgressDeadlineAt = &target
		deltas = append(deltas, delta)
		parts = append(parts, "progress_deadline_at="+target.Format(time.RFC3339))
	}
	applied := maxDuration(deltas)
	if err := enforceExtensionCaps(limits, budget, run.ExtendedTotal, applied); err != nil {
		return ActionResult{}, err
	}

	updated.ExtensionCount = run.ExtensionCount + 1
	updated.ExtendedTotal = run.ExtendedTotal + applied
	// The parent decided: the escalate-first window for this episode is spent,
	// so a later stall escalates afresh instead of firing the fallback for a
	// decision that was already taken (I8).
	updated.DecisionWindowUntil = nil

	runs, err := s.executionRunStore()
	if err != nil {
		return ActionResult{}, err
	}
	ok, err := runs.UpdateExecutionRunCAS(ctx, updated, run.Version)
	if err != nil {
		// A concurrent writer (another extension, or the scanner judging the
		// run) won the row: the caller must re-read instead of blindly
		// retrying, so the run-level conflict surfaces as the action-level
		// conflict sentinel (EC-B1).
		if errors.Is(err, ErrRunConflict) {
			return ActionResult{}, fmt.Errorf("%w: run %s changed while extending", ErrActionConflict, run.RunID)
		}
		return ActionResult{}, fmt.Errorf("extend run %s: %w", run.RunID, err)
	}
	if !ok {
		return ActionResult{}, fmt.Errorf("%w: run %s changed while extending", ErrActionConflict, run.RunID)
	}

	result := "extended " + which + " deadline by " + applied.String() +
		" (已延长 ×" + formatInt(int64(updated.ExtensionCount)) + ", +" + updated.ExtendedTotal.String() + ")"
	if err := s.projectExtension(ctx, updated, applied, result, record); err != nil {
		return ActionResult{}, err
	}
	return ActionResult{Status: ActionCompleted, Result: result, ResultDetail: strings.Join(parts, " ")}, nil
}

// loadExtensionRun resolves the action's target to the execution-run row the
// extension mutates (see loadSubjectRun).
func (s *ActionService) loadExtensionRun(ctx context.Context, record ActionRecord) (*ExecutionRun, error) {
	return s.loadSubjectRun(ctx, record, string(ActionExtendDeadline), HintExtensionTargetMissing)
}

// loadTakeoverRun resolves the action's target to the run row takeover claims.
func (s *ActionService) loadTakeoverRun(ctx context.Context, record ActionRecord) (*ExecutionRun, error) {
	return s.loadSubjectRun(ctx, record, string(ActionTakeover), HintTakeoverTargetMissing)
}

// loadSubjectRun resolves an action's target to the execution-run row it
// mutates. Only agent_run subjects own a run ledger, so a session or team
// target is rejected with the actionable hint instead of silently mutating
// nothing.
func (s *ActionService) loadSubjectRun(ctx context.Context, record ActionRecord, action, hint string) (*ExecutionRun, error) {
	kind := SubjectKind(strings.TrimSpace(string(record.TargetKind)))
	if kind != SubjectAgentRun {
		return nil, fmt.Errorf("%w: %s targets agent_run, got %q; next_action=%s",
			ErrActionInvalid, action, record.TargetKind, hint)
	}
	runs, err := s.executionRunStore()
	if err != nil {
		return nil, err
	}
	run, err := runs.GetExecutionRun(ctx, strings.TrimSpace(record.TargetID))
	if err != nil {
		if errors.Is(err, ErrRunNotFound) {
			return nil, fmt.Errorf("%w: run %s not found; next_action=%s", ErrActionInvalid, record.TargetID, hint)
		}
		return nil, fmt.Errorf("load run %s: %w", record.TargetID, err)
	}
	if run == nil {
		return nil, fmt.Errorf("%w: run %s not found; next_action=%s", ErrActionInvalid, record.TargetID, hint)
	}
	return run, nil
}

// --- takeover: the §6.11 ownership override (change #16) ---

const (
	// HintOwnerLeaseHeld is the stable next_action hint for a mutation rejected
	// because another session holds the run's live owner lease: the caller must
	// take ownership explicitly (takeover) instead of writing behind the
	// owner's back.
	HintOwnerLeaseHeld = "owner_lease_held_use_takeover"
	// HintTakeoverTargetMissing is the takeover counterpart of
	// HintExtensionTargetMissing.
	HintTakeoverTargetMissing = "takeover_requires_agent_run_subject"
	// HintTakeoverTerminal is the stable next_action hint for a takeover on a
	// run that already reached a terminal state: there is no live obligation
	// left to own.
	HintTakeoverTerminal = "takeover_requires_live_run"
	// HintTakeoverNotNeeded is the stable next_action hint when the acting
	// session already owns the run with a live lease, so the mutation can be
	// issued directly.
	HintTakeoverNotNeeded = "already_owner_issue_action_directly"
)

// takeoverLease is the lease a takeover establishes (§6.11: ownership is
// renewed while the parent turn runs). It reuses the heartbeat granularity so a
// taken-over run is re-checked at the same cadence as any other live run.
func takeoverLease() time.Duration {
	return DefaultConfig().HeartbeatTimeout
}

// ensureRunMutationAuthorized enforces the §6.11 ownership boundary for a
// mutation on a ledger row: while another session holds a live owner lease,
// this session must take ownership explicitly (takeover) instead of mutating
// the row behind the owner's back — the double write the fencing token exists
// to stop (EC-B9). An absent or expired lease is not a live competing owner:
// §6.11 deliberately stops renewing the lease while a turn is suspended, so a
// stale owner must not block the surviving session forever.
func ensureRunMutationAuthorized(run *ExecutionRun, actorID, action string, now time.Time) error {
	if run == nil {
		return nil
	}
	owner := strings.TrimSpace(run.OwnerID)
	actor := strings.TrimSpace(actorID)
	if owner == "" || owner == actor {
		return nil
	}
	if run.OwnerLeaseUntil == nil || !run.OwnerLeaseUntil.After(now) {
		return nil
	}
	return fmt.Errorf("%w: %s requires ownership of run %s (owner=%s lease_until=%s, actor=%s); next_action=%s",
		ErrActionNotAllowed, action, run.RunID, owner,
		run.OwnerLeaseUntil.UTC().Format(time.RFC3339), actor, HintOwnerLeaseHeld)
}

// executeTakeover performs the explicit ownership override: it claims the run
// for the acting session, advances the fencing token (so a write the previous
// owner still has in flight is rejected by its own CAS) and writes the audit
// trail — ActorID is the action's requested_by_id and the reason is the
// request's reason (I4), both persisted on the durable action record and
// projected onto the parent-visible lifecycle event.
func (s *ActionService) executeTakeover(ctx context.Context, record ActionRecord) (ActionResult, error) {
	run, err := s.loadTakeoverRun(ctx, record)
	if err != nil {
		return ActionResult{}, err
	}
	now := s.now().UTC()
	if run.Terminal() {
		return ActionResult{}, fmt.Errorf("%w: run %s is %s, not a live obligation; next_action=%s",
			ErrActionInvalid, run.RunID, run.Status, HintTakeoverTerminal)
	}
	actor := strings.TrimSpace(record.RequestedByID)
	if strings.TrimSpace(run.OwnerID) == actor && run.OwnerLeaseUntil != nil && run.OwnerLeaseUntil.After(now) {
		return ActionResult{}, fmt.Errorf("%w: run %s is already owned by %s with a live lease; next_action=%s",
			ErrActionInvalid, run.RunID, actor, HintTakeoverNotNeeded)
	}
	runs, err := s.executionRunStore()
	if err != nil {
		return ActionResult{}, err
	}
	// CAS on the token observed here: if another takeover won the row in
	// between, this one loses instead of silently overwriting its result.
	ok, err := runs.TakeoverExecutionRun(ctx, run.RunID, actor, run.FencingToken, takeoverLease(), now)
	if err != nil {
		return ActionResult{}, fmt.Errorf("takeover run %s: %w", run.RunID, err)
	}
	if !ok {
		return ActionResult{}, fmt.Errorf("%w: run %s changed while taking over; re-read and retry", ErrActionConflict, run.RunID)
	}
	claimed, err := runs.GetExecutionRun(ctx, run.RunID)
	if err != nil {
		return ActionResult{}, fmt.Errorf("reload run %s after takeover: %w", run.RunID, err)
	}
	if claimed == nil {
		return ActionResult{}, fmt.Errorf("%w: run %s vanished during takeover", ErrActionConflict, run.RunID)
	}
	previous := strings.TrimSpace(run.OwnerID)
	if previous == "" {
		previous = "(none)"
	}
	result := "takeover: owner " + previous + " -> " + actor +
		" (fencing_token=" + formatInt(claimed.FencingToken) + ")"
	if err := s.projectTakeover(ctx, *claimed, previous, actor, record); err != nil {
		return ActionResult{}, err
	}
	return ActionResult{
		Status:       ActionCompleted,
		Result:       result,
		ResultDetail: "owner_id=" + actor + " fencing_token=" + formatInt(claimed.FencingToken),
	}, nil
}

// projectTakeover writes the durable, parent-visible ownership event so the
// digest shows that the obligation changed hands and by whose decision.
func (s *ActionService) projectTakeover(ctx context.Context, run ExecutionRun, previousOwner, actor string, record ActionRecord) error {
	reason := "takeover: owner " + previousOwner + " -> " + actor + " (actor=" + actor + ")"
	if trimmed := strings.TrimSpace(record.Reason); trimmed != "" {
		reason += "; reason=" + trimmed
	}
	_, err := ProjectLifecycle(ctx, s.store, nil, LifecycleProjection{
		RootScopeID:           run.RootSessionID,
		TargetParentSessionID: run.ParentSessionID,
		SubjectKind:           SubjectAgentRun,
		SubjectID:             run.RunID,
		SubjectVersion:        run.Version,
		EventType:             "obligation.ownership.taken_over",
		Severity:              SeverityWarning,
		SupervisionState:      SupervisionRunning,
		Reason:                reason,
		RecommendedAction:     string(ActionInspect),
	})
	if err != nil {
		return fmt.Errorf("project takeover event for run %s: %w", run.RunID, err)
	}
	return nil
}

// executionRunStore exposes the execution-run ledger behind the action store.
// The plain Store contract does not include run rows, so a host that persists
// actions without a run ledger gets an explicit rejection instead of a panic.
func (s *ActionService) executionRunStore() (ExecutionRunStore, error) {
	if s == nil || s.store == nil {
		return nil, fmt.Errorf("%w: supervision store is not configured; next_action=%s",
			ErrActionInvalid, HintExtensionTargetMissing)
	}
	runs, ok := s.store.(ExecutionRunStore)
	if !ok || runs == nil {
		return nil, fmt.Errorf("%w: execution-run ledger is not available on this host; next_action=%s",
			ErrActionInvalid, HintExtensionTargetMissing)
	}
	return runs, nil
}

// ensureExtendable enforces I6: once a cancel is in flight or the run reached a
// terminal state, the extension is refused outright (the deadline no longer
// governs anything) with the retry/reassign hint from EC-B3.
func ensureExtendable(run ExecutionRun) error {
	if run.Terminal() {
		return fmt.Errorf("%w: run %s is terminal (%s); next_action=%s",
			ErrActionInvalid, run.RunID, run.Status, HintExtensionIrreversible)
	}
	status := strings.TrimSpace(run.Status)
	if status == RunStatusCancelRequested || status == RunStatusCanceling ||
		(run.CancelRequestedAt != nil && !run.CancelRequestedAt.IsZero()) {
		return fmt.Errorf("%w: run %s is past the irreversible point (status=%s); next_action=%s",
			ErrActionInvalid, run.RunID, status, HintExtensionIrreversible)
	}
	return nil
}

// extensionTarget computes the new absolute deadline and the delta the I5
// accounting must charge. The base is the current deadline, or "now" when the
// deadline is missing or already in the past: an extension always grants the
// requested forward window instead of leaving a past deadline in place.
func extensionTarget(current *time.Time, now time.Time, extendBy time.Duration, newDeadline *time.Time) (time.Time, time.Duration, error) {
	base := now
	if current != nil && !current.IsZero() && current.After(now) {
		base = *current
	}
	if newDeadline != nil && !newDeadline.IsZero() {
		target := newDeadline.UTC()
		if !target.After(base) {
			return time.Time{}, 0, fmt.Errorf("%w: new_deadline must move the deadline forward (base=%s)",
				ErrActionInvalid, base.Format(time.RFC3339))
		}
		return target, target.Sub(base), nil
	}
	if extendBy <= 0 {
		return time.Time{}, 0, fmt.Errorf("%w: extend_by must be positive", ErrActionInvalid)
	}
	return base.Add(extendBy), extendBy, nil
}

// extensionBudget is the obligation's original budget used by the I5 ratios:
// the declared dispatch budget when present, otherwise the originally computed
// deadline span (current span minus what previous extensions already added).
// Zero means "not derivable", in which case only the call-count cap applies.
func extensionBudget(run ExecutionRun) time.Duration {
	if run.DeclaredBudget > 0 {
		return run.DeclaredBudget
	}
	if run.ExecutionDeadlineAt == nil || run.ExecutionDeadlineAt.IsZero() || run.StartedAt.IsZero() {
		return 0
	}
	span := run.ExecutionDeadlineAt.Sub(run.StartedAt) - run.ExtendedTotal
	if span <= 0 {
		return 0
	}
	return span
}

func enforceExtensionCaps(limits ExtensionLimits, budget, alreadyExtended, applied time.Duration) error {
	if budget <= 0 || applied <= 0 {
		return nil
	}
	if maxPerCall := time.Duration(limits.MaxExtensionPerCall * float64(budget)); maxPerCall > 0 && applied > maxPerCall {
		return fmt.Errorf(
			"%w: extend_by %s exceeds the per-call cap %s (%.1f× of the %s budget); next_action=%s",
			ErrActionInvalid, applied, maxPerCall, limits.MaxExtensionPerCall, budget, HintExtensionBudgetExhausted)
	}
	if maxTotal := time.Duration(limits.MaxExtensionTotal * float64(budget)); maxTotal > 0 && alreadyExtended+applied > maxTotal {
		return fmt.Errorf(
			"%w: extension %s exceeds the total cap %s (%.1f× of the %s budget, already extended %s); next_action=%s",
			ErrActionInvalid, applied, maxTotal, limits.MaxExtensionTotal, budget, alreadyExtended, HintExtensionBudgetExhausted)
	}
	return nil
}

// projectExtension writes the durable, parent-visible extension event
// (doc 6.8: obligation.deadline.extended carries the new deadline, the reason
// and the extension counter) so the digest and UI can show "已延长 ×N, +时长".
func (s *ActionService) projectExtension(ctx context.Context, run ExecutionRun, applied time.Duration, detail string, record ActionRecord) error {
	reason := "deadline extended (已延长 ×" + formatInt(int64(run.ExtensionCount)) + ", +" + run.ExtendedTotal.String() + "): " + detail
	if trimmed := strings.TrimSpace(record.Reason); trimmed != "" {
		reason += "; reason=" + trimmed
	}
	_, err := ProjectLifecycle(ctx, s.store, nil, LifecycleProjection{
		RootScopeID:           run.RootSessionID,
		TargetParentSessionID: run.ParentSessionID,
		SubjectKind:           SubjectAgentRun,
		SubjectID:             run.RunID,
		SubjectVersion:        run.Version,
		EventType:             "obligation.deadline.extended",
		Severity:              SeverityInfo,
		SupervisionState:      SupervisionRunning,
		Reason:                reason,
		RecommendedAction:     string(ActionInspect),
	})
	if err != nil {
		return fmt.Errorf("project extension event for run %s: %w", run.RunID, err)
	}
	return nil
}

func maxDuration(values []time.Duration) time.Duration {
	var max time.Duration
	for _, value := range values {
		if value > max {
			max = value
		}
	}
	return max
}
