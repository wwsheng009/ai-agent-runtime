package knowledge

import (
	"encoding/json"
	"os"
	"strings"

	baselsp "github.com/wwsheng009/ai-agent-runtime/internal/lsp"
)

// adapter_lsp_symbols.go 是语义通道的符号面解码（Phase 4 补齐）：把 LSP
// textDocument/documentSymbol 与 workspace/symbol 的两种返回形状统一解码成扁平的
// SemanticSymbol。
//
// 为什么单列一个文件：这两个方法的形状差异（扁平 SymbolInformation vs 层级
// DocumentSymbol）与位置查询通道（definition/references）完全不同，混在
// adapter_lsp.go 里会让那条已经稳定的路径难以阅读。
//
// 边界不变（ADR-0006 §4.4）：协议位置（UTF-16 等）的转换只在这里发生一次，
// 对外一律 canonical 0-based 行 + UTF-8 字节列，Path 是 workspace 相对路径。

// internalDefaultSymbolLimit / internalMaxSymbolLimit 是符号查询的条数边界。
// 下限防止空 query 触发"返回全部符号"的巨量响应；上限防止意外的大 limit
// 把内存和回执字节打爆。
const (
	internalDefaultSymbolLimit = 100
	internalMaxSymbolLimit     = 1000
)

// lspDocumentSymbol 同时容纳 DocumentSymbol（层级：range/selectionRange/
// children）与 SymbolInformation / WorkspaceSymbol（扁平：location/
// containerName）。两种形状字段可选，缺失即为空。
type lspDocumentSymbol struct {
	Name           string              `json:"name"`
	Kind           int                 `json:"kind"`
	Detail         string              `json:"detail"`
	ContainerName  string              `json:"containerName"`
	Location       *lspSymbolLocation  `json:"location"`
	Range          *lspRange           `json:"range"`
	SelectionRange *lspRange           `json:"selectionRange"`
	Children       []lspDocumentSymbol `json:"children"`
}

type lspSymbolLocation struct {
	URI   string   `json:"uri"`
	Range lspRange `json:"range"`
}

// maxSymbolTreeDepth 限制层级展平的递归深度：畸形或超深的 children 不得让解码
// 无限递归（也防止恶意 server 构造爆栈输入）。
const maxSymbolTreeDepth = 8

// decodeSymbols 把符号响应解码成扁平的 SemanticSymbol。
//
// content/abs 只在 documentSymbol 路径需要预读（workspace/symbol 的条目自带
// URI，无需为当前文件预读）；workspace/symbol 传 nil / ""。
func (a *lspSemanticAdapter) decodeSymbols(raw json.RawMessage, enc baselsp.PositionEncoding, content []byte, abs string) ([]SemanticSymbol, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil, nil
	}
	var nodes []lspDocumentSymbol
	if err := json.Unmarshal(raw, &nodes); err != nil {
		// 单对象返回（部分 server）也接受。
		var single lspDocumentSymbol
		if err2 := json.Unmarshal(raw, &single); err2 != nil {
			return nil, err
		}
		nodes = []lspDocumentSymbol{single}
	}

	// 逐文件内容缓存：一次 documentSymbol 会命中同一文件的多个 range，
	// 不缓存就是把同一文件读 N 次。content/abs 命中的那次直接复用已读内容。
	cache := map[string][]byte{}
	if abs != "" {
		cache[abs] = content
	}
	// 层级形状（DocumentSymbol）不带 URI——位置属于请求的那个文档。这是
	// documentSymbol 与 workspace/symbol 的关键差异：前者的 URI 来自请求参数，
	// 后者来自每个条目自身的 location。
	defaultURI := ""
	if abs != "" {
		defaultURI = baselsp.PathToURI(abs)
	}
	out := make([]SemanticSymbol, 0, len(nodes))

	var walk func(node lspDocumentSymbol, container string, depth int)
	walk = func(node lspDocumentSymbol, container string, depth int) {
		if depth > maxSymbolTreeDepth {
			return
		}
		name := strings.TrimSpace(node.Name)
		// 扁平形状自带 containerName，与展平推导出的父链取更具体的那个。
		if container == "" {
			container = strings.TrimSpace(node.ContainerName)
		}
		uri, rng := node.uriAndRange(defaultURI)
		if name != "" && uri != "" {
			if sym, ok := a.decodeOneSymbol(uri, rng, enc, name, container, node.Kind, node.Detail, cache); ok {
				out = append(out, sym)
			}
		}
		for _, child := range node.Children {
			childContainer := container
			if childContainer == "" {
				childContainer = name
			}
			walk(child, childContainer, depth+1)
		}
	}
	for _, node := range nodes {
		walk(node, "", 0)
	}
	return out, nil
}

