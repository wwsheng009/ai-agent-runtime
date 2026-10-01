package knowledge

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Phase 5 交付 1/2/5 的执行端测试：IndexPaths（定向增量）与"增量 vs 全量等价性"。

const indexPathsSourceA = `package demo

// Target 是被其它文件引用的入口。
func Target() int {
	return helper()
}

func helper() int { return 1 }
`

const indexPathsSourceAV2 = `package demo

// TargetRenamed 替换 Target：旧符号必须随本次增量消失。
func TargetRenamed() int {
	return helper()
}

func helper() int { return 2 }
`

const indexPathsSourceB = `package demo

func Caller() int {
	return Target()
}
`

const indexPathsSourceBV2 = `package demo

func Caller() int {
	return TargetRenamed()
}
`

const indexPathsSourceC = `package demo

func Unused() int { return 0 }
`

const indexPathsSourceD = `package demo

func Added() int { return 3 }
`

func indexPathsConfig(root string) Config {
	return DefaultConfig().WithWorkspace(root)
}

// 定向增量只刷新被标记的文件：旧符号被替换、未标记文件保持原样（含其跨文件引用）。
func TestIndexPathsRefreshesOnlyMarkedFiles(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	writeTree(t, root, "demo/a.go", indexPathsSourceA)
	writeTree(t, root, "demo/b.go", indexPathsSourceB)

	store := newTestStore(t)
	cfg := indexPathsConfig(root)
	if _, err := RunIndex(ctx, store, cfg); err != nil {
		t.Fatalf("RunIndex: %v", err)
	}
	wsID, err := store.EnsureWorkspace(ctx, Workspace{RootPath: root})
	if err != nil {
		t.Fatalf("EnsureWorkspace: %v", err)
	}
	bBefore, ok, err := store.FileByPath(ctx, wsID, "demo/b.go")
	if err != nil || !ok {
		t.Fatalf("FileByPath(b.go): ok=%v err=%v", ok, err)
	}

	writeTree(t, root, "demo/a.go", indexPathsSourceAV2)
	// 相对路径形式（调用方常见形态）；重复标记必须幂等。
	result, err := IndexPaths(ctx, store, cfg, []string{"demo/a.go", "demo/a.go"})
	if err != nil {
		t.Fatalf("IndexPaths: %v", err)
	}
	if result.Scanned != 1 || result.Indexed != 1 || result.Skipped != 0 || result.Errors != 0 {
		t.Fatalf("定向增量计数不符: %+v", result)
	}

	if syms, err := store.FindSymbols(ctx, SymbolQuery{Name: "Target", Exact: true, Limit: 50}); err != nil || len(syms) != 0 {
		t.Fatalf("旧符号 Target 必须随增量消失: %+v err=%v", syms, err)
	}
	if syms, err := store.FindSymbols(ctx, SymbolQuery{Name: "TargetRenamed", Exact: true, Limit: 50}); err != nil || len(syms) == 0 {
		t.Fatalf("新符号 TargetRenamed 必须入索引: %+v err=%v", syms, err)
	}
	// 自遮蔽回归：a.go 改写后仍定义 helper，它的内部引用必须绑定到**新**符号
	// （旧符号仍在库内，不排除就会因"同名两候选"而丢绑定——等价性测试捕获的缺陷）。
	ownRefs, err := store.FindRefs(ctx, RefQuery{ToSymbolName: "helper", Limit: 50})
	if err != nil {
		t.Fatalf("FindRefs(helper): %v", err)
	}
	resolved := 0
	for _, ref := range ownRefs {
		if ref.ToSymbolID != "" {
			resolved++
		}
	}
	if resolved == 0 {
		t.Fatalf("重写文件内的同名引用必须绑定到新符号: %+v", ownRefs)
	}

	// 未标记的 b.go 不被重写（内容哈希不变）——它的引用留给"引用方被标记/全量"时刷新。
	bAfter, ok, err := store.FileByPath(ctx, wsID, "demo/b.go")
	if err != nil || !ok {
		t.Fatalf("FileByPath(b.go) after: ok=%v err=%v", ok, err)
	}
	if bAfter.ContentHash != bBefore.ContentHash {
		t.Fatalf("未标记文件不应被重写: before=%s after=%s", bBefore.ContentHash, bAfter.ContentHash)
	}

	// 边界（诚实登记）：b.go 的旧引用此刻悬空（to_symbol_id 指向已被替换掉的旧符号），
	// 直到引用方被标记或下一次全量。这不是 bug，是"按文件增量"的固有口径。
	refs, err := store.FindRefs(ctx, RefQuery{ToSymbolName: "Target", Limit: 50})
	if err != nil {
		t.Fatalf("FindRefs: %v", err)
	}
	if len(refs) == 0 {
		t.Fatal("预期 b.go 仍保留指向 Target 的旧引用（引用方未被标记）")
	}
	live := map[string]struct{}{}
	aSyms, err := store.FindSymbols(ctx, SymbolQuery{PathPrefix: "demo/a.go", Limit: 50})
	if err != nil {
		t.Fatalf("FindSymbols(a.go): %v", err)
	}
	for _, sym := range aSyms {
		live[sym.ID] = struct{}{}
	}
	dangling := 0
	for _, ref := range refs {
		if ref.ToSymbolID == "" {
			continue
		}
		if _, ok := live[ref.ToSymbolID]; !ok {
			dangling++
		}
	}
	if dangling == 0 {
		t.Fatalf("预期未标记引用方的旧引用悬空，实际无悬空: refs=%+v", refs)
	}
}

