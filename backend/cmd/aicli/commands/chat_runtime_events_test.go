package commands

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	runtimellm "github.com/wwsheng009/ai-agent-runtime/internal/llm"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	runtimeskill "github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestFormatInteractiveSupplementPromptLine_PreservesPromptContentWithoutIndent(t *testing.T) {
	got := formatInteractiveSupplementPromptLine("[approval] query=北京 天气预报 未来 7 天")
	if strings.HasPrefix(got, ui.AssistantContentIndent()) {
		t.Fatalf("expected prompt line without assistant gutter indent, got %q", got)
	}
	if !strings.Contains(got, "[approval] query=北京 天气预报 未来 7 天") {
		t.Fatalf("expected approval content to stay visible, got %q", got)
	}
}

func TestChatRuntimeEvents_RenderPlanningAndSubagentTimeline(t *testing.T) {
	if got := renderChatRuntimeEvent(runtimeevents.Event{Type: runtimechat.EventLLMRequestStarted, TraceID: "trace-1", Payload: map[string]interface{}{"model": "gpt-5.4"}}); got != "" {
		t.Fatalf("unexpected llm started render: %q", got)
	}
	if got := renderChatRuntimeEvent(runtimeevents.Event{
		Type:    runtimechat.EventLLMRequestStarted,
		TraceID: "trace-1",
		Payload: map[string]interface{}{
			"model": "gpt-5.4",
			"step":  1,
			"tool_availability": map[string]interface{}{
				"requires_active_team_run": []interface{}{
					"read_task_spec",
					"read_task_context",
					"send_team_message",
					"read_mailbox_digest",
					"report_task_outcome",
				},
			},
		},
	}); got != "" {
		t.Fatalf("unexpected llm started tool availability render: %q", got)
	}
	if got := renderChatRuntimeEvent(runtimeevents.Event{Type: "llm.request.started", TraceID: "trace-1", Payload: map[string]interface{}{"model": "gpt-5.4"}}); got != "" {
		t.Fatalf("unexpected dotted llm started render: %q", got)
	}
	if got := renderChatRuntimeEvent(runtimeevents.Event{
		Type:    "llm.request.started",
		TraceID: "trace-1",
		Payload: map[string]interface{}{
			"model": "gpt-5.4",
			"step":  2,
			"tool_availability": map[string]interface{}{
				"requires_active_team_run": []interface{}{"read_task_spec"},
			},
		},
	}); got != "" {
		t.Fatalf("unexpected repeated llm started tool availability render: %q", got)
	}
	if got := renderChatRuntimeEvent(runtimeevents.Event{
		Type:    "llm.request.started",
		TraceID: "trace-1",
		Payload: map[string]interface{}{
			"step":                  3,
			"prompt_layout_summary": "layers=base/system -> developer/developer | sources=system.md, tools.md",
			"prompt_layout_length":  132,
			"total_message_chars":   2048,
			"instruction_tokens":    33,
			"total_tokens":          512,
		},
	}); got != "[prompt] layers=base/system -> developer/developer | sources=system.md, tools.md (instruction 33 / total 512 tokens, 132 / 2048 chars)" {
		t.Fatalf("unexpected llm started prompt layout render: %q", got)
	}
	if got := renderChatRuntimeEvent(runtimeevents.Event{
		Type:    "llm.request.started",
		TraceID: "trace-1",
		Payload: map[string]interface{}{
			"step":                  3,
			"prompt_layout_summary": "layers=base/system -> developer/developer | sources=system.md, tools.md",
			"prompt_layout_length":  132,
			"instruction_tokens":    33,
		},
	}); got != "[prompt] layers=base/system -> developer/developer | sources=system.md, tools.md (33 tokens, 132 chars)" {
		t.Fatalf("unexpected llm started prompt layout render without total: %q", got)
	}
	if got := renderChatRuntimeEvent(runtimeevents.Event{Type: runtimechat.EventLLMRequestFinished, TraceID: "trace-1", Payload: map[string]interface{}{"success": true}}); got != "" {
		t.Fatalf("expected successful llm finished render to be suppressed, got %q", got)
	}
	if got := renderChatRuntimeEvent(runtimeevents.Event{Type: "llm.request.finished", TraceID: "trace-1", Payload: map[string]interface{}{"success": true}}); got != "" {
		t.Fatalf("expected dotted successful llm finished render to be suppressed, got %q", got)
	}
	if got := renderChatRuntimeEvent(runtimeevents.Event{Type: "planning.started"}); got != "" {
		t.Fatalf("unexpected planning render: %q", got)
	}
	if got := renderChatRuntimeEvent(runtimeevents.Event{Type: "subagent.batch.started"}); got != "" {
		t.Fatalf("unexpected subagent batch render: %q", got)
	}
	if got := renderChatRuntimeEvent(runtimeevents.Event{Type: "subagent.started", Payload: map[string]interface{}{"agent_id": "reader"}}); got != "" {
		t.Fatalf("unexpected subagent started render: %q", got)
	}
	if got := renderChatRuntimeEvent(runtimeevents.Event{
		Type: runtimechat.EventAssistantReasoning,
		Payload: map[string]interface{}{
			"reasoning": map[string]interface{}{
				"provider":        "anthropic",
				"format":          "anthropic_thinking",
				"summary":         "先确认配置，再决定是否调用工具。",
				"replay_required": true,
			},
		},
	}); got != strings.Join([]string{
		chatToolDivider("reasoning"),
		"[reasoning] replay=required",
		"  先确认配置，再决定是否调用工具。",
		chatToolDivider("end reasoning"),
	}, "\n") {
		t.Fatalf("unexpected reasoning render: %q", got)
	}
	if got := renderChatRuntimeEvent(runtimeevents.Event{
		Type: runtimechat.EventAssistantReasoning,
		Payload: map[string]interface{}{
			"reasoning": map[string]interface{}{
				"provider": "CODEX_LOCAL",
				"format":   "openai_responses",
			},
		},
	}); got != "" {
		t.Fatalf("expected metadata-only reasoning render to be suppressed, got %q", got)
	}
	if got := renderChatRuntimeEvent(runtimeevents.Event{Type: "subagent.completed", Payload: map[string]interface{}{"agent_id": "writer"}}); got != "[subagent] completed writer" {
		t.Fatalf("unexpected subagent render: %q", got)
	}
	if got := renderChatRuntimeEvent(runtimeevents.Event{Type: "tool.requested", ToolName: "ls", Payload: map[string]interface{}{"arg_preview": "path=src"}}); got != "• Running ls path=src" {
		t.Fatalf("unexpected tool requested render: %q", got)
	}
	if got := renderChatRuntimeEvent(runtimeevents.Event{Type: "tool.requested", ToolName: "list_mcp_resources", Payload: map[string]interface{}{"tool_source": "meta"}}); got != "• Running [meta] list_mcp_resources" {
		t.Fatalf("unexpected meta tool requested render: %q", got)
	}
	if got := renderChatRuntimeEvent(runtimeevents.Event{Type: "tool.requested", ToolName: "remote_search", Payload: map[string]interface{}{"tool_source": "mcp", "arg_preview": "query=golang"}}); got != "• Running [mcp] remote_search query=golang" {
		t.Fatalf("unexpected mcp tool requested render: %q", got)
	}
	if got := renderChatRuntimeEvent(runtimeevents.Event{Type: "tool.requested", ToolName: "background_task", Payload: map[string]interface{}{"tool_source": "broker", "arg_preview": "command=git status"}}); got != "• Running [broker] background_task command=git status" {
		t.Fatalf("unexpected broker tool requested render: %q", got)
	}
	if got := renderChatRuntimeEvent(runtimeevents.Event{Type: "tool.requested", ToolName: "execute_shell_command", Payload: map[string]interface{}{"command_text": "git status --short", "arg_preview": "command=git status --short"}}); got != "• Running git status --short" {
		t.Fatalf("unexpected shell tool requested render: %q", got)
	}
	if got := renderChatRuntimeEvent(runtimeevents.Event{Type: "tool.requested", ToolName: "execute_shell_command", Payload: map[string]interface{}{"command_text": "git status --short", "workdir": "E:/projects/ai/ai-agent-runtime"}}); got != strings.Join([]string{
		"• Running git status --short",
		"  workdir: E:/projects/ai/ai-agent-runtime",
	}, "\n") {
		t.Fatalf("unexpected shell tool requested workdir render: %q", got)
	}
	if got := renderChatRuntimeEvent(runtimeevents.Event{
		Type:     "tool.completed",
		ToolName: "ls",
		Payload: map[string]interface{}{
			"arg_preview":   "path=src",
			"summary_lines": []interface{}{"目录: src", "📁 a/ · 📁 b/", "统计: 0 个文件, 2 个目录"},
		},
	}); got != strings.Join([]string{
		"• Ran ls path=src",
		"  目录: src",
		"  📁 a/ · 📁 b/",
		"  统计: 0 个文件, 2 个目录",
	}, "\n") {
		t.Fatalf("unexpected tool completed render: %q", got)
	}
	if got := renderChatRuntimeEvent(runtimeevents.Event{
		Type:     "tool.completed",
		ToolName: "execute_shell_command",
		Payload: map[string]interface{}{
			"command_text":  "git status",
			"arg_preview":   "command=git status",
			"summary_lines": []interface{}{"Tool execute_shell_command failed before producing output."},
			"error":         "exit status 128",
		},
	}); got != strings.Join([]string{
		"• Ran git status",
		"  failed: exit status 128",
	}, "\n") {
		t.Fatalf("unexpected failed tool render: %q", got)
	}
	if got := renderChatRuntimeEvent(runtimeevents.Event{
		Type:     "tool.completed",
		ToolName: "execute_shell_command",
		Payload: map[string]interface{}{
			"command_text":  "git status",
			"workdir":       "E:/projects/ai/ai-agent-runtime",
			"summary_lines": []interface{}{"On branch main"},
		},
	}); got != strings.Join([]string{
		"• Ran git status",
		"  On branch main",
		"  workdir: E:/projects/ai/ai-agent-runtime",
	}, "\n") {
		t.Fatalf("unexpected completed tool workdir render: %q", got)
	}
	if got := renderChatRuntimeEvent(runtimeevents.Event{
		Type:     "tool.completed",
		ToolName: "execute_shell_command",
		Payload: map[string]interface{}{
			"command_text":  "go build -o .\\aicli-cachetest.exe .\\cmd\\aicli",
			"arg_preview":   "command=go build -o .\\aicli-cachetest.exe .\\cmd\\aicli",
			"summary_lines": []interface{}{"Tool returned no output."},
		},
	}); got != strings.Join([]string{
		"• Ran go build -o .\\aicli-cachetest.exe .\\cmd\\aicli",
		"  (no output)",
	}, "\n") {
		t.Fatalf("unexpected no-output shell tool render: %q", got)
	}
	if got := renderChatRuntimeEvent(runtimeevents.Event{
		Type:     "tool.completed",
		ToolName: "web_search",
		Payload: map[string]interface{}{
			"arg_preview":    "query=天气预报",
			"summary_lines":  []interface{}{"返回 10 条结果"},
			"awaiting_model": true,
		},
	}); got != strings.Join([]string{
		"• Ran web_search query=天气预报",
		"  返回 10 条结果",
	}, "\n") {
		t.Fatalf("unexpected tool render: %q", got)
	}
	if got := renderChatRuntimeEvent(runtimeevents.Event{
		Type:     "tool.completed",
		ToolName: "list_mcp_resources",
		Payload: map[string]interface{}{
			"tool_source":   "meta",
			"summary_lines": []interface{}{"server=docs resources=12", "next_cursor=cursor-1", "warning=truncated"},
		},
	}); got != strings.Join([]string{
		"• Ran [meta] list_mcp_resources",
		"  server=docs resources=12",
		"  next_cursor=cursor-1",
	}, "\n") {
		t.Fatalf("unexpected meta tool render: %q", got)
	}
	if got := renderChatRuntimeEvent(runtimeevents.Event{
		Type:     "tool.completed",
		ToolName: "remote_search",
		Payload: map[string]interface{}{
			"tool_source":   "mcp",
			"arg_preview":   "query=golang tools",
			"summary_lines": []interface{}{"result 1", "result 2", "result 3"},
		},
	}); got != strings.Join([]string{
		"• Ran [mcp] remote_search query=golang tools",
		"  result 1",
		"  result 2",
	}, "\n") {
		t.Fatalf("unexpected mcp tool render: %q", got)
	}
	if got := renderChatRuntimeEvent(runtimeevents.Event{
		Type:     "tool.completed",
		ToolName: "background_task",
		Payload: map[string]interface{}{
			"tool_source":   "broker",
			"arg_preview":   "command=git status",
			"summary_lines": []interface{}{"job_id=job-1", "status=queued", "restart_policy=fail"},
		},
	}); got != strings.Join([]string{
		"• Ran [broker] background_task command=git status",
		"  job_id=job-1",
		"  status=queued",
	}, "\n") {
		t.Fatalf("unexpected broker tool render: %q", got)
	}
	if got := renderChatRuntimeEvent(runtimeevents.Event{
		Type:    "tool.denied",
		Payload: map[string]interface{}{"reason": "approval denied"},
	}); got != "[tool denied] approval denied" {
		t.Fatalf("unexpected denied tool render: %q", got)
	}
	if got := renderChatRuntimeEvent(runtimeevents.Event{Type: "task.started", Payload: map[string]interface{}{"task_id": "task-1", "assignee": "planner"}}); got != "" {
		t.Fatalf("unexpected task render: %q", got)
	}
	if got := renderChatRuntimeEvent(runtimeevents.Event{Type: runtimechat.EventMailboxReceived, Payload: map[string]interface{}{"team_id": "team-1", "message_id": "msg-1", "from_agent": "planner", "to_agent": "lead", "kind": "progress", "task_id": "task-1", "body": "Started task: Draft"}}); got != "[progress] planner -> lead task-1 Started task: Draft" {
		t.Fatalf("unexpected mailbox render: %q", got)
	}
	if got := renderChatRuntimeEvent(runtimeevents.Event{Type: "team.completed", Payload: map[string]interface{}{"team_id": "team-1", "status": "done"}}); got != "[team] completed team-1 status=done" {
		t.Fatalf("unexpected team completion render: %q", got)
	}
	if got := renderChatRuntimeEvent(runtimeevents.Event{Type: "team.summary", Payload: map[string]interface{}{"team_id": "team-1", "summary": "auto lead summary"}}); got != "[team summary] team-1 auto lead summary" {
		t.Fatalf("unexpected team summary render: %q", got)
	}
	if got := renderChatRuntimeEvent(runtimeevents.Event{
		Type: "team.summary",
		Payload: map[string]interface{}{
			"team_id":                           "team-1",
			"summary":                           "fallback summary",
			"summary_source":                    "fallback",
			"fallback_reason":                   "lead_session_error",
			"error_type":                        "prompt_preflight",
			"failure_reason_code":               "prompt_still_exceeds_budget_after_compaction",
			"resolved_provider":                 "CODEX_LOCAL",
			"resolved_model":                    "codex-gpt-5.4",
			"budget_source":                     "model_capability_auto_compact_token_limit",
			"replacement_history_applied":       true,
			"replacement_history_message_count": 2,
		},
	}); got != strings.Join([]string{
		"[team summary] team-1 [fallback] [prompt preflight] fallback summary",
		"  原因: active-turn 已压缩，但 prompt 仍然超出预算",
		"  模型: CODEX_LOCAL / codex-gpt-5.4",
		"  预算: 模型能力 auto-compact token limit",
		"  恢复: 已自动保存压缩后的上下文，可直接继续下一轮 | history_messages=2",
	}, "\n") {
		t.Fatalf("unexpected fallback team summary render: %q", got)
	}
	if got := renderChatRuntimeEvent(runtimeevents.Event{
		Type: "team.summary.generated",
		Payload: map[string]interface{}{
			"team_id":         "team-2",
			"summary":         "generated fallback summary",
			"summary_source":  "fallback",
			"fallback_reason": "lead_session_error",
		},
	}); got != strings.Join([]string{
		"[team summary] team-2 [fallback] generated fallback summary",
		"  fallback: lead summary 执行失败，改用任务列表回退总结",
	}, "\n") {
		t.Fatalf("unexpected generated fallback team summary render: %q", got)
	}
	if got := renderChatRuntimeEvent(runtimeevents.Event{Type: chatEventInputQueueDetected, Payload: map[string]interface{}{"queued_input_count": 2, "source": "stdin"}}); got != "[input] queued 2 line(s) from stdin" {
		t.Fatalf("unexpected input queue detected render: %q", got)
	}
	if got := renderChatRuntimeEvent(runtimeevents.Event{Type: chatEventInputQueueDrained, Payload: map[string]interface{}{}}); got != "[input] queued input drained" {
		t.Fatalf("unexpected input queue drained render: %q", got)
	}
	if got := renderChatRuntimeEvent(runtimeevents.Event{Type: chatEventInputQueueDiscarded, Payload: map[string]interface{}{"discarded_count": 1, "prompt_kind": "审批提示"}}); got != "[input] discarded 1 queued line(s) before 审批提示" {
		t.Fatalf("unexpected input queue discarded render: %q", got)
	}
	if got := renderChatRuntimeEvent(runtimeevents.Event{
		Type:      runtimechat.EventSessionEnd,
		SessionID: "session-1",
		TraceID:   "trace-preflight",
		Payload: map[string]interface{}{
			"error_type":                        "prompt_preflight",
			"failure_reason_code":               "active_turn_not_compactable",
			"failure_reason":                    "active-turn replay cannot be compacted further",
			"suggested_action":                  "请减少更早历史、提高 prompt 预算，或开启新的用户轮次。",
			"prompt_tokens":                     8192,
			"prompt_budget":                     4096,
			"resolved_provider":                 "CODEX_LOCAL",
			"resolved_model":                    "codex-gpt-5.4",
			"budget_source":                     "model_capability_auto_compact_token_limit",
			"active_turn_message_count":         12,
			"latest_replay_block_message_count": 4,
			"replacement_history_available":     true,
			"replacement_history_message_count": 6,
		},
	}); got != strings.Join([]string{
		"[prompt preflight] 本地拦截：prompt 8192 > budget 4096",
		"  原因: 当前轮次里的 active-turn replay 已无法继续压缩",
		"  建议: 请减少更早历史、提高 prompt 预算，或开启新的用户轮次。",
		"  模型: CODEX_LOCAL / codex-gpt-5.4",
		"  预算: 模型能力 auto-compact token limit",
		"  active-turn: messages=12, latest_replay_block=4, compacted=false",
		"  恢复: 已生成压缩后的恢复上下文 | history_messages=6",
	}, "\n") {
		t.Fatalf("unexpected prompt preflight session_end render: %q", got)
	}
	if got := renderChatRuntimeEvent(runtimeevents.Event{
		Type: "team.task.failed",
		Payload: map[string]interface{}{
			"team_id":                           "team-1",
			"task_id":                           "task-42",
			"assignee":                          "mate-1",
			"summary":                           "prompt preflight budget exceeded",
			"error_type":                        "prompt_preflight",
			"failure_reason_code":               "prompt_still_exceeds_budget_after_compaction",
			"resolved_provider":                 "CODEX_LOCAL",
			"resolved_model":                    "codex-gpt-5.4",
			"budget_source":                     "model_capability_auto_compact_token_limit",
			"replacement_history_applied":       true,
			"replacement_history_message_count": 4,
		},
	}); got != strings.Join([]string{
		"[task] failed task-42 @mate-1 prompt preflight budget exceeded [prompt preflight]",
		"  原因: active-turn 已压缩，但 prompt 仍然超出预算",
		"  模型: CODEX_LOCAL / codex-gpt-5.4",
		"  预算: 模型能力 auto-compact token limit",
		"  恢复: 已自动保存压缩后的上下文，可直接继续下一轮 | history_messages=4",
	}, "\n") {
		t.Fatalf("unexpected prompt preflight team.task.failed render: %q", got)
	}
	if got := renderChatRuntimeEvent(runtimeevents.Event{
		Type: "team.task.blocked",
		Payload: map[string]interface{}{
			"team_id":                                  "team-1",
			"task_id":                                  "task-42",
			"assignee":                                 "mate-1",
			"summary":                                  "waiting on architecture review",
			"replan_error_type":                        "prompt_preflight",
			"replan_failure_reason_code":               "active_turn_not_compactable",
			"replan_resolved_provider":                 "CODEX_LOCAL",
			"replan_resolved_model":                    "codex-gpt-5.4",
			"replan_budget_source":                     "model_capability_auto_compact_token_limit",
			"replan_replacement_history_applied":       true,
			"replan_replacement_history_message_count": 3,
		},
	}); got != strings.Join([]string{
		"[task] blocked task-42 @mate-1 waiting on architecture review",
		"  replan: [prompt preflight] 当前轮次里的 active-turn replay 已无法继续压缩",
		"  replan 模型: CODEX_LOCAL / codex-gpt-5.4",
		"  replan 预算: 模型能力 auto-compact token limit",
		"  replan 恢复: 已自动保存压缩后的上下文，可直接继续下一轮 | history_messages=3",
	}, "\n") {
		t.Fatalf("unexpected prompt preflight team.task.blocked render: %q", got)
	}
	if got := renderChatRuntimeEvent(runtimeevents.Event{
		Type: "team.plan.replan_failed",
		Payload: map[string]interface{}{
			"team_id":                           "team-1",
			"task_id":                           "task-42",
			"error_type":                        "prompt_preflight",
			"failure_reason_code":               "prompt_still_exceeds_budget_after_compaction",
			"resolved_provider":                 "CODEX_LOCAL",
			"resolved_model":                    "codex-gpt-5.4",
			"budget_source":                     "model_capability_auto_compact_token_limit",
			"replacement_history_applied":       true,
			"replacement_history_message_count": 5,
		},
	}); got != strings.Join([]string{
		"[team replan] failed team-1 task-42 [prompt preflight]",
		"  原因: active-turn 已压缩，但 prompt 仍然超出预算",
		"  模型: CODEX_LOCAL / codex-gpt-5.4",
		"  预算: 模型能力 auto-compact token limit",
		"  恢复: 已自动保存压缩后的上下文，可直接继续下一轮 | history_messages=5",
	}, "\n") {
		t.Fatalf("unexpected prompt preflight team.plan.replan_failed render: %q", got)
	}
}

