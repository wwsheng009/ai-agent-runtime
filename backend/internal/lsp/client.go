package lsp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/executor"
)

// DialResult carries a live bidirectional connection to a language server
// process plus the hooks needed to observe or terminate it.
type DialResult struct {
	Conn io.ReadWriteCloser
	PID  int
	// Kill terminates the process. May be nil for in-process dialects.
	Kill func() error
	// Wait blocks until the process exits. May be nil.
	Wait func() error
	// Guard is the process-tree guard that owns this server (ADR-0005):
	// Windows Job Object with KILL_ON_JOB_CLOSE, Unix process group. It may be
	// nil for in-process dialects; callers that need tree semantics must treat
	// nil as "no guard available" rather than creating their own.
	Guard *executor.ProcessGuard
	// StderrTail returns the last captured stderr lines ("" when none); it
	// feeds the crash reason so failures stay attributable.
	StderrTail func() string
}

// DialFunc starts (or attaches to) a language server for the given spec.
type DialFunc func(ctx context.Context, spec ServerSpec, root string, logger Logger) (*DialResult, error)

// ClientOptions configures one language server client.
type ClientOptions struct {
	Spec ServerSpec
	Root string
	Dial DialFunc
	Log  Logger
	// Observer receives lifecycle and diagnostics events. nil disables them.
	Observer Observer
	// StartupTimeout bounds initialize + initialized round trips.
	StartupTimeout time.Duration
	// ShutdownTimeout bounds shutdown + exit plus the kill fallback.
	ShutdownTimeout time.Duration
	ClientName      string
	ClientVersion   string
	// EmptyEarlyAccept trusts a version-stamped empty publish as conclusive
	// (clean fast path). See DiagnosticsConfig.EmptyEarlyAccept.
	EmptyEarlyAccept bool
	// EmptyConfirm debounces a conclusive empty publish (a real set published
	// moments later supersedes it). <=0 accepts immediately.
	EmptyConfirm time.Duration
	// MaxTrackedDocs caps the open-document set; <=0 uses the default.
	MaxTrackedDocs int
}

// diagSnapshot is the last publishDiagnostics payload for one document.
type diagSnapshot struct {
	Version    int
	HasVersion bool
	Items      []Diagnostic
	// Superseded marks snapshots invalidated by a local edit; they still serve
	// as the pre-edit baseline but never satisfy a fresh-diagnostics wait.
	Superseded bool
	ReceivedAt time.Time
}

// acceptedEmpty records a conclusive-empty answer handed to a caller so a
// later non-empty publish for the same version can be counted as a false
// clean.
type acceptedEmpty struct {
	version int
	at      time.Time
}

// emptyAcceptSupersedeWindow bounds how long after a conclusive-empty answer a
// non-empty publish for the same version still counts as a false clean.
const emptyAcceptSupersedeWindow = 30 * time.Second

// Client is a single language server connection bound to one workspace root.
type Client struct {
	opts    ClientOptions
	rootURI string

	dialRes *DialResult
	tr      *transport

	mu       sync.Mutex
	status   ServerStatus
	enc      PositionEncoding
	syncKind textDocumentSync
	docs     map[string]*Document
	docUsed  map[string]time.Time
	diags    map[string]*diagSnapshot
	waiters  map[string][]chan struct{}
	// lastEmit dedupes diagnostics.updated events by set fingerprint so a
	// server re-publishing the same set does not flood the event plane.
	lastEmit map[string]string
	maxDocs  int
	// everPublished marks the first diagnostics publish observed on this
	// connection. Before it, the first analysis of a module view may still be
	// loading; the bridge grants one bounded cold-start grace per path
	// (TakeColdGrace) and reports first-publish latency in the status.
	everPublished atomic.Bool
	firstPublish  time.Time
	coldGraced    map[string]struct{}
	// coldWait marks paths whose wait already timed out with no snapshot at
	// all (cold view still loading); the bridge then uses a reduced retry
	// budget until the first publish for the path arrives.
	coldWait map[string]struct{}
	// nonEmptyVersions remembers the last version that produced a non-empty
	// diagnostic set per document. An empty publish for that same version is a
	// re-analysis artifact (live evidence: gopls republishes an empty set for a
	// version that already had problems, then the real set seconds later), so
	// it must not be treated as a conclusive clean.
	nonEmptyVersions map[string]int
	// emptyAccepted records conclusive-empty answers handed to callers so a
	// later non-empty publish for the same version can be counted as a false
	// clean (empty_accept_superseded), the regression guard for the fast path.
	emptyAccepted map[string]acceptedEmpty
	superseded    atomic.Int64

	exiting atomic.Bool
	done    chan struct{}
	doneOne sync.Once
}

