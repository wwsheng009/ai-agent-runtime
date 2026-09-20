package agent

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

// blockingChildParent builds a parent agent whose children block inside the LLM
// provider until release is closed. It is the seam that lets these tests observe
// real child-run concurrency (one provider call per child) without an LLM.
func blockingChildParent(t *testing.T, provider *BlockingLLMProvider) *Agent {
	t.Helper()
	parent := &Agent{
		config: &Config{
			Name:         "concurrency-test-parent",
			Provider:     provider.name,
			Model:        "concurrency-test-model",
			MaxSteps:     2,
			SystemPrompt: "Parent system prompt.",
		},
		skillRouter: &skill.Router{},
		skillExec:   &skill.Executor{},
		mcpManager:  &MockMCPManager{},
	}
	llmRuntime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultProvider: provider.name,
		DefaultModel:    "concurrency-test-model",
		MaxRetries:      0,
	})
	require.NoError(t, llmRuntime.RegisterProvider(provider.name, provider))
	parent.llmRuntime = llmRuntime
	return parent
}

func readerTasks(prefix string, count int) []SubagentTask {
	tasks := make([]SubagentTask, 0, count)
	for i := 0; i < count; i++ {
		tasks = append(tasks, SubagentTask{
			ID:       fmt.Sprintf("%s-%d", prefix, i),
			Goal:     "Inspect one slice of the logs.",
			ReadOnly: true,
		})
	}
	return tasks
}

// TestSubagentGlobalLimiterCapsConcurrentBatches is the P1-4/H12 acceptance
// test: with a shared GlobalLimiter=2 and three batches whose per-batch
// MaxConcurrent is 4, the observed peak concurrency across all batches must stay
// ≤ 2 (previously each batch got its own 4-slot window → 4×N).
func TestSubagentGlobalLimiterCapsConcurrentBatches(t *testing.T) {
	release := make(chan struct{})
	provider := &BlockingLLMProvider{
		name:    "concurrency-test-provider",
		release: release,
		entered: make(chan struct{}, 16),
	}
	parent := blockingChildParent(t, provider)

	limiter := NewSubagentConcurrencyLimiter(2)
	require.NotNil(t, limiter)

	const batchCount = 3
	const tasksPerBatch = 4

	done := make(chan error, batchCount)
	for batch := 0; batch < batchCount; batch++ {
		scheduler := NewSubagentScheduler(parent, SubagentSchedulerConfig{
			// Deliberately wider than the global ceiling: only the shared
			// limiter can hold the total at 2.
			MaxConcurrent: tasksPerBatch,
			MaxDepth:      1,
			GlobalLimiter: limiter,
		})
		tasks := readerTasks(fmt.Sprintf("batch-%d-reader", batch), tasksPerBatch)
		go func(s *SubagentScheduler, tasks []SubagentTask) {
			_, err := s.RunChildren(context.Background(), SubagentRunOptions{Depth: 1}, tasks)
			done <- err
		}(scheduler, tasks)
	}

	// Wait until the global ceiling is saturated.
	deadline := time.Now().Add(10 * time.Second)
	for provider.RequestCount() < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("expected 2 concurrent child runs, got %d", provider.RequestCount())
		}
		time.Sleep(5 * time.Millisecond)
	}
	// Give any over-admitting batch the chance to show up, then pin the ceiling.
	time.Sleep(250 * time.Millisecond)
	assert.LessOrEqual(t, provider.MaxActive(), 2, "global limiter must cap concurrent children across batches")
	assert.Equal(t, 2, provider.RequestCount(), "no child beyond the global ceiling may reach the provider")

	close(release)
	for i := 0; i < batchCount; i++ {
		select {
		case err := <-done:
			require.NoError(t, err)
		case <-time.After(30 * time.Second):
			t.Fatal("timed out waiting for subagent batches")
		}
	}
	assert.Equal(t, batchCount*tasksPerBatch, provider.RequestCount())
	assert.LessOrEqual(t, provider.MaxActive(), 2)

	// No slot may leak once every batch finished: a fresh acquire must succeed.
	reacquired := make(chan error, 1)
	go func() { reacquired <- limiter.Acquire(context.Background()) }()
	select {
	case err := <-reacquired:
		require.NoError(t, err)
		limiter.Release()
	case <-time.After(2 * time.Second):
		t.Fatal("global limiter slots leaked after all batches finished")
	}
}

