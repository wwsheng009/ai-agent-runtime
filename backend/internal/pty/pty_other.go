//go:build !windows && !linux

package pty

import "context"

// PTY is a stub on platforms without a PTY implementation.
type PTY struct{}

func platformSupported() bool { return false }

// Start always reports ErrUnsupported on this platform; callers must fall back
// to a pipe-based session.
func Start(executable string, args []string, opts ...Option) (*PTY, error) {
	return nil, ErrUnsupported
}

func (p *PTY) Read(b []byte) (int, error)  { return 0, ErrUnsupported }
func (p *PTY) Write(b []byte) (int, error) { return 0, ErrUnsupported }
func (p *PTY) Pid() int                    { return 0 }

// Kill force-terminates the child; unsupported platforms return ErrUnsupported.
func (p *PTY) Kill() error { return ErrUnsupported }

func (p *PTY) Resize(cols, rows int) error { return ErrUnsupported }
func (p *PTY) Wait(ctx context.Context) (int, error) {
	return -1, ErrUnsupported
}
func (p *PTY) Close() error { return nil }
