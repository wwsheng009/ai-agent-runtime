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

// TestChatScreenGateTripleReadsFrozen 是 D3 Batch A 的机械门禁：三联 gate
// （Enabled/OwnedViewport/LeaseActive + 弹层）的内联直读已收敛到单点
// chatSurfaceScreenGate（chat_screen_capability.go）。
//
//   - OwnedViewport() 在 commands 生产源码中仅允许单点 1 处（helper 本体）；
//     新增直读即违规（先经单点，再扩展白名单需同步本测试与 D3 方案文档）；
//   - LeaseActive() 链式直读冻结为 2 处（chat_screen_capability.go 的两个单点：
//     chatSurfaceScreenGate / chatSurfaceLeased；D3 Batch B 收口）；
//   - 内联组合形态 `!X.Surface.Enabled() || !X.Surface.OwnedViewport()` 零容忍。
//
// 白名单按「文件 :: 函数」精确匹配（行号漂移不触发 churn）；失败输出打印全部
// 实际调用点，便于按迁移路径修正。
func TestChatScreenGateTripleReadsFrozen(t *testing.T) {
	sites := collectChatScreenGateReads(t)

	owned := map[string]int{}
	for _, site := range sites {
		if site.Method == "OwnedViewport" {
			owned[site.File+" :: "+site.Func]++
		}
	}
	want := map[string]int{"chat_screen_capability.go :: func chatSurfaceScreenGate": 1}
	if !reflect.DeepEqual(owned, want) {
		var lines []string
		for _, site := range sites {
			if site.Method == "OwnedViewport" {
				lines = append(lines, fmt.Sprintf("  %s:%d %s", site.File, site.Line, site.Func))
			}
		}
		t.Fatalf("OwnedViewport() 直读面已变化（D3 Batch A 冻结）\n got: %#v\nwant: %#v\n当前调用点:\n%s\n"+
			"副屏 gate 一律经 chatSurfaceScreenGate 单点（chat_screen_capability.go）；内联复制即违规。",
			owned, want, strings.Join(lines, "\n"))
	}

	leased := map[string]int{}
	for _, site := range sites {
		if site.Method == "LeaseActive" {
			leased[site.File+" :: "+site.Func]++
		}
	}
	wantLeased := map[string]int{
		"chat_screen_capability.go :: func chatSurfaceScreenGate": 1,
		"chat_screen_capability.go :: func chatSurfaceLeased":     1,
	}
	if !reflect.DeepEqual(leased, wantLeased) {
		var lines []string
		for _, site := range sites {
			if site.Method == "LeaseActive" {
				lines = append(lines, fmt.Sprintf("  %s:%d %s", site.File, site.Line, site.Func))
			}
		}
		t.Fatalf("LeaseActive() 直读面已变化（D3 Batch B 冻结）\n got: %#v\nwant: %#v\n当前调用点:\n%s\n"+
			"租约繁忙判定一律经 chatSurfaceLeased 单点（chat_screen_capability.go）；内联复制即违规。",
			leased, wantLeased, strings.Join(lines, "\n"))
	}
}

type chatScreenGateReadSite struct {
	File   string
	Func   string
	Method string
	Line   int
}

// chatScreenGateMethodNames 是 D3 冻结的方法名集合（按调用表达式选择子匹配，
// 不区分接收者，避免漏掉 session.Surface / c.surface 等变体）。
var chatScreenGateMethodNames = map[string]bool{
	"OwnedViewport": true,
	"LeaseActive":   true,
}

func collectChatScreenGateReads(t *testing.T) []chatScreenGateReadSite {
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
	var sites []chatScreenGateReadSite
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
				if !ok || !chatScreenGateMethodNames[selector.Sel.Name] {
					return true
				}
				sites = append(sites, chatScreenGateReadSite{
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
