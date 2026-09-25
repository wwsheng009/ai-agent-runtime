package skill

import (
	"path/filepath"
	"strings"
)

// Skill source formats.
const (
	SkillSourceFormatUnknown = ""
	SkillSourceFormatLegacy  = "legacy"
	SkillSourceFormatCodex   = "codex"
)

const (
	CodexSkillScopeUnknown = "unknown"
	CodexSkillScopeRepo    = "repo"
	CodexSkillScopeUser    = "user"
	CodexSkillScopeSystem  = "system"
	CodexSkillScopeAdmin   = "admin"
)

// CodexSkillMetadata describes a Codex-style skill loaded from SKILL.md and optional
// agents/openai.yaml metadata.
type CodexSkillMetadata struct {
	Name             string                  `json:"name"`
	Description      string                  `json:"description"`
	ShortDescription string                  `json:"short_description,omitempty"`
	Interface        *CodexSkillInterface    `json:"interface,omitempty"`
	Dependencies     *CodexSkillDependencies `json:"dependencies,omitempty"`
	Policy           *CodexSkillPolicy       `json:"policy,omitempty"`
	PathToSkillsMD   string                  `json:"path_to_skills_md"`
	MetadataPath     string                  `json:"metadata_path,omitempty"`
	Scope            string                  `json:"scope,omitempty"`
	Enabled          bool                    `json:"enabled"`
	Body             string                  `json:"body,omitempty"`

	// Agent Skills 标准可选字段（frontmatter 直读，缺失保持零值）。
	License                string   `json:"license,omitempty"`
	Compatibility          string   `json:"compatibility,omitempty"`
	Author                 string   `json:"author,omitempty"`
	StandardVersion        string   `json:"version,omitempty"`
	AllowedTools           []string `json:"allowed_tools,omitempty"`
	DisallowedTools        []string `json:"disallowed_tools,omitempty"`
	ArgumentHint           string   `json:"argument_hint,omitempty"`
	WhenToUse              string   `json:"when_to_use,omitempty"`
	DisableModelInvocation bool     `json:"disable_model_invocation,omitempty"`
	// UserInvocable 为 nil 表示未声明（默认允许），显式 false 时不得出现在
	// /skills 菜单与函数暴露面。
	UserInvocable *bool                `json:"user_invocable,omitempty"`
	Arguments     []CodexSkillArgument `json:"arguments,omitempty"`
	Model         string               `json:"model,omitempty"`
	Effort        string               `json:"effort,omitempty"`
}

// CodexSkillArgument 描述 frontmatter `arguments` 中声明的命名参数。
type CodexSkillArgument struct {
	Name        string `json:"name" yaml:"name"`
	Description string `json:"description,omitempty" yaml:"description"`
	Required    bool   `json:"required,omitempty" yaml:"required"`
	Default     string `json:"default,omitempty" yaml:"default"`
}

// UserInvocableEnabled 报告技能是否允许用户显式调用（默认允许）。
func (m *CodexSkillMetadata) UserInvocableEnabled() bool {
	if m == nil || m.UserInvocable == nil {
		return true
	}
	return *m.UserInvocable
}

// ImplicitInvocationAllowed 报告技能是否允许被模型隐式选中：
// disable-model-invocation 显式关闭，或 agents/openai.yaml 的
// policy.allow_implicit_invocation=false 关闭；默认允许。
func (m *CodexSkillMetadata) ImplicitInvocationAllowed() bool {
	if m == nil {
		return true
	}
	if m.DisableModelInvocation {
		return false
	}
	if m.Policy != nil && m.Policy.AllowImplicitInvocationValue != nil {
		return *m.Policy.AllowImplicitInvocationValue
	}
	return true
}

// CodexSkillInterface captures UI-facing metadata from agents/openai.yaml.
type CodexSkillInterface struct {
	DisplayName      string `json:"display_name,omitempty"`
	ShortDescription string `json:"short_description,omitempty"`
	IconSmall        string `json:"icon_small,omitempty"`
	IconLarge        string `json:"icon_large,omitempty"`
	BrandColor       string `json:"brand_color,omitempty"`
	DefaultPrompt    string `json:"default_prompt,omitempty"`
}

// CodexSkillDependencies captures optional tool dependencies.
type CodexSkillDependencies struct {
	Tools []CodexSkillToolDependency `json:"tools,omitempty"`
}

// CodexSkillToolDependency is the fail-open dependency tool representation.
type CodexSkillToolDependency struct {
	Type        string `json:"type,omitempty"`
	Value       string `json:"value,omitempty"`
	Description string `json:"description,omitempty"`
	Transport   string `json:"transport,omitempty"`
	Command     string `json:"command,omitempty"`
	URL         string `json:"url,omitempty"`
}

// CodexSkillPolicy captures optional invocation policy metadata.
type CodexSkillPolicy struct {
	AllowImplicitInvocationValue *bool    `json:"allow_implicit_invocation,omitempty"`
	Products                     []string `json:"products,omitempty"`
}

