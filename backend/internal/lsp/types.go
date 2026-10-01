package lsp

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// DiagnosticSeverity mirrors the LSP `DiagnosticSeverity` enum.
type DiagnosticSeverity int

const (
	SeverityError       DiagnosticSeverity = 1
	SeverityWarning     DiagnosticSeverity = 2
	SeverityInformation DiagnosticSeverity = 3
	SeverityHint        DiagnosticSeverity = 4
)

// String renders the severity in a stable, model-facing form.
func (s DiagnosticSeverity) String() string {
	switch s {
	case SeverityError:
		return "error"
	case SeverityWarning:
		return "warning"
	case SeverityInformation:
		return "info"
	case SeverityHint:
		return "hint"
	default:
		return "unknown"
	}
}

// Diagnostic is a canonical-position diagnostic received from one server.
// Range is already converted to UTF-8 byte columns (ADR-0006 §4.4).
type Diagnostic struct {
	Range      CanonicalRange     `json:"range"`
	Severity   DiagnosticSeverity `json:"severity"`
	Code       string             `json:"code,omitempty"`
	Source     string             `json:"source,omitempty"`
	Message    string             `json:"message"`
	Server     string             `json:"server"`
	Version    int                `json:"version"`
	ReceivedAt time.Time          `json:"received_at"`
}

// Key identifies an equal diagnostic for baseline diffing (Q1 "changed"
// scope). Ranges participate so that a moved error counts as new.
func (d Diagnostic) Key() string {
	return fmt.Sprintf("%d|%s|%s|%s|%d:%d-%d:%d",
		d.Severity, d.Source, d.Code, d.Message,
		d.Range.Start.Line, d.Range.Start.Column, d.Range.End.Line, d.Range.End.Column)
}

// canonicalDiagnostic converts one wire diagnostic into the canonical form.
// The range endpoints cross the encoding boundary exactly here (ADR-0006
// §4.4); every other layer only ever sees UTF-8 byte columns.
func canonicalDiagnostic(raw protocolDiagnostic, content []byte, server string, enc PositionEncoding) (Diagnostic, error) {
	message := strings.TrimSpace(raw.Message)
	if message == "" {
		return Diagnostic{}, fmt.Errorf("diagnostic without message")
	}
	return Diagnostic{
		Range: CanonicalRange{
			Start: ProtocolToCanonical(content, enc, raw.Range.Start),
			End:   ProtocolToCanonical(content, enc, raw.Range.End),
		},
		Severity:   normalizeSeverity(raw.Severity),
		Code:       diagnosticCode(raw.Code),
		Source:     strings.TrimSpace(raw.Source),
		Message:    message,
		Server:     server,
		ReceivedAt: time.Now(),
	}, nil
}

// normalizeSeverity maps the wire enum onto the canonical set. A missing
// severity (0) is interpreted as an error so that the agent never silently
// loses the most actionable signal.
func normalizeSeverity(value int) DiagnosticSeverity {
	severity := DiagnosticSeverity(value)
	if severity < SeverityError || severity > SeverityHint {
		return SeverityError
	}
	return severity
}

// diagnosticCode renders the polymorphic `code` field (string | integer) in a
// stable, human-readable form.
func diagnosticCode(code any) string {
	switch value := code.(type) {
	case nil:
		return ""
	case string:
		return value
	case float64:
		return strconv.FormatFloat(value, 'f', -1, 64)
	case json.Number:
		return value.String()
	default:
		data, err := json.Marshal(value)
		if err != nil {
			return ""
		}
		return strings.Trim(string(data), `"`)
	}
}

// ServerState is the observable lifecycle state of one LSP server instance.
type ServerState string

const (
	// StateDisabled marks a server that config did not enable.
	StateDisabled ServerState = "disabled"
	// StateStarting marks a server whose handshake is in flight.
	StateStarting ServerState = "starting"
	// StateReady marks a server that completed initialize.
	StateReady ServerState = "ready"
	// StateUnavailable marks a server that is not usable (missing binary,
	// handshake failure, lock held elsewhere). Tools degrade, never fail.
	StateUnavailable ServerState = "unavailable"
	// StateCrashed marks a server that exited after being ready.
	StateCrashed ServerState = "crashed"
	// StateStopped marks a server shut down by the runtime.
	StateStopped ServerState = "stopped"
)

// ServerStatus is the per-server observability record promised by L2
// (`lsp_servers`-shaped: name, state, workspace root, failure reason).
type ServerStatus struct {
	Name       string           `json:"name"`
	Language   string           `json:"language"`
	Root       string           `json:"root"`
	State      ServerState      `json:"state"`
	Reason     string           `json:"reason,omitempty"`
	Encoding   PositionEncoding `json:"position_encoding,omitempty"`
	PID        int              `json:"pid,omitempty"`
	ServerName string           `json:"server_name,omitempty"`
	ServerVer  string           `json:"server_version,omitempty"`
	Command    string           `json:"command,omitempty"`
	LockPath   string           `json:"lock_path,omitempty"`
	Shared     bool             `json:"shared,omitempty"`
	StartedAt  time.Time        `json:"started_at,omitempty"`
	ReadyAt    time.Time        `json:"ready_at,omitempty"`
	Restarts   int              `json:"restarts,omitempty"`
	LastError  string           `json:"last_error,omitempty"`
	LastActive time.Time        `json:"last_active,omitempty"`
	// FirstPublishMS is the delay from server start to the first diagnostics
	// publish (0 = nothing published yet). It quantifies cold-start / view
	// load latency, the dominant remaining degradation cause.
	FirstPublishMS int64 `json:"first_publish_ms,omitempty"`
	// EmptyAcceptSuperseded counts conclusive-empty answers that a later
	// non-empty publish for the same version contradicted (false cleans). It
	// guards the empty early-accept fast path.
	EmptyAcceptSuperseded int64 `json:"empty_accept_superseded,omitempty"`
}

