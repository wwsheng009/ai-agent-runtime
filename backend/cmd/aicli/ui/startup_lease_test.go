package ui

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

const (
	startupEnterSequence = "\x1b[?1049h\x1b[r\x1b[?25l\x1b[2J\x1b[H"
	startupExitSequence  = "\x1b[?25h\x1b[r\x1b[?1049l"
)

// captureTerminalSink redirects the legacy process terminal sink to a buffer
// for the duration of one test.
func captureTerminalSink(t *testing.T) *bytes.Buffer {
	t.Helper()
	buffer := &bytes.Buffer{}
	restore := SetTerminalOutputForTesting(buffer)
	t.Cleanup(restore)
	return buffer
}

// TestStartupAlternateScreenTransportWritesRawBranchBytes pins the startup
// transport to the retired raw branch's exact byte sequences: the lease owns
// DEC 1049 enter/exit, and RequestPrimaryRecovery is a startup no-op.
func TestStartupAlternateScreenTransportWritesRawBranchBytes(t *testing.T) {
	buffer := captureTerminalSink(t)
	transport := NewStartupAlternateScreenTransport(&Terminal{})

	if err := transport.EnterAlternateScreen(1); err != nil {
		t.Fatalf("EnterAlternateScreen: %v", err)
	}
	if got := buffer.String(); got != startupEnterSequence {
		t.Fatalf("enter bytes = %q, want %q", got, startupEnterSequence)
	}
	buffer.Reset()

	if err := transport.WriteAlternateScreen(1, "frame-bytes"); err != nil {
		t.Fatalf("WriteAlternateScreen: %v", err)
	}
	if got := buffer.String(); got != "frame-bytes" {
		t.Fatalf("frame bytes = %q, want %q", got, "frame-bytes")
	}
	buffer.Reset()

	if err := transport.ExitAlternateScreen(1); err != nil {
		t.Fatalf("ExitAlternateScreen: %v", err)
	}
	if got := buffer.String(); got != startupExitSequence {
		t.Fatalf("exit bytes = %q, want %q", got, startupExitSequence)
	}
	buffer.Reset()

	transport.RequestPrimaryRecovery()
	if buffer.Len() != 0 {
		t.Fatalf("RequestPrimaryRecovery must be a no-op, wrote %q", buffer.String())
	}
}

// TestStartupTransportLeaseRoutesFramesAndCleansUp runs the full lease flow
// through the startup transport: enter bytes on Acquire, frame bytes through
// the lease writer, exit bytes on Release, and an idempotent Disable.
func TestStartupTransportLeaseRoutesFramesAndCleansUp(t *testing.T) {
	terminal := &Terminal{}
	surface := NewFixedBottomSurface(terminal)
	surface.EnableForTest(80, 24)
	surface.SetPhysicalWritesEnabled(false)
	surface.SetAlternateScreenLeaseTransport(NewStartupAlternateScreenTransport(terminal))

	buffer := captureTerminalSink(t)

	lease, err := surface.AcquireAlternateScreenWait(context.Background(), FullscreenRequest{Title: "startup"}, DefaultAlternateScreenWaitBudget)
	if err != nil {
		t.Fatalf("AcquireAlternateScreenWait: %v", err)
	}
	if !lease.Active() || !surface.LeaseActive() {
		t.Fatalf("lease not active after acquire: lease=%t surface=%t", lease.Active(), surface.LeaseActive())
	}
	if got := buffer.String(); got != startupEnterSequence {
		t.Fatalf("lease enter bytes = %q, want %q", got, startupEnterSequence)
	}
	buffer.Reset()

	if err := writeLeaseManagedFullScreenText(lease, "startup-frame"); err != nil {
		t.Fatalf("writeLeaseManagedFullScreenText: %v", err)
	}
	if got := buffer.String(); got != "startup-frame" {
		t.Fatalf("frame bytes = %q, want %q", got, "startup-frame")
	}
	buffer.Reset()

	if err := lease.Release(context.Background()); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if got := buffer.String(); got != startupExitSequence {
		t.Fatalf("lease exit bytes = %q, want %q", got, startupExitSequence)
	}
	if lease.Active() || surface.LeaseActive() {
		t.Fatalf("lease still active after release: lease=%t surface=%t", lease.Active(), surface.LeaseActive())
	}

	// Disable is idempotent and nil-safe after the one-shot session.
	surface.Disable()
	surface.Disable()
}

