package runtimeapi

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	errors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

const agentControlAgentSessionScanLimit = 10000

// ListAgentControlAgents lists AgentControl identity graph rows. When a
// durable registry store is configured, session/team projections are first
// materialized into that store; otherwise the handler returns a compatibility
// projection over the configured session and team stores.
func (h *Handler) ListAgentControlAgents(w http.ResponseWriter, r *http.Request) {
	filter, err := parseAgentControlAgentFilter(r)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	source := "agent_control_projection"
	var records []agentcontrol.AgentRecord
	if store := h.getAgentControlAgentStore(); store != nil {
		if err := h.materializeAgentControlAgentProjections(r.Context(), store, filter); err != nil {
			h.writeError(w, http.StatusInternalServerError, err)
			return
		}
		records, err = store.ListAgentControlAgents(r.Context(), filter)
		if err != nil {
			h.writeError(w, http.StatusInternalServerError, err)
			return
		}
		source = "agent_control_agents"
	} else {
		records, err = h.projectAgentControlAgents(r.Context(), filter)
		if err != nil {
			h.writeError(w, http.StatusInternalServerError, err)
			return
		}
	}
	// 运行态是查询时刻的容器事实（活体 actor → 持久运行态 → missing），与身份
	// 状态分开上报：面板的「运行中」必须看它，否则子代理跑完后会话行仍 open，
	// 身份行会一直显示 active。
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"agents": h.buildAgentControlAgentViews(r.Context(), records),
		"count":  len(records),
		"source": source,
		"filters": map[string]interface{}{
			"agent_id":          filter.AgentID,
			"root_session_id":   filter.RootSessionID,
			"parent_agent_id":   filter.ParentAgentID,
			"parent_session_id": filter.ParentSessionID,
			"session_id":        filter.SessionID,
			"agent_path":        filter.AgentPath,
			"path_prefix":       filter.PathPrefix,
			"workflow":          filter.Workflow,
			"team_id":           filter.TeamID,
			"teammate_id":       filter.TeammateID,
			"include_closed":    filter.IncludeClosed,
			"after_seq":         filter.AfterSeq,
			"limit":             filter.Limit,
		},
	})
}

func parseAgentControlAgentFilter(r *http.Request) (agentcontrol.AgentFilter, error) {
	q := r.URL.Query()
	afterSeq := int64(0)
	if raw := firstNonEmptyString(strings.TrimSpace(q.Get("after_seq")), strings.TrimSpace(q.Get("after"))); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 0 {
			return agentcontrol.AgentFilter{}, errors.New(errors.ErrValidationFailed, "invalid after_seq value")
		}
		afterSeq = parsed
	}
	limit, err := parseOptionalLimit(q.Get("limit"))
	if err != nil {
		return agentcontrol.AgentFilter{}, err
	}
	return agentcontrol.AgentFilter{
		AgentID:         strings.TrimSpace(q.Get("agent_id")),
		RootSessionID:   firstNonEmptyString(chat.NormalizeSessionID(q.Get("root_session_id")), chat.NormalizeSessionID(q.Get("root"))),
		ParentAgentID:   strings.TrimSpace(q.Get("parent_agent_id")),
		ParentSessionID: chat.NormalizeSessionID(q.Get("parent_session_id")),
		SessionID:       firstNonEmptyString(chat.NormalizeSessionID(q.Get("session_id")), chat.NormalizeSessionID(q.Get("session"))),
		AgentPath:       firstNonEmptyString(strings.TrimSpace(q.Get("agent_path")), strings.TrimSpace(q.Get("path"))),
		PathPrefix:      strings.TrimSpace(q.Get("path_prefix")),
		Workflow:        strings.TrimSpace(q.Get("workflow")),
		TeamID:          strings.TrimSpace(q.Get("team_id")),
		TeammateID:      strings.TrimSpace(q.Get("teammate_id")),
		IncludeClosed:   parseBoolQuery(q.Get("include_closed")),
		AfterSeq:        afterSeq,
		Limit:           limit,
	}.Normalize(), nil
}