// NewClient creates an idle client. Call Start to spawn the server.
func NewClient(opts ClientOptions) (*Client, error) {
	if strings.TrimSpace(opts.Spec.Command) == "" {
		return nil, fmt.Errorf("lsp: server spec %q has empty command", opts.Spec.Name)
	}
	if strings.TrimSpace(opts.Root) == "" {
		return nil, errors.New("lsp: client requires a workspace root")
	}
	if opts.Log == nil {
		opts.Log = nopLogger{}
	}
	if opts.Dial == nil {
		opts.Dial = SpawnProcess
	}
	if opts.StartupTimeout <= 0 {
		opts.StartupTimeout = DefaultStartupTimeout
	}
	if opts.ShutdownTimeout <= 0 {
		opts.ShutdownTimeout = DefaultShutdownTimeout
	}
	if opts.ClientName == "" {
		opts.ClientName = "ai-agent-runtime"
	}
	c := &Client{
		opts:    opts,
		rootURI: PathToURI(opts.Root),
		status: ServerStatus{
			Name:   opts.Spec.Name,
			State:  StateUnavailable,
			Reason: "not started",
		},
		enc:              EncodingUTF16,
		syncKind:         textDocumentSync{Change: TextDocumentSyncKindFull},
		docs:             map[string]*Document{},
		docUsed:          map[string]time.Time{},
		diags:            map[string]*diagSnapshot{},
		waiters:          map[string][]chan struct{}{},
		lastEmit:         map[string]string{},
		coldGraced:       map[string]struct{}{},
		coldWait:         map[string]struct{}{},
		nonEmptyVersions: map[string]int{},
		emptyAccepted:    map[string]acceptedEmpty{},
		done:             make(chan struct{}),
	}
	c.maxDocs = opts.MaxTrackedDocs
	if c.maxDocs <= 0 {
		c.maxDocs = DefaultMaxTrackedDocs
	}
	return c, nil
}

// Start spawns the server and completes the initialize handshake.
func (c *Client) Start(ctx context.Context) error {
	if err := c.dialAndInitialize(ctx); err != nil {
		c.setStatus(func(s *ServerStatus) {
			if s.State == StateStarting {
				s.State = StateUnavailable
			}
			s.Reason = judgeReason(err)
			s.LastError = err.Error()
		})
		return err
	}
	c.watchExit()
	return nil
}

func (c *Client) dialAndInitialize(ctx context.Context) error {
	c.setStatus(func(s *ServerStatus) {
		s.State = StateStarting
		s.Command = c.opts.Spec.Command
		s.Language = c.opts.Spec.Name
		s.Root = c.opts.Root
		now := time.Now()
		s.StartedAt = now
	})
	res, err := c.opts.Dial(ctx, c.opts.Spec, c.opts.Root, c.opts.Log)
	if err != nil {
		return fmt.Errorf("lsp: start %s: %w", c.opts.Spec.Name, err)
	}
	c.dialRes = res
	c.tr = newTransport(res.Conn, c.opts.Log)
	c.tr.setNotificationHandler(c.handleNotification)
	c.tr.start()

	initCtx, cancel := context.WithTimeout(ctx, c.opts.StartupTimeout)
	defer cancel()
	raw, err := c.tr.call(initCtx, "initialize", c.initializeParams())
	if err != nil {
		c.tr.close()
		return fmt.Errorf("lsp: initialize %s: %w", c.opts.Spec.Name, err)
	}
	var result initializeResult
	if err := json.Unmarshal(raw, &result); err != nil {
		c.tr.close()
		return fmt.Errorf("lsp: decode initialize result: %w", err)
	}
	if err := c.tr.notify("initialized", struct{}{}); err != nil {
		c.opts.Log.Warnf("lsp: %s initialized notification failed: %v", c.opts.Spec.Name, err)
	}

	enc := NegotiateEncoding(result.Capabilities.PositionEncoding)
	c.mu.Lock()
	c.enc = enc
	c.syncKind = parseTextDocumentSync(result.Capabilities.TextDocumentSync)
	c.mu.Unlock()

	serverName, serverVersion := "", ""
	if result.ServerInfo != nil {
		serverName = result.ServerInfo.Name
		serverVersion = result.ServerInfo.Version
	}
	c.setStatus(func(s *ServerStatus) {
		s.State = StateReady
		s.Reason = ""
		s.PID = res.PID
		s.Encoding = enc
		s.ServerName = serverName
		s.ServerVer = serverVersion
		now := time.Now()
		s.ReadyAt = now
	})
	return nil
}