// TestRunStartupFullScreenListFailClosed covers every fail-closed face of D2:
// no terminal, an unusable terminal, a surface without a unified transport,
// and an Enable failure; none of them may write a raw sequence.
func TestRunStartupFullScreenListFailClosed(t *testing.T) {
	ctx := context.Background()
	options := FullScreenListOptions{Items: []FullScreenListItem{{Title: "startup"}}}

	if _, err := RunStartupFullScreenList(ctx, nil, options); !errors.Is(err, ErrFullScreenUnavailable) {
		t.Fatalf("nil terminal: err = %v, want ErrFullScreenUnavailable", err)
	}

	short := &Terminal{}
	short.SetSizeForTest(80, 4)
	if _, err := RunStartupFullScreenList(ctx, short, options); !errors.Is(err, ErrFullScreenUnavailable) {
		t.Fatalf("unusable terminal: err = %v, want ErrFullScreenUnavailable", err)
	}

	// A surface without a unified transport must fail closed immediately and
	// must not burn the lease wait budget.
	noTransport := NewFixedBottomSurface(&Terminal{})
	noTransport.EnableForTest(80, 24)
	noTransport.SetPhysicalWritesEnabled(false)
	started := time.Now()
	_, err := noTransport.AcquireAlternateScreenWait(ctx, FullscreenRequest{Title: "startup"}, DefaultAlternateScreenWaitBudget)
	if !errors.Is(err, ErrFullScreenUnavailable) {
		t.Fatalf("no transport: err = %v, want ErrFullScreenUnavailable", err)
	}
	if errors.Is(err, ErrScreenLeaseBusy) {
		t.Fatalf("no transport: err = %v, must not be reported as a busy lease", err)
	}
	if elapsed := time.Since(started); elapsed >= DefaultAlternateScreenWaitBudget {
		t.Fatalf("no transport: acquire waited %s, want immediate fail-closed", elapsed)
	}

	// Enable failure: the injectable core fails closed without writing bytes.
	buffer := captureTerminalSink(t)
	disabledSurface := NewFixedBottomSurface(&Terminal{})
	if _, err := runStartupFullScreenList(ctx, &Terminal{}, disabledSurface, options); !errors.Is(err, ErrFullScreenUnavailable) {
		t.Fatalf("enable failure: err = %v, want ErrFullScreenUnavailable", err)
	}
	if buffer.Len() != 0 {
		t.Fatalf("enable failure wrote bytes: %q", buffer.String())
	}
}

// TestFullScreenListLeaseManagedLifecycleWritesNoRawSequences pins the
// lifecycle half of D3: enter/close never emit alternate-screen bytes, and
// close restores stdin raw mode exactly once.
func TestFullScreenListLeaseManagedLifecycleWritesNoRawSequences(t *testing.T) {
	buffer := captureTerminalSink(t)
	restoreCalls := 0
	lifecycle := fullScreenListLifecycle{
		restoreRaw: func() error {
			restoreCalls++
			return nil
		},
	}
	if err := lifecycle.enter(); err != nil {
		t.Fatalf("enter: %v", err)
	}
	if err := lifecycle.close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if buffer.Len() != 0 {
		t.Fatalf("lease-managed lifecycle wrote raw bytes: %q", buffer.String())
	}
	if restoreCalls != 1 {
		t.Fatalf("expected raw mode to be restored exactly once, got %d", restoreCalls)
	}
}

// TestStartupSurfaceDisableExitsActiveLease covers the deferred Disable
// fallback of D2: tearing the one-shot surface down while the lease is still
// held must leave DEC 1049 through the same transport and clear lease state.
func TestStartupSurfaceDisableExitsActiveLease(t *testing.T) {
	terminal := &Terminal{}
	surface := NewFixedBottomSurface(terminal)
	surface.EnableForTest(80, 24)
	surface.SetPhysicalWritesEnabled(false)
	surface.SetAlternateScreenLeaseTransport(NewStartupAlternateScreenTransport(terminal))

	buffer := captureTerminalSink(t)
	lease, err := surface.AcquireAlternateScreenWait(context.Background(), FullscreenRequest{Title: "startup"}, DefaultAlternateScreenWaitBudget)
	if err != nil {
		t.Fatalf("AcquireAlternateScreenWait: %v", err)
	}
	buffer.Reset()

	surface.Disable()
	if got := buffer.String(); !strings.Contains(got, startupExitSequence) {
		t.Fatalf("Disable exit bytes = %q, want %q", got, startupExitSequence)
	}
	if lease.Active() || surface.LeaseActive() {
		t.Fatalf("lease survived Disable: lease=%t surface=%t", lease.Active(), surface.LeaseActive())
	}
	// A later Release is a no-op on the already-exited lease.
	if err := lease.Release(context.Background()); err != nil {
		t.Fatalf("Release after Disable: %v", err)
	}
}
