package commands

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	logpkg "github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
)

// chatStartupTiming records coarse HandleChat stage durations so startup
// regressions can be diagnosed without an external profiler.
//
// Enable console output with AICLI_STARTUP_TIMING=1. Durations are always
// written to the structured logger at debug level when available.
type chatStartupTiming struct {
	// mu 保护 marks/last：watchdog goroutine 会并发读取启动进度。
	mu      sync.Mutex
	enabled bool
	start   time.Time
	last    time.Time
	marks   []chatStartupMark
}

// activeChatStartupTiming is set for the duration of HandleChat so nested
// helpers can record sub-stage marks without threading the timer everywhere.
// The watchdog goroutine loads it concurrently, hence the atomic pointer.
var activeChatStartupTiming atomic.Pointer[chatStartupTiming]

type chatStartupMark struct {
	name    string
	at      time.Time
	delta   time.Duration
	elapsed time.Duration
}

// 启动挂起 watchdog 的采样参数（定义为变量，便于测试压缩时间尺度）。
var (
	// chatStartupWatchdogPoll 是 watchdog 的采样间隔。
	chatStartupWatchdogPoll = 5 * time.Second
	// chatStartupStallThreshold 是「无启动进展」持续多久判定为真挂起。
	chatStartupStallThreshold = 90 * time.Second
	// chatStartupInputWaitNotice 是「一直停在交互输入」多久发一条提示。
	// 等待用户输入不是挂起，所以这里只发一行可操作提示，不发 goroutine 栈。
	chatStartupInputWaitNotice = 10 * time.Minute
)

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
	if t := activeChatStartupTiming.Load(); t != nil {
		t.mark(name)
	}
}

func (t *chatStartupTiming) mark(name string) {
	if t == nil {
		return
	}
	now := time.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
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
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, m := range t.marks {
		if m.name == name {
			return true
		}
	}
	return false
}

// idleSinceProgress 返回距离最后一次「启动进展」的时长与该进展的阶段名。
// 进展 = 记录 mark，或一次交互式 stdin 读取返回（用户刚敲完选择）。
func (t *chatStartupTiming) idleSinceProgress() (time.Duration, string) {
	if t == nil {
		return 0, ""
	}
	t.mu.Lock()
	last := t.last
	stage := "startup"
	if n := len(t.marks); n > 0 {
		if name := t.marks[n-1].name; name != "" {
			stage = name
		}
	}
	t.mu.Unlock()

	if done := lastChatInputDone(); done.After(last) {
		last = done
	}
	return time.Since(last), stage
}

// armChatStartupHangWatchdog 周期采样启动进度：当「无 mark 进展、也没有在等
// 交互输入」持续超过 chatStartupStallThreshold 时，把全部 goroutine 栈打印到
// stderr——用于定位“aicli chat 启动后长时间无响应”（10 分钟级挂起）。
//
// 关键约束：启动阶段会合法地阻塞在 stdin 上等用户选择（provider / model /
// reasoning_effort / 输出模式 / 会话）。只要那次读取还在等待，就不算挂起；否则
// 用户在菜单前多停留 90s 就会收到误导性的 goroutine dump。等待输入超过
// chatStartupInputWaitNotice 只发一条提示（含 stdin 形态），方便区分“真人在
// 终端前慢慢选”与“管道没人喂”。
func armChatStartupHangWatchdog() {
	_ = watchChatStartupHang()
}

// watchChatStartupHang 启动 watchdog 采样循环，返回的 channel 在循环退出后关闭。
// 循环在「启动已 ready」或「HandleChat 结束（active timing 被清空）」时退出；
// 可等待的退出句柄让测试能在还原包级配置前先确认采样循环已停止。
func watchChatStartupHang() <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		chatStartupWatchdogLoop()
	}()
	return done
}

func chatStartupWatchdogLoop() {
	dumped := false
	noticedInputWait := false
	for {
		time.Sleep(chatStartupWatchdogPoll)

		t := activeChatStartupTiming.Load()
		if t == nil || t.reached("ready") {
			return
		}

		if pending, waited := chatInputWaitState(); pending {
			if !noticedInputWait && waited >= chatStartupInputWaitNotice {
				noticedInputWait = true
				aicliDiagf("[aicli-diag] chat 启动仍在等待交互输入（已 %s，stdin=%s）：这是等待用户输入，不是启动挂起\n",
					waited.Round(time.Second), chatStdinKind())
			}
			continue
		}
		noticedInputWait = false

		if dumped {
			continue
		}
		idle, stage := t.idleSinceProgress()
		if idle < chatStartupStallThreshold {
			continue
		}
		dumped = true
		buf := make([]byte, 2<<20)
		n := runtime.Stack(buf, true)
		aicliDiagf("[aicli-diag] chat startup stalled >%s (last stage %q); goroutine dump:\n%s",
			idle.Round(time.Second), stage, buf[:n])
	}
}

func (t *chatStartupTiming) flush(opts *chatCommandOptions) {
	if t == nil {
		return
	}
	t.mu.Lock()
	marks := append([]chatStartupMark(nil), t.marks...)
	enabled := t.enabled
	t.mu.Unlock()
	if len(marks) == 0 {
		return
	}
	parts := make([]string, 0, len(marks))
	for _, mark := range marks {
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
	if !enabled {
		return
	}
	// Keep JSON/non-interactive stdout clean; timing diagnostics go to stderr.
	if opts != nil && (opts.OutputFormat == "json" || opts.NoInteractive) {
		fmt.Fprintln(os.Stderr, summary)
		return
	}
	fmt.Fprintln(os.Stderr, summary)
}
