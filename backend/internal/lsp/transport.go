package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// ErrTransportClosed is returned for calls issued after the connection died.
var ErrTransportClosed = errors.New("lsp: transport closed")

// rpcError is a JSON-RPC error object.
type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("lsp rpc error %d: %s", e.Code, e.Message)
}

type rpcMessage struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string           `json:"method,omitempty"`
	Params  json.RawMessage  `json:"params,omitempty"`
	Result  json.RawMessage  `json:"result,omitempty"`
	Error   *rpcError        `json:"error,omitempty"`
}

type notificationHandler func(method string, params json.RawMessage)

// requestHandler handles a server-initiated request. Returning handled=false
// makes the transport answer with a null result.
type requestHandler func(method string, params json.RawMessage) (result interface{}, handled bool)

// transport speaks LSP-framed JSON-RPC (Content-Length headers) over an
// arbitrary byte stream. The stream may be a child process' stdio or an
// in-memory pipe (tests).
type transport struct {
	conn   io.ReadWriteCloser
	logger Logger

	writeMu sync.Mutex
	nextID  atomic.Int64
	pending map[string]chan *rpcMessage
	mu      sync.Mutex

	onNotification notificationHandler
	onRequest      requestHandler

	closed    chan struct{}
	closeOnce sync.Once
	closeErr  error
}

func newTransport(conn io.ReadWriteCloser, logger Logger) *transport {
	return &transport{
		conn:    conn,
		logger:  LoggerOrNop(logger),
		pending: make(map[string]chan *rpcMessage),
		closed:  make(chan struct{}),
	}
}

func (t *transport) setNotificationHandler(h notificationHandler) {
	t.mu.Lock()
	t.onNotification = h
	t.mu.Unlock()
}

func (t *transport) setRequestHandler(h requestHandler) {
	t.mu.Lock()
	t.onRequest = h
	t.mu.Unlock()
}

func (t *transport) start() {
	go t.readLoop()
}

func (t *transport) done() <-chan struct{} { return t.closed }

// closedErr returns the terminal error, if the transport is already dead.
func (t *transport) closedErr() error {
	select {
	case <-t.closed:
		return t.closeErr
	default:
		return nil
	}
}

// call performs a JSON-RPC request and waits for its response or ctx expiry.
func (t *transport) call(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
	if err := t.closedErr(); err != nil {
		return nil, err
	}
	id := t.nextID.Add(1)
	key := strconv.FormatInt(id, 10)

	payload, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("lsp: marshal %s params: %w", method, err)
	}
	rawID := json.RawMessage(key)
	msg := rpcMessage{JSONRPC: "2.0", ID: &rawID, Method: method, Params: payload}

	respCh := make(chan *rpcMessage, 1)
	t.mu.Lock()
	t.pending[key] = respCh
	t.mu.Unlock()

	if err := t.writeMessage(&msg); err != nil {
		t.removePending(key)
		return nil, err
	}

	select {
	case resp := <-respCh:
		if resp == nil {
			return nil, t.terminalError()
		}
		if resp.Error != nil {
			return nil, resp.Error
		}
		return resp.Result, nil
	case <-ctx.Done():
		t.removePending(key)
		return nil, ctx.Err()
	case <-t.closed:
		t.removePending(key)
		return nil, t.terminalError()
	}
}

func (t *transport) notify(method string, params interface{}) error {
	payload, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("lsp: marshal %s params: %w", method, err)
	}
	return t.writeMessage(&rpcMessage{JSONRPC: "2.0", Method: method, Params: payload})
}

func (t *transport) writeMessage(msg *rpcMessage) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("lsp: marshal message: %w", err)
	}
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body))

	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	if err := t.closedErr(); err != nil {
		return err
	}
	if _, err := io.WriteString(t.conn, header); err != nil {
		t.fail(err)
		return err
	}
	if _, err := t.conn.Write(body); err != nil {
		t.fail(err)
		return err
	}
	return nil
}

func (t *transport) readLoop() {
	reader := bufio.NewReaderSize(t.conn, 64*1024)
	for {
		body, err := readRPCMessage(reader)
		if err != nil {
			t.fail(err)
			return
		}
		var msg rpcMessage
		if err := json.Unmarshal(body, &msg); err != nil {
			t.logger.Warnf("lsp: drop malformed message: %v", err)
			continue
		}
		t.dispatch(&msg)
	}
}

func (t *transport) dispatch(msg *rpcMessage) {
	switch {
	case msg.Method == "" && msg.ID != nil:
		t.mu.Lock()
		ch := t.pending[string(*msg.ID)]
		delete(t.pending, string(*msg.ID))
		t.mu.Unlock()
		if ch != nil {
			ch <- msg
		}
	case msg.Method != "" && msg.ID == nil:
		t.mu.Lock()
		handler := t.onNotification
		t.mu.Unlock()
		if handler != nil {
			handler(msg.Method, msg.Params)
		}
	case msg.Method != "" && msg.ID != nil:
		t.handleServerRequest(msg)
	}
}

func (t *transport) handleServerRequest(msg *rpcMessage) {
	t.mu.Lock()
	handler := t.onRequest
	t.mu.Unlock()

	var (
		result  interface{}
		handled bool
	)
	if handler != nil {
		result, handled = handler(msg.Method, msg.Params)
	}
	if !handled {
		// Servers tolerate a null response for capability registration and
		// window requests; workspace/configuration explicitly expects an array.
		if msg.Method == "workspace/configuration" {
			result = t.nullConfigurationResult(msg.Params)
		}
	}
	resp := rpcMessage{JSONRPC: "2.0", ID: msg.ID}
	if result != nil {
		payload, err := json.Marshal(result)
		if err == nil {
			resp.Result = payload
		}
	} else {
		resp.Result = json.RawMessage("null")
	}
	if err := t.writeMessage(&resp); err != nil {
		t.logger.Debugf("lsp: reply to %s failed: %v", msg.Method, err)
	}
}

func (t *transport) nullConfigurationResult(params json.RawMessage) []interface{} {
	var request struct {
		Items []json.RawMessage `json:"items"`
	}
	_ = json.Unmarshal(params, &request)
	return make([]interface{}, len(request.Items))
}

func (t *transport) removePending(key string) {
	t.mu.Lock()
	delete(t.pending, key)
	t.mu.Unlock()
}

func (t *transport) terminalError() error {
	if t.closeErr != nil {
		return t.closeErr
	}
	return ErrTransportClosed
}

// fail records the first terminal error and unblocks every waiter.
func (t *transport) fail(err error) {
	t.closeOnce.Do(func() {
		if err == nil || errors.Is(err, io.EOF) {
			err = ErrTransportClosed
		}
		t.closeErr = err
		close(t.closed)
		t.mu.Lock()
		pending := t.pending
		t.pending = make(map[string]chan *rpcMessage)
		t.mu.Unlock()
		for key, ch := range pending {
			_ = key
			close(ch)
		}
	})
}

// close shuts the transport down; the underlying connection is closed by the
// process owner, not here, so shutdown notifications can still be flushed.
func (t *transport) close() {
	t.fail(ErrTransportClosed)
}

func readRPCMessage(reader *bufio.Reader) ([]byte, error) {
	contentLength := -1
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			length, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil || length < 0 {
				return nil, fmt.Errorf("lsp: invalid Content-Length %q", value)
			}
			contentLength = length
		}
	}
	if contentLength < 0 {
		return nil, errors.New("lsp: missing Content-Length header")
	}
	body := make([]byte, contentLength)
	if _, err := io.ReadFull(reader, body); err != nil {
		return nil, err
	}
	return body, nil
}
