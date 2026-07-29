package render

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// ANSIToLines parses untrusted terminal output into visual lines and keeps
// only safe SGR styles. Cursor movement, erase, OSC, DCS, APC, PM, BEL and
// unknown sequences are dropped. Newlines are represented only by Line
// boundaries, never by Span.Text.
func ANSIToLines(input string) []Line {
	if input == "" {
		return nil
	}
	var lines []Line
	var line Line
	var text strings.Builder
	style := Style{}

	flushText := func() {
		if text.Len() == 0 {
			return
		}
		line.Spans = append(line.Spans, Span{Text: text.String(), Style: style})
		text.Reset()
	}
	flushLine := func() {
		flushText()
		if len(line.Spans) == 0 {
			line.Spans = []Span{{Text: "", Style: style}}
		}
		lines = append(lines, line)
		line = Line{}
	}

	i := 0
	for i < len(input) {
		if input[i] != '\x1b' {
			r, size := utf8.DecodeRuneInString(input[i:])
			if r == utf8.RuneError && size == 1 {
				i++
				continue
			}
			// Drop C0/C1 controls except tab/newline handled by caller layout.
			switch {
			case r == '\t':
				text.WriteString("    ")
			case r == '\n':
				flushLine()
			case r == '\r':
				flushLine()
				if i+size < len(input) && input[i+size] == '\n' {
					size++
				}
			case r < 32 || r == 127:
				// drop
			case r >= 0x80 && r <= 0x9f:
				// drop C1
			default:
				text.WriteRune(r)
			}
			i += size
			continue
		}

		// ESC sequence
		rest := input[i:]
		if len(rest) >= 2 && rest[1] == '[' {
			// CSI
			n, params, final := parseCSI(rest)
			i += n
			if final == 'm' {
				flushText()
				style = applySGR(style, params)
			}
			// All other CSI finals (cursor, erase, mode) are discarded.
			continue
		}
		if len(rest) >= 2 && rest[1] == ']' {
			// OSC: drop until BEL or ST
			i += consumeOSC(rest)
			continue
		}
		if len(rest) >= 2 && (rest[1] == 'P' || rest[1] == 'X' || rest[1] == '^' || rest[1] == '_') {
			// DCS/SOS/PM/APC: drop until ST
			i += consumeStringSeq(rest)
			continue
		}
		// Lone ESC or unknown — drop one byte.
		i++
	}
	flushLine()
	return lines
}

// ANSIToSpans is the single-line convenience projection. Multi-line input is
// flattened with spaces; use ANSIToLines when line boundaries matter.
func ANSIToSpans(input string) []Span {
	lines := ANSIToLines(input)
	if len(lines) == 0 {
		return nil
	}
	var spans []Span
	for i, line := range lines {
		if i > 0 {
			spans = append(spans, Span{Text: " "})
		}
		spans = append(spans, line.Spans...)
	}
	return spans
}

// ANSIToPlain strips all escape sequences and returns visible text only.
func ANSIToPlain(input string) string {
	lines := ANSIToLines(input)
	var b strings.Builder
	for i, line := range lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		for _, span := range line.Spans {
			b.WriteString(span.Text)
		}
	}
	return b.String()
}

func parseCSI(s string) (consumed int, params []int, final byte) {
	// ESC [
	if len(s) < 2 || s[0] != '\x1b' || s[1] != '[' {
		return 1, nil, 0
	}
	i := 2
	// Skip optional private marker ? > =
	if i < len(s) && (s[i] == '?' || s[i] == '>' || s[i] == '=' || s[i] == '!') {
		i++
	}
	start := i
	for i < len(s) {
		c := s[i]
		if c >= 0x40 && c <= 0x7e {
			// Final byte
			paramStr := s[start:i]
			params = parseSGRParams(paramStr)
			return i + 1, params, c
		}
		// Parameter / intermediate bytes
		if c < 0x20 || c > 0x3f {
			// invalid — abort
			return i + 1, nil, 0
		}
		i++
		if i-start > 64 {
			return i, nil, 0
		}
	}
	return len(s), nil, 0
}

func parseSGRParams(s string) []int {
	if s == "" {
		return []int{0}
	}
	parts := strings.Split(s, ";")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			out = append(out, 0)
			continue
		}
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || n > 255 {
			continue
		}
		out = append(out, n)
	}
	if len(out) == 0 {
		return []int{0}
	}
	return out
}

func applySGR(style Style, params []int) Style {
	if len(params) == 0 {
		return Style{}
	}
	i := 0
	for i < len(params) {
		p := params[i]
		switch p {
		case 0:
			style = Style{}
		case 1:
			style.Bold = true
		case 2:
			style.Dim = true
		case 3:
			style.Italic = true
		case 4:
			style.Underline = true
		case 7:
			style.Reverse = true
		case 22:
			style.Bold = false
			style.Dim = false
		case 23:
			style.Italic = false
		case 24:
			style.Underline = false
		case 27:
			style.Reverse = false
		case 39:
			style.Foreground = DefaultColor()
		case 49:
			style.Background = DefaultColor()
		default:
			if p >= 30 && p <= 37 {
				style.Foreground = ANSI(uint8(p - 30))
			} else if p >= 90 && p <= 97 {
				style.Foreground = ANSI(uint8(p - 90 + 8))
			} else if p >= 40 && p <= 47 {
				style.Background = ANSI(uint8(p - 40))
			} else if p >= 100 && p <= 107 {
				style.Background = ANSI(uint8(p - 100 + 8))
			} else if p == 38 || p == 48 {
				color, consumed := parseExtendedColor(params[i:])
				if p == 38 {
					style.Foreground = color
				} else {
					style.Background = color
				}
				if consumed > 1 {
					i += consumed - 1
				}
			}
		}
		i++
	}
	return style
}

func parseExtendedColor(params []int) (Color, int) {
	// 38;5;N or 38;2;R;G;B (same for 48)
	if len(params) < 3 {
		return DefaultColor(), 1
	}
	switch params[1] {
	case 5:
		return Indexed(uint8(params[2])), 3
	case 2:
		if len(params) < 5 {
			return DefaultColor(), 1
		}
		return RGB(uint8(params[2]), uint8(params[3]), uint8(params[4])), 5
	default:
		return DefaultColor(), 1
	}
}

func consumeOSC(s string) int {
	// ESC ] ... (BEL | ESC \)
	if len(s) < 2 {
		return len(s)
	}
	i := 2
	for i < len(s) {
		if s[i] == '\x07' { // BEL
			return i + 1
		}
		if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '\\' {
			return i + 2
		}
		i++
	}
	return len(s)
}

func consumeStringSeq(s string) int {
	// ESC P/X/^/_ ... ST (ESC \)
	if len(s) < 2 {
		return len(s)
	}
	i := 2
	for i < len(s) {
		if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '\\' {
			return i + 2
		}
		if s[i] == '\x07' {
			return i + 1
		}
		i++
	}
	return len(s)
}
