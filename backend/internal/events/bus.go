package events

import (
	"sort"
	"strings"
	"sync"
	"time"
)

// Event 描述 runtime 主链上的一个生命周期事件。
type Event struct {
	Type      string                 `json:"type"`
	TraceID   string                 `json:"trace_id,omitempty"`
	AgentName string                 `json:"agent_name,omitempty"`
	SessionID string                 `json:"session_id,omitempty"`
	ToolName  string                 `json:"tool_name,omitempty"`
	Payload   map[string]interface{} `json:"payload,omitempty"`
	Timestamp time.Time              `json:"timestamp"`
}

// Handler 处理一条 runtime event。
type Handler func(Event)

// Publisher 是 runtime event 的最小发布接口。
type Publisher interface {
	Publish(event Event)
}

// QueryFilter 定义 runtime event 查询条件。
type QueryFilter struct {
	TraceID             string
	SessionID           string
	AgentName           string
	ToolName            string
	EventType           string
	TeamID              string
	ProfileResourceKind string
	Limit               int
}

// TraceFilter 定义 recent trace summary 的查询条件。
type TraceFilter struct {
	TraceIDPrefix       string
	SessionID           string
	AgentName           string
	ToolName            string
	EventType           string
	TeamID              string
	ProfileResourceKind string
	Limit               int
}

type ExecutionView struct {
	ToolRequested     int            `json:"tool_requested"`
	ToolCompleted     int            `json:"tool_completed"`
	ToolReduced       int            `json:"tool_reduced"`
	ArtifactRefs      int            `json:"artifact_refs"`
	Reducers          map[string]int `json:"reducers"`
	SubagentBatches   int            `json:"subagent_batches"`
	SubagentBatchDone int            `json:"subagent_batch_completed"`
	SubagentStarted   int            `json:"subagent_started"`
	SubagentCompleted int            `json:"subagent_completed"`
	SubagentRoles     map[string]int `json:"subagent_roles"`
	PatchApplied      int            `json:"patch_applied"`
	AppliedBy         map[string]int `json:"applied_by"`
}

type ProvenanceView struct {
	ProfileContextInjected int            `json:"profile_context_injected"`
	RecallWithSourceRefs   int            `json:"recall_with_source_refs"`
	ProfileResourceRefs    []string       `json:"profile_resource_refs,omitempty"`
	ProfileResourceKinds   map[string]int `json:"profile_resource_kinds"`
	ProfileResourceCount   int            `json:"profile_resource_count"`
	ProfileMemoryCount     int            `json:"profile_memory_count"`
	ProfileNotesCount      int            `json:"profile_notes_count"`
	ProfileResourceLabels  []string       `json:"profile_resource_labels,omitempty"`
}

// TraceSummary 表示一个 trace 的聚合摘要。
type TraceSummary struct {
	TraceID              string         `json:"trace_id"`
	EventCount           int            `json:"event_count"`
	EventTypes           map[string]int `json:"event_types"`
	Agents               []string       `json:"agents,omitempty"`
	Sessions             []string       `json:"sessions,omitempty"`
	TeamIDs              []string       `json:"team_ids,omitempty"`
	TeamCount            int            `json:"team_count"`
	MCPNames             []string       `json:"mcp_names,omitempty"`
	TransportTypes       []string       `json:"transport_types,omitempty"`
	Tools                []string       `json:"tools,omitempty"`
	PatchApprovalTickets []string       `json:"patch_approval_tickets,omitempty"`
	Governance           GovernanceView `json:"governance"`
	Execution            ExecutionView  `json:"execution"`
	Provenance           ProvenanceView `json:"provenance"`
	StartedAt            time.Time      `json:"started_at"`
	EndedAt              time.Time      `json:"ended_at"`
}

// GovernanceView 是 trace 级别的治理摘要。
type GovernanceView struct {
	DeniedEvents             int            `json:"denied_events"`
	ToolDenied               int            `json:"tool_denied"`
	SubagentDenied           int            `json:"subagent_denied"`
	PatchDecisions           int            `json:"patch_decisions"`
	PatchBlocked             int            `json:"patch_blocked"`
	PatchApproved            int            `json:"patch_approved"`
	PatchApprovedOverride    int            `json:"patch_approved_override"`
	PatchApprovalsWithTicket int            `json:"patch_approvals_with_ticket"`
	Policies                 map[string]int `json:"policies"`
	Reasons                  map[string]int `json:"reasons"`
	PatchPolicies            map[string]int `json:"patch_policies"`
}

