package skill

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/errors"
	"gopkg.in/yaml.v3"
)

// ManifestParser Manifest 解析器
type ManifestParser struct {
	validateTriggers bool
	validateTools    bool
	promptLoadMode   CompanionPromptLoadMode
}

type CompanionPromptLoadMode string

const (
	CompanionPromptLoadEager CompanionPromptLoadMode = "eager"
	CompanionPromptLoadLazy  CompanionPromptLoadMode = "lazy"
)

// NewManifestParser 创建解析器
func NewManifestParser() *ManifestParser {
	return &ManifestParser{
		validateTriggers: true,
		validateTools:    true,
		promptLoadMode:   CompanionPromptLoadEager,
	}
}

// SetCompanionPromptLoadMode 设置 companion prompt 加载模式。
func (p *ManifestParser) SetCompanionPromptLoadMode(mode CompanionPromptLoadMode) {
	if p == nil {
		return
	}
	switch mode {
	case CompanionPromptLoadLazy:
		p.promptLoadMode = CompanionPromptLoadLazy
	default:
		p.promptLoadMode = CompanionPromptLoadEager
	}
}

// ParseFile 从文件解析 Manifest
func (p *ManifestParser) ParseFile(filePath string) (*Skill, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, errors.Wrap(errors.ErrConfigNotFound,
			fmt.Sprintf("failed to read manifest file: %s", filePath), err)
	}

	skill, err := p.ParseBytes(data)
	if err != nil {
		return nil, err
	}
	switch p.promptLoadMode {
	case CompanionPromptLoadLazy:
		if err := p.discoverCompanionPrompt(skill, filepath.Dir(filePath)); err != nil {
			return nil, err
		}
	default:
		if err := p.loadCompanionPrompt(skill, filepath.Dir(filePath)); err != nil {
			return nil, err
		}
	}
	skill.SetSource(filePath, filepath.Dir(filePath), SkillSourceLayerUnknown)
	return skill, nil
}

// ParseSummaryFile 从文件解析轻量 skill 摘要。
func (p *ManifestParser) ParseSummaryFile(filePath string) (*SkillSummary, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, errors.Wrap(errors.ErrConfigNotFound,
			fmt.Sprintf("failed to read manifest file: %s", filePath), err)
	}

	summary, err := p.ParseSummaryBytes(data)
	if err != nil {
		return nil, err
	}
	if err := p.discoverCompanionPromptSummary(summary, filepath.Dir(filePath)); err != nil {
		return nil, err
	}
	if summary.Source == nil {
		summary.Source = &SkillSource{}
	}
	summary.Source.Path = filePath
	summary.Source.Dir = filepath.Dir(filePath)
	if strings.TrimSpace(summary.Source.Layer) == "" {
		summary.Source.Layer = SkillSourceLayerUnknown
	}
	summary.Source.DiscoveryOnly = true
	return summary, nil
}

// ParseBytes 从字节解析 Manifest
func (p *ManifestParser) ParseBytes(data []byte) (*Skill, error) {
	var skill Skill
	if err := yaml.Unmarshal(data, &skill); err != nil {
		return nil, errors.Wrap(errors.ErrConfigInvalid,
			"failed to parse manifest", err)
	}

	// 验证 Skill
	if err := p.validate(&skill); err != nil {
		return nil, err
	}

	return &skill, nil
}

