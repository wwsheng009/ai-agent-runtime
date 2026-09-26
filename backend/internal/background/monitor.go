package background

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// MonitorCheckEventType names the job event a scheduled monitor emits when its
// check deadline arrives. Hosts project it into a durable supervision item plus
// a progress-class wake, so an idle session resumes with the job's current state
// instead of polling task_output.
const MonitorCheckEventType = "monitor_check"

// ErrJobNotRunning marks a monitor request for a job that already reached a
// terminal state: there is nothing left to monitor, which callers report as a
// content result rather than a failure.
var ErrJobNotRunning = errors.New("background: job is not running")

// Monitor deadline defaults and bounds. The tool layer narrows these to the
// model-facing range (≥5s check, ≤10min check, ≤1h max duration); the manager
// only clamps defensively so a hand-built Manager can never be armed with an
// absurd timer.
const (
	DefaultMonitorCheckAfter = 45 * time.Second
	MinMonitorCheckAfter     = 100 * time.Millisecond
	MaxMonitorCheckAfter     = 30 * time.Minute
	MinMonitorMaxDuration    = 100 * time.Millisecond
	MaxMonitorMaxDuration    = 2 * time.Hour
)

// CancelSourceMonitorMaxDuration is stamped on a job the monitor's max-duration
// deadline terminated, so the terminal evidence distinguishes it from a user
// cancellation.
const CancelSourceMonitorMaxDuration = "monitor_max_duration"

// MonitorOptions carries the two monitor deadlines. MaxDuration == 0 disables
// the automatic termination.
type MonitorOptions struct {
	CheckAfter  time.Duration
	MaxDuration time.Duration
}

func (o MonitorOptions) normalized() MonitorOptions {
	out := o
	if out.CheckAfter <= 0 {
		out.CheckAfter = DefaultMonitorCheckAfter
	}
	out.CheckAfter = clampDuration(out.CheckAfter, MinMonitorCheckAfter, MaxMonitorCheckAfter)
	if out.MaxDuration <= 0 {
		out.MaxDuration = 0
		return out
	}
	out.MaxDuration = clampDuration(out.MaxDuration, MinMonitorMaxDuration, MaxMonitorMaxDuration)
	return out
}

