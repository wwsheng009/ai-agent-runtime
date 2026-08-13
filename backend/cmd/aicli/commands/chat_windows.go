//go:build windows

package commands

import (
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// setupSignalHandler installs the Windows Ctrl+C handler. The listener keeps
// running after an exit request so later interrupts keep cancelling active
// work instead of panicking on a closed signal-count channel.
func setupSignalHandler(session *ChatSession, sigChan chan os.Signal, shouldExit *atomic.Bool) func() {
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	state := &chatInterruptExitState{}
	stopCh := make(chan struct{})
	doneCh := make(chan struct{})
	go func() {
		defer close(doneCh)
		for {
			select {
			case <-sigChan:
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
