package ui

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// TestUIInteractiveDirectWriterInventory is the P0 baseline gate for the
// unified-render writer migration (audit:
// docs/plan/aicli-unified-render-architecture-audit-20261005.md §2/§6).
//
// It freezes every direct os.Stdout/os.Stderr touchpoint and implicit stdout
// call (fmt.Print*, TerminalOutput()) in this package's production files.
//
// L4 门禁语义重构（2026-10-08，
// docs/plan/aicli-legacy-fallback-retirement-plan-20261008.md §5）起，基线拆两组：
//
//   - sanctioned console writers（受认可白名单类，
//     uiSanctionedConsoleWriterInventory）：启动期探针/句柄初始化、TRACE/诊断通道、
//     console/plain（--compat-mode）降级承重链、平台差异。
//     类白名单 + 零新增：不得为任何新交互功能新增条目；条目只在实现退役时删除。
//   - migration debt（迁移债务，uiWriterMigrationDebtInventory）：等待整改的直写
//     （InputBox legacy 方法链、默认 stdout 绑定）。删除实现时同步摘除条目；
//     债务点位只能递减（uiWriterMigrationDebtCeiling 只降不升）。
//
// 两组并集仍做精确匹配（键与计数都不允许漂移）；分类间移动条目必须同时修改
// 对应 ceiling 常量，使重新分类在评审中可见。
func TestUIInteractiveDirectWriterInventory(t *testing.T) {
	got := collectUIDirectWriters(t)
	sanctioned := uiSanctionedConsoleWriterInventory()
	debt := uiWriterMigrationDebtInventory()
	if key := duplicateUIDirectWriterInventoryKey(sanctioned, debt); key != "" {
		t.Fatalf("inventory key registered in both classes: %s", key)
	}
	want := append(append([]uiDirectWriterInventoryEntry{}, sanctioned...), debt...)
	if diff := diffUIDirectWriterInventory(want, got); diff != "" {
		t.Fatalf("ui direct-writer inventory changed (-want +got) "+
			"[sanctioned %d entries/%d sites; debt %d entries/%d sites]:\n%s",
			len(sanctioned), uiDirectWriterInventorySites(sanctioned),
			len(debt), uiDirectWriterInventorySites(debt), diff)
	}
	if sites := uiDirectWriterInventorySites(debt); sites > uiWriterMigrationDebtCeiling {
		t.Fatalf("migration debt grew: %d sites > ceiling %d（债务只能递减；"+
			"上调前须先在计划/台账中完成重新分类并说明理由）", sites, uiWriterMigrationDebtCeiling)
	}
	if sites := uiDirectWriterInventorySites(sanctioned); sites > uiSanctionedConsoleWriterCeiling {
		t.Fatalf("sanctioned console writers grew: %d sites > ceiling %d（类白名单零新增；"+
			"新增调用点必须先经门禁评审）", sites, uiSanctionedConsoleWriterCeiling)
	}
}

// L5-1 Batch A 机械口径（2026-10-09）：启动期无租约 raw 回落退役后，受认可
// 17 条/20 点位 + 债务 4 条/4 点位 = 合计 21 条/24 点位。ceiling 只允许下调；
// 任何上调都等于重新分类，必须同步更新计划 §5 与 P0 台账 §4。
// L1-d（2026-10-09）：InputBox legacy 显示链退役后，债务 1 条/1 点位，
// 合计 18 条/21 点位。
const (
	uiSanctionedConsoleWriterCeiling = 20
	uiWriterMigrationDebtCeiling     = 1
)

type uiDirectWriter struct {
	File string
	Func string
	Kind string
	Line int
}

type uiDirectWriterInventoryEntry struct {
	File  string
	Func  string
	Kind  string
	Count int
}

func (writer uiDirectWriter) inventoryKey() string {
	return writer.File + "\t" + writer.Func + "\t" + writer.Kind
}

