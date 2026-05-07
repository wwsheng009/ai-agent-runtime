package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	errors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

// SetTeamStore sets the Team store used by the handler.
func (h *Handler) SetTeamStore(store team.Store) {
	if h == nil {
		return
	}
	h.teamStoreMu.Lock()
	h.teamStore = store
	h.teamStoreMu.Unlock()
}

// SetTeamOrchestrator sets the Team orchestrator used by the handler.
func (h *Handler) SetTeamOrchestrator(orchestrator *team.Orchestrator) {
	if h == nil {
		return
	}
	h.teamStoreMu.Lock()
	h.teamOrchestrator = orchestrator
	h.teamStoreMu.Unlock()
}

// SetTeamClaimsManager sets the path claim manager for Team operations.
func (h *Handler) SetTeamClaimsManager(manager *team.PathClaimManager) {
	if h == nil {
		return
	}
	h.teamStoreMu.Lock()
	h.teamClaimsManager = manager
	h.teamStoreMu.Unlock()
}

func (h *Handler) getTeamStore() team.Store {
	if h == nil {
		return nil
	}
	h.teamStoreMu.RLock()
	store := h.teamStore
	h.teamStoreMu.RUnlock()
	return store
}

func (h *Handler) getTeamClaimsManager() *team.PathClaimManager {
	if h == nil {
		return nil
	}

	h.teamStoreMu.RLock()
	claims := h.teamClaimsManager
	store := h.teamStore
	config := h.runtimeConfig
	h.teamStoreMu.RUnlock()

	if claims != nil {
		return claims
	}
	if store == nil {
		return nil
	}
	root := ""
	if config != nil {
		root = strings.TrimSpace(config.Workspace.Root)
	}
	newClaims := team.NewPathClaimManager(store, root)

	h.teamStoreMu.Lock()
	if h.teamClaimsManager == nil {
		h.teamClaimsManager = newClaims
	}
	claims = h.teamClaimsManager
	h.teamStoreMu.Unlock()
	return claims
}

func (h *Handler) getTeamOrchestrator() *team.Orchestrator {
	if h == nil {
		return nil
	}

	h.teamStoreMu.RLock()
	store := h.teamStore
	h.teamStoreMu.RUnlock()
	if store == nil {
		return nil
	}

	claims := h.getTeamClaimsManager()
	hub := h.getSessionHub()
	h.teamStoreMu.Lock()
	defer h.teamStoreMu.Unlock()
	if h.teamOrchestrator == nil {
		h.teamOrchestrator = team.NewOrchestrator(store, claims, nil)
	}
	orchestrator := h.teamOrchestrator
	orchestrator.Store = store
	orchestrator.Claims = claims

	mailbox := orchestrator.Mailbox
	if mailbox == nil {
		mailbox = team.NewMailboxService(store)
		orchestrator.Mailbox = mailbox
	}

	if orchestrator.Events == nil {
		orchestrator.Events = team.NewTeamEventBus()
		orchestrator.Events.Subscribe("", func(event team.TeamEvent) {
			payload := map[string]interface{}{}
			for key, value := range event.Payload {
				payload[key] = value
			}
			if event.TeamID != "" {
				payload["team_id"] = event.TeamID
			}
			traceID := ""
			if raw, ok := payload["trace_id"].(string); ok {
				traceID = strings.TrimSpace(raw)
			}
			h.deliverTeamLifecycleMailbox(context.Background(), event)
			h.deliverTeamTaskLifecycleMailbox(context.Background(), event)
			h.getRuntimeEventBus().Publish(runtimeevents.Event{
				Type:      normalizeTeamEventType(event.Type),
				TraceID:   traceID,
				AgentName: "team-orchestrator",
				Payload:   payload,
				Timestamp: event.Timestamp,
			})
		})
	}

	if orchestrator.LeaseManager == nil {
		orchestrator.LeaseManager = team.NewLeaseManager(store, claims)
	}
	orchestrator.LeaseManager.Store = store
	orchestrator.LeaseManager.Claims = claims
	if orchestrator.LeaseManager.Mailbox == nil {
		orchestrator.LeaseManager.Mailbox = mailbox
	}

	if hub != nil {
		sessionClient := &sessionActorClient{
			hub:        hub,
			store:      store,
			eventStore: h.getSessionEventStore(),
			eventBus:   h.getRuntimeEventBus(),
		}
		if orchestrator.Runner == nil {
			orchestrator.Runner = &team.TeammateRunner{}
		}
		orchestrator.Runner.Sessions = sessionClient
		orchestrator.Runner.AgentControl = sessionClient
		if orchestrator.Runner.Mailbox == nil {
			orchestrator.Runner.Mailbox = mailbox
		}
		if orchestrator.Runner.Context == nil {
			orchestrator.Runner.Context = team.NewContextBuilder(store)
		}
		if orchestrator.LeadPlanner == nil {
			orchestrator.LeadPlanner = &team.LeadPlanner{}
		}
		orchestrator.LeadPlanner.Sessions = sessionClient
		orchestrator.LeadPlanner.Store = store
		if orchestrator.LeadPlanner.Mailbox == nil {
			orchestrator.LeadPlanner.Mailbox = mailbox
		}
	}
	return cloneTeamOrchestrator(orchestrator)
}

func (h *Handler) getTeamEvents() *team.TeamEventBus {
	orchestrator := h.getTeamOrchestrator()
	if orchestrator == nil {
		return nil
	}
	return orchestrator.Events
}

func cloneTeamOrchestrator(orchestrator *team.Orchestrator) *team.Orchestrator {
	if orchestrator == nil {
		return nil
	}
	cloned := *orchestrator
	if orchestrator.LeaseManager != nil {
		leaseManager := *orchestrator.LeaseManager
		cloned.LeaseManager = &leaseManager
	}
	if orchestrator.Runner != nil {
		runner := *orchestrator.Runner
		cloned.Runner = &runner
	}
	if orchestrator.LeadPlanner != nil {
		planner := *orchestrator.LeadPlanner
		cloned.LeadPlanner = &planner
	}
	return &cloned
}

// DispatchTeamMailboxMessage delivers mailbox notifications to target sessions.
func (h *Handler) DispatchTeamMailboxMessage(ctx context.Context, message team.MailMessage) error {
	if h == nil {
		return nil
	}
	if strings.TrimSpace(message.ID) == "" || strings.TrimSpace(message.TeamID) == "" {
		return nil
	}
	store := h.getTeamStore()
	if store == nil {
		return nil
	}
	targets, err := h.resolveTeamMailboxSessionTargets(ctx, store, message)
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		return nil
	}
	var failures []string
	eventStore := h.getSessionEventStore()
	eventBus := h.getRuntimeEventBus()
	var controller *sessionAgentController
	if eventStore == nil {
		controller = h.getAgentSessionController()
	}
	for _, sessionID := range targets {
		var fallback chat.MailboxActorFallback
		if eventStore == nil {
			if controller == nil {
				failures = append(failures, "session controller not configured")
				continue
			}
			fallback = controller.deliverMailboxToActor
		}
		if err := chat.DeliverMailboxEventFirst(ctx, eventStore, eventBus, fallback, sessionID, message); err != nil {
			failures = append(failures, err.Error())
		}
	}
	if len(failures) == 0 {
		return nil
	}
	return fmt.Errorf("deliver mailbox message: %s", strings.Join(failures, "; "))
}

func (h *Handler) deliverTeamLifecycleMailbox(ctx context.Context, event team.TeamEvent) {
	if h == nil {
		return
	}
	switch strings.TrimSpace(event.Type) {
	case "team.completed", "team.summary":
	default:
		return
	}
	sessionID := h.teamLifecycleMailboxSessionID(ctx, strings.TrimSpace(event.TeamID))
	if sessionID == "" {
		return
	}
	_ = chat.DeliverMailboxEventFirst(ctx, h.getSessionEventStore(), nil, nil, sessionID, team.BuildTeamLifecycleMailboxMessage(event))
}

func (h *Handler) deliverTeamTaskLifecycleMailbox(ctx context.Context, event team.TeamEvent) {
	if h == nil {
		return
	}
	switch strings.TrimSpace(event.Type) {
	case "task.completed", "task.failed":
	default:
		return
	}
	assignee := teamEventPayloadString(event.Payload["assignee"])
	if assignee == "" {
		return
	}
	store := h.getTeamStore()
	if store == nil {
		return
	}
	mate, err := store.GetTeammate(ctx, assignee)
	if err != nil || mate == nil || strings.TrimSpace(mate.SessionID) == "" {
		return
	}
	_ = chat.DeliverMailboxEventFirst(ctx, h.getSessionEventStore(), nil, nil, strings.TrimSpace(mate.SessionID), team.BuildTaskLifecycleMailboxMessage(event))
}

func teamEventPayloadString(value interface{}) string {
	if value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case fmt.Stringer:
		return strings.TrimSpace(typed.String())
	default:
		return strings.TrimSpace(fmt.Sprint(value))
	}
}

func (h *Handler) teamLifecycleMailboxSessionID(ctx context.Context, teamID string) string {
	teamID = strings.TrimSpace(teamID)
	if h == nil || teamID == "" {
		return ""
	}
	store := h.getTeamStore()
	if store == nil {
		return ""
	}
	record, err := store.GetTeam(ctx, teamID)
	if err != nil || record == nil {
		return ""
	}
	return strings.TrimSpace(record.LeadSessionID)
}

func (h *Handler) resolveTeamMailboxSessionTargets(ctx context.Context, store team.Store, message team.MailMessage) ([]string, error) {
	if store == nil {
		return nil, nil
	}
	teamID := strings.TrimSpace(message.TeamID)
	if teamID == "" {
		return nil, nil
	}
	record, err := store.GetTeam(ctx, teamID)
	if err != nil {
		return nil, err
	}
	targetSet := make(map[string]struct{})
	addTarget := func(sessionID string) {
		sessionID = strings.TrimSpace(sessionID)
		if sessionID == "" {
			return
		}
		targetSet[sessionID] = struct{}{}
	}
	toAgent := strings.TrimSpace(message.ToAgent)
	switch toAgent {
	case "", "*":
		if record != nil {
			addTarget(record.LeadSessionID)
		}
		teammates, err := store.ListTeammates(ctx, teamID)
		if err != nil {
			return nil, err
		}
		for _, mate := range teammates {
			addTarget(mate.SessionID)
		}
	case "lead":
		if record != nil {
			addTarget(record.LeadSessionID)
		}
	default:
		mate, err := store.GetTeammate(ctx, toAgent)
		if err != nil {
			return nil, err
		}
		if mate != nil && strings.TrimSpace(mate.TeamID) == teamID {
			addTarget(mate.SessionID)
		}
		if record != nil && (toAgent == record.LeadSessionID || strings.EqualFold(toAgent, "lead")) {
			addTarget(record.LeadSessionID)
		}
	}
	targets := make([]string, 0, len(targetSet))
	for sessionID := range targetSet {
		targets = append(targets, sessionID)
	}
	sort.Strings(targets)
	return targets, nil
}

// ListTeams lists teams with optional filters.
func (h *Handler) ListTeams(w http.ResponseWriter, r *http.Request) {
	store := h.getTeamStore()
	if store == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "team store not configured"))
		return
	}
	status, err := parseTeamStatus(r.URL.Query().Get("status"))
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	limit, err := parseOptionalLimit(r.URL.Query().Get("limit"))
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	ids := parseIDList(r.URL.Query().Get("team_ids"))
	workspaceID := strings.TrimSpace(r.URL.Query().Get("workspace_id"))
	listLimit := limit
	if len(ids) > 0 {
		listLimit = 0
	}
	teams, err := store.ListTeams(r.Context(), team.TeamFilter{
		WorkspaceID: workspaceID,
		Status:      status,
		Limit:       listLimit,
	})
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	if len(ids) > 0 {
		allowed := make(map[string]struct{}, len(ids))
		for _, id := range ids {
			allowed[id] = struct{}{}
		}
		filtered := make([]team.Team, 0, len(teams))
		for _, item := range teams {
			if _, ok := allowed[item.ID]; ok {
				filtered = append(filtered, item)
			}
		}
		teams = filtered
		if limit > 0 && len(teams) > limit {
			teams = teams[:limit]
		}
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"teams":        teams,
		"count":        len(teams),
		"limit":        limit,
		"team_ids":     ids,
		"workspace_id": workspaceID,
		"status":       status,
	})
}

// ListTeamEvents lists persisted team events.
func (h *Handler) ListTeamEvents(w http.ResponseWriter, r *http.Request) {
	store := h.getTeamStore()
	if store == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "team store not configured"))
		return
	}
	teamID := mux.Vars(r)["id"]
	rawAfter := strings.TrimSpace(r.URL.Query().Get("after"))
	afterSeq := int64(0)
	if rawAfter != "" {
		value, err := strconv.ParseInt(rawAfter, 10, 64)
		if err != nil || value < 0 {
			h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "invalid after value"))
			return
		}
		afterSeq = value
	}
	limit, err := parseOptionalLimit(r.URL.Query().Get("limit"))
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	eventType := strings.TrimSpace(r.URL.Query().Get("event_type"))
	if eventType != "" {
		normalized := normalizeTeamEventType(eventType)
		if strings.HasPrefix(normalized, "team.task.") {
			eventType = strings.TrimPrefix(normalized, "team.")
		} else {
			eventType = normalized
		}
	}
	since, err := parseOptionalTime(r.URL.Query().Get("since"))
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	until, err := parseOptionalTime(r.URL.Query().Get("until"))
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	events, err := store.ListTeamEvents(r.Context(), team.TeamEventFilter{
		TeamID:    teamID,
		AfterSeq:  afterSeq,
		Limit:     limit,
		EventType: eventType,
		Since:     since,
		Until:     until,
	})
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"team_id":    teamID,
		"events":     events,
		"after":      afterSeq,
		"limit":      limit,
		"event_type": eventType,
		"since":      since,
		"until":      until,
	})
}

