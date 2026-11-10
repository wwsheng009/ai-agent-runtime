package main

import (
	"os"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

// memory_reclaim.go —— 长驻进程的空闲堆归还。
//
// 背景：GOMEMLIMIT/GOGC 只决定"什么时候 GC"，不保证空闲 span 立刻交还
// 操作系统。Windows 上尤其明显：进程在完成一次大会话装载后，即使存活堆
// 已经降到 150 MB，HeapSys 仍可停在 400 MB 以上，任务管理器看到的是历史
// 峰值而不是当前工作集。
//
// 这里只在进程确实空闲时（连续两个采样窗口的累计分配都很低）调用一次
// debug.FreeOSMemory，并设置冷却窗口，避免把主动归还变成周期性全局 GC。
const (
	idleMemoryReclaimInterval  = 30 * time.Second
	idleMemoryReclaimStableFor = 2 * time.Minute
	idleMemoryReclaimCooldown  = 10 * time.Minute
	// 只有"已提交但未归还"的空闲堆达到该阈值才值得做一次全局 GC。
	idleMemoryReclaimMinCommittedIdle uint64 = 64 << 20
	// 单个采样窗口内累计分配低于该值视为空闲。后台健康检查/日志会产生
	// 少量分配，不能要求 TotalAlloc 完全不变。
	idleMemoryReclaimMaxAllocDelta uint64 = 4 << 20
)

type idleMemorySample struct {
	totalAlloc   uint64
	heapIdle     uint64
	heapReleased uint64
}

// idleMemoryReclaimDecision 是纯函数，便于锁定"什么时候允许归还"的边界。
//
// 返回 (是否归还, 更新后的 stableSince)。调用方负责在真正归还后重置
// stableSince 与 cooldown。分配量上升（或计数器回退）会重置稳定窗口；
// 空闲堆不足时保留稳定窗口，等它涨上来再归还，避免白跑一次采样。
func idleMemoryReclaimDecision(now, stableSince, lastReclaim time.Time, previous, current idleMemorySample) (bool, time.Time) {
	if stableSince.IsZero() ||
		current.totalAlloc < previous.totalAlloc ||
		current.totalAlloc-previous.totalAlloc > idleMemoryReclaimMaxAllocDelta {
		return false, now
	}

	committedIdle := uint64(0)
	if current.heapIdle > current.heapReleased {
		committedIdle = current.heapIdle - current.heapReleased
	}
	if committedIdle < idleMemoryReclaimMinCommittedIdle {
		return false, stableSince
	}
	if now.Sub(stableSince) < idleMemoryReclaimStableFor {
		return false, stableSince
	}
	if !lastReclaim.IsZero() && now.Sub(lastReclaim) < idleMemoryReclaimCooldown {
		return false, stableSince
	}
	return true, stableSince
}

// startIdleMemoryReclaimer 启动进程级空闲堆归还循环。生命周期跟随进程；
// 短命令退出时 goroutine 随进程一起结束，不需要额外关闭。
func startIdleMemoryReclaimer() {
	if !idleMemoryReclaimEnabled() {
		return
	}
	go idleMemoryReclaimLoop(nil, idleMemoryReclaimInterval)
}

// idleMemoryReclaimEnabled 尊重用户的显式关闭：
//   - AICLI_MEMORY_RECLAIM=off/none/0/-1 直接关闭空闲归还；
//   - GOGC=off（且 AICLI_GOGC 未覆盖）表示用户明确不要 GC，归还循环也关闭。
//
// 主动 FreeOSMemory 会做一次 GC；用户禁用 GC 时不应在后台偷偷触发。
func idleMemoryReclaimEnabled() bool {
	if raw := strings.TrimSpace(os.Getenv("AICLI_MEMORY_RECLAIM")); raw != "" {
		return !disabledMemoryLimit(raw)
	}
	for _, name := range gcPercentEnvVars {
		raw := strings.TrimSpace(os.Getenv(name))
		if raw == "" {
			continue
		}
		// 按 applyGCPercentFromEnv 的优先级：AICLI_GOGC 先于 GOGC。
		return !disabledMemoryLimit(raw)
	}
	return true
}

func idleMemoryReclaimLoop(stop <-chan struct{}, interval time.Duration) {
	if interval <= 0 {
		interval = idleMemoryReclaimInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var previous idleMemorySample
	var stableSince time.Time
	var lastReclaim time.Time
	initialized := false

	for {
		select {
		case <-stop:
			return
		case now := <-ticker.C:
			var stats runtime.MemStats
			runtime.ReadMemStats(&stats)
			current := idleMemorySample{
				totalAlloc:   stats.TotalAlloc,
				heapIdle:     stats.HeapIdle,
				heapReleased: stats.HeapReleased,
			}
			if !initialized {
				previous = current
				stableSince = now
				initialized = true
				continue
			}

			due, nextStable := idleMemoryReclaimDecision(now, stableSince, lastReclaim, previous, current)
			stableSince = nextStable
			previous = current
			if !due {
				continue
			}

			// 先释放可重建的布局/渲染缓存，再 FreeOSMemory 把空闲 span 交还
			// 操作系统。两者都只在稳定空闲窗口触发，避免影响交互路径。
			ui.TrimIdleCaches()
			debug.FreeOSMemory()
			lastReclaim = time.Now()
			stableSince = lastReclaim
			// FreeOSMemory 自身会做一次 GC；重新采样，避免把这次 GC 的
			// 计数器变化误判成"进程又活跃了"。
			runtime.ReadMemStats(&stats)
			previous = idleMemorySample{
				totalAlloc:   stats.TotalAlloc,
				heapIdle:     stats.HeapIdle,
				heapReleased: stats.HeapReleased,
			}
		}
	}
}