// TestSubagentPerBatchCeilingWithoutGlobalLimiter is the regression guard for
// the historical behavior: with no GlobalLimiter the per-batch MaxConcurrent
// remains the only ceiling.
func TestSubagentPerBatchCeilingWithoutGlobalLimiter(t *testing.T) {
	release := make(chan struct{})
	provider := &BlockingLLMProvider{
		name:    "per-batch-provider",
		release: release,
		entered: make(chan struct{}, 8),
	}
	parent := blockingChildParent(t, provider)

	scheduler := NewSubagentScheduler(parent, SubagentSchedulerConfig{
		MaxConcurrent: 2,
		MaxDepth:      1,
	})
	require.Nil(t, scheduler.config.GlobalLimiter, "an unset limiter must stay nil (unlimited)")

	done := make(chan error, 1)
	go func() {
		_, err := scheduler.RunChildren(context.Background(), SubagentRunOptions{Depth: 1}, readerTasks("reader", 4))
		done <- err
	}()

	deadline := time.Now().Add(10 * time.Second)
	for provider.RequestCount() < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("expected 2 concurrent child runs, got %d", provider.RequestCount())
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(250 * time.Millisecond)
	assert.LessOrEqual(t, provider.MaxActive(), 2, "per-batch ceiling must still apply without a global limiter")
	assert.Equal(t, 2, provider.RequestCount())

	close(release)
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(30 * time.Second):
		t.Fatal("timed out waiting for the subagent batch")
	}
	assert.Equal(t, 4, provider.RequestCount())
	assert.Equal(t, 2, provider.MaxActive())
}

// TestChildSubagentSchedulerInheritsGlobalLimiter pins the depth-N contract: a
// child scheduler built by the child factory must reuse the parent's limiter,
// otherwise every nesting level would silently get its own budget again.
func TestChildSubagentSchedulerInheritsGlobalLimiter(t *testing.T) {
	limiter := NewSubagentConcurrencyLimiter(2)
	parentScheduler := NewSubagentScheduler(nil, SubagentSchedulerConfig{
		MaxConcurrent: 4,
		MaxDepth:      2,
		GlobalLimiter: limiter,
	})

	inherited := childSubagentSchedulerConfig(parentScheduler, SubagentSchedulerConfig{})
	require.Same(t, limiter, inherited.GlobalLimiter)

	childAgent := NewAgent(&Config{Name: "inherited-child"}, nil)
	childScheduler := NewSubagentScheduler(childAgent, inherited)
	require.Same(t, limiter, childScheduler.config.GlobalLimiter)

	// An explicitly supplied child limiter is never overwritten.
	explicit := NewSubagentConcurrencyLimiter(3)
	kept := childSubagentSchedulerConfig(parentScheduler, SubagentSchedulerConfig{GlobalLimiter: explicit})
	require.Same(t, explicit, kept.GlobalLimiter)
}

