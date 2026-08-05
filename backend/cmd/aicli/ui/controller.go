package ui

import (
	"runtime"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/renderengine"
)

// UIControllerConfig 控制 UI actor 的 mailbox 容量。
// MailboxSize <= 0 时使用默认值 256。
type UIControllerConfig struct {
	MailboxSize int
}

// EffectConsumer receives effects after the reducer has published the
// corresponding AppState revision. Consumers must perform terminal work
// outside the reducer goroutine and post typed results back through UIAction.
// A nil consumer deliberately disables delivery, which is useful for plain
// and test-only controllers.
type EffectConsumer func(Effect)

func (c UIControllerConfig) mailboxCap() int {
	if c.MailboxSize <= 0 {
		return 256
	}
	return c.MailboxSize
}

// Reducer 是 AppState 的唯一写者契约（IR-1）。Phase 1 的 reducer 是
// legacy adapter：调用现有 surface/coordinator 路径生成相同输出，
// 保证行为不变（实施指南 Phase 1 任务 5）。
//
// Apply 返回的 []Effect 除 PostActionEffect 外由 controller 按序投递给
// effect sink；PostActionEffect 会作为当前 action 的 causal child 重新入队。
// revision 是 action 应用前的当前 revision（0 起始，每次应用 +1）。
type Reducer interface {
	Apply(revision uint64, action UIAction) []Effect
}

// ContextualReducer is the reducer form for code that needs to emit a causal
// action while applying the current action. ReducerContext is valid only for
// this synchronous ApplyWithContext call and cannot be used by an external
// producer to bypass mailbox FIFO.
//
// Reducer is deliberately kept as the base interface so Phase 1 adapters and
// existing callers can migrate incrementally.
type ContextualReducer interface {
	Reducer
	ApplyWithContext(revision uint64, action UIAction, context *ReducerContext) []Effect
}

// ReducerContext carries the controller-issued capability for the action that
// is currently being reduced. It is intentionally not constructible outside
// this package: PostFollowup validates both the active transaction and the
// reducer goroutine before accepting an action.
type ReducerContext struct {
	controller *UIController
	token      *reducerTransaction
}

// PostFollowup appends an action after the current reducer action and before
// the next external mailbox item. It is non-blocking and is the only API that
// should be used by newly migrated reducer code for this purpose.
func (c *ReducerContext) PostFollowup(action UIAction) bool {
	if c == nil || c.controller == nil || c.token == nil {
		return false
	}
	return c.controller.postFollowupWithToken(c.token, action)
}

// ReducerFunc 是 Reducer 的函数形态。
type ReducerFunc func(revision uint64, action UIAction) []Effect

// Apply 实现 Reducer。
func (f ReducerFunc) Apply(revision uint64, action UIAction) []Effect {
	return f(revision, action)
}

// ContextualReducerFunc is ContextualReducer's function form.
type ContextualReducerFunc func(revision uint64, action UIAction, context *ReducerContext) []Effect

// Apply keeps ContextualReducerFunc usable anywhere a legacy Reducer is
// expected. UIController detects its contextual form and calls
// ApplyWithContext instead.
func (f ContextualReducerFunc) Apply(revision uint64, action UIAction) []Effect {
	return f(revision, action, nil)
}

// ApplyWithContext implements ContextualReducer.
func (f ContextualReducerFunc) ApplyWithContext(revision uint64, action UIAction, context *ReducerContext) []Effect {
	return f(revision, action, context)
}

// Effect 是 reducer 输出到 effect sink 的结果单元。
// Phase 1 只定义 flush 与 post-action 两类；Phase 3/4 增加
// terminal effect（Ack/Failed）与 frame plan。
type Effect interface {
	isEffect()
}

// FlushEffect 请求一帧物理输出（Phase 1：legacy adapter 的 flush 入口；
// 后续由 Presenter/TerminalSession 消费）。
type FlushEffect struct {
	Dirty renderengine.DirtyFlags
}

func (FlushEffect) isEffect() {}

// PostActionEffect requests a controller-owned causal follow-up. It is
// consumed by UIController after the parent reducer returns, not delivered to
// the external effect sink. This gives new reducers an effect-based migration
// path when threading ReducerContext through a legacy call chain is awkward.
type PostActionEffect struct {
	Action UIAction
}

func (PostActionEffect) isEffect() {}

// HistoryCommitWakeEffect asks the primary terminal-effect consumer to inspect
// the newest actor snapshot. It intentionally has no text payload: handing a
// stale []Line to a worker would let resize or a lease race the reducer. The
// consumer must select and claim a token from UIController.State instead.
type HistoryCommitWakeEffect struct{}

