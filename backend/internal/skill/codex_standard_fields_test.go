package skill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeStandardSkill(t *testing.T, dir, name, frontmatter string) string {
	t.Helper()
	skillDir := filepath.Join(dir, name)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	path := filepath.Join(skillDir, "SKILL.md")
	if err := os.WriteFile(path, []byte(frontmatter), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	return path
}

// Agent Skills 标准可选字段必须被解析并落到 CodexSkillMetadata。
func TestParseCodexSkill_StandardOptionalFields(t *testing.T) {
	dir := t.TempDir()
	path := writeStandardSkill(t, dir, "demo-skill", `---
name: demo-skill
description: demo skill for standard fields
license: Apache-2.0
compatibility: requires git and python >= 3.10
metadata:
  short-description: demo
  author: wwsheng
  version: 2.1.0
allowed-tools: Bash(git:*) Read
disallowed-tools:
  - Write
argument-hint: "[file] [--flag]"
when_to_use: Use when the user asks to inspect the repository history.
disable-model-invocation: true
user-invocable: false
arguments:
  - name: file
    description: target file
    required: true
  - name: mode
    default: safe
model: claude-sonnet
effort: high
---

Body text.
`)

	skillItem, err := NewLoader(nil).LoadFileFull(path)
	if err != nil {
		t.Fatalf("LoadFileFull: %v", err)
	}
	meta := skillItem.Codex
	if meta == nil {
		t.Fatal("codex metadata is nil")
	}
	if meta.License != "Apache-2.0" {
		t.Fatalf("license = %q", meta.License)
	}
	if meta.Compatibility != "requires git and python >= 3.10" {
		t.Fatalf("compatibility = %q", meta.Compatibility)
	}
	if meta.Author != "wwsheng" || meta.StandardVersion != "2.1.0" {
		t.Fatalf("author/version = %q/%q", meta.Author, meta.StandardVersion)
	}
	if len(meta.AllowedTools) != 2 || meta.AllowedTools[0] != "Bash(git:*)" || meta.AllowedTools[1] != "Read" {
		t.Fatalf("allowed-tools = %#v", meta.AllowedTools)
	}
	if len(meta.DisallowedTools) != 1 || meta.DisallowedTools[0] != "Write" {
		t.Fatalf("disallowed-tools = %#v", meta.DisallowedTools)
	}
	if meta.ArgumentHint != "[file] [--flag]" {
		t.Fatalf("argument-hint = %q", meta.ArgumentHint)
	}
	if !strings.Contains(meta.WhenToUse, "inspect the repository history") {
		t.Fatalf("when_to_use = %q", meta.WhenToUse)
	}
	if !meta.DisableModelInvocation {
		t.Fatal("disable-model-invocation not parsed")
	}
	if meta.UserInvocableEnabled() {
		t.Fatal("user-invocable: false must disable user invocation")
	}
	if meta.ImplicitInvocationAllowed() {
		t.Fatal("disable-model-invocation must disable implicit invocation")
	}
	if len(meta.Arguments) != 2 || meta.Arguments[0].Name != "file" || meta.Arguments[1].Default != "safe" {
		t.Fatalf("arguments = %#v", meta.Arguments)
	}
	if meta.Model != "claude-sonnet" || meta.Effort != "high" {
		t.Fatalf("model/effort = %q/%q", meta.Model, meta.Effort)
	}

	// 运行时访问器与摘要口径一致。
	if skillItem.ModelInvocable() {
		t.Fatal("Skill.ModelInvocable must be false")
	}
	if skillItem.UserInvocable() {
		t.Fatal("Skill.UserInvocable must be false")
	}
	summary := SummaryFromSkill(skillItem)
	if summary.ModelInvocable() || summary.UserInvocable() {
		t.Fatal("summary must inherit invocable flags")
	}
}

func TestCodexSkillMetadata_DefaultsAreInvocable(t *testing.T) {
	meta := &CodexSkillMetadata{}
	if !meta.UserInvocableEnabled() {
		t.Fatal("default user invocable must be true")
	}
	if !meta.ImplicitInvocationAllowed() {
		t.Fatal("default implicit invocation must be allowed")
	}
	// agents/openai.yaml 的 policy 可关闭隐式调用。
	disabled := false
	meta.Policy = &CodexSkillPolicy{AllowImplicitInvocationValue: &disabled}
	if meta.ImplicitInvocationAllowed() {
		t.Fatal("policy allow_implicit_invocation=false must disable implicit invocation")
	}
}

func TestDiscoverCodexSkillLoadOutcome_StandardWarnings(t *testing.T) {
	dir := t.TempDir()
	writeStandardSkill(t, filepath.Join(dir, ".agents", "skills"), "wrong-dir", `---
name: another-name
description: name and directory differ
---

body
`)

	outcome := discoverCodexSkillLoadOutcome(dir, "", "", nil)
	if len(outcome.Skills) != 1 {
		t.Fatalf("skills = %d, want 1", len(outcome.Skills))
	}
	if len(outcome.Warnings) == 0 {
		t.Fatal("expected standard compliance warnings")
	}
	joined := outcome.Warnings[0].Message
	for _, warning := range outcome.Warnings {
		joined += "\n" + warning.Message
	}
	if !strings.Contains(joined, "does not match frontmatter name") {
		t.Fatalf("warnings = %v", outcome.Warnings)
	}
}

func TestBuildCatalogEntries_SkipsDisableModelInvocationAndAddsWhenToUse(t *testing.T) {
	disabled := true
	summaries := []*SkillSummary{
		{Name: "hidden", Description: "hidden skill", Codex: &CodexSkillMetadata{Name: "hidden", DisableModelInvocation: disabled, WhenToUse: "never"}},
		{Name: "visible", Description: "visible skill", Codex: &CodexSkillMetadata{Name: "visible", WhenToUse: "when editing docs"}},
	}
	entries := BuildCatalogEntries(summaries)
	if len(entries) != 1 || entries[0].Name != "visible" {
		t.Fatalf("entries = %#v", entries)
	}
	body, _ := RenderSkillCatalogWithOptions(entries, DefaultCatalogBudget(0), false)
	if !strings.Contains(body, "when: when editing docs") {
		t.Fatalf("catalog body does not carry when_to_use:\n%s", body)
	}
	if strings.Contains(body, "hidden") {
		t.Fatalf("disable-model-invocation skill leaked into catalog:\n%s", body)
	}
}

// Agent Skills 标准的用户级根 ~/.agents/skills 必须显式参与发现，
// 不依赖 cwd 是否位于 home 之下。
func TestDiscoverCodexCompatibleSkillRootSpecs_UserAgentsSkillsRoot(t *testing.T) {
	home := t.TempDir()
	userRoot := filepath.Join(home, ".agents", "skills")
	if err := os.MkdirAll(userRoot, 0o755); err != nil {
		t.Fatalf("mkdir user root: %v", err)
	}

	specs := discoverCodexCompatibleSkillRootSpecs("", "", home)
	found := false
	for _, spec := range specs {
		if filepath.Clean(spec.Path) == filepath.Clean(userRoot) {
			found = true
			if spec.Scope != CodexSkillScopeUser {
				t.Fatalf("scope = %q, want user", spec.Scope)
			}
		}
	}
	if !found {
		t.Fatalf("user root %s not discovered: %#v", userRoot, specs)
	}
}

// 同名遮蔽必须作为可见诊断上报，而不是静默歧义。
func TestDiscoverCodexSkillLoadOutcome_DuplicateNameWarning(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, ".agents", "skills")
	writeStandardSkill(t, root, "first", "---\nname: dup\ndescription: first\n---\n\nbody\n")
	writeStandardSkill(t, root, "second", "---\nname: dup\ndescription: second\n---\n\nbody\n")

	outcome := discoverCodexSkillLoadOutcome(dir, "", "", nil)
	if len(outcome.Skills) != 2 {
		t.Fatalf("skills = %d, want 2", len(outcome.Skills))
	}
	joined := ""
	for _, warning := range outcome.Warnings {
		joined += warning.Message + "\n"
	}
	if !strings.Contains(joined, "duplicate skill name \"dup\"") {
		t.Fatalf("duplicate warning missing: %v", outcome.Warnings)
	}
}
