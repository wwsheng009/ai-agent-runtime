package skills

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	errors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

const agentControlMailboxSessionScanLimit = 10000
const agentControlMailboxProjectionReconcileTimeout = 10 * time.Second

// ListAgentControlMailbox lists the in-process global AgentControl mailbox
// registry. When a durable global registry store is configured, local
// runtime/session and team rows are first materialized into that store and the
// response uses its durable global cursor.
func (h *Handler) ListAgentControlMailbox(w http.ResponseWriter, r *http.Request) {
	filter, err := parseAgentControlMailboxFilter(r)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	registry, err := h.agentControlMailboxRegistry(r.Context(), filter)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	records, err := registry.ListAgentControlMailboxRecords(r.Context(), filter)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	latestSeq, err := registry.LastAgentControlMailboxRecordSeq(r.Context(), filter)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"records":    records,
		"count":      len(records),
		"latest_seq": latestSeq,
		"sources":    registry.SourceNames(),
		"filters": map[string]interface{}{
			"workflow":   filter.Workflow,
			"scope":      filter.Scope,
			"session_id": filter.SessionID,
			"team_id":    filter.TeamID,
			"after_seq":  filter.AfterSeq,
			"limit":      filter.Limit,
		},
	})
}

func parseAgentControlMailboxFilter(r *http.Request) (agentcontrol.MailboxRecordFilter, error) {
	q := r.URL.Query()
	scope := strings.TrimSpace(q.Get("scope"))
	if scope != "" && scope != agentcontrol.MailboxScopeSession && scope != agentcontrol.MailboxScopeTeam {
		return agentcontrol.MailboxRecordFilter{}, errors.New(errors.ErrValidationFailed, "invalid mailbox scope")
	}
	afterSeq := int64(0)
	if raw := firstNonEmptyString(strings.TrimSpace(q.Get("after_seq")), strings.TrimSpace(q.Get("after"))); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 0 {
			return agentcontrol.MailboxRecordFilter{}, errors.New(errors.ErrValidationFailed, "invalid after_seq value")
		}
		afterSeq = parsed
	}
	limit, err := parseOptionalLimit(q.Get("limit"))
	if err != nil {
		return agentcontrol.MailboxRecordFilter{}, err
	}
	if limit <= 0 {
		limit = 50
	}
	return agentcontrol.MailboxRecordFilter{
		Workflow:  strings.TrimSpace(q.Get("workflow")),
		Scope:     scope,
		SessionID: firstNonEmptyString(strings.TrimSpace(q.Get("session_id")), strings.TrimSpace(q.Get("session"))),
		TeamID:    strings.TrimSpace(q.Get("team_id")),
		AfterSeq:  afterSeq,
		Limit:     limit,
	}.Normalize(), nil
}

func (h *Handler) agentControlMailboxRegistry(ctx context.Context, filter agentcontrol.MailboxRecordFilter) (agentcontrol.GlobalMailboxRegistry, error) {
	sources := make([]agentcontrol.NamedMailboxRegistrySource, 0, 2)
	if filter.Scope == "" || filter.Scope == agentcontrol.MailboxScopeSession {
		eventStore := h.getSessionEventStore()
		sessionIDs, err := h.agentControlMailboxSessionIDs(ctx, filter.SessionID)
		if err != nil {
			return agentcontrol.GlobalMailboxRegistry{}, err
		}
		if eventStore != nil && len(sessionIDs) > 0 {
			sources = append(sources, agentcontrol.NamedMailboxRegistrySource{
				Name:   agentcontrol.MailboxSourceRuntimeSessions,
				Source: chat.NewAgentControlMailboxRegistry(eventStore, sessionIDs...),
			})
		}
	}
	if filter.Scope == "" || filter.Scope == agentcontrol.MailboxScopeTeam {
		if store := h.getTeamStore(); store != nil {
			sources = append(sources, agentcontrol.NamedMailboxRegistrySource{
				Name:   agentcontrol.MailboxSourceTeams,
				Source: team.NewAgentControlMailboxRegistry(store),
			})
		}
	}
	if store := h.getAgentControlMailboxStore(); store != nil {
		if _, err := store.MaterializeMailboxRecords(ctx, sources, filter); err != nil {
			return agentcontrol.GlobalMailboxRegistry{}, err
		}
		return agentcontrol.NewGlobalMailboxRegistryWithDurable(
			agentcontrol.NamedMailboxRegistrySource{
				Name:   agentcontrol.MailboxSourceGlobal,
				Source: store,
			},
			sources...,
		), nil
	}
	return agentcontrol.NewGlobalMailboxRegistry(sources...), nil
}

