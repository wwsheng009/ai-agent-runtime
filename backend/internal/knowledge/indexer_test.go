package knowledge

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// writeTree 在 root 下写入文件（自动创建目录）。
func writeTree(t *testing.T, root, rel, content string) string {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
	return path
}

const demoGoSource = `package demo

import (
	"fmt"
	"strings"
)

// OpenFile 打开文件。
func OpenFile(path string) error {
	fmt.Println(strings.TrimSpace(path))
	return helper(path)
}

type Config struct {
	Name string
}

func (c *Config) Validate() error {
	return helper(c.Name)
}

func helper(name string) error {
	return nil
}
`

func TestRunIndexExtractsSymbolsAndRefs(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeTree(t, root, "demo/demo.go", demoGoSource)
	// 调用必须独占一行：声明行会被 declPrefix 排除（避免把 `func Foo(` 当成调用）。
	writeTree(t, root, "demo/demo_test.go", "package demo\n\nimport \"testing\"\n\nfunc TestOpenFile(t *testing.T) {\n\t_ = OpenFile(\"x\")\n}\n")
	writeTree(t, root, "web/app.ts", "export function renderApp() { return mount(); }\n")
	writeTree(t, root, "node_modules/ignored.js", "function shouldNotBeIndexed() {}\n")
	writeTree(t, root, "README.md", "# not code\n")

	store := newTestStore(t)
	cfg := DefaultConfig().WithWorkspace(root)
	result, err := RunIndex(ctx, store, cfg)
	if err != nil {
		t.Fatalf("RunIndex: %v", err)
	}
	if result.Scanned != 3 {
		t.Fatalf("scanned = %d, want 3 (README 与 node_modules 必须被忽略)", result.Scanned)
	}
	if result.Indexed != 3 || result.Errors != 0 {
		t.Fatalf("indexed=%d errors=%d, want 3/0", result.Indexed, result.Errors)
	}

	wsID, err := store.EnsureWorkspace(ctx, Workspace{RootPath: root})
	if err != nil {
		t.Fatalf("EnsureWorkspace: %v", err)
	}

	// Go：函数、类型、方法都要被识别，且方法的 qualified_name 带宿主类型。
	fns, err := store.FindSymbols(ctx, SymbolQuery{Name: "OpenFile", Exact: true})
	if err != nil {
		t.Fatalf("FindSymbols(OpenFile): %v", err)
	}
	if len(fns) != 1 || fns[0].Kind != SymbolFunction || !fns[0].IsExported {
		t.Fatalf("OpenFile = %+v", fns)
	}
	methods, err := store.FindSymbols(ctx, SymbolQuery{Name: "Validate", Kind: SymbolMethod})
	if err != nil {
		t.Fatalf("FindSymbols(Validate): %v", err)
	}
	if len(methods) != 1 || methods[0].QualifiedName != "Config.Validate" {
		t.Fatalf("Validate = %+v", methods)
	}
	types, err := store.FindSymbols(ctx, SymbolQuery{Name: "Config", Exact: true, Kind: SymbolType})
	if err != nil || len(types) != 1 {
		t.Fatalf("Config = %+v (err=%v)", types, err)
	}

	// 引用：OpenFile 在测试文件里被调用，helper 有两处调用。
	openRefs, err := store.FindRefs(ctx, RefQuery{ToSymbolName: "OpenFile", Kind: RefCall})
	if err != nil {
		t.Fatalf("FindRefs(OpenFile): %v", err)
	}
	if len(openRefs) != 1 {
		t.Fatalf("OpenFile refs = %+v, want 1", openRefs)
	}
	helperRefs, err := store.FindRefs(ctx, RefQuery{ToSymbolName: "helper"})
	if err != nil {
		t.Fatalf("FindRefs(helper): %v", err)
	}
	if len(helperRefs) != 2 {
		t.Fatalf("helper refs = %d, want 2", len(helperRefs))
	}
	// 跨文件引用必须解析到唯一的同名符号（同文件内定义的 helper 是唯一候选）。
	for _, ref := range helperRefs {
		if ref.ToSymbolID == "" {
			t.Fatalf("helper ref 未解析目标: %+v", ref)
		}
		if ref.Source != SourceBuiltin || ref.Confidence != ConfidenceHeuristic.Score() {
			t.Fatalf("ref 未如实标注启发式来源: %+v", ref)
		}
	}

	// import 作为文件级引用入库。
	fmtImports, err := store.FindRefs(ctx, RefQuery{Kind: RefImport, ToSymbolName: "fmt"})
	if err != nil {
		t.Fatalf("FindRefs(import fmt): %v", err)
	}
	if len(fmtImports) != 1 {
		t.Fatalf("import fmt = %d, want 1", len(fmtImports))
	}
	stringsImports, err := store.FindRefs(ctx, RefQuery{Kind: RefImport, ToSymbolName: "strings"})
	if err != nil || len(stringsImports) != 1 {
		t.Fatalf("import strings = %+v (err=%v)", stringsImports, err)
	}

	// TypeScript 符号也要入库。
	tsSyms, err := store.FindSymbols(ctx, SymbolQuery{Name: "renderApp"})
	if err != nil || len(tsSyms) != 1 {
		t.Fatalf("renderApp = %+v (err=%v)", tsSyms, err)
	}

	stats, err := store.Stats(ctx, wsID)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Files != 3 || stats.Symbols == 0 || stats.Refs == 0 || stats.IndexedAt == 0 {
		t.Fatalf("stats = %+v", stats)
	}
}

