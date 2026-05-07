package team

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
)

// AgentControlTaskRegistry adapts the team task store to the shared
// AgentControl task registry read seam. Team remains the write authority.
type AgentControlTaskRegistry struct {
	Store Store
}

var _ agentcontrol.TaskRegistryReader = AgentControlTaskRegistry{}
var _ agentcontrol.TaskRegistryCreateWriter = AgentControlTaskRegistry{}
var _ agentcontrol.TaskDependencyReader = AgentControlTaskRegistry{}
var _ agentcontrol.TaskDependencyCreateWriter = AgentControlTaskRegistry{}
var _ agentcontrol.TaskRegistryStatusWriter = AgentControlTaskRegistry{}
var _ agentcontrol.TaskRegistryReleaseWriter = AgentControlTaskRegistry{}
var _ agentcontrol.TaskRegistryLeaseRenewWriter = AgentControlTaskRegistry{}
var _ agentcontrol.TaskRegistryClaimWriter = AgentControlTaskRegistry{}
var _ agentcontrol.TaskRegistryTerminalWriter = AgentControlTaskRegistry{}
var _ agentcontrol.TaskRegistryBlockWriter = AgentControlTaskRegistry{}
var _ agentcontrol.TaskWakeWatcher = AgentControlTaskRegistry{}
var _ agentcontrol.TaskWakeSequencer = AgentControlTaskRegistry{}

// NewAgentControlTaskRegistry creates a task registry projection over a team
// store.
func NewAgentControlTaskRegistry(store Store) AgentControlTaskRegistry {
	return AgentControlTaskRegistry{Store: store}
}

// ListAgentControlTasks projects team tasks into AgentControl task records.
func (r AgentControlTaskRegistry) ListAgentControlTasks(ctx context.Context, filter agentcontrol.TaskFilter) ([]agentcontrol.TaskRecord, error) {
	if r.Store == nil {
		return nil, nil
	}
	if workflow := strings.TrimSpace(filter.Workflow); workflow != "" && workflow != agentcontrol.WorkflowSpawnTeam {
		return nil, nil
	}
	teamIDs, err := r.registryTeamIDs(ctx, filter.TeamID)
	if err != nil {
		return nil, err
	}
	records := make([]agentcontrol.TaskRecord, 0)
	for _, teamID := range teamIDs {
		teamRecords, err := AgentControlTaskRecords(ctx, r.Store, teamID)
		if err != nil {
			return nil, err
		}
		for _, record := range teamRecords {
			if !agentControlTaskRecordMatches(record, filter) {
				continue
			}
			records = append(records, record)
			if filter.Limit > 0 && len(records) >= filter.Limit {
				return records, nil
			}
		}
	}
	return records, nil
}

// ActiveTaskForAssignee returns the most relevant active task record for a
// teammate through the AgentControl task registry seam.
func (r AgentControlTaskRegistry) ActiveTaskForAssignee(ctx context.Context, teamID, assignee string) (*agentcontrol.TaskRecord, error) {
	records, err := r.ListAgentControlTasks(ctx, agentcontrol.TaskFilter{
		Workflow: agentcontrol.WorkflowSpawnTeam,
		TeamID:   teamID,
		Assignee: assignee,
		Status: []string{
			string(TaskStatusRunning),
			string(TaskStatusReady),
			string(TaskStatusBlocked),
			string(TaskStatusPending),
		},
	})
	if err != nil {
		return nil, err
	}
	var selected *agentcontrol.TaskRecord
	for _, record := range records {
		recordCopy := record
		if selected == nil || activeTaskStatusRank(TaskStatus(recordCopy.Status)) < activeTaskStatusRank(TaskStatus(selected.Status)) {
			selected = &recordCopy
		}
	}
	return selected, nil
}

