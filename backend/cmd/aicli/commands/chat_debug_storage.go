package commands

import (
	"fmt"
	"strings"
	"time"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
)

// ============================================================================
// 存储与持久化区块（P1.5 / P1.6 / P1.7 观测接线）
//
// 数据源与 runtime-server /api/runtime/health 的 `persist` / `store_pools`
// 段同源：都直接取 internal/chat 的 Go 访问器（PoolStats / AppendTimingStats /
// ContentionStats / EventPersistBuffer.Stats），JSON 字段名沿用这些结构体的
// tag，不另造第二份契约。本区块只做只读快照，全部是原子读/短锁，不做 IO。
//
// 接线范围：aicli `/debug display`（TUI）与 `/web/api/status?format=text`
// 共用同一份文档；`/web/api/status`（JSON）走 chatDebugDisplaySnapshot.Storage。
// ============================================================================

// chatDebugPoolStatsProvider 是 *runtimechat.SQLiteRuntimeStore 的只读统计子集。
type chatDebugPoolStatsProvider interface {
	PoolStats() runtimechat.RuntimeStorePoolStats
}

type chatDebugAppendStatsProvider interface {
	AppendTimingStats() runtimechat.AppendTimingStats
}

type chatDebugContentionStatsProvider interface {
	ContentionStats() runtimechat.SQLiteContentionStats
}

type chatDebugPersistStatsProvider interface {
	Stats() runtimechat.EventPersistBufferStats
}

// chatDebugStorageInfo 是存储/持久化快照；各子段按能力出现（不支持则该键缺失）。
type chatDebugStorageInfo struct {
	StorePath  string                               `json:"store_path,omitempty"`
	Pool       *runtimechat.RuntimeStorePoolStats   `json:"pool,omitempty"`
	Append     *runtimechat.AppendTimingStats       `json:"append,omitempty"`
	Contention *runtimechat.SQLiteContentionStats   `json:"contention,omitempty"`
	Persist    *runtimechat.EventPersistBufferStats `json:"persist,omitempty"`
}

// chatDebugStorageSnapshot 从会话的本地宿主采集存储/持久化快照。
// 无宿主、无 store 且无批量缓冲时返回 nil（文档与 JSON 都不出现该区块，
// 保持既有响应形状）。
func chatDebugStorageSnapshot(session *ChatSession) *chatDebugStorageInfo {
	host := localChatRuntimeHostOf(session)
	if host == nil {
		return nil
	}
	var buffer chatDebugPersistStatsProvider
	if host.runtimeEventBuffer != nil {
		buffer = host.runtimeEventBuffer
	}
	return chatDebugStorageSnapshotFrom(host.EventStore, buffer, currentRuntimeSessionPath(session))
}

// chatDebugStorageSnapshotFrom 是 host 无关的采集核心（便于测试注入 provider）。
// store 为 any：只有实现了对应统计子集的实现才会填对应子段。
func chatDebugStorageSnapshotFrom(store any, buffer chatDebugPersistStatsProvider, storePath string) *chatDebugStorageInfo {
	info := &chatDebugStorageInfo{StorePath: strings.TrimSpace(storePath)}
	if provider, ok := store.(chatDebugPoolStatsProvider); ok {
		stats := provider.PoolStats()
		info.Pool = &stats
	}
	if provider, ok := store.(chatDebugAppendStatsProvider); ok {
		stats := provider.AppendTimingStats()
		info.Append = &stats
	}
	if provider, ok := store.(chatDebugContentionStatsProvider); ok {
		stats := provider.ContentionStats()
		info.Contention = &stats
	}
	if buffer != nil {
		stats := buffer.Stats()
		info.Persist = &stats
	}
	if info.Pool == nil && info.Append == nil && info.Contention == nil && info.Persist == nil {
		return nil
	}
	return info
}