// ParseSummaryBytes 从字节解析轻量 manifest。
func (p *ManifestParser) ParseSummaryBytes(data []byte) (*SkillSummary, error) {
	type workflowStepSummaryManifest struct {
		ID        string                 `yaml:"id"`
		Name      string                 `yaml:"name"`
		Tool      string                 `yaml:"tool"`
		Args      map[string]interface{} `yaml:"args"`
		DependsOn []string               `yaml:"dependsOn"`
		Condition string                 `yaml:"condition"`
	}
	type workflowSummaryManifest struct {
		Steps []workflowStepSummaryManifest `yaml:"steps"`
	}
	type skillSummaryManifest struct {
		Name         string                   `yaml:"name"`
		Description  string                   `yaml:"description"`
		Version      string                   `yaml:"version"`
		Category     string                   `yaml:"category"`
		Capabilities []string                 `yaml:"capabilities"`
		Tags         []string                 `yaml:"tags"`
		Triggers     []Trigger                `yaml:"triggers"`
		Tools        []string                 `yaml:"tools"`
		Context      ContextConfig            `yaml:"context"`
		Permissions  []string                 `yaml:"permissions"`
		Workflow     *workflowSummaryManifest `yaml:"workflow"`
	}

	var manifest skillSummaryManifest
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		return nil, errors.Wrap(errors.ErrConfigInvalid,
			"failed to parse manifest summary", err)
	}

	summary := &SkillSummary{
		Name:         manifest.Name,
		Description:  manifest.Description,
		Version:      manifest.Version,
		Category:     manifest.Category,
		Capabilities: append([]string(nil), manifest.Capabilities...),
		Tags:         append([]string(nil), manifest.Tags...),
		Triggers:     cloneTriggers(manifest.Triggers),
		Tools:        append([]string(nil), manifest.Tools...),
		Context: ContextConfig{
			Files:       append([]string(nil), manifest.Context.Files...),
			Environment: append([]string(nil), manifest.Context.Environment...),
			Symbols:     append([]string(nil), manifest.Context.Symbols...),
		},
		Permissions: append([]string(nil), manifest.Permissions...),
	}
	if manifest.Workflow != nil && len(manifest.Workflow.Steps) > 0 {
		summary.WorkflowStepCount = len(manifest.Workflow.Steps)
		summary.WorkflowSteps = make([]WorkflowStepSummary, 0, len(manifest.Workflow.Steps))
		for _, step := range manifest.Workflow.Steps {
			summary.WorkflowSteps = append(summary.WorkflowSteps, WorkflowStepSummary{
				ID:        step.ID,
				Name:      step.Name,
				Tool:      step.Tool,
				Args:      cloneMapInterface(step.Args),
				DependsOn: append([]string(nil), step.DependsOn...),
				Condition: step.Condition,
			})
		}
	}

	if err := p.validate(summary.ToSkillStub()); err != nil {
		return nil, err
	}
	return summary, nil
}

// ParseDir 从目录解析所有 Manifest
func (p *ManifestParser) ParseDir(dirPath string) ([]*Skill, error) {
	var skills []*Skill

	err := filepath.Walk(dirPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() {
			// 跳过隐藏目录
			if info.Name()[0] == '.' {
				return filepath.SkipDir
			}
			return nil
		}

		// 只解析 .yaml 或 .yml 文件
		ext := filepath.Ext(path)
		if ext != ".yaml" && ext != ".yml" {
			return nil
		}

		// 解析文件
		skill, err := p.ParseFile(path)
		if err != nil {
			// 记录错误但继续解析其他文件
			fmt.Printf("Warning: failed to parse %s: %v\n", path, err)
			return nil
		}

		skills = append(skills, skill)
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to scan directory: %w", err)
	}

	return skills, nil
}

// ParseSummaryDir 从目录解析所有轻量 Manifest。
func (p *ManifestParser) ParseSummaryDir(dirPath string) ([]*SkillSummary, error) {
	var summaries []*SkillSummary

	err := filepath.Walk(dirPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() {
			if info.Name()[0] == '.' {
				return filepath.SkipDir
			}
			return nil
		}

		ext := filepath.Ext(path)
		if ext != ".yaml" && ext != ".yml" {
			return nil
		}

		summary, err := p.ParseSummaryFile(path)
		if err != nil {
			fmt.Printf("Warning: failed to parse summary %s: %v\n", path, err)
			return nil
		}

		summaries = append(summaries, summary)
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to scan directory: %w", err)
	}

	return summaries, nil
}

// validate 验证 Skill
func (p *ManifestParser) validate(s *Skill) error {
	// 基本字段验证
	if s.Name == "" {
		return errors.New(errors.ErrValidationFailed, "skill name is required")
	}

	if s.Version == "" {
		// 设置默认版本
		s.Version = "1.0.0"
	}

	// 验证触发规则
	if len(s.Triggers) == 0 {
		return errors.New(errors.ErrValidationFailed, "at least one trigger is required")
	}

	if p.validateTriggers {
		for i, trigger := range s.Triggers {
			if err := p.validateTrigger(&trigger); err != nil {
				return errors.Wrap(errors.ErrInvalidManifest,
					fmt.Sprintf("trigger[%d] is invalid", i), err)
			}
		}
	}

	// 验证工作流
	if s.Workflow != nil {
		for i, step := range s.Workflow.Steps {
			if step.ID == "" {
				return errors.New(errors.ErrValidationFailed,
					fmt.Sprintf("workflow step[%d] has empty ID", i))
			}
			if step.Tool == "" {
				return errors.New(errors.ErrValidationFailed,
					fmt.Sprintf("workflow step[%d] has empty tool", i))
			}
		}
	}

	return nil
}

