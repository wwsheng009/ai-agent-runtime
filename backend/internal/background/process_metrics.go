package background

import (
	"strconv"
	"strings"
	"time"
)

// usageSample is the previous CPU sample used to derive a percentage.
type usageSample struct {
	cpuSeconds float64
	at         time.Time
}

// processMetrics returns a CPU percentage (from the delta since the previous
// sample for this job) and RSS in KB. Either value is nil when unavailable
// (2026-10-11).
func (m *Manager) processMetrics(jobID string, pid int) (*float64, *int64) {
	if m == nil || pid <= 0 {
		return nil, nil
	}
	usage, ok := sampleProcessUsage(pid)
	if !ok {
		return nil, nil
	}
	rss := usage.RSSKB
	now := time.Now()

	m.metricsMu.Lock()
	if m.lastUsage == nil {
		m.lastUsage = make(map[string]usageSample)
	}
	prev, seen := m.lastUsage[jobID]
	m.lastUsage[jobID] = usageSample{cpuSeconds: usage.CPUSeconds, at: now}
	m.metricsMu.Unlock()

	var cpu *float64
	if seen {
		if elapsed := now.Sub(prev.at).Seconds(); elapsed > 0 {
			percent := (usage.CPUSeconds - prev.cpuSeconds) / elapsed * 100
			if percent < 0 {
				percent = 0
			}
			cpu = &percent
		}
	}
	return cpu, &rss
}

// attachProcessMetrics fills os_pid/cpu_percent/rss_kb on a task output result
// from the job's recorded pid (2026-10-11).
func (m *Manager) attachProcessMetrics(result *TaskOutputResult, info Job) {
	if m == nil || result == nil {
		return
	}
	pid := jobPID(info.Metadata)
	if pid <= 0 {
		return
	}
	result.OSPID = &pid
	cpu, rss := m.processMetrics(info.ID, pid)
	result.CPUPercent = cpu
	result.RSSKB = rss
}

// attachTerminalMetrics fills the same fields on a write_stdin exchange result.
func (m *Manager) attachTerminalMetrics(result *TerminalReadResult, sess *interactiveSession) {
	if m == nil || result == nil || sess == nil {
		return
	}
	sess.job.mu.RLock()
	pid := jobPID(sess.job.info.Metadata)
	jobID := sess.job.info.ID
	sess.job.mu.RUnlock()
	if pid <= 0 {
		return
	}
	result.OSPID = &pid
	cpu, rss := m.processMetrics(jobID, pid)
	result.CPUPercent = cpu
	result.RSSKB = rss
}

// jobPID extracts the recorded process id from job metadata.
func jobPID(metadata map[string]interface{}) int {
	if metadata == nil {
		return 0
	}
	switch value := metadata[backgroundMetaPID].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	case string:
		if pid, err := strconv.Atoi(strings.TrimSpace(value)); err == nil {
			return pid
		}
	}
	return 0
}
