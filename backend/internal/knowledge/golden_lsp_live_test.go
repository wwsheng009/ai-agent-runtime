package knowledge

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	knowledgelsp "github.com/wwsheng009/ai-agent-runtime/internal/knowledge/lsp"
	baselsp "github.com/wwsheng009/ai-agent-runtime/internal/lsp"
)

// Phase 4 验收门槛的 live 测量（04 §5 Phase 4）：语义（LSP）通道的
// definition 精度/召回，门槛 ≥90% / ≥85%。
//
// 默认跳过（需要本机语言服务器与真实启动成本）。运行方式：
//
//	AICLI_KNOWLEDGE_LSP_LIVE=1 go test -run TestGoldenDefinitionQualityLSPLive -v ./internal/knowledge/
//
// 口径：
//   - 真值 = go/parser 顶层声明（1-based 行，与索引通道同口径）；
//   - 查询点 = 内置通道抽出的调用点（真实使用位置）；
//   - precision = 返回结果命中真值的查询数 / 有返回的查询数；
//   - recall = 至少一次被正确定位的真值符号数 / 参与抽样的符号数。
func TestGoldenDefinitionQualityLSPLive(t *testing.T) {
	if os.Getenv("AICLI_KNOWLEDGE_LSP_LIVE") != "1" {
		t.Skip("live 测试：设置 AICLI_KNOWLEDGE_LSP_LIVE=1 后运行（需本机 gopls）")
	}
	const sampleSize = 80

	ctx := context.Background()
	ctx, store, pathByFile, truth := liveIndexFixture(t)
	// 语义查询走**真实 backend 模块**：子集拷贝缺少依赖包，gopls 拿不到类型信息
	// （首轮实测 answered=0）。索引仍在子集上跑，查询前把路径重映射回真实仓库。
	moduleRoot := filepath.Join(repoRoot(), "backend")
	adapter := liveSemanticAdapter(t, ctx, moduleRoot)
	truthBySymbol := map[string][]goldenEntry{}
	for _, e := range truth {
		truthBySymbol[e.Name] = append(truthBySymbol[e.Name], e)
	}

	// 3) 抽样：每个符号取一个调用点。
	type query struct {
		symbol string
		file   string
		line   int // 1-based（索引口径）
		col    int
	}
	var queries []query
	seen := map[string]bool{}
	for name := range truthBySymbol {
		if len(queries) >= sampleSize {
			break
		}
		refs, err := store.FindRefs(ctx, RefQuery{ToSymbolName: name, Kind: RefCall, Limit: 3})
		if err != nil || len(refs) == 0 {
			continue
		}
		ref := refs[0]
		if seen[name] {
			continue
		}
		path := pathByFile[ref.FileID]
		if path == "" {
			continue
		}
		seen[name] = true
		queries = append(queries, query{symbol: name, file: path, line: ref.Line, col: ref.Col})
	}
	if len(queries) < 20 {
		t.Skipf("可用调用点样本不足（%d），跳过 live 测量", len(queries))
	}

	answered, correct := 0, 0
	correctSymbols := map[string]bool{}
	for _, q := range queries {
		// 索引口径（1-based）→ canonical（0-based，ADR-0006）。
		rel := strings.TrimPrefix(q.file, "backend/")
		locs, err := adapter.Definition(ctx, rel, q.line-1, q.col)
		if err != nil || len(locs) == 0 {
			continue
		}
		answered++
		wants := truthBySymbol[q.symbol]
		for _, loc := range locs {
			// 语义通道返回 canonical（0-based）→ 回到索引口径比较。
			for _, want := range wants {
				// loc.Path 相对 manager root（backend/）；真值相对仓库根。
				if loc.Path == strings.TrimPrefix(want.File, "backend/") && loc.Line+1 == want.Line {
					correct++
					correctSymbols[q.symbol] = true
					break
				}
			}
			if correctSymbols[q.symbol] {
				break
			}
		}
		if answered <= 5 && !correctSymbols[q.symbol] {
			t.Logf("miss: %s @ %s:%d -> %v", q.symbol, q.file, q.line, locationStrings(locs))
		}
	}

	precision := float64(correct) / float64(max(answered, 1))
	recall := float64(len(correctSymbols)) / float64(max(len(queries), 1))
	t.Logf("LSP definition: precision=%.4f recall=%.4f (queries=%d answered=%d correct=%d symbols=%d)",
		precision, recall, len(queries), answered, correct, len(correctSymbols))
	if answered == 0 {
		t.Fatalf("语义通道没有任何返回：gopls 可能未就绪或 workspace 不可解析")
	}
	if precision < 0.90 {
		t.Fatalf("LSP precision = %.4f < 0.90（Phase 4 门槛）", precision)
	}
	if recall < 0.85 {
		t.Fatalf("LSP recall = %.4f < 0.85（Phase 4 门槛）", recall)
	}
}

