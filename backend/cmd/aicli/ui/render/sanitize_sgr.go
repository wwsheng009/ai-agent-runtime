package render

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// SanitizeKeepSGR removes dangerous terminal controls while preserving:
//   - SGR color/style sequences (CSI ... m)
//   - OSC 8 hyperlinks
//   - printable text, newline, tab
//
// Cursor movement, erase, title, clipboard, alt-screen and other CSI/OSC are dropped.
// Use this for trusted app-rendered ANSI (markdown/syntax backends), not raw tool output.
func SanitizeKeepSGR(input string) string {
	if input == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(input))
	keptSGR := false
	linkOpen := false
	i := 0
	for i < len(input) {
		if input[i] != '\x1b' {
			r, size := utf8.DecodeRuneInString(input[i:])
			if r == utf8.RuneError && size == 1 {
				i++
				continue
			}
			switch {
			case r == '\n' || r == '\r' || r == '\t':
				if r == '\r' {
					b.WriteByte('\n')
					if i+size < len(input) && input[i+size] == '\n' {
						size++
					}
				} else {
					b.WriteRune(r)
				}
			case r < 32 || r == 127:
				// drop other C0
			case r >= 0x80 && r <= 0x9f:
				// drop C1
			default:
				b.WriteRune(r)
			}
			i += size
			continue
		}

		rest := input[i:]
		if len(rest) >= 2 && rest[1] == '[' {
			n, _, final := parseCSI(rest)
			if final == 'm' && safeSGRBytes(rest[:n]) {
				b.WriteString(rest[:n])
				keptSGR = true
			}
			i += n
			if n <= 0 {
				i++
			}
			continue
		}
		if len(rest) >= 2 && rest[1] == ']' {
			// OSC: keep only OSC 8 hyperlinks
			n, keep, opens := consumeOSCKeepHyperlink(rest)
			if keep {
				b.WriteString(rest[:n])
				linkOpen = opens
			}
			i += n
			if n <= 0 {
				i++
			}
			continue
		}
		// Drop other ESC sequences.
		if len(rest) >= 2 && (rest[1] == 'P' || rest[1] == 'X' || rest[1] == '^' || rest[1] == '_') {
			i += consumeStringSeq(rest)
			continue
		}
		i++
	}
	if linkOpen {
		b.WriteString("\x1b]8;;\x1b\\")
	}
	if keptSGR {
		b.WriteString("\x1b[0m")
	}
	return b.String()
}

func consumeOSCKeepHyperlink(s string) (consumed int, keep bool, opens bool) {
	if len(s) < 2 || s[0] != '\x1b' || s[1] != ']' {
		return 1, false, false
	}
	n := consumeOSC(s)
	payload, terminated := oscPayload(s, n)
	if !terminated || !strings.HasPrefix(payload, "8;") {
		return n, false, false
	}
	rest := strings.TrimPrefix(payload, "8;")
	separator := strings.IndexByte(rest, ';')
	if separator < 0 {
		return n, false, false
	}
	params, target := rest[:separator], rest[separator+1:]
	if strings.IndexFunc(params, func(r rune) bool { return unicode.IsControl(r) }) >= 0 {
		return n, false, false
	}
	if target != "" && !safeHyperlink(target) {
		return n, false, false
	}
	return n, true, target != ""
}

func safeSGRBytes(sequence string) bool {
	if len(sequence) < 3 || !strings.HasPrefix(sequence, "\x1b[") || sequence[len(sequence)-1] != 'm' {
		return false
	}
	for _, c := range sequence[2 : len(sequence)-1] {
		if (c < '0' || c > '9') && c != ';' {
			return false
		}
	}
	return true
}

func oscPayload(sequence string, consumed int) (string, bool) {
	if consumed < 3 || consumed > len(sequence) {
		return "", false
	}
	end := consumed
	if sequence[end-1] == '\x07' {
		end--
	} else if end >= 2 && sequence[end-2] == '\x1b' && sequence[end-1] == '\\' {
		end -= 2
	} else {
		return "", false
	}
	return sequence[2:end], true
}