func TestRunIndexIsIncremental(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := writeTree(t, root, "demo/demo.go", demoGoSource)

	store := newTestStore(t)
	cfg := DefaultConfig().WithWorkspace(root)

	first, err := RunIndex(ctx, store, cfg)
	if err != nil {
		t.Fatalf("RunIndex(first): %v", err)
	}
	if first.Indexed != 1 {
		t.Fatalf("first.Indexed = %d, want 1", first.Indexed)
	}

	// 无改动：mtime/size 未变，必须整体跳过解析。
	second, err := RunIndex(ctx, store, cfg)
	if err != nil {
		t.Fatalf("RunIndex(second): %v", err)
	}
	if second.Indexed != 0 || second.Skipped != 1 {
		t.Fatalf("second = %+v, want indexed=0 skipped=1", second)
	}

	// 内容变化：必须重新索引，且旧符号被替换而不是累积。
	writeTree(t, root, "demo/demo.go", demoGoSource+"\nfunc Added() {}\n")
	third, err := RunIndex(ctx, store, cfg)
	if err != nil {
		t.Fatalf("RunIndex(third): %v", err)
	}
	if third.Indexed != 1 {
		t.Fatalf("third = %+v, want indexed=1", third)
	}
	added, err := store.FindSymbols(ctx, SymbolQuery{Name: "Added", Exact: true})
	if err != nil || len(added) != 1 {
		t.Fatalf("Added = %+v (err=%v)", added, err)
	}
	open, err := store.FindSymbols(ctx, SymbolQuery{Name: "OpenFile", Exact: true})
	if err != nil || len(open) != 1 {
		t.Fatalf("OpenFile after reindex = %+v (err=%v)", open, err)
	}
	if path == "" {
		t.Fatal("unreachable")
	}
}

func TestRunIndexMarksOversizedFileAsError(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	big := make([]byte, 0, 2048)
	for i := 0; i < 2048; i++ {
		big = append(big, 'x')
	}
	writeTree(t, root, "demo/big.go", string(big))

	store := newTestStore(t)
	cfg := DefaultConfig().WithWorkspace(root)
	cfg.MaxFileBytes = 1024

	if _, err := RunIndex(ctx, store, cfg); err != nil {
		t.Fatalf("RunIndex: %v", err)
	}
	wsID, _ := store.EnsureWorkspace(ctx, Workspace{RootPath: root})
	rec, ok, err := store.FileByPath(ctx, wsID, "demo/big.go")
	if err != nil || !ok {
		t.Fatalf("FileByPath: ok=%v err=%v", ok, err)
	}
	if rec.IndexState != IndexError {
		t.Fatalf("index_state = %q, want %q", rec.IndexState, IndexError)
	}
}

func TestBuiltinAdapterHandlesPythonAndRust(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeTree(t, root, "svc/main.py", "import os\nfrom pkg.util import helper\n\n\ndef run():\n    return helper()\n\n\nclass Worker:\n    pass\n")
	writeTree(t, root, "svc/lib.rs", "use std::fmt;\n\npub fn compute() -> i32 { 1 }\n\nstruct Job { id: u32 }\n")

	store := newTestStore(t)
	cfg := DefaultConfig().WithWorkspace(root)
	if _, err := RunIndex(ctx, store, cfg); err != nil {
		t.Fatalf("RunIndex: %v", err)
	}

	run, err := store.FindSymbols(ctx, SymbolQuery{Name: "run", Exact: true})
	if err != nil || len(run) != 1 || run[0].Language != "python" {
		t.Fatalf("run = %+v (err=%v)", run, err)
	}
	worker, err := store.FindSymbols(ctx, SymbolQuery{Name: "Worker", Exact: true, Kind: SymbolType})
	if err != nil || len(worker) != 1 {
		t.Fatalf("Worker = %+v (err=%v)", worker, err)
	}
	compute, err := store.FindSymbols(ctx, SymbolQuery{Name: "compute", Exact: true})
	if err != nil || len(compute) != 1 || !compute[0].IsExported {
		t.Fatalf("compute = %+v (err=%v)", compute, err)
	}

	imports, err := store.FindRefs(ctx, RefQuery{Kind: RefImport})
	if err != nil {
		t.Fatalf("FindRefs(imports): %v", err)
	}
	if len(imports) < 3 {
		t.Fatalf("imports = %d, want >= 3 (os, helper, fmt)", len(imports))
	}
}
