package skill

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/errors"
)

// Loader Skill 加载器
type Loader struct {
	parser       *ManifestParser
	mcpManager   MCPManager
	skillDir     string
	skillDirs    []string
	filePatterns []string
}

// NewLoader 创建加载器
func NewLoader(mcpManager MCPManager) *Loader {
	parser := NewManifestParser()
	parser.SetCompanionPromptLoadMode(CompanionPromptLoadLazy)
	return &Loader{
		parser:       parser,
		mcpManager:   mcpManager,
		filePatterns: []string{"skill.yaml", "skill.yml"},
	}
}

// Load 从目录加载所有 Skills
func (l *Loader) Load(dir string) ([]*Skill, error) {
	l.skillDir = dir
	l.skillDirs = normalizeSkillDirs([]string{dir})

	skills, err := l.parser.ParseDir(dir)
	if err != nil {
		return nil, errors.Wrap(errors.ErrSkillLoadFailed,
			fmt.Sprintf("failed to load skills from directory: %s", dir), err)
	}
	l.annotateSkillLayers(skills, dir, SkillSourceLayerSystem)

	return skills, nil
}

// LoadAll 从多个目录加载所有 Skills
func (l *Loader) LoadAll(dirs []string) ([]*Skill, error) {
	normalized := normalizeSkillDirs(dirs)
	if len(normalized) == 0 {
		return nil, errors.New(errors.ErrSkillLoadFailed, "no skill directories configured")
	}

	l.skillDirs = normalized
	l.skillDir = normalized[0]

	skills := make([]*Skill, 0)
	for _, dir := range normalized {
		loaded, err := l.parser.ParseDir(dir)
		if err != nil {
			return nil, errors.Wrap(errors.ErrSkillLoadFailed,
				fmt.Sprintf("failed to load skills from directory: %s", dir), err)
		}
		layer := SkillSourceLayerExternal
		if dir == normalized[0] {
			layer = SkillSourceLayerSystem
		}
		l.annotateSkillLayers(loaded, dir, layer)
		skills = append(skills, loaded...)
	}

	return dedupeSkillsByName(skills), nil
}

// Discover 从目录发现轻量 Skills 摘要。
func (l *Loader) Discover(dir string) ([]*SkillSummary, error) {
	l.skillDir = dir
	l.skillDirs = normalizeSkillDirs([]string{dir})

	summaries, err := l.parser.ParseSummaryDir(dir)
	if err != nil {
		return nil, errors.Wrap(errors.ErrSkillLoadFailed,
			fmt.Sprintf("failed to discover skills from directory: %s", dir), err)
	}
	l.annotateSummaryLayers(summaries, dir, SkillSourceLayerSystem)
	return summaries, nil
}

// DiscoverAll 从多个目录发现轻量 Skills 摘要。
func (l *Loader) DiscoverAll(dirs []string) ([]*SkillSummary, error) {
	normalized := normalizeSkillDirs(dirs)
	if len(normalized) == 0 {
		return nil, errors.New(errors.ErrSkillLoadFailed, "no skill directories configured")
	}

	l.skillDirs = normalized
	l.skillDir = normalized[0]

	summaries := make([]*SkillSummary, 0)
	for _, dir := range normalized {
		discovered, err := l.parser.ParseSummaryDir(dir)
		if err != nil {
			return nil, errors.Wrap(errors.ErrSkillLoadFailed,
				fmt.Sprintf("failed to discover skills from directory: %s", dir), err)
		}
		layer := SkillSourceLayerExternal
		if dir == normalized[0] {
			layer = SkillSourceLayerSystem
		}
		l.annotateSummaryLayers(discovered, dir, layer)
		summaries = append(summaries, discovered...)
	}

	return dedupeSkillSummariesByName(summaries), nil
}

// LoadFile 加载单个 Skill 文件
func (l *Loader) LoadFile(filePath string) (*Skill, error) {
	return l.loadFileWithMode(filePath, CompanionPromptLoadLazy)
}

