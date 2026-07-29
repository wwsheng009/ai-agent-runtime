package render

import (
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxHyperlinkBytes = 4096

// sanitizeSpanText enforces the structured-render boundary. It removes
// terminal controls and flattens accidental line breaks because layout owns
// line boundaries, not Span.Text.
func sanitizeSpanText(text string) string {
	if text == "" {
		return ""
	}
	needsSanitizing := !utf8.ValidString(text)
	if !needsSanitizing {
		for _, r := range text {
			if unicode.IsControl(r) {
				needsSanitizing = true
				break
			}
		}
	}
	if !needsSanitizing {
		return text
	}
	text = ANSIToPlain(text)
	text = strings.ReplaceAll(text, "\n", " ")
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\t':
			return ' '
		case unicode.IsControl(r):
			return -1
		default:
			return r
		}
	}, text)
}

// safeHyperlink validates the OSC 8 payload before it reaches a terminal.
// Relative links and common web/file schemes are supported; all control
// characters, whitespace padding and unusual schemes are rejected.
func safeHyperlink(link string) bool {
	if link == "" || len(link) > maxHyperlinkBytes || !utf8.ValidString(link) {
		return false
	}
	if strings.TrimSpace(link) != link {
		return false
	}
	for _, r := range link {
		if unicode.IsControl(r) || (r >= 0x80 && r <= 0x9f) {
			return false
		}
	}
	parsed, err := url.Parse(link)
	if err != nil {
		return false
	}
	switch strings.ToLower(parsed.Scheme) {
	case "", "http", "https", "mailto", "file":
		return true
	default:
		return false
	}
}
