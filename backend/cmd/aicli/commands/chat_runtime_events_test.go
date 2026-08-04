package commands

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/cell"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimechatcore "github.com/wwsheng009/ai-agent-runtime/internal/chatcore"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	runtimellm "github.com/wwsheng009/ai-agent-runtime/internal/llm"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	runtimeskill "github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolprotocol"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestFormatInteractiveSupplementPromptLine_PreservesPromptContentWithoutIndent(t *testing.T) {
	got := formatInteractiveSupplementPromptLine("[approval] query=北京 天气预报 未来 7 天")
	if strings.HasPrefix(got, " ") {
		t.Fatalf("expected prompt line without leading assistant gutter indent, got %q", got)
	}
	if !strings.Contains(got, "[approval] query=北京 天气预报 未来 7 天") {
		t.Fatalf("expected approval content to stay visible, got %q", got)
	}
}

func TestRenderChatRuntimeTimelineEventCarriesTypedModel(t *testing.T) {
	cases := []struct {
		name     string
		event    runtimeevents.Event
		wantLine string
		wantKind cell.TimelineKind
	}{
		{
			name:     "planning",
			event:    runtimeevents.Event{Type: "planning.completed"},
			wantLine: "[planning] completed",
			wantKind: cell.TimelinePlanning,
		},
		{
			name: "question",
			event: runtimeevents.Event{Type: runtimechat.EventQuestionAsked, Payload: map[string]interface{}{
				"prompt": "choose a model",
			}},
			wantLine: "[question] choose a model",
			wantKind: cell.TimelineQuestion,
		},
		{
			name:     "input",
			event:    runtimeevents.Event{Type: chatEventInputQueueDrained, SessionID: "session-1"},
			wantLine: "[input] queued input drained",
			wantKind: cell.TimelineInput,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rendered := renderChatRuntimeTimelineEvent(tc.event)
			if rendered.Line != tc.wantLine {
				t.Fatalf("line=%q want=%q", rendered.Line, tc.wantLine)
			}
			if rendered.Timeline == nil || rendered.Timeline.Kind != tc.wantKind {
				t.Fatalf("typed timeline=%+v want kind=%v", rendered.Timeline, tc.wantKind)
			}
		})
	}
}

func TestChatRuntimeEventBridgeEmitsTypedTimelineWithoutLegacyLineWriter(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	session := &ChatSession{}
	interaction := newChatInteractionCoordinator(session)
	var output bytes.Buffer
	interaction.SetWriter(&output)
	session.Interaction = interaction
	bridge := newChatRuntimeEventBridge(session)
	bridge.writeLine = func(line string) {
		t.Fatalf("typed timeline unexpectedly used legacy line writer: %q", line)
	}

	rendered := renderChatRuntimeTimelineEvent(runtimeevents.Event{Type: "planning.completed"})
	bridge.emitTimelineEvent(rendered)
	if got := output.String(); !strings.Contains(got, "[planning] completed") {
		t.Fatalf("typed timeline was not rendered: %q", got)
	}
}

func TestChatToolCompletedTimelineEventUsesTypedDocument(t *testing.T) {
	line := "• Completed shell go test\n  ok"
	rendered := chatToolCompletedTimelineEvent(line)
	if rendered.Line != line || rendered.Timeline == nil || rendered.Document == nil {
		t.Fatalf("regular completion is not typed: %+v", rendered)
	}
	if rendered.Timeline.Status != cell.StatusSuccess {
		t.Fatalf("status=%v", rendered.Timeline.Status)
	}

	failed := chatToolCompletedTimelineEvent("• Failed shell\n  exit 1")
	if failed.Timeline == nil || failed.Timeline.Status != cell.StatusError {
		t.Fatalf("failed completion status=%+v", failed.Timeline)
	}
}

func TestChatRuntimeEvents_ToolRunningIsViewportOnlyAndFinalCommitsOnce(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	session := &ChatSession{
		Stream:         true,
		RuntimeSession: &runtimechat.Session{ID: "lead-session"},
	}
	interaction := newChatInteractionCoordinator(session)
	t.Cleanup(interaction.Shutdown)
	var history bytes.Buffer
	interaction.SetWriter(&history)
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(80, 24)
	interaction.SetSurface(surface)
	session.Interaction = interaction
	bridge := newChatRuntimeEventBridge(session)
	bridge.BeginRun()
	bridge.handleEvent(runtimeevents.Event{
		Type:      runtimechat.EventSessionStart,
		SessionID: "lead-session",
		Payload:   map[string]interface{}{"turn_id": "turn-1"},
	})

	requested := runtimeevents.Event{
		Type:      "tool.requested",
		SessionID: "lead-session",
		ToolName:  "shell",
		TraceID:   "trace-1",
		Payload: map[string]interface{}{
			"turn_id":      "turn-1",
			"tool_call_id": "call-1",
			"command_text": "go test ./...",
			"tool_source":  "meta",
		},
	}
	bridge.handleEvent(requested)
	require.NotContains(t, history.String(), "Running")
	interaction.waitUIActorIdle()
	require.Contains(t, strings.Join(surface.ActiveBandLines(), "\n"), "• Running [meta] go test ./...")

	bridge.handleEvent(runtimeevents.Event{
		Type:      "tool.progress",
		SessionID: "lead-session",
		ToolName:  "shell",
		TraceID:   "trace-1",
		Payload: map[string]interface{}{
			"turn_id":      "turn-1",
			"tool_call_id": "call-1",
			"message":      "package 2/3",
		},
	})
	interaction.waitUIActorIdle()
	require.Contains(t, strings.Join(surface.ActiveBandLines(), "\n"), "• Running [meta] go test ./...")
	require.NotContains(t, history.String(), "Progress")

	completed := runtimeevents.Event{
		Type:      "tool.completed",
		SessionID: "lead-session",
		ToolName:  "shell",
		TraceID:   "trace-1",
		Payload: map[string]interface{}{
			"turn_id":      "turn-1",
			"tool_call_id": "call-1",
			"command_text": "go test ./...",
			"tool_source":  "meta",
			"summary_lines": []string{
				"ok",
			},
		},
	}
	bridge.handleEvent(completed)
	bridge.handleEvent(completed)
	interaction.waitUIActorIdle()
	require.NotContains(t, strings.Join(surface.ActiveBandLines(), "\n"), "Running")
	require.Equal(t, 1, strings.Count(history.String(), "• Completed [meta] go test ./..."))

	failedRequested := requested
	failedRequested.TraceID = "trace-2"
	failedRequested.Payload = map[string]interface{}{
		"turn_id":      "turn-1",
		"tool_call_id": "call-2",
		"command_text": "go test ./failed",
		"tool_source":  "meta",
	}
	bridge.handleEvent(failedRequested)
	interaction.waitUIActorIdle()
	require.Contains(t, strings.Join(surface.ActiveBandLines(), "\n"), "• Running [meta] go test ./failed")

	failed := runtimeevents.Event{
		Type:      "tool.failed",
		SessionID: "lead-session",
		ToolName:  "shell",
		TraceID:   "trace-2",
		Payload: map[string]interface{}{
			"turn_id":      "turn-1",
			"tool_call_id": "call-2",
			"command_text": "go test ./failed",
			"tool_source":  "meta",
			"reason":       "exit status 1",
		},
	}
	bridge.handleEvent(failed)
	bridge.handleEvent(failed)
	// Runtime event 的 surface 清理经 UI actor 异步应用；与前面的
	// requested/progress/completed 断言保持同一 drain 边界，避免在 failed
	// action 尚未消费时读取旧 ActiveBand 投影。
	interaction.waitUIActorIdle()
	require.NotContains(t, strings.Join(surface.ActiveBandLines(), "\n"), "Running")
	require.Equal(t, 1, strings.Count(history.String(), "• Failed [meta] go test ./failed"))
}

func TestChatRuntimeEventBridge_ToolLifecycleMirrorsSceneActiveCell(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	session := &ChatSession{
		Stream:         true,
		RuntimeSession: &runtimechat.Session{ID: "lead-session"},
	}
	interaction := newChatInteractionCoordinator(session)
	t.Cleanup(interaction.Shutdown)
	var output bytes.Buffer
	interaction.SetWriter(&output)
	session.Interaction = interaction
	bridge := newChatRuntimeEventBridge(session)
	bridge.BeginRun()

	post := func(event runtimeevents.Event) {
		t.Helper()
		if !bridge.postRuntimeEventToUIActor(event) {
			t.Fatalf("runtime event was not accepted by UI actor: %s", event.Type)
		}
		interaction.waitUIActorIdle()
	}
	post(runtimeevents.Event{
		Type:      runtimechat.EventSessionStart,
		SessionID: "lead-session",
		Payload:   map[string]interface{}{"turn_id": "turn-1"},
	})
	post(runtimeevents.Event{
		Type:      "tool.requested",
		SessionID: "lead-session",
		TraceID:   "trace-1",
		ToolName:  "shell",
		Payload: map[string]interface{}{
			"turn_id":      "turn-1",
			"tool_call_id": "call-1",
			"tool_name":    "shell",
		},
	})

	state := interaction.uiActor.AppState()
	if state.Active.Phase != ui.ActiveCellMutable || state.Active.Kind != scene.KindToolChain {
		t.Fatalf("requested active state = %#v, want mutable tool chain", state.Active)
	}
	if state.Active.Source != "shell" {
		t.Fatalf("requested active source = %q, want shell", state.Active.Source)
	}
	if len(state.Transcript.Cells) != 1 || state.Transcript.Cells[0].Kind != scene.KindToolChain ||
		state.Transcript.Cells[0].Phase != scene.CellMutable {
		t.Fatalf("requested transcript = %#v, want one mutable tool cell", state.Transcript.Cells)
	}
	if strings.Contains(strings.ToLower(state.Transcript.Cells[0].Source), "running") {
		t.Fatalf("running viewport label leaked into transcript source: %q", state.Transcript.Cells[0].Source)
	}

	post(runtimeevents.Event{
		Type:      "tool.progress",
		SessionID: "lead-session",
		TraceID:   "trace-1",
		ToolName:  "shell",
		Payload: map[string]interface{}{
			"turn_id":      "turn-1",
			"tool_call_id": "call-1",
			"message":      "50% complete",
		},
	})
	state = interaction.uiActor.AppState()
	if state.Active.Phase != ui.ActiveCellMutable || state.Active.Source != "shell\n50% complete" {
		t.Fatalf("progress active state = %#v, want source-backed update", state.Active)
	}
	if strings.Contains(output.String(), "Progress") {
		t.Fatalf("stable-identity progress leaked into durable output: %q", output.String())
	}
	if len(state.Transcript.Cells) != 1 || state.Transcript.Cells[0].Source != state.Active.Source {
		t.Fatalf("progress transcript = %#v, want one updated mutable cell", state.Transcript.Cells)
	}

	completed := runtimeevents.Event{
		Type:      "tool.completed",
		SessionID: "lead-session",
		TraceID:   "trace-1",
		ToolName:  "shell",
		Payload: map[string]interface{}{
			"turn_id":      "turn-1",
			"tool_call_id": "call-1",
			"summary_lines": []interface{}{
				"ok",
			},
		},
	}
	post(completed)
	post(completed)
	state = interaction.uiActor.AppState()
	if state.Active.Phase != ui.ActiveCellInactive {
		t.Fatalf("completed active state = %#v, want inactive", state.Active)
	}
	if len(state.Transcript.Cells) != 1 {
		t.Fatalf("completed transcript cells = %#v, want one merged tool chain", state.Transcript.Cells)
	}
	cell := state.Transcript.Cells[0]
	if cell.Kind != scene.KindToolChain || cell.Phase != scene.CellCommitted {
		t.Fatalf("completed cell = %#v, want committed tool chain", cell)
	}
	if cell.Source != "shell\n50% complete\nok" {
		t.Fatalf("completed source = %q, want merged final tool source", cell.Source)
	}
	if bridge.renderEncoderStats().DuplicateCount == 0 {
		t.Fatalf("duplicate completion was not counted by encoder")
	}
}

func TestChatRuntimeEventBridge_IdentitylessToolProgressFallsBackToSystem(t *testing.T) {
	session := &ChatSession{
		Stream:         true,
		RuntimeSession: &runtimechat.Session{ID: "lead-session"},
	}
	interaction := newChatInteractionCoordinator(session)
	t.Cleanup(interaction.Shutdown)
	var output bytes.Buffer
	interaction.SetWriter(&output)
	session.Interaction = interaction
	bridge := newChatRuntimeEventBridge(session)
	bridge.BeginRun()
	post := func(event runtimeevents.Event) {
		t.Helper()
		if !bridge.postRuntimeEventToUIActor(event) {
			t.Fatalf("runtime event was not accepted by UI actor: %s", event.Type)
		}
		interaction.waitUIActorIdle()
	}
	post(runtimeevents.Event{
		Type:      runtimechat.EventSessionStart,
		SessionID: "lead-session",
		Payload:   map[string]interface{}{"turn_id": "turn-1"},
	})
	post(runtimeevents.Event{
		Type:      "tool.progress",
		SessionID: "lead-session",
		ToolName:  "shell",
		Payload: map[string]interface{}{
			"turn_id": "turn-1",
			"message": "legacy progress without call identity",
		},
	})

	state := interaction.uiActor.AppState()
	if state.Active.Phase != ui.ActiveCellInactive {
		t.Fatalf("identityless progress mounted active cell: %#v", state.Active)
	}
	if len(state.Transcript.Cells) != 1 || state.Transcript.Cells[0].Kind != scene.KindSystem ||
		!strings.Contains(state.Transcript.Cells[0].Source, "legacy progress without call identity") {
		t.Fatalf("identityless progress transcript = %#v, want visible system fallback", state.Transcript.Cells)
	}
	if !strings.Contains(output.String(), "Progress shell legacy progress without call identity") {
		t.Fatalf("identityless progress was not rendered visibly: %q", output.String())
	}
}

func TestChatRuntimeEvents_FailedFinalDoesNotSuppressExecutorFallback(t *testing.T) {
	session := &ChatSession{
		Stream:         true,
		RuntimeSession: &runtimechat.Session{ID: "lead-session"},
	}
	bridge := newChatRuntimeEventBridge(session)
	bridge.completeDelta = func(string) bool { return false }
	bridge.finalizeDelta = func() {}
	bridge.BeginRun()
	bridge.handleEvent(runtimeevents.Event{
		Type:      runtimechat.EventSessionStart,
		SessionID: "lead-session",
		Payload:   map[string]interface{}{"turn_id": "turn-1"},
	})
	bridge.handleEvent(runtimeevents.Event{
		Type:      runtimechat.EventAssistantDelta,
		SessionID: "lead-session",
		Payload: map[string]interface{}{
			"turn_id":   "turn-1",
			"stream_id": "stream-1",
			"sequence":  uint64(1),
			"mode":      "append",
			"delta":     "partial",
		},
	})
	bridge.handleEvent(runtimeevents.Event{
		Type:      runtimechat.EventAssistantMessage,
		SessionID: "lead-session",
		Payload: map[string]interface{}{
			"turn_id":   "turn-1",
			"stream_id": "stream-1",
			"sequence":  uint64(2),
			"mode":      "snapshot",
			"content":   "authoritative final",
		},
	})
	bridge.BindExecutorTurn("turn-1")

	require.False(t, bridge.HasRenderedAssistantFinal())
	require.False(t, bridge.HasCommittedExecutorTurnFinal())
	require.False(t, bridge.HasRenderedAssistantFinalResponse("authoritative final"))
}

func TestChatReasoningTimelineEventCarriesDocument(t *testing.T) {
	rendered := chatReasoningTimelineEvent("trace-1", "1", &runtimetypes.ReasoningBlock{
		Summary:    "inspect the renderer",
		Visibility: runtimetypes.ReasoningVisibilitySummary,
	})
	if rendered.Document == nil {
		t.Fatalf("reasoning event has no document: %+v", rendered)
	}
	if !strings.Contains(rendered.Line, "inspect the renderer") {
		t.Fatalf("reasoning projection=%q", rendered.Line)
	}
}

func TestChatRuntimeEventBridge_LLMRequestStartedDoesNotDisplayEstimateBeforeFinished(t *testing.T) {
	runtimeSession := runtimechat.NewSession("tester")
	session := &ChatSession{
		RuntimeSession: runtimeSession,
		NoInteractive:  true,
	}
	bridge := newChatRuntimeEventBridge(session)
	bridge.BeginRun()

	bridge.handleEvent(runtimeevents.Event{
		Type:      "llm.request.started",
		SessionID: runtimeSession.ID,
		Payload: map[string]interface{}{
			"message_count":         11,
			"success":               true,
			"context_prompt_tokens": 23099,
			"context_window_tokens": 270000,
			"usage_total_tokens":    24762,
		},
	})

	if session.ContextTokenCount != 0 {
		t.Fatalf("expected request-start estimate not to display as ctx used before finish, got context=%d", session.ContextTokenCount)
	}
	if session.ContextWindowTokenCount != 270000 {
		t.Fatalf("expected context window token count 270000, got %d", session.ContextWindowTokenCount)
	}
	if session.TurnContextTokenCount != 23099 {
		t.Fatalf("expected turn aggregate token count 23099, got %d", session.TurnContextTokenCount)
	}
	if session.StatusMessageCount != 11 {
		t.Fatalf("expected request event to update status message count, got %d", session.StatusMessageCount)
	}

	bridge.handleEvent(runtimeevents.Event{
		Type:      "llm.request.started",
		SessionID: runtimeSession.ID,
		Payload: map[string]interface{}{
			"message_count":         13,
			"success":               true,
			"context_prompt_tokens": 24299,
			"context_window_tokens": 270000,
			"usage_total_tokens":    1400,
		},
	})

	if session.ContextTokenCount != 0 {
		t.Fatalf("expected second request-start estimate not to display as ctx used before finish, got context=%d", session.ContextTokenCount)
	}
	if session.TurnContextTokenCount != 47398 {
		t.Fatalf("expected turn diagnostic aggregate 47398 after second request, got %d", session.TurnContextTokenCount)
	}
	if session.StatusMessageCount != 13 {
		t.Fatalf("expected second request event to refresh status message count, got %d", session.StatusMessageCount)
	}

	bridge.handleEvent(runtimeevents.Event{
		Type:      "llm.request.started",
		SessionID: runtimeSession.ID,
		Payload: map[string]interface{}{
			"message_count":         15,
			"success":               true,
			"context_prompt_tokens": 22080,
			"context_window_tokens": 270000,
		},
	})

	if session.ContextTokenCount != 0 {
		t.Fatalf("expected later request-start estimate not to display as ctx used before finish, got %d", session.ContextTokenCount)
	}
	if session.TurnContextTokenCount != 69478 {
		t.Fatalf("expected turn diagnostic aggregate to include smaller request prompt, got %d", session.TurnContextTokenCount)
	}
	if session.StatusMessageCount != 15 {
		t.Fatalf("expected smaller request event to keep refreshing status message count, got %d", session.StatusMessageCount)
	}

	bridge.handleEvent(runtimeevents.Event{
		Type:      "llm.request.finished",
		SessionID: runtimeSession.ID,
		Payload: map[string]interface{}{
			"success":               true,
			"context_prompt_tokens": 24299,
			"context_window_tokens": 270000,
			"usage_total_tokens":    1400,
		},
	})

	if session.TurnContextTokenCount != 69478 {
		t.Fatalf("expected finished event not to double count turn aggregate tokens, got %d", session.TurnContextTokenCount)
	}
	if session.ContextTokenCount != 1400 {
		t.Fatalf("expected finished provider usage to refresh live context snapshot, got %d", session.ContextTokenCount)
	}
	if session.ContextWindowTokenCount != 270000 {
		t.Fatalf("expected finished event to preserve context window token count 270000, got %d", session.ContextWindowTokenCount)
	}
}

