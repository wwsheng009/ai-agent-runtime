package commands

// chat_ui_actor.go — Phase 1 UI actor 接线（实施指南 Phase 1 任务 2/3/5）。
//
// 目标：业务 producer 不直接 mutation surface；每个 frame 有单一
// action/revision 顺序；FramePump 回调只投递 action。
//
// 本文件是 Phase 1 的 legacy adapter 层：
//   - postUIAction 是协调器侧唯一的 action 投递入口；
//   - reduceUIAction 是 UIController 的 reducer（初期直接调用现有
//     coordinator 路径生成相同输出，保证行为不变——任务 5）；
//   - 定时业务（dynamic-status / stable-commit / active-frame / prompt）
//     由 pump 到期后只 Post(Timer) 触发，业务执行移入 reducer，
//     回调不再直接拿 coordinator/surface lock（任务 3，IR-11）。

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	outputpkg "github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render/output"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/renderengine"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

const chatUIRuntimeEventActionKind = "chat.runtime-event"

const (
	chatUIActorCloseTimeout    = 3 * time.Second
	chatUIActorAbortGrace      = 2 * time.Second
	chatUIActorIdleWaitTimeout = 5 * time.Second
)

// chatRuntimeEventUIAction is commands' typed payload carried by the generic
// ui.RuntimeEvent envelope. The bridge queue has already validated the local
// run epoch before it posts this action; the UI actor supplies the single
// revision order for the resulting UI mutations.
type chatRuntimeEventUIAction struct {
	bridge *chatRuntimeEventBridge
	event  runtimeevents.Event
	epoch  uint64
}

// ensureUIActor 惰性创建 UI actor 并启动其 Run 循环（幂等）。
// 放在协调器构造之后首次投递时创建，避免测试构造路径引入额外 goroutine。
func (c *chatInteractionCoordinator) ensureUIActor() *ui.UIController {
	if c == nil {
		return nil
	}
	c.uiActorOnce.Do(func() {
		// The physical effect consumer is attached by TerminalSessionPresenter
		// only after the legacy surface writer has been fenced. Starting with a
		// nil consumer makes actor construction incapable of creating a second
		// terminal writer.
		c.uiActor = ui.NewUIController(ui.UIControllerConfig{}, ui.ContextualReducerFunc(c.reduceUIActionWithContext), nil)
		go c.uiActor.Run()
	})
	return c.uiActor
}

// 生产 interactive 会话必须经 EnableUnifiedRendererGateway——
// PhysicalSink→RenderOutputGateway。直写 writer 模式仅存在于测试，
// 见 enableUnifiedRendererWithWriter。

// sessionRenderIDLocked 返回稳定 render session ID（Phase 6 gateway 命名；
// 不直接用会随 resume/load 变化的 RuntimeSession.ID）。调用方无需持锁。
func (c *chatInteractionCoordinator) sessionRenderIDLocked() string {
	if c == nil {
		return "render-unknown"
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.renderGatewayID != "" {
		return c.renderGatewayID
	}
	if c.session != nil && c.session.RuntimeSession != nil && c.session.RuntimeSession.ID != "" {
		c.renderGatewayID = "render-" + c.session.RuntimeSession.ID
		return c.renderGatewayID
	}
	c.renderGatewayID = "render-" + fmt.Sprintf("%d", time.Now().UnixNano())
	return c.renderGatewayID
}

// EnableUnifiedRendererGateway 是 Phase 6 production factory：构造
// PhysicalSink→RenderOutputGateway，session 的所有 terminal bytes 经
// gateway 提交（receipt/journal/mirror 可观测），不再直写 os.Stdout。
// 返回 gateway 供 /debug 与测试观察；失败返回 nil。
func (c *chatInteractionCoordinator) EnableUnifiedRendererGateway() *outputpkg.RenderOutputGateway {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	if c.shutdown {
		c.mu.Unlock()
		return nil
	}
	if c.primaryPresenter != nil {
		// 已安装（可能为直写模式）：不重复安装。
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()

	sink := outputpkg.NewPhysicalSink(
		outputpkg.TargetDescriptor{
			SinkID:             "physical-interactive",
			Class:              outputpkg.TargetClassPhysical,
			ProjectionTargetID: "pt-interactive",
		},
		os.Stdout,
		outputpkg.PhysicalSinkOptions{},
	)
	route := outputpkg.RenderRouteConfig{
		Primary:            sink,
		PrimaryOwnership:   outputpkg.SinkOwned,
		ProjectionTargetID: "pt-interactive",
	}
	if path := strings.TrimSpace(c.renderOutputFile); path != "" {
		// --render-output-file：FileSink 作为 committed-only mirror，把全部
		// terminal wire 字节镜像落盘；console primary 渲染完全不变。mirror
		// 是 best-effort 旁路——文件打开失败不阻断交互会话（网关本身无碍）。
		// SyncEveryWrite：每个 committed batch 写入后立即 fsync，运行期间
		// 数据就落盘可见，不依赖 aicli 退出时的 Close 才 flush；SyncOnClose
		// 兜底保证收尾时再做最终同步。
		fs, fsErr := outputpkg.NewFileSink(
			outputpkg.TargetDescriptor{
				SinkID:             "file-interactive",
				Class:              outputpkg.TargetClassCapture,
				ProjectionTargetID: "pt-interactive",
			},
			path,
			outputpkg.FileSinkOptions{SyncEveryWrite: true, SyncOnClose: true},
		)
		if fsErr != nil {
			fmt.Fprintf(os.Stderr, "[aicli] --render-output-file 打开失败，已跳过镜像落盘: %v\n", fsErr)
		} else {
			route.Mirrors = append(route.Mirrors, outputpkg.RenderMirror{
				Sink:      fs,
				Policy:    outputpkg.MirrorCommittedOnly,
				ApplyMode: outputpkg.MirrorApplyBytes,
				Ownership: outputpkg.SinkOwned,
				Timeout:   3 * time.Second,
			})
		}
	}
	gw, err := outputpkg.NewRenderOutputGateway(
		"render-"+c.sessionRenderIDLocked(),
		outputpkg.RenderGatewayOptions{
			Clock:                 outputpkg.SystemClock{},
			CloseTimeout:          3 * time.Second,
			ReconfigureTimeout:    5 * time.Second,
			MaxIntentBytes:        1 << 20,
			MirrorQueueCapacity:   64,
			DeliveryJournalLimit:  outputpkg.JournalLimit{MaxItems: 512, MaxBytes: 4 << 20},
			EventJournalLimit:     outputpkg.JournalLimit{MaxItems: 1024, MaxBytes: 8 << 20},
			MaxSubscriptions:      16,
			MaxSubscriptionBuffer: 128,
		},
		route,
	)
	if err != nil {
		return nil
	}
	gw.Run()
	if !c.enableUnifiedRendererWithPort(gw) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = gw.Close(ctx)
		return nil
	}
	c.renderGateway = gw
	return gw
}

func (c *chatInteractionCoordinator) enableUnifiedRendererWithPort(port outputpkg.RenderOutputPort) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	if c.shutdown {
		c.mu.Unlock()
		return false
	}
	if c.primaryPresenter != nil {
		c.mu.Unlock()
		return true
	}
	surface := c.surface
	c.unifiedRenderer = true
	c.mu.Unlock()
	if surface != nil {
		surface.SetPhysicalWritesEnabled(false)
	}
	actor := c.ensureUIActor()
	if actor == nil {
		c.mu.Lock()
		c.unifiedRenderer = false
		c.mu.Unlock()
		return false
	}
	if !actor.Post(ui.SetThemeContextAction{Theme: ui.CurrentThemeContext()}) ||
		!actor.Post(ui.SetSemanticActiveCellProjectionAction{Enabled: true}) {
		c.mu.Lock()
		c.unifiedRenderer = false
		c.mu.Unlock()
		return false
	}
	actor.WaitIdle()
	presenter := ui.NewTerminalSessionPresenterWithOutput(actor, port, c.primaryTerminalGeometry)
	if c.SetPrimaryPresenter(presenter) {
		return true
	}
	c.mu.Lock()
	if c.primaryPresenter == nil {
		c.unifiedRenderer = false
	}
	c.mu.Unlock()
	return false
}

