package tools

import (
	"context"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/knowledge"
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
		wsID, ok, err := store.FindWorkspace(ctx, wsRoot)
		if err != nil || !ok {
			return nil, false
		}
		filePaths := map[string]string{}
		if files, err := store.ListActiveFiles(ctx, wsID); err == nil {
			for _, file := range files {
				if file.DeletedAt != 0 || strings.TrimSpace(file.ID) == "" {
					continue
				}
				filePaths[file.ID] = file.Path
			}
		}
		// ADR-0004 §4.1/§4.2：快照元数据来自索引最近一次成功写事务
		// （Stats.IndexedAt，与 status.go 同源）。writer 判定复用 knowledge
		// 写锁仲裁：锁持有者是本进程即 writer（本地索引即最新，S=0）。
		stats, err := store.Stats(ctx, wsID)
		if err != nil {
			// 陈旧度未知：视为不可用，工具按降级协议 fallback（fail closed）。
			return nil, false
		}
		writer := knowledge.LockHolderPID(resolved) == os.Getpid()
		snapshotTS := stats.IndexedAt / 1000
		stalenessSeconds := int64(0)
		if !writer && stats.IndexedAt > 0 {
			if delta := time.Now().UnixMilli() - stats.IndexedAt; delta > 0 {
				// 向上取整：60.1s 不得因截断仍落在"新鲜"档（安全一侧）。
				stalenessSeconds = (delta + 999) / 1000
			}
		}
		return &tools.CodeIndexHandle{
			Index:            store,
			Mode:             base.Mode,
			WorkspaceID:      wsID,
			Root:             wsRoot,
			Semantic:         semanticAdapterFor(base, wsRoot),
			FilePaths:        filePaths,
			SnapshotTS:       snapshotTS,
			StalenessSeconds: stalenessSeconds,
			Writer:           writer,
			Tier: tools.CodeTierForSnapshot(
				writer, snapshotTS, stalenessSeconds, base.Tools.StaleReaderGradingEnabled(),
			),
		}, true
	}
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
func semanticAdapterFor(cfg knowledge.Config, root string) knowledge.SemanticAdapter {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil
	}
	key := root + "|" + strconv.FormatBool(cfg.LSP.Enabled) + "|" + strings.ToLower(strings.TrimSpace(cfg.LSP.Mode))
	semanticAdapterCache.mu.Lock()
	if adapter, ok := semanticAdapterCache.entries[key]; ok {
		semanticAdapterCache.mu.Unlock()
		return adapter
	}
	semanticAdapterCache.mu.Unlock()

	adapter, _ := knowledge.NewSemanticAdapterForWorkspace(cfg, root)
	if adapter != nil {
		semanticAdapterCache.mu.Lock()
		semanticAdapterCache.entries[key] = adapter
		semanticAdapterCache.mu.Unlock()
	}
	return adapter
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