// WatchAgentControlTaskWake adapts the team task-signal watcher to the shared
// AgentControl task wake seam.
func (r AgentControlTaskRegistry) WatchAgentControlTaskWake(ctx context.Context, filter agentcontrol.TaskWakeFilter) (<-chan agentcontrol.TaskWakeEvent, func()) {
	out := make(chan agentcontrol.TaskWakeEvent, 1)
	if ctx == nil {
		ctx = context.Background()
	}
	if r.Store == nil {
		return out, func() {}
	}
	filter = filter.Normalize()
	if filter.Workflow != "" && filter.Workflow != agentcontrol.WorkflowSpawnTeam {
		return out, func() {}
	}
	watcher, ok := r.Store.(TaskWatcherStore)
	if !ok {
		return out, func() {}
	}
	input, unwatch := watcher.WatchTaskSignals(ctx, filter.TeamID)
	done := make(chan struct{})
	var once sync.Once
	go func() {
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case signal, ok := <-input:
				if !ok {
					return
				}
				event := taskSignalToAgentControlWake(signal)
				if event.TeamID == "" || (filter.TeamID != "" && !strings.EqualFold(event.TeamID, filter.TeamID)) {
					continue
				}
				select {
				case out <- event:
				default:
				}
			}
		}
	}()
	return out, func() {
		once.Do(func() {
			close(done)
			unwatch()
		})
	}
}

// LastAgentControlTaskWakeSeq adapts the team task-signal sequence to the
// shared AgentControl task wake seam.
func (r AgentControlTaskRegistry) LastAgentControlTaskWakeSeq(ctx context.Context, filter agentcontrol.TaskWakeFilter) (int64, error) {
	if r.Store == nil {
		return 0, nil
	}
	filter = filter.Normalize()
	if filter.Workflow != "" && filter.Workflow != agentcontrol.WorkflowSpawnTeam {
		return 0, nil
	}
	sequencer, ok := r.Store.(TaskSequenceStore)
	if !ok {
		return 0, nil
	}
	return sequencer.LastTaskSignalSeq(ctx, filter.TeamID)
}

// CreateAgentControlTask maps the shared AgentControl task create writer seam
// onto the current team task store.
func (r AgentControlTaskRegistry) CreateAgentControlTask(ctx context.Context, request agentcontrol.TaskCreateRequest) (*agentcontrol.TaskRecord, error) {
	if r.Store == nil {
		return nil, fmt.Errorf("team store is not configured")
	}
	request = request.Normalize()
	if request.Workflow != "" && request.Workflow != agentcontrol.WorkflowSpawnTeam {
		return nil, fmt.Errorf("unsupported task workflow: %s", request.Workflow)
	}
	if request.TeamID == "" {
		return nil, fmt.Errorf("team id is required")
	}
	status := TaskStatus(request.Status)
	if status != "" && !isAgentControlWritableTaskStatus(status) {
		return nil, fmt.Errorf("unsupported task status: %s", request.Status)
	}
	var parentID *string
	if request.ParentTaskID != "" {
		value := request.ParentTaskID
		parentID = &value
	}
	var assignee *string
	if request.Assignee != "" {
		value := request.Assignee
		assignee = &value
	}
	var resultRef *string
	if request.ResultRef != "" {
		value := request.ResultRef
		resultRef = &value
	}
	task := Task{
		ID:           request.ID,
		TeamID:       request.TeamID,
		ParentTaskID: parentID,
		Title:        request.Title,
		Goal:         request.Goal,
		Status:       status,
		Priority:     request.Priority,
		Assignee:     assignee,
		Inputs:       request.Inputs,
		ReadPaths:    request.ReadPaths,
		WritePaths:   request.WritePaths,
		Deliverables: request.Deliverables,
		Summary:      request.Summary,
		ResultRef:    resultRef,
	}
	taskID, err := r.Store.CreateTask(ctx, task)
	if err != nil {
		return nil, err
	}
	created, err := r.Store.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if created == nil {
		task.ID = taskID
		created = &task
	}
	var mate *Teammate
	if assignee := taskAssigneeID(*created); assignee != "" {
		if record, err := r.Store.GetTeammate(ctx, assignee); err == nil && record != nil {
			mate = record
		}
	}
	record := AgentControlTaskRecord(*created, mate)
	return &record, nil
}

