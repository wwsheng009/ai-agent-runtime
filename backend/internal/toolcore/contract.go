// Package toolcore defines the surface-independent tool contract.
//
// A tool is a Definition plus a Handler. Handlers never read process state
// directly: paths, permissions, command policy, output shaping and artifact
// storage are injected through Runtime. Surfaces (builtin agent, MCP, remote,
// CLI, HTTP) adapt this contract without redefining tool semantics.
package toolcore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ContractVersion is the wire-independent version of this package's core
// interfaces. Breaking changes to Definition/Handler/Runtime require a major
// bump.
const ContractVersion = 1

// Capability declares what a tool does. It is not an authorization decision.
type Capability string

const (
	CapReadFS    Capability = "read_fs"
	CapWriteFS   Capability = "write_fs"
	CapExecShell Capability = "exec_shell"
	CapNetwork   Capability = "network"
	CapImage     Capability = "image"
	CapArtifact  Capability = "artifact"
	CapStateful  Capability = "stateful"
)

// Definition is the single declaration of a tool's identity and contract.
type Definition struct {
	Name        string
	Aliases     []string
	Description string
	Version     string
	InputSchema json.RawMessage

	// Capabilities describe behavior, never permission.
	Capabilities []Capability

	// DefaultOutputBudget is the tool-owned model-visible window. Zero means
	// the runtime's default budget applies.
	DefaultOutputBudget int

	// DirectCall only controls whether non-MCP surfaces may expose the tool.
	// It never bypasses policy, approval, sandbox or command checks.
	DirectCall bool

	Metadata map[string]any
}

// Handler executes one tool call.
type Handler interface {
	Definition() Definition
	Execute(ctx context.Context, call *Call) (*Result, error)
}

// Call carries normalized arguments and the injected runtime.
type Call struct {
	Args    map[string]any
	Runtime Runtime
	Request RequestInfo
}

// RequestInfo identifies the caller without leaking surface-specific types.
type RequestInfo struct {
	SessionID  string
	Principal  string
	RemoteAddr string
	ToolCallID string
	TraceID    string
	Surface    string // builtin | mcp | remote | cli | http
}

// OutputKind values are the stable result taxonomy.
const (
	KindText       = "text"
	KindStructured = "structured"
	KindBinary     = "binary"
	KindEmpty      = "empty"
)

// Result is the surface-independent tool result.
type Result struct {
	Success    bool
	OutputKind string
	Content    string
	Data       []byte
	MIMEType   string

	// Origin is stamped by the runtime/surface. Handlers must not forge it.
	Origin ResultOrigin

	Metadata map[string]any

	ErrorCode  string
	NextAction string
	Retriable  bool
	Err        error
}

// ResultOrigin records where a result was produced and how much it is trusted.
type ResultOrigin struct {
	Scope      string // local | remote | cli | test
	Target     string // remote target name, empty otherwise
	TrustLevel string // local | trusted_remote | untrusted_remote
}

const (
	TrustLocal           = "local"
	TrustTrustedRemote   = "trusted_remote"
	TrustUntrustedRemote = "untrusted_remote"
)

// IsRemote reports whether the result crossed a remote trust boundary.
func (o ResultOrigin) IsRemote() bool {
	return strings.EqualFold(strings.TrimSpace(o.Scope), "remote")
}

// Runtime is the complete environment injected into a Handler. A nil or
// partially implemented Runtime fails closed at the tool boundary.
type Runtime interface {
	Paths() PathResolver
	Permissions() PermissionChecker
	Commands() CommandPolicy
	Env() EnvProvider
	Output() OutputShaper
	Artifacts() ArtifactSink
	Sessions() SessionState
	Clock() Clock
	IDs() IDGenerator
}

type Operation string

const (
	OpRead  Operation = "read"
	OpWrite Operation = "write"
	OpList  Operation = "list"
	OpExec  Operation = "exec"
)

type PathResolver interface {
	Root() string
	Resolve(raw string) (string, error)
	Relative(abs string) string
}

type PermissionChecker interface {
	CheckPath(op Operation, abs string) error
}

type CommandPolicy interface {
	Check(command string) (rule string, err error)
}

type OutputShaper interface {
	Budget(tool string) int
	Shape(tool string, result *Result) (*Result, error)
}

type ArtifactScope string

const (
	ArtifactScopeLocal  ArtifactScope = "local"
	ArtifactScopeRemote ArtifactScope = "remote"
)

