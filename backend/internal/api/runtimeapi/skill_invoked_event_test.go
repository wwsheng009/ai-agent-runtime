package runtimeapi

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

// SK-3：skills.invoked 事件——每次命中一条（去重），字段对齐计划口径，
// 且显式/隐式两种 kind 都能透传。仅观测，不参与权限与计费。
func TestPublishSkillInvokedEvents_DedupesAndCarriesFields(t *testing.T) {
	h := NewHandler(skill.NewRegistry(nil), nil, nil)
	bus := h.getRuntimeEventBus()

	var got []events.Event
	bus.Subscribe(skillsInvokedEventType, func(event events.Event) {
		got = append(got, event)
	})

	h.publishSkillInvokedEvents(nil, "session-1", []skill.ImplicitInvocation{
		{Name: "build-tool", Scope: "user", Path: "/skills/build/scripts", Kind: skill.InvocationKindImplicit, Basis: skill.ImplicitBasisScriptsDir, Tool: "run_shell_command"},
		{Name: "build-tool", Scope: "user", Path: "/skills/build/scripts", Kind: skill.InvocationKindImplicit, Basis: skill.ImplicitBasisScriptsDir, Tool: "run_shell_command"}, // 重复 → 必须只发一条
		skill.NewExplicitInvocation("docs", "repo", "/skills/docs/SKILL.md"),
	})

	require.Len(t, got, 2)
	require.Equal(t, skillsInvokedEventType, got[0].Type)
	require.Equal(t, "build-tool", got[0].Payload["name"])
	require.Equal(t, "user", got[0].Payload["scope"])
	require.Equal(t, "/skills/build/scripts", got[0].Payload["path"])
	require.Equal(t, "implicit", got[0].Payload["kind"])
	require.Equal(t, "scripts_dir", got[0].Payload["basis"])
	require.Equal(t, "run_shell_command", got[0].Payload["tool"])
	require.Equal(t, "session-1", got[0].Payload["session_id"])

	require.Equal(t, "docs", got[1].Payload["name"])
	require.Equal(t, "repo", got[1].Payload["scope"])
	require.Equal(t, "explicit", got[1].Payload["kind"])
	require.Equal(t, "mention", got[1].Payload["basis"])
}

// 空输入不得产生任何事件（nil handler / 空列表都要静默）。
func TestPublishSkillInvokedEvents_EmptyIsNoop(t *testing.T) {
	h := NewHandler(skill.NewRegistry(nil), nil, nil)
	bus := h.getRuntimeEventBus()

	count := 0
	bus.Subscribe(skillsInvokedEventType, func(events.Event) { count++ })

	h.publishSkillInvokedEvents(nil, "session-1", nil)
	require.Zero(t, count)

	var nilHandler *Handler
	nilHandler.publishSkillInvokedEvents(nil, "session-1", []skill.ImplicitInvocation{{Name: "x"}})
	require.Zero(t, count)
}
