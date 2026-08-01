package policy

import (
	"path/filepath"
	"strings"
	"sync"
)

// Decision stage identifiers for permission pipeline observability.
const (
	StageHooks        = "hooks"
	StagePolicy       = "policy"
	StageRules        = "rules"
	StageGrants       = "grants"
	StageReadonlyAuto = "readonly_auto"
	StageMode         = "mode"
	StageCallback     = "callback"
	StageAsk          = "ask"
	StageHeadlessDeny = "headless_deny"
)

// Grant records a remembered allow decision for a tool (and optional pattern).
type Grant struct {
	Tool    string
	Pattern string // optional argv/path pattern; empty matches tool-wide
	Scope   string // project|session
}

// GrantStore stores remembered grants. Dangerous tools are never accepted.
type GrantStore interface {
	Find(toolName string, args map[string]interface{}) (Grant, bool)
	Remember(grant Grant) error
}

// GrantLister is an optional GrantStore extension for control-plane listing.
type GrantLister interface {
	List() []Grant
}

// GrantRevoker is an optional GrantStore extension for revoking remembered grants.
type GrantRevoker interface {
	// Revoke removes matching grants. Empty pattern matches tool-wide grants only
	// when matchEmptyPattern is true; otherwise empty pattern revokes all grants for the tool.
	Revoke(toolName, pattern string, matchEmptyPattern bool) int
}

// MemoryGrantStore is an in-memory GrantStore implementation.
type MemoryGrantStore struct {
	mu     sync.RWMutex
	grants []Grant
}

// Find returns the first matching grant.
func (s *MemoryGrantStore) Find(toolName string, args map[string]interface{}) (Grant, bool) {
	if s == nil {
		return Grant{}, false
	}
	toolName = strings.ToLower(strings.TrimSpace(toolName))
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, grant := range s.grants {
		if !strings.EqualFold(strings.TrimSpace(grant.Tool), toolName) {
			continue
		}
		pattern := strings.TrimSpace(grant.Pattern)
		if pattern == "" {
			return grant, true
		}
		if argsMatchGrantPattern(args, pattern) {
			return grant, true
		}
	}
	return Grant{}, false
}

// Remember stores a grant unless the tool is dangerous.
func (s *MemoryGrantStore) Remember(grant Grant) error {
	if s == nil {
		return nil
	}
	grant.Tool = strings.TrimSpace(grant.Tool)
	if grant.Tool == "" {
		return nil
	}
	if IsDangerousTool(grant.Tool) {
		return errDangerousGrant
	}
	grant.Pattern = strings.TrimSpace(grant.Pattern)
	grant.Scope = strings.TrimSpace(grant.Scope)
	if grant.Scope == "" {
		grant.Scope = "session"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.grants = append(s.grants, grant)
	return nil
}

// List returns a copy of remembered grants.
func (s *MemoryGrantStore) List() []Grant {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.grants) == 0 {
		return nil
	}
	out := make([]Grant, len(s.grants))
	copy(out, s.grants)
	return out
}

// Revoke removes matching grants and returns how many were removed.
func (s *MemoryGrantStore) Revoke(toolName, pattern string, matchEmptyPattern bool) int {
	if s == nil {
		return 0
	}
	toolName = strings.TrimSpace(toolName)
	if toolName == "" {
		return 0
	}
	pattern = strings.TrimSpace(pattern)
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.grants) == 0 {
		return 0
	}
	kept := s.grants[:0]
	removed := 0
	for _, grant := range s.grants {
		if !strings.EqualFold(strings.TrimSpace(grant.Tool), toolName) {
			kept = append(kept, grant)
			continue
		}
		grantPattern := strings.TrimSpace(grant.Pattern)
		if pattern == "" {
			if matchEmptyPattern && grantPattern != "" {
				kept = append(kept, grant)
				continue
			}
			removed++
			continue
		}
		if !strings.EqualFold(grantPattern, pattern) {
			kept = append(kept, grant)
			continue
		}
		removed++
	}
	if removed == 0 {
		return 0
	}
	// zero trailing slots to avoid retaining references
	for i := len(kept); i < len(s.grants); i++ {
		s.grants[i] = Grant{}
	}
	s.grants = kept
	return removed
}

