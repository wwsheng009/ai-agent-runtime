package teamsupervisor

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testConfig() Config {
	return Config{
		ScanInterval:      10 * time.Millisecond,
		RestartBackoff:    []time.Duration{15 * time.Millisecond, 25 * time.Millisecond},
		MaxRestartBackoff: 50 * time.Millisecond,
		Jitter:            func(delay time.Duration) time.Duration { return delay },
	}
}

func waitFor(t *testing.T, timeout time.Duration, condition func() bool, message string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%s", message)
}

func TestSupervisorStartsMissingActiveLoopWithoutExternalReconcile(t *testing.T) {
	started := make(chan struct{}, 1)
	supervisor := New(testConfig(), Hooks{
		DesiredTeams: func(context.Context) ([]string, error) {
			return []string{"team-active"}, nil
		},
		RunLoop: func(ctx context.Context, _ string, _ <-chan struct{}) error {
			select {
			case started <- struct{}{}:
			default:
			}
			<-ctx.Done()
			return ctx.Err()
		},
	})
	t.Cleanup(supervisor.Stop)

	supervisor.Start()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("supervisor did not start the missing active team loop")
	}
	if !supervisor.HasLoop("team-active") {
		t.Fatal("expected active team loop to be registered")
	}
}

func TestSupervisorRestartsUnexpectedExitWithBackoffAndNoDuplicateLoop(t *testing.T) {
	var attempts atomic.Int32
	var active atomic.Int32
	var maxActive atomic.Int32
	var eventsMu sync.Mutex
	events := make([]Event, 0)
	secondStarted := make(chan struct{})

	supervisor := New(testConfig(), Hooks{
		DesiredTeams: func(context.Context) ([]string, error) {
			return []string{"team-restart"}, nil
		},
		RunLoop: func(ctx context.Context, _ string, _ <-chan struct{}) error {
			current := active.Add(1)
			defer active.Add(-1)
			for {
				observed := maxActive.Load()
				if current <= observed || maxActive.CompareAndSwap(observed, current) {
					break
				}
			}
			attempt := attempts.Add(1)
			if attempt == 1 {
				return errors.New("injected orchestrator failure")
			}
			if attempt == 2 {
				close(secondStarted)
			}
			<-ctx.Done()
			return ctx.Err()
		},
		OnEvent: func(event Event) {
			eventsMu.Lock()
			events = append(events, event)
			eventsMu.Unlock()
		},
	})
	t.Cleanup(supervisor.Stop)

	supervisor.Start()
	select {
	case <-secondStarted:
	case <-time.After(time.Second):
		t.Fatal("supervisor did not restart the failed loop")
	}
	if attempts.Load() < 2 {
		t.Fatalf("expected at least two loop attempts, got %d", attempts.Load())
	}
	if maxActive.Load() != 1 {
		t.Fatalf("expected at most one active loop, got %d", maxActive.Load())
	}

	snapshot := supervisor.Snapshot()
	if snapshot.RestartTotal != 1 || snapshot.LoopCount != 1 {
		t.Fatalf("unexpected supervisor snapshot: %+v", snapshot)
	}
	if snapshot.DegradedLoops != 0 {
		t.Fatalf("successful restart remained degraded: %+v", snapshot)
	}
	eventsMu.Lock()
	defer eventsMu.Unlock()
	var sawError, sawScheduled, sawRestarted bool
	for _, event := range events {
		switch event.Type {
		case "team.orchestrator.loop.error":
			sawError = event.Error == "injected orchestrator failure" && !event.NextRestartAt.IsZero()
		case "team.orchestrator.restart_scheduled":
			sawScheduled = event.RestartCount == 1
		case "team.orchestrator.restarted":
			sawRestarted = event.RestartCount == 1
		}
	}
	if !sawError || !sawScheduled || !sawRestarted {
		t.Fatalf("missing restart lifecycle events: %+v", events)
	}
}

func TestSupervisorStopsLoopWhenTeamIsNoLongerActive(t *testing.T) {
	var active atomic.Bool
	active.Store(true)
	loopExited := make(chan struct{})
	supervisor := New(testConfig(), Hooks{
		DesiredTeams: func(context.Context) ([]string, error) {
			if active.Load() {
				return []string{"team-paused"}, nil
			}
			return nil, nil
		},
		RunLoop: func(ctx context.Context, _ string, _ <-chan struct{}) error {
			<-ctx.Done()
			close(loopExited)
			return ctx.Err()
		},
	})
	t.Cleanup(supervisor.Stop)

	supervisor.Start()
	waitFor(t, time.Second, func() bool { return supervisor.HasLoop("team-paused") }, "active team loop did not start")
	active.Store(false)
	supervisor.Wake()
	select {
	case <-loopExited:
	case <-time.After(time.Second):
		t.Fatal("paused team loop was not cancelled")
	}
	if supervisor.HasLoop("team-paused") {
		t.Fatal("paused team loop remained registered")
	}
	time.Sleep(40 * time.Millisecond)
	if supervisor.HasLoop("team-paused") {
		t.Fatal("paused team loop was restarted")
	}
}

