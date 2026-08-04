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
	"os"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/renderengine"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

const chatUIRuntimeEventActionKind = "chat.runtime-event"

// chatRuntimeEventUIAction is commands' typed payload carried by the generic
// ui.RuntimeEvent envelope. The bridge queue has already validated the local
// run epoch before it posts this action; the UI actor supplies the single
// revision order for the resulting UI mutations.
type chatRuntimeEventUIAction struct {
	bridge *chatRuntimeEventBridge
	event  runtimeevents.Event
}

// ensureUIActor 惰性创建 UI actor 并启动其 Run 循环（幂等）。
// 放在协调器构造之后首次投递时创建，避免测试构造路径引入额外 goroutine。
func (c *chatInteractionCoordinator) ensureUIActor() *ui.UIController {
	if c == nil {
		return nil
	}
	c.uiActorOnce.Do(func() {
		c.uiActor = ui.NewUIController(ui.UIControllerConfig{}, ui.ReducerFunc(c.reduceUIAction), nil)
		go c.uiActor.Run()
	})
	return c.uiActor
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

// postSurfaceFacadeAction is the surface-only action entry. A facade reached
// from a runtime reducer is a causal consequence of the current action, so it
// must use the controller's internal follow-up queue instead of blocking on
// the bounded external mailbox that this reducer is currently draining. Calls
// made outside a reducer retain the normal Post semantics.
//
// PostFollowup intentionally keeps a follow-up as a separately reduced action
// (and therefore a separately visible revision); it is not a direct surface
// mutation shortcut.
func (c *chatInteractionCoordinator) postSurfaceFacadeAction(action ui.UIAction) bool {
	return c.postCausalUIAction(action)
}

// postCausalUIAction preserves the current reducer's happens-before relation
// for semantic snapshots and facade actions. It is intentionally not the
// default producer API: normal producers must still use postUIAction so their
// durable backpressure remains visible at the ingress boundary.
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

// activeStreamShadowActionLocked creates the migration-only AppState mirror
// for the currently mounted Scene active cell. It must be called while c.mu is
// held, but the returned action is posted only after that lock is released.
// The legacy ActiveStreamController/surface remains the sole production
// visual owner during this phase.
func (c *chatInteractionCoordinator) activeStreamShadowActionLocked() ui.UIAction {
	if c == nil || c.activeStream == nil {
		return nil
	}
	return c.activeSourceShadowActionLocked(c.activeStream.SourceSnapshot())
}

// activeSourceShadowActionLocked mirrors an arbitrary semantic active source
// (assistant, reasoning, or running tool) without making rendered rows part of
// AppState. Callers must hold c.mu and post the returned action after unlock.
func (c *chatInteractionCoordinator) activeSourceShadowActionLocked(snapshot ui.ActiveStreamSourceSnapshot) ui.UIAction {
	if c == nil {
		return nil
	}
	// Use the same sync.Once-protected accessor as every other producer. The
	// actor may be created while c.mu is held; its reducer can only continue
	// after this method releases c.mu, and the action itself is posted later.
	actor := c.ensureUIActor()
	if actor == nil {
		return nil
	}
	current := actor.AppState().Active
	if current.CellID == 0 || current.Phase == ui.ActiveCellInactive {
		c.activeCellShadowID = 0
		c.activeCellShadowRevision = 0
		return nil
	}
	if !snapshot.Active {
		if c.activeCellShadowID != 0 && current.CellID == c.activeCellShadowID {
			c.activeCellShadowID = 0
			c.activeCellShadowRevision = 0
			return ui.ClearActiveCellAction{}
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
	active := actor.AppState().Active
	if active.CellID == 0 || active.Phase != ui.ActiveCellMutable || active.Revision == 0 {
		return nil
	}
	snapshot := c.session.RuntimeEventBridge.sceneSnapshot()
	if !finalizedSceneCellAtOrAfter(snapshot, active.CellID, active.Revision) {
		return nil
	}
	return ui.FinalizeActiveCellAction{
		Snapshot:               snapshot,
		ExpectedActiveCellID:   active.CellID,
		ExpectedActiveRevision: active.Revision,
	}
}

// finalizedSceneCellAtOrAfter is intentionally local to the commands adapter:
// it decides whether a legacy completion may be mirrored into AppState. The
// reducer repeats the validation before it publishes state, so this check is a
// producer-side stale-snapshot guard rather than an alternate authority.
func finalizedSceneCellAtOrAfter(snapshot *scene.Snapshot, id scene.CellID, revision uint64) bool {
	if snapshot == nil || id == 0 {
		return false
	}
	for _, candidate := range snapshot.Cells {
		if candidate == nil || candidate.ID != id || candidate.Revision < revision {
			continue
		}
		switch candidate.Phase {
		case scene.CellCommitted, scene.CellPartiallyHandedOff, scene.CellHandedOff:
			return true
		}
	}
	return false
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
	c.uiActorOnce.Do(func() {}) // 确保 Once 已消费，避免之后误建
	if c.uiActor != nil {
		c.uiActor.Close()
		c.uiActor.WaitIdle()
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
	if c == nil || c.uiActionRejectedAfterShutdown() {
		return nil
	}
	switch act := action.(type) {
	case ui.Timer:
		c.applyTimerAction(act)
	case ui.RuntimeEvent:
		c.applyRuntimeEventAction(act)
	case ui.InputEvent:
		c.applyInputEvent(act)
	case ui.Resize:
		c.applyResizeAction(act)
	case ui.LeaseAcquired, ui.LeaseReleased, ui.EffectResult:
		// UIController owns the Phase 1 transition ledger for these barrier
		// actions. Their terminal semantics remain Phase 3/4 work, so this
		// legacy coordinator adapter intentionally performs no extra mutation.
	case ui.DrawRequested:
		c.applyDrawRequested(act)
	case ui.SetActiveBandAction, ui.ClearActiveBandAction,
		ui.SetStatusModelsAction, ui.SetStatusModelAction, ui.SetDynamicStatusModelAction,
		ui.ShowPromptAction, ui.ClearPromptRowsAction, ui.SetPromptStateAction,
		ui.TrackPromptInputAction, ui.ResetPromptAction, ui.SetPromptRowsAction,
		ui.SetPromptNoticeAction, ui.SetPromptEditorStatusAction,
		ui.SetComposerPreviewAction, ui.ClearComposerPreviewAction,
		ui.ShowPopupAction, ui.ClearPopupAction, ui.UpdatePopupAction:
		// Phase 1 任务 4/5：facade action 经 legacy adapter（surface.Apply）
		// 同步应用，输出与改造前一致。注意：Apply 不能在持有 c.mu 时调用
		// （生产者可能在持 c.mu 时投递 action，避免锁环）；surface 自带锁。
		if surface := c.uiSurface.Load(); surface != nil {
			surface.Apply(action)
		}
	default:
		// Phase 2+ 扩展；P1 其余 action 为定义性类型，尚未接线。
	}
	return nil
}

func (c *chatInteractionCoordinator) applyRuntimeEventAction(action ui.RuntimeEvent) {
	if c == nil || action.Kind != chatUIRuntimeEventActionKind {
		return
	}
	payload, ok := action.Payload.(chatRuntimeEventUIAction)
	if !ok || payload.bridge == nil || payload.bridge.session == nil {
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
		c.postCausalUIAction(ui.ReplaceTranscriptAction{Snapshot: snapshot})
	}
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
	if action.Render {
		c.renderPromptInputSnapshotNow(snapshot)
		return
	}
	c.setPromptInputSnapshotNow(snapshot)
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
	if c.shutdown {
		return
	}
	if seq != c.promptSeq {
		return
	}
	if c.promptVisible || c.thinkingActive || c.streamingActive || c.reasoningActive {
		return
	}
	prompt := formatSessionUserPrompt(c.session)
	if c.writer == os.Stdout && c.surface != nil && c.surface.ShowPrompt(prompt) {
		c.promptVisible = true
		c.promptRenderedOnSurface = true
		c.preparePromptGapLocked(false)
		if c.promptInput != "" {
			rows := c.currentPromptDisplayRowsLocked()
			cursorRow, cursorCol := c.currentPromptCursorPositionLocked()
			c.surface.SetPromptInputState(prompt, c.promptInput, rows, cursorRow, cursorCol)
		}
		return
	}
	c.promptRenderedOnSurface = false
	c.preparePromptGapLocked(true)
	c.writeTextLocked(prompt)
	if c.promptInput != "" {
		c.writeTextLocked(c.promptInput)
	}
	c.promptVisible = true
}