// validateTrigger 验证触发规则
func (p *ManifestParser) validateTrigger(t *Trigger) error {
	switch t.Type {
	case "keyword":
		if len(t.Values) == 0 {
			return errors.New(errors.ErrValidationFailed,
				"keyword trigger requires at least one value")
		}
	case "pattern":
		if len(t.Values) == 0 {
			return errors.New(errors.ErrValidationFailed,
				"pattern trigger requires at least one value")
		}
		// 验证正则表达式
		for _, pattern := range t.Values {
			if _, err := compilePattern(pattern); err != nil {
				return errors.Wrap(errors.ErrValidationFailed,
					fmt.Sprintf("invalid pattern: %s", pattern), err)
			}
		}
	case "embedding":
		// embedding trigger 不需要 values
	default:
		return errors.New(errors.ErrValidationFailed,
			fmt.Sprintf("unknown trigger type: %s", t.Type))
	}

	return nil
}

// compilePattern 编译正则表达式（用于验证）
func compilePattern(pattern string) (interface{}, error) {
	return regexp.Compile(pattern)
}

func (p *ManifestParser) loadCompanionPrompt(skill *Skill, skillDir string) error {
	if skill == nil || strings.TrimSpace(skillDir) == "" {
		return nil
	}

	promptPath := filepath.Join(skillDir, "prompt.md")
	data, err := os.ReadFile(promptPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return errors.Wrap(errors.ErrConfigNotFound,
			fmt.Sprintf("failed to read companion prompt file: %s", promptPath), err)
	}

	systemPrompt, userPrompt := parsePromptMarkdown(string(data))
	if strings.TrimSpace(skill.SystemPrompt) == "" && systemPrompt != "" {
		skill.SystemPrompt = systemPrompt
	}
	if strings.TrimSpace(skill.UserPrompt) == "" && userPrompt != "" {
		skill.UserPrompt = userPrompt
	}
	skill.SetPromptSource(promptPath)

	return nil
}

func (p *ManifestParser) discoverCompanionPromptSummary(summary *SkillSummary, skillDir string) error {
	if summary == nil || strings.TrimSpace(skillDir) == "" {
		return nil
	}

	promptPath := discoverPromptPath(skillDir)
	if _, err := os.Stat(promptPath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return errors.Wrap(errors.ErrConfigNotFound,
			fmt.Sprintf("failed to inspect companion prompt file: %s", promptPath), err)
	}
	if summary.Source == nil {
		summary.Source = &SkillSource{}
	}
	summary.Source.PromptPath = promptPath
	return nil
}

func (p *ManifestParser) discoverCompanionPrompt(skill *Skill, skillDir string) error {
	if skill == nil || strings.TrimSpace(skillDir) == "" {
		return nil
	}

	promptPath := discoverPromptPath(skillDir)
	if _, err := os.Stat(promptPath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return errors.Wrap(errors.ErrConfigNotFound,
			fmt.Sprintf("failed to inspect companion prompt file: %s", promptPath), err)
	}
	skill.SetPromptSource(promptPath)
	return nil
}

func resolveSkillPrompts(skill *Skill) (string, string, error) {
	if skill == nil {
		return "", "", nil
	}

	systemPrompt := strings.TrimSpace(skill.SystemPrompt)
	userPrompt := strings.TrimSpace(skill.UserPrompt)
	promptPath := ""
	if skill.Source != nil {
		promptPath = strings.TrimSpace(skill.Source.PromptPath)
	}
	if promptPath == "" || (systemPrompt != "" && userPrompt != "") {
		return systemPrompt, userPrompt, nil
	}

	data, err := os.ReadFile(promptPath)
	if err != nil {
		if os.IsNotExist(err) {
			return systemPrompt, userPrompt, nil
		}
		return "", "", errors.Wrap(errors.ErrConfigNotFound,
			fmt.Sprintf("failed to read companion prompt file: %s", promptPath), err)
	}

	lazySystem, lazyUser := parsePromptMarkdown(string(data))
	if systemPrompt == "" {
		systemPrompt = lazySystem
	}
	if userPrompt == "" {
		userPrompt = lazyUser
	}
	return systemPrompt, userPrompt, nil
}