func TestChatRuntimeEvents_RenderSessionCompactTimeline(t *testing.T) {
	if got := renderChatRuntimeEvent(runtimeevents.Event{
		Type:      runtimechat.EventSessionCompactStarted,
		SessionID: "session-1",
		TraceID:   "trace-compact",
		Payload: map[string]interface{}{
			"phase":               "pre_turn",
			"mode":                "local",
			"token_before":        1200,
			"trigger_token_limit": 900,
			"max_context_tokens":  10000,
			"provider":            "CODEX_LOCAL",
			"model":               "codex-gpt-5.4",
		},
	}); got != "[context] session compact started mode=local phase=pre_turn token_before=1200 trigger_token_limit=900 max_context_tokens=10000 target=CODEX_LOCAL/codex-gpt-5.4" {
		t.Fatalf("unexpected session compact started render: %q", got)
	}

	if got := renderChatRuntimeEvent(runtimeevents.Event{
		Type:      runtimechat.EventSessionCompactCompleted,
		SessionID: "session-1",
		TraceID:   "trace-compact",
		Payload: map[string]interface{}{
			"phase":               "pre_turn",
			"mode":                "local",
			"token_before":        1200,
			"token_after":         240,
			"compacted_messages":  6,
			"message_count_after": 4,
			"checkpoint_id":       "cp-1",
		},
	}); got != "[context] session compact completed mode=local phase=pre_turn token 1200 -> 240 compacted_messages=6 history_messages=4 checkpoint_id=cp-1" {
		t.Fatalf("unexpected session compact completed render: %q", got)
	}

	if got := renderChatRuntimeEvent(runtimeevents.Event{
		Type:      runtimechat.EventSessionCompactSkipped,
		SessionID: "session-1",
		TraceID:   "trace-compact",
		Payload: map[string]interface{}{
			"phase":  "pre_turn",
			"mode":   "local",
			"reason": "below_limit",
		},
	}); got != "[context] session compact skipped mode=local phase=pre_turn reason=below_limit" {
		t.Fatalf("unexpected session compact skipped render: %q", got)
	}

	if got := renderChatRuntimeEvent(runtimeevents.Event{
		Type:      runtimechat.EventSessionCompactFailed,
		SessionID: "session-1",
		TraceID:   "trace-compact",
		Payload: map[string]interface{}{
			"phase":  "pre_turn",
			"mode":   "local",
			"reason": "summary_generation_failed",
			"error":  "compact summary failed",
		},
	}); got != "[context] session compact failed mode=local phase=pre_turn reason=summary_generation_failed error=compact summary failed" {
		t.Fatalf("unexpected session compact failed render: %q", got)
	}
}

func TestChatRuntimeEvents_DedupesStableTimelineEventsPerRun(t *testing.T) {
	session := &ChatSession{}
	bridge := newChatRuntimeEventBridge(session)
	var rendered []string
	bridge.writeLine = func(line string) {
		rendered = append(rendered, line)
	}

	bridge.BeginRun()
	event := runtimeevents.Event{
		Type:    "team.summary",
		Payload: map[string]interface{}{"team_id": "team-1", "summary": "auto lead summary"},
	}
	bridge.handleEvent(event)
	bridge.handleEvent(event)

	if len(rendered) != 1 {
		t.Fatalf("expected one rendered line after dedupe, got %d (%v)", len(rendered), rendered)
	}
}

func TestChatRuntimeEvents_RendersRepeatedLLMRequestStartedForDifferentSteps(t *testing.T) {
	session := &ChatSession{}
	bridge := newChatRuntimeEventBridge(session)
	var rendered []string
	bridge.writeLine = func(line string) {
		rendered = append(rendered, line)
	}

	bridge.BeginRun()
	bridge.handleEvent(runtimeevents.Event{
		Type:    "llm.request.started",
		TraceID: "trace-1",
		Payload: map[string]interface{}{"model": "gpt-5.4", "step": 1},
	})
	bridge.handleEvent(runtimeevents.Event{
		Type:    "llm.request.started",
		TraceID: "trace-1",
		Payload: map[string]interface{}{"model": "gpt-5.4", "step": 2},
	})

	require.Empty(t, rendered)
}

func TestChatRuntimeEvents_RendersRepeatedLLMRequestFinishedForDifferentSteps(t *testing.T) {
	session := &ChatSession{}
	bridge := newChatRuntimeEventBridge(session)
	var rendered []string
	bridge.writeLine = func(line string) {
		rendered = append(rendered, line)
	}

	bridge.BeginRun()
	bridge.handleEvent(runtimeevents.Event{
		Type:    "llm.request.finished",
		TraceID: "trace-1",
		Payload: map[string]interface{}{"success": true, "step": 1},
	})
	bridge.handleEvent(runtimeevents.Event{
		Type:    "llm.request.finished",
		TraceID: "trace-1",
		Payload: map[string]interface{}{"success": true, "step": 2},
	})

	require.Empty(t, rendered)
}

func TestChatRuntimeEvents_DedupesRepeatedLLMRequestStartedWithinSameStep(t *testing.T) {
	session := &ChatSession{}
	bridge := newChatRuntimeEventBridge(session)
	var rendered []string
	bridge.writeLine = func(line string) {
		rendered = append(rendered, line)
	}

	bridge.BeginRun()
	event := runtimeevents.Event{
		Type:    "llm.request.started",
		TraceID: "trace-1",
		Payload: map[string]interface{}{"model": "gpt-5.4", "step": 2},
	}
	bridge.handleEvent(event)
	bridge.handleEvent(event)

	require.Empty(t, rendered)
}

func TestChatRuntimeEvents_RendersAssistantMessageReasoningBeforeContent(t *testing.T) {
	session := &ChatSession{
		RuntimeSession: &runtimechat.Session{ID: "lead-session"},
	}
	bridge := newChatRuntimeEventBridge(session)
	var rendered []string
	bridge.writeLine = func(line string) {
		rendered = append(rendered, line)
	}
	bridge.renderResponse = func(response string) {
		rendered = append(rendered, response)
	}

	bridge.BeginRun()
	bridge.handleEvent(runtimeevents.Event{
		Type:      runtimechat.EventAssistantMessage,
		SessionID: "lead-session",
		TraceID:   "trace-1",
		Payload: map[string]interface{}{
			"content": "Hello!",
			"reasoning": map[string]interface{}{
				"provider": "nvidia",
				"format":   "openai_compatible",
				"summary":  "先输出 reasoning，再输出正文。",
			},
		},
	})

	if len(rendered) != 2 {
		t.Fatalf("expected reasoning and content render, got %v", rendered)
	}
	if !strings.Contains(rendered[0], chatToolDivider("reasoning")) || !strings.Contains(rendered[0], "先输出 reasoning，再输出正文。") {
		t.Fatalf("expected reasoning block first, got %q", rendered[0])
	}
	if rendered[1] != "Hello!" {
		t.Fatalf("expected assistant content second, got %q", rendered[1])
	}
}

