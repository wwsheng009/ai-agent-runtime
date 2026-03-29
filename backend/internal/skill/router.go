package skill

import (
	"context"
	"regexp"
	"sort"
	"strings"
)

// RouteResult 路由结果
type RouteResult struct {
	Skill     *Skill  `json:"skill"`
	Score     float64 `json:"score"`
	MatchedBy string  `json:"matchedBy"`
	Details   string  `json:"details,omitempty"`
}

// Router Skill 路由器
type Router struct {
	registry         *Registry
	embeddingRouter  EmbeddingRouter
	minScore         float64
	maxResults       int
	caseSensitive    bool
}

// NewRouter 创建路由器
func NewRouter(registry *Registry) *Router {
	return &Router{
		registry:      registry,
		minScore:      0.0,
		maxResults:    5,
		caseSensitive: false,
	}
}

// SetMinScore 设置最小分数
func (r *Router) SetMinScore(score float64) {
	r.minScore = score
}

// SetMaxResults 设置最大结果数
func (r *Router) SetMaxResults(max int) {
	r.maxResults = max
}

// SetCaseSensitive 设置是否区分大小写
func (r *Router) SetCaseSensitive(sensitive bool) {
	r.caseSensitive = sensitive
}

// SetEmbeddingRouter 设置 Embedding 路由器
func (r *Router) SetEmbeddingRouter(er EmbeddingRouter) {
	r.embeddingRouter = er
}

// Registry 返回内部注册表
func (r *Router) Registry() *Registry {
	return r.registry
}

// Route 路由用户请求
func (r *Router) Route(ctx context.Context, prompt string) []*RouteResult {
	return r.RouteWithConfig(ctx, prompt, nil)
}

// RouteWithConfig 带配置的路由
func (r *Router) RouteWithConfig(ctx context.Context, prompt string, config map[string]interface{}) []*RouteResult {
	processedPrompt := prompt
	if !r.caseSensitive {
		processedPrompt = strings.ToLower(prompt)
	}

	var allResults []*RouteResult

	// 1. 关键词匹配
	keywordResults := r.matchKeywords(processedPrompt, prompt)
	allResults = append(allResults, keywordResults...)

	// 2. 正则模式匹配
	patternResults := r.matchPatterns(processedPrompt, prompt)
	allResults = append(allResults, patternResults...)

	// 3. Embedding 匹配（如果启用）
	if r.embeddingRouter != nil {
		embeddingResults, err := r.embeddingRouter.Route(ctx, prompt)
		if err == nil {
			allResults = append(allResults, embeddingResults...)
		}
	}

	// 4. 去重并排序
	results := r.deduplicateAndSort(allResults)

	// 5. 过滤低分结果
	if r.minScore > 0 {
		results = r.filterByScore(results, r.minScore)
	}

	// 6. 限制结果数量
	if r.maxResults > 0 && len(results) > r.maxResults {
		results = results[:r.maxResults]
	}

	return results
}

// RouteBest 获取最佳匹配
func (r *Router) RouteBest(ctx context.Context, prompt string) (*RouteResult, bool) {
	results := r.Route(ctx, prompt)
	if len(results) == 0 {
		return nil, false
	}
	return results[0], true
}

// matchKeywords 关键词匹配
func (r *Router) matchKeywords(processedPrompt, originalPrompt string) []*RouteResult {
	var results []*RouteResult

	for keyword, skills := range r.registry.keywordIndex {
		// 检查是否包含关键词
		if strings.Contains(processedPrompt, keyword) {
			for _, skill := range skills {
				results = append(results, &RouteResult{
					Skill:     skill,
					Score:     r.calculateKeywordScore(skill, keyword, originalPrompt),
					MatchedBy: "keyword:" + keyword,
					Details:   "keyword match",
				})
			}
		}
	}

	return results
}

// matchPatterns 正则模式匹配
func (r *Router) matchPatterns(processedPrompt, originalPrompt string) []*RouteResult {
	var results []*RouteResult

	for pattern, skills := range r.registry.patternIndex {
		matched, _ := regexp.MatchString(pattern, processedPrompt)
		if matched {
			for _, skill := range skills {
				results = append(results, &RouteResult{
					Skill:     skill,
					Score:     r.calculatePatternScore(skill, pattern, originalPrompt),
					MatchedBy: "pattern:" + pattern,
					Details:   "pattern match",
				})
			}
		}
	}

	return results
}

