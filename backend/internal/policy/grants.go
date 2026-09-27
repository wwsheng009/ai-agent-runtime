package policy

import (
	"path/filepath"
	"strings"
	"sync"

	"github.com/wwsheng009/ai-agent-runtime/internal/shellrisk"
)

// Decision stage identifiers for permission pipeline observability.
const (
	StageHooks        = "hooks"
	StagePolicy       = "policy"
	StageRules        = "rules"
	StageGrants       = "grants"
	StageReadonlyAuto = "readonly_auto"
	// StageShellBreaker marks the root/home removal circuit breaker, and
	// StageSensitiveWrite the sensitive-path write gate (see engine.go steps
	// 5b/5c).
	StageShellBreaker   = "shell_breaker"
	StageSensitiveWrite = "sensitive_write"
	// StageExternalDir marks the external-directory gate (§4.5): a path outside
	// the workspace / admitted roots is admitted before the call runs.
	StageExternalDir  = "external_dir"
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
	return s.FindWithRoot(toolName, args, "")
}

// FindWithRoot is Find with the workspace root available for "/"-anchored path
// patterns (RootedGrantFinder).
func (s *MemoryGrantStore) FindWithRoot(toolName string, args map[string]interface{}, root string) (Grant, bool) {
	if s == nil {
		return Grant{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return findGrantIn(s.grants, toolName, args, root)
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
	case "shell", "bash", "aicli_exec", "background_task", "task_kill", "task_monitor":
		return true
	default:
		return false
	}
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
	// ShellReadOnlyReasonUnparsable marks a command whose quoting could not be
	// tokenized; it is never auto-allowed.
	ShellReadOnlyReasonUnparsable = "unparsable_command"
	// ShellReadOnlyReasonSensitiveArg marks a read-only command whose file
	// argument names a sensitive path (e.g. "cat .env"): the command must fall
	// out of the fast path and reach the mode/approval decision.
	ShellReadOnlyReasonSensitiveArg = "sensitive_argument"
)

// ShellReadOnlyAssessment describes why a shell command is or is not accepted
// by the read-only execution boundary. The reason is intentionally stable so
// tool-result recovery can distinguish "split this batch" from a real mutation.
type ShellReadOnlyAssessment struct {
	Allowed bool
	Reason  string
	// Segment 是触发拒绝的具体命令段，SegmentIndex 是它在拆分结果中的序号
	// （1 起，P1-5）。Reason 仍是稳定码：段信息只用于把失败位置回显给模型，
	// 不参与判定，也不允许调用方据此放行。
	Segment      string
	SegmentIndex int
}

// AssessShellReadOnlyCommand validates one concrete shell command against the
// read-only allow table. Compound commands are split at &&, ||, ;, |, & and
// newlines and every segment must be on the table (§4.6): `cat a | head -5`
// is read-only, while any mutating or unresolvable segment keeps the whole
// command out of the fast path.
func AssessShellReadOnlyCommand(command string) ShellReadOnlyAssessment {
	command = strings.TrimSpace(command)
	if command == "" {
		return ShellReadOnlyAssessment{Reason: ShellReadOnlyReasonEmpty}
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

	segments, parsed := shellrisk.Segments(command)
	if !parsed {
		return ShellReadOnlyAssessment{Reason: ShellReadOnlyReasonUnparsable}
	}
	if len(segments) == 0 {
		return ShellReadOnlyAssessment{Reason: ShellReadOnlyReasonEmpty}
	}

	for index, segment := range segments {
		assessment := assessShellReadOnlySegment(segment)
		if !assessment.Allowed {
			assessment.Segment = strings.TrimSpace(segment)
			assessment.SegmentIndex = index + 1
			return assessment
		}
	}
	return ShellReadOnlyAssessment{Allowed: true}
}

// assessShellReadOnlySegment validates one independently executed segment
// after env-prefix stripping and wrapper piercing. Segments that cannot be
// resolved statically (shell interpreters, unknown wrapper shapes) are not
// allowlisted, so the fast path stays fail-closed.
func assessShellReadOnlySegment(segment string) ShellReadOnlyAssessment {
	fields, ok := shellrisk.ResolveSegment(segment)
	if !ok || len(fields) == 0 {
		return ShellReadOnlyAssessment{Reason: ShellReadOnlyReasonNotAllowed}
	}
	argv0 := normalizeShellCommandName(fields[0])

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
	case "select-object", "sort-object", "measure-object", "where-object":
		// P1-5：只读流水后段的固定 cmdlet 形态（仅静态可验证的字面量参数）。
		// 脚本块、计算属性、变量替换在命令级检查就会拒绝；这里再收紧参数形态。
		allowed = isReadOnlyPipelineCmdlet(argv0, fields[1:])
	case "npm", "pnpm", "yarn", "cargo", "python", "python3", "node", "pip", "pip3":
		allowed = isReadOnlyVersionFlag(fields[1:])
	}
	if allowed && shellReadsFileContent(argv0) {
		for _, candidate := range fields[1:] {
			if SensitiveReadArgument(candidate) {
				return ShellReadOnlyAssessment{Reason: ShellReadOnlyReasonSensitiveArg}
			}
		}
	}
	if allowed {
		return ShellReadOnlyAssessment{Allowed: true}
	}
	return ShellReadOnlyAssessment{Reason: ShellReadOnlyReasonNotAllowed}
}

// normalizeShellCommandName lowercases the argv0 basename for the read-only
// table lookup.
func normalizeShellCommandName(name string) string {
	lower := strings.ToLower(strings.TrimSpace(name))
	lower = strings.TrimSuffix(lower, ".exe")
	lower = strings.TrimSuffix(lower, ".cmd")
	lower = strings.TrimSuffix(lower, ".bat")
	return strings.ToLower(filepath.Base(lower))
}

// shellReadsFileContent reports the read-only commands whose arguments are
// files whose contents flow back to the model. Other allow-table commands
// (ls, wc, file, git status) only expose metadata and stay on the fast path.
func shellReadsFileContent(argv0 string) bool {
	switch argv0 {
	case "cat", "type", "get-content", "gc", "head", "tail":
		return true
	default:
		return false
	}
}

// isReadOnlyPipelineCmdlet 验证常用只读流水后段 cmdlet 的固定形态（P1-5）：
// 只接受静态可验证的字面量参数。脚本块、计算属性、变量替换、类型转换与
// 位置参数列表一律拒绝，宁可不放行也不做模糊归并。
func isReadOnlyPipelineCmdlet(argv0 string, args []string) bool {
	switch argv0 {
	case "select-object":
		return isReadOnlySelectObject(args)
	case "sort-object":
		return isReadOnlySortObject(args)
	case "measure-object":
		return isReadOnlyMeasureObject(args)
	case "where-object":
		return isReadOnlyWhereObject(args)
	default:
		return false
	}
}

// isReadOnlySelectObject 只接受 `-First/-Skip <非负整数字面量>`（含
// PowerShell 的 `-First:5` 形式）。Bare `Select-Object` 没有查询意义，同样拒绝。
func isReadOnlySelectObject(args []string) bool {
	if len(args) == 0 {
		return false
	}
	seen := false
	for i := 0; i < len(args); i++ {
		token := strings.ToLower(strings.TrimSpace(args[i]))
		switch {
		case token == "-first" || token == "-skip":
			if i+1 >= len(args) || !isNonNegativeIntLiteralToken(args[i+1]) {
				return false
			}
			i++
			seen = true
		case strings.HasPrefix(token, "-first:"), strings.HasPrefix(token, "-skip:"):
			if !isNonNegativeIntLiteralToken(token[strings.IndexByte(token, ':')+1:]) {
				return false
			}
			seen = true
		default:
			return false
		}
	}
	return seen
}

// isReadOnlySortObject 接受 bare `Sort-Object`、固定布尔开关、单个字面量属性名
// （`-Property Name` / `-Property:Name` / 位置形式 `Sort-Object Name`）。
func isReadOnlySortObject(args []string) bool {
	positional := 0
	for i := 0; i < len(args); i++ {
		token := strings.ToLower(strings.TrimSpace(args[i]))
		switch {
		case token == "", token == "-descending", token == "-ascending", token == "-unique", token == "-casesensitive":
			continue
		case token == "-property":
			if i+1 >= len(args) || !isReadOnlyPropertyName(args[i+1]) {
				return false
			}
			i++
		case strings.HasPrefix(token, "-property:"):
			if !isReadOnlyPropertyName(token[len("-property:"):]) {
				return false
			}
		case strings.HasPrefix(token, "-"):
			return false
		default:
			positional++
			if positional > 1 || !isReadOnlyPropertyName(args[i]) {
				return false
			}
		}
	}
	return true
}

// isReadOnlyMeasureObject 接受 bare `Measure-Object`、固定统计开关与单个字面量
// 属性名；`-Line/-Word/-Character` 只作用于输入文本，没有副作用。
func isReadOnlyMeasureObject(args []string) bool {
	positional := 0
	for i := 0; i < len(args); i++ {
		token := strings.ToLower(strings.TrimSpace(args[i]))
		switch token {
		case "", "-sum", "-average", "-minimum", "-maximum", "-line", "-word", "-character", "-ignorewhitespace":
			continue
		case "-property":
			if i+1 >= len(args) || !isReadOnlyPropertyName(args[i+1]) {
				return false
			}
			i++
		default:
			if strings.HasPrefix(token, "-property:") && isReadOnlyPropertyName(token[len("-property:"):]) {
				continue
			}
			if strings.HasPrefix(token, "-") {
				return false
			}
			positional++
			if positional > 1 || !isReadOnlyPropertyName(args[i]) {
				return false
			}
		}
	}
	return true
}

// isReadOnlyWhereObject 只接受字面量属性比较：
//
//	Where-Object -Property <Name> <Op> <Literal>
//	Where-Object <Name> <Op> <Literal>
//
// 脚本块形式（`{ $_ ... }`）、计算属性、逻辑连接符与额外 token 全部拒绝。
func isReadOnlyWhereObject(args []string) bool {
	if len(args) == 0 {
		return false
	}
	index := 0
	if strings.EqualFold(strings.TrimSpace(args[0]), "-property") {
		if len(args) < 4 || !isReadOnlyPropertyName(args[1]) {
			return false
		}
		index = 2
	} else if strings.HasPrefix(strings.TrimSpace(args[0]), "-") {
		return false
	} else if !isReadOnlyPropertyName(args[0]) {
		return false
	} else {
		index = 1
	}
	if index+1 >= len(args) || index+2 != len(args) {
		return false
	}
	if !isReadOnlyWhereOperator(args[index]) {
		return false
	}
	return isReadOnlyWhereLiteral(args[index+1])
}

func isReadOnlyWhereOperator(token string) bool {
	switch strings.ToLower(strings.TrimSpace(token)) {
	case "-eq", "-ne", "-gt", "-ge", "-lt", "-le",
		"-like", "-notlike", "-match", "-notmatch",
		"-contains", "-notcontains":
		return true
	default:
		return false
	}
}

// isReadOnlyWhereLiteral 接受数字/单位字面量、引号字符串与普通词元；其余形态
// （子表达式、类型转换、变量、属性解引用）一律拒绝。
func isReadOnlyWhereLiteral(token string) bool {
	token = strings.TrimSpace(token)
	if token == "" {
		return false
	}
	if quote := token[:1]; quote == `"` || quote == `'` {
		return len(token) >= 2 && strings.HasSuffix(token, quote)
	}
	return isReadOnlyWordToken(token)
}

// isReadOnlyPropertyName 接受属性名形态（字母数字开头，可含 . _ -）。
func isReadOnlyPropertyName(token string) bool {
	token = strings.TrimSpace(token)
	if token == "" || strings.HasPrefix(token, "-") {
		return false
	}
	return isReadOnlyWordToken(token)
}

// isReadOnlyWordToken 接受字母/数字与 . _ - / * 组成的普通词元（含 100kb 这类
// PowerShell 字面量与 *.log 这类通配字面量）。
func isReadOnlyWordToken(token string) bool {
	for _, r := range token {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.', r == '_', r == '-', r == '/', r == '*':
		default:
			return false
		}
	}
	return true
}

// isNonNegativeIntLiteralToken 只接受纯十进制非负整数字面量。
func isNonNegativeIntLiteralToken(token string) bool {
	token = strings.TrimSpace(token)
	if token == "" {
		return false
	}
	for _, r := range token {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
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
	// Leading global options that keep the following subcommand read-only:
	// `-C <dir>` only changes the repository directory and `--no-pager` suppresses
	// the pager. Every other leading option stays rejected: `-c`/`--config-env`
	// can install hooks or a pager that executes code under a read-only-looking
	// subcommand, so the classifier must not skip over them.
	for len(args) > 0 {
		raw := strings.TrimSpace(args[0])
		token := strings.ToLower(raw)
		if !strings.HasPrefix(token, "-") {
			break // reached the subcommand
		}
		switch {
		case token == "--no-pager", token == "--no-optional-locks":
			args = args[1:]
			continue
		case raw == "-C":
			// `-C <dir>` only changes the repository directory; the original case
			// matters here because `-c key=value` (lowercase) stays rejected.
			if len(args) < 2 {
				return false
			}
			args = args[2:]
			continue
		default:
			return false
		}
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
