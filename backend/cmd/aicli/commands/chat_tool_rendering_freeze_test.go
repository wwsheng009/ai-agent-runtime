package commands

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// TestToolRenderStringAPICallSitesFrozen 用源码级调用点白名单冻结工具渲染的
// 字符串 / 不透明入口。权威装配已收敛到 compactToolRequestedBlock /
// compactToolCompletedBlock 与结构化注入 API（架构文档 §16.2）：
//
//   - renderSharedChatToolEvent：只允许 decode / 兼容回退路径调用；
//   - SubmitToolResultDisplay：只允许旧事件日志与空白归一差异回退调用；
//   - bridge 的 submitToolRequested / submitToolResultDisplay 兼容包装：
//     生产代码不得再调用（coordinator 已改用 Block 变体），仅在测试与旧路径
//     文档中保留。
//
// 新增调用点必须先证明它属于 decode/兼容路径，并同步本白名单与 §16.2。
func TestToolRenderStringAPICallSitesFrozen(t *testing.T) {
	sites := collectToolRenderStringCallSites(t)
	got := map[string]int{}
	for _, site := range sites {
		got[site.String()]++
	}
	want := map[string]int{
		// Running 行的懒渲染回退（newToolChainCell / withCompleted 旧构造路径）。
		"chat_history_cell.go :: method (toolChainCell) DisplayLines": 1,
		// batch_start / batch_end / 未知 stage 的兼容兜底。
		"chat_interaction.go :: method (*chatInteractionCoordinator) renderToolChainEvent": 1,
		// 无 coordinator 交互会话中非 tool_result stage 的兼容投影。
		"chat_transcript_renderer.go :: method (*aicliTranscriptRenderer) RenderToolEvent": 1,
		// 历史种子：旧日志或空白归一差异下的不透明导入回退。
		"chat_history_reconcile.go :: method (persistedHistorySeedUnit) apply":       1,
		"chat_history_reconcile.go :: method (persistedHistorySeedUnit) applyBefore": 1,
		// bridge 兼容包装（生产调用方已迁移，仅测试使用）。
		"chat_runtime_events.go :: method (*chatRuntimeEventBridge) submitToolResultDisplay": 1,
	}
	if !reflect.DeepEqual(got, want) {
		var lines []string
		for _, site := range sites {
			lines = append(lines, fmt.Sprintf("  %s:%d %s", site.File, site.Line, site.Func))
		}
		t.Fatalf("工具渲染字符串入口调用点已变化（白名单语义见测试注释）\n got: %#v\nwant: %#v\n当前调用点:\n%s",
			got, want, strings.Join(lines, "\n"))
	}
}

type toolRenderCallSite struct {
	File string
	Func string
	Line int
}

func (s toolRenderCallSite) String() string { return s.File + " :: " + s.Func }

// toolRenderStringAPINames 是需要冻结的生产代码入口；测试文件不参与扫描。
var toolRenderStringAPINames = map[string]bool{
	"renderSharedChatToolEvent": true,
	"SubmitToolResultDisplay":   true,
	"submitToolRequested":       true,
	"submitToolResultDisplay":   true,
}

func collectToolRenderStringCallSites(t *testing.T) []toolRenderCallSite {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	commandsDir := filepath.Dir(currentFile)
	dirs := []string{
		commandsDir,
		filepath.Join(commandsDir, "..", "ui", "render", "encoding"),
	}
	var sites []toolRenderCallSite
	for _, dir := range dirs {
		paths, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatalf("glob %s: %v", dir, err)
		}
		sort.Strings(paths)
		fset := token.NewFileSet()
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
				label := toolRenderFuncLabel(fn)
				ast.Inspect(fn.Body, func(node ast.Node) bool {
					call, ok := node.(*ast.CallExpr)
					if !ok || !toolRenderStringAPICall(call) {
						return true
					}
					sites = append(sites, toolRenderCallSite{
						File: filepath.Base(path),
						Func: label,
						Line: fset.Position(call.Pos()).Line,
					})
					return true
				})
			}
		}
	}
	sort.Slice(sites, func(i, j int) bool {
		if sites[i].File != sites[j].File {
			return sites[i].File < sites[j].File
		}
		if sites[i].Line != sites[j].Line {
			return sites[i].Line < sites[j].Line
		}
		return sites[i].Func < sites[j].Func
	})
	return sites
}

func toolRenderStringAPICall(call *ast.CallExpr) bool {
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		return toolRenderStringAPINames[fun.Name]
	case *ast.SelectorExpr:
		return toolRenderStringAPINames[fun.Sel.Name]
	}
	return false
}

func toolRenderFuncLabel(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return "func " + fn.Name.Name
	}
	return "method (" + toolRenderReceiverType(fn.Recv.List[0].Type) + ") " + fn.Name.Name
}

func toolRenderReceiverType(expr ast.Expr) string {
	switch typed := expr.(type) {
	case *ast.Ident:
		return typed.Name
	case *ast.StarExpr:
		return "*" + toolRenderReceiverType(typed.X)
	case *ast.IndexExpr:
		return toolRenderReceiverType(typed.X)
	case *ast.IndexListExpr:
		return toolRenderReceiverType(typed.X)
	}
	return "?"
}
