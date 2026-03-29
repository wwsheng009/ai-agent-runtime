package catalog

import (
	"encoding/json"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ai-gateway/ai-agent-runtime/internal/skill"
)

// Catalog 为 MCP tools 提供轻量目录化与按需搜索能力。
type Catalog struct {
	mu        sync.RWMutex
	tools     []skill.ToolInfo
	docs      []catalogDoc
	docFreq   map[string]int
	avgDocLen float64
	stats     RefreshStats
	store     SnapshotStore
}

type catalogDoc struct {
	tool                skill.ToolInfo
	normalizedName      string
	normalizedDesc      string
	nameTermFreq        map[string]int
	descriptionTermFreq map[string]int
	argTermFreq         map[string]int
	nameLen             int
	descriptionLen      int
	argLen              int
}

type RefreshStats struct {
	ToolCount     int       `json:"tool_count"`
	Added         int       `json:"added"`
	Removed       int       `json:"removed"`
	Updated       int       `json:"updated"`
	LastRefreshAt time.Time `json:"last_refresh_at"`
}

// New 创建一个空 catalog。
func New() *Catalog {
	return NewWithStore(nil)
}

// NewWithStore 创建一个带可选快照存储的 catalog。
func NewWithStore(store SnapshotStore) *Catalog {
	catalog := &Catalog{
		tools:   make([]skill.ToolInfo, 0),
		docs:    make([]catalogDoc, 0),
		docFreq: make(map[string]int),
		store:   store,
	}
	if store != nil {
		if snapshot, err := store.LoadCatalogSnapshot(); err == nil && snapshot != nil {
			catalog.rebuildLocked(snapshot.Tools, snapshot.Stats)
		}
	}
	return catalog
}

