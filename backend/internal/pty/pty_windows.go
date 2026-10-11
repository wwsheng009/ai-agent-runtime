//go:build windows

package pty

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The ConPTY path is built on golang.org/x/sys/windows' official wrappers
// (CreatePseudoConsole / ResizePseudoConsole / ClosePseudoConsole /
// NewProcThreadAttributeList + StartupInfoEx). No hand-rolled attribute-list
// layout: the previous STATUS_HEAP_CORRUPTION came from emulating STARTUPINFOEX
// with a []byte slice header standing in for the ProcThreadAttributeList pointer
// and calling kernel32 through NewProc, which corrupts the marshalled struct.
// SyscallProc in x/sys is properly //go:syscall marked and the struct has the
// exact field layout the API expects (2026-10-11).

type handleIO struct{ handle windows.Handle }

func (h *handleIO) Read(p []byte) (int, error) {
	var n uint32
	err := windows.ReadFile(h.handle, p, &n, nil)
	if err != nil && n > 0 {
		// ConPTY pipes report ERROR_BROKEN_PIPE after the child exits; surface
		// the bytes read first so callers can drain the tail.
		return int(n), err
	}
	return int(n), err
}

func (h *handleIO) Write(p []byte) (int, error) {
	var n uint32
	err := windows.WriteFile(h.handle, p, &n, nil)
	return int(n), err
}

func (h *handleIO) Close() error { return windows.CloseHandle(h.handle) }

// PTY is a pseudoconsole-backed child process.
type PTY struct {
	hpc    windows.Handle
	pi     windows.ProcessInformation
	attrs  *windows.ProcThreadAttributeListContainer
	ptyIn  *handleIO
	ptyOut *handleIO
	cmdIn  *handleIO
	cmdOut *handleIO
}

var (
	supportedOnce sync.Once
	supportedOK   bool
)

func platformSupported() bool {
	supportedOnce.Do(func() {
		// The wrappers resolve through kernel32 lazy-procs; a Windows 7 host
		// without ConPTY reports a syscall-not-found error on first use, so the
		// capability probe actually creates and closes a tiny pseudoconsole.
		var hpc windows.Handle
		var probeErr error
		func() {
			in, out, cmdIn, cmdOut := openConPTYPipePair()
			if in == 0 || out == 0 {
				probeErr = errors.New("pty: probe CreatePipe failed")
				return
			}
			defer closeHandles(in, out, cmdIn, cmdOut)
			probeErr = windows.CreatePseudoConsole(windows.Coord{X: 80, Y: 40}, in, out, 0, &hpc)
		}()
		if probeErr != nil {
			return
		}
		windows.ClosePseudoConsole(hpc)
		supportedOK = true
	})
	return supportedOK
}

// openConPTYPipePair creates the two anonymous pipes ConPTY needs. It returns
// both ends of each pipe: (ptyIn, ptyOut) are the console-side ends handed to
// CreatePseudoConsole; (cmdIn, cmdOut) are the host-side read/write ends.
func openConPTYPipePair() (ptyIn, ptyOut, cmdIn, cmdOut windows.Handle) {
	if err := windows.CreatePipe(&ptyIn, &cmdIn, nil, 0); err != nil {
		return 0, 0, 0, 0
	}
	if err := windows.CreatePipe(&cmdOut, &ptyOut, nil, 0); err != nil {
		_ = windows.CloseHandle(ptyIn)
		_ = windows.CloseHandle(cmdIn)
		return 0, 0, 0, 0
	}
	return ptyIn, ptyOut, cmdIn, cmdOut
}

