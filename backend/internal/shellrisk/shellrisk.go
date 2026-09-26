// Package shellrisk provides a conservative, dependency-free classifier for
// shell commands whose destructive effect must not be waived by permission
// modes. It backs the root/home removal circuit breaker
// (docs/analysis/commandcode-permissions-design-borrowing-20260926.md §4.3)
// and is deliberately shared: the policy engine, the bash tool, and approval
// surfaces must agree on what counts as a catastrophic removal instead of each
// keeping its own substring blacklist.
//
// The parser is intentionally small and conservative:
//
//   - compound commands are split into independently executed segments
//     (&&, ||, ;, |, &, newline) outside quotes and command substitutions;
//   - quoting, env prefixes (FOO=bar), common wrappers (timeout, nice, nohup,
//     env, sudo, stdbuf), and shell interpreters (sh -c, pwsh -Command, cmd /c)
//     are pierced so a wrapped command is judged by the command it really runs;
//   - unparseable input (unbalanced quotes) is reported via Assessment.Unparsed
//     and still analyzed best-effort — callers should treat Unparsed as
//     "please ask the user", never as "safe".
//
// The only risk classified today is root/home removal: recursive rm (POSIX or
// PowerShell) or find -delete aimed at /, ~, $HOME, %USERPROFILE%, a shell
// variable spelling of those, or the literal home directory. False positives
// only ever cost one confirmation; false negatives are the failure mode this
// package exists to prevent, so the matcher errs toward detection.
package shellrisk

import (
	"os"
	"path/filepath"
	"strings"
)

// Risk identifies a catastrophic command class.
type Risk string

// RiskRootHomeRemoval marks a recursive delete of the filesystem root or the
// user's home directory (or a shell-variable spelling of either).
const RiskRootHomeRemoval Risk = "root_home_removal"

// Finding is one detected risk inside a command.
type Finding struct {
	Risk    Risk
	Segment string
	Target  string
}

// Assessment is the result of inspecting one command (or a batch).
type Assessment struct {
	Findings []Finding
	// Unparsed is true when the command could not be fully tokenized
	// (unbalanced quotes). Callers must not treat such a command as safe.
	Unparsed bool
}

// Risky reports whether at least one finding was detected.
func (a Assessment) Risky() bool {
	return len(a.Findings) > 0
}

// First returns the first finding, if any.
func (a Assessment) First() (Finding, bool) {
	if len(a.Findings) == 0 {
		return Finding{}, false
	}
	return a.Findings[0], true
}

// PrimaryRisk returns the first finding's risk, or "" when clean.
func (a Assessment) PrimaryRisk() Risk {
	if finding, ok := a.First(); ok {
		return finding.Risk
	}
	return ""
}

// Merge folds two assessments together, preserving order and the unparsed flag.
func Merge(a, b Assessment) Assessment {
	merged := Assessment{Unparsed: a.Unparsed || b.Unparsed}
	if len(a.Findings) > 0 {
		merged.Findings = append(merged.Findings, a.Findings...)
	}
	if len(b.Findings) > 0 {
		merged.Findings = append(merged.Findings, b.Findings...)
	}
	return merged
}

// AssessCommands inspects every command of a batch and merges the results, so
// one destructive entry is reported even when the others are harmless.
func AssessCommands(commands []string) Assessment {
	var merged Assessment
	for _, command := range commands {
		merged = Merge(merged, Assess(command))
	}
	return merged
}

const maxSubstitutionDepth = 4

// Assess inspects one command string.
func Assess(command string) Assessment {
	return assess(command, 0)
}

func assess(command string, depth int) Assessment {
	command = strings.TrimSpace(command)
	if command == "" || depth > maxSubstitutionDepth {
		return Assessment{}
	}
	segments, parsed := Segments(command)
	assessment := Assessment{Unparsed: !parsed}
	for _, segment := range segments {
		assessment = Merge(assessment, analyzeSegment(segment, depth))
	}
	return assessment
}

