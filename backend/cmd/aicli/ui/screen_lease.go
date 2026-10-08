package ui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// DefaultAlternateScreenWaitBudget 是等待在途副屏租约释放的默认预算
	// （方案 §3.7.1 前置①，≤2s）。超时后调用方按 §3.7.3 降级 D/I。
	DefaultAlternateScreenWaitBudget = 2 * time.Second
	// alternateScreenWaitPollInterval 是等待轮询间隔；租约释放由用户交互驱动，
	// 通常在毫秒级完成，10ms 轮询对首帧延迟的影响可忽略。
	alternateScreenWaitPollInterval = 10 * time.Millisecond
)

// SetAlternateScreenWaitBudget 配置「撞上在途租约时」的等待预算（P2-4b）。
// 0（默认）保持历史语义：立即返回 ErrScreenLeaseBusy；>0 时
// AcquireAlternateScreen 会在预算内轮询等待在途租约释放，使既有调用点
// （各 S 档 screen handler）无需改动即可获得忙时等待能力。
func (s *FixedBottomSurface) SetAlternateScreenWaitBudget(budget time.Duration) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.leaseWaitBudget = budget
	s.mu.Unlock()
}

// AlternateScreenWaitBudget 返回当前配置的等待预算（只读，供测试/诊断）。
func (s *FixedBottomSurface) AlternateScreenWaitBudget() time.Duration {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.leaseWaitBudget
}

// AcquireAlternateScreenWait 在预算内等待在途租约释放后再获取副屏租约
// （P2-4 前置①：租约等待）。与 AcquireAlternateScreen 的差异：
//   - 撞上在途租约（ErrScreenLeaseBusy）时按 interval 轮询重试，直到 budget 用尽；
//   - 非租约类错误（surface 未启用 / 无统一 transport）立即返回，不做等待；
//   - budget<=0 时使用 DefaultAlternateScreenWaitBudget；ctx 取消立即返回。
//
// 返回的 busy 错误保留底层原因，便于审计「谁占着租约/等了多久」。
func (s *FixedBottomSurface) AcquireAlternateScreenWait(ctx context.Context, req FullscreenRequest, budget time.Duration) (ScreenLease, error) {
	if budget <= 0 {
		budget = DefaultAlternateScreenWaitBudget
	}
	return s.acquireAlternateScreenWithBudget(ctx, req, budget)
}

// acquireAlternateScreenWithBudget 是等待/立即两种语义的公共实现：
// budget<=0 直接单次尝试；>0 时在预算内轮询重试。
func (s *FixedBottomSurface) acquireAlternateScreenWithBudget(ctx context.Context, req FullscreenRequest, budget time.Duration) (ScreenLease, error) {
	if budget <= 0 {
		return s.acquireAlternateScreenOnce(ctx, req)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	deadline := time.Now().Add(budget)
	for {
		lease, err := s.acquireAlternateScreenOnce(ctx, req)
		if err == nil {
			return lease, nil
		}
		if !errors.Is(err, ErrScreenLeaseBusy) {
			return nil, err
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, fmt.Errorf("%w: wait budget %s exhausted: %v", ErrScreenLeaseBusy, budget, err)
		}
		wait := alternateScreenWaitPollInterval
		if remaining < wait {
			wait = remaining
		}
		select {
		case <-ctx.Done():
			return nil, errors.Join(fmt.Errorf("%w: wait cancelled", ErrScreenLeaseBusy), ctx.Err())
		case <-time.After(wait):
		}
	}
}

// ScreenMode identifies which terminal buffer owns the physical screen while
// an alternate-screen lease is active.
type ScreenMode uint8

const (
	// ScreenModePrimary is the default mode: the chat surface owns stdout.
	ScreenModePrimary ScreenMode = iota
	// ScreenModeAlternate marks an alternate-screen session (for example a
	// fullscreen picker) that temporarily owns the physical screen.
	ScreenModeAlternate
)

// ErrScreenLeaseBusy is returned when AcquireAlternateScreen is called while
// another lease is still active.
var ErrScreenLeaseBusy = errors.New("alternate-screen lease is already active")

// ScreenLease hands the physical screen to a fullscreen presenter for the
// duration of one modal interaction. While the lease is active the primary
// FixedBottomSurface keeps updating retained state but must not flush any
// bytes to the terminal; on Release the primary is fully repainted from that
// retained state.
//
// This is the foundation of the fullscreen lifecycle described in
// docs/plan/aicli-tui-unified-render-architecture-refactor-plan.md §11. The
// lease owns the DEC 1049 alternate-screen transport (enter on Acquire, exit
// on Release) inside the same terminal ownership transaction that suspends or
// resumes primary flushing, so a primary status tick, resize or prompt
// repaint can never interleave into the alternate screen.
type ScreenLease interface {
	// ID uniquely identifies this lease instance.
	ID() uint64
	// Mode reports the screen mode granted by the lease.
	Mode() ScreenMode
	// Active reports whether the lease still holds the screen.
	Active() bool
	// Release ends the lease and repaints the primary surface from retained
	// state. Release is idempotent.
	Release(context.Context) error
}

// AlternateScreenLeaseWriter is implemented by a ScreenLease whose fullscreen
// content must travel through the terminal authority that entered DEC 1049.
// Callers such as the transcript pager type-assert this optional capability;
// leases are always transport-backed (the raw os.Stdout fallback was retired
// with the single-writer fence), so fullscreen content can never bypass the
// session writer.
type AlternateScreenLeaseWriter interface {
	WriteAlternateScreen(string) error
}

// AlternateScreenLeaseTransport is the sole physical owner used by a fenced
// FixedBottomSurface. TerminalSessionPresenter implements this interface, so
// DEC 1049 enter/exit and fullscreen frames share the unified terminal writer
// and Release can schedule a source-backed primary recovery only after the
// LeaseReleased action has been posted.
type AlternateScreenLeaseTransport interface {
	EnterAlternateScreen(leaseID uint64) error
	WriteAlternateScreen(leaseID uint64, value string) error
	ExitAlternateScreen(leaseID uint64) error
	RequestPrimaryRecovery()
}

// FullscreenRequest describes an alternate-screen session.
type FullscreenRequest struct {
	// Title is a short human-readable label for diagnostics.
	Title string
}

type fullscreenLease struct {
	surface  *FixedBottomSurface
	id       uint64
	mode     ScreenMode
	mu       sync.Mutex
	released bool
}

func (l *fullscreenLease) WriteAlternateScreen(value string) error {
	if l == nil || l.surface == nil {
		return ErrFullScreenUnavailable
	}
	return l.surface.writeAlternateScreen(l.id, value)
}

func (l *fullscreenLease) ID() uint64 {
	if l == nil {
		return 0
	}
	return l.id
}

func (l *fullscreenLease) Mode() ScreenMode {
	if l == nil {
		return ScreenModePrimary
	}
	return l.mode
}

func (l *fullscreenLease) Active() bool {
	if l == nil || l.surface == nil {
		return false
	}
	l.mu.Lock()
	released := l.released
	l.mu.Unlock()
	return !released && l.surface.LeaseOwned(l.id)
}

func (l *fullscreenLease) Release(ctx context.Context) error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	if l.released {
		l.mu.Unlock()
		return nil
	}
	if l.surface == nil {
		l.released = true
		l.mu.Unlock()
		return nil
	}
	err := l.surface.releaseAlternateScreen(ctx, l.id)
	if err == nil {
		l.released = true
	}
	l.mu.Unlock()
	return err
}

