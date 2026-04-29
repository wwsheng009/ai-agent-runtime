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
	Metadata    struct {
		ShortDescription string `yaml:"short-description"`
	} `yaml:"metadata"`
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

	skill := &CodexSkillMetadata{
		Name:             name,
		Description:      description,
		ShortDescription: shortDescription,
		PathToSkillsMD:   filepath.Clean(filePath),
		MetadataPath:     codexMetadataPathForSkillPath(filePath),
		Enabled:          true,
	}
	if loadBody {
		skill.Body = string(bodyBytes)
	}
	if meta := parseCodexOpenAIMetadata(skill.MetadataPath); meta != nil {
		skill.Interface = meta.Interface
		skill.Dependencies = meta.Dependencies
		skill.Policy = meta.Policy
	}
	return skill, nil
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
