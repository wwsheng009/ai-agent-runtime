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
// process-compat 与 legacy 适配器/测试入口。active unified session 的字节由
// TerminalSession/gateway 的注入 writer 承载，不触达本 sink。
//
// 修正（G11 文档漂移，2026-10-06）：旧注释声称生产路径经
// Terminal.SetLegacyBinding / FixedBottomSurface.SetLegacyBinding 重定向到
// gateway binding——该 API 从未落地（全仓无实现）。legacy 适配器的隔离由
// FixedBottomSurface 的 physical fence 保证；本函数不得被 late goroutine
// 用来补写旧 session 的输出。
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
