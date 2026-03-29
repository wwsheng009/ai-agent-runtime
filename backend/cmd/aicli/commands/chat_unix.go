//go:build !windows

package commands

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

// setupSignalHandler 设置信号处理器（Unix-like 特定）
// Unix-like: 支持 Ctrl+C (SIGINT), Ctrl+Break (SIGTERM), ESC 键 (SIGUSR2)
func setupSignalHandler(session *ChatSession, sigChan chan os.Signal, sigCountChan chan<- int) {
	// Unix-like: 支持 SIGINT (Ctrl+C), SIGTERM (Ctrl+Break), SIGUSR2 (ESC)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM, syscall.SIGUSR2)

	go func() {
		sigCount := 0
		lastSigTime := time.Now()

		for sig := range sigChan {
			// SIGUSR2 表示 ESC 键，直接中断不计数
			if sig == syscall.SIGUSR2 {
				session.Interrupt()
				fmt.Print("\n")
				ui.PrintInfo("已中断 - 输入已取消")
				continue
			}

			// 如果两次信号间隔超过 2 秒，重置计数
			if time.Since(lastSigTime) > 2*time.Second {
				sigCount = 0
			}
			sigCount++
			lastSigTime = time.Now()

			// 发送当前信号计数
			select {
			case sigCountChan <- sigCount:
			default:
			}

			if sigCount == 1 {
				// 第一次 Ctrl+C：中断当前操作
				session.Interrupt()
				fmt.Print("\n")
				ui.PrintInfo("已中断 - 输入已取消")
			}

			if sigCount >= 2 {
				// 第二次 Ctrl+C：正常退出循环（会保存日志）
				fmt.Println("\n正在退出...")
				session.interrupted = true
				close(sigCountChan)
				return
			}
		}
	}()
}
