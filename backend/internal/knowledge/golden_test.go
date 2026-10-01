package knowledge

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Phase 4 验收门槛的测量装置（04 §5 Phase 4）。
//
// 真值（golden set）由 go/parser 从仓库子集实时生成——编译级口径，等价于
// 人工标注但不会随代码漂移过期。规模下限 200 条（门槛要求 ≥200）。
//
// 本文件测量**内置通道**（builtin 索引）的 definition 精度/召回；LSP 通道
// 见 golden_lsp_live_test.go（opt-in，需本机语言服务器）。

// goldenMinEntries 是门槛要求的真值条数下限。
const goldenMinEntries = 200

// goldenSubsetDirs 是被纳入真值的仓库子集（相对仓库根）。
// 选择标准：核心语言层 + 工具层——符号密度高、覆盖多文件多包。
var goldenSubsetDirs = []string{
	"backend/internal/knowledge",
	"backend/internal/tools",
	"backend/internal/toolkit/tools",
}

// goldenEntry 是一条定义真值；Line 为 1-based——与**索引通道**的既有口径一致
// （adapter_builtin.go:276 `lineNo := idx+1`）。注意 ADR-0006 的 canonical 是
// 0-based：语义通道（LSP）返回 0-based，接入工具面前必须显式转换（登记为
// Phase 4 遗留项）。
type goldenEntry struct {
	Name string
	Kind SymbolKind
	File string
	Line int
}

// repoRoot 从包目录回到仓库根（<root>/backend/internal/knowledge → <root>）。
func repoRoot() string { return filepath.Join("..", "..", "..") }

// buildGoldenSet 用 go/parser 解析 workspace 下的全部 .go 文件，收集顶层声明。
func buildGoldenSet(t *testing.T, workspace string) []goldenEntry {
	t.Helper()
	var entries []goldenEntry
	fset := token.NewFileSet()
	err := filepath.WalkDir(workspace, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		// 测试文件不进真值：索引虽持久化测试符号（供引用解析/诊断），
		// 但常规查询面（FindSymbols）按设计过滤 is_test=1
		// （store_sqlite_read.go:25-28），两者口径必须一致才能比较。
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(workspace, path)
		if err != nil {
			return err
		}
		rel = normalizeRelPath(rel)
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return nil // 真值只覆盖可解析文件；解析失败不进入真值。
		}
		for _, decl := range file.Decls {
			switch node := decl.(type) {
			case *ast.FuncDecl:
				kind := SymbolKind("function")
				if node.Recv != nil {
					kind = "method"
				}
				entries = append(entries, goldenEntry{
					Name: node.Name.Name, Kind: kind, File: rel,
					Line: fset.Position(node.Pos()).Line,
				})
			case *ast.GenDecl:
				for _, spec := range node.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						entries = append(entries, goldenEntry{
							Name: s.Name.Name, Kind: "type", File: rel,
							Line: fset.Position(s.Pos()).Line,
						})
					case *ast.ValueSpec:
						kind := SymbolKind("variable")
						if node.Tok == token.CONST {
							kind = "constant"
						}
						for _, ident := range s.Names {
							entries = append(entries, goldenEntry{
								Name: ident.Name, Kind: kind, File: rel,
								Line: fset.Position(ident.Pos()).Line,
							})
						}
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk golden subset: %v", err)
	}
	return entries
}

// copyGoldenSubset 把仓库子集的 .go 文件复制到 temp workspace，保持相对路径。
func copyGoldenSubset(t *testing.T, dst string) int {
	t.Helper()
	root := repoRoot()
	count := 0
	for _, dir := range goldenSubsetDirs {
		srcDir := filepath.Join(root, filepath.FromSlash(dir))
		err := filepath.WalkDir(srcDir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			target := filepath.Join(dst, filepath.FromSlash(normalizeRelPath(rel)))
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(target, data, 0o644); err != nil {
				return err
			}
			count++
			return nil
		})
		if err != nil {
			t.Fatalf("copy %s: %v", dir, err)
		}
	}
	return count
}

// TestGoldenSetSize 自检：真值规模必须 ≥ 门槛（≥200 条）。
func TestGoldenSetSize(t *testing.T) {
	root := t.TempDir()
	if files := copyGoldenSubset(t, root); files < 10 {
		t.Fatalf("子集文件数异常: %d", files)
	}
	entries := buildGoldenSet(t, root)
	if len(entries) < goldenMinEntries {
		t.Fatalf("golden set = %d 条，门槛要求 ≥%d", len(entries), goldenMinEntries)
	}
	kinds := map[SymbolKind]int{}
	for _, e := range entries {
		kinds[e.Kind]++
	}
	t.Logf("golden set: %d 条 %v", len(entries), kinds)
}

