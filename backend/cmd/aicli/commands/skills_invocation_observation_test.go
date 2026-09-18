package commands

import (
	"testing"
	"time"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/runtimeobserve"
	runtimeskill "github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

// SK-3 远程可观测补口：TUI 技能桥（/skill → skill__<name>）把调用发布到本地
// runtime 总线——显式一次 + 执行中命中的隐式调用，事件保持总线级、会话归属在载荷。
func TestSkillFunctionPublishesInvocationsToLocalBus(t *testing.T) {
	bus := runtimeevents.NewBusWithRetention(256)
	var got []runtimeevents.Event
	bus.Subscribe(runtimeskill.SkillInvokedEventType, func(event runtimeevents.Event) {
		got = append(got, event)
	})

	session := &ChatSession{
		LocalRuntimeHost: &localChatRuntimeHost{EventBus: bus},
		RuntimeSession:   &runtimechat.Session{ID: "session-skill-1"},
	}
	fn := &SkillFunction{
		summary: &runtimeskill.SkillSummary{
			Name: "run_shell_command",
			Source: &runtimeskill.SkillSource{
				Path:  "/skills/run_shell_command/SKILL.md",
				Dir:   "/skills/run_shell_command",
				Layer: "repo",
			},
		},
		sourcePath:          "/skills/run_shell_command/SKILL.md",
		invocationPublisher: newSkillInvocationPublisher(session),
	}
	result := &runtimeskill.ExecuteResult{
		SkillName: "run_shell_command",
		Success:   true,
		ImplicitInvocations: []runtimeskill.ImplicitInvocation{{
			Name:  "run_shell_command",
			Scope: "repo",
			Path:  "/skills/run_shell_command",
			Kind:  runtimeskill.InvocationKindImplicit,
			Basis: runtimeskill.ImplicitBasisScriptsDir,
			Tool:  "run_shell_command",
		}},
	}
	fn.publishSkillInvocations(&runtimeskill.Skill{Name: "run_shell_command"}, result)

	if len(got) != 2 {
		t.Fatalf("expected explicit+implicit events, got %d: %+v", len(got), got)
	}
	for i, event := range got {
		if event.Type != runtimeskill.SkillInvokedEventType {
			t.Fatalf("event[%d] type=%q", i, event.Type)
		}
		if event.SessionID != "" {
			t.Fatalf("event[%d] must stay bus-level (empty SessionID), got %q", i, event.SessionID)
		}
		if event.AgentName != "aicli-skill-bridge" {
			t.Fatalf("event[%d] agent=%q", i, event.AgentName)
		}
		if event.Payload["session_id"] != "session-skill-1" {
			t.Fatalf("event[%d] session_id=%v", i, event.Payload["session_id"])
		}
		if event.Payload["source"] != "tui_skill_bridge" {
			t.Fatalf("event[%d] source=%v", i, event.Payload["source"])
		}
		if event.Payload["name"] != "run_shell_command" {
			t.Fatalf("event[%d] name=%v", i, event.Payload["name"])
		}
	}
	if got[0].Payload["kind"] != "explicit" || got[0].Payload["basis"] != "mention" {
		t.Fatalf("explicit invocation payload=%+v", got[0].Payload)
	}
	if got[0].Payload["scope"] != "repo" || got[0].Payload["path"] != "/skills/run_shell_command/SKILL.md" {
		t.Fatalf("explicit invocation scope/path payload=%+v", got[0].Payload)
	}
	if got[1].Payload["kind"] != "implicit" || got[1].Payload["basis"] != "scripts_dir" {
		t.Fatalf("implicit invocation payload=%+v", got[1].Payload)
	}
}

// 无发布器（host 尚未建立总线）时退化为日志，不 panic。
func TestSkillFunctionWithoutPublisherDoesNotPanic(t *testing.T) {
	fn := &SkillFunction{}
	fn.publishSkillInvocations(&runtimeskill.Skill{Name: "alpha"}, &runtimeskill.ExecuteResult{Success: true})
}

// 空会话的发布器可安全调用（运行时再判定 host/EventBus 缺席）。
func TestSkillInvocationPublisherWithoutHostIsNoop(t *testing.T) {
	publish := newSkillInvocationPublisher(&ChatSession{})
	if publish == nil {
		t.Fatal("empty session must still yield a safe publisher")
	}
	publish(runtimeskill.NewExplicitInvocation("alpha", "repo", "/skills/alpha/SKILL.md"))
}

// 跨层闭环：TUI 发布器 → 本地 EventBus → observe collector → 按 session_id 查询
// （与远程 GET /api/runtime/observe/v1/events?session_id=… 同一条链路）。
func TestSkillInvocationPublisherFeedsLocalObservePlane(t *testing.T) {
	bus := runtimeevents.NewBusWithRetention(256)
	cfg := runtimeobserve.WithDefaults(runtimecfg.DefaultRuntimeConfig().Observe)
	cfg.Enabled = true
	redactor := runtimeobserve.NewRedactor(nil, "", cfg.RedactionProfile)
	projector := runtimeobserve.NewProjector(redactor, cfg.ExposeProviderRequestID, int(cfg.MaxEventBytes))
	collector := runtimeobserve.NewCollector(cfg, bus, projector)
	if collector == nil {
		t.Fatal("expected non-nil collector")
	}
	collector.Start()
	defer collector.Stop()

	session := &ChatSession{
		LocalRuntimeHost: &localChatRuntimeHost{EventBus: bus},
		RuntimeSession:   &runtimechat.Session{ID: "session-skill-1"},
	}
	publishSkillTurnInvocation(session, "skill__run_shell_command", &runtimeskill.Skill{
		Name:   "run_shell_command",
		Source: &runtimeskill.SkillSource{Path: "/skills/run_shell_command/SKILL.md", Dir: "/skills/run_shell_command", Layer: "repo"},
	})

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, latest := collector.RingBounds(); latest >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	res, err := collector.Query(runtimeobserve.EventQuery{SessionID: "session-skill-1", Limit: 10})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(res.Events) != 1 {
		t.Fatalf("observe query must return the skill invocation, got %d", len(res.Events))
	}
	event := res.Events[0]
	if event.Type != runtimeobserve.EventSkillInvoked {
		t.Fatalf("type=%q want %q", event.Type, runtimeobserve.EventSkillInvoked)
	}
	if event.Correlation.SessionID != "session-skill-1" {
		t.Fatalf("correlation session=%q", event.Correlation.SessionID)
	}
	if event.Payload["name"] != "run_shell_command" || event.Payload["kind"] != "explicit" {
		t.Fatalf("payload=%+v", event.Payload)
	}
	if _, leaked := event.Payload["path"]; leaked {
		t.Fatalf("path must not be exported: %+v", event.Payload)
	}
}

// /skill 回合可能用技能声明的程序工具（如 bash）完成，不经过 SkillFunction.Execute：
// 派发点发布显式事件；模型随后点名同一技能函数不重复发；隐式命中不受抑制；
// 新回合重置后同一显式调用重新可见。
func TestPublishSkillTurnInvocationDedupesWithinTurn(t *testing.T) {
	bus := runtimeevents.NewBusWithRetention(64)
	var got []runtimeevents.Event
	bus.Subscribe(runtimeskill.SkillInvokedEventType, func(event runtimeevents.Event) {
		got = append(got, event)
	})
	session := &ChatSession{
		LocalRuntimeHost: &localChatRuntimeHost{EventBus: bus},
		RuntimeSession:   &runtimechat.Session{ID: "session-skill-2"},
	}
	skillItem := &runtimeskill.Skill{
		Name:   "run_shell_command",
		Source: &runtimeskill.SkillSource{Path: "/skills/run_shell_command/SKILL.md", Dir: "/skills/run_shell_command", Layer: "repo"},
	}

	publishSkillTurnInvocation(session, "skill__run_shell_command", skillItem)
	if len(got) != 1 {
		t.Fatalf("dispatch must publish exactly one explicit event, got %d", len(got))
	}
	if got[0].Payload["name"] != "run_shell_command" ||
		got[0].Payload["kind"] != "explicit" ||
		got[0].Payload["basis"] != "mention" ||
		got[0].Payload["scope"] != "repo" ||
		got[0].Payload["path"] != "/skills/run_shell_command/SKILL.md" {
		t.Fatalf("dispatch payload=%+v", got[0].Payload)
	}

	publish := newSkillInvocationPublisher(session)
	publish(runtimeskill.NewExplicitInvocation("run_shell_command", "repo", "/skills/run_shell_command/SKILL.md"))
	if len(got) != 1 {
		t.Fatalf("same-turn explicit duplicate must be suppressed, got %d", len(got))
	}

	publish(runtimeskill.ImplicitInvocation{
		Name:  "run_shell_command",
		Scope: "repo",
		Path:  "/skills/run_shell_command",
		Kind:  runtimeskill.InvocationKindImplicit,
		Basis: runtimeskill.ImplicitBasisScriptsDir,
		Tool:  "run_shell_command",
	})
	if len(got) != 2 || got[1].Payload["kind"] != "implicit" {
		t.Fatalf("implicit hit must still publish, got %d: %+v", len(got), got)
	}

	session.resetSkillInvocationObservation()
	publish(runtimeskill.NewExplicitInvocation("run_shell_command", "repo", "/skills/run_shell_command/SKILL.md"))
	if len(got) != 3 {
		t.Fatalf("new turn must publish the explicit invocation again, got %d", len(got))
	}
}
