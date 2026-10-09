package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// StartupAlternateScreenTransport is the one-shot, terminal-backed
// AlternateScreenLeaseTransport for the startup full-screen picker (L5-1 D1).
// It composes the retired raw branch's byte sequences through the Terminal's
// sanctioned control exits, so startup fullscreen bytes travel through the
// same unified process sink as every other legacy UI control and no new
// os.Std* touchpoint is introduced.
//
// The transport is bound to one startup surface and keeps no state between
// sessions: the chat shell builds its own surface/presenter after the picker
// closes.
type StartupAlternateScreenTransport struct {
	terminal *Terminal
}

// NewStartupAlternateScreenTransport binds the startup lease transport to the
// terminal that owns the startup full-screen session.
func NewStartupAlternateScreenTransport(terminal *Terminal) *StartupAlternateScreenTransport {
	return &StartupAlternateScreenTransport{terminal: terminal}
}

// EnterAlternateScreen writes the DEC 1049 enter sequence byte-for-byte
// identical to the retired raw branch: ?1049h + ESC[r + ?25l + 2J + H.
func (t *StartupAlternateScreenTransport) EnterAlternateScreen(uint64) error {
	if t == nil || t.terminal == nil {
		return fmt.Errorf("%w: startup alternate-screen transport is not configured", ErrFullScreenUnavailable)
	}
	t.terminal.EnableAltScreen()
	t.terminal.ResetScrollRegion()
	t.terminal.HideCursor()
	// Clear() would append the \x1b[1;1H cursor-home variant, so the clear and
	// home pair is emitted through the terminal's unified control exit to keep
	// the byte sequence identical to the retired raw branch.
	t.terminal.emitControl("\x1b[2J")
	t.terminal.emitControl("\x1b[H")
	return nil
}

// WriteAlternateScreen forwards fullscreen frame bytes through the terminal's
// unified control exit; the startup transport owns no separate writer, so
// frames can never bypass the process terminal sink.
func (t *StartupAlternateScreenTransport) WriteAlternateScreen(_ uint64, value string) error {
	if t == nil || t.terminal == nil {
		return fmt.Errorf("%w: startup alternate-screen transport is not configured", ErrFullScreenUnavailable)
	}
	t.terminal.emitControl(value)
	return nil
}

// ExitAlternateScreen writes the DEC 1049 exit sequence byte-for-byte
// identical to the retired raw branch: ?25h + ESC[r + ?1049l.
func (t *StartupAlternateScreenTransport) ExitAlternateScreen(uint64) error {
	if t == nil || t.terminal == nil {
		return fmt.Errorf("%w: startup alternate-screen transport is not configured", ErrFullScreenUnavailable)
	}
	t.terminal.ShowCursor()
	t.terminal.ResetScrollRegion()
	t.terminal.DisableAltScreen()
	return nil
}

// RequestPrimaryRecovery is a no-op for the startup transport: the startup
// session has no retained primary frame to repaint, and the picker teardown
// (Release + Disable) hands the primary buffer back to the caller's numbered
// fallback or to the chat shell that starts afterwards.
func (*StartupAlternateScreenTransport) RequestPrimaryRecovery() {}

// RunStartupFullScreenList runs the startup full-screen picker under a
// one-shot lease-managed surface (L5-1 D2): the surface is created for this
// interaction only, enters DEC 1049 through the startup transport, renders
// every frame through the lease, and is released and disabled on every path.
//
// It is fail-closed: a missing ANSI TTY, a terminal whose caps cannot enable
// the surface, an unavailable or busy lease, or any lease-managed failure
// returns ErrFullScreenUnavailable and never writes a raw alternate-screen
// sequence. Callers fall back to their numbered prompt.
func RunStartupFullScreenList(ctx context.Context, terminal *Terminal, options FullScreenListOptions) (FullScreenListResult, error) {
	if !CanUseFullScreenList(terminal) {
		return FullScreenListResult{}, fullScreenUnavailable("startup full-screen list requires an interactive ANSI terminal", nil)
	}
	return runStartupFullScreenList(ctx, terminal, NewFixedBottomSurface(terminal), options)
}

// runStartupFullScreenList is the surface-injectable core of D2. The surface
// is a one-shot object: it is never shared with the chat shell that starts
// after the picker closes.
func runStartupFullScreenList(ctx context.Context, terminal *Terminal, surface *FixedBottomSurface, options FullScreenListOptions) (FullScreenListResult, error) {
	surface.SetPhysicalWritesEnabled(false)
	surface.SetAlternateScreenLeaseTransport(NewStartupAlternateScreenTransport(terminal))
	if !surface.Enable() {
		return FullScreenListResult{}, fullScreenUnavailable("enable startup full-screen surface", nil)
	}
	defer surface.Disable()

	request := FullscreenRequest{Title: strings.TrimSpace(options.Title)}
	if request.Title == "" {
		request.Title = "startup picker"
	}
	lease, err := surface.AcquireAlternateScreenWait(ctx, request, DefaultAlternateScreenWaitBudget)
	if err != nil {
		return FullScreenListResult{}, fmt.Errorf("%w: acquire startup alternate screen: %v", ErrFullScreenUnavailable, err)
	}

	result, runErr := SelectFullScreenListWithLease(ctx, terminal, options, lease)
	releaseErr := lease.Release(ctx)
	if releaseErr != nil {
		releaseErr = fmt.Errorf("%w: release startup alternate screen: %v", ErrFullScreenUnavailable, releaseErr)
	}
	if err := errors.Join(runErr, releaseErr); err != nil {
		return FullScreenListResult{}, err
	}
	return result, nil
}
