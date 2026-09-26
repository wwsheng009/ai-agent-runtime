package background

import (
	"context"
	"testing"
	"time"
)

// newObservationTestJob injects a managed job directly: the flag only needs the
// manager's in-memory state, not a real process.
func newObservationTestJob(t *testing.T, manager *Manager, jobID string) *managedJob {
	t.Helper()
	job := &managedJob{
		info: Job{
			ID:        jobID,
			Status:    StatusRunning,
			Command:   "sleep 1",
			SessionID: "sess-1",
			CreatedAt: time.Now().UTC(),
		},
		output: newOutputBuffer(1024),
	}
	manager.mu.Lock()
	manager.jobs[jobID] = job
	manager.mu.Unlock()
	t.Cleanup(func() {
		manager.mu.Lock()
		delete(manager.jobs, jobID)
		manager.mu.Unlock()
	})
	return job
}

// TestTerminalEventMarksObservedWhileWaitInFlight pins the ordering contract:
// when a terminal event is emitted while a task_output wait is registered, the
// job is flagged before the host handler sees the event, so the projection can
// skip the redundant wake.
func TestTerminalEventMarksObservedWhileWaitInFlight(t *testing.T) {
	manager := NewManager(Config{})
	var observedAtHandler []bool
	manager.eventHandler = func(event JobEvent) {
		job, err := manager.GetJob(context.Background(), event.JobID)
		observedAtHandler = append(observedAtHandler, err == nil && TerminalObserved(job))
	}
	newObservationTestJob(t, manager, "job-observed")

	manager.BeginOutputWait("job-observed")
	manager.appendJobEvent(context.Background(), "job-observed", string(StatusCompleted), map[string]interface{}{
		"status": StatusCompleted,
	})

	job, err := manager.GetJob(context.Background(), "job-observed")
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if !TerminalObserved(job) {
		t.Fatal("terminal transition during an in-flight wait must be flagged as observed")
	}
	if len(observedAtHandler) != 1 || !observedAtHandler[0] {
		t.Fatalf("the flag must be visible to the event handler, got %v", observedAtHandler)
	}
}

func TestTerminalEventWithoutWaitStaysUnobserved(t *testing.T) {
	manager := NewManager(Config{})
	newObservationTestJob(t, manager, "job-plain")

	manager.appendJobEvent(context.Background(), "job-plain", string(StatusFailed), map[string]interface{}{
		"status": StatusFailed,
	})

	job, err := manager.GetJob(context.Background(), "job-plain")
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if TerminalObserved(job) {
		t.Fatal("a terminal transition nobody is waiting on must stay unobserved")
	}
}

func TestEndOutputWaitReleasesTheRegistration(t *testing.T) {
	manager := NewManager(Config{})
	newObservationTestJob(t, manager, "job-released")

	manager.BeginOutputWait("job-released")
	manager.EndOutputWait("job-released")
	manager.appendJobEvent(context.Background(), "job-released", string(StatusTimedOut), nil)

	job, err := manager.GetJob(context.Background(), "job-released")
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if TerminalObserved(job) {
		t.Fatal("a released wait must not suppress the terminal wake")
	}
}

func TestNonTerminalEventsNeverMarkObserved(t *testing.T) {
	manager := NewManager(Config{})
	newObservationTestJob(t, manager, "job-running")

	manager.BeginOutputWait("job-running")
	manager.appendJobEvent(context.Background(), "job-running", "output", map[string]interface{}{"chunk": "still working"})
	manager.appendJobEvent(context.Background(), "job-running", string(StatusRunning), nil)

	job, err := manager.GetJob(context.Background(), "job-running")
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if TerminalObserved(job) {
		t.Fatal("output/running events must not flag the job as observed")
	}
}

func TestOutputWaitRegistryBoundsAndAge(t *testing.T) {
	registry := newOutputWaitRegistry()
	now := time.Now().UTC()

	registry.begin("job-1", now)
	if !registry.active("job-1", now.Add(time.Second)) {
		t.Fatal("a fresh registration must be active")
	}
	// 泄漏保护：超过 TTL 的登记按不存在处理，不能永久抑制合法唤醒。
	if registry.active("job-1", now.Add(outputWaitMaxAge+time.Second)) {
		t.Fatal("a stale registration must not suppress a wake")
	}
	if registry.active("job-1", now) {
		t.Fatal("a stale registration must be forgotten, not resurrected")
	}

	// 计数语义：两个并发 waiter，释放一个仍活跃；全部释放后不活跃。
	registry.begin("job-2", now)
	registry.begin("job-2", now)
	registry.end("job-2", now)
	if !registry.active("job-2", now) {
		t.Fatal("one released waiter must keep the registration active")
	}
	registry.end("job-2", now)
	if registry.active("job-2", now) {
		t.Fatal("all waiters released must end the registration")
	}
	registry.end("job-2", now) // 多余的 end 不得 panic 或复活登记
	if registry.active("job-2", now) {
		t.Fatal("an extra release must stay a no-op")
	}
}

// TestOutputWaitHelpersAreNilSafe keeps literal Manager values (tests, embedded
// hosts) working: registration must not be required for the terminal path.
func TestOutputWaitHelpersAreNilSafe(t *testing.T) {
	var nilManager *Manager
	nilManager.BeginOutputWait("job-1")
	nilManager.EndOutputWait("job-1")
	if nilManager.outputWaitActive("job-1") {
		t.Fatal("nil manager must not report active waiters")
	}

	bare := &Manager{jobs: make(map[string]*managedJob)}
	bare.BeginOutputWait("job-1")
	bare.EndOutputWait("job-1")
	if TerminalObserved(nil) {
		t.Fatal("nil job must not report observed")
	}
}