// CodexSkillError captures a load or parse failure for a specific path.
type CodexSkillError struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// CodexSkillLoadOutcome captures the result of a Codex-style discovery run.
type CodexSkillLoadOutcome struct {
	Skills []*CodexSkillMetadata `json:"skills,omitempty"`
	Errors []CodexSkillError     `json:"errors,omitempty"`
	// Warnings 是"仍可加载、但不符合 Agent Skills 标准或存在歧义"的诊断，
	// 例如 name 与目录名不一致、name 含非小写连字符字符集。
	Warnings []CodexSkillError `json:"warnings,omitempty"`
}

// Clone returns a deep copy of the metadata.
func (m *CodexSkillMetadata) Clone() *CodexSkillMetadata {
	if m == nil {
		return nil
	}
	cloned := *m
	if m.Interface != nil {
		iface := *m.Interface
		cloned.Interface = &iface
	}
	if m.Dependencies != nil {
		deps := *m.Dependencies
		deps.Tools = append([]CodexSkillToolDependency(nil), m.Dependencies.Tools...)
		cloned.Dependencies = &deps
	}
	if m.Policy != nil {
		policy := *m.Policy
		policy.Products = append([]string(nil), m.Policy.Products...)
		cloned.Policy = &policy
	}
	cloned.AllowedTools = append([]string(nil), m.AllowedTools...)
	cloned.DisallowedTools = append([]string(nil), m.DisallowedTools...)
	cloned.Arguments = append([]CodexSkillArgument(nil), m.Arguments...)
	if m.UserInvocable != nil {
		value := *m.UserInvocable
		cloned.UserInvocable = &value
	}
	return &cloned
}

// CloneWithoutBody returns a deep copy without body text.
func (m *CodexSkillMetadata) CloneWithoutBody() *CodexSkillMetadata {
	cloned := m.Clone()
	if cloned != nil {
		cloned.Body = ""
	}
	return cloned
}

// Normalize collapses whitespace on display-oriented fields and validates defaults.
func (m *CodexSkillMetadata) Normalize() {
	if m == nil {
		return
	}
	m.Name = collapseWhitespace(m.Name)
	m.Description = collapseWhitespace(m.Description)
	m.ShortDescription = collapseWhitespace(m.ShortDescription)
	if m.Interface != nil {
		m.Interface.DisplayName = collapseWhitespace(m.Interface.DisplayName)
		m.Interface.ShortDescription = collapseWhitespace(m.Interface.ShortDescription)
		m.Interface.BrandColor = collapseWhitespace(m.Interface.BrandColor)
		m.Interface.DefaultPrompt = collapseWhitespace(m.Interface.DefaultPrompt)
	}
	m.Scope = collapseWhitespace(m.Scope)
	m.License = collapseWhitespace(m.License)
	m.Compatibility = collapseWhitespace(m.Compatibility)
	m.Author = collapseWhitespace(m.Author)
	m.StandardVersion = collapseWhitespace(m.StandardVersion)
	m.ArgumentHint = collapseWhitespace(m.ArgumentHint)
	m.WhenToUse = collapseWhitespace(m.WhenToUse)
	m.Model = collapseWhitespace(m.Model)
	m.Effort = collapseWhitespace(m.Effort)
	m.AllowedTools = normalizeStandardToolList(m.AllowedTools)
	m.DisallowedTools = normalizeStandardToolList(m.DisallowedTools)
	if path := strings.TrimSpace(m.PathToSkillsMD); path != "" {
		m.PathToSkillsMD = filepath.Clean(path)
	}
	if path := strings.TrimSpace(m.MetadataPath); path != "" {
		m.MetadataPath = filepath.Clean(path)
	}
	m.Enabled = true
}

// ToSkill converts the codex metadata into the legacy runtime skill model.
func (m *CodexSkillMetadata) ToSkill(loadBody bool) *Skill {
	if m == nil {
		return nil
	}

	clone := m.Clone()
	if clone == nil {
		return nil
	}
	if !loadBody {
		clone.Body = ""
	}

	skill := &Skill{
		Name:             clone.Name,
		Description:      clone.Description,
		ShortDescription: clone.ShortDescription,
		Body:             clone.Body,
		Version:          "1.0.0",
		Tools:            nil,
		Context:          ContextConfig{},
		Permissions:      nil,
		Codex:            clone.CloneWithoutBody(),
	}
	if skill.Codex != nil {
		skill.Codex.Enabled = clone.Enabled
	}
	if loadBody && strings.TrimSpace(clone.Body) != "" {
		skill.SystemPrompt = clone.Body
	} else if clone.Interface != nil && strings.TrimSpace(clone.Interface.DefaultPrompt) != "" {
		skill.SystemPrompt = clone.Interface.DefaultPrompt
	}

	source := &SkillSource{
		Path:          clone.PathToSkillsMD,
		Dir:           filepath.Dir(clone.PathToSkillsMD),
		Layer:         clone.Scope,
		Format:        SkillSourceFormatCodex,
		MetadataPath:  clone.MetadataPath,
		DiscoveryOnly: !loadBody,
	}
	skill.Source = source
	return skill
}

