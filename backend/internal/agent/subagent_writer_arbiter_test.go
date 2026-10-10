package agent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

func TestSubagentWriterArbiterNilReceiversAreNoOps(t *testing.T) {
	var arbiter *SubagentWriterArbiter
	release, err := arbiter.Acquire(context.Background(), "writer", []string{"a"})
	require.NoError(t, err)
	require.NotNil(t, release)
	release()
	release() // idempotent
	assert.Zero(t, arbiter.InFlight())
	assert.Nil(t, arbiter.ActiveOwners())
}

func TestSubagentWriterArbiterUndeclaredScopeIsExclusive(t *testing.T) {
	arbiter := NewSubagentWriterArbiter()
	release, err := arbiter.Acquire(context.Background(), "first", nil)
	require.NoError(t, err)
	assert.Equal(t, 1, arbiter.InFlight())
	require.Equal(t, []string{"first (whole workspace)"}, arbiter.ActiveOwners())

	waitCtx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	_, waitErr := arbiter.Acquire(waitCtx, "second", []string{"frontend"})
	require.Error(t, waitErr)
	assert.Contains(t, waitErr.Error(), "single-writer fence wait cancelled")
	assert.Contains(t, waitErr.Error(), "next_action=retry_later_or_split_writers")
	assert.Contains(t, waitErr.Error(), "first (whole workspace)")
	assert.Equal(t, "single_writer", classifySubagentDeniedPolicy(waitErr.Error()))

	release()
	assert.Zero(t, arbiter.InFlight())

	secondRelease, err := arbiter.Acquire(context.Background(), "second", []string{"frontend"})
	require.NoError(t, err)
	secondRelease()
}

func TestSubagentWriterArbiterDisjointScopesRunConcurrently(t *testing.T) {
	arbiter := NewSubagentWriterArbiter()
	backendRelease, err := arbiter.Acquire(context.Background(), "backend-writer", []string{"backend/internal/agent"})
	require.NoError(t, err)
	frontendRelease, err := arbiter.Acquire(context.Background(), "frontend-writer", []string{"frontend/src"})
	require.NoError(t, err)
	assert.Equal(t, 2, arbiter.InFlight())

	backendRelease()
	frontendRelease()
	assert.Zero(t, arbiter.InFlight())
}

func TestSubagentWriterArbiterOverlapIsPathBoundaryAware(t *testing.T) {
	arbiter := NewSubagentWriterArbiter()
	release, err := arbiter.Acquire(context.Background(), "holder", []string{"backend/internal"})
	require.NoError(t, err)

	// A sibling directory sharing a name prefix is not inside the claim; a
	// nested file and a parent directory both are.
	assert.False(t, subagentWritePathOverlap("backend/internal", "backend/internal2"))
	assert.True(t, subagentWritePathOverlap("backend/internal", "backend/internal/agent/scheduler.go"))
	assert.True(t, subagentWritePathOverlap("backend", "backend/internal"))

	siblingRelease, err := arbiter.Acquire(context.Background(), "sibling", []string{"backend/internal2"})
	require.NoError(t, err)

	waitCtx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	_, waitErr := arbiter.Acquire(waitCtx, "nested", []string{"backend/internal/agent/scheduler.go"})
	require.Error(t, waitErr)

	siblingRelease()
	release()
	assert.Zero(t, arbiter.InFlight())
}

func TestNormalizeSubagentWritePaths(t *testing.T) {
	require.Nil(t, NormalizeSubagentWritePaths(nil))
	require.Nil(t, NormalizeSubagentWritePaths([]string{"  ", ".", "./", "/"}))
	assert.Equal(t,
		[]string{"a/b", "c"},
		NormalizeSubagentWritePaths([]string{"./a//b", "a\\b", "c", " c "}))
}

func TestSubagentWriterArbiterReleaseWakesWaiter(t *testing.T) {
	arbiter := NewSubagentWriterArbiter()
	release, err := arbiter.Acquire(context.Background(), "holder", []string{"pkg"})
	require.NoError(t, err)

	acquired := make(chan func(), 1)
	go func() {
		releaseWaiter, acquireErr := arbiter.Acquire(context.Background(), "waiter", []string{"pkg/file.go"})
		if acquireErr != nil {
			acquired <- nil
			return
		}
		acquired <- releaseWaiter
	}()

	select {
	case <-acquired:
		t.Fatal("waiter acquired a conflicting claim before the holder released")
	case <-time.After(30 * time.Millisecond):
	}

	release()

	select {
	case releaseWaiter := <-acquired:
		require.NotNil(t, releaseWaiter, "waiter must be admitted after the holder releases")
		releaseWaiter()
	case <-time.After(2 * time.Second):
		t.Fatal("waiter was not woken by release")
	}
	assert.Zero(t, arbiter.InFlight())
}

