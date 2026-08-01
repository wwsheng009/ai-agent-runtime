package teamsupervisor

import (
	"context"
	"errors"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"time"
)

const DefaultScanInterval = 5 * time.Second

var DefaultRestartBackoff = []time.Duration{
	time.Second,
	2 * time.Second,
	5 * time.Second,
	10 * time.Second,
	30 * time.Second,
	time.Minute,
}

const DefaultMaxRestartBackoff = 5 * time.Minute

type Config struct {
	ScanInterval      time.Duration
	RestartBackoff    []time.Duration
	MaxRestartBackoff time.Duration
	Jitter            func(time.Duration) time.Duration
	Clock             func() time.Time
}

type Hooks struct {
	DesiredTeams func(context.Context) ([]string, error)
	RunLoop      func(context.Context, string, <-chan struct{}) error
	OwnerAllowed func(context.Context, string) bool
	// OnEvent and OnSettled run synchronously. They must not call Stop,
	// Reconcile, or StopLoop on this Supervisor.
	OnEvent   func(Event)
	OnSettled func(string)
}

type Event struct {
	Type          string
	TeamID        string
	Generation    uint64
	RestartCount  int
	Reason        string
	Error         string
	NextRestartAt time.Time
	Timestamp     time.Time
}

type LoopSnapshot struct {
	TeamID        string
	Generation    uint64
	Running       bool
	RestartCount  int
	LastExitError string
	NextRestartAt time.Time
}

type Snapshot struct {
	Running        bool
	LoopCount      int
	RestartTotal   int
	RestartPending int
	DegradedLoops  int
	Loops          []LoopSnapshot
}

type loopEntry struct {
	cancel        context.CancelFunc
	wake          chan struct{}
	generation    uint64
	running       bool
	restartCount  int
	lastExitError string
	nextRestartAt time.Time
}

type Supervisor struct {
	config Config
	hooks  Hooks

	mu             sync.Mutex
	reconcileMu    sync.Mutex
	loops          map[string]*loopEntry
	nextGeneration uint64
	started        bool
	stopping       bool
	restartPending bool
	stop           context.CancelFunc
	done           chan struct{}
	stopDone       chan struct{}
	reconcileWake  chan struct{}
	runWG          sync.WaitGroup
}

func New(config Config, hooks Hooks) *Supervisor {
	config = normalizeConfig(config)
	return &Supervisor{
		config:        config,
		hooks:         hooks,
		loops:         make(map[string]*loopEntry),
		reconcileWake: make(chan struct{}, 1),
	}
}

func normalizeConfig(config Config) Config {
	if config.ScanInterval <= 0 {
		config.ScanInterval = DefaultScanInterval
	}
	if len(config.RestartBackoff) == 0 {
		config.RestartBackoff = append([]time.Duration(nil), DefaultRestartBackoff...)
	} else {
		config.RestartBackoff = append([]time.Duration(nil), config.RestartBackoff...)
	}
	if config.MaxRestartBackoff <= 0 {
		config.MaxRestartBackoff = DefaultMaxRestartBackoff
	}
	if config.Clock == nil {
		config.Clock = time.Now
	}
	if config.Jitter == nil {
		var jitterMu sync.Mutex
		random := rand.New(rand.NewSource(time.Now().UnixNano()))
		config.Jitter = func(delay time.Duration) time.Duration {
			if delay <= 0 {
				return 0
			}
			jitterMu.Lock()
			factor := 0.9 + random.Float64()*0.2
			jitterMu.Unlock()
			return time.Duration(float64(delay) * factor)
		}
	}
	return config
}

func (s *Supervisor) Start() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return
	}
	if s.stopping {
		s.restartPending = true
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.started = true
	s.stop = cancel
	s.done = make(chan struct{})
	done := s.done
	s.mu.Unlock()
	go s.supervise(ctx, done)
}

