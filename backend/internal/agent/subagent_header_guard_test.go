package agent

// subagent_header_guard_test.go — §8.1 专项：provider response-header guard
//（上游接受连接与请求但永不返回响应头）在**任务层**的接线：
//
//   - guard 快速失败（≈ 2×ResponseHeaderTimeout），不得把 task deadline 之前的
//     重试预算静默吞掉：attempt 1 失败后必须仍能执行 attempt 2；
//   - guard 失败必须归类为可重试（retryable=true、retry_skipped 空），并且
//     不得被误报成 task_timeout / batch_deadline；
//   - 每次 attempt 都用尽 transport budget 时，收尾原因必须显式
//     （max_attempts_reached），而不是静默停在那里等 deadline。
//
// provider 侧机制（两连超时 streak guard、transport budget 学、handoff 到
// runtime retry）由 llm 包用例固定：
// TestProviderAbortsAfterRepeatedResponseHeaderTimeouts /
// TestProviderTransportBudgetWinsOverHeaderGuard /
// TestProviderTransportBudgetHandoffRecoversAtRuntimeLayer。这里固定 agent 侧。

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
)

// hangThenServeUpstream 模拟挂起上游：前 serveAfter 个请求不写任何响应头
// （等客户端 header-guard 断开），之后正常应答（body 里 "stream":true 时走 SSE）。
type hangThenServeUpstream struct {
	serveAfter int
	mu         sync.Mutex
	hits       int
	lastBody   string
}

func (u *hangThenServeUpstream) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		u.hits++
		n := u.hits
		u.mu.Unlock()
		if n <= u.serveAfter {
			// 挂起：不写任何响应头，等客户端 header-guard 超时。
			// 用**有界睡眠**（150ms ≫ 40ms guard 超时）而不是 <-r.Context().Done()：
			// 后者在断言失败提前收尾时会让 httptest.Server.Close 永久等待仍未
			// 断开的连接（实测 2 条 active conn 卡死 Close 直至 test 超时）。
			time.Sleep(150 * time.Millisecond)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		u.mu.Lock()
		u.lastBody = string(raw)
		u.mu.Unlock()
		if strings.Contains(string(raw), `"stream":true`) {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "data: %s\n\n", `{"choices":[{"index":0,"delta":{"content":"recovered"},"finish_reason":"stop"}]}`)
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"chatcmpl-guard","object":"chat.completion","model":"guard-model",`+
			`"choices":[{"index":0,"message":{"role":"assistant","content":"recovered"},"finish_reason":"stop"}],`+
			`"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`)
	}
}

// hitCount 返回上游收到的请求数（并发安全）。
func (u *hangThenServeUpstream) hitCount() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.hits
}

// servedBody 返回最近一次被应答请求的原始报文（诊断用）。
func (u *hangThenServeUpstream) servedBody() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.lastBody
}

// newHeaderGuardProvider 构造真实 openai 兼容 provider：单次业务尝试（MaxRetries=0
// → 每次 provider 调用恰 1 个 HTTP 请求；transport budget 只在业务重放内生效，
// 这里仅声明为 1 不参与重放）+ 小 header 超时。provider 层不重放 → 任务级 attempt
// 是唯一重放层，计数与归因都干净。
func newHeaderGuardProvider(t *testing.T, baseURL string) llm.LLMProvider {
	t.Helper()
	provider, err := llm.NewProvider(&llm.ProviderConfig{
		Type:                  "openai",
		BaseURL:               baseURL,
		MaxRetries:            0,
		MaxTransportRetries:   1,
		ResponseHeaderTimeout: 40 * time.Millisecond,
		RetryTuning: llm.RetryTuning{
			BaseDelay:     time.Millisecond,
			MaxDelay:      time.Millisecond,
			Randomization: -1,
		},
	})
	require.NoError(t, err)
	return provider
}

// headerGuardRig 装配 agent 侧装置：真实 provider 注册为 rig 既有 provider 名
// （与 fake-based 用例同源解析路径），runtime 级重试关闭 → 只留任务级 attempt。
func headerGuardRig(t *testing.T, upstream *hangThenServeUpstream) *attemptEventRig {
	t.Helper()
	rig := newAttemptEventRig(t)
	server := httptest.NewServer(upstream.handler())
	t.Cleanup(server.Close)

	llmRuntime := llm.NewLLMRuntime(nil)
	// 关闭 runtime 级重试：失败后由任务级 attempt 决定是否重放整调用。
	llmRuntime.UpdateRetryConfig(0, llm.RetryTuning{}, nil)
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", newHeaderGuardProvider(t, server.URL)))
	rig.agent.llmRuntime = llmRuntime
	return rig
}

// attemptPayloads 汇总 rig 采集到的 attempt 事件（按 attempt 序号）与任务终态。
func attemptPayloads(events []recordedLifecycleEvent) (map[int]map[string]interface{}, map[string]interface{}) {
	finished := map[int]map[string]interface{}{}
	var completed map[string]interface{}
	for _, event := range events {
		switch event.eventType {
		case runtimeevents.EventSubagentAttemptFinished:
			if attempt, ok := event.payload["attempt"].(int); ok {
				finished[attempt] = event.payload
			}
		case "subagent.completed":
			completed = event.payload
		}
	}
	return finished, completed
}

