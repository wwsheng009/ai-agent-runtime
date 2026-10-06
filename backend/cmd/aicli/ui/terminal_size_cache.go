package ui

import (
	"os"
	"sync"

	"golang.org/x/term"
)

// processTerminalSizeCache 是进程级「最近一次真实探测」的尺寸备忘。
//
// 几何收敛目标（P1-2b）：唯一 raw 探测点是 TerminalDriver.ProbeSize；
// Terminal.updateSize / RefreshSize 探测成功后把结果发布到这里；
// GetTerminalWidth/Height/Size（theme.go/terminal.go）与 30+ 展示宽度调用点
// 直接读缓存转发，不再各自调用 term.GetSize。
//
// 只发布「真实测得」的尺寸（GetSize 成功且 >0）；管道/无 TTY 下的 80x24
// 兜底不发布，避免把测试环境兜底值固化成进程权威。
var processTerminalSizeCache struct {
	mu     sync.RWMutex
	width  int
	height int
}

// publishProcessTerminalSize 记录一次真实探测结果（非正数忽略）。
func publishProcessTerminalSize(width, height int) {
	if width <= 0 || height <= 0 {
		return
	}
	processTerminalSizeCache.mu.Lock()
	processTerminalSizeCache.width = width
	processTerminalSizeCache.height = height
	processTerminalSizeCache.mu.Unlock()
}

// cachedProcessTerminalSize 返回最近一次真实探测结果；无有效缓存时 ok=false。
func cachedProcessTerminalSize() (width, height int, ok bool) {
	processTerminalSizeCache.mu.RLock()
	defer processTerminalSizeCache.mu.RUnlock()
	if processTerminalSizeCache.width > 0 && processTerminalSizeCache.height > 0 {
		return processTerminalSizeCache.width, processTerminalSizeCache.height, true
	}
	return 0, 0, false
}

// probeHostTerminalSize 是无缓存时的一次性 raw 探测（唯一 syscall 回退点）。
// 探测成功会发布到缓存，后续调用点走缓存转发。
func probeHostTerminalSize() (width, height int, ok bool) {
	width, height, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || width <= 0 || height <= 0 {
		return 0, 0, false
	}
	publishProcessTerminalSize(width, height)
	return width, height, true
}

// resetProcessTerminalSizeCacheForTest 清空进程缓存，供测试隔离使用。
func resetProcessTerminalSizeCacheForTest() {
	processTerminalSizeCache.mu.Lock()
	processTerminalSizeCache.width = 0
	processTerminalSizeCache.height = 0
	processTerminalSizeCache.mu.Unlock()
}
