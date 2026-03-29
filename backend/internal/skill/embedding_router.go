package skill

import (
	"context"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/embedding"
)

// SemanticEmbeddingRouter 语义路由器实现
type SemanticEmbeddingRouter struct {
	searcher  *embedding.SemanticSearcher
	registry  *Registry
	threshold float32
	topK      int
}

// NewSemanticEmbeddingRouter 创建语义路由器
func NewSemanticEmbeddingRouter(
	vectorIndex *embedding.VectorIndex,
	registry *Registry,
) (*SemanticEmbeddingRouter, error) {
	if vectorIndex == nil {
		return nil, fmt.Errorf("vector index cannot be nil")
	}
	if registry == nil {
		return nil, fmt.Errorf("registry cannot be nil")
	}

	searchConfig := embedding.DefaultSearchConfig()
	searcher, err := embedding.NewSemanticSearcher(vectorIndex, searchConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create semantic searcher: %w", err)
	}

	return &SemanticEmbeddingRouter{
		searcher:  searcher,
		registry:  registry,
		threshold: 0.5,
		topK:      5,
	}, nil
}

// SetThreshold 设置相似度阈值
func (r *SemanticEmbeddingRouter) SetThreshold(threshold float32) {
	r.threshold = threshold
}

// SetTopK 设置返回结果数量
func (r *SemanticEmbeddingRouter) SetTopK(topK int) {
	r.topK = topK
}

// GetThreshold 获取当前阈值
func (r *SemanticEmbeddingRouter) GetThreshold() float32 {
	return r.threshold
}

// GetTopK 获取当前返回数量
func (r *SemanticEmbeddingRouter) GetTopK() int {
	return r.topK
}

// CloneForRegistry 基于同一向量索引为另一个 Registry 创建路由器副本
func (r *SemanticEmbeddingRouter) CloneForRegistry(registry *Registry) (*SemanticEmbeddingRouter, error) {
	if r == nil {
		return nil, fmt.Errorf("embedding router cannot be nil")
	}

	cloned, err := NewSemanticEmbeddingRouter(r.searcher.Index(), registry)
	if err != nil {
		return nil, err
	}
	cloned.threshold = r.threshold
	cloned.topK = r.topK
	return cloned, nil
}

// VectorIndex 返回底层向量索引
func (r *SemanticEmbeddingRouter) VectorIndex() *embedding.VectorIndex {
	if r == nil || r.searcher == nil {
		return nil
	}
	return r.searcher.Index()
}

// Route 执行路由
func (r *SemanticEmbeddingRouter) Route(ctx context.Context, prompt string) ([]*RouteResult, error) {
	// 更新搜索配置
	searchConfig := &embedding.SearchConfig{
		TopK:            r.topK,
		Threshold:       r.threshold,
		IncludeMetadata: true,
		IncludeDistance: false,
	}

	// 创建新的搜索器
	searcher, err := embedding.NewSemanticSearcher(r.searcher.Index(), searchConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create semantic searcher: %w", err)
	}

	// 执行搜索
	results, err := searcher.Search(prompt)
	if err != nil {
		return nil, fmt.Errorf("failed to search: %w", err)
	}

	// 转换为路由结果
	routeResults := make([]*RouteResult, 0, len(results))
	for _, result := range results {
		// 从元数据中提取 Skill 信息
		metadata := result.Metadata
		skillName, ok := metadata["skill_name"].(string)
		if !ok {
			// 如果没有 Skill 名称，跳过
			continue
		}

		// 从注册表中获取 Skill
		skill, exists := r.registry.Get(skillName)
		if !exists {
			continue
		}

		routeResults = append(routeResults, &RouteResult{
			Skill:     skill,
			Score:     float64(result.Score),
			MatchedBy: "embedding",
			Details:   fmt.Sprintf("semantic similarity: %.4f", result.Score),
		})
	}

	return routeResults, nil
}

// IndexSkills 将 Skills 索引到向量数据库
func (r *SemanticEmbeddingRouter) IndexSkills() error {
	skills := r.registry.List()

	// 准备索引项
	items := make([]struct {
		ID       string
		Content  string
		Metadata map[string]any
	}, 0, len(skills))

	for _, skill := range skills {
		// 构建用于索引的文本内容
		content := r.buildSkillText(skill)
		id := r.buildSkillID(skill)

		// 构建元数据
		metadata := map[string]any{
			"skill_name":   skill.Name,
			"description":  skill.Description,
			"category":     skill.Category,
			"tools":        skill.Tools,
			"capabilities": skill.Capabilities,
			"tags":         skill.Tags,
		}

		items = append(items, struct {
			ID       string
			Content  string
			Metadata map[string]any
		}{
			ID:       id,
			Content:  content,
			Metadata: metadata,
		})
	}

	// 批量添加到向量索引
	return r.searcher.Index().AddBatch(items)
}

