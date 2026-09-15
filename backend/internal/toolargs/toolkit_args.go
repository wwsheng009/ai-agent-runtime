package toolargs

import (
	"sort"
	"strings"
)

// ToolkitArgAliasPair binds the canonical argument name a built-in toolkit tool
// reads to the alternative names the model may send for it. Aliases are ordered
// by preference: the first alias present in the call wins.
type ToolkitArgAliasPair struct {
	Canonical string
	Aliases   []string
}

// ToolkitArgAliases describes every argument name one built-in toolkit tool
// accepts. Args lists top-level pairs; ListFields maps an array-of-objects
// argument (files/commands/edits) to the pairs its items accept.
//
// This table is the single source of truth for built-in argument naming: the
// executor uses it to promote aliases before execution (internal/tools), and the
// runtime policy uses it to know which argument names must be inspected for
// sandbox/path/command rules (internal/policy). Keeping one table is what makes
// "the policy inspects the same argument the executor reads" a structural
// property instead of an assumption.
type ToolkitArgAliases struct {
	Args       []ToolkitArgAliasPair
	ListFields map[string][]ToolkitArgAliasPair
}

func pairs(canonical string, aliases ...string) ToolkitArgAliasPair {
	return ToolkitArgAliasPair{Canonical: canonical, Aliases: aliases}
}

var toolkitFileArgPair = pairs("file_path", "path", "file", "filename", "filePath")
var toolkitCommandArgPair = pairs("command", "cmd", "script", "shell_command")
var toolkitWorkdirArgPair = pairs("workdir", "cwd", "working_directory")
var toolkitSearchPathArgPair = pairs("path", "root", "directory", "search_path")
var toolkitOldStringArgPair = pairs("old_string", "old_text", "old", "oldString")
var toolkitNewStringArgPair = pairs("new_string", "new_text", "new", "replacement", "newString")

// toolkitArgAliases maps a built-in tool name to the argument names it accepts.
var toolkitArgAliases = map[string]ToolkitArgAliases{
	"view": {
		Args: []ToolkitArgAliasPair{toolkitFileArgPair},
		ListFields: map[string][]ToolkitArgAliasPair{
			"files": {toolkitFileArgPair},
		},
	},
	"edit": {
		Args: []ToolkitArgAliasPair{toolkitFileArgPair, toolkitOldStringArgPair, toolkitNewStringArgPair},
	},
	"write": {
		Args: []ToolkitArgAliasPair{toolkitFileArgPair, pairs("content", "text", "data")},
	},
	"append_write": {
		Args: []ToolkitArgAliasPair{toolkitFileArgPair, pairs("content", "text", "data")},
	},
	"multiedit": {
		Args: []ToolkitArgAliasPair{toolkitFileArgPair},
		ListFields: map[string][]ToolkitArgAliasPair{
			"edits": {toolkitOldStringArgPair, toolkitNewStringArgPair},
		},
	},
	"shell": {
		Args: []ToolkitArgAliasPair{toolkitCommandArgPair, toolkitWorkdirArgPair},
		ListFields: map[string][]ToolkitArgAliasPair{
			"commands": {toolkitCommandArgPair, toolkitWorkdirArgPair},
		},
	},
	"bash": {
		Args: []ToolkitArgAliasPair{toolkitCommandArgPair, toolkitWorkdirArgPair},
		ListFields: map[string][]ToolkitArgAliasPair{
			"commands": {toolkitCommandArgPair, toolkitWorkdirArgPair},
		},
	},
	"execute_shell_command": {
		Args: []ToolkitArgAliasPair{toolkitCommandArgPair, toolkitWorkdirArgPair},
		ListFields: map[string][]ToolkitArgAliasPair{
			"commands": {toolkitCommandArgPair, toolkitWorkdirArgPair},
		},
	},
	"grep": {
		Args: []ToolkitArgAliasPair{
			pairs("pattern", "query", "search", "regex"),
			pairs("patterns", "queries", "searches"),
			toolkitSearchPathArgPair,
		},
	},
	"glob": {
		Args: []ToolkitArgAliasPair{
			pairs("pattern", "glob", "glob_pattern"),
			toolkitSearchPathArgPair,
		},
	},
	"ls": {
		Args: []ToolkitArgAliasPair{toolkitSearchPathArgPair},
	},
	"download": {
		Args: []ToolkitArgAliasPair{
			pairs("url", "uri"),
			pairs("file_path", "path", "target", "target_path"),
		},
	},
	"apply_patch": {
		Args: []ToolkitArgAliasPair{pairs("patch", "diff", "input", "patch_text")},
	},
}

// NormalizeToolkitToolName canonicalizes a tool name for built-in lookups.
func NormalizeToolkitToolName(toolName string) string {
	return strings.ToLower(strings.TrimSpace(toolName))
}

// ToolkitArgAliasesFor returns the argument aliases of a built-in toolkit tool.
// The second result is false for tools the runtime does not own (MCP tools keep
// their provider-defined arguments untouched).
func ToolkitArgAliasesFor(toolName string) (ToolkitArgAliases, bool) {
	aliases, ok := toolkitArgAliases[NormalizeToolkitToolName(toolName)]
	return aliases, ok
}

// ToolkitToolNames lists every built-in tool that declares argument aliases, in
// sorted order. It lets consumers enumerate the table (for example a drift guard
// that checks every declared argument is classified by the runtime policy).
func ToolkitToolNames() []string {
	names := make([]string, 0, len(toolkitArgAliases))
	for name := range toolkitArgAliases {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
