package embedding

import (
	"fmt"
	"regexp"
	"strings"
)

// Filter 过滤器
type Filter struct {
	Key      string      // 元数据键
	Value    any         // 期望值
	Operator FilterOperator // 操作符
}

// FilterOperator 过滤器操作符
type FilterOperator int

const (
	FilterEqual FilterOperator = iota // 等于
	FilterNotEqual                    // 不等于
	FilterContains                    // 包含
	FilterNotContains                 // 不包含
	FilterGreaterThan                 // 大于
	FilterLessThan                    // 小于
	FilterExists                      // 存在
	FilterNotExists                   // 不存在
)

// SearchConfig 搜索配置
type SearchConfig struct {
	TopK              int          // 返回结果数量
	Threshold         float32      // 相似度阈值
	Filters           []Filter     // 过滤条件
	IncludeMetadata   bool         // 是否包含元数据
	IncludeDistance   bool         // 是否包含距离信息
	Rerank            bool         // 是否重新排序
	ContextWindowSize int          // 上下文窗口大小（字符）
}

// DefaultSearchConfig 默认搜索配置
func DefaultSearchConfig() *SearchConfig {
	return &SearchConfig{
		TopK:              10,
		Threshold:         0.5,
		Filters:           nil,
		IncludeMetadata:   true,
		IncludeDistance:   true,
		Rerank:            false,
		ContextWindowSize: 1000,
	}
}

// SemanticSearchResult 语义搜索结果
type SemanticSearchResult struct {
	*SearchResult
	Context    string            // 上下文片段
	Metadata   map[string]any    // 过滤后的元数据
	Highlight  []string          // 高亮关键词
}

// SemanticSearcher 语义搜索器
type SemanticSearcher struct {
	index   *VectorIndex
	config  *SearchConfig
}

// NewSemanticSearcher 创建语义搜索器
func NewSemanticSearcher(index *VectorIndex, config *SearchConfig) (*SemanticSearcher, error) {
	if index == nil {
		return nil, fmt.Errorf("index cannot be nil")
	}

	if config == nil {
		config = DefaultSearchConfig()
	}

	return &SemanticSearcher{
		index:  index,
		config: config,
	}, nil
}

// Index 返回底层向量索引
func (ss *SemanticSearcher) Index() *VectorIndex {
	return ss.index
}

// Search 执行语义搜索
func (ss *SemanticSearcher) Search(query string) ([]*SemanticSearchResult, error) {
	// 执行基础搜索
	results, err := ss.index.Search(query, ss.config.TopK, ss.config.Threshold)
	if err != nil {
		return nil, err
	}

	// 应用过滤器
	filteredResults := ss.applyFilters(results)

	// 转换为语义搜索结果
	semanticResults := make([]*SemanticSearchResult, 0, len(filteredResults))
	for _, result := range filteredResults {
		// 提取上下文
		context := ss.extractContext(result.Item.Content, query)

		// 准备元数据
		metadata := make(map[string]any)
		if ss.config.IncludeMetadata {
			if result.Item.Metadata != nil {
				// 根据过滤器选择性地包含元数据
				for k, v := range result.Item.Metadata {
					metadata[k] = v
				}
			}
		}

		// 提取高亮
		highlights := ss.extractHighlights(result.Item.Content, query)

		semanticResult := &SemanticSearchResult{
			SearchResult: result,
			Context:      context,
			Metadata:     metadata,
			Highlight:    highlights,
		}

		// 如果不包含距离信息，则清零
		if !ss.config.IncludeDistance {
			semanticResult.Distance = 0
		}

		semanticResults = append(semanticResults, semanticResult)
	}

	// 重新排序（如果需要）
	if ss.config.Rerank {
		semanticResults = ss.rerank(semanticResults, query)
	}

	return semanticResults, nil
}

// SearchWithFilters 使用指定过滤器搜索
func (ss *SemanticSearcher) SearchWithFilters(query string, filters []Filter) ([]*SemanticSearchResult, error) {
	// 临时保存原始配置
	originalFilters := ss.config.Filters
	defer func() {
		ss.config.Filters = originalFilters
	}()

	// 应用临时过滤器
	ss.config.Filters = filters

	return ss.Search(query)
}

// CodeSearchOptions 代码搜索选项
type CodeSearchOptions struct {
	Language  string   // 编程语言
	FilePaths []string // 文件路径模式
	Symbols   []string // 符号名称
}