// enableUnifiedRendererWithWriter 构造直写 writer 模式的 unified renderer。
// Test-only：生产 interactive 会话禁止使用——直写模式不经 gateway，违反
// "所有 interactive terminal effects 经 session-scoped gateway" 收敛目标。
// 生产入口是 EnableUnifiedRendererGateway（PhysicalSink→RenderOutputGateway）。
func (c *chatInteractionCoordinator) enableUnifiedRendererWithWriter(writer io.Writer) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	if c.shutdown {
		c.mu.Unlock()
		return false
	}
	if c.primaryPresenter != nil {
		c.mu.Unlock()
		return true
	}
	surface := c.surface
	c.unifiedRenderer = true
	c.mu.Unlock()
	if writer == nil {
		writer = os.Stdout
	}
	if surface != nil {
		// No physical surface operation may race the forthcoming presenter
		// attach. Logical facade state remains available to existing adapters.
		surface.SetPhysicalWritesEnabled(false)
	}

	actor := c.ensureUIActor()
	if actor == nil {
		c.mu.Lock()
		c.unifiedRenderer = false
		c.mu.Unlock()
		return false
	}
	// Establish the semantic active-cell projection before a physical presenter
	// can consume effects. This is a one-way production authority boundary: a
	// delayed ActiveBand facade action must not replace a Scene mutable cell in
	// the first unified frame.
	if !actor.Post(ui.SetThemeContextAction{Theme: ui.CurrentThemeContext()}) ||
		!actor.Post(ui.SetSemanticActiveCellProjectionAction{Enabled: true}) {
		c.mu.Lock()
		c.unifiedRenderer = false
		c.mu.Unlock()
		if chatDebugFlagEnabled() {
			aicliDiagln("[aicli-diag] enableUnifiedRendererWithWriter: actor.Post failed -> unified renderer OFF")
		}
		return false
	}
	actor.WaitIdle()
	presenter := ui.NewTerminalSessionPresenter(actor, writer, c.primaryTerminalGeometry)
	if c.SetPrimaryPresenter(presenter) {
		if chatDebugFlagEnabled() {
			aicliDiagln("[aicli-diag] enableUnifiedRendererWithWriter: presenter attached -> unified renderer ON")
		}
		return true
	}
	c.mu.Lock()
	if c.primaryPresenter == nil {
		c.unifiedRenderer = false
	}
	c.mu.Unlock()
	return false
}

func (c *chatInteractionCoordinator) UnifiedRendererEnabled() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.unifiedRenderer
}

func (c *chatInteractionCoordinator) RequestUnifiedFrame() {
	if c == nil {
		return
	}
	c.mu.Lock()
	presenter := c.primaryPresenter
	unified := c.unifiedRenderer
	c.mu.Unlock()
	if unified && presenter != nil {
		presenter.Request()
	}
}

// postUIAction 投递一个 action 到 UI actor。actor 尚未初始化（或已关闭）
// 时返回 false。只应在“只投递 action”的调度回调与后续切片的生产者中调用。
func (c *chatInteractionCoordinator) postUIAction(action ui.UIAction) bool {
	actor := c.ensureUIActor()
	if actor == nil {
		return false
	}
	return actor.Post(action)
}

// tryPostUIAction is the non-blocking UI actor ingress used by coalesced
// streaming runtime events. A full mailbox returns false immediately; the
// caller drops the delta rather than letting a stalled reducer propagate
// backpressure into the LLM stream callback.
func (c *chatInteractionCoordinator) tryPostUIAction(action ui.UIAction) bool {
	actor := c.ensureUIActor()
	if actor == nil {
		return false
	}
	return actor.TryPost(action)
}