// appendChatDebugStorageLines 输出"存储与持久化:"子区块（挂在"运行时组件"之后）。
func appendChatDebugStorageLines(builder *chatDebugDocumentBuilder, session *ChatSession) {
	info := chatDebugStorageSnapshot(session)
	if info == nil {
		return
	}
	builder.heading("存储与持久化: (GET /debug/chat/status#storage)")
	builder.meta("Runtime Store:", chatDebugValueOrNone(info.StorePath))
	if pool := info.Pool; pool != nil {
		builder.meta("Write Pool:", fmt.Sprintf("open=%d/%d waits=%d wait=%s",
			pool.WriteOpenConnections, pool.WriteMaxOpenConnections,
			pool.WriteWaitCount, chatDebugNanosText(int64(pool.WriteWaitDuration))))
		readLine := fmt.Sprintf("open=%t conns=%d/%d waits=%d wait=%s queries=%d clamps=%d",
			pool.ReadPoolOpen, pool.ReadOpenConnections, pool.ReadMaxOpenConnections,
			pool.ReadWaitCount, chatDebugNanosText(int64(pool.ReadWaitDuration)),
			pool.ReadQueries, pool.ReadLimitClamps)
		if pool.ReadPoolDegraded {
			readLine += fmt.Sprintf(" degraded=true(%s) open_errors=%d",
				chatDebugValueOrNone(pool.ReadPoolDegradedReason), pool.ReadPoolOpenErrors)
		}
		builder.meta("Read Pool:", readLine)
		builder.meta("WAL:", chatDebugBytesText(pool.WALSizeBytes))
	}
	if appendStats := info.Append; appendStats != nil {
		avg := int64(0)
		if appendStats.Appends > 0 {
			avg = appendStats.TotalNs / appendStats.Appends
		}
		lockAvg := int64(0)
		if appendStats.BatchedEvents > 0 {
			lockAvg = appendStats.BatchLockHoldNs / appendStats.BatchedEvents
		}
		builder.meta("Append:", fmt.Sprintf(
			"path=%s appends=%d legacy=%d avg=%s max=%s lock_hold_avg=%s batches=%d batched=%d avg_batch=%.1f",
			chatDebugAppendPath(appendStats), appendStats.Appends, appendStats.LegacyUsed,
			chatDebugNanosText(avg), chatDebugNanosText(appendStats.MaxNs), chatDebugNanosText(lockAvg),
			appendStats.Batches, appendStats.BatchedEvents,
			chatDebugRatio(float64(appendStats.BatchedEvents), float64(appendStats.Batches))))
		builder.meta("Maintenance:", fmt.Sprintf(
			"runs=%d skipped=%d busy=%d failures=%d pending=%t prune=%d vacuum=%d(%s)",
			appendStats.MaintenanceRuns, appendStats.MaintenanceSkipped, appendStats.MaintenanceBusy,
			appendStats.MaintenanceFailures, appendStats.MaintenancePending,
			appendStats.PruneRuns, appendStats.VacuumRuns, chatDebugNanosText(appendStats.VacuumTotalNs)))
		builder.meta("SQLite:", fmt.Sprintf("%s supports_returning=%t",
			chatDebugValueOrNone(appendStats.SQLiteVersion), appendStats.SupportsReturning))
	}
	if contention := info.Contention; contention != nil {
		builder.meta("Contention:", fmt.Sprintf("busy_retries=%d busy_snapshot_517=%d busy_exhausted=%d",
			contention.BusyRetries, contention.BusySnapshotErrors, contention.BusyExhausted))
	}
	if persist := info.Persist; persist != nil {
		builder.meta("批量落盘:", chatDebugPersistLine(persist))
	} else {
		builder.meta("批量落盘:", "disabled")
	}
}

// chatDebugAppendPath 归纳 append 取号路径（RETURNING / legacy / mixed）。
func chatDebugAppendPath(stats *runtimechat.AppendTimingStats) string {
	switch {
	case stats.ReturningUsed > 0 && stats.LegacyUsed > 0:
		return "mixed"
	case stats.ReturningUsed > 0:
		return "returning"
	case stats.LegacyUsed > 0:
		return "legacy"
	default:
		return "n/a"
	}
}

// chatDebugPersistLine 渲染批量落盘缓冲的一行摘要（含 P2.11 派发遥测）。
func chatDebugPersistLine(stats *runtimechat.EventPersistBufferStats) string {
	line := fmt.Sprintf(
		"enabled=true async=%t enqueued=%d flushed=%d batches=%d avg_batch=%.1f flush_p95=%s queue=%d/%d failed=%d dropped=%d retry=%d sync_fallback=%d",
		stats.AsyncDispatch, stats.Enqueued, stats.FlushedEvents, stats.Batches,
		chatDebugRatio(float64(stats.FlushedEvents), float64(stats.Batches)),
		chatDebugNanosText(stats.FlushP95Ns), stats.QueueDepth, stats.QueueLimit,
		stats.Failed, stats.Dropped, stats.Retried, stats.SyncFallback)
	if stats.Degraded {
		line += " degraded=true"
	}
	return line
}

// chatDebugRatio 返回 a/b（b<=0 时为 0），用于批均值等比率展示。
func chatDebugRatio(a, b float64) float64 {
	if b <= 0 {
		return 0
	}
	return a / b
}

// chatDebugNanosText 把纳秒计数渲染为易读时长（<=0 视为 0s，保留微秒精度）。
func chatDebugNanosText(ns int64) string {
	if ns <= 0 {
		return "0s"
	}
	return time.Duration(ns).Round(time.Microsecond).String()
}

// chatDebugBytesText 把字节数渲染为 B/KiB/MiB/GiB。
func chatDebugBytesText(bytes int64) string {
	if bytes <= 0 {
		return "0 B"
	}
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	value := float64(bytes)
	for _, suffix := range []string{"KiB", "MiB", "GiB", "TiB"} {
		value /= unit
		if value < unit {
			return fmt.Sprintf("%.1f %s", value, suffix)
		}
	}
	return fmt.Sprintf("%.1f PiB", value/unit)
}