// locationStrings 渲染语义位置（live 诊断用）。
func locationStrings(locs []SemanticLocation) []string {
	out := make([]string, 0, len(locs))
	for _, loc := range locs {
		out = append(out, fmt.Sprintf("%s:%d", loc.Path, loc.Line+1))
	}
	return out
}

// liveIndexFixture 建立子集索引、真值与 file_id → 路径映射（两个 live 测量共用）。
func liveIndexFixture(t *testing.T) (context.Context, Store, map[string]string, []goldenEntry) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	copyGoldenSubset(t, root)
	truth := buildGoldenSet(t, root)

	store := newTestStore(t)
	cfg := DefaultConfig().WithWorkspace(root)
	if _, err := RunIndex(ctx, store, cfg); err != nil {
		t.Fatalf("RunIndex: %v", err)
	}
	wsID, err := store.EnsureWorkspace(ctx, Workspace{RootPath: root})
	if err != nil {
		t.Fatalf("EnsureWorkspace: %v", err)
	}
	files, err := store.ListActiveFiles(ctx, wsID)
	if err != nil {
		t.Fatalf("ListActiveFiles: %v", err)
	}
	pathByFile := make(map[string]string, len(files))
	for _, f := range files {
		pathByFile[f.ID] = f.Path
	}
	return ctx, store, pathByFile, truth
}

// liveSemanticAdapter 启动 gopls 并返回语义适配器（live 测量共用；锁关闭以免
// 影响开发者本机会话）。
func liveSemanticAdapter(t *testing.T, ctx context.Context, moduleRoot string) SemanticAdapter {
	t.Helper()
	specs := baselsp.PresetServersNamed([]string{"gopls"})
	if len(specs) == 0 {
		t.Skip("gopls 预设不可用")
	}
	manager := knowledgelsp.NewManager(knowledgelsp.Options{
		Root:           moduleRoot,
		Spec:           specs[0],
		MaxProcesses:   1,
		MemoryLimitMB:  2048,
		StartupTimeout: 90 * time.Second,
		RequestTimeout: 30 * time.Second,
		SkipLock:       true,
		MemoryProbe:    func(int) (int64, error) { return 0, nil },
	})
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	if err := manager.Ensure(ctx); err != nil {
		t.Fatalf("gopls 启动失败（live 测试需要本机 gopls）: %v", err)
	}
	return NewLSPSemanticAdapter(manager, moduleRoot)
}