// ---- protocol wire types (only this package may use them) ----

type protocolDiagnostic struct {
	Range    ProtocolRange `json:"range"`
	Severity int           `json:"severity,omitempty"`
	Code     any           `json:"code,omitempty"`
	Source   string        `json:"source,omitempty"`
	Message  string        `json:"message"`
}

type publishDiagnosticsParams struct {
	URI         string               `json:"uri"`
	Version     *int                 `json:"version,omitempty"`
	Diagnostics []protocolDiagnostic `json:"diagnostics"`
}

type textDocumentItem struct {
	URI        string `json:"uri"`
	LanguageID string `json:"languageId"`
	Version    int    `json:"version"`
	Text       string `json:"text"`
}

type textDocumentIdentifier struct {
	URI string `json:"uri"`
}

type versionedTextDocumentIdentifier struct {
	URI     string `json:"uri"`
	Version int    `json:"version"`
}

type textDocumentContentChangeEvent struct {
	Range *ProtocolRange `json:"range,omitempty"`
	Text  string         `json:"text"`
}

type didOpenParams struct {
	TextDocument textDocumentItem `json:"textDocument"`
}

type didChangeParams struct {
	TextDocument   versionedTextDocumentIdentifier  `json:"textDocument"`
	ContentChanges []textDocumentContentChangeEvent `json:"contentChanges"`
}

type didSaveParams struct {
	TextDocument textDocumentIdentifier `json:"textDocument"`
	Text         *string                `json:"text,omitempty"`
}

type didCloseParams struct {
	TextDocument textDocumentIdentifier `json:"textDocument"`
}

type initializeParams struct {
	ProcessID             int                    `json:"processId"`
	ClientInfo            clientInfo             `json:"clientInfo"`
	Locale                string                 `json:"locale,omitempty"`
	RootURI               string                 `json:"rootUri,omitempty"`
	WorkspaceFolders      []workspaceFolder      `json:"workspaceFolders,omitempty"`
	Capabilities          clientCapabilities     `json:"capabilities"`
	InitializationOptions map[string]interface{} `json:"initializationOptions,omitempty"`
}

type clientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

type workspaceFolder struct {
	URI  string `json:"uri"`
	Name string `json:"name"`
}

type clientCapabilities struct {
	General      *generalCapabilities      `json:"general,omitempty"`
	TextDocument *textDocumentCapabilities `json:"textDocument,omitempty"`
	Workspace    *workspaceCapabilities    `json:"workspace,omitempty"`
}

type generalCapabilities struct {
	PositionEncodings []string `json:"positionEncodings,omitempty"`
}

type textDocumentCapabilities struct {
	PublishDiagnostics *publishDiagnosticsCapabilities `json:"publishDiagnostics,omitempty"`
	Synchronization    *synchronizationCapabilities    `json:"synchronization,omitempty"`
}

type publishDiagnosticsCapabilities struct {
	VersionSupport bool `json:"versionSupport,omitempty"`
}

type synchronizationCapabilities struct {
	DidSave bool `json:"didSave,omitempty"`
}

type workspaceCapabilities struct {
	Configuration    bool `json:"configuration,omitempty"`
	WorkspaceFolders bool `json:"workspaceFolders,omitempty"`
}

type initializeResult struct {
	Capabilities serverCapabilities `json:"capabilities"`
	ServerInfo   *serverInfo        `json:"serverInfo,omitempty"`
}

type serverInfo struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

type serverCapabilities struct {
	PositionEncoding string          `json:"positionEncoding,omitempty"`
	TextDocumentSync json.RawMessage `json:"textDocumentSync,omitempty"`
}

// textDocumentSync mirrors the union of a number and TextDocumentSyncOptions.
type textDocumentSync struct {
	OpenClose bool `json:"openClose,omitempty"`
	Change    int  `json:"change,omitempty"`
	Save      *struct {
		IncludeText bool `json:"includeText,omitempty"`
	} `json:"save,omitempty"`
}

// parseTextDocumentSync accepts either a bare TextDocumentSyncKind number or
// the TextDocumentSyncOptions object.
func parseTextDocumentSync(raw json.RawMessage) textDocumentSync {
	if len(raw) == 0 || string(raw) == "null" {
		return textDocumentSync{}
	}
	var kind int
	if err := json.Unmarshal(raw, &kind); err == nil {
		return textDocumentSync{Change: kind}
	}
	var opts textDocumentSync
	if err := json.Unmarshal(raw, &opts); err != nil {
		return textDocumentSync{}
	}
	return opts
}

// TextDocumentSyncKindFull / Incremental mirror the LSP enum.
const (
	TextDocumentSyncKindNone        = 0
	TextDocumentSyncKindFull        = 1
	TextDocumentSyncKindIncremental = 2
)
