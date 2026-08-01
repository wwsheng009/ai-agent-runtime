package runtimeserver

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

// SupervisionRuntimeExecutor maps durable supervision actions to the existing
// AgentControl and Team stores. A host provides CloseAgent because API and CLI
// own different session hubs, while the graph traversal itself stays shared.
type SupervisionRuntimeExecutor struct {
	Store               supervision.Store
	TeamStore           team.Store
	AgentRegistry       agentcontrol.AgentRegistryReader
	AgentRegistryWriter agentcontrol.AgentRegistryWriter
	CloseAgent          func(ctx context.Context, sessionID string) error
}

// Execute performs only runtime mutation actions. Bookkeeping actions are
// completed by runtimeActionExecutor before this adapter is called.
func (e SupervisionRuntimeExecutor) Execute(ctx context.Context, action supervision.ActionRecord) (supervision.ActionResult, error) {
	switch action.Action {
	case supervision.ActionRetry, supervision.ActionReassign:
		return supervision.ActionResult{
			Status: supervision.ActionFailed,
			Result: fmt.Sprintf("%s is not enabled by this runtime executor", action.Action),
		}, nil
	case supervision.ActionCancel, supervision.ActionClose, supervision.ActionCancelSubtree:
	default:
		return supervision.ActionResult{Status: supervision.ActionFailed, Result: "unsupported runtime action " + string(action.Action)}, nil
	}

	switch action.TargetKind {
	case supervision.SubjectAgentSession, supervision.SubjectAgentRun:
		return e.executeAgentAction(ctx, action)
	case supervision.SubjectTeam:
		return e.executeTeamAction(ctx, action)
	case supervision.SubjectTeamTask:
		return e.executeTaskAction(ctx, action)
	default:
		return supervision.ActionResult{Status: supervision.ActionFailed, Result: "unsupported target kind " + string(action.TargetKind)}, nil
	}
}

func (e SupervisionRuntimeExecutor) executeAgentAction(ctx context.Context, action supervision.ActionRecord) (supervision.ActionResult, error) {
	targets, err := e.agentTargets(ctx, action)
	if err != nil {
		return supervision.ActionResult{Status: supervision.ActionFailed, Result: err.Error()}, nil
	}
	results := make([]supervision.NodeActionResult, 0, len(targets))
	for _, target := range targets {
		result := supervision.NodeActionResult{
			Kind:   action.TargetKind,
			ID:     target.ID,
			Status: supervision.ActionCompleted,
			Result: "agent closed",
		}
		if e.CloseAgent == nil {
			result.Status = supervision.ActionFailed
			result.Result = "agent close adapter is not configured"
		} else if err := e.CloseAgent(ctx, target.ID); err != nil {
			result.Status = supervision.ActionFailed
			result.Result = err.Error()
		} else if e.AgentRegistryWriter != nil && target.Record != nil && !target.Record.Closed() {
			// Persist the durable identity subtree after the live session
			// closed, so the AgentControl graph and the supervision snapshot
			// agree on the terminal state (doc 6.2 / 7.3).
			if _, err := e.AgentRegistryWriter.CloseAgentControlAgentSubtree(
				ctx,
				target.Record.RootSessionID,
				target.Record.AgentPath,
				time.Now().UTC(),
			); err != nil {
				result.Status = supervision.ActionFailed
				result.Result = "close live session ok, but persist agent graph close: " + err.Error()
			}
		}
		results = append(results, result)
	}
	return aggregateActionResults(results, "agent action completed"), nil
}

// agentTarget is one resolved close target. Record is non-nil when the target
// was resolved through the durable AgentControl graph, which is what allows
// the writer to persist the terminal subtree state afterwards.
type agentTarget struct {
	ID     string
	Record *agentcontrol.AgentRecord
}