// CreateAgentControlTaskDependency maps the shared AgentControl task graph
// writer seam onto the current team task dependency store.
func (r AgentControlTaskRegistry) CreateAgentControlTaskDependency(ctx context.Context, request agentcontrol.TaskDependencyCreateRequest) error {
	if r.Store == nil {
		return fmt.Errorf("team store is not configured")
	}
	request = request.Normalize()
	if request.Workflow != "" && request.Workflow != agentcontrol.WorkflowSpawnTeam {
		return fmt.Errorf("unsupported task workflow: %s", request.Workflow)
	}
	if request.TaskID == "" || request.DependsOnID == "" {
		return fmt.Errorf("task dependency ids are required")
	}
	if request.TeamID != "" {
		task, err := r.Store.GetTask(ctx, request.TaskID)
		if err != nil {
			return err
		}
		if task == nil {
			return fmt.Errorf("task not found: %s", request.TaskID)
		}
		if !strings.EqualFold(strings.TrimSpace(task.TeamID), request.TeamID) {
			return fmt.Errorf("task does not belong to team: %s", request.TaskID)
		}
		dependsOn, err := r.Store.GetTask(ctx, request.DependsOnID)
		if err != nil {
			return err
		}
		if dependsOn == nil {
			return fmt.Errorf("dependency task not found: %s", request.DependsOnID)
		}
		if !strings.EqualFold(strings.TrimSpace(dependsOn.TeamID), request.TeamID) {
			return fmt.Errorf("dependency task does not belong to team: %s", request.DependsOnID)
		}
	}
	return r.Store.AddTaskDependency(ctx, request.TaskID, request.DependsOnID)
}

// ListAgentControlTaskDependencies maps the shared AgentControl task graph
// reader seam onto the current team task dependency store.
func (r AgentControlTaskRegistry) ListAgentControlTaskDependencies(ctx context.Context, filter agentcontrol.TaskDependencyFilter) ([]agentcontrol.TaskDependencyRecord, error) {
	if r.Store == nil {
		return nil, nil
	}
	filter = filter.Normalize()
	if filter.Workflow != "" && filter.Workflow != agentcontrol.WorkflowSpawnTeam {
		return nil, nil
	}
	if filter.TaskID == "" && filter.DependsOnID == "" {
		return nil, fmt.Errorf("task_id or depends_on_id is required")
	}
	if reader, ok := r.Store.(TaskDependencyReaderStore); ok {
		return r.listAgentControlTaskDependencyRecords(ctx, filter, reader)
	}

	records := make([]agentcontrol.TaskDependencyRecord, 0)
	seen := make(map[string]struct{})
	appendRecord := func(record agentcontrol.TaskDependencyRecord) {
		record = record.Normalize()
		key := strings.ToLower(record.TaskID) + "\x00" + strings.ToLower(record.DependsOnID)
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		records = append(records, record)
	}
	if filter.TaskID != "" {
		task, err := r.Store.GetTask(ctx, filter.TaskID)
		if err != nil {
			return nil, err
		}
		if task == nil {
			return nil, fmt.Errorf("task not found: %s", filter.TaskID)
		}
		if filter.TeamID != "" && !strings.EqualFold(strings.TrimSpace(task.TeamID), filter.TeamID) {
			return nil, fmt.Errorf("task does not belong to team: %s", filter.TaskID)
		}
		deps, err := r.Store.ListTaskDependencies(ctx, filter.TaskID)
		if err != nil {
			return nil, err
		}
		for _, dependsOnID := range deps {
			dependsOnID = strings.TrimSpace(dependsOnID)
			if dependsOnID == "" {
				continue
			}
			if filter.DependsOnID != "" && !strings.EqualFold(dependsOnID, filter.DependsOnID) {
				continue
			}
			appendRecord(agentcontrol.TaskDependencyRecord{
				Workflow:    agentcontrol.WorkflowSpawnTeam,
				TeamID:      strings.TrimSpace(task.TeamID),
				TaskID:      strings.TrimSpace(task.ID),
				DependsOnID: dependsOnID,
			})
		}
	}

	if filter.IncludeDependents {
		dependentTargetID := firstNonEmptyString(filter.TaskID, filter.DependsOnID)
		if dependentTargetID == "" {
			return records, nil
		}
		dependencyTask, err := r.Store.GetTask(ctx, dependentTargetID)
		if err != nil {
			return nil, err
		}
		if dependencyTask == nil {
			return nil, fmt.Errorf("dependency task not found: %s", dependentTargetID)
		}
		if filter.TeamID != "" && !strings.EqualFold(strings.TrimSpace(dependencyTask.TeamID), filter.TeamID) {
			return nil, fmt.Errorf("dependency task does not belong to team: %s", dependentTargetID)
		}
		dependents, err := r.Store.ListTaskDependents(ctx, dependentTargetID)
		if err != nil {
			return nil, err
		}
		for _, taskID := range dependents {
			taskID = strings.TrimSpace(taskID)
			if taskID == "" {
				continue
			}
			appendRecord(agentcontrol.TaskDependencyRecord{
				Workflow:    agentcontrol.WorkflowSpawnTeam,
				TeamID:      strings.TrimSpace(dependencyTask.TeamID),
				TaskID:      taskID,
				DependsOnID: strings.TrimSpace(dependencyTask.ID),
			})
		}
	}
	return records, nil
}

