package llm

import (
	"context"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestAttemptUsageTrackerAccumulatesAndClones(t *testing.T) {
	tracker := NewAttemptUsageTracker()
	if tracker.Usage() != nil {
		t.Fatal("empty tracker must report no usage")
	}
	tracker.Record(nil)
	if tracker.Attempts() != 0 {
		t.Fatal("nil usage must not count as an attempt")
	}
	tracker.Record(&types.TokenUsage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120, ReasoningTokens: 15})
	tracker.Record(&types.TokenUsage{PromptTokens: 50, CompletionTokens: 10})
	usage := tracker.Usage()
	if usage == nil {
		t.Fatal("expected accumulated usage")
	}
	if usage.PromptTokens != 150 || usage.CompletionTokens != 30 || usage.TotalTokens != 120 {
		t.Fatalf("accumulated usage=%+v", usage)
	}
	// TotalTokens 只取 provider 上报值：第二笔没有上报 total 时不得自行估算补写。
	if usage.ReasoningTokens != 15 {
		t.Fatalf("reasoning tokens lost: %+v", usage)
	}
	if tracker.Attempts() != 2 {
		t.Fatalf("attempts=%d want 2", tracker.Attempts())
	}
	// Usage() 返回副本：调用方修改不得影响账本。
	usage.PromptTokens = 0
	if tracker.Usage().PromptTokens != 150 {
		t.Fatal("Usage() must return a copy")
	}
}

func TestRecordDiscardedAttemptUsageReadsProviderReportedBody(t *testing.T) {
	ctx := WithAttemptUsageTracker(context.Background(), NewAttemptUsageTracker())
	body := []byte("data: {\"choices\":[{\"delta\":{}}],\"usage\":{\"prompt_tokens\":120,\"completion_tokens\":30,\"total_tokens\":150,\"completion_tokens_details\":{\"reasoning_tokens\":20}}}\n\ndata: [DONE]\n\n")
	if !RecordDiscardedAttemptUsage(ctx, body) {
		t.Fatal("provider-reported usage in the response body must be recorded")
	}
	tracker := AttemptUsageTrackerFromContext(ctx)
	usage := tracker.Usage()
	if usage == nil || usage.PromptTokens != 120 || usage.CompletionTokens != 30 || usage.TotalTokens != 150 {
		t.Fatalf("recorded usage=%+v", usage)
	}
	if tracker.Attempts() != 1 {
		t.Fatalf("attempts=%d want 1", tracker.Attempts())
	}
	// 无 usage 字段/无 body/无账本：不记录，也绝不回退到本地估算。
	if RecordDiscardedAttemptUsage(ctx, []byte("data: {\"choices\":[]}\n\n")) {
		t.Fatal("body without provider usage must not be recorded")
	}
	if RecordDiscardedAttemptUsage(ctx, nil) {
		t.Fatal("nil body must not be recorded")
	}
	if RecordDiscardedAttemptUsage(context.Background(), body) {
		t.Fatal("missing tracker must be a no-op")
	}
	if tracker.Attempts() != 1 {
		t.Fatalf("rejected bodies must not advance attempts: %d", tracker.Attempts())
	}
}

func TestRecordDiscardedChatUsageSkipsZeroUsage(t *testing.T) {
	ctx := WithAttemptUsageTracker(context.Background(), NewAttemptUsageTracker())
	if !RecordDiscardedChatUsage(ctx, &ChatResponse{Usage: Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}}) {
		t.Fatal("decoded wire usage must be recorded")
	}
	if usage := AttemptUsageTrackerFromContext(ctx).Usage(); usage == nil || usage.TotalTokens != 15 {
		t.Fatalf("recorded usage=%+v", usage)
	}
	if RecordDiscardedChatUsage(ctx, &ChatResponse{}) {
		t.Fatal("zero wire usage must not be recorded")
	}
	if RecordDiscardedChatUsage(ctx, nil) {
		t.Fatal("nil response must be a no-op")
	}
	if RecordDiscardedChatUsage(context.Background(), &ChatResponse{Usage: Usage{TotalTokens: 9}}) {
		t.Fatal("missing tracker must be a no-op")
	}
}
