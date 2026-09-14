package agent

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
)

// PR-4 §6.4 第 5 条的纯逻辑回归：连续失败阈值 → 短期熔断（边沿通告 + 退避升级）、
// 每窗口至多退避一次、冷却到期重新计数。时间全部注入，不依赖真实时钟。
func TestPromptCacheBreaker_TripsOnThresholdWithEdgeAndEscalatingBackoff(t *testing.T) {
	breaker := NewPromptCacheBreaker(PromptCacheBreakerSpec{
		FailureThreshold: 3,
		Cooldown:         time.Minute,
		BaseBackoff:      2 * time.Second,
		MaxBackoff:       8 * time.Second,
	})
	now := time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)

	first := breaker.ObserveFailure("fp-a", UpstreamInvalidResponseCode, now)
	require.False(t, first.Tripped)
	require.Equal(t, 1, first.Consecutive)

	second := breaker.ObserveFailure("fp-a", UpstreamInvalidResponseCode, now.Add(time.Second))
	require.False(t, second.Tripped, "two consecutive failures are still treated as upstream jitter")

	third := breaker.ObserveFailure("fp-a", UpstreamInvalidResponseCode, now.Add(2*time.Second))
	require.True(t, third.Tripped, "reaching the threshold opens the breaker")
	require.True(t, third.Open)
	require.Equal(t, 3, third.Consecutive)
	require.Equal(t, 1, third.Trips)
	require.Equal(t, 2*time.Second, third.Backoff)

	// 窗口内继续失败：升级退避，但不重复通告（Tripped 是边沿信号）。
	fourth := breaker.ObserveFailure("fp-a", UpstreamInvalidResponseCode, now.Add(3*time.Second))
	require.False(t, fourth.Tripped, "trip must be edge-triggered, not repeated per failure")
	require.Equal(t, 4*time.Second, fourth.Backoff)
	require.Equal(t, 2, fourth.Trips)

	fifth := breaker.ObserveFailure("fp-a", UpstreamInvalidResponseCode, now.Add(4*time.Second))
	require.Equal(t, 8*time.Second, fifth.Backoff, "backoff doubles but is capped by MaxBackoff")

	// 其他指纹有自己的计数，不共享阈值。
	other := breaker.ObserveFailure("fp-b", UpstreamInvalidResponseCode, now)
	require.False(t, other.Tripped)
	require.Equal(t, 1, other.Consecutive)
}

func TestPromptCacheBreaker_BacksOffOncePerWindowAndReArmsAfterCoolDown(t *testing.T) {
	breaker := NewPromptCacheBreaker(PromptCacheBreakerSpec{
		FailureThreshold: 2,
		Cooldown:         30 * time.Second,
		BaseBackoff:      2 * time.Second,
		MaxBackoff:       8 * time.Second,
	})
	now := time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)
	breaker.ObserveFailure("fp", UpstreamInvalidResponseCode, now)
	tripped := breaker.ObserveFailure("fp", UpstreamInvalidResponseCode, now)
	require.True(t, tripped.Tripped)

	delay, ok := breaker.PendingBackoff("fp", now)
	require.True(t, ok, "an open window must delay the next identical request")
	require.Equal(t, 2*time.Second, delay)

	_, ok = breaker.PendingBackoff("fp", now.Add(time.Second))
	require.False(t, ok, "each window backs off at most once (no double wait with llm retries)")

	_, ok = breaker.PendingBackoff("fp", now.Add(31*time.Second))
	require.False(t, ok, "after the cool-down the window is closed")
	require.Zero(t, breaker.Snapshot(now.Add(31*time.Second)).OpenFingerprints)

	// 冷却后重新累积：重置为 1，再跨阈值时 trips=2、退避升级到 4s。
	reArmed := breaker.ObserveFailure("fp", UpstreamInvalidResponseCode, now.Add(31*time.Second))
	require.False(t, reArmed.Tripped)
	require.Equal(t, 1, reArmed.Consecutive)
	second := breaker.ObserveFailure("fp", UpstreamInvalidResponseCode, now.Add(32*time.Second))
	require.True(t, second.Tripped)
	require.Equal(t, 2, second.Trips)
	require.Equal(t, 4*time.Second, second.Backoff)

	require.Equal(t, 2, breaker.Trips())
}

