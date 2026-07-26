package acp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
)

// JSON-RPC 2.0 error codes.
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
)

// JSONRPCVersion is the fixed JSON-RPC version string.
const JSONRPCVersion = "2.0"

// Message is a JSON-RPC 2.0 message (request, response, or notification).
type Message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// RPCError is a JSON-RPC 2.0 error object.
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message == "" {
		return fmt.Sprintf("jsonrpc error %d", e.Code)
	}
	return fmt.Sprintf("jsonrpc error %d: %s", e.Code, e.Message)
}

// NewRPCError builds an RPCError.
func NewRPCError(code int, message string) *RPCError {
	return &RPCError{Code: code, Message: message}
}

// IsNotification reports whether the message has no id (notification).
func (m Message) IsNotification() bool {
	return len(m.ID) == 0 || string(m.ID) == "null"
}

// IsResponse reports whether the message is a response (has id, no method).
func (m Message) IsResponse() bool {
	return m.Method == "" && !m.IsNotification()
}

// Handler processes an inbound request or notification.
// For notifications, respond should not be called (or is a no-op).
type Handler func(ctx context.Context, msg Message) (result interface{}, err *RPCError)

// Conn is a bidirectional NDJSON JSON-RPC 2.0 connection.
//
// Serve only reads and demuxes: inbound *requests/notifications* are handled
// asynchronously so the read loop stays free to deliver peer responses and
// accept session/cancel while a long method (e.g. session/prompt) is in flight.
// Outbound client requests (agent → peer) may be issued concurrently from other
// goroutines; responses are matched by id.
type Conn struct {
	reader *bufio.Reader
	writer io.Writer

	writeMu sync.Mutex

	handler Handler

	pendingMu sync.Mutex
	pending   map[string]chan Message

	nextID atomic.Uint64

	// inFlight tracks async handler goroutines for clean shutdown.
	inFlight sync.WaitGroup

	closed   atomic.Bool
	closeErr error
	closeMu  sync.Mutex
}

// NewConn creates a Conn over r/w. w should be stdout for ACP agents.
func NewConn(r io.Reader, w io.Writer) *Conn {
	return &Conn{
		reader:  bufio.NewReader(r),
		writer:  w,
		pending: make(map[string]chan Message),
	}
}

// SetHandler installs the inbound request/notification handler.
func (c *Conn) SetHandler(h Handler) {
	c.handler = h
}

// Serve reads NDJSON messages until EOF or a fatal transport error.
// It blocks; cancel ctx to stop accepting new work after the current line.
// In-flight request handlers are allowed to finish writing responses before
// the connection is marked closed, so a final request on EOF still gets a reply.
func (c *Conn) Serve(ctx context.Context) error {
	if c == nil {
		return errors.New("acp: nil conn")
	}
	var serveErr error
loop:
	for {
		if ctx.Err() != nil {
			serveErr = ctx.Err()
			break loop
		}
		line, err := c.reader.ReadBytes('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				line = bytes.TrimSpace(line)
				if len(line) > 0 {
					c.handleLine(ctx, line)
				}
				serveErr = nil
				break loop
			}
			serveErr = err
			break loop
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		c.handleLine(ctx, line)
	}
	// Let async request handlers finish writing responses before closing.
	c.inFlight.Wait()
	if serveErr == nil {
		c.closeWith(io.EOF)
		return nil
	}
	c.closeWith(serveErr)
	return serveErr
}

func (c *Conn) handleLine(ctx context.Context, line []byte) {
	var msg Message
	if decodeErr := json.Unmarshal(line, &msg); decodeErr != nil {
		_ = c.writeMessage(Message{
			JSONRPC: JSONRPCVersion,
			Error:   NewRPCError(CodeParseError, "parse error: "+decodeErr.Error()),
		})
		return
	}
	if msg.JSONRPC == "" {
		msg.JSONRPC = JSONRPCVersion
	}
	if msg.IsResponse() {
		c.deliverResponse(msg)
		return
	}
	c.dispatch(ctx, msg)
}

func (c *Conn) dispatch(ctx context.Context, msg Message) {
	if c.handler == nil {
		if !msg.IsNotification() {
			_ = c.writeMessage(Message{
				JSONRPC: JSONRPCVersion,
				ID:      msg.ID,
				Error:   NewRPCError(CodeMethodNotFound, "no handler registered"),
			})
		}
		return
	}
	// Never block the read loop on handlers. session/prompt can run for a long
	// time and may itself Call the peer (request_permission); cancel and
	// response demux must keep flowing on Serve.
	c.inFlight.Add(1)
	go func() {
		defer c.inFlight.Done()
		c.runHandler(ctx, msg)
	}()
}

