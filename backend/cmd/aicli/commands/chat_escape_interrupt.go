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
			handleChatEscapeInterruptRequest(session)
		case <-done:
			return
		}
	}
}

// chatInterruptDecisionResult is the machine-readable outcome of one interrupt
// request. It lets every plane (TUI Esc, Web/HTTP interrupt, /stop) share the
// same decision while keeping its own response contract (audit R6).
type chatInterruptDecisionResult string

const (
	// chatInterruptStarted: this request really started an interrupt (a
	// stalled cleanup detached and retried counts as started).
	chatInterruptStarted chatInterruptDecisionResult = "started"
	// chatInterruptAlreadyStopping: the previous interrupt's cleanup is still
	// in flight (and not stalled). The request collapses into the stopping
	// window instead of stacking another cancellation.
	chatInterruptAlreadyStopping chatInterruptDecisionResult = "already_stopping"
	// chatInterruptNoSession: nil session.
	chatInterruptNoSession chatInterruptDecisionResult = "no_session"
)

// chatInterruptDecision is the single session-level interrupt decision shared by
// every plane: TUI Esc (handleChatEscapeInterruptRequest), the Web/HTTP
// interrupt endpoint and the /stop command. preserve matches
// InterruptPreservePendingInput (true) vs Interrupt (false).
//
// The old inline Esc branch could swallow a keypress in two situations:
//   - a stale `interrupted` flag with no cleanup in flight (wake/actor turns do
//     not pass through the local loop's ResetInterrupt); the retry fix covers
//     that case;
//   - a failed interrupt whose cleanup had already completed but whose
//     `interrupted` flag was still set, while the UI had already returned to
//     Ready. The next request was treated as a repeat and silently dropped.
//
// A completed cleanup means the previous attempt is over: a new request must
// always start a fresh interrupt attempt. Only an actually in-flight cleanup
// (and not stalled) may collapse the request into the "stopping" window.
func chatInterruptDecision(session *ChatSession, preserve bool) chatInterruptDecisionResult {
	if session == nil {
		return chatInterruptNoSession
	}
	if session.IsInterrupted() && session.isInterruptCleanupInFlight() && !session.interruptCleanupStalled() {
		return chatInterruptAlreadyStopping
	}
	session.detachStalledInterruptCleanup()
	if preserve {
		session.InterruptPreservePendingInput()
	} else {
		session.Interrupt()
	}
	return chatInterruptStarted
}

// handleChatEscapeInterruptRequest is the TUI wrapper around the shared
// decision: it renders the Esc-specific feedback (fresh-interrupt notice or the
// rate-limited "stopping" note) that keypresses need.
func handleChatEscapeInterruptRequest(session *ChatSession) {
	switch chatInterruptDecision(session, true) {
	case chatInterruptAlreadyStopping:
		if session.chatEscapeStoppingNoticeDue() {
			renderChatEscapeStoppingNotice(session)
		}
	case chatInterruptNoSession:
		return
	default:
		renderChatEscapeInterruptNotice(session)
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

// chatEscapeInterruptConsumerActive reports whether a turn-scoped ESC consumer
// currently exists for the session.
//
// It is the precise capability query for the composer's own ESC routing: during
// actor / goal-continuation / supervision-wake turns no busy capture owns stdin,
// so the main-loop composer is the live reader of the same console input the
// KeyHandler polls. The composer must therefore hand its ESC to the interrupt
// decision itself (chat_composer.go onCancel); the KeyHandler peek can lose that
// race and the status line's "esc to interrupt" promise would be dead
// (2026-10-18: retry/wake turns were uninterruptible from the TUI for this
// reason).
func chatEscapeInterruptConsumerActive(session *ChatSession) bool {
	if session == nil {
		return false
	}
	session.escapeConsumerMu.Lock()
	state := session.escapeConsumer
	session.escapeConsumerMu.Unlock()
	if state == nil {
		return false
	}
	state.mu.Lock()
	active := state.refs > 0
	state.mu.Unlock()
	return active
}

// chatEscapeSignalOwnedByKeyHandler reports whether the KeyHandler will consume
// a Unix SIGUSR2 (armed and not suspended). The legacy SIGUSR2 bridge must stand
// down in that case so one signal is not consumed twice; all other states
// (handler absent/disabled, disarmed, suspended during a busy capture) keep the
// signal bridge as the ESC entry (chat_unix.go).
func chatEscapeSignalOwnedByKeyHandler(session *ChatSession) bool {
	if session == nil {
		return false
	}
	kh := session.KeyHandler
	return kh != nil && kh.IsEnabled() && kh.Armed() && !kh.Suspended()
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
