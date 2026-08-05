package ui

import (
	"io"
	"sync"
)

// TerminalGeometryProbe reads the dimensions negotiated by the terminal
// owner. It must not write terminal bytes. The presenter turns a changed
// result into an Applied Resize action before asking TerminalSessionExecutor
// to compose a frame.
type TerminalGeometryProbe func() (width, height int, ok bool)

// TerminalSessionPresenter is the lifecycle boundary for the unified
// production primary writer. It binds a UIController effect stream to one
// TerminalSessionExecutor and optionally supplies terminal geometry. Keeping
// this boundary separate from FixedBottomSurface makes it possible to create
// the actor first and attach the physical owner only after chat setup has
// negotiated the writer.
//
// This type does not bypass legacy writers: callers must attach it only at the
// renderer-mode cutover where FixedBottomSurface physical output is disabled.
// Attaching it beside the legacy surface would create two terminal owners.
type TerminalSessionPresenter struct {
	controller *UIController
	executor   *TerminalSessionExecutor
	probe      TerminalGeometryProbe

	mu           sync.Mutex
	closed       bool
	lastWidth    int
	lastHeight   int
	probePending bool
}

// NewTerminalSessionPresenter constructs a presenter but does not attach it
// to the controller. Call Attach after the terminal writer and renderer mode
// have been selected, then call Close before the controller is closed.
func NewTerminalSessionPresenter(controller *UIController, writer io.Writer, probe TerminalGeometryProbe) *TerminalSessionPresenter {
	return NewTerminalSessionPresenterForSession(controller, NewTerminalSession(writer), probe)
}

// NewTerminalSessionPresenterForSession binds an already-created terminal
// projection to a controller. Chat setup uses this form so the session object
// published for diagnostics and terminal replacement is exactly the object
// owned by the presenter.
func NewTerminalSessionPresenterForSession(controller *UIController, session *TerminalSession, probe TerminalGeometryProbe) *TerminalSessionPresenter {
	return &TerminalSessionPresenter{
		controller: controller,
		executor:   NewTerminalSessionExecutor(controller, session),
		probe:      probe,
	}
}

// Attach makes this presenter the controller's effect consumer. It is safe to
// call once during setup; a closed controller rejects the attachment.
func (p *TerminalSessionPresenter) Attach() bool {
	if p == nil || p.controller == nil || p.executor == nil {
		return false
	}
	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if closed {
		return false
	}
	return p.controller.SetEffectConsumer(p.HandleEffect)
}

// HandleEffect is the controller callback. Geometry is posted before the
// executor request; the worker waits for the actor barrier before composing
// its immutable AppState snapshot, so the first frame never uses zero size.
func (p *TerminalSessionPresenter) HandleEffect(effect Effect) {
	if p == nil || effect == nil {
		return
	}
	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if closed {
		return
	}
	switch effect.(type) {
	case FlushEffect, HistoryCommitWakeEffect:
		p.publishGeometry()
		p.executor.HandleEffect(effect)
	}
}

// Request asks for a frame without manufacturing an effect. This is used by
// lifecycle barriers such as initial attach, terminal capability changes and
// alternate-screen release.
func (p *TerminalSessionPresenter) Request() {
	if p == nil {
		return
	}
	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if closed {
		return
	}
	p.publishGeometry()
	p.executor.Request()
}

func (p *TerminalSessionPresenter) publishGeometry() {
	if p == nil || p.controller == nil || p.probe == nil {
		return
	}
	width, height, ok := p.probe()
	if !ok || width < 1 || height < 1 {
		return
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	if p.lastWidth == width && p.lastHeight == height && !p.probePending {
		p.mu.Unlock()
		return
	}
	p.lastWidth = width
	p.lastHeight = height
	if !p.probePending {
		p.probePending = true
	}
	p.mu.Unlock()

	// Resize is a coalescable semantic barrier. If the bounded mailbox is full,
	// leave the pending marker set and let the next presenter request retry it;
	// terminal work must never block the reducer effect callback.
	if p.controller.TryPost(Resize{Width: width, Height: height, Applied: true}) {
		p.mu.Lock()
		p.probePending = false
		p.mu.Unlock()
	}
}

// WaitIdle waits for physical work already requested by this presenter.
func (p *TerminalSessionPresenter) WaitIdle() {
	if p != nil && p.executor != nil {
		p.executor.WaitIdle()
	}
}

// Close detaches the effect consumer, prevents new requests and waits for the
// terminal worker. It must be called before UIController.Close so no effect
// callback can outlive the terminal writer.
func (p *TerminalSessionPresenter) Close() {
	if p == nil {
		return
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	p.mu.Unlock()
	if p.controller != nil {
		_ = p.controller.SetEffectConsumer(nil)
	}
	if p.executor != nil {
		p.executor.Close()
	}
}

// Session exposes the physical projection object for lifecycle code that must
// invalidate it after a terminal replacement. The caller must not use it as
// a semantic transcript source.
func (p *TerminalSessionPresenter) Session() *TerminalSession {
	if p == nil || p.executor == nil {
		return nil
	}
	return p.executor.session
}

// Executor exposes the bounded physical worker for compatibility diagnostics
// during the migration. New call sites should use the presenter lifecycle and
// effect consumer rather than invoking this executor directly.
func (p *TerminalSessionPresenter) Executor() *TerminalSessionExecutor {
	if p == nil {
		return nil
	}
	return p.executor
}

// EnterAlternateScreen implements AlternateScreenLeaseTransport. It keeps
// fullscreen DEC 1049 control bytes under the same TerminalSession ownership
// boundary as primary frames; FixedBottomSurface only retains the logical
// ScreenLease facade after the unified cutover.
func (p *TerminalSessionPresenter) EnterAlternateScreen(leaseID uint64) error {
	if p == nil {
		return ErrTerminalWriterMissing
	}
	p.mu.Lock()
	closed := p.closed
	session := p.Session()
	p.mu.Unlock()
	if closed || session == nil {
		return ErrTerminalWriterMissing
	}
	return session.EnterAlternateScreen(leaseID)
}

// WriteAlternateScreen implements AlternateScreenLeaseTransport and is the
// only route used by lease-managed pagers/lists in unified mode.
func (p *TerminalSessionPresenter) WriteAlternateScreen(leaseID uint64, value string) error {
	if p == nil {
		return ErrTerminalWriterMissing
	}
	p.mu.Lock()
	closed := p.closed
	session := p.Session()
	p.mu.Unlock()
	if closed || session == nil {
		return ErrTerminalWriterMissing
	}
	return session.WriteAlternateScreen(leaseID, value)
}

// ExitAlternateScreen implements AlternateScreenLeaseTransport. TerminalSession
// invalidates its primary projection here; RequestPrimaryRecovery is invoked by
// the surface only after it posts LeaseReleased to the UI actor.
func (p *TerminalSessionPresenter) ExitAlternateScreen(leaseID uint64) error {
	if p == nil {
		return ErrTerminalWriterMissing
	}
	p.mu.Lock()
	closed := p.closed
	session := p.Session()
	p.mu.Unlock()
	if closed || session == nil {
		return ErrTerminalWriterMissing
	}
	return session.ExitAlternateScreen(leaseID)
}

// RequestPrimaryRecovery implements AlternateScreenLeaseTransport. The
// executor waits for the actor's LeaseReleased barrier before composing, so
// this cannot repaint a stale leased AppState snapshot.
func (p *TerminalSessionPresenter) RequestPrimaryRecovery() {
	p.Request()
}
