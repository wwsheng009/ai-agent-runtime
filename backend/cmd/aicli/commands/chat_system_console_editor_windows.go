//go:build windows

package commands

import (
	"context"
	"io"
	"os"
	"strings"
	"unicode/utf16"

	"golang.org/x/sys/windows"
)

const (
	systemConsoleReadBufferUnits   = 1024
	enableVirtualTerminalInputMode = 0x0200
)

var readSystemConsoleFn = windows.ReadConsole

// readSystemConsoleLine reads one Unicode line through the native cooked
// console path. Unlike the custom ReadConsoleInputW editor, ReadConsoleW keeps
// conhost in charge of IME composition, candidate selection and committed text.
// This is the preferred compatibility path for Windows 7.
func readSystemConsoleLine(ctx context.Context) (line string, ok bool, err error) {
	in, err := stdinConsoleHandle()
	if err != nil {
		return "", false, nil
	}
	out := windows.Handle(os.Stdout.Fd())
	var outMode uint32
	if err := windows.GetConsoleMode(out, &outMode); err != nil {
		return "", false, nil
	}

	var originalMode uint32
	if err := windows.GetConsoleMode(in, &originalMode); err != nil {
		return "", false, nil
	}
	cookedMode := originalMode |
		windows.ENABLE_PROCESSED_INPUT |
		windows.ENABLE_LINE_INPUT |
		windows.ENABLE_ECHO_INPUT
	// ReadConsoleW consumes Unicode characters, not VT byte sequences.
	cookedMode &^= enableVirtualTerminalInputMode
	if err := windows.SetConsoleMode(in, cookedMode); err != nil {
		return "", false, nil
	}
	defer windows.SetConsoleMode(in, originalMode)

	if chatDebugFlagEnabled() {
		legacyConsoleDebugln("[aicli-diag] system Unicode console line editor active (ReadConsoleW, IME enabled)")
	}
	line, err = readSystemConsoleUTF16Line(ctx, in)
	return line, true, err
}

func readSystemConsoleUTF16Line(ctx context.Context, in windows.Handle) (string, error) {
	units := make([]uint16, 0, systemConsoleReadBufferUnits)
	buffer := make([]uint16, systemConsoleReadBufferUnits)
	for {
		if ctx != nil {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			default:
			}
		}

		var read uint32
		err := readSystemConsoleFn(
			in,
			&buffer[0],
			uint32(len(buffer)),
			&read,
			nil,
		)
		if err != nil {
			if ctx != nil {
				select {
				case <-ctx.Done():
					return "", ctx.Err()
				default:
				}
			}
			return "", err
		}
		if read == 0 {
			return "", io.EOF
		}
		units = append(units, buffer[:read]...)
		if systemConsoleUnitsContainLineEnding(buffer[:read]) {
			return decodeSystemConsoleLine(units), nil
		}
	}
}

func systemConsoleUnitsContainLineEnding(units []uint16) bool {
	for _, unit := range units {
		if unit == '\r' || unit == '\n' {
			return true
		}
	}
	return false
}

func decodeSystemConsoleLine(units []uint16) string {
	text := string(utf16.Decode(units))
	text = strings.TrimSuffix(text, "\n")
	text = strings.TrimSuffix(text, "\r")
	return text
}