// 标记了已从磁盘删除的路径 → 软删除（与全量对账同语义），行保留。
func TestIndexPathsSoftDeletesMissingFile(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	aPath := writeTree(t, root, "demo/a.go", indexPathsSourceA)
	writeTree(t, root, "demo/b.go", indexPathsSourceB)

	store := newTestStore(t)
	cfg := indexPathsConfig(root)
	if _, err := RunIndex(ctx, store, cfg); err != nil {
		t.Fatalf("RunIndex: %v", err)
	}
	wsID, _ := store.EnsureWorkspace(ctx, Workspace{RootPath: root})

	if err := os.Remove(aPath); err != nil {
		t.Fatalf("remove: %v", err)
	}
	result, err := IndexPaths(ctx, store, cfg, []string{"demo/a.go"})
	if err != nil {
		t.Fatalf("IndexPaths: %v", err)
	}
	if result.Deleted != 1 || result.Indexed != 0 {
		t.Fatalf("删除对账不符: %+v", result)
	}

	active, err := store.ListActiveFiles(ctx, wsID)
	if err != nil {
		t.Fatalf("ListActiveFiles: %v", err)
	}
	if len(active) != 1 || active[0].Path != "demo/b.go" {
		t.Fatalf("active files = %+v, want 仅 demo/b.go", active)
	}
	rec, ok, err := store.FileByPath(ctx, wsID, "demo/a.go")
	if err != nil || !ok || rec.DeletedAt == 0 {
		t.Fatalf("软删除行必须保留（deleted_at 非零）: ok=%v rec=%+v err=%v", ok, rec, err)
	}
}

// 越界路径与不可索引后缀计入 Errors，不静默丢弃。
func TestIndexPathsRejectsOutOfWorkspaceAndUnknownExtension(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	writeTree(t, root, "demo/a.go", indexPathsSourceA)
	outside := writeTree(t, t.TempDir(), "outside.go", "package outside\n")
	writeTree(t, root, "notes.txt", "not code\n")

	store := newTestStore(t)
	cfg := indexPathsConfig(root)
	if _, err := RunIndex(ctx, store, cfg); err != nil {
		t.Fatalf("RunIndex: %v", err)
	}

	result, err := IndexPaths(ctx, store, cfg, []string{outside, "notes.txt", "  "})
	if err != nil {
		t.Fatalf("IndexPaths: %v", err)
	}
	if result.Scanned != 0 || result.Indexed != 0 || result.Errors != 3 {
		t.Fatalf("拒绝计数不符（越界/后缀/空串）: %+v", result)
	}
}

