package ui

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/renderengine"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

type terminalSessionLeaseTransportForTest struct {
	session    *TerminalSession
	recoveries int
	exitErr    error
}

func (t *terminalSessionLeaseTransportForTest) EnterAlternateScreen(leaseID uint64) error {
	return t.session.EnterAlternateScreen(leaseID)
}

func (t *terminalSessionLeaseTransportForTest) WriteAlternateScreen(leaseID uint64, value string) error {
	return t.session.WriteAlternateScreen(leaseID, value)
}

func (t *terminalSessionLeaseTransportForTest) ExitAlternateScreen(leaseID uint64) error {
	if t.exitErr != nil {
		err := t.exitErr
		t.exitErr = nil
		return err
	}
	return t.session.ExitAlternateScreen(leaseID)
}

func (t *terminalSessionLeaseTransportForTest) RequestPrimaryRecovery() {
	t.recoveries++
}

// recordingLeaseTransport is a minimal AlternateScreenLeaseTransport for
// lease-mechanics tests: after the raw DEC 1049 fallback was retired (L1-c)
// every lease travels through a transport.
type recordingLeaseTransport struct {
	mu         sync.Mutex
	entered    []uint64
	exited     []uint64
	written    []string
	recoveries int
	enterErr   error
	exitErr    error
}

func (t *recordingLeaseTransport) EnterAlternateScreen(leaseID uint64) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.enterErr != nil {
		err := t.enterErr
		t.enterErr = nil
		return err
	}
	t.entered = append(t.entered, leaseID)
	return nil
}

func (t *recordingLeaseTransport) WriteAlternateScreen(leaseID uint64, value string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.written = append(t.written, value)
	return nil
}

func (t *recordingLeaseTransport) ExitAlternateScreen(leaseID uint64) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.exitErr != nil {
		err := t.exitErr
		t.exitErr = nil
		return err
	}
	t.exited = append(t.exited, leaseID)
	return nil
}

func (t *recordingLeaseTransport) RequestPrimaryRecovery() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.recoveries++
}

func (t *recordingLeaseTransport) enteredIDs() []uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]uint64(nil), t.entered...)
}

func (t *recordingLeaseTransport) exitedIDs() []uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]uint64(nil), t.exited...)
}

func (t *recordingLeaseTransport) recoveryCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.recoveries
}

// newFencedLeaseTestSurface returns a test surface whose physical writer is
// fenced and whose leases travel through a recording transport (L1-c).
func newFencedLeaseTestSurface(t *testing.T) (*FixedBottomSurface, *recordingLeaseTransport) {
	t.Helper()
	surface := NewFixedBottomSurface(nil)
	surface.EnableForTest(80, 24)
	surface.SetPhysicalWritesEnabled(false)
	transport := &recordingLeaseTransport{}
	surface.SetAlternateScreenLeaseTransport(transport)
	return surface, transport
}

// TestFixedBottomSurface_AcquireAlternateScreenLifecycle covers the lease
// contract: acquire, single-lease invariant, idempotent release, and reuse.
func TestFixedBottomSurface_AcquireAlternateScreenLifecycle(t *testing.T) {
	surface, _ := newFencedLeaseTestSurface(t)
	ctx := context.Background()

	lease, err := surface.AcquireAlternateScreen(ctx, FullscreenRequest{Title: "test"})
	if err != nil {
		t.Fatalf("AcquireAlternateScreen: %v", err)
	}
	if lease == nil || lease.ID() == 0 {
		t.Fatalf("lease id must be non-zero, got %d", lease.ID())
	}
	if lease.Mode() != ScreenModeAlternate {
		t.Fatalf("lease mode = %v, want ScreenModeAlternate", lease.Mode())
	}
	if !lease.Active() || !surface.LeaseActive() {
		t.Fatalf("lease must be active: lease.Active=%t surface.LeaseActive=%t", lease.Active(), surface.LeaseActive())
	}

	// A second lease must be rejected while the first is active.
	if _, err := surface.AcquireAlternateScreen(ctx, FullscreenRequest{Title: "second"}); !errors.Is(err, ErrScreenLeaseBusy) {
		t.Fatalf("second acquire: want ErrScreenLeaseBusy, got %v", err)
	}

	if err := lease.Release(ctx); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if lease.Active() || surface.LeaseActive() {
		t.Fatalf("lease must be released: lease.Active=%t surface.LeaseActive=%t", lease.Active(), surface.LeaseActive())
	}
	if err := lease.Release(ctx); err != nil {
		t.Fatalf("double Release must be idempotent, got %v", err)
	}

	// A new lease can be acquired after release.
	lease2, err := surface.AcquireAlternateScreen(ctx, FullscreenRequest{Title: "again"})
	if err != nil {
		t.Fatalf("re-acquire: %v", err)
	}
	if lease2.ID() == lease.ID() {
		t.Fatalf("lease ids must be unique: %d reused", lease2.ID())
	}
	if err := lease2.Release(ctx); err != nil {
		t.Fatalf("Release lease2: %v", err)
	}
}

