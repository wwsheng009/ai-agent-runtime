package team

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ai-gateway/ai-agent-runtime/internal/pkg/logger"
)

// Orchestrator coordinates ready task claims and release lifecycle.
type Orchestrator struct {
	Store         Store
	Claims        *PathClaimManager
	Scheduler     Scheduler
	Runner        *TeammateRunner
	Dispatcher    MailboxDispatcher
	LeaseManager  *LeaseManager
	LeadPlanner   *LeadPlanner
	Mailbox       *MailboxService
	Events        *TeamEventBus
	LeaseDuration time.Duration
	TickInterval  time.Duration
	Clock         func() time.Time
}

// NewOrchestrator builds a team orchestrator with defaults.
func NewOrchestrator(store Store, claims *PathClaimManager, scheduler Scheduler) *Orchestrator {
	return &Orchestrator{
		Store:         store,
		Claims:        claims,
		Scheduler:     scheduler,
		LeaseDuration: 10 * time.Minute,
		TickInterval:  1 * time.Second,
		Clock:         time.Now,
	}
}

// Run starts the orchestrator loop for a team.
func (o *Orchestrator) Run(ctx context.Context, teamID string) error {
	if o == nil || o.Store == nil {
		return fmt.Errorf("orchestrator store is not configured")
	}
	teamID = strings.TrimSpace(teamID)
	if teamID == "" {
		return fmt.Errorf("team id is required")
	}
	interval := o.TickInterval
	if interval <= 0 {
		interval = 1 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			team, err := o.Store.GetTeam(ctx, teamID)
			if err != nil {
				if IsSQLiteLockError(err) {
					continue
				}
				return err
			}
			if team == nil || team.Status != TeamStatusActive {
				return nil
			}
			if err := o.tick(ctx, teamID); err != nil {
				if IsSQLiteLockError(err) {
					continue
				}
				return err
			}
		}
	}
}

