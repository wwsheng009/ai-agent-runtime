package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

// Supervision store wiring follows the team store pattern: the runtime server
// (or tests) calls SetSupervisionStore/... once configured; every handler
// degrades to 503 until then.

// SetSupervisionStore sets the durable supervision control-plane store.
func (h *Handler) SetSupervisionStore(store supervision.Store) {
	if h == nil {
		return
	}
	h.supervisionStoreMu.Lock()
	h.supervisionStore = store
	h.supervisionStoreMu.Unlock()
}

// SetSupervisionActionService sets the durable action service.
func (h *Handler) SetSupervisionActionService(service *supervision.ActionService) {
	if h == nil {
		return
	}
	h.supervisionStoreMu.Lock()
	h.supervisionActions = service
	h.supervisionStoreMu.Unlock()
}

// SetSupervisionWakeScheduler sets the wake scheduler.
func (h *Handler) SetSupervisionWakeScheduler(scheduler *supervision.WakeScheduler) {
	if h == nil {
		return
	}
	h.supervisionStoreMu.Lock()
	h.supervisionWakes = scheduler
	h.supervisionStoreMu.Unlock()
	if scheduler != nil {
		h.bindSupervisionTurnEndConsumer()
	}
}

// bindSupervisionTurnEndConsumer subscribes parent session turn-end events
// once so wakes accumulated while a parent was busy are drained as soon as
// the parent becomes idle again (doc 6.5 rule 2 closure). It reacts to any
// session turn end: for a root session it drains root wakes; for an
// intermediate parent it drains that parent's own child wakes.
func (h *Handler) bindSupervisionTurnEndConsumer() {
	if h == nil {
		return
	}
	h.supervisionWakeOnce.Do(func() {
		bus := h.getRuntimeEventBus()
		if bus == nil {
			return
		}
		bus.SubscribeCancelable(chat.EventSessionEnd, func(event runtimeevents.Event) {
			sessionID := strings.TrimSpace(event.SessionID)
			if sessionID == "" {
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if h.sessionManager == nil {
				return
			}
			session, err := h.sessionManager.Get(ctx, sessionID)
			if err != nil || session == nil {
				return
			}
			rootScopeID := apiAgentRootSessionID(session, sessionID)
			controller := &sessionAgentController{handler: h}
			_ = controller.wakeSupervisedParent(ctx, rootScopeID, sessionID)
		})
	})
}

// SetSupervisionDescendantProvider sets the runtime descendant provider
// (agentcontrol + team store rollup) used by snapshot aggregation.
func (h *Handler) SetSupervisionDescendantProvider(provider supervision.DescendantProvider) {
	if h == nil {
		return
	}
	h.supervisionStoreMu.Lock()
	h.supervisionDescendantProvider = provider
	h.supervisionStoreMu.Unlock()
}

// CloseAgentSessionByID closes a child agent session through the runtime
// session controller. It backs the durable supervision action executor so
// cancel/close actions actually stop the live session (doc 6.6).
func (h *Handler) CloseAgentSessionByID(ctx context.Context, sessionID string) error {
	if h == nil {
		return fmt.Errorf("handler is nil")
	}
	controller := h.getAgentSessionController()
	if controller == nil {
		return fmt.Errorf("agent session controller is not configured")
	}
	_, err := controller.Close(ctx, sessionID)
	return err
}

func (h *Handler) getSupervisionStore() supervision.Store {
	if h == nil {
		return nil
	}
	h.supervisionStoreMu.RLock()
	store := h.supervisionStore
	h.supervisionStoreMu.RUnlock()
	return store
}

func (h *Handler) getSupervisionActionService() *supervision.ActionService {
	if h == nil {
		return nil
	}
	h.supervisionStoreMu.RLock()
	service := h.supervisionActions
	h.supervisionStoreMu.RUnlock()
	return service
}

func (h *Handler) getSupervisionWakeScheduler() *supervision.WakeScheduler {
	if h == nil {
		return nil
	}
	h.supervisionStoreMu.RLock()
	scheduler := h.supervisionWakes
	h.supervisionStoreMu.RUnlock()
	return scheduler
}

func (h *Handler) getSupervisionDescendantProvider() supervision.DescendantProvider {
	if h == nil {
		return nil
	}
	h.supervisionStoreMu.RLock()
	provider := h.supervisionDescendantProvider
	h.supervisionStoreMu.RUnlock()
	return provider
}

func (h *Handler) supervisionUnavailable(w http.ResponseWriter) bool {
	if h.getSupervisionStore() == nil {
		h.writeError(w, http.StatusServiceUnavailable, runtimeerrors.New(runtimeerrors.ErrConfigInvalid, "supervision store not configured"))
		return true
	}
	return false
}

// injectSupervisionPreflight builds the unresolved lifecycle digest immediately
// before a parent/lead turn starts and prepends it to that turn's prompt. A
// notification becoming visible in the prompt is marked delivered+seen, but is
// deliberately not acknowledged (doc 6.3: seen != acknowledged).
func (h *Handler) injectSupervisionPreflight(ctx context.Context, sessionID, prompt string, runMeta *team.RunMeta) (string, error) {
	store := h.getSupervisionStore()
	sessionID = strings.TrimSpace(sessionID)
	if store == nil || sessionID == "" {
		return prompt, nil
	}
	rootScopeID := sessionID
	targetTeamID := ""
	isTeamLead := false
	if runMeta != nil && runMeta.Team != nil {
		targetTeamID = strings.TrimSpace(runMeta.Team.TeamID)
		// A child task's run meta also has a TeamID. Only the team's registered
		// lead session is allowed to consume the team's supervision inbox.
		if targetTeamID != "" {
			if teamStore := h.getTeamStore(); teamStore != nil {
				if teamRecord, err := teamStore.GetTeam(ctx, targetTeamID); err == nil && teamRecord != nil {
					isTeamLead = strings.TrimSpace(teamRecord.LeadSessionID) == sessionID
				}
			}
		}
	}
	if isTeamLead {
		rootScopeID = targetTeamID
	} else {
		targetTeamID = ""
	}
	digest, err := supervision.BuildDigest(ctx, store, supervision.DigestRequest{
		RootScopeID:           rootScopeID,
		TargetParentSessionID: sessionID,
		TargetParentTeamID:    targetTeamID,
		Limit:                 20,
		IncludeResolvedSince:  true,
	})
	if err != nil {
		return "", fmt.Errorf("build supervision preflight digest: %w", err)
	}
	if digest == nil || len(digest.Items) == 0 {
		return prompt, nil
	}
	now := time.Now().UTC()
	for _, item := range digest.Items {
		if item.NotificationID == "" {
			continue
		}
		if err := store.MarkNotificationDelivered(ctx, item.NotificationID, now); err != nil {
			return "", fmt.Errorf("mark supervision notification delivered: %w", err)
		}
		if err := store.MarkNotificationSeen(ctx, item.NotificationID, now); err != nil {
			return "", fmt.Errorf("mark supervision notification seen: %w", err)
		}
	}
	return strings.TrimSpace(digest.Text) + "\n\n" + prompt, nil
}

// InjectSupervisionPreflight exposes the parent/lead turn hook to local hosts
// which submit actor prompts directly rather than through the HTTP runtime
// command endpoint.
func (h *Handler) InjectSupervisionPreflight(ctx context.Context, sessionID, prompt string, runMeta *team.RunMeta) (string, error) {
	return h.injectSupervisionPreflight(ctx, sessionID, prompt, runMeta)
}

// GetSupervisionDigest returns the parent turn preflight lifecycle digest
// (doc 6.4). Query params: root_scope_id, target_parent_session_id,
// target_parent_team_id, after_seq, limit, include_resolved_since.
func (h *Handler) GetSupervisionDigest(w http.ResponseWriter, r *http.Request) {
	if h.supervisionUnavailable(w) {
		return
	}
	q := r.URL.Query()
	digest, err := supervision.BuildDigest(r.Context(), h.getSupervisionStore(), supervision.DigestRequest{
		RootScopeID:           strings.TrimSpace(q.Get("root_scope_id")),
		TargetParentSessionID: strings.TrimSpace(q.Get("target_parent_session_id")),
		TargetParentTeamID:    strings.TrimSpace(q.Get("target_parent_team_id")),
		AfterSeq:              int64Query(q.Get("after_seq")),
		Limit:                 intQuery(q.Get("limit")),
		IncludeResolvedSince:  boolQuery(q.Get("include_resolved_since")),
	})
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"digest": digest,
	})
}