// TestFixedBottomSurface_AcquireRejectsDisabledSurface ensures a disabled or
// missing surface cannot grant a lease.
func TestFixedBottomSurface_AcquireRejectsDisabledSurface(t *testing.T) {
	surface := NewFixedBottomSurface(nil)
	if _, err := surface.AcquireAlternateScreen(context.Background(), FullscreenRequest{Title: "x"}); err == nil {
		t.Fatalf("expected error for disabled surface")
	}

	var nilSurface *FixedBottomSurface
	if _, err := nilSurface.AcquireAlternateScreen(context.Background(), FullscreenRequest{Title: "x"}); err == nil {
		t.Fatalf("expected error for nil surface")
	}
}

func TestFixedBottomSurface_FencedLeaseUsesTerminalSessionTransport(t *testing.T) {
	surface := NewFixedBottomSurface(nil)
	surface.EnableForTest(80, 24)
	surface.SetPhysicalWritesEnabled(false)
	legacy := &errorWriter{}
	surface.alternateWriter = legacy

	var output bytes.Buffer
	terminalSession := NewTerminalSession(&output)
	base := terminalSessionPlan(1, 80, 24, 20, LeaseState{})
	if result := terminalSession.Flush(base); result.Err != nil || !result.FullRepaint {
		t.Fatalf("initial terminal frame = %#v", result)
	}
	transport := &terminalSessionLeaseTransportForTest{session: terminalSession}
	surface.SetAlternateScreenLeaseTransport(transport)

	var actions []UIAction
	surface.SetUIActorPoster(func(action UIAction) bool {
		actions = append(actions, action)
		return true
	})

	lease, err := surface.AcquireAlternateScreen(context.Background(), FullscreenRequest{Title: "unified"})
	if err != nil {
		t.Fatalf("AcquireAlternateScreen: %v", err)
	}
	if output.Len() == 0 || !strings.Contains(output.String(), "\x1b[?1049h") {
		t.Fatalf("TerminalSession did not emit DEC 1049 enter: %q", output.String())
	}
	if legacy.String() != "" {
		t.Fatalf("fenced surface wrote enter bytes directly: %q", legacy.String())
	}
	if terminalSession.AlternateScreenLeaseID() != lease.ID() {
		t.Fatalf("terminal session lease = %d, want %d", terminalSession.AlternateScreenLeaseID(), lease.ID())
	}

	if _, ok := lease.(AlternateScreenLeaseWriter); !ok {
		t.Fatalf("unified lease does not expose alternate writer: %T", lease)
	}
	if err := writeLeaseManagedFullScreenText(lease, legacy, "pager-frame"); err != nil {
		t.Fatalf("writeLeaseManagedFullScreenText: %v", err)
	}
	if !strings.Contains(output.String(), "pager-frame") {
		t.Fatalf("pager content bypassed terminal session: %q", output.String())
	}
	if legacy.String() != "" {
		t.Fatalf("fenced surface wrote pager bytes directly: %q", legacy.String())
	}

	if err := lease.Release(context.Background()); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if terminalSession.AlternateScreenLeaseID() != 0 {
		t.Fatalf("terminal alternate lease remained active: %d", terminalSession.AlternateScreenLeaseID())
	}
	if transport.recoveries != 1 {
		t.Fatalf("recovery requests = %d, want 1", transport.recoveries)
	}
	if terminalSession.ProjectionState().Validity != renderengine.ProjectionUnknown {
		t.Fatalf("release did not invalidate terminal projection: %#v", terminalSession.ProjectionState())
	}
	if legacy.String() != "" {
		t.Fatalf("fenced surface wrote exit bytes directly: %q", legacy.String())
	}
	if len(actions) != 2 {
		t.Fatalf("lease actions = %#v, want acquire/release", actions)
	}
	if _, ok := actions[0].(LeaseAcquired); !ok {
		t.Fatalf("first action = %T, want LeaseAcquired", actions[0])
	}
	if _, ok := actions[1].(LeaseReleased); !ok {
		t.Fatalf("second action = %T, want LeaseReleased", actions[1])
	}
}

