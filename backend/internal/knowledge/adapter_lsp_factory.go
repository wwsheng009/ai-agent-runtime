package knowledge

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"

	knowledgelsp "github.com/wwsheng009/ai-agent-runtime/internal/knowledge/lsp"
	baselsp "github.com/wwsheng009/ai-agent-runtime/internal/lsp"
)

// Phase 4 接线（04 §5 Phase 4 交付 2/3 → Phase 3 工具面消费）：语义通道的
// 生产构造入口。工具面按配置获得 SemanticAdapter；不可用时返回稳定 reason，
// 由调用方降级到索引/启发式路径（Degrade-Not-Fail）。

// 语义通道降级原因 token（稳定，供日志与工具 explanation 消费）。
const (
	SemanticReasonDisabled       = "lsp_disabled"
	SemanticReasonModeNotSelf    = "lsp_mode_not_self"
	SemanticReasonWorkspace      = "workspace_missing"
	SemanticReasonNoLanguage     = "no_supported_language"
	SemanticReasonServerSpecMiss = "server_spec_missing"
)

// maxModuleRootDepth 限制模块根向下探测的深度。
//
// 为什么需要向下探测：monorepo 的会话 workspace 根常常不是模块根（本仓库就是
// `<repo>/backend/go.mod`，而 workspace 根是 repo 根）。只认 `<root>/go.mod` 时，
// 这类 workspace 恒定拿到 `no_supported_language`——配置写对了语义通道也不生效，
// 是「配了却看不到效果」的主要来源。深度上限避免把整个仓库扫穿。
const maxModuleRootDepth = 3

// skippedModuleRootDirs 是向下探测时跳过的目录名：与语言无关的噪声或体量陷阱
// （依赖树、构建产物、版本控制与本工具自身的工作区）。
var skippedModuleRootDirs = map[string]bool{
	"node_modules": true, ".git": true, "vendor": true,
	"dist": true, "build": true, "out": true, "target": true,
	".aicli": true, ".cache": true, ".venv": true, "__pycache__": true,
	".idea": true, ".vscode": true,
	// worktrees/ 是本仓库的 git worktree 容器：里面的 checkout 是同一模块的
	// 副本，被选中会让同一份代码出现两个"模块根"，且相对路径对不上索引。
	"worktrees": true,
}

// SemanticWorkspaceLanguage 报告 workspace 的语义服务语言；v1 仅 Go（需要
// 可解析的 Go 模块——gopls 的类型信息依赖它）。空串表示不支持。
func SemanticWorkspaceLanguage(workspaceRoot string) string {
	if SemanticModuleRoot(workspaceRoot) == "" {
		return ""
	}
	return "go"
}

// SemanticModuleRoot 返回 workspace 内的 Go 模块根（含 go.mod 的目录），空串表示
// 未找到。
//
// 口径（ADR-0002 §4.1 的保守一侧）：优先 `<workspace>/go.mod`；否则按深度受限的
// 广度优先向下探测，返回**最浅**的模块根。同深度出现多个模块根时返回空串——
// v1 单通道只能锚定一个根，与其猜一个不如降级到索引通道（Degrade-Not-Fail）。
// 目录遍历按名字排序，保证探测结果确定（可复现，不随文件系统枚举顺序漂移）。
func SemanticModuleRoot(workspaceRoot string) string {
	root := strings.TrimSpace(workspaceRoot)
	if root == "" {
		return ""
	}
	if hasGoMod(root) {
		return root
	}
	type candidate struct {
		depth int
		path  string
	}
	frontier := []string{root}
	visited := map[string]bool{root: true}
	for depth := 1; depth <= maxModuleRootDepth && len(frontier) > 0; depth++ {
		var next []string
		var hits []candidate
		for _, dir := range frontier {
			entries, err := os.ReadDir(dir)
			if err != nil {
				continue // 不可读目录：跳过，不让探测整体失败
			}
			names := make([]string, 0, len(entries))
			for _, entry := range entries {
				if !entry.IsDir() {
					continue
				}
				names = append(names, entry.Name())
			}
			sort.Strings(names)
			for _, name := range names {
				// 隐藏目录一律跳过（含 .aicli / .git / .github 等）：模块根几乎不会
				// 是隐藏目录，跳过它们同时省掉了 skippedModuleRootDirs 的大部分条目。
				if strings.HasPrefix(name, ".") || skippedModuleRootDirs[name] {
					continue
				}
				child := filepath.Join(dir, name)
				// 独立 checkout（git worktree / 子模块 / vendored repo）一律跳过：
				// 它是当前模块的副本，不是 workspace 的模块根。
				if isSeparateCheckout(child) {
					continue
				}
				if hasGoMod(child) {
					hits = append(hits, candidate{depth: depth, path: child})
					continue // 模块根不再向下钻（嵌套模块归属最浅者）
				}
				if visited[child] {
					continue
				}
				visited[child] = true
				next = append(next, child)
			}
		}
		if len(hits) == 1 {
			return hits[0].path
		}
		if len(hits) > 1 {
			// 同深度多模块：单通道锚定不了，交给上层降级。
			return ""
		}
		frontier = next
	}
	return ""
}