// calculateKeywordScore 计算关键词分数
func (r *Router) calculateKeywordScore(skill *Skill, keyword, prompt string) float64 {
	totalWeight := 0.0
	for _, trigger := range skill.Triggers {
		if trigger.Type == "keyword" {
			for _, val := range trigger.Values {
				valLower := strings.ToLower(val)
				if valLower == keyword {
					totalWeight += trigger.Weight
				}
			}
		}
	}

	// 根据关键词在提示词中的位置调整分数
	promptLower := strings.ToLower(prompt)
	index := strings.Index(promptLower, keyword)
	positionBonus := 1.0
	if index == 0 {
		positionBonus = 1.2  // 开头匹配加分
	} else if index > 0 && index < len(promptLower)/3 {
		positionBonus = 1.1  // 前三分之一的分数加成
	}

	return totalWeight * positionBonus
}

// calculatePatternScore 计算模式分数
func (r *Router) calculatePatternScore(skill *Skill, pattern, prompt string) float64 {
	baseScore := 0.8  // 模式匹配基础分数

	for _, trigger := range skill.Triggers {
		if trigger.Type == "pattern" {
			baseScore += trigger.Weight
		}
	}

	return baseScore
}

// deduplicateAndSort 去重并排序
func (r *Router) deduplicateAndSort(results []*RouteResult) []*RouteResult {
	// 去重
	seen := make(map[string]bool)
	unique := make([]*RouteResult, 0)
	for _, result := range results {
		key := result.Skill.Name
		if !seen[key] {
			seen[key] = true
			unique = append(unique, result)
		}
	}

	// 按分数降序排序
	sort.Slice(unique, func(i, j int) bool {
		return unique[i].Score > unique[j].Score
	})

	return unique
}

// filterByScore 过滤低分结果
func (r *Router) filterByScore(results []*RouteResult, minScore float64) []*RouteResult {
	var filtered []*RouteResult
	for _, result := range results {
		if result.Score >= minScore {
			filtered = append(filtered, result)
		}
	}
	return filtered
}

// RouterConfig 路由器配置
type RouterConfig struct {
	MinScore      float64 `yaml:"minScore"`
	MaxResults    int     `yaml:"maxResults"`
	CaseSensitive bool    `yaml:"caseSensitive"`
	EnableEmbedding bool `yaml:"enableEmbedding"`
}

// ApplyConfig 应用配置
func (r *Router) ApplyConfig(config *RouterConfig) {
	r.minScore = config.MinScore
	r.maxResults = config.MaxResults
	r.caseSensitive = config.CaseSensitive
}

// GetStats 获取路由统计信息
func (r *Router) GetStats() *RouterStats {
	skills := r.registry.List()

	return &RouterStats{
		TotalSkills:        len(skills),
		SkillsWithKeywords: len(r.registry.keywordIndex),
		SkillsWithPatterns: len(r.registry.patternIndex),
		CaseSensitive:      r.caseSensitive,
		MinScore:           r.minScore,
		MaxResults:         r.maxResults,
	}
}

// RouterStats 路由器统计信息
type RouterStats struct {
	TotalSkills         int     `json:"totalSkills"`
	SkillsWithKeywords  int     `json:"skillsWithKeywords"`
	SkillsWithPatterns  int     `json:"skillsWithPatterns"`
	CaseSensitive       bool    `json:"caseSensitive"`
	MinScore            float64 `json:"minScore"`
	MaxResults          int     `json:"maxResults"`
}

// EmbeddingRouter Embedding 路由器接口
type EmbeddingRouter interface {
	Route(ctx context.Context, prompt string) ([]*RouteResult, error)
}

// SimpleEmbeddingRouter 简单的 Embedding 路由器实现
type SimpleEmbeddingRouter struct {
	embedder Embedder
	index    *VectorIndex
}

type Embedder interface {
	Embed(ctx context.Context, text string) ([]float32, error)
}

type VectorIndex struct {
	// 向量索引实现
}

func (e *SimpleEmbeddingRouter) Route(ctx context.Context, prompt string) ([]*RouteResult, error) {
	// 简化实现：暂时返回空结果
	return []*RouteResult{}, nil
}