type ArtifactSink interface {
	// Put may be called with an empty ref.ID; the returned ref is canonical.
	Put(ctx context.Context, ref ArtifactRef, data []byte, meta ArtifactMeta) (ArtifactRef, error)
	// Open only reads the scope/target declared by ref.
	Open(ctx context.Context, ref ArtifactRef, offset, limit int) (ArtifactChunk, error)
}

type ArtifactMeta struct {
	ToolName   string
	ToolCallID string
	SessionID  string
	Summary    string
	Metadata   map[string]any
}

type ArtifactChunk struct {
	Data   []byte
	Size   int64
	Offset int64
	EOF    bool
}

type SessionState interface {
	IDs() SessionIDs
	Workspace() WorkspaceState
	Artifacts() ArtifactState
	Extensions() ExtensionStore
}

type SessionIDs struct {
	SessionID  string
	ToolCallID string
	TurnID     string
	GoalID     string
	AgentDepth int
}

type WorkspaceState struct {
	Root          string
	AllowedRoots  []string
	ReadOnlyRoots []string
}

type ArtifactState struct {
	Store     ArtifactSink
	ReadScope string
}

type ExtensionStore interface {
	Get(key string) (any, bool)
	Set(key string, value any)
}

type EnvProvider interface {
	Filter(env []string) []string
}

type Clock interface {
	Now() time.Time
}

type IDGenerator interface {
	NewID(prefix string) string
}

// Stable error codes shared by every surface.
const (
	ErrInvalidArgument     = "INVALID_ARGUMENT"
	ErrToolNotFound        = "TOOL_NOT_FOUND"
	ErrUnsupported         = "UNSUPPORTED"
	ErrToolDenied          = "TOOL_DENIED"
	ErrCapabilityDenied    = "CAPABILITY_DENIED"
	ErrPathDenied          = "PATH_DENIED"
	ErrCommandDenied       = "COMMAND_DENIED"
	ErrApprovalRequired    = "APPROVAL_REQUIRED"
	ErrArtifactScopeDenied = "ARTIFACT_SCOPE_DENIED"
	ErrCommandFailed       = "COMMAND_FAILED"
	ErrTimeout             = "TIMEOUT"
	ErrCancelled           = "CANCELLED"
	ErrOutputLimit         = "OUTPUT_LIMIT_EXCEEDED"
	ErrNotFound            = "NOT_FOUND"
	ErrWorkspaceUnavail    = "WORKSPACE_UNAVAILABLE"
	ErrArtifactWriteFailed = "ARTIFACT_WRITE_FAILED"
	ErrArtifactExpired     = "ARTIFACT_EXPIRED"
	ErrRemoteUnavailable   = "REMOTE_UNAVAILABLE"
	ErrRateLimited         = "RATE_LIMITED"
	ErrProtocol            = "PROTOCOL_ERROR"
	ErrRemoteTooLarge      = "REMOTE_RESPONSE_TOO_LARGE"
	ErrInternal            = "INTERNAL_ERROR"
)

// NormalizeErrorCode maps compatibility spellings onto the canonical set.
func NormalizeErrorCode(code string) string {
	switch strings.ToUpper(strings.TrimSpace(code)) {
	case "", "OK":
		return ""
	case "INVALID_ARGS":
		return ErrInvalidArgument
	default:
		return strings.ToUpper(strings.TrimSpace(code))
	}
}

// Error builds a classified result error while preserving the underlying err.
func Error(code, nextAction string, err error) *Result {
	if err == nil {
		err = errors.New(strings.ToLower(strings.TrimSpace(code)))
	}
	return &Result{
		Success:    false,
		OutputKind: KindText,
		Content:    err.Error(),
		ErrorCode:  NormalizeErrorCode(code),
		NextAction: nextAction,
		Err:        err,
	}
}

// ValidateDefinition checks the minimum invariants required for registration.
func ValidateDefinition(def Definition) error {
	if strings.TrimSpace(def.Name) == "" {
		return fmt.Errorf("tool name is required")
	}
	if len(def.InputSchema) == 0 {
		return fmt.Errorf("tool %q has no input schema", def.Name)
	}
	if !json.Valid(def.InputSchema) {
		return fmt.Errorf("tool %q has invalid input schema JSON", def.Name)
	}
	for _, alias := range def.Aliases {
		if strings.TrimSpace(alias) == "" {
			return fmt.Errorf("tool %q has an empty alias", def.Name)
		}
		if strings.EqualFold(strings.TrimSpace(alias), strings.TrimSpace(def.Name)) {
			return fmt.Errorf("tool %q lists its canonical name as an alias", def.Name)
		}
	}
	return nil
}