// postScheduledUIAction is the FramePump-safe posting entry. Timer and
// DrawRequested are coalescable internal actions with a fixed key set; the
// deferred FIFO lane admits them without waiting for bounded mailbox capacity,
// so the single scheduler goroutine can never be blocked by a full actor
// mailbox. Runtime events keep their normal bounded Post backpressure.
func (c *chatInteractionCoordinator) postScheduledUIAction(action ui.UIAction) bool {
	actor := c.ensureUIActor()
	if actor == nil {
		return false
	}
	return actor.PostDeferred(action)
}

// postPromptInputAction is the non-blocking editor ingress. Input snapshots
// are complete latest-wins values, so a full actor mailbox must not make the
// line editor wait behind runtime/terminal work. When TryPost cannot admit the
// snapshot, one retry loop retains only the newest value and eventually posts
// it after the actor makes progress. The reducer remains the sole physical
// prompt writer.
func (c *chatInteractionCoordinator) postPromptInputAction(action ui.InputEvent) bool {
	if c == nil {
		return false
	}
	actor := c.ensureUIActor()
	if actor == nil {
		return false
	}

	// Fold any previously deferred render intent into this newer snapshot
	// before attempting admission. This prevents a later semantic-only update
	// from accidentally clearing an earlier Render=true request.
	c.promptInputDispatchMu.Lock()
	if pending := c.promptInputDispatchPending; pending != nil && pending.Sequence <= action.Sequence {
		action.Render = action.Render || pending.Render
		c.promptInputDispatchPending = nil
	}
	c.promptInputDispatchMu.Unlock()

	if actor.TryPost(action) {
		c.clearPromptInputDispatchThrough(action.Sequence)
		return true
	}
	if actor.Stats().Closed {
		return false
	}

	c.promptInputDispatchMu.Lock()
	if pending := c.promptInputDispatchPending; pending == nil || pending.Sequence <= action.Sequence {
		if pending != nil {
			action.Render = action.Render || pending.Render
		}
		copy := action
		c.promptInputDispatchPending = &copy
	}
	start := !c.promptInputDispatchRunning
	if start {
		c.promptInputDispatchRunning = true
	}
	c.promptInputDispatchMu.Unlock()
	if start {
		go c.flushPromptInputDispatch(actor)
	}
	return true
}

func (c *chatInteractionCoordinator) clearPromptInputDispatchThrough(sequence uint64) {
	if c == nil {
		return
	}
	c.promptInputDispatchMu.Lock()
	if pending := c.promptInputDispatchPending; pending != nil && pending.Sequence <= sequence {
		c.promptInputDispatchPending = nil
	}
	c.promptInputDispatchMu.Unlock()
}

func (c *chatInteractionCoordinator) flushPromptInputDispatch(actor *ui.UIController) {
	if c == nil || actor == nil {
		return
	}
	defer func() {
		c.mu.Lock()
		shutdown := c.shutdown
		c.mu.Unlock()
		c.promptInputDispatchMu.Lock()
		c.promptInputDispatchRunning = false
		restart := !shutdown && c.promptInputDispatchPending != nil
		if restart {
			c.promptInputDispatchRunning = true
		}
		c.promptInputDispatchMu.Unlock()
		if restart {
			go c.flushPromptInputDispatch(actor)
		}
	}()

	for {
		c.mu.Lock()
		shutdown := c.shutdown
		c.mu.Unlock()
		if shutdown {
			c.promptInputDispatchMu.Lock()
			c.promptInputDispatchPending = nil
			c.promptInputDispatchMu.Unlock()
			return
		}

		c.promptInputDispatchMu.Lock()
		pending := c.promptInputDispatchPending
		if pending == nil {
			c.promptInputDispatchMu.Unlock()
			return
		}
		action := *pending
		c.promptInputDispatchMu.Unlock()

		if actor.TryPost(action) {
			c.clearPromptInputDispatchThrough(action.Sequence)
			continue
		}
		if actor.Stats().Closed {
			c.promptInputDispatchMu.Lock()
			c.promptInputDispatchPending = nil
			c.promptInputDispatchMu.Unlock()
			return
		}
		// TryPost is intentionally non-blocking. A short retry lets the actor
		// drain without allocating one goroutine per editor callback.
		time.Sleep(time.Millisecond)
	}
}

// postSurfaceFacadeAction is the surface-only action entry. A facade reached
// synchronously by the legacy reducer adapter is accepted as a checked legacy
// follow-up. A non-reducer call uses the controller's deferred FIFO lane rather
// than waiting for bounded capacity, because the caller may hold c.mu while the
// reducer needs that same lock.
//
// PostFollowup intentionally keeps a follow-up as a separately reduced action
// (and therefore a separately visible revision); it is not a direct surface
// mutation shortcut.
func (c *chatInteractionCoordinator) postSurfaceFacadeAction(action ui.UIAction) bool {
	if c == nil || action == nil {
		return false
	}
	actor := c.ensureUIActor()
	if actor == nil {
		return false
	}
	// A reducer-side facade must remain a causal child. This checked legacy
	// path is non-blocking even when the bounded external mailbox is full.
	if actor.PostFollowup(action) {
		return true
	}
	if status, ok := action.(ui.SetPromptEditorStatusAction); ok {
		return c.postPromptEditorStatusAction(actor, status)
	}
	// A facade call can be reached while its producer holds c.mu. The reducer
	// also reads that mutex, so waiting here for a full bounded mailbox would
	// create a lock cycle. Keep the action in the controller's FIFO, but admit it
	// through the internal deferred lane; external runtime events retain normal
	// Post backpressure.
	return actor.PostDeferred(action)
}