// Segments splits a command into independently executed segments at
// &&, ||, ;, |, &, and newlines outside quotes and substitutions. ok is false
// when the input could not be fully parsed (e.g. an unbalanced quote); the
// returned segments are still the best-effort prefix and remain analyzable.
func Segments(command string) ([]string, bool) {
	var (
		segments []string
		current  strings.Builder
		quote    rune // 0, '\'', '"'
		subDepth int
		escaped  bool
	)
	flush := func() {
		text := strings.TrimSpace(current.String())
		current.Reset()
		if text != "" {
			segments = append(segments, text)
		}
	}
	runes := []rune(command)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if escaped {
			current.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' && quote != '\'' && escapesNext(runes, i, quote) {
			current.WriteRune(r)
			escaped = true
			continue
		}
		if quote != 0 {
			current.WriteRune(r)
			if r == quote {
				quote = 0
			}
			continue
		}
		switch r {
		case '\'', '"':
			quote = r
			current.WriteRune(r)
			continue
		case '$':
			if i+1 < len(runes) && runes[i+1] == '(' {
				current.WriteString("$(")
				i++
				subDepth++
				continue
			}
			current.WriteRune(r)
			continue
		case '(':
			if subDepth > 0 {
				subDepth++
			}
			current.WriteRune(r)
			continue
		case ')':
			if subDepth > 0 {
				subDepth--
			}
			current.WriteRune(r)
			continue
		case '`':
			// Toggle command substitution; contents stay in the token and are
			// recursively analyzed by the caller.
			current.WriteRune(r)
			continue
		}
		if subDepth > 0 {
			current.WriteRune(r)
			continue
		}
		switch r {
		case '&', '|':
			// Consume the doubled operator as one separator.
			if i+1 < len(runes) && runes[i+1] == r {
				i++
			}
			flush()
			continue
		case ';', '\n', '\r':
			flush()
			continue
		}
		current.WriteRune(r)
	}
	flush()
	return segments, quote == 0 && subDepth == 0 && !escaped
}

// Fields tokenizes one segment: whitespace splits tokens outside quotes,
// quotes are removed, and $(...) / `...` stay inside their token so nested
// commands can be analyzed recursively.
func Fields(segment string) []string {
	var (
		fields     []string
		current    strings.Builder
		quote      rune
		escaped    bool
		subDepth   int
		inBacktick bool
	)
	flush := func() {
		if current.Len() > 0 {
			fields = append(fields, current.String())
			current.Reset()
		}
	}
	runes := []rune(segment)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if escaped {
			current.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' && quote != '\'' && escapesNext(runes, i, quote) {
			escaped = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
				continue
			}
			current.WriteRune(r)
			continue
		}
		switch r {
		case '\'', '"':
			quote = r
			continue
		case '$':
			if i+1 < len(runes) && runes[i+1] == '(' {
				// Keep a command substitution inside one token so its inner
				// command can be analyzed recursively.
				current.WriteString("$(")
				i++
				subDepth++
				continue
			}
		case '(':
			if subDepth > 0 {
				subDepth++
			}
		case ')':
			if subDepth > 0 {
				subDepth--
			}
		case '`':
			inBacktick = !inBacktick
		}
		if subDepth == 0 && !inBacktick && (r == ' ' || r == '\t') {
			flush()
			continue
		}
		current.WriteRune(r)
	}
	flush()
	return fields
}

func analyzeSegment(segment string, depth int) Assessment {
	assessment := Assessment{}
	fields := Fields(segment)
	if len(fields) == 0 {
		return assessment
	}
	fields = stripEnvPrefix(fields)

	// Nested command substitutions execute independently of the outer command.
	for _, field := range fields {
		for _, inner := range extractSubstitutions(field) {
			assessment = Merge(assessment, assess(inner, depth+1))
		}
	}

	fields, handled := unwrap(fields, depth, &assessment)
	if handled || len(fields) == 0 {
		return assessment
	}
	argv0 := normalizeCommandName(fields[0])
	args := fields[1:]

	switch {
	case isRemoveCommand(argv0) && isRecursiveRemove(argv0, args):
		for _, target := range targetArguments(fields[1:]) {
			if isRootOrHomeTarget(target) {
				assessment.Findings = append(assessment.Findings, Finding{
					Risk:    RiskRootHomeRemoval,
					Segment: segment,
					Target:  target,
				})
			}
		}
	case argv0 == "find" && hasFlag(args, "-delete"):
		for _, target := range startPaths(args) {
			if isRootOrHomeTarget(target) {
				assessment.Findings = append(assessment.Findings, Finding{
					Risk:    RiskRootHomeRemoval,
					Segment: segment,
					Target:  target,
				})
			}
		}
	}
	return assessment
}