// SearchCode 搜索代码片段
func (ss *SemanticSearcher) SearchCode(query string, options *CodeSearchOptions) ([]*SemanticSearchResult, error) {
	// 构建过滤器
	filters := make([]Filter, 0)

	if options != nil {
		if options.Language != "" {
			filters = append(filters, Filter{
				Key:      "language",
				Value:    options.Language,
				Operator: FilterEqual,
			})
		}

		if len(options.FilePaths) > 0 {
			// 文件路径过滤器（简化实现）
			for _, pattern := range options.FilePaths {
				filters = append(filters, Filter{
					Key:      "file_path",
					Value:    pattern,
					Operator: FilterContains,
				})
			}
		}

		if len(options.Symbols) > 0 {
			// 符号过滤器（简化实现）
			for _, symbol := range options.Symbols {
				filters = append(filters, Filter{
					Key:      "symbol",
					Value:    symbol,
					Operator: FilterContains,
				})
			}
		}
	}

	return ss.SearchWithFilters(query, filters)
}

// SearchBySemantic 使用向量搜索
func (ss *SemanticSearcher) SearchBySemantic(queryEmbedding *Embedding) ([]*SemanticSearchResult, error) {
	// 执行向量搜索
	results, err := ss.index.SearchByVector(queryEmbedding, ss.config.TopK, ss.config.Threshold)
	if err != nil {
		return nil, err
	}

	// 应用过滤器
	filteredResults := ss.applyFilters(results)

	// 转换为语义搜索结果
	semanticResults := make([]*SemanticSearchResult, 0, len(filteredResults))
	for _, result := range filteredResults {
		// 简化：不提供上下文提取
		context := ss.extractContext(result.Item.Content, "")

		metadata := make(map[string]any)
		if ss.config.IncludeMetadata && result.Item.Metadata != nil {
			for k, v := range result.Item.Metadata {
				metadata[k] = v
			}
		}

		semanticResult := &SemanticSearchResult{
			SearchResult: result,
			Context:      context,
			Metadata:     metadata,
			Highlight:    []string{},
		}

		if !ss.config.IncludeDistance {
			semanticResult.Distance = 0
		}

		semanticResults = append(semanticResults, semanticResult)
	}

	return semanticResults, nil
}

// applyFilters 应用过滤器
func (ss *SemanticSearcher) applyFilters(results []*SearchResult) []*SearchResult {
	if len(ss.config.Filters) == 0 {
		return results
	}

	filtered := make([]*SearchResult, 0)
	for _, result := range results {
		if ss.matchesFilters(result.Item.Metadata) {
			filtered = append(filtered, result)
		}
	}

	return filtered
}

// matchesFilters 检查元数据是否匹配所有过滤器
func (ss *SemanticSearcher) matchesFilters(metadata map[string]any) bool {
	for _, filter := range ss.config.Filters {
		if !ss.matchFilter(metadata, filter) {
			return false
		}
	}
	return true
}

// matchFilter 检查元数据是否匹配单个过滤器
func (ss *SemanticSearcher) matchFilter(metadata map[string]any, filter Filter) bool {
	value, exists := metadata[filter.Key]

	switch filter.Operator {
	case FilterEqual:
		if !exists {
			return false
		}
		return ss.compareValues(value, filter.Value)

	case FilterNotEqual:
		if !exists {
			return true
		}
		return !ss.compareValues(value, filter.Value)

	case FilterContains:
		if !exists {
			return false
		}
		valueStr := toString(value)
		filterStr := toString(filter.Value)
		return strings.Contains(valueStr, filterStr)

	case FilterNotContains:
		if !exists {
			return true
		}
		valueStr := toString(value)
		filterStr := toString(filter.Value)
		return !strings.Contains(valueStr, filterStr)

	case FilterExists:
		return exists

	case FilterNotExists:
		return !exists

	case FilterGreaterThan:
		if !exists {
			return false
		}
		return ss.compareNumbers(value, filter.Value) > 0

	case FilterLessThan:
		if !exists {
			return false
		}
		return ss.compareNumbers(value, filter.Value) < 0

	default:
		return false
	}
}

// compareValues 比较两个值是否相等
func (ss *SemanticSearcher) compareValues(a, b any) bool {
	aStr := toString(a)
	bStr := toString(b)
	return aStr == bStr
}