func TestSupervisorSettledLoopDoesNotScheduleRestart(t *testing.T) {
	var active atomic.Bool
	active.Store(true)
	var attempts atomic.Int32
	var eventsMu sync.Mutex
	events := make([]Event, 0)
	settled := make(chan struct{})
	supervisor := New(testConfig(), Hooks{
		DesiredTeams: func(context.Context) ([]string, error) {
			if active.Load() {
				return []string{"team-settled"}, nil
			}
			return nil, nil
		},
		RunLoop: func(context.Context, string, <-chan struct{}) error {
			attempts.Add(1)
			return nil
		},
		OnSettled: func(string) {
			active.Store(false)
			close(settled)
		},
		OnEvent: func(event Event) {
			eventsMu.Lock()
			events = append(events, event)
			eventsMu.Unlock()
		},
	})
	t.Cleanup(supervisor.Stop)

	supervisor.Start()
	select {
	case <-settled:
	case <-time.After(time.Second):
		t.Fatal("team loop did not settle")
	}
	waitFor(t, time.Second, func() bool {
		return !supervisor.HasLoop("team-settled")
	}, "settled loop remained registered")
	time.Sleep(50 * time.Millisecond)
	if got := attempts.Load(); got != 1 {
		t.Fatalf("settled loop restarted unexpectedly: %d attempts", got)
	}
	if snapshot := supervisor.Snapshot(); snapshot.RestartTotal != 0 || snapshot.RestartPending != 0 {
		t.Fatalf("settled loop retained restart state: %+v", snapshot)
	}
	eventsMu.Lock()
	defer eventsMu.Unlock()
	for _, event := range events {
		if event.Type == "team.orchestrator.restart_scheduled" {
			t.Fatalf("settled loop emitted restart schedule: %+v", events)
		}
	}
}

func TestSupervisorStopWaitsForTickerAndLoopShutdown(t *testing.T) {
	loopExited := make(chan struct{})
	supervisor := New(testConfig(), Hooks{
		DesiredTeams: func(context.Context) ([]string, error) { return []string{"team-stop"}, nil },
		RunLoop: func(ctx context.Context, _ string, _ <-chan struct{}) error {
			<-ctx.Done()
			close(loopExited)
			return ctx.Err()
		},
	})
	supervisor.Start()
	waitFor(t, time.Second, func() bool { return supervisor.HasLoop("team-stop") }, "team loop did not start")
	supervisor.Stop()
	select {
	case <-loopExited:
	default:
		t.Fatal("Stop returned before loop goroutine exited")
	}
	if snapshot := supervisor.Snapshot(); snapshot.Running || snapshot.LoopCount != 0 {
		t.Fatalf("supervisor remained active after Stop: %+v", snapshot)
	}
}

func TestSupervisorConcurrentStopWaitsForCompleteLoopShutdown(t *testing.T) {
	cancelObserved := make(chan struct{})
	releaseLoop := make(chan struct{})
	supervisor := New(testConfig(), Hooks{
		DesiredTeams: func(context.Context) ([]string, error) { return []string{"team-stop-concurrent"}, nil },
		RunLoop: func(ctx context.Context, _ string, _ <-chan struct{}) error {
			<-ctx.Done()
			close(cancelObserved)
			<-releaseLoop
			return ctx.Err()
		},
	})
	supervisor.Start()
	waitFor(t, time.Second, func() bool {
		return supervisor.HasLoop("team-stop-concurrent")
	}, "team loop did not start")

	firstStopped := make(chan struct{})
	go func() {
		supervisor.Stop()
		close(firstStopped)
	}()
	select {
	case <-cancelObserved:
	case <-time.After(time.Second):
		t.Fatal("first Stop did not cancel the loop")
	}

	secondStopped := make(chan struct{})
	go func() {
		supervisor.Stop()
		close(secondStopped)
	}()
	select {
	case <-secondStopped:
		t.Fatal("concurrent Stop returned before loop shutdown completed")
	case <-time.After(30 * time.Millisecond):
	}

	close(releaseLoop)
	for name, stopped := range map[string]<-chan struct{}{
		"first":  firstStopped,
		"second": secondStopped,
	} {
		select {
		case <-stopped:
		case <-time.After(time.Second):
			t.Fatalf("%s Stop did not return after loop shutdown", name)
		}
	}
}

func TestSupervisorCanRestartAfterStop(t *testing.T) {
	var attempts atomic.Int32
	started := make(chan int32, 2)
	supervisor := New(testConfig(), Hooks{
		DesiredTeams: func(context.Context) ([]string, error) { return []string{"team-reload"}, nil },
		RunLoop: func(ctx context.Context, _ string, _ <-chan struct{}) error {
			attempt := attempts.Add(1)
			started <- attempt
			<-ctx.Done()
			return ctx.Err()
		},
	})

	supervisor.Start()
	select {
	case attempt := <-started:
		if attempt != 1 {
			t.Fatalf("unexpected first attempt: %d", attempt)
		}
	case <-time.After(time.Second):
		t.Fatal("initial supervisor loop did not start")
	}
	supervisor.Stop()

	requireNoLoop := supervisor.Snapshot()
	if requireNoLoop.Running || requireNoLoop.LoopCount != 0 {
		t.Fatalf("supervisor did not fully stop: %+v", requireNoLoop)
	}
	if err := supervisor.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile after Stop failed: %v", err)
	}
	select {
	case attempt := <-started:
		if attempt != 2 {
			t.Fatalf("unexpected restarted attempt: %d", attempt)
		}
	case <-time.After(time.Second):
		t.Fatal("supervisor did not restart after Stop")
	}
	supervisor.Stop()
}

