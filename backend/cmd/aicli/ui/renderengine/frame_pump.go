package renderengine

import (
	"sync"
	"time"
)

// FramePump is the single render scheduler of the TUI (Stage A).
//
// Semantics are designed to be drop-in equivalent to the coordinator's old
// `time.AfterFunc` fields:
//
//   - Schedule(key, delay, fn): arms one delayed job per key. A pending job
//     for the same key is replaced (its timer is stopped and the new
//     delay/fn take over) — this matches the active-frame "reschedule when
//     the new due time is earlier" pattern and the prompt "last redraw wins"
//     pattern.
//   - Pending(key): reports whether a job is armed but not yet fired — the
//     replacement for the old `timer != nil` guards.
//   - Cancel(key): disarms a pending job. A callback that already fired (or
//     is already queued) still runs, exactly like time.Timer.Stop(); callers
//     keep their existing sequence/generation guards for that edge.
//   - Shutdown(): disarms everything and stops the executor goroutine.
//     Later Schedule calls become no-ops.
//
// All callbacks run serially on a single executor goroutine. The old code
// already serialized every callback behind the coordinator mutex, so this
// does not change ordering; it removes the per-timer goroutine spawns and
// gives later stages (ScreenModel compose, Presenter batch flush) one
// well-defined render thread.
type FramePump struct {
	mu     sync.Mutex
	closed bool
	jobs   map[string]*scheduledJob
	exec   chan func()
	stop   chan struct{}
}

type scheduledJob struct {
	key   string
	timer *time.Timer
	fn    func()
}

// NewFramePump creates a pump and starts its executor goroutine.
func NewFramePump() *FramePump {
	p := &FramePump{
		jobs: make(map[string]*scheduledJob),
		exec: make(chan func()),
		stop: make(chan struct{}),
	}
	go p.run()
	return p
}

// run executes callbacks serially until Shutdown closes stop.
func (p *FramePump) run() {
	for {
		select {
		case fn := <-p.exec:
			fn()
		case <-p.stop:
			return
		}
	}
}

// Schedule arms fn under key after delay. A pending job for the same key is
// replaced. Safe to call from any goroutine; a no-op after Shutdown.
func (p *FramePump) Schedule(key string, delay time.Duration, fn func()) {
	if key == "" || fn == nil || delay < 0 {
		return
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	if old := p.jobs[key]; old != nil {
		old.timer.Stop()
		delete(p.jobs, key)
	}
	j := &scheduledJob{key: key, fn: fn}
	p.jobs[key] = j
	j.timer = time.AfterFunc(delay, func() { p.fire(j) })
	p.mu.Unlock()
}

// fire moves a fired job to the executor. It is called from the timer's own
// goroutine. If the job was replaced or cancelled in the meantime, or the
// pump is shut down, the callback is dropped.
func (p *FramePump) fire(j *scheduledJob) {
	p.mu.Lock()
	if p.closed || p.jobs[j.key] != j {
		p.mu.Unlock()
		return
	}
	delete(p.jobs, j.key)
	p.mu.Unlock()
	select {
	case p.exec <- j.fn:
	case <-p.stop:
	}
}

// Pending reports whether a job for key is armed but has not fired yet.
func (p *FramePump) Pending(key string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, ok := p.jobs[key]
	return ok
}

// PendingCount reports how many jobs are currently armed (diagnostics).
func (p *FramePump) PendingCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.jobs)
}

// Cancel disarms a pending job for key. Callbacks that already fired are not
// interrupted; callers must keep their sequence/generation guards.
func (p *FramePump) Cancel(key string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	if j := p.jobs[key]; j != nil {
		j.timer.Stop()
		delete(p.jobs, key)
	}
}

// Shutdown disarms all pending jobs and stops the executor goroutine.
// Idempotent. After Shutdown, Schedule and Cancel are no-ops.
func (p *FramePump) Shutdown() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	for _, j := range p.jobs {
		j.timer.Stop()
	}
	p.jobs = nil
	p.mu.Unlock()
	close(p.stop)
}