func (h *Handler) materializeAgentControlAgentProjections(ctx context.Context, store agentcontrol.AgentRegistryStore, filter agentcontrol.AgentFilter) error {
	if h == nil || store == nil {
		return nil
	}
	// G5：物化是一次 O(sessions) 的全量扫描，spawn 门控、列表刷新与周期对账都会
	// 触发；单飞锁保证同一时刻只有一次扫描 + 写入在进行，避免重复扫描与重复写。
	h.agentControlMaterializeMu.Lock()
	defer h.agentControlMaterializeMu.Unlock()
	// G3：先收敛已失联/已结束的绑定，再写投影。CLI 宿主的 materialize 一直
	// 是"先 sweep 再 upsert"，API 宿主此前只在审计里发现问题、不收敛，导致同一
	// 份 registry 在两个宿主下漂移（见 sweepStaleAgentControlAgentRegistry）。
	if err := h.sweepStaleAgentControlAgentRegistry(ctx, store); err != nil {
		return err
	}
	records, err := h.projectAgentControlAgents(ctx, projectionAgentFilter(filter))
	if err != nil {
		return err
	}
	// G4：一次列表建立内存索引，取代每条投影记录一次点查（N+1 → 1）。
	index, err := newAgentControlRecordIndex(ctx, store)
	if err != nil {
		return err
	}
	for _, record := range records {
		record = record.Normalize()
		if record.AgentID == "" || record.RootSessionID == "" || record.AgentPath == "" {
			continue
		}
		existing, exists := index.lookup(record)
		if exists && existing.Closed() && !record.Closed() {
			continue
		}
		if _, err := store.UpsertAgentControlAgent(ctx, record); err != nil {
			return err
		}
		index.store(record)
	}
	return nil
}

// agentControlRecordIndex answers "does this identity already exist?" from one
// registry listing instead of one query per projected record. Identities are
// keyed exactly like the previous point lookups: agent id first, then the
// root-session + path binding, so the closed-row guard keeps its semantics.
type agentControlRecordIndex struct {
	byAgentID map[string]agentcontrol.AgentRecord
	byPath    map[string]agentcontrol.AgentRecord
}

func newAgentControlRecordIndex(ctx context.Context, store agentcontrol.AgentRegistryStore) (*agentControlRecordIndex, error) {
	index := &agentControlRecordIndex{
		byAgentID: map[string]agentcontrol.AgentRecord{},
		byPath:    map[string]agentcontrol.AgentRecord{},
	}
	if store == nil {
		return index, nil
	}
	records, err := store.ListAgentControlAgents(ctx, agentcontrol.AgentFilter{IncludeClosed: true})
	if err != nil {
		return nil, err
	}
	for _, record := range records {
		index.store(record)
	}
	return index, nil
}

func (i *agentControlRecordIndex) store(record agentcontrol.AgentRecord) {
	if i == nil {
		return
	}
	record = record.Normalize()
	if agentID := strings.TrimSpace(record.AgentID); agentID != "" {
		i.byAgentID[agentID] = record
	}
	if key := agentControlBindingKey(record.RootSessionID, record.AgentPath); key != "" {
		i.byPath[key] = record
	}
}

func (i *agentControlRecordIndex) lookup(record agentcontrol.AgentRecord) (agentcontrol.AgentRecord, bool) {
	if i == nil {
		return agentcontrol.AgentRecord{}, false
	}
	record = record.Normalize()
	if agentID := strings.TrimSpace(record.AgentID); agentID != "" {
		if existing, ok := i.byAgentID[agentID]; ok {
			return existing, true
		}
	}
	if key := agentControlBindingKey(record.RootSessionID, record.AgentPath); key != "" {
		if existing, ok := i.byPath[key]; ok {
			return existing, true
		}
	}
	return agentcontrol.AgentRecord{}, false
}

func agentControlBindingKey(rootSessionID, agentPath string) string {
	rootSessionID = strings.TrimSpace(rootSessionID)
	agentPath = strings.TrimSpace(agentPath)
	if rootSessionID == "" || agentPath == "" {
		return ""
	}
	return rootSessionID + "\x00" + agentPath
}

