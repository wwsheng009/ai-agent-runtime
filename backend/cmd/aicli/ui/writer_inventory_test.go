package ui

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
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
// The inventory below is a migration-debt ledger, not an authorization:
//
//   - a new writer (or a count change) fails the gate;
//   - migrating a writer means removing its entry;
//   - do not add entries for new interactive features.
//
// Legacy non-interactive paths (plain/JSON projections, startup/shutdown) are
// still listed here so that any new caller of those helpers cannot silently
// re-open a second byte channel into the terminal.
func TestUIInteractiveDirectWriterInventory(t *testing.T) {
	got := collectUIDirectWriters(t)
	want := uiDirectWriterInventory()
	if diff := diffUIDirectWriterInventory(want, got); diff != "" {
		t.Fatalf("ui direct-writer inventory changed (-want +got):\n%s", diff)
	}
}

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

// uiDirectWriterInventory is the grouped P0 audit baseline for
// backend/cmd/aicli/ui/*.go. Keep exact lines out of this ledger: source
// movement must not churn it; the scanner reports actual lines on failure.
func uiDirectWriterInventory() []uiDirectWriterInventoryEntry {
	return []uiDirectWriterInventoryEntry{
		// --- P0 live targets: unified-render must claim these next ---
		{File: "inputbox_editor.go", Func: "method readPromptWithHooksContext", Kind: "os.Std*", Count: 3},
		{File: "inputbox_editor.go", Func: "method readPrompt", Kind: "os.Std*", Count: 3},
		{File: "inputbox_editor.go", Func: "method ReadTransientSecretPrompt", Kind: "os.Std*", Count: 2},
		// status.go Print* 仅由 no-popup/legacy 兜底分支调用（unified 会话
		// 走与 !unifiedInteractiveOutputMustFailClosed 相反的路径）。
		{File: "status.go", Func: "method Print", Kind: "os.Std*", Count: 2},
		{File: "status.go", Func: "method PrintTo", Kind: "os.Std*", Count: 1},
		// 启动期 OSC 查询（一次性探针，发生在 presenter attach 之前）。
		{File: "osc_live.go", Func: "LiveOSCProbe", Kind: "os.Std*", Count: 1},
		// 全屏/覆盖层：会话内路径已带 lease transport；raw 分支仅在 lease
		// 缺失时可达（presenter attach 之前的启动选择）。
		{File: "debug_overlay.go", Func: "RunDebugOverlayWithLease", Kind: "os.Std*", Count: 1},
		{File: "fullscreen_list.go", Func: "SelectFullScreenList", Kind: "os.Std*", Count: 1},
		{File: "fullscreen_list.go", Func: "SelectFullScreenListWithLease", Kind: "os.Std*", Count: 1},
		{File: "transcript_pager.go", Func: "RunTranscriptPagerWithLease", Kind: "os.Std*", Count: 1},
		{File: "screen_lease.go", Func: "method acquireAlternateScreenOnce", Kind: "os.Std*", Count: 1},
		{File: "screen_lease.go", Func: "method releaseAlternateScreen", Kind: "os.Std*", Count: 1},
		{File: "screen_lease.go", Func: "method writeAlternateScreen", Kind: "os.Std*", Count: 1},
		// TERM_SESSION_TRACE 门控的会话调试追踪（诊断通道，非交互输出）。
		{File: "terminal_session.go", Func: "method flushTransactionLocked", Kind: "fmt.Print", Count: 3},
		// console/UTF8 句柄初始化（进程启动期，非帧输出）。
		{File: "terminal_driver.go", Func: "EnsureConsoleUTF8Output", Kind: "os.Std*", Count: 1},
		{File: "terminal.go", Func: "NewTerminal", Kind: "os.Std*", Count: 1},
		// --- legacy surface helpers still fenced by FixedBottomSurface ----
		{File: "fixed_bottom_surface.go", Func: "method Disable", Kind: "os.Std*", Count: 1},
		{File: "fixed_bottom_surface.go", Func: "method appendOwnedDirectPaintLocked", Kind: "TerminalOutput()", Count: 1},
		{File: "fixed_bottom_surface.go", Func: "method appendOwnedDirectPaintLocked", Kind: "os.Std*", Count: 1},
		{File: "fixed_bottom_surface.go", Func: "method clearActiveBand", Kind: "TerminalOutput()", Count: 1},
		{File: "fixed_bottom_surface.go", Func: "method insertHistoryLinesInRegionLocked", Kind: "TerminalOutput()", Count: 1},
		{File: "fixed_bottom_surface.go", Func: "method writeOutput", Kind: "os.Std*", Count: 1},
		{File: "fixed_bottom_surface_snapshot.go", Func: "method renderOwnedViewportLocked", Kind: "TerminalOutput()", Count: 1},
		// --- legacy printers: production call chains are dead today, but they
		// have no fence and must never be re-wired outside the unified path ---
		{File: "history_trace.go", Func: "method warnOnceLocked", Kind: "os.Std*", Count: 1},
		{File: "info.go", Func: "writeInfoDocument", Kind: "os.Std*", Count: 1},
		{File: "input.go", Func: "PromptAssistant", Kind: "os.Std*", Count: 1},
		{File: "input.go", Func: "writeInputDocument", Kind: "os.Std*", Count: 1},
		{File: "keyhandler_unix.go", Func: "method Start", Kind: "fmt.Print", Count: 1},
		{File: "layout.go", Func: "method ClearChatArea", Kind: "os.Std*", Count: 1},
		{File: "layout.go", Func: "method PrintMessage", Kind: "os.Std*", Count: 2},
		{File: "layout.go", Func: "method PrintToChat", Kind: "os.Std*", Count: 1},
		{File: "layout.go", Func: "method Render", Kind: "os.Std*", Count: 1},
		{File: "layout.go", Func: "method RenderInputArea", Kind: "os.Std*", Count: 1},
		{File: "layout.go", Func: "method writeDoc", Kind: "os.Std*", Count: 1},
		{File: "layout.go", Func: "method writeRightAligned", Kind: "os.Std*", Count: 1},
		{File: "message.go", Func: "DisplayAssistantMessage", Kind: "os.Std*", Count: 1},
		{File: "message.go", Func: "method Print", Kind: "os.Std*", Count: 1},
		{File: "output.go", Func: "PrintCodeBlock", Kind: "os.Std*", Count: 1},
		{File: "output.go", Func: "PrintFormattedOutput", Kind: "os.Std*", Count: 1},
		{File: "progress.go", Func: "PrintProgress", Kind: "os.Std*", Count: 1},
		{File: "progress.go", Func: "PrintSpinner", Kind: "os.Std*", Count: 1},
		{File: "progress.go", Func: "method Done", Kind: "os.Std*", Count: 1},
		{File: "progress.go", Func: "method Stop", Kind: "os.Std*", Count: 1},
		{File: "progress.go", Func: "method render", Kind: "os.Std*", Count: 1},
		{File: "progress.go", Func: "method renderIfCurrent", Kind: "os.Std*", Count: 1},
		{File: "separator.go", Func: "PrintEmptyLine", Kind: "os.Std*", Count: 1},
		{File: "separator.go", Func: "method Print", Kind: "os.Std*", Count: 1},
		{File: "shell_feedback.go", Func: "DisplayShellCommand", Kind: "fmt.Print", Count: 1},
		{File: "shell_feedback.go", Func: "DisplayShellOutput", Kind: "fmt.Print", Count: 1},
		{File: "shell_feedback.go", Func: "method Print", Kind: "fmt.Print", Count: 1},
		{File: "statusbar.go", Func: "method Render", Kind: "os.Std*", Count: 1},
		{File: "statusbar.go", Func: "method RenderSimple", Kind: "os.Std*", Count: 1},
		{File: "statusbar.go", Func: "method RenderWithLayout", Kind: "os.Std*", Count: 1},
		{File: "terminal.go", Func: "method PrintAt", Kind: "TerminalOutput()", Count: 1},
		{File: "terminal.go", Func: "method emitControl", Kind: "TerminalOutput()", Count: 1},
		{File: "theme.go", Func: "method PrintBorder", Kind: "os.Std*", Count: 1},
		{File: "theme.go", Func: "method PrintSeparator", Kind: "os.Std*", Count: 1},
		{File: "toolcall.go", Func: "PrintToolCallResult", Kind: "fmt.Print", Count: 2},
		{File: "toolcall.go", Func: "PrintToolCallStart", Kind: "fmt.Print", Count: 2},
		{File: "toolcall.go", Func: "PrintToolCallsEnd", Kind: "fmt.Print", Count: 3},
		{File: "toolcall.go", Func: "PrintToolCallsStart", Kind: "fmt.Print", Count: 4},
		{File: "welcome.go", Func: "PrintGoodbye", Kind: "os.Std*", Count: 1},
		{File: "welcome.go", Func: "PrintHelp", Kind: "os.Std*", Count: 2},
		{File: "welcome.go", Func: "PrintWelcomeWithConfig", Kind: "os.Std*", Count: 1},
	}
}

func collectUIDirectWriters(t *testing.T) []uiDirectWriter {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	uiDir := filepath.Dir(currentFile)
	paths, err := filepath.Glob(filepath.Join(uiDir, "*.go"))
	if err != nil {
		t.Fatalf("glob ui sources: %v", err)
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
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			name := fn.Name.Name
			if fn.Recv != nil {
				name = "method " + name
			}
			// Fd() probes (terminal size, console handles) are not byte
			// writers. Record their selector positions so the main walk can
			// skip them and the ledger stays about real output channels.
			fdProbe := map[token.Pos]bool{}
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
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
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				kind := ""
				switch n := node.(type) {
				case *ast.SelectorExpr:
					if uiStdStreamSelector(n) != nil && !fdProbe[n.Pos()] {
						kind = "os.Std*"
					}
				case *ast.CallExpr:
					kind = uiDirectWriterCallKind(n)
				}
				if kind != "" {
					writers = append(writers, uiDirectWriter{
						File: filepath.Base(path),
						Func: name,
						Kind: kind,
						Line: fset.Position(node.Pos()).Line,
					})
				}
				return true
			})
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
