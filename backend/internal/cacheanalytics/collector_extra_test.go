package cacheanalytics

import (
	"fmt"
	"math/rand"
	"testing"
	"time"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// publishAssistantMessage 发布 assistant 消息事件（§5.2 关联输入）。
func publishAssistantMessage(bus *runtimeevents.Bus, sessionID, traceID, turnID, messageID string) {
	payload := map[string]interface{}{
		"turn_id": turnID,
		"role":    "assistant",
	}
	if messageID != "" {
		payload["message_id"] = messageID
	}
	bus.Publish(runtimeevents.Event{
		Type:      eventAssistantMessage,
		SessionID: sessionID,
		TraceID:   traceID,
		Payload:   payload,
		Timestamp: nextTestTimestamp(),
	})
}

// TestCorrelationMultiStepTurnTrace 同 turn 多步 ReAct（§12 关联用例）：
// N 个请求（含失败步）+ assistant 消息事件 → produced_by 指向产出请求，
// 后续 turn 请求进入 consumed_by。
func TestCorrelationMultiStepTurnTrace(t *testing.T) {
	bus, service := attachTest(t, 100)
	src := service.Source()

	// turn-7：step1 成功 hit、step2 失败、step3 成功 write。
	publishStarted(bus, "s1", "req-1", map[string]interface{}{"logical_turn_id": "turn-7", "step": 1})
	publishFinished(bus, "s1", "req-1", true, map[string]interface{}{
		"logical_turn_id":           "turn-7",
		"usage_prompt_tokens":       1000,
		"usage_cache_read_tokens":   800,
		"usage_cache_read_reported": true,
	})
	publishStarted(bus, "s1", "req-2", map[string]interface{}{"logical_turn_id": "turn-7", "step": 2})
	publishFinished(bus, "s1", "req-2", false, map[string]interface{}{"logical_turn_id": "turn-7"})
	publishStarted(bus, "s1", "req-3", map[string]interface{}{"logical_turn_id": "turn-7", "step": 3})
	publishFinished(bus, "s1", "req-3", true, map[string]interface{}{
		"logical_turn_id":              "turn-7",
		"usage_prompt_tokens":          1000,
		"usage_cache_creation_tokens":  300,
	})

	// assistant 消息事件（载荷携带 message_id 时走事件流主路径，§5.2 path 1）。
	publishAssistantMessage(bus, "s1", "trace-req-3", "turn-7", "msg-final")

	trace, err := src.MessageTrace("s1", "msg-final")
	if err != nil {
		t.Fatalf("MessageTrace: %v", err)
	}
	if trace.ProducedBy == nil {
		t.Fatal("produced_by missing")
	}
	// 产出请求 = trace_id 对应的请求（最后成功步 req-3，失败步 req-2 不参与）。
	if trace.ProducedBy.LLMRequestID != "req-3" {
		t.Fatalf("produced_by.llm_request_id = %q, want req-3", trace.ProducedBy.LLMRequestID)
	}
	if trace.TurnID != "turn-7" {
		t.Fatalf("turn_id = %q, want turn-7", trace.TurnID)
	}
	if len(trace.ConsumedBy) != 0 {
		t.Fatalf("consumed_by before follow-up turns = %v, want empty", trace.ConsumedBy)
	}

	// 后续 turn 的请求消费了该消息产出的上下文 → consumed_by。
	publishStarted(bus, "s1", "req-4", map[string]interface{}{"logical_turn_id": "turn-8", "step": 1})
	publishFinished(bus, "s1", "req-4", true, map[string]interface{}{
		"logical_turn_id":           "turn-8",
		"usage_prompt_tokens":       2000,
		"usage_cache_read_tokens":   1500,
		"usage_cache_read_reported": true,
	})

	trace, err = src.MessageTrace("s1", "msg-final")
	if err != nil {
		t.Fatalf("MessageTrace(2): %v", err)
	}
	if len(trace.ConsumedBy) != 1 || trace.ConsumedBy[0].LLMRequestID != "req-4" {
		t.Fatalf("consumed_by = %+v, want [req-4]", trace.ConsumedBy)
	}

	// 事件流未携带 message_id 时（生产现状）走 HistoryLookup 兜底；此处
	// history 为 nil，未知消息 id 应返回 ErrNotFound 而非空 trace。
	if _, err := src.MessageTrace("s1", "msg-unknown"); err != ErrNotFound {
		t.Fatalf("unknown message err = %v, want ErrNotFound", err)
	}
}

// TestOverviewIncrementalMatchesFullRecompute 聚合一致性（§12 property 风格）：
// 随机记录流增量聚合后，overview 与全量重算逐字段一致。
func TestOverviewIncrementalMatchesFullRecompute(t *testing.T) {
	rng := rand.New(rand.NewSource(20260910))
	projector := newProjector(1000)

	type expect struct {
		total         int
		withUsage     int
		cacheReported int
		sumPrompt     int64
		sumCompletion int64
		sumTotal      int64
		sumRead       int64
		sumCreation   int64
		sumReasoning  int64
		readPrompt    int64
		readTokens    int64
		writePrompt   int64
		writeTokens   int64
		dist          CacheStatusDistribution
	}
	var want expect

	for i := 0; i < 60; i++ {
		record := &CacheRequestRecord{
			SchemaVersion: SchemaVersion,
			LLMRequestID:  fmt.Sprintf("req-%d", i),
			SessionID:     "s-prop",
			StartedAt:     time.Date(2026, 1, 1, 0, 0, i, 0, time.UTC),
			CacheStatus:   []string{CacheStatusHit, CacheStatusWrite, CacheStatusReportedZero, CacheStatusNotReported, CacheStatusError}[rng.Intn(5)],
		}
		if rng.Intn(4) != 0 { // 3/4 带 usage
			usage := &CacheUsage{
				PromptTokens:    int64(100 + rng.Intn(900)),
				CompletionTokens: int64(rng.Intn(200)),
			}
			usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
			usage.ReasoningTokens = int64(rng.Intn(50))
			switch record.CacheStatus {
			case CacheStatusHit:
				usage.CacheReadTokens = int64(rng.Intn(int(usage.PromptTokens)))
				usage.CacheReadReported = true
			case CacheStatusWrite:
				usage.CacheCreationTokens = int64(rng.Intn(int(usage.PromptTokens)))
				usage.CacheCreationReported = true
			case CacheStatusReportedZero:
				usage.CacheReadReported = true
			}
			record.Usage = usage
			want.total++
			want.withUsage++
			want.sumPrompt += usage.PromptTokens
			want.sumCompletion += usage.CompletionTokens
			want.sumTotal += usage.TotalTokens
			want.sumRead += usage.CacheReadTokens
			want.sumCreation += usage.CacheCreationTokens
			want.sumReasoning += usage.ReasoningTokens
			if usage.CacheReadReported {
				want.cacheReported++
				want.readPrompt += usage.PromptTokens
				want.readTokens += usage.CacheReadTokens
			}
			if usage.CacheCreationReported {
				want.writePrompt += usage.PromptTokens
				want.writeTokens += usage.CacheCreationTokens
			}
		} else {
			want.total++
		}
		switch record.CacheStatus {
		case CacheStatusHit:
			want.dist.Hit++
		case CacheStatusWrite:
			want.dist.Write++
		case CacheStatusReportedZero:
			want.dist.ReportedZero++
		case CacheStatusNotReported:
			want.dist.NotReported++
		case CacheStatusError:
			want.dist.Error++
		}
		projector.append(record)
	}

	overview := projector.overview("s-prop")
	if overview.RequestsTotal != want.total {
		t.Fatalf("requests_total = %d, want %d", overview.RequestsTotal, want.total)
	}
	if overview.RequestsWithUsage != want.withUsage {
		t.Fatalf("requests_with_usage = %d, want %d", overview.RequestsWithUsage, want.withUsage)
	}
	if overview.RequestsCacheReported != want.cacheReported {
		t.Fatalf("requests_cache_reported = %d, want %d", overview.RequestsCacheReported, want.cacheReported)
	}
	tokens := overview.Tokens
	if tokens.PromptTokens != want.sumPrompt || tokens.CompletionTokens != want.sumCompletion ||
		tokens.TotalTokens != want.sumTotal || tokens.CacheReadTokens != want.sumRead ||
		tokens.CacheCreationTokens != want.sumCreation || tokens.ReasoningTokens != want.sumReasoning {
		t.Fatalf("tokens mismatch: %+v want prompt=%d completion=%d total=%d read=%d creation=%d reasoning=%d",
			tokens, want.sumPrompt, want.sumCompletion, want.sumTotal, want.sumRead, want.sumCreation, want.sumReasoning)
	}
	if overview.CacheStatusDistribution != want.dist {
		t.Fatalf("distribution mismatch: %+v want %+v", overview.CacheStatusDistribution, want.dist)
	}
	// 比率分母只统计对应 reported 请求（§4.2）。
	if want.readPrompt > 0 {
		if overview.CacheHitRatio == nil {
			t.Fatal("cache_hit_ratio missing")
		}
		expectRatio := float64(want.readTokens) / float64(want.readPrompt)
		if diff := *overview.CacheHitRatio - expectRatio; diff > 1e-9 || diff < -1e-9 {
			t.Fatalf("cache_hit_ratio = %v, want %v", *overview.CacheHitRatio, expectRatio)
		}
	}
	if want.writePrompt > 0 {
		if overview.CacheWriteRatio == nil {
			t.Fatal("cache_write_ratio missing")
		}
		expectRatio := float64(want.writeTokens) / float64(want.writePrompt)
		if diff := *overview.CacheWriteRatio - expectRatio; diff > 1e-9 || diff < -1e-9 {
			t.Fatalf("cache_write_ratio = %v, want %v", *overview.CacheWriteRatio, expectRatio)
		}
	}
	// 无 ring 溢出 → partial=false（即使存在 not_reported，§4.2）。
	if overview.Coverage.Partial {
		t.Fatalf("partial = true without overflow, reasons=%v", overview.Coverage.PartialReasons)
	}
}

// TestOverviewPartialOnlyOnOverflow partial 语义（§4.2）：not_reported 存在时
// partial 仍为 false；环形缓冲溢出才置 true + ring_overflow。
func TestOverviewPartialOnlyOnOverflow(t *testing.T) {
	bus, service := attachTest(t, 100)
	src := service.Source()

	// 一条 not_reported（provider 未上报）请求：partial 必须为 false。
	publishStarted(bus, "s1", "req-nr", nil)
	publishFinished(bus, "s1", "req-nr", true, map[string]interface{}{
		"usage_prompt_tokens":     500,
		"usage_completion_tokens": 20,
	})
	overview, err := src.Overview("s1")
	if err != nil {
		t.Fatalf("Overview: %v", err)
	}
	if overview.Coverage.Partial {
		t.Fatalf("partial = true with only not_reported, reasons=%v", overview.Coverage.PartialReasons)
	}
	if overview.Coverage.UsageRequestRate == nil || *overview.Coverage.UsageRequestRate != 1 {
		t.Fatalf("usage_request_rate = %v, want 1", overview.Coverage.UsageRequestRate)
	}

	// 空会话：partial=false（requests_total=0 自明，不误显横幅）。
	empty, err := src.Overview("s-empty")
	if err != nil {
		t.Fatalf("Overview(empty): %v", err)
	}
	if empty.Coverage.Partial || len(empty.Coverage.PartialReasons) != 0 {
		t.Fatalf("empty session coverage = %+v, want partial=false", empty.Coverage)
	}
}