// adapter 版本不一致时定向增量整体跳过：绝不写出"新旧 adapter 混装"的库。
func TestIndexPathsSkipsOnAdapterVersionChange(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	writeTree(t, root, "demo/a.go", indexPathsSourceA)

	store := newTestStore(t)
	cfg := indexPathsConfig(root)
	if _, err := RunIndex(ctx, store, cfg); err != nil {
		t.Fatalf("RunIndex: %v", err)
	}
	wsID, _ := store.EnsureWorkspace(ctx, Workspace{RootPath: root})
	if err := store.SetWorkspaceAdapterVersion(ctx, wsID, "builtin/old"); err != nil {
		t.Fatalf("SetWorkspaceAdapterVersion: %v", err)
	}

	writeTree(t, root, "demo/a.go", indexPathsSourceAV2)
	result, err := IndexPaths(ctx, store, cfg, []string{"demo/a.go"})
	if err != nil {
		t.Fatalf("IndexPaths: %v", err)
	}
	if !result.FullRebuild || !result.AdapterDegraded || result.Indexed != 0 || result.Skipped != 1 {
		t.Fatalf("版本变化必须整体跳过并说明原因: %+v", result)
	}
	if syms, err := store.FindSymbols(ctx, SymbolQuery{Name: "Target", Exact: true, Limit: 50}); err != nil || len(syms) == 0 {
		t.Fatalf("跳过时不得写入任何符号（旧索引保持）: %+v err=%v", syms, err)
	}
}

// Phase 5 交付 5：编辑 N 次后的增量索引结果必须与"同一终态全量重建"逐行等价。
//
// 口径：所有被写过的文件（含引用方）都标记——这是变更源（edit hook / git diff）
// 的现实语义；ID 级比较成立的前提是引用方也被重索引（见上一个用例登记的悬空引用边界）。
func TestIncrementalMatchesFullRebuild(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// 同一个根（同一 wsID → 符号 ID 跨 store 可比），两个独立 store：
	// incStore 走"全量 + 增量编辑"，fullStore 只对终态做一次全量。
	root := t.TempDir()
	writeTree(t, root, "demo/a.go", indexPathsSourceA)
	writeTree(t, root, "demo/b.go", indexPathsSourceB)
	writeTree(t, root, "demo/c.go", indexPathsSourceC)

	cfg := indexPathsConfig(root)
	incStore := newTestStore(t)
	if _, err := RunIndex(ctx, incStore, cfg); err != nil {
		t.Fatalf("RunIndex(initial): %v", err)
	}

	// 编辑序列：改 2 个文件、加 1 个、删 1 个，再分两批标记（覆盖 MaxBatch 语义）。
	writeTree(t, root, "demo/a.go", indexPathsSourceAV2)
	writeTree(t, root, "demo/b.go", indexPathsSourceBV2)
	writeTree(t, root, "demo/d.go", indexPathsSourceD)
	if err := os.Remove(filepath.Join(root, "demo", "c.go")); err != nil {
		t.Fatalf("remove c.go: %v", err)
	}
	first, err := IndexPaths(ctx, incStore, cfg, []string{"demo/a.go", "demo/b.go"})
	if err != nil {
		t.Fatalf("IndexPaths(batch1): %v", err)
	}
	second, err := IndexPaths(ctx, incStore, cfg, []string{"demo/d.go", "demo/c.go"})
	if err != nil {
		t.Fatalf("IndexPaths(batch2): %v", err)
	}
	if first.Indexed != 2 || second.Indexed != 1 || second.Deleted != 1 {
		t.Fatalf("增量计数不符: first=%+v second=%+v", first, second)
	}

	// 终态全量重建（同一文件系统状态、独立 store）。
	fullStore := newTestStore(t)
	if _, err := RunIndex(ctx, fullStore, cfg); err != nil {
		t.Fatalf("RunIndex(final full): %v", err)
	}

	incProj, err := projectWorkspaceIndex(ctx, incStore, root)
	if err != nil {
		t.Fatalf("project(incremental): %v", err)
	}
	fullProj, err := projectWorkspaceIndex(ctx, fullStore, root)
	if err != nil {
		t.Fatalf("project(full): %v", err)
	}
	if diff := diffProjections(incProj, fullProj); diff != "" {
		t.Fatalf("增量与全量不等价:\n%s", diff)
	}
}

