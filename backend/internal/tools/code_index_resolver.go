package tools

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/knowledge"
	baselsp "github.com/wwsheng009/ai-agent-runtime/internal/lsp"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit/tools"
)

// Phase 3（06 §4 Phase 3）：code.* 工具面的只读索引解析。
//
// 口径：
//   - mode=off / 库文件不存在 / 打开失败 → 不可用（工具按降级协议 fallback 到 grep/view）；
//   - 只读打开（OpenStore(readOnly=true)）：读者永不写库、不参与 owner 仲裁，
//     因此不会与 owner 进程争锁（ADR-0001 单写者 / 多读者）；
//   - 句柄按 db 路径在进程内缓存；库文件被重建/替换（size 或 mtime 变化）时
//     下一次解析重开只读句柄，避免进程永久指向旧快照。

// codeIndexCache 是进程级只读句柄缓存（key = knowledge.db 绝对路径）。
type codeIndexCache struct {
	mu      sync.Mutex
	entries map[string]*codeIndexEntry
}

type codeIndexEntry struct {
	store   knowledge.Store
	err     error
	size    int64
	modTime time.Time
}

var codeIndexCacheGlobal = &codeIndexCache{entries: map[string]*codeIndexEntry{}}

// newCodeIndexResolver 返回按调用时刻 workspace 解析只读索引的解析器。
//
// workspace 解析顺序：ctx 的 workspace root（会话级绑定，优先）→ 管理器配置
// 的 workspace root（进程级）。两者都没有时视为不可用（fail closed）。
func newCodeIndexResolver(cfg knowledge.Config, workspaceRoot string) tools.CodeIndexResolver {
	return newCodeIndexResolverWithShared(cfg, workspaceRoot, nil)
}

// newCodeIndexResolverWithShared 是带宿主语言服务器借用缝的解析器构造。
//
// shared != nil 时语义通道只借用诊断池的进程（单实例），不再自己 spawn。
func newCodeIndexResolverWithShared(cfg knowledge.Config, workspaceRoot string, shared func(context.Context, string) *baselsp.Client) tools.CodeIndexResolver {
	base := cfg.Normalize()
	root := strings.TrimSpace(workspaceRoot)
	return func(ctx context.Context) (*tools.CodeIndexHandle, bool) {
		if base.Mode == knowledge.ModeOff {
			return nil, false
		}
		wsRoot := strings.TrimSpace(toolctx.WorkspaceRoot(ctx))
		if wsRoot == "" {
			wsRoot = root
		}
		if wsRoot == "" {
			return nil, false
		}
		resolved := base.WithWorkspace(wsRoot)
		path := knowledge.StorePathFor(resolved)
		if strings.TrimSpace(path) == "" {
			return nil, false
		}
		if _, err := os.Stat(path); err != nil {
			// 尚未建库：索引不可用（工具 fallback），不是错误。
			return nil, false
		}
		store, err := codeIndexCacheGlobal.open(ctx, path)
		if err != nil || store == nil {
			return nil, false
		}
		// 快照视图（filePaths / FileStamps / IndexedAt）按 TTL 复用：这些量只在
		// 索引推进时才变，而解析器在一次 think 里会被调用多次，每次重算都要跑
		// 查询并把全部文件重新物化成三张映射（实测 5,134 文件 23ms，文件数更多
		// 时线性放大）。缓存方向是保守的，说明见 codeIndexSnapshotCache。
		snapshot, err := codeIndexSnapshotsGlobal.load(ctx, path, wsRoot, func(ctx context.Context) (*codeIndexSnapshot, error) {
			return buildCodeIndexSnapshot(ctx, store, wsRoot)
		})
		if err != nil {
			// 工作区未登记 / 库读失败：索引不可用（工具按降级协议 fallback，fail closed）。
			return nil, false
		}
		// ADR-0004 §4.1/§4.2：快照元数据来自索引最近一次**成功全工作区对账**
		// （IndexedAt = 成功 light 索引的 finished_at，与 status.go 同源；
		// incremental 与预算截断的运行都不续期）。writer 判定复用 knowledge
		// 写锁仲裁：锁持有者是本进程即 writer（本地索引即最新，S=0）。
		//
		// writer 与陈旧度都按**调用时刻**重算，不吃快照的 TTL：缓存回来的只是
		// 库内的 IndexedAt，时间推进带来的陈旧度增长因此不受 TTL 影响。
		writer := knowledge.LockHolderPID(resolved) == os.Getpid()
		snapshotTS := snapshot.indexedAt / 1000
		stalenessSeconds := int64(0)
		if !writer && snapshot.indexedAt > 0 {
			if delta := time.Now().UnixMilli() - snapshot.indexedAt; delta > 0 {
				// 向上取整：60.1s 不得因截断仍落在"新鲜"档（安全一侧）。
				stalenessSeconds = (delta + 999) / 1000
			}
		}
		return &tools.CodeIndexHandle{
			Index:       store,
			Mode:        base.Mode,
			WorkspaceID: snapshot.workspaceID,
			Root:        wsRoot,
			Semantic:    semanticAdapterFor(base, wsRoot, shared),
			// 逐次拷贝：句柄是调用方可见的可变量，历史行为是每次解析返回全新映射；
			// 直接共享缓存里的映射会让两处调用互相看见对方的改动。
			FilePaths:        cloneStringMap(snapshot.filePaths),
			FileStamps:       cloneFileStamps(snapshot.fileStamps),
			FileIDsByPath:    cloneStringMap(snapshot.fileIDsByPath),
			SnapshotTS:       snapshotTS,
			StalenessSeconds: stalenessSeconds,
			Writer:           writer,
			Tier: tools.CodeTierForSnapshot(
				writer, snapshotTS, stalenessSeconds, base.Tools.StaleReaderGradingEnabled(),
			),
		}, true
	}
}

