package policy

import (
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/shellrisk"
)

// §4.8：remembered grants 以 §4.1 的 specifier 形态持久化，避免"记住 git *"
// 退化为字符串包含匹配。支持四种形态（大小写不敏感前缀）：
//
//	cmd:<pattern>     shell 命令；语义与规则中的命令模式完全一致
//	path:<pattern>    文件工具路径；语义与路径 specifier 一致
//	host:<pattern>    URL 工具域名；语义与域名 specifier 一致
//	exact:<text>      完整命令/路径/URL 精确相等（解释器等无法安全泛化的场景）
//
// 未带前缀的模式是历史遗留（specifier 之前的子串语义），保留原行为不变，
// 新写入的 grant 永不使用该形态。
const (
	GrantPatternCommand = "cmd:"
	GrantPatternPath    = "path:"
	GrantPatternHost    = "host:"
	GrantPatternExact   = "exact:"
)

// DeriveGrantPattern returns the narrowest remembered-grant pattern that still
// covers the approved call:
//
//   - 命令的每个段都是同一个基命令时泛化为 "cmd:<base>:*"（CommandCode 的
//     Shell(git:*) 语义）；
//   - 含多个不同基命令、解释器、破坏性基命令（rm/dd/sudo…）或无法静态解析的
//     包装时退化为 "exact:<完整命令>"：泛化只发生在"整条命令只有一个基命令且
//     该基命令不是高风险命令"的情况下；
//   - 文件工具记该次调用的路径（path:）；URL 工具记域名（host:）；
//   - 其余工具返回空模式（工具级 grant，与旧行为一致）。
func DeriveGrantPattern(toolName string, args map[string]interface{}) string {
	if commands := grantShellCommands(args); len(commands) > 0 {
		return deriveCommandGrantPattern(commands)
	}
	if path, ok := firstStringArg(args, "file_path", "path"); ok {
		return GrantPatternPath + toSlash(path)
	}
	if raw, ok := firstStringArg(args, "url"); ok {
		if host := urlHost(raw); host != "" {
			return GrantPatternHost + host
		}
		return GrantPatternExact + strings.TrimSpace(raw)
	}
	return ""
}

func deriveCommandGrantPattern(commands []string) string {
	bases := make([]string, 0, len(commands))
	for _, command := range commands {
		segments, parsed := shellrisk.Segments(command)
		if !parsed || len(segments) == 0 {
			return GrantPatternExact + strings.TrimSpace(strings.Join(commands, " && "))
		}
		for _, segment := range segments {
			fields, resolved := shellrisk.ResolveSegment(segment)
			if !resolved || len(fields) == 0 {
				return GrantPatternExact + strings.TrimSpace(strings.Join(commands, " && "))
			}
			argv0 := normalizeShellCommandName(fields[0])
			if grantCommandIsInterpreter(argv0) {
				// 解释器可执行任意代码：只记精确命令。
				return GrantPatternExact + strings.TrimSpace(strings.Join(commands, " && "))
			}
			if !containsFold(bases, argv0) {
				bases = append(bases, argv0)
			}
		}
	}
	switch len(bases) {
	case 0:
		return GrantPatternExact + strings.TrimSpace(strings.Join(commands, " && "))
	case 1:
		if !grantBaseAllowsWildcard(bases[0]) {
			return GrantPatternExact + strings.TrimSpace(strings.Join(commands, " && "))
		}
		return GrantPatternCommand + bases[0] + ":*"
	default:
		return GrantPatternExact + strings.TrimSpace(strings.Join(commands, " && "))
	}
}

// grantBaseAllowsWildcard reports whether a single base command may be
// remembered as "cmd:<base>:*". Destructive or privilege-changing bases stay
// exact so a remembered approval can never widen into "any rm/sudo/dd".
func grantBaseAllowsWildcard(argv0 string) bool {
	switch normalizeShellCommandName(argv0) {
	case "rm", "del", "erase", "rmdir", "rd", "format", "diskpart", "dd", "shred",
		"truncate", "mkfs", "fdisk", "chmod", "chown", "chgrp", "sudo", "su", "runas",
		"takeown", "icacls", "reg", "regedit", "schtasks", "netsh", "sc", "taskkill",
		"kill", "killall", "systemctl", "service", "launchctl", "crontab", "at",
		"curl", "wget", "nc", "ncat", "telnet", "ssh", "scp", "rsync", "git-lfs":
		return false
	default:
		return true
	}
}

// grantCommandIsInterpreter reports argv0 values that can execute arbitrary
// code from their arguments.
func grantCommandIsInterpreter(argv0 string) bool {
	switch normalizeShellCommandName(argv0) {
	case "sh", "bash", "zsh", "fish", "dash", "ksh", "pwsh", "powershell", "cmd",
		"python", "python3", "py", "node", "nodejs", "deno", "bun",
		"ruby", "perl", "php", "java", "osascript", "wscript", "cscript":
		return true
	default:
		return false
	}
}

