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

// TestChatPopupFamilyDirectReadsFrozen 是 L5-2 Batch B（方案 §2 D1：popup 所有权
// 上移）的机械门禁：扫描 commands 生产源码，冻结 popup 族直读面。
//
//   - popup 状态机族（BeginPopup*/ShowPopup*/UpdatePopupInputForHandle/ClearPopup*/
//     HasActivePopup + pending paste preview，方案 §1.1 的 39 点位扣除邻近白名单）：
//     零直读。全部调用必须经会话门面 chatSessionPopupPort / ui.PopupPort
//     （unified 直投 controller；legacy 回落 surface），新增直读即门禁违规；
//   - §1.1 邻近点位（SetPromptEditorStatusLine / PromptInputMaxVisibleRows）：
//     已于 L5-2c 迁移至会话门面 chatSessionPromptPort / ui.PromptEditorPort
//     （unified 直投状态行 action、预算走渲染器同源投影；legacy 回落 surface），
//     本门禁按 file::func::method 冻结计数并期望为 0；新增直读即违规。
//
// 白名单按「文件 :: 函数 :: 方法」精确匹配（行号漂移不触发 churn）。门面接收者
// 判定：调用接收者为 chatSessionPopupPort(...) 的调用表达式视为合规门面调用；
// 其余接收者（session.Surface / c.surface / 局部 surface 变量等）一律计入直读。
func TestChatPopupFamilyDirectReadsFrozen(t *testing.T) {
	sites := collectChatPopupFamilyDirectReads(t)

	var violations []chatPopupDirectReadSite
	adjacent := map[string]int{}
	for _, site := range sites {
		if chatPopupFamilyAdjacentMethodNames[site.Method] {
			adjacent[site.File+" :: "+site.Func+" :: "+site.Method]++
			continue
		}
		violations = append(violations, site)
	}

	if len(violations) != 0 {
		var lines []string
		for _, site := range violations {
			lines = append(lines, fmt.Sprintf("  %s:%d %s :: %s", site.File, site.Line, site.Func, site.Method))
		}
		t.Fatalf("commands 生产代码不得直读 popup 族方法（L5-2 Batch B：须经 chatSessionPopupPort / ui.PopupPort，unified 直投 controller、legacy 回落 surface）:\n%s",
			strings.Join(lines, "\n"))
	}

	// prompt-editor/composer 邻近点位（L5-2c 迁移后期望为 0；空表保留以冻结面）。
	want := map[string]int{}
	if !reflect.DeepEqual(adjacent, want) {
		var lines []string
		for _, site := range sites {
			if chatPopupFamilyAdjacentMethodNames[site.Method] {
				lines = append(lines, fmt.Sprintf("  %s:%d %s :: %s", site.File, site.Line, site.Func, site.Method))
			}
		}
		t.Fatalf("popup 族邻近白名单直读面已变化（L5-2c 冻结）\n got: %#v\nwant: %#v\n当前邻近调用点:\n%s\n"+
			"SetPromptEditorStatusLine / PromptInputMaxVisibleRows 已迁移至 chatSessionPromptPort / ui.PromptEditorPort（L5-2c），"+
			"此处期望为 0；新增直读即违规。", adjacent, want, strings.Join(lines, "\n"))
	}
}

type chatPopupDirectReadSite struct {
	File   string
	Func   string
	Method string
	Line   int
}

// chatPopupFamilyMethodNames 是 popup 族直读冻结的方法名集合（方案 §1.1：39 点位
// 扣除邻近白名单后的全部方法；按调用表达式选择子匹配，接收者是否为合规门面由
// isChatPopupFacadeReceiver 判定）。
var chatPopupFamilyMethodNames = map[string]bool{
	"BeginPopupInputForOwner":                    true,
	"BeginPopupInputForOwnerWithViewport":        true,
	"ShowPopupInputForOwner":                     true,
	"ShowPopupInputPreserveCursorForOwner":       true,
	"ShowPopupPreserveCursorForOwner":            true,
	"ShowPopupPreserveCursorForOwnerBelowPrompt": true,
	"UpdatePopupInputForHandle":                  true,
	"ClearPopup":                                 true,
	"ClearPopupPreserveCursor":                   true,
	"ClearPopupForOwnerPreserveCursor":           true,
	"ClearPopupHandlePreserveCursor":             true,
	"HasActivePopup":                             true,
	"ShowPendingPastePreview":                    true,
	"ClearPendingPastePreview":                   true,
}

// chatPopupFamilyAdjacentMethodNames 是 §1.1 邻近点位（prompt-editor/composer
// facade 组，非 popup 状态机）的冻结集合：本批保持直读，按 file::func::method
// 精确计数白名单。
var chatPopupFamilyAdjacentMethodNames = map[string]bool{
	"SetPromptEditorStatusLine": true,
	"PromptInputMaxVisibleRows": true,
}

// isChatPopupFacadeReceiver 判定调用接收者是否为合规门面入口
// chatSessionPopupPort(...)。门面变量形式（先赋值后调用）不在冻结口径内：
// 若未来采用该形式，需同步更新本门禁的接收者判定。
func isChatPopupFacadeReceiver(expr ast.Expr) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return false
	}
	ident, ok := call.Fun.(*ast.Ident)
	return ok && ident.Name == "chatSessionPopupPort"
}

func collectChatPopupFamilyDirectReads(t *testing.T) []chatPopupDirectReadSite {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	commandsDir := filepath.Dir(currentFile)
	paths, err := filepath.Glob(filepath.Join(commandsDir, "*.go"))
	if err != nil {
		t.Fatalf("glob commands sources: %v", err)
	}
	sort.Strings(paths)

	fset := token.NewFileSet()
	var sites []chatPopupDirectReadSite
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", filepath.Base(path), err)
		}
		fileKey := filepath.Base(path)
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			label := chatPopupCallSiteLabel(fn)
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				name := selector.Sel.Name
				if !chatPopupFamilyMethodNames[name] && !chatPopupFamilyAdjacentMethodNames[name] {
					return true
				}
				if isChatPopupFacadeReceiver(selector.X) {
					return true
				}
				sites = append(sites, chatPopupDirectReadSite{
					File:   fileKey,
					Func:   label,
					Method: name,
					Line:   fset.Position(call.Pos()).Line,
				})
				return true
			})
		}
	}
	sort.Slice(sites, func(i, j int) bool {
		if sites[i].File != sites[j].File {
			return sites[i].File < sites[j].File
		}
		if sites[i].Line != sites[j].Line {
			return sites[i].Line < sites[j].Line
		}
		return sites[i].Method < sites[j].Method
	})
	return sites
}

func chatPopupCallSiteLabel(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return "func " + fn.Name.Name
	}
	return "method (" + chatPopupReceiverType(fn.Recv.List[0].Type) + ") " + fn.Name.Name
}

func chatPopupReceiverType(expr ast.Expr) string {
	switch typed := expr.(type) {
	case *ast.Ident:
		return typed.Name
	case *ast.StarExpr:
		return "*" + chatPopupReceiverType(typed.X)
	case *ast.IndexExpr:
		return chatPopupReceiverType(typed.X)
	case *ast.IndexListExpr:
		return chatPopupReceiverType(typed.X)
	}
	return "?"
}
