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

// TestChatSurfaceOutputEnabledReadsFrozen 是 D3 Batch C（部分）的机械门禁：
// coordinator 侧 `c.surface.Enabled()` 链式直读已收敛到既有单点
// surfaceOutputActiveLocked()（chat_interaction.go，D3 方案 §2 D3）。
//
// 白名单（文件 :: 函数，行号漂移不触发 churn）：
//   - surfaceOutputActiveLocked：单点本体（唯一合法直读）；
//   - applyDrawRequested（chat_ui_actor.go:1378）：**剩余 1 处**——目标文件处于
//     并发 WIP（web/resume 工作流），待其收口后迁移并从白名单删除（D3 Batch C 剩余项）。
//
// 仅冻结链式形态（`X.surface.Enabled()`）；SetSurface 的局部参数 `surface.Enabled()`
// 与 `session.Surface.Enabled()`（A 族已收敛）不在此门禁范围。
func TestChatSurfaceOutputEnabledReadsFrozen(t *testing.T) {
	sites := collectChatSurfaceOutputEnabledReads(t)

	got := map[string]int{}
	for _, site := range sites {
		got[site.File+" :: "+site.Func]++
	}
	want := map[string]int{
		"chat_interaction.go :: method (*chatInteractionCoordinator) surfaceOutputActiveLocked": 1,
		"chat_ui_actor.go :: method (*chatInteractionCoordinator) applyDrawRequested":           1,
	}
	if !reflect.DeepEqual(got, want) {
		var lines []string
		for _, site := range sites {
			lines = append(lines, fmt.Sprintf("  %s:%d %s", site.File, site.Line, site.Func))
		}
		t.Fatalf("c.surface.Enabled() 链式直读面已变化（D3 Batch C 冻结）\n got: %#v\nwant: %#v\n当前调用点:\n%s\n"+
			"输出面活跃判定一律经 surfaceOutputActiveLocked() 单点；新增直读即违规。",
			got, want, strings.Join(lines, "\n"))
	}
}

type chatSurfaceOutputEnabledReadSite struct {
	File string
	Func string
	Line int
}

func collectChatSurfaceOutputEnabledReads(t *testing.T) []chatSurfaceOutputEnabledReadSite {
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
	var sites []chatSurfaceOutputEnabledReadSite
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
				if !ok || selector.Sel.Name != "Enabled" {
					return true
				}
				// 链式形态：X.surface.Enabled()（X 为任意接收者表达式）。
				receiver, ok := selector.X.(*ast.SelectorExpr)
				if !ok || receiver.Sel.Name != "surface" {
					return true
				}
				sites = append(sites, chatSurfaceOutputEnabledReadSite{
					File: fileKey,
					Func: label,
					Line: fset.Position(call.Pos()).Line,
				})
				return true
			})
		}
	}
	sort.Slice(sites, func(i, j int) bool {
		if sites[i].File != sites[j].File {
			return sites[i].File < sites[j].File
		}
		return sites[i].Line < sites[j].Line
	})
	return sites
}
