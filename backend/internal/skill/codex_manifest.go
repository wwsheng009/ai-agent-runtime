package skill

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/errors"
	"gopkg.in/yaml.v3"
)

type codexSkillFrontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	License     string `yaml:"license"`
	// Compatibility 是标准可选字段（≤500 字符），用于描述环境/依赖兼容性。
	Compatibility string `yaml:"compatibility"`
	Metadata      struct {
		ShortDescription string `yaml:"short-description"`
		Author           string `yaml:"author"`
		Version          string `yaml:"version"`
	} `yaml:"metadata"`
	// AllowedTools / DisallowedTools 同时接受 YAML 列表与空格分隔字符串
	// （Claude Code 风格的 `allowed-tools: Bash(git:*) Read`）。
	AllowedTools    codexStringList `yaml:"allowed-tools"`
	DisallowedTools codexStringList `yaml:"disallowed-tools"`
	ArgumentHint    string          `yaml:"argument-hint"`
	// WhenToUse 既接受 snake_case 也接受 kebab-case（不同实现的写法差异）。
	WhenToUseSnake         string               `yaml:"when_to_use"`
	WhenToUseKebab         string               `yaml:"when-to-use"`
	DisableModelInvocation *bool                `yaml:"disable-model-invocation"`
	UserInvocable          *bool                `yaml:"user-invocable"`
	Arguments              []CodexSkillArgument `yaml:"arguments"`
	Model                  string               `yaml:"model"`
	Effort                 string               `yaml:"effort"`
}

// codexStringList 兼容标量（空格/逗号分隔）与序列两种 YAML 形态。
type codexStringList []string

func (l *codexStringList) UnmarshalYAML(value *yaml.Node) error {
	if value == nil {
		return nil
	}
	switch value.Kind {
	case yaml.ScalarNode:
		for _, part := range strings.FieldsFunc(value.Value, func(r rune) bool {
			return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
		}) {
			if trimmed := strings.TrimSpace(part); trimmed != "" {
				*l = append(*l, trimmed)
			}
		}
	case yaml.SequenceNode:
		for _, item := range value.Content {
			if item == nil {
				continue
			}
			if trimmed := strings.TrimSpace(item.Value); trimmed != "" {
				*l = append(*l, trimmed)
			}
		}
	}
	return nil
}

type codexOpenAIMetadataFile struct {
	Interface    *codexOpenAIInterface    `yaml:"interface"`
	Dependencies *codexOpenAIDependencies `yaml:"dependencies"`
	Policy       *codexOpenAIPolicy       `yaml:"policy"`
}

type codexOpenAIInterface struct {
	DisplayName      string `yaml:"display_name"`
	ShortDescription string `yaml:"short_description"`
	IconSmall        string `yaml:"icon_small"`
	IconLarge        string `yaml:"icon_large"`
	BrandColor       string `yaml:"brand_color"`
	DefaultPrompt    string `yaml:"default_prompt"`
}

type codexOpenAIDependencies struct {
	Tools []codexOpenAIToolDependency `yaml:"tools"`
}

type codexOpenAIToolDependency struct {
	Type        string `yaml:"type"`
	Value       string `yaml:"value"`
	Description string `yaml:"description"`
	Transport   string `yaml:"transport"`
	Command     string `yaml:"command"`
	URL         string `yaml:"url"`
}

type codexOpenAIPolicy struct {
	AllowImplicitInvocation *bool    `yaml:"allow_implicit_invocation"`
	Products                []string `yaml:"products"`
}

const (
	codexFrontmatterMaxNameLen        = 64
	codexFrontmatterMaxDescriptionLen = 1024
)

func (p *ManifestParser) parseCodexFile(filePath string, loadBody bool) (*Skill, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, errors.Wrap(errors.ErrConfigNotFound,
			fmt.Sprintf("failed to read skill file: %s", filePath), err)
	}

	metadata, err := parseCodexSkillMetadata(filePath, data, loadBody)
	if err != nil {
		return nil, err
	}

	skill := metadata.ToSkill(loadBody)
	if skill == nil {
		return nil, errors.New(errors.ErrConfigInvalid, "failed to construct codex skill")
	}
	return skill, nil
}

func (p *ManifestParser) parseCodexSummaryFile(filePath string) (*SkillSummary, error) {
	skill, err := p.parseCodexFile(filePath, false)
	if err != nil {
		return nil, err
	}
	return SummaryFromSkill(skill), nil
}

