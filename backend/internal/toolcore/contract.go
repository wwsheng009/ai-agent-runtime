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

// OptionalParam names a tool parameter whose availability depends on the
// surface runtime (for example symbol lookup needs a code index). A schema is
// declared once; surfaces that cannot honor an optional parameter simply omit
// it from the model-facing schema instead of accepting then rejecting it.
type OptionalParam string

const (
	ParamSymbol OptionalParam = "symbol"
)

// SymbolTarget is one resolved symbol window: where to read it from. It is a
// plain path plus offset/limit so a symbol request can reuse the ordinary file
// reader instead of becoming a second read path.
type SymbolTarget struct {
	Path   string
	Offset int
	Limit  int
}

// SymbolResolver is an optional Runtime extension for symbol-aware tools: a
// surface with a code index resolves `symbol` to a window, one without an index
// filters the parameter out of its schema with FilterSchemaParams and never
// sees this call. The index itself stays host-side; only the window crosses.
type SymbolResolver interface {
	// ResolveSymbol maps a symbol name to a read window. ok=false means the index
	// is unavailable, is in shadow mode, or the symbol does not exist, and the
	// caller falls back to the file_path window it was given.
	ResolveSymbol(ctx context.Context, name string, limit int) (SymbolTarget, bool)
}

// RenderRequest is one non-text request handed to a surface's ContentRenderer.
// The path is already resolved and authorized.
type RenderRequest struct {
	ToolName    string
	AbsPath     string
	DisplayPath string
	Offset      int
	Limit       int
}

// ContentRenderer is an optional Runtime extension for containers the shared
// text reader cannot read on its own: a document, a notebook, an image.
//
// The surface decides what to answer with (a window, an attachment declaration,
// a degradation note, or nothing at all); the shared handler keeps the
// delivered-window bookkeeping, so "only a window that was really delivered may
// be remembered" stays in one place instead of being re-derived per renderer.
type ContentRenderer interface {
	// RenderContent answers one request. handled=false means "read it as text".
	RenderContent(ctx context.Context, req RenderRequest) (*Result, bool)
}

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
	OpRead   Operation = "read"
	OpWrite  Operation = "write"
	OpDelete Operation = "delete"
	OpList   Operation = "list"
	OpExec   Operation = "exec"
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

// CaptureBudgetProvider is an optional Runtime extension for surfaces where the
// pre-fold capture ceiling differs from the model-visible output budget. A
// Runtime that does not implement it uses the core package default.
type CaptureBudgetProvider interface {
	CaptureLimit(tool string) int
}

// MutationObserver is an optional Runtime extension for mutations: it reports a
// write that actually landed, so a host can update its own state (a read-before-
// write ledger, a knowledge index, a change notifier).
//
// It exists because that state is genuinely host-scoped - a session ledger has
// no meaning on a remote host that never saw the read - while the mutation
// itself must run in one shared implementation. A Runtime that does not
// implement it simply has no such state to maintain.
type MutationObserver interface {
	FileMutated(tool, absPath string, newBytes []byte)
}

// MutationGuard is an optional Runtime extension that can refuse a mutation
// before it lands. The host knows what the model has actually seen (a
// read-before-write ledger); the shared handler cannot. Returning a non-nil
// result vetoes the write and becomes the tool result.
//
// A caller that supplied an explicit precondition does not need the guard: the
// precondition is the caller's own statement about the expected state, and
// re-checking against a stale ledger would reject a legitimate write.
type MutationGuard interface {
	// CheckMutation runs before the write. It returns a non-nil result to veto,
	// and may return metadata describing the read state the host observed (for
	// example read_before_write=partial). The metadata is computed from the
	// pre-write snapshot on purpose: after the write the ledger describes the
	// new content, not what the model had seen.
	CheckMutation(tool, absPath string, raw []byte, hasExplicitPrecondition bool) (*Result, map[string]any)
}

// ReadWindow describes one window that was really delivered to the model. It is
// the host-facing summary of a read; the shared handler derives it so the rule
// of what counts as a complete window exists once, and a host only maps it onto
// whatever state it keeps.
type ReadWindow struct {
	Offset    int
	Limit     int
	LinesRead int
	// TotalLines is zero when the read never learned the file length.
	TotalLines int
	EOF        bool
	// More marks a window that stopped before EOF (is_truncated).
	More               bool
	ByteBudgetApplied  bool
	LongLinesTruncated int
	Tail               bool
	Empty              bool
	// FullRead marks a window that covered the whole file from line 1, which is
	// what a read-before-write ledger treats as "the model has seen this file".
	FullRead bool
}

// ReadObserver is an optional Runtime extension for reads: the host learns what
// the model has actually seen, and may answer an unchanged repeat with a stub.
//
// It exists for the same reason as MutationObserver: that state is genuinely
// host-scoped (a session read ledger has no meaning on a remote host that never
// served the read), while the policy of *when* an entry may be written must not
// drift between surfaces - a window dropped by a batch aggregate cap was never
// delivered and must not be remembered as seen.
type ReadObserver interface {
	// ReadDelivered is called once a window was really delivered.
	ReadDelivered(tool, absPath string, window ReadWindow)
	// ReadRemembered is called only for complete windows: the ones a later
	// identical read may answer with a stub.
	ReadRemembered(tool, absPath string, window ReadWindow)
	// RememberedStub returns a stub for an unchanged window the model already
	// saw, plus the closure that consumes the entry. The handler calls consume
	// only once the stub is really delivered, so a batch item dropped by the
	// aggregate cap does not burn it.
	RememberedStub(tool, absPath string, offset, limit int) (stub *Result, consume func(), hit bool)
}

// PathHinter lets a Runtime explain where a missing path probably meant to
// point. Candidates depend on each surface's root, so the runtime computes them
// and the shared handler only formats the error.
type PathHinter interface {
	PathHint(raw string) string
}

// KindHinter explains a path that exists but is not the expected kind (a file
// name that resolves to a directory, most commonly). It is separate from
// PathHinter because the candidate search is different: near-name siblings
// rather than the closest existing ancestor.
type KindHinter interface {
	KindHint(raw string) string
}

// BackupPolicy is an optional Runtime extension: a host whose workspace has no
// version control (or that wants belt-and-braces rollback points) asks mutation
// tools to leave a copy behind. The decision is host-scoped because only the
// host knows whether `.backups/` is useful or just litter.
type BackupPolicy interface {
	WantsBackup(absPath string) bool
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