// RebuildIndex 重建索引
func (r *SemanticEmbeddingRouter) RebuildIndex() error {
	// 清空现有索引
	r.searcher.Index().Clear()

	// 重新索引
	return r.IndexSkills()
}

// IncrementalIndex 增量索引（只索引新增的 Skill）
func (r *SemanticEmbeddingRouter) IncrementalIndex(skill *Skill) error {
	// 构建索引内容
	content := r.buildSkillText(skill)
	id := r.buildSkillID(skill)

	metadata := map[string]any{
		"skill_name":   skill.Name,
		"description":  skill.Description,
		"category":     skill.Category,
		"tools":        skill.Tools,
		"capabilities": skill.Capabilities,
		"tags":         skill.Tags,
	}

	// 查找现有索引项
	_, err := r.searcher.Index().Get(id)
	if err == nil {
		// 更新现有项
		return r.searcher.Index().Update(id, content, metadata)
	}

	// 添加新项
	return r.searcher.Index().Add(id, content, metadata)
}

// RemoveIndex 移除索引
func (r *SemanticEmbeddingRouter) RemoveIndex(skill *Skill) error {
	id := r.buildSkillID(skill)
	return r.searcher.Index().Delete(id)
}

// buildSkillText 构建用于索引的 Skill 文本
func (r *SemanticEmbeddingRouter) buildSkillText(skill *Skill) string {
	text := skill.Name + " "

	if skill.Description != "" {
		text += skill.Description + " "
	}

	if skill.Category != "" {
		text += "category: " + skill.Category + " "
	}

	// 添加触发器信息
	for _, trigger := range skill.Triggers {
		if trigger.Type == "keyword" {
			text += "keywords: "
			for _, val := range trigger.Values {
				text += val + " "
			}
		}
	}

	// 添加能力信息
	for _, capability := range skill.Capabilities {
		text += "can " + capability + " "
	}

	// 添加标签信息
	for _, tag := range skill.Tags {
		text += "tag: " + tag + " "
	}

	return text
}

// buildSkillID 构建 Skill ID
func (r *SemanticEmbeddingRouter) buildSkillID(skill *Skill) string {
	return embedding.GenerateID("skill", skill.Name)
}

// GetStats 获取路由统计信息
func (r *SemanticEmbeddingRouter) GetStats() *EmbeddingRouterStats {
	return &EmbeddingRouterStats{
		IndexSize: r.searcher.Index().Size(),
		Threshold: r.threshold,
		TopK:      r.topK,
	}
}

// EmbeddingRouterStats 语义路由统计
type EmbeddingRouterStats struct {
	IndexSize int     `json:"indexSize"`
	Threshold float32 `json:"threshold"`
	TopK      int     `json:"topK"`
}

// RouterConfig 扩展包含 EmbeddingRouter 配置
type EmbeddingRouterConfig struct {
	Enable    bool    `yaml:"enable"`
	Threshold float32 `yaml:"threshold"`
	TopK      int     `yaml:"topK"`
	AutoIndex bool    `yaml:"autoIndex"`
}

// RouteWithEmbedding 使用 Embedding 路由（辅助方法）
func (r *Router) RouteWithEmbedding(ctx context.Context, prompt string, embeddingRoute bool) []*RouteResult {
	results := r.RouteWithConfig(ctx, prompt, nil)

	// 如果用户明确要求使用 Embedding 路由
	if embeddingRoute && r.embeddingRouter != nil {
		embeddingResults, err := r.embeddingRouter.Route(ctx, prompt)
		if err == nil && len(embeddingResults) > 0 {
			// 合并结果
			merged := make(map[string]*RouteResult)
			for _, result := range results {
				merged[result.Skill.Name] = result
			}
			for _, result := range embeddingResults {
				if existing, exists := merged[result.Skill.Name]; exists {
					// 提高分数（考虑两个方法的匹配）
					if result.Score > existing.Score {
						existing.Score = result.Score
						existing.MatchedBy = existing.MatchedBy + "+" + result.MatchedBy
					}
				} else {
					merged[result.Skill.Name] = result
				}
			}

			// 转换回切片
			results = make([]*RouteResult, 0, len(merged))
			for _, result := range merged {
				results = append(results, result)
			}

			// 重新排序
			r.sortResults(results)
		}
	}

	return results
}

