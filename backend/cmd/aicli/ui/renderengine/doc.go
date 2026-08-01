// Package renderengine owns the aicli TUI's unified rendering pipeline.
//
// This package is the landing layer of the long-term refactor described in
// docs/plan/aicli-tui-render-engine-module-design.md (module design) and
// docs/plan/aicli-tui-unified-render-architecture-refactor-plan.md (Scene
// architecture). It converges every render intent onto one scheduling and
// output path instead of the historical collection of ad-hoc timers and
// direct terminal writes.
//
// Stage A (current): the FramePump replaces the coordinator's four
// time.AfterFunc render loops (dynamic status tick, stable commit tick,
// active stream frame, prompt redraw) with a single key-coalesced delayed
// scheduler that runs callbacks serially on one goroutine. The Engine is the
// facade and the Presenter is the batch-flush primitive that will carry the
// ScreenModel diff output in later stages.
package renderengine

// Frame keys for coordinator-owned render intents. Each key maps to at most
// one pending job in the pump; re-scheduling the same key replaces the
// pending job instead of stacking another timer.
const (
	// FrameKeyDynamicStatus is the one-second-aligned dynamic status bar tick.
	FrameKeyDynamicStatus = "dynamicStatus"
	// FrameKeyStableCommit is the coalesced ActiveBand -> scrollback commit tick.
	FrameKeyStableCommit = "stableCommit"
	// FrameKeyActiveFrame is the coalesced live ActiveBand frame (FPS window).
	FrameKeyActiveFrame = "activeFrame"
	// FrameKeyPrompt is the debounced interactive prompt redraw.
	FrameKeyPrompt = "prompt"
)