// ToSkillSummary converts the codex metadata into a legacy discovery summary.
func (m *CodexSkillMetadata) ToSkillSummary() *SkillSummary {
	if m == nil {
		return nil
	}

	skill := m.ToSkill(false)
	if skill == nil {
		return nil
	}
	return SummaryFromSkill(skill)
}

// AllowImplicitInvocation returns the effective invocation policy.
func (p *CodexSkillPolicy) AllowImplicitInvocation() bool {
	if p == nil || p.AllowImplicitInvocationValue == nil {
		return true
	}
	return *p.AllowImplicitInvocationValue
}

// LegacyToolNames returns the non-empty dependency values for compatibility layers.
func (d *CodexSkillDependencies) LegacyToolNames() []string {
	if d == nil || len(d.Tools) == 0 {
		return nil
	}
	names := make([]string, 0, len(d.Tools))
	for _, tool := range d.Tools {
		if strings.TrimSpace(tool.Value) == "" {
			continue
		}
		names = append(names, strings.TrimSpace(tool.Value))
	}
	return names
}

func collapseWhitespace(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return strings.Join(strings.Fields(value), " ")
}

func isCodexSkillPath(path string) bool {
	return strings.EqualFold(filepath.Base(strings.TrimSpace(path)), "SKILL.md")
}

// normalizeStandardToolList 去空白、去重并保持声明顺序。
func normalizeStandardToolList(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func isCodexMetadataPath(path string) bool {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "." || path == "" {
		return false
	}
	if !strings.EqualFold(filepath.Base(path), "openai.yaml") {
		return false
	}
	return strings.EqualFold(filepath.Base(filepath.Dir(path)), "agents")
}

func codexMetadataPathForSkillPath(skillPath string) string {
	skillPath = filepath.Clean(strings.TrimSpace(skillPath))
	if skillPath == "." || skillPath == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(skillPath), "agents", "openai.yaml")
}

func codexSkillPathForMetadataPath(metadataPath string) string {
	metadataPath = filepath.Clean(strings.TrimSpace(metadataPath))
	if metadataPath == "." || metadataPath == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(filepath.Dir(metadataPath)), "SKILL.md")
}

func skillManifestFormatForPath(path string) string {
	switch {
	case isCodexSkillPath(path):
		return SkillSourceFormatCodex
	case strings.TrimSpace(path) != "":
		return SkillSourceFormatLegacy
	default:
		return SkillSourceFormatUnknown
	}
}

func skillSourceFormat(skill *Skill) string {
	if skill == nil || skill.Source == nil {
		return skillManifestFormatForPath("")
	}
	if format := strings.TrimSpace(skill.Source.Format); format != "" {
		return format
	}
	return skillManifestFormatForPath(skill.Source.Path)
}

// isCodexSummarySource 报告摘要是否来自 Codex 兼容技能。
func isCodexSummarySource(summary *SkillSummary) bool {
	if summary == nil {
		return false
	}
	if summary.Codex != nil {
		return true
	}
	return skillSummarySourceFormat(summary) == SkillSourceFormatCodex
}

func skillSummarySourceFormat(summary *SkillSummary) string {
	if summary == nil || summary.Source == nil {
		return skillManifestFormatForPath("")
	}
	if format := strings.TrimSpace(summary.Source.Format); format != "" {
		return format
	}
	return skillManifestFormatForPath(summary.Source.Path)
}

func skillIdentityPath(skill *Skill) string {
	if skill == nil {
		return ""
	}
	if skill.Source != nil {
		if path := strings.TrimSpace(skill.Source.Path); path != "" {
			return filepath.Clean(path)
		}
	}
	if path := strings.TrimSpace(skill.Name); path != "" {
		return path
	}
	return ""
}

func skillCacheKey(skill *Skill) string {
	if skill == nil {
		return ""
	}
	if isCodexSkillSource(skill) {
		if key := skillIdentityPath(skill); key != "" {
			return key
		}
	}
	if key := strings.TrimSpace(skill.Name); key != "" {
		return key
	}
	return skillIdentityPath(skill)
}

func skillCacheKeyFromPath(path string) string {
	path = filepath.Clean(strings.TrimSpace(path))
	if path != "" && path != "." {
		return path
	}
	return ""
}

func skillHydrationCacheKey(skill *Skill) string {
	if skill == nil {
		return ""
	}
	if isCodexSkillSource(skill) {
		if path := skillCacheKeyFromPath(skillIdentityPath(skill)); path != "" {
			return path
		}
	}
	if key := strings.TrimSpace(skill.Name); key != "" {
		return key
	}
	return skillIdentityPath(skill)
}

func skillSummaryIdentityPath(summary *SkillSummary) string {
	if summary == nil {
		return ""
	}
	if summary.Source != nil {
		if path := strings.TrimSpace(summary.Source.Path); path != "" {
			return filepath.Clean(path)
		}
	}
	if name := strings.TrimSpace(summary.Name); name != "" {
		return name
	}
	return ""
}
