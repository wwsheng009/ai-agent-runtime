package team

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
)

// AgentControlTaskRegistry adapts team task operations to the shared
// AgentControl task graph. SQLite stores use the AgentControl task graph as
// the primary task substrate.
type AgentControlTaskRegistry struct {
	Store Store
}

type agentControlTaskRecordReaderStore interface {
	ListAgentControlTaskRecords(ctx context.Context, filter agentcontrol.TaskFilter) ([]agentcontrol.TaskRecord, error)
}

type agentControlTaskRecordCreateStore interface {
	CreateAgentControlTaskRecord(ctx context.Context, task Task) (string, error)
}

type agentControlTaskRecordUpdateStore interface {
	UpdateAgentControlTaskRecord(ctx context.Context, task Task) error
}

type agentControlTaskDependencyCreateStore interface {
	CreateAgentControlTaskDependencyRecord(ctx context.Context, teamID, taskID, dependsOnID string) error
}

type agentControlTaskReadyStore interface {
	MarkAgentControlTaskRecordsReady(ctx context.Context, teamID string) (int64, error)
}

var _ agentcontrol.TaskRegistryReader = AgentControlTaskRegistry{}
var _ agentcontrol.TaskRegistryCreateWriter = AgentControlTaskRegistry{}
var _ agentcontrol.TaskRegistryUpdateWriter = AgentControlTaskRegistry{}
var _ agentcontrol.TaskDependencyReader = AgentControlTaskRegistry{}
var _ agentcontrol.TaskGraphEventReader = AgentControlTaskRegistry{}
var _ agentcontrol.TaskDependencyCreateWriter = AgentControlTaskRegistry{}
var _ agentcontrol.TaskRegistryReadyWriter = AgentControlTaskRegistry{}
var _ agentcontrol.TaskRegistryStatusWriter = AgentControlTaskRegistry{}
var _ agentcontrol.TaskRegistryReleaseWriter = AgentControlTaskRegistry{}
var _ agentcontrol.TaskRegistryLeaseRenewWriter = AgentControlTaskRegistry{}
var _ agentcontrol.TaskRegistryRetryWriter = AgentControlTaskRegistry{}
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
	if reader, ok := r.Store.(agentControlTaskRecordReaderStore); ok {
		return reader.ListAgentControlTaskRecords(ctx, filter)
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

// WatchAgentControlTaskWake adapts task wake notifications to the shared
// AgentControl task wake seam. Stores with an AgentControl task wake mirror are
// preferred; team-native task signals remain as a compatibility fallback.
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
	if watcher, ok := r.Store.(agentControlWakeRegistryStore); ok {
		input, unwatch := watcher.WatchAgentControlWake(ctx, agentcontrol.WakeFilter{
			Workflow: agentcontrol.WorkflowSpawnTeam,
			Kind:     agentcontrol.WakeKindTask,
			TeamID:   filter.TeamID,
		})
		return r.watchGenericAgentControlTaskWake(ctx, filter, input, unwatch, out)
	}
	if watcher, ok := r.Store.(AgentControlTaskWatcherStore); ok {
		input, unwatch := watcher.WatchAgentControlTaskSignals(ctx, agentcontrol.WorkflowSpawnTeam, filter.TeamID)
		return r.watchAgentControlTaskWake(ctx, filter, input, unwatch, out)
	}
	watcher, ok := r.Store.(TaskWatcherStore)
	if !ok {
		return out, func() {}
	}
	input, unwatch := watcher.WatchTaskSignals(ctx, filter.TeamID)
	return r.watchAgentControlTaskWake(ctx, filter, input, unwatch, out)
}

