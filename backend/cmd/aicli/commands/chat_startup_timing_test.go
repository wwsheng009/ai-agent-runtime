package commands

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestChatStartupTiming_MarkOrderAndSummary(t *testing.T) {
	timing := &chatStartupTiming{
		enabled: true,
		start:   time.Now().Add(-50 * time.Millisecond),
		last:    time.Now().Add(-50 * time.Millisecond),
	}
	timing.mark("begin")
	time.Sleep(5 * time.Millisecond)
	timing.mark("ready")
	if len(timing.marks) != 2 {
		t.Fatalf("expected 2 marks, got %d", len(timing.marks))
	}
	if timing.marks[0].name != "begin" || timing.marks[1].name != "ready" {
		t.Fatalf("unexpected mark names: %#v", timing.marks)
	}
	if timing.marks[1].elapsed < timing.marks[0].elapsed {
		t.Fatalf("elapsed should be monotonic: %#v", timing.marks)
	}

	parts := make([]string, 0, len(timing.marks))
	for _, mark := range timing.marks {
		parts = append(parts, mark.name)
	}
	if got := strings.Join(parts, ","); got != "begin,ready" {
		t.Fatalf("unexpected mark sequence %q", got)
	}
}

func TestChatStartupTimingEnabled(t *testing.T) {
	t.Setenv("AICLI_STARTUP_TIMING", "1")
	if !chatStartupTimingEnabled() {
		t.Fatal("expected AICLI_STARTUP_TIMING=1 to enable timing")
	}
	t.Setenv("AICLI_STARTUP_TIMING", "0")
	if chatStartupTimingEnabled() {
		t.Fatal("expected AICLI_STARTUP_TIMING=0 to disable timing")
	}
}

// TestChatStartupTiming_InputActivityCountsAsProgress 固定「输入返回也是进展」：
// 用户刚敲完选择时，idle 必须从输入返回那刻重新计时，否则等待输入结束后会立刻
// 被误判成挂起。
func TestChatStartupTiming_InputActivityCountsAsProgress(t *testing.T) {
	timing := &chatStartupTiming{start: time.Now(), last: time.Now()}
	timing.mark("runtime_state")

	time.Sleep(30 * time.Millisecond)
	idle, stage := timing.idleSinceProgress()
	if stage != "runtime_state" {
		t.Fatalf("expected last stage runtime_state, got %q", stage)
	}
	if idle < 20*time.Millisecond {
		t.Fatalf("expected idle time to include the sleep, got %s", idle)
	}

	beginChatInputWait()
	endChatInputWait()
	if idleAfterInput, _ := timing.idleSinceProgress(); idleAfterInput >= 20*time.Millisecond {
		t.Fatalf("expected a completed stdin read to reset startup idle time, got %s", idleAfterInput)
	}
}

