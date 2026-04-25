//go:build windows

package commands

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

// setupSignalHandler 设置信号处理器（Windows 特定）
// Windows: 只支持 Ctrl+C (SIGINT)
func setupSignalHandler(session *ChatSession, sigChan chan os.Signal, sigCountChan chan<- int) {
	// Windows: 只支持 SIGINT (Ctrl+C)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sigCount := 0
		lastSigTime := time.Now()

		for range sigChan {
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
				session.interrupted.Store(true)
				close(sigCountChan)
				return
			}
		}
	}()
}
