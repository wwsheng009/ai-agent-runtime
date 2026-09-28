package background

import (
	"context"
	"strings"
	"time"
)

const (
	// backgroundMetaProcessGroup records the process tree id (Job Object root
	// pid / Unix pgid) for observability and cross-instance kills.
	backgroundMetaProcessGroup = "process_group"
	// backgroundMetaKillAttempts / backgroundMetaKillRetryAt document reaper
	// retries on the job metadata.
	backgroundMetaKillAttempts = "kill_attempts"
	backgroundMetaKillRetryAt  = "kill_retry_at"
)

// Kill verification tuning (P2): a kill is only reported as done once the
// recorded process (matched by identity when available) is actually gone.
const (
	killVerifyTimeout  = 5 * time.Second
	killVerifyPoll     = 100 * time.Millisecond
	reaperMaxAttempts  = 3
	reaperRetryBackoff = 10 * time.Second
	reaperFinalBackoff = 5 * time.Minute
)

// killWatchEntry tracks a terminal job whose recorded process may still be
// alive; the reaper verifies and kills it until it is gone (2026-09-28, P2).
type killWatchEntry struct {
	jobID     string
	pid       int
	identity  string
	attempts  int
	nextAt    time.Time
	lastErr   string
	exhausted bool
}

// processMatchesGone reports whether the recorded process is gone. An identity
// mismatch means the pid was reused, so the original process is gone too.
func processMatchesGone(pid int, identity string) bool {
	if pid <= 0 {
		return true
	}
	health := inspectProcess(pid)
	if !health.Running || health.Zombie {
		return true
	}
	return !detachedProcessMatches(map[string]interface{}{backgroundMetaProcessIdentity: identity}, health)
}

func waitProcessGone(pid int, identity string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if processMatchesGone(pid, identity) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(killVerifyPoll)
	}
}

func (m *Manager) registerKillWatch(jobID string, pid int, identity string, attempts int, lastErr string) {
	if m == nil || pid <= 0 {
		return
	}
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return
	}
	m.killWatchMu.Lock()
	defer m.killWatchMu.Unlock()
	if m.killWatch == nil {
		m.killWatch = make(map[string]*killWatchEntry)
	}
	entry, exists := m.killWatch[jobID]
	if !exists {
		entry = &killWatchEntry{jobID: jobID, pid: pid, identity: identity}
		m.killWatch[jobID] = entry
	}
	entry.pid = pid
	if identity != "" {
		entry.identity = identity
	}
	if attempts > entry.attempts {
		entry.attempts = attempts
	}
	if lastErr != "" {
		entry.lastErr = lastErr
	}
	if entry.nextAt.IsZero() {
		entry.nextAt = time.Now().UTC()
	}
}

// registerTerminalKillWatch makes the reaper verify that a terminal job left no
// live process behind.
func (m *Manager) registerTerminalKillWatch(managed *managedJob) {
	if m == nil || managed == nil {
		return
	}
	managed.mu.RLock()
	jobID := managed.info.ID
	pid, hasPID := detachedPID(managed.info.Metadata)
	identity, _ := stringMetadataValue(managed.info.Metadata, backgroundMetaProcessIdentity)
	managed.mu.RUnlock()
	if !hasPID {
		return
	}
	m.registerKillWatch(jobID, pid, identity, 0, "")
}

// killAndVerifyJobProcess kills the recorded process tree and verifies it is
// gone; leftovers are handed to the reaper instead of being forgotten
// (2026-09-28, P2).
func (m *Manager) killAndVerifyJobProcess(ctx context.Context, jobID string, pid int, identity string) {
	if m == nil || pid <= 0 {
		return
	}
	killErr := terminateJobProcess(pid)
	if waitProcessGone(pid, identity, killVerifyTimeout) {
		m.appendJobEvent(ctx, jobID, "kill_verified", map[string]interface{}{
			"pid": pid,
		})
		return
	}
	errText := "process still alive after kill"
	if killErr != nil {
		errText = killErr.Error()
	}
	m.appendJobEvent(ctx, jobID, "kill_failed", map[string]interface{}{
		"pid":   pid,
		"error": errText,
	})
	m.registerKillWatch(jobID, pid, identity, 1, errText)
}

// releaseJobTree terminates the in-memory process tree of a terminal job (the
// whole tree dies as one unit) and drops the handle. Jobs that still run keep
// their tree; only terminal transitions call this.
func (m *Manager) releaseJobTree(managed *managedJob) {
	if managed == nil {
		return
	}
	managed.mu.Lock()
	tree := managed.tree
	managed.tree = nil
	managed.mu.Unlock()
	if tree == nil {
		return
	}
	_ = tree.Terminate()
	tree.Release()
}

// runOrphanReaper verifies that terminal jobs left no live process behind and
// kills (with retries) when they did. It runs on OrphanReaperInterval ticks on
// the instance that spawned the jobs (2026-09-28, P2).
func (m *Manager) runOrphanReaper() {
	if m == nil {
		return
	}
	now := time.Now().UTC()
	due := make([]*killWatchEntry, 0)
	m.killWatchMu.Lock()
	for _, entry := range m.killWatch {
		if entry == nil || now.Before(entry.nextAt) {
			continue
		}
		due = append(due, entry)
	}
	m.killWatchMu.Unlock()

	for _, entry := range due {
		if m.reapKillWatch(entry) {
			m.killWatchMu.Lock()
			delete(m.killWatch, entry.jobID)
			m.killWatchMu.Unlock()
		}
	}
}

func (m *Manager) reapKillWatch(entry *killWatchEntry) bool {
	if entry == nil {
		return true
	}
	ctx := context.Background()
	if processMatchesGone(entry.pid, entry.identity) {
		if entry.attempts > 0 {
			m.appendJobEvent(ctx, entry.jobID, "kill_verified", map[string]interface{}{
				"pid":      entry.pid,
				"attempts": entry.attempts,
			})
		}
		return true
	}
	killErr := terminateJobProcess(entry.pid)
	if waitProcessGone(entry.pid, entry.identity, killVerifyTimeout) {
		m.appendJobEvent(ctx, entry.jobID, "kill_verified", map[string]interface{}{
			"pid":      entry.pid,
			"attempts": entry.attempts + 1,
		})
		return true
	}
	entry.attempts++
	entry.lastErr = "process still alive after kill"
	if killErr != nil {
		entry.lastErr = killErr.Error()
	}
	if entry.attempts <= reaperMaxAttempts {
		m.appendJobEvent(ctx, entry.jobID, "kill_failed", map[string]interface{}{
			"pid":      entry.pid,
			"attempts": entry.attempts,
			"error":    entry.lastErr,
		})
	}
	backoff := reaperRetryBackoff
	if entry.attempts >= reaperMaxAttempts {
		if !entry.exhausted {
			entry.exhausted = true
			m.appendJobEvent(ctx, entry.jobID, "kill_failed", map[string]interface{}{
				"pid":       entry.pid,
				"attempts":  entry.attempts,
				"error":     entry.lastErr,
				"exhausted": true,
			})
		}
		backoff = reaperFinalBackoff
	}
	entry.nextAt = time.Now().UTC().Add(backoff)
	return false
}