func clampDuration(value, min, max time.Duration) time.Duration {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

// MonitorInfo is the durable description of a scheduled monitor, sized for the
// model-visible tool result.
type MonitorInfo struct {
	MonitorID     string        `json:"monitor_id"`
	JobID         string        `json:"job_id"`
	SessionID     string        `json:"session_id,omitempty"`
	CheckAfter    time.Duration `json:"check_after"`
	CheckAt       time.Time     `json:"check_at"`
	MaxDuration   time.Duration `json:"max_duration,omitempty"`
	MaxDurationAt time.Time     `json:"max_duration_at,omitempty"`
}

type monitorEntry struct {
	info  MonitorInfo
	check *time.Timer
	kill  *time.Timer
}

// monitorRegistry keeps the in-process monitor timers per job. Monitors are
// deliberately in-memory: they only exist to nudge a live session, while the
// evidence itself stays durable in the job store.
type monitorRegistry struct {
	mu    sync.Mutex
	seq   uint64
	byJob map[string][]*monitorEntry
}

// monitors returns the manager's monitor registry, creating it on first use so
// hosts and tests that assemble a Manager literally keep working.
func (m *Manager) monitors() *monitorRegistry {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	if m.monitorRegistry == nil {
		m.monitorRegistry = &monitorRegistry{byJob: make(map[string][]*monitorEntry)}
	}
	registry := m.monitorRegistry
	m.mu.Unlock()
	return registry
}

// ScheduleMonitor arms a one-shot check for a running job. At the check
// deadline the manager emits MonitorCheckEventType (unless the job already
// reached a terminal state, which has its own wake path); an optional
// MaxDuration deadline terminates the job through the shared cancellation path
// so the terminal evidence still flows through the normal projection.
func (m *Manager) ScheduleMonitor(jobID string, opts MonitorOptions) (MonitorInfo, error) {
	if m == nil {
		return MonitorInfo{}, fmt.Errorf("background manager is nil")
	}
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return MonitorInfo{}, fmt.Errorf("job_id is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	job, err := m.GetJob(ctx, jobID)
	if err != nil {
		return MonitorInfo{}, err
	}
	if job == nil {
		return MonitorInfo{}, jobNotFoundError(jobID)
	}
	if IsTerminalStatus(job.Status) {
		return MonitorInfo{}, fmt.Errorf("%w: %s", ErrJobNotRunning, job.Status)
	}

	options := opts.normalized()
	now := time.Now().UTC()
	registry := m.monitors()
	if registry == nil {
		return MonitorInfo{}, fmt.Errorf("background manager is nil")
	}
	registry.mu.Lock()
	registry.seq++
	info := MonitorInfo{
		MonitorID:   fmt.Sprintf("mon-%d-%d", now.UnixNano(), registry.seq),
		JobID:       jobID,
		SessionID:   strings.TrimSpace(job.SessionID),
		CheckAfter:  options.CheckAfter,
		CheckAt:     now.Add(options.CheckAfter),
		MaxDuration: options.MaxDuration,
	}
	if options.MaxDuration > 0 {
		info.MaxDurationAt = now.Add(options.MaxDuration)
	}
	entry := &monitorEntry{info: info}
	entry.check = time.AfterFunc(options.CheckAfter, func() { m.fireMonitor(jobID, info.MonitorID) })
	if options.MaxDuration > 0 {
		maxDuration := options.MaxDuration
		entry.kill = time.AfterFunc(maxDuration, func() { m.fireMonitorMaxDuration(jobID, info.MonitorID) })
	}
	registry.byJob[jobID] = append(registry.byJob[jobID], entry)
	registry.mu.Unlock()
	return info, nil
}

// takeMonitor removes one entry so a monitor fires at most once and its sibling
// timer can be stopped.
func (m *Manager) takeMonitor(jobID, monitorID string) (*monitorEntry, bool) {
	registry := m.monitors()
	if registry == nil {
		return nil, false
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	entries := registry.byJob[jobID]
	for index, entry := range entries {
		if entry == nil || entry.info.MonitorID != monitorID {
			continue
		}
		remaining := append(entries[:index:index], entries[index+1:]...)
		if len(remaining) == 0 {
			delete(registry.byJob, jobID)
		} else {
			registry.byJob[jobID] = remaining
		}
		return entry, true
	}
	return nil, false
}

func stopMonitorTimer(timer *time.Timer) {
	if timer != nil {
		timer.Stop()
	}
}

// fireMonitor emits the check event for a still-running job. A job that reached
// a terminal state in the meantime is dropped on purpose: the terminal
// transition already produced the wake that carries the same evidence.
func (m *Manager) fireMonitor(jobID, monitorID string) {
	entry, ok := m.takeMonitor(jobID, monitorID)
	if !ok {
		return
	}
	stopMonitorTimer(entry.kill)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	job, err := m.GetJob(ctx, jobID)
	if err != nil || job == nil || IsTerminalStatus(job.Status) {
		return
	}
	elapsed := time.Since(entry.info.CheckAt.Add(-entry.info.CheckAfter))
	m.appendJobEvent(context.Background(), jobID, MonitorCheckEventType, map[string]interface{}{
		"status":         job.Status,
		"monitor_id":     entry.info.MonitorID,
		"check_after_ms": entry.info.CheckAfter.Milliseconds(),
		"elapsed_ms":     elapsed.Milliseconds(),
	})
}

// fireMonitorMaxDuration terminates a job that outlived its monitor deadline.
func (m *Manager) fireMonitorMaxDuration(jobID, monitorID string) {
	entry, ok := m.takeMonitor(jobID, monitorID)
	if !ok {
		return
	}
	stopMonitorTimer(entry.check)
	_, _ = m.cancelJobWithSource(context.Background(), jobID, CancelSourceMonitorMaxDuration)
}

// cancelJobMonitors stops every monitor armed for the job. It runs on the
// terminal transition so a finished job never fires a late check.
func (m *Manager) cancelJobMonitors(jobID string) int {
	registry := m.monitors()
	if registry == nil {
		return 0
	}
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return 0
	}
	registry.mu.Lock()
	entries := registry.byJob[jobID]
	delete(registry.byJob, jobID)
	registry.mu.Unlock()
	for _, entry := range entries {
		if entry == nil {
			continue
		}
		stopMonitorTimer(entry.check)
		stopMonitorTimer(entry.kill)
	}
	return len(entries)
}

// CancelSessionMonitors stops every monitor belonging to the session; hosts call
// it when a session closes, where a later check would only deliver a nudge to a
// session that no longer exists.
func (m *Manager) CancelSessionMonitors(sessionID string) int {
	if m == nil {
		return 0
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return 0
	}
	registry := m.monitors()
	registry.mu.Lock()
	jobIDs := make([]string, 0, len(registry.byJob))
	for jobID := range registry.byJob {
		jobIDs = append(jobIDs, jobID)
	}
	registry.mu.Unlock()

	cancelled := 0
	for _, jobID := range jobIDs {
		managed := m.getJob(jobID)
		if managed == nil {
			continue
		}
		managed.mu.RLock()
		jobSession := strings.TrimSpace(managed.info.SessionID)
		managed.mu.RUnlock()
		if jobSession != sessionID {
			continue
		}
		cancelled += m.cancelJobMonitors(jobID)
	}
	return cancelled
}

// ActiveMonitorCount reports how many monitors are currently armed. It exists
// for host cleanup tests and observability.
func (m *Manager) ActiveMonitorCount() int {
	if m == nil {
		return 0
	}
	registry := m.monitors()
	registry.mu.Lock()
	defer registry.mu.Unlock()
	total := 0
	for _, entries := range registry.byJob {
		total += len(entries)
	}
	return total
}