func (r AgentControlTaskRegistry) listAgentControlTaskDependencyRecords(ctx context.Context, filter agentcontrol.TaskDependencyFilter, reader TaskDependencyReaderStore) ([]agentcontrol.TaskDependencyRecord, error) {
	if filter.TaskID != "" {
		task, err := r.Store.GetTask(ctx, filter.TaskID)
		if err != nil {
			return nil, err
		}
		if task == nil {
			return nil, fmt.Errorf("task not found: %s", filter.TaskID)
		}
		if filter.TeamID != "" && !strings.EqualFold(strings.TrimSpace(task.TeamID), filter.TeamID) {
			return nil, fmt.Errorf("task does not belong to team: %s", filter.TaskID)
		}
	}
	if filter.DependsOnID != "" {
		dependencyTask, err := r.Store.GetTask(ctx, filter.DependsOnID)
		if err != nil {
			return nil, err
		}
		if dependencyTask == nil {
			return nil, fmt.Errorf("dependency task not found: %s", filter.DependsOnID)
		}
		if filter.TeamID != "" && !strings.EqualFold(strings.TrimSpace(dependencyTask.TeamID), filter.TeamID) {
			return nil, fmt.Errorf("dependency task does not belong to team: %s", filter.DependsOnID)
		}
	}
	edges, err := reader.ListTaskDependencyRecords(ctx, TaskDependencyFilter{
		TaskID:            filter.TaskID,
		DependsOnID:       filter.DependsOnID,
		IncludeDependents: filter.IncludeDependents,
	})
	if err != nil {
		return nil, err
	}
	records := make([]agentcontrol.TaskDependencyRecord, 0, len(edges))
	seen := make(map[string]struct{}, len(edges))
	for _, edge := range edges {
		record, err := r.agentControlDependencyRecordFromTeamEdge(ctx, edge, filter.TeamID)
		if err != nil {
			return nil, err
		}
		if record == nil {
			continue
		}
		key := strings.ToLower(record.TaskID) + "\x00" + strings.ToLower(record.DependsOnID)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		records = append(records, record.Normalize())
	}
	return records, nil
}

func (r AgentControlTaskRegistry) agentControlDependencyRecordFromTeamEdge(ctx context.Context, edge TaskDependency, filterTeamID string) (*agentcontrol.TaskDependencyRecord, error) {
	taskID := strings.TrimSpace(edge.TaskID)
	dependsOnID := strings.TrimSpace(edge.DependsOnID)
	if taskID == "" || dependsOnID == "" {
		return nil, nil
	}
	task, err := r.Store.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if task == nil {
		return nil, fmt.Errorf("task not found: %s", taskID)
	}
	teamID := strings.TrimSpace(task.TeamID)
	if filterTeamID != "" && !strings.EqualFold(teamID, filterTeamID) {
		return nil, nil
	}
	dependencyTask, err := r.Store.GetTask(ctx, dependsOnID)
	if err != nil {
		return nil, err
	}
	if dependencyTask == nil {
		return nil, fmt.Errorf("dependency task not found: %s", dependsOnID)
	}
	if filterTeamID != "" && !strings.EqualFold(strings.TrimSpace(dependencyTask.TeamID), filterTeamID) {
		return nil, nil
	}
	if teamID != "" && strings.TrimSpace(dependencyTask.TeamID) != "" && !strings.EqualFold(teamID, strings.TrimSpace(dependencyTask.TeamID)) {
		return nil, fmt.Errorf("dependency task does not belong to task team: %s", dependsOnID)
	}
	return &agentcontrol.TaskDependencyRecord{
		ID:          strings.TrimSpace(edge.ID),
		Workflow:    agentcontrol.WorkflowSpawnTeam,
		TeamID:      teamID,
		TaskID:      taskID,
		DependsOnID: dependsOnID,
		CreatedAt:   edge.CreatedAt,
	}, nil
}