// uiSanctionedConsoleWriterInventory is the 受认可 console writer baseline
// (L4): the class whitelist for backend/cmd/aicli/ui/*.go. Keep exact lines
// out of this ledger: source movement must not churn it; the scanner reports
// actual lines on failure. 零新增：不得加入新调用点；条目只在实现退役时删除。
func uiSanctionedConsoleWriterInventory() []uiDirectWriterInventoryEntry {
	return []uiDirectWriterInventoryEntry{
		// 输入主链（plan §4.1 A 类）：模式序列已改经 LineEditorHooks.OnTerminalControl
		// 进入 TerminalSession（TransactionPromptEditor）；此处保留回调未认领时的
		// raw 兜底（chat_interaction.go:5382-5390 降级承重）与编辑器自有字节的
		// 单一 raw 出口 writeEditorRaw。
		{File: "inputbox_editor.go", Func: "method readPromptWithHooksContext", Kind: "os.Std*", Count: 1},
		{File: "inputbox_editor.go", Func: "writeEditorRaw", Kind: "os.Std*", Count: 1},
		// 降级/命令路径（unified 会话不可达）：no-popup/legacy 兜底与启动期错误输出。
		{File: "status.go", Func: "method Print", Kind: "os.Std*", Count: 2},
		{File: "status.go", Func: "method PrintTo", Kind: "os.Std*", Count: 1},
		// 启动期 OSC 查询（一次性探针，发生在 presenter attach 之前）。
		{File: "osc_live.go", Func: "LiveOSCProbe", Kind: "os.Std*", Count: 1},
		// 全屏/覆盖层 raw 回落已退役（L5-1 Batch A）：启动选择器改经
		// RunStartupFullScreenList 的租约流，三个 WithLease 入口 lease 必需且
		// fail-closed，此处不再有条目。
		// TERM_SESSION_TRACE 门控的会话调试追踪（诊断通道，非交互输出）。
		{File: "terminal_session.go", Func: "method flushTransactionLocked", Kind: "fmt.Print", Count: 3},
		// console/UTF8 句柄初始化与 Terminal 构造（进程启动期，非帧输出）。
		{File: "terminal_driver.go", Func: "EnsureConsoleUTF8Output", Kind: "os.Std*", Count: 1},
		{File: "terminal.go", Func: "NewTerminal", Kind: "os.Std*", Count: 1},
		// --- FixedBottomSurface 物理写族已全部退役（L3-2 删绘制实现；L3-3 删
		// clearActiveBand paint 分支），此处不再有条目。----
		// AIR_TRACE_HISTORY 诊断 sink 的一次性 misconfig 告警（诊断通道）。
		{File: "history_trace.go", Func: "method warnOnceLocked", Kind: "os.Std*", Count: 1},
		// console/plain 投影链（plan §4.1 A 类）：M2/M3/M4 输出主通道——会话信息、
		// 欢迎页、助手/系统消息、选择/分隔打印族经这些 printer 落 stdout（产品承诺）。
		{File: "info.go", Func: "writeInfoDocument", Kind: "os.Std*", Count: 1},
		// 平台差异：Unix SIGUSR2 模拟 ESC（Windows 生产派发见 keyhandler_windows.go）。
		{File: "keyhandler_unix.go", Func: "method Start", Kind: "fmt.Print", Count: 1},
		{File: "message.go", Func: "DisplayAssistantMessage", Kind: "os.Std*", Count: 1},
		{File: "message.go", Func: "method Print", Kind: "os.Std*", Count: 1},
		{File: "separator.go", Func: "PrintEmptyLine", Kind: "os.Std*", Count: 1},
		{File: "separator.go", Func: "method Print", Kind: "os.Std*", Count: 1},
		// Terminal 控制序列统一出口（经 process sink；默认 stdout 绑定属债务组）。
		{File: "terminal.go", Func: "method emitControl", Kind: "TerminalOutput()", Count: 1},
		{File: "welcome.go", Func: "PrintWelcomeWithConfig", Kind: "os.Std*", Count: 1},
	}
}