// sortResults 对结果排序（辅助方法）
func (r *Router) sortResults(results []*RouteResult) {
	// 按分数降序排序
	for i := 0; i < len(results)-1; i++ {
		for j := i + 1; j < len(results); j++ {
			if results[i].Score < results[j].Score {
				results[i], results[j] = results[j], results[i]
			}
		}
	}
}

// HybridRouter 混合路由器（结合关键词/模式匹配和语义匹配）
type HybridRouter struct {
	*Router
	embeddingRouter *SemanticEmbeddingRouter
	weights         RouterWeights
}

// RouterWeights 路由权重配置
type RouterWeights struct {
	Keyword   float64 // 关键词权重
	Pattern   float64 // 模式权重
	Embedding float64 // 语义权重
}

// DefaultRouterWeights 默认权重配置
func DefaultRouterWeights() RouterWeights {
	return RouterWeights{
		Keyword:   1.0,
		Pattern:   1.0,
		Embedding: 0.8,
	}
}

// NewHybridRouter 创建混合路由器
func NewHybridRouter(registry *Registry, embeddingRouter *SemanticEmbeddingRouter) *HybridRouter {
	return &HybridRouter{
		Router:          NewRouter(registry),
		embeddingRouter: embeddingRouter,
		weights:         DefaultRouterWeights(),
	}
}

// SetWeights 设置路由权重
func (hr *HybridRouter) SetWeights(weights RouterWeights) {
	hr.weights = weights
}

// RouteWithWeights 使用权重执行混合路由
func (hr *HybridRouter) RouteWithWeights(ctx context.Context, prompt string) []*RouteResult {
	var allResults []*RouteResult

	// 1. 关键词匹配
	if hr.weights.Keyword > 0 {
		processPrompt := prompt
		if !hr.caseSensitive {
			processPrompt = strings.ToLower(processPrompt)
		}
		keywordResults := hr.matchKeywords(processPrompt, prompt)
		for _, result := range keywordResults {
			result.Score *= hr.weights.Keyword
			allResults = append(allResults, result)
		}
	}

	// 2. 模式匹配
	if hr.weights.Pattern > 0 {
		processPrompt := prompt
		if !hr.caseSensitive {
			processPrompt = strings.ToLower(processPrompt)
		}
		patternResults := hr.matchPatterns(processPrompt, prompt)
		for _, result := range patternResults {
			result.Score *= hr.weights.Pattern
			allResults = append(allResults, result)
		}
	}

	// 3. 语义匹配
	if hr.weights.Embedding > 0 && hr.embeddingRouter != nil {
		embeddingResults, err := hr.embeddingRouter.Route(ctx, prompt)
		if err == nil {
			for _, result := range embeddingResults {
				result.Score *= hr.weights.Embedding
				result.MatchedBy = "embedding_weighted"
				allResults = append(allResults, result)
			}
		}
	}

	// 4. 去重并排序
	results := hr.deduplicateAndSort(allResults)

	// 5. 过滤低分结果
	if hr.minScore > 0 {
		results = hr.filterByScore(results, hr.minScore)
	}

	// 6. 限制结果数量
	if hr.maxResults > 0 && len(results) > hr.maxResults {
		results = results[:hr.maxResults]
	}

	return results
}

// GetStats 获取混合路由统计信息
func (hr *HybridRouter) GetStats() *HybridRouterStats {
	routerStats := hr.Router.GetStats()
	var embeddingStats *EmbeddingRouterStats
	if hr.embeddingRouter != nil {
		embeddingStats = hr.embeddingRouter.GetStats()
	}

	return &HybridRouterStats{
		RouterStats:    routerStats,
		EmbeddingStats: embeddingStats,
		Weights:        hr.weights,
	}
}

// HybridRouterStats 混合路由统计信息
type HybridRouterStats struct {
	*RouterStats
	EmbeddingStats *EmbeddingRouterStats `json:"embeddingStats"`
	Weights        RouterWeights         `json:"weights"`
}