// TestChatInputActivity_TracksBlockingRead 覆盖 stdin 读取跟踪本身：读取阻塞
// 期间必须可观测为「正在等待输入」，读完立刻回到非等待并留下活动时间。
func TestChatInputActivity_TracksBlockingRead(t *testing.T) {
	originalStdin := os.Stdin
	restoreActivity := resetChatInputActivityForTest()
	pipeReader, pipeWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	t.Cleanup(func() {
		os.Stdin = originalStdin
		restoreActivity()
		_ = pipeReader.Close()
		_ = pipeWriter.Close()
	})
	os.Stdin = pipeReader

	readDone := make(chan error, 1)
	go func() {
		// 走生产用的 reader 工厂（newChatInputReader），确认它确实带等待跟踪。
		line, readErr := newChatInputReader().ReadString('\n')
		if readErr != nil {
			readDone <- readErr
			return
		}
		if strings.TrimSpace(line) != "high" {
			readDone <- fmt.Errorf("unexpected line %q", line)
			return
		}
		readDone <- nil
	}()

	waitForChatInputWaitState(t, true)
	waitForPositiveChatInputWait(t)

	if _, err := pipeWriter.WriteString("high\n"); err != nil {
		t.Fatalf("write to stdin pipe: %v", err)
	}
	select {
	case readErr := <-readDone:
		if readErr != nil {
			t.Fatalf("stdin read: %v", readErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stdin read did not return after data was written")
	}

	waitForChatInputWaitState(t, false)
	if lastChatInputDone().IsZero() {
		t.Fatal("expected the completed stdin read to be recorded")
	}
}

// TestChatStartupWatchdog_DoesNotDumpWhileAwaitingInteractiveInput 是这次误报
// 的回归围栏：启动期 prompt 阻塞在 stdin 上（用户还在选）时，watchdog 只能发
// 一行「等待交互输入」提示，绝不能把 goroutine 栈当成启动挂起打出来。
func TestChatStartupWatchdog_DoesNotDumpWhileAwaitingInteractiveInput(t *testing.T) {
	tuneChatStartupWatchdogForTest(t, 5*time.Millisecond, 10*time.Millisecond, 15*time.Millisecond)
	capture := captureAICLIDiagForTest(t)
	installActiveChatStartupTimingForTest(t, &chatStartupTiming{
		start: time.Now().Add(-time.Hour),
		last:  time.Now().Add(-time.Hour),
	})

	armChatStartupWatchdogForTest(t)
	simulateChatInputWaitForTest(t)

	waitForDiagText(t, capture, "等待交互输入", 3*time.Second)
	// 远远超过 stall 阈值：只要还在等输入，就不允许出现「挂起」判定。
	time.Sleep(20 * chatStartupWatchdogPoll)
	if text := capture.text(); strings.Contains(text, "stalled") || strings.Contains(text, "goroutine dump") {
		t.Fatalf("watchdog reported a startup stall while an interactive read was pending:\n%s", text)
	}
}

// TestChatStartupWatchdog_DumpsWhenStartupStallsAfterInputReturned 保证误报修复
// 不会让 watchdog 失效：用户输入返回后启动真的卡住（不再有 mark），仍然要 dump
// 栈，并且带上最后一次进展的阶段名。
func TestChatStartupWatchdog_DumpsWhenStartupStallsAfterInputReturned(t *testing.T) {
	tuneChatStartupWatchdogForTest(t, 5*time.Millisecond, 20*time.Millisecond, time.Hour)
	capture := captureAICLIDiagForTest(t)
	timing := &chatStartupTiming{
		start: time.Now().Add(-time.Hour),
		last:  time.Now().Add(-time.Hour),
	}
	installActiveChatStartupTimingForTest(t, timing)
	timing.mark("runtime_state")

	armChatStartupWatchdogForTest(t)
	releaseInputWait := simulateChatInputWaitForTest(t)
	time.Sleep(10 * chatStartupWatchdogPoll)
	if text := capture.text(); strings.Contains(text, "stalled") {
		t.Fatalf("watchdog dumped before the user input returned:\n%s", text)
	}

	// 用户敲完（读取返回）后启动卡死：不再有任何 mark 进展。
	releaseInputWait()
	waitForDiagText(t, capture, "stalled", 5*time.Second)

	text := capture.text()
	if !strings.Contains(text, "goroutine dump") {
		t.Fatalf("expected a goroutine dump in the stall diagnostic:\n%s", text)
	}
	if !strings.Contains(text, `last stage "runtime_state"`) {
		t.Fatalf("expected the diagnostic to name the last startup stage:\n%s", text)
	}
	if got := strings.Count(text, "chat startup stalled"); got != 1 {
		t.Fatalf("expected exactly one stall dump, got %d:\n%s", got, text)
	}
}

// TestChatStartupWatchdog_StopsOnceReady 覆盖正常启动：一旦记录 ready，
// watchdog 立刻退出，不该再产生任何诊断。
func TestChatStartupWatchdog_StopsOnceReady(t *testing.T) {
	tuneChatStartupWatchdogForTest(t, 5*time.Millisecond, 5*time.Millisecond, 5*time.Millisecond)
	capture := captureAICLIDiagForTest(t)
	timing := &chatStartupTiming{start: time.Now().Add(-time.Hour), last: time.Now().Add(-time.Hour)}
	installActiveChatStartupTimingForTest(t, timing)

	timing.mark("begin")
	timing.mark("ready")
	armChatStartupWatchdogForTest(t)

	time.Sleep(20 * chatStartupWatchdogPoll)
	if text := capture.text(); text != "" {
		t.Fatalf("expected no diagnostics after ready, got:\n%s", text)
	}
}

func tuneChatStartupWatchdogForTest(t *testing.T, poll, stall, inputWaitNotice time.Duration) {
	t.Helper()
	oldPoll, oldStall, oldNotice := chatStartupWatchdogPoll, chatStartupStallThreshold, chatStartupInputWaitNotice
	chatStartupWatchdogPoll, chatStartupStallThreshold, chatStartupInputWaitNotice = poll, stall, inputWaitNotice
	t.Cleanup(func() {
		chatStartupWatchdogPoll, chatStartupStallThreshold, chatStartupInputWaitNotice = oldPoll, oldStall, oldNotice
	})
}

// simulateChatInputWaitForTest 模拟「启动 prompt 正阻塞在 stdin 上」，返回结束
// 这次等待的函数（幂等：测试体内可提前调用，收尾时也会自动调用）。
func simulateChatInputWaitForTest(t *testing.T) func() {
	t.Helper()
	beginChatInputWait()
	release := sync.OnceFunc(endChatInputWait)
	t.Cleanup(release)
	return release
}

// armChatStartupWatchdogForTest 启动 watchdog，并登记「先让采样循环退出、再还原
// 包级配置」的清理动作。t.Cleanup 后进先出，所以本清理会先于 tune/install 类
// 清理执行——否则测试写包级变量会与仍在采样的 goroutine 形成数据竞争。
func armChatStartupWatchdogForTest(t *testing.T) {
	t.Helper()
	done := watchChatStartupHang()
	t.Cleanup(func() {
		activeChatStartupTiming.Store(nil)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Errorf("startup watchdog did not exit after the active timing was cleared")
		}
	})
}

// installActiveChatStartupTimingForTest 暴露启动期全局计时器，并在测试结束时
// 复位（watchdog goroutine 观察到 nil 后会自行退出）。
func installActiveChatStartupTimingForTest(t *testing.T, timing *chatStartupTiming) {
	t.Helper()
	previous := activeChatStartupTiming.Load()
	activeChatStartupTiming.Store(timing)
	t.Cleanup(func() {
		activeChatStartupTiming.Store(previous)
	})
}

type chatDiagCapture struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (c *chatDiagCapture) format(format string, args ...any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	fmt.Fprintf(&c.buf, format, args...)
}

func (c *chatDiagCapture) text() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}

