package knowledge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// IndexResult 汇总一次索引运行，供状态面与 shadow 指标消费。
type IndexResult struct {
	// Scanned 是遍历到的候选代码文件数。
	Scanned int `json:"scanned"`
	// Indexed 是本次真正解析并写入的文件数。
	Indexed int `json:"indexed"`
	// Skipped 是 content_hash 未变而跳过的文件数（增量索引的收益）。
	Skipped int `json:"skipped"`
	// Errors 是读取/解析失败的文件数；这些文件被标记为 index_state=error。
	Errors int `json:"errors"`
	// Symbols / Refs 是本次写入的行数。
	Symbols int `json:"symbols"`
	Refs    int `json:"refs"`
	// Truncated 表示预算上限提前终止；工具面据此标记结果可能陈旧（ADR-0004）。
	Truncated bool          `json:"truncated"`
	Duration  time.Duration `json:"duration_ms"`
}

// ignoreDirs 是索引永不进入的目录（与 workspace scanner 的忽略集保持一致的语义）。
var ignoreDirs = map[string]bool{
	".git": true, ".hg": true, ".svn": true,
	"node_modules": true, "vendor": true, "dist": true, "build": true,
	"target": true, "__pycache__": true, ".venv": true, "venv": true,
	".idea": true, ".vscode": true, "bin": true, "obj": true,
	".aicli": true, ".next": true, ".cache": true,
}

// codeExtensions 是内置 adapter 能识别的代码文件后缀。
var codeExtensions = map[string]string{
	".go": "go", ".ts": "typescript", ".tsx": "typescript", ".js": "javascript",
	".jsx": "javascript", ".mjs": "javascript", ".cjs": "javascript",
	".py": "python", ".rs": "rust", ".java": "java", ".c": "c", ".h": "c",
	".cc": "cpp", ".cpp": "cpp", ".cxx": "cpp", ".hpp": "cpp",
	".cs": "csharp", ".rb": "ruby", ".php": "php", ".kt": "kotlin",
	".swift": "swift", ".scala": "scala", ".sh": "shell", ".sql": "sql",
}

// maxIndexFiles 是一次运行的文件数上限，防止超大仓库把一次运行拖成不可控的长任务。
const maxIndexFiles = 20000

// RunIndex 对 cfg.Workspace 执行一次增量索引。
//
// 流程（Phase 1 的 light 通道）：
//  1. 遍历工作区，收集候选代码文件；
//  2. 以 content_hash 判增量：哈希未变的文件整体跳过（不解析、不写库）；
//  3. 解析变更文件得到符号与引用候选；
//  4. 用"本次符号 + 库内既有符号"解析引用目标，落库。
//
// 解析器当前是 regex_builtin（confidence=heuristic）；tree-sitter / LSP 通道
// 属于后续阶段，接入时只替换 LanguageAdapter，本流程不变。
func RunIndex(ctx context.Context, store Store, cfg Config) (IndexResult, error) {
	started := time.Now()
	var result IndexResult
	if store == nil {
		return result, fmt.Errorf("knowledge: index requires a store")
	}
	cfg = cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		return result, err
	}

	wsID, err := store.EnsureWorkspace(ctx, Workspace{RootPath: cfg.Workspace})
	if err != nil {
		return result, err
	}

	files, truncated, err := collectIndexableFiles(cfg)
	if err != nil {
		return result, err
	}
	result.Truncated = truncated
	result.Scanned = len(files)

	adapter := builtinAdapter{}
	// 库内既有符号：为跨文件的"旧文件 → 新文件"引用解析提供目标。
	known, err := loadKnownSymbols(ctx, store)
	if err != nil {
		return result, err
	}

	type pendingFile struct {
		rec     FileRecord
		symbols []Symbol
		refs    []pendingRef
	}
	var pending []pendingFile

	for _, path := range files {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		rec, decision, err := inspectFile(ctx, store, cfg, wsID, path)
		if err != nil {
			result.Errors++
			continue
		}
		switch decision {
		case fileUnchanged:
			result.Skipped++
			continue
		case fileRecorded:
			// 已按 index_state=error 显式登记（超限文件），不再解析。
			result.Errors++
			continue
		}
		content, err := os.ReadFile(path)
		if err != nil {
			result.Errors++
			_, _ = store.UpsertFile(ctx, rec.withState(IndexError))
			continue
		}
		extraction, err := adapter.Extract(ctx, rec, content)
		if err != nil {
			result.Errors++
			_, _ = store.UpsertFile(ctx, rec.withState(IndexError))
			continue
		}
		for i := range extraction.Symbols {
			extraction.Symbols[i].WorkspaceID = wsID
			extraction.Symbols[i].FileID = rec.ID
		}
		pending = append(pending, pendingFile{rec: rec, symbols: extraction.Symbols, refs: extraction.Refs})
	}

	// 先把本次解析出的符号并入名字表，再做引用解析：同一轮内新增的跨文件引用
	// 因此也能解析（旧文件里的引用则依赖库内既有符号表）。
	for _, pf := range pending {
		for _, sym := range pf.symbols {
			known[sym.Name] = append(known[sym.Name], sym.ID)
		}
	}

	for _, pf := range pending {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if _, err := store.UpsertFile(ctx, pf.rec.withState(IndexLight)); err != nil {
			result.Errors++
			continue
		}
		if err := store.ReplaceSymbols(ctx, pf.rec.ID, pf.symbols); err != nil {
			result.Errors++
			continue
		}
		refs := resolveRefs(wsID, pf.rec.ID, pf.refs, known)
		if err := store.ReplaceRefs(ctx, pf.rec.ID, refs); err != nil {
			result.Errors++
			continue
		}
		result.Indexed++
		result.Symbols += len(pf.symbols)
		result.Refs += len(refs)
	}

	result.Duration = time.Since(started)
	return result, nil
}

