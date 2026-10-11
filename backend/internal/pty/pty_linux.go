//go:build linux

package pty

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"
)

// PTY is a /dev/ptmx-backed child process.
type PTY struct {
	ptmx *os.File
	cmd  *exec.Cmd
}

func platformSupported() bool { return true }

// Start launches executable+args attached to a fresh pts.
func Start(executable string, args []string, opts ...Option) (*PTY, error) {
	o := resolveOptions(opts)
	cols, rows := normalizeSize(o.cols, o.rows)

	ptmx, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, fmt.Errorf("pty: open /dev/ptmx: %w", err)
	}
	if err := unix.IoctlSetPointerInt(int(ptmx.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		_ = ptmx.Close()
		return nil, fmt.Errorf("pty: unlockpt: %w", err)
	}
	n, err := unix.IoctlGetInt(int(ptmx.Fd()), unix.TIOCGPTN)
	if err != nil {
		_ = ptmx.Close()
		return nil, fmt.Errorf("pty: ptsname: %w", err)
	}
	slavePath := "/dev/pts/" + strconv.Itoa(n)
	slave, err := os.OpenFile(slavePath, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		_ = ptmx.Close()
		return nil, fmt.Errorf("pty: open slave %s: %w", slavePath, err)
	}
	defer slave.Close()

	_ = unix.IoctlSetWinsize(int(ptmx.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: uint16(rows), Col: uint16(cols)})

	cmd := exec.Command(executable, args...)
	cmd.Stdin = slave
	cmd.Stdout = slave
	cmd.Stderr = slave
	if o.workDir != "" {
		cmd.Dir = o.workDir
	}
	if o.env != nil {
		cmd.Env = o.env
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	if err := cmd.Start(); err != nil {
		_ = ptmx.Close()
		return nil, fmt.Errorf("pty: start: %w", err)
	}
	return &PTY{ptmx: ptmx, cmd: cmd}, nil
}

func (p *PTY) Read(b []byte) (int, error)  { return p.ptmx.Read(b) }
func (p *PTY) Write(b []byte) (int, error) { return p.ptmx.Write(b) }
func (p *PTY) Pid() int                    { return p.cmd.Process.Pid }

// Resize changes the terminal window size.
func (p *PTY) Resize(cols, rows int) error {
	if p == nil || p.ptmx == nil {
		return fmt.Errorf("pty: nil")
	}
	cols, rows = normalizeSize(cols, rows)
	return unix.IoctlSetWinsize(int(p.ptmx.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: uint16(rows), Col: uint16(cols)})
}

// Wait blocks until the child exits or ctx is cancelled.
func (p *PTY) Wait(ctx context.Context) (int, error) {
	if p == nil || p.cmd == nil {
		return -1, fmt.Errorf("pty: nil")
	}
	done := make(chan error, 1)
	go func() { done <- p.cmd.Wait() }()
	select {
	case <-ctx.Done():
		return -1, ctx.Err()
	case err := <-done:
		if err == nil {
			return 0, nil
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode(), nil
		}
		return -1, err
	}
}

func (p *PTY) Close() error {
	if p == nil || p.ptmx == nil {
		return nil
	}
	return p.ptmx.Close()
}