func TestSupervisorStartDuringStopRestartsAfterShutdown(t *testing.T) {
	var attempts atomic.Int32
	cancelObserved := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondStarted := make(chan struct{})
	supervisor := New(testConfig(), Hooks{
		DesiredTeams: func(context.Context) ([]string, error) { return []string{"team-restart-after-stop"}, nil },
		RunLoop: func(ctx context.Context, _ string, _ <-chan struct{}) error {
			switch attempts.Add(1) {
			case 1:
				<-ctx.Done()
				close(cancelObserved)
				<-releaseFirst
				return ctx.Err()
			case 2:
				close(secondStarted)
				<-ctx.Done()
				return ctx.Err()
			default:
				return errors.New("unexpected extra loop attempt")
			}
		},
	})

	supervisor.Start()
	waitFor(t, time.Second, func() bool {
		return supervisor.HasLoop("team-restart-after-stop")
	}, "initial team loop did not start")
	stopped := make(chan struct{})
	go func() {
		supervisor.Stop()
		close(stopped)
	}()
	select {
	case <-cancelObserved:
	case <-time.After(time.Second):
		t.Fatal("Stop did not cancel the initial loop")
	}
	supervisor.Start()
	close(releaseFirst)
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Stop did not complete")
	}
	select {
	case <-secondStarted:
	case <-time.After(time.Second):
		t.Fatal("Start request during Stop was not honored")
	}
	supervisor.Stop()
}

func TestSupervisorConcurrentReconcileDoesNotStartDuplicateLoop(t *testing.T) {
	var attempts atomic.Int32
	var active atomic.Int32
	var maxActive atomic.Int32
	started := make(chan struct{})
	supervisor := New(Config{
		ScanInterval:      time.Hour,
		RestartBackoff:    []time.Duration{time.Second},
		MaxRestartBackoff: time.Second,
		Jitter:            func(delay time.Duration) time.Duration { return delay },
	}, Hooks{
		DesiredTeams: func(context.Context) ([]string, error) { return []string{"team-concurrent"}, nil },
		RunLoop: func(ctx context.Context, _ string, _ <-chan struct{}) error {
			attempts.Add(1)
			current := active.Add(1)
			defer active.Add(-1)
			for {
				observed := maxActive.Load()
				if current <= observed || maxActive.CompareAndSwap(observed, current) {
					break
				}
			}
			select {
			case <-started:
			default:
				close(started)
			}
			<-ctx.Done()
			return ctx.Err()
		},
	})
	t.Cleanup(supervisor.Stop)

	var reconcileWG sync.WaitGroup
	for i := 0; i < 20; i++ {
		reconcileWG.Add(1)
		go func() {
			defer reconcileWG.Done()
			if err := supervisor.Reconcile(context.Background()); err != nil {
				t.Errorf("concurrent reconcile failed: %v", err)
			}
		}()
	}
	reconcileWG.Wait()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("team loop did not start")
	}
	time.Sleep(20 * time.Millisecond)
	if got := attempts.Load(); got != 1 {
		t.Fatalf("expected one loop attempt, got %d", got)
	}
	if got := maxActive.Load(); got != 1 {
		t.Fatalf("expected at most one active loop, got %d", got)
	}
}

func TestSupervisorDesiredTeamScanFailureEmitsEventAndRecovers(t *testing.T) {
	var failScan atomic.Bool
	failScan.Store(true)
	var eventsMu sync.Mutex
	events := make([]Event, 0)
	started := make(chan struct{})
	supervisor := New(testConfig(), Hooks{
		DesiredTeams: func(context.Context) ([]string, error) {
			if failScan.Load() {
				return nil, errors.New("injected scan failure")
			}
			return []string{"team-recovered"}, nil
		},
		RunLoop: func(ctx context.Context, _ string, _ <-chan struct{}) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		},
		OnEvent: func(event Event) {
			eventsMu.Lock()
			events = append(events, event)
			eventsMu.Unlock()
		},
	})
	t.Cleanup(supervisor.Stop)

	supervisor.Start()
	waitFor(t, time.Second, func() bool {
		eventsMu.Lock()
		defer eventsMu.Unlock()
		for _, event := range events {
			if event.Type == "team.orchestrator.sync_failed" &&
				event.Reason == "desired_team_scan_failed" &&
				event.Error == "injected scan failure" {
				return true
			}
		}
		return false
	}, "supervisor did not emit scan failure event")

	failScan.Store(false)
	supervisor.Wake()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("supervisor did not recover after desired-team scan failure")
	}
}