func (c *chatInteractionCoordinator) postPromptEditorStatusAction(actor *ui.UIController, action ui.SetPromptEditorStatusAction) bool {
	if c == nil || actor == nil {
		return false
	}
	if actor.TryPost(action) {
		c.clearPromptEditorStatusDispatch(action)
		return true
	}
	if actor.Stats().Closed {
		return false
	}
	c.promptEditorStatusDispatchMu.Lock()
	copy := action
	c.promptEditorStatusDispatchPending = &copy
	start := !c.promptEditorStatusDispatchRunning
	if start {
		c.promptEditorStatusDispatchRunning = true
	}
	c.promptEditorStatusDispatchMu.Unlock()
	if start {
		go c.flushPromptEditorStatusDispatch(actor)
	}
	return true
}

func (c *chatInteractionCoordinator) clearPromptEditorStatusDispatch(action ui.SetPromptEditorStatusAction) {
	if c == nil {
		return
	}
	c.promptEditorStatusDispatchMu.Lock()
	if pending := c.promptEditorStatusDispatchPending; pending != nil && pending.Line == action.Line {
		c.promptEditorStatusDispatchPending = nil
	}
	c.promptEditorStatusDispatchMu.Unlock()
}

func (c *chatInteractionCoordinator) flushPromptEditorStatusDispatch(actor *ui.UIController) {
	if c == nil || actor == nil {
		return
	}
	defer func() {
		c.mu.Lock()
		shutdown := c.shutdown
		c.mu.Unlock()
		c.promptEditorStatusDispatchMu.Lock()
		c.promptEditorStatusDispatchRunning = false
		pending := c.promptEditorStatusDispatchPending != nil
		c.promptEditorStatusDispatchMu.Unlock()
		if pending && !shutdown {
			c.promptEditorStatusDispatchMu.Lock()
			if !c.promptEditorStatusDispatchRunning {
				c.promptEditorStatusDispatchRunning = true
				go c.flushPromptEditorStatusDispatch(actor)
			}
			c.promptEditorStatusDispatchMu.Unlock()
		}
	}()

	for {
		c.mu.Lock()
		shutdown := c.shutdown
		c.mu.Unlock()
		if shutdown {
			c.promptEditorStatusDispatchMu.Lock()
			c.promptEditorStatusDispatchPending = nil
			c.promptEditorStatusDispatchMu.Unlock()
			return
		}
		c.promptEditorStatusDispatchMu.Lock()
		pending := c.promptEditorStatusDispatchPending
		if pending == nil {
			c.promptEditorStatusDispatchMu.Unlock()
			return
		}
		action := *pending
		c.promptEditorStatusDispatchMu.Unlock()
		if actor.TryPost(action) {
			c.clearPromptEditorStatusDispatch(action)
			continue
		}
		if actor.Stats().Closed {
			c.promptEditorStatusDispatchMu.Lock()
			c.promptEditorStatusDispatchPending = nil
			c.promptEditorStatusDispatchMu.Unlock()
			return
		}
		time.Sleep(time.Millisecond)
	}
}

// postCausalUIAction preserves the current reducer's happens-before relation
// for legacy facade call chains. UIController validates that PostFollowup is
// called by its actual reducer goroutine; external producers therefore fall
// back to normal FIFO even while a reducer is in flight. New code should use
// postCausalUIActionWithContext and pass its explicit ReducerContext.
func (c *chatInteractionCoordinator) postCausalUIAction(action ui.UIAction) bool {
	actor := c.ensureUIActor()
	if actor == nil {
		return false
	}
	if actor.PostFollowup(action) {
		return true
	}
	return actor.Post(action)
}

// postCausalUIActionWithContext is the typed migration path for reducer code.
// Its capability expires when the current reducer returns, so a retained or
// externally invoked context cannot insert a follow-up ahead of mailbox work.
func (c *chatInteractionCoordinator) postCausalUIActionWithContext(context *ui.ReducerContext, action ui.UIAction) bool {
	actor := c.ensureUIActor()
	if actor == nil {
		return false
	}
	if context != nil {
		return context.PostFollowup(action)
	}
	return c.postCausalUIAction(action)
}

// activeStreamShadowActionLocked creates the AppState active-cell update for
// the currently mounted stream. Legacy sessions still map the retained
// ActiveStreamController source. Unified sessions deliberately read the
// already-updated Scene snapshot instead: rendered ActiveBand rows are never a
// source of truth for a production frame.
func (c *chatInteractionCoordinator) activeStreamShadowActionLocked() ui.UIAction {
	if c == nil {
		return nil
	}
	if c.unifiedRenderer {
		return c.unifiedSceneActiveCellActionLocked()
	}
	if c.activeStream == nil {
		return nil
	}
	return c.activeSourceShadowActionLocked(c.activeStream.SourceSnapshot())
}

// activeSourceShadowActionLocked maps a legacy active-stream source into the
// AppState update shape. Unified sessions never accept this controller-local
// source as a production input; they use unifiedSceneActiveCellActionLocked so
// Scene remains the single semantic owner.
func (c *chatInteractionCoordinator) activeSourceShadowActionLocked(snapshot ui.ActiveStreamSourceSnapshot) ui.UIAction {
	if c == nil {
		return nil
	}
	if c.unifiedRenderer {
		return c.unifiedSceneActiveCellActionLocked()
	}
	// Use the same sync.Once-protected accessor as every other producer. The
	// actor may be created while c.mu is held; its reducer can only continue
	// after this method releases c.mu, and the action itself is posted later.
	actor := c.ensureUIActor()
	if actor == nil {
		return nil
	}
	current := actor.ActiveCellState()
	if current.CellID == 0 || current.Phase == ui.ActiveCellInactive {
		c.activeCellShadowID = 0
		c.activeCellShadowRevision = 0
		return nil
	}
	if !snapshot.Active {
		if c.activeCellShadowID != 0 && current.CellID == c.activeCellShadowID {
			c.activeCellShadowID = 0
			c.activeCellShadowRevision = 0
			return ui.ClearActiveCellAction{
				ExpectedCellID:    current.CellID,
				ExpectedKind:      current.Kind,
				ExpectedKindKnown: true,
			}
		}
		return nil
	}
	action, err := ui.UpdateActiveCellActionFromSourceSnapshot(current, snapshot)
	if err != nil {
		// A stale Scene snapshot or a source correction with an incompatible
		// physical range is ignored until the causal transcript action catches
		// up. It must never be repaired by guessing an Ack boundary.
		return nil
	}
	if c.activeCellShadowID == current.CellID && c.activeCellShadowRevision >= action.Active.Revision {
		action.Active.Revision = c.activeCellShadowRevision + 1
	}
	c.activeCellShadowID = current.CellID
	c.activeCellShadowRevision = action.Active.Revision
	return action
}