// collectIndexableFiles 返回按路径排序的候选文件绝对路径列表。
func collectIndexableFiles(cfg Config) ([]string, bool, error) {
	root := cfg.Workspace
	var (
		files     []string
		truncated bool
	)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// 单个目录不可读不应中止整次索引。
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if path == root {
				return nil
			}
			if ignoreDirs[name] || strings.HasPrefix(name, ".") {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if _, ok := codeExtensions[strings.ToLower(filepath.Ext(name))]; !ok {
			return nil
		}
		if len(files) >= maxIndexFiles {
			truncated = true
			return fs.SkipAll
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		return nil, false, fmt.Errorf("knowledge: walk workspace: %w", err)
	}
	sort.Strings(files)
	return files, truncated, nil
}

// fileDecision 是 inspectFile 对单个文件的处置结论。
type fileDecision int

const (
	// fileProcess 表示需要读取并解析该文件。
	fileProcess fileDecision = iota
	// fileUnchanged 表示内容哈希未变，整体跳过。
	fileUnchanged
	// fileRecorded 表示文件已被显式登记为不可索引（超限），无需再解析。
	fileRecorded
)

// inspectFile 读取文件元数据并与库内记录比对，判断是否可以跳过解析。
//
// 只有 mtime_ns + size 都相同才信任既有 content_hash；否则读文件算哈希——
// 这是"廉价预筛 → 精确判据"的两级策略（04 §4.3 files.content_hash 注释）。
func inspectFile(ctx context.Context, store Store, cfg Config, wsID, absPath string) (FileRecord, fileDecision, error) {
	info, err := os.Stat(absPath)
	if err != nil {
		return FileRecord{}, fileProcess, err
	}
	rel, err := filepath.Rel(cfg.Workspace, absPath)
	if err != nil {
		return FileRecord{}, fileProcess, err
	}
	rel = normalizeRelPath(rel)
	lang := codeExtensions[strings.ToLower(filepath.Ext(absPath))]

	rec := FileRecord{
		ID:          FileID(wsID, rel),
		WorkspaceID: wsID,
		Path:        rel,
		Language:    lang,
		Size:        info.Size(),
		MTimeNS:     info.ModTime().UnixNano(),
		IsTest:      isTestPath(rel),
		IsGenerated: isGeneratedPath(rel),
		IndexState:  IndexLight,
		IndexedAt:   time.Now(),
	}
	if info.Size() > cfg.MaxFileBytes {
		// 超限文件不进解析：显式记为 error，让状态面能解释"为什么没有它的符号"。
		rec.IndexState = IndexError
		_, err := store.UpsertFile(ctx, rec)
		return rec, fileRecorded, err
	}

	existing, ok, err := store.FileByPath(ctx, wsID, rel)
	if err != nil {
		return rec, fileProcess, err
	}
	if ok && existing.Size == rec.Size && existing.MTimeNS == rec.MTimeNS && existing.ContentHash != "" {
		rec.ContentHash = existing.ContentHash
		rec.IndexState = existing.IndexState
		if rec.IndexState == IndexLight || rec.IndexState == IndexDeep {
			return rec, fileUnchanged, nil
		}
	}

	content, err := os.ReadFile(absPath)
	if err != nil {
		return rec, fileProcess, err
	}
	sum := sha256.Sum256(content)
	rec.ContentHash = hex.EncodeToString(sum[:])
	if ok && existing.ContentHash == rec.ContentHash && (existing.IndexState == IndexLight || existing.IndexState == IndexDeep) {
		// mtime 变了但内容没变（checkout / touch）：跳过解析，仅刷新元数据。
		return rec, fileUnchanged, nil
	}
	return rec, fileProcess, nil
}

// loadKnownSymbols 载入库内既有符号的名字 → id 映射，供跨文件引用解析。
func loadKnownSymbols(ctx context.Context, store Store) (map[string][]string, error) {
	syms, err := store.FindSymbols(ctx, SymbolQuery{Limit: maxKnownSymbols})
	if err != nil {
		return nil, err
	}
	known := make(map[string][]string, len(syms))
	for _, sym := range syms {
		known[sym.Name] = append(known[sym.Name], sym.ID)
	}
	return known, nil
}

// maxKnownSymbols 是引用解析名字表的规模上限，防止超大仓库把内存吃满。
const maxKnownSymbols = 200000

// resolveRefs 把引用候选解析成 refs 行：目标名唯一时才绑定 to_symbol_id，
// 歧义或未知名保持未解析（ToSymbolID 为空），避免把猜测写成事实。
func resolveRefs(wsID, fileID string, pending []pendingRef, known map[string][]string) []Reference {
	out := make([]Reference, 0, len(pending))
	for _, ref := range pending {
		row := Reference{
			WorkspaceID:  wsID,
			FileID:       fileID,
			FromSymbolID: ref.FromSymbolID,
			// 名字始终记录：目标未解析时它是唯一线索。
			ToSymbolName: ref.Name,
			Kind:         ref.Kind,
			Line:         ref.Line,
			Col:          ref.Col,
			Snippet:      ref.Snippet,
			Source:       SourceBuiltin,
			Confidence:   ConfidenceFromSource(SourceBuiltin).Score(),
		}
		if ids := known[ref.Name]; len(ids) == 1 {
			row.ToSymbolID = ids[0]
		}
		row.ID = RefID(wsID, fileID, row.Line, row.Col, row.Kind)
		out = append(out, row)
	}
	return out
}

// withState 返回替换了 index_state 的副本。
func (f FileRecord) withState(state IndexState) FileRecord {
	f.IndexState = state
	return f
}

// isTestPath 按路径判定测试文件（与 04 §4.3 files.is_test 的用途一致）。
func isTestPath(rel string) bool {
	lower := strings.ToLower(rel)
	base := strings.ToLower(filepath.Base(rel))
	switch {
	case strings.HasSuffix(base, "_test.go"), strings.HasSuffix(base, "_test.py"),
		strings.HasSuffix(base, ".test.ts"), strings.HasSuffix(base, ".test.tsx"),
		strings.HasSuffix(base, ".test.js"), strings.HasSuffix(base, ".spec.ts"),
		strings.HasSuffix(base, ".spec.js"):
		return true
	case strings.Contains(lower, "/test/"), strings.Contains(lower, "/tests/"),
		strings.Contains(lower, "/__tests__/"):
		return true
	default:
		return false
	}
}

// isGeneratedPath 按路径/文件名判定生成代码。
func isGeneratedPath(rel string) bool {
	lower := strings.ToLower(rel)
	base := strings.ToLower(filepath.Base(rel))
	return strings.HasSuffix(base, ".pb.go") || strings.HasSuffix(base, "_generated.go") ||
		strings.HasSuffix(base, ".gen.ts") || strings.HasSuffix(base, ".g.dart") ||
		strings.Contains(lower, "/generated/") || strings.Contains(lower, "/gen/")
}
