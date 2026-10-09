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

// TestChatGeometryFamilyDirectReadsFrozen 是 L5-2 Batch A（方案 §2 D2-a ①：
// 接口注入收口直读）的机械门禁：扫描 commands 生产源码，冻结几何族直读面。
//
//   - SyncTerminalGeometry / SyncTerminalGeometryThrottled：零直读。探针一律
//     经注入的 ui.GeometrySyncPort（RequestGeometrySync），新增调用必须先迁移
//     到门面；
//   - ActiveBandViewportSize：已于 L5-2b 迁移至会话门面
//     ui.ActiveBandViewportPort（unified 走渲染链几何投影、legacy 回落 surface
//     终端缓存），本门禁冻结计数并期望为 0；新增直读即违规。
//
// 白名单按「文件 :: 函数」精确匹配（行号漂移不触发 churn）；失败输出会打印
// 全部实际调用点，便于按迁移路径修正。
func TestChatGeometryFamilyDirectReadsFrozen(t *testing.T) {
	sites := collectChatGeometryFamilyDirectReads(t)

	var syncSites []chatGeometryDirectReadSite
	activeBandReads := map[string]int{}
	for _, site := range sites {
		if strings.HasPrefix(site.Method, "SyncTerminalGeometry") {
			syncSites = append(syncSites, site)
			continue
		}
		activeBandReads[site.File+" :: "+site.Func]++
	}

	if len(syncSites) != 0 {
		var lines []string
		for _, site := range syncSites {
			lines = append(lines, fmt.Sprintf("  %s:%d %s :: %s", site.File, site.Line, site.Func, site.Method))
		}
		t.Fatalf("commands 生产代码不得直读 SyncTerminalGeometry*（L5-2 Batch A：探针须经 ui.GeometrySyncPort.RequestGeometrySync）:\n%s",
			strings.Join(lines, "\n"))
	}

	// ActiveBandViewportSize 直读（L5-2b 迁移后期望为 0；空表保留以冻结面）。
	want := map[string]int{}
	if !reflect.DeepEqual(activeBandReads, want) {
		var lines []string
		for _, site := range sites {
			if !strings.HasPrefix(site.Method, "SyncTerminalGeometry") {
				lines = append(lines, fmt.Sprintf("  %s:%d %s :: %s", site.File, site.Line, site.Func, site.Method))
			}
		}
		t.Fatalf("ActiveBandViewportSize 直读面已变化（L5-2b 冻结）\n got: %#v\nwant: %#v\n当前调用点:\n%s\n"+
			"宽+行视口读已迁移至 ui.ActiveBandViewportPort（L5-2b），此处期望为 0；新增直读即违规。",
			activeBandReads, want, strings.Join(lines, "\n"))
	}
}

type chatGeometryDirectReadSite struct {
	File   string
	Func   string
	Method string
	Line   int
}

// chatGeometryFamilyMethodNames 是几何族直读冻结的方法名集合（按调用表达式
// 选择子匹配，不区分接收者，避免漏掉 session.Surface / c.surface 等变体）。
var chatGeometryFamilyMethodNames = map[string]bool{
	"SyncTerminalGeometry":          true,
	"SyncTerminalGeometryThrottled": true,
	"ActiveBandViewportSize":        true,
}

func collectChatGeometryFamilyDirectReads(t *testing.T) []chatGeometryDirectReadSite {
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
	var sites []chatGeometryDirectReadSite
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
			label := chatGeometryCallSiteLabel(fn)
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || !chatGeometryFamilyMethodNames[selector.Sel.Name] {
					return true
				}
				sites = append(sites, chatGeometryDirectReadSite{
					File:   fileKey,
					Func:   label,
					Method: selector.Sel.Name,
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

func chatGeometryCallSiteLabel(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return "func " + fn.Name.Name
	}
	return "method (" + chatGeometryReceiverType(fn.Recv.List[0].Type) + ") " + fn.Name.Name
}

func chatGeometryReceiverType(expr ast.Expr) string {
	switch typed := expr.(type) {
	case *ast.Ident:
		return typed.Name
	case *ast.StarExpr:
		return "*" + chatGeometryReceiverType(typed.X)
	case *ast.IndexExpr:
		return chatGeometryReceiverType(typed.X)
	case *ast.IndexListExpr:
		return chatGeometryReceiverType(typed.X)
	}
	return "?"
}