// LoadFileFull 加载单个 Skill 文件并立即解析 companion prompt。
func (l *Loader) LoadFileFull(filePath string) (*Skill, error) {
	return l.loadFileWithMode(filePath, CompanionPromptLoadEager)
}

func (l *Loader) loadFileWithMode(filePath string, mode CompanionPromptLoadMode) (*Skill, error) {
	parser := l.parser
	if parser == nil || parser.promptLoadMode != mode {
		parser = NewManifestParser()
		parser.SetCompanionPromptLoadMode(mode)
	}

	skill, err := parser.ParseFile(filePath)
	if err != nil {
		return nil, errors.Wrap(errors.ErrSkillLoadFailed,
			fmt.Sprintf("failed to load skill file: %s", filePath), err)
	}

	return skill, nil
}

// DiscoverFile 发现单个 Skill 文件的轻量摘要。
func (l *Loader) DiscoverFile(filePath string) (*SkillSummary, error) {
	summary, err := l.parser.ParseSummaryFile(filePath)
	if err != nil {
		return nil, errors.Wrap(errors.ErrSkillLoadFailed,
			fmt.Sprintf("failed to discover skill file: %s", filePath), err)
	}
	return summary, nil
}

// LoadManifest 从目录和文件名加载
func (l *Loader) LoadManifest(dir, filename string) (*Skill, error) {
	filePath := filepath.Join(dir, filename)
	return l.LoadFile(filePath)
}

// LoadByName 从目录加载指定名称的 Skill
func (l *Loader) LoadByName(dir, name string) (*Skill, error) {
	// 尝试在目录中查找匹配的 skill 文件
	var foundFile string

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}

		// 检查文件名是否匹配
		baseName := filepath.Base(path)
		for _, pattern := range l.filePatterns {
			if baseName == pattern {
				// 读取文件验证名称
				skill, err := l.parser.ParseFile(path)
				if err == nil && skill.Name == name {
					foundFile = path
					return filepath.SkipDir
				}
			}
		}

		// 检查是否在 name 目录下
		parentDir := filepath.Dir(path)
		if filepath.Base(parentDir) == name {
			for _, pattern := range l.filePatterns {
				if baseName == pattern {
					foundFile = path
					return filepath.SkipDir
				}
			}
		}

		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to scan directory: %w", err)
	}

	if foundFile == "" {
		return nil, errors.New(errors.ErrSkillNotFound,
			fmt.Sprintf("skill not found: %s", name))
	}

	return l.LoadFile(foundFile)
}

// LoadWithRegistry 加载 Skills 并注册到 Registry
func (l *Loader) LoadWithRegistry(dir string, registry *Registry) error {
	skills, err := l.Load(dir)
	if err != nil {
		return err
	}

	return l.registerSkills(skills, registry)
}

// LoadAllWithRegistry 从多个目录加载 Skills 并注册到 Registry
func (l *Loader) LoadAllWithRegistry(dirs []string, registry *Registry) error {
	skills, err := l.LoadAll(dirs)
	if err != nil {
		return err
	}

	return l.registerSkills(skills, registry)
}

// DiscoverAllWithRegistry 从多个目录发现 Skills 并注册轻量 stub 到 Registry。
func (l *Loader) DiscoverAllWithRegistry(dirs []string, registry *Registry) error {
	summaries, err := l.DiscoverAll(dirs)
	if err != nil {
		return err
	}
	return l.registerSummaryStubs(summaries, registry)
}

