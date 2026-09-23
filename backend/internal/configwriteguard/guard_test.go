package configwriteguard

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// 配置文件写锁的**静态守卫**（方案 §9.1 U-2 / §12 R4，2026-09-22 第四次增补后的防御性加强）。
//
// 背景：锁与写事务只能约束**已经接入**的写点。本包之前的两轮工作把 agentconfig 包内全部
// 写点与 runtime-server 的三个包外写点并入同一把锁，但「后来者」仍可能新增一条裸写通道
// （新的 handler 里直接 `os.WriteFile(configPath, …)`，或调用通道助手却不取锁），而行为
// 用例只在已知写点上钉，覆盖不到新代码。
//
// 因此本用例做**源码级**检查：扫描 `backend/` 下全部非测试 Go 文件，找出对配置文件的裸写，
// 要求每个写点都处于共享写锁的保护下。「受保护」的判据（保守优先，宁可误报不可漏报）：
//
//	① 写点所在函数体内出现 `LockConfigFileWrite` / `LockConfigFileWriteAll`（含
//	   `agentconfig.LockConfigFileWrite` 这类包外写法）；或
//	② 沿**同目录**调用图向上（≤ guardMaxCallerDepth 层）能到达这样的函数——用于
//	   `…Locked` 变体、格式分派助手这类「调用方持锁」的写法；或
//	③ 在 guardWriteExemptions 里显式豁免（必须写明理由，且豁免过期会被报出来）。
//
// 判为「配置文件裸写」的两种情形：
//
//	A. 调 `os.WriteFile` / `ioutil.WriteFile` 且目标表达式含 config / cfg 语义；
//	B. 调 `writeFileAtomic` / `writeFilePreserveMode`（agentconfig 与 runtime-server 的
//	   共享写通道助手）——不论目标变量叫什么，都必须受锁保护。
//
// 已知局限（如实登记）：不做数据流分析，`p := someConfigPath()` 之后再 `os.WriteFile(p, …)`
// 这种改名后的裸写判不出来（B 类可覆盖其中走通道助手的部分）；`os.Create` + `io.Copy` 的
// 写法也不在判据内。判据只求挡住最常见的新增通道，不追求完备。
//
// 范围排除（如实登记）：`scripts/` 下的 e2e 驱动（`acp_e2e_*.go` 等，均为独立 `package main`）
// 各自用一次性 AICLI_HOME 预置 `config.yaml` 后拉起被测进程，不是产品内共享配置的并发写者，
// 因此整目录跳过；`testdata` / `vendor` / `node_modules` / `.git` 同样跳过。
const guardMaxCallerDepth = 6

// guardWriteExemptions 是显式豁免的写点，键 = `<相对 backend/ 的目录>::<函数名>`。
// 每一项都必须写明理由，并保持最小。
var guardWriteExemptions = map[string]string{
	"internal/agentconfig::EnsureUserPresetsFile": "create-once 的独立文件（~/.aicli/presets.yaml，方案 §12 已登记的例外），除首次创建外没有并发读-改-写写者",
}

// guardFuncInfo 是一个函数（或同目录同名函数集合）的守卫信息。
type guardFuncInfo struct {
	dirRel  string
	name    string
	fileRel string
	hasLock bool
	calls   map[string]struct{}
	writes  []guardWriteSite
}

// guardWriteSite 是一个候选的配置文件裸写点。
type guardWriteSite struct {
	fileRel string
	line    int
	fn      string
	target  string
	reason  string
}

