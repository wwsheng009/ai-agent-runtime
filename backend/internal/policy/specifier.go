package policy

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/shellrisk"
)

// This file implements the rule-specifier syntax of
// docs/analysis/commandcode-permissions-design-borrowing-20260926.md §4.1/§4.6/§4.10:
//
//	Rule = Tool | Tool(specifier)
//
//   - Shell(...) / Bash(...)       command pattern, matched per compound segment
//   - Read(...) / Edit(...)        path pattern with //, ~/, / and relative anchors
//   - WebFetch(domain:...)         host pattern for network tools
//   - Tool(param:value)            top-level argument match (deny/ask only)
//   - mcp__server__* / edit_*      tool-name globs (deny/ask; allow must be explicit)
//
// Plain tool names keep their legacy exact-match semantics, so existing
// permissions.yaml files behave unchanged.

// SpecifierKind identifies the matched field of a specifier entry.
type SpecifierKind string

const (
	SpecifierKindCommand  SpecifierKind = "command"
	SpecifierKindPath     SpecifierKind = "path"
	SpecifierKindDomain   SpecifierKind = "domain"
	SpecifierKindToolGlob SpecifierKind = "tool_glob"
	SpecifierKindParam    SpecifierKind = "param"
)

// ToolSpecifier is one parsed `Tool(specifier)` (or tool-name glob) entry.
type ToolSpecifier struct {
	Kind SpecifierKind
	// Group is true when the entry used a friendly group head (Shell, Read,
	// Edit, WebFetch) instead of a concrete tool name.
	Group bool
	// Head is the normalized head as written (e.g. "Shell", "shell", "edit").
	Head string
	// Pattern is the command/path/domain/glob text; empty for param specs.
	Pattern string
	// Param is the top-level argument name for SpecifierKindParam.
	Param string
}

// friendlySpecifierGroups maps the documented friendly names to a tool group.
// Only the parenthesized (specifier) form uses the group semantics; a bare
// `shell` entry stays an exact tool name for backward compatibility.
var friendlySpecifierGroups = map[string]string{
	"shell":      "shell",
	"bash":       "shell",
	"sh":         "shell",
	"zsh":        "shell",
	"pwsh":       "shell",
	"powershell": "shell",
	"exec":       "shell",

	"read": "read",
	"view": "read",
	"grep": "read",

	"edit":  "edit",
	"write": "edit",
	"patch": "edit",

	"webfetch":  "webfetch",
	"web_fetch": "webfetch",
	"fetch":     "webfetch",
	"web":       "webfetch",
}

var readGroupTools = map[string]bool{
	"view": true, "read": true, "read_file": true, "readfile": true,
	"file_read": true, "grep": true, "glob": true, "ls": true,
	"list_dir": true, "listdir": true, "filebrowse": true, "file_browse": true,
}

var editGroupTools = map[string]bool{
	"write": true, "write_file": true, "create_file": true, "overwrite_file": true,
	"edit": true, "update_file": true, "str_replace": true, "str_replace_editor": true,
	"multiedit": true, "multi_edit": true, "append_write": true, "append_file": true,
	"apply_patch": true, "applypatch": true, "patch_file": true,
	"delete_file": true, "remove_file": true, "move_file": true, "rename_file": true,
	"mkdir": true, "notebook_edit": true,
}

var webFetchGroupTools = map[string]bool{
	"fetch": true, "web_fetch": true, "webfetch": true, "download": true,
	"http_request": true, "http_get": true, "curl": true,
}

// ownedSpecifierArgNames are tool fields that must be expressed with the
// command/path/domain form; the param form is rejected for them (§4.10).
var ownedSpecifierArgNames = map[string]bool{
	"command": true, "cmd": true, "commands": true,
	"file_path": true, "path": true, "paths": true,
	"url": true, "urls": true, "patch": true, "diff": true,
}

