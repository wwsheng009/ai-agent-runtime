package transport

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// LifecycleEvent 描述 transport 级别的生命周期事件。
type LifecycleEvent struct {
	Type          string
	TraceID       string
	TransportType string
	SessionID     string
	Payload       map[string]interface{}
	Timestamp     time.Time
}

// LifecycleObserver 订阅 transport 生命周期事件。
type LifecycleObserver func(LifecycleEvent)

// ObservableTransport 暴露可选的 transport 生命周期能力。
type ObservableTransport interface {
	AddLifecycleObserver(LifecycleObserver)
}

type traceIDContextKey struct{}

func WithTraceID(ctx context.Context, traceID string) context.Context {
	if strings.TrimSpace(traceID) == "" {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, traceIDContextKey{}, strings.TrimSpace(traceID))
}

func TraceIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	traceID, _ := ctx.Value(traceIDContextKey{}).(string)
	return strings.TrimSpace(traceID)
}

type lifecycleEmitter struct {
	mu        sync.RWMutex
	observers []LifecycleObserver
}

func (e *lifecycleEmitter) AddLifecycleObserver(observer LifecycleObserver) {
	if e == nil || observer == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.observers = append(e.observers, observer)
}

func (e *lifecycleEmitter) emitLifecycleEvent(traceID, eventType, transportType, sessionID string, payload map[string]interface{}) {
	if e == nil {
		return
	}
	e.mu.RLock()
	observers := append([]LifecycleObserver(nil), e.observers...)
	e.mu.RUnlock()
	if len(observers) == 0 {
		return
	}

	event := LifecycleEvent{
		Type:          eventType,
		TraceID:       traceID,
		TransportType: strings.TrimSpace(transportType),
		SessionID:     strings.TrimSpace(sessionID),
		Payload:       clonePayload(payload),
		Timestamp:     time.Now().UTC(),
	}
	for _, observer := range observers {
		observer(event)
	}
}

type observedMCPTransport struct {
	transportType string
	target        string
	inner         mcp.Transport
	emitter       *lifecycleEmitter
}

func newObservedMCPTransport(transportType, target string, inner mcp.Transport, emitter *lifecycleEmitter) mcp.Transport {
	if inner == nil || emitter == nil {
		return inner
	}
	return &observedMCPTransport{
		transportType: strings.TrimSpace(transportType),
		target:        strings.TrimSpace(target),
		inner:         inner,
		emitter:       emitter,
	}
}

func (t *observedMCPTransport) Connect(ctx context.Context) (mcp.Connection, error) {
	traceID := TraceIDFromContext(ctx)
	payload := t.basePayload()
	t.emitter.emitLifecycleEvent(traceID, "mcp.transport.connecting", t.transportType, "", payload)

	conn, err := t.inner.Connect(ctx)
	if err != nil {
		payload["error"] = err.Error()
		t.emitter.emitLifecycleEvent(traceID, "mcp.transport.connect_failed", t.transportType, "", payload)
		return nil, err
	}

	sessionID := strings.TrimSpace(conn.SessionID())
	t.emitter.emitLifecycleEvent(traceID, "mcp.transport.connected", t.transportType, sessionID, payload)
	return &observedConnection{
		transportType: t.transportType,
		target:        t.target,
		inner:         conn,
		emitter:       t.emitter,
		traceID:       traceID,
		sessionID:     sessionID,
	}, nil
}

func (t *observedMCPTransport) basePayload() map[string]interface{} {
	payload := map[string]interface{}{}
	if t.target != "" {
		payload["target"] = t.target
	}
	return payload
}

type observedConnection struct {
	transportType string
	target        string
	inner         mcp.Connection
	emitter       *lifecycleEmitter
	traceID       string
	sessionID     string
	closeOnce     sync.Once
}

func (c *observedConnection) Read(ctx context.Context) (jsonrpc.Message, error) {
	msg, err := c.inner.Read(ctx)
	traceID := firstNonEmpty(TraceIDFromContext(ctx), c.traceID)
	payload := c.basePayload()
	if err != nil {
		payload["error"] = err.Error()
		c.emitter.emitLifecycleEvent(traceID, "mcp.transport.read_failed", c.transportType, c.sessionID, payload)
		return nil, err
	}
	payload["bytes"] = messageBytes(msg)
	payload["message_type"] = fmt.Sprintf("%T", msg)
	c.emitter.emitLifecycleEvent(traceID, "mcp.transport.read", c.transportType, c.sessionID, payload)
	return msg, nil
}

func (c *observedConnection) Write(ctx context.Context, msg jsonrpc.Message) error {
	traceID := firstNonEmpty(TraceIDFromContext(ctx), c.traceID)
	payload := c.basePayload()
	payload["bytes"] = messageBytes(msg)
	payload["message_type"] = fmt.Sprintf("%T", msg)

	err := c.inner.Write(ctx, msg)
	if err != nil {
		payload["error"] = err.Error()
		c.emitter.emitLifecycleEvent(traceID, "mcp.transport.write_failed", c.transportType, c.sessionID, payload)
		return err
	}
	c.emitter.emitLifecycleEvent(traceID, "mcp.transport.write", c.transportType, c.sessionID, payload)
	return nil
}

func (c *observedConnection) Close() error {
	var err error
	c.closeOnce.Do(func() {
		err = c.inner.Close()
		payload := c.basePayload()
		if err != nil {
			payload["error"] = err.Error()
		}
		c.emitter.emitLifecycleEvent(c.traceID, "mcp.transport.closed", c.transportType, c.sessionID, payload)
	})
	return err
}

func (c *observedConnection) SessionID() string {
	return c.inner.SessionID()
}

func (c *observedConnection) basePayload() map[string]interface{} {
	payload := map[string]interface{}{}
	if c.target != "" {
		payload["target"] = c.target
	}
	return payload
}

func messageBytes(msg jsonrpc.Message) int {
	if msg == nil {
		return 0
	}
	data, err := jsonrpc.EncodeMessage(msg)
	if err != nil {
		return 0
	}
	return len(data)
}

func clonePayload(payload map[string]interface{}) map[string]interface{} {
	if len(payload) == 0 {
		return nil
	}
	cloned := make(map[string]interface{}, len(payload))
	for key, value := range payload {
		cloned[key] = value
	}
	return cloned
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