// TestHeaderGuardFailureKeepsTaskRetryBudget 锁定 §8.1 验收：上游挂起 → provider
// header-guard 快速失败 → 任务级 attempt 2 仍能执行并在预算内成功；失败归因可重试、
// 无 retry_skipped、无 task_timeout/batch_deadline 误报，remaining_ms 保持大值。
func TestHeaderGuardFailureKeepsTaskRetryBudget(t *testing.T) {
	// attempt 1 的第 1 个请求挂起（guard 失败）；attempt 2 的第 2 个请求开始正常应答。
	upstream := &hangThenServeUpstream{serveAfter: 1}
	rig := headerGuardRig(t, upstream)

	coordinator := rig.agent.GetSubagentBatchCoordinator()
	require.NotNil(t, coordinator)
	start := time.Now()
	batch, err := coordinator.StartBackground(context.Background(), BatchStartOptions{
		ParentSessionID: "parent-header-guard",
		RootScopeID:     "parent-header-guard",
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
	}, []SubagentTask{
		{ID: "guard-child", Goal: "Recover from a hung upstream", ReadOnly: true, TimeoutSec: 30},
	})
	require.NoError(t, err)
	require.NotNil(t, batch)
	rig.waitTerminal(t)
	elapsed := time.Since(start)

	finished, completed := attemptPayloads(rig.snapshot())
	require.NotNil(t, finished[1], "attempt 1 必须有 finished 事件")
	first := finished[1]
	require.Equal(t, false, first["success"])
	require.Equal(t, true, first["retryable"], "header-guard 失败必须是可重试类别")
	require.Equal(t, "", first["retry_skipped"],
		"预算未被吞：transient 失败且预算充足时不得记录跳过原因")
	require.Equal(t, "", first["cancel_source"],
		"header-guard 快速失败不得被误报成 task_timeout/batch_deadline")
	require.Contains(t, fmt.Sprint(first["error"]), "timeout awaiting response headers",
		"失败必须保留 hung-upstream 签名（诊断/分类依赖该文案）")
	require.Greater(t, intValue(first["remaining_ms"]), 10000,
		"30s 任务预算只消耗一次快速失败：剩余预算必须仍是大值")

	second := finished[2]
	require.NotNil(t, second, "attempt 2 必须发生（重试预算未被静默吞掉）")
	if second["success"] != true {
		t.Logf("诊断: hits=%d attempt1.err=%q attempt2: status=%v reason=%v err=%q servedBody=%q",
			upstream.hitCount(), fmt.Sprint(first["error"]), second["status"], second["completion_reason"],
			fmt.Sprint(second["error"]), upstream.servedBody())
	}
	require.Equal(t, true, second["success"])

	require.NotNil(t, completed, "任务终态必须可见")
	require.Equal(t, true, completed["success"])
	require.Equal(t, 2, completed["attempt"])
	// 成功终态可不携带该键（nil 与空串同义）：都表示"无取消归因"。
	require.Empty(t, completed["cancel_source"], "成功终态不得携带取消归因")

	stored, err := coordinator.Get(context.Background(), batch.BatchID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.Equal(t, subagentbatch.BatchCompleted, stored.Status)

	require.Equal(t, 2, upstream.hitCount(),
		"attempt 1（挂起）与 attempt 2（成功）各 1 个上游请求：共 2 个")
	require.Less(t, elapsed, 10*time.Second,
		"guard 必须快速失败（远小于 30s 任务 deadline），不得靠 deadline 兜底")
}

// TestHeaderGuardExhaustionIsExplicitNotSilent 锁定 §8.1 的另一半：上游持续挂起
// （每个 attempt 的 provider 调用都以 guard 失败）时，收尾必须显式
// max_attempts_reached，且仍不得误报 task_timeout——任务在远小于 deadline 的
// 时间内失败收口。
func TestHeaderGuardExhaustionIsExplicitNotSilent(t *testing.T) {
	upstream := &hangThenServeUpstream{serveAfter: 1 << 20} // 始终挂起
	rig := headerGuardRig(t, upstream)

	coordinator := rig.agent.GetSubagentBatchCoordinator()
	require.NotNil(t, coordinator)
	start := time.Now()
	batch, err := coordinator.StartBackground(context.Background(), BatchStartOptions{
		ParentSessionID: "parent-header-guard-exhausted",
		RootScopeID:     "parent-header-guard-exhausted",
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
	}, []SubagentTask{
		{ID: "guard-exhausted-child", Goal: "Never recovers", ReadOnly: true, TimeoutSec: 30},
	})
	require.NoError(t, err)
	require.NotNil(t, batch)
	rig.waitTerminal(t)
	elapsed := time.Since(start)

	finished, completed := attemptPayloads(rig.snapshot())
	require.NotNil(t, finished[1])
	require.Equal(t, true, finished[1]["retryable"])
	require.Equal(t, "", finished[1]["retry_skipped"], "attempt 1 应允许重试")
	require.Equal(t, "", finished[1]["cancel_source"])

	require.NotNil(t, finished[2], "attempt 2 必须发生")
	require.Equal(t, false, finished[2]["success"])
	require.Equal(t, "max_attempts_reached", finished[2]["retry_skipped"],
		"预算耗尽必须显式记录 max_attempts_reached，而不是静默停住")
	require.Equal(t, "", finished[2]["cancel_source"],
		"仍不得误报 task_timeout：任务没有等到 deadline")

	require.NotNil(t, completed)
	require.Equal(t, false, completed["success"])
	require.Equal(t, "", completed["cancel_source"])

	stored, err := coordinator.Get(context.Background(), batch.BatchID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.Equal(t, subagentbatch.BatchFailed, stored.Status)

	require.Equal(t, 2, upstream.hitCount(),
		"两次 attempt 各 1 个上游请求（provider 层不重放）")
	require.Less(t, elapsed, 15*time.Second,
		"guard 失败收口必须远快于 30s deadline（否则等于静默吞预算）")
}