// ParseToolSpecifier parses one `tools:` entry. It returns (nil, nil) when the
// entry is a plain tool name that must keep its legacy exact-match semantics.
// decision controls the allow-side restrictions of §4.10: broad globs and param
// patterns are only legal in deny/ask rules.
func ParseToolSpecifier(raw string, decision DecisionType) (*ToolSpecifier, error) {
	entry := strings.TrimSpace(raw)
	if entry == "" {
		return nil, nil
	}

	if open := strings.IndexByte(entry, '('); open > 0 && strings.HasSuffix(entry, ")") {
		head := strings.TrimSpace(entry[:open])
		inner := strings.TrimSpace(entry[open+1 : len(entry)-1])
		if head == "" || inner == "" {
			return nil, fmt.Errorf("invalid specifier %q: empty tool or pattern", entry)
		}
		if group, ok := friendlySpecifierGroups[strings.ToLower(head)]; ok {
			return parseGroupSpecifier(head, group, inner, decision)
		}
		return parseConcreteToolSpecifier(head, inner, decision)
	}

	if strings.Contains(entry, "*") {
		spec := &ToolSpecifier{Kind: SpecifierKindToolGlob, Head: entry, Pattern: entry}
		if decision == DecisionAllow {
			if err := validateAllowToolGlob(entry); err != nil {
				return nil, err
			}
		}
		return spec, nil
	}
	return nil, nil
}

func parseGroupSpecifier(head, group, inner string, decision DecisionType) (*ToolSpecifier, error) {
	switch group {
	case "shell":
		if looksLikeParamPattern(inner) {
			param, value, err := splitParamPattern(inner)
			if err != nil {
				return nil, fmt.Errorf("specifier %s(%s): %w", head, inner, err)
			}
			if ownedSpecifierArgNames[strings.ToLower(param)] {
				return nil, fmt.Errorf("specifier %s(%s): field %q must use the command/path/domain form", head, inner, param)
			}
			if decision == DecisionAllow {
				return nil, fmt.Errorf("specifier %s(%s): parameter patterns are only allowed in deny/ask rules", head, inner)
			}
			return &ToolSpecifier{Kind: SpecifierKindParam, Group: true, Head: head, Param: param, Pattern: value}, nil
		}
		return &ToolSpecifier{Kind: SpecifierKindCommand, Group: true, Head: head, Pattern: inner}, nil
	case "read", "edit":
		return &ToolSpecifier{Kind: SpecifierKindPath, Group: true, Head: head, Pattern: inner}, nil
	case "webfetch":
		pattern := strings.TrimSpace(inner)
		if strings.HasPrefix(strings.ToLower(pattern), "domain:") {
			pattern = strings.TrimSpace(pattern[len("domain:"):])
		}
		if pattern == "" {
			return nil, fmt.Errorf("specifier %s(%s): empty domain pattern", head, inner)
		}
		return &ToolSpecifier{Kind: SpecifierKindDomain, Group: true, Head: head, Pattern: pattern}, nil
	default:
		return nil, fmt.Errorf("specifier %s(%s): unknown specifier group", head, inner)
	}
}

func parseConcreteToolSpecifier(head, inner string, decision DecisionType) (*ToolSpecifier, error) {
	toolName := normalizeToolName(head)
	if looksLikeParamPattern(inner) {
		param, value, err := splitParamPattern(inner)
		if err != nil {
			return nil, fmt.Errorf("specifier %s(%s): %w", head, inner, err)
		}
		if ownedSpecifierArgNames[strings.ToLower(param)] {
			return nil, fmt.Errorf("specifier %s(%s): field %q must use the command/path/domain form", head, inner, param)
		}
		if decision == DecisionAllow {
			return nil, fmt.Errorf("specifier %s(%s): parameter patterns are only allowed in deny/ask rules", head, inner)
		}
		return &ToolSpecifier{Kind: SpecifierKindParam, Head: head, Param: param, Pattern: value}, nil
	}
	switch {
	case IsShellLikeToolName(toolName):
		return &ToolSpecifier{Kind: SpecifierKindCommand, Head: head, Pattern: inner}, nil
	case readGroupTools[toolName], editGroupTools[toolName], isPlanWriteToolName(toolName):
		return &ToolSpecifier{Kind: SpecifierKindPath, Head: head, Pattern: inner}, nil
	case webFetchGroupTools[toolName]:
		pattern := inner
		if strings.HasPrefix(strings.ToLower(pattern), "domain:") {
			pattern = strings.TrimSpace(pattern[len("domain:"):])
		}
		return &ToolSpecifier{Kind: SpecifierKindDomain, Head: head, Pattern: pattern}, nil
	default:
		return nil, fmt.Errorf("specifier %s(%s): unknown tool for a specifier (use Shell/Read/Edit/WebFetch or a known tool name)", head, inner)
	}
}

