package workspace

import (
	"fmt"
	"sort"
	"strings"
)

// SymbolInfo 统一符号索引项
type SymbolInfo struct {
	Name       string     `json:"name"`
	Type       SymbolType `json:"type"`
	Language   Language   `json:"language"`
	File       string     `json:"file"`
	Line       int        `json:"line"`
	LineEnd    int        `json:"line_end,omitempty"`
	References int        `json:"references,omitempty"`
}

// SymbolIndex 工作区符号索引
type SymbolIndex struct {
	byName map[string][]SymbolInfo
	byFile map[string][]SymbolInfo
	all    []SymbolInfo
}

// NewSymbolIndex 根据扫描结果创建符号索引
func NewSymbolIndex(scan *ScanResult) *SymbolIndex {
	index := &SymbolIndex{
		byName: make(map[string][]SymbolInfo),
		byFile: make(map[string][]SymbolInfo),
		all:    make([]SymbolInfo, 0),
	}
	if scan == nil {
		return index
	}

	seen := make(map[string]struct{})
	for _, chunk := range scan.CodeChunks {
		for _, symbol := range chunk.Symbols {
			key := fmt.Sprintf("%s|%s|%s|%s|%d", symbol.File, symbol.Name, symbol.Type, symbol.Language, symbol.Line)
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}

			info := SymbolInfo{
				Name:     symbol.Name,
				Type:     symbol.Type,
				Language: symbol.Language,
				File:     symbol.File,
				Line:     symbol.Line,
				LineEnd:  symbol.LineEnd,
			}
			index.all = append(index.all, info)
			index.byName[strings.ToLower(symbol.Name)] = append(index.byName[strings.ToLower(symbol.Name)], info)
			index.byFile[symbol.File] = append(index.byFile[symbol.File], info)
		}
	}

	for file := range index.byFile {
		sort.Slice(index.byFile[file], func(i, j int) bool {
			if index.byFile[file][i].Line == index.byFile[file][j].Line {
				return index.byFile[file][i].Name < index.byFile[file][j].Name
			}
			return index.byFile[file][i].Line < index.byFile[file][j].Line
		})
	}

	return index
}

// Count 返回符号总数
func (s *SymbolIndex) Count() int {
	if s == nil {
		return 0
	}
	return len(s.all)
}

// Search 按名称搜索符号
func (s *SymbolIndex) Search(query string, limit int) []SymbolInfo {
	if s == nil {
		return nil
	}

	tokens := strings.Fields(strings.ToLower(strings.TrimSpace(query)))
	if len(tokens) == 0 {
		return nil
	}

	results := make([]SymbolInfo, 0)
	for _, symbol := range s.all {
		name := strings.ToLower(symbol.Name)
		file := strings.ToLower(symbol.File)
		typ := strings.ToLower(string(symbol.Type))
		for _, token := range tokens {
			if strings.Contains(name, token) || strings.Contains(file, token) || strings.Contains(typ, token) {
				results = append(results, symbol)
				break
			}
		}
	}

	sort.Slice(results, func(i, j int) bool {
		if len(results[i].Name) == len(results[j].Name) {
			if results[i].File == results[j].File {
				return results[i].Line < results[j].Line
			}
			return results[i].File < results[j].File
		}
		return len(results[i].Name) < len(results[j].Name)
	})

	if limit > 0 && len(results) > limit {
		return results[:limit]
	}
	return results
}

// ByFile 返回指定文件的符号
func (s *SymbolIndex) ByFile(file string) []SymbolInfo {
	if s == nil {
		return nil
	}
	symbols := s.byFile[file]
	if len(symbols) == 0 {
		return nil
	}
	cloned := make([]SymbolInfo, len(symbols))
	copy(cloned, symbols)
	return cloned
}