func parseCodexSkillMetadata(filePath string, data []byte, loadBody bool) (*CodexSkillMetadata, error) {
	frontmatterBytes, bodyBytes, err := splitCodexFrontmatter(data)
	if err != nil {
		return nil, errors.Wrap(errors.ErrConfigInvalid,
			fmt.Sprintf("failed to parse codex skill frontmatter: %s", filePath), err)
	}

	var frontmatter codexSkillFrontmatter
	if err := yaml.Unmarshal(frontmatterBytes, &frontmatter); err != nil {
		return nil, errors.Wrap(errors.ErrConfigInvalid,
			fmt.Sprintf("failed to unmarshal codex frontmatter: %s", filePath), err)
	}

	name := collapseWhitespace(frontmatter.Name)
	if name == "" {
		name = collapseWhitespace(filepath.Base(filepath.Dir(filePath)))
	}
	if name == "" {
		return nil, errors.New(errors.ErrValidationFailed, "codex skill name is required")
	}
	if len(name) > codexFrontmatterMaxNameLen {
		return nil, errors.New(errors.ErrValidationFailed,
			fmt.Sprintf("codex skill name exceeds %d characters", codexFrontmatterMaxNameLen))
	}

	description := collapseWhitespace(frontmatter.Description)
	if description == "" {
		return nil, errors.New(errors.ErrValidationFailed, "codex skill description is required")
	}
	if len(description) > codexFrontmatterMaxDescriptionLen {
		return nil, errors.New(errors.ErrValidationFailed,
			fmt.Sprintf("codex skill description exceeds %d characters", codexFrontmatterMaxDescriptionLen))
	}

	shortDescription := collapseWhitespace(frontmatter.Metadata.ShortDescription)
	if len(shortDescription) > codexFrontmatterMaxDescriptionLen {
		return nil, errors.New(errors.ErrValidationFailed,
			fmt.Sprintf("codex skill short description exceeds %d characters", codexFrontmatterMaxDescriptionLen))
	}

	whenToUse := strings.TrimSpace(frontmatter.WhenToUseSnake)
	if whenToUse == "" {
		whenToUse = strings.TrimSpace(frontmatter.WhenToUseKebab)
	}

	skill := &CodexSkillMetadata{
		Name:                   name,
		Description:            description,
		ShortDescription:       shortDescription,
		PathToSkillsMD:         filepath.Clean(filePath),
		MetadataPath:           codexMetadataPathForSkillPath(filePath),
		Enabled:                true,
		License:                frontmatter.License,
		Compatibility:          frontmatter.Compatibility,
		Author:                 frontmatter.Metadata.Author,
		StandardVersion:        frontmatter.Metadata.Version,
		AllowedTools:           append([]string(nil), frontmatter.AllowedTools...),
		DisallowedTools:        append([]string(nil), frontmatter.DisallowedTools...),
		ArgumentHint:           frontmatter.ArgumentHint,
		WhenToUse:              whenToUse,
		DisableModelInvocation: frontmatter.DisableModelInvocation != nil && *frontmatter.DisableModelInvocation,
		UserInvocable:          cloneOptionalBool(frontmatter.UserInvocable),
		Arguments:              normalizeCodexArguments(frontmatter.Arguments),
		Model:                  frontmatter.Model,
		Effort:                 frontmatter.Effort,
	}
	if loadBody {
		skill.Body = string(bodyBytes)
	}
	if meta := parseCodexOpenAIMetadata(skill.MetadataPath); meta != nil {
		skill.Interface = meta.Interface
		skill.Dependencies = meta.Dependencies
		skill.Policy = meta.Policy
	}
	skill.Normalize()
	return skill, nil
}

// normalizeCodexArguments 清理命名参数声明：去空白、按名去重、保持声明顺序，
// 并丢弃缺名的条目。
func normalizeCodexArguments(values []CodexSkillArgument) []CodexSkillArgument {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]CodexSkillArgument, 0, len(values))
	for _, item := range values {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, CodexSkillArgument{
			Name:        name,
			Description: collapseWhitespace(item.Description),
			Required:    item.Required,
			Default:     item.Default,
		})
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// validateCodexSkillName 按 Agent Skills 标准校验 name：
// 小写字母/数字/连字符，不得以连字符开头或结尾，不得出现连续连字符。
// 返回空串表示合法；否则返回可直接展示的诊断文案。
func validateCodexSkillName(name string) string {
	name = collapseWhitespace(name)
	if name == "" {
		return "skill name is empty"
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
		default:
			return fmt.Sprintf("skill name %q should only contain lowercase letters, digits and hyphens (Agent Skills standard)", name)
		}
	}
	if strings.HasPrefix(name, "-") || strings.HasSuffix(name, "-") {
		return fmt.Sprintf("skill name %q must not start or end with a hyphen", name)
	}
	if strings.Contains(name, "--") {
		return fmt.Sprintf("skill name %q must not contain consecutive hyphens", name)
	}
	return ""
}

// codexSkillStandardWarnings 返回该技能的"可加载但不符合标准"诊断列表。
func codexSkillStandardWarnings(meta *CodexSkillMetadata, skillPath string) []string {
	if meta == nil {
		return nil
	}
	warnings := make([]string, 0, 2)
	if message := validateCodexSkillName(meta.Name); message != "" {
		warnings = append(warnings, message)
	}
	dirName := filepath.Base(filepath.Dir(filepath.Clean(skillPath)))
	if dirName != "" && dirName != "." && !strings.EqualFold(dirName, meta.Name) {
		warnings = append(warnings, fmt.Sprintf(
			"skill directory %q does not match frontmatter name %q (Agent Skills standard requires them to match)",
			dirName, meta.Name))
	}
	if len(warnings) == 0 {
		return nil
	}
	return warnings
}