// Start launches executable+args attached to a fresh pseudoconsole.
func Start(executable string, args []string, opts ...Option) (*PTY, error) {
	if !platformSupported() {
		return nil, ErrUnsupported
	}
	o := resolveOptions(opts)
	cols, rows := normalizeSize(o.cols, o.rows)

	var ptyIn, cmdIn, cmdOut, ptyOut windows.Handle
	if err := windows.CreatePipe(&ptyIn, &cmdIn, nil, 0); err != nil {
		return nil, fmt.Errorf("pty: CreatePipe: %w", err)
	}
	if err := windows.CreatePipe(&cmdOut, &ptyOut, nil, 0); err != nil {
		_ = windows.CloseHandle(ptyIn)
		_ = windows.CloseHandle(cmdIn)
		return nil, fmt.Errorf("pty: CreatePipe: %w", err)
	}

	var hpc windows.Handle
	if err := windows.CreatePseudoConsole(windows.Coord{X: int16(cols), Y: int16(rows)}, ptyIn, ptyOut, 0, &hpc); err != nil {
		closeHandles(ptyIn, ptyOut, cmdIn, cmdOut)
		if errors.Is(err, windows.ERROR_PROC_NOT_FOUND) || strings.Contains(err.Error(), "not found") {
			// Windows without ConPTY: remember and degrade once.
			supportedOK = false
			return nil, ErrUnsupported
		}
		return nil, fmt.Errorf("pty: CreatePseudoConsole: %w", err)
	}
	// NOTE: keep ptyIn/ptyOut open for the PTY's lifetime (matching Microsoft's
	// ConPTY sample). Closing the console-side ends early breaks ConPTY's
	// internal read/write loops — the child produces no output at all.

	attrs, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		windows.ClosePseudoConsole(hpc)
		closeHandles(cmdIn, cmdOut)
		return nil, fmt.Errorf("pty: NewProcThreadAttributeList: %w", err)
	}
	// Attribute payload: the pseudoconsole handle *value* itself.
	//
	// Empirically (Windows 11 26200, x/sys v0.48.0), only the handle-as-value
	// spelling starts the child: passing &local makes cmd.exe die with
	// STATUS_DLL_INIT_FAILED (0xC0000142). This matches go-pty's reference
	// (unsafe.Pointer(p.handle)). UpdateProcThreadAttribute copies the payload
	// bytes at call time and never dereferences lpValue afterwards; the handle
	// stays alive for the PTY's lifetime in p.hpc.
	//
	// KNOWN vet exception: `go vet ./internal/pty` prints one
	// "possible misuse of unsafe.Pointer" for this line. The conversion is the
	// API-required spelling (the attribute value is an arbitrary 8-byte
	// payload, not a Go pointer) — the same pattern ships inside x/sys and
	// go-pty. Do not "fix" it to &local: that breaks ConPTY startup.
	if err := attrs.Update(windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE,
		unsafe.Pointer(hpc), unsafe.Sizeof(hpc)); err != nil {
		attrs.Delete()
		windows.ClosePseudoConsole(hpc)
		closeHandles(cmdIn, cmdOut)
		return nil, fmt.Errorf("pty: attribute update: %w", err)
	}

	siEx := &windows.StartupInfoEx{}
	siEx.Cb = uint32(unsafe.Sizeof(*siEx))
	// STARTF_USESTDHANDLES mirrors go-pty's reference: the pseudoconsole
	// attribute binds conhost; the flag keeps stdio wiring deterministic.
	siEx.Flags = windows.STARTF_USESTDHANDLES
	// Bind the attribute list into the extended startup info. StartupInfoEx in
	// x/sys declares the list as a *ProcThreadAttributeList (a pointer-sized
	// field); the container's List() hands out the raw data pointer the API
	// expects at that offset.
	siEx.ProcThreadAttributeList = attrs.List()

	// A console program cannot start with an empty environment (cmd.exe fails
	// with STATUS_DLL_NOT_FOUND / 0xC0000142 without SystemRoot etc.), so the
	// child gets a proper unicode block: the WithEnv slice when provided,
	// otherwise the parent environment (matches the Linux implementation's
	// nil-env inheritance).
	childEnv := o.env
	if len(childEnv) == 0 {
		childEnv = os.Environ()
	}
	envBlock := createEnvBlock(childEnv)

	cmdLine, err := windows.UTF16PtrFromString(composeCommandLine(executable, args))
	if err != nil {
		attrs.Delete()
		windows.ClosePseudoConsole(hpc)
		closeHandles(cmdIn, cmdOut)
		return nil, fmt.Errorf("pty: command line: %w", err)
	}
	var currentDirectory *uint16
	if workDir := strings.TrimSpace(o.workDir); workDir != "" {
		currentDirectory, err = windows.UTF16PtrFromString(workDir)
		if err != nil {
			attrs.Delete()
			windows.ClosePseudoConsole(hpc)
			closeHandles(cmdIn, cmdOut)
			return nil, fmt.Errorf("pty: workdir: %w", err)
		}
	}

	var pi windows.ProcessInformation
	err = windows.CreateProcess(
		nil, cmdLine, nil, nil, false,
		windows.EXTENDED_STARTUPINFO_PRESENT|windows.CREATE_UNICODE_ENVIRONMENT,
		envBlock, currentDirectory,
		&siEx.StartupInfo, &pi,
	)
	if err != nil {
		attrs.Delete()
		windows.ClosePseudoConsole(hpc)
		closeHandles(cmdIn, cmdOut)
		return nil, fmt.Errorf("pty: CreateProcess: %w", err)
	}

	return &PTY{
		hpc:    hpc,
		pi:     pi,
		attrs:  attrs,
		ptyIn:  &handleIO{ptyIn},
		ptyOut: &handleIO{ptyOut},
		cmdIn:  &handleIO{cmdIn},
		cmdOut: &handleIO{cmdOut},
	}, nil
}