// errCodeIndexWorkspaceUnknown 表示库内没有该 workspace 的登记行——"尚未索引"，
// 不是故障：解析器据此走不可用（工具 fallback）分支。
var errCodeIndexWorkspaceUnknown = errors.New("tools: code index workspace is not registered")

// indexedAtReader 是 knowledge.Store 的可选快路径：只取索引快照时间戳，
// 不计算行数汇总。
//
// 动机：解析器只需要 Stats.IndexedAt 一个标量，而 Stats 的三个 COUNT 聚合在
// 数十万行 refs/symbols 上是重活（实测 355 MiB 库单次 ~2.5s）。Store 接口保持
// 不变，识别不到该能力的实现回退到 Stats（口径相同，只是更贵）。
type indexedAtReader interface {
	IndexedAt(ctx context.Context, workspaceID string) (int64, error)
}

// codeIndexIndexedAt 取索引快照时间戳，优先走 IndexedAt 快路径。
func codeIndexIndexedAt(ctx context.Context, store knowledge.Store, workspaceID string) (int64, error) {
	if reader, ok := store.(indexedAtReader); ok {
		return reader.IndexedAt(ctx, workspaceID)
	}
	stats, err := store.Stats(ctx, workspaceID)
	if err != nil {
		return 0, err
	}
	return stats.IndexedAt, nil
}

// codeIndexSnapshot 是解析器每次解析都要用到的「库内快照视图」。
type codeIndexSnapshot struct {
	workspaceID   string
	filePaths     map[string]string
	fileIDsByPath map[string]string
	fileStamps    map[string]tools.FileStamp
	indexedAt     int64
}

// codeIndexSnapshotTTL 是快照视图的存活时间。
//
// 取 10s 是刻意保守的取舍，两个方向都不会把"陈旧"说成"新鲜"：
//   - IndexedAt 偏旧 → staleness 只会被算得更大 → 档位只会更低；
//   - 文件指纹偏旧 → 与磁盘比对时判 stale 或无法判定 → 关系类工具回退 grep，
//     而不是把未校验的结果当成已校验（正是 code_freshness.go 要消灭的那类静默错误）。
//
// 反过来，缓存窗口内索引刚推进时会短暂停留在更低档位——这是安全一侧的退化。
// 声明为 var 以便测试收紧/关闭。
var codeIndexSnapshotTTL = 10 * time.Second

type codeIndexSnapshotCache struct {
	mu      sync.Mutex
	entries map[string]*codeIndexSnapshotEntry
}

type codeIndexSnapshotEntry struct {
	snapshot  *codeIndexSnapshot
	size      int64
	modTime   time.Time
	fetchedAt time.Time
}

var codeIndexSnapshotsGlobal = &codeIndexSnapshotCache{entries: map[string]*codeIndexSnapshotEntry{}}

// load 返回缓存的快照；库文件 size/mtime 变化或超过 TTL 时重新构建。
//
// 只缓存成功结果：读取失败（库被替换、瞬时 IO 失败）不落缓存，下一次调用重试。
func (c *codeIndexSnapshotCache) load(ctx context.Context, path, wsRoot string, build func(context.Context) (*codeIndexSnapshot, error)) (*codeIndexSnapshot, error) {
	if c == nil {
		return build(ctx)
	}
	info, statErr := os.Stat(path)
	key := path + "\x00" + wsRoot
	c.mu.Lock()
	entry := c.entries[key]
	c.mu.Unlock()
	if statErr == nil && entry != nil && entry.snapshot != nil &&
		entry.size == info.Size() && entry.modTime.Equal(info.ModTime()) &&
		time.Since(entry.fetchedAt) < codeIndexSnapshotTTL {
		return entry.snapshot, nil
	}
	snapshot, err := build(ctx)
	if err != nil {
		return nil, err
	}
	if statErr == nil {
		c.mu.Lock()
		c.entries[key] = &codeIndexSnapshotEntry{
			snapshot:  snapshot,
			size:      info.Size(),
			modTime:   info.ModTime(),
			fetchedAt: time.Now(),
		}
		c.mu.Unlock()
	}
	return snapshot, nil
}

