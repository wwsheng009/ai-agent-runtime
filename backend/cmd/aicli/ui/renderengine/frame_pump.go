package renderengine

import (
	"sort"
	"sync"
	"time"
)

// DirtyFlags identifies the part of the scene that caused a render intent.
// Multiple intents may be pending at once; Dirty reports their union so a
// future composer can skip unaffected work without changing scheduling keys.
type DirtyFlags uint32

const (
	DirtyNone DirtyFlags = 0
)

const (
	DirtyContent DirtyFlags = 1 << iota
	DirtyBand
	DirtyStatus
	DirtyPrompt
	DirtyPopup
	DirtyGeometry
	DirtyFull
	DirtyExternal
)

const defaultFrameMaxFPS = 60

// FramePumpConfig controls the scheduler's frame budget. A zero MaxFPS uses
// the production default; a negative value disables the budget for deterministic
// compatibility paths.
type FramePumpConfig struct {
	MaxFPS int
}

// PumpStats is a point-in-time diagnostic snapshot. It intentionally contains
// no callback or scene data, so callers can inspect it without touching UI
// locks.
type PumpStats struct {
	Scheduled       uint64
	Replaced        uint64
	Fired           uint64
	Frames          uint64
	Pending         int
	Dirty           DirtyFlags
	MaxFPS          int
	LastFrameAt     time.Time
	LastFrameBudget time.Duration
}

// FramePump is the single render scheduler of the TUI.
//
// Schedule keeps one pending intent per key. A single coordinator goroutine
// owns the scheduler timer and executes callbacks serially; scheduling a new
// key never creates another timer goroutine. Replacing or cancelling a job is
// handled by removing it from the map, so timer callbacks cannot race a stale
// per-key AfterFunc into the render path.
type FramePump struct {
	mu              sync.Mutex
	closed          bool
	seq             uint64
	jobs            map[string]*scheduledJob
	dirty           DirtyFlags
	maxFPS          int
	frameInterval   time.Duration
	lastFrameAt     time.Time
	lastFrameBudget time.Duration
	scheduled       uint64
	replaced        uint64
	fired           uint64
	frames          uint64
	wake            chan struct{}
	stop            chan struct{}
}

type scheduledJob struct {
	key   string
	due   time.Time
	seq   uint64
	dirty DirtyFlags
	fn    func()
}

// NewFramePump creates a pump and starts its single scheduler goroutine.
func NewFramePump() *FramePump {
	return NewFramePumpWithConfig(FramePumpConfig{})
}

// NewFramePumpWithConfig creates a pump with an explicit frame budget.
func NewFramePumpWithConfig(config FramePumpConfig) *FramePump {
	maxFPS, interval := normalizeFrameBudget(config.MaxFPS)
	p := &FramePump{
		jobs:          make(map[string]*scheduledJob),
		maxFPS:        maxFPS,
		frameInterval: interval,
		wake:          make(chan struct{}, 1),
		stop:          make(chan struct{}),
	}
	go p.run()
	return p
}

func normalizeFrameBudget(maxFPS int) (int, time.Duration) {
	if maxFPS == 0 {
		maxFPS = defaultFrameMaxFPS
	}
	if maxFPS < 0 {
		return maxFPS, 0
	}
	return maxFPS, time.Second / time.Duration(maxFPS)
}

// run waits for the nearest due intent and executes all intents that are due
// at that instant in sequence order. A wake signal restarts the wait whenever
// Schedule or Cancel changes the nearest deadline.
func (p *FramePump) run() {
	for {
		wait, ok := p.nextWait()
		if !ok {
			select {
			case <-p.stop:
				return
			case <-p.wake:
				continue
			}
		}

		timer := time.NewTimer(wait)
		select {
		case <-timer.C:
			jobs := p.takeDue(time.Now())
			for _, job := range jobs {
				job.fn()
			}
		case <-p.wake:
			stopTimer(timer)
		case <-p.stop:
			stopTimer(timer)
			return
		}
	}
}

func stopTimer(timer *time.Timer) {
	if timer == nil || timer.Stop() {
		return
	}
	select {
	case <-timer.C:
	default:
	}
}

func (p *FramePump) nextWait() (time.Duration, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || len(p.jobs) == 0 {
		return 0, false
	}
	var due time.Time
	for _, job := range p.jobs {
		if due.IsZero() || job.due.Before(due) {
			due = job.due
		}
	}
	wait := time.Until(due)
	if !p.lastFrameAt.IsZero() && p.frameInterval > 0 {
		budgetDue := p.lastFrameAt.Add(p.frameInterval)
		if budgetDue.After(due) {
			wait = time.Until(budgetDue)
		}
	}
	if wait < 0 {
		wait = 0
	}
	return wait, true
}

