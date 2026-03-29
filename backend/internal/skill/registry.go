package skill

import (
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/wwsheng009/ai-agent-runtime/internal/errors"
)

// MCPManager MCP 管理器接口
type MCPManager interface {
	FindTool(toolName string) (ToolInfo, error)
	CallTool(ctx interface{}, mcpName, toolName string, args map[string]interface{}) (interface{}, error)
	ListTools() []ToolInfo
}

// ToolInfo 工具信息（简化版）
type ToolInfo struct {
	Name          string
	Description   string
	InputSchema   map[string]interface{}
	MCPName       string
	MCPTrustLevel string
	ExecutionMode string
	Enabled       bool
}

// Registry Skill 注册表
type Registry struct {
	mu          sync.RWMutex
	skills      map[string]*Skill
	summaries   map[string]*SkillSummary
	loadedCache map[string]*hydratedSkillCacheEntry

	// 按触发类型索引
	keywordIndex map[string][]*Skill
	patternIndex map[string][]*Skill
	weightIndex  map[*Skill]float64 // 技能权重索引

	// MCP Manager 引用
	mcpManager MCPManager
}

// NewRegistry 创建注册表
func NewRegistry(mcpManager MCPManager) *Registry {
	return &Registry{
		skills:       make(map[string]*Skill),
		summaries:    make(map[string]*SkillSummary),
		loadedCache:  make(map[string]*hydratedSkillCacheEntry),
		keywordIndex: make(map[string][]*Skill),
		patternIndex: make(map[string][]*Skill),
		weightIndex:  make(map[*Skill]float64),
		mcpManager:   mcpManager,
	}
}

// Register 注册 Skill
func (r *Registry) Register(s *Skill) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	// 验证 Skill
	if err := r.validate(s); err != nil {
		return err
	}

	// 存储 Skill
	r.skills[s.Name] = s
	r.storeSummaryLocked(s)

	// 构建索引
	r.buildIndex(s)

	return nil
}

// Unregister 注销 Skill
func (r *Registry) Unregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if s, ok := r.skills[name]; ok {
		r.removeFromIndex(s)
		delete(r.skills, name)
		delete(r.summaries, name)
		delete(r.loadedCache, name)
		delete(r.weightIndex, s)
	}
}

// Get 获取 Skill
func (r *Registry) Get(name string) (*Skill, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	s, ok := r.skills[name]
	return s, ok
}

// List 列出所有 Skills
func (r *Registry) List() []*Skill {
	r.mu.RLock()
	defer r.mu.RUnlock()

	skills := make([]*Skill, 0, len(r.skills))
	for _, s := range r.skills {
		skills = append(skills, s)
	}
	return skills
}

// GetSummary 获取 Skill 摘要。
func (r *Registry) GetSummary(name string) (*SkillSummary, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	summary, ok := r.summaries[name]
	if !ok || summary == nil {
		return nil, false
	}
	return cloneSkillSummary(summary), true
}

// ListSummaries 列出所有 Skill 摘要。
func (r *Registry) ListSummaries() []*SkillSummary {
	r.mu.RLock()
	defer r.mu.RUnlock()

	summaries := make([]*SkillSummary, 0, len(r.summaries))
	for _, summary := range r.summaries {
		if summary == nil {
			continue
		}
		summaries = append(summaries, cloneSkillSummary(summary))
	}
	return summaries
}

// FindByKeyword 根据关键词查找 Skills
func (r *Registry) FindByKeyword(keyword string) []*Skill {
	r.mu.RLock()
	defer r.mu.RUnlock()

	keywordLower := strings.ToLower(keyword)
	var results []*Skill

	if skills, ok := r.keywordIndex[keywordLower]; ok {
		results = append(results, skills...)
	}

	// 检查部分匹配
	for kw, skills := range r.keywordIndex {
		if strings.Contains(keywordLower, kw) {
			results = append(results, skills...)
		}
	}

	return deduplicateSkills(results)
}

// FindByPattern 根据正则模式查找 Skills
func (r *Registry) FindByPattern(text string) []*Skill {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var results []*Skill

	for pattern, skills := range r.patternIndex {
		matched, _ := regexp.MatchString(pattern, text)
		if matched {
			results = append(results, skills...)
		}
	}

	return deduplicateSkills(results)
}

// Search 综合搜索 Skills
func (r *Registry) Search(text string) []*Skill {
	return r.SearchWithContext(text, nil)
}

// SearchWithContext 带上下文搜索 Skills
func (r *Registry) SearchWithContext(text string, context map[string]interface{}) []*Skill {
	var allResults []*Skill

	// 1. 关键词匹配
	keywordResults := r.FindByKeyword(text)
	allResults = append(allResults, keywordResults...)

	// 2. 正则模式匹配
	patternResults := r.FindByPattern(text)
	allResults = append(allResults, patternResults...)

	// 按权重排序
	r.sortByWeight(allResults)

	return deduplicateSkills(allResults)
}

// RegisterBatch 批量注册 Skills
func (r *Registry) RegisterBatch(skills []*Skill) []error {
	r.mu.Lock()
	defer r.mu.Unlock()

	var errs []error
	for _, skill := range skills {
		if err := r.validate(skill); err != nil {
			errs = append(errs, err)
			continue
		}

		r.skills[skill.Name] = skill
		r.storeSummaryLocked(skill)
		r.buildIndex(skill)
	}

	return errs
}

// Clear 清空注册表
func (r *Registry) Clear() {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.skills = make(map[string]*Skill)
	r.summaries = make(map[string]*SkillSummary)
	r.loadedCache = make(map[string]*hydratedSkillCacheEntry)
	r.keywordIndex = make(map[string][]*Skill)
	r.patternIndex = make(map[string][]*Skill)
	r.weightIndex = make(map[*Skill]float64)
}