func (HistoryCommitWakeEffect) isEffect() {}

// ControllerStats 是 UI actor 的诊断快照（供 /debug 与测试）。
type ControllerStats struct {
	Posted        uint64
	Processed     uint64
	Dropped       uint64
	Pending       int
	Revision      uint64
	LastAction    string
	ReducerPanics uint64
	Closed        bool
}

// UIController 是 UI actor：bounded mailbox + 单一 Run 循环。
//
// 投递语义（实施指南 Phase 1 任务 1）：
//   - durable：FIFO 追加，mailbox 满时 Post 背压阻塞（不丢内容）；
//   - coalescable：同 key 待处理时合并/替换，不占用新槽位（不会因
//     意图洪泛阻塞生产者）；
//   - barrier：FIFO 追加（在其之前入队的 action 之后执行），不参与合并。
//
// 所有 action 由 Run 循环单 goroutine 消费并按序调用 reducer ——
// 这是每个 frame 单一 action/revision 顺序的来源。
type UIController struct {
	mu    sync.Mutex
	cond  *sync.Cond
	queue []UIAction
	// followups holds actions causally emitted while the reducer applies the
	// current action. They are consumed before the next external mailbox item,
	// but do not consume external mailbox capacity: accepting a facade mutation
	// from an already accepted reducer action must never wait for that same
	// reducer to resume consumption.
	followups []UIAction
	coalesce  map[string]int // coalescable key -> queue 内下标
	closed    bool
	cap       int
	reducer   Reducer
	onEffect  func(Effect)

	posted        uint64
	processed     uint64
	dropped       uint64
	revision      uint64
	reducerPanics uint64
	inFlight      bool
	// activeTransaction is the capability currently issued to Run's reducer
	// invocation. legacyReducerGID supports only the pre-context facade
	// adapter during migration; unlike the former inFlight-only check it
	// cannot be claimed by a different producer goroutine.
	activeTransaction *reducerTransaction
	legacyReducerGID  uint64
	delivering        bool
	lastAction        string
	state             UIControllerState
}

type reducerTransaction struct {
	goroutineID uint64
}

// NewUIController 创建 UI actor。reducer 为 nil 时 action 只记账不应用
// （测试/诊断用）；onEffect 为 nil 时 effect 被丢弃。
func NewUIController(cfg UIControllerConfig, reducer Reducer, onEffect func(Effect)) *UIController {
	c := &UIController{
		coalesce: make(map[string]int),
		cap:      cfg.mailboxCap(),
		reducer:  reducer,
		onEffect: onEffect,
	}
	c.cond = sync.NewCond(&c.mu)
	return c
}

// SetEffectConsumer installs or replaces the physical-effect consumer after
// controller construction. Chat setup creates the actor lazily, while the
// terminal owner may only become available after the session has negotiated
// its writer and geometry source. The assignment is synchronized with
// delivery; an in-flight callback keeps the consumer snapshot it already
// acquired, and future effects use the new consumer.
func (c *UIController) SetEffectConsumer(consumer EffectConsumer) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return false
	}
	c.onEffect = consumer
	return true
}

// Post 投递一个 action。closed 后返回 false。durable/barrier 在 mailbox
// 满时阻塞等待消费者；coalescable 同 key 待处理时直接合并返回。
// 可在任意 goroutine 调用。
func (c *UIController) Post(action UIAction) bool {
	if c == nil || action == nil {
		return false
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return false
	}
	if action.Class() == ClassCoalescable {
		if key := action.CoalesceKey(); key != "" {
			if idx, ok := c.coalesce[key]; ok && idx < len(c.queue) {
				c.queue[idx] = mergeActions(c.queue[idx], action)
				c.posted++
				c.dropped++
				c.mu.Unlock()
				return true
			}
		}
	}
	for len(c.queue) >= c.cap {
		if c.closed {
			c.mu.Unlock()
			return false
		}
		c.cond.Wait()
	}
	if c.closed {
		c.mu.Unlock()
		return false
	}
	c.queue = append(c.queue, action)
	if action.Class() == ClassCoalescable {
		if key := action.CoalesceKey(); key != "" {
			c.coalesce[key] = len(c.queue) - 1
		}
	}
	c.posted++
	c.mu.Unlock()
	c.cond.Broadcast()
	return true
}