// GetSupervisionSnapshot returns the unified supervision read model
// (doc 6.2). Query params: root_session_id, root_team_id, mode,
// after_seq, health, include_terminal, limit.
func (h *Handler) GetSupervisionSnapshot(w http.ResponseWriter, r *http.Request) {
	if h.supervisionUnavailable(w) {
		return
	}
	q := r.URL.Query()
	snapshot, err := supervision.BuildSnapshot(r.Context(), h.getSupervisionStore(), supervision.SnapshotRequest{
		Scope: supervision.Scope{
			RootSessionID: strings.TrimSpace(q.Get("root_session_id")),
			RootTeamID:    strings.TrimSpace(q.Get("root_team_id")),
			Mode:          strings.TrimSpace(q.Get("mode")),
		},
		AfterSeq:        int64Query(q.Get("after_seq")),
		Health:          strings.TrimSpace(q.Get("health")),
		IncludeTerminal: boolQuery(q.Get("include_terminal")),
		Limit:           intQuery(q.Get("limit")),
		Provider:        h.getSupervisionDescendantProvider(),
	})
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"snapshot": snapshot,
	})
}

// RequestSupervisionAction persists a durable control action request
// (doc 6.6). Body: root_scope_id, requested_by_kind, requested_by_id,
// target_kind, target_id, action, cascade_mode, reason, expected_version,
// expected_fencing_token.
func (h *Handler) RequestSupervisionAction(w http.ResponseWriter, r *http.Request) {
	if h.supervisionUnavailable(w) {
		return
	}
	var req supervision.ActionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		h.writeError(w, http.StatusBadRequest, runtimeerrors.New(runtimeerrors.ErrValidationFailed, "failed to parse request body"))
		return
	}
	service := h.getSupervisionActionService()
	if service == nil {
		service = supervision.NewActionService(h.getSupervisionStore(), nil, nil)
	}
	record, err := service.RequestAction(r.Context(), req)
	if err != nil {
		h.writeSupervisionActionError(w, err)
		return
	}
	h.writeJSON(w, http.StatusCreated, map[string]interface{}{
		"action": record,
	})
}