func taskSignalToAgentControlWake(signal TaskSignal) agentcontrol.TaskWakeEvent {
	return agentcontrol.TaskWakeEvent{
		Seq:       signal.Seq,
		Workflow:  agentcontrol.WorkflowSpawnTeam,
		TeamID:    strings.TrimSpace(signal.TeamID),
		TaskID:    strings.TrimSpace(signal.TaskID),
		Kind:      strings.TrimSpace(signal.Kind),
		Status:    strings.TrimSpace(string(signal.Status)),
		CreatedAt: signal.CreatedAt,
	}.Normalize()
}

// ClaimAgentControlTask maps the shared AgentControl claim writer seam onto
// the current team task store. It preserves the store's optimistic version
// checks and optional path-claim transaction semantics.
func (r AgentControlTaskRegistry) ClaimAgentControlTask(ctx context.Context, request agentcontrol.TaskClaimRequest) (*agentcontrol.TaskRecord, bool, error) {
	if r.Store == nil {
		return nil, false, fmt.Errorf("team store is not configured")
	}
	request = request.Normalize()
	if request.ID == "" {
		return nil, false, fmt.Errorf("task id is required")
	}
	if request.Workflow != "" && request.Workflow != agentcontrol.WorkflowSpawnTeam {
		return nil, false, fmt.Errorf("unsupported task workflow: %s", request.Workflow)
	}
	if request.Assignee == "" {
		return nil, false, fmt.Errorf("assignee is required")
	}
	if request.LeaseUntil.IsZero() {
		return nil, false, fmt.Errorf("lease_until is required")
	}

	claimed := false
	if request.UsePathClaims {
		task := Task{
			ID:         request.ID,
			TeamID:     request.TeamID,
			ReadPaths:  request.ReadPaths,
			WritePaths: request.WritePaths,
			Version:    request.ExpectedVersion,
		}
		if task.TeamID == "" {
			existing, err := r.Store.GetTask(ctx, request.ID)
			if err != nil {
				return nil, false, err
			}
			if existing == nil {
				return nil, false, nil
			}
			task.TeamID = existing.TeamID
			if request.ExpectedVersion <= 0 {
				task.Version = existing.Version
			}
			if len(request.ReadPaths) == 0 {
				task.ReadPaths = existing.ReadPaths
			}
			if len(request.WritePaths) == 0 {
				task.WritePaths = existing.WritePaths
			}
		}
		var err error
		claimed, err = r.Store.ClaimTaskWithPathClaims(ctx, task, request.Assignee, request.LeaseUntil, request.WorkspaceRoot)
		if err != nil || !claimed {
			return nil, claimed, err
		}
	} else {
		var err error
		claimed, err = r.Store.ClaimTask(ctx, request.ID, request.Assignee, request.LeaseUntil, request.ExpectedVersion)
		if err != nil || !claimed {
			return nil, claimed, err
		}
	}

	task, err := r.Store.GetTask(ctx, request.ID)
	if err != nil {
		return nil, true, err
	}
	if task == nil {
		return nil, true, fmt.Errorf("task not found after claim: %s", request.ID)
	}
	var mate *Teammate
	if assignee := taskAssigneeID(*task); assignee != "" {
		if record, err := r.Store.GetTeammate(ctx, assignee); err == nil && record != nil {
			mate = record
		}
	}
	record := AgentControlTaskRecord(*task, mate)
	return &record, true, nil
}

