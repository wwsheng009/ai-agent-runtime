//go:build win7compat

// The Windows 7 compatible build disables MCP wholesale (see
// manager_win7compat.go and skill's mcp_adapter_win7compat.go), and with it the
// remote-target integration: the standard files here build remote MCP server
// entries through internal/remotetools, whose server half needs the MCP SDK and
// cannot be compiled by the Go 1.21.4 win7 toolchain. This file keeps the
// package's export surface identical so the chat flag/config plumbing still
// compiles, but every entry point fails closed: `--remote` reports that remote
// targets are unavailable, and a `remoteTargets` file is treated as absent.
package remotetargets

import (
	"errors"

	mcpconfig "github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
)

// errWin7Unsupported is the single "this build has no remote targets" error.
var errWin7Unsupported = errors.New("remote targets are not supported in the Windows 7 compatible build")

// SpecDepth selects the capability grant of a spec-derived target.
type SpecDepth string

const (
	// DepthReadonly exposes the four read tools and grants read only.
	DepthReadonly SpecDepth = "readonly"
	// DepthStandard adds the file-mutation tools and grants read+write.
	DepthStandard SpecDepth = "standard"
	// DepthOps adds shell and grants read+write+shell.
	DepthOps SpecDepth = "ops"
	// DepthAll exposes every tool family the remote backend/store can provide.
	DepthAll SpecDepth = "all"
)

// Spec mirrors the standard build's parsed spec shape so callers compile
// unchanged; ParseSpec never returns one in this build.
type Spec struct {
	Transport     string
	Depth         SpecDepth
	DepthExplicit bool

	URL string

	Host         string
	User         string
	Port         int
	Workspace    string
	Binary       string
	IdentityFile string

	EnvDTokenEnv string
	CallTimeout  string
	AuditLog     string
	CAFile       string

	AllowedCommands        []string
	AllowedCommandPatterns []string
	DeniedCommands         []string
	DeniedCommandPatterns  []string
	DenyShellMetachars     bool
	AllowShellMetachars    bool
}

// ParseSpec parses the compact remote target syntax. Unavailable here.
func ParseSpec(raw string) (*Spec, error) {
	return nil, errWin7Unsupported
}

// Expand expands a parsed spec into MCP entries. Unavailable here.
func (s *Spec) Expand() (*Expanded, error) {
	return nil, errWin7Unsupported
}

// Expanded is the result of expanding one file.
type Expanded struct {
	// Servers are the generated MCP entries, keyed by server name.
	Servers map[string]mcpconfig.MCPConfig
	// Warnings describe lossy or ignored configuration.
	Warnings []string
}

// LoadFile reads and expands a remoteTargets file. Unavailable here; a win7
// session has no remote targets to connect.
func LoadFile(path string) (*Expanded, error) {
	return nil, errWin7Unsupported
}

// FindConfigFile reports "no declaration" so a win7 session skips the file it
// could never honor instead of mis-reporting it as loadable.
func FindConfigFile(workspace string) string { return "" }