// TestGoldenReferencesQualityLSPLive 测量语义通道的 references 质量。
//
// 口径（登记偏差，无独立人工标注引用真值时的可自动化近似）：
//   - **precision（代理）**：返回位置必须通过词边界 token 校验（该行确实出现
//     目标标识符）且不是声明自身——编译器级通道不应返回非标识符位置。
//   - **recall（相对基线）**：以内置索引的引用集合为基线，语义通道对基线的
//     覆盖率（语义 ⊇ 索引是期望方向；索引含正则误报，故该值是语义召回的下界）。
//
// 门槛同 Phase 4：precision ≥ 0.90、相对 recall ≥ 0.85。
func TestGoldenReferencesQualityLSPLive(t *testing.T) {
	if os.Getenv("AICLI_KNOWLEDGE_LSP_LIVE") != "1" {
		t.Skip("live 测试：设置 AICLI_KNOWLEDGE_LSP_LIVE=1 后运行（需本机 gopls）")
	}
	const sampleSize = 40

	ctx, store, pathByFile, truth := liveIndexFixture(t)
	moduleRoot := filepath.Join(repoRoot(), "backend")
	adapter := liveSemanticAdapter(t, ctx, moduleRoot)

	lineCache := map[string][]string{}
	lineAt := func(repoRel string, line0 int) string {
		lines, ok := lineCache[repoRel]
		if !ok {
			content, err := os.ReadFile(filepath.Join(repoRoot(), filepath.FromSlash(repoRel)))
			if err != nil {
				lineCache[repoRel] = nil
				return ""
			}
			lines = strings.Split(string(content), "\n")
			lineCache[repoRel] = lines
		}
		if line0 < 0 || line0 >= len(lines) {
			return ""
		}
		return lines[line0]
	}

	seen := map[string]bool{}
	sampled, semanticTotal, tokenOK := 0, 0, 0
	builtinTotal, builtinCovered := 0, 0
	baselineFalseBindings, baselineFalsePositives, semanticMisses := 0, 0, 0
	for _, e := range truth {
		if sampled >= sampleSize {
			break
		}
		if seen[e.Name] {
			continue
		}
		// 声明位置（真值是 1-based 行；列由行内名字偏移给出）。
		line := lineAt(e.File, e.Line-1)
		col := strings.Index(line, e.Name)
		if col < 0 {
			continue
		}
		seen[e.Name] = true
		sampled++

		rel := strings.TrimPrefix(e.File, "backend/")
		locs, err := adapter.References(ctx, rel, e.Line-1, col)
		if err != nil {
			continue
		}
		word := regexp.MustCompile(`(^|[^0-9A-Za-z_])` + regexp.QuoteMeta(e.Name) + `([^0-9A-Za-z_]|$)`)
		semKeys := map[string]bool{}
		for _, loc := range locs {
			if loc.Path == rel && loc.Line == e.Line-1 {
				continue // 声明自身
			}
			semanticTotal++
			semKeys[semanticKey(loc.Path, loc.Line)] = true
			if word.MatchString(lineAt("backend/"+loc.Path, loc.Line)) {
				tokenOK++
			}
		}

		// 基线必须按**符号身份**取（ToSymbolID）：按名字取会把同名符号的引用
		// 混进来（首轮实测 recall=0.25 的主因），那不是语义通道的漏报。
		symID := resolveSubsetSymbol(ctx, store, pathByFile, e)
		if symID == "" {
			continue
		}
		refs, err := store.FindRefs(ctx, RefQuery{ToSymbolID: symID, Kind: RefCall, Limit: 500})
		if err != nil {
			continue
		}
		builtinKeys := map[string]bool{}
		type builtinRef struct {
			path  string
			line0 int
			col   int
		}
		byKey := map[string]builtinRef{}
		for _, ref := range refs {
			path := pathByFile[ref.FileID]
			if path == "" {
				continue
			}
			relPath := strings.TrimPrefix(path, "backend/")
			if relPath == rel && ref.Line-1 == e.Line-1 {
				continue
			}
			key := semanticKey(relPath, ref.Line-1)
			builtinKeys[key] = true
			if _, ok := byKey[key]; !ok {
				byKey[key] = builtinRef{path: relPath, line0: ref.Line - 1, col: ref.Col}
			}
		}
		for key := range builtinKeys {
			builtinTotal++
			if semKeys[key] {
				builtinCovered++
				continue
			}
			// 未覆盖：用 definition 查询裁决——语义通道在该位置返回的定义是否
			// 就是被采样符号？
			//   - 返回该符号 → 语义漏报（references 缺失）；
			//   - 返回别的符号 → 索引把同名/限定的其他符号误绑定（误绑定噪音）；
			//   - **没有返回且无错误** → 该位置根本不是标识符引用（字符串/注释
			//     文本），即索引误报（如 `t.Fatalf("Activate(owner): %v")`）；
			//   - 查询出错 → 保守计入语义漏报（不美化数字）。
			ref := byKey[key]
			defs, defErr := adapter.Definition(ctx, ref.path, ref.line0, ref.col)
			matched := false
			for _, d := range defs {
				if d.Path == rel && d.Line == e.Line-1 {
					matched = true
					break
				}
			}
			switch {
			case defErr != nil:
				semanticMisses++
				t.Logf("miss(no-def): %s -> %s (semantic=%d)", e.Name, key, len(locs))
			case len(defs) == 0:
				baselineFalsePositives++
				t.Logf("false-positive(index): %s -> %s", e.Name, key)
			case matched:
				semanticMisses++
				sameFile := 0
				for k := range semKeys {
					if strings.HasPrefix(k, ref.path+"|") {
						sameFile++
					}
				}
				t.Logf("miss(def-match): %s -> %s (semantic=%d same_file=%d)", e.Name, key, len(locs), sameFile)
			default:
				baselineFalseBindings++
			}
		}
	}

	precision := float64(tokenOK) / float64(max(semanticTotal, 1))
	adjudicated := builtinTotal - baselineFalseBindings - baselineFalsePositives
	recall := float64(builtinCovered) / float64(max(adjudicated, 1))
	t.Logf("LSP references: precision(代理)=%.4f recall(裁决后)=%.4f (symbols=%d semantic=%d token_ok=%d builtin=%d covered=%d false_bindings=%d false_positives=%d semantic_misses=%d)",
		precision, recall, sampled, semanticTotal, tokenOK, builtinTotal, builtinCovered, baselineFalseBindings, baselineFalsePositives, semanticMisses)
	if semanticTotal == 0 || builtinTotal == 0 {
		t.Fatalf("references 样本为空（semantic=%d builtin=%d）：gopls 未就绪或索引无引用", semanticTotal, builtinTotal)
	}
	if precision < 0.90 {
		t.Fatalf("LSP references precision = %.4f < 0.90（Phase 4 门槛）", precision)
	}
	// 门槛（≥0.85）：上一轮实测 0.7674 是**裁决口径错误**（把索引在字符串字面量
	// 里的误报计入语义漏报）；本轮修正裁决（无定义返回 → 索引误报）并把误报挡在
	// 抽取阶段（`adapter_builtin.insideStringOrComment`），断言回到 Phase 4 门槛。
	if recall < 0.85 {
		t.Fatalf("LSP references recall = %.4f < 0.85（Phase 4 门槛）", recall)
	}
}