func TestPromptCacheBreaker_AggregatesUpstreamInvalidResponseOnly(t *testing.T) {
	breaker := NewPromptCacheBreaker(PromptCacheBreakerSpec{})

	suppress, count := breaker.AggregateRetryError("fp-a", UpstreamInvalidResponseCode)
	require.False(t, suppress, "the first occurrence still reaches the status line")
	require.Equal(t, 1, count)

	suppress, count = breaker.AggregateRetryError("fp-b", UpstreamInvalidResponseCode)
	require.True(t, suppress, "later occurrences are counted instead of re-emitted")
	require.Equal(t, 2, count)

	suppress, count = breaker.AggregateRetryError("fp-a", "TRANSPORT_TIMEOUT")
	require.False(t, suppress, "other error codes keep per-attempt reporting")
	require.Zero(t, count)

	require.Equal(t, 2, breaker.AggregatedTotal())

	aggregate, ok := breaker.FlushAggregated()
	require.True(t, ok)
	require.Equal(t, UpstreamInvalidResponseCode, aggregate.ErrorCode)
	require.Equal(t, 2, aggregate.Count)
	require.Equal(t, []string{"fp-a", "fp-b"}, aggregate.Fingerprints)

	_, ok = breaker.FlushAggregated()
	require.False(t, ok, "flush drains the ledger so a run cannot report the same counts twice")
	require.Equal(t, 2, breaker.AggregatedTotal(), "the cumulative total survives the flush")
}

func TestPromptCacheBreaker_SuccessClearsAndNilReceiverIsSafe(t *testing.T) {
	breaker := NewPromptCacheBreaker(PromptCacheBreakerSpec{FailureThreshold: 3})
	now := time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)
	breaker.ObserveFailure("fp", UpstreamInvalidResponseCode, now)
	breaker.ObserveFailure("fp", UpstreamInvalidResponseCode, now)
	breaker.ObserveSuccess("fp")

	after := breaker.ObserveFailure("fp", UpstreamInvalidResponseCode, now)
	require.False(t, after.Tripped, "success resets the consecutive counter")
	require.Equal(t, 1, after.Consecutive)
	require.Zero(t, breaker.Trips())

	// 未接线的 loop（子代理路径）必须退化为观测空操作，而不是 panic。
	var nilBreaker *PromptCacheBreaker
	require.False(t, nilBreaker.ObserveFailure("fp", "X", now).Tripped)
	nilBreaker.ObserveSuccess("fp")
	_, ok := nilBreaker.PendingBackoff("fp", now)
	require.False(t, ok)
	suppress, count := nilBreaker.AggregateRetryError("fp", UpstreamInvalidResponseCode)
	require.False(t, suppress)
	require.Zero(t, count)
	_, ok = nilBreaker.FlushAggregated()
	require.False(t, ok)
	require.Zero(t, nilBreaker.AggregatedTotal())
	require.Zero(t, nilBreaker.Trips())
	require.Equal(t, PromptCacheBreakerSnapshot{}, nilBreaker.Snapshot(now))
}

// 退避必须可取消：plan §8.1 明令禁止伪等待，ctx 取消后要立刻返回。
func TestPromptCacheBreaker_BackoffWaitHonorsContext(t *testing.T) {
	started := time.Now()
	require.NoError(t, waitPromptCacheBackoff(context.Background(), 0))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := waitPromptCacheBackoff(ctx, 30*time.Second)
	require.ErrorIs(t, err, context.Canceled)
	require.Less(t, time.Since(started), 5*time.Second, "a cancelled backoff must not keep waiting")
}

func TestPromptFingerprintFromRequest(t *testing.T) {
	require.Empty(t, promptFingerprintFromRequest(nil))
	require.Empty(t, promptFingerprintFromRequest(&llm.LLMRequest{}))
	require.Empty(t, promptFingerprintFromRequest(&llm.LLMRequest{Metadata: map[string]interface{}{"prompt_fingerprint": "   "}}))
	require.Equal(t, "fp-a", promptFingerprintFromRequest(&llm.LLMRequest{
		Metadata: map[string]interface{}{"prompt_fingerprint": " fp-a "},
	}))
}