func (c *Conn) runHandler(ctx context.Context, msg Message) {
	result, rpcErr := c.handler(ctx, msg)
	if msg.IsNotification() {
		return
	}
	if rpcErr != nil {
		_ = c.writeMessage(Message{
			JSONRPC: JSONRPCVersion,
			ID:      msg.ID,
			Error:   rpcErr,
		})
		return
	}
	raw, err := marshalResult(result)
	if err != nil {
		_ = c.writeMessage(Message{
			JSONRPC: JSONRPCVersion,
			ID:      msg.ID,
			Error:   NewRPCError(CodeInternalError, "marshal result: "+err.Error()),
		})
		return
	}
	_ = c.writeMessage(Message{
		JSONRPC: JSONRPCVersion,
		ID:      msg.ID,
		Result:  raw,
	})
}

func marshalResult(result interface{}) (json.RawMessage, error) {
	if result == nil {
		return json.RawMessage("null"), nil
	}
	if raw, ok := result.(json.RawMessage); ok {
		if len(raw) == 0 {
			return json.RawMessage("null"), nil
		}
		return raw, nil
	}
	return json.Marshal(result)
}

func (c *Conn) deliverResponse(msg Message) {
	key := idKey(msg.ID)
	c.pendingMu.Lock()
	ch, ok := c.pending[key]
	if ok {
		delete(c.pending, key)
	}
	c.pendingMu.Unlock()
	if !ok {
		return
	}
	select {
	case ch <- msg:
	default:
	}
}

// Notify sends a JSON-RPC notification (no id).
func (c *Conn) Notify(method string, params interface{}) error {
	raw, err := marshalParams(params)
	if err != nil {
		return err
	}
	return c.writeMessage(Message{
		JSONRPC: JSONRPCVersion,
		Method:  method,
		Params:  raw,
	})
}

// Call sends a JSON-RPC request and waits for the matching response.
func (c *Conn) Call(ctx context.Context, method string, params interface{}, result interface{}) error {
	if c.closed.Load() {
		return errors.New("acp: connection closed")
	}
	raw, err := marshalParams(params)
	if err != nil {
		return err
	}
	id := c.allocateID()
	idRaw, err := json.Marshal(id)
	if err != nil {
		return err
	}
	ch := make(chan Message, 1)
	key := idKey(idRaw)
	c.pendingMu.Lock()
	c.pending[key] = ch
	c.pendingMu.Unlock()
	defer func() {
		c.pendingMu.Lock()
		delete(c.pending, key)
		c.pendingMu.Unlock()
	}()

	if err := c.writeMessage(Message{
		JSONRPC: JSONRPCVersion,
		ID:      idRaw,
		Method:  method,
		Params:  raw,
	}); err != nil {
		return err
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case msg, ok := <-ch:
		if !ok {
			return errors.New("acp: response channel closed")
		}
		if msg.Error != nil {
			return msg.Error
		}
		if result == nil {
			return nil
		}
		if len(msg.Result) == 0 || string(msg.Result) == "null" {
			return nil
		}
		return json.Unmarshal(msg.Result, result)
	}
}

func marshalParams(params interface{}) (json.RawMessage, error) {
	if params == nil {
		return nil, nil
	}
	if raw, ok := params.(json.RawMessage); ok {
		return raw, nil
	}
	return json.Marshal(params)
}

func (c *Conn) allocateID() uint64 {
	return c.nextID.Add(1)
}

func idKey(id json.RawMessage) string {
	return string(bytes.TrimSpace(id))
}

func (c *Conn) writeMessage(msg Message) error {
	if c == nil || c.writer == nil {
		return errors.New("acp: nil writer")
	}
	if msg.JSONRPC == "" {
		msg.JSONRPC = JSONRPCVersion
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.closed.Load() {
		return errors.New("acp: connection closed")
	}
	if _, err := c.writer.Write(data); err != nil {
		return err
	}
	_, err = c.writer.Write([]byte("\n"))
	return err
}

func (c *Conn) closeWith(err error) {
	c.closeMu.Lock()
	defer c.closeMu.Unlock()
	if c.closed.Swap(true) {
		return
	}
	c.closeErr = err
	c.pendingMu.Lock()
	for key, ch := range c.pending {
		close(ch)
		delete(c.pending, key)
	}
	c.pendingMu.Unlock()
}

// Wait waits for in-flight request handlers to finish (best-effort shutdown).
func (c *Conn) Wait() {
	c.inFlight.Wait()
}

// DecodeParams unmarshals msg.Params into dest.
func DecodeParams(msg Message, dest interface{}) *RPCError {
	if len(msg.Params) == 0 || string(msg.Params) == "null" {
		// Allow empty params for methods that tolerate it.
		return nil
	}
	if err := json.Unmarshal(msg.Params, dest); err != nil {
		return NewRPCError(CodeInvalidParams, "invalid params: "+err.Error())
	}
	return nil
}