// uiWriterMigrationDebtInventory 是仍属迁移债务的直写（必须递减）：
//   - processTerminalOutput 默认 stdout 绑定：ClearIfSupported 改显式 writer 后
//     移除默认值（§4.3；proxy 保留）。
//   - InputBox legacy 显示链（Read/ReadMultiLine/Show/Update/Hide/Clear）与其渲染
//     出口 RenderInputArea/writeDoc/writeInputDocument 已于 L1-d 删除（§4.2）。
//
// 删除实现时同步摘除条目；uiWriterMigrationDebtCeiling 只降不升。
func uiWriterMigrationDebtInventory() []uiDirectWriterInventoryEntry {
	return []uiDirectWriterInventoryEntry{
		{File: "terminal_output.go", Func: "var processTerminalOutput", Kind: "os.Std*", Count: 1},
	}
}

func collectUIDirectWriters(t *testing.T) []uiDirectWriter {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	uiDir := filepath.Dir(currentFile)
	// Recursive walk: subpackages (renderengine, scene, render, …) are part
	// of the same terminal-byte surface, so a flat ui/*.go glob is a blind
	// spot (audit gap G2: 子包与包级 var 扫描盲区).
	var paths []string
	err := filepath.WalkDir(uiDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != uiDir && (entry.Name() == "testdata" || strings.HasPrefix(entry.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		paths = append(paths, path)
		return nil
	})
	if err != nil {
		t.Fatalf("walk ui sources: %v", err)
	}
	sort.Strings(paths)

	fset := token.NewFileSet()
	var writers []uiDirectWriter
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", filepath.Base(path), err)
		}
		rel, err := filepath.Rel(uiDir, path)
		if err != nil {
			t.Fatalf("rel %s: %v", path, err)
		}
		fileKey := filepath.ToSlash(rel)
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Body == nil {
					continue
				}
				name := d.Name.Name
				if d.Recv != nil {
					name = "method " + name
				}
				collectUIWritersFromNode(fset, fileKey, name, d.Body, &writers)
			case *ast.GenDecl:
				// Package-level vars can hold writers too (struct literals,
				// func literals, plain os.Std* values); without this pass they
				// never enter the ledger (audit gap G2 blind spot).
				if d.Tok != token.VAR {
					continue
				}
				for _, spec := range d.Specs {
					valueSpec, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					name := "var "
					for i, ident := range valueSpec.Names {
						if i > 0 {
							name += ","
						}
						name += ident.Name
					}
					for _, value := range valueSpec.Values {
						collectUIWritersFromNode(fset, fileKey, name, value, &writers)
					}
				}
			}
		}
	}
	sort.Slice(writers, func(i, j int) bool {
		if writers[i].File != writers[j].File {
			return writers[i].File < writers[j].File
		}
		if writers[i].Func != writers[j].Func {
			return writers[i].Func < writers[j].Func
		}
		if writers[i].Kind != writers[j].Kind {
			return writers[i].Kind < writers[j].Kind
		}
		return writers[i].Line < writers[j].Line
	})
	return writers
}

// collectUIWritersFromNode records direct-writer sites inside one AST node.
// FuncDecl bodies are attributed to the function name; package-level var
// initializers are attributed to "var <names>" so package-scope writers
// cannot hide from the gate.
func collectUIWritersFromNode(fset *token.FileSet, file string, name string, node ast.Node, writers *[]uiDirectWriter) {
	// Fd() probes (terminal size, console handles) are not byte writers.
	// Record their selector positions so the main walk can skip them and the
	// ledger stays about real output channels.
	fdProbe := map[token.Pos]bool{}
	ast.Inspect(node, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "Fd" {
			return true
		}
		if stream := uiStdStreamSelector(selector.X); stream != nil {
			fdProbe[stream.Pos()] = true
		}
		return true
	})
	ast.Inspect(node, func(n ast.Node) bool {
		kind := ""
		switch x := n.(type) {
		case *ast.SelectorExpr:
			if uiStdStreamSelector(x) != nil && !fdProbe[x.Pos()] {
				kind = "os.Std*"
			}
		case *ast.CallExpr:
			kind = uiDirectWriterCallKind(x)
		}
		if kind != "" {
			*writers = append(*writers, uiDirectWriter{
				File: file,
				Func: name,
				Kind: kind,
				Line: fset.Position(n.Pos()).Line,
			})
		}
		return true
	})
}