// TryPost 是 Post 的非阻塞形态：mailbox 满且不可合并时立即返回 false
// （不丢弃任何已接受 action；调用方可选择稍后重试）。closed 时返回 false。
func (c *UIController) TryPost(action UIAction) bool {
	if c == nil || action == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return false
	}
	if action.Class() == ClassCoalescable {
		if key := action.CoalesceKey(); key != "" {
			if idx, ok := c.coalesce[key]; ok && idx < len(c.queue) {
				c.queue[idx] = mergeActions(c.queue[idx], action)
				c.posted++
				c.dropped++
				return true
			}
		}
	}
	if len(c.queue) >= c.cap {
		return false
	}
	c.queue = append(c.queue, action)
	if action.Class() == ClassCoalescable {
		if key := action.CoalesceKey(); key != "" {
			c.coalesce[key] = len(c.queue) - 1
		}
	}
	c.posted++
	c.cond.Broadcast()
	return true
}

// PostFollowup accepts an action emitted synchronously by the current reducer.
// New reducer code must use ReducerContext.PostFollowup instead. This legacy
// adapter remains for deep facade call chains that have not yet received an
// explicit context; it verifies the controller's reducer goroutine rather
// than treating the process-wide inFlight flag as proof of causality.
//
// Follow-ups are durable, ordered after the current action and before the next
// external mailbox action. They receive their own reducer revision. Calls from
// any other goroutine, including an external producer that races an in-flight
// reducer, receive false and must use Post instead.
//
// Close deliberately does not reject an in-flight action's follow-up. Close
// drains the causal consequences of accepted work before Run exits.
func (c *UIController) PostFollowup(action UIAction) bool {
	if c == nil || action == nil {
		return false
	}
	return c.postFollowupFromLegacyReducer(action)
}

func (c *UIController) postFollowupFromLegacyReducer(action UIAction) bool {
	if c == nil || action == nil {
		return false
	}
	callerGID := currentGoroutineID()
	if callerGID == 0 {
		return false
	}
	c.mu.Lock()
	if !c.inFlight || c.activeTransaction == nil || c.legacyReducerGID != callerGID {
		c.mu.Unlock()
		return false
	}
	c.followups = append(c.followups, action)
	c.posted++
	c.mu.Unlock()
	c.cond.Broadcast()
	return true
}

func (c *UIController) postFollowupWithToken(token *reducerTransaction, action UIAction) bool {
	if c == nil || token == nil || action == nil {
		return false
	}
	callerGID := currentGoroutineID()
	if callerGID == 0 || callerGID != token.goroutineID {
		return false
	}
	c.mu.Lock()
	if !c.inFlight || c.activeTransaction != token {
		c.mu.Unlock()
		return false
	}
	c.followups = append(c.followups, action)
	c.posted++
	c.mu.Unlock()
	c.cond.Broadcast()
	return true
}

// Run 消费 mailbox 并调用 reducer。阻塞直到 Close 且队列排空。
// 必须在单一 goroutine 中运行。reducer panic 会被捕获：该 action 视为
// 已消费（revision 照常推进），panic 计数入 Stats，循环继续。
func (c *UIController) Run() {
	if c == nil {
		return
	}
	for {
		c.mu.Lock()
		for len(c.queue) == 0 && len(c.followups) == 0 && !c.closed {
			c.cond.Wait()
		}
		if len(c.queue) == 0 && len(c.followups) == 0 {
			// closed 且队列已空：排空完成。
			c.mu.Unlock()
			return
		}
		var action UIAction
		if len(c.followups) > 0 {
			action = c.followups[0]
			c.followups = c.followups[1:]
		} else {
			action = c.queue[0]
			c.queue = c.queue[1:]
			c.reindexCoalesceLocked()
		}
		c.inFlight = true
		transaction := &reducerTransaction{
			goroutineID: currentGoroutineID(),
		}
		c.activeTransaction = transaction
		c.legacyReducerGID = transaction.goroutineID
		rev := c.revision
		// Follow-ups already queued here belong to earlier causal siblings. A
		// panicking reducer may only discard actions it emitted after this point.
		followupStart := len(c.followups)
		c.mu.Unlock()

		effects, panicked := c.apply(action, rev, &ReducerContext{controller: c, token: transaction})

		c.mu.Lock()
		c.inFlight = false
		c.activeTransaction = nil
		c.legacyReducerGID = 0
		c.processed++
		c.revision++
		if !panicked {
			effects = c.collectPostActionEffectsLocked(effects)
			c.state = reduceUIControllerState(c.state, action, c.revision)
			if historyCommitWakeNeeded(action, c.state) {
				effects = append(effects, HistoryCommitWakeEffect{})
			}
		} else if len(c.followups) > followupStart {
			// A reducer panic aborts the current action's causal continuation.
			// Letting those follow-ups run would publish a partial transaction
			// (for example a facade paint without its semantic parent action).
			c.followups = c.followups[:followupStart]
		}
		c.lastAction = actionClassString(action)
		c.delivering = true
		c.mu.Unlock()
		c.cond.Broadcast()
		c.deliver(effects)
		c.mu.Lock()
		c.delivering = false
		c.mu.Unlock()
		c.cond.Broadcast()
	}
}