func TestChatRuntimeEvents_CompletesFinalStreamableReasoningInsteadOfRestartingIt(t *testing.T) {
	session := &ChatSession{
		RuntimeSession: &runtimechat.Session{ID: "lead-session"},
	}
	session.Interaction = newChatInteractionCoordinator(session)
	session.Interaction.liveStreamFn = func() bool { return true }

	bridge := newChatRuntimeEventBridge(session)
	var deltaCalls []string
	var completed []string
	bridge.writeReasoningDelta = func(block *runtimetypes.ReasoningBlock) {
		deltaCalls = append(deltaCalls, block.DisplayText())
	}
	bridge.completeReasoning = func(block *runtimetypes.ReasoningBlock) bool {
		completed = append(completed, block.DisplayText())
		return true
	}

	bridge.BeginRun()
	bridge.handleEvent(runtimeevents.Event{
		Type:      runtimechat.EventAssistantReasoning,
		SessionID: "lead-session",
		TraceID:   "trace-1",
		Payload: map[string]interface{}{
			"step": 1,
			"reasoning": map[string]interface{}{
				"provider":   "nvidia",
				"format":     "stream_delta",
				"summary":    "先检查目录。",
				"streamable": true,
			},
		},
	})
	bridge.handleEvent(runtimeevents.Event{
		Type:      runtimechat.EventAssistantReasoning,
		SessionID: "lead-session",
		TraceID:   "trace-1",
		Payload: map[string]interface{}{
			"step": 1,
			"reasoning": map[string]interface{}{
				"provider":   "nvidia",
				"format":     "openai_compatible",
				"summary":    "先检查目录，再整理结果。",
				"streamable": true,
			},
		},
	})

	require.Equal(t, []string{"先检查目录。"}, deltaCalls)
	require.Equal(t, []string{"先检查目录，再整理结果。"}, completed)
	if !bridge.hasRenderedReasoningFinal() {
		t.Fatal("expected reasoning stream to be finalized")
	}
}

func TestChatRuntimeEvents_IgnoresLateDuplicateReasoningAfterAssistantMessageCompletion(t *testing.T) {
	session := &ChatSession{
		RuntimeSession: &runtimechat.Session{ID: "lead-session"},
	}
	session.Interaction = newChatInteractionCoordinator(session)
	session.Interaction.liveStreamFn = func() bool { return true }

	bridge := newChatRuntimeEventBridge(session)
	var rendered []string
	bridge.writePrompt = func() {}
	bridge.writeReasoningDelta = func(block *runtimetypes.ReasoningBlock) {
		rendered = append(rendered, "delta:"+block.DisplayText())
	}
	bridge.completeReasoning = func(block *runtimetypes.ReasoningBlock) bool {
		rendered = append(rendered, "complete:"+block.DisplayText())
		return true
	}
	bridge.renderResponse = func(response string) {
		rendered = append(rendered, "content:"+response)
	}

	bridge.BeginRun()
	bridge.handleEvent(runtimeevents.Event{
		Type:      runtimechat.EventAssistantReasoning,
		SessionID: "lead-session",
		TraceID:   "trace-1",
		Payload: map[string]interface{}{
			"step": 1,
			"reasoning": map[string]interface{}{
				"provider":   "nvidia",
				"format":     "stream_delta",
				"summary":    "先确认上下文。",
				"streamable": true,
			},
		},
	})
	bridge.handleEvent(runtimeevents.Event{
		Type:      runtimechat.EventAssistantMessage,
		SessionID: "lead-session",
		TraceID:   "trace-1",
		Payload: map[string]interface{}{
			"content": "Hello!",
			"reasoning": map[string]interface{}{
				"provider":   "nvidia",
				"format":     "openai_compatible",
				"summary":    "先确认上下文，再直接问候。",
				"streamable": true,
			},
		},
	})
	bridge.handleEvent(runtimeevents.Event{
		Type:      runtimechat.EventAssistantReasoning,
		SessionID: "lead-session",
		TraceID:   "trace-1",
		Payload: map[string]interface{}{
			"step": 1,
			"reasoning": map[string]interface{}{
				"provider":   "nvidia",
				"format":     "openai_compatible",
				"summary":    "先确认上下文，再直接问候。",
				"streamable": true,
			},
		},
	})

	require.Equal(t, []string{
		"delta:先确认上下文。",
		"complete:先确认上下文，再直接问候。",
		"content:Hello!",
	}, rendered)
}

func TestChatRuntimeEvents_RendersAsyncAssistantSummaryAfterTeamCompletion(t *testing.T) {
	session := &ChatSession{
		RuntimeSession: &runtimechat.Session{ID: "lead-session"},
		ActiveTeam:     &chatTeamBinding{TeamID: "team-1", AgentID: "lead"},
	}
	bridge := newChatRuntimeEventBridge(session)
	var rendered []string
	bridge.writeLine = func(line string) {
		rendered = append(rendered, line)
	}
	bridge.renderResponse = func(response string) {
		rendered = append(rendered, response)
	}

	bridge.BeginRun()
	bridge.handleEvent(runtimeevents.Event{
		Type:      "team.completed",
		SessionID: "lead-session",
		Payload:   map[string]interface{}{"team_id": "team-1", "status": "done"},
	})
	bridge.handleEvent(runtimeevents.Event{
		Type:      runtimechat.EventAssistantMessage,
		SessionID: "lead-session",
		Payload:   map[string]interface{}{"content": "Completed all work."},
	})

	if !containsAllChatTimelineLines(rendered, "[team] completed team-1 status=done", "[team summary] team-1 Completed all work.", "Completed all work.") {
		t.Fatalf("expected async summary fallback render, got %v", rendered)
	}
}

func TestChatRuntimeEvents_RendersAsyncAssistantSummaryWhenTeamAlreadyTerminalInStore(t *testing.T) {
	store, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	teamID, err := store.CreateTeam(context.Background(), team.Team{
		ID:     "team-1",
		Status: team.TeamStatusDone,
	})
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	if teamID == "" {
		t.Fatal("expected team id")
	}

	session := &ChatSession{
		RuntimeSession:   &runtimechat.Session{ID: "lead-session"},
		ActiveTeam:       &chatTeamBinding{TeamID: "team-1", AgentID: "lead"},
		LocalRuntimeHost: &localChatRuntimeHost{TeamStore: store},
	}
	bridge := newChatRuntimeEventBridge(session)
	var rendered []string
	bridge.writeLine = func(line string) {
		rendered = append(rendered, line)
	}
	bridge.renderResponse = func(response string) {
		rendered = append(rendered, response)
	}

	bridge.BeginRun()
	bridge.handleEvent(runtimeevents.Event{
		Type:      runtimechat.EventAssistantMessage,
		SessionID: "lead-session",
		Payload:   map[string]interface{}{"content": "Completed all work from persisted terminal state."},
	})

	if !containsAllChatTimelineLines(rendered, "[team summary] team-1 Completed all work from persisted terminal state.", "Completed all work from persisted terminal state.") {
		t.Fatalf("expected async summary fallback render from terminal team store, got %v", rendered)
	}
}

func TestChatRuntimeEvents_RendersAsyncAssistantSummaryAfterPrimaryAssistantAlreadyRendered(t *testing.T) {
	session := &ChatSession{
		RuntimeSession: &runtimechat.Session{ID: "lead-session"},
		ActiveTeam:     &chatTeamBinding{TeamID: "team-1", AgentID: "lead"},
	}
	bridge := newChatRuntimeEventBridge(session)
	var rendered []string
	bridge.writeLine = func(line string) {
		rendered = append(rendered, line)
	}
	bridge.renderResponse = func(response string) {
		rendered = append(rendered, response)
	}

	bridge.BeginRun()
	bridge.MarkAssistantFinalRendered()
	bridge.handleEvent(runtimeevents.Event{
		Type:      "team.completed",
		SessionID: "lead-session",
		Payload:   map[string]interface{}{"team_id": "team-1", "status": "done"},
	})
	bridge.handleEvent(runtimeevents.Event{
		Type:      runtimechat.EventAssistantMessage,
		SessionID: "lead-session",
		Payload:   map[string]interface{}{"content": "Completed all work after the initial reply."},
	})

	if !containsAllChatTimelineLines(rendered,
		"[team] completed team-1 status=done",
		"[team summary] team-1 Completed all work after the initial reply.",
		"Completed all work after the initial reply.",
	) {
		t.Fatalf("expected async assistant summary to render after primary final message, got %v", rendered)
	}
}

func TestChatRuntimeEvents_RedrawsPromptAfterAsyncRenderWhenSessionIdle(t *testing.T) {
	runtimeStore := runtimechat.NewInMemoryRuntimeStore(16)
	require.NoError(t, runtimeStore.SaveState(context.Background(), &runtimechat.RuntimeState{
		SessionID: "lead-session",
		Status:    runtimechat.SessionIdle,
	}))

	session := &ChatSession{
		RuntimeSession:   &runtimechat.Session{ID: "lead-session"},
		ActiveTeam:       &chatTeamBinding{TeamID: "team-1", AgentID: "lead"},
		LocalRuntimeHost: &localChatRuntimeHost{RuntimeStore: runtimeStore, TeamStore: nil},
	}
	bridge := newChatRuntimeEventBridge(session)
	var rendered []string
	bridge.writeLine = func(line string) {
		rendered = append(rendered, line)
	}
	bridge.writePrompt = func() {
		rendered = append(rendered, "PROMPT")
	}

	bridge.BeginRun()
	bridge.EndRun()
	bridge.handleEvent(runtimeevents.Event{
		Type:      "team.completed",
		SessionID: "lead-session",
		Payload:   map[string]interface{}{"team_id": "team-1", "status": "done"},
	})

	if !containsAllChatTimelineLines(rendered, "[team] completed team-1 status=done", "PROMPT") {
		t.Fatalf("expected prompt redraw after async event, got %v", rendered)
	}
}

func TestChatRuntimeEvents_DoesNotRedrawPromptWhileRunActive(t *testing.T) {
	runtimeStore := runtimechat.NewInMemoryRuntimeStore(16)
	require.NoError(t, runtimeStore.SaveState(context.Background(), &runtimechat.RuntimeState{
		SessionID: "lead-session",
		Status:    runtimechat.SessionIdle,
	}))

	session := &ChatSession{
		RuntimeSession:   &runtimechat.Session{ID: "lead-session"},
		ActiveTeam:       &chatTeamBinding{TeamID: "team-1", AgentID: "lead"},
		LocalRuntimeHost: &localChatRuntimeHost{RuntimeStore: runtimeStore, TeamStore: nil},
	}
	bridge := newChatRuntimeEventBridge(session)
	var rendered []string
	bridge.writeLine = func(line string) {
		rendered = append(rendered, line)
	}
	bridge.writePrompt = func() {
		rendered = append(rendered, "PROMPT")
	}

	bridge.BeginRun()
	bridge.handleEvent(runtimeevents.Event{
		Type:      "team.completed",
		SessionID: "lead-session",
		Payload:   map[string]interface{}{"team_id": "team-1", "status": "done"},
	})

	if containsAllChatTimelineLines(rendered, "PROMPT") {
		t.Fatalf("expected no prompt redraw while run is active, got %v", rendered)
	}
	if !containsAllChatTimelineLines(rendered, "[team] completed team-1 status=done") {
		t.Fatalf("expected async event to still render, got %v", rendered)
	}
}

func TestChatRuntimeEvents_DoesNotRedrawPromptWhileTeamStillActiveAfterRun(t *testing.T) {
	runtimeStore := runtimechat.NewInMemoryRuntimeStore(16)
	require.NoError(t, runtimeStore.SaveState(context.Background(), &runtimechat.RuntimeState{
		SessionID: "lead-session",
		Status:    runtimechat.SessionIdle,
	}))

	store, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	require.NoError(t, err)
	defer store.Close()
	teamID, err := store.CreateTeam(context.Background(), team.Team{
		ID:     "team-1",
		Status: team.TeamStatusActive,
	})
	require.NoError(t, err)

	session := &ChatSession{
		RuntimeSession:   &runtimechat.Session{ID: "lead-session"},
		ActiveTeam:       &chatTeamBinding{TeamID: teamID, AgentID: "lead"},
		LocalRuntimeHost: &localChatRuntimeHost{RuntimeStore: runtimeStore, TeamStore: store},
	}
	bridge := newChatRuntimeEventBridge(session)
	var rendered []string
	bridge.writePrompt = func() {
		rendered = append(rendered, "PROMPT")
	}

	bridge.BeginRun()
	bridge.EndRun()
	bridge.writePromptIfIdle()
	if containsAllChatTimelineLines(rendered, "PROMPT") {
		t.Fatalf("expected no prompt while team remains active, got %v", rendered)
	}

	require.NoError(t, store.UpdateTeamStatus(context.Background(), teamID, team.TeamStatusDone))
	bridge.writePromptIfIdle()
	if !containsAllChatTimelineLines(rendered, "PROMPT") {
		t.Fatalf("expected prompt after team completion, got %v", rendered)
	}
}

func TestTeamRunSettled_IgnoresAmbientTeamRunningPlaceholderState(t *testing.T) {
	store, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	require.NoError(t, err)
	defer store.Close()

	teamID, err := store.CreateTeam(context.Background(), team.Team{
		ID:            "team-1",
		LeadSessionID: "lead-session",
		Status:        team.TeamStatusFailed,
	})
	require.NoError(t, err)

	runtimeStore := runtimechat.NewInMemoryRuntimeStore(16)
	require.NoError(t, runtimeStore.SaveState(context.Background(), &runtimechat.RuntimeState{
		SessionID: "lead-session",
		Status:    runtimechat.SessionIdle,
		AmbientRunMeta: &team.RunMeta{
			Team: &team.TeamRunMeta{
				TeamID: teamID,
			},
		},
	}))

	host := &localChatRuntimeHost{
		RuntimeStore: runtimeStore,
		TeamStore:    store,
	}
	settled, err := host.teamRunSettled(context.Background(), teamID)
	require.NoError(t, err)
	if !settled {
		t.Fatalf("expected ambient team-running placeholder state to be ignored")
	}
}

func TestTeamRunSettled_DoesNotIgnoreAmbientTeamRunningSessionWhileStillRunning(t *testing.T) {
	store, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	require.NoError(t, err)
	defer store.Close()

	teamID, err := store.CreateTeam(context.Background(), team.Team{
		ID:            "team-1",
		LeadSessionID: "lead-session",
		Status:        team.TeamStatusDone,
	})
	require.NoError(t, err)

	runtimeStore := runtimechat.NewInMemoryRuntimeStore(16)
	require.NoError(t, runtimeStore.SaveState(context.Background(), &runtimechat.RuntimeState{
		SessionID: "lead-session",
		Status:    runtimechat.SessionRunning,
		AmbientRunMeta: &team.RunMeta{
			Team: &team.TeamRunMeta{
				TeamID: teamID,
			},
		},
	}))

	host := &localChatRuntimeHost{
		RuntimeStore: runtimeStore,
		TeamStore:    store,
	}
	settled, err := host.teamRunSettled(context.Background(), teamID)
	require.NoError(t, err)
	if settled {
		t.Fatalf("expected running ambient team session to keep team unsettled")
	}
}