// TraceStats 是 recent trace / event 的聚合统计。
type TraceStats struct {
	TraceCount     int            `json:"trace_count"`
	EventCount     int            `json:"event_count"`
	EventTypes     map[string]int `json:"event_types"`
	Agents         map[string]int `json:"agents"`
	Sessions       map[string]int `json:"sessions"`
	TeamIDs        map[string]int `json:"team_ids"`
	TeamCount      int            `json:"team_count"`
	MCPNames       map[string]int `json:"mcp_names"`
	TransportTypes map[string]int `json:"transport_types"`
	Tools          map[string]int `json:"tools"`
	Governance     GovernanceView `json:"governance"`
	Execution      ExecutionView  `json:"execution"`
	Provenance     ProvenanceView `json:"provenance"`
	LatestTraceIDs []string       `json:"latest_trace_ids,omitempty"`
	StartedAt      time.Time      `json:"started_at"`
	EndedAt        time.Time      `json:"ended_at"`
}

// GovernanceStats 是 recent denied events 的专门治理统计。
type GovernanceStats struct {
	TraceCount               int            `json:"trace_count"`
	DeniedEvents             int            `json:"denied_events"`
	ToolDenied               int            `json:"tool_denied"`
	SubagentDenied           int            `json:"subagent_denied"`
	PatchDecisions           int            `json:"patch_decisions"`
	PatchBlocked             int            `json:"patch_blocked"`
	PatchApproved            int            `json:"patch_approved"`
	PatchApprovedOverride    int            `json:"patch_approved_override"`
	PatchApprovalsWithTicket int            `json:"patch_approvals_with_ticket"`
	Policies                 map[string]int `json:"policies"`
	Reasons                  map[string]int `json:"reasons"`
	PatchPolicies            map[string]int `json:"patch_policies"`
	Agents                   map[string]int `json:"agents"`
	Sessions                 map[string]int `json:"sessions"`
	TeamIDs                  map[string]int `json:"team_ids"`
	TeamCount                int            `json:"team_count"`
	Tools                    map[string]int `json:"tools"`
	MCPNames                 map[string]int `json:"mcp_names"`
	Execution                ExecutionView  `json:"execution"`
	Provenance               ProvenanceView `json:"provenance"`
	LatestTraceIDs           []string       `json:"latest_trace_ids,omitempty"`
	StartedAt                time.Time      `json:"started_at"`
	EndedAt                  time.Time      `json:"ended_at"`
}

// Bus 是最小的 runtime 事件总线。
type Bus struct {
	mu          sync.RWMutex
	subscribers map[string][]Handler
	all         []Handler
	retention   int
	events      []Event
}

// NewBus 创建一个新的 runtime event bus。
func NewBus() *Bus {
	return NewBusWithRetention(512)
}

// NewBusWithRetention 创建带有界事件留存的 runtime event bus。
func NewBusWithRetention(retention int) *Bus {
	if retention < 0 {
		retention = 0
	}
	return &Bus{
		subscribers: make(map[string][]Handler),
		all:         make([]Handler, 0),
		retention:   retention,
		events:      make([]Event, 0, retention),
	}
}

// Subscribe 订阅指定 event type；eventType 为空时订阅全部事件。
func (b *Bus) Subscribe(eventType string, handler Handler) {
	if b == nil || handler == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	if eventType == "" {
		b.all = append(b.all, handler)
		return
	}
	b.subscribers[eventType] = append(b.subscribers[eventType], handler)
}

// Publish 同步发布事件。
func (b *Bus) Publish(event Event) {
	if b == nil {
		return
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}

	b.mu.Lock()
	if b.retention > 0 {
		if len(b.events) == b.retention {
			copy(b.events, b.events[1:])
			b.events[len(b.events)-1] = cloneEvent(event)
		} else {
			b.events = append(b.events, cloneEvent(event))
		}
	}
	typed := append([]Handler(nil), b.subscribers[event.Type]...)
	all := append([]Handler(nil), b.all...)
	b.mu.Unlock()

	for _, handler := range all {
		handler(event)
	}
	for _, handler := range typed {
		handler(event)
	}
}

