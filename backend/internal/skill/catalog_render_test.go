package skill

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func mustEntry(name, scope, srcPath, srcDir string, useAliases bool) SkillCatalogEntry {
	return SkillCatalogEntry{
		Name:        name,
		Description: name + " description",
		Scope:       scope,
		SourcePath:  srcPath,
		SourceDir:   srcDir,
		UseAliases:  useAliases,
	}
}

func TestBuildCatalogEntries_SortStable(t *testing.T) {
	skills := []*SkillSummary{
		{Name: "zebra", Source: &SkillSource{Path: "/a/SKILL.md", Dir: "/a", Layer: "user"}},
		{Name: "apple", Source: &SkillSource{Path: "/b/SKILL.md", Dir: "/b", Layer: "system"}},
		{Name: "mango", Source: &SkillSource{Path: "/c/SKILL.md", Dir: "/c", Layer: "repo"}},
	}
	entries := BuildCatalogEntries(skills)
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
	// scope 优先级 repo(0) > user(1) > system(2) → repo first, then user, then system。
	if entries[0].Name != "mango" || entries[1].Name != "zebra" || entries[2].Name != "apple" {
		t.Fatalf("scope-priority sort failed: %+v", entries)
	}
}

// TestBuildCatalogEntries_NilSourceDoesNotPanic 锁定程序化注册（Source 缺省）的
// 渲染健壮性：catalog 注入在 runtime-server 每回合都会渲染，任何 nil Source
// 都不得 panic（此前 s.Source.Path 未判空）。
func TestBuildCatalogEntries_NilSourceDoesNotPanic(t *testing.T) {
	entries := BuildCatalogEntries([]*SkillSummary{
		{Name: "no-source", Description: "d"},
		{Name: "with-source", Source: &SkillSource{Path: "/a/SKILL.md", Dir: "/a", Layer: "user"}},
	})
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	for _, entry := range entries {
		if entry.Name != "no-source" {
			continue
		}
		if entry.SourcePath != "" || entry.SourceDir != "" || entry.UseAliases {
			t.Fatalf("nil source must degrade to empty path/dir without aliases: %+v", entry)
		}
	}
	body, report := RenderSkillCatalog(entries, DefaultCatalogBudget(0))
	if body == "" || report.Included != 2 {
		t.Fatalf("render must succeed for source-less entries: %+v", report)
	}
}

// TestRenderSkillCatalog_FingerprintStableAcrossPermutation 验证 SK-8 的
// "排序稳定 = 快照"：同一技能集合的任意发现顺序必须渲染出逐字节一致的正文与指纹，
// 否则跨回合重注入会因顺序微变击穿 provider 前缀缓存。
func TestRenderSkillCatalog_FingerprintStableAcrossPermutation(t *testing.T) {
	summaries := []*SkillSummary{
		{Name: "zebra", Description: "z", Source: &SkillSource{Path: "/z/SKILL.md", Dir: "/z", Layer: "user"}},
		{Name: "apple", Description: "a", Source: &SkillSource{Path: "/a/SKILL.md", Dir: "/a", Layer: "repo"}},
		{Name: "mango", Description: "m", Source: &SkillSource{Path: "/m/SKILL.md", Dir: "/m", Layer: "system"}},
	}
	permuted := []*SkillSummary{summaries[2], summaries[0], summaries[1]}

	bodyA, reportA := RenderSkillCatalog(BuildCatalogEntries(summaries), DefaultCatalogBudget(0))
	bodyB, reportB := RenderSkillCatalog(BuildCatalogEntries(permuted), DefaultCatalogBudget(0))

	if bodyA != bodyB {
		t.Fatalf("render body must be order-independent:\n--- A ---\n%s\n--- B ---\n%s", bodyA, bodyB)
	}
	sumA := sha256.Sum256([]byte(bodyA))
	sumB := sha256.Sum256([]byte(bodyB))
	if hex.EncodeToString(sumA[:]) != hex.EncodeToString(sumB[:]) {
		t.Fatalf("render fingerprint must be order-independent")
	}
	if reportA.Included != reportB.Included || reportA.BodyChars != reportB.BodyChars {
		t.Fatalf("render report must be order-independent: %+v vs %+v", reportA, reportB)
	}
}