// ClaimReadyTasks assigns and claims ready tasks, returning accepted assignments.
func (o *Orchestrator) ClaimReadyTasks(ctx context.Context, teamID string, limit int) ([]Assignment, error) {
	if o == nil || o.Store == nil {
		return nil, fmt.Errorf("orchestrator store is not configured")
	}
	if teamID == "" {
		return nil, fmt.Errorf("team id is required")
	}
	teamRecord, err := o.Store.GetTeam(ctx, teamID)
	if err != nil {
		return nil, err
	}
	_, _ = o.Store.MarkReadyTasks(ctx, teamID)

	readyTasks, err := o.Store.ListTasks(ctx, TaskFilter{
		TeamID: teamID,
		Status: []TaskStatus{TaskStatusReady},
		Limit:  limit,
	})
	if err != nil {
		return nil, err
	}
	if len(readyTasks) == 0 {
		return nil, nil
	}

	teammates, err := o.Store.ListTeammates(ctx, teamID)
	if err != nil {
		return nil, err
	}
	idle := make([]Teammate, 0, len(teammates))
	activeCount := 0
	for _, mate := range teammates {
		if mate.State == TeammateStateIdle {
			idle = append(idle, mate)
			continue
		}
		if mate.State == TeammateStateBusy || mate.State == TeammateStateBlocked {
			activeCount++
		}
	}
	if len(idle) == 0 {
		return nil, nil
	}

	maxTeammates := 0
	maxWriters := 0
	if teamRecord != nil {
		maxTeammates = teamRecord.MaxTeammates
		maxWriters = teamRecord.MaxWriters
	}
	if maxTeammates > 0 {
		available := maxTeammates - activeCount
		if available <= 0 {
			return nil, nil
		}
		if available < len(idle) {
			idle = idle[:available]
		}
	}

	writerSlots := -1
	if maxWriters > 0 {
		running, err := o.Store.ListTasks(ctx, TaskFilter{
			TeamID: teamID,
			Status: []TaskStatus{TaskStatusRunning},
		})
		if err != nil {
			return nil, err
		}
		now := time.Now().UTC()
		inUse := 0
		for _, task := range running {
			if len(task.WritePaths) == 0 {
				continue
			}
			if task.LeaseUntil != nil && task.LeaseUntil.Before(now) {
				continue
			}
			inUse++
		}
		writerSlots = maxWriters - inUse
		if writerSlots < 0 {
			writerSlots = 0
		}
	}

	scheduler := o.Scheduler
	if scheduler == nil {
		scheduler = &RoundRobinScheduler{}
	}

	leaseDuration := o.LeaseDuration
	if leaseDuration <= 0 {
		leaseDuration = 10 * time.Minute
	}

	idleByID := make(map[string]Teammate, len(idle))
	for _, mate := range idle {
		idleByID[mate.ID] = mate
	}

	assignments := make([]Assignment, 0, len(readyTasks))
	assignedTeammates := make(map[string]bool)

	pinned := make([]Task, 0)
	unassigned := make([]Task, 0)
	for _, task := range readyTasks {
		if task.Assignee != nil && strings.TrimSpace(*task.Assignee) != "" {
			pinned = append(pinned, task)
		} else {
			unassigned = append(unassigned, task)
		}
	}

	canAssignWriter := func(task Task) bool {
		if writerSlots < 0 {
			return true
		}
		if len(task.WritePaths) == 0 {
			return true
		}
		return writerSlots > 0
	}

	claimAssignment := func(task Task, mate Teammate) bool {
		leaseUntil := time.Now().UTC().Add(leaseDuration)
		usedAtomicClaim := false
		if o.Claims != nil {
			claimed, err := o.Store.ClaimTaskWithPathClaims(ctx, task, mate.ID, leaseUntil, o.Claims.root)
			if err != nil || !claimed {
				return false
			}
			usedAtomicClaim = true
		} else {
			claimed, err := o.Store.ClaimTask(ctx, task.ID, mate.ID, leaseUntil, task.Version)
			if err != nil || !claimed {
				return false
			}
		}
		if !usedAtomicClaim {
			_ = o.Store.UpdateTeammateState(ctx, mate.ID, TeammateStateBusy)
		}
		assignments = append(assignments, Assignment{Task: task, Teammate: mate})
		assignedTeammates[mate.ID] = true
		if writerSlots > 0 && len(task.WritePaths) > 0 {
			writerSlots--
		}
		return true
	}

	for _, task := range pinned {
		if limit > 0 && len(assignments) >= limit {
			break
		}
		assignee := strings.TrimSpace(*task.Assignee)
		if assignee == "" {
			continue
		}
		mate, ok := idleByID[assignee]
		if !ok || assignedTeammates[assignee] {
			continue
		}
		if !canAssignWriter(task) {
			continue
		}
		_ = claimAssignment(task, mate)
	}

	available := make([]Teammate, 0, len(idle))
	for _, mate := range idle {
		if !assignedTeammates[mate.ID] {
			available = append(available, mate)
		}
	}
	if len(available) == 0 || len(unassigned) == 0 {
		return assignments, nil
	}

	remainingLimit := len(unassigned)
	if limit > 0 {
		remainingLimit = limit - len(assignments)
		if remainingLimit <= 0 {
			return assignments, nil
		}
	}
	if remainingLimit < len(unassigned) {
		unassigned = unassigned[:remainingLimit]
	}
	if len(unassigned) > len(available) {
		unassigned = unassigned[:len(available)]
	}

	proposed := scheduler.Select(available, unassigned)
	if len(proposed) == 0 {
		return assignments, nil
	}
	for _, assignment := range proposed {
		if limit > 0 && len(assignments) >= limit {
			break
		}
		if assignedTeammates[assignment.Teammate.ID] {
			continue
		}
		if !canAssignWriter(assignment.Task) {
			continue
		}
		_ = claimAssignment(assignment.Task, assignment.Teammate)
	}

	return assignments, nil
}

func (o *Orchestrator) tick(ctx context.Context, teamID string) error {
	if o.LeaseManager != nil {
		if _, err := o.LeaseManager.ReclaimExpired(ctx, teamID); err != nil {
			return err
		}
	}
	_, _ = o.Store.MarkReadyTasks(ctx, teamID)

	assignments, err := o.ClaimReadyTasks(ctx, teamID, 0)
	if err != nil {
		return err
	}
	if len(assignments) > 0 && o.Runner != nil {
		for _, assignment := range assignments {
			go o.executeAssignment(ctx, teamID, assignment)
		}
	}
	return o.checkTerminalState(ctx, teamID)
}

