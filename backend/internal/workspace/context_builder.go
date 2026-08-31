package workspace

import (
	"fmt"
	"sort"
	"strings"
)

// ContextBuilderConfig 工作区上下文构建配置
type ContextBuilderConfig struct {
	MaxFiles      int
	MaxSymbols    int
	MaxReferences int
	MaxChunks     int
}

// DefaultContextBuilderConfig 默认上下文配置
func DefaultContextBuilderConfig() *ContextBuilderConfig {
	return &ContextBuilderConfig{
		MaxFiles:      5,
		MaxSymbols:    8,
		MaxReferences: 3,
		MaxChunks:     6,
	}
}

// WorkspaceContext 统一上下文结果
type WorkspaceContext struct {
	Query      string                 `json:"query"`
	Files      []string               `json:"files,omitempty"`
	Symbols    []SymbolInfo           `json:"symbols,omitempty"`
	References map[string][]Reference `json:"references,omitempty"`
	Chunks     []CodeChunk            `json:"chunks,omitempty"`
	Summary    string                 `json:"summary,omitempty"`
}

// ContextBuilder 工作区上下文构建器
type ContextBuilder struct {
	scan       *ScanResult
	symbols    *SymbolIndex
	references *ReferenceGraph
	config     *ContextBuilderConfig
}

// NewContextBuilder 创建工作区上下文构建器
func NewContextBuilder(scan *ScanResult, config *ContextBuilderConfig) *ContextBuilder {
	if config == nil {
		config = DefaultContextBuilderConfig()
	}
	return &ContextBuilder{
		scan:       scan,
		symbols:    NewSymbolIndex(scan),
		references: NewReferenceGraph(scan),
		config:     config,
	}
}

// NewContextBuilderWithIndexes 用预构建的符号索引和引用图创建构建器。
// 索引构建（NewSymbolIndex / NewReferenceGraph）在扫描大仓库时开销可观，
// 而搜索与引用查找对索引只读；多次请求复用同一套索引可避免重复构建。
func NewContextBuilderWithIndexes(scan *ScanResult, symbols *SymbolIndex, references *ReferenceGraph, config *ContextBuilderConfig) *ContextBuilder {
	if config == nil {
		config = DefaultContextBuilderConfig()
	}
	if symbols == nil {
		symbols = NewSymbolIndex(scan)
	}
	if references == nil {
		references = NewReferenceGraph(scan)
	}
	return &ContextBuilder{
		scan:       scan,
		symbols:    symbols,
		references: references,
		config:     config,
	}
}

// Build 构建工作区上下文
func (b *ContextBuilder) Build(query string) *WorkspaceContext {
	ctx := &WorkspaceContext{
		Query:      query,
		References: make(map[string][]Reference),
	}
	if b == nil || b.scan == nil {
		return ctx
	}

	ctx.Symbols = b.symbols.Search(query, b.config.MaxSymbols)
	ctx.Chunks = b.selectChunks(query, b.config.MaxChunks)
	ctx.Files = b.selectFiles(ctx.Chunks, ctx.Symbols, b.config.MaxFiles)
	for index, symbol := range ctx.Symbols {
		refs := b.references.References(symbol.Name, b.config.MaxReferences)
		ctx.References[symbol.Name] = refs
		ctx.Symbols[index].References = len(refs)
	}
	ctx.Summary = buildWorkspaceSummary(ctx)
	return ctx
}

func (b *ContextBuilder) selectChunks(query string, limit int) []CodeChunk {
	if b == nil || b.scan == nil || limit == 0 {
		return nil
	}
	queryTokens := tokenizeQuery(query)
	type scoredChunk struct {
		chunk CodeChunk
		score int
	}
	scored := make([]scoredChunk, 0, len(b.scan.CodeChunks))
	for _, chunk := range b.scan.CodeChunks {
		score := scoreChunk(chunk, queryTokens)
		if score > 0 {
			scored = append(scored, scoredChunk{chunk: chunk, score: score})
		}
	}

	sort.Slice(scored, func(i, j int) bool {
		if scored[i].score == scored[j].score {
			if scored[i].chunk.FilePath == scored[j].chunk.FilePath {
				return scored[i].chunk.StartLine < scored[j].chunk.StartLine
			}
			return scored[i].chunk.FilePath < scored[j].chunk.FilePath
		}
		return scored[i].score > scored[j].score
	})

	if len(scored) > limit {
		scored = scored[:limit]
	}
	chunks := make([]CodeChunk, 0, len(scored))
	for _, item := range scored {
		chunks = append(chunks, item.chunk)
	}
	return chunks
}

func (b *ContextBuilder) selectFiles(chunks []CodeChunk, symbols []SymbolInfo, limit int) []string {
	seen := make(map[string]struct{})
	files := make([]string, 0, limit)
	add := func(file string) {
		if file == "" || len(files) == limit {
			return
		}
		if _, exists := seen[file]; exists {
			return
		}
		seen[file] = struct{}{}
		files = append(files, file)
	}
	for _, chunk := range chunks {
		add(chunk.FilePath)
	}
	for _, symbol := range symbols {
		add(symbol.File)
	}
	return files
}

func tokenizeQuery(query string) []string {
	fields := strings.Fields(strings.ToLower(strings.TrimSpace(query)))
	if len(fields) == 0 {
		return nil
	}
	return fields
}

func scoreChunk(chunk CodeChunk, queryTokens []string) int {
	if len(queryTokens) == 0 {
		return 0
	}
	content := strings.ToLower(chunk.Content)
	score := 0
	for _, token := range queryTokens {
		if strings.Contains(content, token) {
			score += 2
		}
		for _, symbol := range chunk.Symbols {
			if strings.Contains(strings.ToLower(symbol.Name), token) {
				score += 3
			}
		}
	}
	return score
}

func buildWorkspaceSummary(ctx *WorkspaceContext) string {
	if ctx == nil {
		return ""
	}
	parts := []string{
		fmt.Sprintf("query=%q", ctx.Query),
		fmt.Sprintf("files=%d", len(ctx.Files)),
		fmt.Sprintf("symbols=%d", len(ctx.Symbols)),
		fmt.Sprintf("chunks=%d", len(ctx.Chunks)),
	}
	if len(ctx.Symbols) > 0 {
		names := make([]string, 0, len(ctx.Symbols))
		for _, symbol := range ctx.Symbols {
			names = append(names, symbol.Name)
		}
		parts = append(parts, "top_symbols="+strings.Join(names, ","))
	}
	return strings.Join(parts, " ")
}