// semanticKey 是 (模块相对路径, canonical 行) 的稳定键。
func semanticKey(path string, line int) string {
	return fmt.Sprintf("%s|%d", path, line)
}

// resolveSubsetSymbol 在子集索引中按 (名字, 文件, 行) 定位真值符号，返回其 id。
// 未解析到（例如块内常量不入轻索引）时返回空串。
func resolveSubsetSymbol(ctx context.Context, store Store, pathByFile map[string]string, e goldenEntry) string {
	symbols, err := store.FindSymbols(ctx, SymbolQuery{Name: e.Name, Exact: true, Limit: 50})
	if err != nil {
		return ""
	}
	for _, sym := range symbols {
		if pathByFile[sym.FileID] == e.File && sym.Range.Start.Line == e.Line {
			return sym.ID
		}
	}
	return ""
}

// copyModuleFiles 把 backend/go.mod 与 go.sum 复制到 temp workspace，
// 使 gopls 能把子集当作一个真实模块加载。
func copyModuleFiles(t *testing.T, dst string) {
	t.Helper()
	for _, name := range []string{"go.mod", "go.sum"} {
		src := filepath.Join(repoRoot(), "backend", name)
		data, err := os.ReadFile(src)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		target := filepath.Join(dst, "backend", name)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(target, data, 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	// 子集没有全部依赖包：让 gopls 容忍缺失（仅用于定义查询）。
	gomod := filepath.Join(dst, "backend", "go.mod")
	data, err := os.ReadFile(gomod)
	if err == nil && !strings.Contains(string(data), "// live-test") {
		_ = os.WriteFile(gomod, append(data, []byte("\n// live-test subset\n")...), 0o644)
	}
}