func captureAICLIDiagForTest(t *testing.T) *chatDiagCapture {
	t.Helper()
	capture := &chatDiagCapture{}
	previous := aicliDiagf
	aicliDiagf = capture.format
	t.Cleanup(func() {
		aicliDiagf = previous
	})
	return capture
}

func waitForDiagText(t *testing.T, capture *chatDiagCapture, needle string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if strings.Contains(capture.text(), needle) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("diagnostic %q did not appear within %s; got:\n%s", needle, timeout, capture.text())
}

func waitForChatInputWaitState(t *testing.T, want bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if pending, _ := chatInputWaitState(); pending == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("stdin wait state did not become %v within 5s", want)
}

// waitForPositiveChatInputWait 等待「正在等待输入」且等待时长已开始累计。
// 等待计数与等待起点是两个独立的原子量，观测者可能在两者发布之间读到
// pending>0 而时长为 0（全包高负载运行下已复现为 flaky）。把「已记录等待时长」
// 作为观测目标即可跨过这个发布间隙，不必依赖写入顺序。
func waitForPositiveChatInputWait(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if pending, waited := chatInputWaitState(); pending && waited > 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("stdin wait state did not report a positive wait duration within 5s")
}

// resetChatInputActivityForTest 清空共享 stdin 活动计数，并返回复位函数。
func resetChatInputActivityForTest() func() {
	pending := chatInputActivity.pending.Load()
	pendingFrom := chatInputActivity.pendingFrom.Load()
	lastDone := chatInputActivity.lastDone.Load()
	chatInputActivity.pending.Store(0)
	chatInputActivity.pendingFrom.Store(0)
	chatInputActivity.lastDone.Store(0)
	return func() {
		chatInputActivity.pending.Store(pending)
		chatInputActivity.pendingFrom.Store(pendingFrom)
		chatInputActivity.lastDone.Store(lastDone)
	}
}
