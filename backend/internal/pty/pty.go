// Package pty provides a minimal pseudoterminal primitive for interactive
// background sessions. It is intentionally small: start a process attached to a
// PTY, read/write its terminal, resize it, and wait for exit.
//
// Platforms:
//   - Windows 10 1809+: ConPTY (CreatePseudoConsole).
//   - Unix: /dev/ptmx + /dev/pts/N via golang.org/x/sys/unix.
//   - Others (and Windows without ConPTY, e.g. Windows 7): Supported() is false
//     and Start returns ErrUnsupported, so callers fall back to a pipe.
package pty

import "errors"

// ErrUnsupported reports that the platform cannot host a PTY. Callers must fall
// back to a pipe-based session instead of failing the request.
var ErrUnsupported = errors.New("pty: not supported on this platform")

// DefaultCols/DefaultRows size a PTY when the caller supplies no dimensions.
const (
	DefaultCols = 80
	DefaultRows = 40
)

// startOptions collects the optional Start configuration.
type startOptions struct {
	workDir string
	env     []string
	cols    int
	rows    int
}

// Option configures Start.
type Option func(*startOptions)

// WithWorkDir sets the child's working directory.
func WithWorkDir(dir string) Option { return func(o *startOptions) { o.workDir = dir } }

// WithEnv overrides the child environment. On Linux the slice is passed to
// os/exec verbatim (nil inherits the parent). On Windows the slice is encoded
// into the UTF-16LE environment block; an empty/nil slice inherits the parent
// environment (a truly empty block would fail console startup with
// STATUS_DLL_INIT_FAILED).
func WithEnv(env []string) Option { return func(o *startOptions) { o.env = env } }

// WithSize sets the initial terminal size.
func WithSize(cols, rows int) Option {
	return func(o *startOptions) {
		o.cols = cols
		o.rows = rows
	}
}

func resolveOptions(opts []Option) startOptions {
	o := startOptions{}
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}
	return o
}

func normalizeSize(cols, rows int) (int, int) {
	if cols <= 0 {
		cols = DefaultCols
	}
	if rows <= 0 {
		rows = DefaultRows
	}
	return cols, rows
}

// Supported reports whether the current platform can host a PTY. The result is
// cached per process.
func Supported() bool { return platformSupported() }