// decodeOneSymbol 解码单个符号条目；目标文件不可读时返回 ok=false 跳过该条，
// 而不是让整次查询失败（与 decodeLocations 同口径：宁可少报，不假装精确）。
func (a *lspSemanticAdapter) decodeOneSymbol(
	uri string,
	rng lspRange,
	enc baselsp.PositionEncoding,
	name, container string,
	kind int,
	detail string,
	cache map[string][]byte,
) (SemanticSymbol, bool) {
	path := baselsp.URIToPath(uri)
	if path == "" {
		return SemanticSymbol{}, false
	}
	body, ok := cache[path]
	if !ok {
		read, err := os.ReadFile(path)
		if err != nil {
			// 目标不可读：跳过该条，不让整次查询失败（与 decodeLocations 同口径）。
			// 也无法计算 UTF-8 字节列——返回协议坐标会是错的行内偏移。
			cache[path] = nil
			return SemanticSymbol{}, false
		}
		body = read
		cache[path] = body
	}
	// 编码转换需要文件内容算 UTF-8 字节列（ADR-0006 §4.4）。
	doc := baselsp.NewDocument(uri, path, "", body)
	start := doc.Canonical(enc, baselsp.ProtocolPosition{Line: rng.Start.Line, Character: rng.Start.Character})
	end := doc.Canonical(enc, baselsp.ProtocolPosition{Line: rng.End.Line, Character: rng.End.Character})
	return SemanticSymbol{
		Name:      name,
		Kind:      semanticSymbolKind(kind),
		Detail:    strings.TrimSpace(detail),
		Container: container,
		Path:      a.relative(path),
		Line:      start.Line,
		Col:       start.Column,
		EndLine:   end.Line,
		EndCol:    end.Column,
	}, true
}

// uriAndRange 统一两种形状的位置：扁平形状用 location（自带 URI），层级形状
// 没有 URI，用请求文档的 defaultURI，range 优先 selectionRange（精确到符号名），
// 缺失时退 range（整个声明体）。
func (s lspDocumentSymbol) uriAndRange(defaultURI string) (string, lspRange) {
	if s.Location != nil && s.Location.URI != "" {
		return s.Location.URI, s.Location.Range
	}
	rng := lspRange{}
	if s.SelectionRange != nil {
		rng = *s.SelectionRange
	}
	if isZeroRange(rng) && s.Range != nil {
		rng = *s.Range
	}
	return defaultURI, rng
}

func isZeroRange(r lspRange) bool {
	return r.Start.Line == 0 && r.Start.Character == 0 &&
		r.End.Line == 0 && r.End.Character == 0
}

// semanticSymbolKind 把 LSP SymbolKind（1..26 的整数）映射到与语言无关的
// SymbolKind 闭集（ADR-0007）。未覆盖的取值归 unknown——不臆测，宁可让上层
// 看到 unknown 也不要给一个错误的分类。
func semanticSymbolKind(kind int) SymbolKind {
	switch kind {
	case 1:
		return SymbolPackage
	case 2, 3, 4, 5:
		return SymbolNamespace
	case 6:
		return SymbolMethod
	case 8:
		return SymbolField
	case 9:
		return SymbolInterface
	case 10:
		return SymbolEnum
	case 11:
		return SymbolConstant
	case 12:
		return SymbolFunction
	case 13:
		return SymbolVariable
	case 14:
		return SymbolConstant
	case 22, 23:
		return SymbolType
	case 26:
		return SymbolType
	default:
		return SymbolUnknown
	}
}

// ---- 接口断言：lspSemanticAdapter 同时实现三个语义面 ----

var (
	_ DocumentSymbolAdapter  = (*lspSemanticAdapter)(nil)
	_ WorkspaceSymbolAdapter = (*lspSemanticAdapter)(nil)
	_ SemanticAdapter        = (*lspSemanticAdapter)(nil)
)