package team

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
)

// DefaultReclaimGrace is how long a task stays in reclaim_pending before the
// lease manager returns it to the ready queue. The grace window lets a healthy
// runner that briefly failed to renew (for example during a SQLite lock) win
// its lease back instead of being immediately re-assigned.
const DefaultReclaimGrace = 30 * time.Second

// LeaseManager handles task lease renewal and reclamation.
type LeaseManager struct {
	Store   Store
	Claims  *PathClaimManager
	Mailbox *MailboxService
	Clock   func() time.Time
	// ReclaimGrace overrides DefaultReclaimGrace when positive.
	ReclaimGrace time.Duration
}

// NewLeaseManager creates a lease manager.
func NewLeaseManager(store Store, claims *PathClaimManager) *LeaseManager {
	return &LeaseManager{
		Store:  store,
		Claims: claims,
	}
}

func (m *LeaseManager) reclaimGrace() time.Duration {
	if m.ReclaimGrace > 0 {
		return m.ReclaimGrace
	}
	return DefaultReclaimGrace
}

// ReclaimExpired releases expired task leases and returns reclaimed tasks.
func (m *LeaseManager) ReclaimExpired(ctx context.Context, teamID string) ([]LeaseReclaim, error) {
	return m.ReclaimExpiredTasks(ctx, teamID, time.Time{}, 0, false)
}

// ReclaimExpiredTasks runs the reclaim state machine at a given time,
// optionally limiting results. It is safe to call repeatedly:
//
//  1. running tasks whose lease expired are marked reclaim_pending (not
//     re-assigned immediately);
//  2. reclaim_pending tasks that stayed past the reclaim grace window are
//     returned to the ready queue via RetryAgentControlTask, which bumps the
//     retry counter and clears the previous fencing token.
func (m *LeaseManager) ReclaimExpiredTasks(ctx context.Context, teamID string, asOf time.Time, limit int, dryRun bool) ([]LeaseReclaim, error) {
	if m == nil || m.Store == nil {
		return nil, fmt.Errorf("lease manager store is not configured")
	}
	teamID = strings.TrimSpace(teamID)
	if teamID == "" {
		return nil, fmt.Errorf("team id is required")
	}
	now := time.Now().UTC()
	if !asOf.IsZero() {
		now = asOf.UTC()
	} else if m.Clock != nil {
		now = m.Clock().UTC()
	}
	reclaimed := make([]LeaseReclaim, 0)
	registry := NewAgentControlTaskRegistry(m.Store).WithClaims(m.Claims)

	// Phase 1: expired running leases -> reclaim_pending.
	tasks, err := m.Store.ListTasks(ctx, TaskFilter{
		TeamID: teamID,
		Status: []TaskStatus{TaskStatusRunning},
	})
	if err != nil {
		return nil, err
	}
	for _, task := range tasks {
		if task.LeaseUntil == nil || task.LeaseUntil.After(now) {
			continue
		}
		previousAssignee := ""
		if task.Assignee != nil {
			previousAssignee = strings.TrimSpace(*task.Assignee)
		}
		previousLease := task.LeaseUntil

		reclaimed = append(reclaimed, LeaseReclaim{
			Task:               task,
			PreviousAssignee:   previousAssignee,
			PreviousLeaseUntil: previousLease,
		})
		if dryRun {
			continue
		}
		if _, err := registry.UpdateAgentControlTaskStatus(ctx, agentcontrol.TaskStatusUpdateRequest{
			ID:       task.ID,
			Workflow: agentcontrol.WorkflowSpawnTeam,
			Status:   string(TaskStatusReclaimPending),
		}); err != nil {
			return nil, err
		}
		// The lease field now carries the reclaim_pending start time (in the
		// asOf clock domain): the grace window is measured from here, so
		// sweeps with a fixed asOf behave deterministically.
		if err := m.Store.RenewTaskLease(ctx, task.ID, now); err != nil {
			return nil, err
		}
		if m.Mailbox != nil {
			body := fmt.Sprintf("Lease expired for task %s. Marked reclaim_pending; re-queued after the reclaim grace window.", summarizeTaskTitle(task))
			_, _ = m.Mailbox.Send(ctx, MailMessage{
				TeamID:    teamID,
				FromAgent: "orchestrator",
				ToAgent:   "*",
				Kind:      "warning",
				Body:      body,
				TaskID:    &task.ID,
			})
		}
		if limit > 0 && len(reclaimed) >= limit {
			break
		}
	}

	// Phase 2: reclaim_pending tasks past the grace window -> ready queue.
	pending, err := m.Store.ListTasks(ctx, TaskFilter{
		TeamID: teamID,
		Status: []TaskStatus{TaskStatusReclaimPending},
	})
	if err != nil {
		return nil, err
	}
	for _, task := range pending {
		pendingSince := task.UpdatedAt
		if task.LeaseUntil != nil {
			pendingSince = *task.LeaseUntil
		}
		if pendingSince.IsZero() || now.Sub(pendingSince) < m.reclaimGrace() {
			continue
		}
		previousAssignee := ""
		if task.Assignee != nil {
			previousAssignee = strings.TrimSpace(*task.Assignee)
		}
		reclaimed = append(reclaimed, LeaseReclaim{
			Task:             task,
			PreviousAssignee: previousAssignee,
		})
		if dryRun {
			continue
		}
		if _, err := registry.RetryAgentControlTask(ctx, agentcontrol.TaskRetryRequest{
			ID:       task.ID,
			Workflow: agentcontrol.WorkflowSpawnTeam,
			Status:   string(TaskStatusReady),
		}); err != nil {
			return nil, err
		}
		if previousAssignee != "" {
			_ = m.Store.UpdateTeammateState(ctx, previousAssignee, TeammateStateIdle)
		}
		if m.Mailbox != nil {
			body := fmt.Sprintf("Reclaim grace elapsed for task %s. Returned to the ready queue for re-assignment.", summarizeTaskTitle(task))
			_, _ = m.Mailbox.Send(ctx, MailMessage{
				TeamID:    teamID,
				FromAgent: "orchestrator",
				ToAgent:   "*",
				Kind:      "warning",
				Body:      body,
				TaskID:    &task.ID,
			})
		}
		if limit > 0 && len(reclaimed) >= limit {
			break
		}
	}
	return reclaimed, nil
}

// RenewTask extends the lease for a task and associated path claims.
func (m *LeaseManager) RenewTask(ctx context.Context, taskID string, leaseUntil time.Time) error {
	if m == nil || m.Store == nil {
		return fmt.Errorf("lease manager store is not configured")
	}
	if strings.TrimSpace(taskID) == "" {
		return fmt.Errorf("task id is required")
	}
	if leaseUntil.IsZero() {
		leaseUntil = time.Now().UTC().Add(5 * time.Minute)
	}
	if _, err := NewAgentControlTaskRegistry(m.Store).WithClaims(m.Claims).RenewAgentControlTaskLease(ctx, agentcontrol.TaskLeaseRenewRequest{
		ID:         taskID,
		Workflow:   agentcontrol.WorkflowSpawnTeam,
		LeaseUntil: leaseUntil,
	}); err != nil {
		return err
	}
	return nil
}

func summarizeTaskTitle(task Task) string {
	title := strings.TrimSpace(task.Title)
	if title == "" {
		title = strings.TrimSpace(task.Goal)
	}
	if title == "" {
		title = task.ID
	}
	return truncateLine(title, 120)
}
