//go:build !windows

package commands

import (
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// setupSignalHandler installs the Unix signal handler. SIGINT/SIGTERM use the
// shared interrupt/exit state machine; SIGUSR2 (ESC from the terminal bridge)
// remains interrupt-only.
func setupSignalHandler(session *ChatSession, sigChan chan os.Signal, shouldExit *atomic.Bool) func() {
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM, syscall.SIGUSR2)
	state := &chatInterruptExitState{}
	stopCh := make(chan struct{})
	doneCh := make(chan struct{})
	go func() {
		defer close(doneCh)
		for {
			select {
			case sig := <-sigChan:
				if sig == syscall.SIGUSR2 {
					if session != nil {
						// SIGUSR2 有两个潜在消费者：KeyHandler（交互 TTY，armed
						// 且未 suspend 时经会话级 watcher 进入统一中断决策点）
						// 与本信号处理器（无 KeyHandler 的宿主兜底，例如
						// MobaXterm/SSH/管道场景）。只有 KeyHandler 确实会消费
						// 时才让位，避免一个信号被两条路径各消费一次；其余情况
						// （未 arm / capture 期间 suspend / 无 KeyHandler）本
						// 处理器进入同一决策点，语义与 TUI Esc 完全一致
						// （2026-10-18 入口审计：原先这里直接
						// InterruptPreservePendingInput，绕过了重复 Esc 提示与
						// stalled cleanup 重试）。
						if chatEscapeSignalOwnedByKeyHandler(session) {
							continue
						}
						handleChatEscapeInterruptRequest(session)
					}
					continue
				}
				state.handleInterruptSignal(session, shouldExit, time.Now())
			case <-stopCh:
				return
			}
		}
	}()
	var stopOnce sync.Once
	return func() {
		stopOnce.Do(func() {
			signal.Stop(sigChan)
			close(stopCh)
			<-doneCh
		})
	}
}