func existingAgentControlAgentRecord(ctx context.Context, store agentcontrol.AgentRegistryStore, record agentcontrol.AgentRecord) (agentcontrol.AgentRecord, bool, error) {
	if store == nil {
		return agentcontrol.AgentRecord{}, false, nil
	}
	if agentID := strings.TrimSpace(record.AgentID); agentID != "" {
		records, err := store.ListAgentControlAgents(ctx, agentcontrol.AgentFilter{
			AgentID:       agentID,
			IncludeClosed: true,
			Limit:         1,
		})
		if err != nil {
			return agentcontrol.AgentRecord{}, false, err
		}
		if len(records) > 0 {
			return records[0].Normalize(), true, nil
		}
	}
	if rootSessionID, agentPath := strings.TrimSpace(record.RootSessionID), strings.TrimSpace(record.AgentPath); rootSessionID != "" && agentPath != "" {
		records, err := store.ListAgentControlAgents(ctx, agentcontrol.AgentFilter{
			RootSessionID: rootSessionID,
			AgentPath:     agentPath,
			IncludeClosed: true,
			Limit:         1,
		})
		if err != nil {
			return agentcontrol.AgentRecord{}, false, err
		}
		if len(records) > 0 {
			return records[0].Normalize(), true, nil
		}
	}
	return agentcontrol.AgentRecord{}, false, nil
}

func projectionAgentFilter(filter agentcontrol.AgentFilter) agentcontrol.AgentFilter {
	filter = filter.Normalize()
	filter.AgentID = ""
	filter.ParentAgentID = ""
	filter.ParentSessionID = ""
	filter.SessionID = ""
	filter.AgentPath = ""
	filter.PathPrefix = ""
	filter.AfterSeq = 0
	filter.Limit = 0
	filter.IncludeClosed = true
	return filter
}

func (h *Handler) projectAgentControlAgents(ctx context.Context, filter agentcontrol.AgentFilter) ([]agentcontrol.AgentRecord, error) {
	filter = filter.Normalize()
	records := make([]agentcontrol.AgentRecord, 0)
	sessionRecords, err := h.projectAgentControlSessionAgents(ctx, filter)
	if err != nil {
		return nil, err
	}
	records = append(records, sessionRecords...)
	teamRecords, err := h.projectAgentControlTeamAgents(ctx, filter)
	if err != nil {
		return nil, err
	}
	records = append(records, teamRecords...)
	records = dedupeAgentControlRecords(records)
	filtered := make([]agentcontrol.AgentRecord, 0, len(records))
	for _, record := range records {
		record = record.Normalize()
		if !agentRecordMatchesFilter(record, filter) {
			continue
		}
		filtered = append(filtered, record)
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		left := firstNonEmptyString(filtered[i].RootSessionID, "") + "\x00" + firstNonEmptyString(filtered[i].AgentPath, filtered[i].AgentID)
		right := firstNonEmptyString(filtered[j].RootSessionID, "") + "\x00" + firstNonEmptyString(filtered[j].AgentPath, filtered[j].AgentID)
		return left < right
	})
	for index := range filtered {
		filtered[index].Seq = int64(index + 1)
	}
	if filter.AfterSeq > 0 {
		kept := filtered[:0]
		for _, record := range filtered {
			if record.Seq > filter.AfterSeq {
				kept = append(kept, record)
			}
		}
		filtered = kept
	}
	if filter.Limit > 0 && len(filtered) > filter.Limit {
		filtered = filtered[:filter.Limit]
	}
	return filtered, nil
}