func TestSanitizeInteractiveAsyncTeamLaunchResponse_StripsFollowUpDecisionBlock(t *testing.T) {
	raw := `已创建 3 个团队成员来并行探索 docs 目录文档，团队已在后台开始工作。

我会在他们完成后为你汇总：
- 每一组文档的核心内容
- 推荐优先阅读顺序

如果你愿意，我下一步可以继续：
1.. 等团队结果返回后给你总览总结
2.. 现在直接由我先快速浏览 docs 并给你一个即时概览`

	got := sanitizeInteractiveAsyncTeamLaunchResponse(raw)
	if strings.Contains(got, "如果你愿意，我下一步可以继续") {
		t.Fatalf("expected follow-up choice block to be removed, got %q", got)
	}
	if strings.Contains(got, "1.. 等团队结果返回后给你总览总结") {
		t.Fatalf("expected numbered options to be removed, got %q", got)
	}
	if !strings.Contains(got, "团队已在后台开始工作") {
		t.Fatalf("expected background execution notice to remain, got %q", got)
	}
	if !strings.Contains(got, "我会在他们完成后为你汇总") {
		t.Fatalf("expected automatic-summary promise to remain, got %q", got)
	}
}

func TestIsReadOnlyShellCommand(t *testing.T) {
	for _, command := range []string{
		"Get-ChildItem docs",
		"Get-ChildItem docs -Recurse | Select-String README",
		"rg team docs",
		"git diff -- docs",
		"type README.md",
	} {
		if !isReadOnlyShellCommand(command) {
			t.Fatalf("expected read-only shell command to be cacheable: %q", command)
		}
	}
	for _, command := range []string{
		"echo hi > out.txt",
		"Remove-Item temp.txt",
		"mkdir tmp",
		"git commit -m test",
		"cmd /c dir",
	} {
		if isReadOnlyShellCommand(command) {
			t.Fatalf("expected mutating or ambiguous shell command to require approval: %q", command)
		}
	}
}

func TestChatRuntimeEvents_RendersPermissionModeHintOnce(t *testing.T) {
	session := &ChatSession{
		PermissionMode:    runtimepolicy.ModeDefault,
		ApprovalReuseMode: chatApprovalReuseSessionReadOnlyShell,
	}
	bridge := newChatRuntimeEventBridge(session)
	var rendered []string
	bridge.writeLine = func(line string) {
		rendered = append(rendered, line)
	}

	bridge.maybeRenderPermissionModeHint("permission_mode_requires_approval")
	bridge.maybeRenderPermissionModeHint("permission_mode_requires_approval")

	if len(rendered) != 1 {
		t.Fatalf("expected one permission mode hint, got %v", rendered)
	}
	if !strings.Contains(rendered[0], "--yolo") || !strings.Contains(rendered[0], "--approval-reuse=session_readonly_shell") {
		t.Fatalf("unexpected permission mode hint: %q", rendered[0])
	}
}

func TestChatRuntimeEvents_ApprovalPromptHintForReadonlyShell(t *testing.T) {
	bridge := newChatRuntimeEventBridge(&ChatSession{
		ApprovalReuseMode: chatApprovalReuseSessionReadOnlyShell,
	})
	hint := bridge.approvalPromptHint("session-1", &runtimechat.ApprovalRequest{
		ToolName: "execute_shell_command",
		ArgsJSON: []byte(`{"command":"git status --short"}`),
	})
	if !strings.Contains(hint, "readonly_shell") {
		t.Fatalf("expected readonly_shell hint, got %q", hint)
	}
	if !strings.Contains(hint, "当前会话") {
		t.Fatalf("expected session-scoped hint, got %q", hint)
	}
}

func TestChatRuntimeEvents_ApprovalPromptHintForApprovedShell(t *testing.T) {
	bridge := newChatRuntimeEventBridge(&ChatSession{
		ApprovalReuseMode: chatApprovalReuseSessionReadOnlyShell,
	})
	hint := bridge.approvalPromptHint("session-1", &runtimechat.ApprovalRequest{
		ToolName: "execute_shell_command",
		ArgsJSON: []byte(`{"command":"go test ./..."}`),
	})
	if !strings.Contains(hint, "approved_shell") {
		t.Fatalf("expected approved_shell hint, got %q", hint)
	}
	if !strings.Contains(hint, "首次仍需审批") {
		t.Fatalf("expected first-approval hint, got %q", hint)
	}
}

func TestChatRuntimeEvents_ApprovalPromptHintForMutatingShell(t *testing.T) {
	bridge := newChatRuntimeEventBridge(&ChatSession{
		ApprovalReuseMode: chatApprovalReuseSessionReadOnlyShell,
	})
	hint := bridge.approvalPromptHint("session-1", &runtimechat.ApprovalRequest{
		ToolName: "execute_shell_command",
		ArgsJSON: []byte(`{"command":"git add a.txt && git commit -m \"test\"","mutated_paths":["a.txt"]}`),
	})
	if !strings.Contains(hint, "mutated_paths") {
		t.Fatalf("expected mutated_paths hint, got %q", hint)
	}
	if !strings.Contains(hint, "不参与 approval-reuse") {
		t.Fatalf("expected non-reusable hint, got %q", hint)
	}
}

func TestApprovalRequestPreviewLines_ShellCommand(t *testing.T) {
	lines := approvalRequestPreviewLines(&runtimechat.ApprovalRequest{
		ToolName: "execute_shell_command",
		ArgsJSON: []byte(`{"command":"git status --short --branch","workdir":"E:/projects/ai/ai-gateway","mutated_paths":null}`),
	})
	require.Equal(t, []string{
		"command=git status --short --branch",
		"workdir=E:/projects/ai/ai-gateway",
	}, lines)
}

func TestApprovalRequestPreviewLines_BackgroundTaskCwd(t *testing.T) {
	lines := approvalRequestPreviewLines(&runtimechat.ApprovalRequest{
		ToolName: "background_task",
		ArgsJSON: []byte(`{"command":"git status --short --branch","cwd":"E:/projects/ai/ai-gateway"}`),
	})
	require.Equal(t, []string{
		"command=git status --short --branch",
		"cwd=E:/projects/ai/ai-gateway",
	}, lines)
}

func TestApprovalRequestPreviewLines_FallbackArgs(t *testing.T) {
	lines := approvalRequestPreviewLines(&runtimechat.ApprovalRequest{
		ToolName: "team_echo",
		ArgsJSON: []byte(`{"message":"hello"}`),
	})
	require.Equal(t, []string{"args={\"message\":\"hello\"}"}, lines)
}

func TestChatRuntimeEvents_WaitForCurrentEventsWaitsForLateArrivingEvents(t *testing.T) {
	session := &ChatSession{}
	bridge := newChatRuntimeEventBridge(session)
	done := make(chan struct{})
	go func() {
		defer close(done)
		bridge.run()
	}()
	defer func() {
		close(bridge.eventQueue)
		<-done
	}()

	bridge.BeginRun()
	bridge.Handle(runtimeevents.Event{Type: "llm.request.started"})
	go func() {
		time.Sleep(20 * time.Millisecond)
		bridge.Handle(runtimeevents.Event{Type: "tool.completed"})
	}()

	start := time.Now()
	bridge.WaitForCurrentEvents(300 * time.Millisecond)
	elapsed := time.Since(start)

	bridge.progressMu.Lock()
	processed := bridge.processedEvents
	enqueued := bridge.enqueuedEvents
	bridge.progressMu.Unlock()

	if processed < 2 || enqueued < 2 {
		t.Fatalf("expected late event to be included before return, enqueued=%d processed=%d", enqueued, processed)
	}
	if elapsed < 20*time.Millisecond {
		t.Fatalf("expected wait to stay pending for late event arrival, got %v", elapsed)
	}
}

func TestChatRuntimeEvents_HandleDoesNotDropEventsWhenQueueBacksUp(t *testing.T) {
	session := &ChatSession{
		Stream:         true,
		RuntimeSession: &runtimechat.Session{ID: "lead-session"},
	}
	bridge := newChatRuntimeEventBridge(session)
	bridge.eventQueue = make(chan runtimeevents.Event, 1)

	var (
		mu      sync.Mutex
		deltas  []string
		started = make(chan struct{}, 1)
		release = make(chan struct{})
	)
	bridge.writeDelta = func(delta string) {
		mu.Lock()
		deltas = append(deltas, delta)
		mu.Unlock()
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		bridge.run()
	}()
	defer func() {
		close(bridge.eventQueue)
		<-done
	}()

	bridge.BeginRun()
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		bridge.Handle(runtimeevents.Event{
			Type:      runtimechat.EventAssistantDelta,
			SessionID: "lead-session",
			Payload:   map[string]interface{}{"delta": "Hello"},
		})
	}()

	<-started

	secondDone := make(chan struct{})
	go func() {
		defer close(secondDone)
		bridge.Handle(runtimeevents.Event{
			Type:      runtimechat.EventAssistantDelta,
			SessionID: "lead-session",
			Payload:   map[string]interface{}{"delta": " world"},
		})
	}()

	<-secondDone

	thirdDone := make(chan struct{})
	go func() {
		defer close(thirdDone)
		bridge.Handle(runtimeevents.Event{
			Type:      runtimechat.EventAssistantDelta,
			SessionID: "lead-session",
			Payload:   map[string]interface{}{"delta": "!"},
		})
	}()

	select {
	case <-thirdDone:
		t.Fatal("expected third Handle call to block until queue space was available")
	case <-time.After(30 * time.Millisecond):
	}

	close(release)
	<-firstDone
	<-thirdDone
	bridge.WaitForCurrentEvents(300 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []string{"Hello", " world", "!"}, deltas)
}

func TestChatRuntimeEvents_RendersAssistantDeltaAndFinalizesWithoutRepeatingResponse(t *testing.T) {
	session := &ChatSession{
		Stream:         true,
		RuntimeSession: &runtimechat.Session{ID: "lead-session"},
	}
	bridge := newChatRuntimeEventBridge(session)
	var deltas []string
	finalized := 0
	renderedResponses := 0
	bridge.writeDelta = func(delta string) {
		deltas = append(deltas, delta)
	}
	bridge.finalizeDelta = func() {
		finalized++
	}
	bridge.renderResponse = func(response string) {
		renderedResponses++
	}

	bridge.BeginRun()
	bridge.handleEvent(runtimeevents.Event{
		Type:      runtimechat.EventAssistantDelta,
		SessionID: "lead-session",
		Payload:   map[string]interface{}{"delta": "Hello"},
	})
	bridge.handleEvent(runtimeevents.Event{
		Type:      runtimechat.EventAssistantMessage,
		SessionID: "lead-session",
		Payload:   map[string]interface{}{"content": "Hello"},
	})

	if len(deltas) != 1 || deltas[0] != "Hello" {
		t.Fatalf("expected one rendered delta, got %v", deltas)
	}
	if finalized != 1 {
		t.Fatalf("expected one delta finalization, got %d", finalized)
	}
	if renderedResponses != 0 {
		t.Fatalf("expected final response fallback to stay suppressed, got %d renders", renderedResponses)
	}
	if !bridge.HasRenderedAssistantDelta() {
		t.Fatal("expected bridge to remember rendered assistant delta")
	}
	if !bridge.HasRenderedAssistantFinal() {
		t.Fatal("expected bridge to remember rendered assistant final output")
	}
}

func TestChatRuntimeEvents_CompletesAssistantDeltaWithFinalMessageContent(t *testing.T) {
	session := &ChatSession{
		Stream:         true,
		RuntimeSession: &runtimechat.Session{ID: "lead-session"},
	}
	bridge := newChatRuntimeEventBridge(session)
	var deltas []string
	var completed []string
	finalized := 0
	renderedResponses := 0
	bridge.writeDelta = func(delta string) {
		deltas = append(deltas, delta)
	}
	bridge.completeDelta = func(content string) bool {
		completed = append(completed, content)
		return true
	}
	bridge.finalizeDelta = func() {
		finalized++
	}
	bridge.renderResponse = func(response string) {
		renderedResponses++
	}

	bridge.BeginRun()
	bridge.handleEvent(runtimeevents.Event{
		Type:      runtimechat.EventAssistantDelta,
		SessionID: "lead-session",
		Payload:   map[string]interface{}{"delta": "`E:\\projects\\ai"},
	})
	bridge.handleEvent(runtimeevents.Event{
		Type:      runtimechat.EventAssistantMessage,
		SessionID: "lead-session",
		Payload: map[string]interface{}{
			"content": "`E:\\projects\\ai\\ai-gateway` 的 git 状态如下：\n\n- 当前分支：`main`",
		},
	})

	require.Equal(t, []string{"`E:\\projects\\ai"}, deltas)
	require.Equal(t, []string{"`E:\\projects\\ai\\ai-gateway` 的 git 状态如下：\n\n- 当前分支：`main`"}, completed)
	require.Equal(t, 0, finalized)
	require.Equal(t, 0, renderedResponses)
	require.True(t, bridge.HasRenderedAssistantDelta())
	require.True(t, bridge.HasRenderedAssistantFinal())
}

func TestChatRuntimeEvents_MarksAssistantDeltaRenderedBeforeSlowWriteCompletes(t *testing.T) {
	session := &ChatSession{
		Stream:         true,
		RuntimeSession: &runtimechat.Session{ID: "lead-session"},
	}
	bridge := newChatRuntimeEventBridge(session)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	bridge.writeDelta = func(delta string) {
		started <- struct{}{}
		<-release
	}

	done := make(chan struct{})
	go func() {
		bridge.handleEvent(runtimeevents.Event{
			Type:      runtimechat.EventAssistantDelta,
			SessionID: "lead-session",
			Payload:   map[string]interface{}{"delta": "Hello"},
		})
		close(done)
	}()

	<-started
	if !bridge.HasRenderedAssistantDelta() {
		t.Fatal("expected delta rendered flag to flip before slow write returns")
	}
	close(release)
	<-done
}

func TestChatRuntimeEvents_PreservesWhitespaceInAssistantDelta(t *testing.T) {
	session := &ChatSession{
		Stream:         true,
		RuntimeSession: &runtimechat.Session{ID: "lead-session"},
	}
	bridge := newChatRuntimeEventBridge(session)
	var deltas []string
	bridge.writeDelta = func(delta string) {
		deltas = append(deltas, delta)
	}

	bridge.BeginRun()
	bridge.handleEvent(runtimeevents.Event{
		Type:      runtimechat.EventAssistantDelta,
		SessionID: "lead-session",
		Payload:   map[string]interface{}{"delta": " world"},
	})

	if len(deltas) != 1 || deltas[0] != " world" {
		t.Fatalf("expected delta whitespace to be preserved, got %v", deltas)
	}
}