// CreateTeam creates a new team.
func (h *Handler) CreateTeam(w http.ResponseWriter, r *http.Request) {
	store := h.getTeamStore()
	if store == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "team store not configured"))
		return
	}
	var req struct {
		ID            string `json:"id,omitempty"`
		WorkspaceID   string `json:"workspace_id,omitempty"`
		LeadSessionID string `json:"lead_session_id,omitempty"`
		Status        string `json:"status,omitempty"`
		Strategy      string `json:"strategy,omitempty"`
		MaxTeammates  int    `json:"max_teammates,omitempty"`
		MaxWriters    int    `json:"max_writers,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "failed to parse request body"))
		return
	}
	status, err := parseTeamStatus(req.Status)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	teamRecord := team.Team{
		ID:            strings.TrimSpace(req.ID),
		WorkspaceID:   strings.TrimSpace(req.WorkspaceID),
		LeadSessionID: strings.TrimSpace(req.LeadSessionID),
		Status:        status,
		Strategy:      strings.TrimSpace(req.Strategy),
		MaxTeammates:  req.MaxTeammates,
		MaxWriters:    req.MaxWriters,
	}
	id, err := store.CreateTeam(r.Context(), teamRecord)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	created, _ := store.GetTeam(r.Context(), id)
	if created == nil {
		teamRecord.ID = id
		created = &teamRecord
	}
	{
		traceID, requestID := runtimeTraceIDForRequest(r)
		payload := map[string]interface{}{
			"team_id":      created.ID,
			"workspace_id": created.WorkspaceID,
			"status":       created.Status,
		}
		if requestID != "" {
			payload["request_id"] = requestID
		}
		h.publishRuntimeEvent("team.created", traceID, payload)
	}
	if lifecycle := h.teamLifecycleService(); lifecycle != nil {
		go lifecycle.SyncLoops()
	}
	h.writeJSON(w, http.StatusCreated, map[string]interface{}{
		"team": created,
	})
}

// GetTeam returns a single team by id.
func (h *Handler) GetTeam(w http.ResponseWriter, r *http.Request) {
	store := h.getTeamStore()
	if store == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "team store not configured"))
		return
	}
	teamID := mux.Vars(r)["id"]
	item, err := store.GetTeam(r.Context(), teamID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	if item == nil {
		h.writeError(w, http.StatusNotFound, errors.New(errors.ErrValidationFailed, "team not found"))
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"team": item,
	})
}

// UpdateTeam updates a team.
func (h *Handler) UpdateTeam(w http.ResponseWriter, r *http.Request) {
	store := h.getTeamStore()
	if store == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "team store not configured"))
		return
	}
	teamID := mux.Vars(r)["id"]
	current, err := store.GetTeam(r.Context(), teamID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	if current == nil {
		h.writeError(w, http.StatusNotFound, errors.New(errors.ErrValidationFailed, "team not found"))
		return
	}
	var req struct {
		WorkspaceID   *string `json:"workspace_id,omitempty"`
		LeadSessionID *string `json:"lead_session_id,omitempty"`
		Status        *string `json:"status,omitempty"`
		Strategy      *string `json:"strategy,omitempty"`
		MaxTeammates  *int    `json:"max_teammates,omitempty"`
		MaxWriters    *int    `json:"max_writers,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "failed to parse request body"))
		return
	}
	if req.WorkspaceID != nil {
		current.WorkspaceID = strings.TrimSpace(*req.WorkspaceID)
	}
	if req.LeadSessionID != nil {
		current.LeadSessionID = strings.TrimSpace(*req.LeadSessionID)
	}
	if req.Strategy != nil {
		current.Strategy = strings.TrimSpace(*req.Strategy)
	}
	if req.Status != nil {
		status, err := parseTeamStatus(*req.Status)
		if err != nil {
			h.writeError(w, http.StatusBadRequest, err)
			return
		}
		current.Status = status
	}
	if req.MaxTeammates != nil {
		current.MaxTeammates = *req.MaxTeammates
	}
	if req.MaxWriters != nil {
		current.MaxWriters = *req.MaxWriters
	}
	if err := store.UpdateTeam(r.Context(), *current); err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	{
		traceID, requestID := runtimeTraceIDForRequest(r)
		payload := map[string]interface{}{
			"team_id":      current.ID,
			"workspace_id": current.WorkspaceID,
			"status":       current.Status,
		}
		if requestID != "" {
			payload["request_id"] = requestID
		}
		h.publishRuntimeEvent("team.updated", traceID, payload)
	}
	if lifecycle := h.teamLifecycleService(); lifecycle != nil {
		go lifecycle.SyncLoops()
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"team": current,
	})
}

// DeleteTeam removes a team by id.
func (h *Handler) DeleteTeam(w http.ResponseWriter, r *http.Request) {
	store := h.getTeamStore()
	if store == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "team store not configured"))
		return
	}
	teamID := mux.Vars(r)["id"]
	if err := store.DeleteTeam(r.Context(), teamID); err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	{
		traceID, requestID := runtimeTraceIDForRequest(r)
		payload := map[string]interface{}{
			"team_id": teamID,
		}
		if requestID != "" {
			payload["request_id"] = requestID
		}
		h.publishRuntimeEvent("team.deleted", traceID, payload)
	}
	if lifecycle := h.teamLifecycleService(); lifecycle != nil {
		lifecycle.StopLoop(teamID)
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"team_id": teamID,
	})
}

// GetTeamSummary returns aggregate counts for a team.
func (h *Handler) GetTeamSummary(w http.ResponseWriter, r *http.Request) {
	store := h.getTeamStore()
	if store == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "team store not configured"))
		return
	}
	teamID := mux.Vars(r)["id"]
	includeMailbox := true
	includePathClaims := true
	light := parseOptionalBool(r.URL.Query().Get("light"))
	if light {
		includeMailbox = false
		includePathClaims = false
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("include_mailbox")); raw != "" {
		includeMailbox = parseOptionalBool(raw)
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("include_path_claims")); raw != "" {
		includePathClaims = parseOptionalBool(raw)
	}
	asOf, err := parseOptionalTime(r.URL.Query().Get("as_of"))
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	now := time.Now().UTC()
	if asOf == nil {
		asOf = &now
	}

	tasks, err := store.ListTasks(r.Context(), team.TaskFilter{TeamID: teamID})
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	taskCounts := map[string]int{
		string(team.TaskStatusPending):   0,
		string(team.TaskStatusReady):     0,
		string(team.TaskStatusRunning):   0,
		string(team.TaskStatusBlocked):   0,
		string(team.TaskStatusDone):      0,
		string(team.TaskStatusFailed):    0,
		string(team.TaskStatusCancelled): 0,
	}
	for _, task := range tasks {
		key := string(task.Status)
		if _, ok := taskCounts[key]; ok {
			taskCounts[key]++
		} else if strings.TrimSpace(key) != "" {
			taskCounts[key] = taskCounts[key] + 1
		}
	}

	teammates, err := store.ListTeammates(r.Context(), teamID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	teammateCounts := map[string]int{
		string(team.TeammateStateIdle):    0,
		string(team.TeammateStateBusy):    0,
		string(team.TeammateStateBlocked): 0,
		string(team.TeammateStateOffline): 0,
		"total":                           len(teammates),
	}
	for _, mate := range teammates {
		key := string(mate.State)
		if _, ok := teammateCounts[key]; ok {
			teammateCounts[key]++
		} else if strings.TrimSpace(key) != "" {
			teammateCounts[key] = teammateCounts[key] + 1
		}
	}

	mailboxSummary := map[string]interface{}{}
	if includeMailbox {
		messages, err := store.ListMail(r.Context(), team.MailFilter{
			TeamID: teamID,
		})
		if err != nil {
			h.writeError(w, http.StatusInternalServerError, err)
			return
		}
		unread, err := store.ListMail(r.Context(), team.MailFilter{
			TeamID:     teamID,
			UnreadOnly: true,
		})
		if err != nil {
			h.writeError(w, http.StatusInternalServerError, err)
			return
		}
		mailboxSummary["total"] = len(messages)
		mailboxSummary["unread"] = len(unread)
	}

	pathClaimsSummary := map[string]interface{}{}
	if includePathClaims {
		claims, err := store.ListPathClaims(r.Context(), teamID)
		if err != nil {
			h.writeError(w, http.StatusInternalServerError, err)
			return
		}
		activeClaims := 0
		for _, claim := range claims {
			if claim.LeaseUntil.IsZero() || !claim.LeaseUntil.Before(*asOf) {
				activeClaims++
			}
		}
		pathClaimsSummary["total"] = len(claims)
		pathClaimsSummary["active"] = activeClaims
	}

	response := map[string]interface{}{
		"team_id": teamID,
		"as_of":   asOf,
		"tasks": map[string]interface{}{
			"total":  len(tasks),
			"counts": taskCounts,
		},
		"teammates": map[string]interface{}{
			"total":  len(teammates),
			"counts": teammateCounts,
		},
		"mailbox_included":     includeMailbox,
		"path_claims_included": includePathClaims,
		"generated_at":         now,
		"light":                light,
	}
	if includeMailbox {
		response["mailbox"] = mailboxSummary
	}
	if includePathClaims {
		response["path_claims"] = pathClaimsSummary
	}
	h.writeJSON(w, http.StatusOK, response)
}

// GetTeamFinalSummary returns a lead-generated summary for a team.
func (h *Handler) GetTeamFinalSummary(w http.ResponseWriter, r *http.Request) {
	store := h.getTeamStore()
	if store == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "team store not configured"))
		return
	}
	teamID := mux.Vars(r)["id"]
	hub := h.getSessionHub()
	planner := &team.LeadPlanner{
		Store: store,
	}
	if hub != nil {
		planner.Sessions = &sessionActorClient{hub: hub}
	}
	summaryResult, err := planner.FinalSummaryDetailed(r.Context(), teamID)
	if err != nil {
		traceID, requestID := runtimeTraceIDForRequest(r)
		payload := map[string]interface{}{
			"team_id": teamID,
		}
		if requestID != "" {
			payload["request_id"] = requestID
		}
		resolvedTraceID := h.publishTeamSessionExecutionFailure("team.summary.failed", traceID, payload, err)
		if resolvedTraceID != "" {
			w.Header().Set("X-Trace-ID", resolvedTraceID)
		}
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	summary := ""
	if summaryResult != nil {
		summary = strings.TrimSpace(summaryResult.Summary)
		if traceID := strings.TrimSpace(summaryResult.TraceID); traceID != "" {
			w.Header().Set("X-Trace-ID", traceID)
		}
	}
	{
		traceID, requestID := runtimeTraceIDForRequest(r)
		if summaryResult != nil && strings.TrimSpace(summaryResult.TraceID) != "" {
			traceID = strings.TrimSpace(summaryResult.TraceID)
		}
		payload := map[string]interface{}{
			"team_id": teamID,
		}
		if requestID != "" {
			payload["request_id"] = requestID
		}
		if strings.TrimSpace(summary) != "" {
			payload["summary_present"] = true
			payload["summary"] = truncateLine(summary, 240)
		}
		if summaryResult != nil {
			team.AppendFinalSummaryMetadata(payload, summaryResult)
		}
		h.publishRuntimeEvent("team.summary.generated", traceID, payload)
	}
	response := map[string]interface{}{
		"team_id": teamID,
		"summary": summary,
	}
	if summaryResult != nil {
		team.AppendFinalSummaryMetadata(response, summaryResult)
		if metadata := summaryResult.CloneErrorMetadata(); len(metadata) > 0 {
			response["error_metadata"] = metadata
		}
	}
	h.writeJSON(w, http.StatusOK, response)
}

// ListTeamSummaries returns summary aggregates for multiple teams.
func (h *Handler) ListTeamSummaries(w http.ResponseWriter, r *http.Request) {
	store := h.getTeamStore()
	if store == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "team store not configured"))
		return
	}
	includeMailbox := false
	includeTeammateStates := false
	includePathClaims := true
	light := parseOptionalBool(r.URL.Query().Get("light"))
	if light {
		includeMailbox = false
		includeTeammateStates = false
		includePathClaims = false
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("include_mailbox")); raw != "" {
		includeMailbox = parseOptionalBool(raw)
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("include_teammate_states")); raw != "" {
		includeTeammateStates = parseOptionalBool(raw)
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("include_path_claims")); raw != "" {
		includePathClaims = parseOptionalBool(raw)
	}
	asOf, err := parseOptionalTime(r.URL.Query().Get("as_of"))
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	now := time.Now().UTC()
	if asOf == nil {
		asOf = &now
	}
	limit, err := parseOptionalLimit(r.URL.Query().Get("limit"))
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	requestedIDs := parseIDList(r.URL.Query().Get("team_ids"))
	ids, err := store.ListTeamIDs(r.Context())
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	if len(requestedIDs) > 0 {
		available := make(map[string]bool, len(ids))
		for _, id := range ids {
			available[id] = true
		}
		filtered := make([]string, 0, len(requestedIDs))
		for _, id := range requestedIDs {
			if available[id] {
				filtered = append(filtered, id)
			}
		}
		ids = filtered
	}
	if limit > 0 && len(ids) > limit {
		ids = ids[:limit]
	}

	summaries := make([]map[string]interface{}, 0, len(ids))
	for _, teamID := range ids {
		tasks, err := store.ListTasks(r.Context(), team.TaskFilter{TeamID: teamID})
		if err != nil {
			h.writeError(w, http.StatusInternalServerError, err)
			return
		}
		taskCounts := map[string]int{
			string(team.TaskStatusPending):   0,
			string(team.TaskStatusReady):     0,
			string(team.TaskStatusRunning):   0,
			string(team.TaskStatusBlocked):   0,
			string(team.TaskStatusDone):      0,
			string(team.TaskStatusFailed):    0,
			string(team.TaskStatusCancelled): 0,
		}
		for _, task := range tasks {
			key := string(task.Status)
			if _, ok := taskCounts[key]; ok {
				taskCounts[key]++
			} else if strings.TrimSpace(key) != "" {
				taskCounts[key] = taskCounts[key] + 1
			}
		}
		teammates, err := store.ListTeammates(r.Context(), teamID)
		if err != nil {
			h.writeError(w, http.StatusInternalServerError, err)
			return
		}
		teammateSummary := map[string]interface{}{
			"total": len(teammates),
		}
		if includeTeammateStates {
			stateCounts := map[string]int{
				string(team.TeammateStateIdle):    0,
				string(team.TeammateStateBusy):    0,
				string(team.TeammateStateBlocked): 0,
				string(team.TeammateStateOffline): 0,
			}
			for _, mate := range teammates {
				key := string(mate.State)
				if _, ok := stateCounts[key]; ok {
					stateCounts[key]++
				} else if strings.TrimSpace(key) != "" {
					stateCounts[key] = stateCounts[key] + 1
				}
			}
			teammateSummary["counts"] = stateCounts
		}

		pathClaimsSummary := map[string]interface{}{}
		if includePathClaims {
			claims, err := store.ListPathClaims(r.Context(), teamID)
			if err != nil {
				h.writeError(w, http.StatusInternalServerError, err)
				return
			}
			activeClaims := 0
			for _, claim := range claims {
				if claim.LeaseUntil.IsZero() || !claim.LeaseUntil.Before(*asOf) {
					activeClaims++
				}
			}
			pathClaimsSummary["total"] = len(claims)
			pathClaimsSummary["active"] = activeClaims
		}

		mailboxSummary := map[string]interface{}{}
		if includeMailbox {
			messages, err := store.ListMail(r.Context(), team.MailFilter{
				TeamID: teamID,
			})
			if err != nil {
				h.writeError(w, http.StatusInternalServerError, err)
				return
			}
			unread, err := store.ListMail(r.Context(), team.MailFilter{
				TeamID:     teamID,
				UnreadOnly: true,
			})
			if err != nil {
				h.writeError(w, http.StatusInternalServerError, err)
				return
			}
			mailboxSummary["total"] = len(messages)
			mailboxSummary["unread"] = len(unread)
		}

		entry := map[string]interface{}{
			"team_id": teamID,
			"tasks": map[string]interface{}{
				"total":  len(tasks),
				"counts": taskCounts,
			},
			"teammates": teammateSummary,
		}
		if includePathClaims {
			entry["path_claims"] = pathClaimsSummary
		}
		if includeMailbox {
			entry["mailbox"] = mailboxSummary
		}
		summaries = append(summaries, entry)
	}

	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"teams":                   summaries,
		"count":                   len(summaries),
		"as_of":                   asOf,
		"team_ids":                ids,
		"include_mailbox":         includeMailbox,
		"include_teammate_states": includeTeammateStates,
		"include_path_claims":     includePathClaims,
		"light":                   light,
	})
}

// ListTeammates lists teammates for a team.
func (h *Handler) ListTeammates(w http.ResponseWriter, r *http.Request) {
	store := h.getTeamStore()
	if store == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "team store not configured"))
		return
	}
	teamID := mux.Vars(r)["id"]
	limit, err := parseOptionalLimit(r.URL.Query().Get("limit"))
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	stateFilterRaw := strings.TrimSpace(r.URL.Query().Get("state"))
	var stateFilter *team.TeammateState
	if stateFilterRaw != "" {
		parsed, err := parseTeammateState(stateFilterRaw)
		if err != nil {
			h.writeError(w, http.StatusBadRequest, err)
			return
		}
		stateFilter = &parsed
	}
	teammates, err := store.ListTeammates(r.Context(), teamID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	if stateFilter != nil {
		filtered := make([]team.Teammate, 0, len(teammates))
		for _, mate := range teammates {
			if mate.State == *stateFilter {
				filtered = append(filtered, mate)
			}
		}
		teammates = filtered
	}
	if limit > 0 && len(teammates) > limit {
		teammates = teammates[:limit]
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"teammates": teammates,
		"count":     len(teammates),
		"limit":     limit,
		"state":     stateFilter,
	})
}

