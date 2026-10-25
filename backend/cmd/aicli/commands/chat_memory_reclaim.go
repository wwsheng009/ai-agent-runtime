package commands

import (
	"runtime/debug"
	"sync"
	"time"
)

// chat_memory_reclaim.go —— 大批 transcript 逐出收敛后的 OS 内存归还。
//
// 背景：自动逐出把旧 cell 移出 Go 堆后，Go 并不保证立刻把空闲 span 归还给
// 操作系统；长会话的 Private Bytes 会长期维持在内存软上限附近。这里只在大批
// 历史真正完成写穿、驻留窗口已收敛时异步触发一次 FreeOSMemory，并做两分钟
// 冷却，避免每个 changeset 都制造一次全局 GC。
const (
	chatTranscriptReclaimDelay    = 2 * time.Second
	chatTranscriptReclaimCooldown = 2 * time.Minute
)

// 仅测试可替换；生产固定指向 runtime/debug.FreeOSMemory。
var chatTranscriptReclaimNow = debug.FreeOSMemory

var (
	chatTranscriptReclaimMu     sync.Mutex
	chatTranscriptReclaimArmed  bool
	chatTranscriptReclaimLastAt time.Time
)

// scheduleChatTranscriptMemoryReclaim 安排一次去抖的 OS 内存归还。
// 返回 false 表示已 armed 或仍在冷却窗口内。
func scheduleChatTranscriptMemoryReclaim() bool {
	chatTranscriptReclaimMu.Lock()
	if chatTranscriptReclaimArmed || (!chatTranscriptReclaimLastAt.IsZero() &&
		time.Since(chatTranscriptReclaimLastAt) < chatTranscriptReclaimCooldown) {
		chatTranscriptReclaimMu.Unlock()
		return false
	}
	chatTranscriptReclaimArmed = true
	chatTranscriptReclaimLastAt = time.Now()
	chatTranscriptReclaimMu.Unlock()

	time.AfterFunc(chatTranscriptReclaimDelay, func() {
		defer func() {
			chatTranscriptReclaimMu.Lock()
			chatTranscriptReclaimArmed = false
			chatTranscriptReclaimMu.Unlock()
		}()
		chatTranscriptReclaimNow()
	})
	return true
}