// TestSubagentGlobalSlotReentrancyAvoidsSelfDeadlock pins the reentrancy rule:
// a wave running inside an execution that already holds the only slot must not
// try to acquire a second one (that would deadlock a saturated limiter).
func TestSubagentGlobalSlotReentrancyAvoidsSelfDeadlock(t *testing.T) {
	limiter := NewSubagentConcurrencyLimiter(1)
	require.NotNil(t, limiter)
	require.NoError(t, limiter.Acquire(context.Background()))
	defer limiter.Release()

	scheduler := NewSubagentScheduler(nil, SubagentSchedulerConfig{
		MaxConcurrent: 1,
		GlobalLimiter: limiter,
	})

	heldCtx := withSubagentGlobalSlot(context.Background(), limiter)
	acquired, err := scheduler.acquireGlobalSlot(heldCtx)
	require.NoError(t, err)
	require.False(t, acquired, "an execution already inside the limiter must not take a second slot")

	// Without the marker the same call has to block while the slot is taken.
	blocked := make(chan struct{})
	go func() {
		_, _ = scheduler.acquireGlobalSlot(context.Background())
		close(blocked)
	}()
	select {
	case <-blocked:
		t.Fatal("expected a saturated limiter to block a fresh acquisition")
	case <-time.After(100 * time.Millisecond):
	}
}

// TestSubagentQueueDepthRejectsOversizedWave pins the opt-in backpressure knob:
// MaxQueueDepth=0 keeps unbounded waiting, a positive value fails the batch
// fast before any child starts, naming the knob and the limits.
func TestSubagentQueueDepthRejectsOversizedWave(t *testing.T) {
	parent := NewAgent(&Config{Name: "queue-depth-parent"}, nil)

	unbounded := NewSubagentScheduler(parent, SubagentSchedulerConfig{MaxConcurrent: 1, MaxDepth: 1})
	require.NoError(t, unbounded.checkQueueDepth(3), "default 0 must not limit the queue")

	scheduler := NewSubagentScheduler(parent, SubagentSchedulerConfig{
		MaxConcurrent: 1,
		MaxDepth:      1,
		MaxQueueDepth: 1,
	})
	require.NoError(t, scheduler.checkQueueDepth(2), "maxConcurrent+queueDepth tasks are admissible")

	err := scheduler.checkQueueDepth(3)
	require.Error(t, err)
	require.Contains(t, err.Error(), "agents.maxConcurrentQueueDepth=1")
	require.Contains(t, err.Error(), "agents.maxConcurrent=1")

	// The rejection happens at wave admission, before any child is created.
	_, err = scheduler.RunChildren(context.Background(), SubagentRunOptions{Depth: 1}, readerTasks("reader", 3))
	require.Error(t, err)
	require.Contains(t, err.Error(), "agents.maxConcurrentQueueDepth=1")
}

// TestSubagentQueueTimeoutFailsWaitingTask pins the opt-in queue timeout: a task
// that cannot get a per-batch slot within the configured window fails with an
// actionable error instead of queueing forever.
func TestSubagentQueueTimeoutFailsWaitingTask(t *testing.T) {
	scheduler := NewSubagentScheduler(nil, SubagentSchedulerConfig{
		MaxConcurrent: 1,
		QueueTimeout:  25 * time.Millisecond,
	})
	sem := make(chan struct{}, 1)
	sem <- struct{}{} // the only slot is already taken

	err := scheduler.acquireBatchSlot(context.Background(), sem)
	require.Error(t, err)
	require.Contains(t, err.Error(), "agents.maxConcurrentQueueTimeoutMs=25")

	// Cancellation is honored on the same path.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, scheduler.acquireBatchSlot(ctx, sem), context.Canceled)
}

// TestSubagentConcurrencyLimiterNilMeansUnlimited pins the "absent config
// changes nothing" contract of the limiter itself.
func TestSubagentConcurrencyLimiterNilMeansUnlimited(t *testing.T) {
	var limiter *SubagentConcurrencyLimiter
	require.Equal(t, 0, limiter.Limit())
	require.NoError(t, limiter.Acquire(context.Background()))
	limiter.Release() // no-op, must not panic

	require.Nil(t, NewSubagentConcurrencyLimiter(0))
	require.Nil(t, NewSubagentConcurrencyLimiter(-1))
	require.Equal(t, 2, NewSubagentConcurrencyLimiter(2).Limit())
}
