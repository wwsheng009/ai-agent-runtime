package commands

import (
	"bufio"
	"io"
	"os"
	"sync/atomic"
	"time"
)

var newChatInputReader = func() *bufio.Reader {
	return newTrackedStdinReader()
}

// chatInputActivity 跟踪「当前是否有读取正阻塞在 stdin 上」。
//
// 启动阶段会合法地阻塞在 ReadString 上等待用户选择（provider / model /
// reasoning_effort / 输出模式 / 会话）。启动挂起 watchdog 必须把这种等待与真正
// 的启动挂起区分开：否则用户只是在菜单前多停留 90s，就会拿到一份误导性的
// “chat startup stalled” goroutine dump（真机已出现的误报）。
var chatInputActivity struct {
	pending     atomic.Int64 // >0 表示有读取正停在 stdin 上
	pendingFrom atomic.Int64 // 本轮等待的起点（unix nanos）
	lastDone    atomic.Int64 // 最近一次读取返回的时间（unix nanos）
}

func beginChatInputWait() {
	if chatInputActivity.pending.Add(1) == 1 {
		chatInputActivity.pendingFrom.Store(time.Now().UnixNano())
	}
}

func endChatInputWait() {
	if chatInputActivity.pending.Add(-1) <= 0 {
		// 没有配对的 begin（调用方成对性被破坏）时把计数夹回 0，避免负值
		// 让后续 begin 仍被判定为“不在等待输入”。
		chatInputActivity.pending.Store(0)
		chatInputActivity.pendingFrom.Store(0)
	}
	chatInputActivity.lastDone.Store(time.Now().UnixNano())
}

// chatInputWaitState 返回是否有读取阻塞在 stdin 上，以及本轮已等待时长。
func chatInputWaitState() (bool, time.Duration) {
	if chatInputActivity.pending.Load() <= 0 {
		return false, 0
	}
	from := chatInputActivity.pendingFrom.Load()
	if from <= 0 {
		return true, 0
	}
	return true, time.Since(time.Unix(0, from))
}

// lastChatInputDone 返回最近一次 stdin 读取返回的时间；零值表示尚未发生过读取。
func lastChatInputDone() time.Time {
	ns := chatInputActivity.lastDone.Load()
	if ns <= 0 {
		return time.Time{}
	}
	return time.Unix(0, ns)
}

// chatStdinKind 描述 stdin 形态：等待输入是「真人在终端前」还是「管道没人喂」，
// 决定这条等待提示该怎么读。
func chatStdinKind() string {
	if os.Stdin == nil {
		return "nil"
	}
	info, err := os.Stdin.Stat()
	if err != nil {
		return "unknown"
	}
	switch {
	case info.Mode()&os.ModeCharDevice != 0:
		return "char-device"
	case info.Mode()&os.ModeNamedPipe != 0:
		return "pipe"
	default:
		return "redirected-file"
	}
}

// trackedStdinReader 在每次 stdin 读取前后打点，让启动 watchdog 能识别
// 「等用户输入」。os.Stdin 在每次 Read 时解析，保持与直接使用 os.Stdin 一致的
// 可替换语义（测试会替换 os.Stdin）。
type trackedStdinReader struct{}

func (trackedStdinReader) Read(p []byte) (int, error) {
	beginChatInputWait()
	defer endChatInputWait()
	if os.Stdin == nil {
		return 0, io.EOF
	}
	return os.Stdin.Read(p)
}

// newTrackedStdinReader 构造带交互等待跟踪的 stdin buffered reader。交互 prompt
// 一律经它建 reader，启动 watchdog 才能区分「等用户输入」与「真挂起」。
func newTrackedStdinReader() *bufio.Reader {
	return bufio.NewReader(trackedStdinReader{})
}

func chatOptionInputReader(opts *chatCommandOptions) *bufio.Reader {
	if opts == nil {
		return newChatInputReader()
	}
	if opts.InputReader == nil {
		opts.InputReader = newChatInputReader()
	}
	return opts.InputReader
}

func chatSessionInputReader(session *ChatSession) *bufio.Reader {
	if session == nil {
		return newChatInputReader()
	}
	if session.InputReader == nil {
		session.InputReader = newChatInputReader()
	}
	return session.InputReader
}
