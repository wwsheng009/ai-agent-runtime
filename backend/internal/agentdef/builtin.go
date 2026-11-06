package agentdef

// ReservedAgentNames are the built-in role ids. User/project/profile
// definitions may still override them by name (documented behavior), but lint
// and management surfaces must make the override visible.
var ReservedAgentNames = []string{"explore", "plan", "general"}

func readOnlyTrue() *bool {
	v := true
	return &v
}

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
			Tools:                 []string{"view", "grep", "glob", "list_dir", "shell"},
			DisallowedTools:       []string{"write", "edit", "apply_patch", "append_write", "multiedit"},
			PermissionMode:        "plan",
			PromptMode:            PromptModeExtend,
			CompletionRequirement: CompletionNone,
			// Read-only tool surface without the workspace-only path jail: the
			// role keeps write-like tools denied, but path access follows the
			// session's external-directory admission (§4.5) like the parent.
			// Explicit sandbox configs on user/project overrides re-narrow it.
			ReadOnly:   readOnlyTrue(),
			Body:       "You are a read-only explorer. Prefer view/grep/glob/list_dir. Use shell only for read-only commands. Do not mutate the workspace.",
			SourcePath: "builtin:explore",
			Source:     SourceBuiltin,
		},
		{
			Name:                  "plan",
			Description:           "Planning agent that drafts implementation plans",
			Tools:                 []string{"view", "grep", "glob", "list_dir", "shell", "write", "edit", "apply_patch"},
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