func TestFixedBottomSurface_FailedUnifiedExitRetainsRetryableLease(t *testing.T) {
	surface := NewFixedBottomSurface(nil)
	surface.EnableForTest(80, 24)
	surface.SetPhysicalWritesEnabled(false)
	session := NewTerminalSession(&bytes.Buffer{})
	transport := &terminalSessionLeaseTransportForTest{session: session, exitErr: errors.New("exit unavailable")}
	surface.SetAlternateScreenLeaseTransport(transport)
	var actions []UIAction
	surface.SetUIActorPoster(func(action UIAction) bool {
		actions = append(actions, action)
		return true
	})

	lease, err := surface.AcquireAlternateScreen(context.Background(), FullscreenRequest{Title: "retry"})
	if err != nil {
		t.Fatalf("AcquireAlternateScreen: %v", err)
	}
	if err := lease.Release(context.Background()); err == nil {
		t.Fatal("first release unexpectedly succeeded")
	}
	if !lease.Active() || !surface.LeaseActive() || session.AlternateScreenLeaseID() != lease.ID() {
		t.Fatalf("failed exit lost lease: active=%t surface=%t physical=%d", lease.Active(), surface.LeaseActive(), session.AlternateScreenLeaseID())
	}
	if len(actions) != 1 || transport.recoveries != 0 {
		t.Fatalf("failed exit published release/recovery: actions=%#v recoveries=%d", actions, transport.recoveries)
	}

	if err := lease.Release(context.Background()); err != nil {
		t.Fatalf("retry release: %v", err)
	}
	if lease.Active() || surface.LeaseActive() || session.AlternateScreenLeaseID() != 0 {
		t.Fatalf("successful retry retained lease: active=%t surface=%t physical=%d", lease.Active(), surface.LeaseActive(), session.AlternateScreenLeaseID())
	}
	if len(actions) != 2 || transport.recoveries != 1 {
		t.Fatalf("successful retry barrier = actions=%#v recoveries=%d", actions, transport.recoveries)
	}
}

func TestFixedBottomSurface_ReleaseActiveAlternateScreenUsesTransportBeforeDetach(t *testing.T) {
	surface := NewFixedBottomSurface(nil)
	surface.EnableForTest(80, 24)
	surface.SetPhysicalWritesEnabled(false)
	session := NewTerminalSession(&bytes.Buffer{})
	transport := &terminalSessionLeaseTransportForTest{session: session}
	surface.SetAlternateScreenLeaseTransport(transport)
	lease, err := surface.AcquireAlternateScreen(context.Background(), FullscreenRequest{Title: "shutdown"})
	if err != nil {
		t.Fatalf("AcquireAlternateScreen: %v", err)
	}
	if err := surface.ReleaseActiveAlternateScreen(context.Background()); err != nil {
		t.Fatalf("ReleaseActiveAlternateScreen: %v", err)
	}
	if surface.LeaseActive() || lease.Active() || session.AlternateScreenLeaseID() != 0 {
		t.Fatalf("teardown release retained lease: surface=%t handle=%t physical=%d", surface.LeaseActive(), lease.Active(), session.AlternateScreenLeaseID())
	}
}

func TestFixedBottomSurface_FencedLeaseFailsClosedWithoutTerminalTransport(t *testing.T) {
	surface := NewFixedBottomSurface(nil)
	surface.EnableForTest(80, 24)
	surface.SetPhysicalWritesEnabled(false)

	if _, err := surface.AcquireAlternateScreen(context.Background(), FullscreenRequest{Title: "unwired"}); !errors.Is(err, ErrFullScreenUnavailable) {
		t.Fatalf("fenced acquire error = %v, want ErrFullScreenUnavailable", err)
	}
	if surface.LeaseActive() {
		t.Fatal("failed unified acquire left a logical lease active")
	}
}

