package agent

import (
	"testing"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	runtimeskill "github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

func newMainLoopObservationAgent(t *testing.T) *Agent {
	t.Helper()
	registry := runtimeskill.NewRegistry(nil)
	return &Agent{
		config:      &Config{Name: "test-agent"},
		skillRouter: runtimeskill.NewRouter(registry),
		eventBus:    runtimeevents.NewBus(),
	}
}

func documentModeTestSkill(name, dir string) *runtimeskill.Skill {
	return &runtimeskill.Skill{
		Name:        name,
		Description: "document mode test skill",
		Source: &runtimeskill.SkillSource{
			Path:   dir + "/SKILL.md",
			Dir:    dir,
			Layer:  "user",
			Format: runtimeskill.SkillSourceFormatCodex,
		},
		Codex: &runtimeskill.CodexSkillMetadata{Scope: "user"},
	}
}

// TestDetectMainLoopSkillInvocations_IncludesDocumentModeAndRefreshes 验证：
// 主循环观测索引包含文档模式技能，且技能面变化（注册）后索引自动重建。
func TestDetectMainLoopSkillInvocations_IncludesDocumentModeAndRefreshes(t *testing.T) {
	agent := newMainLoopObservationAgent(t)
	readArgs := map[string]interface{}{"path": "/skills/doc/SKILL.md"}

	if got := agent.DetectMainLoopSkillInvocations("read_file", readArgs); len(got) != 0 {
		t.Fatalf("empty registry must not match, got %+v", got)
	}

	if err := agent.RegisterSkill(documentModeTestSkill("doc-skill", "/skills/doc")); err != nil {
		t.Fatalf("register document-mode skill: %v", err)
	}
	got := agent.DetectMainLoopSkillInvocations("read_file", readArgs)
	if len(got) != 1 || got[0].Name != "doc-skill" {
		t.Fatalf("expected doc-skill attribution after registry mutation, got %+v", got)
	}
	shell := agent.DetectMainLoopSkillInvocations("run_shell_command", map[string]interface{}{
		"command": "bash build.sh",
		"cwd":     "/skills/doc/scripts",
	})
	if len(shell) != 1 || shell[0].Basis != runtimeskill.ImplicitBasisScriptsDir {
		t.Fatalf("expected scripts_dir attribution for doc-skill, got %+v", shell)
	}
}

// TestPublishSkillInvoked_DedupesWithinTurn 验证事件发布：总线级（空 SessionID）、
// 载荷带 source/session_id/trace_id，且同一 turn 内同键只发一次。
func TestPublishSkillInvoked_DedupesWithinTurn(t *testing.T) {
	agent := newMainLoopObservationAgent(t)
	var events []runtimeevents.Event
	agent.GetEventBus().Subscribe(runtimeskill.SkillInvokedEventType, func(event runtimeevents.Event) {
		events = append(events, event)
	})

	seen := make(map[string]struct{})
	invocations := []runtimeskill.ImplicitInvocation{{
		Name:  "doc-skill",
		Scope: "user",
		Path:  "/skills/doc",
		Kind:  runtimeskill.InvocationKindImplicit,
		Basis: runtimeskill.ImplicitBasisDocPath,
		Tool:  "read_file",
	}}
	agent.publishSkillInvoked("trace-1", "sess-1", 3, invocations, seen)
	agent.publishSkillInvoked("trace-1", "sess-1", 4, invocations, seen)

	if len(events) != 1 {
		t.Fatalf("expected exactly 1 deduped event, got %d", len(events))
	}
	if events[0].SessionID != "" {
		t.Fatalf("skills.invoked must stay bus-level (empty SessionID), got %q", events[0].SessionID)
	}
	if events[0].TraceID != "trace-1" {
		t.Fatalf("expected trace id stamped from payload, got %q", events[0].TraceID)
	}
	payload := events[0].Payload
	if payload["name"] != "doc-skill" || payload["source"] != "main_loop" || payload["session_id"] != "sess-1" {
		t.Fatalf("unexpected payload: %+v", payload)
	}
	if payload["step"] != 3 {
		t.Fatalf("expected first-emitting step in payload, got %+v", payload["step"])
	}
}
