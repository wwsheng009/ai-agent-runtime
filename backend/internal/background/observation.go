package background

import (
	"strings"
	"sync"
	"time"
)

// MetadataTerminalObserved marks a job whose terminal state an in-flight
// task_output long-poll will surface to the model. Hosts read it through
// TerminalObserved: when set, the terminal state is already in the model's
// context, so the supervision projection records the durable inbox item without
// scheduling a redundant wake turn.
//
// It is exported so hosts (and their tests) can stamp jobs whose terminal state
// reaches the model through a read path other than the broker's wait. Prefer
// TerminalObserved for reads: a missing key means "not observed".
const MetadataTerminalObserved = "terminal_observed"

// Terminal output drain metadata. output_drained_at marks the terminal
// transition's declared drain boundary; late_output_count/late_output_discarded
// record producer writes that arrived after that boundary and were deliberately
// not attached to the settled result.
const (
	backgroundMetaOutputDrainedAt     = "output_drained_at"
	backgroundMetaLateOutputCount     = "late_output_count"
	backgroundMetaLateOutputDiscarded = "late_output_discarded"
)

// outputWaitMaxAge bounds how long an in-flight waiter registration is trusted.
// The longest supported task_output wait is two minutes; anything older is a
// leak (a wait that returned without unregistering) and must not suppress a
// legitimate wake.
const outputWaitMaxAge = 5 * time.Minute

// outputWaitRegistry counts in-flight task_output long-polls per job. It is
// in-memory by design: the flag it produces only matters for the transition
// that happens while the waiter is actually running.
type outputWaitRegistry struct {
	mu      sync.Mutex
	waiters map[string]outputWaitMark
}

type outputWaitMark struct {
	count int
	since time.Time
	last  time.Time
}

func newOutputWaitRegistry() *outputWaitRegistry {
	return &outputWaitRegistry{waiters: make(map[string]outputWaitMark)}
}

func (r *outputWaitRegistry) begin(jobID string, now time.Time) {
	if r == nil {
		return
	}
	if r.waiters == nil {
		r.waiters = make(map[string]outputWaitMark)
	}
	mark := r.waiters[jobID]
	mark.count++
	if mark.since.IsZero() {
		mark.since = now
	}
	mark.last = now
	r.waiters[jobID] = mark
}

func (r *outputWaitRegistry) end(jobID string, now time.Time) {
	if r == nil {
		return
	}
	mark, ok := r.waiters[jobID]
	if !ok {
		return
	}
	mark.count--
	if mark.count <= 0 {
		delete(r.waiters, jobID)
		return
	}
	mark.last = now
	r.waiters[jobID] = mark
}

func (r *outputWaitRegistry) active(jobID string, now time.Time) bool {
	if r == nil {
		return false
	}
	mark, ok := r.waiters[jobID]
	if !ok {
		return false
	}
	if now.Sub(mark.last) > outputWaitMaxAge {
		delete(r.waiters, jobID)
		return false
	}
	return mark.count > 0
}

// outputWaiters returns the manager's waiter registry, creating it on first use
// so hosts and tests that assemble a Manager literally keep working.
func (m *Manager) outputWaiters() *outputWaitRegistry {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	if m.waiters == nil {
		m.waiters = newOutputWaitRegistry()
	}
	registry := m.waiters
	m.mu.Unlock()
	return registry
}

// BeginOutputWait registers an in-flight task_output long-poll for jobID. The
// broker brackets its wait with Begin/End so a terminal transition that happens
// during the wait can be recognized as "the model is about to read this".
func (m *Manager) BeginOutputWait(jobID string) {
	if m == nil {
		return
	}
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return
	}
	registry := m.outputWaiters()
	registry.mu.Lock()
	registry.begin(jobID, time.Now().UTC())
	registry.mu.Unlock()
}

// EndOutputWait releases a registration made by BeginOutputWait. It must be
// called exactly once per successful Begin (the broker uses defer).
func (m *Manager) EndOutputWait(jobID string) {
	if m == nil {
		return
	}
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return
	}
	registry := m.outputWaiters()
	registry.mu.Lock()
	registry.end(jobID, time.Now().UTC())
	registry.mu.Unlock()
}

// outputWaitActive reports whether any task_output long-poll is currently
// waiting on the job.
func (m *Manager) outputWaitActive(jobID string) bool {
	if m == nil {
		return false
	}
	registry := m.outputWaiters()
	registry.mu.Lock()
	active := registry.active(strings.TrimSpace(jobID), time.Now().UTC())
	registry.mu.Unlock()
	return active
}

// TerminalObserved reports whether the job's terminal state has already been
// (or is about to be) surfaced to the model through a task_output wait. Hosts
// use it to record the terminal notification without forcing an extra turn.
func TerminalObserved(job *Job) bool {
	if job == nil {
		return false
	}
	observed, _ := job.Metadata[MetadataTerminalObserved].(bool)
	return observed
}

// outputTerminal reports whether the job's output buffer is closed to producer
// writes. It is read under managed.mu; callers may already hold outputMu.
func (j *managedJob) outputTerminal() bool {
	if j == nil {
		return false
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return isTerminalStatus(j.info.Status)
}

// recordLateOutput counts bytes discarded after the terminal drain boundary.
// The in-memory metadata is enough for the live task_output view; the terminal
// job row is not rewritten on every late chunk.
func (j *managedJob) recordLateOutput(bytes int) {
	if j == nil || bytes <= 0 {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.info.Metadata == nil {
		j.info.Metadata = map[string]interface{}{}
	}
	existing, _ := intMetadataValue(j.info.Metadata, backgroundMetaLateOutputCount)
	j.info.Metadata[backgroundMetaLateOutputCount] = int64(existing + bytes)
	j.info.Metadata[backgroundMetaLateOutputDiscarded] = true
}

// markTerminalObservedByWaiter stamps the job before its terminal event reaches
// the host handler: an active waiter reads the terminal snapshot within one
// poll interval (250ms), so the model already has the evidence this event would
// otherwise deliver through a wake turn. Marking is best-effort; a missing job
// or store only means a redundant wake.
func (m *Manager) markTerminalObservedByWaiter(jobID string) {
	if m == nil {
		return
	}
	jobID = strings.TrimSpace(jobID)
	if jobID == "" || !m.outputWaitActive(jobID) {
		return
	}
	managed := m.getJob(jobID)
	if managed == nil {
		return
	}
	managed.mu.Lock()
	if managed.info.Metadata == nil {
		managed.info.Metadata = map[string]interface{}{}
	}
	if observed, _ := managed.info.Metadata[MetadataTerminalObserved].(bool); observed {
		managed.mu.Unlock()
		return
	}
	managed.info.Metadata[MetadataTerminalObserved] = true
	managed.mu.Unlock()
	m.persistManagedJob(managed)
}