func (l *Loader) registerSkills(skills []*Skill, registry *Registry) error {
	var errs []error
	for _, skill := range skills {
		if err := registry.Register(skill); err != nil {
			errs = append(errs, err)
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("failed to register %d skills: %v", len(errs), errs)
	}

	return nil
}

func (l *Loader) registerSummaryStubs(summaries []*SkillSummary, registry *Registry) error {
	var errs []error
	for _, summary := range summaries {
		if summary == nil {
			continue
		}
		if err := registry.Register(summary.ToSkillStub()); err != nil {
			errs = append(errs, err)
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("failed to register %d skills: %v", len(errs), errs)
	}
	return nil
}

// SetSkillDir 设置默认技能目录
func (l *Loader) SetSkillDir(dir string) {
	l.skillDir = dir
	l.skillDirs = normalizeSkillDirs([]string{dir})
}

// SetSkillDirs 设置多个技能目录
func (l *Loader) SetSkillDirs(dirs []string) {
	l.skillDirs = normalizeSkillDirs(dirs)
	if len(l.skillDirs) > 0 {
		l.skillDir = l.skillDirs[0]
		return
	}
	l.skillDir = ""
}

// GetSkillDir 获取技能目录
func (l *Loader) GetSkillDir() string {
	return l.skillDir
}

// GetSkillDirs 获取技能目录列表
func (l *Loader) GetSkillDirs() []string {
	return append([]string(nil), l.skillDirs...)
}

// SetFilePatterns 设置文件匹配模式
func (l *Loader) SetFilePatterns(patterns []string) {
	l.filePatterns = patterns
}

// Reload 重新加载所有 Skills
func (l *Loader) Reload(dir string) ([]*Skill, error) {
	return l.Load(dir)
}

// ReloadAll 重新加载多个目录的 Skills
func (l *Loader) ReloadAll(dirs []string) ([]*Skill, error) {
	return l.LoadAll(dirs)
}

// CheckSkill 检查 Skill 是否有效
func (l *Loader) CheckSkill(skill *Skill) error {
	// 使用验证器检查
	if err := l.parser.validate(skill); err != nil {
		return err
	}

	// 检查工具是否可用
	if l.mcpManager != nil {
		for _, toolName := range skill.Tools {
			if _, err := l.mcpManager.FindTool(toolName); err != nil {
				return errors.Wrap(errors.ErrToolNotRegistered,
					fmt.Sprintf("tool not available: %s in skill %s", toolName, skill.Name), err)
			}
		}
	}

	return nil
}

// Save 保存 Skill 到指定目录
func (l *Loader) Save(skill *Skill, dir string) error {
	// 确保目录存在
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	// 创建 skill 子目录
	skillDir := filepath.Join(dir, skill.Name)
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		return fmt.Errorf("failed to create skill directory: %w", err)
	}

	filePath := filepath.Join(skillDir, "skill.yaml")
	return l.parser.SaveFile(skill, filePath)
}

// SaveToFile 保存 Skill 到指定文件
func (l *Loader) SaveToFile(skill *Skill, filePath string) error {
	return l.parser.SaveFile(skill, filePath)
}

// ListSkillNames 列出所有 Skill 名称
func (l *Loader) ListSkillNames(dir string) ([]string, error) {
	skills, err := l.Load(dir)
	if err != nil {
		return nil, err
	}

	names := make([]string, len(skills))
	for i, skill := range skills {
		names[i] = skill.Name
	}

	return names, nil
}

// GetSkillStats 获取技能统计信息
func (l *Loader) GetSkillStats(dir string) (*SkillStats, error) {
	skills, err := l.Load(dir)
	if err != nil {
		return nil, err
	}

	stats := &SkillStats{
		TotalSkills:  len(skills),
		Skills:       make(map[string]*SkillInfo),
		Tools:        make(map[string]int),
		TriggerTypes: make(map[string]int),
	}

	for _, skill := range skills {
		stats.Skills[skill.Name] = &SkillInfo{
			Name:        skill.Name,
			Version:     skill.Version,
			Description: skill.Description,
			HasWorkflow: skill.HasWorkflow(),
			ToolCount:   len(skill.Tools),
		}

		// 统计工具
		for _, tool := range skill.Tools {
			stats.Tools[tool]++
		}

		// 统计触发类型
		for _, trigger := range skill.Triggers {
			stats.TriggerTypes[trigger.Type]++
		}
	}

	return stats, nil
}

// SkillStats 技能统计信息
type SkillStats struct {
	TotalSkills  int                   `json:"totalSkills"`
	Skills       map[string]*SkillInfo `json:"skills"`
	Tools        map[string]int        `json:"tools"`
	TriggerTypes map[string]int        `json:"triggerTypes"`
}

// SkillInfo 技能信息
type SkillInfo struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description"`
	HasWorkflow bool   `json:"hasWorkflow"`
	ToolCount   int    `json:"toolCount"`
}

