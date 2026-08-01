// Package renderengine owns the aicli TUI's unified rendering pipeline.
//
// This package is the landing layer of the long-term refactor described in
// docs/plan/aicli-tui-render-engine-module-design.md (module design) and
// docs/plan/aicli-tui-unified-render-architecture-refactor-plan.md (Scene
// architecture). It converges every render intent onto one scheduling and
// output path instead of the historical collection of ad-hoc timers and
// direct terminal writes.
//
// Stages A-D (current): the FramePump replaces the coordinator's four
// time.AfterFunc render loops (dynamic status tick, stable commit tick,
// active stream frame, prompt redraw) with one key-coalesced deadline
// scheduler that runs callbacks serially on one goroutine. Each pending job
// contributes to a DirtyFlags union and is constrained by the configured frame
// budget. The Engine is the facade, the Presenter assembles a frame in
// memory before one target Write, owned viewport composition emits row owners,
// and markdown documents use the shared RenderCache. ScreenModel, row
// ownership, and composition live here; ui/viewport is now a compatibility
// wrapper while the Scene migration proceeds.
package renderengine

import "strings"

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

// DirtyForReason maps the coordinator's diagnostic reason to a stable dirty
// classification. Unknown reasons remain observable as DirtyExternal instead
// of being silently treated as a full-screen repaint.
func DirtyForReason(reason string) DirtyFlags {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "content", "stable-commit":
		return DirtyContent
	case "band", "active-frame":
		return DirtyBand | DirtyContent
	case "status", "dynamic-status":
		return DirtyStatus
	case "prompt":
		return DirtyPrompt
	case "popup":
		return DirtyPopup
	case "geometry", "resize":
		return DirtyGeometry
	case "full", "reconcile":
		return DirtyFull
	default:
		return DirtyExternal
	}
}