func splitCodexFrontmatter(data []byte) ([]byte, []byte, error) {
	content := strings.ReplaceAll(string(data), "\r\n", "\n")
	lines := strings.Split(content, "\n")

	start := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if strings.TrimSpace(line) != "---" {
			return nil, nil, errors.New(errors.ErrConfigInvalid, "missing YAML frontmatter")
		}
		start = i
		break
	}
	if start < 0 {
		return nil, nil, errors.New(errors.ErrConfigInvalid, "missing YAML frontmatter")
	}

	end := -1
	for i := start + 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return nil, nil, errors.New(errors.ErrConfigInvalid, "missing YAML frontmatter terminator")
	}

	frontmatter := strings.Join(lines[start+1:end], "\n")
	body := ""
	if end+1 < len(lines) {
		body = strings.Join(lines[end+1:], "\n")
	}
	return []byte(frontmatter), []byte(body), nil
}

func parseCodexOpenAIMetadata(metadataPath string) *CodexSkillMetadata {
	metadataPath = filepath.Clean(strings.TrimSpace(metadataPath))
	if metadataPath == "" || metadataPath == "." {
		return nil
	}

	info, err := os.Stat(metadataPath)
	if err != nil || info.IsDir() {
		return nil
	}

	data, err := os.ReadFile(metadataPath)
	if err != nil {
		return nil
	}

	var raw codexOpenAIMetadataFile
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil
	}

	result := &CodexSkillMetadata{}
	if raw.Interface != nil {
		iface := &CodexSkillInterface{}
		if display := collapseWhitespace(raw.Interface.DisplayName); display != "" {
			iface.DisplayName = display
		}
		if short := collapseWhitespace(raw.Interface.ShortDescription); short != "" {
			iface.ShortDescription = short
		}
		if icon := sanitizeCodexRelativeAssetPath(raw.Interface.IconSmall); icon != "" {
			iface.IconSmall = icon
		}
		if icon := sanitizeCodexRelativeAssetPath(raw.Interface.IconLarge); icon != "" {
			iface.IconLarge = icon
		}
		if color := sanitizeCodexBrandColor(raw.Interface.BrandColor); color != "" {
			iface.BrandColor = color
		}
		if prompt := collapseWhitespace(raw.Interface.DefaultPrompt); prompt != "" {
			iface.DefaultPrompt = prompt
		}
		if iface.DisplayName != "" || iface.ShortDescription != "" || iface.IconSmall != "" ||
			iface.IconLarge != "" || iface.BrandColor != "" || iface.DefaultPrompt != "" {
			result.Interface = iface
		}
	}
	if raw.Dependencies != nil {
		deps := &CodexSkillDependencies{}
		for _, tool := range raw.Dependencies.Tools {
			dependency := CodexSkillToolDependency{
				Type:        collapseWhitespace(tool.Type),
				Value:       collapseWhitespace(tool.Value),
				Description: collapseWhitespace(tool.Description),
				Transport:   collapseWhitespace(tool.Transport),
				Command:     collapseWhitespace(tool.Command),
				URL:         collapseWhitespace(tool.URL),
			}
			if dependency.Type == "" || dependency.Value == "" {
				continue
			}
			deps.Tools = append(deps.Tools, dependency)
		}
		if len(deps.Tools) > 0 {
			result.Dependencies = deps
		}
	}
	if raw.Policy != nil {
		policy := &CodexSkillPolicy{}
		if raw.Policy.AllowImplicitInvocation != nil {
			value := *raw.Policy.AllowImplicitInvocation
			policy.AllowImplicitInvocationValue = &value
		}
		for _, product := range raw.Policy.Products {
			if value := collapseWhitespace(product); value != "" {
				policy.Products = append(policy.Products, value)
			}
		}
		if policy.AllowImplicitInvocationValue != nil || len(policy.Products) > 0 {
			result.Policy = policy
		}
	}

	if result.Interface == nil && result.Dependencies == nil && result.Policy == nil {
		return nil
	}
	return result
}

func sanitizeCodexRelativeAssetPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if filepath.IsAbs(path) {
		return ""
	}
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == ".." {
			return ""
		}
	}
	path = filepath.Clean(path)
	if path == "" || path == "." {
		return ""
	}
	if strings.HasPrefix(filepath.ToSlash(path), "../") || strings.EqualFold(path, "..") {
		return ""
	}
	return filepath.ToSlash(path)
}

func sanitizeCodexBrandColor(color string) string {
	color = collapseWhitespace(color)
	if color == "" {
		return ""
	}
	if len(color) != 7 || color[0] != '#' {
		return ""
	}
	for _, ch := range color[1:] {
		switch {
		case ch >= '0' && ch <= '9':
		case ch >= 'a' && ch <= 'f':
		case ch >= 'A' && ch <= 'F':
		default:
			return ""
		}
	}
	return color
}