func (c *Client) initializeParams() initializeParams {
	root := filepath.ToSlash(c.opts.Root)
	return initializeParams{
		ProcessID: os.Getpid(),
		ClientInfo: clientInfo{
			Name:    c.opts.ClientName,
			Version: c.opts.ClientVersion,
		},
		RootURI:               c.rootURI,
		WorkspaceFolders:      []workspaceFolder{{URI: c.rootURI, Name: filepath.Base(strings.TrimRight(root, "/"))}},
		Capabilities:          defaultClientCapabilities(),
		InitializationOptions: c.opts.Spec.InitializationOptions,
	}
}

func defaultClientCapabilities() clientCapabilities {
	return clientCapabilities{
		General: &generalCapabilities{
			PositionEncodings: []string{
				string(EncodingUTF8),
				string(EncodingUTF16),
			},
		},
		TextDocument: &textDocumentCapabilities{
			PublishDiagnostics: &publishDiagnosticsCapabilities{VersionSupport: true},
			Synchronization:    &synchronizationCapabilities{DidSave: true},
		},
		// We always send a workspaceFolders array in initializeParams; the
		// capability must therefore advertise support (LSP 3.17 spec). A
		// false value while sending folders is contradictory and lets
		// strict servers ignore the folder set.
		Workspace: &workspaceCapabilities{WorkspaceFolders: true},
	}
}

func (c *Client) watchExit() {
	if c.dialRes == nil || c.dialRes.Wait == nil {
		return
	}
	go func() {
		err := c.dialRes.Wait()
		if c.exiting.Load() {
			return
		}
		reason := judgeReason(err)
		if c.dialRes.StderrTail != nil {
			if tail := strings.TrimSpace(c.dialRes.StderrTail()); tail != "" {
				reason = truncateReason(reason + ": " + tail)
			}
		}
		c.opts.Log.Warnf("lsp: server %s exited unexpectedly: %v", c.opts.Spec.Name, reason)
		c.setStatus(func(s *ServerStatus) {
			s.State = StateCrashed
			s.Reason = reason
			s.LastError = reason
		})
		if c.tr != nil {
			c.tr.close()
		}
		c.broadcastAll()
		c.closeDone()
	}()
}

func (c *Client) closeDone() {
	c.doneOne.Do(func() { close(c.done) })
}

// Done is closed when the server exits or the client is shut down.
func (c *Client) Done() <-chan struct{} { return c.done }

// Status returns a copy of the current server status.
func (c *Client) Status() ServerStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	status := c.status
	if !c.firstPublish.IsZero() && !status.StartedAt.IsZero() {
		status.FirstPublishMS = c.firstPublish.Sub(status.StartedAt).Milliseconds()
	}
	status.EmptyAcceptSuperseded = c.superseded.Load()
	return status
}

// EverPublished reports whether this connection has ever received a
// diagnostics publish (including empty sets).
func (c *Client) EverPublished() bool {
	return c != nil && c.everPublished.Load()
}

// TakeColdGrace grants a one-time extended wait for path while that path has
// no snapshot yet. The grant is per path per server instance: an ignored file
// (never published) pays the extension once and is then covered by the
// per-path cold fast-fail, so steady-state latency stays untouched.
//
// The gate used to be client-level ("this connection has never published
// anything"), which starved every path first seen after the connection's
// first publish. Live evidence (2026-10-01, session_20261001112556_LRs1jqOD,
// warm gopls): a newly created Go file's first analysis published at 1.71s
// while the plain 1.0s budget expired first, so the model never saw the
// compile error of the file it had just written; the same request succeeds
// inside the grace-extended budget.
func (c *Client) TakeColdGrace(path string) bool {
	if c == nil {
		return false
	}
	uri := PathToURI(path)
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.coldGraced[uri]; ok {
		return false
	}
	c.coldGraced[uri] = struct{}{}
	return true
}

// FirstPublishAt returns when the first diagnostics publish arrived (zero
// before that).
func (c *Client) FirstPublishAt() time.Time {
	if c == nil {
		return time.Time{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.firstPublish
}

// ColdGraceUsed reports whether the one-time cold grace was already spent on
// this path (granted, regardless of whether it helped).
func (c *Client) ColdGraceUsed(path string) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.coldGraced[PathToURI(path)]
	return ok
}

// MarkColdWait records that a wait for path timed out with no snapshot at all.
// Callers gate it on the cold-start signal (grace already spent, nothing ever
// published) so a transient slow analysis on a warm connection never trips it.
// The mark is cleared by the first publish for the path.
func (c *Client) MarkColdWait(path string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.coldWait[PathToURI(path)] = struct{}{}
}

// ColdWait reports whether this path is known cold (see MarkColdWait).
func (c *Client) ColdWait(path string) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.coldWait[PathToURI(path)]
	return ok
}