func (e SupervisionRuntimeExecutor) agentTargets(ctx context.Context, action supervision.ActionRecord) ([]agentTarget, error) {
	targetID := strings.TrimSpace(action.TargetID)
	if targetID == "" {
		return nil, fmt.Errorf("agent target id is required")
	}
	needsSubtree := action.Action == supervision.ActionCancelSubtree || action.CascadeMode == supervision.CascadeDescendants
	if !needsSubtree {
		// Resolve the target (which may be an AgentID or a SessionID) to the
		// durable record when the graph is available, so the close adapter
		// always receives a session ID and the writer can persist the close.
		record, err := e.resolveAgentRecord(ctx, action, targetID)
		if err != nil {
			return nil, err
		}
		id := targetID
		if record != nil {
			if sessionID := strings.TrimSpace(record.SessionID); sessionID != "" {
				id = sessionID
			}
		}
		return []agentTarget{{ID: id, Record: record}}, nil
	}
	if e.AgentRegistry == nil {
		return nil, fmt.Errorf("agent graph is required for subtree action")
	}
	records, err := e.AgentRegistry.ListAgentControlAgents(ctx, agentcontrol.AgentFilter{
		RootSessionID: strings.TrimSpace(action.RootScopeID),
		IncludeClosed: true,
	})
	if err != nil {
		return nil, fmt.Errorf("list agent subtree: %w", err)
	}
	var root *agentcontrol.AgentRecord
	for i := range records {
		if strings.EqualFold(strings.TrimSpace(records[i].AgentID), targetID) ||
			strings.EqualFold(strings.TrimSpace(records[i].SessionID), targetID) {
			root = &records[i]
			break
		}
	}
	if root == nil {
		return nil, fmt.Errorf("target agent is not in the durable root scope")
	}
	selected := make([]agentcontrol.AgentRecord, 0, len(records))
	for _, record := range records {
		if record.Closed() || !agentcontrol.AgentPathMatchesPrefix(record.AgentPath, root.AgentPath) {
			continue
		}
		selected = append(selected, record)
	}
	// Leaf-first prevents a parent close from orphaning still-runnable children.
	sort.SliceStable(selected, func(i, j int) bool {
		return strings.Count(selected[i].AgentPath, "/") > strings.Count(selected[j].AgentPath, "/")
	})
	targets := make([]agentTarget, 0, len(selected))
	seen := make(map[string]struct{}, len(selected))
	for _, record := range selected {
		id := strings.TrimSpace(record.SessionID)
		if id == "" {
			id = strings.TrimSpace(record.AgentID)
		}
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		copyRecord := record
		targets = append(targets, agentTarget{ID: id, Record: &copyRecord})
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("no active agent descendants found")
	}
	return targets, nil
}

// resolveAgentRecord finds the durable record for a single agent target
// (AgentID, SessionID or path). It returns (nil, nil) when the graph is not
// configured (permissive hosts keep working); when the graph is configured
// but the target is not inside the root scope it returns an error so an
// ambiguous or cross-scope identifier is never forwarded to the close
// adapter.
func (e SupervisionRuntimeExecutor) resolveAgentRecord(ctx context.Context, action supervision.ActionRecord, targetID string) (*agentcontrol.AgentRecord, error) {
	if e.AgentRegistry == nil {
		return nil, nil
	}
	records, err := e.AgentRegistry.ListAgentControlAgents(ctx, agentcontrol.AgentFilter{
		RootSessionID: strings.TrimSpace(action.RootScopeID),
		IncludeClosed: true,
	})
	if err != nil {
		return nil, fmt.Errorf("list agent targets: %w", err)
	}
	for i := range records {
		record := &records[i]
		if strings.EqualFold(strings.TrimSpace(record.AgentID), targetID) ||
			strings.EqualFold(strings.TrimSpace(record.SessionID), targetID) ||
			strings.EqualFold(record.AgentPath, targetID) {
			return record, nil
		}
	}
	return nil, fmt.Errorf("target agent %q is not in the durable root scope %q", targetID, strings.TrimSpace(action.RootScopeID))
}

func (e SupervisionRuntimeExecutor) executeTeamAction(ctx context.Context, action supervision.ActionRecord) (supervision.ActionResult, error) {
	if e.TeamStore == nil {
		return supervision.ActionResult{Status: supervision.ActionFailed, Result: "team store is not configured"}, nil
	}
	teamIDs, err := e.teamTargets(ctx, action)
	if err != nil {
		return supervision.ActionResult{Status: supervision.ActionFailed, Result: err.Error()}, nil
	}
	results := make([]supervision.NodeActionResult, 0, len(teamIDs))
	for _, teamID := range teamIDs {
		result := e.cancelOneTeam(ctx, teamID)
		results = append(results, result...)
	}
	return aggregateActionResults(results, "team action completed"), nil
}