func (s *Supervisor) supervise(ctx context.Context, done chan struct{}) {
	defer close(done)
	ticker := time.NewTicker(s.config.ScanInterval)
	defer ticker.Stop()
	_ = s.Reconcile(ctx)
	for {
		timer, timerC := s.nextRestartTimer()
		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return
		case <-ticker.C:
		case <-s.reconcileWake:
		case <-timerC:
		}
		if timer != nil {
			timer.Stop()
		}
		_ = s.Reconcile(ctx)
	}
}

func (s *Supervisor) nextRestartTimer() (*time.Timer, <-chan time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.started {
		return nil, nil
	}
	var earliest time.Time
	for _, entry := range s.loops {
		if entry == nil || entry.running || entry.nextRestartAt.IsZero() {
			continue
		}
		if earliest.IsZero() || entry.nextRestartAt.Before(earliest) {
			earliest = entry.nextRestartAt
		}
	}
	if earliest.IsZero() {
		return nil, nil
	}
	delay := earliest.Sub(s.now())
	if delay < 0 {
		delay = 0
	}
	timer := time.NewTimer(delay)
	return timer, timer.C
}

func (s *Supervisor) Reconcile(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.Start()
	s.reconcileMu.Lock()
	defer s.reconcileMu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	if s.hooks.DesiredTeams == nil || s.hooks.RunLoop == nil {
		return errors.New("team loop supervisor hooks are not configured")
	}
	teamIDs, err := s.hooks.DesiredTeams(ctx)
	if err != nil {
		s.emit(Event{Type: "team.orchestrator.sync_failed", Reason: "desired_team_scan_failed", Error: err.Error()})
		return err
	}
	desired := make(map[string]struct{}, len(teamIDs))
	for _, teamID := range teamIDs {
		teamID = strings.TrimSpace(teamID)
		if teamID == "" {
			continue
		}
		if s.hooks.OwnerAllowed != nil && !s.hooks.OwnerAllowed(ctx, teamID) {
			continue
		}
		desired[teamID] = struct{}{}
	}

	type stopItem struct {
		teamID     string
		generation uint64
		cancel     context.CancelFunc
	}
	type startItem struct {
		teamID       string
		generation   uint64
		restartCount int
		ctx          context.Context
		wake         <-chan struct{}
	}
	stops := make([]stopItem, 0)
	starts := make([]startItem, 0)
	now := s.now()

	s.mu.Lock()
	if !s.started || s.stopping {
		s.mu.Unlock()
		return context.Canceled
	}
	for teamID, entry := range s.loops {
		if _, ok := desired[teamID]; ok {
			continue
		}
		if entry != nil && entry.running && entry.cancel != nil {
			stops = append(stops, stopItem{teamID: teamID, generation: entry.generation, cancel: entry.cancel})
		}
		delete(s.loops, teamID)
	}
	for teamID := range desired {
		entry := s.loops[teamID]
		if entry != nil && entry.running {
			signal(entry.wake)
			continue
		}
		if entry != nil && !entry.nextRestartAt.IsZero() && now.Before(entry.nextRestartAt) {
			continue
		}
		if entry == nil {
			entry = &loopEntry{}
			s.loops[teamID] = entry
		}
		runCtx, cancel := context.WithCancel(context.Background())
		wake := make(chan struct{}, 1)
		s.nextGeneration++
		entry.cancel = cancel
		entry.wake = wake
		entry.generation = s.nextGeneration
		entry.running = true
		entry.lastExitError = ""
		entry.nextRestartAt = time.Time{}
		starts = append(starts, startItem{
			teamID:       teamID,
			generation:   entry.generation,
			restartCount: entry.restartCount,
			ctx:          runCtx,
			wake:         wake,
		})
	}
	s.mu.Unlock()

	for _, item := range stops {
		item.cancel()
		s.emit(Event{
			Type:       "team.orchestrator.loop.stopped",
			TeamID:     item.teamID,
			Generation: item.generation,
			Reason:     "team_not_active",
		})
	}
	for _, item := range starts {
		if item.restartCount > 0 {
			s.emit(Event{
				Type:         "team.orchestrator.restarted",
				TeamID:       item.teamID,
				Generation:   item.generation,
				RestartCount: item.restartCount,
				Reason:       "restart_backoff_elapsed",
			})
		}
		s.emit(Event{
			Type:         "team.orchestrator.loop.started",
			TeamID:       item.teamID,
			Generation:   item.generation,
			RestartCount: item.restartCount,
			Reason:       firstStartReason(item.restartCount),
		})
		s.runWG.Add(1)
		go s.runLoop(item.ctx, item.teamID, item.generation, item.wake)
	}
	return nil
}