// setStatus mutates the status record and emits a state event whenever the
// observable state (or its failure reason) changed.
func (c *Client) setStatus(mutate func(*ServerStatus)) {
	c.mu.Lock()
	beforeState := c.status.State
	beforeErr := c.status.LastError
	mutate(&c.status)
	status := c.status
	c.mu.Unlock()
	if status.State != beforeState || status.LastError != beforeErr {
		c.emit(Event{Kind: EventServerState, Status: status})
	}
}

// emit delivers one observer event outside the client lock.
func (c *Client) emit(event Event) {
	if c.opts.Observer == nil {
		return
	}
	if event.Server == "" {
		event.Server = c.opts.Spec.Name
	}
	if event.Time.IsZero() {
		event.Time = time.Now()
	}
	c.opts.Observer(event)
}

// touchActive records the last document operation the client served: the
// observable "this server is actually working" fact in `lsp_servers` (L2).
// It never emits an event by itself.
func (c *Client) touchActive() {
	c.setStatus(func(s *ServerStatus) { s.LastActive = time.Now() })
}

// Encoding reports the negotiated position encoding.
func (c *Client) Encoding() PositionEncoding {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.enc
}

// PID returns the language server process id, if any.
func (c *Client) PID() int {
	if c.dialRes == nil {
		return 0
	}
	return c.dialRes.PID
}

// Call performs a raw JSON-RPC request against the running server.
//
// It exists so consumers that need LSP methods beyond diagnostics (knowledge's
// semantic adapter: textDocument/definition, textDocument/references) can reuse
// this client's transport, lifecycle and position-encoding negotiation instead
// of speaking the protocol themselves. The caller owns method/param semantics.
func (c *Client) Call(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
	tr, err := c.transport()
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.touchActive()
	return tr.call(ctx, method, params)
}

func (c *Client) transport() (*transport, error) {
	if c.tr == nil {
		return nil, ErrTransportClosed
	}
	return c.tr, nil
}

// OpenOrUpdate opens the document or pushes the new content, returning the
// resulting document version so callers can match diagnostics to it.
func (c *Client) OpenOrUpdate(ctx context.Context, path string, content []byte) (int, error) {
	tr, err := c.transport()
	if err != nil {
		return 0, err
	}
	uri := PathToURI(path)
	stripped, bom := StripBOM(content)

	c.mu.Lock()
	doc, opened := c.docs[uri]
	enc := c.enc
	kind := c.syncKind.Change
	var (
		version    int
		change     *ProtocolRange
		changeText string
	)
	switch {
	case !opened:
		doc = &Document{
			URI:      uri,
			Path:     path,
			Language: c.languageID(path),
			Version:  1,
			Content:  stripped,
			BOMBytes: bom,
		}
		c.docs[uri] = doc
		version = doc.Version
	case bytes.Equal(doc.Content, stripped):
		// No textual change: nothing to notify. The document version (and any
		// diagnostics already published for it) stays current, so a wait can
		// resolve from the existing snapshot instead of waiting for a publish
		// that will never come.
		c.docUsed[uri] = time.Now()
		c.mu.Unlock()
		return doc.Version, nil
	default:
		oldContent := doc.Content
		doc.Version++
		doc.Content = stripped
		version = doc.Version
		if kind == TextDocumentSyncKindIncremental {
			if span, ok := singleSpanEdit(oldContent, stripped); ok {
				change = &ProtocolRange{
					Start: CanonicalToProtocol(oldContent, enc, OffsetToCanonical(oldContent, span.start)),
					End:   CanonicalToProtocol(oldContent, enc, OffsetToCanonical(oldContent, span.oldEnd)),
				}
				changeText = string(stripped[span.newStart:span.newEnd])
			}
		}
		if change == nil {
			// Full-sync servers (or a diff that cannot be expressed as one
			// span): send the whole document as a full-range replacement.
			full := doc.FullRange(enc)
			change = &full
			changeText = string(doc.Content)
		}
	}
	if snap := c.diags[uri]; snap != nil {
		snap.Superseded = true
	}
	c.docUsed[uri] = time.Now()
	evicted := c.evictOverCapLocked(uri)
	text := string(doc.Content)
	languageID := doc.Language
	c.mu.Unlock()

	// Evicted documents are closed after the lock: didClose is best effort and
	// must never block the edit itself.
	for _, evictedURI := range evicted {
		params := didCloseParams{TextDocument: textDocumentIdentifier{URI: evictedURI}}
		if err := tr.notify("textDocument/didClose", params); err != nil {
			c.opts.Log.Debugf("lsp: %s didClose %s: %v", c.opts.Spec.Name, evictedURI, err)
		}
	}

	if !opened {
		params := didOpenParams{
			TextDocument: textDocumentItem{
				URI:        uri,
				LanguageID: languageID,
				Version:    version,
				Text:       text,
			},
		}
		if err := tr.notify("textDocument/didOpen", params); err != nil {
			return version, fmt.Errorf("lsp: didOpen %s: %w", path, err)
		}
		c.touchActive()
		return version, nil
	}

	event := textDocumentContentChangeEvent{Text: changeText}
	if kind == TextDocumentSyncKindIncremental {
		event.Range = change
	}
	params := didChangeParams{
		TextDocument:   versionedTextDocumentIdentifier{URI: uri, Version: version},
		ContentChanges: []textDocumentContentChangeEvent{event},
	}
	if err := tr.notify("textDocument/didChange", params); err != nil {
		return version, fmt.Errorf("lsp: didChange %s: %w", path, err)
	}
	c.touchActive()
	return version, nil
}