func (h *Handler) projectAgentControlSessionAgents(ctx context.Context, filter agentcontrol.AgentFilter) ([]agentcontrol.AgentRecord, error) {
	if h == nil || h.sessionManager == nil || h.sessionManager.GetStorage() == nil {
		return nil, nil
	}
	storage := h.sessionManager.GetStorage()
	lister, ok := storage.(chat.SessionStorageAllLister)
	if !ok || lister == nil {
		return nil, nil
	}
	sessions, err := lister.ListAll(ctx, agentControlAgentSessionScanLimit, 0)
	if err != nil {
		return nil, err
	}
	records := make([]agentcontrol.AgentRecord, 0, len(sessions))
	rootSeen := map[string]struct{}{}
	for _, session := range sessions {
		if session == nil {
			continue
		}
		sessionID := strings.TrimSpace(session.ID)
		if sessionID == "" {
			continue
		}
		rootSessionID := agentcontrol.RootSessionID(session, sessionID, "")
		if rootSessionID == "" {
			rootSessionID = sessionID
		}
		if _, ok := rootSeen[rootSessionID]; !ok {
			rootSeen[rootSessionID] = struct{}{}
			records = append(records, agentcontrol.AgentRecord{
				AgentID:       apiRootAgentID(rootSessionID),
				RootSessionID: rootSessionID,
				SessionID:     rootSessionID,
				AgentPath:     "/root",
				AgentType:     agentcontrol.AgentTypeRoot,
				Status:        agentcontrol.AgentStatusActive,
			})
		}
		if !isAPIAgentSession(session) {
			continue
		}
		status := agentcontrol.AgentStatusActive
		var closedAt *time.Time
		if isClosedAPIAgentSession(session) {
			status = agentcontrol.AgentStatusClosed
			closed := session.UpdatedAt
			if closed.IsZero() {
				closed = time.Now().UTC()
			}
			closedAt = &closed
		}
		record := agentcontrol.AgentRecord{
			AgentID:         sessionID,
			RootSessionID:   rootSessionID,
			ParentAgentID:   apiRootAgentID(rootSessionID),
			ParentSessionID: agentcontrol.ContextString(session, toolbroker.AgentSessionContextParentSessionID),
			SessionID:       sessionID,
			AgentPath:       apiAgentSessionPath(session),
			Depth:           apiAgentSessionDepth(session),
			AgentType:       firstNonEmptyString(agentcontrol.ContextString(session, toolbroker.AgentSessionContextAgentType), agentcontrol.AgentTypeChild),
			Workflow:        agentcontrol.WorkflowSpawnAgent,
			TeamID:          agentcontrol.ContextString(session, toolbroker.AgentSessionContextTeamID),
			TeammateID:      agentcontrol.ContextString(session, toolbroker.AgentSessionContextTeammateID),
			Status:          status,
			CreatedAt:       session.CreatedAt,
			UpdatedAt:       session.UpdatedAt,
			ClosedAt:        closedAt,
		}
		// Parity with the CLI host projection (chat_actor_registry.go): the
		// session context is the projection source of truth for route metadata.
		// Leaving these fields empty would let the next materialize pass upsert
		// an empty route over the durable row written by the spawn reservation.
		toolbroker.ApplySpawnAgentRouteRecordContext(&record, session)
		if record.TeamID != "" {
			record.Workflow = agentcontrol.WorkflowSpawnTeam
			record.AgentID = "team:" + record.TeamID + ":" + firstNonEmptyString(record.TeammateID, sessionID)
			record.ParentAgentID = apiRootAgentID(rootSessionID)
		}
		records = append(records, record)
	}
	return records, nil
}

func (h *Handler) projectAgentControlTeamAgents(ctx context.Context, filter agentcontrol.AgentFilter) ([]agentcontrol.AgentRecord, error) {
	store := h.getTeamStore()
	if store == nil {
		return nil, nil
	}
	teamIDs := make([]string, 0)
	if filter.TeamID != "" {
		teamIDs = append(teamIDs, filter.TeamID)
	} else {
		ids, err := store.ListTeamIDs(ctx)
		if err != nil {
			return nil, err
		}
		teamIDs = append(teamIDs, ids...)
	}
	records := make([]agentcontrol.AgentRecord, 0)
	for _, teamID := range teamIDs {
		teamID = strings.TrimSpace(teamID)
		if teamID == "" {
			continue
		}
		record, err := store.GetTeam(ctx, teamID)
		if err != nil {
			return nil, err
		}
		if record == nil {
			continue
		}
		rootSessionID := strings.TrimSpace(record.LeadSessionID)
		rootSessionBinding := rootSessionID
		if rootSessionID == "" {
			rootSessionID = "team:" + teamID
			rootSessionBinding = ""
		}
		records = append(records, agentcontrol.AgentRecord{
			AgentID:       apiRootAgentID(rootSessionID),
			RootSessionID: rootSessionID,
			SessionID:     rootSessionBinding,
			AgentPath:     "/root",
			AgentType:     agentcontrol.AgentTypeRoot,
			Status:        agentcontrol.AgentStatusActive,
		})
		teammates, err := store.ListTeammates(ctx, teamID)
		if err != nil {
			return nil, err
		}
		for _, mate := range teammates {
			if filter.TeammateID != "" && !strings.EqualFold(strings.TrimSpace(mate.ID), filter.TeammateID) {
				continue
			}
			sessionID := strings.TrimSpace(mate.SessionID)
			path := agentcontrol.TeamTeammatePath(teamID, mate.ID, mate.Name, sessionID)
			records = append(records, agentcontrol.AgentRecord{
				AgentID:         "team:" + teamID + ":" + firstNonEmptyString(strings.TrimSpace(mate.ID), sessionID),
				RootSessionID:   rootSessionID,
				ParentAgentID:   apiRootAgentID(rootSessionID),
				ParentSessionID: rootSessionID,
				SessionID:       sessionID,
				AgentPath:       path,
				Depth:           1,
				AgentType:       firstNonEmptyString(strings.TrimSpace(mate.Profile), agentcontrol.AgentTypeTeamTeammate),
				Nickname:        strings.TrimSpace(mate.Name),
				Workflow:        agentcontrol.WorkflowSpawnTeam,
				TeamID:          teamID,
				TeammateID:      strings.TrimSpace(mate.ID),
				Status:          agentcontrol.AgentStatusActive,
				CreatedAt:       mate.CreatedAt,
				UpdatedAt:       mate.UpdatedAt,
			})
		}
	}
	return records, nil
}