// stripEnvPrefix removes leading NAME=value assignments.
func stripEnvPrefix(fields []string) []string {
	for len(fields) > 0 && looksLikeEnvAssignment(fields[0]) {
		fields = fields[1:]
	}
	return fields
}

func looksLikeEnvAssignment(field string) bool {
	eq := strings.IndexByte(field, '=')
	if eq <= 0 {
		return false
	}
	name := field[:eq]
	for i, r := range name {
		if r == '_' || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (i > 0 && r >= '0' && r <= '9') {
			continue
		}
		return false
	}
	return true
}

// unwrap pierces common process wrappers and shell interpreters. It returns the
// remaining argv and handled=true when the segment was fully resolved by
// recursion (shell -c inner command) and must not be analyzed further.
func unwrap(fields []string, depth int, assessment *Assessment) ([]string, bool) {
	for iteration := 0; iteration < 8 && len(fields) > 0; iteration++ {
		argv0 := normalizeCommandName(fields[0])
		args := fields[1:]

		if isShellInterpreter(argv0) {
			if inner, ok := interpreterPayload(argv0, args); ok {
				*assessment = Merge(*assessment, assess(inner, depth+1))
				return nil, true
			}
			// Interactive/plain shell: analyze what follows as a command when
			// it is not a flag, otherwise stop.
			if len(args) > 0 && !strings.HasPrefix(args[0], "-") && !strings.HasPrefix(args[0], "/") {
				fields = args
				continue
			}
			return nil, true
		}

		switch argv0 {
		case "timeout":
			fields = skipWrapperArgs(args, map[string]bool{"-s": true, "--signal": true, "-k": true, "--kill-after": true})
			fields = skipDuration(fields)
			continue
		case "nice":
			fields = skipWrapperArgs(args, map[string]bool{"-n": true, "--adjustment": true})
			continue
		case "stdbuf":
			fields = skipWrapperArgs(args, map[string]bool{"-i": true, "-o": true, "-e": true, "--input": true, "--output": true, "--error": true})
			continue
		case "nohup", "setsid", "command", "exec", "builtin", "doas", "sudo":
			fields = skipWrapperArgs(args, map[string]bool{"-u": true, "--user": true, "-g": true, "--group": true, "-C": true, "--chdir": true, "-p": true, "--prompt": true})
			continue
		case "env":
			fields = skipWrapperArgs(args, map[string]bool{"-u": true, "--unset": true, "-C": true, "--chdir": true})
			fields = stripEnvPrefix(fields)
			continue
		default:
			return fields, false
		}
	}
	return fields, false
}

// skipWrapperArgs drops leading flags; flags named in withValue also consume
// their following argument.
func skipWrapperArgs(args []string, withValue map[string]bool) []string {
	for len(args) > 0 {
		arg := args[0]
		if arg == "--" {
			return args[1:]
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			return args
		}
		args = args[1:]
		if withValue[arg] && len(args) > 0 {
			args = args[1:]
		}
	}
	return args
}

// skipDuration drops an optional timeout duration (first bare argument).
func skipDuration(args []string) []string {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") && !looksLikeCommand(args[0]) {
		return args[1:]
	}
	return args
}

// looksLikeCommand is a cheap heuristic used only to decide whether a bare
// timeout argument is a duration: durations are numeric with optional unit.
func looksLikeCommand(token string) bool {
	if token == "" {
		return false
	}
	if _, ok := parseDurationLike(token); ok {
		return false
	}
	return true
}

func parseDurationLike(token string) (string, bool) {
	trimmed := strings.TrimSpace(token)
	if trimmed == "" {
		return "", false
	}
	index := 0
	for index < len(trimmed) && (trimmed[index] >= '0' && trimmed[index] <= '9' || trimmed[index] == '.') {
		index++
	}
	if index == 0 {
		return "", false
	}
	unit := strings.ToLower(trimmed[index:])
	switch unit {
	case "", "s", "m", "h", "d", "ms", "us", "ns":
		return trimmed, true
	default:
		return "", false
	}
}

func isShellInterpreter(argv0 string) bool {
	switch argv0 {
	case "sh", "bash", "zsh", "dash", "ksh", "ash", "pwsh", "powershell", "cmd":
		return true
	default:
		return false
	}
}