// UpsertTeammate creates or updates a teammate.
func (h *Handler) UpsertTeammate(w http.ResponseWriter, r *http.Request) {
	store := h.getTeamStore()
	if store == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "team store not configured"))
		return
	}
	teamID := mux.Vars(r)["id"]
	var req struct {
		ID            string   `json:"id,omitempty"`
		Name          string   `json:"name,omitempty"`
		Profile       string   `json:"profile,omitempty"`
		SessionID     string   `json:"session_id,omitempty"`
		State         string   `json:"state,omitempty"`
		Capabilities  []string `json:"capabilities,omitempty"`
		LastHeartbeat string   `json:"last_heartbeat,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "failed to parse request body"))
		return
	}
	state, err := parseTeammateState(req.State)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	var heartbeat time.Time
	if strings.TrimSpace(req.LastHeartbeat) != "" {
		parsed, err := parseTimeValue(req.LastHeartbeat)
		if err != nil {
			h.writeError(w, http.StatusBadRequest, err)
			return
		}
		heartbeat = parsed
	}
	teammate := team.Teammate{
		ID:            strings.TrimSpace(req.ID),
		TeamID:        teamID,
		Name:          strings.TrimSpace(req.Name),
		Profile:       strings.TrimSpace(req.Profile),
		SessionID:     strings.TrimSpace(req.SessionID),
		State:         state,
		Capabilities:  req.Capabilities,
		LastHeartbeat: heartbeat,
	}
	id, err := store.UpsertTeammate(r.Context(), teammate)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	updated, _ := store.GetTeammate(r.Context(), id)
	if updated == nil {
		teammate.ID = id
		updated = &teammate
	}
	{
		traceID, requestID := runtimeTraceIDForRequest(r)
		payload := map[string]interface{}{
			"team_id":     updated.TeamID,
			"teammate_id": updated.ID,
			"state":       updated.State,
			"profile":     updated.Profile,
		}
		if requestID != "" {
			payload["request_id"] = requestID
		}
		h.publishRuntimeEvent("team.teammate.upserted", traceID, payload)
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"teammate": updated,
	})
}

// UpdateTeammate patches an existing teammate.
func (h *Handler) UpdateTeammate(w http.ResponseWriter, r *http.Request) {
	store := h.getTeamStore()
	if store == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "team store not configured"))
		return
	}
	vars := mux.Vars(r)
	teamID := vars["id"]
	teammateID := vars["teammate_id"]
	current, err := store.GetTeammate(r.Context(), teammateID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	if current == nil || current.TeamID != teamID {
		h.writeError(w, http.StatusNotFound, errors.New(errors.ErrValidationFailed, "teammate not found"))
		return
	}
	var req struct {
		Name          *string  `json:"name,omitempty"`
		Profile       *string  `json:"profile,omitempty"`
		SessionID     *string  `json:"session_id,omitempty"`
		State         *string  `json:"state,omitempty"`
		Capabilities  []string `json:"capabilities,omitempty"`
		LastHeartbeat *string  `json:"last_heartbeat,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "failed to parse request body"))
		return
	}
	if req.Name != nil {
		current.Name = strings.TrimSpace(*req.Name)
	}
	if req.Profile != nil {
		current.Profile = strings.TrimSpace(*req.Profile)
	}
	if req.SessionID != nil {
		current.SessionID = strings.TrimSpace(*req.SessionID)
	}
	if req.State != nil {
		state, err := parseTeammateState(*req.State)
		if err != nil {
			h.writeError(w, http.StatusBadRequest, err)
			return
		}
		current.State = state
	}
	if req.Capabilities != nil {
		current.Capabilities = req.Capabilities
	}
	if req.LastHeartbeat != nil && strings.TrimSpace(*req.LastHeartbeat) != "" {
		parsed, err := parseTimeValue(*req.LastHeartbeat)
		if err != nil {
			h.writeError(w, http.StatusBadRequest, err)
			return
		}
		current.LastHeartbeat = parsed
	}
	id, err := store.UpsertTeammate(r.Context(), *current)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	updated, _ := store.GetTeammate(r.Context(), id)
	if updated == nil {
		updated = current
	}
	{
		traceID, requestID := runtimeTraceIDForRequest(r)
		payload := map[string]interface{}{
			"team_id":     updated.TeamID,
			"teammate_id": updated.ID,
			"state":       updated.State,
			"profile":     updated.Profile,
		}
		if requestID != "" {
			payload["request_id"] = requestID
		}
		h.publishRuntimeEvent("team.teammate.updated", traceID, payload)
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"teammate": updated,
	})
}

// UpdateTeammateHeartbeat updates a teammate heartbeat timestamp.
func (h *Handler) UpdateTeammateHeartbeat(w http.ResponseWriter, r *http.Request) {
	store := h.getTeamStore()
	if store == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "team store not configured"))
		return
	}
	vars := mux.Vars(r)
	teamID := vars["id"]
	teammateID := vars["teammate_id"]
	if strings.TrimSpace(teammateID) == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "teammate_id is required"))
		return
	}
	current, err := store.GetTeammate(r.Context(), teammateID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	if current == nil || current.TeamID != teamID {
		h.writeError(w, http.StatusNotFound, errors.New(errors.ErrValidationFailed, "teammate not found"))
		return
	}
	var req struct {
		Heartbeat string `json:"heartbeat,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "failed to parse request body"))
		return
	}
	heartbeat := time.Now().UTC()
	if strings.TrimSpace(req.Heartbeat) != "" {
		parsed, err := parseTimeValue(req.Heartbeat)
		if err != nil {
			h.writeError(w, http.StatusBadRequest, err)
			return
		}
		heartbeat = parsed
	}
	if err := store.UpdateTeammateHeartbeat(r.Context(), teammateID, heartbeat); err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	updated, _ := store.GetTeammate(r.Context(), teammateID)
	if updated == nil {
		updated = current
		updated.LastHeartbeat = heartbeat
	}
	{
		traceID, requestID := runtimeTraceIDForRequest(r)
		payload := map[string]interface{}{
			"team_id":        updated.TeamID,
			"teammate_id":    updated.ID,
			"last_heartbeat": heartbeat,
		}
		if requestID != "" {
			payload["request_id"] = requestID
		}
		h.publishRuntimeEvent("team.teammate.heartbeat", traceID, payload)
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"teammate": updated,
	})
}

// ListAgentControlTasks lists tasks through the shared AgentControl task
// registry read model instead of the team-native task shape.
func (h *Handler) ListAgentControlTasks(w http.ResponseWriter, r *http.Request) {
	store := h.getTeamStore()
	if store == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "team store not configured"))
		return
	}
	limit, err := parseOptionalLimit(r.URL.Query().Get("limit"))
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	filter := agentcontrol.TaskFilter{
		Workflow:   strings.TrimSpace(r.URL.Query().Get("workflow")),
		TeamID:     strings.TrimSpace(r.URL.Query().Get("team_id")),
		Assignee:   strings.TrimSpace(r.URL.Query().Get("assignee")),
		Status:     parseAgentControlTaskStatusList(r.URL.Query().Get("status")),
		PathPrefix: strings.TrimSpace(r.URL.Query().Get("path_prefix")),
		Limit:      limit,
	}
	registry := team.NewAgentControlTaskRegistry(store)
	records, err := registry.ListAgentControlTasks(r.Context(), filter)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"tasks": records,
		"count": len(records),
		"filters": map[string]interface{}{
			"workflow":    filter.Workflow,
			"team_id":     filter.TeamID,
			"assignee":    filter.Assignee,
			"status":      filter.Status,
			"path_prefix": filter.PathPrefix,
			"limit":       filter.Limit,
		},
	})
}

// CreateAgentControlTask creates a task through the shared AgentControl task
// registry write seam.
func (h *Handler) CreateAgentControlTask(w http.ResponseWriter, r *http.Request) {
	store := h.getTeamStore()
	if store == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "team store not configured"))
		return
	}
	var req struct {
		ID           string   `json:"id,omitempty"`
		Workflow     string   `json:"workflow,omitempty"`
		TeamID       string   `json:"team_id,omitempty"`
		ParentTaskID string   `json:"parent_task_id,omitempty"`
		Title        string   `json:"title,omitempty"`
		Goal         string   `json:"goal,omitempty"`
		Status       string   `json:"status,omitempty"`
		Priority     int      `json:"priority,omitempty"`
		Assignee     string   `json:"assignee,omitempty"`
		Inputs       []string `json:"inputs,omitempty"`
		ReadPaths    []string `json:"read_paths,omitempty"`
		WritePaths   []string `json:"write_paths,omitempty"`
		Deliverables []string `json:"deliverables,omitempty"`
		Summary      string   `json:"summary,omitempty"`
		ResultRef    string   `json:"result_ref,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "failed to parse request body"))
		return
	}
	status, err := parseTaskStatus(req.Status)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	registry := team.NewAgentControlTaskRegistry(store)
	record, err := registry.CreateAgentControlTask(r.Context(), agentcontrol.TaskCreateRequest{
		ID:           req.ID,
		Workflow:     firstNonEmptyString(strings.TrimSpace(req.Workflow), agentcontrol.WorkflowSpawnTeam),
		TeamID:       req.TeamID,
		ParentTaskID: req.ParentTaskID,
		Title:        req.Title,
		Goal:         req.Goal,
		Status:       string(status),
		Priority:     req.Priority,
		Assignee:     req.Assignee,
		Inputs:       req.Inputs,
		ReadPaths:    req.ReadPaths,
		WritePaths:   req.WritePaths,
		Deliverables: req.Deliverables,
		Summary:      req.Summary,
		ResultRef:    req.ResultRef,
	})
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	h.writeJSON(w, http.StatusCreated, map[string]interface{}{
		"task": record,
	})
}

// UpdateAgentControlTaskStatus updates a task status through the shared
// AgentControl task registry write seam.
func (h *Handler) UpdateAgentControlTaskStatus(w http.ResponseWriter, r *http.Request) {
	store := h.getTeamStore()
	if store == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "team store not configured"))
		return
	}
	taskID := mux.Vars(r)["task_id"]
	var req struct {
		Workflow string `json:"workflow,omitempty"`
		Status   string `json:"status,omitempty"`
		Summary  string `json:"summary,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "failed to parse request body"))
		return
	}
	status, err := parseTaskStatus(req.Status)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	registry := team.NewAgentControlTaskRegistry(store)
	record, err := registry.UpdateAgentControlTaskStatus(r.Context(), agentcontrol.TaskStatusUpdateRequest{
		ID:       taskID,
		Workflow: firstNonEmptyString(strings.TrimSpace(req.Workflow), agentcontrol.WorkflowSpawnTeam),
		Status:   string(status),
		Summary:  req.Summary,
	})
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{"task": record})
}

// ClaimAgentControlTask claims a task through the shared AgentControl task
// registry write seam.
func (h *Handler) ClaimAgentControlTask(w http.ResponseWriter, r *http.Request) {
	store := h.getTeamStore()
	if store == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "team store not configured"))
		return
	}
	taskID := mux.Vars(r)["task_id"]
	var req struct {
		Workflow        string   `json:"workflow,omitempty"`
		TeamID          string   `json:"team_id,omitempty"`
		Assignee        string   `json:"assignee,omitempty"`
		LeaseUntil      string   `json:"lease_until,omitempty"`
		DurationSec     int      `json:"duration_sec,omitempty"`
		ExpectedVersion int64    `json:"expected_version,omitempty"`
		ReadPaths       []string `json:"read_paths,omitempty"`
		WritePaths      []string `json:"write_paths,omitempty"`
		UsePathClaims   bool     `json:"use_path_claims,omitempty"`
		WorkspaceRoot   string   `json:"workspace_root,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "failed to parse request body"))
		return
	}
	leaseUntil, err := resolveLeaseUntil(req.LeaseUntil, req.DurationSec, h.defaultLeaseDuration())
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.UsePathClaims && strings.TrimSpace(req.WorkspaceRoot) == "" {
		if claims := h.getTeamClaimsManager(); claims != nil {
			req.WorkspaceRoot = claims.Root()
		}
	}
	registry := team.NewAgentControlTaskRegistry(store)
	record, claimed, err := registry.ClaimAgentControlTask(r.Context(), agentcontrol.TaskClaimRequest{
		ID:              taskID,
		Workflow:        firstNonEmptyString(strings.TrimSpace(req.Workflow), agentcontrol.WorkflowSpawnTeam),
		TeamID:          req.TeamID,
		Assignee:        req.Assignee,
		LeaseUntil:      leaseUntil,
		ExpectedVersion: req.ExpectedVersion,
		ReadPaths:       req.ReadPaths,
		WritePaths:      req.WritePaths,
		UsePathClaims:   req.UsePathClaims,
		WorkspaceRoot:   req.WorkspaceRoot,
	})
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"task":    record,
		"claimed": claimed,
	})
}

// RenewAgentControlTaskLease renews a task lease through the shared
// AgentControl task registry write seam.
func (h *Handler) RenewAgentControlTaskLease(w http.ResponseWriter, r *http.Request) {
	store := h.getTeamStore()
	if store == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "team store not configured"))
		return
	}
	taskID := mux.Vars(r)["task_id"]
	var req struct {
		Workflow    string `json:"workflow,omitempty"`
		LeaseUntil  string `json:"lease_until,omitempty"`
		DurationSec int    `json:"duration_sec,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "failed to parse request body"))
		return
	}
	leaseUntil, err := resolveLeaseUntil(req.LeaseUntil, req.DurationSec, h.defaultLeaseDuration())
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	registry := team.NewAgentControlTaskRegistry(store)
	record, err := registry.RenewAgentControlTaskLease(r.Context(), agentcontrol.TaskLeaseRenewRequest{
		ID:         taskID,
		Workflow:   firstNonEmptyString(strings.TrimSpace(req.Workflow), agentcontrol.WorkflowSpawnTeam),
		LeaseUntil: leaseUntil,
	})
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	if claims := h.getTeamClaimsManager(); claims != nil {
		_ = claims.Renew(r.Context(), taskID, leaseUntil)
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{"task": record})
}

// ReleaseAgentControlTask releases a task lease through the shared
// AgentControl task registry write seam.
func (h *Handler) ReleaseAgentControlTask(w http.ResponseWriter, r *http.Request) {
	store := h.getTeamStore()
	if store == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "team store not configured"))
		return
	}
	taskID := mux.Vars(r)["task_id"]
	var req struct {
		Workflow   string `json:"workflow,omitempty"`
		Status     string `json:"status,omitempty"`
		Summary    string `json:"summary,omitempty"`
		TeammateID string `json:"teammate_id,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "failed to parse request body"))
		return
	}
	status, err := parseTaskStatus(req.Status)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	if status == "" {
		status = team.TaskStatusPending
	}
	registry := team.NewAgentControlTaskRegistry(store)
	record, err := registry.ReleaseAgentControlTask(r.Context(), agentcontrol.TaskReleaseRequest{
		ID:       taskID,
		Workflow: firstNonEmptyString(strings.TrimSpace(req.Workflow), agentcontrol.WorkflowSpawnTeam),
		Status:   string(status),
		Summary:  req.Summary,
	})
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	if claims := h.getTeamClaimsManager(); claims != nil {
		_ = claims.Release(r.Context(), taskID)
	}
	if teammateID := strings.TrimSpace(req.TeammateID); teammateID != "" {
		_ = store.UpdateTeammateState(r.Context(), teammateID, team.TeammateStateIdle)
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{"task": record})
}

