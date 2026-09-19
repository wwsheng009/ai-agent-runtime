package commands

import (
	"sync"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

// chatEscapeConsumerState is the session-scoped ESC consumer shared by every
// overlapping turn driver (local sendMessage, actor executor, supervision wake
// turns). Reference counting keeps the KeyHandler armed and the goroutine alive
// for the union of the consumers, so an actor-only turn is interruptible from
// the TUI even when no local sendMessage frame is active, and two overlapping
// consumers no longer disarm each other (plan doc stage C, P1-3).
type chatEscapeConsumerState struct {
	mu            sync.Mutex
	refs          int
	done          chan struct{}
	stopped       chan struct{}
	escCh         <-chan bool
	shadowRelease func()
}

// startChatEscapeInterruptWatcher registers one turn-scoped ESC consumer and
// returns its release function. For a single caller the observable behavior is
// unchanged; concurrent callers now share one consumer instead of racing on
// KeyHandler.Arm/Disarm.
func startChatEscapeInterruptWatcher(session *ChatSession) func() {
	return beginChatEscapeInterruptConsumer(session)
}

func beginChatEscapeInterruptConsumer(session *ChatSession) func() {
	if session == nil || session.NoInteractive || session.JSONOutput || session.KeyHandler == nil || !session.KeyHandler.IsEnabled() {
		return func() {}
	}

	session.escapeConsumerMu.Lock()
	state := session.escapeConsumer
	if state == nil {
		state = &chatEscapeConsumerState{}
		session.escapeConsumer = state
	}
	session.escapeConsumerMu.Unlock()

	state.mu.Lock()
	if state.refs == 0 {
		escCh := session.KeyHandler.GetESCChannel()
		drainChatEscapeEvents(escCh)
		// 阶段 F P2b：灰度开关打开时由仲裁器（syncChatInputArbitration）独占
		// 驱动 KeyHandler；关闭时保留散点调用（旧路径保留一个版本）。
		if !chatInputArbitrationEnforced() {
			session.KeyHandler.Arm()
		}
		state.escCh = escCh
		state.done = make(chan struct{})
		state.stopped = make(chan struct{})
		// 阶段 F P0：影子仲裁登记（只观测，不改变 Arm/Disarm 行为）。
		state.shadowRelease = beginChatInputShadowLevel(session, chatInputOwnerESC)
		go runChatEscapeInterruptConsumer(session, escCh, state.done, state.stopped)
	}
	state.refs++
	state.mu.Unlock()

	var releaseOnce sync.Once
	return func() {
		releaseOnce.Do(func() {
			releaseChatEscapeInterruptConsumer(session, state)
		})
	}
}

func releaseChatEscapeInterruptConsumer(session *ChatSession, state *chatEscapeConsumerState) {
	if session == nil || state == nil {
		return
	}
	state.mu.Lock()
	if state.refs > 0 {
		state.refs--
	}
	if state.refs > 0 {
		state.mu.Unlock()
		return
	}
	done := state.done
	stopped := state.stopped
	escCh := state.escCh
	shadowRelease := state.shadowRelease
	state.done, state.stopped, state.escCh, state.shadowRelease = nil, nil, nil, nil
	if done != nil {
		if !chatInputArbitrationEnforced() {
			session.KeyHandler.Disarm()
		}
		close(done)
	}
	state.mu.Unlock()

	if shadowRelease != nil {
		// 单写者模式下这里经 syncChatInputArbitration 完成 Disarm。
		shadowRelease()
	}
	if stopped != nil {
		<-stopped
		drainChatEscapeEvents(escCh)
	}
}

func runChatEscapeInterruptConsumer(session *ChatSession, escCh <-chan bool, done <-chan struct{}, stopped chan<- struct{}) {
	defer close(stopped)
	for {
		select {
		case <-escCh:
			// A second Esc while Stopping has no new interrupt target.
			// Ignore it instead of opening backtrack or re-rendering.
			if session.IsInterrupted() {
				// P2-10：重复 Esc 不再完全静默——每个中断周期提示一次
				// “停止处理中”，让用户知道按键已被接收且不会重复触发。
				if session.chatEscapeStoppingNoticeDue() {
					renderChatEscapeStoppingNotice(session)
				}
				continue
			}
			session.InterruptPreservePendingInput()
			renderChatEscapeInterruptNotice(session)
		case <-done:
			return
		}
	}
}

// chatEscapeStoppingNoticeDue reports whether the "stop in progress" note for a
// repeated Esc should be rendered. It returns true at most once per interrupt
// cycle (reset when a new interrupt starts or via ResetInterrupt), so repeated
// Esc gives honest feedback without spamming (plan doc P2-10).
func (s *ChatSession) chatEscapeStoppingNoticeDue() bool {
	if s == nil {
		return false
	}
	return s.escapeStoppingNoticeShown.CompareAndSwap(false, true)
}

func renderChatEscapeStoppingNotice(session *ChatSession) {
	if session == nil || session.NoInteractive || session.JSONOutput {
		return
	}
	const notice = "停止处理中 - 正在取消运行并释放资源"
	if session.Interaction != nil {
		session.Interaction.RenderLocalSupplement(notice)
		return
	}
	printDirectInteractiveOutput(session, ui.NewStatus(ui.StatusInfo, notice).Build()+"\n")
}

func drainChatEscapeEvents(ch <-chan bool) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

func renderChatEscapeInterruptNotice(session *ChatSession) {
	if session == nil || session.NoInteractive || session.JSONOutput {
		return
	}
	if session.Interaction != nil {
		session.Interaction.RenderLocalSupplement("已中断 - ESC 取消当前操作")
		return
	}
	printDirectInteractiveOutput(session, ui.NewStatus(ui.StatusInfo, "已中断 - ESC 取消当前操作").Build()+"\n")
}