// CreateTemplate 创建 Skill 模板
func (l *Loader) CreateTemplate(name, description string) (*Skill, error) {
	skill := &Skill{
		Name:        name,
		Description: description,
		Version:     "1.0.0",
		Triggers: []Trigger{
			{
				Type:   "keyword",
				Values: []string{name},
				Weight: 1.0,
			},
		},
		Tools:        []string{},
		SystemPrompt: fmt.Sprintf("You are a %s assistant.", name),
		UserPrompt:   "{{.Prompt}}",
		Context: ContextConfig{
			Files:       []string{},
			Environment: []string{},
			Symbols:     []string{},
		},
		Permissions: []string{},
	}

	return skill, nil
}

// ValidateAndLoad 验证并加载所有 Skills
func (l *Loader) ValidateAndLoad(dir string) ([]*Skill, []error) {
	var validSkills []*Skill
	var validationErrors []error

	// 扫描目录
	skills, err := l.parser.ParseDir(dir)
	if err != nil {
		validationErrors = append(validationErrors, err)
		return nil, validationErrors
	}

	// 验证每个 Skill
	for _, skill := range skills {
		if err := l.CheckSkill(skill); err != nil {
			validationErrors = append(validationErrors,
				errors.Wrap(errors.ErrValidationFailed,
					fmt.Sprintf("skill %s validation failed", skill.Name), err))
		} else {
			validSkills = append(validSkills, skill)
		}
	}

	return validSkills, validationErrors
}

func normalizeSkillDirs(dirs []string) []string {
	seen := make(map[string]struct{})
	normalized := make([]string, 0, len(dirs))

	for _, dir := range dirs {
		dir = filepath.Clean(strings.TrimSpace(dir))
		if dir == "." || dir == "" {
			if strings.TrimSpace(dir) == "" {
				continue
			}
			dir = filepath.Clean(dir)
		}
		if _, exists := seen[dir]; exists {
			continue
		}
		seen[dir] = struct{}{}
		normalized = append(normalized, dir)
	}

	return normalized
}

func (l *Loader) annotateSkillLayers(skills []*Skill, dir, layer string) {
	for _, skill := range skills {
		if skill == nil {
			continue
		}
		if skill.Source == nil {
			skill.SetSource("", dir, layer)
			continue
		}
		if skill.Source.Dir == "" {
			skill.Source.Dir = dir
		}
		if skill.Source.Layer == "" || skill.Source.Layer == SkillSourceLayerUnknown {
			skill.Source.Layer = layer
		}
	}
}

func (l *Loader) annotateSummaryLayers(summaries []*SkillSummary, dir, layer string) {
	for _, summary := range summaries {
		if summary == nil {
			continue
		}
		if summary.Source == nil {
			summary.Source = &SkillSource{Dir: dir, Layer: layer}
			continue
		}
		if summary.Source.Dir == "" {
			summary.Source.Dir = dir
		}
		if summary.Source.Layer == "" || summary.Source.Layer == SkillSourceLayerUnknown {
			summary.Source.Layer = layer
		}
	}
}

func dedupeSkillsByName(skills []*Skill) []*Skill {
	seen := make(map[string]struct{})
	deduped := make([]*Skill, 0, len(skills))

	for _, skill := range skills {
		if skill == nil {
			continue
		}
		if _, exists := seen[skill.Name]; exists {
			continue
		}
		seen[skill.Name] = struct{}{}
		deduped = append(deduped, skill)
	}

	return deduped
}

func dedupeSkillSummariesByName(summaries []*SkillSummary) []*SkillSummary {
	seen := make(map[string]struct{})
	deduped := make([]*SkillSummary, 0, len(summaries))

	for _, summary := range summaries {
		if summary == nil {
			continue
		}
		if _, exists := seen[summary.Name]; exists {
			continue
		}
		seen[summary.Name] = struct{}{}
		deduped = append(deduped, summary)
	}

	return deduped
}
