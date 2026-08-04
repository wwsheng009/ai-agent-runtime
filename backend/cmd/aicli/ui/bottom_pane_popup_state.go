package ui

import "strings"

// popupLayerPresent mirrors the surface's popup-stack admission rule. A layer
// with an owner is meaningful even before it has display lines because an
// input popup may only own a composer line.
func popupLayerPresent(layer PopupLayer) bool {
	return layer.Owner != "" || len(layer.Lines) > 0 || strings.TrimSpace(layer.ComposerLine) != ""
}

func (s BottomPaneState) activePopupLayer() PopupLayer {
	return PopupLayer{
		Lines:        append([]string(nil), s.PopupLines...),
		Owner:        s.PopupOwner,
		Instance:     s.PopupInstance,
		Viewport:     clonePopupViewportSpec(s.PopupViewport),
		ComposerLine: s.ComposerLine,
		BelowPrompt:  s.PopupBelowPrompt,
		ReservedRows: s.PopupReservedRows,
	}
}

func (s *BottomPaneState) setActivePopupLayer(layer PopupLayer) {
	if s == nil {
		return
	}
	s.PopupLines = append([]string(nil), layer.Lines...)
	s.PopupOwner = layer.Owner
	s.PopupInstance = layer.Instance
	s.PopupViewport = clonePopupViewportSpec(layer.Viewport)
	s.ComposerLine = layer.ComposerLine
	s.PopupBelowPrompt = layer.BelowPrompt
	s.PopupReservedRows = layer.ReservedRows
	if popupLayerPresent(layer) {
		s.Focus = BottomFocusPopup
		return
	}
	if s.PromptVisible {
		s.Focus = BottomFocusPrompt
	} else {
		s.Focus = BottomFocusNone
	}
}

func (s *BottomPaneState) clearActivePopupLayer() {
	if s == nil {
		return
	}
	s.setActivePopupLayer(PopupLayer{})
}

func clonePopupLayer(layer PopupLayer) PopupLayer {
	layer.Lines = append([]string(nil), layer.Lines...)
	layer.Viewport = clonePopupViewportSpec(layer.Viewport)
	return layer
}

func (s *BottomPaneState) upsertPopupLayer(layer PopupLayer) {
	if s == nil || strings.TrimSpace(layer.Owner) == "" {
		return
	}
	layer = clonePopupLayer(layer)
	for index := range s.PopupStack {
		existing := s.PopupStack[index]
		sameLegacyOwner := layer.Instance == 0 && existing.Instance == 0 && existing.Owner == layer.Owner
		sameInstance := layer.Instance != 0 && existing.Owner == layer.Owner && existing.Instance == layer.Instance
		if sameLegacyOwner || sameInstance {
			s.PopupStack[index] = layer
			return
		}
	}
	s.PopupStack = append(s.PopupStack, layer)
}

func (s *BottomPaneState) removePopupOwnerFromStack(owner string) {
	if s == nil || owner == "" || len(s.PopupStack) == 0 {
		return
	}
	filtered := s.PopupStack[:0]
	for _, layer := range s.PopupStack {
		if layer.Owner != owner {
			filtered = append(filtered, layer)
		}
	}
	s.PopupStack = filtered
}

func (s *BottomPaneState) removePopupHandleFromStack(handle PopupHandle) {
	if s == nil || !handle.Valid() || len(s.PopupStack) == 0 {
		return
	}
	filtered := s.PopupStack[:0]
	for _, layer := range s.PopupStack {
		if layer.Owner != handle.owner || layer.Instance != handle.instance {
			filtered = append(filtered, layer)
		}
	}
	s.PopupStack = filtered
}

func (s *BottomPaneState) restorePopupLayerFromStack() {
	if s == nil {
		return
	}
	for len(s.PopupStack) > 0 {
		last := s.PopupStack[len(s.PopupStack)-1]
		s.PopupStack = s.PopupStack[:len(s.PopupStack)-1]
		if popupLayerPresent(last) {
			s.setActivePopupLayer(last)
			return
		}
	}
	s.clearActivePopupLayer()
}

func popupReservedRowsForAction(state BottomPaneState, height int, lines []string, owner string, belowPrompt bool) int {
	if !belowPrompt || len(lines) == 0 {
		return 0
	}
	rows := len(lines)
	if state.PopupBelowPrompt && state.PopupOwner == owner && state.PopupReservedRows > rows {
		rows = state.PopupReservedRows
	}
	if maxRows := maxBottomPanePopupRows(height, state.promptReservedRowCount(), 0); maxRows > 0 && rows > maxRows {
		return maxRows
	}
	return rows
}

