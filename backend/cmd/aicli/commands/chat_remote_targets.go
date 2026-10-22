package commands

import (
	"strings"

	mcpconfig "github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/remotetargets"
)

// remoteTargetsDeclaration is the resolved `remoteTargets` file for one session.
//
// It carries ONLY the connection half of the declaration. Permissions are not a
// property of this file: a remote tool is classified by its own attributes
// (write-like / shell-like / background) exactly like a local or ordinary MCP
// tool, and the permission mode decides what the session may do with it.
//
// Before this narrowing, the declaration also fed a per-target authorization
// table plus a role-scoped delegation grant. Both duplicated what
// `--permission-mode` and `--deny-tool` already express, and the delegation half
// was unreachable by any documented UI, so it was removed (2026-10-22).
type remoteTargetsDeclaration struct {
	// Servers are the generated MCP entries, so the declaration can connect the
	// target itself instead of requiring a duplicate mcp.yaml entry (gap G7).
	Servers map[string]mcpconfig.MCPConfig
	// Warnings describe lossy or ignored configuration.
	Warnings []string
}

// loadRemoteTargetsDeclaration reads the `remoteTargets` declaration for a
// workspace.
//
// A missing file is not an error and yields a nil declaration: with no
// declaration there is nothing to connect, and the normal MCP config chain still
// applies.
func loadRemoteTargetsDeclaration(workspace string) (*remoteTargetsDeclaration, []string) {
	path := remotetargets.FindConfigFile(workspace)
	if path == "" {
		return nil, nil
	}
	expanded, err := remotetargets.LoadFile(path)
	if err != nil {
		return nil, []string{"remoteTargets " + path + ": " + err.Error()}
	}
	if len(expanded.Servers) == 0 {
		return nil, append([]string(nil), expanded.Warnings...)
	}
	return &remoteTargetsDeclaration{
		Servers:  expanded.Servers,
		Warnings: append([]string(nil), expanded.Warnings...),
	}, expanded.Warnings
}

// sessionHasDeclaredRemoteTargets reports whether the session's workspace declares
// any connectable remote target.
//
// It exists so the startup path can tell "a file that happens to list servers"
// apart from "this workspace declared the machines it works against". The former
// degrades to a warning; the latter must fail closed.
func sessionHasDeclaredRemoteTargets(session *ChatSession) bool {
	declaration, _ := loadRemoteTargetsDeclaration(chatSessionWorkspaceDir(session))
	return len(declarationServerNames(declaration)) > 0
}

// remoteTargetWarnings renders load warnings once, for the session banner.
func remoteTargetWarnings(warnings []string) []string {
	out := make([]string, 0, len(warnings))
	for _, warning := range warnings {
		if trimmed := strings.TrimSpace(warning); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