// evictOverCapLocked trims the open-document set to maxDocs, closing the
// least-recently-used documents. The just-touched document is never evicted.
// Callers must hold c.mu and send didClose for the returned URIs.
func (c *Client) evictOverCapLocked(keep string) []string {
	if c.maxDocs <= 0 || len(c.docs) <= c.maxDocs {
		return nil
	}
	var evicted []string
	for len(c.docs) > c.maxDocs {
		oldestURI := ""
		var oldest time.Time
		for uri := range c.docs {
			if uri == keep {
				continue
			}
			used := c.docUsed[uri]
			if oldestURI == "" || used.Before(oldest) {
				oldestURI, oldest = uri, used
			}
		}
		if oldestURI == "" {
			break
		}
		delete(c.docs, oldestURI)
		delete(c.diags, oldestURI)
		delete(c.docUsed, oldestURI)
		delete(c.lastEmit, oldestURI)
		delete(c.nonEmptyVersions, oldestURI)
		delete(c.emptyAccepted, oldestURI)
		delete(c.coldWait, oldestURI)
		evicted = append(evicted, oldestURI)
	}
	return evicted
}

// Save notifies the server that a tracked document was flushed to disk.
func (c *Client) Save(ctx context.Context, path string) error {
	tr, err := c.transport()
	if err != nil {
		return err
	}
	uri := PathToURI(path)
	c.mu.Lock()
	doc, ok := c.docs[uri]
	includeText := c.syncKind.Save != nil && c.syncKind.Save.IncludeText
	var text string
	if doc != nil {
		text = string(doc.Content)
	}
	c.mu.Unlock()
	if !ok {
		return nil
	}
	params := didSaveParams{TextDocument: textDocumentIdentifier{URI: uri}}
	if includeText {
		params.Text = &text
	}
	if err := tr.notify("textDocument/didSave", params); err != nil {
		return fmt.Errorf("lsp: didSave %s: %w", path, err)
	}
	c.touchActive()
	return nil
}

// CloseDocument removes a document from client state.
func (c *Client) CloseDocument(ctx context.Context, path string) error {
	tr, err := c.transport()
	if err != nil {
		return err
	}
	uri := PathToURI(path)
	c.mu.Lock()
	_, ok := c.docs[uri]
	delete(c.docs, uri)
	delete(c.diags, uri)
	c.mu.Unlock()
	if !ok {
		return nil
	}
	params := didCloseParams{TextDocument: textDocumentIdentifier{URI: uri}}
	if err := tr.notify("textDocument/didClose", params); err != nil {
		return fmt.Errorf("lsp: didClose %s: %w", path, err)
	}
	return nil
}

// TrackedCount reports how many documents the client currently holds open.
func (c *Client) TrackedCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.docs)
}

// CurrentDiagnostics returns the last known diagnostics for a path, regardless
// of whether the snapshot was superseded by a local edit.
func (c *Client) CurrentDiagnostics(path string) []Diagnostic {
	c.mu.Lock()
	defer c.mu.Unlock()
	snap := c.diags[PathToURI(path)]
	if snap == nil {
		return nil
	}
	return append([]Diagnostic(nil), snap.Items...)
}