func (e SupervisionRuntimeExecutor) teamTargets(ctx context.Context, action supervision.ActionRecord) ([]string, error) {
	targetID := strings.TrimSpace(action.TargetID)
	if targetID == "" {
		return nil, fmt.Errorf("team target id is required")
	}
	needsSubtree := action.Action == supervision.ActionCancelSubtree || action.CascadeMode == supervision.CascadeDescendants
	if !needsSubtree {
		return []string{targetID}, nil
	}
	if e.Store == nil {
		return nil, fmt.Errorf("supervision team graph is required for subtree action")
	}
	seen := map[string]bool{}
	var visit func(string) ([]string, error)
	visit = func(teamID string) ([]string, error) {
		teamID = strings.TrimSpace(teamID)
		if teamID == "" || seen[teamID] {
			return nil, nil
		}
		seen[teamID] = true
		edges, err := e.Store.ListChildTeams(ctx, teamID)
		if err != nil {
			return nil, fmt.Errorf("list child teams for %s: %w", teamID, err)
		}
		var ids []string
		for _, edge := range edges {
			children, err := visit(edge.ChildTeamID)
			if err != nil {
				return nil, err
			}
			ids = append(ids, children...)
		}
		return append(ids, teamID), nil
	}
	return visit(targetID)
}

func (e SupervisionRuntimeExecutor) cancelOneTeam(ctx context.Context, teamID string) []supervision.NodeActionResult {
	teamResult := supervision.NodeActionResult{Kind: supervision.SubjectTeam, ID: teamID, Status: supervision.ActionCompleted, Result: "team cancelled"}
	var results []supervision.NodeActionResult
	tasks, err := e.TeamStore.ListTasks(ctx, team.TaskFilter{TeamID: teamID})
	if err != nil {
		teamResult.Status = supervision.ActionFailed
		teamResult.Result = "list team tasks: " + err.Error()
		return []supervision.NodeActionResult{teamResult}
	}
	for _, task := range tasks {
		switch task.Status {
		case team.TaskStatusPending, team.TaskStatusReady, team.TaskStatusRunning, team.TaskStatusBlocked:
			result := supervision.NodeActionResult{Kind: supervision.SubjectTeamTask, ID: task.ID, Status: supervision.ActionCompleted, Result: "task cancelled"}
			if err := e.TeamStore.UpdateTaskStatus(ctx, task.ID, team.TaskStatusCancelled, "cancelled by supervision action"); err != nil {
				result.Status = supervision.ActionFailed
				result.Result = err.Error()
			}
			results = append(results, result)
		}
	}
	teammates, err := e.TeamStore.ListTeammates(ctx, teamID)
	if err != nil {
		teamResult.Status = supervision.ActionFailed
		teamResult.Result = "list teammates: " + err.Error()
	} else {
		for _, teammate := range teammates {
			sessionID := strings.TrimSpace(teammate.SessionID)
			if sessionID == "" || e.CloseAgent == nil {
				continue
			}
			result := supervision.NodeActionResult{Kind: supervision.SubjectAgentSession, ID: sessionID, Status: supervision.ActionCompleted, Result: "teammate closed"}
			if err := e.CloseAgent(ctx, sessionID); err != nil {
				result.Status = supervision.ActionFailed
				result.Result = err.Error()
			}
			results = append(results, result)
		}
	}
	if err := e.TeamStore.UpdateTeamStatus(ctx, teamID, team.TeamStatusCanceled); err != nil {
		teamResult.Status = supervision.ActionFailed
		teamResult.Result = err.Error()
	}
	return append(results, teamResult)
}

func (e SupervisionRuntimeExecutor) executeTaskAction(ctx context.Context, action supervision.ActionRecord) (supervision.ActionResult, error) {
	if e.TeamStore == nil {
		return supervision.ActionResult{Status: supervision.ActionFailed, Result: "team store is not configured"}, nil
	}
	result := supervision.NodeActionResult{Kind: supervision.SubjectTeamTask, ID: action.TargetID, Status: supervision.ActionCompleted, Result: "task cancelled"}
	if err := e.TeamStore.UpdateTaskStatus(ctx, action.TargetID, team.TaskStatusCancelled, "cancelled by supervision action"); err != nil {
		result.Status = supervision.ActionFailed
		result.Result = err.Error()
	}
	return aggregateActionResults([]supervision.NodeActionResult{result}, "task action completed"), nil
}

func aggregateActionResults(results []supervision.NodeActionResult, success string) supervision.ActionResult {
	if len(results) == 0 {
		return supervision.ActionResult{Status: supervision.ActionCompleted, Result: success}
	}
	failed := 0
	for _, result := range results {
		if result.Status == supervision.ActionFailed || result.Status == supervision.ActionRejected {
			failed++
		}
	}
	status := supervision.ActionCompleted
	message := success
	if failed == len(results) {
		status = supervision.ActionFailed
		message = "runtime action failed for every target"
	} else if failed > 0 {
		status = supervision.ActionPartiallyCompleted
		message = "runtime action completed partially"
	}
	return supervision.ActionResult{Status: status, Result: message, NodeResults: results}
}