// unifiedSceneActiveCellActionLocked advances an already-mounted AppState
// active cell from the current Scene snapshot. The runtime encoder has applied
// the semantic event before it invokes the coordinator callback, so this path
// never reconstructs content from ActiveStreamController, ActiveBand rows, or
// terminal state. A first mount is intentionally left to the following
// ReplaceTranscriptAction, which carries the immutable Scene snapshot.
//
// Callers hold c.mu and post the returned action only after releasing it.
func (c *chatInteractionCoordinator) unifiedSceneActiveCellActionLocked() ui.UIAction {
	if c == nil || c.session == nil || c.session.RuntimeEventBridge == nil {
		return nil
	}
	actor := c.ensureUIActor()
	if actor == nil {
		return nil
	}
	snapshot := c.session.RuntimeEventBridge.sceneSnapshot()
	if snapshot == nil {
		return nil
	}
	next, ok := ui.ActiveCellFromSnapshot(snapshot)
	if !ok || next.Phase != ui.ActiveCellMutable {
		return nil
	}
	current := actor.ActiveCellState()
	if current.CellID == 0 || current.Phase != ui.ActiveCellMutable || current.CellID != next.CellID || current.Kind != next.Kind {
		// ReplaceTranscriptAction mounts a new Scene cell atomically. Guessing
		// an identity here would permit an old stream update to overwrite it.
		return nil
	}
	if current.Source == next.Source {
		return nil
	}

	// Active.Revision is a reducer-side source fence, separate from Scene's
	// cell revision. Preserve confirmed effect ranges for append-only source
	// updates; a non-prefix correction must reset all byte offsets.
	next.Revision = current.Revision + 1
	next.Stable = current.Stable
	next.Enqueued = current.Enqueued
	next.Acked = current.Acked
	if !strings.HasPrefix(next.Source, current.Source) {
		next.Stable = ui.SourceRange{}
		next.Enqueued = ui.SourceRange{}
		next.Acked = ui.SourceRange{}
	}
	if err := next.ValidateStreamingRanges(); err != nil {
		return nil
	}
	return ui.UpdateActiveCellAction{
		ExpectedCellID:   current.CellID,
		ExpectedRevision: current.Revision,
		Active:           next,
	}
}

// finalizeActiveCellShadowActionLocked creates the migration-only atomic
// transcript/active transition for a Scene cell that has already reached a
// committed phase. It must be called under c.mu, while the coordinator still
// knows that the legacy stream belongs to this completion boundary. Posting
// remains the caller's responsibility and must happen after c.mu is released.
//
// The active revision is an AppState fence, not the Scene cell revision. A
// shadow source update can consume a revision even when the corresponding
// Scene snapshot has the same semantic cell revision, so equality is valid at
// finalization. The reducer still rejects a stale active fence and requires a
// committed cell in the supplied immutable Scene snapshot.
func (c *chatInteractionCoordinator) finalizeActiveCellShadowActionLocked() ui.UIAction {
	if c == nil || c.session == nil || c.session.RuntimeEventBridge == nil {
		return nil
	}
	actor := c.ensureUIActor()
	if actor == nil {
		return nil
	}
	active := actor.ActiveCellState()
	if active.CellID == 0 || active.Phase != ui.ActiveCellMutable || active.Revision == 0 {
		return nil
	}
	snapshot := c.session.RuntimeEventBridge.sceneSnapshot()
	sceneRevision, ok := finalizedSceneCellRevision(snapshot, active.CellID, active.Kind)
	if !ok {
		return nil
	}
	return ui.FinalizeActiveCellAction{
		Snapshot:                snapshot,
		ExpectedActiveCellID:    active.CellID,
		ExpectedActiveRevision:  active.Revision,
		ExpectedSceneRevision:   sceneRevision,
		ExpectedActiveKind:      active.Kind,
		ExpectedActiveKindKnown: true,
	}
}

// finalizedSceneCellRevision is intentionally local to the commands adapter:
// it decides whether a legacy completion may be mirrored into AppState. The
// reducer repeats the validation before it publishes state, so this check is a
// producer-side stale-snapshot guard rather than an alternate authority.
// Scene and Active revisions are separate version domains; the returned Scene
// revision is carried as its own immutable snapshot fence.
func finalizedSceneCellRevision(snapshot *scene.Snapshot, id scene.CellID, kind scene.CellKind) (uint64, bool) {
	if snapshot == nil || id == 0 {
		return 0, false
	}
	for _, candidate := range snapshot.Cells {
		if candidate == nil || candidate.ID != id || candidate.Kind != kind {
			continue
		}
		switch candidate.Phase {
		case scene.CellCommitted, scene.CellPartiallyHandedOff, scene.CellHandedOff:
			return candidate.Revision, true
		}
	}
	return 0, false
}

// postTranscriptSnapshotFromBridge is the Phase 2 transition bridge for
// non-runtime Scene producers (submitted user input, command result and local
// error). Those legacy paths still build the Scene under coordinator locks,
// so callers must invoke this only after releasing that lock. The immutable
// snapshot then enters the same UIController reducer boundary as a runtime
// event; it is never reconstructed from terminal/history-window state.
func (c *chatInteractionCoordinator) postTranscriptSnapshotFromBridge(bridge *chatRuntimeEventBridge) {
	if c == nil || bridge == nil {
		return
	}
	snapshot := bridge.sceneSnapshot()
	if snapshot == nil {
		return
	}
	_ = c.postUIAction(ui.ReplaceTranscriptAction{Snapshot: snapshot})
}