// HasSnapshot reports whether the server ever published a diagnostic set for
// this document (fresh or stale). It lets callers separate "nothing to report"
// from "never published" when a wait times out.
func (c *Client) HasSnapshot(path string) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.diags[PathToURI(path)] != nil
}

// WaitDiagnostics waits for diagnostics published for the given document
// version. fresh=false means the wait timed out or only stale data was
// available. version<=0 matches any publish received after the local change.
//
// An empty publish is only taken at face value once the freshness budget is
// exhausted: servers such as rust-analyzer publish an empty set on didOpen
// before analysis completes, and returning that interim result immediately
// would mask the real diagnostics published moments later.
func (c *Client) WaitDiagnostics(ctx context.Context, path string, version int, wait time.Duration) (items []Diagnostic, fresh bool) {
	uri := PathToURI(path)
	if wait <= 0 {
		return c.bestEffort(uri)
	}
	deadline := time.Now().Add(wait)
	var emptySince time.Time
	for {
		c.mu.Lock()
		snap := c.diags[uri]
		emptyConclusive := false
		if snap != nil && !snap.Superseded && acceptable(snap, version) {
			if len(snap.Items) > 0 {
				items := append([]Diagnostic(nil), snap.Items...)
				c.mu.Unlock()
				return items, true
			}
			// Conclusive empty: the server stamped the publish with the
			// current document version, so it analyzed exactly this text.
			// Unstamped empty publishes stay conservative (see settle) to
			// avoid masking interim results from servers like rust-analyzer.
			// The same version must never have shown problems before: an empty
			// publish after a non-empty one is a re-analysis artifact, not a
			// clean bill of health.
			if c.opts.EmptyEarlyAccept && version > 0 && snap.HasVersion && snap.Version == version &&
				c.nonEmptyVersions[uri] != version {
				if emptySince.IsZero() {
					emptySince = time.Now()
				}
				if c.opts.EmptyConfirm <= 0 || time.Since(emptySince) >= c.opts.EmptyConfirm {
					emptyConclusive = true
				}
			} else {
				emptySince = time.Time{}
			}
		} else {
			emptySince = time.Time{}
		}
		if emptyConclusive {
			c.emptyAccepted[uri] = acceptedEmpty{version: snap.Version, at: time.Now()}
			c.mu.Unlock()
			return nil, true
		}
		// Compute the wait window *after* evaluating the snapshot: the first
		// empty publish sets emptySince in this iteration, and the debounce
		// deadline must be derived from it rather than from the previous loop.
		waitUntil := deadline
		if !emptySince.IsZero() && c.opts.EmptyConfirm > 0 {
			if confirmAt := emptySince.Add(c.opts.EmptyConfirm); confirmAt.Before(waitUntil) {
				waitUntil = confirmAt
			}
		}
		remaining := time.Until(waitUntil)
		if remaining <= 0 {
			c.mu.Unlock()
			return c.settle(uri, version)
		}
		ch := make(chan struct{})
		c.waiters[uri] = append(c.waiters[uri], ch)
		c.mu.Unlock()

		timer := time.NewTimer(remaining)
		select {
		case <-ch:
			timer.Stop()
		case <-ctx.Done():
			timer.Stop()
			return c.settle(uri, version)
		case <-timer.C:
			return c.settle(uri, version)
		case <-c.done:
			timer.Stop()
			return c.settle(uri, version)
		}
	}
}

// settle resolves a wait whose budget ran out: a conclusive empty snapshot is
// an explicit "no problems" answer, anything else stays best effort (stale
// data may still be reported, but never as fresh).
func (c *Client) settle(uri string, version int) ([]Diagnostic, bool) {
	c.mu.Lock()
	snap := c.diags[uri]
	if snap != nil && !snap.Superseded && acceptable(snap, version) && len(snap.Items) == 0 {
		// Conclusive only when this version never had a non-empty set; a churn
		// empty (live evidence: gopls republished empty then the real set
		// 1.3-7.9s later for the same version) must degrade, not claim clean.
		if !snap.HasVersion || c.nonEmptyVersions[uri] != snap.Version {
			c.emptyAccepted[uri] = acceptedEmpty{version: snap.Version, at: time.Now()}
			c.mu.Unlock()
			return nil, true
		}
	}
	c.mu.Unlock()
	return c.bestEffort(uri)
}

