package commands

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

// 方案 §3.1/§5.6：子 Agent 作用域必须说明「team 继承 sub_agent」，并只读展示
// task_types/roles 的存在；主 Agent 作用域不出现这些行（避免噪音与误读）。

func TestChatRoutingSubScopeNotesTeamInheritance(t *testing.T) {
	cfg := &agentconfig.AICLIConfig{
		Subagents: &agentconfig.AICLISubagentsConfig{
			Routing: &agentconfig.AICLISubagentRoutingConfig{
				Levels: map[string]agentconfig.AICLISubagentRouteProfile{"hard": {Model: "sub-model"}},
			},
		},
	}
	session := routingStatusTestSession(t, nil, cfg)

	show, err := chatRoutingShowText(session, "sub", false)
	if err != nil {
		t.Fatalf("show sub: %v", err)
	}
	if !strings.Contains(show, "继承 sub_agent 路由") {
		t.Fatalf("show sub 应说明 team 继承 sub_agent，得到:\n%s", show)
	}

	doctor := chatRoutingDoctorText(session, "sub")
	if !strings.Contains(doctor, "继承 sub_agent 路由") {
		t.Fatalf("doctor sub 应说明 team 继承 sub_agent，得到:\n%s", doctor)
	}

	mainShow, err := chatRoutingShowText(session, "main", false)
	if err != nil {
		t.Fatalf("show main: %v", err)
	}
	if strings.Contains(mainShow, "继承 sub_agent 路由") {
		t.Fatalf("main 作用域不应出现 team 说明，得到:\n%s", mainShow)
	}
}

func TestChatRoutingSubScopeNotesTeamConfiguredAndTaskTypes(t *testing.T) {
	cfg := &agentconfig.AICLIConfig{
		Teams: &agentconfig.AICLITeamsConfig{
			Routing: &agentconfig.AICLISubagentRoutingConfig{DefaultDifficulty: "hard"},
		},
		Subagents: &agentconfig.AICLISubagentsConfig{
			Routing: &agentconfig.AICLISubagentRoutingConfig{
				TaskTypes: map[string]map[string]agentconfig.AICLISubagentRouteProfile{
					"research": {"hard": {Model: "m"}},
				},
			},
		},
	}
	session := routingStatusTestSession(t, nil, cfg)

	show, err := chatRoutingShowText(session, "sub", false)
	if err != nil {
		t.Fatalf("show sub: %v", err)
	}
	if !strings.Contains(show, "已单独配置 aicli.teams.routing") {
		t.Fatalf("team 已配置时应只读展示其存在，得到:\n%s", show)
	}
	if !strings.Contains(show, "task_types/roles") {
		t.Fatalf("task_types 已配置时应只读展示其存在，得到:\n%s", show)
	}
}