func TestChatRuntimeEvents_WaitForCurrentEventsDrainsQueuedAssistantDelta(t *testing.T) {
	session := &ChatSession{
		Stream:         true,
		RuntimeSession: &runtimechat.Session{ID: "lead-session"},
	}
	bridge := newChatRuntimeEventBridge(session)
	bridge.startOnce.Do(func() {})
	go bridge.run()
	defer close(bridge.eventQueue)

	bridge.BeginRun()
	bridge.Handle(runtimeevents.Event{
		Type:      runtimechat.EventAssistantDelta,
		SessionID: "lead-session",
		Payload:   map[string]interface{}{"delta": "Hello"},
	})
	bridge.WaitForCurrentEvents(200 * time.Millisecond)

	if !bridge.HasRenderedAssistantDelta() {
		t.Fatal("expected queued assistant delta to be rendered after drain")
	}
}

func TestChatRuntimeEvents_SuppressesLLMFinishedLineDuringActiveAssistantStream(t *testing.T) {
	session := &ChatSession{
		Stream:         true,
		RuntimeSession: &runtimechat.Session{ID: "lead-session"},
	}
	bridge := newChatRuntimeEventBridge(session)
	var lines []string
	finalized := 0
	bridge.writeDelta = func(string) {}
	bridge.writeLine = func(line string) {
		lines = append(lines, line)
	}
	bridge.finalizeDelta = func() {
		finalized++
	}

	bridge.BeginRun()
	bridge.handleEvent(runtimeevents.Event{
		Type:      runtimechat.EventAssistantDelta,
		SessionID: "lead-session",
		Payload:   map[string]interface{}{"delta": "Hello"},
	})
	bridge.handleEvent(runtimeevents.Event{
		Type:      "llm.request.finished",
		SessionID: "lead-session",
		Payload:   map[string]interface{}{"success": true},
	})
	bridge.handleEvent(runtimeevents.Event{
		Type:      runtimechat.EventAssistantMessage,
		SessionID: "lead-session",
		Payload:   map[string]interface{}{"content": "Hello"},
	})

	if finalized != 1 {
		t.Fatalf("expected finalization after assistant message, got %d", finalized)
	}
	for _, line := range lines {
		if strings.Contains(line, "model responded") {
			t.Fatalf("expected llm finished line to stay suppressed during active stream, got %v", lines)
		}
	}
}

func TestActorExecutor_AnswersPendingQuestionThroughCLIBridge(t *testing.T) {
	manager, userID, dir, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	runtimeSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}

	provider := &questionToolProvider{}
	llmRuntime := runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{
		DefaultProvider: "test-provider",
		DefaultModel:    "test-model",
	})
	if err := llmRuntime.RegisterProvider("test-provider", provider); err != nil {
		t.Fatalf("RegisterProvider: %v", err)
	}
	if err := llmRuntime.RegisterProviderAlias("test-model", "test-provider"); err != nil {
		t.Fatalf("RegisterProviderAlias: %v", err)
	}

	host := &localChatRuntimeHost{
		EventBus:     runtimeevents.NewBusWithRetention(64),
		SessionStore: manager.GetStorage(),
		SessionUser:  userID,
	}
	host.SessionHub = runtimechat.NewSessionHub(func(sessionID string) (*runtimechat.SessionActor, error) {
		runtimeStore := runtimechat.NewInMemoryRuntimeStore(64)
		a := agent.NewAgentWithLLM(&agent.Config{
			Name:     "bridge-test",
			Provider: "test-provider",
			Model:    "test-model",
			MaxSteps: 4,
		}, nil, llmRuntime)
		return runtimechat.NewSessionActor(sessionID, runtimechat.SessionActorConfig{
			Agent:        a,
			LLMRuntime:   llmRuntime,
			SessionStore: manager.GetStorage(),
			StateStore:   runtimeStore,
			EventStore:   runtimeStore,
			EventBus:     host.EventBus,
		})
	})

	session := &ChatSession{
		ProviderName:     "test-provider",
		Model:            "test-model",
		SessionManager:   manager,
		RuntimeSession:   runtimeSession,
		SessionUserID:    userID,
		SessionDir:       dir,
		LocalRuntimeHost: host,
		ChatExecutor:     newAICLIActorChatExecutor(),
	}
	host.BaseSession = session

	var rendered bytes.Buffer
	bridge := newChatRuntimeEventBridge(session)
	bridge.writeLine = func(line string) {
		rendered.WriteString(line)
		rendered.WriteString("\n")
	}
	bridge.askQuestion = func(prompt string, suggestions []string, required bool) (string, error) {
		if prompt != "Need user input" {
			t.Fatalf("unexpected prompt: %q", prompt)
		}
		return "provided answer", nil
	}
	session.RuntimeEventBridge = bridge

	output, err := session.ChatExecutor.Execute(context.Background(), session, "trigger question")
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if output != "question answered" {
		t.Fatalf("unexpected output: %q", output)
	}
	if provider.callCount.Load() < 2 {
		t.Fatalf("expected provider to be called twice, got %d", provider.callCount.Load())
	}
	if rendered.Len() == 0 {
		t.Fatal("expected runtime event output")
	}
}

func TestActorExecutor_AskUserQuestionAnswerSurvivesReducerAndStreamFallback(t *testing.T) {
	manager, userID, dir, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	runtimeSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}

	provider := &answerPreservingQuestionProvider{}
	llmRuntime := runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{
		DefaultProvider: "test-provider",
		DefaultModel:    "test-model",
	})
	if err := llmRuntime.RegisterProvider("test-provider", provider); err != nil {
		t.Fatalf("RegisterProvider: %v", err)
	}
	if err := llmRuntime.RegisterProviderAlias("test-model", "test-provider"); err != nil {
		t.Fatalf("RegisterProviderAlias: %v", err)
	}

	host := &localChatRuntimeHost{
		EventBus:     runtimeevents.NewBusWithRetention(64),
		SessionStore: manager.GetStorage(),
		SessionUser:  userID,
	}
	host.SessionHub = runtimechat.NewSessionHub(func(sessionID string) (*runtimechat.SessionActor, error) {
		runtimeStore := runtimechat.NewInMemoryRuntimeStore(64)
		a := agent.NewAgentWithLLM(&agent.Config{
			Name:     "bridge-test",
			Provider: "test-provider",
			Model:    "test-model",
			MaxSteps: 4,
		}, nil, llmRuntime)
		return runtimechat.NewSessionActor(sessionID, runtimechat.SessionActorConfig{
			Agent:        a,
			LLMRuntime:   llmRuntime,
			SessionStore: manager.GetStorage(),
			StateStore:   runtimeStore,
			EventStore:   runtimeStore,
			EventBus:     host.EventBus,
		})
	})

	session := &ChatSession{
		ProviderName:     "test-provider",
		Model:            "test-model",
		Stream:           true,
		SessionManager:   manager,
		RuntimeSession:   runtimeSession,
		SessionUserID:    userID,
		SessionDir:       dir,
		LocalRuntimeHost: host,
		ChatExecutor:     newAICLIActorChatExecutor(),
	}
	host.BaseSession = session

	var rendered bytes.Buffer
	bridge := newChatRuntimeEventBridge(session)
	bridge.writeLine = func(line string) {
		rendered.WriteString(line)
		rendered.WriteString("\n")
	}
	bridge.askQuestion = func(prompt string, suggestions []string, required bool) (string, error) {
		if prompt != "Need user input" {
			t.Fatalf("unexpected prompt: %q", prompt)
		}
		return "provided answer 42", nil
	}
	session.RuntimeEventBridge = bridge

	output, err := session.ChatExecutor.Execute(context.Background(), session, "trigger preserved answer")
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if output != "answer survived: provided answer 42" {
		t.Fatalf("unexpected output: %q", output)
	}
	if !shouldDisplayFinalResponse(session, output) {
		t.Fatalf("expected actor stream fallback response to be displayable, got %q", output)
	}
	if !provider.answerObserved() {
		t.Fatalf("expected provider to observe preserved answer, saw content=%q metadata=%v", provider.toolContent(), provider.toolMetadata())
	}
	if !strings.Contains(rendered.String(), "[question] Need user input") {
		t.Fatalf("expected rendered question timeline, got %q", rendered.String())
	}

	reloaded, err := manager.Get(context.Background(), runtimeSession.ID)
	if err != nil {
		t.Fatalf("manager.Get: %v", err)
	}
	toolMessage := latestToolMessage(reloaded.History)
	if toolMessage == nil {
		t.Fatalf("expected persisted tool message, got %+v", reloaded.History)
	}
	if !strings.Contains(toolMessage.Content, "answer=provided answer 42") {
		t.Fatalf("expected persisted tool message to preserve answer, got %+v", toolMessage)
	}
	if toolMessage.Metadata.GetString("reducer", "") != "json_summary" {
		t.Fatalf("expected json_summary reducer metadata, got %+v", toolMessage.Metadata)
	}
}

func TestActorExecutor_ApprovalThroughCLIBridgeExecutesToolOnceAndResumes(t *testing.T) {
	manager, userID, dir, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	runtimeSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}

	provider := &approvalToolProvider{}
	llmRuntime := runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{
		DefaultProvider: "test-provider",
		DefaultModel:    "test-model",
	})
	if err := llmRuntime.RegisterProvider("test-provider", provider); err != nil {
		t.Fatalf("RegisterProvider: %v", err)
	}
	if err := llmRuntime.RegisterProviderAlias("test-model", "test-provider"); err != nil {
		t.Fatalf("RegisterProviderAlias: %v", err)
	}

	mcpManager := &approvalCapturingMCPManager{}
	host := &localChatRuntimeHost{
		EventBus:     runtimeevents.NewBusWithRetention(64),
		SessionStore: manager.GetStorage(),
		SessionUser:  userID,
	}
	host.SessionHub = runtimechat.NewSessionHub(func(sessionID string) (*runtimechat.SessionActor, error) {
		runtimeStore := runtimechat.NewInMemoryRuntimeStore(64)
		a := agent.NewAgentWithLLM(&agent.Config{
			Name:     "bridge-test",
			Provider: "test-provider",
			Model:    "test-model",
			MaxSteps: 4,
		}, mcpManager, llmRuntime)
		a.SetPermissionEngine(&agent.PermissionEngine{
			Callback: func(ctx context.Context, req runtimepolicy.EvalRequest) (runtimepolicy.Decision, string, error) {
				if req.ToolName == "team_echo" {
					return runtimepolicy.Decision{Type: runtimepolicy.DecisionAsk}, "manual approval", nil
				}
				return runtimepolicy.Decision{Type: runtimepolicy.DecisionAllow}, "", nil
			},
		})
		return runtimechat.NewSessionActor(sessionID, runtimechat.SessionActorConfig{
			Agent:        a,
			LLMRuntime:   llmRuntime,
			SessionStore: manager.GetStorage(),
			StateStore:   runtimeStore,
			EventStore:   runtimeStore,
			EventBus:     host.EventBus,
		})
	})

	session := &ChatSession{
		ProviderName:     "test-provider",
		Model:            "test-model",
		Stream:           true,
		SessionManager:   manager,
		RuntimeSession:   runtimeSession,
		SessionUserID:    userID,
		SessionDir:       dir,
		LocalRuntimeHost: host,
		ChatExecutor:     newAICLIActorChatExecutor(),
		ActiveTeam:       &chatTeamBinding{TeamID: "team-approval", AgentID: "mate-approval", TaskID: "task-approval"},
	}
	host.BaseSession = session

	var (
		rendered      bytes.Buffer
		approvalCalls atomic.Int32
	)
	bridge := newChatRuntimeEventBridge(session)
	bridge.writeLine = func(line string) {
		rendered.WriteString(line)
		rendered.WriteString("\n")
	}
	bridge.askApproval = func(approval *runtimechat.ApprovalRequest) (bool, error) {
		approvalCalls.Add(1)
		if approval == nil || approval.ToolName != "team_echo" {
			t.Fatalf("unexpected approval request: %+v", approval)
		}
		if approval.Reason != "manual approval" {
			t.Fatalf("unexpected approval reason: %q", approval.Reason)
		}
		return true, nil
	}
	session.RuntimeEventBridge = bridge

	output, err := session.ChatExecutor.Execute(context.Background(), session, "trigger approval")
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if output != "approval survived and resumed" {
		t.Fatalf("unexpected output: %q", output)
	}
	if !shouldDisplayFinalResponse(session, output) {
		t.Fatalf("expected actor stream fallback response to be displayable, got %q", output)
	}
	if approvalCalls.Load() != 1 {
		t.Fatalf("expected exactly one approval prompt, got %d", approvalCalls.Load())
	}
	if mcpManager.callCount != 1 {
		t.Fatalf("expected approved tool execution exactly once, got %d", mcpManager.callCount)
	}
	if mcpManager.lastMeta == nil || mcpManager.lastMeta.Team == nil {
		t.Fatalf("expected run meta on approved tool execution, got %+v", mcpManager.lastMeta)
	}
	if mcpManager.lastMeta.Team.TeamID != "team-approval" || mcpManager.lastMeta.Team.AgentID != "mate-approval" || mcpManager.lastMeta.Team.CurrentTaskID != "task-approval" {
		t.Fatalf("unexpected run meta on approved tool execution: %+v", mcpManager.lastMeta)
	}
	if strings.Contains(rendered.String(), "[approval] team_echo") {
		t.Fatalf("expected interactive approval timeline noise to stay suppressed, got %q", rendered.String())
	}
	if strings.Contains(rendered.String(), "[approval] approved team_echo, executing...") {
		t.Fatalf("expected post-approval execution noise to stay suppressed, got %q", rendered.String())
	}
	if strings.Contains(rendered.String(), "[tool denied]") {
		t.Fatalf("expected no tool denial after approval, got %q", rendered.String())
	}

	reloaded, err := manager.Get(context.Background(), runtimeSession.ID)
	if err != nil {
		t.Fatalf("manager.Get: %v", err)
	}
	toolMessage := latestToolMessage(reloaded.History)
	if toolMessage == nil {
		t.Fatalf("expected persisted tool message, got %+v", reloaded.History)
	}
	if !strings.Contains(toolMessage.Content, "approved echo: hello") {
		t.Fatalf("expected persisted approved tool output, got %+v", toolMessage)
	}
}

