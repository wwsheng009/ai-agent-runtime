package ui

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

func TestFixedBottomSurface_TerminalProfileMatrix(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	cases := []struct {
		name       string
		caps       TerminalCapabilities
		wantEnable bool
	}{
		{
			name: "WindowsTerminal",
			caps: TerminalCapabilities{
				Interactive:    true,
				ANSI:           true,
				ScrollRegion:   true,
				BracketedPaste: true,
				TerminalTitle:  true,
				VTProcessing:   true,
				Width:          120,
				Height:         30,
				TerminalName:   "Windows Terminal",
			},
			wantEnable: true,
		},
		{
			name: "PowerShell",
			caps: TerminalCapabilities{
				Interactive:    true,
				ANSI:           true,
				ScrollRegion:   true,
				BracketedPaste: true,
				TerminalTitle:  true,
				VTProcessing:   true,
				Width:          100,
				Height:         28,
				TerminalName:   "PowerShell",
			},
			wantEnable: true,
		},
		{
			name: "WSLUbuntu",
			caps: TerminalCapabilities{
				Interactive:    true,
				ANSI:           true,
				ScrollRegion:   true,
				BracketedPaste: true,
				TerminalTitle:  true,
				VTProcessing:   true,
				Width:          110,
				Height:         32,
				TerminalName:   "Ubuntu",
			},
			wantEnable: true,
		},
		{
			name: "LinuxTerminal",
			caps: TerminalCapabilities{
				Interactive:    true,
				ANSI:           true,
				ScrollRegion:   true,
				BracketedPaste: true,
				TerminalTitle:  true,
				VTProcessing:   true,
				Width:          100,
				Height:         24,
				TerminalName:   "xterm-256color",
			},
			wantEnable: true,
		},
		{
			name: "VSCodeTerminal",
			caps: TerminalCapabilities{
				Interactive:    true,
				ANSI:           true,
				ScrollRegion:   true,
				BracketedPaste: true,
				TerminalTitle:  true,
				VTProcessing:   true,
				Width:          96,
				Height:         26,
				TerminalName:   "vscode",
			},
			wantEnable: true,
		},
		{
			name: "LegacyConsole",
			caps: TerminalCapabilities{
				Interactive:  true,
				ANSI:         false,
				ScrollRegion: false,
				Width:        80,
				Height:       24,
				TerminalName: "conhost",
			},
			wantEnable: false,
		},
		{
			name: "Zellij",
			caps: TerminalCapabilities{
				Interactive:     true,
				ANSI:            true,
				ScrollRegion:    true,
				BracketedPaste:  true,
				TerminalTitle:   true,
				VTProcessing:    true,
				Width:           120,
				Height:          30,
				TerminalName:    "wezterm",
				MultiplexerName: "zellij",
			},
			wantEnable: false,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			surface := newProfileFixedBottomSurface(tc.caps)
			gotEnable := surface.Enable()
			if gotEnable != tc.wantEnable {
				t.Fatalf("Enable() = %v, want %v for caps %+v", gotEnable, tc.wantEnable, tc.caps)
			}
			if !tc.wantEnable {
				return
			}

			output := captureUIStdout(t, func() {
				surface.SetStatusModel(style.StatusLineModel{
					State:     style.RunReady,
					Separator: " | ",
					Segments:  []style.StatusSegment{{Text: tc.name}},
				})
				surface.SetComposerPreview("draft: /model")
			})

			if surface.popupRenderedRows != 1 {
				t.Fatalf("expected one composer row for %s, got %d", tc.name, surface.popupRenderedRows)
			}
			if surface.bottomRowsLocked() != 2 {
				t.Fatalf("expected status + composer bottom rows for %s, got %d", tc.name, surface.bottomRowsLocked())
			}
			frame := frameDump(surface.ComposedFrameForTest())
			if !strings.Contains(frame, "draft: /model") {
				t.Fatalf("expected composer preview in frame for %s, got\n%s\noutput=%q", tc.name, frame, output)
			}
			if !strings.Contains(frame, "Ready | "+tc.name) {
				t.Fatalf("expected status line in frame for %s, got\n%s\noutput=%q", tc.name, frame, output)
			}
		})
	}
}

func newProfileFixedBottomSurface(caps TerminalCapabilities) *FixedBottomSurface {
	width := caps.Width
	if width <= 0 {
		width = 80
	}
	height := caps.Height
	if height <= 0 {
		height = 24
	}
	term := &Terminal{
		width:  width,
		height: height,
		theme:  GetTheme(ThemeAuto),
		driver: &TerminalDriver{caps: caps},
	}
	surface := NewFixedBottomSurface(term)
	surface.enabled = false
	return surface
}
