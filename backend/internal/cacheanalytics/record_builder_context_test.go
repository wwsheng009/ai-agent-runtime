package cacheanalytics

import (
	"testing"
	"time"
)

// TestBuildTerminalRecordCapturesContextFacts 验证终态记录携带 llm.request.finished
// 载荷里的上下文事实（出站 token / 窗口 / 预算）——工作台"上下文用量"面板依赖它们。
func TestBuildTerminalRecordCapturesContextFacts(t *testing.T) {
	finishedAt := time.Date(2026, 9, 17, 9, 30, 0, 0, time.UTC)
	record := BuildTerminalRecord(TerminalRecordInput{
		LLMRequestID: "req-ctx",
		SessionID:    "sess-ctx",
		StartedAt:    finishedAt.Add(-time.Second),
		FinishedAt:   finishedAt,
		Payload: map[string]interface{}{
			"success":               true,
			"context_prompt_tokens": 120224,
			"context_window_tokens": 128000,
			"prompt_budget":         108800,
		},
	})
	if record.ContextPromptTokens != 120224 || record.ContextWindowTokens != 128000 || record.PromptBudget != 108800 {
		t.Fatalf("context facts = %+v", record)
	}
}

// TestBuildTerminalRecordKeepsContextFactsOnError 验证失败请求（如上下文超限被拒）
// 同样保留窗口与预算：前端仍能显示"已用 / 窗口 / 预算"而不是退回"未知"。
func TestBuildTerminalRecordKeepsContextFactsOnError(t *testing.T) {
	finishedAt := time.Date(2026, 9, 17, 9, 30, 0, 0, time.UTC)
	record := BuildTerminalRecord(TerminalRecordInput{
		LLMRequestID: "req-err",
		SessionID:    "sess-err",
		StartedAt:    finishedAt.Add(-time.Second),
		FinishedAt:   finishedAt,
		Payload: map[string]interface{}{
			"success":               false,
			"error_code":            "context_length_exceeded",
			"context_window_tokens": 128000,
			"prompt_budget":         108800,
		},
	})
	if record.Status != RequestStatusError {
		t.Fatalf("status = %q, want %q", record.Status, RequestStatusError)
	}
	if record.ContextWindowTokens != 128000 || record.PromptBudget != 108800 {
		t.Fatalf("context facts on error = %+v", record)
	}
}

// TestBuildTerminalRecordWithoutContextFacts 验证载荷缺失时三项保持 0（"未观测"），
// 由查询层按缺失处理，前端显示"未知"而不是 0 值。
func TestBuildTerminalRecordWithoutContextFacts(t *testing.T) {
	finishedAt := time.Date(2026, 9, 17, 9, 30, 0, 0, time.UTC)
	record := BuildTerminalRecord(TerminalRecordInput{
		LLMRequestID: "req-legacy",
		SessionID:    "sess-legacy",
		StartedAt:    finishedAt,
		FinishedAt:   finishedAt,
		Payload:      map[string]interface{}{"success": true},
	})
	if record.ContextPromptTokens != 0 || record.ContextWindowTokens != 0 || record.PromptBudget != 0 {
		t.Fatalf("context facts = %+v, want zero", record)
	}
}
