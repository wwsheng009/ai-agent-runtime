package agent

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestFormatSystemReminder(t *testing.T) {
	out := FormatSystemReminder(ReminderKindCompletionRequirement, "call report_task_outcome")
	assert.Contains(t, out, `<system-reminder kind="completion_requirement">`)
	assert.Contains(t, out, "call report_task_outcome")
	assert.True(t, strings.HasSuffix(out, "</system-reminder>"))

	assert.Empty(t, FormatSystemReminder("x", "  "))
	assert.Equal(t, ReminderKindRuntimeAdvisory, NormalizeReminderKind(""))
	assert.Equal(t, ReminderKindDoomLoop, NormalizeReminderKind(" Doom_Loop "))
}

func TestNewSystemReminderMessage(t *testing.T) {
	msg := NewSystemReminderMessage(SystemReminder{
		Kind:    ReminderKindStopHook,
		Body:    "tests are still failing",
		Durable: true,
		Extra: types.Metadata{
			"stop_hook_block": true,
		},
	})
	require.NotNil(t, msg)
	assert.Equal(t, "user", msg.Role)
	assert.True(t, IsSystemReminder(*msg))
	assert.Equal(t, ReminderKindStopHook, ReminderKindOf(*msg))
	assert.Equal(t, true, msg.Metadata[MetaSystemReminder])
	assert.Equal(t, true, msg.Metadata[MetaEphemeralInstruction])
	assert.Equal(t, true, msg.Metadata[MetaSystemReminderDurable])
	assert.True(t, IsSystemReminderDurable(*msg))
	assert.Equal(t, true, msg.Metadata["stop_hook_block"])
	assert.Contains(t, msg.Content, `<system-reminder kind="stop_hook">`)
	assert.Contains(t, msg.Content, "tests are still failing")
}

func TestNewSystemReminderMessage_NonDurableMarksExplicitFalse(t *testing.T) {
	msg := NewSystemReminderMessage(SystemReminder{
		Kind:    ReminderKindDoomLoop,
		Body:    "stop repeating",
		Durable: false,
	})
	require.NotNil(t, msg)
	assert.Equal(t, false, msg.Metadata[MetaSystemReminderDurable])
	assert.False(t, IsSystemReminderDurable(*msg))
}

func TestNewSystemReminderMessage_EmptyBody(t *testing.T) {
	assert.Nil(t, NewSystemReminderMessage(SystemReminder{Kind: ReminderKindDoomLoop, Body: "  "}))
}

func TestPlanModeReminderBody(t *testing.T) {
	body := PlanModeReminderBody("docs/plan.md")
	assert.Contains(t, body, "plan mode")
	assert.Contains(t, body, "docs/plan.md")
	assert.Contains(t, body, "approve")
}

func TestToolResultsToPayloads_WrapsAdvisoryAsSystemReminder(t *testing.T) {
	payloads := toolResultsToPayloads([]toolExecutionResult{
		{Call: types.ToolCall{ID: "c1", Name: "view"}, Output: "first"},
		{Call: types.ToolCall{ID: "c2", Name: "view"}, Output: "second"},
	}, "Runtime advisory: stop repeating")
	require.Len(t, payloads, 2)
	assert.NotContains(t, payloads[0].Content, "system-reminder")
	assert.Contains(t, payloads[1].Content, `<system-reminder kind="runtime_advisory">`)
	assert.Contains(t, payloads[1].Content, "Runtime advisory: stop repeating")
	assert.Equal(t, true, payloads[1].Metadata[MetaSystemReminder])
	assert.Equal(t, ReminderKindRuntimeAdvisory, payloads[1].Metadata[MetaSystemReminderKind])
	assert.Equal(t, true, payloads[1].Metadata["semantic_repeat_advisory"])
	assert.Equal(t, false, payloads[1].Metadata[MetaSystemReminderDurable])
}

func TestDurableToolResultPayloads_StripsPureAdvisory(t *testing.T) {
	payloads := toolResultsToPayloads([]toolExecutionResult{
		{Call: types.ToolCall{ID: "c1", Name: "view"}, Output: "first"},
		{Call: types.ToolCall{ID: "c2", Name: "grep"}, Output: "second"},
	}, repeatedSemanticToolCallAdvisory(3))
	require.Len(t, payloads, 2)
	require.Contains(t, payloads[1].Content, "system-reminder")

	durable := DurableToolResultPayloads(payloads)
	require.Len(t, durable, 2)
	assert.Equal(t, "first", durable[0].Content)
	assert.Equal(t, "second", durable[1].Content)
	assert.NotContains(t, durable[1].Content, "system-reminder")
	assert.NotContains(t, durable[1].Content, "Runtime advisory")
	if durable[1].Metadata != nil {
		assert.Nil(t, durable[1].Metadata[MetaSystemReminder])
		assert.Nil(t, durable[1].Metadata["semantic_repeat_advisory"])
		assert.Nil(t, durable[1].Metadata[MetaSystemReminderDurable])
	}
	// Prompt path still has advisory.
	assert.Contains(t, payloads[1].Content, "system-reminder")
}

