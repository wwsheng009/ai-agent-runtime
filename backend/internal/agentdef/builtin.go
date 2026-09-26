package agentdef

// ReservedAgentNames are the built-in role ids. User/project/profile
// definitions may still override them by name (documented behavior), but lint
// and management surfaces must make the override visible.
var ReservedAgentNames = []string{"explore", "plan", "general"}

// IsReservedAgentName reports whether name (normalized) is a built-in role id.
func IsReservedAgentName(name string) bool {
	normalized := normalizeAgentName(name)
	for _, reserved := range ReservedAgentNames {
		if normalized == reserved {
			return true
		}
	}
	return false
}

// BuiltinDefinitions returns the minimal built-in role stubs (explore / plan / general).
// Project and user definitions override these by name during discovery.
func BuiltinDefinitions() []*Definition {
	return []*Definition{
		{
			Name:                  "explore",
			Description:           "Read-only codebase explorer",
			Tools:                 []string{"view", "grep", "glob", "ls", "shell"},
			DisallowedTools:       []string{"write", "edit", "apply_patch", "append_write", "multiedit"},
			PermissionMode:        "plan",
			PromptMode:            PromptModeExtend,
			CompletionRequirement: CompletionNone,
			Sandbox:               "read-only",
			Body:                  "You are a read-only explorer. Prefer view/grep/glob/ls. Use shell only for read-only commands. Do not mutate the workspace.",
			SourcePath:            "builtin:explore",
			Source:                SourceBuiltin,
		},
		{
			Name:                  "plan",
			Description:           "Planning agent that drafts implementation plans",
			Tools:                 []string{"view", "grep", "glob", "ls", "shell", "write", "edit", "apply_patch"},
			PermissionMode:        "plan",
			PromptMode:            PromptModeExtend,
			CompletionRequirement: CompletionNone,
			Sandbox:               "workspace",
			Body:                  "You are a planning agent. Explore the codebase, then write or update a clear plan (prefer plan.md). Do not implement beyond the plan unless asked.",
			SourcePath:            "builtin:plan",
			Source:                SourceBuiltin,
		},
		{
			Name:                  "general",
			Description:           "General-purpose coding agent",
			Tools:                 nil, // inherit full toolkit
			PermissionMode:        "default",
			PromptMode:            PromptModeExtend,
			CompletionRequirement: CompletionNone,
			Sandbox:               "workspace",
			Body:                  "You are a general-purpose coding agent. Prefer precise, verified changes and follow repository conventions.",
			SourcePath:            "builtin:general",
			Source:                SourceBuiltin,
		},
	}
}
