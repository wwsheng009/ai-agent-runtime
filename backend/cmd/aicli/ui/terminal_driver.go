package ui

import (
	"os"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
	"golang.org/x/term"
)

// TerminalCapabilities records the terminal features that are safe to use.
// The values are intentionally conservative; callers must keep a plain-output
// fallback for non-TTY, dumb terminals, and legacy Windows consoles.
type TerminalCapabilities struct {
	Interactive    bool
	ANSI           bool
	ScrollRegion   bool
	BracketedPaste bool
	TerminalTitle  bool
	VTProcessing   bool
	// SynchronizedOutput reports whether the terminal is expected to honor the
	// DEC 2026 synchronized-update mode (\x1b[?2026h / \x1b[?2026l). Codex wraps
	// every frame in this mode so a multi-step repaint applies atomically; we use
	// it the same way to remove tearing/flicker in the fixed-bottom surface.
	SynchronizedOutput bool
	Width              int
	Height             int
	TerminalName       string
	MultiplexerName    string
	// ColorEnabled / ColorDepth mirror style.ColorProfile for surface decisions.
	// Full profile resolution (hyperlinks, background) lives in style.DetectColorProfile.
	ColorEnabled bool
	ColorDepth   int // 0=none, 1=ansi16, 2=ansi256, 3=truecolor
}

// TerminalDriver owns low-level terminal capability detection for aicli UI.
type TerminalDriver struct {
	stdin  *os.File
	stdout *os.File
	caps   TerminalCapabilities
}

// EnsureConsoleUTF8Output 在任何输出发生前调用一次，确保无 VT 的 Windows
// 控制台（如 Win7 conhost）按 UTF-8 解码程序输出，避免中文显示为乱码。
// 支持 VT 的控制台、管道/文件重定向、非 Windows 平台均为空操作。
// 返回值：仅在确实切换过代码页时非 nil；调用者应 defer 它在进程正常
// 退出时恢复原代码页，避免污染同一 console 上后续命令的显示。
func EnsureConsoleUTF8Output() (restore func()) {
	return platformEnsureConsoleUTF8Output(os.Stdout)
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

	detectOpts := style.DetectOptions{
		Interactive:   interactive,
		ANSICapable:   ansi,
		ColorOverride: "auto",
		DepthOverride: "auto",
	}
	// Capability refresh only needs depth/enabled; live OSC is deferred to
	// ColorProfile() so stdin is not queried on every size refresh.
	profile := style.DetectColorProfile(detectOpts)

	d.caps = TerminalCapabilities{
		Interactive:    interactive,
		ANSI:           ansi,
		ScrollRegion:   ansi,
		BracketedPaste: ansi,
		TerminalTitle:  ansi,
		VTProcessing:   vt,
		// Synchronized output needs both ANSI parsing and VT processing enabled.
		// Unsupported terminals silently ignore the unknown DEC private mode, so
		// this stays safe; a hard opt-out lives in the write-lock env kill switch.
		SynchronizedOutput: ansi && vt,
		Width:              width,
		Height:             height,
		TerminalName:       firstNonEmptyEnv("WT_SESSION", "TERM_PROGRAM", "TERM"),
		MultiplexerName:    firstNonEmptyEnv("ZELLIJ", "TMUX"),
		ColorEnabled:       profile.Enabled,
		ColorDepth:         int(profile.Depth),
	}
	return d.caps
}

// ColorProfile returns the structured color profile for this driver.
// On interactive ANSI terminals, attaches a process-once bounded OSC 10/11
// probe so DefaultFG/BG and background luminance can be resolved when env
// offline defaults are absent.
func (d *TerminalDriver) ColorProfile() style.ColorProfile {
	caps := d.Capabilities()
	opts := style.DetectOptions{
		Interactive:   caps.Interactive,
		ANSICapable:   caps.ANSI,
		ColorOverride: "auto",
		DepthOverride: "auto",
	}
	if caps.Interactive && caps.ANSI {
		opts.OSCProbe = LiveOSCProbe()
	}
	return style.DetectColorProfile(opts)
}

// ColorDepthName returns a stable label for diagnostics.
func ColorDepthName(depth int) string {
	switch render.ColorDepth(depth) {
	case render.ColorTrueColor:
		return "truecolor"
	case render.ColorANSI256:
		return "ansi256"
	case render.ColorANSI16:
		return "ansi16"
	default:
		return "none"
	}
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