func discoverPromptPath(skillDir string) string {
	return filepath.Join(skillDir, "prompt.md")
}

func parsePromptMarkdown(content string) (string, string) {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return "", ""
	}

	type promptSection struct {
		name  string
		lines []string
	}

	current := "system"
	sections := map[string]*promptSection{
		"system": {name: "system", lines: []string{}},
		"user":   {name: "user", lines: []string{}},
	}
	foundExplicitSection := false

	for _, line := range strings.Split(content, "\n") {
		if section, ok := promptMarkdownHeading(line); ok {
			current = section
			foundExplicitSection = true
			continue
		}
		sections[current].lines = append(sections[current].lines, line)
	}

	if !foundExplicitSection {
		return strings.TrimSpace(content), ""
	}

	return strings.TrimSpace(strings.Join(sections["system"].lines, "\n")),
		strings.TrimSpace(strings.Join(sections["user"].lines, "\n"))
}

func promptMarkdownHeading(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return "", false
	}

	normalized := trimmed
	if strings.HasPrefix(normalized, "#") {
		normalized = strings.TrimSpace(strings.TrimLeft(normalized, "#"))
	}
	normalized = strings.TrimSuffix(normalized, ":")
	normalized = strings.ToLower(strings.TrimSpace(normalized))

	switch normalized {
	case "system":
		return "system", true
	case "user":
		return "user", true
	default:
		return "", false
	}
}

// SaveFile 保存 Skill 到文件
func (p *ManifestParser) SaveFile(skill *Skill, filePath string) error {
	// 设置目录
	skillDir := filepath.Dir(filePath)
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	persistedSkill := skillForPersistence(skill)
	data, err := yaml.Marshal(persistedSkill)
	if err != nil {
		return fmt.Errorf("failed to marshal skill: %w", err)
	}

	if err := os.WriteFile(filePath, data, 0644); err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}
	if err := writeCompanionPromptFile(skill, skillDir); err != nil {
		return err
	}

	return nil
}

// Unmarshal 通用解析接口
func Unmarshal(data []byte) (*Skill, error) {
	parser := NewManifestParser()
	return parser.ParseBytes(data)
}

// Marshal 通用序列化接口
func Marshal(skill *Skill) ([]byte, error) {
	return yaml.Marshal(skill)
}

func skillForPersistence(skill *Skill) *Skill {
	if skill == nil {
		return nil
	}

	clone := *skill
	if strings.TrimSpace(skill.SystemPrompt) != "" || strings.TrimSpace(skill.UserPrompt) != "" {
		clone.SystemPrompt = ""
		clone.UserPrompt = ""
	}
	clone.Handler = nil
	clone.Source = nil
	return &clone
}

func writeCompanionPromptFile(skill *Skill, skillDir string) error {
	if skill == nil || strings.TrimSpace(skillDir) == "" {
		return nil
	}

	promptPath := filepath.Join(skillDir, "prompt.md")
	content := buildCompanionPromptMarkdown(skill.SystemPrompt, skill.UserPrompt)
	if content == "" {
		if err := os.Remove(promptPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("failed to remove prompt companion: %w", err)
		}
		skill.SetPromptSource("")
		return nil
	}

	if err := os.WriteFile(promptPath, []byte(content), 0644); err != nil {
		return fmt.Errorf("failed to write prompt companion: %w", err)
	}
	skill.SetPromptSource(promptPath)
	return nil
}

func buildCompanionPromptMarkdown(systemPrompt, userPrompt string) string {
	systemPrompt = strings.TrimSpace(systemPrompt)
	userPrompt = strings.TrimSpace(userPrompt)

	switch {
	case systemPrompt == "" && userPrompt == "":
		return ""
	case systemPrompt != "" && userPrompt == "":
		return systemPrompt + "\n"
	case systemPrompt == "" && userPrompt != "":
		return "# User\n" + userPrompt + "\n"
	default:
		return "# System\n" + systemPrompt + "\n\n# User\n" + userPrompt + "\n"
	}
}