// ListSupervisionActions lists durable actions. Query params:
// root_scope_id, target_kind, target_id, action, status, limit.
func (h *Handler) ListSupervisionActions(w http.ResponseWriter, r *http.Request) {
	if h.supervisionUnavailable(w) {
		return
	}
	q := r.URL.Query()
	records, err := h.getSupervisionStore().ListActions(r.Context(), supervision.ActionFilter{
		RootScopeID: strings.TrimSpace(q.Get("root_scope_id")),
		TargetKind:  supervision.SubjectKind(strings.TrimSpace(q.Get("target_kind"))),
		TargetID:    strings.TrimSpace(q.Get("target_id")),
		Action:      supervision.ActionKind(strings.TrimSpace(q.Get("action"))),
		Status:      supervision.ActionStatus(strings.TrimSpace(q.Get("status"))),
		Limit:       intQuery(q.Get("limit")),
	})
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"actions": records,
	})
}

// GetSupervisionAction returns one durable action.
func (h *Handler) GetSupervisionAction(w http.ResponseWriter, r *http.Request) {
	if h.supervisionUnavailable(w) {
		return
	}
	actionID := mux.Vars(r)["id"]
	service := h.getSupervisionActionService()
	if service == nil {
		service = supervision.NewActionService(h.getSupervisionStore(), nil, nil)
	}
	record, err := service.GetAction(r.Context(), actionID)
	if err != nil {
		h.writeSupervisionActionError(w, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"action": record,
	})
}