// acceptable reports whether a snapshot satisfies a wait for version.
func acceptable(snap *diagSnapshot, version int) bool {
	if version <= 0 {
		return true
	}
	if !snap.HasVersion {
		// Servers that do not stamp publishes: the snapshot was received after
		// the local change (it is not superseded), so accept it.
		return true
	}
	return snap.Version == version
}

// bestEffort returns the newest known snapshot, possibly stale, when the
// freshness wait cannot be satisfied.
func (c *Client) bestEffort(uri string) ([]Diagnostic, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	snap := c.diags[uri]
	if snap == nil {
		return nil, false
	}
	return append([]Diagnostic(nil), snap.Items...), false
}

func (c *Client) handleNotification(method string, params json.RawMessage) {
	switch method {
	case "textDocument/publishDiagnostics":
		c.handlePublishDiagnostics(params)
	case "window/logMessage":
		var msg struct {
			Type    int    `json:"type"`
			Message string `json:"message"`
		}
		if json.Unmarshal(params, &msg) == nil && msg.Message != "" {
			c.opts.Log.Debugf("lsp: %s: %s", c.opts.Spec.Name, msg.Message)
		}
	case "window/showMessage":
		var msg struct {
			Type    int    `json:"type"`
			Message string `json:"message"`
		}
		if json.Unmarshal(params, &msg) == nil && msg.Message != "" {
			c.opts.Log.Infof("lsp: %s: %s", c.opts.Spec.Name, msg.Message)
		}
	}
}

func (c *Client) handlePublishDiagnostics(params json.RawMessage) {
	var payload publishDiagnosticsParams
	if err := json.Unmarshal(params, &payload); err != nil {
		c.opts.Log.Warnf("lsp: %s malformed diagnostics: %v", c.opts.Spec.Name, err)
		return
	}
	// Servers spell file URIs in their own way (drive-letter case, escaped
	// colon). Normalize before matching tracked documents so the snapshot
	// lands under the same key WaitDiagnostics looks up.
	payload.URI = canonicalURI(payload.URI)
	c.mu.Lock()
	doc := c.docs[payload.URI]
	if payload.Version != nil && doc != nil && doc.Version != *payload.Version {
		// ADR-0006 D7: diagnostics for a superseded version are discarded.
		current := doc.Version
		c.mu.Unlock()
		c.opts.Log.Debugf("lsp: %s dropping diagnostics for %s v%d (current v%d)",
			c.opts.Spec.Name, payload.URI, *payload.Version, current)
		return
	}
	enc := c.enc
	var content []byte
	if doc != nil {
		content = doc.Content
	}
	items := make([]Diagnostic, 0, len(payload.Diagnostics))
	for _, raw := range payload.Diagnostics {
		d, err := canonicalDiagnostic(raw, content, c.opts.Spec.Name, enc)
		if err != nil {
			c.opts.Log.Debugf("lsp: %s skip diagnostic: %v", c.opts.Spec.Name, err)
			continue
		}
		items = append(items, d)
	}
	snap := &diagSnapshot{Items: items, ReceivedAt: time.Now()}
	if payload.Version != nil {
		snap.Version = *payload.Version
		snap.HasVersion = true
	}
	c.diags[payload.URI] = snap
	firstPublish := false
	var coldStatus ServerStatus
	if c.firstPublish.IsZero() {
		c.firstPublish = time.Now()
		c.everPublished.Store(true)
		// 冷启动延迟只在"首个发布"这一刻可观测：状态本身没有变化，setStatus
		// 永远不会发这条事件，所以在这里显式补一条带 first_publish_ms 的状态
		// 事件（锁外发送，观察者回调可能回读客户端状态）。
		firstPublish = true
		coldStatus = c.status
		if !coldStatus.StartedAt.IsZero() {
			coldStatus.FirstPublishMS = c.firstPublish.Sub(coldStatus.StartedAt).Milliseconds()
		}
	}
	// Any publish for the path ends its cold state: the view is alive.
	delete(c.coldWait, payload.URI)
	if len(items) > 0 && snap.HasVersion {
		// Remember which version showed problems so a later empty publish for
		// it is not mistaken for a clean result (see WaitDiagnostics/settle).
		c.nonEmptyVersions[payload.URI] = snap.Version
	}
	if len(items) > 0 {
		// A conclusive-empty answer that a non-empty publish for the same
		// version contradicts is a false clean: count it as the regression
		// guard for the fast path.
		if accepted, ok := c.emptyAccepted[payload.URI]; ok {
			if accepted.version == snap.Version && time.Since(accepted.at) <= emptyAcceptSupersedeWindow {
				c.superseded.Add(1)
			}
			delete(c.emptyAccepted, payload.URI)
		}
	}
	waiters := c.waiters[payload.URI]
	delete(c.waiters, payload.URI)
	tracked := doc != nil
	c.mu.Unlock()
	if firstPublish {
		c.emit(Event{Kind: EventServerState, Status: coldStatus})
	}
	for _, ch := range waiters {
		close(ch)
	}
	if tracked {
		// Dedupe by set fingerprint: servers re-publish identical sets on
		// unrelated notifications; only changes are observable facts.
		fingerprint := DiagnosticsFingerprint(items)
		c.mu.Lock()
		changed := c.lastEmit[payload.URI] != fingerprint
		if changed {
			c.lastEmit[payload.URI] = fingerprint
		}
		c.mu.Unlock()
		if changed {
			c.emit(Event{
				Kind:            EventDiagnostics,
				Path:            URIToPath(payload.URI),
				PathFingerprint: FingerprintPath(URIToPath(payload.URI)),
				Count:           len(items),
				DiagFingerprint: fingerprint,
				Version:         snap.Version,
				HasVersion:      snap.HasVersion,
			})
		}
	}
}