var errDangerousGrant = monadicError("grant_rejected_dangerous_tool")

// IsDangerousGrantError reports whether Remember rejected a dangerous tool.
func IsDangerousGrantError(err error) bool {
	return err != nil && err.Error() == string(errDangerousGrant)
}

type monadicError string

func (e monadicError) Error() string { return string(e) }

// IsDangerousTool reports tools that must never be remembered as always-allow.
func IsDangerousTool(toolName string) bool {
	name := normalizeToolName(toolName)
	switch name {
	case "shell", "bash", "aicli_exec", "background_task":
		return true
	default:
		return false
	}
}

func argsMatchGrantPattern(args map[string]interface{}, pattern string) bool {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return true
	}
	if command, ok := firstStringArg(args, "command", "cmd"); ok {
		if strings.Contains(strings.ToLower(command), strings.ToLower(pattern)) {
			return true
		}
	}
	if path, ok := firstStringArg(args, "file_path", "path"); ok {
		if strings.EqualFold(path, pattern) || strings.Contains(strings.ToLower(path), strings.ToLower(pattern)) {
			return true
		}
	}
	return false
}

func firstStringArg(args map[string]interface{}, keys ...string) (string, bool) {
	if len(args) == 0 {
		return "", false
	}
	for _, key := range keys {
		if raw, ok := args[key]; ok {
			if text, ok := raw.(string); ok && strings.TrimSpace(text) != "" {
				return strings.TrimSpace(text), true
			}
		}
	}
	return "", false
}

const (
	ShellReadOnlyReasonEmpty         = "empty_command"
	ShellReadOnlyReasonCompound      = "compound_command"
	ShellReadOnlyReasonDynamicSyntax = "dynamic_shell_syntax"
	ShellReadOnlyReasonNotAllowed    = "command_not_allowlisted"
)

// ShellReadOnlyAssessment describes why a shell command is or is not accepted
// by the read-only execution boundary. The reason is intentionally stable so
// tool-result recovery can distinguish "split this batch" from a real mutation.
type ShellReadOnlyAssessment struct {
	Allowed bool
	Reason  string
}

// AssessShellReadOnlyCommand validates one concrete shell command against the
// read-only allow table. Compound statements are deliberately rejected: callers
// should submit multiple commands through the shell tool's structured commands
// array so every entry can be validated independently.
func AssessShellReadOnlyCommand(command string) ShellReadOnlyAssessment {
	command = strings.TrimSpace(command)
	if command == "" {
		return ShellReadOnlyAssessment{Reason: ShellReadOnlyReasonEmpty}
	}
	// Reject obvious chaining first so callers can return a precise recovery
	// action (use commands=[...], not an approval request).
	for _, bad := range []string{"&&", "||", "&", ";", "|", "\n", "\r"} {
		if strings.Contains(command, bad) {
			return ShellReadOnlyAssessment{Reason: ShellReadOnlyReasonCompound}
		}
	}
	// Redirection and command substitution can smuggle side effects even when
	// argv[0] itself is a read-only command.
	for _, bad := range []string{">", "<", "`", "$"} {
		if strings.Contains(command, bad) {
			return ShellReadOnlyAssessment{Reason: ShellReadOnlyReasonDynamicSyntax}
		}
	}
	// cmd.exe and delayed-expansion shells use paired percent/bang markers for
	// environment substitution. Reject them conservatively; an expanded value
	// can inject options or a different path after this classifier runs.
	if hasPairedShellExpansion(command, '%') || hasPairedShellExpansion(command, '!') {
		return ShellReadOnlyAssessment{Reason: ShellReadOnlyReasonDynamicSyntax}
	}

	fields := splitCommandFields(command)
	if len(fields) == 0 {
		return ShellReadOnlyAssessment{Reason: ShellReadOnlyReasonEmpty}
	}
	argv0 := strings.ToLower(filepath.Base(fields[0]))
	argv0 = strings.TrimSuffix(argv0, ".exe")
	argv0 = strings.TrimSuffix(argv0, ".cmd")
	argv0 = strings.TrimSuffix(argv0, ".bat")

	allowed := false
	switch argv0 {
	case "rg":
		allowed = isReadOnlyRipgrepCommand(fields[1:])
	case "grep", "findstr", "ag", "ack":
		allowed = true
	case "file":
		allowed = isReadOnlyFileCommand(fields[1:])
	case "ls", "dir", "pwd", "get-location", "get-childitem", "gci", "gl", "cat", "type", "get-content", "gc", "head", "tail", "wc", "stat", "which", "where", "where.exe", "echo", "printf":
		allowed = true
	case "git":
		allowed = isReadOnlyGitCommand(fields[1:])
	case "go":
		allowed = isReadOnlyGoCommand(fields[1:])
	case "npm", "pnpm", "yarn", "cargo", "python", "python3", "node", "pip", "pip3":
		allowed = isReadOnlyVersionFlag(fields[1:])
	}
	if allowed {
		return ShellReadOnlyAssessment{Allowed: true}
	}
	return ShellReadOnlyAssessment{Reason: ShellReadOnlyReasonNotAllowed}
}