// collectPostActionEffectsLocked transfers reducer-declared causal children
// into the same follow-up lane as ReducerContext.PostFollowup. The caller
// holds c.mu after a successful reducer return, so the children remain after
// direct follow-ups emitted by that reducer and before the next external item.
func (c *UIController) collectPostActionEffectsLocked(effects []Effect) []Effect {
	if len(effects) == 0 {
		return nil
	}
	deliver := make([]Effect, 0, len(effects))
	for _, effect := range effects {
		post, ok := effect.(PostActionEffect)
		if !ok {
			deliver = append(deliver, effect)
			continue
		}
		if post.Action == nil {
			continue
		}
		c.followups = append(c.followups, post.Action)
		c.posted++
	}
	return deliver
}

// historyCommitWakeNeeded is evaluated only after the controller has published
// the action's new AppState. It keeps HistoryCommit scheduling on the reducer
// side while leaving physical terminal ownership to the injected presenter.
func historyCommitWakeNeeded(action UIAction, state UIControllerState) bool {
	if !state.HistoryEffects.HasPending() {
		return false
	}
	switch action.(type) {
	case ReplaceTranscriptAction, SetActiveCellAction, UpdateActiveCellAction,
		SetSemanticActiveCellProjectionAction,
		FinalizeActiveCellAction, Resize,
		LeaseReleased, HistoryProjectionRecovered, HistoryScrollbackReconciled,
		HistoryCommitAcknowledged, HistoryCommitsAcknowledged:
		return true
	default:
		return false
	}
}

// apply 调用 reducer 并捕获 panic。
func (c *UIController) apply(action UIAction, rev uint64, context *ReducerContext) (effects []Effect, panicked bool) {
	if c.reducer == nil {
		return nil, false
	}
	defer func() {
		if r := recover(); r != nil {
			c.mu.Lock()
			c.reducerPanics++
			c.mu.Unlock()
			effects = nil
			panicked = true
		}
	}()
	if reducer, ok := c.reducer.(ContextualReducer); ok {
		return reducer.ApplyWithContext(rev, action, context), false
	}
	return c.reducer.Apply(rev, action), false
}

// currentGoroutineID is a narrow compatibility fence for legacy facade
// callbacks. Go intentionally has no public goroutine identifier, so new code
// must use ReducerContext rather than depend on this helper. The parser only
// consumes runtime.Stack's stable header prefix and returns zero on failure.
func currentGoroutineID() uint64 {
	var buffer [64]byte
	n := runtime.Stack(buffer[:], false)
	const prefix = "goroutine "
	if n <= len(prefix) || string(buffer[:len(prefix)]) != prefix {
		return 0
	}
	var id uint64
	for _, b := range buffer[len(prefix):n] {
		if b < '0' || b > '9' {
			break
		}
		id = id*10 + uint64(b-'0')
	}
	return id
}

// deliver 按序投递 effect。
func (c *UIController) deliver(effects []Effect) {
	if c == nil || len(effects) == 0 {
		return
	}
	c.mu.Lock()
	consumer := c.onEffect
	c.mu.Unlock()
	if consumer == nil {
		return
	}
	for _, e := range effects {
		if e == nil {
			continue
		}
		consumer(e)
	}
}

// reindexCoalesceLocked 在队首出队后修正 coalesce 下标。
// 调用方必须持有 c.mu。
func (c *UIController) reindexCoalesceLocked() {
	for key, idx := range c.coalesce {
		if idx == 0 {
			// 被出队的正是该 key 的待处理项（或该槽已被合并替换）。
			delete(c.coalesce, key)
			continue
		}
		c.coalesce[key] = idx - 1
	}
}

