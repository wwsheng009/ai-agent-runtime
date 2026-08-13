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
						// KeyHandler also translates SIGUSR2 into an ESC event
						// while the turn-scoped watcher is armed; keep the
						// legacy bridge idempotent with that path.
						if session.IsInterrupted() {
							continue
						}
						session.InterruptPreservePendingInput()
					}
					renderChatEscapeInterruptNotice(session)
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
