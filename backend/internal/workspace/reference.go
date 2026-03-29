package workspace

import (
	"sort"
	"strings"
)

// ReferenceKind 引用类型
type ReferenceKind string

const (
	ReferenceDeclaration ReferenceKind = "declaration"
	ReferenceUsage       ReferenceKind = "usage"
)

// Reference 统一引用信息
type Reference struct {
	Symbol  string        `json:"symbol"`
	File    string        `json:"file"`
	Line    int           `json:"line"`
	Kind    ReferenceKind `json:"kind"`
	Snippet string        `json:"snippet,omitempty"`
}

// ReferenceGraph 符号引用图
type ReferenceGraph struct {
	refs map[string][]Reference
}

type referenceSeenKey struct {
	symbol string
	file   string
	line   int
	kind   ReferenceKind
}

// NewReferenceGraph 根据扫描结果创建引用图
func NewReferenceGraph(scan *ScanResult) *ReferenceGraph {
	graph := &ReferenceGraph{
		refs: make(map[string][]Reference),
	}
	if scan == nil {
		return graph
	}

	index := NewSymbolIndex(scan)
	if index.Count() == 0 {
		return graph
	}

	symbolsByName := make(map[string][]SymbolInfo, len(index.all))
	for _, symbol := range index.all {
		name := strings.TrimSpace(symbol.Name)
		if name == "" {
			continue
		}
		symbolsByName[name] = append(symbolsByName[name], symbol)
	}

	seen := make(map[referenceSeenKey]struct{})
	for _, chunk := range scan.CodeChunks {
		lines := strings.Split(chunk.Content, "\n")
		for offset, line := range lines {
			lineNumber := chunk.StartLine + offset
			for _, symbolName := range lineSymbolNames(line, symbolsByName) {
				for _, symbol := range symbolsByName[symbolName] {
					kind := ReferenceUsage
					if symbol.File == chunk.FilePath && symbol.Line == lineNumber {
						kind = ReferenceDeclaration
					}

					key := referenceSeenKey{
						symbol: symbolName,
						file:   chunk.FilePath,
						line:   lineNumber,
						kind:   kind,
					}
					if _, exists := seen[key]; exists {
						continue
					}
					seen[key] = struct{}{}

					graph.refs[symbolName] = append(graph.refs[symbolName], Reference{
						Symbol:  symbolName,
						File:    chunk.FilePath,
						Line:    lineNumber,
						Kind:    kind,
						Snippet: strings.TrimSpace(line),
					})
				}
			}
		}
	}

	for symbol := range graph.refs {
		sort.Slice(graph.refs[symbol], func(i, j int) bool {
			if graph.refs[symbol][i].File == graph.refs[symbol][j].File {
				return graph.refs[symbol][i].Line < graph.refs[symbol][j].Line
			}
			return graph.refs[symbol][i].File < graph.refs[symbol][j].File
		})
	}

	return graph
}

func lineSymbolNames(line string, symbolsByName map[string][]SymbolInfo) []string {
	if len(symbolsByName) == 0 || line == "" {
		return nil
	}

	seen := make(map[string]struct{})
	names := make([]string, 0, 4)
	start := -1

	flush := func(end int) {
		if start < 0 || end <= start {
			return
		}
		name := line[start:end]
		start = -1
		if _, ok := symbolsByName[name]; !ok {
			return
		}
		if _, ok := seen[name]; ok {
			return
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}

	for i, r := range line {
		if isIdentifierRune(r, start >= 0) {
			if start < 0 {
				start = i
			}
			continue
		}
		flush(i)
	}
	flush(len(line))

	return names
}

func isIdentifierRune(r rune, continuation bool) bool {
	if r == '_' {
		return true
	}
	if r >= 'a' && r <= 'z' {
		return true
	}
	if r >= 'A' && r <= 'Z' {
		return true
	}
	return continuation && r >= '0' && r <= '9'
}

// References 返回符号引用
func (g *ReferenceGraph) References(symbol string, limit int) []Reference {
	if g == nil {
		return nil
	}
	refs := g.refs[symbol]
	if len(refs) == 0 {
		return nil
	}
	if limit > 0 && len(refs) > limit {
		refs = refs[:limit]
	}
	cloned := make([]Reference, len(refs))
	copy(cloned, refs)
	return cloned
}

// Count 返回符号引用数量
func (g *ReferenceGraph) Count(symbol string) int {
	if g == nil {
		return 0
	}
	return len(g.refs[symbol])
}