func isReadOnlyRipgrepCommand(args []string) bool {
	for _, raw := range args {
		arg := strings.ToLower(strings.TrimSpace(raw))
		switch {
		case arg == "--pre", strings.HasPrefix(arg, "--pre="):
			return false
		case arg == "--hostname-bin", strings.HasPrefix(arg, "--hostname-bin="):
			return false
		case arg == "--search-zip", arg == "--zip", hasShortOption(arg, 'z'):
			// Compressed-file search launches external decompressor binaries.
			return false
		}
	}
	return true
}

func isReadOnlyFileCommand(args []string) bool {
	for _, raw := range args {
		arg := strings.TrimSpace(raw)
		lower := strings.ToLower(arg)
		switch {
		case lower == "--compile", hasShortOption(arg, 'C'):
			// file --compile writes a compiled .mgc database.
			return false
		case lower == "--uncompress", lower == "--uncompress-noreport",
			hasShortOption(arg, 'z'), hasShortOption(arg, 'Z'):
			// Avoid external decompressor execution inside a read-only child.
			return false
		}
	}
	return true
}

func isReadOnlyVersionFlag(args []string) bool {
	if len(args) != 1 {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "-v", "--version", "-h", "--help":
		return true
	default:
		return false
	}
}

func hasShortOption(arg string, option rune) bool {
	arg = strings.TrimSpace(arg)
	if len(arg) < 2 || !strings.HasPrefix(arg, "-") || strings.HasPrefix(arg, "--") {
		return false
	}
	return strings.ContainsRune(arg[1:], option)
}

func hasPairedShellExpansion(command string, marker rune) bool {
	return strings.Count(command, string(marker)) >= 2
}

// IsShellReadOnlyCommand reports whether a shell/bash command is on the
// read-only allow table (git status, rg, ls, pwd, etc.).
func IsShellReadOnlyCommand(command string) bool {
	return AssessShellReadOnlyCommand(command).Allowed
}

func isReadOnlyGitCommand(args []string) bool {
	if len(args) == 0 {
		return false
	}
	sub := strings.ToLower(strings.TrimSpace(args[0]))
	rest := args[1:]
	switch sub {
	case "status":
		return !containsForbiddenGitReadFlag(rest)
	case "diff", "log", "show", "blame", "shortlog":
		return !containsForbiddenGitReadFlag(rest)
	case "rev-parse", "describe", "ls-files", "ls-tree":
		return !containsForbiddenGitReadFlag(rest)
	case "branch":
		return isReadOnlyGitBranch(rest)
	case "tag":
		return isReadOnlyGitTag(rest)
	case "remote":
		return isReadOnlyGitRemote(rest)
	case "stash":
		if len(rest) == 0 {
			return false
		}
		switch strings.ToLower(strings.TrimSpace(rest[0])) {
		case "list", "show":
			return !containsForbiddenGitReadFlag(rest[1:])
		default:
			return false
		}
	default:
		return false
	}
}