// UpdateAgentControlTaskTerminal applies a done/failed terminal task
// transition through the shared AgentControl task registry write seam.
func (h *Handler) UpdateAgentControlTaskTerminal(w http.ResponseWriter, r *http.Request) {
	store := h.getTeamStore()
	if store == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "team store not configured"))
		return
	}
	taskID := mux.Vars(r)["task_id"]
	var req struct {
		Workflow        string  `json:"workflow,omitempty"`
		TeamID          string  `json:"team_id,omitempty"`
		Status          string  `json:"status,omitempty"`
		Summary         string  `json:"summary,omitempty"`
		ResultRef       *string `json:"result_ref,omitempty"`
		TeammateID      string  `json:"teammate_id,omitempty"`
		SkipStateUpdate bool    `json:"skip_state_update,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "failed to parse request body"))
		return
	}
	status, err := parseTaskStatus(req.Status)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	registry := team.NewAgentControlTaskRegistry(store)
	record, err := registry.UpdateAgentControlTaskTerminal(r.Context(), agentcontrol.TaskTerminalUpdateRequest{
		ID:              taskID,
		Workflow:        firstNonEmptyString(strings.TrimSpace(req.Workflow), agentcontrol.WorkflowSpawnTeam),
		TeamID:          req.TeamID,
		Status:          string(status),
		Summary:         req.Summary,
		ResultRef:       normalizeOptionalString(req.ResultRef),
		TeammateID:      req.TeammateID,
		SkipStateUpdate: req.SkipStateUpdate,
	})
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	if claims := h.getTeamClaimsManager(); claims != nil {
		_ = claims.Release(r.Context(), taskID)
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{"task": record})
}

// BlockAgentControlTask blocks a task through the shared AgentControl task
// registry write seam.
func (h *Handler) BlockAgentControlTask(w http.ResponseWriter, r *http.Request) {
	store := h.getTeamStore()
	if store == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "team store not configured"))
		return
	}
	taskID := mux.Vars(r)["task_id"]
	var req struct {
		Workflow        string `json:"workflow,omitempty"`
		Summary         string `json:"summary,omitempty"`
		TeammateID      string `json:"teammate_id,omitempty"`
		SkipStateUpdate bool   `json:"skip_state_update,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "failed to parse request body"))
		return
	}
	registry := team.NewAgentControlTaskRegistry(store)
	record, err := registry.BlockAgentControlTask(r.Context(), agentcontrol.TaskBlockRequest{
		ID:              taskID,
		Workflow:        firstNonEmptyString(strings.TrimSpace(req.Workflow), agentcontrol.WorkflowSpawnTeam),
		Summary:         req.Summary,
		TeammateID:      req.TeammateID,
		SkipStateUpdate: req.SkipStateUpdate,
	})
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{"task": record})
}

// CreateAgentControlTaskDependency creates a dependency edge through the
// shared AgentControl task graph writer seam.
func (h *Handler) CreateAgentControlTaskDependency(w http.ResponseWriter, r *http.Request) {
	store := h.getTeamStore()
	if store == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "team store not configured"))
		return
	}
	taskID := mux.Vars(r)["task_id"]
	var req struct {
		Workflow    string `json:"workflow,omitempty"`
		TeamID      string `json:"team_id,omitempty"`
		DependsOnID string `json:"depends_on_id,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "failed to parse request body"))
		return
	}
	dependsOnID := strings.TrimSpace(req.DependsOnID)
	if dependsOnID == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "depends_on_id is required"))
		return
	}
	registry := team.NewAgentControlTaskRegistry(store)
	if err := registry.CreateAgentControlTaskDependency(r.Context(), agentcontrol.TaskDependencyCreateRequest{
		Workflow:    firstNonEmptyString(strings.TrimSpace(req.Workflow), agentcontrol.WorkflowSpawnTeam),
		TeamID:      req.TeamID,
		TaskID:      taskID,
		DependsOnID: dependsOnID,
	}); err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"task_id":       strings.TrimSpace(taskID),
		"depends_on_id": dependsOnID,
	})
}

// ListAgentControlTaskDependencies lists task graph edges through the shared
// AgentControl dependency read seam.
func (h *Handler) ListAgentControlTaskDependencies(w http.ResponseWriter, r *http.Request) {
	store := h.getTeamStore()
	if store == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "team store not configured"))
		return
	}
	taskID := strings.TrimSpace(mux.Vars(r)["task_id"])
	if taskID == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "task_id is required"))
		return
	}
	includeDependents := parseOptionalBool(r.URL.Query().Get("include_dependents"))
	filter := agentcontrol.TaskDependencyFilter{
		Workflow:          firstNonEmptyString(strings.TrimSpace(r.URL.Query().Get("workflow")), agentcontrol.WorkflowSpawnTeam),
		TeamID:            strings.TrimSpace(r.URL.Query().Get("team_id")),
		TaskID:            taskID,
		DependsOnID:       strings.TrimSpace(r.URL.Query().Get("depends_on_id")),
		IncludeDependents: includeDependents,
	}
	registry := team.NewAgentControlTaskRegistry(store)
	records, err := registry.ListAgentControlTaskDependencies(r.Context(), filter)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	dependencies := make([]string, 0, len(records))
	dependents := make([]string, 0, len(records))
	for _, record := range records {
		if strings.EqualFold(record.TaskID, taskID) && strings.TrimSpace(record.DependsOnID) != "" {
			dependencies = append(dependencies, strings.TrimSpace(record.DependsOnID))
		}
		if strings.EqualFold(record.DependsOnID, taskID) && strings.TrimSpace(record.TaskID) != "" {
			dependents = append(dependents, strings.TrimSpace(record.TaskID))
		}
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"task_id":       taskID,
		"dependencies":  dependencies,
		"dependents":    dependents,
		"edges":         records,
		"count":         len(records),
		"edge_count":    len(records),
		"workflow":      filter.Workflow,
		"team_id":       filter.TeamID,
		"depends_on_id": filter.DependsOnID,
	})
}

// ListTasks lists tasks for a team.
func (h *Handler) ListTasks(w http.ResponseWriter, r *http.Request) {
	store := h.getTeamStore()
	if store == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "team store not configured"))
		return
	}
	teamID := mux.Vars(r)["id"]
	statuses, err := parseTaskStatuses(r.URL.Query().Get("status"))
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	limit, err := parseOptionalLimit(r.URL.Query().Get("limit"))
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	assignee := strings.TrimSpace(r.URL.Query().Get("assignee"))
	var assigneePtr *string
	if assignee != "" {
		assigneePtr = &assignee
	}
	taskIDs := parseIDList(r.URL.Query().Get("task_ids"))
	parentTaskParam := r.URL.Query().Get("parent_task_id")
	var parentTaskID *string
	if strings.TrimSpace(parentTaskParam) != "" {
		value := strings.TrimSpace(parentTaskParam)
		if strings.EqualFold(value, "null") || strings.EqualFold(value, "root") {
			value = ""
		}
		parentTaskID = &value
	}
	includeDeps := parseOptionalBool(r.URL.Query().Get("include_dependencies"))
	includeDependents := parseOptionalBool(r.URL.Query().Get("include_dependents"))
	tasks, err := store.ListTasks(r.Context(), team.TaskFilter{
		TeamID:       teamID,
		Status:       statuses,
		Assignee:     assigneePtr,
		ParentTaskID: parentTaskID,
		Limit:        limit,
	})
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	response := map[string]interface{}{
		"tasks":          tasks,
		"count":          len(tasks),
		"limit":          limit,
		"status":         statuses,
		"assignee":       assigneePtr,
		"parent_task_id": parentTaskID,
		"task_ids":       taskIDs,
	}
	if includeDeps && len(tasks) > 0 {
		depsByTask := make(map[string][]string, len(tasks))
		for _, task := range tasks {
			deps, err := store.ListTaskDependencies(r.Context(), task.ID)
			if err != nil {
				h.writeError(w, http.StatusInternalServerError, err)
				return
			}
			depsByTask[task.ID] = deps
		}
		response["dependencies"] = depsByTask
	}
	if includeDependents && len(tasks) > 0 {
		dependentsByTask := make(map[string][]string, len(tasks))
		for _, task := range tasks {
			dependents, err := store.ListTaskDependents(r.Context(), task.ID)
			if err != nil {
				h.writeError(w, http.StatusInternalServerError, err)
				return
			}
			dependentsByTask[task.ID] = dependents
		}
		response["dependents"] = dependentsByTask
	}
	if len(taskIDs) > 0 {
		allowed := make(map[string]struct{}, len(taskIDs))
		for _, id := range taskIDs {
			allowed[id] = struct{}{}
		}
		filtered := make([]team.Task, 0, len(tasks))
		for _, task := range tasks {
			if _, ok := allowed[task.ID]; ok {
				filtered = append(filtered, task)
			}
		}
		tasks = filtered
		if len(tasks) == 0 {
			h.writeJSON(w, http.StatusOK, map[string]interface{}{
				"tasks":                tasks,
				"count":                0,
				"filter":               taskIDs,
				"include_dependencies": includeDeps,
				"include_dependents":   includeDependents,
				"status":               statuses,
				"assignee":             assigneePtr,
				"parent_task_id":       parentTaskID,
				"task_ids":             taskIDs,
				"limit":                limit,
			})
			return
		}
		response["tasks"] = tasks
		response["count"] = len(tasks)
	}
	h.writeJSON(w, http.StatusOK, response)
}

// PlanTeamTasks asks the lead session to create an initial task plan.
func (h *Handler) PlanTeamTasks(w http.ResponseWriter, r *http.Request) {
	store := h.getTeamStore()
	if store == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "team store not configured"))
		return
	}
	hub := h.getSessionHub()
	if hub == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "session hub not configured"))
		return
	}
	teamID := mux.Vars(r)["id"]
	teamRecord, err := store.GetTeam(r.Context(), teamID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	if teamRecord == nil {
		h.writeError(w, http.StatusNotFound, errors.New(errors.ErrValidationFailed, "team not found"))
		return
	}
	var req struct {
		Goal        string `json:"goal,omitempty"`
		AutoPersist bool   `json:"auto_persist,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "failed to parse request body"))
		return
	}
	goal := strings.TrimSpace(req.Goal)
	if goal == "" {
		goal = strings.TrimSpace(r.URL.Query().Get("goal"))
	}
	if goal == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "goal is required"))
		return
	}
	autoPersist := req.AutoPersist
	if raw := strings.TrimSpace(r.URL.Query().Get("auto_persist")); raw != "" {
		autoPersist = parseOptionalBool(raw)
	}

	planner := &team.LeadPlanner{
		Sessions:    &sessionActorClient{hub: hub},
		Store:       store,
		AutoPersist: autoPersist,
	}
	planResult, err := planner.InitialPlan(r.Context(), *teamRecord, goal)
	if err != nil {
		traceID, requestID := runtimeTraceIDForRequest(r)
		payload := map[string]interface{}{
			"team_id":      teamID,
			"auto_persist": autoPersist,
			"goal_length":  len(goal),
		}
		if requestID != "" {
			payload["request_id"] = requestID
		}
		resolvedTraceID := h.publishTeamSessionExecutionFailure("team.plan.failed", traceID, payload, err)
		if resolvedTraceID != "" {
			w.Header().Set("X-Trace-ID", resolvedTraceID)
		}
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	tasks := []team.Task{}
	deps := []team.TaskDependency{}
	summary := ""
	if planResult != nil {
		tasks = planResult.Tasks
		deps = planResult.Dependencies
		summary = strings.TrimSpace(planResult.Summary)
	}
	{
		inputCount := 0
		inputTaskCount := 0
		for _, task := range tasks {
			if len(task.Inputs) > 0 {
				inputTaskCount++
				inputCount += len(task.Inputs)
			}
		}
		traceID, requestID := runtimeTraceIDForRequest(r)
		payload := map[string]interface{}{
			"team_id":          teamID,
			"task_count":       len(tasks),
			"dependency_count": len(deps),
			"auto_persist":     autoPersist,
			"goal_length":      len(goal),
			"input_count":      inputCount,
			"input_task_count": inputTaskCount,
		}
		if requestID != "" {
			payload["request_id"] = requestID
		}
		if summary != "" {
			payload["summary_present"] = true
			payload["summary"] = truncateLine(summary, 240)
		}
		h.publishRuntimeEvent("team.plan.created", traceID, payload)
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"team_id":          teamID,
		"goal":             goal,
		"auto_persist":     autoPersist,
		"tasks":            tasks,
		"dependencies":     deps,
		"task_count":       len(tasks),
		"dependency_count": len(deps),
		"summary":          summary,
	})
}

// GetTaskGraph returns tasks with dependency edges for a team.
func (h *Handler) GetTaskGraph(w http.ResponseWriter, r *http.Request) {
	store := h.getTeamStore()
	if store == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "team store not configured"))
		return
	}
	teamID := mux.Vars(r)["id"]
	statuses, err := parseTaskStatuses(r.URL.Query().Get("status"))
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	limit, err := parseOptionalLimit(r.URL.Query().Get("limit"))
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	assignee := strings.TrimSpace(r.URL.Query().Get("assignee"))
	var assigneePtr *string
	if assignee != "" {
		assigneePtr = &assignee
	}
	parentTaskParam := r.URL.Query().Get("parent_task_id")
	var parentTaskID *string
	if strings.TrimSpace(parentTaskParam) != "" {
		value := strings.TrimSpace(parentTaskParam)
		if strings.EqualFold(value, "null") || strings.EqualFold(value, "root") {
			value = ""
		}
		parentTaskID = &value
	}
	includeExternal := parseOptionalBool(r.URL.Query().Get("include_external"))
	taskIDs := parseIDList(r.URL.Query().Get("task_ids"))

	tasks, err := store.ListTasks(r.Context(), team.TaskFilter{
		TeamID:       teamID,
		Status:       statuses,
		Assignee:     assigneePtr,
		ParentTaskID: parentTaskID,
		Limit:        limit,
	})
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}

	if len(taskIDs) > 0 {
		allowed := make(map[string]struct{}, len(taskIDs))
		for _, id := range taskIDs {
			allowed[id] = struct{}{}
		}
		filtered := make([]team.Task, 0, len(tasks))
		for _, task := range tasks {
			if _, ok := allowed[task.ID]; ok {
				filtered = append(filtered, task)
			}
		}
		tasks = filtered
		if len(tasks) == 0 {
			h.writeJSON(w, http.StatusOK, map[string]interface{}{
				"tasks":                tasks,
				"count":                0,
				"edges":                []map[string]interface{}{},
				"edge_count":           0,
				"missing_dependencies": []string{},
				"task_ids":             taskIDs,
				"status":               statuses,
				"assignee":             assigneePtr,
				"parent_task_id":       parentTaskID,
				"include_external":     includeExternal,
				"limit":                limit,
			})
			return
		}
	}
	taskSet := make(map[string]bool, len(tasks))
	for _, task := range tasks {
		if strings.TrimSpace(task.ID) != "" {
			taskSet[strings.TrimSpace(task.ID)] = true
		}
	}
	type edge struct {
		TaskID      string `json:"task_id"`
		DependsOnID string `json:"depends_on_id"`
	}
	edges := make([]edge, 0)
	missing := make(map[string]bool)
	for _, task := range tasks {
		deps, err := store.ListTaskDependencies(r.Context(), task.ID)
		if err != nil {
			h.writeError(w, http.StatusInternalServerError, err)
			return
		}
		for _, dep := range deps {
			dep = strings.TrimSpace(dep)
			if dep == "" {
				continue
			}
			_, ok := taskSet[dep]
			if ok || includeExternal {
				edges = append(edges, edge{TaskID: task.ID, DependsOnID: dep})
			}
			if !ok {
				missing[dep] = true
			}
		}
	}

	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"tasks":                tasks,
		"count":                len(tasks),
		"edges":                edges,
		"edge_count":           len(edges),
		"missing_dependencies": sortedStringKeys(missing),
		"task_ids":             taskIDs,
		"limit":                limit,
		"include_external":     includeExternal,
		"status":               statuses,
		"assignee":             assigneePtr,
		"parent_task_id":       parentTaskID,
	})
}