func (o *Orchestrator) executeAssignment(ctx context.Context, teamID string, assignment Assignment) {
	if o == nil || o.Runner == nil {
		return
	}
	team := o.loadTeam(ctx, teamID)
	o.publish("task.started", teamID, map[string]interface{}{
		"task_id":  assignment.Task.ID,
		"assignee": assignment.Teammate.ID,
	})
	o.sendLeadProgress(ctx, teamID, assignment, "progress", fmt.Sprintf("Started task: %s", summarizeTaskTitle(assignment.Task)))
	result, err := o.Runner.StartTask(ctx, team, assignment.Teammate, assignment.Task)
	if result != nil && result.OutcomeApplied {
		summary := strings.TrimSpace(firstNonEmptyString(result.Summary, summarizeRunSuccess(result), summarizeRunFailure(result, err)))
		switch result.Outcome {
		case TaskOutcomeBlocked, TaskOutcomeHandoff:
			o.setTeammateState(ctx, assignment.Teammate.ID, TeammateStateBlocked)
			o.publish("task.blocked", teamID, map[string]interface{}{
				"task_id":    assignment.Task.ID,
				"assignee":   assignment.Teammate.ID,
				"summary":    summary,
				"trace_id":   resultTraceID(result),
				"handoff_to": result.HandoffTo,
			})
			return
		case TaskOutcomeFailed:
			o.setTeammateState(ctx, assignment.Teammate.ID, TeammateStateIdle)
			o.sendLeadProgress(ctx, teamID, assignment, "failed", summary)
			o.publish("task.failed", teamID, map[string]interface{}{
				"task_id":  assignment.Task.ID,
				"assignee": assignment.Teammate.ID,
				"summary":  summary,
				"trace_id": resultTraceID(result),
			})
			if termErr := o.checkTerminalState(context.Background(), teamID); termErr != nil {
				logger.Debug("team orchestrator: terminal check failed",
					logger.String("team_id", teamID),
					logger.String("task_id", assignment.Task.ID),
					logger.String("error", termErr.Error()),
				)
			}
			return
		default:
			o.setTeammateState(ctx, assignment.Teammate.ID, TeammateStateIdle)
			o.sendLeadProgress(ctx, teamID, assignment, "done", summary)
			o.publish("task.completed", teamID, map[string]interface{}{
				"task_id":  assignment.Task.ID,
				"assignee": assignment.Teammate.ID,
				"summary":  summary,
				"trace_id": resultTraceID(result),
			})
			if termErr := o.checkTerminalState(context.Background(), teamID); termErr != nil {
				logger.Debug("team orchestrator: terminal check failed",
					logger.String("team_id", teamID),
					logger.String("task_id", assignment.Task.ID),
					logger.String("error", termErr.Error()),
				)
			}
			return
		}
	}
	if err != nil || result == nil || !result.Success {
		summary := summarizeRunFailure(result, err)
		_ = o.FailTask(ctx, assignment, summary)
		o.sendLeadProgress(ctx, teamID, assignment, "failed", summary)
		o.publish("task.failed", teamID, map[string]interface{}{
			"task_id":  assignment.Task.ID,
			"assignee": assignment.Teammate.ID,
			"summary":  summary,
			"trace_id": resultTraceID(result),
		})
		if termErr := o.checkTerminalState(context.Background(), teamID); termErr != nil {
			logger.Debug("team orchestrator: terminal check failed",
				logger.String("team_id", teamID),
				logger.String("task_id", assignment.Task.ID),
				logger.String("error", termErr.Error()),
			)
		}
		return
	}
	if result.Blocked {
		summary := summarizeRunBlocked(result)
		plannedTaskIDs, dependencyCount, blockErr := o.BlockTask(ctx, team, assignment, summary, result.HandoffTo)
		o.publish("task.blocked", teamID, map[string]interface{}{
			"task_id":          assignment.Task.ID,
			"assignee":         assignment.Teammate.ID,
			"summary":          summary,
			"trace_id":         resultTraceID(result),
			"planned_task_ids": plannedTaskIDs,
			"dependency_count": dependencyCount,
			"block_error":      errorString(blockErr),
			"replanned":        len(plannedTaskIDs) > 0,
			"handoff_to":       result.HandoffTo,
		})
		return
	}
	summary := summarizeRunSuccess(result)
	_ = o.CompleteTask(ctx, assignment, summary)
	o.sendLeadProgress(ctx, teamID, assignment, "done", summary)
	o.publish("task.completed", teamID, map[string]interface{}{
		"task_id":  assignment.Task.ID,
		"assignee": assignment.Teammate.ID,
		"summary":  summary,
		"trace_id": resultTraceID(result),
	})
	if termErr := o.checkTerminalState(context.Background(), teamID); termErr != nil {
		logger.Debug("team orchestrator: terminal check failed",
			logger.String("team_id", teamID),
			logger.String("task_id", assignment.Task.ID),
			logger.String("error", termErr.Error()),
		)
	}
}