func containsForbiddenGitReadFlag(args []string) bool {
	for _, arg := range args {
		lower := strings.ToLower(strings.TrimSpace(arg))
		switch {
		case lower == "--am", lower == "--continue", lower == "--abort":
			return true
		case lower == "--output", strings.HasPrefix(lower, "--output="):
			return true
		case lower == "--ext-diff", lower == "--textconv":
			return true
		}
	}
	return false
}

func isReadOnlyGitBranch(args []string) bool {
	if len(args) == 0 {
		return true
	}
	queryMode := false
	for _, raw := range args {
		arg := strings.ToLower(strings.TrimSpace(raw))
		switch {
		case arg == "":
			continue
		case isGitBranchMutationFlag(arg):
			return false
		case arg == "--list", arg == "--show-current",
			arg == "-a", arg == "--all", arg == "-r", arg == "--remotes",
			arg == "-v", arg == "-vv", arg == "--verbose",
			arg == "--contains", strings.HasPrefix(arg, "--contains="),
			arg == "--no-contains", strings.HasPrefix(arg, "--no-contains="),
			arg == "--merged", strings.HasPrefix(arg, "--merged="),
			arg == "--no-merged", strings.HasPrefix(arg, "--no-merged="),
			arg == "--points-at", strings.HasPrefix(arg, "--points-at="),
			arg == "--format", strings.HasPrefix(arg, "--format="),
			arg == "--sort", strings.HasPrefix(arg, "--sort="),
			arg == "--column", strings.HasPrefix(arg, "--column="),
			arg == "--no-column", arg == "--color", strings.HasPrefix(arg, "--color="),
			arg == "--no-color", arg == "--ignore-case",
			arg == "--abbrev", strings.HasPrefix(arg, "--abbrev="), arg == "--no-abbrev":
			queryMode = true
		case strings.HasPrefix(arg, "-"):
			return false
		default:
			// A positional value is a pattern/revision only after an explicit
			// query selector. Without queryMode, "git branch NAME" creates it.
			if !queryMode {
				return false
			}
		}
	}
	return queryMode
}

func isGitBranchMutationFlag(arg string) bool {
	switch {
	case arg == "-d", arg == "--delete", arg == "-m", arg == "--move",
		arg == "-c", arg == "--copy", arg == "-f", arg == "--force",
		arg == "--edit-description", arg == "-u", arg == "--set-upstream-to",
		strings.HasPrefix(arg, "--set-upstream-to="), arg == "--unset-upstream",
		arg == "--track", strings.HasPrefix(arg, "--track="), arg == "--no-track",
		arg == "--recurse-submodules":
		return true
	default:
		return false
	}
}

func isReadOnlyGitTag(args []string) bool {
	if len(args) == 0 {
		return true
	}
	queryMode := false
	for _, raw := range args {
		arg := strings.ToLower(strings.TrimSpace(raw))
		switch {
		case arg == "":
			continue
		case isGitTagMutationFlag(arg):
			return false
		case arg == "-l", arg == "--list", arg == "-n", strings.HasPrefix(arg, "-n"),
			arg == "-v", arg == "--verify",
			arg == "--contains", strings.HasPrefix(arg, "--contains="),
			arg == "--no-contains", strings.HasPrefix(arg, "--no-contains="),
			arg == "--merged", strings.HasPrefix(arg, "--merged="),
			arg == "--no-merged", strings.HasPrefix(arg, "--no-merged="),
			arg == "--points-at", strings.HasPrefix(arg, "--points-at="),
			arg == "--format", strings.HasPrefix(arg, "--format="),
			arg == "--sort", strings.HasPrefix(arg, "--sort="),
			arg == "--column", strings.HasPrefix(arg, "--column="),
			arg == "--no-column", arg == "--color", strings.HasPrefix(arg, "--color="),
			arg == "--no-color", arg == "--ignore-case":
			queryMode = true
		case strings.HasPrefix(arg, "-"):
			return false
		default:
			// Without a query flag, a positional value creates a lightweight tag.
			if !queryMode {
				return false
			}
		}
	}
	return queryMode
}