func dedupeAgentControlRecords(records []agentcontrol.AgentRecord) []agentcontrol.AgentRecord {
	seen := map[string]struct{}{}
	out := make([]agentcontrol.AgentRecord, 0, len(records))
	for _, record := range records {
		record = record.Normalize()
		key := strings.ToLower(firstNonEmptyString(record.AgentID, record.RootSessionID+"|"+record.AgentPath))
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, record)
	}
	return out
}

func agentRecordMatchesFilter(record agentcontrol.AgentRecord, filter agentcontrol.AgentFilter) bool {
	if filter.AgentID != "" && !strings.EqualFold(record.AgentID, filter.AgentID) {
		return false
	}
	if filter.RootSessionID != "" && !strings.EqualFold(record.RootSessionID, filter.RootSessionID) {
		return false
	}
	if filter.ParentAgentID != "" && !strings.EqualFold(record.ParentAgentID, filter.ParentAgentID) {
		return false
	}
	if filter.ParentSessionID != "" && !strings.EqualFold(record.ParentSessionID, filter.ParentSessionID) {
		return false
	}
	if filter.SessionID != "" && !strings.EqualFold(record.SessionID, filter.SessionID) {
		return false
	}
	if filter.AgentPath != "" && !strings.EqualFold(record.AgentPath, filter.AgentPath) {
		return false
	}
	if filter.PathPrefix != "" {
		if !agentcontrol.AgentPathMatchesPrefix(record.AgentPath, filter.PathPrefix) {
			return false
		}
	}
	if filter.Workflow != "" && !strings.EqualFold(record.Workflow, filter.Workflow) {
		return false
	}
	if filter.TeamID != "" && !strings.EqualFold(record.TeamID, filter.TeamID) {
		return false
	}
	if filter.TeammateID != "" && !strings.EqualFold(record.TeammateID, filter.TeammateID) {
		return false
	}
	if !filter.IncludeClosed && record.Closed() {
		return false
	}
	return true
}

// SetAgentControlAgentStore sets the optional durable AgentControl identity
// registry store used by AgentControl agent APIs.
func (h *Handler) SetAgentControlAgentStore(store agentcontrol.AgentRegistryStore) {
	if h == nil {
		return
	}
	h.agentControlMu.Lock()
	oldService := h.agentControlRegistryService
	h.agentControlRegistryService = nil
	h.agentControlRegistryStoreKey = ""
	h.agentControlAgentStore = store
	h.agentControlAgentStoreKey = ""
	h.agentControlAgentStoreAuto = false
	if oldService != nil && h.agentControlMailboxStoreAuto {
		h.agentControlMailboxStore = nil
		h.agentControlMailboxStoreKey = ""
		h.agentControlMailboxStoreAuto = false
	}
	h.agentControlMu.Unlock()
	if oldService != nil {
		_ = oldService.Close()
	}
	if oldService != nil {
		h.configureMailboxWriteThrough(nil)
	}
	// P2-9：宿主直接注入 store 时也让对账循环跟上（store 为 nil 时停止循环）。
	h.ensureAgentRegistryReconciler()
}

func (h *Handler) getAgentControlAgentStore() agentcontrol.AgentRegistryStore {
	if h == nil {
		return nil
	}
	h.agentControlMu.RLock()
	store := h.agentControlAgentStore
	h.agentControlMu.RUnlock()
	return store
}

func parseBoolQuery(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
