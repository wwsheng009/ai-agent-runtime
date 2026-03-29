//go:build !windows

package ui

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

// Start 启动键盘监听（Unix-like 系统）
// 使用 SIGUSR2 信号来模拟 ESC 键
// 用户可以通过 kill -USR2 <pid> 来触发 ESC 键
func (kh *KeyHandler) Start() <-chan bool {
	if kh.enabled {
		return kh.notifyChan
	}

	kh.enabled = true

	// 在 Unix-like 系统上，监听 SIGUSR2 信号作为 ESC 键模拟
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGUSR2)

	go func() {
		for {
			select {
			case <-sigChan:
				// 接收到信号，发送 ESC 通知
				select {
				case kh.notifyChan <- true:
					fmt.Println("\n" + IndentAssistantContent("[ESC 检测到 - 中断操作]"))
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