func (p *FramePump) takeDue(now time.Time) []*scheduledJob {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	due := make([]*scheduledJob, 0, len(p.jobs))
	for key, job := range p.jobs {
		if !job.due.After(now) {
			due = append(due, job)
			delete(p.jobs, key)
		}
	}
	p.recomputeDirtyLocked()
	sort.SliceStable(due, func(i, j int) bool {
		if due[i].due.Equal(due[j].due) {
			return due[i].seq < due[j].seq
		}
		return due[i].due.Before(due[j].due)
	})
	if len(due) > 0 {
		p.frames++
		p.fired += uint64(len(due))
		p.lastFrameAt = now
		p.lastFrameBudget = p.frameInterval
	}
	return due
}

func (p *FramePump) recomputeDirtyLocked() {
	var dirty DirtyFlags
	for _, job := range p.jobs {
		dirty |= job.dirty
	}
	p.dirty = dirty
}

func (p *FramePump) signal() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// Schedule arms fn under key after delay. A pending job for the same key is
// replaced. Safe to call from any goroutine; a no-op after Shutdown.
func (p *FramePump) Schedule(key string, delay time.Duration, fn func()) {
	p.ScheduleDirty(key, DirtyExternal, delay, fn)
}

// ScheduleDirty is Schedule with an explicit dirty classification. A pending
// job under the same key is replaced and its dirty bit is replaced as well.
func (p *FramePump) ScheduleDirty(key string, dirty DirtyFlags, delay time.Duration, fn func()) {
	if p == nil || key == "" || fn == nil || delay < 0 {
		return
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	if _, exists := p.jobs[key]; exists {
		p.replaced++
	}
	p.seq++
	p.jobs[key] = &scheduledJob{key: key, due: time.Now().Add(delay), seq: p.seq, dirty: dirty, fn: fn}
	p.scheduled++
	p.recomputeDirtyLocked()
	p.mu.Unlock()
	p.signal()
}

// Pending reports whether a job for key is armed but has not fired yet.
func (p *FramePump) Pending(key string) bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	_, ok := p.jobs[key]
	return ok
}

// PendingCount reports how many jobs are currently armed (diagnostics).
func (p *FramePump) PendingCount() int {
	if p == nil {
		return 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.jobs)
}

// Dirty reports the union of dirty flags for all pending intents.
func (p *FramePump) Dirty() DirtyFlags {
	if p == nil {
		return DirtyNone
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.dirty
}

// Stats returns a consistent diagnostic snapshot of scheduler activity.
func (p *FramePump) Stats() PumpStats {
	if p == nil {
		return PumpStats{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return PumpStats{
		Scheduled:       p.scheduled,
		Replaced:        p.replaced,
		Fired:           p.fired,
		Frames:          p.frames,
		Pending:         len(p.jobs),
		Dirty:           p.dirty,
		MaxFPS:          p.maxFPS,
		LastFrameAt:     p.lastFrameAt,
		LastFrameBudget: p.lastFrameBudget,
	}
}

// SetMaxFPS updates the frame budget for future callbacks. Zero restores the
// production default; a negative value disables rate limiting. Pending jobs
// are not dropped, and the scheduler is woken so a tightened budget applies
// immediately.
func (p *FramePump) SetMaxFPS(maxFPS int) {
	if p == nil {
		return
	}
	maxFPS, interval := normalizeFrameBudget(maxFPS)
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.maxFPS = maxFPS
	p.frameInterval = interval
	p.mu.Unlock()
	p.signal()
}

// MaxFPS reports the current frame budget. A negative value means unlimited.
func (p *FramePump) MaxFPS() int {
	if p == nil {
		return 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.maxFPS
}

// Cancel disarms a pending job for key. A callback already removed from the
// pending set is considered fired and is not interrupted.
func (p *FramePump) Cancel(key string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	delete(p.jobs, key)
	p.recomputeDirtyLocked()
	p.mu.Unlock()
	p.signal()
}

// Shutdown disarms all pending jobs and stops the scheduler goroutine.
// Idempotent. A callback already executing is allowed to finish.
func (p *FramePump) Shutdown() {
	if p == nil {
		return
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	p.jobs = nil
	p.mu.Unlock()
	close(p.stop)
}
