package skills

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

// TestCatalogSummariesFromCodexSkills_MapsPathDirAndScope 验证 discovery 结果到
// catalog 摘要的映射：目录取 PathToSkillsMD 的父目录，scope 进 Layer，格式固定 Codex。
func TestCatalogSummariesFromCodexSkills_MapsPathDirAndScope(t *testing.T) {
	items := []*skill.CodexSkillMetadata{
		{Name: "build", Description: "build it", PathToSkillsMD: "/repo/.agents/skills/build/SKILL.md", Scope: "repo"},
		nil,
		{Name: "   "},
	}
	summaries := catalogSummariesFromCodexSkills(items)
	if len(summaries) != 1 {
		t.Fatalf("expected 1 summary, got %d", len(summaries))
	}
	source := summaries[0].Source
	if source == nil {
		t.Fatalf("summary source must be set")
	}
	if source.Dir != filepath.Dir("/repo/.agents/skills/build/SKILL.md") {
		t.Fatalf("unexpected dir mapping: %q", source.Dir)
	}
	if source.Layer != "repo" || source.Format != skill.SkillSourceFormatCodex {
		t.Fatalf("unexpected source layer/format: %+v", source)
	}
}

// TestBuildCodexListCatalogProjection_MatchesCatalogRenderer 是 SK-5 的一致性验收：
// list 投影必须与注入共用 BuildCatalogEntries + RenderSkillCatalogWithOptions——
// 同一输入快照下条目数/正文体积/裁剪计数逐项相等，且降级可见。
func TestBuildCodexListCatalogProjection_MatchesCatalogRenderer(t *testing.T) {
	long := strings.Repeat("d", 300)
	items := []*skill.CodexSkillMetadata{
		{Name: "alpha", Description: long, PathToSkillsMD: "/r/alpha/SKILL.md", Scope: "repo"},
		{Name: "beta", Description: long, PathToSkillsMD: "/r/beta/SKILL.md", Scope: "user"},
		{Name: "gamma", Description: long, PathToSkillsMD: "/r/gamma/SKILL.md", Scope: "system"},
	}
	groups := []codexSkillsListGroup{{Skills: items}}

	const budget = 600
	projection := buildCodexListCatalogProjection(groups, budget, false)
	if projection == nil {
		t.Fatalf("projection must be built for non-empty discovery results")
	}

	body, report := skill.RenderSkillCatalogWithOptions(
		skill.BuildCatalogEntries(catalogSummariesFromCodexSkills(items)),
		skill.CatalogBudget{Characters: budget},
		false,
	)
	if projection.EntryCount != report.Included {
		t.Fatalf("entry count mismatch: projection=%d renderer=%d", projection.EntryCount, report.Included)
	}
	if projection.BodyChars != report.BodyChars || projection.BudgetChars != report.BudgetChars {
		t.Fatalf("body/budget mismatch: %+v vs %+v", projection, report)
	}
	if projection.TruncatedDescriptions != report.TruncatedDescriptionCount ||
		projection.OmittedDescriptions != report.OmittedDescriptionCount {
		t.Fatalf("degradation counters mismatch: %+v vs %+v", projection, report)
	}
	if !projection.Degraded || projection.TruncatedDescriptions == 0 {
		t.Fatalf("expected visible degradation for over-budget snapshot: %+v", projection)
	}
	if projection.Fingerprint == "" {
		t.Fatalf("fingerprint must be set for cross-surface consistency checks")
	}
	if len(body) == 0 {
		t.Fatalf("renderer body must be non-empty")
	}
}

// TestBuildCodexListCatalogProjection_Empty 验证空输入/无技能组的省略语义。
func TestBuildCodexListCatalogProjection_Empty(t *testing.T) {
	if projection := buildCodexListCatalogProjection(nil, 8_000, true); projection != nil {
		t.Fatalf("nil groups must omit projection, got %+v", projection)
	}
	if projection := buildCodexListCatalogProjection([]codexSkillsListGroup{{}}, 8_000, true); projection != nil {
		t.Fatalf("empty skills must omit projection, got %+v", projection)
	}
}

// TestBuildCatalogProjectionFromSummaries_MatchesCodexVariant 锁定"唯一实现"：
// discovery 结果与等价摘要必须产出逐字段相同的投影（同一渲染器、同一预算）。
func TestBuildCatalogProjectionFromSummaries_MatchesCodexVariant(t *testing.T) {
	long := strings.Repeat("f", 300)
	items := []*skill.CodexSkillMetadata{
		{Name: "alpha", Description: long, PathToSkillsMD: "/r/alpha/SKILL.md", Scope: "repo"},
		{Name: "beta", Description: long, PathToSkillsMD: "/r/beta/SKILL.md", Scope: "user"},
	}
	fromCodex := buildCodexListCatalogProjection([]codexSkillsListGroup{{Skills: items}}, 500, true)
	fromSummaries := buildCatalogProjectionFromSummaries(catalogSummariesFromCodexSkills(items), 500, true)

	require.NotNil(t, fromCodex)
	require.NotNil(t, fromSummaries)
	require.Equal(t, *fromCodex, *fromSummaries)
}

// TestCatalogProjectionFromRegistry_RequiresRegistryAndConfig 验证注册表侧投影的
// 前置条件：注册表缺失或 skills runtime 未配置时省略（不得伪造默认渲染）。
func TestCatalogProjectionFromRegistry_RequiresRegistryAndConfig(t *testing.T) {
	registry := skill.NewRegistry(nil)
	require.NoError(t, registry.Register(&skill.Skill{
		Name:        "alpha",
		Description: strings.Repeat("g", 300),
		Source:      &skill.SkillSource{Path: "/r/alpha/SKILL.md", Dir: "/r/alpha", Layer: "user"},
	}))

	handler := NewHandler(registry, nil, nil)
	require.Nil(t, handler.catalogProjectionFromRegistry(), "unconfigured skills runtime must omit projection")

	handler.SetAICLIConfig(&agentconfig.Config{
		AICLI:         &agentconfig.AICLIConfig{},
		SkillsRuntime: &agentconfig.SkillsRuntimeConfig{CatalogBudgetChars: 400},
	})
	projection := handler.catalogProjectionFromRegistry()
	require.NotNil(t, projection)
	require.Equal(t, 400, projection.BudgetChars)
	require.Positive(t, projection.EntryCount)

	require.Nil(t, NewHandler(nil, nil, nil).catalogProjectionFromRegistry(), "missing registry must omit projection")
}