func TestChatRuntimeEvents_SerializesConcurrentApprovalsAndQuestions(t *testing.T) {
	manager, userID, dir, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	leadSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create lead: %v", err)
	}
	teammateSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create teammate: %v", err)
	}

	provider := &taggedQuestionToolProvider{}
	llmRuntime := runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{
		DefaultProvider: "test-provider",
		DefaultModel:    "test-model",
	})
	if err := llmRuntime.RegisterProvider("test-provider", provider); err != nil {
		t.Fatalf("RegisterProvider: %v", err)
	}
	if err := llmRuntime.RegisterProviderAlias("test-model", "test-provider"); err != nil {
		t.Fatalf("RegisterProviderAlias: %v", err)
	}

	host := &localChatRuntimeHost{
		EventBus:     runtimeevents.NewBusWithRetention(64),
		SessionStore: manager.GetStorage(),
		SessionUser:  userID,
	}
	host.SessionHub = runtimechat.NewSessionHub(func(sessionID string) (*runtimechat.SessionActor, error) {
		runtimeStore := runtimechat.NewInMemoryRuntimeStore(64)
		a := agent.NewAgentWithLLM(&agent.Config{
			Name:     "bridge-test",
			Provider: "test-provider",
			Model:    "test-model",
			MaxSteps: 4,
		}, nil, llmRuntime)
		return runtimechat.NewSessionActor(sessionID, runtimechat.SessionActorConfig{
			Agent:        a,
			LLMRuntime:   llmRuntime,
			SessionStore: manager.GetStorage(),
			StateStore:   runtimeStore,
			EventStore:   runtimeStore,
			EventBus:     host.EventBus,
		})
	})

	session := &ChatSession{
		ProviderName:     "test-provider",
		Model:            "test-model",
		SessionManager:   manager,
		RuntimeSession:   leadSession,
		SessionUserID:    userID,
		SessionDir:       dir,
		LocalRuntimeHost: host,
		ChatExecutor:     newAICLIActorChatExecutor(),
	}
	host.BaseSession = session

	bridge := newChatRuntimeEventBridge(session)
	bridge.writeLine = func(string) {}
	var activePrompts atomic.Int32
	var maxConcurrent atomic.Int32
	started := make(chan string, 2)
	releaseFirst := make(chan struct{})
	var firstPrompt sync.Once
	bridge.askQuestion = func(prompt string, suggestions []string, required bool) (string, error) {
		current := activePrompts.Add(1)
		for {
			observed := maxConcurrent.Load()
			if current <= observed || maxConcurrent.CompareAndSwap(observed, current) {
				break
			}
		}
		started <- prompt
		firstPrompt.Do(func() {
			<-releaseFirst
		})
		activePrompts.Add(-1)
		return "provided answer", nil
	}
	session.RuntimeEventBridge = bridge
	bridge.start()

	leadErrCh := make(chan error, 1)
	go func() {
		_, execErr := session.ChatExecutor.Execute(context.Background(), session, "lead question")
		leadErrCh <- execErr
	}()

	var first string
	select {
	case first = <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first question prompt")
	}
	if first != "Need user input: lead question" {
		t.Fatalf("unexpected first prompt: %q", first)
	}

	teammateActor, err := host.SessionHub.GetOrCreate(teammateSession.ID)
	if err != nil {
		t.Fatalf("GetOrCreate teammate actor: %v", err)
	}
	teammateErrCh := make(chan error, 1)
	go func() {
		_, submitErr := teammateActor.SubmitPrompt(context.Background(), "teammate question", nil)
		teammateErrCh <- submitErr
	}()

	select {
	case prompt := <-started:
		t.Fatalf("second prompt should stay queued until the first is answered, got %q", prompt)
	case <-time.After(200 * time.Millisecond):
	}

	close(releaseFirst)

	var second string
	select {
	case second = <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for queued second prompt")
	}
	if second != "Need user input: teammate question" {
		t.Fatalf("unexpected second prompt: %q", second)
	}
	if maxConcurrent.Load() != 1 {
		t.Fatalf("expected prompts to stay serialized, max concurrency = %d", maxConcurrent.Load())
	}
	if err := <-leadErrCh; err != nil {
		t.Fatalf("lead Execute failed: %v", err)
	}
	if err := <-teammateErrCh; err != nil {
		t.Fatalf("teammate SubmitPrompt failed: %v", err)
	}
}

func TestChatRuntimeEvents_ReusesReadOnlyShellApprovalWithinSameTeamRun(t *testing.T) {
	manager, userID, dir, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	runtimeSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}

	provider := &cachedShellApprovalProvider{}
	llmRuntime := runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{
		DefaultProvider: "test-provider",
		DefaultModel:    "test-model",
	})
	if err := llmRuntime.RegisterProvider("test-provider", provider); err != nil {
		t.Fatalf("RegisterProvider: %v", err)
	}
	if err := llmRuntime.RegisterProviderAlias("test-model", "test-provider"); err != nil {
		t.Fatalf("RegisterProviderAlias: %v", err)
	}

	mcpManager := &shellApprovalCapturingMCPManager{}
	host := &localChatRuntimeHost{
		EventBus:     runtimeevents.NewBusWithRetention(64),
		SessionStore: manager.GetStorage(),
		SessionUser:  userID,
	}
	host.SessionHub = runtimechat.NewSessionHub(func(sessionID string) (*runtimechat.SessionActor, error) {
		runtimeStore := runtimechat.NewInMemoryRuntimeStore(64)
		a := agent.NewAgentWithLLM(&agent.Config{
			Name:     "bridge-test",
			Provider: "test-provider",
			Model:    "test-model",
			MaxSteps: 6,
		}, mcpManager, llmRuntime)
		a.SetPermissionEngine(&agent.PermissionEngine{
			Callback: func(ctx context.Context, req runtimepolicy.EvalRequest) (runtimepolicy.Decision, string, error) {
				switch req.ToolName {
				case "bash", "execute_shell_command":
					return runtimepolicy.Decision{Type: runtimepolicy.DecisionAsk}, "manual approval", nil
				default:
					return runtimepolicy.Decision{Type: runtimepolicy.DecisionAllow}, "", nil
				}
			},
		})
		return runtimechat.NewSessionActor(sessionID, runtimechat.SessionActorConfig{
			Agent:        a,
			LLMRuntime:   llmRuntime,
			SessionStore: manager.GetStorage(),
			StateStore:   runtimeStore,
			EventStore:   runtimeStore,
			EventBus:     host.EventBus,
		})
	})

	session := &ChatSession{
		ProviderName:      "test-provider",
		Model:             "test-model",
		Stream:            true,
		SessionManager:    manager,
		RuntimeSession:    runtimeSession,
		SessionUserID:     userID,
		SessionDir:        dir,
		LocalRuntimeHost:  host,
		ChatExecutor:      newAICLIActorChatExecutor(),
		ApprovalReuseMode: chatApprovalReuseTeamReadOnlyShell,
		ActiveTeam:        &chatTeamBinding{TeamID: "team-approval", AgentID: "lead", TaskID: "task-approval"},
	}
	host.BaseSession = session

	var (
		rendered      bytes.Buffer
		approvalCalls atomic.Int32
	)
	bridge := newChatRuntimeEventBridge(session)
	bridge.writeLine = func(line string) {
		rendered.WriteString(line)
		rendered.WriteString("\n")
	}
	bridge.askApproval = func(approval *runtimechat.ApprovalRequest) (bool, error) {
		approvalCalls.Add(1)
		if approval == nil {
			t.Fatal("expected approval request")
		}
		if approval.Reason != "manual approval" {
			t.Fatalf("unexpected approval reason: %q", approval.Reason)
		}
		return true, nil
	}
	session.RuntimeEventBridge = bridge

	output, err := session.ChatExecutor.Execute(context.Background(), session, "trigger cached shell approvals")
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if output != "shell approvals reused" {
		t.Fatalf("unexpected output: %q", output)
	}
	if approvalCalls.Load() != 1 {
		t.Fatalf("expected a single interactive approval prompt, got %d", approvalCalls.Load())
	}
	if mcpManager.callCount != 2 {
		t.Fatalf("expected both shell tools to execute, got %d", mcpManager.callCount)
	}
	if strings.Contains(rendered.String(), "[approval] execute_shell_command") {
		t.Fatalf("expected interactive approval line to stay suppressed, got %q", rendered.String())
	}
	if strings.Contains(rendered.String(), "[approval] approved execute_shell_command, executing...") {
		t.Fatalf("expected post-approval execution noise to stay suppressed, got %q", rendered.String())
	}
	if strings.Contains(rendered.String(), "[approval] bash") {
		t.Fatalf("expected cached approval for bash to stay silent, got %q", rendered.String())
	}
	if strings.Contains(rendered.String(), "[approval] auto-approved bash") {
		t.Fatalf("expected no auto-approved line for cached bash approval, got %q", rendered.String())
	}
}

func TestChatRuntimeEvents_ApprovalReusePersistsAcrossTurnsForSameTeam(t *testing.T) {
	bridge := newChatRuntimeEventBridge(&ChatSession{
		ApprovalReuseMode: chatApprovalReuseTeamReadOnlyShell,
		ActiveTeam:        &chatTeamBinding{TeamID: "team-1", AgentID: "lead"},
	})
	bridge.BeginRun()

	approval := &runtimechat.ApprovalRequest{
		ToolName: "bash",
		ArgsJSON: []byte(`{"command":"Get-ChildItem docs"}`),
	}
	key := bridge.autoApprovalGrantKey("session-1", approval)
	if key == "" {
		t.Fatal("expected non-empty team-scoped approval key")
	}
	bridge.rememberApprovalGrant(key)

	bridge.BeginRun()
	if !bridge.hasApprovalGrant(key) {
		t.Fatalf("expected approval grant to persist across turns for same team")
	}
}

func TestChatRuntimeEvents_ApprovalReuseDoesNotApplyWithoutActiveTeam(t *testing.T) {
	bridge := newChatRuntimeEventBridge(&ChatSession{})
	approval := &runtimechat.ApprovalRequest{
		ToolName: "bash",
		ArgsJSON: []byte(`{"command":"Get-ChildItem docs"}`),
	}
	if key := bridge.autoApprovalGrantKey("session-1", approval); key != "" {
		t.Fatalf("expected no approval reuse scope without active team, got %q", key)
	}
}

func TestChatRuntimeEvents_SessionReadOnlyShellScopeWithoutTeam(t *testing.T) {
	bridge := newChatRuntimeEventBridge(&ChatSession{
		ApprovalReuseMode: chatApprovalReuseSessionReadOnlyShell,
		// No ActiveTeam — session_readonly_shell should still work.
	})
	approval := &runtimechat.ApprovalRequest{
		ToolName: "execute_shell_command",
		ArgsJSON: []byte(`{"command":"dir"}`),
	}
	key := bridge.autoApprovalGrantKey("session-abc", approval)
	if key == "" {
		t.Fatal("expected session-scoped approval key without active team")
	}
	if !strings.HasPrefix(key, "session:") {
		t.Fatalf("expected session-scoped key, got %q", key)
	}
}

func TestChatRuntimeEvents_SessionReadOnlyShellScopePersistsAcrossTurns(t *testing.T) {
	bridge := newChatRuntimeEventBridge(&ChatSession{
		ApprovalReuseMode: chatApprovalReuseSessionReadOnlyShell,
	})
	bridge.BeginRun()

	approval := &runtimechat.ApprovalRequest{
		ToolName: "bash",
		ArgsJSON: []byte(`{"command":"Get-ChildItem docs"}`),
	}
	key := bridge.autoApprovalGrantKey("session-1", approval)
	if key == "" {
		t.Fatal("expected non-empty session-scoped approval key")
	}
	bridge.rememberApprovalGrant(key)

	bridge.BeginRun()
	if !bridge.hasApprovalGrant(key) {
		t.Fatalf("expected approval grant to persist across turns for session scope")
	}
}

func TestChatRuntimeEvents_TeamReadOnlyShellStillRequiresTeam(t *testing.T) {
	// team_readonly_shell without ActiveTeam should return empty scope.
	bridge := newChatRuntimeEventBridge(&ChatSession{
		ApprovalReuseMode: chatApprovalReuseTeamReadOnlyShell,
	})
	approval := &runtimechat.ApprovalRequest{
		ToolName: "bash",
		ArgsJSON: []byte(`{"command":"dir"}`),
	}
	if key := bridge.autoApprovalGrantKey("session-1", approval); key != "" {
		t.Fatalf("expected empty key for team_readonly_shell without ActiveTeam, got %q", key)
	}
}

func TestChatRuntimeEvents_ReadOnlyNetworkToolsApprovalReuse(t *testing.T) {
	bridge := newChatRuntimeEventBridge(&ChatSession{
		ApprovalReuseMode: chatApprovalReuseSessionReadOnlyShell,
	})
	bridge.BeginRun()

	// web_search should produce a readonly_network grant key.
	webSearchApproval := &runtimechat.ApprovalRequest{
		ToolName: "web_search",
		ArgsJSON: []byte(`{"query":"golang testing"}`),
	}
	key := bridge.autoApprovalGrantKey("session-1", webSearchApproval)
	if key == "" {
		t.Fatal("expected non-empty approval grant key for web_search")
	}
	if !strings.Contains(key, "readonly_network") {
		t.Fatalf("expected readonly_network in key, got %q", key)
	}
	bridge.rememberApprovalGrant(key)

	// Second web_search should be auto-approved.
	if !bridge.hasApprovalGrant(key) {
		t.Fatal("expected approval grant to exist for subsequent web_search")
	}
}

func TestChatRuntimeEvents_SourcegraphApprovalReuse(t *testing.T) {
	bridge := newChatRuntimeEventBridge(&ChatSession{
		ApprovalReuseMode: chatApprovalReuseSessionReadOnlyShell,
	})
	bridge.BeginRun()

	approval := &runtimechat.ApprovalRequest{
		ToolName: "sourcegraph",
		ArgsJSON: []byte(`{"query":"func approvalGrantFamily"}`),
	}
	key := bridge.autoApprovalGrantKey("session-1", approval)
	if key == "" {
		t.Fatal("expected non-empty approval grant key for sourcegraph")
	}
	if !strings.Contains(key, "readonly_network") {
		t.Fatalf("expected readonly_network in key, got %q", key)
	}
}

func TestChatRuntimeEvents_FetchApprovalReuse(t *testing.T) {
	bridge := newChatRuntimeEventBridge(&ChatSession{
		ApprovalReuseMode: chatApprovalReuseSessionReadOnlyShell,
	})
	bridge.BeginRun()

	approval := &runtimechat.ApprovalRequest{
		ToolName: "fetch",
		ArgsJSON: []byte(`{"url":"https://example.com"}`),
	}
	key := bridge.autoApprovalGrantKey("session-1", approval)
	if key == "" {
		t.Fatal("expected non-empty approval grant key for fetch")
	}
	if !strings.Contains(key, "readonly_network") {
		t.Fatalf("expected readonly_network in key, got %q", key)
	}
}