// Refresh 用当前 MCP tools 刷新目录内容。
func (c *Catalog) Refresh(tools []skill.ToolInfo) RefreshStats {
	if c == nil {
		return RefreshStats{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	stats := diffRefreshStats(c.tools, tools)
	stats.ToolCount = len(tools)
	stats.LastRefreshAt = time.Now().UTC()
	c.rebuildLocked(tools, stats)
	if c.store != nil {
		_ = c.store.SaveCatalogSnapshot(Snapshot{
			Tools: cloneTools(c.tools),
			Stats: c.stats,
		})
	}
	return stats
}

// Count 返回当前目录中的工具数量。
func (c *Catalog) Count() int {
	if c == nil {
		return 0
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.tools)
}

// RefreshStats 返回最近一次目录刷新统计。
func (c *Catalog) RefreshStats() RefreshStats {
	if c == nil {
		return RefreshStats{}
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.stats
}

// Close 关闭底层可选持久化 store。
func (c *Catalog) Close() error {
	if c == nil || c.store == nil {
		return nil
	}
	if closer, ok := c.store.(interface{ Close() error }); ok {
		return closer.Close()
	}
	return nil
}

// Search 返回与 query 最相关的 MCP tools。
func (c *Catalog) Search(query string, limit int) []skill.ToolInfo {
	if c == nil {
		return nil
	}
	if limit <= 0 {
		limit = 5
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	if len(c.tools) == 0 {
		return nil
	}
	if query = strings.TrimSpace(query); query != "" {
		if searchStore, ok := c.store.(SearchStore); ok {
			if persisted, err := searchStore.SearchCatalogTools(query, limit); err == nil && len(persisted) > 0 {
				return cloneTools(persisted)
			}
		}
	}

	queryTokens := tokenize(query)
	if len(queryTokens) == 0 {
		return cloneTools(c.tools[:min(limit, len(c.tools))])
	}

	type scored struct {
		tool         skill.ToolInfo
		score        float64
		coverage     int
		phraseHits   int
		exactNameHit bool
	}

	scoredTools := make([]scored, 0, len(c.tools))
	normalizedQuery := normalizeForSearch(query)
	for _, doc := range c.docs {
		score, coverage, phraseHits, exactNameHit := doc.score(queryTokens, normalizedQuery, c.docFreq, len(c.docs), c.avgDocLen)
		if score <= 0 {
			continue
		}
		scoredTools = append(scoredTools, scored{
			tool:         cloneTool(doc.tool),
			score:        score,
			coverage:     coverage,
			phraseHits:   phraseHits,
			exactNameHit: exactNameHit,
		})
	}

	sort.Slice(scoredTools, func(i, j int) bool {
		if scoredTools[i].exactNameHit != scoredTools[j].exactNameHit {
			return scoredTools[i].exactNameHit
		}
		if scoredTools[i].phraseHits != scoredTools[j].phraseHits {
			return scoredTools[i].phraseHits > scoredTools[j].phraseHits
		}
		if scoredTools[i].score == scoredTools[j].score {
			if scoredTools[i].coverage == scoredTools[j].coverage {
				return scoredTools[i].tool.Name < scoredTools[j].tool.Name
			}
			return scoredTools[i].coverage > scoredTools[j].coverage
		}
		return scoredTools[i].score > scoredTools[j].score
	})

	if len(scoredTools) > limit {
		scoredTools = scoredTools[:limit]
	}

	results := make([]skill.ToolInfo, 0, len(scoredTools))
	for _, item := range scoredTools {
		results = append(results, item.tool)
	}
	return results
}

func (c *Catalog) rebuildLocked(tools []skill.ToolInfo, stats RefreshStats) {
	c.tools = make([]skill.ToolInfo, 0, len(tools))
	c.docs = make([]catalogDoc, 0, len(tools))
	c.docFreq = make(map[string]int)
	totalDocLen := 0
	for _, tool := range tools {
		cloned := cloneTool(tool)
		c.tools = append(c.tools, cloned)
		doc := buildCatalogDoc(cloned)
		c.docs = append(c.docs, doc)
		totalDocLen += doc.totalLength()
		for term := range doc.uniqueTerms() {
			c.docFreq[term]++
		}
	}
	if len(c.docs) > 0 {
		c.avgDocLen = float64(totalDocLen) / float64(len(c.docs))
	} else {
		c.avgDocLen = 0
	}
	if stats.ToolCount <= 0 {
		stats.ToolCount = len(c.tools)
	}
	c.stats = stats
}

func buildCatalogDoc(tool skill.ToolInfo) catalogDoc {
	nameTokens := tokenize(tool.Name)
	descTokens := tokenize(tool.Description)
	argTokens := extractArgNames(tool.InputSchema)
	return catalogDoc{
		tool:                tool,
		normalizedName:      normalizeForSearch(tool.Name),
		normalizedDesc:      normalizeForSearch(tool.Description),
		nameTermFreq:        buildTermFreq(nameTokens),
		descriptionTermFreq: buildTermFreq(descTokens),
		argTermFreq:         buildTermFreq(argTokens),
		nameLen:             len(nameTokens),
		descriptionLen:      len(descTokens),
		argLen:              len(argTokens),
	}
}

func (d catalogDoc) totalLength() int {
	return d.nameLen + d.descriptionLen + d.argLen
}

func (d catalogDoc) uniqueTerms() map[string]bool {
	terms := make(map[string]bool, len(d.nameTermFreq)+len(d.descriptionTermFreq)+len(d.argTermFreq))
	for term := range d.nameTermFreq {
		terms[term] = true
	}
	for term := range d.descriptionTermFreq {
		terms[term] = true
	}
	for term := range d.argTermFreq {
		terms[term] = true
	}
	return terms
}

func (d catalogDoc) score(queryTokens []string, normalizedQuery string, docFreq map[string]int, totalDocs int, avgDocLen float64) (float64, int, int, bool) {
	score := 0.0
	coverage := make(map[string]bool, len(queryTokens))
	docLen := d.totalLength()
	if docLen <= 0 {
		docLen = 1
	}
	if avgDocLen <= 0 {
		avgDocLen = float64(docLen)
	}
	for _, token := range queryTokens {
		idf := tokenIDF(docFreq[token], totalDocs)
		matched := false
		if tf := d.nameTermFreq[token]; tf > 0 {
			score += bm25(tf, d.nameLen, avgDocLen, idf) * 3.5
			matched = true
		}
		if tf := d.descriptionTermFreq[token]; tf > 0 {
			score += bm25(tf, d.descriptionLen, avgDocLen, idf) * 1.8
			matched = true
		}
		if tf := d.argTermFreq[token]; tf > 0 {
			score += bm25(tf, d.argLen, avgDocLen, idf) * 1.2
			matched = true
		}
		if matched {
			coverage[token] = true
		}
	}

	phraseHits := 0
	exactNameHit := false
	if normalizedQuery != "" {
		if d.normalizedName == normalizedQuery {
			score += 12
			phraseHits++
			exactNameHit = true
		} else if strings.Contains(d.normalizedName, normalizedQuery) {
			score += 8
			phraseHits++
		}
		if strings.Contains(d.normalizedDesc, normalizedQuery) {
			score += 4
			phraseHits++
		}
	}

	if len(queryTokens) > 0 {
		score += (float64(len(coverage)) / float64(len(queryTokens))) * 2.5
	}

	return score, len(coverage), phraseHits, exactNameHit
}

func extractArgNames(schema map[string]interface{}) []string {
	if len(schema) == 0 {
		return nil
	}
	props, ok := schema["properties"].(map[string]interface{})
	if !ok {
		return nil
	}
	names := make([]string, 0, len(props))
	for name := range props {
		names = append(names, strings.ToLower(name))
	}
	sort.Strings(names)
	return names
}

func buildTermFreq(tokens []string) map[string]int {
	freq := make(map[string]int, len(tokens))
	for _, token := range tokens {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		freq[token]++
	}
	return freq
}

func tokenIDF(docFreq, totalDocs int) float64 {
	if totalDocs <= 0 {
		return 1
	}
	return math.Log(1 + (float64(totalDocs-docFreq)+0.5)/(float64(docFreq)+0.5))
}

func bm25(termFreq, fieldLen int, avgDocLen float64, idf float64) float64 {
	if termFreq <= 0 {
		return 0
	}
	if fieldLen <= 0 {
		fieldLen = 1
	}
	if avgDocLen <= 0 {
		avgDocLen = float64(fieldLen)
	}
	const (
		k1 = 1.2
		b  = 0.75
	)
	tf := float64(termFreq)
	norm := 1 - b + b*(float64(fieldLen)/avgDocLen)
	return idf * ((tf * (k1 + 1)) / (tf + k1*norm))
}

func tokenize(query string) []string {
	fields := strings.FieldsFunc(normalizeForSearch(query), func(r rune) bool {
		switch r {
		case ' ', '\n', '\t', ',', '.', ':', ';', '/', '\\', '-', '_', '(', ')', '[', ']':
			return true
		default:
			return false
		}
	})

	tokens := make([]string, 0, len(fields))
	seen := make(map[string]bool)
	for _, field := range fields {
		if len(field) < 2 || seen[field] {
			continue
		}
		seen[field] = true
		tokens = append(tokens, field)
	}
	return tokens
}

func normalizeForSearch(input string) string {
	input = strings.ToLower(strings.TrimSpace(input))
	input = strings.ReplaceAll(input, "_", " ")
	input = strings.ReplaceAll(input, "-", " ")
	return input
}

func cloneTools(tools []skill.ToolInfo) []skill.ToolInfo {
	cloned := make([]skill.ToolInfo, 0, len(tools))
	for _, tool := range tools {
		cloned = append(cloned, cloneTool(tool))
	}
	return cloned
}

func cloneTool(tool skill.ToolInfo) skill.ToolInfo {
	cloned := tool
	if len(tool.InputSchema) > 0 {
		cloned.InputSchema = make(map[string]interface{}, len(tool.InputSchema))
		for key, value := range tool.InputSchema {
			cloned.InputSchema[key] = value
		}
	}
	return cloned
}

func diffRefreshStats(previous []skill.ToolInfo, current []skill.ToolInfo) RefreshStats {
	before := make(map[string]string, len(previous))
	for _, tool := range previous {
		name := strings.TrimSpace(tool.Name)
		if name == "" {
			continue
		}
		before[name] = toolSignature(tool)
	}

	after := make(map[string]string, len(current))
	stats := RefreshStats{}
	for _, tool := range current {
		name := strings.TrimSpace(tool.Name)
		if name == "" {
			continue
		}
		signature := toolSignature(tool)
		after[name] = signature
		if previousSignature, ok := before[name]; !ok {
			stats.Added++
		} else if previousSignature != signature {
			stats.Updated++
		}
	}
	for name := range before {
		if _, ok := after[name]; !ok {
			stats.Removed++
		}
	}
	return stats
}

func toolSignature(tool skill.ToolInfo) string {
	payload, err := json.Marshal(struct {
		Name          string                 `json:"name"`
		Description   string                 `json:"description"`
		InputSchema   map[string]interface{} `json:"input_schema,omitempty"`
		MCPName       string                 `json:"mcp_name"`
		MCPTrustLevel string                 `json:"mcp_trust_level"`
		ExecutionMode string                 `json:"execution_mode"`
		Enabled       bool                   `json:"enabled"`
	}{
		Name:          tool.Name,
		Description:   tool.Description,
		InputSchema:   tool.InputSchema,
		MCPName:       tool.MCPName,
		MCPTrustLevel: tool.MCPTrustLevel,
		ExecutionMode: tool.ExecutionMode,
		Enabled:       tool.Enabled,
	})
	if err != nil {
		return ""
	}
	return string(payload)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
