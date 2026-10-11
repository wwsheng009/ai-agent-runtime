//go:build windows

package pty

import "context"

// ConPTY is deliberately NOT enabled yet.
//
// A reference ConPTY implementation exists in backend/cmd/conpty-probe/main.go,
// but extracting it verbatim into this package crashed the process with
// STATUS_HEAP_CORRUPTION (0xC0000374) in windows.CreateProcess when exercised by
// TestPTYRoundTrip. The same reference probe also never completes its read
// loop. Until the unsafe STARTUPINFOEX/attribute-list handling is rewritten and
// validated against a real ConPTY, Windows reports PTY unsupported and the
// manager uses the pipe-based interactive session instead (which is fully
// tested). This is a P1 follow-up, not a silent degradation: callers receive
// ErrUnsupported and can surface the capability gap.
func platformSupported() bool { return false }

// PTY is a stub on Windows until ConPTY is validated.
type PTY struct{}

// Start reports ErrUnsupported on Windows (see platformSupported).
func Start(executable string, args []string, opts ...Option) (*PTY, error) {
	return nil, ErrUnsupported
}

func (p *PTY) Read(b []byte) (int, error)  { return 0, ErrUnsupported }
func (p *PTY) Write(b []byte) (int, error) { return 0, ErrUnsupported }
func (p *PTY) Pid() int                    { return 0 }
func (p *PTY) Resize(cols, rows int) error { return ErrUnsupported }
func (p *PTY) Wait(ctx context.Context) (int, error) {
	return -1, ErrUnsupported
}
func (p *PTY) Close() error { return nil }