// reset 清空缓存（测试路径用；生产路径随进程存活）。
func (c *codeIndexSnapshotCache) reset() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.entries = map[string]*codeIndexSnapshotEntry{}
	c.mu.Unlock()
}

// buildCodeIndexSnapshot 组装一次快照视图（只在缓存未命中时执行）。
func buildCodeIndexSnapshot(ctx context.Context, store knowledge.Store, wsRoot string) (*codeIndexSnapshot, error) {
	wsID, ok, err := store.FindWorkspace(ctx, wsRoot)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errCodeIndexWorkspaceUnknown
	}
	snapshot := &codeIndexSnapshot{
		workspaceID:   wsID,
		filePaths:     map[string]string{},
		fileIDsByPath: map[string]string{},
		fileStamps:    map[string]tools.FileStamp{},
	}
	// 文件指纹与 filePaths 取自同一次 ListActiveFiles，无额外查询成本。
	// 关系类工具据此做文件级新鲜度校验（全局 staleness_seconds 挡不住
	// "索引一直在写别的文件、本次查询的文件却几小时没更新"这一失效模式）。
	if files, err := store.ListActiveFiles(ctx, wsID); err == nil {
		for _, file := range files {
			if file.DeletedAt != 0 || strings.TrimSpace(file.ID) == "" {
				continue
			}
			snapshot.filePaths[file.ID] = file.Path
			snapshot.fileIDsByPath[file.Path] = file.ID
			snapshot.fileStamps[file.ID] = tools.FileStamp{
				MTimeNS:     file.MTimeNS,
				Size:        file.Size,
				ContentHash: file.ContentHash,
			}
		}
	}
	indexedAt, err := codeIndexIndexedAt(ctx, store, wsID)
	if err != nil {
		return nil, err
	}
	snapshot.indexedAt = indexedAt
	return snapshot, nil
}

func cloneStringMap(src map[string]string) map[string]string {
	if src == nil {
		return map[string]string{}
	}
	out := make(map[string]string, len(src))
	for key, value := range src {
		out[key] = value
	}
	return out
}

func cloneFileStamps(src map[string]tools.FileStamp) map[string]tools.FileStamp {
	if src == nil {
		return map[string]tools.FileStamp{}
	}
	out := make(map[string]tools.FileStamp, len(src))
	for key, value := range src {
		out[key] = value
	}
	return out
}

// semanticAdapterCache 是进程级语义适配器缓存（key = root|enabled|mode）。
//
// 只缓存成功构造的适配器：构造失败（未启用/不支持的语言/缺 server spec）
// 不缓存——配置修正后下一次调用即可生效。超时/上限等细粒度配置变化不参与
// key（进程内配置通常来自同一份 runtime 配置，重建进程即刷新）。
var semanticAdapterCache = struct {
	mu      sync.Mutex
	entries map[string]knowledge.SemanticAdapter
}{entries: map[string]knowledge.SemanticAdapter{}}

// semanticAdapterFor 返回（必要时构造）workspace 的语义适配器；nil = 不可用。
func semanticAdapterFor(cfg knowledge.Config, root string, shared func(context.Context, string) *baselsp.Client) knowledge.SemanticAdapter {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil
	}
	// 缓存键必须区分单实例/自建两种模式：借来的适配器与自建的适配器生命周期
	// 与降级语义完全不同，共用缓存会让一个模式悄悄服务另一个模式的调用方。
	key := root + "|" + strconv.FormatBool(cfg.LSP.Enabled) + "|" + strings.ToLower(strings.TrimSpace(cfg.LSP.Mode)) +
		"|shared=" + strconv.FormatBool(shared != nil)
	semanticAdapterCache.mu.Lock()
	if adapter, ok := semanticAdapterCache.entries[key]; ok {
		semanticAdapterCache.mu.Unlock()
		return adapter
	}
	semanticAdapterCache.mu.Unlock()

	adapter, _ := knowledge.NewSemanticAdapterForWorkspaceWithOptions(cfg, root, knowledge.SemanticAdapterOptions{
		SharedClient: shared,
	})
	if adapter != nil {
		semanticAdapterCache.mu.Lock()
		semanticAdapterCache.entries[key] = adapter
		semanticAdapterCache.mu.Unlock()
	}
	return adapter
}