// Recent 返回最近留存的事件。
func (b *Bus) Recent(limit int) []Event {
	if b == nil {
		return nil
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	return limitEvents(cloneEvents(b.events), limit)
}

// Trace 返回指定 trace 的最近事件。
func (b *Bus) Trace(traceID string, limit int) []Event {
	return b.Query(QueryFilter{
		TraceID: traceID,
		Limit:   limit,
	})
}

// Query 根据过滤条件检索最近留存的事件。
func (b *Bus) Query(filter QueryFilter) []Event {
	if b == nil {
		return nil
	}

	b.mu.RLock()
	events := cloneEvents(b.events)
	b.mu.RUnlock()

	if len(events) == 0 {
		return nil
	}

	return filterEvents(events, filter)
}

// RecentTraces 返回最近 trace 的聚合摘要。
func (b *Bus) RecentTraces(filter TraceFilter) []TraceSummary {
	if b == nil {
		return nil
	}

	b.mu.RLock()
	events := cloneEvents(b.events)
	b.mu.RUnlock()

	if len(events) == 0 {
		return nil
	}

	candidates := filterEvents(events, QueryFilter{
		SessionID:           filter.SessionID,
		AgentName:           filter.AgentName,
		ToolName:            filter.ToolName,
		EventType:           filter.EventType,
		TeamID:              filter.TeamID,
		ProfileResourceKind: filter.ProfileResourceKind,
	})
	if len(candidates) == 0 {
		return nil
	}

	selectedTraceIDs := make(map[string]bool)
	for _, event := range candidates {
		if event.TraceID == "" {
			continue
		}
		if filter.TraceIDPrefix != "" && !strings.HasPrefix(event.TraceID, filter.TraceIDPrefix) {
			continue
		}
		selectedTraceIDs[event.TraceID] = true
	}
	if len(selectedTraceIDs) == 0 {
		return nil
	}

	type groupedSummary struct {
		summary      TraceSummary
		agentSet     map[string]bool
		sessionSet   map[string]bool
		teamSet      map[string]bool
		mcpSet       map[string]bool
		transportSet map[string]bool
		toolSet      map[string]bool
		ticketSet    map[string]bool
	}

	grouped := make(map[string]*groupedSummary)
	order := make([]string, 0)
	for index := len(events) - 1; index >= 0; index-- {
		event := events[index]
		if event.TraceID == "" || !selectedTraceIDs[event.TraceID] {
			continue
		}

		item, ok := grouped[event.TraceID]
		if !ok {
			item = &groupedSummary{
				summary: TraceSummary{
					TraceID:    event.TraceID,
					EventTypes: make(map[string]int),
					Governance: GovernanceView{
						Policies:      make(map[string]int),
						Reasons:       make(map[string]int),
						PatchPolicies: make(map[string]int),
					},
					Execution: ExecutionView{
						Reducers:      make(map[string]int),
						SubagentRoles: make(map[string]int),
						AppliedBy:     make(map[string]int),
					},
					Provenance: ProvenanceView{
						ProfileResourceKinds: make(map[string]int),
					},
					StartedAt: event.Timestamp,
					EndedAt:   event.Timestamp,
				},
				agentSet:     make(map[string]bool),
				sessionSet:   make(map[string]bool),
				teamSet:      make(map[string]bool),
				mcpSet:       make(map[string]bool),
				transportSet: make(map[string]bool),
				toolSet:      make(map[string]bool),
				ticketSet:    make(map[string]bool),
			}
			grouped[event.TraceID] = item
			order = append(order, event.TraceID)
		}

		item.summary.EventCount++
		item.summary.EventTypes[event.Type]++
		if !event.Timestamp.IsZero() {
			if item.summary.StartedAt.IsZero() || event.Timestamp.Before(item.summary.StartedAt) {
				item.summary.StartedAt = event.Timestamp
			}
			if item.summary.EndedAt.IsZero() || event.Timestamp.After(item.summary.EndedAt) {
				item.summary.EndedAt = event.Timestamp
			}
		}
		if event.AgentName != "" {
			item.agentSet[event.AgentName] = true
		}
		if event.SessionID != "" {
			item.sessionSet[event.SessionID] = true
		}
		if teamID := eventTeamID(event); teamID != "" {
			item.teamSet[teamID] = true
		}
		if event.ToolName != "" {
			item.toolSet[event.ToolName] = true
		}
		applyGovernanceEvent(&item.summary.Governance, event)
		applyExecutionEvent(&item.summary.Execution, event)
		applyProvenanceEvent(&item.summary.Provenance, event)
		if mcpName, ok := event.Payload["mcp_name"].(string); ok && strings.TrimSpace(mcpName) != "" {
			item.mcpSet[strings.TrimSpace(mcpName)] = true
		}
		if transportType, ok := event.Payload["transport_type"].(string); ok && strings.TrimSpace(transportType) != "" {
			item.transportSet[strings.TrimSpace(transportType)] = true
		}
		if approval, ok := event.Payload["patch_approval"].(map[string]interface{}); ok {
			if ticketID, ok := approval["ticket_id"].(string); ok && strings.TrimSpace(ticketID) != "" {
				item.ticketSet[strings.TrimSpace(ticketID)] = true
			}
		}
	}

	if len(order) == 0 {
		return nil
	}
	if filter.Limit > 0 && len(order) > filter.Limit {
		order = order[:filter.Limit]
	}

	summaries := make([]TraceSummary, 0, len(order))
	for _, traceID := range order {
		item := grouped[traceID]
		if item == nil {
			continue
		}
		item.summary.Agents = sortedKeys(item.agentSet)
		item.summary.Sessions = sortedKeys(item.sessionSet)
		item.summary.TeamIDs = sortedKeys(item.teamSet)
		item.summary.TeamCount = len(item.summary.TeamIDs)
		item.summary.MCPNames = sortedKeys(item.mcpSet)
		item.summary.TransportTypes = sortedKeys(item.transportSet)
		item.summary.Tools = sortedKeys(item.toolSet)
		item.summary.PatchApprovalTickets = sortedKeys(item.ticketSet)
		summaries = append(summaries, item.summary)
	}

	return summaries
}

// TraceStats 返回最近事件/trace 的聚合统计。
func (b *Bus) TraceStats(filter TraceFilter) TraceStats {
	stats := TraceStats{
		EventTypes:     make(map[string]int),
		Agents:         make(map[string]int),
		Sessions:       make(map[string]int),
		TeamIDs:        make(map[string]int),
		MCPNames:       make(map[string]int),
		TransportTypes: make(map[string]int),
		Tools:          make(map[string]int),
		Governance: GovernanceView{
			Policies:      make(map[string]int),
			Reasons:       make(map[string]int),
			PatchPolicies: make(map[string]int),
		},
		Execution: ExecutionView{
			Reducers:      make(map[string]int),
			SubagentRoles: make(map[string]int),
			AppliedBy:     make(map[string]int),
		},
		Provenance: ProvenanceView{
			ProfileResourceKinds: make(map[string]int),
		},
		LatestTraceIDs: []string{},
	}
	if b == nil {
		return stats
	}

	traces := b.RecentTraces(filter)
	if len(traces) == 0 {
		return stats
	}

	stats.TraceCount = len(traces)
	stats.StartedAt = traces[0].StartedAt
	stats.EndedAt = traces[0].EndedAt

	for _, trace := range traces {
		stats.EventCount += trace.EventCount
		stats.LatestTraceIDs = append(stats.LatestTraceIDs, trace.TraceID)

		if stats.StartedAt.IsZero() || (!trace.StartedAt.IsZero() && trace.StartedAt.Before(stats.StartedAt)) {
			stats.StartedAt = trace.StartedAt
		}
		if stats.EndedAt.IsZero() || trace.EndedAt.After(stats.EndedAt) {
			stats.EndedAt = trace.EndedAt
		}

		for eventType, count := range trace.EventTypes {
			stats.EventTypes[eventType] += count
		}
		for _, agent := range trace.Agents {
			stats.Agents[agent]++
		}
		for _, session := range trace.Sessions {
			stats.Sessions[session]++
		}
		for _, teamID := range trace.TeamIDs {
			stats.TeamIDs[teamID]++
		}
		stats.TeamCount = len(stats.TeamIDs)
		for _, mcpName := range trace.MCPNames {
			stats.MCPNames[mcpName]++
		}
		for _, transportType := range trace.TransportTypes {
			stats.TransportTypes[transportType]++
		}
		for _, tool := range trace.Tools {
			stats.Tools[tool]++
		}
		stats.Governance.DeniedEvents += trace.Governance.DeniedEvents
		stats.Governance.ToolDenied += trace.Governance.ToolDenied
		stats.Governance.SubagentDenied += trace.Governance.SubagentDenied
		stats.Governance.PatchDecisions += trace.Governance.PatchDecisions
		stats.Governance.PatchBlocked += trace.Governance.PatchBlocked
		stats.Governance.PatchApproved += trace.Governance.PatchApproved
		stats.Governance.PatchApprovedOverride += trace.Governance.PatchApprovedOverride
		stats.Governance.PatchApprovalsWithTicket += trace.Governance.PatchApprovalsWithTicket
		for policy, count := range trace.Governance.Policies {
			stats.Governance.Policies[policy] += count
		}
		for reason, count := range trace.Governance.Reasons {
			stats.Governance.Reasons[reason] += count
		}
		for policy, count := range trace.Governance.PatchPolicies {
			stats.Governance.PatchPolicies[policy] += count
		}
		stats.Execution.ToolRequested += trace.Execution.ToolRequested
		stats.Execution.ToolCompleted += trace.Execution.ToolCompleted
		stats.Execution.ToolReduced += trace.Execution.ToolReduced
		stats.Execution.ArtifactRefs += trace.Execution.ArtifactRefs
		stats.Execution.SubagentBatches += trace.Execution.SubagentBatches
		stats.Execution.SubagentBatchDone += trace.Execution.SubagentBatchDone
		stats.Execution.SubagentStarted += trace.Execution.SubagentStarted
		stats.Execution.SubagentCompleted += trace.Execution.SubagentCompleted
		stats.Execution.PatchApplied += trace.Execution.PatchApplied
		for reducer, count := range trace.Execution.Reducers {
			stats.Execution.Reducers[reducer] += count
		}
		for role, count := range trace.Execution.SubagentRoles {
			stats.Execution.SubagentRoles[role] += count
		}
		for actor, count := range trace.Execution.AppliedBy {
			stats.Execution.AppliedBy[actor] += count
		}
		stats.Provenance.ProfileContextInjected += trace.Provenance.ProfileContextInjected
		stats.Provenance.RecallWithSourceRefs += trace.Provenance.RecallWithSourceRefs
		stats.Provenance.ProfileResourceRefs = mergeSortedStrings(stats.Provenance.ProfileResourceRefs, trace.Provenance.ProfileResourceRefs)
		for kind, count := range trace.Provenance.ProfileResourceKinds {
			stats.Provenance.ProfileResourceKinds[kind] += count
		}
		updateProvenanceDisplay(&stats.Provenance)
	}

	return stats
}

// GovernanceStats 返回最近 denied events 的专门治理统计。
func (b *Bus) GovernanceStats(filter TraceFilter) GovernanceStats {
	stats := GovernanceStats{
		Policies:      make(map[string]int),
		Reasons:       make(map[string]int),
		PatchPolicies: make(map[string]int),
		Agents:        make(map[string]int),
		Sessions:      make(map[string]int),
		TeamIDs:       make(map[string]int),
		Tools:         make(map[string]int),
		MCPNames:      make(map[string]int),
		Execution: ExecutionView{
			Reducers:      make(map[string]int),
			SubagentRoles: make(map[string]int),
			AppliedBy:     make(map[string]int),
		},
		Provenance: ProvenanceView{
			ProfileResourceKinds: make(map[string]int),
		},
		LatestTraceIDs: []string{},
	}
	if b == nil {
		return stats
	}

	traces := b.RecentTraces(filter)
	if len(traces) == 0 {
		return stats
	}

	selectedTraceIDs := make(map[string]bool, len(traces))
	for _, trace := range traces {
		if trace.Governance.DeniedEvents == 0 && trace.Governance.PatchDecisions == 0 {
			continue
		}
		selectedTraceIDs[trace.TraceID] = true
		stats.TraceCount++
		stats.LatestTraceIDs = append(stats.LatestTraceIDs, trace.TraceID)
		if stats.StartedAt.IsZero() || (!trace.StartedAt.IsZero() && trace.StartedAt.Before(stats.StartedAt)) {
			stats.StartedAt = trace.StartedAt
		}
		if stats.EndedAt.IsZero() || trace.EndedAt.After(stats.EndedAt) {
			stats.EndedAt = trace.EndedAt
		}
	}
	if len(selectedTraceIDs) == 0 {
		return stats
	}

	b.mu.RLock()
	events := cloneEvents(b.events)
	b.mu.RUnlock()

	seenAgentByTrace := make(map[string]map[string]bool)
	seenSessionByTrace := make(map[string]map[string]bool)
	seenTeamByTrace := make(map[string]map[string]bool)
	seenToolByTrace := make(map[string]map[string]bool)
	seenMCPByTrace := make(map[string]map[string]bool)

	for _, event := range events {
		if !selectedTraceIDs[event.TraceID] {
			continue
		}
		switch event.Type {
		case "tool.denied":
			stats.DeniedEvents++
			stats.ToolDenied++
		case "subagent.denied":
			stats.DeniedEvents++
			stats.SubagentDenied++
		case "patch.decision":
			stats.PatchDecisions++
			if decision, ok := stringPayloadValue(event.Payload, "patch_decision"); ok {
				switch decision {
				case "blocked":
					stats.PatchBlocked++
				case "approved":
					stats.PatchApproved++
				case "approved_override":
					stats.PatchApproved++
					stats.PatchApprovedOverride++
				}
			}
			if approval, ok := event.Payload["patch_approval"].(map[string]interface{}); ok {
				if ticketID, ok := approval["ticket_id"].(string); ok && strings.TrimSpace(ticketID) != "" {
					stats.PatchApprovalsWithTicket++
				}
			}
		}

		if policy, ok := stringPayloadValue(event.Payload, "policy"); ok {
			stats.Policies[policy]++
		}
		if reason, ok := stringPayloadValue(event.Payload, "reason"); ok {
			stats.Reasons[reason]++
		}
		if patchPolicy, ok := stringPayloadValue(event.Payload, "patch_decision_policy"); ok {
			stats.PatchPolicies[patchPolicy]++
		}
		if event.AgentName != "" {
			if markSeen(seenAgentByTrace, event.TraceID, event.AgentName) {
				stats.Agents[event.AgentName]++
			}
		}
		if event.SessionID != "" {
			if markSeen(seenSessionByTrace, event.TraceID, event.SessionID) {
				stats.Sessions[event.SessionID]++
			}
		}
		if teamID := eventTeamID(event); teamID != "" {
			if markSeen(seenTeamByTrace, event.TraceID, teamID) {
				stats.TeamIDs[teamID]++
			}
		}
		stats.TeamCount = len(stats.TeamIDs)
		if event.ToolName != "" {
			if markSeen(seenToolByTrace, event.TraceID, event.ToolName) {
				stats.Tools[event.ToolName]++
			}
		}
		if mcpName, ok := stringPayloadValue(event.Payload, "mcp_name"); ok {
			if markSeen(seenMCPByTrace, event.TraceID, mcpName) {
				stats.MCPNames[mcpName]++
			}
		}
		applyExecutionEvent(&stats.Execution, event)
		applyProvenanceEvent(&stats.Provenance, event)
	}

	return stats
}

func limitEvents(events []Event, limit int) []Event {
	if limit <= 0 || len(events) <= limit {
		return events
	}
	return append([]Event(nil), events[len(events)-limit:]...)
}

func cloneEvents(events []Event) []Event {
	if len(events) == 0 {
		return nil
	}
	cloned := make([]Event, 0, len(events))
	for _, event := range events {
		cloned = append(cloned, cloneEvent(event))
	}
	return cloned
}

func filterEvents(events []Event, filter QueryFilter) []Event {
	matches := make([]Event, 0, len(events))
	for _, event := range events {
		if filter.TraceID != "" && event.TraceID != filter.TraceID {
			continue
		}
		if filter.SessionID != "" && event.SessionID != filter.SessionID {
			continue
		}
		if filter.AgentName != "" && event.AgentName != filter.AgentName {
			continue
		}
		if filter.ToolName != "" && event.ToolName != filter.ToolName {
			continue
		}
		if filter.TeamID != "" && eventTeamID(event) != filter.TeamID {
			continue
		}
		if filter.ProfileResourceKind != "" && !eventHasProfileResourceKind(event, filter.ProfileResourceKind) {
			continue
		}
		if filter.EventType != "" && !strings.HasPrefix(event.Type, filter.EventType) {
			continue
		}
		matches = append(matches, event)
	}

	return limitEvents(matches, filter.Limit)
}

func sortedKeys(values map[string]bool) []string {
	if len(values) == 0 {
		return nil
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func applyGovernanceEvent(summary *GovernanceView, event Event) {
	if summary == nil {
		return
	}
	if summary.Policies == nil {
		summary.Policies = make(map[string]int)
	}
	if summary.Reasons == nil {
		summary.Reasons = make(map[string]int)
	}
	if summary.PatchPolicies == nil {
		summary.PatchPolicies = make(map[string]int)
	}
	switch event.Type {
	case "tool.denied", "subagent.denied":
		summary.DeniedEvents++
		if event.Type == "tool.denied" {
			summary.ToolDenied++
		}
		if event.Type == "subagent.denied" {
			summary.SubagentDenied++
		}
		if policy, ok := event.Payload["policy"].(string); ok && strings.TrimSpace(policy) != "" {
			summary.Policies[strings.TrimSpace(policy)]++
		}
		if reason, ok := event.Payload["reason"].(string); ok && strings.TrimSpace(reason) != "" {
			summary.Reasons[strings.TrimSpace(reason)]++
		}
	case "patch.decision":
		summary.PatchDecisions++
		if policy, ok := event.Payload["patch_decision_policy"].(string); ok && strings.TrimSpace(policy) != "" {
			summary.PatchPolicies[strings.TrimSpace(policy)]++
		}
		if decision, ok := event.Payload["patch_decision"].(string); ok {
			switch strings.TrimSpace(decision) {
			case "blocked":
				summary.PatchBlocked++
			case "approved":
				summary.PatchApproved++
			case "approved_override":
				summary.PatchApproved++
				summary.PatchApprovedOverride++
			}
		}
		if approval, ok := event.Payload["patch_approval"].(map[string]interface{}); ok {
			if ticketID, ok := approval["ticket_id"].(string); ok && strings.TrimSpace(ticketID) != "" {
				summary.PatchApprovalsWithTicket++
			}
		}
	}
}

func applyExecutionEvent(summary *ExecutionView, event Event) {
	if summary == nil {
		return
	}
	if summary.Reducers == nil {
		summary.Reducers = make(map[string]int)
	}
	if summary.SubagentRoles == nil {
		summary.SubagentRoles = make(map[string]int)
	}
	if summary.AppliedBy == nil {
		summary.AppliedBy = make(map[string]int)
	}

	switch event.Type {
	case "tool.requested":
		summary.ToolRequested++
	case "tool.completed":
		summary.ToolCompleted++
	case "tool.reduced":
		summary.ToolReduced++
		if reducer, ok := stringPayloadValue(event.Payload, "reducer"); ok {
			summary.Reducers[reducer]++
		}
		summary.ArtifactRefs += intPayloadValue(event.Payload, "artifact_ref_count")
	case "subagent.batch.started":
		summary.SubagentBatches++
	case "subagent.batch.completed":
		summary.SubagentBatchDone++
	case "subagent.started":
		summary.SubagentStarted++
		if role, ok := stringPayloadValue(event.Payload, "role"); ok {
			summary.SubagentRoles[role]++
		}
	case "subagent.completed":
		summary.SubagentCompleted++
	case "patch.applied":
		summary.PatchApplied++
		summary.ArtifactRefs += intPayloadValue(event.Payload, "artifact_ref_count")
		for _, actor := range stringSlicePayloadValue(event.Payload, "applied_by") {
			summary.AppliedBy[actor]++
		}
	}
}

func applyProvenanceEvent(summary *ProvenanceView, event Event) {
	if summary == nil {
		return
	}
	if summary.ProfileResourceKinds == nil {
		summary.ProfileResourceKinds = make(map[string]int)
	}

	refs := eventSourceRefs(event.Payload)
	switch event.Type {
	case "context.profile.injected":
		summary.ProfileContextInjected++
	case "recall.performed":
		if len(refs) > 0 {
			summary.RecallWithSourceRefs++
		}
	}

	if len(refs) == 0 {
		return
	}
	summary.ProfileResourceRefs = mergeSortedStrings(summary.ProfileResourceRefs, refs)
	for _, ref := range refs {
		if kind := profileResourceKind(ref); kind != "" {
			summary.ProfileResourceKinds[kind]++
		}
	}
	updateProvenanceDisplay(summary)
}

// ApplyProvenanceEventForAPI exposes provenance aggregation for API-side event list summaries.
func ApplyProvenanceEventForAPI(summary *ProvenanceView, event Event) {
	applyProvenanceEvent(summary, event)
}

func isGovernanceDeniedEvent(eventType string) bool {
	return eventType == "tool.denied" || eventType == "subagent.denied"
}

func isGovernanceAuditEvent(eventType string) bool {
	return isGovernanceDeniedEvent(eventType) || eventType == "patch.decision"
}

func stringPayloadValue(payload map[string]interface{}, key string) (string, bool) {
	if len(payload) == 0 {
		return "", false
	}
	value, ok := payload[key].(string)
	if !ok {
		return "", false
	}
	value = strings.TrimSpace(value)
	return value, value != ""
}

func intPayloadValue(payload map[string]interface{}, key string) int {
	if len(payload) == 0 {
		return 0
	}
	switch value := payload[key].(type) {
	case int:
		return value
	case int32:
		return int(value)
	case int64:
		return int(value)
	case float32:
		return int(value)
	case float64:
		return int(value)
	default:
		return 0
	}
}

func stringSlicePayloadValue(payload map[string]interface{}, key string) []string {
	if len(payload) == 0 {
		return nil
	}
	switch value := payload[key].(type) {
	case []string:
		return append([]string(nil), value...)
	case []interface{}:
		out := make([]string, 0, len(value))
		for _, item := range value {
			text, ok := item.(string)
			if !ok {
				continue
			}
			text = strings.TrimSpace(text)
			if text == "" {
				continue
			}
			out = append(out, text)
		}
		return out
	default:
		return nil
	}
}

func markSeen(sets map[string]map[string]bool, traceID, value string) bool {
	if traceID == "" || value == "" {
		return false
	}
	if sets[traceID] == nil {
		sets[traceID] = make(map[string]bool)
	}
	if sets[traceID][value] {
		return false
	}
	sets[traceID][value] = true
	return true
}

func eventSourceRefs(payload map[string]interface{}) []string {
	if len(payload) == 0 {
		return nil
	}
	for _, key := range []string{"source_refs", "profile_source_refs"} {
		value, ok := payload[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case []string:
			return mergeSortedStrings(nil, typed)
		case []interface{}:
			out := make([]string, 0, len(typed))
			for _, item := range typed {
				if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
					out = append(out, strings.TrimSpace(text))
				}
			}
			return mergeSortedStrings(nil, out)
		}
	}
	return nil
}

func profileResourceKind(ref string) string {
	switch {
	case strings.HasPrefix(ref, "profile-resource:memory:"):
		return "memory"
	case strings.HasPrefix(ref, "profile-resource:notes:"):
		return "notes"
	default:
		return ""
	}
}

func mergeSortedStrings(base []string, extra []string) []string {
	if len(base) == 0 && len(extra) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(base)+len(extra))
	result := make([]string, 0, len(base)+len(extra))
	for _, value := range append(append([]string(nil), base...), extra...) {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func eventHasProfileResourceKind(event Event, kind string) bool {
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind == "" {
		return true
	}
	for _, ref := range eventSourceRefs(event.Payload) {
		switch {
		case kind == "memory" && strings.HasPrefix(ref, "profile-resource:memory:"):
			return true
		case kind == "notes" && strings.HasPrefix(ref, "profile-resource:notes:"):
			return true
		}
	}
	return false
}

func updateProvenanceDisplay(summary *ProvenanceView) {
	if summary == nil {
		return
	}
	summary.ProfileResourceCount = len(summary.ProfileResourceRefs)
	memoryCount, notesCount, labels := profileResourceDisplay(summary.ProfileResourceRefs)
	summary.ProfileMemoryCount = memoryCount
	summary.ProfileNotesCount = notesCount
	summary.ProfileResourceLabels = labels
}

func profileResourceDisplay(refs []string) (int, int, []string) {
	if len(refs) == 0 {
		return 0, 0, nil
	}
	labels := make([]string, 0, len(refs))
	memoryCount := 0
	notesCount := 0
	for _, ref := range refs {
		kind, label := profileResourceLabel(ref)
		switch kind {
		case "memory":
			memoryCount++
		case "notes":
			notesCount++
		}
		if label != "" {
			labels = append(labels, label)
		}
	}
	return memoryCount, notesCount, mergeSortedStrings(nil, labels)
}

func profileResourceLabel(ref string) (string, string) {
	switch {
	case strings.HasPrefix(ref, "profile-resource:memory:"):
		return "memory", "memory:" + shortProfileResourceName(strings.TrimPrefix(ref, "profile-resource:memory:"))
	case strings.HasPrefix(ref, "profile-resource:notes:"):
		return "notes", "notes:" + shortProfileResourceName(strings.TrimPrefix(ref, "profile-resource:notes:"))
	default:
		return "", ""
	}
}

func shortProfileResourceName(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	if value == "" {
		return ""
	}
	if index := strings.LastIndex(value, "/"); index >= 0 && index+1 < len(value) {
		return value[index+1:]
	}
	return value
}

func cloneEvent(event Event) Event {
	cloned := event
	if len(event.Payload) > 0 {
		cloned.Payload = make(map[string]interface{}, len(event.Payload))
		for key, value := range event.Payload {
			cloned.Payload[key] = value
		}
	}
	return cloned
}

func eventTeamID(event Event) string {
	if value, ok := stringPayloadValue(event.Payload, "team_id"); ok {
		return value
	}
	if value, ok := stringPayloadValue(event.Payload, "teamID"); ok {
		return value
	}
	return ""
}