func (c *Client) broadcastAll() {
	c.mu.Lock()
	all := c.waiters
	c.waiters = map[string][]chan struct{}{}
	c.mu.Unlock()
	for _, chans := range all {
		for _, ch := range chans {
			close(ch)
		}
	}
}

// languageID maps a file extension to an LSP language identifier. It reuses
// the same mapping as HandlesFile so routing and didOpen cannot drift.
func (c *Client) languageID(path string) string {
	if id := languageIDForPath(path); id != "" {
		return id
	}
	ext := strings.ToLower(filepath.Ext(path))
	return strings.TrimPrefix(ext, ".")
}

var extensionLanguageID = map[string]string{
	".go":    "go",
	".ts":    "typescript",
	".tsx":   "typescriptreact",
	".js":    "javascript",
	".jsx":   "javascriptreact",
	".py":    "python",
	".rs":    "rust",
	".java":  "java",
	".c":     "c",
	".cpp":   "cpp",
	".cc":    "cpp",
	".h":     "c",
	".hpp":   "cpp",
	".cs":    "csharp",
	".rb":    "ruby",
	".php":   "php",
	".json":  "json",
	".yaml":  "yaml",
	".yml":   "yaml",
	".md":    "markdown",
	".sql":   "sql",
	".sh":    "shellscript",
	".lua":   "lua",
	".kt":    "kotlin",
	".swift": "swift",
}

// Shutdown performs the graceful shutdown/exit handshake and kills leftovers.
func (c *Client) Shutdown(ctx context.Context) {
	c.exiting.Store(true)
	if c.tr != nil {
		callCtx, cancel := context.WithTimeout(ctx, c.opts.ShutdownTimeout)
		if _, err := c.tr.call(callCtx, "shutdown", nil); err != nil {
			c.opts.Log.Debugf("lsp: %s shutdown: %v", c.opts.Spec.Name, err)
		}
		cancel()
		_ = c.tr.notify("exit", nil)
		c.tr.close()
	}
	if c.dialRes != nil {
		if c.dialRes.Kill != nil {
			time.Sleep(20 * time.Millisecond)
			if err := c.dialRes.Kill(); err != nil {
				c.opts.Log.Debugf("lsp: %s kill: %v", c.opts.Spec.Name, err)
			}
		}
		if c.dialRes.Wait != nil {
			waited := make(chan struct{})
			go func() {
				_ = c.dialRes.Wait()
				close(waited)
			}()
			select {
			case <-waited:
			case <-time.After(c.opts.ShutdownTimeout):
				c.opts.Log.Warnf("lsp: %s did not exit within %s", c.opts.Spec.Name, c.opts.ShutdownTimeout)
			}
		}
	}
	c.setStatus(func(s *ServerStatus) {
		if s.State != StateCrashed {
			s.State = StateStopped
			s.Reason = "shut down"
		}
	})
	c.broadcastAll()
	c.closeDone()
}

// judgeReason turns a transport/process error into a short degrade reason.
func judgeReason(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, ErrTransportClosed):
		return "transport closed"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, os.ErrNotExist):
		return "command not found"
	default:
		return truncateReason(err.Error())
	}
}

func truncateReason(reason string) string {
	const max = 160
	if len(reason) <= max {
		return reason
	}
	return reason[:max] + "..."
}