func TestChatRuntimeEvents_NetworkAndShellGrantsAreSeparateFamilies(t *testing.T) {
	bridge := newChatRuntimeEventBridge(&ChatSession{
		ApprovalReuseMode: chatApprovalReuseSessionReadOnlyShell,
	})
	bridge.BeginRun()

	shellApproval := &runtimechat.ApprovalRequest{
		ToolName: "bash",
		ArgsJSON: []byte(`{"command":"ls"}`),
	}
	shellKey := bridge.autoApprovalGrantKey("session-1", shellApproval)

	networkApproval := &runtimechat.ApprovalRequest{
		ToolName: "web_search",
		ArgsJSON: []byte(`{"query":"test"}`),
	}
	networkKey := bridge.autoApprovalGrantKey("session-1", networkApproval)

	if shellKey == networkKey {
		t.Fatalf("shell and network grants should have different keys, got same: %q", shellKey)
	}
	if !strings.Contains(shellKey, "readonly_shell") {
		t.Fatalf("expected readonly_shell in shell key, got %q", shellKey)
	}
	if !strings.Contains(networkKey, "readonly_network") {
		t.Fatalf("expected readonly_network in network key, got %q", networkKey)
	}
}

func TestChatRuntimeEvents_WriteToolsAreNotApprovalReusable(t *testing.T) {
	bridge := newChatRuntimeEventBridge(&ChatSession{
		ApprovalReuseMode: chatApprovalReuseSessionReadOnlyShell,
	})
	bridge.BeginRun()

	// write/edit/download tools should not produce an approval grant key.
	for _, toolName := range []string{"write", "edit", "multiedit", "download"} {
		approval := &runtimechat.ApprovalRequest{
			ToolName: toolName,
			ArgsJSON: []byte(`{}`),
		}
		key := bridge.autoApprovalGrantKey("session-1", approval)
		if key != "" {
			t.Fatalf("expected no approval grant key for write-like tool %q, got %q", toolName, key)
		}
	}
}

func TestChatRuntimeEvents_FutureNetworkToolAutoCovered(t *testing.T) {
	// A hypothetical future tool "web_fetch" containing "fetch" should be
	// automatically covered by the capability-based family derivation without
	// any code changes to approvalGrantFamily.
	bridge := newChatRuntimeEventBridge(&ChatSession{
		ApprovalReuseMode: chatApprovalReuseSessionReadOnlyShell,
	})
	bridge.BeginRun()

	approval := &runtimechat.ApprovalRequest{
		ToolName: "web_fetch",
		ArgsJSON: []byte(`{"url":"https://example.com/api"}`),
	}
	key := bridge.autoApprovalGrantKey("session-1", approval)
	if key == "" {
		t.Fatal("expected approval grant key for future network tool web_fetch")
	}
	if !strings.Contains(key, "readonly_network") {
		t.Fatalf("expected readonly_network in key for web_fetch, got %q", key)
	}
}

func TestChatRuntimeEvents_MutatingShellNotReusable(t *testing.T) {
	bridge := newChatRuntimeEventBridge(&ChatSession{
		ApprovalReuseMode: chatApprovalReuseSessionReadOnlyShell,
	})
	bridge.BeginRun()

	// A shell command that writes (e.g. mkdir) should not produce a grant key.
	approval := &runtimechat.ApprovalRequest{
		ToolName: "bash",
		ArgsJSON: []byte(`{"command":"mkdir /tmp/test"}`),
	}
	key := bridge.autoApprovalGrantKey("session-1", approval)
	if key != "" {
		t.Fatalf("expected no approval grant key for mutating shell, got %q", key)
	}
}

func TestChatRuntimeEvents_ApprovedShellFamilyForNonWhitelistedCommands(t *testing.T) {
	bridge := newChatRuntimeEventBridge(&ChatSession{
		ApprovalReuseMode: chatApprovalReuseSessionReadOnlyShell,
	})
	bridge.BeginRun()

	// "go test" is not in the read-only whitelist but is also not dangerous.
	// It should produce an "approved_shell" family key.
	approval := &runtimechat.ApprovalRequest{
		ToolName: "execute_shell_command",
		ArgsJSON: []byte(`{"command":"go test ./..."}`),
	}
	key := bridge.autoApprovalGrantKey("session-1", approval)
	if key == "" {
		t.Fatal("expected approval grant key for non-dangerous non-whitelisted shell command")
	}
	if !strings.Contains(key, "approved_shell") {
		t.Fatalf("expected approved_shell in key, got %q", key)
	}

	// Once remembered, the grant should allow auto-approval of similar commands.
	bridge.rememberApprovalGrant(key)
	if !bridge.hasApprovalGrant(key) {
		t.Fatal("expected approved_shell grant to be cached")
	}
}

func TestChatRuntimeEvents_ApprovedShellSeparateFromReadonlyShell(t *testing.T) {
	bridge := newChatRuntimeEventBridge(&ChatSession{
		ApprovalReuseMode: chatApprovalReuseSessionReadOnlyShell,
	})
	bridge.BeginRun()

	readonlyApproval := &runtimechat.ApprovalRequest{
		ToolName: "bash",
		ArgsJSON: []byte(`{"command":"git status"}`),
	}
	readonlyKey := bridge.autoApprovalGrantKey("session-1", readonlyApproval)

	approvedApproval := &runtimechat.ApprovalRequest{
		ToolName: "execute_shell_command",
		ArgsJSON: []byte(`{"command":"go build ./..."}`),
	}
	approvedKey := bridge.autoApprovalGrantKey("session-1", approvedApproval)

	if readonlyKey == approvedKey {
		t.Fatalf("readonly_shell and approved_shell should have different keys, got same: %q", readonlyKey)
	}
	if !strings.Contains(readonlyKey, "readonly_shell") {
		t.Fatalf("expected readonly_shell in key, got %q", readonlyKey)
	}
	if !strings.Contains(approvedKey, "approved_shell") {
		t.Fatalf("expected approved_shell in key, got %q", approvedKey)
	}
}

func TestChatRuntimeEvents_ApprovedShellDoesNotCoverDangerousCommands(t *testing.T) {
	bridge := newChatRuntimeEventBridge(&ChatSession{
		ApprovalReuseMode: chatApprovalReuseSessionReadOnlyShell,
	})
	bridge.BeginRun()

	// Commands with redirect operators should not produce any grant key.
	approval := &runtimechat.ApprovalRequest{
		ToolName: "bash",
		ArgsJSON: []byte(`{"command":"echo hello > /tmp/out.txt"}`),
	}
	key := bridge.autoApprovalGrantKey("session-1", approval)
	if key != "" {
		t.Fatalf("expected no approval grant key for command with redirect, got %q", key)
	}
}

func TestChatRuntimeEvents_FlushesBufferedDeltaOnSessionEnd(t *testing.T) {
	session := &ChatSession{
		Stream:         true,
		RuntimeSession: &runtimechat.Session{ID: "lead-session"},
	}
	bridge := newChatRuntimeEventBridge(session)
	var deltas []string
	finalized := 0
	bridge.writeDelta = func(delta string) {
		deltas = append(deltas, delta)
	}
	bridge.finalizeDelta = func() {
		finalized++
	}

	bridge.BeginRun()
	bridge.handleEvent(runtimeevents.Event{
		Type:      runtimechat.EventAssistantDelta,
		SessionID: "lead-session",
		Payload:   map[string]interface{}{"delta": "Analyzing the issue..."},
	})
	// Session ends without an EventAssistantMessage (e.g. ReAct loop ended
	// with tool calls but no final text, or LLM only returned tool calls).
	bridge.handleEvent(runtimeevents.Event{
		Type:      runtimechat.EventSessionEnd,
		SessionID: "lead-session",
		Payload:   map[string]interface{}{"success": true},
	})

	if len(deltas) != 1 || deltas[0] != "Analyzing the issue..." {
		t.Fatalf("expected buffered delta to be written, got %v", deltas)
	}
	if finalized != 1 {
		t.Fatalf("expected delta to be finalized on session_end, got %d finalizations", finalized)
	}
	if !bridge.HasRenderedAssistantFinal() {
		t.Fatal("expected assistant final flag after session_end flush")
	}
}

func TestChatRuntimeEvents_SkipsDeltaFlushOnSessionEndWhenAlreadyFinalized(t *testing.T) {
	session := &ChatSession{
		Stream:         true,
		RuntimeSession: &runtimechat.Session{ID: "lead-session"},
	}
	bridge := newChatRuntimeEventBridge(session)
	finalized := 0
	bridge.writeDelta = func(string) {}
	bridge.finalizeDelta = func() {
		finalized++
	}

	bridge.BeginRun()
	bridge.handleEvent(runtimeevents.Event{
		Type:      runtimechat.EventAssistantDelta,
		SessionID: "lead-session",
		Payload:   map[string]interface{}{"delta": "Hello"},
	})
	bridge.handleEvent(runtimeevents.Event{
		Type:      runtimechat.EventAssistantMessage,
		SessionID: "lead-session",
		Payload:   map[string]interface{}{"content": "Hello"},
	})
	if finalized != 1 {
		t.Fatalf("expected initial finalize, got %d", finalized)
	}

	bridge.handleEvent(runtimeevents.Event{
		Type:      runtimechat.EventSessionEnd,
		SessionID: "lead-session",
		Payload:   map[string]interface{}{"success": true},
	})
	if finalized != 1 {
		t.Fatalf("expected no double-finalize on session_end, got %d", finalized)
	}
}

func TestChatRuntimeEvents_SessionEndPromptPreflightStillRendersAfterDeltaFlush(t *testing.T) {
	session := &ChatSession{
		Stream:         true,
		RuntimeSession: &runtimechat.Session{ID: "lead-session"},
	}
	bridge := newChatRuntimeEventBridge(session)
	finalized := 0
	var lines []string
	bridge.writeDelta = func(string) {}
	bridge.finalizeDelta = func() {
		finalized++
	}
	bridge.writeLine = func(line string) {
		lines = append(lines, line)
	}

	bridge.BeginRun()
	bridge.handleEvent(runtimeevents.Event{
		Type:      runtimechat.EventAssistantDelta,
		SessionID: "lead-session",
		Payload:   map[string]interface{}{"delta": "Analyzing the issue..."},
	})
	bridge.handleEvent(runtimeevents.Event{
		Type:      runtimechat.EventSessionEnd,
		SessionID: "lead-session",
		TraceID:   "trace-preflight",
		Payload: map[string]interface{}{
			"error_type":                        "prompt_preflight",
			"failure_reason_code":               "prompt_still_exceeds_budget_after_compaction",
			"suggested_action":                  "请继续收缩上下文层、提高预算，或从新的轮次继续。",
			"prompt_tokens":                     12000,
			"prompt_budget":                     10000,
			"resolved_model":                    "codex-gpt-5.4",
			"replacement_history_available":     true,
			"replacement_history_applied":       true,
			"replacement_history_message_count": 4,
		},
	})

	if finalized != 1 {
		t.Fatalf("expected delta to be finalized on prompt preflight session_end, got %d", finalized)
	}
	require.Len(t, lines, 1)
	require.Contains(t, lines[0], "[prompt preflight] 本地拦截：prompt 12000 > budget 10000")
	require.Contains(t, lines[0], "原因: active-turn 已压缩，但 prompt 仍然超出预算")
	require.Contains(t, lines[0], "建议: 请继续收缩上下文层、提高预算，或从新的轮次继续。")
	require.Contains(t, lines[0], "模型: codex-gpt-5.4")
	require.Contains(t, lines[0], "恢复: 已自动保存压缩后的上下文，可直接继续下一轮 | history_messages=4")
}

func TestIsReadOnlyShellCommand_ChainedAndCommands(t *testing.T) {
	tests := []struct {
		command string
		want    bool
	}{
		// Simple commands (existing behavior)
		{"dir", true},
		{"ls", true},
		{"git status", true},
		{"git commit", false},
		{"rm -rf /", false},
		// cd && read-only: should now be read-only
		{"cd E:\\projects\\ai\\codex-server && dir", true},
		{"cd /tmp && ls", true},
		{"cd /tmp && pwd && ls", true},
		// cd && write: not read-only
		{"cd /tmp && npm install", false},
		// Pipe (existing behavior)
		{"dir | findstr /i job", true},
		{"cat file.txt | grep pattern", true},
		// || chains: each segment checked
		{"dir || ls", true},
		// Mixed: read-only && write
		{"ls && npm install", false},
		// cd is read-only for approval purposes
		{"cd somedir", true},
		// echo is read-only for approval purposes
		{"echo hello", true},
		// printf is stdout-only for approval purposes
		{"printf 'hello\\n'", true},
		{"git diff --stat && printf '\\n---\\n' && git diff --name-only", true},
		// Redirect: still not read-only
		{"echo hello > file.txt", false},
		// Windows-style: cd /d with && dir
		{"cd /d E:\\code && dir /b", true},
	}
	for _, tt := range tests {
		got := isReadOnlyShellCommand(tt.command)
		if got != tt.want {
			t.Errorf("isReadOnlyShellCommand(%q) = %v, want %v", tt.command, got, tt.want)
		}
	}
}

type questionToolProvider struct {
	callCount atomic.Int32
}

func (p *questionToolProvider) Name() string { return "test-provider" }

func (p *questionToolProvider) Call(ctx context.Context, req *runtimellm.LLMRequest) (*runtimellm.LLMResponse, error) {
	p.callCount.Add(1)
	for _, message := range req.Messages {
		if message.Role == "tool" {
			return &runtimellm.LLMResponse{
				Content: "question answered",
				Model:   req.Model,
			}, nil
		}
	}
	return &runtimellm.LLMResponse{
		Model: req.Model,
		ToolCalls: []runtimetypes.ToolCall{
			{
				ID:   "call-1",
				Name: toolbroker.ToolAskUserQuestion,
				Args: map[string]interface{}{
					"prompt":   "Need user input",
					"required": true,
				},
			},
		},
	}, nil
}

func (p *questionToolProvider) Stream(ctx context.Context, req *runtimellm.LLMRequest) (<-chan runtimellm.StreamChunk, error) {
	ch := make(chan runtimellm.StreamChunk, 1)
	close(ch)
	return ch, nil
}

func (p *questionToolProvider) CountTokens(text string) int { return len(text) }

func (p *questionToolProvider) GetCapabilities() *runtimellm.ModelCapabilities {
	return &runtimellm.ModelCapabilities{
		SupportsTools: true,
	}
}

func (p *questionToolProvider) CheckHealth(ctx context.Context) error { return nil }

type answerPreservingQuestionProvider struct {
	mu          sync.Mutex
	toolMsg     string
	toolMeta    runtimetypes.Metadata
	answerFound bool
}

func (p *answerPreservingQuestionProvider) Name() string { return "test-provider" }