// grantPatternMatches reports whether a stored grant pattern covers the call's
// arguments. The workspace root (when known) resolves "/"-anchored path
// patterns exactly like rule specifiers.
func grantPatternMatches(args map[string]interface{}, pattern, root string) bool {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return true
	}
	lower := strings.ToLower(pattern)
	switch {
	case strings.HasPrefix(lower, GrantPatternCommand):
		body := pattern[len(GrantPatternCommand):]
		commands := grantShellCommands(args)
		if len(commands) == 0 {
			return false
		}
		// Allow-side asymmetry (§4.6)：每条命令的每个段都必须命中，
		// 否则 `git status && rm -rf x` 会被 `cmd:git:*` 放行。
		for _, command := range commands {
			if !grantCommandMatches(body, command) {
				return false
			}
		}
		return true
	case strings.HasPrefix(lower, GrantPatternPath):
		body := pattern[len(GrantPatternPath):]
		path, ok := firstStringArg(args, "file_path", "path")
		if !ok {
			return false
		}
		return pathSpecifierMatches(body, path, root, true)
	case strings.HasPrefix(lower, GrantPatternHost):
		body := pattern[len(GrantPatternHost):]
		raw, ok := firstStringArg(args, "url")
		if !ok {
			return false
		}
		host := urlHost(raw)
		if host == "" {
			return false
		}
		return domainPatternMatches(body, host, true)
	case strings.HasPrefix(lower, GrantPatternExact):
		text := strings.TrimSpace(pattern[len(GrantPatternExact):])
		if text == "" {
			return false
		}
		for _, candidate := range grantTextCandidates(args) {
			if strings.EqualFold(strings.TrimSpace(candidate), text) {
				return true
			}
		}
		return false
	default:
		return legacyGrantPatternMatches(args, pattern)
	}
}

func grantCommandMatches(body, command string) bool {
	segments, parsed := shellrisk.Segments(command)
	if !parsed || len(segments) == 0 {
		return false
	}
	for _, segment := range segments {
		fields, resolved := shellrisk.ResolveSegment(segment)
		if !resolved || len(fields) == 0 {
			return false
		}
		if !commandPatternMatches(body, strings.Join(fields, " "), true) {
			return false
		}
	}
	return true
}

// legacyGrantPatternMatches keeps the pre-specifier substring semantics for
// grants written before GrantPatternCommand & co. existed.
func legacyGrantPatternMatches(args map[string]interface{}, pattern string) bool {
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

// grantShellCommands returns the shell command(s) carried by the call args.
func grantShellCommands(args map[string]interface{}) []string {
	out := make([]string, 0, 1)
	if command, ok := firstStringArg(args, "command", "cmd"); ok {
		out = append(out, command)
	}
	for _, key := range []string{"commands", "batch"} {
		switch raw := args[key].(type) {
		case []string:
			for _, command := range raw {
				if trimmed := strings.TrimSpace(command); trimmed != "" {
					out = append(out, trimmed)
				}
			}
		case []interface{}:
			for _, item := range raw {
				if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
					out = append(out, strings.TrimSpace(text))
				}
			}
		}
	}
	return out
}

// grantTextCandidates lists the raw string values an exact: pattern may compare
// against (command/path/url keys, in that order).
func grantTextCandidates(args map[string]interface{}) []string {
	out := make([]string, 0, 3)
	for _, keys := range [][]string{{"command", "cmd"}, {"file_path", "path"}, {"url"}} {
		if value, ok := firstStringArg(args, keys...); ok {
			out = append(out, value)
		}
	}
	return out
}

func containsFold(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(value, target) {
			return true
		}
	}
	return false
}

// RootedGrantFinder is an optional GrantStore extension that lets the engine
// pass the workspace root so "/"-anchored path patterns resolve like rule
// specifiers instead of failing closed.
type RootedGrantFinder interface {
	FindWithRoot(toolName string, args map[string]interface{}, root string) (Grant, bool)
}

// findRememberedGrant consults a store, preferring the rooted lookup.
func findRememberedGrant(store GrantStore, toolName string, args map[string]interface{}, root string) (Grant, bool) {
	if store == nil {
		return Grant{}, false
	}
	if rooted, ok := store.(RootedGrantFinder); ok {
		return rooted.FindWithRoot(toolName, args, root)
	}
	return store.Find(toolName, args)
}

// findGrantIn scans grants for the first entry that covers the call.
func findGrantIn(grants []Grant, toolName string, args map[string]interface{}, root string) (Grant, bool) {
	toolName = strings.ToLower(strings.TrimSpace(toolName))
	for _, grant := range grants {
		if !strings.EqualFold(strings.TrimSpace(grant.Tool), toolName) {
			continue
		}
		if grantPatternMatches(args, grant.Pattern, root) {
			return grant, true
		}
	}
	return Grant{}, false
}