func TestChatRuntimeEventBridge_LLMRequestFinishedOverwritesWithLastTurnUsage(t *testing.T) {
	// Codex-aligned: finished usage becomes last_token_usage and overwrites the prior snapshot.
	runtimeSession := runtimechat.NewSession("tester")
	session := &ChatSession{
		RuntimeSession:    runtimeSession,
		ContextTokenCount: 1320,
		NoInteractive:     true,
	}
	bridge := newChatRuntimeEventBridge(session)
	bridge.BeginRun()

	bridge.handleEvent(runtimeevents.Event{
		Type:      "llm.request.finished",
		SessionID: runtimeSession.ID,
		Payload: map[string]interface{}{
			"success":                 true,
			"context_prompt_tokens":   1322,
			"context_window_tokens":   256000,
			"usage_prompt_tokens":     12,
			"usage_completion_tokens": 28,
			"usage_total_tokens":      40,
		},
	})

	if session.ContextTokenCount != 40 {
		t.Fatalf("expected last-turn provider usage to overwrite active context snapshot, got %d", session.ContextTokenCount)
	}
	if session.ContextWindowTokenCount != 256000 {
		t.Fatalf("expected context window to update, got %d", session.ContextWindowTokenCount)
	}
}

func TestChatRuntimeEventBridge_LLMRequestFinishedAddsAnthropicCachedInput(t *testing.T) {
	runtimeSession := runtimechat.NewSession("tester")
	session := &ChatSession{
		RuntimeSession: runtimeSession,
		Provider:       config.Provider{Protocol: "anthropic"},
		NoInteractive:  true,
	}
	bridge := newChatRuntimeEventBridge(session)
	bridge.BeginRun()

	bridge.handleEvent(runtimeevents.Event{
		Type:      "llm.request.finished",
		SessionID: runtimeSession.ID,
		Payload: map[string]interface{}{
			"success":                 true,
			"context_window_tokens":   1000000,
			"usage_prompt_tokens":     176,
			"usage_completion_tokens": 240,
			"usage_total_tokens":      18400,
			"usage_cached_tokens":     17984,
		},
	})

	if session.ContextTokenCount != 18400 {
		t.Fatalf("expected anthropic cached input to count toward active context snapshot, got %d", session.ContextTokenCount)
	}
	if session.ContextWindowTokenCount != 1000000 {
		t.Fatalf("expected context window to update, got %d", session.ContextWindowTokenCount)
	}
}

func TestChatRuntimeEventBridge_LLMRequestFinishedFallsBackToEstimateWhenUsageMissing(t *testing.T) {
	runtimeSession := runtimechat.NewSession("tester")
	session := &ChatSession{
		RuntimeSession: runtimeSession,
		NoInteractive:  true,
	}
	bridge := newChatRuntimeEventBridge(session)
	bridge.BeginRun()

	bridge.handleEvent(runtimeevents.Event{
		Type:      "llm.request.finished",
		SessionID: runtimeSession.ID,
		Payload: map[string]interface{}{
			"success":               false,
			"context_prompt_tokens": 1322,
			"context_window_tokens": 256000,
		},
	})

	if session.ContextTokenCount != 1322 {
		t.Fatalf("expected missing usage to fall back to request estimate after finish, got %d", session.ContextTokenCount)
	}
	if session.ContextWindowTokenCount != 256000 {
		t.Fatalf("expected context window to update, got %d", session.ContextWindowTokenCount)
	}
}

func TestChatRuntimeEventBridge_SessionCompactCompletedResetsContextUsageToTokenAfter(t *testing.T) {
	runtimeSession := runtimechat.NewSession("tester")
	session := &ChatSession{
		RuntimeSession:          runtimeSession,
		TokenCount:              9999,
		ContextTokenCount:       23099,
		TurnContextTokenCount:   24299,
		ContextWindowTokenCount: 270000,
		NoInteractive:           true,
	}
	bridge := newChatRuntimeEventBridge(session)

	bridge.handleEvent(runtimeevents.Event{
		Type:      runtimechat.EventSessionCompactCompleted,
		SessionID: runtimeSession.ID,
		Payload: map[string]interface{}{
			"token_after":        1200,
			"max_context_tokens": 270000,
		},
	})

	if session.ContextTokenCount != 1200 {
		t.Fatalf("expected compact completion to set context token count to token_after, got %d", session.ContextTokenCount)
	}
	if session.TokenCount != 9999 {
		t.Fatalf("expected compact completion to preserve cumulative API token count, got %d", session.TokenCount)
	}
	if session.TurnContextTokenCount != 0 {
		t.Fatalf("expected compact completion to clear turn aggregate context usage, got %d", session.TurnContextTokenCount)
	}
	if session.ContextWindowTokenCount != 270000 {
		t.Fatalf("expected compact completion to preserve context window token count, got %d", session.ContextWindowTokenCount)
	}
}

func TestRenderSharedChatToolEvent_AppendsShellContext(t *testing.T) {
	got := renderSharedChatToolEvent(runtimechatcore.ChatEvent{
		Stage:    "tool_result",
		ToolName: "execute_shell_command",
		Arguments: map[string]interface{}{
			"command": "git status",
			"workdir": "E:/projects/ai/ai-agent-runtime",
		},
		Output:  "On branch main",
		Success: true,
		Metadata: map[string]interface{}{
			toolresult.SourceKey: "toolkit",
			"shell_display":      `pwsh (C:\Program Files\PowerShell\7\pwsh.exe)`,
		},
	})

	want := strings.Join([]string{
		"• Completed git status",
		"  workdir: E:/projects/ai/ai-agent-runtime",
		`  shell: pwsh (C:\Program Files\PowerShell\7\pwsh.exe)`,
		"  On branch main",
	}, "\n")
	if got != want {
		t.Fatalf("unexpected shared chat tool render: %q", got)
	}
}

func TestRenderSharedChatToolEvent_ShowsMultipleGenericArgs(t *testing.T) {
	got := renderSharedChatToolEvent(runtimechatcore.ChatEvent{
		Stage:    "tool_requested",
		ToolName: "view",
		Arguments: map[string]interface{}{
			"file_path":            "main.go",
			"offset":               40,
			"limit":                20,
			"include_line_numbers": false,
		},
	})

	want := "• Running view file_path=main.go include_line_numbers=false limit=20 offset=40"
	if got != want {
		t.Fatalf("unexpected shared generic tool render:\nwant: %s\n got: %s", want, got)
	}
}

func TestRenderSharedChatToolEvent_PreservesLongFilenameOnOwnLine(t *testing.T) {
	base := t.TempDir()
	filename := strings.Repeat("very-long-file-name-", 4) + "component.generated.tsx"
	absPath := filepath.Join(base, "apps", "portal-modern", "src", filename)
	displayPath := filepath.Join("apps", "portal-modern", "src", filename)

	got := renderSharedChatToolEvent(runtimechatcore.ChatEvent{
		Stage:    "tool_requested",
		ToolName: "view",
		Arguments: map[string]interface{}{
			"file_path": absPath,
			"workdir":   base,
			"limit":     20,
		},
	})

	want := strings.Join([]string{
		"• Running view limit=20",
		"  file_path: " + displayPath,
		"  workdir: " + base,
	}, "\n")
	if got != want {
		t.Fatalf("unexpected long filename render:\nwant:\n%s\n\ngot:\n%s", want, got)
	}
	if !strings.Contains(got, filename) || strings.Contains(got, "...") {
		t.Fatalf("full filename was not preserved: %q", got)
	}
}

func TestRenderSharedChatToolEvent_RedactsSensitiveArgs(t *testing.T) {
	got := renderSharedChatToolEvent(runtimechatcore.ChatEvent{
		Stage:    "tool_result",
		ToolName: "fetch",
		Arguments: map[string]interface{}{
			"url":          "https://example.test",
			"api_key":      "should-not-leak",
			"timeout_ms":   3000,
			"token_budget": 4096,
		},
		Output:  "200 OK",
		Success: true,
	})

	want := strings.Join([]string{
		"• Completed fetch url=https://example.test api_key=<redacted> timeout_ms=3000 token_budget=4096",
		"  200 OK",
	}, "\n")
	if got != want {
		t.Fatalf("unexpected redacted shared tool render:\nwant:\n%s\n\ngot:\n%s", want, got)
	}
	if strings.Contains(got, "should-not-leak") {
		t.Fatalf("sensitive value leaked in shared tool render: %q", got)
	}
}

func TestRenderSharedChatToolEvent_ShowsSearchArgsAndBackend(t *testing.T) {
	got := renderSharedChatToolEvent(runtimechatcore.ChatEvent{
		Stage:    "tool_result",
		ToolName: "grep",
		Arguments: map[string]interface{}{
			"patterns": []interface{}{"Popover", "DialogTrigger"},
			"paths":    []interface{}{"apps/portal-modern/src"},
			"glob":     "*.tsx",
			"context":  2,
		},
		Output:  "24 matches",
		Success: true,
		Metadata: map[string]interface{}{
			"tool_metadata": map[string]interface{}{"engine": "rg"},
			"duration_ms":   17,
		},
	})

	want := strings.Join([]string{
		`• Completed grep patterns=["Popover","DialogTrigger"] paths=["apps/portal-modern/src"] glob=*.tsx context=2 via rg in 17ms`,
		"  24 matches",
	}, "\n")
	if got != want {
		t.Fatalf("unexpected shared search render:\nwant:\n%s\n\ngot:\n%s", want, got)
	}
}

func TestRenderSharedChatToolEvent_RendersTodosListAndUpdateState(t *testing.T) {
	output := strings.Join([]string{
		"任务列表已更新: 2 待处理, 1 进行中, 0 已完成",
		"任务列表更新状态: 新增 3, 状态变更 0, 保持 0, 移除 0",
		"当前任务列表:",
		"1. [待处理] 分析需求 (新增)",
		"2. [进行中] 修改实现 (新增)",
		"3. [待处理] 运行测试 (新增)",
	}, "\n")

	got := renderSharedChatToolEvent(runtimechatcore.ChatEvent{
		Stage:    "tool_result",
		ToolName: "todos",
		Arguments: map[string]interface{}{
			"todos": []interface{}{1, 2, 3},
		},
		Output:  output,
		Success: true,
	})

	want := strings.Join([]string{
		"• Completed todos todos=[3]",
		"  任务列表已更新: 2 待处理, 1 进行中, 0 已完成",
		"  任务列表更新状态: 新增 3, 状态变更 0, 保持 0, 移除 0",
		"  当前任务列表:",
		"  1. [待处理] 分析需求 (新增)",
		"  2. [进行中] 修改实现 (新增)",
		"  3. [待处理] 运行测试 (新增)",
	}, "\n")
	if got != want {
		t.Fatalf("unexpected todos render:\nwant:\n%s\n\ngot:\n%s", want, got)
	}
}

func TestRenderChatRuntimeEvent_RendersTodosListFromEventToolName(t *testing.T) {
	got := renderChatRuntimeEvent(runtimeevents.Event{
		Type:     "tool.completed",
		ToolName: "todos",
		Payload: map[string]interface{}{
			"arg_preview": "todos=[3]",
			"summary_lines": []interface{}{
				"任务列表已更新: 2 待处理, 1 进行中, 0 已完成",
				"任务列表更新状态: 新增 3, 状态变更 0, 保持 0, 移除 0",
				"当前任务列表:",
				"1. [待处理] 分析需求 (新增)",
				"2. [进行中] 修改实现 (新增)",
				"3. [待处理] 运行测试 (新增)",
			},
		},
	})

	want := strings.Join([]string{
		"• Completed todos todos=[3]",
		"  任务列表已更新: 2 待处理, 1 进行中, 0 已完成",
		"  任务列表更新状态: 新增 3, 状态变更 0, 保持 0, 移除 0",
		"  当前任务列表:",
		"  1. [待处理] 分析需求 (新增)",
		"  2. [进行中] 修改实现 (新增)",
		"  3. [待处理] 运行测试 (新增)",
	}, "\n")
	if got != want {
		t.Fatalf("unexpected runtime todos render:\nwant:\n%s\n\ngot:\n%s", want, got)
	}
}

func TestRenderEditedDiffOutput_HandlesCreatedAndDeletedFiles(t *testing.T) {
	output := strings.Join([]string{
		"文件差异:",
		"```diff",
		"--- /dev/null",
		"+++ b/internal/new_file.go",
		"@@ -0,0 +1,2 @@",
		"+package internal",
		"+",
		"--- a/internal/old_file.go",
		"+++ /dev/null",
		"@@ -1,2 +0,0 @@",
		"-package internal",
		"-",
		"```",
	}, "\n")

	got := renderEditedDiffOutput(output)
	want := strings.Join([]string{
		`• Edited internal\new_file.go (+2 -0)`,
		`        1 + package internal`,
		`        2 + `,
		`  `,
		`• Edited internal\old_file.go (+0 -2)`,
		`        1 - package internal`,
		`        2 - `,
	}, "\n")
	if got != want {
		t.Fatalf("unexpected created/deleted diff render:\nwant:\n%s\n\ngot:\n%s", want, got)
	}
}

func TestRenderEditedDiffOutput_HandlesRawUnifiedDiffAndMultipleHunks(t *testing.T) {
	output := strings.Join([]string{
		"--- a/internal/service.go",
		"+++ b/internal/service.go",
		"@@ -10,2 +10,2 @@",
		" unchanged",
		"-old",
		"+new",
		"@@ -30,1 +30,2 @@",
		"-gone",
		"+first",
		"+second",
	}, "\n")

	got := renderEditedDiffOutput(output)
	want := strings.Join([]string{
		`• Edited internal\service.go (+3 -2)`,
		`       10   unchanged`,
		`       11 - old`,
		`       11 + new`,
		`          ...`,
		`       30 - gone`,
		`       30 + first`,
		`       31 + second`,
	}, "\n")
	if got != want {
		t.Fatalf("unexpected raw multi-hunk diff render:\nwant:\n%s\n\ngot:\n%s", want, got)
	}
}

func TestRenderSharedChatToolEvent_RendersApplyRawDiff(t *testing.T) {
	diff := strings.Join([]string{
		"--- a/app.go",
		"+++ b/app.go",
		"@@ -1,1 +1,1 @@",
		"-old",
		"+new",
	}, "\n")

	got := renderSharedChatToolEvent(runtimechatcore.ChatEvent{
		Stage:    "tool_result",
		ToolName: "apply",
		Output:   diff,
		Success:  true,
	})
	want := strings.Join([]string{
		`• Edited app.go (+1 -1)`,
		`        1 - old`,
		`        1 + new`,
	}, "\n")
	if got != want {
		t.Fatalf("unexpected apply diff render:\nwant:\n%s\n\ngot:\n%s", want, got)
	}
}

func TestRenderEditedDiffOutput_IgnoresRawDiffTrailingStatusLines(t *testing.T) {
	output := strings.Join([]string{
		"--- a/app.go",
		"+++ b/app.go",
		"@@ -1,1 +1,1 @@",
		"-old",
		"+new",
		"",
		"+ applied successfully",
	}, "\n")

	got := renderEditedDiffOutput(output)
	want := strings.Join([]string{
		`• Edited app.go (+1 -1)`,
		`        1 - old`,
		`        1 + new`,
	}, "\n")
	if got != want {
		t.Fatalf("unexpected raw diff render with trailing status:\nwant:\n%s\n\ngot:\n%s", want, got)
	}
}

func TestRenderSharedChatToolEvent_RendersNamespacedEditDiff(t *testing.T) {
	diff := strings.Join([]string{
		"--- a/app.go",
		"+++ b/app.go",
		"@@ -1,1 +1,1 @@",
		"-old",
		"+new",
	}, "\n")

	got := renderSharedChatToolEvent(runtimechatcore.ChatEvent{
		Stage:    "tool_result",
		ToolName: "filesystem.edit_file",
		Output:   diff,
		Success:  true,
	})
	if !strings.Contains(got, "• Edited app.go (+1 -1)") {
		t.Fatalf("expected namespaced edit tool to render diff, got:\n%s", got)
	}
}

func TestRenderSharedChatToolEvent_RendersShellDiffAsReadOnlyDiff(t *testing.T) {
	diff := strings.Join([]string{
		"--- a/app.go",
		"+++ b/app.go",
		"@@ -1,1 +1,1 @@",
		"-old",
		"+new",
	}, "\n")

	got := renderSharedChatToolEvent(runtimechatcore.ChatEvent{
		Stage:    "tool_result",
		ToolName: "execute_shell_command",
		Arguments: map[string]interface{}{
			"command": "git diff",
		},
		Output:  diff,
		Success: true,
	})
	if strings.Contains(got, "• Edited app.go") {
		t.Fatalf("read-only shell diff must not be labeled as an edit, got:\n%s", got)
	}
	if !strings.Contains(got, "• Diff app.go (+1 -1)") {
		t.Fatalf("expected structured read-only diff render, got:\n%s", got)
	}
}