// TestConfigFileWritesStayUnderSharedWriteLock：新增的配置写点必须走共享写锁，否则用例失败。
func TestConfigFileWritesStayUnderSharedWriteLock(t *testing.T) {
	root := guardBackendRoot(t)

	funcs := map[string]*guardFuncInfo{} // key: 相对目录 + "\x00" + 函数名
	scannedFiles := 0

	walkErr := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case "testdata", "vendor", "node_modules", ".git", "scripts":
				return fs.SkipDir
			}
			return nil
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}

		dirRel, err := filepath.Rel(root, filepath.Dir(path))
		if err != nil {
			return err
		}
		dirRel = filepath.ToSlash(dirRel)
		fileRel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		fileRel = filepath.ToSlash(fileRel)

		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		fset := token.NewFileSet()
		parsed, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			return fmt.Errorf("parse %s: %w", fileRel, err)
		}
		scannedFiles++

		for _, decl := range parsed.Decls {
			fnDecl, ok := decl.(*ast.FuncDecl)
			if !ok || fnDecl.Body == nil {
				continue
			}
			fnName := fnDecl.Name.Name
			// 通道助手自身的实现不参与检查（它们就是「原子写」这一层，锁在其调用方）。
			if fnName == "writeFileAtomic" || fnName == "writeFilePreserveMode" {
				continue
			}

			info := &guardFuncInfo{
				dirRel:  dirRel,
				name:    fnName,
				fileRel: fileRel,
				calls:   map[string]struct{}{},
			}
			ast.Inspect(fnDecl.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				callee := guardCalleeName(call.Fun)
				if callee == "" {
					return true
				}
				info.calls[callee] = struct{}{}
				if callee == "LockConfigFileWrite" || callee == "LockConfigFileWriteAll" {
					info.hasLock = true
				}
				if site, ok := guardRawConfigWrite(call, callee, src, fset); ok {
					site.fileRel = fileRel
					site.line = fset.Position(call.Pos()).Line
					site.fn = fnName
					info.writes = append(info.writes, site)
				}
				return true
			})

			key := dirRel + "\x00" + fnName
			if existing := funcs[key]; existing != nil {
				// 同目录同名（不同 receiver 的方法）：保守合并——调用取并集，锁取交集
				// （只要有一个同名函数没取锁，就不能算受保护）。
				existing.hasLock = existing.hasLock && info.hasLock
				for callee := range info.calls {
					existing.calls[callee] = struct{}{}
				}
				existing.writes = append(existing.writes, info.writes...)
				continue
			}
			funcs[key] = info
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("扫描源码树失败: %v", walkErr)
	}

	// 反向调用索引：相对目录 → 被调函数名 → 调用者函数名集合。
	callers := map[string]map[string]map[string]struct{}{}
	for _, info := range funcs {
		for callee := range info.calls {
			byName := callers[info.dirRel]
			if byName == nil {
				byName = map[string]map[string]struct{}{}
				callers[info.dirRel] = byName
			}
			set := byName[callee]
			if set == nil {
				set = map[string]struct{}{}
				byName[callee] = set
			}
			set[info.name] = struct{}{}
		}
	}

	var protected func(dirRel, fnName string, depth int, seen map[string]struct{}) bool
	protected = func(dirRel, fnName string, depth int, seen map[string]struct{}) bool {
		key := dirRel + "\x00" + fnName
		if _, ok := seen[key]; ok {
			return false
		}
		seen[key] = struct{}{}
		info := funcs[key]
		if info == nil {
			return false
		}
		if info.hasLock {
			return true
		}
		if depth <= 0 {
			return false
		}
		for caller := range callers[dirRel][fnName] {
			if protected(dirRel, caller, depth-1, seen) {
				return true
			}
		}
		return false
	}

	exemptUsed := map[string]bool{}
	protectedSites := 0
	var protectedList []string
	var violations []string
	for _, info := range funcs {
		for _, site := range info.writes {
			exemptKey := site.fileDirKey() + "::" + site.fn
			if _, ok := guardWriteExemptions[exemptKey]; ok {
				exemptUsed[exemptKey] = true
				continue
			}
			if protected(info.dirRel, site.fn, guardMaxCallerDepth, map[string]struct{}{}) {
				protectedSites++
				protectedList = append(protectedList, fmt.Sprintf("%s:%d %s", site.fileRel, site.line, site.fn))
				continue
			}
			violations = append(violations, fmt.Sprintf("%s:%d %s() 写入目标 `%s`（%s）", site.fileRel, site.line, site.fn, site.target, site.reason))
		}
	}
	sort.Strings(violations)
	sort.Strings(protectedList)

	// 自检：守卫本身失效（扫描根不对、解析没生效）必须报错，而不是静默通过。
	if scannedFiles < 800 {
		t.Fatalf("守卫只扫到 %d 个非测试 Go 文件（扫描根 %s）：源码树没扫全，判据不可信", scannedFiles, root)
	}
	if protectedSites < 8 {
		t.Fatalf("守卫只识别到 %d 个受保护的写点（已知至少 10 个）：保护判据失效，判据不可信", protectedSites)
	}
	for key, reason := range guardWriteExemptions {
		if !exemptUsed[key] {
			t.Errorf("豁免已过期：%s 已没有目标写点，请删除该豁免（理由：%s）", key, reason)
		}
	}
	t.Logf("扫描 %d 个非测试 Go 文件；受保护的配置写点 %d 个；豁免 %d 个\n受保护写点：\n  %s",
		scannedFiles, protectedSites, len(exemptUsed), strings.Join(protectedList, "\n  "))
	if len(violations) > 0 {
		t.Fatalf("发现未走共享写锁的配置文件写点（%d 个）：\n  %s\n\n修复：把写入收敛到 agentconfig 的 `updateConfigFileDocument`（或已持锁的 `...Locked` 变体），"+
			"或先用 `LockConfigFileWrite` / `LockConfigFileWriteAll` 覆盖整个「读-改-写」再落盘；"+
			"确属独立文件（没有并发读-改-写写者）时，把该函数加入 guardWriteExemptions 并写明理由。",
			len(violations), strings.Join(violations, "\n  "))
	}
}

