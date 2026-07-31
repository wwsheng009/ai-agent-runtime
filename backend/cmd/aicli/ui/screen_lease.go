package ui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
)

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
	l.released = true
	l.mu.Unlock()
	if l.surface == nil {
		return nil
	}
	return l.surface.releaseAlternateScreen(ctx, l.id)
}

// leaseCounter hands out unique lease ids process-wide.
var leaseCounter atomic.Uint64

// AcquireAlternateScreen suspends primary flushing and hands the physical
// screen to the caller for one fullscreen session. Only one lease can be
// active at a time. The returned lease must be released (preferably with
// defer) on every path, including error paths.
//
// Acquire performs the whole DEC 1049 enter sequence inside the same terminal
// ownership transaction that marks the lease active, so no primary frame can
// interleave between "alternate screen entered" and "primary flush
// suspended". A failed enter rolls the sequence back and leaves no suspended
// state behind.
func (s *FixedBottomSurface) AcquireAlternateScreen(_ context.Context, req FullscreenRequest) (ScreenLease, error) {
	if s == nil || s.terminal == nil {
		return nil, fmt.Errorf("%w: no terminal", ErrFullScreenUnavailable)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled {
		return nil, fmt.Errorf("%w: surface is not enabled", ErrFullScreenUnavailable)
	}
	if s.leaseID != 0 {
		return nil, fmt.Errorf("%w: lease id=%d still active", ErrScreenLeaseBusy, s.leaseID)
	}
	writer := s.alternateWriter
	if writer == nil {
		if s.testMode {
			// Synthetic surfaces never write real terminal bytes; tests that
			// assert the sequence boundary inject alternateWriter explicitly.
			writer = io.Discard
		} else {
			writer = os.Stdout
		}
	}
	var enterErr error
	WithTerminalWriteLock(func() {
		if err := writeLeaseSequencesLocked(writer,
			"\x1b[?1049h",
			"\x1b[r",
			"\x1b[?25l",
			"\x1b[2J",
			"\x1b[H",
		); err != nil {
			enterErr = err
			return
		}
		s.leaseID = leaseCounter.Add(1)
		s.leaseMode = ScreenModeAlternate
	})
	if enterErr != nil {
		// Roll back whatever partial enter bytes reached the terminal so a
		// failed acquire cannot leave the primary screen wedged.
		WithTerminalWriteLock(func() {
			_ = writeLeaseSequencesLocked(writer,
				"\x1b[?25h",
				"\x1b[r",
				"\x1b[?1049l",
			)
		})
		return nil, fmt.Errorf("%w: enter alternate screen: %v", ErrFullScreenUnavailable, enterErr)
	}
	return &fullscreenLease{surface: s, id: s.leaseID, mode: ScreenModeAlternate}, nil
}

// writeLeaseSequencesLocked writes terminal sequences while the caller already
// holds the terminal write lock (WithTerminalWriteLock is non-reentrant, so
// the shared writeFullScreenSequences helper cannot be used here).
func writeLeaseSequencesLocked(writer io.Writer, sequences ...string) error {
	if writer == nil {
		return nil
	}
	var writeErr error
	for _, sequence := range sequences {
		n, err := io.WriteString(writer, sequence)
		if err == nil && n != len(sequence) {
			err = io.ErrShortWrite
		}
		writeErr = errors.Join(writeErr, err)
	}
	return writeErr
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

// releaseAlternateScreen ends the lease identified by id and repaints the
// primary surface from retained state. It is idempotent: releasing an unknown
// or already-released id is a no-op.
//
// The DEC 1049 exit sequence and the primary repaint run inside the same
// terminal ownership transaction, so the transition from alternate screen back
// to the retained primary frame is atomic from the terminal's point of view.
func (s *FixedBottomSurface) releaseAlternateScreen(_ context.Context, id uint64) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.leaseID != id {
		return nil
	}
	s.leaseID = 0
	s.leaseMode = ScreenModePrimary
	if !s.enabled || s.terminal == nil {
		return nil
	}
	writer := s.alternateWriter
	if writer == nil {
		if s.testMode {
			writer = io.Discard
		} else {
			writer = os.Stdout
		}
	}
	var exitErr error
	WithTerminalWriteLock(func() {
		if err := writeLeaseSequencesLocked(writer,
			"\x1b[?25h",
			"\x1b[r",
			"\x1b[?1049l",
		); err != nil {
			exitErr = err
		}
		// Invalidate the double-buffer so Release paints a full frame from the
		// latest retained scene instead of diffing against the pre-lease frame.
		if s.viewportBackend != nil {
			s.viewportBackend.Invalidate()
		}
		if s.ownedViewport {
			s.applyLayoutLocked()
			s.renderOwnedViewportLocked()
			s.restoreStoredPromptCursorLocked()
			return
		}
		s.applyLayoutLocked()
		s.renderPopupLocked()
		s.renderStatusLocked()
		s.renderPromptRowsLocked(true)
		s.moveToOutputLocked()
	})
	return exitErr
}