func hasGoMod(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, "go.mod"))
	return err == nil && !info.IsDir()
}

// isSeparateCheckout 报告目录是否是一个独立 checkout 的根（含 .git 条目）。
func isSeparateCheckout(dir string) bool {
	if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
		return true
	}
	return false
}

// NewSemanticAdapterForWorkspace 按配置为 workspace 构造语义适配器。
//
// 返回 (adapter, reason)：adapter == nil 时 reason 为稳定降级 token；
// 构造本身**不启动进程**——首次查询时由适配器内部 Ensure（受 startup_timeout
// 约束），启动失败同样降级。门控与 ADR-0002 §4.1 一致：
// Enabled=false 是硬闸；Mode 仅接受 self（external 由宿主侧集成，不在自管通道）。
//
// 两个根的分工（不要混淆）：
//   - adapter.root = workspace 根：结果路径相对化的锚点，必须与索引 FilePaths
//     同源，否则工具侧拼不出可读路径；
//   - manager Root = 模块根：gopls 解析类型信息的锚点，同时也是 ADR-0002 §4.4
//     锁的位置（按模块而非按 workspace 粒度去重）。
func NewSemanticAdapterForWorkspace(cfg Config, workspaceRoot string) (SemanticAdapter, string) {
	return NewSemanticAdapterForWorkspaceWithOptions(cfg, workspaceRoot, SemanticAdapterOptions{})
}

// SemanticAdapterOptions 是语义通道的宿主协作缝。
type SemanticAdapterOptions struct {
	// SharedClient 让语义通道复用宿主已持有的语言服务器进程（诊断池）。
	//
	// 非 nil = 单实例模式：本通道**不 spawn**，只在宿主给出活进程时工作，借不到
	// 就降级（错误文案明确说明"宿主没有可用进程"，不会误导成配置错误）。判据是
	// 内存而不是洁癖：两条链路都要 gopls，双实例让常驻内存翻倍，而本仓库实测的
	// gopls 崩溃根因正是整机内存耗尽。
	//
	// nil = 自建模式（诊断池关闭、只有 knowledge.lsp 打开）：行为与本选项引入
	// 前完全一致，含 ADR-0002 §4.4 的锁。
	SharedClient func(ctx context.Context, serverName string) *baselsp.Client
}

// NewSemanticAdapterForWorkspaceWithOptions 是带宿主协作缝的构造函数。
func NewSemanticAdapterForWorkspaceWithOptions(cfg Config, workspaceRoot string, opts SemanticAdapterOptions) (SemanticAdapter, string) {
	cfg = cfg.Normalize()
	if !cfg.LSP.Enabled {
		return nil, SemanticReasonDisabled
	}
	if mode := strings.ToLower(strings.TrimSpace(cfg.LSP.Mode)); mode != "self" {
		return nil, SemanticReasonModeNotSelf
	}
	root := strings.TrimSpace(workspaceRoot)
	if root == "" {
		return nil, SemanticReasonWorkspace
	}
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	moduleRoot := SemanticModuleRoot(root)
	if moduleRoot == "" {
		return nil, SemanticReasonNoLanguage
	}
	specs := baselsp.PresetServersNamed([]string{"gopls"})
	if len(specs) == 0 {
		return nil, SemanticReasonServerSpecMiss
	}
	manager := knowledgelsp.NewManager(knowledgelsp.Options{
		Root:           moduleRoot,
		Spec:           specs[0],
		MaxProcesses:   cfg.LSP.MaxProcesses,
		MemoryLimitMB:  cfg.LSP.MemoryLimitMB,
		StartupTimeout: cfg.LSP.StartupTimeout,
		RequestTimeout: cfg.LSP.RequestTimeout,
		SharedClient:   opts.SharedClient,
	})
	return NewLSPSemanticAdapter(manager, root), ""
}