// compareNumbers 比较两个数值
func (ss *SemanticSearcher) compareNumbers(a, b any) int {
	aFloat, ok1 := toFloat64(a)
	bFloat, ok2 := toFloat64(b)

	if !ok1 || !ok2 {
		return 0
	}

	if aFloat < bFloat {
		return -1
	} else if aFloat > bFloat {
		return 1
	}
	return 0
}

// toString 将值转换为字符串
func toString(value any) string {
	if value == nil {
		return ""
	}
	return fmt.Sprintf("%v", value)
}

// toFloat64 将值转换为 float64
func toFloat64(value any) (float64, bool) {
	switch v := value.(type) {
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case float32:
		return float64(v), true
	case float64:
		return v, true
	default:
		return 0, false
	}
}

// extractContext 提取上下文片段
func (ss *SemanticSearcher) extractContext(content, query string) string {
	if ss.config.ContextWindowSize <= 0 {
		return content
	}

	// 简化：返回前 N 个字符
	if len(content) <= ss.config.ContextWindowSize {
		return content
	}

	// 如果有查询，尝试查找相关上下文
	if query != "" {
		// 查找第一个匹配项
		idx := strings.Index(strings.ToLower(content), strings.ToLower(query))
		if idx >= 0 {
			// 计算上下文窗口
			start := idx - ss.config.ContextWindowSize/2
			if start < 0 {
				start = 0
			}
			end := idx + ss.config.ContextWindowSize/2
			if end > len(content) {
				end = len(content)
			}
			return content[start:end]
		}
	}

	// 默认：返回开头
	return content[:ss.config.ContextWindowSize]
}

// extractHighlights 提取高亮关键词
func (ss *SemanticSearcher) extractHighlights(content, query string) []string {
	if query == "" {
		return []string{}
	}

	highlights := make([]string, 0)

	// 提取查询中的关键词
	words := strings.Fields(query)
	for _, word := range words {
		// 过滤停用词（简化实现）
		if len(word) <= 2 {
			continue
		}

		// 检查是否在内容中
		if strings.Contains(strings.ToLower(content), strings.ToLower(word)) {
			highlights = append(highlights, word)
		}
	}

	return highlights
}

// rerank 重新排序搜索结果
func (ss *SemanticSearcher) rerank(results []*SemanticSearchResult, query string) []*SemanticSearchResult {
	// 简化实现：基于关键词匹配进行重新排序
	scores := make([]float32, len(results))
	for i, result := range results {
		scores[i] = result.Score

		// 添加关键词匹配的额外分数
		highlightCount := float32(len(result.Highlight))
		scores[i] += highlightCount * 0.1
	}

	// 根据新分数排序
	for i := 0; i < len(results)-1; i++ {
		for j := i + 1; j < len(results); j++ {
			if scores[j] > scores[i] {
				scores[i], scores[j] = scores[j], scores[i]
				results[i], results[j] = results[j], results[i]
			}
		}
	}

	return results
}

// SearchRegex 正则表达式搜索（辅助方法）
func (ss *SemanticSearcher) SearchRegex(pattern string, topK int) ([]*SemanticSearchResult, error) {
	// 编译正则表达式
	regex, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid regex pattern: %w", err)
	}

	// 获取所有项
	items := ss.index.List()

	// 筛选匹配项
	results := make([]*SemanticSearchResult, 0)
	for _, item := range items {
		if regex.MatchString(item.Content) {
			// 提取上下文
			matches := regex.FindStringIndex(item.Content)
			if matches != nil {
				start := matches[0] - ss.config.ContextWindowSize/2
				if start < 0 {
					start = 0
				}
				end := matches[1] + ss.config.ContextWindowSize/2
				if end > len(item.Content) {
					end = len(item.Content)
				}

				// 为匹配的项创建搜索结果
				metadata := make(map[string]any)
				if ss.config.IncludeMetadata && item.Metadata != nil {
					for k, v := range item.Metadata {
						metadata[k] = v
					}
				}

				// 简化：使用固定分数
				result := &SemanticSearchResult{
					SearchResult: &SearchResult{
						Item:     item,
						Score:    1.0,
						Rank:     len(results),
						Distance: 0,
					},
					Context:  item.Content[start:end],
					Metadata: metadata,
					Highlight: []string{item.Content[matches[0]:matches[1]]},
				}

				if !ss.config.IncludeDistance {
					result.Distance = 0
				}

				results = append(results, result)
			}
		}
	}

	// 限制返回数量
	if topK > 0 && topK < len(results) {
		results = results[:topK]
	}

	return results, nil
}