func popupLayerFromShowAction(state BottomPaneState, height int, action ShowPopupAction) PopupLayer {
	owner := strings.TrimSpace(action.Owner)
	lines := cloneAndSanitizePopupLines(action.Lines)
	layer := PopupLayer{
		Lines:        lines,
		Owner:        owner,
		ComposerLine: strings.TrimRight(SanitizeTerminalText(action.Prompt), "\r\n"),
		BelowPrompt:  action.BelowPrompt,
		ReservedRows: popupReservedRowsForAction(state, height, lines, owner, action.BelowPrompt),
	}
	if action.Handle != nil && action.Handle.Valid() {
		layer.Owner = action.Handle.owner
		layer.Instance = action.Handle.instance
		layer.Viewport = clonePopupViewportSpec(action.Viewport)
		layer.BelowPrompt = false
		layer.ReservedRows = 0
	}
	return layer
}

func (s *BottomPaneState) applyPopupShow(action ShowPopupAction, height int) {
	if s == nil {
		return
	}
	layer := popupLayerFromShowAction(*s, height, action)
	if action.Handle != nil && action.Handle.Valid() {
		s.applyPopupBegin(layer)
		return
	}
	s.applyPopupSet(layer)
}

// applyPopupSet is the owner-based ShowPopup* transition. It mirrors
// setActivePopupStateLocked, but only changes semantic overlay state.
func (s *BottomPaneState) applyPopupSet(layer PopupLayer) {
	if s == nil {
		return
	}
	if layer.Owner == "" {
		s.PopupStack = nil
		s.setActivePopupLayer(layer)
		return
	}
	active := s.activePopupLayer()
	if active.Owner == layer.Owner {
		layer.Instance = 0
		layer.Viewport = nil
		s.setActivePopupLayer(layer)
		return
	}
	if active.Owner != "" && popupOwnerPriority(layer.Owner) < popupOwnerPriority(active.Owner) {
		s.upsertPopupLayer(layer)
		return
	}
	if popupLayerPresent(active) {
		s.upsertPopupLayer(active)
	}
	s.removePopupOwnerFromStack(layer.Owner)
	s.setActivePopupLayer(layer)
}

// applyPopupBegin is the tokenized BeginPopupInputForOwner* transition. It
// permits callers to receive a handle before actor reduction while retaining
// the same priority and restoration behavior as FixedBottomSurface.
func (s *BottomPaneState) applyPopupBegin(layer PopupLayer) {
	if s == nil || layer.Owner == "" || layer.Instance == 0 {
		return
	}
	active := s.activePopupLayer()
	if active.Owner != "" && popupOwnerPriority(layer.Owner) < popupOwnerPriority(active.Owner) {
		s.PopupStack = append(s.PopupStack, clonePopupLayer(layer))
		return
	}
	if popupLayerPresent(active) {
		s.PopupStack = append(s.PopupStack, active)
	}
	s.setActivePopupLayer(layer)
}

func (s *BottomPaneState) applyPopupUpdate(action UpdatePopupAction) {
	if s == nil || !action.Handle.Valid() {
		return
	}
	lines := cloneAndSanitizePopupLines(action.Lines)
	prompt := strings.TrimRight(SanitizeTerminalText(action.Prompt), "\r\n")
	if s.PopupOwner == action.Handle.owner && s.PopupInstance == action.Handle.instance {
		s.PopupLines = lines
		s.PopupBelowPrompt = false
		s.PopupReservedRows = 0
		s.ComposerLine = prompt
		s.Focus = BottomFocusPopup
		return
	}
	for index := len(s.PopupStack) - 1; index >= 0; index-- {
		layer := &s.PopupStack[index]
		if layer.Owner == action.Handle.owner && layer.Instance == action.Handle.instance {
			layer.Lines = lines
			layer.BelowPrompt = false
			layer.ReservedRows = 0
			layer.ComposerLine = prompt
			return
		}
	}
}

func (s *BottomPaneState) applyPopupClear(action ClearPopupAction) {
	if s == nil {
		return
	}
	if action.Handle != nil && action.Handle.Valid() {
		handle := *action.Handle
		if s.PopupOwner != handle.owner || s.PopupInstance != handle.instance {
			s.removePopupHandleFromStack(handle)
			return
		}
		s.restorePopupLayerFromStack()
		return
	}
	owner := strings.TrimSpace(action.Owner)
	if owner != "" {
		if s.PopupOwner != owner {
			s.removePopupOwnerFromStack(owner)
			return
		}
		s.restorePopupLayerFromStack()
		return
	}
	s.PopupStack = nil
	s.clearActivePopupLayer()
}
