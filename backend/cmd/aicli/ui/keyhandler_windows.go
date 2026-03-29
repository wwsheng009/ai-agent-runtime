//go:build windows

package ui

import (
	"fmt"
)

// Start 启动键盘监听（Windows 系统）
// 不支持 ESC 键监听，只依赖 Ctrl+C (SIGINT)
func (kh *KeyHandler) Start() <-chan bool {
	if kh.enabled {
		return kh.notifyChan
	}

	kh.enabled = true

	// Windows 上不支持 SIGUSR2，只返回空通道
	// 用户应该使用 Ctrl+C 来中断操作
	fmt.Println()
	fmt.Println(IndentAssistantContent("[提示] Windows 系统使用 Ctrl+C 中断操作"))

	return kh.notifyChan
}