// AcceptSupervisionAction moves requested -> accepted (CAS), freezing cascade
// roots first (doc 6.6 constraint 3).
func (h *Handler) AcceptSupervisionAction(w http.ResponseWriter, r *http.Request) {
	if h.supervisionUnavailable(w) {
		return
	}
	service := h.getSupervisionActionService()
	if service == nil {
		service = supervision.NewActionService(h.getSupervisionStore(), nil, nil)
	}
	record, err := service.AcceptAction(r.Context(), mux.Vars(r)["id"])
	if err != nil {
		h.writeSupervisionActionError(w, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"action": record,
	})
}

// ExecuteSupervisionAction runs the executor and records the terminal outcome
// plus resolution notification (doc 6.6 constraint 7).
func (h *Handler) ExecuteSupervisionAction(w http.ResponseWriter, r *http.Request) {
	if h.supervisionUnavailable(w) {
		return
	}
	service := h.getSupervisionActionService()
	if service == nil {
		service = supervision.NewActionService(h.getSupervisionStore(), nil, nil)
	}
	record, err := service.ExecuteAction(r.Context(), mux.Vars(r)["id"])
	if err != nil {
		h.writeSupervisionActionError(w, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"action": record,
	})
}

// AcknowledgeSupervisionNotification acknowledges a lifecycle notification
// (CAS on expected_version). Body: expected_version, note.
func (h *Handler) AcknowledgeSupervisionNotification(w http.ResponseWriter, r *http.Request) {
	if h.supervisionUnavailable(w) {
		return
	}
	var req struct {
		ExpectedVersion int64  `json:"expected_version"`
		Note            string `json:"note,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		h.writeError(w, http.StatusBadRequest, runtimeerrors.New(runtimeerrors.ErrValidationFailed, "failed to parse request body"))
		return
	}
	notificationID := mux.Vars(r)["id"]
	ok, err := h.getSupervisionStore().AcknowledgeNotification(r.Context(), notificationID, time.Now().UTC(), req.ExpectedVersion)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !ok {
		h.writeError(w, http.StatusConflict, runtimeerrors.New(runtimeerrors.ErrValidationFailed, "notification version changed; re-read snapshot"))
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"acknowledged":    true,
		"notification_id": notificationID,
	})
}

// DeferSupervisionNotification defers a lifecycle notification until a time.
// Body: until (RFC3339), reason, expected_version.
func (h *Handler) DeferSupervisionNotification(w http.ResponseWriter, r *http.Request) {
	if h.supervisionUnavailable(w) {
		return
	}
	var req struct {
		Until           string `json:"until"`
		Reason          string `json:"reason"`
		ExpectedVersion int64  `json:"expected_version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		h.writeError(w, http.StatusBadRequest, runtimeerrors.New(runtimeerrors.ErrValidationFailed, "failed to parse request body"))
		return
	}
	until, err := time.Parse(time.RFC3339, strings.TrimSpace(req.Until))
	if err != nil {
		h.writeError(w, http.StatusBadRequest, runtimeerrors.New(runtimeerrors.ErrValidationFailed, "until must be RFC3339"))
		return
	}
	notificationID := mux.Vars(r)["id"]
	ok, err := h.getSupervisionStore().DeferNotification(r.Context(), notificationID, until.UTC(), strings.TrimSpace(req.Reason), req.ExpectedVersion)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !ok {
		h.writeError(w, http.StatusConflict, runtimeerrors.New(runtimeerrors.ErrValidationFailed, "notification version changed; re-read snapshot"))
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"deferred":        true,
		"until":           until.UTC(),
		"notification_id": notificationID,
	})
}

// ScheduleSupervisionWake persists a durable parent-turn wake request
// (doc 6.5 rule 3). Body: root_scope_id, target_parent_session_id,
// target_parent_team_id, wake_reason, notification_seq.
func (h *Handler) ScheduleSupervisionWake(w http.ResponseWriter, r *http.Request) {
	if h.supervisionUnavailable(w) {
		return
	}
	var req supervision.WakeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		h.writeError(w, http.StatusBadRequest, runtimeerrors.New(runtimeerrors.ErrValidationFailed, "failed to parse request body"))
		return
	}
	scheduler := h.getSupervisionWakeScheduler()
	if scheduler == nil {
		scheduler = supervision.NewWakeScheduler(h.getSupervisionStore(), supervision.WakeSchedulerConfig{})
	}
	result, err := scheduler.ScheduleWake(r.Context(), req)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	h.writeJSON(w, http.StatusCreated, map[string]interface{}{
		"wake": result,
	})
}