// TestFixedBottomSurface_LeaseSuppressesPrimaryFlushAndReleaseRepaints pins
// the screen-ownership guarantee for the transport-backed lease (L1-c): a
// fenced surface never emits primary bytes while the lease is active, and
// Release hands the primary recovery to the lease transport.
func TestFixedBottomSurface_LeaseSuppressesPrimaryFlushAndReleaseRepaints(t *testing.T) {
	surface, transport := newFencedLeaseTestSurface(t)

	var captured bytes.Buffer
	if _, err, ok := surface.WriteOutput(&captured, "line-1\n"); !ok || err != nil {
		t.Fatalf("WriteOutput: ok=%t err=%v", ok, err)
	}
	if captured.Len() != 0 {
		t.Fatalf("fenced surface wrote primary bytes: %q", captured.String())
	}

	lease, err := surface.AcquireAlternateScreen(context.Background(), FullscreenRequest{Title: "picker"})
	if err != nil {
		t.Fatalf("AcquireAlternateScreen: %v", err)
	}

	// State updates during the lease are retained, never written.
	if _, err, ok := surface.WriteOutput(&captured, "line-2\n"); !ok || err != nil {
		t.Fatalf("WriteOutput during lease: ok=%t err=%v", ok, err)
	}
	surface.SetStatusModel(style.StatusLineModel{State: style.RunStreaming, StateText: "streaming"})
	if captured.Len() != 0 {
		t.Fatalf("primary bytes leaked during lease: %q", captured.String())
	}
	if !surface.LeaseActive() {
		t.Fatalf("lease should still be active")
	}

	// Release must hand the primary recovery to the lease transport.
	if err := lease.Release(context.Background()); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if surface.LeaseActive() {
		t.Fatalf("lease must be inactive after release")
	}
	if transport.recoveryCount() != 1 {
		t.Fatalf("release must request primary recovery, got %d", transport.recoveryCount())
	}
}

// TestFixedBottomSurface_DisableDuringLeaseIsTeardownSafe ensures shutdown
// while a picker is open asks the lease transport to leave DEC 1049 without
// painting a primary frame; the pending Release becomes a no-op.
func TestFixedBottomSurface_DisableDuringLeaseIsTeardownSafe(t *testing.T) {
	surface, transport := newFencedLeaseTestSurface(t)
	ctx := context.Background()

	lease, err := surface.AcquireAlternateScreen(ctx, FullscreenRequest{Title: "picker"})
	if err != nil {
		t.Fatalf("AcquireAlternateScreen: %v", err)
	}
	if !surface.LeaseActive() {
		t.Fatalf("lease should be active")
	}

	// Teardown during the lease: Disable exits the alternate buffer via the
	// transport without repainting a primary frame.
	surface.Disable()
	if surface.LeaseActive() {
		t.Fatalf("Disable must drop the lease")
	}
	if got := transport.exitedIDs(); len(got) != 1 || got[0] != lease.ID() {
		t.Fatalf("Disable must exit the alternate screen via transport, got %v", got)
	}
	if err := lease.Release(ctx); err != nil {
		t.Fatalf("Release after Disable must be a no-op, got %v", err)
	}

	// The surface can be re-enabled (test surfaces go through EnableForTest;
	// production Enable() requires a live interactive TTY) and leased again.
	surface.EnableForTest(80, 24)
	if !surface.Enabled() {
		t.Fatalf("surface should be enabled after teardown")
	}
	lease2, err := surface.AcquireAlternateScreen(ctx, FullscreenRequest{Title: "again"})
	if err != nil {
		t.Fatalf("re-acquire after teardown: %v", err)
	}
	if err := lease2.Release(ctx); err != nil {
		t.Fatalf("Release lease2: %v", err)
	}
}

func TestFixedBottomSurface_StaleLeaseDoesNotReportActive(t *testing.T) {
	surface, _ := newFencedLeaseTestSurface(t)
	ctx := context.Background()

	first, err := surface.AcquireAlternateScreen(ctx, FullscreenRequest{Title: "first"})
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	// Simulate teardown invalidating the old handle, then establish a new lease.
	surface.Disable()
	surface.EnableForTest(80, 24)
	second, err := surface.AcquireAlternateScreen(ctx, FullscreenRequest{Title: "second"})
	if err != nil {
		t.Fatalf("second acquire: %v", err)
	}
	defer second.Release(ctx)
	if first.Active() {
		t.Fatal("stale lease must not report active while a newer lease owns the screen")
	}
	if !second.Active() {
		t.Fatal("new lease must report active")
	}
}

