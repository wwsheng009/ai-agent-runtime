package lsp

import (
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
}

// DialFunc starts (or attaches to) a language server for the given spec.
type DialFunc func(ctx context.Context, spec ServerSpec, root string, logger Logger) (*DialResult, error)

// ClientOptions configures one language server client.
type ClientOptions struct {
	Spec ServerSpec
	Root string
	Dial DialFunc
	Log  Logger
	// StartupTimeout bounds initialize + initialized round trips.
	StartupTimeout time.Duration
	// ShutdownTimeout bounds shutdown + exit plus the kill fallback.
	ShutdownTimeout time.Duration
	ClientName      string
	ClientVersion   string
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
	diags    map[string]*diagSnapshot
	waiters  map[string][]chan struct{}

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
		enc:      EncodingUTF16,
		syncKind: textDocumentSync{Change: TextDocumentSyncKindFull},
		docs:     map[string]*Document{},
		diags:    map[string]*diagSnapshot{},
		waiters:  map[string][]chan struct{}{},
		done:     make(chan struct{}),
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
		Workspace: &workspaceCapabilities{WorkspaceFolders: false},
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
		c.opts.Log.Warnf("lsp: server %s exited unexpectedly: %v", c.opts.Spec.Name, err)
		c.setStatus(func(s *ServerStatus) {
			s.State = StateCrashed
			s.Reason = judgeReason(err)
			s.LastError = judgeReason(err)
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
	return c.status
}

func (c *Client) setStatus(mutate func(*ServerStatus)) {
	c.mu.Lock()
	mutate(&c.status)
	c.mu.Unlock()
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
		version int
		change  *ProtocolRange
	)
	if !opened {
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
	} else {
		full := doc.FullRange(enc)
		doc.Version++
		doc.Content = stripped
		version = doc.Version
		change = &full
	}
	if snap := c.diags[uri]; snap != nil {
		snap.Superseded = true
	}
	text := string(doc.Content)
	languageID := doc.Language
	c.mu.Unlock()

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
		return version, nil
	}

	event := textDocumentContentChangeEvent{Text: text}
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
	return version, nil
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
	for {
		remaining := time.Until(deadline)
		c.mu.Lock()
		snap := c.diags[uri]
		if snap != nil && !snap.Superseded && acceptable(snap, version) && len(snap.Items) > 0 {
			items := append([]Diagnostic(nil), snap.Items...)
			c.mu.Unlock()
			return items, true
		}
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
		c.mu.Unlock()
		return nil, true
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
	waiters := c.waiters[payload.URI]
	delete(c.waiters, payload.URI)
	c.mu.Unlock()
	for _, ch := range waiters {
		close(ch)
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