func TestDefaultCatalogBudget(t *testing.T) {
	// 0 tokens → 固定 8000。
	if b := DefaultCatalogBudget(0); b.Characters != 8_000 {
		t.Fatalf("expected 8000, got %d", b.Characters)
	}
	// 200k tokens → 2% = 4000 tokens → 16000 chars > 8000 → 16000。
	if b := DefaultCatalogBudget(200_000); b.Characters != 16_000 {
		t.Fatalf("expected 16000, got %d", b.Characters)
	}
	// 100k tokens → 2% = 2000 tokens → 8000 chars == 8000 → 8000。
	if b := DefaultCatalogBudget(100_000); b.Characters != 8_000 {
		t.Fatalf("expected 8000, got %d", b.Characters)
	}
}

func TestRenderSkillCatalog_BudgetDegradation(t *testing.T) {
	long := strings.Repeat("x", 5_000)
	entries := []SkillCatalogEntry{
		mustEntry("alpha", "repo", "/a/SKILL.md", "/a", false),
		mustEntry("beta", "repo", "/b/SKILL.md", "/b", false),
		{Name: "gamma", Description: long, Scope: "repo", SourcePath: "/c/SKILL.md", SourceDir: "/c", UseAliases: false},
	}
	// 极小预算 → 描述被省去但条目仍在。
	body, report := RenderSkillCatalog(entries, CatalogBudget{Characters: 100})
	if report.Total != 3 || report.Included != 3 {
		t.Fatalf("all skills must remain: %+v", report)
	}
	if report.OmittedDescriptionCount != 3 {
		t.Fatalf("expected 3 omitted descriptions, got %d", report.OmittedDescriptionCount)
	}
	for _, name := range []string{"alpha", "beta", "gamma"} {
		if !strings.Contains(body, name) {
			t.Fatalf("skill %q must still appear in catalog", name)
		}
	}
	if !strings.Contains(body, "Skill descriptions were omitted") {
		t.Fatalf("expected degradation warning")
	}
}

func TestRenderSkillCatalog_AliasForm(t *testing.T) {
	entries := []SkillCatalogEntry{
		mustEntry("alpha", "repo", "/repo/.agents/skills/alpha/SKILL.md", "/repo/.agents/skills/alpha", true),
	}
	body, _ := RenderSkillCatalog(entries, DefaultCatalogBudget(0))
	if !strings.Contains(body, "### Skill roots") {
		t.Fatalf("alias form must include skill roots table:\n%s", body)
	}
	if !strings.Contains(body, "repo: /repo/.agents/skills/alpha") {
		t.Fatalf("expected root alias line")
	}
	if !strings.Contains(body, SKILLS_HOW_TO_USE_WITH_ALIASES) {
		t.Fatalf("expected alias discipline block")
	}
}

func TestRenderSkillCatalog_DisciplineBlockPresent(t *testing.T) {
	entries := []SkillCatalogEntry{mustEntry("alpha", "repo", "/a/SKILL.md", "/a", false)}
	body, _ := RenderSkillCatalog(entries, DefaultCatalogBudget(0))
	if !strings.Contains(body, "How to use a skill") || !strings.Contains(body, "Do not carry skills across turns") {
		t.Fatalf("discipline block missing:\n%s", body)
	}
}