func (r AgentControlTaskRegistry) watchGenericAgentControlTaskWake(ctx context.Context, filter agentcontrol.TaskWakeFilter, input <-chan agentcontrol.WakeEvent, unwatch func(), out chan agentcontrol.TaskWakeEvent) (<-chan agentcontrol.TaskWakeEvent, func()) {
	done := make(chan struct{})
	var once sync.Once
	go func() {
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case event, ok := <-input:
				if !ok {
					return
				}
				if event.Kind != agentcontrol.WakeKindTask {
					continue
				}
				wake := taskWakeFromGenericWake(event)
				if wake.TeamID == "" || (filter.TeamID != "" && !strings.EqualFold(wake.TeamID, filter.TeamID)) {
					continue
				}
				select {
				case out <- wake:
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

func (r AgentControlTaskRegistry) watchAgentControlTaskWake(ctx context.Context, filter agentcontrol.TaskWakeFilter, input <-chan TaskSignal, unwatch func(), out chan agentcontrol.TaskWakeEvent) (<-chan agentcontrol.TaskWakeEvent, func()) {
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

// LastAgentControlTaskWakeSeq returns the AgentControl task wake high-water
// mark, preferring the AgentControl task wake mirror when available.
func (r AgentControlTaskRegistry) LastAgentControlTaskWakeSeq(ctx context.Context, filter agentcontrol.TaskWakeFilter) (int64, error) {
	if r.Store == nil {
		return 0, nil
	}
	filter = filter.Normalize()
	if filter.Workflow != "" && filter.Workflow != agentcontrol.WorkflowSpawnTeam {
		return 0, nil
	}
	if sequencer, ok := r.Store.(agentControlWakeRegistryStore); ok {
		return sequencer.LastAgentControlWakeSeq(ctx, agentcontrol.WakeFilter{
			Workflow: agentcontrol.WorkflowSpawnTeam,
			Kind:     agentcontrol.WakeKindTask,
			TeamID:   filter.TeamID,
		})
	}
	if sequencer, ok := r.Store.(AgentControlTaskSequenceStore); ok {
		return sequencer.LastAgentControlTaskSignalSeq(ctx, agentcontrol.WorkflowSpawnTeam, filter.TeamID)
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
	var (
		taskID string
		err    error
	)
	if writer, ok := r.Store.(agentControlTaskRecordCreateStore); ok {
		taskID, err = writer.CreateAgentControlTaskRecord(ctx, task)
	} else {
		taskID, err = r.Store.CreateTask(ctx, task)
	}
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

// UpdateAgentControlTask maps the shared AgentControl full task patch writer
// seam onto the current team task store.
func (r AgentControlTaskRegistry) UpdateAgentControlTask(ctx context.Context, request agentcontrol.TaskUpdateRequest) (*agentcontrol.TaskRecord, error) {
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
	task, err := r.Store.GetTask(ctx, request.ID)
	if err != nil {
		return nil, err
	}
	if task == nil {
		return nil, fmt.Errorf("task not found: %s", request.ID)
	}
	if request.TeamID != "" && !strings.EqualFold(strings.TrimSpace(task.TeamID), request.TeamID) {
		return nil, fmt.Errorf("task does not belong to team: %s", request.ID)
	}
	if request.ParentTaskID != nil {
		if *request.ParentTaskID == "" {
			task.ParentTaskID = nil
		} else {
			value := *request.ParentTaskID
			task.ParentTaskID = &value
		}
	}
	if request.Title != nil {
		task.Title = *request.Title
	}
	if request.Goal != nil {
		task.Goal = *request.Goal
	}
	closedStatusUpdate := false
	if request.Status != nil && *request.Status != "" {
		status := TaskStatus(*request.Status)
		if !isAgentControlWritableTaskStatus(status) {
			return nil, fmt.Errorf("unsupported task status: %s", *request.Status)
		}
		task.Status = status
		closedStatusUpdate = isAgentControlClosedTaskStatus(status)
	}
	if request.Priority != nil {
		task.Priority = *request.Priority
	}
	if request.Assignee != nil {
		if *request.Assignee == "" {
			task.Assignee = nil
		} else {
			value := *request.Assignee
			task.Assignee = &value
		}
	}
	if request.Inputs != nil {
		task.Inputs = *request.Inputs
	}
	if request.ReadPaths != nil {
		task.ReadPaths = *request.ReadPaths
	}
	if request.WritePaths != nil {
		task.WritePaths = *request.WritePaths
	}
	if request.Deliverables != nil {
		task.Deliverables = *request.Deliverables
	}
	if request.Summary != nil {
		task.Summary = *request.Summary
	}
	if request.ResultRef != nil {
		if *request.ResultRef == "" {
			task.ResultRef = nil
		} else {
			value := *request.ResultRef
			task.ResultRef = &value
		}
	}
	if closedStatusUpdate {
		task.Assignee = nil
		task.LeaseUntil = nil
	}
	if writer, ok := r.Store.(agentControlTaskRecordUpdateStore); ok {
		err = writer.UpdateAgentControlTaskRecord(ctx, *task)
	} else {
		err = r.Store.UpdateTask(ctx, *task)
	}
	if err != nil {
		return nil, err
	}
	updated, err := r.Store.GetTask(ctx, request.ID)
	if err != nil {
		return nil, err
	}
	if updated == nil {
		updated = task
	}
	var mate *Teammate
	if assignee := taskAssigneeID(*updated); assignee != "" {
		if record, err := r.Store.GetTeammate(ctx, assignee); err == nil && record != nil {
			mate = record
		}
	}
	record := AgentControlTaskRecord(*updated, mate)
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
	if writer, ok := r.Store.(agentControlTaskDependencyCreateStore); ok {
		return writer.CreateAgentControlTaskDependencyRecord(ctx, request.TeamID, request.TaskID, request.DependsOnID)
	}
	return r.Store.AddTaskDependency(ctx, request.TaskID, request.DependsOnID)
}

// MarkAgentControlTasksReady promotes dependency-satisfied tasks through the
// shared AgentControl task registry seam.
func (r AgentControlTaskRegistry) MarkAgentControlTasksReady(ctx context.Context, request agentcontrol.TaskReadyRequest) (int64, error) {
	if r.Store == nil {
		return 0, fmt.Errorf("team store is not configured")
	}
	request = request.Normalize()
	if request.Workflow != "" && request.Workflow != agentcontrol.WorkflowSpawnTeam {
		return 0, fmt.Errorf("unsupported task workflow: %s", request.Workflow)
	}
	if request.TeamID == "" {
		return 0, fmt.Errorf("team id is required")
	}
	if writer, ok := r.Store.(agentControlTaskReadyStore); ok {
		return writer.MarkAgentControlTaskRecordsReady(ctx, request.TeamID)
	}
	return r.Store.MarkReadyTasks(ctx, request.TeamID)
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

// ListAgentControlTaskGraphEvents maps team task timeline events into the
// shared AgentControl task graph event read model.
func (r AgentControlTaskRegistry) ListAgentControlTaskGraphEvents(ctx context.Context, filter agentcontrol.TaskGraphEventFilter) ([]agentcontrol.TaskGraphEvent, error) {
	if r.Store == nil {
		return nil, nil
	}
	filter = filter.Normalize()
	if filter.Workflow != "" && filter.Workflow != agentcontrol.WorkflowSpawnTeam {
		return nil, nil
	}
	if reader, ok := r.Store.(TaskGraphEventReaderStore); ok {
		return r.listAgentControlTaskGraphEvents(ctx, filter, reader)
	}
	teamIDs, err := r.registryTeamIDs(ctx, filter.TeamID)
	if err != nil {
		return nil, err
	}
	useCombinedCursor := strings.TrimSpace(filter.TeamID) == ""
	events := make([]agentcontrol.TaskGraphEvent, 0)
	for _, teamID := range teamIDs {
		afterSeq := filter.AfterSeq
		if useCombinedCursor {
			afterSeq = 0
		}
		records, err := r.Store.ListTeamEvents(ctx, TeamEventFilter{
			TeamID:    teamID,
			AfterSeq:  afterSeq,
			EventType: filter.EventType,
		})
		if err != nil {
			return nil, err
		}
		for _, record := range records {
			event, ok := agentControlTaskGraphEventFromTeamEvent(record)
			if !ok {
				continue
			}
			if useCombinedCursor {
				event.Seq = agentControlTeamProjectionSeq(teamID, event.TeamSeq, event.CreatedAt)
			}
			if event.Seq <= filter.AfterSeq {
				continue
			}
			events = append(events, event)
		}
	}
	sort.SliceStable(events, func(i, j int) bool {
		if events[i].Seq != events[j].Seq {
			return events[i].Seq < events[j].Seq
		}
		if events[i].CreatedAt.Equal(events[j].CreatedAt) {
			if strings.EqualFold(events[i].TeamID, events[j].TeamID) {
				return events[i].TeamSeq < events[j].TeamSeq
			}
			return events[i].TeamID < events[j].TeamID
		}
		return events[i].CreatedAt.Before(events[j].CreatedAt)
	})
	if filter.Limit > 0 && len(events) > filter.Limit {
		events = events[:filter.Limit]
	}
	return events, nil
}

func (r AgentControlTaskRegistry) listAgentControlTaskGraphEvents(ctx context.Context, filter agentcontrol.TaskGraphEventFilter, reader TaskGraphEventReaderStore) ([]agentcontrol.TaskGraphEvent, error) {
	records, err := reader.ListTaskGraphEvents(ctx, TaskGraphEventFilter{
		Workflow:  agentcontrol.WorkflowSpawnTeam,
		TeamID:    filter.TeamID,
		EventType: filter.EventType,
		AfterSeq:  filter.AfterSeq,
		Limit:     filter.Limit,
	})
	if err != nil {
		return nil, err
	}
	events := make([]agentcontrol.TaskGraphEvent, 0, len(records))
	for _, record := range records {
		event, ok := agentControlTaskGraphEventFromTaskGraphEvent(record)
		if !ok {
			continue
		}
		events = append(events, event)
	}
	if filter.Limit > 0 && len(events) > filter.Limit {
		events = events[:filter.Limit]
	}
	return events, nil
}

func agentControlTaskGraphEventFromTaskGraphEvent(record TaskGraphEvent) (agentcontrol.TaskGraphEvent, bool) {
	eventType := strings.TrimSpace(record.Type)
	if !strings.HasPrefix(eventType, "task.") {
		return agentcontrol.TaskGraphEvent{}, false
	}
	workflow := strings.TrimSpace(record.Workflow)
	if workflow == "" {
		workflow = agentcontrol.WorkflowSpawnTeam
	}
	payload := cloneStringInterfaceMap(record.Payload)
	event := agentcontrol.TaskGraphEvent{
		Seq:          record.Seq,
		TeamSeq:      record.TeamSeq,
		Workflow:     workflow,
		TeamID:       strings.TrimSpace(record.TeamID),
		EventType:    eventType,
		TaskID:       metadataString(payload, "task_id"),
		DependsOnID:  metadataString(payload, "depends_on_id"),
		DependencyID: metadataString(payload, "dependency_id"),
		Payload:      payload,
		CreatedAt:    record.CreatedAt,
	}.Normalize()
	return event, true
}

func agentControlTaskGraphEventFromTeamEvent(record TeamEventRecord) (agentcontrol.TaskGraphEvent, bool) {
	eventType := strings.TrimSpace(record.Type)
	if !strings.HasPrefix(eventType, "task.") {
		return agentcontrol.TaskGraphEvent{}, false
	}
	payload := cloneStringInterfaceMap(record.Payload)
	event := agentcontrol.TaskGraphEvent{
		Seq:          record.Seq,
		TeamSeq:      record.Seq,
		Workflow:     agentcontrol.WorkflowSpawnTeam,
		TeamID:       strings.TrimSpace(record.TeamID),
		EventType:    eventType,
		TaskID:       metadataString(payload, "task_id"),
		DependsOnID:  metadataString(payload, "depends_on_id"),
		DependencyID: metadataString(payload, "dependency_id"),
		Payload:      payload,
		CreatedAt:    record.Timestamp,
	}.Normalize()
	return event, true
}

func cloneStringInterfaceMap(input map[string]interface{}) map[string]interface{} {
	if len(input) == 0 {
		return nil
	}
	output := make(map[string]interface{}, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func metadataString(metadata map[string]interface{}, key string) string {
	if len(metadata) == 0 {
		return ""
	}
	value, ok := metadata[key]
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case fmt.Stringer:
		return strings.TrimSpace(typed.String())
	default:
		return strings.TrimSpace(fmt.Sprint(typed))
	}
}

func taskSignalToAgentControlWake(signal TaskSignal) agentcontrol.TaskWakeEvent {
	workflow := strings.TrimSpace(signal.Workflow)
	if workflow == "" {
		workflow = agentcontrol.WorkflowSpawnTeam
	}
	return agentcontrol.TaskWakeEvent{
		Seq:       signal.Seq,
		Workflow:  workflow,
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
	changed := false
	for attempt := 0; attempt < 8; attempt++ {
		err = store.WithImmediateTx(ctx, func(tx *sql.Tx) error {
			now := time.Now().UTC()
			result, err := tx.ExecContext(ctx, `
				UPDATE agent_control_task_records
				SET status = ?, summary = ?, result_ref = ?, assignee = NULL, session_id = NULL, agent_path = NULL, lease_until = NULL, updated_at = ?
				WHERE workflow = ? AND task_id = ?
			`, string(status), request.Summary, nullableString(resultRef), formatTime(now), agentcontrol.WorkflowSpawnTeam, request.ID)
			if err != nil {
				return fmt.Errorf("update agent control task: %w", err)
			}
			affected, _ := result.RowsAffected()
			if affected == 0 {
				return nil
			}
			changed = true
			if request.TeammateID != "" && !request.SkipStateUpdate {
				if _, err := tx.ExecContext(ctx, `
					UPDATE teammates SET state = ?, updated_at = ? WHERE id = ?
				`, string(TeammateStateIdle), formatTime(now), request.TeammateID); err != nil {
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
	if changed {
		return store.appendTaskSignalForTask(ctx, request.ID, TaskSignalTaskReleased, status)
	}
	return nil
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
// seam onto the current team task graph substrate.
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
	if isAgentControlClosedTaskStatus(status) {
		if err := r.Store.ReleaseTask(ctx, request.ID, status); err != nil {
			return nil, err
		}
		if request.Summary != "" {
			if err := r.Store.UpdateTaskStatus(ctx, request.ID, status, request.Summary); err != nil {
				return nil, err
			}
		}
	} else {
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

// RetryAgentControlTask maps a retry/reclaim transition onto the current team
// task store while preserving retry count, lease reset, and optional summary.
func (r AgentControlTaskRegistry) RetryAgentControlTask(ctx context.Context, request agentcontrol.TaskRetryRequest) (*agentcontrol.TaskRecord, error) {
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
	if err := r.Store.IncrementTaskRetry(ctx, request.ID); err != nil {
		return nil, err
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
		return nil, fmt.Errorf("task not found after retry: %s", request.ID)
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

func isAgentControlClosedTaskStatus(status TaskStatus) bool {
	switch status {
	case TaskStatusDone, TaskStatusFailed, TaskStatusCancelled:
		return true
	default:
		return false
	}
}