func TestDurableMessagesForPersist_DropsNonDurableStandaloneAndStripsTool(t *testing.T) {
	durableRem := NewSystemReminderMessage(SystemReminder{
		Kind:    ReminderKindCompletionRequirement,
		Body:    "call report_task_outcome",
		Durable: true,
	})
	ephemeralRem := NewSystemReminderMessage(SystemReminder{
		Kind:    ReminderKindDoomLoop,
		Body:    "do not repeat",
		Durable: false,
	})
	toolWithAdvisory := types.NewToolMessage("call-1", "tool-out\n\n"+FormatSystemReminder(ReminderKindDoomLoop, "do not repeat"))
	toolWithAdvisory.Metadata = types.Metadata{
		MetaSystemReminder:         true,
		MetaSystemReminderKind:     ReminderKindDoomLoop,
		MetaEphemeralInstruction:   true,
		MetaSystemReminderDurable:  false,
		"semantic_repeat_advisory": true,
	}
	normalUser := types.NewUserMessage("hello")

	persisted := DurableMessagesForPersist([]types.Message{
		*normalUser,
		*durableRem,
		*ephemeralRem,
		*toolWithAdvisory,
	})
	require.Len(t, persisted, 3)
	assert.Equal(t, "hello", persisted[0].Content)
	assert.True(t, IsSystemReminder(persisted[1]))
	assert.Equal(t, ReminderKindCompletionRequirement, ReminderKindOf(persisted[1]))
	assert.Equal(t, "tool", persisted[2].Role)
	assert.Equal(t, "tool-out", persisted[2].Content)
	assert.False(t, IsSystemReminder(persisted[2]))
}

func TestStripTrailingSystemReminderEnvelope(t *testing.T) {
	wrapped := "output body\n\n" + FormatSystemReminder(ReminderKindExplorationStall, "inspect less")
	assert.Equal(t, "output body", StripTrailingSystemReminderEnvelope(wrapped))
	assert.Equal(t, "plain", StripTrailingSystemReminderEnvelope("plain"))
}

func TestIsPureAdvisoryReminderKind(t *testing.T) {
	assert.True(t, IsPureAdvisoryReminderKind(ReminderKindDoomLoop))
	assert.True(t, IsPureAdvisoryReminderKind(ReminderKindDispositionReplay))
	assert.True(t, IsPureAdvisoryReminderKind(ReminderKindExplorationStall))
	assert.True(t, IsPureAdvisoryReminderKind(ReminderKindRuntimeAdvisory))
	assert.False(t, IsPureAdvisoryReminderKind(ReminderKindCompletionRequirement))
	assert.False(t, IsPureAdvisoryReminderKind(ReminderKindStopHook))
	assert.False(t, IsPureAdvisoryReminderKind(ReminderKindPlanMode))
}

func TestInferAdvisoryReminderKind(t *testing.T) {
	assert.Equal(t, ReminderKindDoomLoop, inferAdvisoryReminderKind("the same semantic tool request has run 3 consecutive times"))
	assert.Equal(t, ReminderKindDispositionReplay, inferAdvisoryReminderKind("previous identical tool batch returned outcome=partial"))
	assert.Equal(t, ReminderKindDispositionReplay, inferAdvisoryReminderKind("failed with STALE_CONTEXT (outcome=failed)"))
	assert.Equal(t, ReminderKindExplorationStall, inferAdvisoryReminderKind("consecutive tool rounds have only inspected"))
	assert.Equal(t, ReminderKindRuntimeAdvisory, inferAdvisoryReminderKind("something else"))
}

func TestPlanModeSystemReminder_WhenEngineInPlanMode(t *testing.T) {
	llmRuntime := llm.NewLLMRuntime(nil)
	agent := NewAgentWithLLM(&Config{Name: "plan-agent", Provider: "test", Model: "test"}, nil, llmRuntime)
	engine := NewPermissionEngine()
	engine.Mode = runtimepolicy.ModePlan
	engine.PlanWriteAllowPaths = []string{"docs/feature-plan.md"}
	agent.SetPermissionEngine(engine)
	loop := NewReActLoop(agent, llmRuntime, &LoopReActConfig{MaxSteps: 1})

	msg := loop.planModeSystemReminder(nil)
	require.NotNil(t, msg)
	assert.True(t, IsSystemReminder(*msg))
	assert.Equal(t, ReminderKindPlanMode, ReminderKindOf(*msg))
	assert.Contains(t, msg.Content, "docs/feature-plan.md")
	assert.Contains(t, msg.Content, `<system-reminder kind="plan_mode">`)
	assert.False(t, IsSystemReminderDurable(*msg))
	assert.Empty(t, DurableMessagesForPersist([]types.Message{*msg}))

	// Already present in history → no second inject.
	assert.Nil(t, loop.planModeSystemReminder([]types.Message{*msg}))

	// Non-plan mode should not inject.
	engine.Mode = runtimepolicy.ModeDefault
	assert.Nil(t, loop.planModeSystemReminder(nil))
}

func TestStripPlanModeSystemReminders_RemovesCurrentAndLegacyMessages(t *testing.T) {
	current := NewSystemReminderMessage(SystemReminder{
		Kind:    ReminderKindPlanMode,
		Body:    PlanModeReminderBody("docs/current.md"),
		Durable: true, // Simulate history persisted by an older runtime.
	})
	require.NotNil(t, current)

	legacy := types.NewUserMessage(`<system-reminder kind="plan_mode">legacy plan instruction</system-reminder>`)
	normal := types.NewUserMessage("implement the approved change")

	filtered := stripPlanModeSystemReminders([]types.Message{*current, *legacy, *normal})
	require.Len(t, filtered, 1)
	assert.Equal(t, normal.Content, filtered[0].Content)
}