// 多条目、单条描述均短于预算：必须发生预算降级且正文不超预算。
// 回归背景：旧实现的裁剪判断是"单行长度 vs 总预算"，该场景零裁剪零告警。
func TestRenderSkillCatalog_MultiEntryBudgetEnforced(t *testing.T) {
	const entryCount = 60
	entries := make([]SkillCatalogEntry, 0, entryCount)
	names := make([]string, 0, entryCount)
	for i := 0; i < entryCount; i++ {
		name := fmt.Sprintf("skill-%03d", i)
		names = append(names, name)
		entries = append(entries, SkillCatalogEntry{
			Name:        name,
			Description: strings.Repeat("d", 150),
			Scope:       "repo",
			SourcePath:  fmt.Sprintf("/skills/%s/SKILL.md", name),
		})
	}
	body, report := RenderSkillCatalog(entries, CatalogBudget{Characters: 8_000})
	if report.Total != entryCount || report.Included != entryCount {
		t.Fatalf("all skills must remain: %+v", report)
	}
	if report.BodyChars > 8_000 {
		t.Fatalf("catalog body must respect budget: body=%d report=%s", report.BodyChars, report)
	}
	if !report.Degraded() {
		t.Fatalf("expected degradation for over-budget multi-entry catalog: %s", report)
	}
	for _, name := range names {
		if !strings.Contains(body, name) {
			t.Fatalf("skill %q must still appear in catalog", name)
		}
	}
	// 渲染不得改写调用方条目，且同输入两次渲染完全一致（确定性）。
	bodyAgain, _ := RenderSkillCatalog(entries, CatalogBudget{Characters: 8_000})
	if bodyAgain != body {
		t.Fatalf("catalog rendering must be deterministic and side-effect free")
	}
}

func TestRenderSkillCatalog_HugeSingleDescriptionTruncated(t *testing.T) {
	entries := []SkillCatalogEntry{{
		Name:        "huge",
		Description: strings.Repeat("x", 20_000),
		Scope:       "repo",
		SourcePath:  "/skills/huge/SKILL.md",
	}}
	body, report := RenderSkillCatalog(entries, CatalogBudget{Characters: 8_000})
	if report.TruncatedDescriptionCount != 1 {
		t.Fatalf("expected the oversized description to be truncated: %s", report)
	}
	if report.BodyChars > 8_000 {
		t.Fatalf("body must respect budget: %d", report.BodyChars)
	}
	if strings.Contains(body, strings.Repeat("x", 101)) {
		t.Fatalf("description beyond the truncate threshold must not be rendered")
	}
}

func TestRenderSkillCatalog_RuneSafeTruncation(t *testing.T) {
	entries := []SkillCatalogEntry{{
		Name:        "cjk",
		Description: strings.Repeat("技能描述", 60), // 240 runes，远超阈值
		Scope:       "repo",
		SourcePath:  "/skills/cjk/SKILL.md",
	}}
	body, report := RenderSkillCatalog(entries, CatalogBudget{Characters: 2_500})
	if !utf8.ValidString(body) {
		t.Fatalf("truncation must not split multi-byte runes")
	}
	if !report.Degraded() {
		t.Fatalf("expected degradation under a tight budget: %s", report)
	}
	if report.BodyChars > 2_500 {
		t.Fatalf("body must respect budget: %d", report.BodyChars)
	}
}

func TestRenderSkillCatalog_ZeroBudgetUsesDefault(t *testing.T) {
	entries := []SkillCatalogEntry{mustEntry("alpha", "repo", "/a/SKILL.md", "/a", false)}
	body, report := RenderSkillCatalog(entries, CatalogBudget{Characters: 0})
	if report.BudgetChars != 8_000 {
		t.Fatalf("zero budget must fall back to default: %s", report)
	}
	if !strings.Contains(body, "alpha description") {
		t.Fatalf("default budget must keep short descriptions intact")
	}
}

func TestRenderSkillCatalogWithOptions_DisciplineBlockDisabled(t *testing.T) {
	entries := []SkillCatalogEntry{mustEntry("alpha", "repo", "/a/SKILL.md", "/a", false)}
	withBlock, _ := RenderSkillCatalogWithOptions(entries, DefaultCatalogBudget(0), true)
	withoutBlock, _ := RenderSkillCatalogWithOptions(entries, DefaultCatalogBudget(0), false)
	if !strings.Contains(withBlock, "Do not carry skills across turns") {
		t.Fatalf("discipline block must be present when enabled")
	}
	if strings.Contains(withoutBlock, "Do not carry skills across turns") {
		t.Fatalf("discipline block must be absent when disabled")
	}
	if !strings.Contains(withoutBlock, "### Available skills") || !strings.Contains(withoutBlock, "alpha") {
		t.Fatalf("catalog entries must survive when the discipline block is disabled")
	}
}