func isGitTagMutationFlag(arg string) bool {
	switch {
	case arg == "-d", arg == "--delete", arg == "-f", arg == "--force",
		arg == "-a", arg == "--annotate", arg == "-s", arg == "--sign",
		arg == "-u", arg == "--local-user", strings.HasPrefix(arg, "--local-user="),
		arg == "-m", arg == "--message", strings.HasPrefix(arg, "--message="),
		arg == "--file", strings.HasPrefix(arg, "--file="),
		arg == "--cleanup", strings.HasPrefix(arg, "--cleanup="),
		arg == "--create-reflog":
		return true
	default:
		return false
	}
}

func isReadOnlyGitRemote(args []string) bool {
	if len(args) == 0 {
		return true
	}
	action := strings.ToLower(strings.TrimSpace(args[0]))
	switch action {
	case "-v", "--verbose":
		return len(args) == 1
	case "show", "get-url":
		return !containsForbiddenGitReadFlag(args[1:])
	default:
		return false
	}
}

func isReadOnlyGoCommand(args []string) bool {
	if len(args) == 0 {
		return false
	}
	sub := strings.ToLower(strings.TrimSpace(args[0]))
	rest := args[1:]
	switch sub {
	case "version", "doc", "help":
		return true
	case "env":
		for _, raw := range rest {
			arg := strings.ToLower(strings.TrimSpace(raw))
			if arg == "-w" || arg == "-u" || strings.HasPrefix(arg, "-w=") || strings.HasPrefix(arg, "-u=") {
				return false
			}
		}
		return true
	case "list":
		for _, raw := range rest {
			arg := strings.ToLower(strings.TrimSpace(raw))
			if arg == "-mod=mod" || strings.HasPrefix(arg, "-modfile=") || arg == "-modfile" {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func splitCommandFields(command string) []string {
	// Lightweight split: respects simple double quotes.
	var (
		fields []string
		cur    strings.Builder
		inQQ   bool
	)
	flush := func() {
		if cur.Len() == 0 {
			return
		}
		fields = append(fields, cur.String())
		cur.Reset()
	}
	for _, r := range command {
		switch {
		case r == '"':
			inQQ = !inQQ
		case (r == ' ' || r == '\t') && !inQQ:
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return fields
}

// ExtractShellCommand returns the primary command string from tool args.
func ExtractShellCommand(args map[string]interface{}) string {
	if cmd, ok := firstStringArg(args, "command", "cmd"); ok {
		return cmd
	}
	// batch commands: only treat as readonly if every entry is readonly.
	if raw, ok := args["commands"]; ok {
		switch typed := raw.(type) {
		case []string:
			if len(typed) == 0 {
				return ""
			}
			for _, item := range typed {
				if !IsShellReadOnlyCommand(item) {
					return item // return first non-readonly for negative path
				}
			}
			return typed[0]
		case []interface{}:
			if len(typed) == 0 {
				return ""
			}
			first := ""
			for i, item := range typed {
				text := ""
				switch v := item.(type) {
				case string:
					text = v
				case map[string]interface{}:
					text, _ = firstStringArg(v, "command", "cmd")
				}
				if i == 0 {
					first = text
				}
				if text != "" && !IsShellReadOnlyCommand(text) {
					return text
				}
			}
			return first
		}
	}
	return ""
}

// withStage prefixes a decision reason and sets Stage.
func withStage(decision Decision, stage, reason string) Decision {
	decision.Stage = stage
	reason = strings.TrimSpace(reason)
	if reason == "" {
		decision.Reason = stage
		return decision
	}
	if strings.HasPrefix(reason, stage+":") {
		decision.Reason = reason
		return decision
	}
	decision.Reason = stage + ":" + reason
	return decision
}