func (p *answerPreservingQuestionProvider) Call(ctx context.Context, req *runtimellm.LLMRequest) (*runtimellm.LLMResponse, error) {
	for _, message := range req.Messages {
		if message.Role != "tool" {
			continue
		}
		p.mu.Lock()
		p.toolMsg = strings.TrimSpace(message.Content)
		p.toolMeta = message.Metadata.Clone()
		p.answerFound = strings.Contains(message.Content, "answer=provided answer 42")
		p.mu.Unlock()
		if strings.Contains(message.Content, "answer=provided answer 42") {
			return &runtimellm.LLMResponse{
				Content: "answer survived: provided answer 42",
				Model:   req.Model,
			}, nil
		}
		return &runtimellm.LLMResponse{
			Content: "answer missing after reducer",
			Model:   req.Model,
		}, nil
	}
	return &runtimellm.LLMResponse{
		Model: req.Model,
		ToolCalls: []runtimetypes.ToolCall{
			{
				ID:   "call-preserve-answer",
				Name: toolbroker.ToolAskUserQuestion,
				Args: map[string]interface{}{
					"prompt":   "Need user input",
					"required": true,
				},
			},
		},
	}, nil
}

func (p *answerPreservingQuestionProvider) Stream(ctx context.Context, req *runtimellm.LLMRequest) (<-chan runtimellm.StreamChunk, error) {
	ch := make(chan runtimellm.StreamChunk, 1)
	close(ch)
	return ch, nil
}

func (p *answerPreservingQuestionProvider) CountTokens(text string) int { return len(text) }

func (p *answerPreservingQuestionProvider) GetCapabilities() *runtimellm.ModelCapabilities {
	return &runtimellm.ModelCapabilities{SupportsTools: true}
}

func (p *answerPreservingQuestionProvider) CheckHealth(ctx context.Context) error { return nil }

type approvalToolProvider struct {
	callCount atomic.Int32
}

type cachedShellApprovalProvider struct {
	callCount atomic.Int32
}

func (p *approvalToolProvider) Name() string { return "test-provider" }

func (p *cachedShellApprovalProvider) Name() string { return "test-provider" }

func (p *approvalToolProvider) Call(ctx context.Context, req *runtimellm.LLMRequest) (*runtimellm.LLMResponse, error) {
	p.callCount.Add(1)
	for _, message := range req.Messages {
		if message.Role == "tool" {
			return &runtimellm.LLMResponse{
				Content: "approval survived and resumed",
				Model:   req.Model,
			}, nil
		}
	}
	return &runtimellm.LLMResponse{
		Model: req.Model,
		ToolCalls: []runtimetypes.ToolCall{
			{
				ID:   "call-approval-1",
				Name: "team_echo",
				Args: map[string]interface{}{"message": "hello"},
			},
		},
	}, nil
}

func (p *cachedShellApprovalProvider) Call(ctx context.Context, req *runtimellm.LLMRequest) (*runtimellm.LLMResponse, error) {
	p.callCount.Add(1)
	toolCount := 0
	for _, message := range req.Messages {
		if message.Role == "tool" {
			toolCount++
		}
	}
	switch toolCount {
	case 0:
		return &runtimellm.LLMResponse{
			Model: req.Model,
			ToolCalls: []runtimetypes.ToolCall{
				{
					ID:   "call-shell-1",
					Name: "execute_shell_command",
					Args: map[string]interface{}{"command": "Get-ChildItem docs"},
				},
			},
		}, nil
	case 1:
		return &runtimellm.LLMResponse{
			Model: req.Model,
			ToolCalls: []runtimetypes.ToolCall{
				{
					ID:   "call-shell-2",
					Name: "bash",
					Args: map[string]interface{}{"command": "Get-Content README.md"},
				},
			},
		}, nil
	default:
		return &runtimellm.LLMResponse{
			Content: "shell approvals reused",
			Model:   req.Model,
		}, nil
	}
}

func (p *approvalToolProvider) Stream(ctx context.Context, req *runtimellm.LLMRequest) (<-chan runtimellm.StreamChunk, error) {
	ch := make(chan runtimellm.StreamChunk, 1)
	close(ch)
	return ch, nil
}

func (p *cachedShellApprovalProvider) Stream(ctx context.Context, req *runtimellm.LLMRequest) (<-chan runtimellm.StreamChunk, error) {
	ch := make(chan runtimellm.StreamChunk, 1)
	close(ch)
	return ch, nil
}

func (p *approvalToolProvider) CountTokens(text string) int { return len(text) }

func (p *cachedShellApprovalProvider) CountTokens(text string) int { return len(text) }

func (p *approvalToolProvider) GetCapabilities() *runtimellm.ModelCapabilities {
	return &runtimellm.ModelCapabilities{SupportsTools: true}
}

func (p *cachedShellApprovalProvider) GetCapabilities() *runtimellm.ModelCapabilities {
	return &runtimellm.ModelCapabilities{SupportsTools: true}
}

func (p *approvalToolProvider) CheckHealth(ctx context.Context) error { return nil }

func (p *cachedShellApprovalProvider) CheckHealth(ctx context.Context) error { return nil }

type approvalCapturingMCPManager struct {
	lastMeta  *team.RunMeta
	callCount int
}

type shellApprovalCapturingMCPManager struct {
	callCount int
}

func (m *approvalCapturingMCPManager) FindTool(toolName string) (runtimeskill.ToolInfo, error) {
	if toolName != "team_echo" {
		return runtimeskill.ToolInfo{}, fmt.Errorf("tool not found: %s", toolName)
	}
	return runtimeskill.ToolInfo{
		Name:          toolName,
		Description:   "Echo tool for approval CLI tests",
		MCPName:       "test-mcp",
		MCPTrustLevel: "local",
		ExecutionMode: "local_mcp",
		Enabled:       true,
	}, nil
}

func (m *approvalCapturingMCPManager) CallTool(ctx interface{}, mcpName, toolName string, args map[string]interface{}) (interface{}, error) {
	runCtx, ok := ctx.(context.Context)
	if !ok {
		return nil, fmt.Errorf("unexpected context type %T", ctx)
	}
	meta, ok := team.GetRunMeta(runCtx)
	if !ok || meta == nil {
		return nil, fmt.Errorf("run meta missing")
	}
	m.lastMeta = meta.Clone()
	m.callCount++
	return "approved echo: " + fmt.Sprint(args["message"]), nil
}

func (m *approvalCapturingMCPManager) ListTools() []runtimeskill.ToolInfo {
	info, _ := m.FindTool("team_echo")
	return []runtimeskill.ToolInfo{info}
}

func (m *shellApprovalCapturingMCPManager) FindTool(toolName string) (runtimeskill.ToolInfo, error) {
	switch toolName {
	case "bash", "execute_shell_command":
		return runtimeskill.ToolInfo{
			Name:          toolName,
			Description:   "Shell tool for approval cache tests",
			MCPName:       "test-mcp",
			MCPTrustLevel: "local",
			ExecutionMode: "local_mcp",
			Enabled:       true,
		}, nil
	default:
		return runtimeskill.ToolInfo{}, fmt.Errorf("tool not found: %s", toolName)
	}
}

func (m *shellApprovalCapturingMCPManager) CallTool(ctx interface{}, mcpName, toolName string, args map[string]interface{}) (interface{}, error) {
	m.callCount++
	return fmt.Sprintf("%s ok: %v", toolName, args["command"]), nil
}

func (m *shellApprovalCapturingMCPManager) ListTools() []runtimeskill.ToolInfo {
	info1, _ := m.FindTool("execute_shell_command")
	info2, _ := m.FindTool("bash")
	return []runtimeskill.ToolInfo{info1, info2}
}

func (p *answerPreservingQuestionProvider) answerObserved() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.answerFound
}

func (p *answerPreservingQuestionProvider) toolContent() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.toolMsg
}

func (p *answerPreservingQuestionProvider) toolMetadata() runtimetypes.Metadata {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.toolMeta.Clone()
}

type taggedQuestionToolProvider struct{}

func (p *taggedQuestionToolProvider) Name() string { return "test-provider" }

func (p *taggedQuestionToolProvider) Call(ctx context.Context, req *runtimellm.LLMRequest) (*runtimellm.LLMResponse, error) {
	for _, message := range req.Messages {
		if message.Role == "tool" {
			return &runtimellm.LLMResponse{
				Content: "question answered",
				Model:   req.Model,
			}, nil
		}
	}
	prompt := ""
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == "user" {
			prompt = strings.TrimSpace(req.Messages[i].Content)
			break
		}
	}
	return &runtimellm.LLMResponse{
		Model: req.Model,
		ToolCalls: []runtimetypes.ToolCall{
			{
				ID:   "call-1",
				Name: toolbroker.ToolAskUserQuestion,
				Args: map[string]interface{}{
					"prompt":   "Need user input: " + prompt,
					"required": true,
				},
			},
		},
	}, nil
}

func (p *taggedQuestionToolProvider) Stream(ctx context.Context, req *runtimellm.LLMRequest) (<-chan runtimellm.StreamChunk, error) {
	ch := make(chan runtimellm.StreamChunk, 1)
	close(ch)
	return ch, nil
}

func (p *taggedQuestionToolProvider) CountTokens(text string) int { return len(text) }

func (p *taggedQuestionToolProvider) GetCapabilities() *runtimellm.ModelCapabilities {
	return &runtimellm.ModelCapabilities{SupportsTools: true}
}

func (p *taggedQuestionToolProvider) CheckHealth(ctx context.Context) error { return nil }

func latestToolMessage(history []runtimetypes.Message) *runtimetypes.Message {
	for index := len(history) - 1; index >= 0; index-- {
		if history[index].Role != "tool" {
			continue
		}
		cloned := history[index]
		return &cloned
	}
	return nil
}

func TestChatRuntimeEvents_NonInteractiveQuestionReturnsError(t *testing.T) {
	manager, userID, dir, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	runtimeSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}

	provider := &questionToolProvider{}
	llmRuntime := runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{
		DefaultProvider: "test-provider",
		DefaultModel:    "test-model",
	})
	if err := llmRuntime.RegisterProvider("test-provider", provider); err != nil {
		t.Fatalf("RegisterProvider: %v", err)
	}
	if err := llmRuntime.RegisterProviderAlias("test-model", "test-provider"); err != nil {
		t.Fatalf("RegisterProviderAlias: %v", err)
	}

	host := &localChatRuntimeHost{
		EventBus:     runtimeevents.NewBusWithRetention(64),
		SessionStore: manager.GetStorage(),
		SessionUser:  userID,
	}
	host.SessionHub = runtimechat.NewSessionHub(func(sessionID string) (*runtimechat.SessionActor, error) {
		runtimeStore := runtimechat.NewInMemoryRuntimeStore(64)
		a := agent.NewAgentWithLLM(&agent.Config{
			Name:     "bridge-test",
			Provider: "test-provider",
			Model:    "test-model",
			MaxSteps: 4,
		}, nil, llmRuntime)
		return runtimechat.NewSessionActor(sessionID, runtimechat.SessionActorConfig{
			Agent:        a,
			LLMRuntime:   llmRuntime,
			SessionStore: manager.GetStorage(),
			StateStore:   runtimeStore,
			EventStore:   runtimeStore,
			EventBus:     host.EventBus,
		})
	})

	session := &ChatSession{
		NoInteractive:    true,
		ProviderName:     "test-provider",
		Model:            "test-model",
		SessionManager:   manager,
		RuntimeSession:   runtimeSession,
		SessionUserID:    userID,
		SessionDir:       dir,
		LocalRuntimeHost: host,
		ChatExecutor:     newAICLIActorChatExecutor(),
	}
	host.BaseSession = session

	session.RuntimeEventBridge = newChatRuntimeEventBridge(session)
	_, err = session.ChatExecutor.Execute(context.Background(), session, "trigger question")
	if err == nil {
		t.Fatal("expected non-interactive question to fail")
	}
	if !strings.Contains(err.Error(), "--no-interactive") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestChatRuntimeEvents_NonInteractiveApprovalReturnsError(t *testing.T) {
	manager, userID, dir, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	runtimeSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}

	provider := &approvalToolProvider{}
	llmRuntime := runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{
		DefaultProvider: "test-provider",
		DefaultModel:    "test-model",
	})
	if err := llmRuntime.RegisterProvider("test-provider", provider); err != nil {
		t.Fatalf("RegisterProvider: %v", err)
	}
	if err := llmRuntime.RegisterProviderAlias("test-model", "test-provider"); err != nil {
		t.Fatalf("RegisterProviderAlias: %v", err)
	}

	mcpManager := &approvalCapturingMCPManager{}
	host := &localChatRuntimeHost{
		EventBus:     runtimeevents.NewBusWithRetention(64),
		SessionStore: manager.GetStorage(),
		SessionUser:  userID,
	}
	host.SessionHub = runtimechat.NewSessionHub(func(sessionID string) (*runtimechat.SessionActor, error) {
		runtimeStore := runtimechat.NewInMemoryRuntimeStore(64)
		a := agent.NewAgentWithLLM(&agent.Config{
			Name:     "bridge-test",
			Provider: "test-provider",
			Model:    "test-model",
			MaxSteps: 4,
		}, mcpManager, llmRuntime)
		a.SetPermissionEngine(&agent.PermissionEngine{
			Callback: func(ctx context.Context, req runtimepolicy.EvalRequest) (runtimepolicy.Decision, string, error) {
				if req.ToolName == "team_echo" {
					return runtimepolicy.Decision{Type: runtimepolicy.DecisionAsk}, "manual approval", nil
				}
				return runtimepolicy.Decision{Type: runtimepolicy.DecisionAllow}, "", nil
			},
		})
		return runtimechat.NewSessionActor(sessionID, runtimechat.SessionActorConfig{
			Agent:        a,
			LLMRuntime:   llmRuntime,
			SessionStore: manager.GetStorage(),
			StateStore:   runtimeStore,
			EventStore:   runtimeStore,
			EventBus:     host.EventBus,
		})
	})

	session := &ChatSession{
		NoInteractive:    true,
		ProviderName:     "test-provider",
		Model:            "test-model",
		SessionManager:   manager,
		RuntimeSession:   runtimeSession,
		SessionUserID:    userID,
		SessionDir:       dir,
		LocalRuntimeHost: host,
		ChatExecutor:     newAICLIActorChatExecutor(),
	}
	host.BaseSession = session

	session.RuntimeEventBridge = newChatRuntimeEventBridge(session)
	_, err = session.ChatExecutor.Execute(context.Background(), session, "trigger approval")
	if err == nil {
		t.Fatal("expected non-interactive approval to fail")
	}
	if !strings.Contains(err.Error(), "--no-interactive") {
		t.Fatalf("unexpected error: %v", err)
	}
	if mcpManager.callCount != 0 {
		t.Fatalf("expected denied approval path to skip tool execution, got %d", mcpManager.callCount)
	}
}