func (o *Orchestrator) sendLeadProgress(ctx context.Context, teamID string, assignment Assignment, kind string, summary string) {
	if o == nil || o.Mailbox == nil {
		return
	}
	kind = strings.TrimSpace(kind)
	if kind == "" {
		kind = "progress"
	}
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return
	}
	message := MailMessage{
		TeamID:    strings.TrimSpace(teamID),
		FromAgent: strings.TrimSpace(assignment.Teammate.ID),
		ToAgent:   "lead",
		Kind:      kind,
		Body:      summary,
	}
	if taskID := strings.TrimSpace(assignment.Task.ID); taskID != "" {
		message.TaskID = &taskID
	}
	messageID, err := o.Mailbox.Send(ctx, message)
	if err != nil {
		return
	}
	message.ID = messageID
	if o.Dispatcher != nil {
		_ = o.Dispatcher.DispatchTeamMailboxMessage(ctx, message)
	}
}

func (o *Orchestrator) loadTeam(ctx context.Context, teamID string) Team {
	if o == nil || o.Store == nil {
		return Team{ID: teamID}
	}
	team, err := o.Store.GetTeam(ctx, teamID)
	if err != nil || team == nil {
		return Team{ID: teamID}
	}
	return *team
}

func (o *Orchestrator) checkTerminalState(ctx context.Context, teamID string) error {
	_, err := ReconcileTerminalTeamState(ctx, TerminalTeamServices{
		Store:   o.Store,
		Planner: o.LeadPlanner,
		Mailbox: o.Mailbox,
		Events:  o.Events,
	}, teamID)
	return err
}

func (o *Orchestrator) setTeammateState(ctx context.Context, teammateID string, state TeammateState) {
	if o == nil || o.Store == nil || strings.TrimSpace(teammateID) == "" {
		return
	}
	_ = o.Store.UpdateTeammateState(ctx, strings.TrimSpace(teammateID), state)
}

// BlockTask marks the task as blocked, releases claims, notifies a recipient, and optionally replans follow-up work.
func (o *Orchestrator) BlockTask(ctx context.Context, team Team, assignment Assignment, summary, handoffTo string) ([]string, int, error) {
	if o == nil || o.Store == nil {
		return nil, 0, fmt.Errorf("orchestrator store is not configured")
	}
	result, err := ApplyBlockedTaskOutcome(ctx, TaskOutcomeApplyServices{
		Store:   o.Store,
		Claims:  o.Claims,
		Mailbox: o.Mailbox,
		Planner: o.LeadPlanner,
	}, BlockedTaskOutcomeRequest{
		Team:       team,
		Task:       assignment.Task,
		TeammateID: assignment.Teammate.ID,
		Outcome: TaskOutcomeContract{
			Summary:   strings.TrimSpace(summary),
			HandoffTo: strings.TrimSpace(handoffTo),
		},
	})
	if err != nil {
		return nil, 0, err
	}
	if result == nil || result.PlanResult == nil {
		return nil, 0, nil
	}
	plannedTaskIDs := make([]string, 0, len(result.PlanResult.Tasks))
	for _, task := range result.PlanResult.Tasks {
		if strings.TrimSpace(task.ID) == "" {
			continue
		}
		plannedTaskIDs = append(plannedTaskIDs, task.ID)
	}
	return plannedTaskIDs, len(result.PlanResult.Dependencies), nil
}