// fileDirKey 返回写点所在文件相对 backend/ 的目录，用于豁免键与诊断。
func (s guardWriteSite) fileDirKey() string {
	dir := filepath.ToSlash(filepath.Dir(s.fileRel))
	if dir == "." {
		return ""
	}
	return dir
}

// guardRawConfigWrite 判定一次调用是否是「配置文件裸写」。
func guardRawConfigWrite(call *ast.CallExpr, callee string, src []byte, fset *token.FileSet) (guardWriteSite, bool) {
	if len(call.Args) == 0 {
		return guardWriteSite{}, false
	}
	channelCall := callee == "writeFileAtomic" || callee == "writeFilePreserveMode"
	if !channelCall {
		if callee != "WriteFile" {
			return guardWriteSite{}, false
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return guardWriteSite{}, false
		}
		pkg, ok := selector.X.(*ast.Ident)
		if !ok || (pkg.Name != "os" && pkg.Name != "ioutil") {
			return guardWriteSite{}, false
		}
	}

	start := fset.Position(call.Args[0].Pos()).Offset
	end := fset.Position(call.Args[0].End()).Offset
	if start < 0 || end > len(src) || start >= end {
		return guardWriteSite{}, false
	}
	target := strings.Join(strings.Fields(string(src[start:end])), "")
	lower := strings.ToLower(target)
	configLike := strings.Contains(lower, "config") || strings.Contains(lower, "cfg")

	switch {
	case channelCall:
		return guardWriteSite{target: target, reason: "调用共享写通道助手，须由调用方持锁"}, true
	case configLike:
		return guardWriteSite{target: target, reason: "目标是配置语义路径"}, true
	default:
		return guardWriteSite{}, false
	}
}

// guardCalleeName 取调用表达式的被调名（函数名或方法/包名.函数名）。
func guardCalleeName(fun ast.Expr) string {
	switch expr := fun.(type) {
	case *ast.Ident:
		return expr.Name
	case *ast.SelectorExpr:
		return expr.Sel.Name
	}
	return ""
}

// guardBackendRoot 返回 <repo>/backend 的绝对路径（本测试位于 backend/internal/configwriteguard）。
func guardBackendRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("解析 backend 根目录失败: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("扫描根 %s 下没有 go.mod：守卫的路径假设失效", root)
	}
	return root
}