// Count 返回注册的 Skill 数量
func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.skills)
}

// validate 验证 Skill
func (r *Registry) validate(s *Skill) error {
	// 检查名称
	if s.Name == "" {
		return errors.New(errors.ErrValidationFailed, "skill name is required")
	}

	// 检查是否已存在
	if _, exists := r.skills[s.Name]; exists {
		return errors.New(errors.ErrValidationFailed, fmt.Sprintf("skill already registered: %s", s.Name))
	}

	// 验证工具是否存在（如果启用了 验证）
	for _, toolName := range s.Tools {
		if r.mcpManager != nil {
			_, err := r.mcpManager.FindTool(toolName)
			if err != nil {
				return errors.Wrap(errors.ErrToolNotRegistered,
					fmt.Sprintf("tool not found: %s", toolName), err)
			}
		}
	}

	return nil
}

// buildIndex 构建索引
func (r *Registry) buildIndex(s *Skill) {
	// 计算总权重
	totalWeight := 0.0
	for _, trigger := range s.Triggers {
		if trigger.Weight > 0 {
			totalWeight += trigger.Weight
		}
	}
	r.weightIndex[s] = totalWeight

	// 构建触发规则索引
	for _, trigger := range s.Triggers {
		switch trigger.Type {
		case "keyword":
			for _, kw := range trigger.Values {
				kwLower := strings.ToLower(kw)
				r.keywordIndex[kwLower] = append(r.keywordIndex[kwLower], s)
			}
		case "pattern":
			for _, pattern := range trigger.Values {
				r.patternIndex[pattern] = append(r.patternIndex[pattern], s)
			}
		}
	}
}

// removeFromIndex 从索引中移除 Skill
func (r *Registry) removeFromIndex(s *Skill) {
	// 从关键词索引中移除
	for kw, skills := range r.keywordIndex {
		r.keywordIndex[kw] = filterSkill(skills, s)
	}

	// 从模式索引中移除
	for pattern, skills := range r.patternIndex {
		r.patternIndex[pattern] = filterSkill(skills, s)
	}
}

// sortByWeight 按权重排序 Skills
func (r *Registry) sortByWeight(skills []*Skill) {
	// 使用简单的冒泡排序（实际项目中应该使用更高效的算法）
	for i := 0; i < len(skills); i++ {
		for j := i + 1; j < len(skills); j++ {
			if r.weightIndex[skills[i]] < r.weightIndex[skills[j]] {
				skills[i], skills[j] = skills[j], skills[i]
			}
		}
	}
}

// deduplicateSkills 去重 Skills
func deduplicateSkills(skills []*Skill) []*Skill {
	seen := make(map[string]bool)
	var result []*Skill

	for _, s := range skills {
		if !seen[s.Name] {
			seen[s.Name] = true
			result = append(result, s)
		}
	}

	return result
}

// filterSkill 从列表中过滤指定的 Skill
func filterSkill(skills []*Skill, target *Skill) []*Skill {
	var result []*Skill
	for _, s := range skills {
		if s != target {
			result = append(result, s)
		}
	}
	return result
}

// GetWeight 获取 Skill 权重
func (r *Registry) GetWeight(s *Skill) float64 {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return r.weightIndex[s]
}

// FindSkillsByTool 根据工具查找使用它的 Skills
func (r *Registry) FindSkillsByTool(toolName string) []*Skill {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var results []*Skill
	for _, s := range r.skills {
		for _, tool := range s.Tools {
			if tool == toolName {
				results = append(results, s)
				break
			}
		}
	}
	return results
}

// Hydrate 将 discovery stub 解析为 full skill，并优先使用 registry 内部 loaded cache。
func (r *Registry) Hydrate(item *Skill) (*Skill, error) {
	return HydrateSkillWithRegistry(item, r)
}

// InvalidateLoadedSkill 清理指定技能的 loaded cache。
func (r *Registry) InvalidateLoadedSkill(name string) {
	r.mu.Lock()
	delete(r.loadedCache, strings.TrimSpace(name))
	r.mu.Unlock()
}

// ClearLoadedCache 清空 loaded cache。
func (r *Registry) ClearLoadedCache() {
	r.mu.Lock()
	r.loadedCache = make(map[string]*hydratedSkillCacheEntry)
	r.mu.Unlock()
}

func (r *Registry) storeSummaryLocked(item *Skill) {
	summary := SummaryFromSkill(item)
	if summary == nil {
		return
	}
	r.summaries[item.Name] = summary
}

func (r *Registry) getLoadedSkill(name string, manifest hydratedSkillFileStamp, prompt hydratedSkillFileStamp) *Skill {
	r.mu.RLock()
	defer r.mu.RUnlock()

	entry, ok := r.loadedCache[strings.TrimSpace(name)]
	if !ok || entry == nil || entry.skill == nil {
		return nil
	}
	if !sameHydratedSkillStamp(entry.manifest, manifest) || !sameHydratedSkillStamp(entry.prompt, prompt) {
		return nil
	}
	return entry.skill
}

func (r *Registry) putLoadedSkill(name string, manifest hydratedSkillFileStamp, prompt hydratedSkillFileStamp, item *Skill) {
	if item == nil {
		return
	}
	r.mu.Lock()
	if r.loadedCache == nil {
		r.loadedCache = make(map[string]*hydratedSkillCacheEntry)
	}
	r.loadedCache[strings.TrimSpace(name)] = &hydratedSkillCacheEntry{
		manifest: manifest,
		prompt:   prompt,
		skill:    cloneSkill(item),
	}
	r.mu.Unlock()
}
