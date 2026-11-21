package memoryruntime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
)

const (
	jobLeaseHeartbeatTimeout     = 5 * time.Second
	jobLeaseHeartbeatMinInterval = 10 * time.Millisecond
)

// withJobLeaseHeartbeat keeps a claimed memory job alive while a bounded model
// call runs. The heartbeat uses the exact worker/attempt fence; a lost lease
// cancels the model context so stale provider output is not committed.
func withJobLeaseHeartbeat(
	ctx context.Context,
	store *memorystore.Store,
	lease time.Duration,
	job memorystore.MemoryJob,
	action func(context.Context) error,
) error {
	if action == nil {
		return nil
	}
	if store == nil || lease <= 0 || strings.TrimSpace(job.ID) == "" ||
		strings.TrimSpace(job.WorkerID) == "" || job.Attempt <= 0 {
		return action(ctx)
	}
	interval := lease / 3
	if interval < jobLeaseHeartbeatMinInterval {
		interval = jobLeaseHeartbeatMinInterval
	}
	if interval >= lease {
		interval = lease / 2
	}
	if interval <= 0 {
		return action(ctx)
	}

	actionCtx, cancel := context.WithCancelCause(ctx)
	var wg sync.WaitGroup
	defer wg.Wait()
	defer cancel(nil)
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-actionCtx.Done():
				return
			case <-ticker.C:
				heartbeatCtx, heartbeatCancel := context.WithTimeout(
					context.WithoutCancel(actionCtx), jobLeaseHeartbeatTimeout)
				err := store.HeartbeatJobForWorkerAttempt(
					heartbeatCtx, job.ID, job.WorkerID, job.Attempt, lease)
				heartbeatCancel()
				if err != nil {
					cancel(fmt.Errorf("memoryruntime: job heartbeat failed: %w", err))
					return
				}
			}
		}
	}()

	err := action(actionCtx)
	cancel(nil)
	wg.Wait()
	if cause := context.Cause(actionCtx); cause != nil && !errors.Is(cause, context.Canceled) {
		return errors.Join(err, cause)
	}
	return err
}