// UpdateAgentControlTaskTerminal maps a shared AgentControl terminal task
// transition onto the current team task store.
func (r AgentControlTaskRegistry) UpdateAgentControlTaskTerminal(ctx context.Context, request agentcontrol.TaskTerminalUpdateRequest) (*agentcontrol.TaskRecord, error) {
	if r.Store == nil {
		return nil, fmt.Errorf("team store is not configured")
	}
	request = request.Normalize()
	if request.ID == "" {
		return nil, fmt.Errorf("task id is required")
	}
	if request.Workflow != "" && request.Workflow != agentcontrol.WorkflowSpawnTeam {
		return nil, fmt.Errorf("unsupported task workflow: %s", request.Workflow)
	}
	if request.Status == "" {
		return nil, fmt.Errorf("task status is required")
	}
	status := TaskStatus(request.Status)
	if !isAgentControlTerminalTaskStatus(status) {
		return nil, fmt.Errorf("unsupported terminal task status: %s", request.Status)
	}
	resultRef := normalizeOptionalTaskResultRef(request.ResultRef)

	if sqliteStore, ok := r.Store.(*SQLiteStore); ok {
		if err := r.updateSQLiteTerminalTask(ctx, sqliteStore, request, status, resultRef); err != nil {
			return nil, err
		}
	} else {
		task, err := r.Store.GetTask(ctx, request.ID)
		if err != nil {
			return nil, err
		}
		if task == nil {
			return nil, fmt.Errorf("task not found: %s", request.ID)
		}
		task.Status = status
		task.Summary = request.Summary
		task.ResultRef = resultRef
		if err := r.Store.UpdateTask(ctx, *task); err != nil {
			return nil, err
		}
		if err := r.Store.ReleaseTask(ctx, request.ID, status); err != nil {
			return nil, err
		}
		if request.TeammateID != "" && !request.SkipStateUpdate {
			_ = r.Store.UpdateTeammateState(ctx, request.TeammateID, TeammateStateIdle)
		}
	}

	task, err := r.Store.GetTask(ctx, request.ID)
	if err != nil {
		return nil, err
	}
	if task == nil {
		return nil, fmt.Errorf("task not found after terminal update: %s", request.ID)
	}
	var mate *Teammate
	if assignee := taskAssigneeID(*task); assignee != "" {
		if record, err := r.Store.GetTeammate(ctx, assignee); err == nil && record != nil {
			mate = record
		}
	} else if teammateID := strings.TrimSpace(request.TeammateID); teammateID != "" {
		if record, err := r.Store.GetTeammate(ctx, teammateID); err == nil && record != nil {
			mate = record
		}
	}
	record := AgentControlTaskRecord(*task, mate)
	return &record, nil
}

func (r AgentControlTaskRegistry) updateSQLiteTerminalTask(ctx context.Context, store *SQLiteStore, request agentcontrol.TaskTerminalUpdateRequest, status TaskStatus, resultRef *string) error {
	var err error
	for attempt := 0; attempt < 8; attempt++ {
		err = store.WithImmediateTx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, `
				UPDATE team_tasks
				SET status = ?, summary = ?, result_ref = ?, assignee = NULL, lease_until = NULL, updated_at = ?
				WHERE id = ?
			`, string(status), request.Summary, nullableString(resultRef), formatTime(time.Now().UTC()), request.ID); err != nil {
				return fmt.Errorf("update task: %w", err)
			}
			if request.TeammateID != "" && !request.SkipStateUpdate {
				if _, err := tx.ExecContext(ctx, `
					UPDATE teammates SET state = ?, updated_at = ? WHERE id = ?
				`, string(TeammateStateIdle), formatTime(time.Now().UTC()), request.TeammateID); err != nil {
					return fmt.Errorf("update teammate state: %w", err)
				}
			}
			return nil
		})
		if err == nil || !IsSQLiteLockError(err) {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(attempt+1) * 10 * time.Millisecond):
		}
	}
	if err != nil {
		return err
	}
	return store.appendTaskSignalForTask(ctx, request.ID, TaskSignalTaskReleased, status)
}