// uiStdStreamSelector reports whether expr is os.Stdout or os.Stderr.
func uiStdStreamSelector(expr ast.Expr) *ast.SelectorExpr {
	selector, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return nil
	}
	ident, ok := selector.X.(*ast.Ident)
	if !ok || ident.Name != "os" {
		return nil
	}
	if selector.Sel.Name != "Stdout" && selector.Sel.Name != "Stderr" {
		return nil
	}
	return selector
}

// uiDirectWriterCallKind classifies implicit-stdout calls. Explicit
// os.Stdout/os.Stderr arguments are already counted as os.Std* references by
// the selector walker, so call classification stays narrow to avoid double
// counting the same write site twice under different kinds.
func uiDirectWriterCallKind(call *ast.CallExpr) string {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "TerminalOutput" {
			return "TerminalOutput()"
		}
		return ""
	}
	if ident, ok := selector.X.(*ast.Ident); ok && ident.Name == "fmt" {
		switch selector.Sel.Name {
		case "Print", "Printf", "Println":
			return "fmt.Print"
		}
	}
	return ""
}

func diffUIDirectWriterInventory(want []uiDirectWriterInventoryEntry, got []uiDirectWriter) string {
	wantSet := make(map[string]int, len(want))
	for _, entry := range want {
		wantSet[entry.File+"\t"+entry.Func+"\t"+entry.Kind] = entry.Count
	}
	gotSet := make(map[string]int, len(got))
	gotLines := make(map[string][]int, len(got))
	for _, writer := range got {
		key := writer.inventoryKey()
		gotSet[key]++
		gotLines[key] = append(gotLines[key], writer.Line)
	}

	keys := make(map[string]bool, len(wantSet)+len(gotSet))
	for key := range wantSet {
		keys[key] = true
	}
	for key := range gotSet {
		keys[key] = true
	}
	var ordered []string
	for key := range keys {
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)

	var lines []string
	for _, key := range ordered {
		expected, actual := wantSet[key], gotSet[key]
		if expected == actual {
			continue
		}
		parts := strings.Split(key, "\t")
		lines = append(lines, fmt.Sprintf(
			"  %s\twant=%d got=%d lines=%v", key, expected, actual, gotLines[key]))
		if actual > expected {
			lines = append(lines, fmt.Sprintf(
				"    add: {File: %q, Func: %q, Kind: %q, Count: %d},",
				parts[0], parts[1], parts[2], actual))
		}
	}
	return strings.Join(lines, "\n")
}

// duplicateUIDirectWriterInventoryKey returns the first key registered in two
// classes, or "" when the classes are disjoint. Overlap would make the
// per-class accounting (and the ceiling checks) ambiguous.
func duplicateUIDirectWriterInventoryKey(
	first []uiDirectWriterInventoryEntry,
	second []uiDirectWriterInventoryEntry,
) string {
	seen := make(map[string]bool, len(first))
	for _, entry := range first {
		seen[entry.File+"\t"+entry.Func+"\t"+entry.Kind] = true
	}
	for _, entry := range second {
		key := entry.File + "\t" + entry.Func + "\t" + entry.Kind
		if seen[key] {
			return key
		}
	}
	return ""
}

// uiDirectWriterInventorySites counts physical write sites (sum of Count) in
// one class; the L4 ceilings are denominated in sites.
func uiDirectWriterInventorySites(inventory []uiDirectWriterInventoryEntry) int {
	total := 0
	for _, entry := range inventory {
		total += entry.Count
	}
	return total
}