// SetAgentControlMailboxStore sets the optional durable global mailbox
// registry store used by AgentControl mailbox APIs.
func (h *Handler) SetAgentControlMailboxStore(store agentcontrol.GlobalMailboxRegistryStore) {
	if h == nil {
		return
	}
	h.agentControlMu.Lock()
	oldService := h.agentControlRegistryService
	h.agentControlRegistryService = nil
	h.agentControlRegistryStoreKey = ""
	h.agentControlMailboxStore = store
	h.agentControlMailboxStoreKey = ""
	h.agentControlMailboxStoreAuto = false
	if oldService != nil && h.agentControlAgentStoreAuto {
		h.agentControlAgentStore = nil
		h.agentControlAgentStoreKey = ""
		h.agentControlAgentStoreAuto = false
	}
	h.agentControlMu.Unlock()
	if oldService != nil {
		_ = oldService.Close()
	}
	h.configureMailboxWriteThrough(store)
}

func (h *Handler) getAgentControlMailboxStore() agentcontrol.GlobalMailboxRegistryStore {
	if h == nil {
		return nil
	}
	h.agentControlMu.RLock()
	store := h.agentControlMailboxStore
	h.agentControlMu.RUnlock()
	return store
}

func (h *Handler) configureMailboxWriteThrough(writer agentcontrol.GlobalMailboxWriter) {
	if h == nil {
		return
	}
	h.sessionRuntimeMu.RLock()
	sessionRuntimeStore := h.sessionRuntimeStore
	h.sessionRuntimeMu.RUnlock()
	if runtimeStore, ok := sessionRuntimeStore.(interface {
		SetGlobalMailboxWriter(agentcontrol.GlobalMailboxWriter)
	}); ok && runtimeStore != nil {
		runtimeStore.SetGlobalMailboxWriter(writer)
	}
	h.teamStoreMu.RLock()
	teamStoreValue := h.teamStore
	h.teamStoreMu.RUnlock()
	if teamStore, ok := teamStoreValue.(interface {
		SetGlobalMailboxWriter(agentcontrol.GlobalMailboxWriter)
	}); ok && teamStore != nil {
		teamStore.SetGlobalMailboxWriter(writer)
	}
	h.teamStoreMu.Lock()
	applyTeamOrchestratorMailboxWake(h.teamOrchestrator, writer)
	h.teamStoreMu.Unlock()
	if writer != nil {
		h.reconcileMailboxProjectionsAsync(sessionRuntimeStore, teamStoreValue)
	}
}

func (h *Handler) reconcileMailboxProjectionsAsync(stores ...interface{}) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), agentControlMailboxProjectionReconcileTimeout)
		defer cancel()
		result, err := agentcontrol.ReconcileMailboxProjections(ctx, agentcontrol.MailboxRecordFilter{}, stores...)
		if err != nil {
			h.publishRuntimeEvent("agent_control.mailbox.projection_reconcile_failed", "", map[string]interface{}{
				"error": err.Error(),
			})
			return
		}
		if result.LocalToGlobal > 0 || result.GlobalToLocal > 0 {
			h.publishRuntimeEvent("agent_control.mailbox.projection_reconciled", "", map[string]interface{}{
				"local_to_global": result.LocalToGlobal,
				"global_to_local": result.GlobalToLocal,
			})
		}
	}()
}

func (h *Handler) agentControlMailboxSessionIDs(ctx context.Context, sessionID string) ([]string, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID != "" {
		return []string{sessionID}, nil
	}
	if h.sessionManager == nil || h.sessionManager.GetStorage() == nil {
		return nil, nil
	}
	lister, ok := h.sessionManager.GetStorage().(chat.SessionStorageAllLister)
	if !ok {
		return nil, nil
	}
	sessions, err := lister.ListAll(ctx, agentControlMailboxSessionScanLimit, 0)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(sessions))
	seen := make(map[string]struct{}, len(sessions))
	for _, session := range sessions {
		if session == nil {
			continue
		}
		id := strings.TrimSpace(session.ID)
		if id == "" {
			continue
		}
		key := strings.ToLower(id)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		ids = append(ids, id)
	}
	return ids, nil
}
