package knowledge

import (
	"context"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// LanguageAdapter 把一份文件内容翻译成符号与引用候选。
//
// 这是知识层唯一的"语言相关"接缝：tree-sitter、LSP、runtime evidence 各自实现
// 本接口后接入，索引流程（indexer.go）不需要任何改动（ADR-0007）。
type LanguageAdapter interface {
	// Name 是写入 refs.source 的生产者标识。
	Name() RefSource
	// Extract 解析文件；实现不得写入 store，也不得依赖 workspace 之外的输入。
	Extract(ctx context.Context, file FileRecord, content []byte) (Extraction, error)
}

// Extraction 是一次解析的产物。
type Extraction struct {
	Symbols []Symbol
	Refs    []pendingRef
}

// pendingRef 是尚未解析目标的引用候选（目标解析见 indexer.resolveRefs）。
type pendingRef struct {
	Name         string
	Kind         RefKind
	Line         int
	Col          int
	Snippet      string
	FromSymbolID string
}

// builtinAdapter 是零依赖的 regex 解析器，产出 confidence=heuristic 的行。
//
// 它存在的意义是让索引在任何环境都能跑通（无 tree-sitter、无 LSP），
// 同时把"这是猜测"如实写进 confidence/source，避免下游把启发式当语义事实。
type builtinAdapter struct{}

// Name 实现 LanguageAdapter。
func (builtinAdapter) Name() RefSource { return SourceBuiltin }

// symbolPattern 描述一条声明匹配规则。
type symbolPattern struct {
	re      *regexp.Regexp
	kind    SymbolKind
	nameIdx int
	// ownerIdx > 0 时该捕获组是宿主类型名（Go 方法接收者、Rust impl 目标等）。
	ownerIdx int
	// exported 判定该声明是否对外可见。
	exported func(line, name string) bool
}

var goSymbolPatterns = []symbolPattern{
	{
		re:       regexp.MustCompile(`^\s*func\s+\(\s*(?:\w+\s+)?\*?(\w+)\s*\)\s*(\w+)`),
		kind:     SymbolMethod,
		nameIdx:  2,
		ownerIdx: 1,
		exported: exportedByCase,
	},
	{
		re:       regexp.MustCompile(`^\s*func\s+(\w+)\s*[\(\[]`),
		kind:     SymbolFunction,
		nameIdx:  1,
		exported: exportedByCase,
	},
	{
		re:       regexp.MustCompile(`^\s*type\s+(\w+)\s+struct\b`),
		kind:     SymbolType,
		nameIdx:  1,
		exported: exportedByCase,
	},
	{
		re:       regexp.MustCompile(`^\s*type\s+(\w+)\s+interface\b`),
		kind:     SymbolInterface,
		nameIdx:  1,
		exported: exportedByCase,
	},
	{
		re:       regexp.MustCompile(`^\s*type\s+(\w+)\s`),
		kind:     SymbolType,
		nameIdx:  1,
		exported: exportedByCase,
	},
	{
		re:       regexp.MustCompile(`^\s*const\s+(\w+)\s`),
		kind:     SymbolConstant,
		nameIdx:  1,
		exported: exportedByCase,
	},
	{
		re:       regexp.MustCompile(`^\s*var\s+(\w+)\s`),
		kind:     SymbolVariable,
		nameIdx:  1,
		exported: exportedByCase,
	},
}

var tsSymbolPatterns = []symbolPattern{
	{
		re:       regexp.MustCompile(`^\s*(?:export\s+)?(?:default\s+)?(?:abstract\s+)?class\s+(\w+)`),
		kind:     SymbolType,
		nameIdx:  1,
		exported: exportedByKeyword,
	},
	{
		re:       regexp.MustCompile(`^\s*(?:export\s+)?interface\s+(\w+)`),
		kind:     SymbolInterface,
		nameIdx:  1,
		exported: exportedByKeyword,
	},
	{
		re:       regexp.MustCompile(`^\s*(?:export\s+)?(?:async\s+)?function\s+(\w+)`),
		kind:     SymbolFunction,
		nameIdx:  1,
		exported: exportedByKeyword,
	},
	{
		re:       regexp.MustCompile(`^\s*(?:export\s+)?const\s+(\w+)\s*=\s*(?:async\s*)?\(`),
		kind:     SymbolFunction,
		nameIdx:  1,
		exported: exportedByKeyword,
	},
	{
		re:       regexp.MustCompile(`^\s*(?:export\s+)?type\s+(\w+)\s*=`),
		kind:     SymbolType,
		nameIdx:  1,
		exported: exportedByKeyword,
	},
	{
		re:       regexp.MustCompile(`^\s*(?:export\s+)?(?:const|let|var)\s+(\w+)\s*[:=]`),
		kind:     SymbolVariable,
		nameIdx:  1,
		exported: exportedByKeyword,
	},
}

var rustSymbolPatterns = []symbolPattern{
	{
		re:       regexp.MustCompile(`^\s*(?:pub(?:\([^)]*\))?\s+)?(?:async\s+)?fn\s+(\w+)`),
		kind:     SymbolFunction,
		nameIdx:  1,
		exported: exportedByKeyword,
	},
	{
		re:       regexp.MustCompile(`^\s*(?:pub(?:\([^)]*\))?\s+)?struct\s+(\w+)`),
		kind:     SymbolType,
		nameIdx:  1,
		exported: exportedByKeyword,
	},
	{
		re:       regexp.MustCompile(`^\s*(?:pub(?:\([^)]*\))?\s+)?enum\s+(\w+)`),
		kind:     SymbolEnum,
		nameIdx:  1,
		exported: exportedByKeyword,
	},
	{
		re:       regexp.MustCompile(`^\s*(?:pub(?:\([^)]*\))?\s+)?trait\s+(\w+)`),
		kind:     SymbolInterface,
		nameIdx:  1,
		exported: exportedByKeyword,
	},
}

// callPattern 捕获调用点：标识符后紧跟左括号。
var callPattern = regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_]*)\s*\(`)

// declPrefix 用于排除"声明本身被当成调用"的行。
var declPrefix = regexp.MustCompile(`^\s*(?:pub\s+)?(?:async\s+)?(?:func|def|fn|function|class|interface|struct|type)\b`)

// callKeywords 是永远不会被当作被调用符号的关键字。
var callKeywords = map[string]bool{
	"if": true, "for": true, "while": true, "switch": true, "return": true, "func": true,
	"def": true, "fn": true, "function": true, "class": true, "struct": true, "type": true,
	"interface": true, "case": true, "catch": true, "select": true, "go": true, "defer": true,
	"new": true, "make": true, "len": false, // len 是内置函数，但确实是调用，保留
	"range": true, "import": true, "package": true, "var": true, "const": true, "let": true,
	"typeof": true, "sizeof": true, "and": true, "or": true, "not": true, "in": true,
	"is": true, "as": true, "else": true, "elif": true, "do": true, "try": true,
	"except": true, "finally": true, "with": true, "lambda": true, "yield": true,
	"await": true, "assert": true, "raise": true, "throw": true, "super": true, "this": true,
}

// Extract 实现 LanguageAdapter：按语言选择规则表，逐行匹配。
func (a builtinAdapter) Extract(ctx context.Context, file FileRecord, content []byte) (Extraction, error) {
	lines := strings.Split(string(content), "\n")
	patterns := patternsFor(file.Language)
	topLevel := topLevelOnly(file.Language)

	var out Extraction
	for idx, raw := range lines {
		if err := ctx.Err(); err != nil {
			return Extraction{}, err
		}
		line := strings.TrimRight(raw, "\r")
		lineNo := idx + 1
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "#") {
			continue
		}

		matchedDecl := false
		for _, pattern := range patterns {
			if topLevel && isIndented(line) {
				// 04 §2 Lazy：轻索引只覆盖"文件 + 顶层符号 + imports"，
				// 方法体与局部变量属于按需深索引；缩进即非顶层的可靠信号。
				break
			}
			m := pattern.re.FindStringSubmatchIndex(line)
			if m == nil {
				continue
			}
			name := capture(line, m, pattern.nameIdx)
			if name == "" {
				continue
			}
			owner := ""
			if pattern.ownerIdx > 0 {
				owner = capture(line, m, pattern.ownerIdx)
			}
			sym := buildSymbol(file, line, lineNo, name, owner, pattern)
			out.Symbols = append(out.Symbols, sym)
			matchedDecl = true
			break
		}
		if matchedDecl || declPrefix.MatchString(line) {
			continue
		}

		// 调用点：一行内可能有多个，逐个人工扫描。
		for _, loc := range callPattern.FindAllStringSubmatchIndex(line, -1) {
			name := capture(line, loc, 1)
			if name == "" || callKeywords[name] {
				continue
			}
			out.Refs = append(out.Refs, pendingRef{
				Name:    name,
				Kind:    RefCall,
				Line:    lineNo,
				Col:     loc[2],
				Snippet: truncateSnippet(trimmed),
			})
		}
	}

	// 引用属于所在符号；import 行按文件级引用处理。
	out.Refs = append(out.Refs, extractImports(file, lines)...)
	assignSymbolRanges(out.Symbols, len(lines))
	for i := range out.Refs {
		out.Refs[i].FromSymbolID = enclosingSymbol(out.Symbols, out.Refs[i].Line)
	}
	return out, nil
}

// symbolNamespace 返回 04 §4.4 要求的 namespace / package / module 分量。
//
// v1 用"文件所在目录"表达包或模块边界：Go 的包路径、Python 的包、TS 的模块目录
// 都由它区分，且同一目录内文件改名不影响身份（§4.4 的"移动"规则）。
//
// 传空串会让不同目录下的同名同签名符号（例如每个 cmd/*/main.go 里的
// `func main() {`）塌缩成同一个 stable_key，从而撞上
// idx_symbols_stable 的 workspace 级唯一约束——这正是 §4.4 所说的
// "adapter 缺陷"，实测会让约 35% 的文件整份符号写入失败。
func symbolNamespace(file FileRecord) string {
	rel := strings.ReplaceAll(file.Path, `\`, "/")
	idx := strings.LastIndex(rel, "/")
	if idx <= 0 {
		return ""
	}
	return rel[:idx]
}

// buildSymbol 组装一个符号行（stable_key 由 §4.4 的公式派生）。
func buildSymbol(file FileRecord, line string, lineNo int, name, owner string, pattern symbolPattern) Symbol {
	col := strings.Index(line, name)
	if col < 0 {
		col = 0
	}
	signature := truncateSnippet(strings.TrimSpace(line))
	qualified := name
	if owner != "" {
		qualified = owner + "." + name
	}
	hash := SignatureHash(signature)
	stableKey := StableKey(file.Language, pattern.kind, symbolNamespace(file), owner, qualified, hash)
	return Symbol{
		// ID 在解析阶段就定稿：引用解析需要用它回填 refs.from_symbol_id。
		ID:            SymbolID(file.WorkspaceID, stableKey),
		StableKey:     stableKey,
		Name:          name,
		QualifiedName: qualified,
		Kind:          pattern.kind,
		Language:      file.Language,
		Signature:     signature,
		SignatureHash: hash,
		ContentHash:   file.ContentHash,
		Range:         Range{Start: Position{Line: lineNo, Column: col}, End: Position{Line: lineNo, Column: col + len(name)}},
		IsExported:    pattern.exported != nil && pattern.exported(line, name),
		IsTest:        file.IsTest,
	}
}

// assignSymbolRanges 把符号的行跨度扩展到"下一个声明之前"，使 range 可用于取 snippet。
func assignSymbolRanges(symbols []Symbol, totalLines int) {
	if len(symbols) == 0 {
		return
	}
	order := make([]int, len(symbols))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		return symbols[order[a]].Range.Start.Line < symbols[order[b]].Range.Start.Line
	})
	for pos, idx := range order {
		end := totalLines
		if pos+1 < len(order) {
			end = symbols[order[pos+1]].Range.Start.Line - 1
		}
		if end < symbols[idx].Range.Start.Line {
			end = symbols[idx].Range.Start.Line
		}
		symbols[idx].Range.End.Line = end
	}
}

// enclosingSymbol 返回覆盖指定行的符号 id；无则返回空（文件级引用）。
func enclosingSymbol(symbols []Symbol, line int) string {
	best := ""
	bestSpan := -1
	for _, sym := range symbols {
		if line < sym.Range.Start.Line || line > sym.Range.End.Line {
			continue
		}
		span := sym.Range.End.Line - sym.Range.Start.Line
		if bestSpan == -1 || span < bestSpan {
			best, bestSpan = sym.ID, span
		}
	}
	return best
}

// patternsFor 返回语言对应的声明规则表；未知语言返回空表（不产出符号）。
func patternsFor(language string) []symbolPattern {
	switch strings.ToLower(language) {
	case "go":
		return goSymbolPatterns
	case "typescript", "javascript":
		return tsSymbolPatterns
	case "rust":
		return rustSymbolPatterns
	case "python":
		return pythonPatterns
	default:
		return nil
	}
}

// pythonPatterns 只覆盖顶层函数与类：Python 的方法归属依赖缩进作用域，
// 由 extractPythonScopes 单独处理（regex 无法可靠表达嵌套作用域）。
var pythonPatterns = []symbolPattern{
	{
		re:       regexp.MustCompile(`^def\s+(\w+)`),
		kind:     SymbolFunction,
		nameIdx:  1,
		exported: exportedByNoUnderscore,
	},
	{
		re:       regexp.MustCompile(`^class\s+(\w+)`),
		kind:     SymbolType,
		nameIdx:  1,
		exported: exportedByNoUnderscore,
	},
}

// capture 取出第 idx 个捕获组的文本；越界或未匹配返回空串。
func capture(line string, m []int, idx int) string {
	pos := idx * 2
	if pos+1 >= len(m) || m[pos] < 0 {
		return ""
	}
	return line[m[pos]:m[pos+1]]
}

// exportedByCase 按首字母大小写判定导出（Go 约定）。
func exportedByCase(_, name string) bool {
	r, _ := utf8.DecodeRuneInString(name)
	return unicode.IsUpper(r)
}

// exportedByKeyword 检查声明行是否带 export / pub 前缀。
func exportedByKeyword(line, _ string) bool {
	trimmed := strings.TrimSpace(line)
	return strings.HasPrefix(trimmed, "export ") || strings.HasPrefix(trimmed, "pub ")
}

// exportedByNoUnderscore 按 Python 约定判定：不以 '_' 开头即公开。
func exportedByNoUnderscore(_, name string) bool {
	return !strings.HasPrefix(name, "_")
}

// truncateSnippet 截断过长的签名/片段，保证单行入库。
func truncateSnippet(s string) string {
	const maxLen = 240
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}

// topLevelOnly 报告该语言的声明规则是否只接受顶格声明（行首无缩进）。
//
// 04 §2 的 Lazy 原则规定 v1 轻索引只覆盖"文件 + 顶层符号 + imports"，
// 方法体、局部变量、类型关系属于按需深索引（见 04 §2 术语表 Light/Deep Index）。
// 缩进是适配器唯一可靠且廉价的作用域信号：gofmt / prettier 之后顶层声明一律顶格，
// 而函数体内的 var/const/let 一定带缩进。实测（本仓库 3860 文件）该规则消掉
// 4295/5080 个被合并的 stable_key——它们全部是局部变量。
//
// Rust 的 impl 方法、Python 的类方法依赖缩进作用域，不适用本规则
// （pythonPatterns 自身已用 ^def/^class 锚定顶层）。
func topLevelOnly(language string) bool {
	switch strings.ToLower(language) {
	case "go", "typescript", "javascript":
		return true
	default:
		return false
	}
}

// isIndented 报告行首是否有缩进（空格或制表符）。
func isIndented(line string) bool {
	return line != "" && (line[0] == ' ' || line[0] == '\t')
}