// BlockAgentControlTask maps a shared AgentControl blocked task transition
// onto the current team task store.
func (r AgentControlTaskRegistry) BlockAgentControlTask(ctx context.Context, request agentcontrol.TaskBlockRequest) (*agentcontrol.TaskRecord, error) {
	if r.Store == nil {
		return nil, fmt.Errorf("team store is not configured")
	}
	request = request.Normalize()
	if request.ID == "" {
		return nil, fmt.Errorf("task id is required")
	}
	if request.Workflow != "" && request.Workflow != agentcontrol.WorkflowSpawnTeam {
		return nil, fmt.Errorf("unsupported task workflow: %s", request.Workflow)
	}
	if request.Summary == "" {
		return nil, fmt.Errorf("summary is required")
	}
	if err := r.Store.BlockTask(ctx, request.ID, request.Summary); err != nil {
		return nil, err
	}
	if request.TeammateID != "" && !request.SkipStateUpdate {
		_ = r.Store.UpdateTeammateState(ctx, request.TeammateID, TeammateStateBlocked)
	}
	task, err := r.Store.GetTask(ctx, request.ID)
	if err != nil {
		return nil, err
	}
	if task == nil {
		return nil, fmt.Errorf("task not found after block: %s", request.ID)
	}
	var mate *Teammate
	if assignee := taskAssigneeID(*task); assignee != "" {
		if record, err := r.Store.GetTeammate(ctx, assignee); err == nil && record != nil {
			mate = record
		}
	}
	record := AgentControlTaskRecord(*task, mate)
	return &record, nil
}

// UpdateAgentControlTaskStatus maps the shared AgentControl task status writer
// seam onto the current team task store. This is a migration bridge; team
// remains the backing store while callers can depend on an AgentControl-shaped
// write surface.
func (r AgentControlTaskRegistry) UpdateAgentControlTaskStatus(ctx context.Context, request agentcontrol.TaskStatusUpdateRequest) (*agentcontrol.TaskRecord, error) {
	if r.Store == nil {
		return nil, fmt.Errorf("team store is not configured")
	}
	request = request.Normalize()
	if request.ID == "" {
		return nil, fmt.Errorf("task id is required")
	}
	if request.Workflow != "" && request.Workflow != agentcontrol.WorkflowSpawnTeam {
		return nil, fmt.Errorf("unsupported task workflow: %s", request.Workflow)
	}
	if request.Status == "" {
		return nil, fmt.Errorf("task status is required")
	}
	status := TaskStatus(request.Status)
	if !isAgentControlWritableTaskStatus(status) {
		return nil, fmt.Errorf("unsupported task status: %s", request.Status)
	}
	if err := r.Store.UpdateTaskStatus(ctx, request.ID, status, request.Summary); err != nil {
		return nil, err
	}
	task, err := r.Store.GetTask(ctx, request.ID)
	if err != nil {
		return nil, err
	}
	if task == nil {
		return nil, fmt.Errorf("task not found: %s", request.ID)
	}
	var mate *Teammate
	if assignee := taskAssigneeID(*task); assignee != "" {
		if record, err := r.Store.GetTeammate(ctx, assignee); err == nil && record != nil {
			mate = record
		}
	}
	record := AgentControlTaskRecord(*task, mate)
	return &record, nil
}

// ReleaseAgentControlTask maps the shared AgentControl release writer seam
// onto the current team task store.
func (r AgentControlTaskRegistry) ReleaseAgentControlTask(ctx context.Context, request agentcontrol.TaskReleaseRequest) (*agentcontrol.TaskRecord, error) {
	if r.Store == nil {
		return nil, fmt.Errorf("team store is not configured")
	}
	request = request.Normalize()
	if request.ID == "" {
		return nil, fmt.Errorf("task id is required")
	}
	if request.Workflow != "" && request.Workflow != agentcontrol.WorkflowSpawnTeam {
		return nil, fmt.Errorf("unsupported task workflow: %s", request.Workflow)
	}
	if request.Status == "" {
		return nil, fmt.Errorf("task status is required")
	}
	status := TaskStatus(request.Status)
	if !isAgentControlWritableTaskStatus(status) {
		return nil, fmt.Errorf("unsupported task status: %s", request.Status)
	}
	if err := r.Store.ReleaseTask(ctx, request.ID, status); err != nil {
		return nil, err
	}
	if request.Summary != "" {
		if err := r.Store.UpdateTaskStatus(ctx, request.ID, status, request.Summary); err != nil {
			return nil, err
		}
	}
	task, err := r.Store.GetTask(ctx, request.ID)
	if err != nil {
		return nil, err
	}
	if task == nil {
		return nil, fmt.Errorf("task not found: %s", request.ID)
	}
	var mate *Teammate
	if assignee := taskAssigneeID(*task); assignee != "" {
		if record, err := r.Store.GetTeammate(ctx, assignee); err == nil && record != nil {
			mate = record
		}
	}
	record := AgentControlTaskRecord(*task, mate)
	return &record, nil
}