// CreateTask creates a new task.
func (h *Handler) CreateTask(w http.ResponseWriter, r *http.Request) {
	store := h.getTeamStore()
	if store == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "team store not configured"))
		return
	}
	teamID := mux.Vars(r)["id"]
	var req struct {
		ID           string   `json:"id,omitempty"`
		ParentTaskID string   `json:"parent_task_id,omitempty"`
		Title        string   `json:"title,omitempty"`
		Goal         string   `json:"goal,omitempty"`
		Status       string   `json:"status,omitempty"`
		Priority     int      `json:"priority,omitempty"`
		Assignee     string   `json:"assignee,omitempty"`
		Inputs       []string `json:"inputs,omitempty"`
		ReadPaths    []string `json:"read_paths,omitempty"`
		WritePaths   []string `json:"write_paths,omitempty"`
		Deliverables []string `json:"deliverables,omitempty"`
		Summary      string   `json:"summary,omitempty"`
		ResultRef    string   `json:"result_ref,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "failed to parse request body"))
		return
	}
	status, err := parseTaskStatus(req.Status)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	taskRegistry := team.NewAgentControlTaskRegistry(store)
	record, err := taskRegistry.CreateAgentControlTask(r.Context(), agentcontrol.TaskCreateRequest{
		ID:           req.ID,
		Workflow:     agentcontrol.WorkflowSpawnTeam,
		TeamID:       teamID,
		ParentTaskID: req.ParentTaskID,
		Title:        req.Title,
		Goal:         req.Goal,
		Status:       string(status),
		Priority:     req.Priority,
		Assignee:     req.Assignee,
		Inputs:       req.Inputs,
		ReadPaths:    req.ReadPaths,
		WritePaths:   req.WritePaths,
		Deliverables: req.Deliverables,
		Summary:      req.Summary,
		ResultRef:    req.ResultRef,
	})
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	if record == nil {
		h.writeError(w, http.StatusInternalServerError, errors.New(errors.ErrAPIServerError, "task creation did not return a record"))
		return
	}
	created, _ := store.GetTask(r.Context(), record.ID)
	if created == nil && record != nil {
		created = &team.Task{
			ID:       record.ID,
			TeamID:   record.TeamID,
			Title:    record.Title,
			Status:   team.TaskStatus(record.Status),
			Priority: record.Priority,
			Summary:  record.Summary,
		}
	}
	{
		traceID, requestID := runtimeTraceIDForRequest(r)
		payload := map[string]interface{}{
			"team_id":     teamID,
			"task_id":     created.ID,
			"status":      created.Status,
			"priority":    created.Priority,
			"assignee":    created.Assignee,
			"read_count":  len(created.ReadPaths),
			"write_count": len(created.WritePaths),
		}
		if requestID != "" {
			payload["request_id"] = requestID
		}
		h.publishRuntimeEvent("team.task.created", traceID, payload)
	}
	h.writeJSON(w, http.StatusCreated, map[string]interface{}{
		"task": created,
	})
}

// GetTask returns a task by id.
func (h *Handler) GetTask(w http.ResponseWriter, r *http.Request) {
	store := h.getTeamStore()
	if store == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "team store not configured"))
		return
	}
	vars := mux.Vars(r)
	teamID := vars["id"]
	taskID := vars["task_id"]
	item, err := store.GetTask(r.Context(), taskID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	if item == nil || item.TeamID != teamID {
		h.writeError(w, http.StatusNotFound, errors.New(errors.ErrValidationFailed, "task not found"))
		return
	}
	response := map[string]interface{}{
		"task": item,
	}
	if parseOptionalBool(r.URL.Query().Get("include_dependencies")) {
		deps, err := store.ListTaskDependencies(r.Context(), taskID)
		if err != nil {
			h.writeError(w, http.StatusInternalServerError, err)
			return
		}
		response["dependencies"] = deps
	}
	if parseOptionalBool(r.URL.Query().Get("include_dependents")) {
		dependents, err := store.ListTaskDependents(r.Context(), taskID)
		if err != nil {
			h.writeError(w, http.StatusInternalServerError, err)
			return
		}
		response["dependents"] = dependents
	}
	h.writeJSON(w, http.StatusOK, response)
}

// UpdateTask patches a task.
func (h *Handler) UpdateTask(w http.ResponseWriter, r *http.Request) {
	store := h.getTeamStore()
	if store == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "team store not configured"))
		return
	}
	vars := mux.Vars(r)
	teamID := vars["id"]
	taskID := vars["task_id"]
	current, err := store.GetTask(r.Context(), taskID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	if current == nil || current.TeamID != teamID {
		h.writeError(w, http.StatusNotFound, errors.New(errors.ErrValidationFailed, "task not found"))
		return
	}
	var req struct {
		ParentTaskID *string  `json:"parent_task_id,omitempty"`
		Title        *string  `json:"title,omitempty"`
		Goal         *string  `json:"goal,omitempty"`
		Status       *string  `json:"status,omitempty"`
		Priority     *int     `json:"priority,omitempty"`
		Assignee     *string  `json:"assignee,omitempty"`
		Inputs       []string `json:"inputs,omitempty"`
		ReadPaths    []string `json:"read_paths,omitempty"`
		WritePaths   []string `json:"write_paths,omitempty"`
		Deliverables []string `json:"deliverables,omitempty"`
		Summary      *string  `json:"summary,omitempty"`
		ResultRef    *string  `json:"result_ref,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "failed to parse request body"))
		return
	}
	if req.ParentTaskID != nil {
		value := strings.TrimSpace(*req.ParentTaskID)
		if value == "" {
			current.ParentTaskID = nil
		} else {
			current.ParentTaskID = &value
		}
	}
	if req.Title != nil {
		current.Title = strings.TrimSpace(*req.Title)
	}
	if req.Goal != nil {
		current.Goal = strings.TrimSpace(*req.Goal)
	}
	if req.Status != nil {
		status, err := parseTaskStatus(*req.Status)
		if err != nil {
			h.writeError(w, http.StatusBadRequest, err)
			return
		}
		current.Status = status
	}
	if req.Priority != nil {
		current.Priority = *req.Priority
	}
	if req.Assignee != nil {
		value := strings.TrimSpace(*req.Assignee)
		if value == "" {
			current.Assignee = nil
		} else {
			current.Assignee = &value
		}
	}
	if req.Inputs != nil {
		current.Inputs = req.Inputs
	}
	if req.ReadPaths != nil {
		current.ReadPaths = req.ReadPaths
	}
	if req.WritePaths != nil {
		current.WritePaths = req.WritePaths
	}
	if req.Deliverables != nil {
		current.Deliverables = req.Deliverables
	}
	if req.Summary != nil {
		current.Summary = strings.TrimSpace(*req.Summary)
	}
	if req.ResultRef != nil {
		value := strings.TrimSpace(*req.ResultRef)
		if value == "" {
			current.ResultRef = nil
		} else {
			current.ResultRef = &value
		}
	}
	if err := store.UpdateTask(r.Context(), *current); err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	{
		traceID, requestID := runtimeTraceIDForRequest(r)
		payload := map[string]interface{}{
			"team_id":     teamID,
			"task_id":     current.ID,
			"status":      current.Status,
			"priority":    current.Priority,
			"assignee":    current.Assignee,
			"read_count":  len(current.ReadPaths),
			"write_count": len(current.WritePaths),
		}
		if requestID != "" {
			payload["request_id"] = requestID
		}
		h.publishRuntimeEvent("team.task.updated", traceID, payload)
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"task": current,
	})
}

// AddTaskDependency attaches a dependency to a task.
func (h *Handler) AddTaskDependency(w http.ResponseWriter, r *http.Request) {
	store := h.getTeamStore()
	if store == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "team store not configured"))
		return
	}
	vars := mux.Vars(r)
	teamID := vars["id"]
	taskID := vars["task_id"]
	current, err := store.GetTask(r.Context(), taskID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	if current == nil || current.TeamID != teamID {
		h.writeError(w, http.StatusNotFound, errors.New(errors.ErrValidationFailed, "task not found"))
		return
	}
	var req struct {
		DependsOnID string `json:"depends_on_id,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "failed to parse request body"))
		return
	}
	if strings.TrimSpace(req.DependsOnID) == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "depends_on_id is required"))
		return
	}
	registry := team.NewAgentControlTaskRegistry(store)
	dependsOnID := strings.TrimSpace(req.DependsOnID)
	if err := registry.CreateAgentControlTaskDependency(r.Context(), agentcontrol.TaskDependencyCreateRequest{
		Workflow:    agentcontrol.WorkflowSpawnTeam,
		TeamID:      teamID,
		TaskID:      taskID,
		DependsOnID: dependsOnID,
	}); err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	{
		traceID, requestID := runtimeTraceIDForRequest(r)
		payload := map[string]interface{}{
			"team_id":       teamID,
			"task_id":       taskID,
			"depends_on_id": dependsOnID,
		}
		if requestID != "" {
			payload["request_id"] = requestID
		}
		h.publishRuntimeEvent("team.task.dependency_added", traceID, payload)
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"task_id":       taskID,
		"depends_on_id": dependsOnID,
	})
}

// ListTaskDependencies lists dependency ids for a task.
func (h *Handler) ListTaskDependencies(w http.ResponseWriter, r *http.Request) {
	store := h.getTeamStore()
	if store == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "team store not configured"))
		return
	}
	vars := mux.Vars(r)
	teamID := vars["id"]
	taskID := vars["task_id"]
	current, err := store.GetTask(r.Context(), taskID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	if current == nil || current.TeamID != teamID {
		h.writeError(w, http.StatusNotFound, errors.New(errors.ErrValidationFailed, "task not found"))
		return
	}
	deps, err := store.ListTaskDependencies(r.Context(), taskID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"task_id":      taskID,
		"dependencies": deps,
		"count":        len(deps),
	})
}

// ListTaskDependents lists task ids that depend on a task.
func (h *Handler) ListTaskDependents(w http.ResponseWriter, r *http.Request) {
	store := h.getTeamStore()
	if store == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "team store not configured"))
		return
	}
	vars := mux.Vars(r)
	teamID := vars["id"]
	taskID := vars["task_id"]
	current, err := store.GetTask(r.Context(), taskID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	if current == nil || current.TeamID != teamID {
		h.writeError(w, http.StatusNotFound, errors.New(errors.ErrValidationFailed, "task not found"))
		return
	}
	dependents, err := store.ListTaskDependents(r.Context(), taskID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"task_id":    taskID,
		"dependents": dependents,
		"count":      len(dependents),
	})
}

// ClaimReadyTasks claims ready tasks using the orchestrator.
func (h *Handler) ClaimReadyTasks(w http.ResponseWriter, r *http.Request) {
	store := h.getTeamStore()
	if store == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "team store not configured"))
		return
	}
	orchestrator := h.getTeamOrchestrator()
	if orchestrator == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "team orchestrator not configured"))
		return
	}
	teamID := mux.Vars(r)["id"]
	var req struct {
		Limit int `json:"limit,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "failed to parse request body"))
		return
	}
	limit := req.Limit
	if limit <= 0 {
		if parsed, err := parseOptionalLimit(r.URL.Query().Get("limit")); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	assignments, err := orchestrator.ClaimReadyTasks(r.Context(), teamID, limit)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	if len(assignments) > 0 {
		traceID, requestID := runtimeTraceIDForRequest(r)
		payload := map[string]interface{}{
			"team_id":          teamID,
			"assignment_count": len(assignments),
			"assignments":      summarizeAssignments(assignments),
		}
		if requestID != "" {
			payload["request_id"] = requestID
		}
		h.publishRuntimeEvent("team.task.claimed", traceID, payload)
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"assignments": assignments,
		"count":       len(assignments),
	})
}