// projection 是一个 workspace 的"按文件"索引投影（ID 级，跨根可比：
// 符号 ID 由 kind+相对路径+限定名派生，不含 workspace id）。
type projection struct {
	symbols map[string][]string // relPath → 排序后的 "id|kind|name|line"
	refs    map[string][]string // relPath → 排序后的 "kind|name|line|from|to|toName"
}

func projectWorkspaceIndex(ctx context.Context, store Store, root string) (projection, error) {
	wsID, err := store.EnsureWorkspace(ctx, Workspace{RootPath: root})
	if err != nil {
		return projection{}, err
	}
	files, err := store.ListActiveFiles(ctx, wsID)
	if err != nil {
		return projection{}, err
	}
	pathByFile := make(map[string]string, len(files))
	proj := projection{symbols: map[string][]string{}, refs: map[string][]string{}}
	for _, f := range files {
		pathByFile[f.ID] = f.Path
		proj.symbols[f.Path] = []string{}
		proj.refs[f.Path] = []string{}
	}

	syms, err := store.FindSymbols(ctx, SymbolQuery{Limit: 100000})
	if err != nil {
		return projection{}, err
	}
	for _, sym := range syms {
		path, ok := pathByFile[sym.FileID]
		if !ok {
			continue
		}
		proj.symbols[path] = append(proj.symbols[path],
			fmt.Sprintf("%s|%s|%s|%d", sym.ID, sym.Kind, sym.Name, sym.Range.Start.Line))
	}
	refs, err := store.FindRefs(ctx, RefQuery{Limit: 100000})
	if err != nil {
		return projection{}, err
	}
	for _, ref := range refs {
		path, ok := pathByFile[ref.FileID]
		if !ok {
			continue
		}
		proj.refs[path] = append(proj.refs[path],
			fmt.Sprintf("%s|%s|%d|%s|%s|%s", ref.Kind, ref.ToSymbolName, ref.Line,
				ref.FromSymbolID, ref.ToSymbolID, ref.ToSymbolName))
	}
	for path := range proj.symbols {
		sort.Strings(proj.symbols[path])
		sort.Strings(proj.refs[path])
	}
	return proj, nil
}

func diffProjections(a, b projection) string {
	var out []string
	paths := map[string]struct{}{}
	for p := range a.symbols {
		paths[p] = struct{}{}
	}
	for p := range b.symbols {
		paths[p] = struct{}{}
	}
	ordered := make([]string, 0, len(paths))
	for p := range paths {
		ordered = append(ordered, p)
	}
	sort.Strings(ordered)
	for _, p := range ordered {
		if got, want := strings.Join(a.symbols[p], "\n"), strings.Join(b.symbols[p], "\n"); got != want {
			out = append(out, fmt.Sprintf("symbols[%s]:\n--- incremental ---\n%s\n--- full ---\n%s", p, got, want))
		}
		if got, want := strings.Join(a.refs[p], "\n"), strings.Join(b.refs[p], "\n"); got != want {
			out = append(out, fmt.Sprintf("refs[%s]:\n--- incremental ---\n%s\n--- full ---\n%s", p, got, want))
		}
	}
	return strings.Join(out, "\n")
}