// leaseCounter hands out unique lease ids process-wide.
var leaseCounter atomic.Uint64

// AcquireAlternateScreen suspends primary flushing and hands the physical
// screen to the caller for one fullscreen session. Only one lease can be
// active at a time. The returned lease must be released (preferably with
// defer) on every path, including error paths.
//
// 撞上在途租约时的行为由 SetAlternateScreenWaitBudget 决定：默认 0 = 立即
// 返回 ErrScreenLeaseBusy（历史语义）；>0 = 在预算内等待释放后重试。
func (s *FixedBottomSurface) AcquireAlternateScreen(ctx context.Context, req FullscreenRequest) (ScreenLease, error) {
	budget := time.Duration(0)
	if s != nil {
		s.mu.Lock()
		budget = s.leaseWaitBudget
		s.mu.Unlock()
	}
	return s.acquireAlternateScreenWithBudget(ctx, req, budget)
}

// acquireAlternateScreenOnce 执行一次立即获取尝试（不等待在途租约）。
//
// Acquire enters DEC 1049 through the lease transport inside the same terminal
// ownership transaction that marks the lease active, so no primary frame can
// interleave between "alternate screen entered" and "primary flush
// suspended". A failed enter leaves no suspended state behind.
func (s *FixedBottomSurface) acquireAlternateScreenOnce(_ context.Context, req FullscreenRequest) (ScreenLease, error) {
	if s == nil || s.terminal == nil {
		return nil, fmt.Errorf("%w: no terminal", ErrFullScreenUnavailable)
	}
	s.mu.Lock()
	if !s.enabled {
		s.mu.Unlock()
		return nil, fmt.Errorf("%w: surface is not enabled", ErrFullScreenUnavailable)
	}
	if s.leaseID != 0 {
		activeLeaseID := s.leaseID
		s.mu.Unlock()
		return nil, fmt.Errorf("%w: lease id=%d still active", ErrScreenLeaseBusy, activeLeaseID)
	}
	// Leases always require the unified terminal transport: granting a logical
	// lease without a TerminalSession transport would let the pager write raw
	// stdout into an un-entered or differently-owned screen. The legacy raw
	// DEC 1049 fallback was retired with the single-writer fence (L1-c).
	transport := s.alternateTransport
	if transport == nil {
		s.mu.Unlock()
		return nil, fmt.Errorf("%w: unified alternate-screen transport is unavailable", ErrFullScreenUnavailable)
	}
	leaseID := leaseCounter.Add(1)
	// Keep the surface mutex through the enter transaction. This prevents a
	// concurrent Acquire/Disable from observing or tearing down a half-entered
	// lease; TerminalSession has its own lock against primary frame writes.
	if err := transport.EnterAlternateScreen(leaseID); err != nil {
		s.mu.Unlock()
		return nil, fmt.Errorf("%w: enter unified alternate screen: %v", ErrFullScreenUnavailable, err)
	}
	s.leaseID = leaseID
	s.leaseMode = ScreenModeAlternate
	s.mu.Unlock()
	// Notify the UI actor only after releasing the surface mutex so mailbox
	// backpressure cannot hold the surface lock.
	s.postFacadeAction(LeaseAcquired{LeaseID: leaseID})
	return &fullscreenLease{surface: s, id: leaseID, mode: ScreenModeAlternate}, nil
}