// ReclaimExpiredTasks releases running tasks with expired leases.
func (h *Handler) ReclaimExpiredTasks(w http.ResponseWriter, r *http.Request) {
	store := h.getTeamStore()
	if store == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "team store not configured"))
		return
	}
	orchestrator := h.getTeamOrchestrator()
	if orchestrator == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "team orchestrator not configured"))
		return
	}
	teamID := mux.Vars(r)["id"]
	var req struct {
		Limit  int    `json:"limit,omitempty"`
		DryRun bool   `json:"dry_run,omitempty"`
		AsOf   string `json:"as_of,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "failed to parse request body"))
		return
	}
	limit := req.Limit
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := parseOptionalLimit(raw)
		if err != nil {
			h.writeError(w, http.StatusBadRequest, err)
			return
		}
		if parsed > 0 {
			limit = parsed
		}
	}
	dryRun := req.DryRun
	if raw := strings.TrimSpace(r.URL.Query().Get("dry_run")); raw != "" {
		dryRun = parseOptionalBool(raw)
	}
	asOf := time.Now().UTC()
	if raw := strings.TrimSpace(req.AsOf); raw != "" {
		parsed, err := parseTimeValue(raw)
		if err != nil {
			h.writeError(w, http.StatusBadRequest, err)
			return
		}
		asOf = parsed
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("as_of")); raw != "" {
		parsed, err := parseTimeValue(raw)
		if err != nil {
			h.writeError(w, http.StatusBadRequest, err)
			return
		}
		asOf = parsed
	}
	reclaimed, err := orchestrator.ReclaimExpiredTasks(r.Context(), teamID, asOf, limit, dryRun)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	{
		traceID, requestID := runtimeTraceIDForRequest(r)
		payload := map[string]interface{}{
			"team_id": teamID,
			"as_of":   asOf,
			"dry_run": dryRun,
			"count":   len(reclaimed),
			"reason":  "lease_expired",
		}
		if requestID != "" {
			payload["request_id"] = requestID
		}
		h.publishRuntimeEvent("team.task.reclaimed", traceID, payload)
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"team_id":   teamID,
		"as_of":     asOf,
		"dry_run":   dryRun,
		"reclaimed": reclaimed,
		"count":     len(reclaimed),
	})
}

// CompleteTask marks a task as done and releases claims.
func (h *Handler) CompleteTask(w http.ResponseWriter, r *http.Request) {
	h.updateTaskOutcome(w, r, team.TaskStatusDone, team.TaskOutcomeDone)
}

// FailTask marks a task as failed and releases claims.
func (h *Handler) FailTask(w http.ResponseWriter, r *http.Request) {
	h.updateTaskOutcome(w, r, team.TaskStatusFailed, team.TaskOutcomeFailed)
}

// ReportTaskOutcome reports a structured terminal or blocked task outcome through a single endpoint.
func (h *Handler) ReportTaskOutcome(w http.ResponseWriter, r *http.Request) {
	h.handleTaskOutcome(w, r, taskOutcomeHandlerOptions{
		defaultStatus:     "",
		requireStructured: true,
		allowed: []team.TaskOutcomeStatus{
			team.TaskOutcomeDone,
			team.TaskOutcomeFailed,
			team.TaskOutcomeBlocked,
			team.TaskOutcomeHandoff,
		},
	})
}

// BlockTask marks a task as blocked, releases claims, optionally notifies the lead, and can trigger replanning.
func (h *Handler) BlockTask(w http.ResponseWriter, r *http.Request) {
	h.handleTaskOutcome(w, r, taskOutcomeHandlerOptions{
		defaultStatus:     team.TaskOutcomeBlocked,
		requireStructured: false,
		requireSessionHub: true,
		allowed: []team.TaskOutcomeStatus{
			team.TaskOutcomeBlocked,
			team.TaskOutcomeHandoff,
		},
	})
}

type taskOutcomeHandlerOptions struct {
	defaultStatus     team.TaskOutcomeStatus
	requireStructured bool
	requireSessionHub bool
	allowed           []team.TaskOutcomeStatus
}

type taskOutcomeRequest struct {
	Outcome    team.TaskOutcomeContract
	TeammateID string
	ResultRef  *string
	NotifyLead *bool
	AutoReplan *bool
	Structured bool
}

type rawTaskOutcomeRequest struct {
	TaskStatus string  `json:"task_status,omitempty"`
	Summary    string  `json:"summary,omitempty"`
	Blocker    string  `json:"blocker,omitempty"`
	HandoffTo  string  `json:"handoff_to,omitempty"`
	TeammateID string  `json:"teammate_id,omitempty"`
	ResultRef  *string `json:"result_ref,omitempty"`
	NotifyLead *bool   `json:"notify_lead,omitempty"`
	AutoReplan *bool   `json:"auto_replan,omitempty"`
}

func decodeTaskOutcomeRequest(body io.Reader, defaultStatus team.TaskOutcomeStatus) (taskOutcomeRequest, error) {
	req := rawTaskOutcomeRequest{}
	if err := json.NewDecoder(body).Decode(&req); err != nil && err != io.EOF {
		return taskOutcomeRequest{}, errors.New(errors.ErrValidationFailed, "failed to parse request body")
	}

	result := taskOutcomeRequest{
		Outcome: team.TaskOutcomeContract{
			Status:  defaultStatus,
			Summary: strings.TrimSpace(req.Summary),
		},
		TeammateID: strings.TrimSpace(req.TeammateID),
		ResultRef:  normalizeOptionalString(req.ResultRef),
		NotifyLead: req.NotifyLead,
		AutoReplan: req.AutoReplan,
	}
	outcome, structured, err := team.NormalizeTaskOutcomeContract(defaultStatus, team.TaskOutcomeContract{
		Status:    team.TaskOutcomeStatus(req.TaskStatus),
		Summary:   strings.TrimSpace(req.Summary),
		Blocker:   strings.TrimSpace(req.Blocker),
		HandoffTo: strings.TrimSpace(req.HandoffTo),
	})
	if err != nil {
		return taskOutcomeRequest{}, errors.New(errors.ErrValidationFailed, err.Error())
	}
	result.Outcome = outcome
	result.Structured = structured
	return result, nil
}

func normalizeOptionalString(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	return &trimmed
}

// ReplanTask asks the lead session to replan after a failed task.
func (h *Handler) ReplanTask(w http.ResponseWriter, r *http.Request) {
	store := h.getTeamStore()
	if store == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "team store not configured"))
		return
	}
	hub := h.getSessionHub()
	if hub == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "session hub not configured"))
		return
	}
	vars := mux.Vars(r)
	teamID := vars["id"]
	taskID := vars["task_id"]
	teamRecord, err := store.GetTeam(r.Context(), teamID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	if teamRecord == nil {
		h.writeError(w, http.StatusNotFound, errors.New(errors.ErrValidationFailed, "team not found"))
		return
	}
	failedTask, err := store.GetTask(r.Context(), taskID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	if failedTask == nil || failedTask.TeamID != teamID {
		h.writeError(w, http.StatusNotFound, errors.New(errors.ErrValidationFailed, "task not found"))
		return
	}
	var req struct {
		AutoPersist bool `json:"auto_persist,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "failed to parse request body"))
		return
	}
	autoPersist := req.AutoPersist
	if raw := strings.TrimSpace(r.URL.Query().Get("auto_persist")); raw != "" {
		autoPersist = parseOptionalBool(raw)
	}

	planner := &team.LeadPlanner{
		Sessions:    &sessionActorClient{hub: hub},
		Store:       store,
		AutoPersist: autoPersist,
	}
	planResult, err := planner.ReplanOnFailure(r.Context(), *teamRecord, *failedTask)
	if err != nil {
		traceID, requestID := runtimeTraceIDForRequest(r)
		payload := map[string]interface{}{
			"team_id":      teamID,
			"task_id":      taskID,
			"auto_persist": autoPersist,
		}
		if requestID != "" {
			payload["request_id"] = requestID
		}
		resolvedTraceID := h.publishTeamSessionExecutionFailure("team.plan.replan_failed", traceID, payload, err)
		if resolvedTraceID != "" {
			w.Header().Set("X-Trace-ID", resolvedTraceID)
		}
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	tasks := []team.Task{}
	deps := []team.TaskDependency{}
	summary := ""
	if planResult != nil {
		tasks = planResult.Tasks
		deps = planResult.Dependencies
		summary = strings.TrimSpace(planResult.Summary)
	}
	{
		inputCount := 0
		inputTaskCount := 0
		for _, task := range tasks {
			if len(task.Inputs) > 0 {
				inputTaskCount++
				inputCount += len(task.Inputs)
			}
		}
		traceID, requestID := runtimeTraceIDForRequest(r)
		payload := map[string]interface{}{
			"team_id":          teamID,
			"task_id":          taskID,
			"task_count":       len(tasks),
			"dependency_count": len(deps),
			"auto_persist":     autoPersist,
			"input_count":      inputCount,
			"input_task_count": inputTaskCount,
		}
		if requestID != "" {
			payload["request_id"] = requestID
		}
		if summary != "" {
			payload["summary_present"] = true
			payload["summary"] = truncateLine(summary, 240)
		}
		h.publishRuntimeEvent("team.plan.replanned", traceID, payload)
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"team_id":          teamID,
		"failed_task":      taskID,
		"auto_persist":     autoPersist,
		"tasks":            tasks,
		"dependencies":     deps,
		"task_count":       len(tasks),
		"dependency_count": len(deps),
		"summary":          summary,
	})
}

func (h *Handler) updateTaskOutcome(w http.ResponseWriter, r *http.Request, status team.TaskStatus, outcomeStatus team.TaskOutcomeStatus) {
	_ = status
	h.handleTaskOutcome(w, r, taskOutcomeHandlerOptions{
		defaultStatus:     outcomeStatus,
		requireStructured: false,
		allowed:           []team.TaskOutcomeStatus{outcomeStatus},
	})
}

func (h *Handler) handleTaskOutcome(w http.ResponseWriter, r *http.Request, options taskOutcomeHandlerOptions) {
	store := h.getTeamStore()
	if store == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "team store not configured"))
		return
	}
	vars := mux.Vars(r)
	teamID := vars["id"]
	taskID := vars["task_id"]
	current, err := store.GetTask(r.Context(), taskID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	if current == nil || current.TeamID != teamID {
		h.writeError(w, http.StatusNotFound, errors.New(errors.ErrValidationFailed, "task not found"))
		return
	}
	req, err := decodeTaskOutcomeRequest(r.Body, options.defaultStatus)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	if options.requireStructured && (!req.Structured || req.Outcome.Status == "") {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "task_status is required"))
		return
	}
	if len(options.allowed) > 0 {
		if err := team.ValidateAllowedTaskOutcomeStatus(req.Outcome, options.allowed...); err != nil {
			h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
			return
		}
	}

	switch req.Outcome.Status {
	case team.TaskOutcomeDone, team.TaskOutcomeFailed:
		applyOutcome := req.Outcome
		if !req.Structured {
			applyOutcome.Status = ""
		}
		result, err := team.ApplyTerminalTaskOutcome(r.Context(), team.TaskOutcomeApplyServices{
			Store:  store,
			Claims: h.getTeamClaimsManager(),
			Events: h.getTeamEvents(),
		}, team.TerminalTaskOutcomeRequest{
			Task:          *current,
			TeammateID:    req.TeammateID,
			Outcome:       applyOutcome,
			ResultRef:     req.ResultRef,
			DefaultStatus: req.Outcome.Status,
		})
		if err != nil {
			h.writeError(w, http.StatusInternalServerError, err)
			return
		}
		var planner *team.LeadPlanner
		if hub := h.getSessionHub(); hub != nil {
			planner = &team.LeadPlanner{
				Sessions: &sessionActorClient{hub: hub},
				Store:    store,
				Mailbox:  team.NewMailboxService(store),
			}
		}
		if _, err := team.ReconcileTerminalTeamState(r.Context(), team.TerminalTeamServices{
			Store:   store,
			Planner: planner,
			Mailbox: team.NewMailboxService(store),
			Events:  h.getTeamEvents(),
		}, teamID); err != nil {
			if team.IsSQLiteLockError(err) {
				if lifecycle := h.teamLifecycleService(); lifecycle != nil {
					go lifecycle.SyncLoops()
				}
			} else {
				h.writeError(w, http.StatusInternalServerError, err)
				return
			}
		}
		status := result.Status
		{
			traceID, requestID := runtimeTraceIDForRequest(r)
			payload := map[string]interface{}{
				"team_id":     teamID,
				"task_id":     taskID,
				"status":      status,
				"summary":     result.Summary,
				"teammate_id": strings.TrimSpace(req.TeammateID),
			}
			if result.Task.ResultRef != nil {
				payload["result_ref"] = *result.Task.ResultRef
			}
			if requestID != "" {
				payload["request_id"] = requestID
			}
			eventType := "team.task.completed"
			if status == team.TaskStatusFailed {
				eventType = "team.task.failed"
			}
			h.publishRuntimeEvent(eventType, traceID, payload)
		}
		h.writeJSON(w, http.StatusOK, map[string]interface{}{
			"task": result.Task,
		})
		return

	case team.TaskOutcomeBlocked, team.TaskOutcomeHandoff:
		hub := h.getSessionHub()
		if options.requireSessionHub && hub == nil {
			h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "session hub not configured"))
			return
		}
		teamRecord, err := store.GetTeam(r.Context(), teamID)
		if err != nil {
			h.writeError(w, http.StatusInternalServerError, err)
			return
		}
		if teamRecord == nil {
			h.writeError(w, http.StatusNotFound, errors.New(errors.ErrValidationFailed, "team not found"))
			return
		}
		var planner *team.LeadPlanner
		if hub != nil {
			planner = &team.LeadPlanner{
				Sessions:    &sessionActorClient{hub: hub},
				Store:       store,
				Mailbox:     team.NewMailboxService(store),
				AutoPersist: true,
			}
		}
		applyOutcome := req.Outcome
		if !req.Structured {
			applyOutcome.Status = ""
		}
		result, err := team.ApplyBlockedTaskOutcome(r.Context(), team.TaskOutcomeApplyServices{
			Store:   store,
			Claims:  h.getTeamClaimsManager(),
			Mailbox: team.NewMailboxService(store),
			Planner: planner,
		}, team.BlockedTaskOutcomeRequest{
			Team:            *teamRecord,
			Task:            *current,
			TeammateID:      req.TeammateID,
			Outcome:         applyOutcome,
			NotifyRecipient: req.NotifyLead,
			AutoReplan:      req.AutoReplan,
		})
		if err != nil {
			h.writeError(w, http.StatusInternalServerError, err)
			return
		}
		if result.Message != nil {
			_ = h.DispatchTeamMailboxMessage(r.Context(), *result.Message)
		}
		messageID := ""
		if result.Message != nil {
			messageID = result.Message.ID
		}
		blockedBy := strings.TrimSpace(req.TeammateID)
		if blockedBy == "" && current.Assignee != nil {
			blockedBy = strings.TrimSpace(*current.Assignee)
		}
		{
			traceID, requestID := runtimeTraceIDForRequest(r)
			payload := map[string]interface{}{
				"team_id":     teamID,
				"task_id":     taskID,
				"summary":     result.Summary,
				"teammate_id": blockedBy,
				"message_id":  messageID,
				"auto_replan": result.AutoReplan,
			}
			if result.HandoffTo != "" {
				payload["handoff_to"] = result.HandoffTo
			}
			if result.ReplanError != "" {
				payload["replan_error"] = result.ReplanError
				appendPrefixedSessionExecutionMetadata(payload, "replan", result.ReplanTraceID, result.ReplanErrorType, result.ReplanErrorMetadata)
			}
			if result.PlanResult != nil {
				payload["planned_tasks"] = len(result.PlanResult.Tasks)
				payload["planned_dependencies"] = len(result.PlanResult.Dependencies)
			}
			if requestID != "" {
				payload["request_id"] = requestID
			}
			h.publishRuntimeEvent("team.task.blocked", traceID, payload)
		}
		response := map[string]interface{}{
			"task":         result.Task,
			"message_id":   messageID,
			"auto_replan":  result.AutoReplan,
			"replan_error": result.ReplanError,
		}
		if result.ReplanError != "" {
			appendPrefixedSessionExecutionMetadata(response, "replan", result.ReplanTraceID, result.ReplanErrorType, result.ReplanErrorMetadata)
		}
		if result.HandoffTo != "" {
			response["handoff_to"] = result.HandoffTo
		}
		if result.PlanResult != nil {
			response["planned_tasks"] = result.PlanResult.Tasks
			response["planned_dependencies"] = result.PlanResult.Dependencies
			response["planned_summary"] = result.PlanResult.Summary
		}
		h.writeJSON(w, http.StatusOK, response)
		return
	}

	h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "unsupported task_status"))
}

// MarkReadyTasks marks pending tasks as ready when dependencies are satisfied.
func (h *Handler) MarkReadyTasks(w http.ResponseWriter, r *http.Request) {
	store := h.getTeamStore()
	if store == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "team store not configured"))
		return
	}
	teamID := mux.Vars(r)["id"]
	count, err := store.MarkReadyTasks(r.Context(), teamID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	{
		traceID, requestID := runtimeTraceIDForRequest(r)
		payload := map[string]interface{}{
			"team_id": teamID,
			"count":   count,
		}
		if requestID != "" {
			payload["request_id"] = requestID
		}
		h.publishRuntimeEvent("team.task.ready_marked", traceID, payload)
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"team_id": teamID,
		"count":   count,
	})
}

// RenewTaskLease extends the lease for a running task.
func (h *Handler) RenewTaskLease(w http.ResponseWriter, r *http.Request) {
	store := h.getTeamStore()
	if store == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "team store not configured"))
		return
	}
	vars := mux.Vars(r)
	teamID := vars["id"]
	taskID := vars["task_id"]
	current, err := store.GetTask(r.Context(), taskID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	if current == nil || current.TeamID != teamID {
		h.writeError(w, http.StatusNotFound, errors.New(errors.ErrValidationFailed, "task not found"))
		return
	}
	var req struct {
		LeaseUntil  string `json:"lease_until,omitempty"`
		DurationSec int    `json:"duration_sec,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "failed to parse request body"))
		return
	}

	leaseUntil, err := resolveLeaseUntil(req.LeaseUntil, req.DurationSec, h.defaultLeaseDuration())
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}

	taskRegistry := team.NewAgentControlTaskRegistry(store)
	if _, err := taskRegistry.RenewAgentControlTaskLease(r.Context(), agentcontrol.TaskLeaseRenewRequest{
		ID:         taskID,
		Workflow:   agentcontrol.WorkflowSpawnTeam,
		LeaseUntil: leaseUntil,
	}); err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	if claims := h.getTeamClaimsManager(); claims != nil {
		_ = claims.Renew(r.Context(), taskID, leaseUntil)
	}
	updated, _ := store.GetTask(r.Context(), taskID)
	if updated == nil {
		updated = current
		updated.LeaseUntil = &leaseUntil
	}
	{
		traceID, requestID := runtimeTraceIDForRequest(r)
		payload := map[string]interface{}{
			"team_id":     teamID,
			"task_id":     taskID,
			"lease_until": leaseUntil,
			"assignee":    updated.Assignee,
		}
		if requestID != "" {
			payload["request_id"] = requestID
		}
		h.publishRuntimeEvent("team.task.lease_renewed", traceID, payload)
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"task": updated,
	})
}

// ReleaseTaskLease releases a task lease and resets status.
func (h *Handler) ReleaseTaskLease(w http.ResponseWriter, r *http.Request) {
	store := h.getTeamStore()
	if store == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "team store not configured"))
		return
	}
	vars := mux.Vars(r)
	teamID := vars["id"]
	taskID := vars["task_id"]
	current, err := store.GetTask(r.Context(), taskID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	if current == nil || current.TeamID != teamID {
		h.writeError(w, http.StatusNotFound, errors.New(errors.ErrValidationFailed, "task not found"))
		return
	}
	var req struct {
		Status     string `json:"status,omitempty"`
		Summary    string `json:"summary,omitempty"`
		TeammateID string `json:"teammate_id,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "failed to parse request body"))
		return
	}
	status, err := parseTaskStatus(req.Status)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	if status == "" {
		status = team.TaskStatusPending
	}
	taskRegistry := team.NewAgentControlTaskRegistry(store)
	released, err := taskRegistry.ReleaseAgentControlTask(r.Context(), agentcontrol.TaskReleaseRequest{
		ID:       taskID,
		Workflow: agentcontrol.WorkflowSpawnTeam,
		Status:   string(status),
		Summary:  strings.TrimSpace(req.Summary),
	})
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	if claims := h.getTeamClaimsManager(); claims != nil {
		_ = claims.Release(r.Context(), taskID)
	}
	if strings.TrimSpace(req.TeammateID) != "" {
		_ = store.UpdateTeammateState(r.Context(), strings.TrimSpace(req.TeammateID), team.TeammateStateIdle)
	}
	updated, _ := store.GetTask(r.Context(), taskID)
	if updated == nil {
		updated = current
		updated.Status = status
		if released != nil {
			updated.Summary = released.Summary
		}
	}
	{
		traceID, requestID := runtimeTraceIDForRequest(r)
		payload := map[string]interface{}{
			"team_id":     teamID,
			"task_id":     taskID,
			"status":      status,
			"summary":     strings.TrimSpace(req.Summary),
			"teammate_id": strings.TrimSpace(req.TeammateID),
		}
		if requestID != "" {
			payload["request_id"] = requestID
		}
		h.publishRuntimeEvent("team.task.released", traceID, payload)
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"task": updated,
	})
}