// TestGoldenDefinitionQualityBuiltin 测量内置通道的 definition 精度/召回。
//
// 口径：真值 = go/parser 顶层声明 (name,file,line)；预测 = 索引 symbols。
// precision = TP/预测数，recall = TP/真值数。数值记入 CHANGELOG；
// 断言下限只防回归（真实门槛针对 LSP 通道，见 golden_lsp_live_test.go）。
func TestGoldenDefinitionQualityBuiltin(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	copied := copyGoldenSubset(t, root)
	truth := buildGoldenSet(t, root)

	store := newTestStore(t)
	cfg := DefaultConfig().WithWorkspace(root)
	result, err := RunIndex(ctx, store, cfg)
	if err != nil {
		t.Fatalf("RunIndex: %v", err)
	}
	wsID, err := store.EnsureWorkspace(ctx, Workspace{RootPath: root})
	if err != nil {
		t.Fatalf("EnsureWorkspace: %v", err)
	}
	files, err := store.ListActiveFiles(ctx, wsID)
	if err != nil {
		t.Fatalf("ListActiveFiles: %v", err)
	}
	pathByFile := make(map[string]string, len(files))
	for _, f := range files {
		pathByFile[f.ID] = f.Path
	}
	symbols, err := store.FindSymbols(ctx, SymbolQuery{Limit: 200000})
	if err != nil {
		t.Fatalf("FindSymbols: %v", err)
	}

	truthSet := make(map[goldenKey]bool, len(truth))
	for _, e := range truth {
		truthSet[goldenKey{e.Name, e.File, e.Line}] = true
	}
	predicted := make(map[goldenKey]bool, len(symbols))
	for _, sym := range symbols {
		file := pathByFile[sym.FileID]
		if file == "" {
			continue
		}
		predicted[goldenKey{sym.Name, file, sym.Range.Start.Line}] = true
	}
	truePositives := 0
	truthByKind := map[SymbolKind]int{}
	tpByKind := map[SymbolKind]int{}
	for k := range truthSet {
		truthByKind[truthKindOf(k.name, truth)]++
	}
	for k := range predicted {
		if truthSet[k] {
			truePositives++
			tpByKind[truthKindOf(k.name, truth)]++
		}
	}
	if truePositives == 0 {
		// 系统性不匹配时给出双方样本，避免"0/0"这类不可诊断的失败。
		truthSample := make([]string, 0, 5)
		for k := range truthSet {
			if len(truthSample) >= 5 {
				break
			}
			truthSample = append(truthSample, fmt.Sprintf("%s|%s|%d", k.name, k.file, k.line))
		}
		predSample := make([]string, 0, 5)
		for k := range predicted {
			if len(predSample) >= 5 {
				break
			}
			predSample = append(predSample, fmt.Sprintf("%s|%s|%d", k.name, k.file, k.line))
		}
		t.Logf("truth sample: %v", truthSample)
		t.Logf("pred  sample: %v", predSample)
		for _, probe := range []string{"FileByPath", "Plan", "ExplorationNodeID"} {
			count := 0
			for k := range predicted {
				if k.name != probe || count >= 3 {
					continue
				}
				t.Logf("pred %s -> %s:%d", probe, k.file, k.line)
				count++
			}
			if count == 0 {
				t.Logf("pred %s -> (none)", probe)
			}
		}
	}
	precision := float64(truePositives) / float64(max(len(predicted), 1))
	recall := float64(truePositives) / float64(max(len(truthSet), 1))
	t.Logf("builtin definition: precision=%.4f recall=%.4f (copied=%d indexed=%d skipped=%d errors=%d symbols=%d truth=%d tp=%d)",
		precision, recall, copied, result.Indexed, result.Skipped, result.Errors, len(predicted), len(truthSet), truePositives)
	for kind, total := range truthByKind {
		t.Logf("  kind=%s recall=%.4f (%d/%d)", kind, float64(tpByKind[kind])/float64(max(total, 1)), tpByKind[kind], total)
	}
	if missing := missingFunctionSamples(truth, truthSet, predicted, 6); len(missing) > 0 {
		t.Logf("  missing function samples: %v", missing)
	}

	// 下限只防回归。内置通道的文档口径是"轻索引：文件 + 顶层符号 + imports"
	// （04 §2 Lazy），缩进块内的 const/var 不在范围内——因此 recall 天然低于
	// 全量真值；≥90%/85% 的门槛针对语义（LSP）通道，见 golden_lsp_live_test.go。
	if precision < 0.95 {
		t.Fatalf("builtin precision = %.4f < 0.95", precision)
	}
	if recall < 0.50 {
		t.Fatalf("builtin recall = %.4f < 0.50", recall)
	}
}

// truthKindOf 返回某个 (name,file,line) 真值条目的 kind（同名多行时取首个）。
func truthKindOf(name string, truth []goldenEntry) SymbolKind {
	for _, e := range truth {
		if e.Name == name {
			return e.Kind
		}
	}
	return SymbolKind("unknown")
}

// goldenKey 是 (name,file,line) 三元组（真值与预测共用的比较键）。
type goldenKey struct {
	name string
	file string
	line int
}

// missingFunctionSamples 返回前 n 条"真值是函数但未被索引命中"的样本，
// 用于诊断召回缺口（不参与断言）。
func missingFunctionSamples(truth []goldenEntry, truthSet, predicted map[goldenKey]bool, n int) []string {
	var out []string
	for _, e := range truth {
		if e.Kind != "function" || len(out) >= n {
			continue
		}
		if !predicted[goldenKey{e.Name, e.File, e.Line}] {
			out = append(out, fmt.Sprintf("%s|%s|%d", e.Name, e.File, e.Line))
		}
	}
	return out
}

// goldenKinds 用于稳定输出（诊断）。
func goldenKinds(entries []goldenEntry) []string {
	set := map[string]bool{}
	for _, e := range entries {
		set[string(e.Kind)] = true
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