// RenewAgentControlTaskLease maps the shared AgentControl lease renew writer
// seam onto the current team task store.
func (r AgentControlTaskRegistry) RenewAgentControlTaskLease(ctx context.Context, request agentcontrol.TaskLeaseRenewRequest) (*agentcontrol.TaskRecord, error) {
	if r.Store == nil {
		return nil, fmt.Errorf("team store is not configured")
	}
	request = request.Normalize()
	if request.ID == "" {
		return nil, fmt.Errorf("task id is required")
	}
	if request.Workflow != "" && request.Workflow != agentcontrol.WorkflowSpawnTeam {
		return nil, fmt.Errorf("unsupported task workflow: %s", request.Workflow)
	}
	if request.LeaseUntil.IsZero() {
		return nil, fmt.Errorf("lease_until is required")
	}
	if err := r.Store.RenewTaskLease(ctx, request.ID, request.LeaseUntil); err != nil {
		return nil, err
	}
	task, err := r.Store.GetTask(ctx, request.ID)
	if err != nil {
		return nil, err
	}
	if task == nil {
		return nil, fmt.Errorf("task not found: %s", request.ID)
	}
	var mate *Teammate
	if assignee := taskAssigneeID(*task); assignee != "" {
		if record, err := r.Store.GetTeammate(ctx, assignee); err == nil && record != nil {
			mate = record
		}
	}
	record := AgentControlTaskRecord(*task, mate)
	return &record, nil
}

func (r AgentControlTaskRegistry) registryTeamIDs(ctx context.Context, teamID string) ([]string, error) {
	teamID = strings.TrimSpace(teamID)
	if teamID != "" {
		return []string{teamID}, nil
	}
	return r.Store.ListTeamIDs(ctx)
}

func agentControlTaskRecordMatches(record agentcontrol.TaskRecord, filter agentcontrol.TaskFilter) bool {
	record = record.Normalize()
	if workflow := strings.TrimSpace(filter.Workflow); workflow != "" && !strings.EqualFold(record.Workflow, workflow) {
		return false
	}
	if teamID := strings.TrimSpace(filter.TeamID); teamID != "" && !strings.EqualFold(record.TeamID, teamID) {
		return false
	}
	if assignee := strings.TrimSpace(filter.Assignee); assignee != "" && !strings.EqualFold(record.Assignee, assignee) {
		return false
	}
	if pathPrefix := strings.TrimRight(strings.TrimSpace(filter.PathPrefix), "/"); pathPrefix != "" {
		path := strings.TrimRight(record.Path, "/")
		if path != pathPrefix && !strings.HasPrefix(path, pathPrefix+"/") {
			return false
		}
	}
	if len(filter.Status) > 0 {
		matched := false
		for _, status := range filter.Status {
			if strings.EqualFold(record.Status, strings.TrimSpace(status)) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

func isAgentControlWritableTaskStatus(status TaskStatus) bool {
	switch status {
	case TaskStatusPending,
		TaskStatusReady,
		TaskStatusRunning,
		TaskStatusBlocked,
		TaskStatusDone,
		TaskStatusFailed,
		TaskStatusCancelled:
		return true
	default:
		return false
	}
}

func isAgentControlTerminalTaskStatus(status TaskStatus) bool {
	switch status {
	case TaskStatusDone, TaskStatusFailed:
		return true
	default:
		return false
	}
}
