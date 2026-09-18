package runtimeobserve

import (
	"testing"
	"time"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// SK-3 远程可观测：skills.invoked 进入 v1 白名单，且投影保持低敏——
// 名称/枚举字段透传，路径与正文不导出，会话归属从载荷提升到 correlation。
func TestProjectRuntimeEventSkillInvokedLiftsSessionAndKeepsLowSensitivityFields(t *testing.T) {
	p := NewProjector(NewRedactor(nil, "", ""), false, 0)
	proj, ok := p.ProjectRuntimeEvent(runtimeevents.Event{
		Type:      EventSkillInvoked,
		Timestamp: time.Unix(1700002000, 0).UTC(),
		AgentName: "chat-actor",
		Payload: map[string]interface{}{
			"name":       "run_shell_command",
			"scope":      "repo",
			"kind":       "implicit",
			"basis":      "scripts_dir",
			"tool":       "run_shell_command",
			"path":       "E:/repo/.agents/skills/run_shell_command",
			"session_id": "session-remote-1",
			"trace_id":   "trace-remote-1",
			"step":       3,
			"prompt":     "secret body",
		},
	})
	if !ok {
		t.Fatal("skills.invoked must be allowlisted for observation")
	}
	if proj.Correlation.SessionID != "session-remote-1" {
		t.Fatalf("session_id must be lifted from payload, got %q", proj.Correlation.SessionID)
	}
	if proj.Correlation.TraceID != "trace-remote-1" {
		t.Fatalf("trace_id must be lifted from payload, got %q", proj.Correlation.TraceID)
	}
	if proj.Correlation.AgentID != "chat-actor" {
		t.Fatalf("agent id must come from the bus event, got %q", proj.Correlation.AgentID)
	}
	for _, key := range []string{"name", "scope", "kind", "basis", "tool"} {
		if _, exists := proj.Payload[key]; !exists {
			t.Fatalf("low-sensitivity field %q must be exported: %+v", key, proj.Payload)
		}
	}
	if proj.Payload["has_path"] != true {
		t.Fatalf("path presence must be exported as has_path: %+v", proj.Payload)
	}
	if proj.Payload["step"] != 3 {
		t.Fatalf("step watermark must be exported, got %v", proj.Payload["step"])
	}
	for _, forbidden := range []string{"path", "session_id", "trace_id", "prompt"} {
		if _, exists := proj.Payload[forbidden]; exists {
			t.Fatalf("field %q must not be exported: %+v", forbidden, proj.Payload)
		}
	}
}

// 事件本身带 SessionID 时（未来若改为会话级发布）以事件字段为准，载荷不得覆盖。
func TestProjectRuntimeEventSkillInvokedPrefersEventSession(t *testing.T) {
	p := NewProjector(NewRedactor(nil, "", ""), false, 0)
	proj, ok := p.ProjectRuntimeEvent(runtimeevents.Event{
		Type:      EventSkillInvoked,
		Timestamp: time.Unix(1700002100, 0).UTC(),
		SessionID: "session-bus",
		AgentName: "agent-loop",
		Payload: map[string]interface{}{
			"name":       "alpha",
			"session_id": "session-payload",
		},
	})
	if !ok {
		t.Fatal("skills.invoked must be allowlisted")
	}
	if proj.Correlation.SessionID != "session-bus" {
		t.Fatalf("bus-level session must win, got %q", proj.Correlation.SessionID)
	}
}

// 空载荷（无低敏字段可导出）仍应投影出事件本身，只保留 correlation。
func TestProjectRuntimeEventSkillInvokedEmptyPayloadStillProjects(t *testing.T) {
	p := NewProjector(NewRedactor(nil, "", ""), false, 0)
	proj, ok := p.ProjectRuntimeEvent(runtimeevents.Event{
		Type:      EventSkillInvoked,
		Timestamp: time.Unix(1700002200, 0).UTC(),
		Payload:   map[string]interface{}{"prompt": "secret"},
	})
	if !ok {
		t.Fatal("skills.invoked must be allowlisted")
	}
	if len(proj.Payload) != 0 {
		t.Fatalf("no low-sensitivity field should survive, got %+v", proj.Payload)
	}
}

// 目录不变量：白名单与已知目录一致（TestKnownEventTypeCatalogInvariants 的另一半）。
func TestKnownEventTypeCatalogIncludesSkillInvoked(t *testing.T) {
	if !IsAllowedType(EventSkillInvoked) {
		t.Fatal("skills.invoked must be in the v1 allowlist")
	}
	if !IsKnownEventType(EventSkillInvoked) {
		t.Fatal("skills.invoked must be in the known-type catalog")
	}
}

// 端到端：总线级 skills.invoked（Event.SessionID 留空、会话归属在载荷）经 collector
// 投影后，按 session_id 过滤的查询必须命中——这就是远程观测技能调用的验收口径。
func TestCollectorProjectsSkillInvokedForSessionFilteredQuery(t *testing.T) {
	bus := runtimeevents.NewBusWithRetention(2048)
	c := NewCollector(testConfig(), bus, nil)
	if c == nil {
		t.Fatal("expected non-nil collector")
	}
	c.Start()
	defer c.Stop()

	bus.Publish(runtimeevents.Event{
		Type:      EventSkillInvoked,
		Timestamp: time.Unix(1700002300, 0).UTC(),
		AgentName: "skills-runtime",
		Payload: map[string]interface{}{
			"name":       "run_shell_command",
			"scope":      "repo",
			"kind":       "explicit",
			"basis":      "explicit",
			"tool":       "run_shell_command",
			"path":       "E:/repo/.agents/skills/run_shell_command",
			"session_id": "session-skill-1",
		},
	})
	waitCollector(t, c, 1)

	res, err := c.Query(EventQuery{SessionID: "session-skill-1", Limit: 10})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(res.Events) != 1 {
		t.Fatalf("session-filtered query must return the skill event, got %d", len(res.Events))
	}
	evt := res.Events[0]
	if evt.Type != EventSkillInvoked {
		t.Fatalf("type=%q want %q", evt.Type, EventSkillInvoked)
	}
	if evt.Correlation.SessionID != "session-skill-1" {
		t.Fatalf("correlation session=%q want session-skill-1", evt.Correlation.SessionID)
	}
	if evt.Payload["name"] != "run_shell_command" || evt.Payload["has_path"] != true {
		t.Fatalf("unexpected low-sensitivity payload: %+v", evt.Payload)
	}
	if _, leaked := evt.Payload["path"]; leaked {
		t.Fatalf("filesystem path must not be exported: %+v", evt.Payload)
	}

	// 反向：其他会话查询不得串场。
	other, err := c.Query(EventQuery{SessionID: "session-other", Limit: 10})
	if err != nil {
		t.Fatalf("query other session: %v", err)
	}
	if len(other.Events) != 0 {
		t.Fatalf("skill event must not leak into other sessions, got %d", len(other.Events))
	}

	// 白名单生效后不再计入"已知但被过滤"。
	rt, _ := c.Stats()
	if _, filtered := rt.FilteredByType[EventSkillInvoked]; filtered {
		t.Fatalf("allowlisted type must not be counted as filtered: %v", rt.FilteredByType)
	}
}
