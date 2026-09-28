package policy

import (
	"runtime"
	"strings"
)

// This file narrows the blanket redirection rejection in the shell fast-path
// classifiers: a redirection whose target is the platform null device is the
// one redirection class that provably has no observable side effect, so
// read-only/accept-edits callers may keep the command on the fast path.
//
// Everything else about redirection stays out of the fast path: `>>`, `>&`,
// `<`, any other target, variable expansion ($/%), backticks, and quoted or
// escaped spellings still hit the character pre-scan in
// AssessShellReadOnlyCommand / AssessShellSafeFileCommand and are rejected.
//
// The platform spellings are not interchangeable: on POSIX "NUL" is an
// ordinary file name, while on Windows "/dev/null" is an ordinary path a shell
// would create. The allowance therefore follows the OS that will execute the
// command, and only the native spelling is accepted.

const (
	posixNullDevice   = "/dev/null"
	windowsNullDevice = "nul"
)

// stripPlatformNullDeviceRedirections applies the null-device allowance for the
// platform that runs the command.
func stripPlatformNullDeviceRedirections(command string) string {
	return stripNullDeviceRedirections(command, runtime.GOOS)
}

// stripNullDeviceRedirections removes unquoted redirections to the platform
// null device from command and returns the remainder. The goos parameter is
// explicit so tests can pin both platform behaviors.
//
// Recognized form (outside quotes, not escaped, start of a token):
//
//	[1|2]>[ \t]*(/dev/null | NUL)
//
// followed by end of string or a shell separator (space, tab, newline, ;, |,
// &). The append operator `>>`, fd duplication `>&`, other targets, other file
// descriptors, and quoted/escaped spellings are left untouched so the
// caller's character pre-scan continues to reject them.
func stripNullDeviceRedirections(command, goos string) string {
	target, fold := nullDeviceTarget(goos)
	runes := []rune(command)
	var out strings.Builder
	out.Grow(len(command))

	quote := rune(0)
	escaped := false
	for i := 0; i < len(runes); {
		r := runes[i]
		if escaped {
			out.WriteRune(r)
			escaped = false
			i++
			continue
		}
		if quote == '\'' {
			out.WriteRune(r)
			if r == '\'' {
				quote = 0
			}
			i++
			continue
		}
		if r == '\\' && quote != '\'' {
			// Keep the backslash and treat the next rune as escaped: an escaped
			// `>` is a literal argument, never a redirection.
			out.WriteRune(r)
			escaped = true
			i++
			continue
		}
		if quote == '"' {
			out.WriteRune(r)
			if r == '"' {
				quote = 0
			}
			i++
			continue
		}
		switch r {
		case '\'', '"':
			quote = r
			out.WriteRune(r)
			i++
			continue
		}
		if end, ok := matchNullDeviceRedirection(runes, i, target, fold); ok {
			i = end
			continue
		}
		out.WriteRune(r)
		i++
	}
	return out.String()
}

// nullDeviceTarget returns the null-device spelling of goos and whether the
// comparison must be case-insensitive (Windows device names are).
func nullDeviceTarget(goos string) (string, bool) {
	if strings.EqualFold(strings.TrimSpace(goos), "windows") {
		return windowsNullDevice, true
	}
	return posixNullDevice, false
}

// matchNullDeviceRedirection reports whether runes[start:] begins with a
// null-device redirection and returns its end offset (exclusive). The match
// must start at a token boundary; the matched span never includes the trailing
// separator or following token.
func matchNullDeviceRedirection(runes []rune, start int, target string, fold bool) (int, bool) {
	if !atRedirectionBoundary(runes, start) {
		return 0, false
	}
	op := start
	switch runes[start] {
	case '1', '2':
		op = start + 1
		if op >= len(runes) || runes[op] != '>' {
			return 0, false
		}
	case '>':
		// op stays at start.
	default:
		return 0, false
	}
	// `>>` (append) and `>&` (fd duplication) are different operators and stay
	// on the reject side.
	if op+1 < len(runes) && (runes[op+1] == '>' || runes[op+1] == '&') {
		return 0, false
	}
	cursor := op + 1
	for cursor < len(runes) && (runes[cursor] == ' ' || runes[cursor] == '\t') {
		cursor++
	}
	targetRunes := []rune(target)
	if cursor+len(targetRunes) > len(runes) {
		return 0, false
	}
	candidate := string(runes[cursor : cursor+len(targetRunes)])
	if fold {
		if !strings.EqualFold(candidate, target) {
			return 0, false
		}
	} else if candidate != target {
		return 0, false
	}
	end := cursor + len(targetRunes)
	// The target is a complete word: `>/dev/nullx` and `>/dev/null/other` are
	// different (file-creating) targets and must not match.
	if end < len(runes) && !isRedirectionTerminator(runes[end]) {
		return 0, false
	}
	return end, true
}

// atRedirectionBoundary reports whether index starts a new shell token.
func atRedirectionBoundary(runes []rune, index int) bool {
	if index <= 0 {
		return true
	}
	switch runes[index-1] {
	case ' ', '\t', '\n', '\r', ';', '|', '&':
		return true
	default:
		return false
	}
}

// isRedirectionTerminator reports whether r ends the redirection token.
func isRedirectionTerminator(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\r', ';', '|', '&':
		return true
	default:
		return false
	}
}