// writeAlternateScreen routes fullscreen content through the same owner that
// acquired the lease. A missing transport is an explicit failure rather than a
// raw stdout fallback; that is the physical-writer fence which keeps pager
// frames out of the primary terminal projection path.
func (s *FixedBottomSurface) writeAlternateScreen(id uint64, value string) error {
	if s == nil || id == 0 {
		return ErrFullScreenUnavailable
	}
	s.mu.Lock()
	if s.leaseID != id || !s.enabled {
		s.mu.Unlock()
		return fmt.Errorf("%w: alternate-screen lease is no longer active", ErrFullScreenUnavailable)
	}
	transport := s.alternateTransport
	s.mu.Unlock()
	if transport == nil {
		return fmt.Errorf("%w: unified alternate-screen transport is unavailable", ErrFullScreenUnavailable)
	}
	return transport.WriteAlternateScreen(id, value)
}

// writeLeaseManagedFullScreenText keeps modal frame bytes with their lease
// owner. Do not wrap AlternateScreenLeaseWriter in io.Writer and pass it to
// writeFullScreenText: that helper takes the global terminal lock, while the
// unified TerminalSession then takes its Presenter lock, which would recurse
// on the non-reentrant lock. The transport writes directly through its own
// transaction boundary instead.
func writeLeaseManagedFullScreenText(lease ScreenLease, fallback io.Writer, value string) error {
	if lease != nil && lease.Active() {
		if writer, ok := lease.(AlternateScreenLeaseWriter); ok {
			return writer.WriteAlternateScreen(value)
		}
	}
	return writeFullScreenText(fallback, value)
}

// LeaseActive reports whether an alternate-screen lease currently suspends
// primary flushing.
func (s *FixedBottomSurface) LeaseActive() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.leaseID != 0
}

// LeaseOwned reports whether id is the currently active lease. Unlike
// LeaseActive, it rejects stale lease handles after a later lease is acquired.
func (s *FixedBottomSurface) LeaseOwned(id uint64) bool {
	if s == nil || id == 0 {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.leaseID == id
}

// ReleaseActiveAlternateScreen is the process-teardown form of ScreenLease
// release. It keeps the unified transport installed until DEC 1049 exit has
// completed, then publishes the same logical release barrier as a live handle.
func (s *FixedBottomSurface) ReleaseActiveAlternateScreen(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	id := s.leaseID
	s.mu.Unlock()
	if id == 0 {
		return nil
	}
	return s.releaseAlternateScreen(ctx, id)
}

// releaseAlternateScreen ends the lease identified by id and asks the lease
// transport for the primary recovery frame. It is idempotent: releasing an
// unknown or already-released id is a no-op.
//
// The DEC 1049 exit sequence and the primary recovery are coordinated by the
// terminal transport, so the transition from alternate screen back to the
// retained primary frame is atomic from the terminal's point of view.
func (s *FixedBottomSurface) releaseAlternateScreen(_ context.Context, id uint64) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.leaseID != id {
		s.mu.Unlock()
		return nil
	}
	if !s.enabled || s.terminal == nil {
		s.leaseID = 0
		s.leaseMode = ScreenModePrimary
		s.mu.Unlock()
		s.postFacadeAction(LeaseReleased{LeaseID: id})
		return nil
	}
	transport := s.alternateTransport
	s.mu.Unlock()
	if transport == nil {
		return fmt.Errorf("%w: unified alternate-screen transport is unavailable", ErrFullScreenUnavailable)
	}
	exitErr := transport.ExitAlternateScreen(id)
	if exitErr != nil {
		return exitErr
	}
	s.mu.Lock()
	if s.leaseID != id {
		s.mu.Unlock()
		return nil
	}
	s.leaseID = 0
	s.leaseMode = ScreenModePrimary
	s.mu.Unlock()
	// Publish the logical barrier only after the terminal transport has
	// invalidated its primary projection. A concurrently requested executor
	// frame therefore cannot observe an unleased AppState while DEC 1049 is
	// still active.
	s.postFacadeAction(LeaseReleased{LeaseID: id})
	// Request after the barrier post. TerminalSessionExecutor waits for the
	// actor to become idle before composing, so it observes LeaseReleased and
	// emits the mandatory source-backed recovery frame, never a legacy repaint.
	transport.RequestPrimaryRecovery()
	return nil
}