// Close 停止接受新 action（Post/TryPost 返回 false），并唤醒 Run 排空
// 剩余队列后退出。幂等。
func (c *UIController) Close() {
	if c == nil {
		return
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.mu.Unlock()
	c.cond.Broadcast()
}

// WaitIdle 阻塞直到队列为空、当前 action 已应用完毕，并且该 action 的
// effect callbacks 已派发。它不等待 effect callback 启动的异步工作；那类
// worker 必须提供自己的受控等待接口。用于测试与确定性路径；生产代码不应
// 依赖（producer 只 Post）。
func (c *UIController) WaitIdle() {
	if c == nil {
		return
	}
	c.mu.Lock()
	for len(c.queue) > 0 || len(c.followups) > 0 || c.inFlight || c.delivering {
		c.cond.Wait()
	}
	c.mu.Unlock()
}

// WaitIdleTimeout is the bounded counterpart used by production drain paths.
// It deliberately polls the actor state instead of spawning a waiter goroutine
// that could outlive the timeout while blocked on the condition variable.
func (c *UIController) WaitIdleTimeout(timeout time.Duration) bool {
	if c == nil {
		return true
	}
	if timeout <= 0 {
		return false
	}
	deadline := time.Now().Add(timeout)
	for {
		c.mu.Lock()
		idle := len(c.queue) == 0 && len(c.followups) == 0 && !c.inFlight && !c.delivering
		c.mu.Unlock()
		if idle {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(time.Millisecond)
	}
}

// Revision 返回已应用的 action 数（AppState Revision 的 Phase 1 来源）。
func (c *UIController) Revision() uint64 {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.revision
}

// Stats 返回一致的诊断快照。
func (c *UIController) Stats() ControllerStats {
	if c == nil {
		return ControllerStats{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return ControllerStats{
		Posted:        c.posted,
		Processed:     c.processed,
		Dropped:       c.dropped,
		Pending:       len(c.queue) + len(c.followups),
		Revision:      c.revision,
		LastAction:    c.lastAction,
		ReducerPanics: c.reducerPanics,
		Closed:        c.closed,
	}
}

// State returns an immutable copy of the controller-owned Phase 1 transition
// state. It intentionally does not expose a partial AppState as a completed
// transcript/bottom-pane model.
func (c *UIController) State() UIControllerState {
	if c == nil {
		return UIControllerState{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state.Clone()
}

// AppState returns the detached semantic/layout snapshot without controller
// delivery diagnostics. Layout and future presenters must consume this value,
// never a live surface mutex state or terminal front buffer.
func (c *UIController) AppState() AppState {
	if c == nil {
		return AppState{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state.AppState.Clone()
}

func actionClassString(action UIAction) string {
	switch a := action.(type) {
	case DrawRequested:
		return "DrawRequested(" + a.Key + ")"
	case Timer:
		return "Timer(" + a.Key + ")"
	case Resize:
		return "Resize"
	case LeaseAcquired:
		return "LeaseAcquired"
	case LeaseReleased:
		return "LeaseReleased"
	case OpenTranscriptOverlay:
		return "OpenTranscriptOverlay"
	case CloseTranscriptOverlay:
		return "CloseTranscriptOverlay"
	case OpenResumePicker:
		return "OpenResumePicker"
	case CloseResumePicker:
		return "CloseResumePicker"
	case OpenBacktrackPicker:
		return "OpenBacktrackPicker"
	case CloseBacktrackPicker:
		return "CloseBacktrackPicker"
	case TranscriptPagerScroll:
		return "TranscriptPagerScroll"
	case TranscriptPagerSetFollowBottom:
		return "TranscriptPagerSetFollowBottom"
	case EffectResult:
		return "EffectResult"
	case BeginHistoryCommit:
		return "BeginHistoryCommit"
	case HistoryCommitAcknowledged:
		return "HistoryCommitAcknowledged"
	case HistoryCommitsAcknowledged:
		return "HistoryCommitsAcknowledged"
	case HistoryCommitFailed:
		return "HistoryCommitFailed"
	case HistoryCommitDeferred:
		return "HistoryCommitDeferred"
	case HistoryProjectionRecovered:
		return "HistoryProjectionRecovered"
	case HistoryProjectionInvalidated:
		return "HistoryProjectionInvalidated"
	case HistoryScrollbackReconciled:
		return "HistoryScrollbackReconciled"
	case RuntimeEvent:
		return "RuntimeEvent(" + a.Kind + ")"
	case ReplaceTranscriptAction:
		return "ReplaceTranscript"
	case SetActiveCellAction:
		return "SetActiveCell"
	case UpdateActiveCellAction:
		return "UpdateActiveCell"
	case ClearActiveCellAction:
		return "ClearActiveCell"
	case FinalizeActiveCellAction:
		return "FinalizeActiveCell"
	case InputEvent:
		return "InputEvent"
	default:
		return "Action"
	}
}