// 熔断器挂在 run ctx 上，会被流的回调（重试上报器）与循环本体同时触碰，
// 因此所有入口都必须在 -race 下并发安全。
func TestPromptCacheBreaker_ConcurrentObservationsAreRaceFree(t *testing.T) {
	breaker := NewPromptCacheBreaker(PromptCacheBreakerSpec{FailureThreshold: 2, Cooldown: time.Minute})
	now := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			fingerprint := fmt.Sprintf("fp-%d", i%4)
			breaker.ObserveFailure(fingerprint, UpstreamInvalidResponseCode, now)
			breaker.PendingBackoff(fingerprint, now)
			breaker.AggregateRetryError(fingerprint, UpstreamInvalidResponseCode)
			breaker.Snapshot(now)
			breaker.Trips()
			if i%2 == 0 {
				breaker.ObserveSuccess(fingerprint)
			}
		}(i)
	}
	wg.Wait()
	require.Equal(t, 32, breaker.AggregatedTotal())
}

// 上报器层面的现场复现：同一个 UPSTREAM_INVALID_RESPONSE 在一次 run 里出现 6 次，
// 只有首次外发（状态行仍能看到"正在重试"），其余全部只计数。
func TestReActLoop_RetryReporterSuppressesRepeatedUpstreamInvalidResponse(t *testing.T) {
	loop, _ := newTurnBudgetTestLoop(t, 3, []*llm.LLMResponse{{Content: "ok", Model: "test-model"}})
	bus := runtimeevents.NewBus()
	var retryEvents []runtimeevents.Event
	bus.Subscribe("llm.retry", func(event runtimeevents.Event) {
		retryEvents = append(retryEvents, event)
	})
	loop.agent.SetEventBus(bus)

	breaker := NewPromptCacheBreaker(PromptCacheBreakerSpec{})
	req := &llm.LLMRequest{
		Provider: "test-provider",
		Model:    "test-model",
		Metadata: map[string]interface{}{"prompt_fingerprint": "fp-field"},
	}
	reporter := loop.runtimeRetryEventReporter("trace_retry", "session_retry", 1, req, breaker)
	require.NotNil(t, reporter)

	for attempt := 1; attempt <= 6; attempt++ {
		reporter(llm.RetryEvent{
			Source:    "test",
			Attempt:   attempt,
			ErrorCode: UpstreamInvalidResponseCode,
			Error:     "tool call 0 has incomplete JSON arguments",
		})
	}
	require.Len(t, retryEvents, 1, "6 identical upstream-invalid-response retries must not produce 6 status lines")
	require.Equal(t, UpstreamInvalidResponseCode, retryEvents[0].Payload["error_code"])
	require.Equal(t, 6, breaker.AggregatedTotal())
	require.Equal(t, "fp-field", retryEvents[0].Payload["prompt_fingerprint"])

	reporter(llm.RetryEvent{Source: "test", ErrorCode: "TRANSPORT_TIMEOUT"})
	require.Len(t, retryEvents, 2, "unrelated error codes keep their per-attempt events")
}

// run 退出时的汇总上报契约：有聚合内容才发一条 warn，发过即清空，不重复。
func TestReActLoop_EmitAggregatedRetryReportOnceAndOnlyWhenPresent(t *testing.T) {
	loop, _ := newTurnBudgetTestLoop(t, 3, []*llm.LLMResponse{{Content: "ok", Model: "test-model"}})
	bus := runtimeevents.NewBus()
	var reports []runtimeevents.Event
	bus.Subscribe("llm.retry.aggregated", func(event runtimeevents.Event) {
		reports = append(reports, event)
	})
	loop.agent.SetEventBus(bus)

	empty := NewPromptCacheBreaker(PromptCacheBreakerSpec{})
	require.False(t, loop.emitAggregatedRetryReport("session_x", "trace_x", 3, empty),
		"no aggregation → no warn line")

	breaker := NewPromptCacheBreaker(PromptCacheBreakerSpec{})
	for i := 0; i < 6; i++ {
		breaker.AggregateRetryError("fp-field", UpstreamInvalidResponseCode)
	}
	require.True(t, loop.emitAggregatedRetryReport("session_x", "trace_x", 3, breaker))
	require.Len(t, reports, 1)
	require.Equal(t, "warn", reports[0].Payload["severity"])
	require.Equal(t, UpstreamInvalidResponseCode, reports[0].Payload["error_code"])
	require.EqualValues(t, 6, reports[0].Payload["count"])
	require.EqualValues(t, 1, reports[0].Payload["prompt_fingerprint_count"])
	require.Equal(t, []string{"fp-field"}, reports[0].Payload["prompt_fingerprints"])

	require.False(t, loop.emitAggregatedRetryReport("session_x", "trace_x", 3, breaker),
		"a flushed report must not be emitted twice")
	require.Len(t, reports, 1)
}
