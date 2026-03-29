package team

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// LeaseManager handles task lease renewal and reclamation.
type LeaseManager struct {
	Store   Store
	Claims  *PathClaimManager
	Mailbox *MailboxService
	Clock   func() time.Time
}

// NewLeaseManager creates a lease manager.
func NewLeaseManager(store Store, claims *PathClaimManager) *LeaseManager {
	return &LeaseManager{
		Store:  store,
		Claims: claims,
	}
}

// ReclaimExpired releases expired task leases and returns reclaimed tasks.
func (m *LeaseManager) ReclaimExpired(ctx context.Context, teamID string) ([]LeaseReclaim, error) {
	return m.ReclaimExpiredTasks(ctx, teamID, time.Time{}, 0, false)
}

// ReclaimExpiredTasks reclaims expired leases at a given time, optionally limiting results.
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
	tasks, err := m.Store.ListTasks(ctx, TaskFilter{
		TeamID: teamID,
		Status: []TaskStatus{TaskStatusRunning},
	})
	if err != nil {
		return nil, err
	}
	if len(tasks) == 0 {
		return nil, nil
	}

	reclaimed := make([]LeaseReclaim, 0)
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
		_ = m.Store.ReleaseTask(ctx, task.ID, TaskStatusReady)
		_ = m.Store.IncrementTaskRetry(ctx, task.ID)
		if m.Claims != nil {
			_ = m.Claims.Release(ctx, task.ID)
		}
		if previousAssignee != "" {
			_ = m.Store.UpdateTeammateState(ctx, previousAssignee, TeammateStateIdle)
		}
		if m.Mailbox != nil {
			body := fmt.Sprintf("Lease expired for task %s. Reclaimed and returned to ready queue.", summarizeTaskTitle(task))
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
	if err := m.Store.RenewTaskLease(ctx, taskID, leaseUntil); err != nil {
		return err
	}
	if m.Claims != nil {
		_ = m.Claims.Renew(ctx, taskID, leaseUntil)
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
