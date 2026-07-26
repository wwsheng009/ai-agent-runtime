package policy

import (
	"path/filepath"
	"strings"
	"sync"
)

// Decision stage identifiers for permission pipeline observability.
const (
	StageHooks         = "hooks"
	StagePolicy        = "policy"
	StageRules         = "rules"
	StageGrants        = "grants"
	StageReadonlyAuto  = "readonly_auto"
	StageMode          = "mode"
	StageCallback      = "callback"
	StageAsk           = "ask"
	StageHeadlessDeny  = "headless_deny"
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

var errDangerousGrant = monadicError("grant_rejected_dangerous_tool")

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

// IsShellReadOnlyCommand reports whether a shell/bash command is on the
// read-only allow table (git status, rg, ls, pwd, etc.).
func IsShellReadOnlyCommand(command string) bool {
	command = strings.TrimSpace(command)
	if command == "" {
		return false
	}
	// Reject obvious chaining / redirection that could hide writes.
	lowerFull := strings.ToLower(command)
	for _, bad := range []string{"&&", "||", ";", "|", ">", "<", "`", "$(", "\n"} {
		if strings.Contains(command, bad) {
			// Allow simple pipes only for known read-only pagers? keep strict for v1.
			_ = lowerFull
			return false
		}
	}

	fields := splitCommandFields(command)
	if len(fields) == 0 {
		return false
	}
	argv0 := strings.ToLower(filepath.Base(fields[0]))
	argv0 = strings.TrimSuffix(argv0, ".exe")
	argv0 = strings.TrimSuffix(argv0, ".cmd")
	argv0 = strings.TrimSuffix(argv0, ".bat")

	switch argv0 {
	case "rg", "grep", "findstr", "ag", "ack":
		return true
	case "ls", "dir", "pwd", "get-location", "get-childitem", "gci", "gl", "cat", "type", "get-content", "gc", "head", "tail", "wc", "file", "stat", "which", "where", "where.exe", "echo", "printf":
		return true
	case "git":
		if len(fields) < 2 {
			return false
		}
		sub := strings.ToLower(fields[1])
		switch sub {
		case "status", "diff", "log", "show", "branch", "tag", "remote", "rev-parse", "describe", "ls-files", "ls-tree", "blame", "shortlog", "stash":
			// git stash without drop/pop/apply is still potentially mutating for "stash push"; be conservative:
			if sub == "stash" && len(fields) > 2 {
				action := strings.ToLower(fields[2])
				switch action {
				case "list", "show":
					return true
				default:
					return false
				}
			}
			if sub == "remote" && len(fields) > 2 {
				action := strings.ToLower(fields[2])
				switch action {
				case "add", "remove", "rm", "rename", "set-url", "prune":
					return false
				}
			}
			return !containsWriteyGitFlags(fields[2:])
		default:
			return false
		}
	case "go":
		if len(fields) >= 2 {
			switch strings.ToLower(fields[1]) {
			case "env", "list", "version", "doc", "help":
				return true
			}
		}
		return false
	case "npm", "pnpm", "yarn", "cargo", "python", "python3", "node", "pip", "pip3":
		// only bare version/help style
		if len(fields) == 2 {
			switch strings.ToLower(fields[1]) {
			case "-v", "--version", "version", "help", "-h", "--help":
				return true
			}
		}
		return false
	default:
		return false
	}
}

func containsWriteyGitFlags(args []string) bool {
	for _, arg := range args {
		lower := strings.ToLower(arg)
		if lower == "--am" || lower == "--continue" || lower == "--abort" {
			return true
		}
	}
	return false
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