// RetryTask releases a task lease and increments retry count.
func (h *Handler) RetryTask(w http.ResponseWriter, r *http.Request) {
	store := h.getTeamStore()
	if store == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "team store not configured"))
		return
	}
	vars := mux.Vars(r)
	teamID := vars["id"]
	taskID := vars["task_id"]
	current, err := store.GetTask(r.Context(), taskID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	if current == nil || current.TeamID != teamID {
		h.writeError(w, http.StatusNotFound, errors.New(errors.ErrValidationFailed, "task not found"))
		return
	}
	var req struct {
		Status     string `json:"status,omitempty"`
		Summary    string `json:"summary,omitempty"`
		TeammateID string `json:"teammate_id,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "failed to parse request body"))
		return
	}
	status, err := parseTaskStatus(req.Status)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	if status == "" {
		status = team.TaskStatusPending
	}
	if err := store.IncrementTaskRetry(r.Context(), taskID); err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	if err := store.ReleaseTask(r.Context(), taskID, status); err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	if summary := strings.TrimSpace(req.Summary); summary != "" {
		_ = store.UpdateTaskStatus(r.Context(), taskID, status, summary)
	}
	if claims := h.getTeamClaimsManager(); claims != nil {
		_ = claims.Release(r.Context(), taskID)
	}
	if strings.TrimSpace(req.TeammateID) != "" {
		_ = store.UpdateTeammateState(r.Context(), strings.TrimSpace(req.TeammateID), team.TeammateStateIdle)
	}
	updated, _ := store.GetTask(r.Context(), taskID)
	if updated == nil {
		updated = current
		updated.Status = status
	}
	{
		traceID, requestID := runtimeTraceIDForRequest(r)
		payload := map[string]interface{}{
			"team_id":     teamID,
			"task_id":     taskID,
			"status":      status,
			"summary":     strings.TrimSpace(req.Summary),
			"teammate_id": strings.TrimSpace(req.TeammateID),
		}
		if requestID != "" {
			payload["request_id"] = requestID
		}
		h.publishRuntimeEvent("team.task.retried", traceID, payload)
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"task": updated,
	})
}

// SendMailboxMessage inserts a mailbox message.
func (h *Handler) SendMailboxMessage(w http.ResponseWriter, r *http.Request) {
	store := h.getTeamStore()
	if store == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "team store not configured"))
		return
	}
	teamID := mux.Vars(r)["id"]
	var req struct {
		FromAgent string                 `json:"from_agent,omitempty"`
		ToAgent   string                 `json:"to_agent,omitempty"`
		TaskID    string                 `json:"task_id,omitempty"`
		Kind      string                 `json:"kind,omitempty"`
		Body      string                 `json:"body,omitempty"`
		Metadata  map[string]interface{} `json:"metadata,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "failed to parse request body"))
		return
	}
	if strings.TrimSpace(req.Body) == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "body is required"))
		return
	}
	var taskID *string
	if strings.TrimSpace(req.TaskID) != "" {
		value := strings.TrimSpace(req.TaskID)
		taskID = &value
	}
	message := team.MailMessage{
		TeamID:    teamID,
		FromAgent: strings.TrimSpace(req.FromAgent),
		ToAgent:   strings.TrimSpace(req.ToAgent),
		TaskID:    taskID,
		Kind:      strings.TrimSpace(req.Kind),
		Body:      req.Body,
		Metadata:  req.Metadata,
	}
	id, err := store.InsertMail(r.Context(), message)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	message.ID = id
	dispatchError := ""
	if err := h.DispatchTeamMailboxMessage(r.Context(), message); err != nil {
		dispatchError = err.Error()
	}
	{
		traceID, requestID := runtimeTraceIDForRequest(r)
		payload := map[string]interface{}{
			"team_id":    teamID,
			"message_id": id,
			"from_agent": message.FromAgent,
			"to_agent":   message.ToAgent,
			"task_id":    message.TaskID,
			"kind":       message.Kind,
		}
		if requestID != "" {
			payload["request_id"] = requestID
		}
		if dispatchError != "" {
			payload["dispatch_error"] = dispatchError
		}
		h.publishRuntimeEvent("team.mailbox.sent", traceID, payload)
	}
	h.writeJSON(w, http.StatusCreated, map[string]interface{}{
		"message":        message,
		"dispatch_error": dispatchError,
	})
}

// ListMailbox lists mailbox messages.
func (h *Handler) ListMailbox(w http.ResponseWriter, r *http.Request) {
	store := h.getTeamStore()
	if store == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "team store not configured"))
		return
	}
	teamID := mux.Vars(r)["id"]
	limit, err := parseOptionalLimit(r.URL.Query().Get("limit"))
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	unreadOnly := parseOptionalBool(r.URL.Query().Get("unread_only"))
	markRead := parseOptionalBool(r.URL.Query().Get("mark_read"))
	since, err := parseOptionalTime(r.URL.Query().Get("since"))
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	rawAfter := strings.TrimSpace(firstNonEmptyString(r.URL.Query().Get("after_seq"), r.URL.Query().Get("after")))
	afterSeq := int64(0)
	if rawAfter != "" {
		value, err := strconv.ParseInt(rawAfter, 10, 64)
		if err != nil || value < 0 {
			h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "invalid after_seq value"))
			return
		}
		afterSeq = value
	}
	includeBroadcast := parseOptionalBool(r.URL.Query().Get("include_broadcast"))
	taskIDFilter := strings.TrimSpace(r.URL.Query().Get("task_id"))
	parentTaskParam := strings.TrimSpace(r.URL.Query().Get("parent_task_id"))
	if taskIDFilter != "" {
		parentTaskParam = ""
	}
	var parentTaskID *string
	if parentTaskParam != "" {
		value := parentTaskParam
		if strings.EqualFold(value, "null") || strings.EqualFold(value, "root") {
			value = ""
		}
		parentTaskID = &value
	}
	filter := team.MailFilter{
		TeamID:           teamID,
		FromAgent:        r.URL.Query().Get("from_agent"),
		ToAgent:          r.URL.Query().Get("to_agent"),
		TaskID:           taskIDFilter,
		Kind:             r.URL.Query().Get("kind"),
		UnreadOnly:       unreadOnly,
		IncludeBroadcast: includeBroadcast,
		AfterSeq:         afterSeq,
		Since:            since,
		Limit:            limit,
	}
	markReadAgent := strings.TrimSpace(r.URL.Query().Get("agent_id"))
	if markReadAgent == "" {
		toAgent := strings.TrimSpace(filter.ToAgent)
		if toAgent != "" && toAgent != "*" {
			markReadAgent = toAgent
		}
	}
	if markRead && markReadAgent == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "agent_id is required when mark_read=true"))
		return
	}
	messages := []team.MailMessage{}
	if parentTaskID != nil {
		tasks, err := store.ListTasks(r.Context(), team.TaskFilter{
			TeamID:       teamID,
			ParentTaskID: parentTaskID,
		})
		if err != nil {
			h.writeError(w, http.StatusInternalServerError, err)
			return
		}
		if len(tasks) == 0 {
			h.writeJSON(w, http.StatusOK, map[string]interface{}{
				"messages":        messages,
				"count":           0,
				"parent_task_id":  parentTaskParam,
				"effective_limit": limit,
				"limit":           limit,
			})
			return
		}
		perTaskLimit := 0
		if limit > 0 {
			perTaskLimit = limit
		}
		for _, task := range tasks {
			taskFilter := filter
			taskFilter.TaskID = task.ID
			taskFilter.Limit = perTaskLimit
			taskMessages, err := store.ListMail(r.Context(), taskFilter)
			if err != nil {
				h.writeError(w, http.StatusInternalServerError, err)
				return
			}
			messages = append(messages, taskMessages...)
		}
		sort.Slice(messages, func(i, j int) bool {
			return messages[i].CreatedAt.After(messages[j].CreatedAt)
		})
		if limit > 0 && len(messages) > limit {
			messages = messages[:limit]
		}
	} else {
		var err error
		messages, err = store.ListMail(r.Context(), filter)
		if err != nil {
			h.writeError(w, http.StatusInternalServerError, err)
			return
		}
	}
	markedRead := false
	if markRead && len(messages) > 0 {
		messageIDs := make([]string, 0, len(messages))
		for _, message := range messages {
			if strings.TrimSpace(message.ID) == "" {
				continue
			}
			messageIDs = append(messageIDs, message.ID)
		}
		if len(messageIDs) > 0 {
			mailbox := team.NewMailboxService(store)
			if err := mailbox.AckByAgent(r.Context(), teamID, markReadAgent, messageIDs); err != nil {
				h.writeError(w, http.StatusInternalServerError, err)
				return
			}
			markedRead = true
		}
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"messages":       messages,
		"count":          len(messages),
		"parent_task_id": parentTaskParam,
		"limit":          limit,
		"marked_read":    markedRead,
		"agent_id":       markReadAgent,
		"filters": map[string]interface{}{
			"task_id":           taskIDFilter,
			"from_agent":        strings.TrimSpace(r.URL.Query().Get("from_agent")),
			"to_agent":          strings.TrimSpace(r.URL.Query().Get("to_agent")),
			"kind":              strings.TrimSpace(r.URL.Query().Get("kind")),
			"unread_only":       unreadOnly,
			"mark_read":         markRead,
			"include_broadcast": includeBroadcast,
			"after_seq":         afterSeq,
			"since":             since,
		},
	})
}

// AckMailboxMessage marks a mailbox message as acknowledged.
func (h *Handler) AckMailboxMessage(w http.ResponseWriter, r *http.Request) {
	store := h.getTeamStore()
	if store == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "team store not configured"))
		return
	}
	vars := mux.Vars(r)
	teamID := vars["id"]
	messageID := vars["message_id"]
	agentID := strings.TrimSpace(r.URL.Query().Get("agent_id"))
	if strings.TrimSpace(messageID) == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "message_id is required"))
		return
	}
	var err error
	if agentID != "" {
		err = store.RecordMailReceipt(r.Context(), team.MailReceipt{
			TeamID:    teamID,
			MessageID: messageID,
			AgentID:   agentID,
			AckedAt:   time.Now().UTC(),
		})
	} else {
		err = store.AckMail(r.Context(), teamID, messageID, time.Now().UTC())
	}
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	{
		traceID, requestID := runtimeTraceIDForRequest(r)
		payload := map[string]interface{}{
			"team_id":    teamID,
			"message_id": messageID,
		}
		if agentID != "" {
			payload["agent_id"] = agentID
		}
		if requestID != "" {
			payload["request_id"] = requestID
		}
		h.publishRuntimeEvent("team.mailbox.acked", traceID, payload)
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"message_id": messageID,
		"team_id":    teamID,
		"agent_id":   agentID,
	})
}

// ListPathClaims lists active path claims for a team.
func (h *Handler) ListPathClaims(w http.ResponseWriter, r *http.Request) {
	store := h.getTeamStore()
	if store == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "team store not configured"))
		return
	}
	teamID := mux.Vars(r)["id"]
	activeOnly := parseOptionalBool(r.URL.Query().Get("active_only"))
	limit, err := parseOptionalLimit(r.URL.Query().Get("limit"))
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	asOf, err := parseOptionalTime(r.URL.Query().Get("as_of"))
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	if asOf == nil {
		now := time.Now().UTC()
		asOf = &now
	}
	taskIDFilter := strings.TrimSpace(r.URL.Query().Get("task_id"))
	ownerFilter := strings.TrimSpace(r.URL.Query().Get("owner_agent_id"))
	modeFilter := strings.TrimSpace(r.URL.Query().Get("mode"))
	var parsedMode team.PathClaimMode
	if modeFilter != "" {
		switch team.PathClaimMode(strings.ToLower(modeFilter)) {
		case team.PathClaimRead, team.PathClaimWrite:
			parsedMode = team.PathClaimMode(strings.ToLower(modeFilter))
		default:
			h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "invalid mode value"))
			return
		}
	}
	claims, err := store.ListPathClaims(r.Context(), teamID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	if activeOnly {
		filtered := make([]team.PathClaim, 0, len(claims))
		for _, claim := range claims {
			if claim.LeaseUntil.IsZero() || !claim.LeaseUntil.Before(*asOf) {
				filtered = append(filtered, claim)
			}
		}
		claims = filtered
	}
	if taskIDFilter != "" || ownerFilter != "" || parsedMode != "" {
		filtered := make([]team.PathClaim, 0, len(claims))
		for _, claim := range claims {
			if taskIDFilter != "" && claim.TaskID != taskIDFilter {
				continue
			}
			if ownerFilter != "" && claim.OwnerAgentID != ownerFilter {
				continue
			}
			if parsedMode != "" && claim.Mode != parsedMode {
				continue
			}
			filtered = append(filtered, claim)
		}
		claims = filtered
	}
	if limit > 0 && len(claims) > limit {
		claims = claims[:limit]
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"claims":      claims,
		"count":       len(claims),
		"active_only": activeOnly,
		"as_of":       asOf,
		"limit":       limit,
		"filters": map[string]interface{}{
			"task_id":        taskIDFilter,
			"owner_agent_id": ownerFilter,
			"mode":           parsedMode,
		},
	})
}