// ReclaimExpiredTasks reclaims tasks whose leases have expired.
func (o *Orchestrator) ReclaimExpiredTasks(ctx context.Context, teamID string, asOf time.Time, limit int, dryRun bool) ([]LeaseReclaim, error) {
	if o == nil || o.Store == nil {
		return nil, fmt.Errorf("orchestrator store is not configured")
	}
	manager := NewLeaseManager(o.Store, o.Claims)
	return manager.ReclaimExpiredTasks(ctx, teamID, asOf, limit, dryRun)
}

// CompleteTask marks the task as done and releases related claims.
func (o *Orchestrator) CompleteTask(ctx context.Context, assignment Assignment, summary string) error {
	if o == nil || o.Store == nil {
		return fmt.Errorf("orchestrator store is not configured")
	}
	_, err := ApplyTerminalTaskOutcome(ctx, TaskOutcomeApplyServices{
		Store:  o.Store,
		Claims: o.Claims,
	}, TerminalTaskOutcomeRequest{
		Task:          assignment.Task,
		TeammateID:    assignment.Teammate.ID,
		DefaultStatus: TaskOutcomeDone,
		Outcome: TaskOutcomeContract{
			Summary: strings.TrimSpace(summary),
		},
	})
	return err
}

// FailTask marks the task as failed and releases related claims.
func (o *Orchestrator) FailTask(ctx context.Context, assignment Assignment, summary string) error {
	if o == nil || o.Store == nil {
		return fmt.Errorf("orchestrator store is not configured")
	}
	_, err := ApplyTerminalTaskOutcome(ctx, TaskOutcomeApplyServices{
		Store:  o.Store,
		Claims: o.Claims,
	}, TerminalTaskOutcomeRequest{
		Task:          assignment.Task,
		TeammateID:    assignment.Teammate.ID,
		DefaultStatus: TaskOutcomeFailed,
		Outcome: TaskOutcomeContract{
			Summary: strings.TrimSpace(summary),
		},
	})
	return err
}

func (o *Orchestrator) publish(eventType, teamID string, payload map[string]interface{}) {
	if o == nil {
		return
	}
	event := TeamEvent{
		Type:    eventType,
		TeamID:  teamID,
		Payload: payload,
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}
	if o.Events != nil {
		o.Events.Publish(event)
	}
	if o.Store != nil {
		_, _ = o.Store.AppendTeamEvent(context.Background(), event)
	}
}

func summarizeRunFailure(result *TaskRunResult, err error) string {
	if result != nil {
		if strings.TrimSpace(result.Summary) != "" {
			return result.Summary
		}
		if strings.TrimSpace(result.Error) != "" {
			return truncateLine(result.Error, 240)
		}
		if strings.TrimSpace(result.Output) != "" {
			return truncateLine(result.Output, 240)
		}
	}
	if err != nil {
		return truncateLine(err.Error(), 240)
	}
	return "task failed"
}

func summarizeRunSuccess(result *TaskRunResult) string {
	if result == nil {
		return ""
	}
	if strings.TrimSpace(result.Summary) != "" {
		return result.Summary
	}
	if strings.TrimSpace(result.Output) != "" {
		return truncateLine(result.Output, 240)
	}
	return ""
}

func summarizeRunBlocked(result *TaskRunResult) string {
	if result == nil {
		return "task blocked"
	}
	if strings.TrimSpace(result.Summary) != "" {
		return result.Summary
	}
	if strings.TrimSpace(result.Output) != "" {
		return truncateLine(result.Output, 240)
	}
	return "task blocked"
}

func resultTraceID(result *TaskRunResult) string {
	if result == nil {
		return ""
	}
	return strings.TrimSpace(result.TraceID)
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