// interpreterPayload extracts the -c / -Command / /c payload of a shell
// invocation.
func interpreterPayload(argv0 string, args []string) (string, bool) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		lower := strings.ToLower(arg)
		switch {
		case argv0 == "cmd":
			if lower == "/c" || lower == "/k" {
				if i+1 < len(args) {
					return strings.Join(args[i+1:], " "), true
				}
				return "", true
			}
		case lower == "-c" || lower == "-command" || lower == "/c" || lower == "-commandwithargs":
			if i+1 < len(args) {
				return strings.Join(args[i+1:], " "), true
			}
			return "", true
		}
	}
	return "", false
}

// isRemoveCommand reports POSIX and PowerShell deletion commands.
func isRemoveCommand(argv0 string) bool {
	switch argv0 {
	case "rm", "del", "erase", "remove-item", "ri":
		return true
	default:
		return false
	}
}

// isRecursiveRemove reports whether the deletion carries a recursive switch.
func isRecursiveRemove(argv0 string, args []string) bool {
	for _, arg := range args {
		lower := strings.ToLower(arg)
		if lower == "--recursive" || lower == "-recurse" || lower == "/s" {
			return true
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" || arg == "--" {
			continue
		}
		if strings.HasPrefix(arg, "--") {
			continue
		}
		// Short flag clusters: any 'r'/'R' in rm -rf, rm -fr, rm -R, and the
		// PowerShell parameter prefix -r (=-Recurse) all mean recursion.
		for _, r := range arg[1:] {
			if r == 'r' || r == 'R' {
				return true
			}
		}
	}
	return false
}

// escapesNext reports whether a backslash at runes[index] acts as an escape
// character. Windows path separators (C:\Users\...) must stay literal, so the
// backslash only escapes quotes, whitespace, another backslash, '$', or '`'.
func escapesNext(runes []rune, index int, quote rune) bool {
	if index+1 >= len(runes) {
		return false
	}
	next := runes[index+1]
	switch next {
	case '"', '\\', '$', '`':
		return true
	case ' ':
		return true
	case '\'':
		// Inside double quotes \" escapes; a bare \' is left literal on
		// Windows and POSIX alike for our purposes.
		return quote == '"'
	default:
		return false
	}
}

// targetArguments returns every non-flag argument that can name a path.
func targetArguments(args []string) []string {
	targets := make([]string, 0, len(args))
	for _, arg := range args {
		if arg == "--" {
			continue
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		targets = append(targets, arg)
	}
	return targets
}

// startPaths returns the search roots of a find invocation (tokens before the
// first flag).
func startPaths(args []string) []string {
	paths := make([]string, 0, len(args))
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			break
		}
		paths = append(paths, arg)
	}
	return paths
}

func hasFlag(args []string, flag string) bool {
	for _, arg := range args {
		if strings.EqualFold(arg, flag) {
			return true
		}
	}
	return false
}

// normalizeCommandName lowercases the basename and strips Windows extensions.
func normalizeCommandName(field string) string {
	name := strings.ToLower(filepath.Base(strings.TrimSpace(field)))
	name = strings.TrimSuffix(name, ".exe")
	name = strings.TrimSuffix(name, ".cmd")
	name = strings.TrimSuffix(name, ".bat")
	name = strings.TrimSuffix(name, ".ps1")
	return name
}

// isRootOrHomeTarget matches filesystem roots and the current user's home
// directory, including shell-variable spellings, after quote removal.
func isRootOrHomeTarget(target string) bool {
	text := strings.TrimSpace(target)
	if text == "" {
		return false
	}
	// Strip trailing separators except when the whole value is a root.
	trimmed := strings.TrimRight(text, "/\\")
	if trimmed == "" {
		return true // "/", "\\", "//"
	}
	lower := strings.ToLower(trimmed)

	switch lower {
	case "~", "~/", "~\\",
		"$home", "${home}", "$home/*", "$home/\\",
		"$env:userprofile", "${env:userprofile}",
		"%userprofile%",
		"$env:home", "$env:homepath",
		"/", "/*", "/**":
		return true
	}
	if strings.HasPrefix(lower, "~/") || strings.HasPrefix(lower, "~\\") ||
		strings.HasPrefix(lower, "$home/") || strings.HasPrefix(lower, "${home}/") ||
		strings.HasPrefix(lower, "%userprofile%/") || strings.HasPrefix(lower, "%userprofile%\\") ||
		strings.HasPrefix(lower, "$env:userprofile/") || strings.HasPrefix(lower, "$env:userprofile\\") {
		return true
	}

	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return false
	}
	home = filepath.Clean(home)
	cleaned := filepath.Clean(text)
	if strings.EqualFold(cleaned, home) {
		return true
	}
	if pathWithinBaseFold(cleaned, home) {
		return true
	}
	// Volume root of the home drive (C:\ on Windows, / elsewhere).
	volume := filepath.VolumeName(home)
	if volume != "" {
		root := volume + string(filepath.Separator)
		if strings.EqualFold(cleaned, root) {
			return true
		}
	}
	return false
}

