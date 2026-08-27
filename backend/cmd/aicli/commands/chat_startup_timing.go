package commands

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	logpkg "github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
)

// chatStartupTiming records coarse HandleChat stage durations so startup
// regressions can be diagnosed without an external profiler.
//
// Enable console output with AICLI_STARTUP_TIMING=1. Durations are always
// written to the structured logger at debug level when available.
type chatStartupTiming struct {
	enabled bool
	start   time.Time
	last    time.Time
	marks   []chatStartupMark
}

// activeChatStartupTiming is set for the duration of HandleChat so nested
// helpers can record sub-stage marks without threading the timer everywhere.
var activeChatStartupTiming *chatStartupTiming

type chatStartupMark struct {
	name    string
	at      time.Time
	delta   time.Duration
	elapsed time.Duration
}

func newChatStartupTiming() *chatStartupTiming {
	now := time.Now()
	return &chatStartupTiming{
		enabled: chatStartupTimingEnabled(),
		start:   now,
		last:    now,
		marks:   make([]chatStartupMark, 0, 8),
	}
}

func chatStartupTimingEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("AICLI_STARTUP_TIMING"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func markChatStartup(name string) {
	if activeChatStartupTiming == nil {
		return
	}
	activeChatStartupTiming.mark(name)
}

func (t *chatStartupTiming) mark(name string) {
	if t == nil {
		return
	}
	now := time.Now()
	t.marks = append(t.marks, chatStartupMark{
		name:    strings.TrimSpace(name),
		at:      now,
		delta:   now.Sub(t.last),
		elapsed: now.Sub(t.start),
	})
	t.last = now
}

func (t *chatStartupTiming) reached(name string) bool {
	if t == nil {
		return false
	}
	for _, m := range t.marks {
		if m.name == name {
			return true
		}
	}
	return false
}

// armChatStartupHangWatchdog 在启动 90 秒后仍未到达 ready 阶段时，把全部
// goroutine 栈打印到 stderr——用于定位“aicli chat 启动后长时间无响应”
// （10 分钟级挂起）的卡点。正常启动（几十毫秒到几秒）永不触发。
func armChatStartupHangWatchdog() {
	go func() {
		time.Sleep(90 * time.Second)
		t := activeChatStartupTiming
		if t == nil || t.reached("ready") {
			return
		}
		buf := make([]byte, 2<<20)
		n := runtime.Stack(buf, true)
		aicliDiagf("[aicli-diag] chat startup stalled >90s (not ready); goroutine dump:\n%s", buf[:n])
	}()
}

func (t *chatStartupTiming) flush(opts *chatCommandOptions) {
	if t == nil || len(t.marks) == 0 {
		return
	}
	parts := make([]string, 0, len(t.marks))
	for _, mark := range t.marks {
		if mark.name == "" {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s=+%s (total %s)", mark.name, mark.delta.Round(time.Millisecond), mark.elapsed.Round(time.Millisecond)))
	}
	if len(parts) == 0 {
		return
	}
	summary := "aicli chat startup timing: " + strings.Join(parts, "; ")
	logpkg.Debugf("%s", summary)
	if !t.enabled {
		return
	}
	// Keep JSON/non-interactive stdout clean; timing diagnostics go to stderr.
	if opts != nil && (opts.OutputFormat == "json" || opts.NoInteractive) {
		fmt.Fprintln(os.Stderr, summary)
		return
	}
	fmt.Fprintln(os.Stderr, summary)
}