func TestFixedBottomSurface_LeaseSuppressesPromptAndActiveBandWrites(t *testing.T) {
	surface, _ := newFencedLeaseTestSurface(t)
	ctx := context.Background()
	lease, err := surface.AcquireAlternateScreen(ctx, FullscreenRequest{Title: "picker"})
	if err != nil {
		t.Fatalf("AcquireAlternateScreen: %v", err)
	}
	defer lease.Release(ctx)

	if prefix, ok := surface.PromptCursorPrefix(0, 1); ok || prefix != "" {
		t.Fatalf("prompt cursor prefix leaked during lease: ok=%t prefix=%q", ok, prefix)
	}
	var editor strings.Builder
	if !surface.WritePromptEditorText(&editor, 0, 1, "draft") {
		t.Fatal("leased prompt editor update should be retained/handled")
	}
	if editor.Len() != 0 {
		t.Fatalf("prompt editor wrote during lease: %q", editor.String())
	}
	before := surface.ownedFrameFlushCount
	if !surface.SetActiveBand([]string{"active-one", "active-two"}) {
		t.Fatal("active band update should be retained during lease")
	}
	if !surface.SetActiveBand([]string{"active-one", "changed"}) {
		t.Fatal("active band diff should be retained during lease")
	}
	if got := surface.ownedFrameFlushCount; got != before {
		t.Fatalf("active band flushed during lease: count=%d want=%d", got, before)
	}
}

// errorWriter fails after a configurable number of successful writes so tests
// can inject failures at each stage of the lease sequence.
type errorWriter struct {
	buffer []byte
	failOn int // fail the Nth Write call (1-based); <=0 disables
	calls  int
}

func (w *errorWriter) Write(p []byte) (int, error) {
	w.calls++
	if w.failOn > 0 && w.calls == w.failOn {
		return 0, errors.New("injected write failure")
	}
	w.buffer = append(w.buffer, p...)
	return len(p), nil
}

func (w *errorWriter) String() string {
	return string(w.buffer)
}

// TestFixedBottomSurface_LeaseOwnsEnterExitSequences verifies the DEC 1049
// transport moved into the lease (L1-c): Acquire enters and Release exits
// through the lease transport, so the picker no longer writes them itself.
func TestFixedBottomSurface_LeaseOwnsEnterExitSequences(t *testing.T) {
	surface, transport := newFencedLeaseTestSurface(t)
	ctx := context.Background()

	lease, err := surface.AcquireAlternateScreen(ctx, FullscreenRequest{Title: "picker"})
	if err != nil {
		t.Fatalf("AcquireAlternateScreen: %v", err)
	}
	if got := transport.enteredIDs(); len(got) != 1 || got[0] != lease.ID() {
		t.Fatalf("acquire must enter the alternate screen via transport, got %v", got)
	}

	if err := lease.Release(ctx); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if got := transport.exitedIDs(); len(got) != 1 || got[0] != lease.ID() {
		t.Fatalf("release must exit the alternate screen via transport, got %v", got)
	}
}

// TestFixedBottomSurface_AcquireEnterFailureRollsBack ensures a failed
// transport enter leaves no suspended state behind: the lease is not granted,
// the surface is not marked leased, and a retry can re-acquire.
func TestFixedBottomSurface_AcquireEnterFailureRollsBack(t *testing.T) {
	surface, transport := newFencedLeaseTestSurface(t)
	transport.enterErr = errors.New("enter unavailable")
	ctx := context.Background()

	lease, err := surface.AcquireAlternateScreen(ctx, FullscreenRequest{Title: "picker"})
	if err == nil {
		t.Fatalf("expected enter failure, got lease %+v", lease)
	}
	if lease != nil {
		t.Fatalf("failed acquire must not return a lease")
	}
	if surface.LeaseActive() {
		t.Fatalf("failed acquire must not leave the surface leased")
	}

	// The surface must be immediately re-leasable after the failure.
	lease2, err := surface.AcquireAlternateScreen(ctx, FullscreenRequest{Title: "again"})
	if err != nil {
		t.Fatalf("re-acquire after enter failure: %v", err)
	}
	if err := lease2.Release(ctx); err != nil {
		t.Fatalf("Release lease2: %v", err)
	}
}