// CheckPathClaims checks for path claim conflicts.
func (h *Handler) CheckPathClaims(w http.ResponseWriter, r *http.Request) {
	store := h.getTeamStore()
	if store == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "team store not configured"))
		return
	}
	teamID := mux.Vars(r)["id"]
	var req struct {
		ReadPaths  []string `json:"read_paths,omitempty"`
		WritePaths []string `json:"write_paths,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "failed to parse request body"))
		return
	}
	claims := h.getTeamClaimsManager()
	if claims == nil {
		claims = team.NewPathClaimManager(store, "")
	}
	ok, conflicts, err := claims.CanClaim(r.Context(), teamID, req.ReadPaths, req.WritePaths)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":        ok,
		"conflicts": conflicts,
	})
}

// PrunePathClaims removes expired path claims for a team.
func (h *Handler) PrunePathClaims(w http.ResponseWriter, r *http.Request) {
	store := h.getTeamStore()
	if store == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "team store not configured"))
		return
	}
	teamID := mux.Vars(r)["id"]
	var req struct {
		AsOf   string `json:"as_of,omitempty"`
		DryRun bool   `json:"dry_run,omitempty"`
		Limit  int    `json:"limit,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "failed to parse request body"))
		return
	}
	asOf := time.Now().UTC()
	if raw := strings.TrimSpace(req.AsOf); raw != "" {
		parsed, err := parseTimeValue(raw)
		if err != nil {
			h.writeError(w, http.StatusBadRequest, err)
			return
		}
		asOf = parsed
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("as_of")); raw != "" {
		parsed, err := parseTimeValue(raw)
		if err != nil {
			h.writeError(w, http.StatusBadRequest, err)
			return
		}
		asOf = parsed
	}
	dryRun := req.DryRun
	if raw := strings.TrimSpace(r.URL.Query().Get("dry_run")); raw != "" {
		dryRun = parseOptionalBool(raw)
	}
	limit := req.Limit
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := parseOptionalLimit(raw)
		if err != nil {
			h.writeError(w, http.StatusBadRequest, err)
			return
		}
		if parsed > 0 {
			limit = parsed
		}
	}

	if dryRun {
		claims, err := store.ListPathClaims(r.Context(), teamID)
		if err != nil {
			h.writeError(w, http.StatusInternalServerError, err)
			return
		}
		expired := make([]team.PathClaim, 0)
		for _, claim := range claims {
			if !claim.LeaseUntil.IsZero() && claim.LeaseUntil.Before(asOf) {
				expired = append(expired, claim)
			}
		}
		if limit > 0 && len(expired) > limit {
			expired = expired[:limit]
		}
		{
			traceID, requestID := runtimeTraceIDForRequest(r)
			payload := map[string]interface{}{
				"team_id": teamID,
				"as_of":   asOf,
				"dry_run": true,
				"count":   len(expired),
			}
			if requestID != "" {
				payload["request_id"] = requestID
			}
			h.publishRuntimeEvent("team.path_claims.prune_checked", traceID, payload)
		}
		h.writeJSON(w, http.StatusOK, map[string]interface{}{
			"team_id": teamID,
			"as_of":   asOf,
			"dry_run": true,
			"claims":  expired,
			"count":   len(expired),
		})
		return
	}

	deleted, err := store.DeleteExpiredPathClaims(r.Context(), teamID, asOf)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	{
		traceID, requestID := runtimeTraceIDForRequest(r)
		payload := map[string]interface{}{
			"team_id": teamID,
			"as_of":   asOf,
			"dry_run": false,
			"count":   deleted,
		}
		if requestID != "" {
			payload["request_id"] = requestID
		}
		h.publishRuntimeEvent("team.path_claims.pruned", traceID, payload)
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"team_id": teamID,
		"as_of":   asOf,
		"dry_run": false,
		"count":   deleted,
	})
}

// SweepTeammates marks teammates offline when heartbeat is stale.
func (h *Handler) SweepTeammates(w http.ResponseWriter, r *http.Request) {
	store := h.getTeamStore()
	if store == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "team store not configured"))
		return
	}
	teamID := mux.Vars(r)["id"]
	var req struct {
		AsOf             string `json:"as_of,omitempty"`
		OfflineAfterSec  int    `json:"offline_after_sec,omitempty"`
		DryRun           bool   `json:"dry_run,omitempty"`
		IncludeNeverSeen bool   `json:"include_never_seen,omitempty"`
		ReclaimTasks     bool   `json:"reclaim_tasks,omitempty"`
		ReclaimStatus    string `json:"reclaim_status,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "failed to parse request body"))
		return
	}
	asOf := time.Now().UTC()
	if raw := strings.TrimSpace(req.AsOf); raw != "" {
		parsed, err := parseTimeValue(raw)
		if err != nil {
			h.writeError(w, http.StatusBadRequest, err)
			return
		}
		asOf = parsed
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("as_of")); raw != "" {
		parsed, err := parseTimeValue(raw)
		if err != nil {
			h.writeError(w, http.StatusBadRequest, err)
			return
		}
		asOf = parsed
	}
	dryRun := req.DryRun
	if raw := strings.TrimSpace(r.URL.Query().Get("dry_run")); raw != "" {
		dryRun = parseOptionalBool(raw)
	}
	includeNeverSeen := req.IncludeNeverSeen
	if raw := strings.TrimSpace(r.URL.Query().Get("include_never_seen")); raw != "" {
		includeNeverSeen = parseOptionalBool(raw)
	}
	reclaimTasks := req.ReclaimTasks
	if raw := strings.TrimSpace(r.URL.Query().Get("reclaim_tasks")); raw != "" {
		reclaimTasks = parseOptionalBool(raw)
	}
	reclaimStatus := strings.TrimSpace(req.ReclaimStatus)
	if raw := strings.TrimSpace(r.URL.Query().Get("reclaim_status")); raw != "" {
		reclaimStatus = strings.TrimSpace(raw)
	}
	parsedReclaimStatus := team.TaskStatusReady
	if reclaimTasks {
		parsed, err := parseTaskStatus(reclaimStatus)
		if err != nil {
			h.writeError(w, http.StatusBadRequest, err)
			return
		}
		if parsed != "" {
			parsedReclaimStatus = parsed
		}
	}
	offlineAfter := time.Duration(req.OfflineAfterSec) * time.Second
	if raw := strings.TrimSpace(r.URL.Query().Get("offline_after_sec")); raw != "" {
		value, err := parseOptionalLimit(raw)
		if err != nil {
			h.writeError(w, http.StatusBadRequest, err)
			return
		}
		if value > 0 {
			offlineAfter = time.Duration(value) * time.Second
		}
	}
	if offlineAfter <= 0 {
		offlineAfter = h.defaultLeaseDuration()
	}
	if offlineAfter <= 0 {
		offlineAfter = 10 * time.Minute
	}

	tasksByAssignee := map[string][]team.Task{}
	if reclaimTasks {
		runningTasks, err := store.ListTasks(r.Context(), team.TaskFilter{
			TeamID: teamID,
			Status: []team.TaskStatus{team.TaskStatusRunning},
		})
		if err != nil {
			h.writeError(w, http.StatusInternalServerError, err)
			return
		}
		for _, task := range runningTasks {
			if task.Assignee == nil {
				continue
			}
			assignee := strings.TrimSpace(*task.Assignee)
			if assignee == "" {
				continue
			}
			tasksByAssignee[assignee] = append(tasksByAssignee[assignee], task)
		}
	}

	teammates, err := store.ListTeammates(r.Context(), teamID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	updated := make([]map[string]interface{}, 0)
	reclaimedTasks := make([]map[string]interface{}, 0)
	for _, mate := range teammates {
		if mate.State == team.TeammateStateOffline {
			continue
		}
		if mate.LastHeartbeat.IsZero() {
			if !includeNeverSeen {
				continue
			}
		} else if asOf.Sub(mate.LastHeartbeat) < offlineAfter {
			continue
		}
		if dryRun {
			updated = append(updated, map[string]interface{}{
				"teammate_id":    mate.ID,
				"previous_state": mate.State,
			})
			if reclaimTasks {
				for _, task := range tasksByAssignee[mate.ID] {
					reclaimedTasks = append(reclaimedTasks, map[string]interface{}{
						"task_id":     task.ID,
						"teammate_id": mate.ID,
						"status":      parsedReclaimStatus,
					})
				}
			}
			continue
		}
		if err := store.UpdateTeammateState(r.Context(), mate.ID, team.TeammateStateOffline); err != nil {
			h.writeError(w, http.StatusInternalServerError, err)
			return
		}
		updated = append(updated, map[string]interface{}{
			"teammate_id":    mate.ID,
			"previous_state": mate.State,
		})
		if reclaimTasks {
			for _, task := range tasksByAssignee[mate.ID] {
				if err := store.IncrementTaskRetry(r.Context(), task.ID); err != nil {
					h.writeError(w, http.StatusInternalServerError, err)
					return
				}
				if err := store.ReleaseTask(r.Context(), task.ID, parsedReclaimStatus); err != nil {
					h.writeError(w, http.StatusInternalServerError, err)
					return
				}
				if claims := h.getTeamClaimsManager(); claims != nil {
					_ = claims.Release(r.Context(), task.ID)
				}
				reclaimedTasks = append(reclaimedTasks, map[string]interface{}{
					"task_id":     task.ID,
					"teammate_id": mate.ID,
					"status":      parsedReclaimStatus,
				})
			}
		}
	}
	{
		traceID, requestID := runtimeTraceIDForRequest(r)
		payload := map[string]interface{}{
			"team_id":            teamID,
			"as_of":              asOf,
			"offline_after_sec":  int(offlineAfter.Seconds()),
			"dry_run":            dryRun,
			"include_never_seen": includeNeverSeen,
			"count":              len(updated),
		}
		if requestID != "" {
			payload["request_id"] = requestID
		}
		h.publishRuntimeEvent("team.teammates.swept", traceID, payload)
		if reclaimTasks {
			reclaimPayload := map[string]interface{}{
				"team_id": teamID,
				"as_of":   asOf,
				"dry_run": dryRun,
				"count":   len(reclaimedTasks),
				"reason":  "teammate_offline",
			}
			if requestID != "" {
				reclaimPayload["request_id"] = requestID
			}
			h.publishRuntimeEvent("team.task.reclaimed", traceID, reclaimPayload)
		}
	}

	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"team_id":            teamID,
		"as_of":              asOf,
		"offline_after_sec":  int(offlineAfter.Seconds()),
		"dry_run":            dryRun,
		"include_never_seen": includeNeverSeen,
		"updated":            updated,
		"reclaimed_tasks":    reclaimedTasks,
		"count":              len(updated),
	})
}

func runtimeTraceIDForRequest(r *http.Request) (string, string) {
	requestID := strings.TrimSpace(requestIDFromRequest(r))
	if requestID != "" {
		return requestID, requestID
	}
	return "trace_" + uuid.NewString(), ""
}

func (h *Handler) publishTeamSessionExecutionFailure(eventType, traceID string, payload map[string]interface{}, err error) string {
	resolvedTraceID := strings.TrimSpace(traceID)
	if payload == nil {
		payload = map[string]interface{}{}
	}
	if err != nil {
		payload["error"] = err.Error()
	}
	if sessionErr, ok := team.AsSessionExecutionError(err); ok && sessionErr != nil {
		if traceValue := strings.TrimSpace(sessionErr.TraceID); traceValue != "" {
			resolvedTraceID = traceValue
		}
		if errorType := strings.TrimSpace(sessionErr.ErrorType); errorType != "" {
			payload["error_type"] = errorType
		}
		for key, value := range sessionErr.CloneMetadata() {
			if key == "" {
				continue
			}
			if _, exists := payload[key]; exists {
				continue
			}
			payload[key] = value
		}
	}
	h.publishRuntimeEvent(eventType, resolvedTraceID, payload)
	return resolvedTraceID
}

func appendPrefixedSessionExecutionMetadata(target map[string]interface{}, prefix, traceID, errorType string, metadata map[string]interface{}) {
	if len(target) == 0 {
		return
	}
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		prefix = "session"
	}
	if value := strings.TrimSpace(traceID); value != "" {
		target[prefix+"_trace_id"] = value
	}
	if value := strings.TrimSpace(errorType); value != "" {
		target[prefix+"_error_type"] = value
	}
	for key, value := range metadata {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		target[prefix+"_"+key] = value
	}
}

func summarizeAssignments(assignments []team.Assignment) []map[string]interface{} {
	if len(assignments) == 0 {
		return nil
	}
	out := make([]map[string]interface{}, 0, len(assignments))
	for _, assignment := range assignments {
		out = append(out, map[string]interface{}{
			"task_id":     assignment.Task.ID,
			"teammate_id": assignment.Teammate.ID,
		})
	}
	return out
}

func normalizeTeamEventType(eventType string) string {
	eventType = strings.TrimSpace(eventType)
	if eventType == "" {
		return "team.event"
	}
	if strings.HasPrefix(eventType, "team.") {
		return eventType
	}
	if strings.HasPrefix(eventType, "task.") {
		return "team." + eventType
	}
	return "team." + eventType
}

func parseOptionalLimit(raw string) (int, error) {
	if strings.TrimSpace(raw) == "" {
		return 0, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return 0, errors.New(errors.ErrValidationFailed, "invalid limit value")
	}
	return value, nil
}

func parseOptionalBool(raw string) bool {
	if strings.TrimSpace(raw) == "" {
		return false
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false
	}
	return value
}

func parseOptionalTime(raw string) (*time.Time, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	parsed, err := parseTimeValue(raw)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func truncateLine(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 || len(value) <= limit {
		return value
	}
	return value[:limit]
}

func parseTimeValue(raw string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(raw))
	if err != nil {
		parsed, err = time.Parse(time.RFC3339, strings.TrimSpace(raw))
	}
	if err != nil {
		return time.Time{}, errors.New(errors.ErrValidationFailed, "invalid time format")
	}
	return parsed, nil
}

func parseTeamStatus(raw string) (team.TeamStatus, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", nil
	}
	switch team.TeamStatus(strings.ToLower(value)) {
	case team.TeamStatusActive, team.TeamStatusPaused, team.TeamStatusDone, team.TeamStatusFailed:
		return team.TeamStatus(strings.ToLower(value)), nil
	default:
		return "", errors.New(errors.ErrValidationFailed, "invalid team status")
	}
}

func parseTeammateState(raw string) (team.TeammateState, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", nil
	}
	switch team.TeammateState(strings.ToLower(value)) {
	case team.TeammateStateIdle, team.TeammateStateBusy, team.TeammateStateBlocked, team.TeammateStateOffline:
		return team.TeammateState(strings.ToLower(value)), nil
	default:
		return "", errors.New(errors.ErrValidationFailed, "invalid teammate state")
	}
}

func parseTaskStatus(raw string) (team.TaskStatus, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", nil
	}
	switch team.TaskStatus(strings.ToLower(value)) {
	case team.TaskStatusPending, team.TaskStatusReady, team.TaskStatusRunning, team.TaskStatusBlocked,
		team.TaskStatusDone, team.TaskStatusFailed, team.TaskStatusCancelled:
		return team.TaskStatus(strings.ToLower(value)), nil
	default:
		return "", errors.New(errors.ErrValidationFailed, "invalid task status")
	}
}

func parseTaskStatuses(raw string) ([]team.TaskStatus, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return nil, nil
	}
	parts := strings.Split(value, ",")
	out := make([]team.TaskStatus, 0, len(parts))
	for _, part := range parts {
		status, err := parseTaskStatus(part)
		if err != nil {
			return nil, err
		}
		if status != "" {
			out = append(out, status)
		}
	}
	return out, nil
}

func parseAgentControlTaskStatusList(raw string) []string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		status := strings.TrimSpace(part)
		if status != "" {
			out = append(out, status)
		}
	}
	return out
}

func (h *Handler) defaultLeaseDuration() time.Duration {
	if orchestrator := h.getTeamOrchestrator(); orchestrator != nil && orchestrator.LeaseDuration > 0 {
		return orchestrator.LeaseDuration
	}
	return 10 * time.Minute
}

func resolveLeaseUntil(raw string, durationSec int, fallback time.Duration) (time.Time, error) {
	if strings.TrimSpace(raw) != "" {
		return parseTimeValue(raw)
	}
	if durationSec > 0 {
		return time.Now().UTC().Add(time.Duration(durationSec) * time.Second), nil
	}
	if fallback <= 0 {
		fallback = 10 * time.Minute
	}
	return time.Now().UTC().Add(fallback), nil
}

func parseIDList(raw string) []string {
	parts := strings.FieldsFunc(strings.TrimSpace(raw), func(r rune) bool {
		switch r {
		case ',', ';', '\n', '\t', ' ':
			return true
		default:
			return false
		}
	})
	if len(parts) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(parts))
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if _, ok := seen[part]; ok {
			continue
		}
		seen[part] = struct{}{}
		out = append(out, part)
	}
	return out
}
