package knowledge

import (
	"regexp"
	"strings"
)

// 导入语句的提取规则。
//
// import 是文件级引用（FromSymbolID 为空），其目标名取路径最后一段：
// 这是语言无关的近似——Go 的包名、TS 的模块名、Python 的模块名都符合该形状。
// 精确的模块解析需要构建系统信息，属于后续阶段；此处如实标记为 heuristic。
var (
	goImportLine     = regexp.MustCompile(`^\s*(?:(\w+)\s+)?"([^"]+)"\s*$`)
	goSingleImport   = regexp.MustCompile(`^\s*import\s+(?:(\w+)\s+)?"([^"]+)"`)
	goImportBlock    = regexp.MustCompile(`^\s*import\s*\(\s*$`)
	jsImportFrom     = regexp.MustCompile(`^\s*import\b[^'"]*['"]([^'"]+)['"]`)
	jsImportBare     = regexp.MustCompile(`^\s*import\s+['"]([^'"]+)['"]`)
	jsRequire        = regexp.MustCompile(`require\(\s*['"]([^'"]+)['"]\s*\)`)
	pyImportFrom     = regexp.MustCompile(`^\s*from\s+([\w.]+)\s+import\b`)
	pyImportPlain    = regexp.MustCompile(`^\s*import\s+([\w.]+)`)
	rustUse          = regexp.MustCompile(`^\s*(?:pub\s+)?use\s+([\w:]+)`)
	importPathTail   = regexp.MustCompile(`[^/\\.:]+$`)
	blockImportClose = regexp.MustCompile(`^\s*\)\s*$`)
)

// extractImports 提取文件级 import 引用。
func extractImports(file FileRecord, lines []string) []pendingRef {
	var (
		out     []pendingRef
		inBlock bool
	)
	for idx, raw := range lines {
		line := strings.TrimRight(raw, "\r")
		lineNo := idx + 1
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		switch strings.ToLower(file.Language) {
		case "go":
			if goImportBlock.MatchString(line) {
				inBlock = true
				continue
			}
			if inBlock {
				if blockImportClose.MatchString(line) {
					inBlock = false
					continue
				}
				if m := goImportLine.FindStringSubmatch(line); m != nil {
					out = appendImport(out, m[2], line, lineNo, strings.Index(line, m[2]))
				}
				continue
			}
			if m := goSingleImport.FindStringSubmatch(line); m != nil {
				out = appendImport(out, m[2], line, lineNo, strings.Index(line, m[2]))
			}
		case "typescript", "javascript":
			if m := jsImportFrom.FindStringSubmatch(line); m != nil {
				out = appendImport(out, m[1], line, lineNo, strings.Index(line, m[1]))
				continue
			}
			if m := jsImportBare.FindStringSubmatch(line); m != nil {
				out = appendImport(out, m[1], line, lineNo, strings.Index(line, m[1]))
				continue
			}
			if m := jsRequire.FindStringSubmatch(line); m != nil {
				out = appendImport(out, m[1], line, lineNo, strings.Index(line, m[1]))
			}
		case "python":
			if m := pyImportFrom.FindStringSubmatch(line); m != nil {
				out = appendImport(out, m[1], line, lineNo, strings.Index(line, m[1]))
				continue
			}
			if m := pyImportPlain.FindStringSubmatch(line); m != nil {
				out = appendImport(out, m[1], line, lineNo, strings.Index(line, m[1]))
			}
		case "rust":
			if m := rustUse.FindStringSubmatch(line); m != nil {
				out = appendImport(out, m[1], line, lineNo, strings.Index(line, m[1]))
			}
		}
	}
	return out
}

// appendImport 组装一条 import 引用；路径无法归一为标识符时跳过。
func appendImport(out []pendingRef, path, line string, lineNo, col int) []pendingRef {
	name := importName(path)
	if name == "" {
		return out
	}
	if col < 0 {
		col = 0
	}
	return append(out, pendingRef{
		Name:    name,
		Kind:    RefImport,
		Line:    lineNo,
		Col:     col,
		Snippet: truncateSnippet(strings.TrimSpace(line)),
	})
}

// importName 取导入路径的最后一段作为目标名。
//
// 例：`github.com/wwsheng009/ai-agent-runtime/internal/knowledge` → `knowledge`；
// `../lib/foo.ts` → `foo`；`fmt` → `fmt`。
func importName(path string) string {
	path = strings.TrimSpace(strings.Trim(path, `"'`))
	if path == "" {
		return ""
	}
	tail := importPathTail.FindString(path)
	tail = strings.TrimSuffix(tail, ".js")
	tail = strings.TrimSuffix(tail, ".ts")
	tail = strings.TrimSuffix(tail, ".py")
	tail = strings.TrimSuffix(tail, ".go")
	if tail == "" || !isIdentifier(tail) {
		return ""
	}
	return tail
}

// isIdentifier 报告 s 是否为合法的类标识符名字。
func isIdentifier(s string) bool {
	for i, r := range s {
		switch {
		case r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'):
		case r >= '0' && r <= '9':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return s != ""
}