// closeUIActor 关闭 UI actor：停止接受新 action，Run 排空剩余队列后退出。
// 调用方不得持有 c.mu，因为排空中的 reducer 可能正在等待该锁。
func (c *chatInteractionCoordinator) closeUIActor() {
	if c == nil {
		return
	}
	c.uiActorOnce.Do(func() {})
	c.mu.Lock()
	presenter := c.primaryPresenter
	surface := c.surface
	c.primaryPresenter = nil
	c.terminalSession = nil
	c.terminalExecutor = nil
	if c.session != nil {
		c.session.TerminalSession = nil
		c.session.TerminalSessionExecutor = nil
	}
	c.mu.Unlock()

	var aborter ui.TerminalWriteAborter
	if presenter != nil {
		aborter = presenter
	}
	var abortOnce sync.Once
	abortTerminalWrite := func() {
		abortOnce.Do(func() {
			if aborter != nil {
				_ = aborter.AbortTerminalWrite()
			}
		})
	}
	watchdogDone := make(chan struct{})
	if surface != nil && aborter != nil {
		go func() {
			select {
			case <-time.After(chatUIActorAbortGrace):
				abortTerminalWrite()
			case <-watchdogDone:
			}
		}()
	}

	// Leave DEC 1049 while the unified transport and presenter are still alive.
	// The watchdog makes the release bounded: if the transport write blocks,
	// AbortTerminalWrite releases the physical writer and the call returns.
	if surface != nil {
		var releaseErr error
		for attempt := 0; attempt < 3; attempt++ {
			releaseErr = surface.ReleaseActiveAlternateScreen(context.Background())
			if releaseErr == nil {
				break
			}
			if attempt < 2 {
				time.Sleep(5 * time.Millisecond)
			}
		}
		if releaseErr != nil {
			writeSessionDebugInfo(c.session, fmt.Sprintf("[shutdown] alternate-screen release failed after bounded retry: %v", releaseErr), false)
		}
	}
	close(watchdogDone)

	if presenter != nil {
		if !presenter.CloseTimeout(chatUIActorCloseTimeout) {
			c.mu.Lock()
			c.terminalWritesAbandoned = true
			c.mu.Unlock()
			writeSessionDebugInfo(c.session, "[shutdown] terminal writer did not drain after abort; terminal output abandoned", false)
		}
	}
	if surface != nil {
		surface.SetAlternateScreenLeaseTransport(nil)
	}
	if c.uiActor != nil {
		c.uiActor.Close()
		if !c.uiActor.WaitIdleTimeout(chatUIActorCloseTimeout) {
			stats := c.uiActor.Stats()
			writeSessionDebugInfo(c.session, fmt.Sprintf("[shutdown] UI actor did not drain after close timeout pending=%d last=%q", stats.Pending, stats.LastAction), false)
		}
	}
}

// reduceUIAction 是 UIController 的 Phase 1 legacy adapter reducer（任务 5）：
// 通过现有 coordinator 方法生成与改造前相同的输出。revision 记账由
// controller 完成（Stats.Revision），此处只负责按 action 分发。
//
// Phase 1 已接线 Timer、普通 RuntimeEvent、Input、Resize、Lease 和 facade
// action。审批/问答 RuntimeEvent 仍由 bridge worker 执行，因为它们的 legacy
// prompt 会同步等待 stdin；待拆为 effect/result action 后再纳入 reducer。
// EffectResult 已有 action 入口与诊断账本，但尚无 TerminalSession/effect queue。
func (c *chatInteractionCoordinator) reduceUIAction(revision uint64, action ui.UIAction) []ui.Effect {
	return c.reduceUIActionWithContext(revision, action, nil)
}

// reduceUIActionWithContext is the contextual reducer entry. The existing
// facade adapter remains in place during migration, but direct actor-side
// consequences now receive an explicit reducer-scoped follow-up capability.
func (c *chatInteractionCoordinator) reduceUIActionWithContext(revision uint64, action ui.UIAction, context *ui.ReducerContext) []ui.Effect {
	if c == nil || c.uiActionRejectedAfterShutdown() {
		return nil
	}
	switch act := action.(type) {
	case ui.Timer:
		c.applyTimerAction(act)
	case ui.RuntimeEvent:
		c.applyRuntimeEventActionWithContext(act, context)
	case ui.InputEvent:
		c.applyInputEvent(act)
	case ui.Resize:
		c.applyResizeAction(act)
	case ui.LeaseAcquired, ui.LeaseReleased, ui.EffectResult:
		// UIController owns the Phase 1 transition ledger for these barrier
		// actions. Their terminal semantics remain Phase 3/4 work, so this
		// legacy coordinator adapter intentionally performs no extra mutation.
	case ui.DrawRequested:
		if !c.UnifiedRendererEnabled() {
			c.applyDrawRequested(act)
		}
	case ui.SetActiveBandAction, ui.ClearActiveBandAction,
		ui.SetStatusModelsAction, ui.SetStatusModelAction, ui.SetDynamicStatusModelAction,
		ui.SetSessionIDLineAction,
		ui.ShowPromptAction, ui.ClearPromptRowsAction, ui.SetPromptStateAction,
		ui.TrackPromptInputAction, ui.ResetPromptAction, ui.SetPromptRowsAction,
		ui.SetPromptNoticeAction, ui.SetPromptEditorStatusAction,
		ui.SetComposerPreviewAction, ui.ClearComposerPreviewAction,
		ui.ShowPopupAction, ui.ClearPopupAction, ui.UpdatePopupAction:
		// In the unified session UIController has already incorporated this
		// action into AppState. Calling the facade's legacy Apply path here would
		// recreate a second mutable screen owner even when its writer is fenced.
		if !c.UnifiedRendererEnabled() {
			if surface := c.uiSurface.Load(); surface != nil {
				surface.Apply(action)
			}
		}
	default:
		// Phase 2+ 扩展；P1 其余 action 为定义性类型，尚未接线。
	}
	if c.unifiedRenderer && unifiedRendererActionNeedsFlush(action) {
		// Every state transition that can affect the visible frame wakes the
		// single terminal transaction. The executor coalesces these requests and
		// composes from the newest immutable AppState snapshot.
		return []ui.Effect{ui.FlushEffect{Dirty: renderengine.DirtyContent | renderengine.DirtyBand | renderengine.DirtyStatus}}
	}
	return nil
}