func TestRenderSharedChatToolEvent_DoesNotRenderTruncatedShellDiff(t *testing.T) {
	got := renderSharedChatToolEvent(runtimechatcore.ChatEvent{
		Stage:    "tool_result",
		ToolName: "execute_shell_command",
		Arguments: map[string]interface{}{
			"command": "git diff",
		},
		Output:  "--- a/app.go\n+++ b/app.go\n@@ -1 +1 @@\n-old",
		Success: true,
		Metadata: map[string]interface{}{
			"output_capture_complete": false,
			"capture_limit_reached":   true,
		},
	})
	if strings.Contains(got, "• Diff app.go") {
		t.Fatalf("truncated shell diff must stay on compact tool output: %s", got)
	}
	if !strings.Contains(got, "• Completed git diff") {
		t.Fatalf("expected compact completion fallback, got: %s", got)
	}
}

func TestRenderChatRuntimeEvent_RendersGitDiffPayloadAsReadOnlyDiff(t *testing.T) {
	got := renderChatRuntimeEvent(runtimeevents.Event{
		Type:     "tool.completed",
		ToolName: "bash",
		Payload: map[string]interface{}{
			"command_text":              "git diff -- app.go",
			"render_output_format":      "diff",
			"render_output_untruncated": true,
			"render_output":             "--- a/app.go\n+++ b/app.go\n@@ -1 +1 @@\n-old\n+new",
		},
	})
	if strings.Contains(got, "• Edited app.go") {
		t.Fatalf("read-only runtime diff was mislabeled: %s", got)
	}
	if !strings.Contains(got, "• Diff app.go (+1 -1)") {
		t.Fatalf("expected structured runtime diff, got: %s", got)
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
	}); got != "" {
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
	}); got != "" {
		t.Fatalf("unexpected llm started prompt layout render without total: %q", got)
	}
	if got := renderChatRuntimeEvent(runtimeevents.Event{Type: runtimechat.EventLLMRequestFinished, TraceID: "trace-1", Payload: map[string]interface{}{"success": true}}); got != "" {
		t.Fatalf("expected successful llm finished render to be suppressed, got %q", got)
	}
	if got := renderChatRuntimeEvent(runtimeevents.Event{Type: "llm.request.finished", TraceID: "trace-1", Payload: map[string]interface{}{"success": true}}); got != "" {
		t.Fatalf("expected dotted successful llm finished render to be suppressed, got %q", got)
	}
	if got := renderChatRuntimeEvent(runtimeevents.Event{
		Type:    "llm.request.finished",
		TraceID: "trace-usage",
		Payload: map[string]interface{}{
			"success":                 true,
			"provider":                "CODEX_LOCAL",
			"model":                   "codex-gpt-5.4",
			"context_prompt_tokens":   23099,
			"prompt_budget":           200000,
			"context_window_tokens":   270000,
			"usage_prompt_tokens":     23099,
			"usage_completion_tokens": 1663,
			"usage_total_tokens":      24762,
			"usage_cached_tokens":     2048,
			"usage_reasoning_tokens":  512,
			"budget_source":           "model_capability_auto_compact_token_limit",
			"budget_source_detail":    "provider/model capability auto-compact token limit",
			"usage_source":            "provider_reported",
			"budget_candidates": map[string]interface{}{
				"context_max_prompt_tokens":                 200000,
				"default_context_max_prompt_tokens":         4096,
				"model_capability_auto_compact_token_limit": 200000,
				"remaining_budget":                          176901,
			},
		},
	}); got != strings.Join([]string{
		"[thinking] request finished CODEX_LOCAL/codex-gpt-5.4",
		"  context: prompt=23099 budget=200000 window=270000",
		"  usage  : in=23099 out=1663 total=24762 cached=2048 cache_hit=8.9% reasoning=512 source=provider_reported",
		"  budget : source=模型能力 auto-compact token limit",
		"           detail    : provider/model capability auto-compact token limit",
		"           candidates: 4 option(s)",
		"             - context manager prompt 预算=200000",
		"             - 默认 prompt 预算=4096",
		"             - 模型能力 auto-compact token limit=200000（选中）",
		"             - 当前轮剩余预算=176901",
	}, "\n") {
		t.Fatalf("unexpected successful llm finished usage render: %q", got)
	}
	if got := renderChatRuntimeEvent(runtimeevents.Event{
		Type:    "llm.retry",
		TraceID: "trace-1",
		Payload: map[string]interface{}{
			"step":           2,
			"source":         "llm_runtime",
			"provider":       "provider-a",
			"protocol":       "openai",
			"model":          "gpt-5.4",
			"attempt":        1,
			"max_attempts":   3,
			"retry_reason":   "rate_limit",
			"retry_delay_ms": int64(25),
			"error":          "HTTP 429: rate limit reached",
		},
	}); got != "[retry] step=2 provider-a / openai / gpt-5.4 attempt=1/3 reason=rate_limit delay=25ms source=llm_runtime error=HTTP 429: rate limit reached" {
		t.Fatalf("unexpected llm retry render: %q", got)
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
	if got := renderChatRuntimeEvent(runtimeevents.Event{
		Type: "subagent.completed",
		Payload: map[string]interface{}{
			"agent_id":               "writer",
			"difficulty":             "hard",
			"route_provider":         "remote",
			"route_model":            "strong-model",
			"route_reasoning_effort": "high",
			"permission_mode":        "bypass_permissions",
			"route_source":           "difficulty_level",
			"route_warnings":         []interface{}{"provider_fallback_parent"},
			"usage_total_tokens":     1200,
		},
	}); got != "[subagent] completed writer difficulty=hard provider=remote model=strong-model reasoning=high permission_mode=bypass_permissions route_source=difficulty_level usage_total_tokens=1200 warnings=provider_fallback_parent" {
		t.Fatalf("unexpected routed subagent render: %q", got)
	}
	if got := renderChatRuntimeEvent(runtimeevents.Event{Type: "tool.requested", ToolName: "ls", Payload: map[string]interface{}{"arg_preview": "path=src"}}); got != "• Running ls path=src" {
		t.Fatalf("unexpected tool requested render: %q", got)
	}
	if got := renderChatRuntimeEvent(runtimeevents.Event{Type: "tool.requested", ToolName: "glob", Payload: map[string]interface{}{"arg_preview": "pattern=**/*.tsx path=apps/portal-modern/src"}}); got != "• Running glob pattern=**/*.tsx path=apps/portal-modern/src" {
		t.Fatalf("unexpected glob requested render: %q", got)
	}
	if got := renderChatRuntimeEvent(runtimeevents.Event{
		Type:     "tool.progress",
		ToolName: "download",
		Payload: map[string]interface{}{
			"tool_call_id": "call-progress-1",
			"message":      "fetching blob",
			"percent":      float64(42),
			"partial":      "chunk-3",
		},
	}); got != "• Progress download 42% fetching blob (chunk-3)" {
		t.Fatalf("unexpected tool progress render: %q", got)
	}
	if got := renderChatRuntimeEvent(runtimeevents.Event{
		Type:     "tool.progress",
		ToolName: "shell",
		Payload:  map[string]interface{}{"message": "still running"},
	}); got != "• Progress shell still running" {
		t.Fatalf("unexpected tool progress message-only render: %q", got)
	}
	if got := renderChatRuntimeEvent(runtimeevents.Event{
		Type:     "tool.progress",
		ToolName: "shell",
		Payload:  map[string]interface{}{},
	}); got != "" {
		t.Fatalf("expected empty progress render without details, got %q", got)
	}
	if got := renderChatRuntimeEvent(runtimeevents.Event{
		Type:     "tool.progress",
		ToolName: "shell",
		Payload: map[string]interface{}{
			"tool_call_id":       "call-stream-1",
			"stream":             true,
			"stream_channel":     "combined",
			"stream_chunk_index": float64(2),
			"partial":            "building packages...\n",
			"phase":              "stream",
		},
	}); got != "• Stream shell building packages..." {
		t.Fatalf("unexpected tool stream progress render: %q", got)
	}
	if got := renderChatRuntimeEvent(runtimeevents.Event{
		Type:     "tool.progress",
		ToolName: "shell",
		Payload: map[string]interface{}{
			"stream":                            true,
			toolprotocol.MetadataOutputMirrored: true,
			"partial":                           "building packages...\n",
		},
	}); got != "" {
		t.Fatalf("expected directly mirrored stream to skip duplicate timeline rendering, got %q", got)
	}
	if got := chatToolProgressStageDetail(runtimeevents.Event{
		Type:     "tool.progress",
		ToolName: "shell",
		Payload: map[string]interface{}{
			"stream":  true,
			"partial": "compiling main.go\n",
		},
	}); got != "shell compiling main.go" {
		t.Fatalf("unexpected stream stage detail: %q", got)
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
		"• Completed ls path=src",
		"  目录: src",
		"  📁 a/ · 📁 b/",
		"  统计: 0 个文件, 2 个目录",
	}, "\n") {
		t.Fatalf("unexpected tool completed render: %q", got)
	}
	if got := renderChatRuntimeEvent(runtimeevents.Event{
		Type:     "tool.completed",
		ToolName: "grep",
		Payload: map[string]interface{}{
			"arg_preview":       `patterns=["Popover","DialogTrigger"] paths=["apps/portal-modern/src"] glob=*.tsx`,
			"execution_backend": "rg",
			"duration_ms":       17,
			"summary_lines":     []interface{}{"24 matches"},
		},
	}); got != strings.Join([]string{
		`• Completed grep patterns=["Popover","DialogTrigger"] paths=["apps/portal-modern/src"] glob=*.tsx via rg in 17ms`,
		"  24 matches",
	}, "\n") {
		t.Fatalf("unexpected grep backend render: %q", got)
	}
	if got := renderChatRuntimeEvent(runtimeevents.Event{
		Type:     "tool.completed",
		ToolName: "edit",
		Payload: map[string]interface{}{
			"arg_preview":               "file_path=sample.txt",
			"render_output_format":      "markdown",
			"render_output_untruncated": true,
			"render_output":             "成功替换了 1 处匹配项\n\n文件差异:\n```diff\n--- a/internal/service/shop/endpoint/security.go\n+++ b/internal/service/shop/endpoint/security.go\n@@ -258,4 +258,3 @@\n updateEndpoint := map[string]interface{}{\n-    \"status\":        nextEndpointStatus,\n-    \"last_audit_id\": audit.ID.String(),\n-    \"updated_at\":    now,\n+    \"status\":     nextEndpointStatus,\n+    \"updated_at\": now,\n }\n```",
			"summary_lines":             []interface{}{"成功替换了 1 处匹配项", "文件差异:", "```diff"},
		},
	}); got != strings.Join([]string{
		`• Edited internal\service\shop\endpoint\security.go (+2 -3)`,
		`      258   updateEndpoint := map[string]interface{}{`,
		`      259 -     "status":        nextEndpointStatus,`,
		`      260 -     "last_audit_id": audit.ID.String(),`,
		`      261 -     "updated_at":    now,`,
		`      259 +     "status":     nextEndpointStatus,`,
		`      260 +     "updated_at": now,`,
		`      261   }`,
	}, "\n") {
		t.Fatalf("unexpected edit diff render: %q", got)
	}
	if got := renderChatRuntimeEvent(runtimeevents.Event{
		Type:     "tool.completed",
		ToolName: "edit",
		Payload: map[string]interface{}{
			"arg_preview":               "file_path=sample.txt",
			"duration_ms":               250,
			"render_output_format":      "markdown",
			"render_output_untruncated": true,
			"render_output":             "updated\nok",
			"workdir":                   "E:/projects/ai/ai-agent-runtime",
			"shell_display":             "pwsh",
		},
	}); got != strings.Join([]string{
		"• Completed edit file_path=sample.txt in 250ms",
		"  workdir: E:/projects/ai/ai-agent-runtime",
		"  shell: pwsh",
		"  updated",
		"  ok",
	}, "\n") {
		t.Fatalf("unexpected markdown tool context render: %q", got)
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
		"• Failed git status",
		"  failed: exit status 128",
	}, "\n") {
		t.Fatalf("unexpected failed tool render: %q", got)
	}
	if got := renderChatRuntimeEvent(runtimeevents.Event{
		Type:     "tool.completed",
		ToolName: "execute_shell_command",
		Payload: map[string]interface{}{
			"command_text":  "git status",
			"duration_ms":   1500,
			"summary_lines": []interface{}{"On branch main"},
		},
	}); got != strings.Join([]string{
		"• Completed git status in 1.5s",
		"  On branch main",
	}, "\n") {
		t.Fatalf("unexpected completed tool duration render: %q", got)
	}
	if got := renderChatRuntimeEvent(runtimeevents.Event{
		Type:     "tool.completed",
		ToolName: "execute_shell_command",
		Payload: map[string]interface{}{
			"command_text":  "git status",
			"workdir":       "E:/projects/ai/ai-agent-runtime",
			"shell_display": `pwsh (C:\Program Files\PowerShell\7\pwsh.exe)`,
			"summary_lines": []interface{}{"On branch main"},
		},
	}); got != strings.Join([]string{
		"• Completed git status",
		"  workdir: E:/projects/ai/ai-agent-runtime",
		`  shell: pwsh (C:\Program Files\PowerShell\7\pwsh.exe)`,
		"  On branch main",
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
		"• Completed go build -o .\\aicli-cachetest.exe .\\cmd\\aicli",
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
		"• Completed web_search query=天气预报",
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
		"• Completed [meta] list_mcp_resources",
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
		"• Completed [mcp] remote_search query=golang tools",
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
		"• Completed [broker] background_task command=git status",
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
	if got := renderChatRuntimeEvent(runtimeevents.Event{
		Type: team.TaskRouteResolvedEvent,
		Payload: map[string]interface{}{
			"team_id":                "team-1",
			"task_id":                "task-1",
			"assignee":               "mate-1",
			"difficulty":             "hard",
			"route_provider":         "remote",
			"route_model":            "strong-model",
			"route_reasoning_effort": "high",
			"route_source":           "difficulty_level",
			"route_attempt":          2,
			"strict":                 true,
			"fallback_used":          true,
			"fallback_reason":        "provider_fallback_parent",
			"route_warnings":         []interface{}{"capability_unknown"},
			"route_error":            "provider unavailable",
		},
	}); got != "[task route] resolved task-1 @mate-1 difficulty=hard provider=remote model=strong-model reasoning=high route_source=difficulty_level attempt=2 strict=true fallback=true fallback_reason=provider_fallback_parent warnings=capability_unknown error=provider unavailable" {
		t.Fatalf("unexpected task route render: %q", got)
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
		"  budget : source=模型能力 auto-compact token limit",
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
		"  context: prompt=8192 budget=4096",
		"  budget : source=模型能力 auto-compact token limit",
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
		"  budget : source=模型能力 auto-compact token limit",
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
		"  budget : source=模型能力 auto-compact token limit",
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
		"  budget : source=模型能力 auto-compact token limit",
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
	}); got != strings.Join([]string{
		"[context] session compact started mode=local phase=pre_turn token_before=1200 trigger_token_limit=900 max_context_tokens=10000 target=CODEX_LOCAL/codex-gpt-5.4",
		"  context: prompt=1200 budget=900 window=10000",
	}, "\n") {
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
			"compact_generation":  2,
			"compact_root_title":  "检查登录流程为什么失败",
		},
	}); got != strings.Join([]string{
		"[context] session compact completed mode=local phase=pre_turn token 1200 -> 240 compacted_messages=6 history_messages=4 checkpoint_id=cp-1 generation=2 root_title=检查登录流程为什么失败",
		"  context: prompt=240",
	}, "\n") {
		t.Fatalf("unexpected session compact completed render: %q", got)
	}

	if got := renderChatRuntimeEvent(runtimeevents.Event{
		Type:      runtimechat.EventSessionCompactStarted,
		SessionID: "session-2",
		TraceID:   "trace-compact-2",
		Payload: map[string]interface{}{
			"phase":                 "pre_turn",
			"mode":                  "local",
			"token_before":          23099,
			"prompt_budget":         200000,
			"context_window_tokens": 270000,
			"provider":              "CODEX_LOCAL",
			"model":                 "codex-gpt-5.4",
			"budget_source":         "model_capability_auto_compact_token_limit",
			"budget_source_detail":  "provider/model capability auto-compact token limit",
			"budget_candidates": map[string]interface{}{
				"context_max_prompt_tokens":                 200000,
				"default_context_max_prompt_tokens":         4096,
				"model_capability_auto_compact_token_limit": 200000,
				"remaining_budget":                          176901,
			},
		},
	}); got != strings.Join([]string{
		"[context] session compact started mode=local phase=pre_turn token_before=23099 trigger_token_limit=200000 max_context_tokens=270000 target=CODEX_LOCAL/codex-gpt-5.4",
		"  context: prompt=23099 budget=200000 window=270000",
		"  budget : source=模型能力 auto-compact token limit",
		"           detail    : provider/model capability auto-compact token limit",
		"           candidates: 4 option(s)",
		"             - context manager prompt 预算=200000",
		"             - 默认 prompt 预算=4096",
		"             - 模型能力 auto-compact token limit=200000（选中）",
		"             - 当前轮剩余预算=176901",
	}, "\n") {
		t.Fatalf("unexpected session compact started render with budget metadata: %q", got)
	}

	if got := renderChatRuntimeEvent(runtimeevents.Event{
		Type:      runtimechat.EventSessionCompactCompleted,
		SessionID: "session-3",
		TraceID:   "trace-compact-3",
		Payload: map[string]interface{}{
			"phase":                   "pre_turn",
			"mode":                    "local",
			"token_before":            23099,
			"token_after":             1892,
			"compacted_messages":      33,
			"message_count_after":     4,
			"checkpoint_id":           "chk-usage-1",
			"usage_prompt_tokens":     23099,
			"usage_completion_tokens": 512,
			"usage_total_tokens":      23611,
			"usage_cached_tokens":     2048,
			"usage_reasoning_tokens":  256,
			"usage_source":            "provider_reported",
			"budget_source":           "model_capability_auto_compact_token_limit",
			"budget_source_detail":    "provider/model capability auto-compact token limit",
			"budget_candidates": map[string]interface{}{
				"context_max_prompt_tokens":                 200000,
				"default_context_max_prompt_tokens":         4096,
				"model_capability_auto_compact_token_limit": 200000,
				"remaining_budget":                          176901,
			},
		},
	}); got != strings.Join([]string{
		"[context] session compact completed mode=local phase=pre_turn token 23099 -> 1892 compacted_messages=33 history_messages=4 checkpoint_id=chk-usage-1",
		"  context: prompt=1892",
		"  usage  : in=23099 out=512 total=23611 cached=2048 cache_hit=8.9% reasoning=256 source=provider_reported",
		"  budget : source=模型能力 auto-compact token limit",
		"           detail    : provider/model capability auto-compact token limit",
		"           candidates: 4 option(s)",
		"             - context manager prompt 预算=200000",
		"             - 默认 prompt 预算=4096",
		"             - 模型能力 auto-compact token limit=200000（选中）",
		"             - 当前轮剩余预算=176901",
	}, "\n") {
		t.Fatalf("unexpected session compact completed render with usage: %q", got)
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

func TestChatRuntimeEvents_DebugOnlyTimelineRequiresDebugMode(t *testing.T) {
	session := &ChatSession{}
	bridge := newChatRuntimeEventBridge(session)
	var rendered []string
	bridge.writeLine = func(line string) {
		rendered = append(rendered, line)
	}

	event := runtimeevents.Event{
		Type:    runtimechat.EventSessionCompactSkipped,
		TraceID: "trace-compact",
		Payload: map[string]interface{}{
			"phase":  "pre_turn",
			"mode":   "local",
			"reason": "below_limit",
		},
	}

	bridge.BeginRun()
	bridge.handleEvent(event)
	require.Empty(t, rendered)

	session.DebugMode = true
	bridge.BeginRun()
	bridge.handleEvent(event)
	require.Equal(t, []string{"[context] session compact skipped mode=local phase=pre_turn reason=below_limit"}, rendered)
}

func TestChatRuntimeEvents_RenderBudgetPanelWrapsLongLines(t *testing.T) {
	got := renderChatRuntimeEvent(runtimeevents.Event{
		Type:    "llm.request.finished",
		TraceID: "trace-wrap",
		Payload: map[string]interface{}{
			"success":               true,
			"provider":              "CODEX_LOCAL",
			"model":                 "codex-gpt-5.4",
			"context_prompt_tokens": 23099,
			"prompt_budget":         200000,
			"context_window_tokens": 270000,
			"budget_source":         "model_capability_auto_compact_token_limit",
			"budget_source_detail":  "provider/model capability auto-compact token limit is selected because the model needs enough headroom to preserve a stable handoff summary, keep recent tool outputs visible, and avoid repeated re-compaction during the next turn",
			"budget_candidates": map[string]interface{}{
				"model_capability_auto_compact_token_limit": "the chosen source is intentionally larger because the runtime wants to keep the next turn readable while still leaving room for tools, summaries, and follow-up reasoning across several more messages",
				"remaining_budget":                          176901,
			},
		},
	})

	lines := strings.Split(got, "\n")
	require.GreaterOrEqual(t, len(lines), 5)
	require.Equal(t, "[thinking] request finished CODEX_LOCAL/codex-gpt-5.4", lines[0])
	require.Equal(t, "  context: prompt=23099 budget=200000 window=270000", lines[1])
	require.Equal(t, "  budget : source=模型能力 auto-compact token limit", lines[2])

	detailContinuationIndent := strings.Repeat(" ", len("           detail    : "))
	candidateContinuationIndent := strings.Repeat(" ", len("             - "))

	hasDetailContinuation := false
	hasCandidateContinuation := false
	for _, line := range lines {
		if strings.HasPrefix(line, detailContinuationIndent) && !strings.Contains(line, "detail") {
			hasDetailContinuation = true
		}
		if strings.HasPrefix(line, candidateContinuationIndent) && !strings.Contains(line, "- ") {
			hasCandidateContinuation = true
		}
	}
	require.True(t, hasDetailContinuation, "expected long budget detail to wrap onto continuation lines: %v", lines)
	require.True(t, hasCandidateContinuation, "expected long budget candidate to wrap onto continuation lines: %v", lines)
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

func TestChatRuntimeEvents_RendersActiveTeamTaskRouteResolvedTimeline(t *testing.T) {
	session := &ChatSession{
		RuntimeSession: &runtimechat.Session{ID: "current-session"},
		ActiveTeam:     &chatTeamBinding{TeamID: "team-active"},
	}
	bridge := newChatRuntimeEventBridge(session)
	var rendered []string
	bridge.writeLine = func(line string) {
		rendered = append(rendered, line)
	}

	bridge.BeginRun()
	bridge.handleEvent(runtimeevents.Event{
		Type:      "task.failed",
		SessionID: "old-session",
		Payload: map[string]interface{}{
			"team_id": "team-old",
			"task_id": "task-old",
			"summary": "stale failure",
		},
	})
	bridge.handleEvent(runtimeevents.Event{
		Type:      team.TaskRouteResolvedEvent,
		SessionID: "old-session",
		Payload: map[string]interface{}{
			"team_id":        "team-old",
			"task_id":        "task-route",
			"assignee":       "mate-1",
			"route_provider": "openai",
			"route_model":    "gpt-test",
			"route_attempt":  1,
		},
	})
	bridge.handleEvent(runtimeevents.Event{
		Type:      team.TaskRouteResolvedEvent,
		SessionID: "old-session",
		Payload: map[string]interface{}{
			"team_id":        "team-active",
			"task_id":        "task-route",
			"assignee":       "mate-1",
			"route_provider": "openai",
			"route_model":    "gpt-test",
			"route_attempt":  1,
		},
	})

	require.Equal(t, []string{
		"[task route] resolved task-route @mate-1 provider=openai model=gpt-test attempt=1",
	}, rendered)
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

func TestChatRuntimeEvents_SuppressesAssistantMessageReasoningWhenReasoningOff(t *testing.T) {
	session := &ChatSession{
		RuntimeSession:          &runtimechat.Session{ID: "lead-session"},
		SuppressReasoningOutput: true,
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

	require.Equal(t, []string{"Hello!"}, rendered)
}

func TestChatRuntimeEvents_IgnoresNonPrimaryReasoningEvents(t *testing.T) {
	session := &ChatSession{
		RuntimeSession: &runtimechat.Session{ID: "lead-session"},
	}
	session.Interaction = newChatInteractionCoordinator(session)
	session.Interaction.liveStreamFn = func() bool { return true }

	bridge := newChatRuntimeEventBridge(session)
	var rendered []string
	bridge.writeLine = func(line string) {
		rendered = append(rendered, line)
	}
	bridge.writeReasoningDelta = func(block *runtimetypes.ReasoningBlock) {
		rendered = append(rendered, "delta:"+block.DisplayText())
	}

	bridge.BeginRun()
	for _, eventType := range []string{runtimechat.EventAssistantReasoning, "assistant.reasoning"} {
		bridge.handleEvent(runtimeevents.Event{
			Type:      eventType,
			SessionID: "child-session",
			TraceID:   "trace-child",
			Payload: map[string]interface{}{
				"reasoning": map[string]interface{}{
					"provider":   "nvidia",
					"format":     "stream_delta",
					"summary":    "child reasoning",
					"streamable": true,
				},
			},
		})
	}

	require.Empty(t, rendered)
	require.False(t, bridge.hasRenderedReasoningDelta())
	require.False(t, bridge.hasRenderedReasoningFinal())
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

func TestChatRuntimeEvents_RestoresPromptWithoutRuntimeStoreAfterRunEnds(t *testing.T) {
	session := &ChatSession{
		RuntimeSession: &runtimechat.Session{ID: "lead-session"},
	}
	bridge := newChatRuntimeEventBridge(session)
	var rendered []string
	bridge.writePrompt = func() {
		rendered = append(rendered, "PROMPT")
	}

	bridge.BeginRun()
	bridge.EndRun()

	require.Equal(t, []string{"PROMPT"}, rendered)
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

func TestChatRuntimeEvents_InjectsToolDurationFromRequestedAndCompletedTimestamps(t *testing.T) {
	session := &ChatSession{
		RuntimeSession: &runtimechat.Session{ID: "lead-session"},
	}
	bridge := newChatRuntimeEventBridge(session)
	var rendered []string
	bridge.writeLine = func(line string) {
		rendered = append(rendered, line)
	}

	base := time.Date(2026, 6, 25, 15, 0, 0, 0, time.UTC)
	bridge.BeginRun()
	bridge.handleEvent(runtimeevents.Event{
		Type:      "tool.requested",
		SessionID: "lead-session",
		ToolName:  "execute_shell_command",
		Timestamp: base,
		Payload: map[string]interface{}{
			"tool_call_id": "call-1",
			"command_text": "git diff --stat",
		},
	})
	bridge.handleEvent(runtimeevents.Event{
		Type:      "tool.completed",
		SessionID: "lead-session",
		ToolName:  "execute_shell_command",
		Timestamp: base.Add(1500 * time.Millisecond),
		Payload: map[string]interface{}{
			"tool_call_id": "call-1",
			"command_text": "git diff --stat",
			"summary_lines": []interface{}{
				"changed 2 files",
			},
		},
	})

	if !containsAllChatTimelineLines(rendered,
		"• Running git diff --stat",
		"• Completed git diff --stat in 1.5s",
		"changed 2 files",
	) {
		t.Fatalf("expected completed tool timeline with injected duration, got %v", rendered)
	}
}

func TestChatRuntimeEvents_DoesNotInjectToolDurationWithoutEventTimestamps(t *testing.T) {
	session := &ChatSession{
		RuntimeSession: &runtimechat.Session{ID: "lead-session"},
	}
	bridge := newChatRuntimeEventBridge(session)
	var rendered []string
	bridge.writeLine = func(line string) {
		rendered = append(rendered, line)
	}

	bridge.BeginRun()
	bridge.handleEvent(runtimeevents.Event{
		Type:      "tool.requested",
		SessionID: "lead-session",
		ToolName:  "execute_shell_command",
		Payload: map[string]interface{}{
			"tool_call_id": "call-1",
			"command_text": "git diff --stat",
		},
	})
	bridge.handleEvent(runtimeevents.Event{
		Type:      "tool.completed",
		SessionID: "lead-session",
		ToolName:  "execute_shell_command",
		Payload: map[string]interface{}{
			"tool_call_id":  "call-1",
			"command_text":  "git diff --stat",
			"summary_lines": []interface{}{"changed 2 files"},
		},
	})

	if containsAllChatTimelineLines(rendered,
		"• Completed git diff --stat in",
	) {
		t.Fatalf("expected no synthetic duration without explicit timestamps, got %v", rendered)
	}
	if !containsAllChatTimelineLines(rendered,
		"• Completed git diff --stat",
		"changed 2 files",
	) {
		t.Fatalf("expected completed tool timeline without duration, got %v", rendered)
	}
}

func TestChatRuntimeEvents_ToolDurationIsScopedBySessionAndTrace(t *testing.T) {
	session := &ChatSession{
		RuntimeSession: &runtimechat.Session{ID: "lead-session"},
	}
	bridge := newChatRuntimeEventBridge(session)
	var rendered []string
	bridge.writeLine = func(line string) {
		rendered = append(rendered, line)
	}

	base := time.Date(2026, 6, 25, 15, 0, 0, 0, time.UTC)
	bridge.BeginRun()
	bridge.handleEvent(runtimeevents.Event{
		Type:      "tool.requested",
		SessionID: "lead-session",
		TraceID:   "trace-lead",
		ToolName:  "execute_shell_command",
		Timestamp: base,
		Payload: map[string]interface{}{
			"tool_call_id": "call-1",
			"command_text": "git diff --stat",
		},
	})
	bridge.handleEvent(runtimeevents.Event{
		Type:      "tool.requested",
		SessionID: "teammate-session",
		TraceID:   "trace-mate",
		ToolName:  "execute_shell_command",
		Timestamp: base.Add(100 * time.Millisecond),
		Payload: map[string]interface{}{
			"tool_call_id": "call-1",
			"command_text": "git status",
		},
	})
	bridge.handleEvent(runtimeevents.Event{
		Type:      "tool.completed",
		SessionID: "lead-session",
		TraceID:   "trace-lead",
		ToolName:  "execute_shell_command",
		Timestamp: base.Add(1500 * time.Millisecond),
		Payload: map[string]interface{}{
			"tool_call_id":  "call-1",
			"command_text":  "git diff --stat",
			"summary_lines": []interface{}{"changed 2 files"},
		},
	})

	if !containsAllChatTimelineLines(rendered,
		"• Completed git diff --stat in 1.5s",
		"changed 2 files",
	) {
		t.Fatalf("expected lead tool duration to use lead start event, got %v", rendered)
	}
}

func TestChatRuntimeEvents_IgnoresLatePrimaryAssistantEventsAfterRunEnds(t *testing.T) {
	session := &ChatSession{
		RuntimeSession: &runtimechat.Session{ID: "lead-session"},
	}
	bridge := newChatRuntimeEventBridge(session)
	var rendered []string
	bridge.writeDelta = func(delta string) {
		rendered = append(rendered, "delta:"+delta)
	}
	bridge.writeLine = func(line string) {
		rendered = append(rendered, "line:"+line)
	}

	bridge.BeginRun()
	bridge.EndRun()
	bridge.handleEvent(runtimeevents.Event{
		Type:      runtimechat.EventAssistantDelta,
		SessionID: "lead-session",
		Payload:   map[string]interface{}{"delta": "late delta"},
	})
	bridge.handleEvent(runtimeevents.Event{
		Type:      runtimechat.EventAssistantMessage,
		SessionID: "lead-session",
		Payload:   map[string]interface{}{"content": "late message"},
	})

	if len(rendered) != 0 {
		t.Fatalf("expected late primary assistant events to be ignored after run end, got %v", rendered)
	}
}

func TestChatRuntimeEvents_RestoresPromptAfterLatePrimaryEventsAreSuppressed(t *testing.T) {
	runtimeStore := runtimechat.NewInMemoryRuntimeStore(16)
	require.NoError(t, runtimeStore.SaveState(context.Background(), &runtimechat.RuntimeState{
		SessionID: "lead-session",
		Status:    runtimechat.SessionIdle,
	}))

	session := &ChatSession{
		RuntimeSession:   &runtimechat.Session{ID: "lead-session"},
		LocalRuntimeHost: &localChatRuntimeHost{RuntimeStore: runtimeStore},
	}
	bridge := newChatRuntimeEventBridge(session)
	var rendered []string
	bridge.writePrompt = func() {
		rendered = append(rendered, "PROMPT")
	}

	bridge.BeginRun()
	bridge.EndRun()
	bridge.handleEvent(runtimeevents.Event{
		Type:      runtimechat.EventAssistantMessage,
		SessionID: "lead-session",
		Payload:   map[string]interface{}{"content": "late message"},
	})

	require.Equal(t, []string{"PROMPT"}, rendered)
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

func TestChatRuntimeEvents_ApprovalRequestContextLinesIncludesTeamRouteAndPermission(t *testing.T) {
	lines := approvalRequestContextLines(map[string]interface{}{
		"team_id":                "team-approval",
		"task_id":                "task-approval",
		"teammate_id":            "mate-approval",
		"permission_mode":        "default",
		"route_provider":         "openai",
		"route_model":            "gpt-test",
		"route_reasoning_effort": "high",
		"route_source":           "difficulty_level",
		"fallback_used":          true,
		"fallback_reason":        "primary unavailable",
		"route_warnings":         []interface{}{"provider_fallback_parent"},
	})

	require.Equal(t, []string{
		"team=team-approval task=task-approval teammate=mate-approval permission_mode=default",
		"provider=openai model=gpt-test reasoning=high route_source=difficulty_level fallback=true fallback_reason=primary unavailable warnings=provider_fallback_parent",
	}, lines)
}

func TestChatRuntimeEvents_RenderApprovalRequestedIncludesTeamRouteContext(t *testing.T) {
	got := renderChatRuntimeEvent(runtimeevents.Event{
		Type: runtimechat.EventApprovalRequested,
		Payload: map[string]interface{}{
			"tool_name":              "execute_shell_command",
			"team_id":                "team-approval",
			"task_id":                "task-approval",
			"teammate_id":            "mate-approval",
			"permission_mode":        "default",
			"route_provider":         "openai",
			"route_model":            "gpt-test",
			"route_reasoning_effort": "high",
			"route_source":           "difficulty_level",
			"route_warnings":         []string{"provider_fallback_parent"},
		},
	})

	require.Equal(t, strings.Join([]string{
		"[approval] execute_shell_command",
		"  team=team-approval task=task-approval teammate=mate-approval permission_mode=default",
		"  provider=openai model=gpt-test reasoning=high route_source=difficulty_level warnings=provider_fallback_parent",
	}, "\n"), got)
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

func TestApprovalPriorityPromptLines_ShowActionContextAndOptions(t *testing.T) {
	lines := approvalPriorityPromptLines(&runtimechat.ApprovalRequest{
		ToolName:  "execute_shell_command",
		Reason:    "permission_mode_requires_approval",
		RiskLevel: "high",
		ArgsJSON:  []byte(`{"command":"git commit -m test","workdir":"E:/projects/ai/ai-agent-runtime"}`),
	}, []string{"team=team-1 permission_mode=default"})

	rendered := strings.Join(lines, "\n")
	for _, want := range []string{
		"[审批] Agent 请求执行需要授权的操作",
		"[审批] 工具：execute_shell_command",
		"[审批] 原因：当前权限模式要求在执行前获得确认（permission_mode_requires_approval）",
		"[审批] 风险等级：高（high）",
		"[审批] 上下文：team=team-1 permission_mode=default",
		"[审批] 命令：git commit -m test",
		"[审批] 工作目录：E:/projects/ai/ai-agent-runtime",
		"[审批] 操作：[1] 仅本次允许  [2] 拒绝  [3] 查看完整参数",
	} {
		require.Contains(t, rendered, want)
	}
}

func TestParseApprovalPromptDecisionSupportsNumberedAndLegacyAnswers(t *testing.T) {
	tests := map[string]approvalPromptDecision{
		"1":   approvalPromptAllowOnce,
		"y":   approvalPromptAllowOnce,
		"YES": approvalPromptAllowOnce,
		"2":   approvalPromptDeny,
		"n":   approvalPromptDeny,
		"":    approvalPromptDeny,
		"3":   approvalPromptShowDetails,
		"4":   approvalPromptInvalid,
		"x":   approvalPromptInvalid,
	}
	for input, want := range tests {
		t.Run(fmt.Sprintf("input_%q", input), func(t *testing.T) {
			require.Equal(t, want, parseApprovalPromptDecision(input))
		})
	}
}

func TestUpsertPriorityPromptValidationLineDoesNotGrowPanel(t *testing.T) {
	lines := []string{"标题", "[审批] 无效选项，请输入 1。", "正文"}
	lines = upsertPriorityPromptValidationLine(lines, "[审批] 无效选项", "[审批] 无效选项，请输入 1、2、3。")
	lines = upsertPriorityPromptValidationLine(lines, "[审批] 无效选项", "[审批] 无效选项，请输入 1、2、3、4。")
	require.Len(t, lines, 3)
	require.Equal(t, "[审批] 无效选项，请输入 1、2、3、4。", lines[2])
}

func TestChatRuntimeEvents_ApprovalDetailsExpandBeforeDecision(t *testing.T) {
	session := &ChatSession{
		InputReader:   bufio.NewReader(strings.NewReader("3\n3\n1\n")),
		NoInteractive: true,
	}
	bridge := newChatRuntimeEventBridge(session)
	approval := &runtimechat.ApprovalRequest{
		ToolName: "execute_shell_command",
		Reason:   "manual approval",
		ArgsJSON: []byte(`{"command":"git status","workdir":"C:/work","mutated_paths":[]}`),
	}

	var answer chatApprovalAnswer
	var askErr error
	output := captureStdout(t, func() {
		answer, askErr = bridge.askApproval(approval, nil)
	})
	require.NoError(t, askErr)
	require.True(t, answer.Allowed)
	require.False(t, answer.Reuse)
	require.Contains(t, output, "[审批] 完整参数：")
	require.Contains(t, output, `"mutated_paths": []`)
	require.GreaterOrEqual(t, strings.Count(output, "[审批] 请选择"), 2)
}

func TestChatRuntimeEvents_ApprovalReuseRequiresExplicitOption(t *testing.T) {
	session := &ChatSession{
		InputReader:       bufio.NewReader(strings.NewReader("4\n")),
		NoInteractive:     true,
		ApprovalReuseMode: chatApprovalReuseSessionReadOnlyShell,
	}
	bridge := newChatRuntimeEventBridge(session)
	approval := &runtimechat.ApprovalRequest{
		ToolName: "execute_shell_command",
		ArgsJSON: []byte(`{"command":"git status","workdir":"C:/work"}`),
	}

	answer, err := bridge.askApproval(approval, nil)
	require.NoError(t, err)
	require.True(t, answer.Allowed)
	require.True(t, answer.Reuse)
	require.Contains(t, approvalDecisionPromptWithReuse("当前会话内"), "[4] 允许并在当前会话内复用")
}

func TestApprovalFullParameterLinesLimitsVisibleOutput(t *testing.T) {
	rawLines := make([]string, 20)
	for index := range rawLines {
		rawLines[index] = fmt.Sprintf("line-%02d=%s", index+1, strings.Repeat("x", 300))
	}
	lines := approvalFullParameterLines(&runtimechat.ApprovalRequest{
		ArgsJSON: []byte(strings.Join(rawLines, "\n")),
	})

	require.LessOrEqual(t, len(lines), 15)
	require.Contains(t, strings.Join(lines, "\n"), "已省略 8 行")
	require.Contains(t, strings.Join(lines, "\n"), "...")
}

func TestQuestionPromptNumbersSuggestionsAndMapsSelection(t *testing.T) {
	suggestions := []string{"继续执行", "", "先查看差异"}
	lines := questionPriorityPromptLines("下一步怎么处理？", suggestions)
	require.Equal(t, []string{
		"[提问] Agent 需要你的补充信息",
		"[提问] 问题：下一步怎么处理？",
		"[提问] 1. 继续执行",
		"[提问] 2. 先查看差异",
	}, lines)
	require.Equal(t, "先查看差异", mapQuestionSuggestionAnswer("2", suggestions))
	require.Equal(t, "使用另一种方案", mapQuestionSuggestionAnswer("使用另一种方案", suggestions))
	require.Empty(t, mapQuestionSuggestionAnswer("", suggestions))
	require.Contains(t, questionAnswerPrompt(false, true), "直接 Enter 跳过")
	require.Contains(t, questionAnswerPrompt(true, true), "必答")
}

func TestChatRuntimeEvents_QuestionNumberSelectsSuggestion(t *testing.T) {
	session := &ChatSession{
		InputReader:   bufio.NewReader(strings.NewReader("2\n")),
		NoInteractive: true,
	}
	bridge := newChatRuntimeEventBridge(session)

	var answer string
	var askErr error
	output := captureStdout(t, func() {
		answer, askErr = bridge.askQuestion("请选择处理方式", []string{"继续", "停止"}, true)
	})
	require.NoError(t, askErr)
	require.Equal(t, "停止", answer)
	require.Contains(t, output, "[提问] 1. 继续")
	require.Contains(t, output, "[提问] 2. 停止")
	require.Contains(t, output, "可输入建议编号")
}

func TestChatRuntimeEvents_RequiredQuestionRejectsEmptyButOptionalQuestionAllowsIt(t *testing.T) {
	requiredSession := &ChatSession{
		InputReader:   bufio.NewReader(strings.NewReader("\n最终回答\n")),
		NoInteractive: true,
	}
	requiredBridge := newChatRuntimeEventBridge(requiredSession)
	var requiredAnswer string
	var requiredErr error
	requiredOutput := captureStdout(t, func() {
		requiredAnswer, requiredErr = requiredBridge.askQuestion("请补充说明", nil, true)
	})
	require.NoError(t, requiredErr)
	require.Equal(t, "最终回答", requiredAnswer)
	require.Contains(t, requiredOutput, "此问题为必答项")

	optionalSession := &ChatSession{
		InputReader:   bufio.NewReader(strings.NewReader("\n")),
		NoInteractive: true,
	}
	optionalBridge := newChatRuntimeEventBridge(optionalSession)
	var optionalAnswer string
	var optionalErr error
	optionalOutput := captureStdout(t, func() {
		optionalAnswer, optionalErr = optionalBridge.askQuestion("还有补充吗？", nil, false)
	})
	require.NoError(t, optionalErr)
	require.Empty(t, optionalAnswer)
	require.Contains(t, optionalOutput, "直接 Enter 跳过")
}

func TestPushChatComposerAgentStageRestoresPreviousStageWithoutOverwritingInterrupt(t *testing.T) {
	session := &ChatSession{NoInteractive: true}
	coord := newChatInteractionCoordinator(session)
	session.Interaction = coord
	coord.SetAgentStageDetail(chatAgentStageToolRunning, "shell_command")

	restore := pushChatComposerAgentStage(session, chatAgentStageAwaitingApproval)
	require.Equal(t, chatAgentStageAwaitingApproval, coord.AgentStage())
	require.Equal(t, chatInputModeApproval, coord.InputMode())
	restore()
	require.Equal(t, chatAgentStageToolRunning, coord.AgentStage())
	require.Equal(t, "shell_command", coord.AgentStageDetail())
	require.Equal(t, chatInputModeChat, coord.InputMode())

	restore = pushChatComposerAgentStage(session, chatAgentStageAwaitingAnswer)
	require.Equal(t, chatInputModeAnswer, coord.InputMode())
	coord.SetAgentStage(chatAgentStageStopping)
	restore()
	require.Equal(t, chatAgentStageStopping, coord.AgentStage())
	require.Equal(t, chatInputModeChat, coord.InputMode())
}

func TestChatRuntimeEvents_PrimaryRunUpdatesComposerAgentStage(t *testing.T) {
	runtimeSession := &runtimechat.Session{ID: "primary-session"}
	session := &ChatSession{RuntimeSession: runtimeSession, NoInteractive: true}
	coord := newChatInteractionCoordinator(session)
	session.Interaction = coord
	bridge := newChatRuntimeEventBridge(session)

	bridge.BeginRun()
	require.Equal(t, chatAgentStagePlanning, coord.AgentStage())

	bridge.handleEvent(runtimeevents.Event{
		Type:      runtimechat.EventToolStarted,
		SessionID: runtimeSession.ID,
		ToolName:  "execute_shell_command",
		Payload:   map[string]interface{}{"tool_call_id": "call-1"},
	})
	require.Equal(t, chatAgentStageToolRunning, coord.AgentStage())
	require.Equal(t, "execute_shell_command", coord.AgentStageDetail())

	bridge.handleEvent(runtimeevents.Event{
		Type:      "tool.progress",
		SessionID: runtimeSession.ID,
		ToolName:  "execute_shell_command",
		Payload: map[string]interface{}{
			"tool_call_id": "call-1",
			"message":      "compiling",
			"percent":      float64(60),
		},
	})
	require.Equal(t, chatAgentStageToolRunning, coord.AgentStage())
	require.Equal(t, "execute_shell_command 60% compiling", coord.AgentStageDetail())

	bridge.handleEvent(runtimeevents.Event{
		Type:      runtimechat.EventToolFinished,
		SessionID: runtimeSession.ID,
		ToolName:  "execute_shell_command",
		Payload:   map[string]interface{}{"tool_call_id": "call-1"},
	})
	require.Equal(t, chatAgentStagePlanning, coord.AgentStage())
	require.Empty(t, coord.AgentStageDetail())

	bridge.handleEvent(runtimeevents.Event{
		Type:      runtimechat.EventLLMRequestStarted,
		SessionID: runtimeSession.ID,
	})
	require.Equal(t, chatAgentStagePlanning, coord.AgentStage())
	require.Empty(t, coord.AgentStageDetail())

	bridge.EndRun()
	// Codex-aligned: natural completion returns to idle/Ready, not sticky Completed.
	require.Equal(t, chatAgentStageIdle, coord.AgentStage())
}

func TestChatRuntimeEvents_SecondaryRunDoesNotOverrideComposerAgentStage(t *testing.T) {
	runtimeSession := &runtimechat.Session{ID: "primary-session"}
	session := &ChatSession{RuntimeSession: runtimeSession, NoInteractive: true}
	coord := newChatInteractionCoordinator(session)
	session.Interaction = coord
	bridge := newChatRuntimeEventBridge(session)
	bridge.BeginRun()

	bridge.handleEvent(runtimeevents.Event{
		Type:      runtimechat.EventToolStarted,
		SessionID: "teammate-session",
		ToolName:  "write_file",
	})
	require.Equal(t, chatAgentStagePlanning, coord.AgentStage())
	require.Empty(t, coord.AgentStageDetail())

	bridge.setRunError(fmt.Errorf("run failed"))
	bridge.EndRun()
	require.Equal(t, chatAgentStageFailed, coord.AgentStage())
}

func TestChatRuntimeEvents_InterruptedRunKeepsComposerStoppingStage(t *testing.T) {
	session := &ChatSession{NoInteractive: true}
	coord := newChatInteractionCoordinator(session)
	session.Interaction = coord
	bridge := newChatRuntimeEventBridge(session)

	bridge.BeginRun()
	// Keep cleanup in-flight so EndRun still surfaces Stopping while stop work runs.
	cleanup := make(chan struct{})
	session.interrupted.Store(true)
	session.setInterruptCleanup(cleanup)
	coord.SetAgentStage(chatAgentStageStopping)
	require.Equal(t, chatRuntimeInterruptedEndRunDrainTimeout, bridge.endRunDrainTimeout())
	require.Equal(t, chatRuntimeInterruptedEndRunDrainTimeout, chatRuntimeEventDrainTimeout(session, 12*time.Second))
	// Simulate an event processor that stopped making progress during interrupt.
	// EndRun must use the short interrupt drain rather than the normal 8s bound.
	bridge.progressMu.Lock()
	bridge.enqueuedEvents = 1
	bridge.processedEvents = 0
	bridge.progressMu.Unlock()
	started := time.Now()
	bridge.EndRun()
	require.Less(t, time.Since(started), time.Second)

	require.Equal(t, chatAgentStageStopping, coord.AgentStage())
	close(cleanup)
	session.finishInterruptCleanupUI()
	require.Equal(t, chatAgentStageIdle, coord.AgentStage())
	session.ResetInterrupt()
	require.Equal(t, chatAgentStageIdle, coord.AgentStage())
}

func TestChatRuntimeEvents_InterruptedRunReturnsReadyAfterCleanup(t *testing.T) {
	session := &ChatSession{NoInteractive: true}
	coord := newChatInteractionCoordinator(session)
	session.Interaction = coord
	bridge := newChatRuntimeEventBridge(session)

	bridge.BeginRun()
	// No LocalRuntimeHost: interrupt cleanup finishes immediately and must leave Ready.
	session.InterruptPreservePendingInput()
	session.waitForInterruptCleanupWithin(time.Second)
	bridge.EndRun()
	require.Equal(t, chatAgentStageIdle, coord.AgentStage())
}

func TestChatRuntimeEvents_RenderApprovalDecisionKeepsStatusOnly(t *testing.T) {
	var rendered bytes.Buffer
	bridge := newChatRuntimeEventBridge(&ChatSession{})
	bridge.writeLine = func(line string) {
		rendered.WriteString(line)
	}

	bridge.renderApprovalDecision(&runtimechat.ApprovalRequest{
		ToolName: "execute_shell_command",
		ArgsJSON: []byte(`{"command":"git commit -m \"feat: add nginx configuration\"","workdir":"E:/projects/ai/ai-agent-runtime"}`),
	}, true)

	output := rendered.String()
	if !strings.Contains(output, "[approval] approved: execute_shell_command") {
		t.Fatalf("expected approval decision line, got %q", output)
	}
	if strings.Contains(output, "[approval] command=") || strings.Contains(output, "[approval] workdir=") {
		t.Fatalf("expected approval decision to avoid duplicating transcript details, got %q", output)
	}
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
	bridge.eventQueue = make(chan chatRuntimeQueuedEvent, 1)

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

func TestChatRuntimeEvents_HandleBackpressuresOnRetainedBytes(t *testing.T) {
	session := &ChatSession{
		Stream:         true,
		RuntimeSession: &runtimechat.Session{ID: "lead-session"},
	}
	bridge := newChatRuntimeEventBridge(session)
	first := runtimeevents.Event{
		Type:      runtimechat.EventAssistantDelta,
		SessionID: "lead-session",
		Payload:   map[string]interface{}{"delta": strings.Repeat("a", 128)},
	}
	second := runtimeevents.Event{
		Type:      runtimechat.EventAssistantDelta,
		SessionID: "lead-session",
		Payload:   map[string]interface{}{"delta": strings.Repeat("b", 128)},
	}
	bridge.eventQueueByteLimit = runtimeevents.ApproximateEventBytes(first)

	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	bridge.writeDelta = func(string) {
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
		releaseOnce.Do(func() { close(release) })
		close(bridge.eventQueue)
		<-done
	}()

	bridge.BeginRun()
	bridge.Handle(first)
	<-started
	secondDone := make(chan struct{})
	go func() {
		defer close(secondDone)
		bridge.Handle(second)
	}()

	select {
	case <-secondDone:
		t.Fatal("expected byte budget to block the second event while the first payload is retained")
	case <-time.After(30 * time.Millisecond):
	}

	releaseOnce.Do(func() { close(release) })
	<-secondDone
	bridge.WaitForCurrentEvents(300 * time.Millisecond)
	bridge.eventQueueMu.Lock()
	queuedBytes := bridge.eventQueueBytes
	bridge.eventQueueMu.Unlock()
	require.Zero(t, queuedBytes)
}

func TestChatRuntimeEvents_AllowsOneEventLargerThanByteLimit(t *testing.T) {
	bridge := newChatRuntimeEventBridge(&ChatSession{})
	bridge.eventQueueByteLimit = 1
	event := runtimeevents.Event{
		Type:    "large.event",
		Payload: map[string]interface{}{"content": strings.Repeat("x", 1024)},
	}

	handled := make(chan struct{})
	go func() {
		defer close(handled)
		bridge.Handle(event)
	}()
	select {
	case <-handled:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected a single oversized event to pass through an empty byte budget")
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		bridge.run()
	}()
	bridge.WaitForCurrentEvents(300 * time.Millisecond)
	close(bridge.eventQueue)
	<-done
	bridge.eventQueueMu.Lock()
	queuedBytes := bridge.eventQueueBytes
	bridge.eventQueueMu.Unlock()
	require.Zero(t, queuedBytes)
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
	require.True(t, bridge.HasRenderedAssistantFinalResponse("Hello"))
}

func TestChatRuntimeEventBridgeReleasesFinalizedDeltaContent(t *testing.T) {
	bridge := newChatRuntimeEventBridge(&ChatSession{})
	content := strings.Repeat("streamed-response-", 32<<10)
	bridge.BeginRun()
	for offset := 0; offset < len(content); offset += 4096 {
		end := offset + 4096
		if end > len(content) {
			end = len(content)
		}
		bridge.markAssistantDeltaRendered(content[offset:end])
	}
	require.Equal(t, len(content), bridge.renderedAssistantDeltaContent.Len())

	bridge.markAssistantDeltaFinalized()
	require.Zero(t, bridge.renderedAssistantDeltaContent.Len())
	require.True(t, bridge.HasRenderedAssistantFinalResponse(content))
	require.False(t, bridge.HasRenderedAssistantFinalResponse(content+"changed"))

	bridge.BeginRun()
	require.False(t, bridge.HasRenderedAssistantFinalResponse(content))
	bridge.markAssistantDeltaRendered(content)
	bridge.markAssistantFinalRendered(content)
	require.Zero(t, bridge.renderedAssistantDeltaContent.Len())
	require.True(t, bridge.HasRenderedAssistantFinalResponse(content))
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
	require.True(t, bridge.HasRenderedAssistantFinalResponse("`E:\\projects\\ai\\ai-gateway` 的 git 状态如下：\n\n- 当前分支：`main`"))
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

func TestChatRuntimeEvents_OrdersAndDeduplicatesIdentifiedAssistantDeltas(t *testing.T) {
	session := &ChatSession{
		Stream:         true,
		RuntimeSession: &runtimechat.Session{ID: "lead-session"},
	}
	bridge := newChatRuntimeEventBridge(session)
	var deltas []string
	bridge.writeDelta = func(delta string) {
		deltas = append(deltas, delta)
	}
	event := func(sequence uint64, delta string) runtimeevents.Event {
		return runtimeevents.Event{
			Type:      runtimechat.EventAssistantDelta,
			SessionID: "lead-session",
			Payload: map[string]interface{}{
				"turn_id":   "turn-1",
				"stream_id": "stream-1",
				"sequence":  sequence,
				"mode":      "append",
				"delta":     delta,
			},
		}
	}

	bridge.BeginRun()
	bridge.handleEvent(runtimeevents.Event{
		Type:      runtimechat.EventSessionStart,
		SessionID: "lead-session",
		Payload:   map[string]interface{}{"turn_id": "turn-1"},
	})
	bridge.handleEvent(event(2, "B"))
	require.Empty(t, deltas)
	bridge.handleEvent(event(1, "A"))
	bridge.handleEvent(event(2, "duplicate"))
	bridge.handleEvent(event(3, "C"))

	require.Equal(t, []string{"A", "B", "C"}, deltas)
}

func TestChatRuntimeEvents_LateOldTurnDeltaCannotRenderInNewRun(t *testing.T) {
	session := &ChatSession{
		Stream:         true,
		RuntimeSession: &runtimechat.Session{ID: "lead-session"},
	}
	bridge := newChatRuntimeEventBridge(session)
	var deltas []string
	bridge.writeDelta = func(delta string) {
		deltas = append(deltas, delta)
	}
	start := func(turnID string) {
		bridge.handleEvent(runtimeevents.Event{
			Type:      runtimechat.EventSessionStart,
			SessionID: "lead-session",
			Payload:   map[string]interface{}{"turn_id": turnID},
		})
	}
	delta := func(turnID, streamID, content string) runtimeevents.Event {
		return runtimeevents.Event{
			Type:      runtimechat.EventAssistantDelta,
			SessionID: "lead-session",
			Payload: map[string]interface{}{
				"turn_id": turnID, "stream_id": streamID,
				"sequence": uint64(1), "mode": "append", "delta": content,
			},
		}
	}

	bridge.BeginRun()
	start("turn-old")
	bridge.handleEvent(delta("turn-old", "stream-old", "old-before-end"))
	bridge.EndRun()

	bridge.BeginRun()
	start("turn-new")
	bridge.handleEvent(delta("turn-old", "stream-old-late", "must-not-render"))
	bridge.handleEvent(delta("turn-new", "stream-new", "new"))

	require.Equal(t, []string{"old-before-end", "new"}, deltas)
}

func TestChatRuntimeEvents_IdentitylessPrimaryEventCannotMutateIdentifiedRun(t *testing.T) {
	session := &ChatSession{
		Stream:         true,
		RuntimeSession: &runtimechat.Session{ID: "lead-session"},
	}
	session.Interaction = newChatInteractionCoordinator(session)
	t.Cleanup(session.Interaction.Shutdown)
	bridge := newChatRuntimeEventBridge(session)
	questionCalls := 0
	bridge.askQuestion = func(string, []string, bool) (string, error) {
		questionCalls++
		return "must not be used", nil
	}
	bridge.BeginRun()
	bridge.handleEvent(runtimeevents.Event{
		Type:      runtimechat.EventSessionStart,
		SessionID: "lead-session",
		Payload:   map[string]interface{}{"turn_id": "turn-current"},
	})

	bridge.handleEvent(runtimeevents.Event{
		Type:      "tool.requested",
		SessionID: "lead-session",
		ToolName:  "stale_tool",
		Payload:   map[string]interface{}{"tool_call_id": "old-call"},
	})
	require.NotEqual(t, chatAgentStageToolRunning, session.Interaction.AgentStage())

	bridge.handleEvent(runtimeevents.Event{
		Type:      "tool.requested",
		SessionID: "lead-session",
		ToolName:  "current_tool",
		Payload: map[string]interface{}{
			"turn_id":      "turn-current",
			"tool_call_id": "current-call",
		},
	})
	require.Equal(t, chatAgentStageToolRunning, session.Interaction.AgentStage())

	bridge.handleEvent(runtimeevents.Event{
		Type:      runtimechat.EventQuestionAsked,
		SessionID: "lead-session",
		Payload: map[string]interface{}{
			"turn_id":     "turn-old",
			"question_id": "old-question",
			"prompt":      "must not prompt",
			"required":    true,
		},
	})
	require.Zero(t, questionCalls)
}

func TestChatRuntimeEvents_FinalCommitUsesTurnIdentityNotContentDigest(t *testing.T) {
	session := &ChatSession{
		Stream:         true,
		RuntimeSession: &runtimechat.Session{ID: "lead-session"},
	}
	bridge := newChatRuntimeEventBridge(session)
	var rendered []string
	bridge.renderResponse = func(content string) {
		rendered = append(rendered, content)
	}

	bridge.BeginRun()
	bridge.handleEvent(runtimeevents.Event{
		Type:      runtimechat.EventSessionStart,
		SessionID: "lead-session",
		Payload:   map[string]interface{}{"turn_id": "turn-1"},
	})
	bridge.handleEvent(runtimeevents.Event{
		Type:      runtimechat.EventAssistantMessage,
		SessionID: "lead-session",
		Payload: map[string]interface{}{
			"turn_id": "turn-1", "stream_id": "stream-1",
			"sequence": uint64(1), "mode": "snapshot", "content": "runtime final",
		},
	})
	bridge.handleEvent(runtimeevents.Event{
		Type:      runtimechat.EventAssistantMessage,
		SessionID: "lead-session",
		Payload: map[string]interface{}{
			"turn_id": "turn-1", "stream_id": "stream-1",
			"sequence": uint64(1), "mode": "snapshot", "content": "duplicate final with different content",
		},
	})
	bridge.BindExecutorTurn("turn-1")

	require.Equal(t, []string{"runtime final"}, rendered)
	require.True(t, bridge.HasRenderedAssistantFinalResponse("executor result differs slightly"))
}

func TestFinishSuccessfulChatSend_DoesNotCommitDifferentExecutorTextForFinalizedTurn(t *testing.T) {
	session := &ChatSession{
		ChatExecutor:   newAICLIActorChatExecutor(),
		RuntimeSession: &runtimechat.Session{ID: "lead-session"},
	}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	var rendered []string
	bridge.renderResponse = func(content string) {
		rendered = append(rendered, content)
	}

	bridge.BeginRun()
	bridge.handleEvent(runtimeevents.Event{
		Type:      runtimechat.EventSessionStart,
		SessionID: "lead-session",
		Payload:   map[string]interface{}{"turn_id": "turn-1"},
	})
	bridge.handleEvent(runtimeevents.Event{
		Type:      runtimechat.EventAssistantMessage,
		SessionID: "lead-session",
		Payload: map[string]interface{}{
			"turn_id": "turn-1", "stream_id": "stream-1",
			"sequence": uint64(1), "mode": "snapshot", "content": "runtime final",
		},
	})
	bridge.BindExecutorTurn("turn-1")
	bridge.EndRun()

	output := captureStdout(t, func() {
		finishSuccessfulChatSend(session, "executor result differs slightly", false)
	})
	require.Equal(t, []string{"runtime final"}, rendered)
	require.NotContains(t, output, "executor result differs slightly")
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

func TestReliabilityEvalActorExecutorApprovalBridgeDelaysAndExecutesToolOnce(t *testing.T) {
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
		PermissionMode:   runtimepolicy.ModeDefault,
		ActiveTeam:       &chatTeamBinding{TeamID: "team-approval", AgentID: "mate-approval", TaskID: "task-approval"},
	}
	host.BaseSession = session

	var (
		rendered      bytes.Buffer
		approvalCalls atomic.Int32
	)
	approvalStarted := make(chan struct{}, 1)
	approvalRelease := make(chan struct{})
	var releaseApproval sync.Once
	t.Cleanup(func() {
		releaseApproval.Do(func() { close(approvalRelease) })
	})
	bridge := newChatRuntimeEventBridge(session)
	bridge.writeLine = func(line string) {
		rendered.WriteString(line)
		rendered.WriteString("\n")
	}
	bridge.askApproval = func(approval *runtimechat.ApprovalRequest, contextLines []string) (chatApprovalAnswer, error) {
		approvalCalls.Add(1)
		if approval == nil || approval.ToolName != "team_echo" {
			t.Errorf("unexpected approval request: %+v", approval)
		}
		if approval != nil && approval.Reason != "manual approval" {
			t.Errorf("unexpected approval reason: %q", approval.Reason)
		}
		if !strings.Contains(strings.Join(contextLines, "\n"), "team=team-approval task=task-approval teammate=mate-approval permission_mode=default") {
			t.Errorf("approval context did not preserve team run metadata: %+v", contextLines)
		}
		select {
		case approvalStarted <- struct{}{}:
		default:
		}
		<-approvalRelease
		return chatApprovalAnswer{Allowed: true}, nil
	}
	session.RuntimeEventBridge = bridge

	type executeResult struct {
		output string
		err    error
	}
	executeDone := make(chan executeResult, 1)
	executeCtx, cancelExecute := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelExecute()
	go func() {
		output, executeErr := session.ChatExecutor.Execute(executeCtx, session, "trigger approval")
		executeDone <- executeResult{output: output, err: executeErr}
	}()

	select {
	case <-approvalStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for delayed approval request")
	}
	if approvalCalls.Load() != 1 {
		t.Fatalf("expected one pending approval prompt, got %d", approvalCalls.Load())
	}
	if mcpManager.callCount != 0 {
		t.Fatalf("tool executed before approval was resolved: %d calls", mcpManager.callCount)
	}
	select {
	case early := <-executeDone:
		t.Fatalf("execution completed before approval release: output=%q err=%v", early.output, early.err)
	case <-time.After(150 * time.Millisecond):
	}
	if mcpManager.callCount != 0 {
		t.Fatalf("tool executed while approval remained delayed: %d calls", mcpManager.callCount)
	}
	releaseApproval.Do(func() { close(approvalRelease) })

	var completed executeResult
	select {
	case completed = <-executeDone:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for execution after approval release")
	}
	output, err := completed.output, completed.err
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
				case "shell", "bash", "execute_shell_command":
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
		PermissionMode:    runtimepolicy.ModeDefault,
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
	bridge.askApproval = func(approval *runtimechat.ApprovalRequest, contextLines []string) (chatApprovalAnswer, error) {
		approvalCalls.Add(1)
		if approval == nil {
			t.Fatal("expected approval request")
		}
		if approval.Reason != "manual approval" {
			t.Fatalf("unexpected approval reason: %q", approval.Reason)
		}
		require.Contains(t, contextLines, "team=team-approval task=task-approval teammate=lead permission_mode=default")
		return chatApprovalAnswer{Allowed: true, Reuse: true}, nil
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
	if mcpManager.callCount != 3 {
		t.Fatalf("expected shell/bash/execute_shell_command to execute, got %d", mcpManager.callCount)
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
	if strings.Contains(rendered.String(), "[approval] shell") {
		t.Fatalf("expected cached approval for shell to stay silent, got %q", rendered.String())
	}
	if strings.Contains(rendered.String(), "[approval] auto-approved shell") {
		t.Fatalf("expected no auto-approved line for cached shell approval, got %q", rendered.String())
	}
}

func TestShowChatRuntimePriorityPrompt_RendersPromptBlockAndReturnsReadablePrompt(t *testing.T) {
	var session ChatSession
	output := captureStdout(t, func() {
		readPrompt, cleanup, transient := showChatRuntimePriorityPrompt(&session, []string{
			"[approval] command=git status",
			"[approval] cwd=C:/work",
		}, "[approval] allow bash? [y/N]: ")
		defer cleanup()
		if readPrompt != "[approval] allow bash? [y/N]: " {
			t.Fatalf("unexpected read prompt: %q", readPrompt)
		}
		if transient {
			t.Fatal("expected plain stdout prompt to be persistent")
		}
	})

	if !strings.Contains(output, "[approval] command=git status") {
		t.Fatalf("expected approval command line in output, got %q", output)
	}
	if !strings.Contains(output, "[approval] allow bash? [y/N]: ") {
		t.Fatalf("expected approval prompt in output, got %q", output)
	}
}

func TestRenderChatRuntimePriorityPromptTranscript_PersistsApprovalDetails(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	var output bytes.Buffer
	coord.SetWriter(&output)
	session.Interaction = coord

	renderChatRuntimePriorityPromptTranscript(session, []string{
		"[approval] command=git commit -m \"feat: add nginx configuration\"",
		"[approval] workdir=E:\\projects\\ai\\ai-gateway",
	}, "[approval] allow bash (permission_mode_requires_approval)? [y/N]: ", "y")

	rendered := output.String()
	for _, want := range []string{
		"[approval] command=git commit -m \"feat: add nginx configuration\"",
		"[approval] workdir=E:\\projects\\ai\\ai-gateway",
		"[approval] allow bash (permission_mode_requires_approval)? [y/N]: y",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("expected persisted approval transcript to contain %q, got %q", want, rendered)
		}
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

func TestChatRuntimeEvents_ApprovalGrantStatusAndClear(t *testing.T) {
	now := time.Now().UTC()
	bridge := newChatRuntimeEventBridge(&ChatSession{})
	bridge.approvalGrants = map[string]time.Time{
		"session:session-1|readonly_shell": now.Add(5 * time.Minute),
		"team:team-1|readonly_network":     now.Add(-time.Second),
	}

	lines := bridge.approvalGrantStatusLines(now)
	require.Len(t, lines, 1)
	require.Contains(t, lines[0], "scope=当前会话")
	require.Contains(t, lines[0], "family=readonly_shell")
	require.Contains(t, lines[0], "expires_in=5m0s")
	require.Equal(t, 1, bridge.clearApprovalGrants())
	require.Empty(t, bridge.approvalGrantStatusLines(now))
	require.Equal(t, 0, bridge.clearApprovalGrants())
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

func TestChatRuntimeEvents_ShellAliasesShareReadonlyGrantKey(t *testing.T) {
	bridge := newChatRuntimeEventBridge(&ChatSession{
		ApprovalReuseMode: chatApprovalReuseSessionReadOnlyShell,
	})
	bridge.BeginRun()

	aliases := []string{"shell", "bash", "execute_shell_command"}
	keys := make([]string, 0, len(aliases))
	for _, name := range aliases {
		key := bridge.autoApprovalGrantKey("session-1", &runtimechat.ApprovalRequest{
			ToolName: name,
			ArgsJSON: []byte(`{"command":"git status --short"}`),
		})
		if key == "" {
			t.Fatalf("expected readonly grant key for %s", name)
		}
		if !strings.Contains(key, "readonly_shell") {
			t.Fatalf("expected readonly_shell family for %s, got %q", name, key)
		}
		keys = append(keys, key)
	}
	for i := 1; i < len(keys); i++ {
		if keys[i] != keys[0] {
			t.Fatalf("shell aliases should share grant key: %q vs %q", keys[0], keys[i])
		}
	}

	// Approving via canonical shell should cover bash/execute_shell_command.
	bridge.rememberApprovalGrant(keys[0])
	for _, name := range aliases {
		key := bridge.autoApprovalGrantKey("session-1", &runtimechat.ApprovalRequest{
			ToolName: name,
			ArgsJSON: []byte(`{"command":"ls"}`),
		})
		if !bridge.hasApprovalGrant(key) {
			t.Fatalf("expected shared approval grant to cover %s", name)
		}
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
	runtimeStore := runtimechat.NewInMemoryRuntimeStore(16)
	require.NoError(t, runtimeStore.SaveState(context.Background(), &runtimechat.RuntimeState{
		SessionID: "lead-session",
		Status:    runtimechat.SessionIdle,
	}))

	session := &ChatSession{
		Stream:           true,
		RuntimeSession:   &runtimechat.Session{ID: "lead-session"},
		LocalRuntimeHost: &localChatRuntimeHost{RuntimeStore: runtimeStore},
	}
	bridge := newChatRuntimeEventBridge(session)
	var rendered []string
	finalized := 0
	bridge.writeDelta = func(delta string) {
		rendered = append(rendered, "delta:"+delta)
	}
	bridge.finalizeDelta = func() {
		rendered = append(rendered, "finalize")
		finalized++
	}
	bridge.writePrompt = func() {
		rendered = append(rendered, "PROMPT")
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
	bridge.EndRun()

	require.Equal(t, []string{"delta:Analyzing the issue...", "finalize", "PROMPT"}, rendered)
	if bridge.HasRenderedAssistantFinal() {
		t.Fatal("expected session_end delta flush not to mark final assistant response rendered")
	}
	if finalized != 1 {
		t.Fatalf("expected delta to be finalized on session_end, got %d finalizations", finalized)
	}
}

func TestChatRuntimeEvents_RendersPrimaryAssistantMessageAfterSessionEndDeltaFlush(t *testing.T) {
	session := &ChatSession{
		Stream:         true,
		RuntimeSession: &runtimechat.Session{ID: "lead-session"},
	}
	bridge := newChatRuntimeEventBridge(session)
	var rendered []string
	finalized := 0
	bridge.writeDelta = func(delta string) {
		rendered = append(rendered, "delta:"+delta)
	}
	bridge.finalizeDelta = func() {
		rendered = append(rendered, "finalize")
		finalized++
	}
	bridge.renderResponse = func(response string) {
		rendered = append(rendered, "response:"+response)
	}

	bridge.BeginRun()
	bridge.handleEvent(runtimeevents.Event{
		Type:      runtimechat.EventAssistantDelta,
		SessionID: "lead-session",
		Payload:   map[string]interface{}{"delta": "Working..."},
	})
	bridge.handleEvent(runtimeevents.Event{
		Type:      runtimechat.EventSessionEnd,
		SessionID: "lead-session",
		Payload:   map[string]interface{}{"success": true},
	})
	bridge.handleEvent(runtimeevents.Event{
		Type:      runtimechat.EventAssistantMessage,
		SessionID: "lead-session",
		Payload:   map[string]interface{}{"content": "Final parent response"},
	})

	require.Equal(t, []string{"delta:Working...", "finalize", "response:Final parent response"}, rendered)
	require.Equal(t, 1, finalized)
	require.True(t, bridge.HasRenderedAssistantFinal())
	require.True(t, bridge.HasRenderedAssistantFinalResponse("Final parent response"))
}

func TestInteractiveActorResponseAlreadyRenderedRequiresMatchingContent(t *testing.T) {
	session := &ChatSession{
		Stream:         true,
		RuntimeSession: &runtimechat.Session{ID: "lead-session"},
		ChatExecutor:   newAICLIActorChatExecutor(),
	}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	bridge.writeDelta = func(string) {}
	bridge.finalizeDelta = func() {}
	bridge.renderResponse = func(string) {}
	bridge.writePrompt = func() {}

	bridge.BeginRun()
	bridge.handleEvent(runtimeevents.Event{
		Type:      runtimechat.EventAssistantDelta,
		SessionID: "lead-session",
		Payload:   map[string]interface{}{"delta": "Working..."},
	})
	bridge.handleEvent(runtimeevents.Event{
		Type:      runtimechat.EventSessionEnd,
		SessionID: "lead-session",
		Payload:   map[string]interface{}{"success": true},
	})

	require.False(t, wasInteractiveActorResponseAlreadyRendered(session, "Final parent response"))

	bridge.handleEvent(runtimeevents.Event{
		Type:      runtimechat.EventAssistantMessage,
		SessionID: "lead-session",
		Payload:   map[string]interface{}{"content": "Final parent response"},
	})

	require.True(t, wasInteractiveActorResponseAlreadyRendered(session, "Final parent response"))
	require.False(t, wasInteractiveActorResponseAlreadyRendered(session, "Different final response"))
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
	require.Contains(t, lines[0], "context: prompt=12000 budget=10000")
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
	case 2:
		return &runtimellm.LLMResponse{
			Model: req.Model,
			ToolCalls: []runtimetypes.ToolCall{
				{
					ID:   "call-shell-3",
					Name: "shell",
					Args: map[string]interface{}{"command": "pwd"},
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
	case "shell", "bash", "execute_shell_command":
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
	info1, _ := m.FindTool("shell")
	info2, _ := m.FindTool("execute_shell_command")
	info3, _ := m.FindTool("bash")
	return []runtimeskill.ToolInfo{info1, info2, info3}
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
	for _, want := range []string{"--disable-tools", "aicli chat"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("expected non-interactive question error to contain %q, got %v", want, err)
		}
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
	for _, want := range []string{"tool=team_echo", "permission-mode=default", "--disable-tools", "--yolo", "aicli chat"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("expected non-interactive approval error to contain %q, got %v", want, err)
		}
	}
	if mcpManager.callCount != 0 {
		t.Fatalf("expected denied approval path to skip tool execution, got %d", mcpManager.callCount)
	}
}

func TestChatRuntimeEventBridge_LogsActorRunToChatLogger(t *testing.T) {
	logger := NewChatLogger("codex_ee", "codex", "gpt-5.4", true, "https://example.com")
	if err := logger.SetLogDir(t.TempDir()); err != nil {
		t.Fatalf("set log dir: %v", err)
	}
	session := &ChatSession{
		Logger:         logger,
		Stream:         true,
		RuntimeSession: &runtimechat.Session{ID: "session-actor-log"},
	}
	bridge := newChatRuntimeEventBridge(session)
	bridge.PrepareRunPrompt("请修复日志保存问题")
	bridge.BeginRun()
	defer bridge.EndRun()

	emitActorLoggingTestRun(bridge, "session-actor-log", "trace-log-1", time.Date(2026, 4, 26, 21, 1, 50, 0, time.UTC))

	if len(logger.sessionLog.Messages) != 7 {
		t.Fatalf("expected 7 log entries, got %d: %+v", len(logger.sessionLog.Messages), logger.sessionLog.Messages)
	}

	expectedTypes := []string{
		"request",
		"response",
		"tool_call",
		"tool_result",
		"request",
		"response",
		"tool_execution_summary",
	}
	for i, expected := range expectedTypes {
		if logger.sessionLog.Messages[i].MessageType != expected {
			t.Fatalf("expected message %d type %q, got %q", i, expected, logger.sessionLog.Messages[i].MessageType)
		}
	}

	firstRequest := logger.sessionLog.Messages[0]
	if firstRequest.TurnID != "turn-0001" || firstRequest.RequestID != "turn-0001-req-01" {
		t.Fatalf("unexpected first request scope: %+v", firstRequest)
	}
	firstRequestContent, ok := firstRequest.Content.(map[string]interface{})
	if !ok {
		t.Fatalf("expected first request content map, got %#v", firstRequest.Content)
	}
	if firstRequestContent["user_message"] != "请修复日志保存问题" {
		t.Fatalf("expected prompt to be logged on first request, got %#v", firstRequestContent)
	}

	toolCall := logger.sessionLog.Messages[2]
	if toolCall.TurnID != "turn-0001" || toolCall.RequestID != "turn-0001-req-01" || toolCall.ToolCallID != "call-1" {
		t.Fatalf("unexpected tool_call scope: %+v", toolCall)
	}

	secondResponse := logger.sessionLog.Messages[5]
	if secondResponse.TurnID != "turn-0001" || secondResponse.RequestID != "turn-0001-req-02" {
		t.Fatalf("unexpected second response scope: %+v", secondResponse)
	}
	secondResponseContent, ok := secondResponse.Content.(map[string]interface{})
	if !ok {
		t.Fatalf("expected second response content map, got %#v", secondResponse.Content)
	}
	if secondResponseContent["content"] != "已完成检查并整理修复建议。" {
		t.Fatalf("expected assistant content to be merged into final response log, got %#v", secondResponseContent)
	}

	summaryEntry := logger.sessionLog.Messages[6]
	summary, ok := summaryEntry.Content.(*aicliToolExecutionSummary)
	if !ok || summary == nil {
		t.Fatalf("expected tool execution summary payload, got %#v", summaryEntry.Content)
	}
	if summary.CallCount != 1 || summary.SuccessCount != 1 || summary.ErrorCount != 0 {
		t.Fatalf("unexpected tool execution summary: %+v", summary)
	}
	if len(summary.Calls) != 1 || summary.Calls[0].ToolCallID != "call-1" || summary.Calls[0].Function != "execute_shell_command" {
		t.Fatalf("unexpected tool execution summary calls: %+v", summary.Calls)
	}
	if summary.Calls[0].CaptureLimitReached == nil || !*summary.Calls[0].CaptureLimitReached {
		t.Fatalf("expected capture_limit_reached=true in summary, got %+v", summary.Calls[0])
	}
	if summary.Calls[0].OutputCaptureComplete == nil || *summary.Calls[0].OutputCaptureComplete {
		t.Fatalf("expected output_capture_complete=false in summary, got %+v", summary.Calls[0])
	}
	if summary.Calls[0].OmittedOutputBytes != 2048 || summary.Calls[0].OutputCaptureLimitBytes != 4096 {
		t.Fatalf("expected capture metadata in summary, got %+v", summary.Calls[0])
	}
	if summary.Calls[0].ShellType != "pwsh" || summary.Calls[0].ShellPath != `C:\Program Files\PowerShell\7\pwsh.exe` {
		t.Fatalf("expected shell metadata in summary, got %+v", summary.Calls[0])
	}
	if summary.Calls[0].RawOutputArtifactPath != `C:\logs\local-shell\toolkit\git_123.txt` {
		t.Fatalf("expected shell artifact path in summary, got %+v", summary.Calls[0])
	}
	if got := currentLastLocalShellArtifactPath(session); got != resolveAbsoluteChatPath(`C:\logs\local-shell\toolkit\git_123.txt`) {
		t.Fatalf("expected session to record latest shell artifact, got %q", got)
	}

	currentSummary := logger.CurrentSummary()
	if currentSummary == nil {
		t.Fatal("expected current summary")
	}
	if currentSummary.TotalRequests != 2 || currentSummary.TotalResponses != 2 || currentSummary.TotalToolCalls != 1 {
		t.Fatalf("unexpected logger summary: %+v", currentSummary)
	}
	if currentSummary.TotalTokens != 26162 {
		t.Fatalf("expected accumulated total tokens 26162, got %+v", currentSummary)
	}

	debugData, err := os.ReadFile(logger.DebugLogPath())
	if err != nil {
		t.Fatalf("read debug log: %v", err)
	}
	debugText := string(debugData)
	if !strings.Contains(debugText, "prompt_layout_summary=layers=base/system -> developer/developer | sources=system.md, tools.md") {
		t.Fatalf("expected prompt layout summary in debug log, got:\n%s", debugText)
	}
	if !strings.Contains(debugText, "usage_total_tokens=24762") || !strings.Contains(debugText, "usage_total_tokens=1400") {
		t.Fatalf("expected per-round usage totals in debug log, got:\n%s", debugText)
	}
}

func TestChatRuntimeEventBridge_BeginRunResetsSupplementSeparator(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	defer func() {
		_ = ui.SetThemePreset(ui.ThemePresetFocus)
	}()

	if err := ui.SetThemePreset(ui.ThemePresetContrast); err != nil {
		t.Fatalf("SetThemePreset: %v", err)
	}

	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	var output bytes.Buffer
	coord.SetWriter(&output)
	session.Interaction = coord

	bridge := newChatRuntimeEventBridge(session)
	bridge.BeginRun()

	bridge.writeLine("[tool] first")
	if !strings.Contains(output.String(), "[tool] first") {
		t.Fatalf("expected first supplement block to render, got %q", output.String())
	}

	bridge.BeginRun()
	bridge.writeLine("[tool] second")

	rendered := output.String()
	if strings.Contains(rendered, "[tool] first\n\n[tool] second") {
		t.Fatalf("expected BeginRun to reset block separator state, got %q", rendered)
	}
	if !strings.Contains(rendered, "[tool] first\n[tool] second") {
		t.Fatalf("expected adjacent runs to stay compact, got %q", rendered)
	}
}

func TestChatRuntimeEventBridge_LogsFailedLLMRequestToDebugFile(t *testing.T) {
	logDir := t.TempDir()
	logger := NewChatLogger("codex_ee", "codex", "gpt-5.4", true, "https://example.com")
	if err := logger.SetLogDir(logDir); err != nil {
		t.Fatalf("set log dir: %v", err)
	}
	session := &ChatSession{
		Logger:         logger,
		Stream:         true,
		RuntimeSession: &runtimechat.Session{ID: "session-actor-failure"},
	}
	bridge := newChatRuntimeEventBridge(session)
	bridge.PrepareRunPrompt("retry the failed request")
	bridge.BeginRun()
	defer bridge.EndRun()

	startedAt := time.Date(2026, 7, 19, 7, 57, 40, 0, time.UTC)
	bridge.handleStructuredLogEvent(runtimeevents.Event{
		Type:      "llm.request.started",
		SessionID: session.RuntimeSession.ID,
		TraceID:   "trace-rate-limit",
		Timestamp: startedAt,
		Payload: map[string]interface{}{
			"trace_id": "trace-rate-limit",
			"step":     1,
		},
	})
	bridge.handleStructuredLogEvent(runtimeevents.Event{
		Type:      "llm.request.finished",
		SessionID: session.RuntimeSession.ID,
		TraceID:   "trace-rate-limit",
		Timestamp: startedAt.Add(3 * time.Second),
		Payload: map[string]interface{}{
			"trace_id": "trace-rate-limit",
			"step":     1,
			"success":  false,
			"error":    "upstream returned HTTP 429 after 3 attempts",
		},
	})

	debugData, err := os.ReadFile(logger.DebugLogPath())
	if err != nil {
		t.Fatalf("read debug log: %v", err)
	}
	debugText := string(debugData)
	if !strings.Contains(debugText, "request_finished") || !strings.Contains(debugText, "success=false") {
		t.Fatalf("expected failed request completion in debug log, got:\n%s", debugText)
	}
	if !strings.Contains(debugText, "upstream returned HTTP 429 after 3 attempts") {
		t.Fatalf("expected failed request error in debug log, got:\n%s", debugText)
	}
}

func TestChatRuntimeEventBridge_FlushSessionPersistsActorLogs(t *testing.T) {
	logDir := t.TempDir()
	logger := NewChatLogger("codex_ee", "codex", "gpt-5.4", true, "https://example.com")
	if err := logger.SetLogDir(logDir); err != nil {
		t.Fatalf("set log dir: %v", err)
	}
	session := &ChatSession{
		Logger:         logger,
		Stream:         true,
		RuntimeSession: &runtimechat.Session{ID: "session-actor-flush"},
	}
	bridge := newChatRuntimeEventBridge(session)
	bridge.PrepareRunPrompt("请确认日志是否完整")
	bridge.BeginRun()
	defer bridge.EndRun()

	emitActorLoggingTestRun(bridge, "session-actor-flush", "trace-log-2", time.Date(2026, 4, 26, 21, 2, 0, 0, time.UTC))

	if err := logger.FlushSession(); err != nil {
		t.Fatalf("flush session: %v", err)
	}

	data, err := os.ReadFile(logger.SessionLogPath())
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}

	var payload ChatSessionLog
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("unmarshal chat log: %v", err)
	}
	if len(payload.Messages) == 0 {
		t.Fatalf("expected persisted messages, got %#v", payload)
	}
	if payload.SessionSummary == nil {
		t.Fatalf("expected persisted summary, got %#v", payload)
	}
	if payload.SessionSummary.TotalRequests < 2 || payload.SessionSummary.TotalResponses < 2 || payload.SessionSummary.TotalToolCalls < 1 {
		t.Fatalf("unexpected persisted summary: %+v", payload.SessionSummary)
	}
}

func TestChatRuntimeEventBridge_EndRunDrainsAndFlushesToolSummary(t *testing.T) {
	logger := NewChatLogger("codex_ee", "codex", "gpt-5.4", true, "https://example.com")
	if err := logger.SetLogDir(t.TempDir()); err != nil {
		t.Fatalf("set log dir: %v", err)
	}
	session := &ChatSession{
		Logger:         logger,
		Stream:         true,
		RuntimeSession: &runtimechat.Session{ID: "session-end-run-flush"},
	}
	bridge := newChatRuntimeEventBridge(session)
	bridge.startProcessor()
	bridge.PrepareRunPrompt("apply the patch")
	bridge.BeginRun()
	bridge.Handle(runtimeevents.Event{
		Type:      "llm.request.started",
		SessionID: session.RuntimeSession.ID,
		TraceID:   "trace-end-run",
		Payload:   map[string]interface{}{"trace_id": "trace-end-run", "step": 1},
	})
	bridge.Handle(runtimeevents.Event{
		Type:      "tool.completed",
		SessionID: session.RuntimeSession.ID,
		TraceID:   "trace-end-run",
		ToolName:  "apply_patch",
		Payload: map[string]interface{}{
			"trace_id":     "trace-end-run",
			"step":         1,
			"tool_call_id": "call-end-run",
			"summary":      "changed 1 file",
		},
	})

	bridge.EndRun()

	data, err := os.ReadFile(logger.SessionLogPath())
	if err != nil {
		t.Fatalf("read flushed chat log: %v", err)
	}
	var payload ChatSessionLog
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("unmarshal chat log: %v", err)
	}
	if len(payload.Messages) != 3 {
		t.Fatalf("expected request, tool result, and forced summary, got %+v", payload.Messages)
	}
	if got := payload.Messages[len(payload.Messages)-1].MessageType; got != "tool_execution_summary" {
		t.Fatalf("expected forced tool summary at run end, got %q", got)
	}
}

func emitActorLoggingTestRun(bridge *chatRuntimeEventBridge, sessionID, traceID string, base time.Time) {
	events := []runtimeevents.Event{
		{
			Type:      "llm.request.started",
			SessionID: sessionID,
			TraceID:   traceID,
			Timestamp: base.Add(10 * time.Millisecond),
			Payload: map[string]interface{}{
				"trace_id":              traceID,
				"step":                  1,
				"provider":              "codex_ee",
				"model":                 "gpt-5.4",
				"message_count":         3,
				"tool_count":            1,
				"prompt_layout_summary": "layers=base/system -> developer/developer | sources=system.md, tools.md",
				"instruction_tokens":    33,
				"total_tokens":          512,
				"prompt_layout_length":  132,
				"total_message_chars":   2048,
				"prompt_budget":         200000,
				"context_window_tokens": 270000,
				"budget_source":         "model_capability_auto_compact_token_limit",
				"budget_source_detail":  "provider/model capability auto-compact token limit",
			},
		},
		{
			Type:      "llm.request.finished",
			SessionID: sessionID,
			TraceID:   traceID,
			Timestamp: base.Add(20 * time.Millisecond),
			Payload: map[string]interface{}{
				"trace_id":                traceID,
				"step":                    1,
				"provider":                "codex_ee",
				"model":                   "gpt-5.4",
				"success":                 true,
				"tool_call_count":         1,
				"usage_prompt_tokens":     23099,
				"usage_completion_tokens": 1663,
				"usage_total_tokens":      24762,
				"usage_cached_tokens":     2048,
				"usage_reasoning_tokens":  512,
				"usage_source":            "provider_reported",
			},
		},
		{
			Type:      "tool.requested",
			SessionID: sessionID,
			TraceID:   traceID,
			ToolName:  "execute_shell_command",
			Timestamp: base.Add(25 * time.Millisecond),
			Payload: map[string]interface{}{
				"trace_id":     traceID,
				"step":         1,
				"tool_call_id": "call-1",
				"arg_preview":  "command=git diff --stat",
				"command_text": "git diff --stat",
				"workdir":      "E:/projects/ai/ai-agent-runtime",
			},
		},
		{
			Type:      "tool.completed",
			SessionID: sessionID,
			TraceID:   traceID,
			ToolName:  "execute_shell_command",
			Timestamp: base.Add(35 * time.Millisecond),
			Payload: map[string]interface{}{
				"trace_id":                      traceID,
				"step":                          1,
				"tool_call_id":                  "call-1",
				"arg_preview":                   "command=git diff --stat",
				"command_text":                  "git diff --stat",
				"workdir":                       "E:/projects/ai/ai-agent-runtime",
				"summary":                       "2 files changed\n10 insertions(+), 4 deletions(-)",
				"summary_lines":                 []interface{}{"2 files changed", "10 insertions(+), 4 deletions(-)"},
				"shell_type":                    "pwsh",
				"shell_path":                    `C:\Program Files\PowerShell\7\pwsh.exe`,
				"shell_display":                 `pwsh (C:\Program Files\PowerShell\7\pwsh.exe)`,
				"output_capture_complete":       false,
				"capture_limit_reached":         true,
				"output_capture_limit_bytes":    4096,
				"retained_output_bytes":         4096,
				"omitted_output_bytes":          2048,
				"output_capture_limit_disabled": false,
				"raw_output_artifact_path":      `C:\logs\local-shell\toolkit\git_123.txt`,
			},
		},
		{
			Type:      "llm.request.started",
			SessionID: sessionID,
			TraceID:   traceID,
			Timestamp: base.Add(45 * time.Millisecond),
			Payload: map[string]interface{}{
				"trace_id":      traceID,
				"step":          2,
				"provider":      "codex_ee",
				"model":         "gpt-5.4",
				"message_count": 5,
				"tool_count":    0,
			},
		},
		{
			Type:      "llm.request.finished",
			SessionID: sessionID,
			TraceID:   traceID,
			Timestamp: base.Add(55 * time.Millisecond),
			Payload: map[string]interface{}{
				"trace_id":                traceID,
				"step":                    2,
				"provider":                "codex_ee",
				"model":                   "gpt-5.4",
				"success":                 true,
				"tool_call_count":         0,
				"usage_prompt_tokens":     1200,
				"usage_completion_tokens": 200,
				"usage_total_tokens":      1400,
				"usage_cached_tokens":     0,
				"usage_reasoning_tokens":  0,
				"usage_source":            "provider_reported",
			},
		},
		{
			Type:      runtimechat.EventAssistantMessage,
			SessionID: sessionID,
			TraceID:   traceID,
			Timestamp: base.Add(60 * time.Millisecond),
			Payload: map[string]interface{}{
				"turn_id": "turn-0001",
				"content": "已完成检查并整理修复建议。",
				"reasoning": map[string]interface{}{
					"summary": "先核对日志，再补齐事件到日志桥接。",
				},
			},
		},
		{
			Type:      runtimechat.EventSessionEnd,
			SessionID: sessionID,
			TraceID:   traceID,
			Timestamp: base.Add(70 * time.Millisecond),
			Payload: map[string]interface{}{
				"trace_id": traceID,
				"success":  true,
				"steps":    2,
				"duration": 70,
			},
		},
	}
	for _, event := range events {
		bridge.handleStructuredLogEvent(event)
	}
}

func TestRuntimeContextSummaryLinesIncludesCacheHitRatio(t *testing.T) {
	lines := runtimeContextSummaryLines(map[string]interface{}{
		"usage_prompt_tokens":         1000,
		"usage_completion_tokens":     100,
		"usage_total_tokens":          1100,
		"usage_cached_tokens":         250,
		"usage_cache_read_tokens":     250,
		"usage_cache_creation_tokens": 500,
		"usage_cache_hit_ratio":       0.25,
	}, true)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "cached=250") {
		t.Fatalf("expected cached token count, got:\n%s", joined)
	}
	if !strings.Contains(joined, "cache_hit=25.0%") {
		t.Fatalf("expected cache hit ratio, got:\n%s", joined)
	}
	if !strings.Contains(joined, "cache_write=500") {
		t.Fatalf("expected cache creation token count, got:\n%s", joined)
	}
}

func TestRuntimeContextSummaryLinesDistinguishesReportedZeroCache(t *testing.T) {
	lines := runtimeContextSummaryLines(map[string]interface{}{
		"usage_prompt_tokens":       1000,
		"usage_cache_hit_ratio":     0.0,
		"usage_cache_read_reported": true,
		"usage_cache_status":        "reported_zero",
	}, true)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "cache_hit=0.0%") {
		t.Fatalf("expected explicit zero cache hit ratio, got:\n%s", joined)
	}
	if !strings.Contains(joined, "cache_status=reported_zero") {
		t.Fatalf("expected reported-zero status, got:\n%s", joined)
	}
}

func TestFormatRuntimeLLMRequestFinishedDebugInfoIncludesCacheHitRatio(t *testing.T) {
	info := formatRuntimeLLMRequestFinishedDebugInfo(runtimeevents.Event{
		Type: "llm.request.finished",
		Payload: map[string]interface{}{
			"success":                   true,
			"usage_prompt_tokens":       1000,
			"usage_completion_tokens":   100,
			"usage_total_tokens":        1100,
			"usage_cached_tokens":       250,
			"usage_cache_read_tokens":   250,
			"usage_cache_read_reported": true,
			"usage_cache_status":        "hit",
			"usage_cache_hit_ratio":     0.25,
		},
	})
	if !strings.Contains(info, "usage_cached_tokens=250") {
		t.Fatalf("expected cached tokens in debug info, got %q", info)
	}
	if !strings.Contains(info, "usage_cache_hit_ratio=0.2500") {
		t.Fatalf("expected cache hit ratio in debug info, got %q", info)
	}
	if !strings.Contains(info, "usage_cache_read_reported=true") || !strings.Contains(info, "usage_cache_status=hit") {
		t.Fatalf("expected cache reporting diagnostics in debug info, got %q", info)
	}
}

func TestFormatRuntimeLLMRequestStartedDebugInfoIncludesToolSurfaceFingerprint(t *testing.T) {
	info := formatRuntimeLLMRequestStartedDebugInfo(runtimeevents.Event{
		Type: "llm.request.started",
		Payload: map[string]interface{}{
			"tool_surface_fingerprint": "abc123fingerprint",
		},
	})
	if !strings.Contains(info, "tool_surface_fingerprint=abc123fingerprint") {
		t.Fatalf("expected tool surface fingerprint in started debug info, got %q", info)
	}
}

func TestRenderChatRuntimeEventLLMQuotaFailureIsActionable(t *testing.T) {
	got := renderChatRuntimeEvent(runtimeevents.Event{
		Type: "llm.request.finished",
		Payload: map[string]interface{}{
			"success":     false,
			"error":       "HTTP 403: insufficient_user_quota",
			"error_code":  "UPSTREAM_QUOTA_EXHAUSTED",
			"retryable":   false,
			"next_action": "Increase provider quota or switch to an available provider/model; do not retry unchanged.",
		},
	})
	want := strings.Join([]string{
		"[thinking] model error [UPSTREAM_QUOTA_EXHAUSTED, retryable=false] HTTP 403: insufficient_user_quota",
		"[action] Increase provider quota or switch to an available provider/model; do not retry unchanged.",
	}, "\n")
	if got != want {
		t.Fatalf("unexpected quota failure render:\n got: %q\nwant: %q", got, want)
	}
}

func TestRenderChatRuntimeEventToolFailureShowsDiagnosticAction(t *testing.T) {
	got := renderChatRuntimeEvent(runtimeevents.Event{
		Type:     "tool.completed",
		ToolName: "task_output",
		Payload: map[string]interface{}{
			"arg_preview": "job_id=guessed-id",
			"error":       "background job not found: guessed-id",
			"error_code":  "JOB_NOT_FOUND",
			"retryable":   false,
			"next_action": "Use the exact job_id returned by background_task; do not guess or synthesize an id.",
		},
	})
	for _, expected := range []string{
		"• Failed task_output job_id=guessed-id",
		"  diagnostic: JOB_NOT_FOUND (retryable=false)",
		"  action: Use the exact job_id returned by background_task; do not guess or synthesize an id.",
		"  failed: background job not found: guessed-id",
	} {
		if !strings.Contains(got, expected) {
			t.Fatalf("expected %q in rendered failure, got %q", expected, got)
		}
	}
}

func TestRenderChatRuntimeEventToolDeniedShowsRecoveryAction(t *testing.T) {
	got := renderChatRuntimeEvent(runtimeevents.Event{
		Type: "tool.denied",
		Payload: map[string]interface{}{
			"reason":      "tool not allowed for this agent",
			"error_code":  "AGENT_PERMISSION",
			"retryable":   false,
			"next_action": "Request the required approval or use an allowed tool; do not retry unchanged.",
		},
	})
	if !strings.Contains(got, "diagnostic: AGENT_PERMISSION (retryable=false)") || !strings.Contains(got, "action: Request the required approval") {
		t.Fatalf("expected denied tool recovery details, got %q", got)
	}
}

// TestChatRuntimeEventBridge_InteractionAnchorSnapshotSemantics 验证 §5.5 用户交互
// 锚点的快照语义：交互触发时刻捕获模型尾部，模型后续增长不影响已记录锚点；
// 新交互覆盖锚点并指向最新模型尾部。
func TestChatRuntimeEventBridge_InteractionAnchorSnapshotSemantics(t *testing.T) {
	session := &ChatSession{}
	bridge := newChatRuntimeEventBridge(session)

	// 模型为空：无锚点可捕获。
	if tail := bridge.recordInteractionAnchor("model"); tail != nil {
		t.Fatalf("empty model anchor = %+v, want nil", tail)
	}

	// 编码第一个事件后模型有尾部。
	bridge.encodeRenderModelEvent(runtimeevents.Event{
		Type:      runtimechat.EventQuestionAsked,
		SessionID: "session-1",
		TraceID:   "trace-1",
		Payload:   map[string]interface{}{"prompt": "choose a model"},
	})
	tail := bridge.recordInteractionAnchor("model")
	if tail == nil {
		t.Fatal("anchor after first event is nil")
	}
	gotTail, at, source, count := bridge.lastInteractionAnchor()
	if gotTail == nil || gotTail.ItemID != tail.ItemID || gotTail.Seq != tail.Seq {
		t.Fatalf("lastInteractionAnchor=%+v want item %s #%d", gotTail, tail.ItemID, tail.Seq)
	}
	if at.IsZero() {
		t.Fatal("anchor time is zero")
	}
	if source != "model" || count != 1 {
		t.Fatalf("source=%q count=%d want model/1", source, count)
	}

	// 模型继续增长后，已记录锚点保持触发时刻快照。
	bridge.encodeRenderModelEvent(runtimeevents.Event{
		Type:      runtimechat.EventApprovalRequested,
		SessionID: "session-1",
		TraceID:   "trace-2",
		Payload:   map[string]interface{}{"request_id": "req-1"},
	})
	gotTail2, _, _, count2 := bridge.lastInteractionAnchor()
	if gotTail2 == nil || gotTail2.ItemID != tail.ItemID || gotTail2.Seq != tail.Seq {
		t.Fatalf("anchor drifted after model growth: got %+v want %+v", gotTail2, tail)
	}
	if count2 != 1 {
		t.Fatalf("count=%d want 1", count2)
	}

	// 新交互覆盖锚点，指向当前模型尾部。
	bridge.recordInteractionAnchor("debug")
	gotTail3, _, source3, count3 := bridge.lastInteractionAnchor()
	cur := bridge.renderModelTail()
	if gotTail3 == nil || cur == nil || gotTail3.Seq != cur.Seq || gotTail3.ItemID != cur.ItemID {
		t.Fatalf("new anchor=%+v want current tail=%+v", gotTail3, cur)
	}
	if source3 != "debug" || count3 != 2 {
		t.Fatalf("source=%q count=%d want debug/2", source3, count3)
	}
}

// TestChatDebugDisplayDocumentShowsInteractionAnchor 验证 /debug 文档展示最近
// 一次用户交互锚点（审计面可见触发时刻的模型尾部）。
func TestChatDebugDisplayDocumentShowsInteractionAnchor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	session := &ChatSession{
		ProviderName: "openai",
		Model:        "gpt-test",
	}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	bridge.encodeRenderModelEvent(runtimeevents.Event{
		Type:      runtimechat.EventQuestionAsked,
		SessionID: "session-1",
		TraceID:   "trace-1",
		Payload:   map[string]interface{}{"prompt": "choose a model"},
	})
	bridge.recordInteractionAnchor("model")

	doc := buildChatDebugDisplayDocument(session)
	plain := ui.RenderDocumentPlain(doc)
	if !strings.Contains(plain, "Interaction Anchor:") {
		t.Fatalf("debug document missing Interaction Anchor line:\n%s", plain)
	}
}

// TestChatRuntimeEventBridge_InteractionInjectedAtAnchor 固化切片 12 的端到端
// 语义：/debug、/model 交互输出经 recordInteractionAnchor（触发时刻捕获模型
// 尾部锚点）+ RenderCommandDocument 提交点（consumePendingInteraction）按
// 锚定语义注入 Scene —— 交互 cell 出现在锚点 item 之后、模型后续增长 item
// 之前；无 pending 标记的普通命令仍 append 到模型末尾。
func TestChatRuntimeEventBridge_InteractionInjectedAtAnchor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	session := &ChatSession{}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	coord := newChatInteractionCoordinator(session)
	var out bytes.Buffer
	coord.SetWriter(&out)

	// 事件 1 → item-1（模型尾部锚点）。
	bridge.encodeRenderModelEvent(runtimeevents.Event{
		Type:      runtimechat.EventQuestionAsked,
		SessionID: "session-1",
		TraceID:   "trace-1",
		Payload:   map[string]interface{}{"prompt": "choose a model"},
	})
	anchor := bridge.recordInteractionAnchor("debug")
	if anchor == nil || anchor.ItemID != "item-1" {
		t.Fatalf("anchor = %+v, want item-1", anchor)
	}
	// 模型在触发时刻后继续增长 → item-2。
	bridge.encodeRenderModelEvent(runtimeevents.Event{
		Type:      runtimechat.EventApprovalRequested,
		SessionID: "session-1",
		TraceID:   "trace-2",
		Payload:   map[string]interface{}{"request_id": "req-1"},
	})

	// /debug 输出提交：pending 交互标记 → 锚定插入（item-1 之后）。
	if ok := coord.RenderCommandDocument(render.Document{
		Blocks: []render.Block{
			{Kind: render.BlockParagraph, Lines: []render.Line{
				{Spans: []render.Span{{Text: "debug 输出"}}},
			}},
		},
	}); !ok {
		t.Fatal("RenderCommandDocument returned false")
	}

	snap := bridge.sceneSnapshot()
	if len(snap.Cells) != 3 {
		t.Fatalf("cells = %d, want 3 (item-1, interaction, item-2)", len(snap.Cells))
	}
	cells := snap.Cells
	if cells[0].ID != 1 || cells[2].ID != 2 {
		t.Fatalf("ids = [%d %d %d], want [1 3 2]", cells[0].ID, cells[1].ID, cells[2].ID)
	}
	// 交互 cell 在锚点 item-1 之后、增长 item-2 之前；按 command cell 呈现。
	if cells[1].ID != 3 || cells[1].Kind != scene.KindCommand || cells[1].Source != "debug 输出" {
		t.Fatalf("interaction cell = %+v, want item-3 command 'debug 输出'", cells[1])
	}

	// pending 已消费：下一次普通命令结果 append 到模型末尾（item-2 之后）。
	if ok := coord.RenderCommandDocument(render.Document{
		Blocks: []render.Block{
			{Kind: render.BlockParagraph, Lines: []render.Line{
				{Spans: []render.Span{{Text: "普通命令"}}},
			}},
		},
	}); !ok {
		t.Fatal("second RenderCommandDocument returned false")
	}
	snap = bridge.sceneSnapshot()
	if len(snap.Cells) != 4 {
		t.Fatalf("cells = %d, want 4", len(snap.Cells))
	}
	last := snap.Cells[len(snap.Cells)-1]
	if last.Kind != scene.KindCommand || last.Source != "普通命令" {
		t.Fatalf("last cell = %+v, want command '普通命令'", last)
	}
}

// TestChatRuntimeEventBridge_ReplayRestoresInteraction 固化 replay 幂等恢复：
// 交互注入记录（interaction + interaction_anchor）与事件行同一全序落盘，
// 新 bridge replay 后 Scene 重建等价 —— 交互 cell 仍出现在锚点 item 之后、
// 增长 item 之前（锚点位置按全序重建，不随 replay 漂移到模型末尾）。
func TestChatRuntimeEventBridge_ReplayRestoresInteraction(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	logPath := filepath.Join(t.TempDir(), "runtime-events.jsonl")
	session := &ChatSession{}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	bridge.eventLogPathOverride = logPath
	coord := newChatInteractionCoordinator(session)
	var out bytes.Buffer
	coord.SetWriter(&out)

	// 实时路径：事件 1 → 锚点 → 事件 2 → /debug 交互输出（锚定插入）。
	bridge.encodeRenderModelEvent(runtimeevents.Event{
		Type:      runtimechat.EventQuestionAsked,
		SessionID: "session-1",
		TraceID:   "trace-1",
		Payload:   map[string]interface{}{"prompt": "choose a model"},
	})
	bridge.recordInteractionAnchor("debug")
	bridge.encodeRenderModelEvent(runtimeevents.Event{
		Type:      runtimechat.EventApprovalRequested,
		SessionID: "session-1",
		TraceID:   "trace-2",
		Payload:   map[string]interface{}{"request_id": "req-1"},
	})
	if ok := coord.RenderCommandDocument(render.Document{
		Blocks: []render.Block{
			{Kind: render.BlockParagraph, Lines: []render.Line{
				{Spans: []render.Span{{Text: "debug 输出"}}},
			}},
		},
	}); !ok {
		t.Fatal("RenderCommandDocument returned false")
	}
	live := bridge.sceneSnapshot()
	if len(live.Cells) != 3 || live.Cells[1].ID != 3 {
		t.Fatalf("live cells = %d, want 3 with interaction at index 1", len(live.Cells))
	}

	// 新 bridge replay 同一日志：Scene 重建等价（同 cell 序列与顺序）。
	replayBridge := newChatRuntimeEventBridge(session)
	replayBridge.eventLogPathOverride = logPath
	if n, err := replayBridge.replayEventLog(); err != nil || n == 0 {
		t.Fatalf("replayEventLog = %d, %v", n, err)
	}
	replayed := replayBridge.sceneSnapshot()
	if len(replayed.Cells) != len(live.Cells) {
		t.Fatalf("replay cells = %d, live = %d", len(replayed.Cells), len(live.Cells))
	}
	for i := range live.Cells {
		if replayed.Cells[i].ID != live.Cells[i].ID ||
			replayed.Cells[i].Kind != live.Cells[i].Kind ||
			replayed.Cells[i].Source != live.Cells[i].Source {
			t.Fatalf("cell %d: replay = %+v, live = %+v", i, replayed.Cells[i], live.Cells[i])
		}
	}
	if replayed.Cells[1].ID != 3 || replayed.Cells[1].Source != "debug 输出" {
		t.Fatalf("replay interaction cell = %+v, want item-3 'debug 输出'", replayed.Cells[1])
	}
	if replayed.Cells[2].ID != 2 {
		t.Fatalf("replay last cell = %+v, want item-2（交互不漂移到末尾）", replayed.Cells[2])
	}
}
