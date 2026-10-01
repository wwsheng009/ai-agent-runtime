package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"
)

// fakeDiagnostic is the compact test description of one diagnostic. Positions
// are protocol positions in whatever encoding the fake server negotiated
// (UTF-16 unless a test overrides it), so a test can exercise the encoding
// boundary end to end.
type fakeDiagnostic struct {
	Line     int
	Char     int
	EndChar  int
	Severity int
	Message  string
	Code     string
}

// fakeServer is an in-process LSP peer used by the package tests. It speaks the
// production Content-Length framing (readRPCMessage / the transport writer), so
// tests cover the real wire path without spawning a language server binary.
type fakeServer struct {
	t        *testing.T
	encoding string
	// syncKind is the textDocumentSync change kind advertised in initialize
	// (1 = full, 2 = incremental). Zero means full.
	syncKind int

	// diagnosticsFor is invoked for every didOpen/didChange with the document
	// version the client just announced; nil publishes an empty set.
	diagnosticsFor func(uri string, version int) []fakeDiagnostic
	// diagnosticsOnOpen, when set, replaces diagnosticsFor for didOpen only.
	diagnosticsOnOpen func(uri string, version int) []fakeDiagnostic
	// suppressPublish models a server that never publishes diagnostics for the
	// document (ignored directory / file outside the server's module).
	suppressPublish bool
	// publishDelay postpones diagnostics publishes, modelling a slow first
	// analysis (cold module view) that lands after the normal wait budget.
	publishDelay time.Duration
	// initDelay postpones the initialize response, modelling a handshake that
	// outlives diagnostics.start_wait_ms.
	initDelay time.Duration

	mu       sync.Mutex
	conn     net.Conn
	opened   map[string]string
	versions map[string]int
	methods  []string
	changes  map[string][]didChangeParams
}

func newFakeServer(t *testing.T) *fakeServer {
	t.Helper()
	return &fakeServer{
		t:        t,
		encoding: "utf-16",
		opened:   make(map[string]string),
		versions: make(map[string]int),
		changes:  make(map[string][]didChangeParams),
	}
}

// dial returns a DialFunc that wires the client to the in-process peer over
// net.Pipe, bypassing process spawn while keeping the transport contract.
func (f *fakeServer) dial() DialFunc {
	return func(_ context.Context, _ ServerSpec, _ string, _ Logger) (*DialResult, error) {
		clientConn, serverConn := net.Pipe()
		done := make(chan struct{})
		go func() {
			defer close(done)
			defer serverConn.Close()
			f.serve(serverConn)
		}()
		return &DialResult{
			Conn: clientConn,
			PID:  4242,
			Kill: func() error { return serverConn.Close() },
			Wait: func() error { <-done; return nil },
		}, nil
	}
}

// dialError returns a DialFunc that always fails, modelling a missing binary.
func dialError(err error) DialFunc {
	return func(_ context.Context, _ ServerSpec, _ string, _ Logger) (*DialResult, error) {
		return nil, err
	}
}

func (f *fakeServer) records() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.methods...)
}

// connection returns the peer connection once the serve loop captured it.
func (f *fakeServer) connection() net.Conn {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.conn
}

// setSuppressPublish toggles publish suppression; safe while serving.
func (f *fakeServer) setSuppressPublish(v bool) {
	f.mu.Lock()
	f.suppressPublish = v
	f.mu.Unlock()
}

func (f *fakeServer) count(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	count := 0
	for _, recorded := range f.methods {
		if recorded == method {
			count++
		}
	}
	return count
}

func (f *fakeServer) lastText(uri string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.opened[uri]
}

func (f *fakeServer) record(method string) {
	f.mu.Lock()
	f.methods = append(f.methods, method)
	f.mu.Unlock()
}

// lastChange returns the most recent didChange params for one document.
func (f *fakeServer) lastChange(uri string) (didChangeParams, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	changes := f.changes[uri]
	if len(changes) == 0 {
		return didChangeParams{}, false
	}
	return changes[len(changes)-1], true
}

type fakeRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type fakeResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *fakeRPCError   `json:"error,omitempty"`
}

type fakeIncoming struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