func looksLikeParamPattern(text string) bool {
	colon := strings.IndexByte(text, ':')
	if colon <= 0 {
		return false
	}
	name := text[:colon]
	if !isSpecifierIdentifier(name) {
		return false
	}
	// Owned fields always parse as the parameter form so the caller can reject
	// them with a precise "use the command/path form" error.
	if ownedSpecifierArgNames[strings.ToLower(name)] {
		return true
	}
	value := text[colon+1:]
	if value == "" {
		return false
	}
	// `git:*` is the documented command-prefix form, not a parameter pattern.
	if value == "*" {
		return false
	}
	// Command patterns such as `git log --format=%h:%s` or Windows paths
	// (`Shell(C:\x)`) must not be mistaken for parameter patterns.
	if strings.ContainsAny(value, `/\`) {
		return false
	}
	if strings.Contains(value, " ") && !strings.HasSuffix(value, "*") {
		return false
	}
	// A bare word before the colon is far more likely a command prefix
	// (`git:status`) than a parameter name; require a parameter-looking name
	// (snake_case or one of the common non-owned shell arguments).
	if !strings.Contains(name, "_") && !commonShellSpecifierParams[strings.ToLower(name)] {
		return false
	}
	return true
}

// commonShellSpecifierParams are non-owned shell arguments that may be matched
// with the param form without an underscore in their name.
var commonShellSpecifierParams = map[string]bool{
	"cwd": true, "timeout": true, "background": true, "env": true, "stdin": true,
	"runinbackground": true, "shell": true, "description": true,
}

func splitParamPattern(text string) (string, string, error) {
	colon := strings.IndexByte(text, ':')
	if colon <= 0 {
		return "", "", fmt.Errorf("invalid parameter pattern %q (want param:value)", text)
	}
	name := strings.TrimSpace(text[:colon])
	value := strings.TrimSpace(text[colon+1:])
	if !isSpecifierIdentifier(name) {
		return "", "", fmt.Errorf("invalid parameter name %q", name)
	}
	if value == "" {
		return "", "", fmt.Errorf("invalid parameter value in %q", text)
	}
	return name, value, nil
}

func isSpecifierIdentifier(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		switch {
		case r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'):
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// validateAllowToolGlob rejects broad globs in allow rules: an allow rule must
// name the concrete namespace it authorizes (§4.10).
func validateAllowToolGlob(pattern string) error {
	trimmed := strings.ToLower(strings.TrimSpace(pattern))
	switch {
	case trimmed == "", trimmed == "*":
		return fmt.Errorf("allow rule cannot use the bare wildcard %q; name a tool, a server, or a concrete prefix", pattern)
	case strings.HasPrefix(trimmed, "*"):
		return fmt.Errorf("allow rule cannot start with a wildcard %q; name the concrete tool or server first", pattern)
	}
	if strings.HasPrefix(trimmed, "mcp__") {
		server := strings.TrimPrefix(trimmed, "mcp__")
		server = strings.TrimSuffix(server, "_*")
		if server == "" || server == "*" || strings.Contains(server, "*") {
			return fmt.Errorf("allow rule %q must name a concrete MCP server (e.g. mcp__github__get_*)", pattern)
		}
	}
	return nil
}

// MatchesTool reports whether the specifier applies to a request and returns a
// human-readable match detail for diagnostics. root anchors workspace-relative
// path patterns (/ and relative); decision controls the allow-side
// conservative semantics: every path/segment must match for an allow rule.
func (s ToolSpecifier) MatchesTool(root string, req EvalRequest, decision DecisionType) (bool, string) {
	if !s.toolScopeMatches(req.ToolName) {
		return false, ""
	}
	switch s.Kind {
	case SpecifierKindToolGlob:
		return true, "tool=" + s.Pattern
	case SpecifierKindCommand:
		return s.matchShellCommand(req, decision)
	case SpecifierKindPath:
		return s.matchPaths(root, req, decision)
	case SpecifierKindDomain:
		return s.matchDomains(req, decision)
	case SpecifierKindParam:
		return s.matchParam(req)
	default:
		return false, ""
	}
}

func (s ToolSpecifier) toolScopeMatches(toolName string) bool {
	canonical := normalizeToolName(toolName)
	if s.Kind == SpecifierKindToolGlob {
		return globMatch(s.Pattern, canonical, 0, true)
	}
	if !s.Group {
		return canonical == normalizeToolName(s.Head)
	}
	group, ok := friendlySpecifierGroups[strings.ToLower(s.Head)]
	if !ok {
		return false
	}
	switch group {
	case "shell":
		return IsShellLikeToolName(toolName)
	case "read":
		return readGroupTools[canonical]
	case "edit":
		return editGroupTools[canonical] || isPlanWriteToolName(canonical)
	case "webfetch":
		return webFetchGroupTools[canonical]
	default:
		return false
	}
}

func (s ToolSpecifier) matchShellCommand(req EvalRequest, decision DecisionType) (bool, string) {
	commands := shellBreakerCommands(req)
	if len(commands) == 0 {
		return false, ""
	}
	allow := decision == DecisionAllow
	for _, command := range commands {
		segments, parsed := shellrisk.Segments(command)
		if !parsed && allow {
			// Allow rules never auto-allow a command that cannot be parsed.
			return false, ""
		}
		if len(segments) == 0 {
			if allow {
				return false, ""
			}
			continue
		}
		for _, segment := range segments {
			fields, resolved := shellrisk.ResolveSegment(segment)
			candidate := strings.TrimSpace(segment)
			if resolved && len(fields) > 0 {
				candidate = strings.Join(fields, " ")
			} else if allow {
				// Unresolvable segment (wrappers, substitutions): conservatively
				// refuse the whole allow match.
				return false, ""
			}
			if commandPatternMatches(s.Pattern, candidate, allow) {
				if !allow {
					return true, "segment=" + candidate
				}
				continue
			}
			if allow {
				return false, ""
			}
		}
	}
	if allow {
		return true, "command=" + strings.Join(commands, " ; ")
	}
	return false, ""
}

func (s ToolSpecifier) matchPaths(root string, req EvalRequest, decision DecisionType) (bool, string) {
	paths := collectPathArgs(req.Args, policyArgKeysForTool(req.ToolName))
	if len(paths) == 0 {
		return false, ""
	}
	allow := decision == DecisionAllow
	matched := ""
	for _, path := range paths {
		// deny/ask fold case; allow rules stay exact (except on Windows, where
		// the filesystem itself is case-insensitive).
		if pathSpecifierMatches(s.Pattern, path, root, !allow) {
			matched = path
			if !allow {
				return true, "path=" + path
			}
			continue
		}
		if allow {
			return false, ""
		}
	}
	if allow && matched != "" {
		return true, "paths=" + strings.Join(paths, ",")
	}
	return false, ""
}

func (s ToolSpecifier) matchDomains(req EvalRequest, decision DecisionType) (bool, string) {
	keys := policyArgKeysForTool(req.ToolName)
	urls := collectStringArgs(req.Args, keys.url)
	if len(urls) == 0 {
		return false, ""
	}
	allow := decision == DecisionAllow
	matched := ""
	for _, raw := range urls {
		host := urlHost(raw)
		if host == "" {
			if allow {
				return false, ""
			}
			continue
		}
		if domainPatternMatches(s.Pattern, host, allow) {
			matched = host
			if !allow {
				return true, "host=" + host
			}
			continue
		}
		if allow {
			return false, ""
		}
	}
	if allow && matched != "" {
		return true, "host=" + matched
	}
	return false, ""
}

func (s ToolSpecifier) matchParam(req EvalRequest) (bool, string) {
	raw, ok := lookupArgCaseInsensitive(req.Args, s.Param)
	if !ok {
		// A parameter the model did not send must not match (§4.10).
		return false, ""
	}
	value := argStringValue(raw)
	if value == "" {
		return false, ""
	}
	if paramPatternMatches(s.Pattern, value) {
		return true, s.Param + "=" + value
	}
	return false, ""
}

// commandPatternMatches implements the space-sensitive command matching:
// exact equality, `prefix:*`, and `*`/`?` globs over the normalized argv text.
func commandPatternMatches(pattern, command string, fold bool) bool {
	pattern = strings.TrimSpace(pattern)
	command = strings.TrimSpace(command)
	if pattern == "" || command == "" {
		return false
	}
	if strings.HasSuffix(pattern, ":*") {
		prefix := strings.TrimSuffix(pattern, ":*")
		if fold {
			return strings.EqualFold(command, prefix) || strings.HasPrefix(strings.ToLower(command), strings.ToLower(prefix)+" ")
		}
		return command == prefix || strings.HasPrefix(command, prefix+" ")
	}
	if strings.ContainsAny(pattern, "*?") {
		return globMatch(pattern, command, 0, fold)
	}
	if fold {
		return strings.EqualFold(pattern, command)
	}
	return pattern == command
}

// pathSpecifierMatches resolves one path pattern against a call target:
//
//	//rest   filesystem-absolute
//	~/rest  home-absolute
//	/rest   workspace-root anchored
//	rest    workspace-relative at any depth (basename when it has no separator)
func pathSpecifierMatches(pattern, target, root string, fold bool) bool {
	pattern = strings.TrimSpace(pattern)
	target = strings.TrimSpace(target)
	if pattern == "" || target == "" {
		return false
	}
	if runtime.GOOS == "windows" {
		fold = true
	}
	slashPattern := toSlash(pattern)
	slashTarget := toSlash(filepath.Clean(target))
	targetAbs := filepath.IsAbs(target)
	if !targetAbs {
		if root == "" {
			abs, err := filepath.Abs(target)
			if err == nil {
				slashTarget = toSlash(filepath.Clean(abs))
				targetAbs = true
			}
		} else {
			slashTarget = toSlash(filepath.Clean(filepath.Join(root, target)))
			targetAbs = true
		}
	}

	switch {
	case strings.HasPrefix(slashPattern, "//"):
		// Filesystem-absolute: compare in the platform's filesystem form
		// without anchoring to the workspace root.
		fsTarget := toSlash(filepath.Clean(target))
		if !filepath.IsAbs(target) && !strings.HasPrefix(fsTarget, "/") {
			return false
		}
		return globMatch(slashPattern[1:], filesystemForm(fsTarget), '/', fold)
	case strings.HasPrefix(slashPattern, "~/"):
		home, err := os.UserHomeDir()
		if err != nil || strings.TrimSpace(home) == "" {
			return false
		}
		patternPath := toSlash(filepath.Join(home, slashPattern[2:]))
		return globMatch(patternPath, slashTarget, '/', fold)
	case strings.HasPrefix(slashPattern, "/"):
		if strings.TrimSpace(root) == "" {
			return false
		}
		patternPath := toSlash(filepath.Join(root, slashPattern[1:]))
		return globMatch(patternPath, slashTarget, '/', fold)
	default:
		if !strings.Contains(slashPattern, "/") {
			base := target
			if targetAbs {
				base = filepath.Base(filepath.FromSlash(slashTarget))
			}
			return globMatch(slashPattern, toSlash(base), '/', fold)
		}
		relative := ""
		if root != "" {
			if rel, err := filepath.Rel(root, filepath.FromSlash(slashTarget)); err == nil && !strings.HasPrefix(rel, "..") {
				relative = toSlash(rel)
			}
		} else if !targetAbs {
			relative = slashTarget
		}
		if relative == "" {
			// No workspace anchor known: fall back to any-depth suffix matching
			// on the absolute target so relative rules keep working.
			relative = strings.TrimPrefix(slashTarget, "/")
		}
		for {
			if globMatch(slashPattern, relative, '/', fold) {
				return true
			}
			next := strings.IndexByte(relative, '/')
			if next < 0 {
				return false
			}
			relative = relative[next+1:]
		}
	}
}

// filesystemForm strips a Windows volume so `//etc/x` can match C:\etc\x; the
// pattern itself carries no volume in the documented syntax.
func filesystemForm(slashTarget string) string {
	if runtime.GOOS != "windows" {
		return slashTarget
	}
	volume := filepath.VolumeName(filepath.FromSlash(slashTarget))
	if volume == "" {
		return slashTarget
	}
	trimmed := strings.TrimPrefix(slashTarget, toSlash(volume))
	if trimmed == "" {
		return "/"
	}
	return trimmed
}

func domainPatternMatches(pattern, host string, fold bool) bool {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	host = strings.ToLower(strings.TrimSpace(host))
	if pattern == "" || host == "" {
		return false
	}
	if strings.ContainsAny(pattern, "*?") {
		// `*.example.com` requires the literal suffix after the star, so the
		// apex never matches while any subdomain depth does.
		return globMatch(pattern, host, 0, fold)
	}
	if fold {
		return strings.EqualFold(pattern, host)
	}
	return pattern == host
}

func paramPatternMatches(pattern, value string) bool {
	pattern = strings.TrimSpace(pattern)
	value = strings.TrimSpace(value)
	if pattern == "" || value == "" {
		return false
	}
	if strings.HasSuffix(pattern, "*") {
		prefix := strings.TrimSuffix(pattern, "*")
		return strings.HasPrefix(strings.ToLower(value), strings.ToLower(prefix))
	}
	return strings.EqualFold(pattern, value)
}

func lookupArgCaseInsensitive(args map[string]interface{}, name string) (interface{}, bool) {
	if len(args) == 0 {
		return nil, false
	}
	if raw, ok := args[name]; ok {
		return raw, true
	}
	for key, value := range args {
		if strings.EqualFold(strings.TrimSpace(key), name) {
			return value, true
		}
	}
	return nil, false
}

func argStringValue(raw interface{}) string {
	switch typed := raw.(type) {
	case string:
		return strings.TrimSpace(typed)
	case bool:
		if typed {
			return "true"
		}
		return "false"
	case float64:
		return strings.TrimSpace(fmt.Sprintf("%v", typed))
	default:
		return ""
	}
}

func urlHost(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Host == "" {
		if !strings.Contains(trimmed, "://") {
			if parsed, err := url.Parse("//" + trimmed); err == nil {
				return strings.ToLower(parsed.Hostname())
			}
		}
		return ""
	}
	return strings.ToLower(parsed.Hostname())
}

func toSlash(path string) string {
	return strings.ReplaceAll(path, `\`, "/")
}

// globMatch matches a `*`/`**`/`?` pattern. `*` never crosses sep, `**` does.
func globMatch(pattern, target string, sep byte, fold bool) bool {
	if fold {
		pattern = strings.ToLower(pattern)
		target = strings.ToLower(target)
	}
	return globMatchAt(pattern, target, sep)
}

func globMatchAt(pattern, target string, sep byte) bool {
	for len(pattern) > 0 {
		switch pattern[0] {
		case '*':
			stars := 0
			for len(pattern) > 0 && pattern[0] == '*' {
				stars++
				pattern = pattern[1:]
			}
			// Two or more stars mean `**`, which crosses separators.
			cross := stars >= 2
			if len(pattern) == 0 {
				if cross {
					return true
				}
				return sep == 0 || !strings.ContainsRune(target, rune(sep))
			}
			limit := len(target)
			if !cross && sep != 0 {
				if idx := strings.IndexByte(target, sep); idx >= 0 {
					limit = idx
				}
			}
			for i := 0; i <= limit; i++ {
				if globMatchAt(pattern, target[i:], sep) {
					return true
				}
			}
			return false
		case '?':
			if len(target) == 0 || (sep != 0 && target[0] == sep) {
				return false
			}
			pattern, target = pattern[1:], target[1:]
		default:
			if len(target) == 0 || pattern[0] != target[0] {
				return false
			}
			pattern, target = pattern[1:], target[1:]
		}
	}
	return len(target) == 0
}