func unifiedRendererActionNeedsFlush(action ui.UIAction) bool {
	switch action.(type) {
	case ui.BeginHistoryCommit,
		ui.HistoryCommitFailed,
		ui.HistoryCommitDeferred,
		ui.HistoryProjectionInvalidated,
		ui.HistoryProjectionRecovered:
		// These actions update terminal-delivery bookkeeping only. The UI
		// controller emits a typed HistoryCommitWakeEffect when recovery or a
		// pending handoff is actually actionable; retrying the same failed frame
		// from a generic FlushEffect would spin forever on a persistent error.
		return false
	case ui.RuntimeEvent:
		// RuntimeEvent is only the semantic parent transaction. Its reducer
		// publishes the actual AppState mutations (status/band actions and the
		// Scene transcript projection) as causal follow-ups. Flushing the parent
		// first paints an unchanged frame for every provider chunk and doubles
		// the pressure on the terminal executor during a fast stream.
		return false
	default:
		return true
	}
}

func (c *chatInteractionCoordinator) applyRuntimeEventAction(action ui.RuntimeEvent) {
	c.applyRuntimeEventActionWithContext(action, nil)
}

func (c *chatInteractionCoordinator) applyRuntimeEventActionWithContext(action ui.RuntimeEvent, context *ui.ReducerContext) {
	if c == nil || action.Kind != chatUIRuntimeEventActionKind {
		return
	}
	payload, ok := action.Payload.(chatRuntimeEventUIAction)
	if !ok || payload.bridge == nil || payload.bridge.session == nil {
		return
	}
	if !payload.bridge.isRunEpochCurrent(payload.epoch) && !isCriticalSubagentLifecycleEvent(payload.event.Type) {
		payload.bridge.logLateRuntimeEvent(payload.event, "runtime event action targets closed run epoch")
		return
	}
	// A replaced interaction owns a different actor/surface. Do not let a
	// queued event from the old bridge mutate the new presentation surface.
	if payload.bridge.session.Interaction != c {
		payload.bridge.logLateRuntimeEvent(payload.event, "runtime event targets replaced interaction")
		return
	}
	payload.bridge.handleEvent(payload.event)
	// Scene is the existing semantic transcript source. Capture it only after
	// the event's mapping transaction succeeds, then make the AppState mirror a
	// causal follow-up of this RuntimeEvent. This never derives transcript from
	// the terminal, ScreenModel, or legacy historyWindow.
	if snapshot := payload.bridge.sceneSnapshot(); snapshot != nil {
		if c.runtimeEventNeedsTranscriptReplace(payload.bridge, payload.event, snapshot) {
			c.postCausalUIActionWithContext(context, ui.ReplaceTranscriptAction{Snapshot: snapshot})
		}
	}
}

// runtimeEventNeedsTranscriptReplace reports whether a runtime event still
// needs a full immutable Scene snapshot projection. The first stream chunk must
// replace the transcript atomically so it can mount the new mutable cell.
// Subsequent chunks for that same mounted assistant or reasoning cell are
// already projected as UpdateActiveCellAction causal follow-ups; replacing the
// whole transcript again would create one full-frame transaction per provider
// chunk and let the UI actor fall arbitrarily far behind the stream.
func (c *chatInteractionCoordinator) runtimeEventNeedsTranscriptReplace(bridge *chatRuntimeEventBridge, event runtimeevents.Event, snapshot *scene.Snapshot) bool {
	if c == nil || snapshot == nil {
		return true
	}
	if c.session == nil || !c.UnifiedRendererEnabled() || !shouldRenderInteractiveOutput(c.session) {
		return true
	}
	if bridge == nil || !bridge.isPrimarySessionEvent(event) {
		return true
	}
	next, ok := ui.ActiveCellFromSnapshot(snapshot)
	if !ok || next.Phase != ui.ActiveCellMutable {
		return true
	}
	expectedKind := scene.KindAssistant
	switch event.Type {
	case runtimechat.EventAssistantDelta:
	case runtimechat.EventAssistantReasoning, "assistant.reasoning":
		if !chatReasoningOutputEnabled(c.session) {
			return true
		}
		expectedKind = scene.KindReasoning
	default:
		return true
	}
	if next.Kind != expectedKind {
		return true
	}
	actor := c.ensureUIActor()
	if actor == nil {
		return true
	}
	current := actor.ActiveCellState()
	// A changed source is the evidence that the compatibility callback emitted
	// an UpdateActiveCellAction for this event. If ordering/request fences
	// rejected it, keep the full snapshot fallback so unrelated Scene changes
	// (for example a late reasoning request) are not hidden.
	return current.CellID == 0 ||
		current.Phase != ui.ActiveCellMutable ||
		current.CellID != next.CellID ||
		current.Kind != next.Kind ||
		current.Source == next.Source
}

func (c *chatInteractionCoordinator) applyInputEvent(action ui.InputEvent) {
	if c == nil {
		return
	}
	snapshot := ui.LineEditorSnapshot{
		Text:        action.Text,
		Cursor:      action.Cursor,
		PasteActive: action.PasteActive,
	}
	c.applyPromptInputSnapshotNow(snapshot, action.Sequence, action.Render)
}

