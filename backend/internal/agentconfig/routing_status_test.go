package agentconfig

import "testing"

// 方案 §6.1/§3.5.1：投影的来源字段必须与实际键空间一致。
// 2026-09-22 复审回归：子 Agent 侧曾误用主 Agent 键前缀，source 恒回落 default。

// TestProjectSubAgentRoutingStatusSourceUsesSubKeySpace 钉住子 Agent 键空间
// （sub_agent.levels.<level>.<field>）：投影与逐级表格都必须带上真实来源。
func TestProjectSubAgentRoutingStatusSourceUsesSubKeySpace(t *testing.T) {
	res := RoutingResolution{
		EffectiveSub: &AICLISubagentRoutingConfig{
			Levels: map[string]AICLISubagentRouteProfile{"hard": {Model: "sub-model"}},
		},
		SubSources: map[string]RoutingSource{
			"sub_agent.levels.hard.model": RoutingSourceSession,
		},
	}

	proj := ProjectSubAgentRoutingStatus(res, "hard", "")
	if proj.Source != string(RoutingSourceSession) {
		t.Fatalf("子 Agent 投影来源应为 session，得到 %q", proj.Source)
	}
	if proj.Model != "sub-model" {
		t.Fatalf("子 Agent 投影 model 应取自 levels，得到 %q", proj.Model)
	}

	rows := BuildRoutingLevelSummaries(res, "sub")
	if len(rows) != 1 {
		t.Fatalf("子 Agent 逐级表格应有 1 行，得到 %#v", rows)
	}
	if rows[0].Source != string(RoutingSourceSession) {
		t.Fatalf("子 Agent 逐级表格来源应为 session，得到 %q", rows[0].Source)
	}
}

// TestProjectSubAgentRoutingStatusFallsBackToDefaultDifficulty 覆盖 default_difficulty
// 兜底路径：档位无逐字段来源时用 sub_agent.default_difficulty 的来源。
func TestProjectSubAgentRoutingStatusFallsBackToDefaultDifficulty(t *testing.T) {
	res := RoutingResolution{
		EffectiveSub: &AICLISubagentRoutingConfig{
			DefaultDifficulty: "hard",
			Levels:            map[string]AICLISubagentRouteProfile{"hard": {Model: "sub-model"}},
		},
		SubSources: map[string]RoutingSource{
			"sub_agent.default_difficulty": RoutingSourceWorkspace,
		},
	}
	if got := ProjectSubAgentRoutingStatus(res, "", "").Source; got != string(RoutingSourceWorkspace) {
		t.Fatalf("子 Agent 来源应回落到 default_difficulty 的 workspace，得到 %q", got)
	}
}

// TestProjectRoutingStatusSourceUsesMainKeySpace 是主 Agent 对照用例：
// 修复不得改变主 Agent 的键空间（main_agent.profiles.*）。
func TestProjectRoutingStatusSourceUsesMainKeySpace(t *testing.T) {
	res := RoutingResolution{
		Effective: &AICLIMainAgentRoutingConfig{
			Enabled:  true,
			Levels:   []string{"hard"},
			Profiles: map[string]AICLISubagentRouteProfile{"hard": {Model: "main-model"}},
		},
		Sources: map[string]RoutingSource{
			"main_agent.profiles.hard.model": RoutingSourceWorkspace,
		},
	}

	proj := ProjectRoutingStatus(res, "hard", "")
	if proj.Source != string(RoutingSourceWorkspace) {
		t.Fatalf("主 Agent 投影来源应为 workspace，得到 %q", proj.Source)
	}

	rows := BuildRoutingLevelSummaries(res, "main")
	if len(rows) != 1 || rows[0].Source != string(RoutingSourceWorkspace) {
		t.Fatalf("主 Agent 逐级表格应带 workspace 来源，得到 %#v", rows)
	}
}

// TestResolveSubagentRoutingFeedsSubKeySpaceSources 端到端钉住 I-6：
// 经 ResolveSubagentRouting 的真实解析结果（会话覆盖生效），其 SubSources
// 键空间必须能被 ProjectSubAgentRoutingStatus / BuildRoutingLevelSummaries 命中。
// 仅构造字面 map 的用例无法防止「解析器改键、投影未跟上」的再次漂移。
func TestResolveSubagentRoutingFeedsSubKeySpaceSources(t *testing.T) {
	enabled := true
	cfg := &Config{AICLI: &AICLIConfig{Subagents: &AICLISubagentsConfig{
		Routing: &AICLISubagentRoutingConfig{
			Enabled: &enabled,
			Levels: map[string]AICLISubagentRouteProfile{
				"hard": {Provider: "anthropic", Model: "cfg-model"},
			},
		},
	}}}
	override := &AICLISessionRoutingOverride{SubAgent: &AICLISessionSubAgentRoutingOverride{
		Levels: map[string]AICLISessionRouteProfileOverride{
			"hard": {Model: routingStrPtr("session-model")},
		},
	}}

	res := ResolveSubagentRouting(cfg, override, nil, nil)
	if got := res.SubSources["sub_agent.levels.hard.model"]; got != RoutingSourceSession {
		t.Fatalf("解析结果应写入 sub_agent.levels.hard.model=session，得到 %q（键=%v）", got, res.SubSources)
	}

	proj := ProjectSubAgentRoutingStatus(res, "hard", "")
	if proj.Source != string(RoutingSourceSession) || proj.Model != "session-model" {
		t.Fatalf("端到端投影应为 session/session-model，得到 source=%q model=%q", proj.Source, proj.Model)
	}

	found := false
	for _, row := range BuildRoutingLevelSummaries(res, "sub") {
		if row.Level != "hard" {
			continue
		}
		found = true
		if row.Source != string(RoutingSourceSession) {
			t.Fatalf("端到端逐级表格来源应为 session，得到 %q", row.Source)
		}
	}
	if !found {
		t.Fatalf("逐级表格应包含 hard 行，得到 %#v", BuildRoutingLevelSummaries(res, "sub"))
	}
}
