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

// TestChatSessionSurfaceEnabledReadsFrozen 是 D3 Batch D 的机械门禁：会话级
// `X.Surface.Enabled()` 直读已收敛到单点 chatSessionSurfaceUsable()
// （chat_screen_capability.go）。
//
// 白名单（文件 :: 函数，行号漂移不触发 churn）：
//   - chatSessionSurfaceUsable：单点本体（唯一合法直读）。
//
// 覆盖形态：`session.Surface.Enabled()` / `c.session.Surface.Enabled()` /
// `o.session.Surface.Enabled()` 等一切「选择子为 `Surface` 的 Enabled 读」；
// coordinator 侧链式 `c.surface.Enabled()`（小写字段）由
// TestChatSurfaceOutputEnabledReadsFrozen 单独冻结。
func TestChatSessionSurfaceEnabledReadsFrozen(t *testing.T) {
	sites := collectChatSessionSurfaceEnabledReads(t)

	got := map[string]int{}
	for _, site := range sites {
		got[site.File+" :: "+site.Func]++
	}
	want := map[string]int{
		"chat_screen_capability.go :: func chatSessionSurfaceUsable": 1,
	}
	if !reflect.DeepEqual(got, want) {
		var lines []string
		for _, site := range sites {
			lines = append(lines, fmt.Sprintf("  %s:%d %s", site.File, site.Line, site.Func))
		}
		t.Fatalf("会话级 X.Surface.Enabled() 直读面已变化（D3 Batch D 冻结）\n got: %#v\nwant: %#v\n当前调用点:\n%s\n"+
			"会话级 surface 可用性一律经 chatSessionSurfaceUsable() 单点；新增直读即违规。",
			got, want, strings.Join(lines, "\n"))
	}
}

type chatSessionSurfaceEnabledReadSite struct {
	File string
	Func string
	Line int
}

func collectChatSessionSurfaceEnabledReads(t *testing.T) []chatSessionSurfaceEnabledReadSite {
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
	var sites []chatSessionSurfaceEnabledReadSite
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
				// 会话级形态：X.Surface.Enabled()（Surface 为选择子名，大小写敏感）。
				receiver, ok := selector.X.(*ast.SelectorExpr)
				if !ok || receiver.Sel.Name != "Surface" {
					return true
				}
				sites = append(sites, chatSessionSurfaceEnabledReadSite{
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
