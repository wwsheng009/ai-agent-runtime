package ui

import (
	"io"
	"os"
	"sync"
)

// processTerminalOutput is the compatibility sink used by legacy immediate
// terminal controls. The owned TerminalSession has its own injected writer;
// this sink exists only for the remaining legacy UI adapters.
//
// Keeping the indirection synchronized matters even before those adapters are
// removed: tests replace stdout to reconstruct ANSI frames while the UI actor
// is allowed to complete an already-posted facade action. Reading os.Stdout
// directly in that actor races with the replacement and can write to a closed
// capture pipe. The proxy serializes writer selection with the actual write.
var processTerminalOutput = struct {
	mu     sync.RWMutex
	writer io.Writer
}{writer: os.Stdout}

type processTerminalOutputProxy struct{}

func (processTerminalOutputProxy) Write(p []byte) (int, error) {
	processTerminalOutput.mu.RLock()
	defer processTerminalOutput.mu.RUnlock()
	if processTerminalOutput.writer == nil {
		return len(p), nil
	}
	return processTerminalOutput.writer.Write(p)
}

var terminalOutputProxy io.Writer = processTerminalOutputProxy{}

// TerminalOutput returns the process terminal sink for legacy UI controls.
// Callers must pass this writer through existing terminal serialization rather
// than retaining os.Stdout directly.
//
// Phase 3 降级语义（8.2 接入顺序 6）：TerminalOutput() 只保留启动前
// process-compat 与测试入口。active session 期间不动态解析"当前 session"；
// 生产 interactive 路径通过 Terminal.SetLegacyBinding /
// FixedBottomSurface.SetLegacyBinding 把输出重定向到 gateway binding——
// binding 存在时相应方法不触达本 writer（physical fence）。本函数自身
// 不得被 late goroutine 用来补写旧 session 的输出。
func TerminalOutput() io.Writer {
	return terminalOutputProxy
}

// SetTerminalOutputForTesting temporarily redirects legacy UI writes and
// returns an idempotent restore function. It is intentionally test-facing:
// production terminal ownership is selected once when a chat session starts.
func SetTerminalOutputForTesting(writer io.Writer) func() {
	if writer == nil {
		writer = io.Discard
	}
	processTerminalOutput.mu.Lock()
	previous := processTerminalOutput.writer
	processTerminalOutput.writer = writer
	processTerminalOutput.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			processTerminalOutput.mu.Lock()
			processTerminalOutput.writer = previous
			processTerminalOutput.mu.Unlock()
		})
	}
}
