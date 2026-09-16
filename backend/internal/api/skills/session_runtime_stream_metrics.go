package skills

import (
	"net/http"
	"sync"
	"time"
)

// Batch 3 · 传输增强（方案 §4 Batch 3 表 / §7 验收）：runtime/stream 的连接级
// 指标并入既有 runtimeStatusSnapshot（键 `runtime_event_delivery`），不新增端点。
//
// 指标口径：
//   - active_connections：当前打开的 SSE 连接数（进入 handler +1，退出 −1）；
//   - frames_sent：本进程已写出的 runtime_event 帧数（注释帧不计）；
//   - bytes_sent：写进 http.ResponseWriter 的响应体字节数（含帧头与注释帧，
//     即真实出网字节的近似——不含 HTTP 头）；
//   - dump_duration_ms / dump_pages：存储层 dump 的累计耗时与分页查询次数
//     （合帧/去重后的帧数下降会直接反映在 frames_sent / dump_pages 上）；
//   - coalesced_frames / coalesced_rows：服务端合帧下发的帧数与折叠的输入行数
//     （Batch 3 合帧收益的实测口径，"行 → 帧"压缩比 = rows / frames）；
//   - dropped_live：B 通道 latest-wins 因键数超限真正丢弃的帧数；
//   - retry_count：存储层瞬时错误触发的重试次数（含注释帧留痕次数）。
//
// 只增不改：既有键与形状保持不变，新指标收在 `stream` 子对象里。
type runtimeEventStreamMetricsCounters struct {
	mu                sync.Mutex
	activeConnections int64
	framesSent        uint64
	bytesSent         uint64
	dumpDurationMs    uint64
	dumpPages         uint64
	coalescedFrames   uint64
	coalescedRows     uint64
	droppedLive       uint64
	retryCount        uint64
}

var runtimeEventStreamMetrics = &runtimeEventStreamMetricsCounters{}

func runtimeEventStreamConnectionOpened() {
	c := runtimeEventStreamMetrics
	c.mu.Lock()
	c.activeConnections++
	c.mu.Unlock()
}

func runtimeEventStreamConnectionClosed() {
	c := runtimeEventStreamMetrics
	c.mu.Lock()
	if c.activeConnections > 0 {
		c.activeConnections--
	}
	c.mu.Unlock()
}

func recordRuntimeEventStreamFrame() {
	c := runtimeEventStreamMetrics
	c.mu.Lock()
	c.framesSent++
	c.mu.Unlock()
}

func recordRuntimeEventStreamBytes(count int) {
	if count <= 0 {
		return
	}
	c := runtimeEventStreamMetrics
	c.mu.Lock()
	c.bytesSent += uint64(count)
	c.mu.Unlock()
}

func recordRuntimeEventStreamDump(duration time.Duration, pages int) {
	c := runtimeEventStreamMetrics
	c.mu.Lock()
	if duration > 0 {
		c.dumpDurationMs += uint64(duration.Milliseconds())
	}
	if pages > 0 {
		c.dumpPages += uint64(pages)
	}
	c.mu.Unlock()
}

// recordRuntimeEventStreamCoalesced 记一次合帧：mergedFrames 是**至少折叠过一行**的
// 输出帧数（合并组数），absorbedRows 是被折叠掉、因而少下发的输入行数。两者一起
// 才能还原压缩比（组数 = 输入行数 − 输出帧数 的分解口径，见 Batch 3 验收）。
func recordRuntimeEventStreamCoalesced(mergedFrames, absorbedRows int) {
	if mergedFrames <= 0 && absorbedRows <= 0 {
		return
	}
	c := runtimeEventStreamMetrics
	c.mu.Lock()
	if mergedFrames > 0 {
		c.coalescedFrames += uint64(mergedFrames)
	}
	if absorbedRows > 0 {
		c.coalescedRows += uint64(absorbedRows)
	}
	c.mu.Unlock()
}

func recordRuntimeEventStreamLiveDrop(count int) {
	if count <= 0 {
		return
	}
	c := runtimeEventStreamMetrics
	c.mu.Lock()
	c.droppedLive += uint64(count)
	c.mu.Unlock()
}

func recordRuntimeEventStreamRetry() {
	c := runtimeEventStreamMetrics
	c.mu.Lock()
	c.retryCount++
	c.mu.Unlock()
}

// runtimeEventStreamMetricsSnapshot 返回可 JSON 序列化的快照（挂在
// runtime_event_delivery.stream 键下）。
func runtimeEventStreamMetricsSnapshot() map[string]interface{} {
	c := runtimeEventStreamMetrics
	c.mu.Lock()
	defer c.mu.Unlock()
	return map[string]interface{}{
		"active_connections": c.activeConnections,
		"frames_sent":        c.framesSent,
		"bytes_sent":         c.bytesSent,
		"dump_duration_ms":   c.dumpDurationMs,
		"dump_pages":         c.dumpPages,
		"coalesced_frames":   c.coalescedFrames,
		"coalesced_rows":     c.coalescedRows,
		"dropped_live":       c.droppedLive,
		"retry_count":        c.retryCount,
	}
}

// resetRuntimeEventStreamMetricsForTest 只供本包测试隔离使用。
func resetRuntimeEventStreamMetricsForTest() {
	c := runtimeEventStreamMetrics
	c.mu.Lock()
	defer c.mu.Unlock()
	c.activeConnections = 0
	c.framesSent = 0
	c.bytesSent = 0
	c.dumpDurationMs = 0
	c.dumpPages = 0
	c.coalescedFrames = 0
	c.coalescedRows = 0
	c.droppedLive = 0
	c.retryCount = 0
}

// streamCountingResponseWriter 统计写进响应体的字节数（bytes_sent 口径）。
// 它只包在 bufio 与 net/http 之间，不实现 http.Flusher——flush 语义仍由外层
// 原始 ResponseWriter 承担（见 StreamSessionRuntimeEvents 的双层 flush 注释）。
type streamCountingResponseWriter struct {
	http.ResponseWriter
}

func (w streamCountingResponseWriter) Write(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p)
	recordRuntimeEventStreamBytes(n)
	return n, err
}
