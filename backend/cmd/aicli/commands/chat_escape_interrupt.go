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
			// 重复 Esc 语义（P2-10 + ESC 失效修复）：
			//   - 中断清理仍在途且未超期：视为“正在停止”，不再叠加新的
			//     中断请求，每个中断周期提示一次“停止处理中”；
			//   - 清理已结束 / 从未启动 / 已超期：不能按会话级 interrupted
			//     标志永久吞键。wake/actor 直驱回合不经过本地主循环的
			//     ResetInterrupt，一次未生效的中断会残留标志，把后续 Esc
			//     全部吞掉（表现为 Esc 无响应）。此时解除超期信号并重新发起
			//     真实中断——interrupt 路径幂等，reserveInterruptCleanup
			//     保证清理 goroutine 不叠加。
			if session.IsInterrupted() && session.isInterruptCleanupInFlight() && !session.interruptCleanupStalled() {
				if session.chatEscapeStoppingNoticeDue() {
					renderChatEscapeStoppingNotice(session)
				}
				continue
			}
			session.detachStalledInterruptCleanup()
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
