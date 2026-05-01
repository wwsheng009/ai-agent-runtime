package ui

import (
	"os"

	"golang.org/x/term"
)

// TerminalCapabilities records the terminal features that are safe to use.
// The values are intentionally conservative; callers must keep a plain-output
// fallback for non-TTY, dumb terminals, and legacy Windows consoles.
type TerminalCapabilities struct {
	Interactive     bool
	ANSI            bool
	ScrollRegion    bool
	BracketedPaste  bool
	TerminalTitle   bool
	VTProcessing    bool
	Width           int
	Height          int
	TerminalName    string
	MultiplexerName string
}

// TerminalDriver owns low-level terminal capability detection for aicli UI.
type TerminalDriver struct {
	stdin  *os.File
	stdout *os.File
	caps   TerminalCapabilities
}

func NewTerminalDriver(stdin, stdout *os.File) *TerminalDriver {
	d := &TerminalDriver{
		stdin:  stdin,
		stdout: stdout,
	}
	d.RefreshCapabilities()
	return d
}

func (d *TerminalDriver) RefreshCapabilities() TerminalCapabilities {
	if d == nil {
		return TerminalCapabilities{Width: 80, Height: 24}
	}
	stdinFD, stdoutFD := -1, -1
	if d.stdin != nil {
		stdinFD = int(d.stdin.Fd())
	}
	if d.stdout != nil {
		stdoutFD = int(d.stdout.Fd())
	}

	width, height := 80, 24
	if stdoutFD >= 0 {
		if w, h, err := term.GetSize(stdoutFD); err == nil && w > 0 && h > 0 {
			width, height = w, h
		}
	}

	interactive := stdinFD >= 0 && stdoutFD >= 0 && term.IsTerminal(stdinFD) && term.IsTerminal(stdoutFD)
	ansi := interactive && platformTerminalSupportsANSI(d.stdout)
	vt := false
	if ansi {
		vt = platformEnableVirtualTerminalProcessing(d.stdout)
		ansi = ansi && vt
	}

	d.caps = TerminalCapabilities{
		Interactive:     interactive,
		ANSI:            ansi,
		ScrollRegion:    ansi,
		BracketedPaste:  ansi,
		TerminalTitle:   ansi,
		VTProcessing:    vt,
		Width:           width,
		Height:          height,
		TerminalName:    firstNonEmptyEnv("WT_SESSION", "TERM_PROGRAM", "TERM"),
		MultiplexerName: firstNonEmptyEnv("ZELLIJ", "TMUX"),
	}
	return d.caps
}

func (d *TerminalDriver) Capabilities() TerminalCapabilities {
	if d == nil {
		return TerminalCapabilities{Width: 80, Height: 24}
	}
	return d.caps
}

func (d *TerminalDriver) Size() (width, height int, err error) {
	if d == nil || d.stdout == nil {
		return 80, 24, nil
	}
	width, height, err = term.GetSize(int(d.stdout.Fd()))
	if err != nil || width <= 0 || height <= 0 {
		caps := d.Capabilities()
		if caps.Width <= 0 {
			caps.Width = 80
		}
		if caps.Height <= 0 {
			caps.Height = 24
		}
		return caps.Width, caps.Height, err
	}
	return width, height, nil
}

func (d *TerminalDriver) IsInteractive() bool {
	return d != nil && d.caps.Interactive
}

func (d *TerminalDriver) SupportsANSI() bool {
	return d != nil && d.caps.ANSI
}

func firstNonEmptyEnv(names ...string) string {
	for _, name := range names {
		if value := os.Getenv(name); value != "" {
			return value
		}
	}
	return ""
}