func (f *fakeServer) serve(conn net.Conn) {
	f.mu.Lock()
	f.conn = conn
	f.mu.Unlock()
	reader := bufio.NewReaderSize(conn, 1<<16)
	for {
		body, err := readRPCMessage(reader)
		if err != nil {
			return
		}
		var msg fakeIncoming
		if err := json.Unmarshal(body, &msg); err != nil {
			f.t.Errorf("fake lsp: unmarshal request: %v", err)
			return
		}
		switch msg.Method {
		case "initialize":
			if f.initDelay > 0 {
				time.Sleep(f.initDelay)
			}
			changeKind := f.syncKind
			if changeKind == 0 {
				changeKind = 1
			}
			f.respond(conn, msg.ID, map[string]interface{}{
				"capabilities": map[string]interface{}{
					"positionEncoding": f.encoding,
					"textDocumentSync": changeKind,
				},
				"serverInfo": map[string]interface{}{"name": "fake-lsp", "version": "0.0.1"},
			})
		case "shutdown":
			f.respond(conn, msg.ID, nil)
		case "exit":
			return
		case "textDocument/didOpen":
			var params didOpenParams
			if err := json.Unmarshal(msg.Params, &params); err == nil {
				f.record("didOpen")
				f.mu.Lock()
				f.opened[params.TextDocument.URI] = params.TextDocument.Text
				f.versions[params.TextDocument.URI] = params.TextDocument.Version
				f.mu.Unlock()
				items := f.diagnosticsFor
				if f.diagnosticsOnOpen != nil {
					items = f.diagnosticsOnOpen
				}
				f.mu.Lock()
				suppress := f.suppressPublish
				f.mu.Unlock()
				if !suppress {
					f.publish(conn, params.TextDocument.URI, params.TextDocument.Version, items)
				}
			}
		case "textDocument/didChange":
			var params didChangeParams
			if err := json.Unmarshal(msg.Params, &params); err == nil {
				f.record("didChange")
				f.mu.Lock()
				f.changes[params.TextDocument.URI] = append(f.changes[params.TextDocument.URI], params)
				f.mu.Unlock()
				text := ""
				if len(params.ContentChanges) > 0 {
					text = params.ContentChanges[0].Text
				}
				f.mu.Lock()
				f.opened[params.TextDocument.URI] = text
				f.versions[params.TextDocument.URI] = params.TextDocument.Version
				f.mu.Unlock()
				f.mu.Lock()
				suppress := f.suppressPublish
				f.mu.Unlock()
				if !suppress {
					f.publish(conn, params.TextDocument.URI, params.TextDocument.Version, f.diagnosticsFor)
				}
			}
		case "textDocument/didSave":
			f.record("didSave")
		case "textDocument/didClose":
			f.record("didClose")
		default:
			if msg.ID != nil {
				f.respondError(conn, msg.ID, -32601, "method not found: "+msg.Method)
			}
		}
	}
}

func (f *fakeServer) publish(conn net.Conn, uri string, version int, source func(uri string, version int) []fakeDiagnostic) {
	items := []fakeDiagnostic(nil)
	if source != nil {
		items = source(uri, version)
	}
	diagnostics := make([]map[string]interface{}, 0, len(items))
	for _, item := range items {
		diagnostics = append(diagnostics, map[string]interface{}{
			"range": map[string]interface{}{
				"start": map[string]interface{}{"line": item.Line, "character": item.Char},
				"end":   map[string]interface{}{"line": item.Line, "character": item.EndChar},
			},
			"severity": item.Severity,
			"message":  item.Message,
			"source":   "fake",
			"code":     item.Code,
		})
	}
	message := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "textDocument/publishDiagnostics",
		"params": map[string]interface{}{
			"uri":         uri,
			"version":     version,
			"diagnostics": diagnostics,
		},
	}
	if delay := f.publishDelay; delay > 0 {
		// Asynchronous: a real server keeps reading while it computes, and on
		// net.Pipe a blocking sleep would also stall the client's next write.
		go func() {
			time.Sleep(delay)
			f.write(conn, message)
		}()
		return
	}
	f.write(conn, message)
}

func (f *fakeServer) respond(conn net.Conn, id json.RawMessage, result interface{}) {
	if len(id) == 0 {
		return
	}
	body, err := json.Marshal(result)
	if err != nil {
		f.t.Errorf("fake lsp: marshal result: %v", err)
		return
	}
	f.write(conn, fakeResponse{JSONRPC: "2.0", ID: id, Result: body})
}

func (f *fakeServer) respondError(conn net.Conn, id json.RawMessage, code int, message string) {
	if len(id) == 0 {
		return
	}
	f.write(conn, fakeResponse{JSONRPC: "2.0", ID: id, Error: &fakeRPCError{Code: code, Message: message}})
}

// write frames one message. The mutex serializes console-style writes from the
// serve goroutine with any asynchronous pushes a test may add later.
func (f *fakeServer) write(conn net.Conn, message interface{}) {
	body, err := json.Marshal(message)
	if err != nil {
		f.t.Errorf("fake lsp: marshal message: %v", err)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, err := fmt.Fprintf(conn, "Content-Length: %d\r\n\r\n", len(body)); err != nil {
		return
	}
	if _, err := conn.Write(body); err != nil {
		return
	}
}
