package profile

import (
	"embed"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// 内置 profile 模板（实施方案 Batch 2）：
//   - 模板随二进制分发（go:embed），不依赖仓库 checkout；
//   - examples/profiles/ 保留为参考样例，与模板同源由一致性测试保证；
//   - 渲染只做占位符替换与 agents/default 目录改名，不做任何"智能"改写。
//
//go:embed templates
var templateFS embed.FS

const (
	templateRootDir = "templates"

	// templateMetadataFile is the per-template metadata file. It drives
	// ListTemplates/RenderTemplate and is never part of the rendered output.
	templateMetadataFile = "template.yaml"

	placeholderName        = "{{name}}"
	placeholderDescription = "{{description}}"
	placeholderAgent       = "{{agent}}"

	// TemplateDefaultAgent is the agent id every built-in template ships with.
	TemplateDefaultAgent = "default"

	// templateAgentDir is the directory every template stores its agent under;
	// RenderTemplate renames it when a different agent id is requested.
	templateAgentDir = "agents/" + TemplateDefaultAgent
)

// TemplateInfo describes one built-in profile template.
type TemplateInfo struct {
	Name         string `json:"name"`
	Description  string `json:"description"`
	DefaultAgent string `json:"default_agent"`
	// Files are the rendered profile-relative paths for the default agent,
	// in deterministic order (useful for `profile create --dry-run`).
	Files []string `json:"files"`
}

type templateMetadata struct {
	Name         string `yaml:"name"`
	Description  string `yaml:"description"`
	DefaultAgent string `yaml:"default_agent"`
}

// TemplateNames returns the built-in template names in deterministic order.
func TemplateNames() []string {
	entries, err := fs.ReadDir(templateFS, templateRootDir)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names
}

// ListTemplates returns metadata for every built-in template, sorted by name.
func ListTemplates() []TemplateInfo {
	names := TemplateNames()
	infos := make([]TemplateInfo, 0, len(names))
	for _, name := range names {
		meta, err := loadTemplateMetadata(name)
		if err != nil {
			continue
		}
		files, err := renderTemplateFiles(name, name, meta.Description, TemplateDefaultAgent)
		if err != nil {
			continue
		}
		paths := make([]string, 0, len(files))
		for rel := range files {
			paths = append(paths, rel)
		}
		sort.Strings(paths)
		infos = append(infos, TemplateInfo{
			Name:         name,
			Description:  meta.Description,
			DefaultAgent: TemplateDefaultAgent,
			Files:        paths,
		})
	}
	return infos
}

// RenderTemplate renders the built-in template <name> for a profile called
// profileName whose default agent is agentID (empty = TemplateDefaultAgent).
//
// 渲染规则：内容中的 {{name}} / {{description}} / {{agent}} 占位符被替换；
// agentID 非默认时，agents/default/ 前缀改名为 agents/<agentID>/。
// 返回值以 profile 相对路径（slash 分隔）为键。
func RenderTemplate(name, profileName, agentID string) (map[string][]byte, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("profile template name is required")
	}
	profileName = strings.TrimSpace(profileName)
	if profileName == "" {
		return nil, fmt.Errorf("profile name is required")
	}
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		agentID = TemplateDefaultAgent
	}
	if err := validateTemplateAgentID(agentID); err != nil {
		return nil, err
	}

	meta, err := loadTemplateMetadata(name)
	if err != nil {
		return nil, err
	}
	return renderTemplateFiles(name, profileName, meta.Description, agentID)
}

func renderTemplateFiles(templateName, profileName, description, agentID string) (map[string][]byte, error) {
	dir := path.Join(templateRootDir, templateName)
	files := make(map[string][]byte)
	err := fs.WalkDir(templateFS, dir, func(filePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		rel := strings.TrimPrefix(path.Clean(filePath), path.Clean(dir)+"/")
		if rel == templateMetadataFile {
			return nil
		}
		content, err := fs.ReadFile(templateFS, filePath)
		if err != nil {
			return err
		}
		rendered := renderTemplateContent(string(content), profileName, description, agentID)
		files[rewriteTemplateAgentDir(rel, agentID)] = []byte(rendered)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("profile template %q has no files", templateName)
	}
	return files, nil
}

func renderTemplateContent(content, profileName, description, agentID string) string {
	content = strings.ReplaceAll(content, placeholderName, profileName)
	content = strings.ReplaceAll(content, placeholderDescription, description)
	content = strings.ReplaceAll(content, placeholderAgent, agentID)
	return content
}

func rewriteTemplateAgentDir(rel, agentID string) string {
	if agentID == TemplateDefaultAgent {
		return rel
	}
	prefix := templateAgentDir + "/"
	if strings.HasPrefix(rel, prefix) {
		return "agents/" + agentID + "/" + strings.TrimPrefix(rel, prefix)
	}
	return rel
}

func loadTemplateMetadata(name string) (templateMetadata, error) {
	meta := templateMetadata{}
	metaPath := path.Join(templateRootDir, name, templateMetadataFile)
	raw, err := fs.ReadFile(templateFS, metaPath)
	if err != nil {
		return meta, fmt.Errorf("unknown profile template %q (available: %s)",
			name, strings.Join(TemplateNames(), ", "))
	}
	if err := yaml.Unmarshal(raw, &meta); err != nil {
		return meta, fmt.Errorf("parse template metadata %s: %w", metaPath, err)
	}
	if strings.TrimSpace(meta.Description) == "" {
		meta.Description = strings.TrimSpace(meta.Name)
	}
	return meta, nil
}

// validateTemplateAgentID keeps generated agent directory names portable and
// path-safe (they become directory names under agents/).
func validateTemplateAgentID(agentID string) error {
	for _, r := range agentID {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '.':
		default:
			return fmt.Errorf("agent id %q contains unsupported character %q (allowed: letters, digits, '-', '_', '.')", agentID, r)
		}
	}
	return nil
}