// RecordSupervisionTeamEdge records a durable parent Team -> child Team edge
// (doc 6.7 rule 1). Body: root_team_id, parent_team_id, parent_kind,
// parent_id, child_team_id, relation, created_by.
func (h *Handler) RecordSupervisionTeamEdge(w http.ResponseWriter, r *http.Request) {
	if h.supervisionUnavailable(w) {
		return
	}
	var req struct {
		RootTeamID   string `json:"root_team_id"`
		ParentTeamID string `json:"parent_team_id"`
		ParentKind   string `json:"parent_kind"`
		ParentID     string `json:"parent_id"`
		ChildTeamID  string `json:"child_team_id"`
		Relation     string `json:"relation"`
		CreatedBy    string `json:"created_by"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		h.writeError(w, http.StatusBadRequest, runtimeerrors.New(runtimeerrors.ErrValidationFailed, "failed to parse request body"))
		return
	}
	edge := supervision.TeamEdge{
		RootScopeID:  firstNonEmptySupervision(req.RootTeamID, req.ParentTeamID),
		RootTeamID:   req.RootTeamID,
		ParentTeamID: req.ParentTeamID,
		ParentKind:   req.ParentKind,
		ParentID:     req.ParentID,
		ChildTeamID:  req.ChildTeamID,
		Relation:     firstNonEmptySupervision(req.Relation, "nested"),
		CreatedBy:    req.CreatedBy,
		CreatedAt:    time.Now().UTC(),
		Status:       supervision.TeamEdgeStatusActive,
		Version:      1,
	}
	recorded, err := h.getSupervisionStore().UpsertTeamEdge(r.Context(), edge)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	h.writeJSON(w, http.StatusCreated, map[string]interface{}{
		"edge": recorded,
	})
}

// ListSupervisionTeamEdges lists active child Team edges. Query params:
// parent_team_id.
func (h *Handler) ListSupervisionTeamEdges(w http.ResponseWriter, r *http.Request) {
	if h.supervisionUnavailable(w) {
		return
	}
	parentTeamID := strings.TrimSpace(r.URL.Query().Get("parent_team_id"))
	edges, err := h.getSupervisionStore().ListChildTeams(r.Context(), parentTeamID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"edges": edges,
	})
}

func (h *Handler) writeSupervisionActionError(w http.ResponseWriter, err error) {
	switch {
	case isSupervisionSentinel(err, supervision.ErrActionConflict):
		h.writeError(w, http.StatusConflict, runtimeerrors.New(runtimeerrors.ErrValidationFailed, err.Error()))
	case isSupervisionSentinel(err, supervision.ErrActionNotAllowed):
		h.writeError(w, http.StatusForbidden, runtimeerrors.New(runtimeerrors.ErrValidationFailed, err.Error()))
	case isSupervisionSentinel(err, supervision.ErrActionNotFound):
		h.writeError(w, http.StatusNotFound, runtimeerrors.New(runtimeerrors.ErrValidationFailed, err.Error()))
	case isSupervisionSentinel(err, supervision.ErrActionNotActionable):
		h.writeError(w, http.StatusConflict, runtimeerrors.New(runtimeerrors.ErrValidationFailed, err.Error()))
	default:
		h.writeError(w, http.StatusInternalServerError, err)
	}
}

func isSupervisionSentinel(err error, target error) bool {
	for err != nil {
		if err == target {
			return true
		}
		unwrapper, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrapper.Unwrap()
	}
	return false
}

func firstNonEmptySupervision(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func int64Query(raw string) int64 {
	value, _ := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	return value
}

func intQuery(raw string) int {
	value, _ := strconv.Atoi(strings.TrimSpace(raw))
	return value
}

func boolQuery(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