func firstStartReason(restartCount int) string {
	if restartCount > 0 {
		return "restart_backoff_elapsed"
	}
	return "sync_missing_loop"
}

func (s *Supervisor) runLoop(ctx context.Context, teamID string, generation uint64, wake <-chan struct{}) {
	defer s.runWG.Done()
	err := s.hooks.RunLoop(ctx, teamID, wake)
	settled := err == nil && ctx.Err() == nil
	if settled && s.hooks.OnSettled != nil {
		s.hooks.OnSettled(teamID)
	}
	s.mu.Lock()
	entry := s.loops[teamID]
	if entry == nil || entry.generation != generation || !entry.running {
		s.mu.Unlock()
		return
	}
	entry.running = false
	entry.cancel = nil
	entry.wake = nil
	if settled {
		delete(s.loops, teamID)
		restartCount := entry.restartCount
		s.mu.Unlock()
		s.emit(Event{
			Type:         "team.orchestrator.loop.stopped",
			TeamID:       teamID,
			Generation:   generation,
			RestartCount: restartCount,
			Reason:       "loop_settled",
		})
		s.Wake()
		return
	}
	entry.restartCount++
	entry.lastExitError = ""
	if err != nil && ctx.Err() == nil {
		entry.lastExitError = err.Error()
	}
	delay := s.restartDelay(entry.restartCount)
	entry.nextRestartAt = s.now().Add(delay)
	restartCount := entry.restartCount
	nextRestartAt := entry.nextRestartAt
	lastExitError := entry.lastExitError
	s.mu.Unlock()

	if err != nil && ctx.Err() == nil {
		s.emit(Event{
			Type:          "team.orchestrator.loop.error",
			TeamID:        teamID,
			Generation:    generation,
			RestartCount:  restartCount,
			Reason:        "orchestrator_error",
			Error:         err.Error(),
			NextRestartAt: nextRestartAt,
		})
	} else {
		s.emit(Event{
			Type:          "team.orchestrator.loop.stopped",
			TeamID:        teamID,
			Generation:    generation,
			RestartCount:  restartCount,
			Reason:        "loop_settled",
			Error:         lastExitError,
			NextRestartAt: nextRestartAt,
		})
	}
	s.emit(Event{
		Type:          "team.orchestrator.restart_scheduled",
		TeamID:        teamID,
		Generation:    generation,
		RestartCount:  restartCount,
		Reason:        "loop_exit",
		Error:         lastExitError,
		NextRestartAt: nextRestartAt,
	})
	s.Wake()
}

func (s *Supervisor) restartDelay(restartCount int) time.Duration {
	index := restartCount - 1
	if index < 0 {
		index = 0
	}
	var delay time.Duration
	if index < len(s.config.RestartBackoff) {
		delay = s.config.RestartBackoff[index]
	} else {
		delay = s.config.RestartBackoff[len(s.config.RestartBackoff)-1]
		for i := len(s.config.RestartBackoff); i <= index; i++ {
			if delay >= s.config.MaxRestartBackoff/2 {
				delay = s.config.MaxRestartBackoff
				break
			}
			delay *= 2
		}
	}
	if delay > s.config.MaxRestartBackoff {
		delay = s.config.MaxRestartBackoff
	}
	delay = s.config.Jitter(delay)
	if delay < 0 {
		return 0
	}
	if delay > s.config.MaxRestartBackoff {
		return s.config.MaxRestartBackoff
	}
	return delay
}

func (s *Supervisor) Wake() {
	if s == nil {
		return
	}
	select {
	case s.reconcileWake <- struct{}{}:
	default:
	}
}

