//go:build !windows

package ui

import (
	"os"
	"os/signal"
	"syscall"
)

// Start 启动键盘监听（Unix-like 系统）
// 使用 SIGUSR2 信号来模拟 ESC 键
// 用户可以通过 kill -USR2 <pid> 来触发 ESC 键
func (kh *KeyHandler) Start() <-chan bool {
	if kh == nil {
		return nil
	}
	if kh.started.Swap(true) {
		return kh.notifyChan
	}

	kh.enabled.Store(true)

	// 在 Unix-like 系统上，监听 SIGUSR2 信号作为 ESC 键模拟
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGUSR2)

	go func() {
		defer close(kh.doneChan)
		defer signal.Stop(sigChan)
		for {
			select {
			case <-sigChan:
				if !kh.armed.Load() || kh.suspended.Load() {
					continue
				}
				// 接收到信号，发送 ESC 通知。这里刻意不打印任何提示：会话级
				// 用户可见反馈由统一中断决策点渲染（"已中断…"/"停止处理中…"），
				// 旧的 "[ESC 检测到 - 中断操作]" 直写会与之重复并与固定 surface
				// 的绘制竞争（2026-10-18 入口审计 R8）。
				select {
				case kh.notifyChan <- true:
				default:
					// 通道已满，忽略
				}
			case <-kh.quitChan:
				return
			}
		}
	}()

	return kh.notifyChan
}