// Invariant: conflicting scopes are never held at the same time, while equal
// scopes serialize and disjoint scopes may run in parallel.
func TestSubagentWriterArbiterSerializesConflictingClaimants(t *testing.T) {
	arbiter := NewSubagentWriterArbiter()
	var mu sync.Mutex
	active := map[string]int{}
	violations := 0
	scopes := []string{"a", "b", "c"}

	claim := func(scope string) {
		release, err := arbiter.Acquire(context.Background(), scope, []string{scope})
		if err != nil {
			return
		}
		mu.Lock()
		for other := range active {
			if subagentWritePathOverlap(scope, other) {
				violations++
			}
		}
		active[scope]++
		mu.Unlock()

		time.Sleep(time.Millisecond)

		mu.Lock()
		active[scope]--
		if active[scope] == 0 {
			delete(active, scope)
		}
		mu.Unlock()
		release()
	}

	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			claim(scopes[i%len(scopes)])
		}(i)
	}
	wg.Wait()

	assert.Zero(t, violations, "conflicting writer claims must never overlap")
	assert.Zero(t, arbiter.InFlight())
}

// End-to-end: a multi-writer batch is admitted, writers run one at a time
// (batch serialization), the process-wide fence is released after each one, and
// undeclared writers carry the serialization advisory into the batch receipt.
func TestSubagentScheduler_MultipleWritersRunSeriallyThroughFence(t *testing.T) {
	agent := &Agent{
		config: &Config{
			Name:         "test-agent",
			Provider:     "parent-provider",
			Model:        "parent-model",
			MaxSteps:     3,
			SystemPrompt: "Parent system prompt.",
		},
		skillRouter: &skill.Router{},
		skillExec:   &skill.Executor{},
		mcpManager:  &MockMCPManager{},
	}
	llmRuntime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultProvider: "parent-provider",
		DefaultModel:    "parent-model",
		MaxRetries:      0,
	})
	provider := &SequenceLLMProvider{
		name: "parent-provider",
		responses: []*llm.LLMResponse{
			{Content: "Writer one summary.", Model: "parent-model"},
			{Content: "Writer two summary.", Model: "parent-model"},
		},
	}
	require.NoError(t, llmRuntime.RegisterProvider("parent-provider", provider))
	agent.llmRuntime = llmRuntime

	arbiter := NewSubagentWriterArbiter()
	globalLimiter := NewSubagentConcurrencyLimiter(1)
	scheduler := NewSubagentScheduler(agent, SubagentSchedulerConfig{
		MaxConcurrent: 2,
		MaxDepth:      1,
		WriterArbiter: arbiter,
		GlobalLimiter: globalLimiter,
	})

	var mu sync.Mutex
	var started []string
	bus := runtimeevents.NewBus()
	bus.Subscribe("subagent.started", func(event runtimeevents.Event) {
		mu.Lock()
		defer mu.Unlock()
		if id, ok := event.Payload["subagent_id"].(string); ok {
			started = append(started, id)
		}
	})
	agent.SetEventBus(bus)

	results, err := scheduler.RunChildren(context.Background(), SubagentRunOptions{Depth: 1}, []SubagentTask{
		{ID: "writer-1", Goal: "Modify config.", ReadOnly: false, ToolsWhitelist: []string{"view", "write"}},
		{ID: "writer-2", Goal: "Modify code.", ReadOnly: false, ToolsWhitelist: []string{"view", "edit"}},
	})
	require.NoError(t, err)
	require.Len(t, results, 2)
	for _, result := range results {
		assert.True(t, result.Success, "writer %s failed: %s", result.ID, result.Error)
	}

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"writer-1", "writer-2"}, started, "writers must run one at a time in declaration order")
	assert.Zero(t, arbiter.InFlight(), "the fence must be released after every writer")
	assert.Zero(t, globalLimiter.InFlight(), "the shared concurrency slot must be released after every writer")
	assert.Contains(t, strings.Join(results[0].Route.Warnings, "|"), "write_paths",
		"undeclared writers must carry the dispatch advisory into the receipt")
}