func (p *PTY) Read(b []byte) (int, error)  { return p.cmdOut.Read(b) }
func (p *PTY) Write(b []byte) (int, error) { return p.cmdIn.Write(b) }
func (p *PTY) Pid() int                    { return int(p.pi.ProcessId) }

// Kill force-terminates the attached child (cleanup path for hung tests). It
// does not wait for exit; callers follow with Wait/Close.
func (p *PTY) Kill() error {
	if p == nil || p.pi.Process == 0 {
		return fmt.Errorf("pty: nil")
	}
	return windows.TerminateProcess(p.pi.Process, 1)
}

// Resize changes the pseudoconsole dimensions.
func (p *PTY) Resize(cols, rows int) error {
	if p == nil || p.hpc == 0 {
		return fmt.Errorf("pty: nil")
	}
	cols, rows = normalizeSize(cols, rows)
	if err := windows.ResizePseudoConsole(p.hpc, windows.Coord{X: int16(cols), Y: int16(rows)}); err != nil {
		return fmt.Errorf("pty: ResizePseudoConsole: %w", err)
	}
	return nil
}

// Wait blocks until the child exits or ctx is cancelled, returning the exit code.
//
// NOTE: poll loop instead of WaitForSingleObject. The test binary build
// (-race, cgo) observed immediate spurious wakeups from WaitForSingleObject
// on the child process handle, reporting exit code 0 while the child was
// still running (2026-10-11, interactive E2E driver). GetExitCodeProcess
// polling is semantically equivalent for a non-0-terminated contract and
// immune to that failure mode.
func (p *PTY) Wait(ctx context.Context) (int, error) {
	if p == nil || p.pi.Process == 0 {
		return -1, fmt.Errorf("pty: nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	const stillActive = 259 // windows.STILL_ACTIVE
	tick := time.NewTimer(20 * time.Millisecond)
	defer tick.Stop()
	for {
		var exitCode uint32
		if err := windows.GetExitCodeProcess(p.pi.Process, &exitCode); err != nil {
			return -1, err
		}
		if exitCode != stillActive {
			return int(exitCode), nil
		}
		select {
		case <-ctx.Done():
			return -1, ctx.Err()
		case <-tick.C:
			tick.Reset(20 * time.Millisecond)
		}
	}
}

func (p *PTY) Close() error {
	if p == nil {
		return nil
	}
	if p.hpc != 0 {
		windows.ClosePseudoConsole(p.hpc)
		p.hpc = 0
	}
	if p.attrs != nil {
		p.attrs.Delete()
		p.attrs = nil
	}
	var firstErr error
	for _, handle := range []windows.Handle{p.pi.Process, p.pi.Thread} {
		if handle != 0 {
			if err := windows.CloseHandle(handle); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	closeErr := closeHandles(
		p.ptyIn.handle, p.ptyOut.handle,
		p.cmdIn.handle, p.cmdOut.handle,
	)
	if closeErr != nil && firstErr == nil {
		firstErr = closeErr
	}
	return firstErr
}

func closeHandles(handles ...windows.Handle) error {
	var firstErr error
	for _, h := range handles {
		if h == 0 || h == windows.InvalidHandle {
			continue
		}
		if err := windows.CloseHandle(h); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// osCommandOutput runs a helper command hidden (lifecycle diagnostics/tests).
func osCommandOutput(name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd.Output()
}

// StartPipeForBench starts the same command through a plain pipe pair (no
// pseudoconsole) so benchmarks can compare pipe vs ConPTY throughput.
// Diagnostics-only entry point; the interactive manager always picks via
// Supported().
func StartPipeForBench(executable string, args []string) (*PipeProc, error) {
	cmd := exec.Command(executable, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &PipeProc{cmd: cmd, stdout: stdout}, nil
}

// PipeProc is a minimal pipe-backed child used only by benchmarks/diagnostics.
type PipeProc struct {
	cmd    *exec.Cmd
	stdout io.ReadCloser
}

func (p *PipeProc) Read(b []byte) (int, error) { return p.stdout.Read(b) }

func (p *PipeProc) Close() error {
	if p == nil {
		return nil
	}
	_ = p.stdout.Close()
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
	return p.cmd.Wait()
}

var (
	_ = bytes.MinRead // keep bytes import for future drain helpers
	_ = context.Background
)

// createEnvBlock converts environment strings into the NUL-delimited UTF-16
// block CreateProcess expects (double NUL terminated). Mirrors go-pty/std.
func createEnvBlock(env []string) *uint16 {
	if len(env) == 0 {
		return &utf16.Encode([]rune("\x00\x00"))[0]
	}
	length := 0
	for _, entry := range env {
		length += len(entry) + 1
	}
	length++

	b := make([]byte, length)
	i := 0
	for _, entry := range env {
		l := len(entry)
		copy(b[i:i+l], []byte(entry))
		copy(b[i+l:i+l+1], []byte{0})
		i = i + l + 1
	}
	copy(b[i:i+1], []byte{0})

	// Each source byte becomes one UTF-16LE unit (bytes < 0x100), matching
	// go-pty/std's block encoding consumed by CreateProcessW under
	// CREATE_UNICODE_ENVIRONMENT.
	return &utf16.Encode([]rune(string(b)))[0]
}

// composeCommandLine builds a Windows command line from an executable and its
// arguments using the standard quoting rules.
func composeCommandLine(executable string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, quoteWindowsArg(executable))
	for _, arg := range args {
		parts = append(parts, quoteWindowsArg(arg))
	}
	return strings.Join(parts, " ")
}

func quoteWindowsArg(arg string) string {
	if arg == "" {
		return `""`
	}
	if !strings.ContainsAny(arg, " \t\n\v\"") {
		return arg
	}
	var b strings.Builder
	b.WriteByte('"')
	backslashes := 0
	for _, r := range arg {
		switch r {
		case '\\':
			backslashes++
		case '"':
			b.WriteString(strings.Repeat("\\", backslashes*2+1))
			b.WriteByte('"')
			backslashes = 0
		default:
			if backslashes > 0 {
				b.WriteString(strings.Repeat("\\", backslashes))
				backslashes = 0
			}
			b.WriteRune(r)
		}
	}
	if backslashes > 0 {
		b.WriteString(strings.Repeat("\\", backslashes*2))
	}
	b.WriteByte('"')
	return b.String()
}