func pathWithinBaseFold(path, base string) bool {
	relative, err := filepath.Rel(base, path)
	if err != nil {
		return false
	}
	if relative == "." || relative == "" {
		return true
	}
	if strings.HasPrefix(relative, "..") {
		return false
	}
	return !filepath.IsAbs(relative)
}

// extractSubstitutions returns the contents of $(...) and `...` inside one
// token.
func extractSubstitutions(token string) []string {
	var (
		inner []string
		runes = []rune(token)
	)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if r == '`' {
			for j := i + 1; j < len(runes); j++ {
				if runes[j] == '\\' {
					j++
					continue
				}
				if runes[j] == '`' {
					inner = append(inner, string(runes[i+1:j]))
					i = j
					break
				}
			}
			continue
		}
		if r == '$' && i+1 < len(runes) && runes[i+1] == '(' {
			depth := 0
			for j := i + 2; j < len(runes); j++ {
				switch runes[j] {
				case '(':
					depth++
				case ')':
					if depth == 0 {
						inner = append(inner, string(runes[i+2:j]))
						i = j
					} else {
						depth--
					}
				}
				if i == j {
					break
				}
			}
		}
	}
	return inner
}

// ResolveSegment returns the static argv of one shell segment after env-prefix
// stripping and common-wrapper piercing, for callers that match rules or
// classify commands and must not be fooled by `FOO=bar timeout 5 cmd`. ok is
// false when the segment cannot be resolved statically (unbalanced quotes,
// variable expansion, command substitution, backticks, or a shell-interpreter
// payload); allow-style callers must treat such segments conservatively and
// never auto-allow them.
func ResolveSegment(segment string) ([]string, bool) {
	segment = strings.TrimSpace(segment)
	if segment == "" || !staticallyResolvable(segment) {
		return nil, false
	}
	fields := Fields(segment)
	if len(fields) == 0 {
		return nil, false
	}
	fields = stripEnvPrefix(fields)
	for iteration := 0; iteration < 8 && len(fields) > 0; iteration++ {
		argv0 := normalizeCommandName(fields[0])
		args := fields[1:]
		if isShellInterpreter(argv0) {
			return nil, false
		}
		var next []string
		switch argv0 {
		case "timeout":
			next = skipWrapperArgs(args, map[string]bool{"-s": true, "--signal": true, "-k": true, "--kill-after": true})
			next = skipDuration(next)
		case "nice":
			next = skipWrapperArgs(args, map[string]bool{"-n": true, "--adjustment": true})
		case "stdbuf":
			next = skipWrapperArgs(args, map[string]bool{"-i": true, "-o": true, "-e": true, "--input": true, "--output": true, "--error": true})
		case "nohup", "setsid", "command", "exec", "builtin", "doas", "sudo":
			next = skipWrapperArgs(args, map[string]bool{"-u": true, "--user": true, "-g": true, "--group": true, "-C": true, "--chdir": true, "-p": true, "--prompt": true})
		case "env":
			next = skipWrapperArgs(args, map[string]bool{"-u": true, "--unset": true, "-C": true, "--chdir": true})
			next = stripEnvPrefix(next)
		default:
			return fields, true
		}
		fields = next
	}
	if len(fields) == 0 {
		return nil, false
	}
	return fields, true
}

// staticallyResolvable rejects segments whose executed text cannot be known
// without evaluation: variable expansion, command substitution, backticks, and
// unbalanced quotes.
func staticallyResolvable(segment string) bool {
	quote := rune(0)
	escaped := false
	runes := []rune(segment)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if escaped {
			escaped = false
			continue
		}
		if r == '\\' && quote != '\'' && escapesNext(runes, i, quote) {
			escaped = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			}
			continue
		}
		switch r {
		case '\'', '"':
			quote = r
		case '$', '`':
			return false
		}
	}
	return quote == 0 && !escaped
}