// SemanticChannelStatus 返回 workspace 的语义通道观测快照，供宿主状态面
// （/lsp、lsp_servers）渲染。
//
// 为什么必须有这个出口：语义通道的装配失败原因（lsp_disabled /
// no_supported_language / lock held / …）原先在构造时被丢弃，模型侧只能看到
// source=index，用户侧完全无从判断"为什么 code_* 没吃到语义精度"。这是可观测性
// 盲区，补在这里。
//
// 构造成功但尚未 Ensure 时返回 state=idle——语义通道是惰性启动的，那是正常态
// 而不是故障（与诊断池的"starting"同义不同源）。
func (m *Manager) SemanticChannelStatus() (knowledge.SemanticChannelStatus, bool) {
	if m == nil || m.runtimeConfig == nil {
		return knowledge.SemanticChannelStatus{}, false
	}
	root := strings.TrimSpace(m.runtimeConfig.Workspace.Root)
	shared := m.sharedLanguageClient
	adapter := semanticAdapterFor(m.runtimeConfig.Knowledge, root, shared)
	if adapter == nil {
		// 复用工厂的稳定 reason token，而不是自己猜原因。
		_, reason := knowledge.NewSemanticAdapterForWorkspaceWithOptions(
			m.runtimeConfig.Knowledge, root, knowledge.SemanticAdapterOptions{SharedClient: shared})
		state := "disabled"
		// lsp_disabled（设计意图）与"配了但装配不上"（配置/环境问题）对用户的
		// 行动含义不同：前者不该改，后者该查。因此不合并成同一个词。
		if reason != knowledge.SemanticReasonDisabled {
			state = "degraded"
		}
		return knowledge.SemanticChannelStatus{
			Server: "gopls",
			Root:   root,
			State:  state,
			Reason: reason,
		}, true
	}
	if reporter, ok := adapter.(knowledge.SemanticStatusAdapter); ok {
		return reporter.SemanticStatus(), true
	}
	return knowledge.SemanticChannelStatus{Server: "gopls", Root: root, State: "ready"}, true
}

// sharedLanguageClient 是诊断池的进程借用缝（单实例）。
//
// 诊断池启用时返回非 nil 的借用函数：语义通道因此不再 spawn 自己的 gopls，
// 而是复用池里那个进程——同一种语言在一个进程里只有一个 server 实例。
// 诊断池未启用时返回 nil，语义通道按 ADR-0002 §4.4 自建（并持锁），行为不变。
func (m *Manager) sharedLanguageClient(ctx context.Context, serverName string) *baselsp.Client {
	bridge := m.currentLSPBridge()
	if bridge == nil || !bridge.Enabled() {
		return nil
	}
	client, ok := bridge.SharedClient(ctx, serverName)
	if !ok {
		return nil
	}
	return client
}

// closeSemanticAdapters 关闭并清空语义适配器缓存（释放锁文件；测试路径用）。
func closeSemanticAdapters() {
	semanticAdapterCache.mu.Lock()
	entries := semanticAdapterCache.entries
	semanticAdapterCache.entries = map[string]knowledge.SemanticAdapter{}
	semanticAdapterCache.mu.Unlock()
	for _, adapter := range entries {
		if closer, ok := adapter.(interface{ Close(context.Context) error }); ok {
			_ = closer.Close(context.Background())
		}
	}
}

// open 返回（必要时打开并缓存）指定 db 路径的只读句柄。
//
// 失效口径：库文件的 size/mtime 变化（全量重建 / 替换 / checkpoint）时重开。
// 旧句柄刻意不 Close：并发调用可能仍持有它，提前关闭会造成 use-after-close；
// 只读句柄随进程回收，而库重建是低频事件。打开失败不缓存（下次调用重试）。
func (c *codeIndexCache) open(ctx context.Context, path string) (knowledge.Store, error) {
	if c == nil {
		return nil, os.ErrInvalid
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if entry, ok := c.entries[path]; ok && entry != nil && entry.store != nil &&
		entry.size == info.Size() && entry.modTime.Equal(info.ModTime()) {
		return entry.store, entry.err
	}
	store, openErr := knowledge.OpenStore(ctx, path, true)
	if openErr != nil {
		return nil, openErr
	}
	c.entries[path] = &codeIndexEntry{
		store:   store,
		size:    info.Size(),
		modTime: info.ModTime(),
	}
	return store, nil
}

// closeAll 关闭并清空缓存。
//
// 生产路径不调用它：只读句柄随进程存活。测试路径必须调用——Windows 上
// 未关闭的句柄会阻止 t.TempDir 的目录清理（unlinkat: file in use）。
func (c *codeIndexCache) closeAll() {
	if c == nil {
		return
	}
	c.mu.Lock()
	entries := c.entries
	c.entries = map[string]*codeIndexEntry{}
	c.mu.Unlock()
	for _, entry := range entries {
		if entry != nil && entry.store != nil {
			_ = entry.store.Close()
		}
	}
}