// applyResizeAction is the Phase 1 geometry barrier adapter. The current
// surface still owns terminal probing/layout application, while the
// coordinator owns source-backed reflow of the active stream. Keeping both in
// this reducer action prevents an explicit refresh (theme/resize) from
// reflowing retained source concurrently with a stream paint or facade update.
func (c *chatInteractionCoordinator) applyResizeAction(action ui.Resize) {
	if c == nil {
		return
	}
	if action.Applied {
		// A legacy probe has already applied this geometry and reflowed its
		// physical cache. This barrier only transfers the measured dimensions
		// into AppState; probing again would create a resize feedback loop.
		return
	}
	c.refreshActiveStreamViewportNow()
}

// applyDrawRequested is intentionally narrow in Phase 1: it redraws only the
// retained ActiveBand projection. Transcript/history handoff remains outside
// this adapter until the tokenized presenter effect path is in place.
func (c *chatInteractionCoordinator) applyDrawRequested(action ui.DrawRequested) {
	if c == nil {
		return
	}
	// The active-stream scheduler is the first production DrawRequested source.
	// Keep its generation fence in paintScheduledActiveStreamFrame so an old
	// coalesced deadline cannot repaint a newer stream generation.
	if action.Key == renderengine.FrameKeyActiveFrame {
		c.paintScheduledActiveStreamFrame(action.Generation)
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.shutdown || c.surface == nil || !c.surface.Enabled() {
		return
	}
	if c.activeStream != nil && c.activeStream.Active() {
		_ = c.publishActiveStreamFrameLocked(true)
		return
	}
	c.surface.RefreshActiveBand()
}

func (c *chatInteractionCoordinator) uiActionRejectedAfterShutdown() bool {
	if c == nil {
		return true
	}
	c.mu.Lock()
	shuttingDown := c.shutdown
	c.mu.Unlock()
	return shuttingDown
}

// waitUIActorIdle 等待 UI actor 排空当前队列（测试辅助与确定性路径）。
// actor 尚未创建时立即返回。生产代码不应依赖（producer 只 Post）。
func (c *chatInteractionCoordinator) waitUIActorIdle() {
	if c == nil || c.uiActor == nil {
		return
	}
	c.uiActor.WaitIdle()
}

func (c *chatInteractionCoordinator) waitUIActorIdleTimeout(timeout time.Duration) bool {
	if c == nil || c.uiActor == nil {
		return true
	}
	return c.uiActor.WaitIdleTimeout(timeout)
}

// waitUIActorIdleBounded is the production idle barrier. On timeout it records
// a diagnostic and returns false so callers can fail closed instead of
// continuing into a legacy writer or modal transition with unsettled state.
func (c *chatInteractionCoordinator) waitUIActorIdleBounded(what string) bool {
	if c == nil || c.uiActor == nil {
		return true
	}
	if c.uiActor.WaitIdleTimeout(chatUIActorIdleWaitTimeout) {
		return true
	}
	stats := c.uiActor.Stats()
	writeSessionDebugInfo(c.session, fmt.Sprintf("[ui-actor] idle barrier timeout what=%q pending=%d last=%q", what, stats.Pending, stats.LastAction), false)
	return false
}

// TerminalWritesAbandoned reports whether shutdown had to abandon a blocked
// physical writer. Callers may use it to skip further terminal cleanup.
func (c *chatInteractionCoordinator) TerminalWritesAbandoned() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.terminalWritesAbandoned
}

// applyTimerAction 把 Timer action 分发到对应定时业务（各业务函数自带
// generation/sequence 过期防护，重复触发安全）。
func (c *chatInteractionCoordinator) applyTimerAction(t ui.Timer) {
	if c == nil {
		return
	}
	switch t.Key {
	case renderengine.FrameKeyDynamicStatus:
		c.refreshDynamicStatusTick(t.Generation)
	case renderengine.FrameKeyStableCommit:
		c.runActiveStableCommitTick(t.Generation)
	case renderengine.FrameKeyActiveFrame:
		c.paintScheduledActiveStreamFrame(t.Generation)
	case renderengine.FrameKeyPrompt:
		c.paintScheduledPromptFrame(t.Generation)
	}
}

// paintScheduledPromptFrame 是 prompt 定时绘制的 reducer 端实现
// （原 SchedulePromptRedraw 回调体，任务 3 提取）。
func (c *chatInteractionCoordinator) paintScheduledPromptFrame(seq uint64) {
	if c == nil || !shouldDisplayInteractivePrompt(c.session) {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.shutdown || !c.isReadyLocked() {
		if chatDebugFlagEnabled() {
			aicliDiagf("[aicli-diag] paintScheduledPromptFrame: skipped (shutdown=%v ready=%v)\n",
				c.shutdown, c.isReadyLocked())
		}
		return
	}
	if seq != c.promptSeq {
		return
	}
	if c.promptVisible || c.thinkingActive || c.streamingActive || c.reasoningActive {
		return
	}
	prompt := formatSessionUserPrompt(c.session)
	draft := c.promptInputSnapshotState()
	if c.writer == os.Stdout && c.surface != nil && c.surface.ShowPrompt(prompt) {
		if chatDebugFlagEnabled() {
			aicliDiagln("[aicli-diag] prompt painted on surface (surface!=nil showPrompt=ok)")
		}
		c.promptVisible = true
		c.promptRenderedOnSurface = true
		c.preparePromptGapLocked(false)
		if draft.text != "" {
			rows := c.currentPromptDisplayRowsLocked()
			cursorRow, cursorCol := c.currentPromptCursorPositionLocked()
			c.surface.SetPromptInputStateVersioned(prompt, draft.text, rows, cursorRow, cursorCol, draft.sequence)
		}
		return
	}
	c.promptRenderedOnSurface = false
	if chatDebugFlagEnabled() {
		aicliDiagln("[aicli-diag] prompt NOT painted: surface prompt path unavailable -> physical prompt write")
	}
	c.preparePromptGapLocked(true)
	c.writeTextLocked(prompt)
	if draft.text != "" {
		c.writeTextLocked(draft.text)
	}
	c.promptVisible = true
}