func (s *Supervisor) Signal(teamID string) {
	teamID = strings.TrimSpace(teamID)
	if s == nil || teamID == "" {
		return
	}
	s.mu.Lock()
	entry := s.loops[teamID]
	if entry != nil && entry.running {
		signal(entry.wake)
	}
	s.mu.Unlock()
}

func signal(wake chan struct{}) {
	if wake == nil {
		return
	}
	select {
	case wake <- struct{}{}:
	default:
	}
}

func (s *Supervisor) StopLoop(teamID, reason string) {
	teamID = strings.TrimSpace(teamID)
	if s == nil || teamID == "" {
		return
	}
	s.reconcileMu.Lock()
	defer s.reconcileMu.Unlock()
	s.mu.Lock()
	entry := s.loops[teamID]
	delete(s.loops, teamID)
	s.mu.Unlock()
	if entry != nil && entry.running && entry.cancel != nil {
		entry.cancel()
		s.emit(Event{
			Type:         "team.orchestrator.loop.stopped",
			TeamID:       teamID,
			Generation:   entry.generation,
			RestartCount: entry.restartCount,
			Reason:       strings.TrimSpace(reason),
		})
	}
}

func (s *Supervisor) Stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.stopping {
		stopDone := s.stopDone
		s.mu.Unlock()
		if stopDone != nil {
			<-stopDone
		}
		return
	}
	s.stopping = true
	stopDone := make(chan struct{})
	s.stopDone = stopDone
	stop := s.stop
	done := s.done
	s.mu.Unlock()

	if stop != nil {
		stop()
	}
	if done != nil {
		<-done
	}

	s.reconcileMu.Lock()
	s.mu.Lock()
	entries := s.loops
	s.loops = make(map[string]*loopEntry)
	s.started = false
	s.stop = nil
	s.done = nil
	s.mu.Unlock()
	s.reconcileMu.Unlock()

	for teamID, entry := range entries {
		if entry == nil || !entry.running || entry.cancel == nil {
			continue
		}
		entry.cancel()
		s.emit(Event{
			Type:         "team.orchestrator.loop.stopped",
			TeamID:       teamID,
			Generation:   entry.generation,
			RestartCount: entry.restartCount,
			Reason:       "host_shutdown",
		})
	}
	s.runWG.Wait()
	s.mu.Lock()
	close(stopDone)
	s.stopping = false
	s.stopDone = nil
	restart := s.restartPending
	s.restartPending = false
	s.mu.Unlock()
	if restart {
		s.Start()
	}
}

func (s *Supervisor) HasLoop(teamID string) bool {
	teamID = strings.TrimSpace(teamID)
	if s == nil || teamID == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.loops[teamID]
	return entry != nil && entry.running
}

func (s *Supervisor) LoopCount() int {
	return s.Snapshot().LoopCount
}

func (s *Supervisor) Snapshot() Snapshot {
	if s == nil {
		return Snapshot{}
	}
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	result := Snapshot{Running: s.started}
	result.Loops = make([]LoopSnapshot, 0, len(s.loops))
	for teamID, entry := range s.loops {
		if entry == nil {
			continue
		}
		item := LoopSnapshot{
			TeamID:        teamID,
			Generation:    entry.generation,
			Running:       entry.running,
			RestartCount:  entry.restartCount,
			LastExitError: entry.lastExitError,
			NextRestartAt: entry.nextRestartAt,
		}
		result.Loops = append(result.Loops, item)
		if entry.running {
			result.LoopCount++
		}
		result.RestartTotal += entry.restartCount
		if !entry.running && !entry.nextRestartAt.IsZero() && entry.nextRestartAt.After(now) {
			result.RestartPending++
		}
		if entry.lastExitError != "" {
			result.DegradedLoops++
		}
	}
	sort.Slice(result.Loops, func(i, j int) bool {
		return result.Loops[i].TeamID < result.Loops[j].TeamID
	})
	return result
}

func (s *Supervisor) now() time.Time {
	return s.config.Clock().UTC()
}

func (s *Supervisor) emit(event Event) {
	if s == nil || s.hooks.OnEvent == nil {
		return
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = s.now()
	}
	s.hooks.OnEvent(event)
}
