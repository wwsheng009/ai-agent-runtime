package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/tunnel"
)

// ---------------------------------------------------------------------------
// 测试替身

type fakeRegistry struct {
	nodes map[string]*tunnel.Node
}

func newFakeRegistry(nodes ...*tunnel.Node) *fakeRegistry {
	registry := &fakeRegistry{nodes: make(map[string]*tunnel.Node)}
	for _, node := range nodes {
		registry.nodes[node.NodeID] = node
	}
	return registry
}

func (r *fakeRegistry) List() []*tunnel.Node {
	out := make([]*tunnel.Node, 0, len(r.nodes))
	for _, node := range r.nodes {
		out = append(out, node)
	}
	return out
}

func (r *fakeRegistry) Get(nodeID string) (*tunnel.Node, bool) {
	node, ok := r.nodes[nodeID]
	return node, ok
}

type proxyCall struct {
	nodeID string
	path   string
	body   []byte
}

// fakeProxy 记录转发调用并按 respond 脚本返回响应；respond 为 nil 时模拟 502。
type fakeProxy struct {
	mu      sync.Mutex
	calls   []proxyCall
	respond func(nodeID string, r *http.Request) (status int, body string)
	block   bool
}

func (p *fakeProxy) ServeProxy(w http.ResponseWriter, r *http.Request, nodeID, path string) {
	body, _ := io.ReadAll(r.Body)
	p.mu.Lock()
	p.calls = append(p.calls, proxyCall{nodeID: nodeID, path: path, body: body})
	block := p.block
	p.mu.Unlock()
	if block {
		// 模拟节点长时间不返回：等调用方 ctx 超时。
		<-r.Context().Done()
		http.Error(w, "node offline", http.StatusBadGateway)
		return
	}
	if p.respond == nil {
		http.Error(w, "node offline", http.StatusBadGateway)
		return
	}
	status, respBody := p.respond(nodeID, r)
	if status == 0 {
		status = http.StatusOK
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, respBody)
}

func (p *fakeProxy) lastCall(t *testing.T) proxyCall {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.calls) == 0 {
		t.Fatal("proxy was not called")
	}
	return p.calls[len(p.calls)-1]
}

// relayProxy 把 ServeProxy 调用真实转发到 httptest 节点（模拟隧道 agent 的
// forwardStream 行为，用于端到端解析验证）。
type relayProxy struct {
	base string
}

func (p *relayProxy) ServeProxy(w http.ResponseWriter, r *http.Request, _ string, path string) {
	body, _ := io.ReadAll(r.Body)
	req, err := http.NewRequestWithContext(r.Context(), r.Method, strings.TrimRight(p.base, "/")+path, bytes.NewReader(body))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	req.Header.Set("Content-Type", r.Header.Get("Content-Type"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(data)
}

func liveNode(id string) *tunnel.Node {
	return &tunnel.Node{NodeID: id, Routes: []string{"/web", "/web/api"}, LastHeartbeat: time.Now()}
}

func staleNode(id string) *tunnel.Node {
	return &tunnel.Node{NodeID: id, Routes: []string{"/web", "/web/api"}, Stale: true}
}

func invokeResponse(status, reason, sessionID, assistant string) string {
	payload := map[string]any{"status": status, "session_id": sessionID}
	if reason != "" {
		payload["reason"] = reason
	}
	if assistant != "" {
		payload["assistant"] = map[string]any{"role": "assistant", "content": assistant}
	}
	data, _ := json.Marshal(payload)
	return string(data)
}

func testBackend(registry *fakeRegistry, proxy *fakeProxy, store connectors.Store) *gatewayNodeBackend {
	backend := newGatewayNodeBackend(registry, proxy, store)
	if store == nil {
		store = connectors.NewMemoryStore()
	}
	backend.store = store
	return backend
}

// ---------------------------------------------------------------------------
// 节点选择

func TestGatewayNodeBackendAutoSelectsSingleLiveNode(t *testing.T) {
	registry := newFakeRegistry(liveNode("node-a"), staleNode("node-old"))
	proxy := &fakeProxy{respond: func(string, *http.Request) (int, string) {
		return http.StatusOK, invokeResponse("completed", "", "sess-1", "你好，收到")
	}}
	backend := testBackend(registry, proxy, connectors.NewMemoryStore(connectors.Config{ID: "tg-1", Enabled: true, Settings: map[string]string{}}))

	result, err := backend.Handle(context.Background(), connectors.BackendRequest{ConnectorID: "tg-1", Text: "hi"}, nil)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	call := proxy.lastCall(t)
	if call.nodeID != "node-a" {
		t.Fatalf("node = %q, want node-a (stale excluded)", call.nodeID)
	}
	if call.path != gatewayNodeInvokePath {
		t.Fatalf("path = %q, want %q", call.path, gatewayNodeInvokePath)
	}
	if result.Session.NodeID != "node-a" || result.Session.SessionID != "sess-1" {
		t.Fatalf("session = %+v", result.Session)
	}
	if len(result.Reply) != 1 || result.Reply[0].Text != "你好，收到" {
		t.Fatalf("reply = %+v", result.Reply)
	}
	var sent gatewayNodeInvokeRequest
	if err := json.Unmarshal(call.body, &sent); err != nil {
		t.Fatalf("decode sent body: %v", err)
	}
	if sent.Prompt != "hi" || sent.SessionID != "" || sent.TimeoutMs <= 0 {
		t.Fatalf("sent request = %+v", sent)
	}
}

func TestGatewayNodeBackendHonorsSettingsNodeID(t *testing.T) {
	registry := newFakeRegistry(liveNode("node-a"), liveNode("node-b"))
	proxy := &fakeProxy{}
	proxy.respond = func(nodeID string, _ *http.Request) (int, string) {
		if nodeID != "node-b" {
			return http.StatusBadGateway, `{"error":"wrong node"}`
		}
		return http.StatusOK, invokeResponse("settled", "", "sess-b", "done")
	}
	store := connectors.NewMemoryStore(connectors.Config{ID: "qq-1", Enabled: true, Settings: map[string]string{"node_id": "node-b"}})
	backend := testBackend(registry, proxy, store)

	result, err := backend.Handle(context.Background(), connectors.BackendRequest{ConnectorID: "qq-1", Text: "hi"}, nil)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got := proxy.lastCall(t).nodeID; got != "node-b" {
		t.Fatalf("node = %q, want node-b", got)
	}
	if result.Session.NodeID != "node-b" || result.Session.SessionID != "sess-b" || len(result.Reply) != 1 || result.Reply[0].Text != "done" {
		t.Fatalf("result = %+v", result)
	}
}

func TestGatewayNodeBackendMultipleLiveNodesRequireNodeID(t *testing.T) {
	registry := newFakeRegistry(liveNode("node-a"), liveNode("node-b"))
	backend := testBackend(registry, &fakeProxy{}, connectors.NewMemoryStore(connectors.Config{ID: "tg-1", Enabled: true}))
	_, err := backend.Handle(context.Background(), connectors.BackendRequest{ConnectorID: "tg-1", Text: "hi"}, nil)
	if err == nil || !strings.Contains(err.Error(), "node_id") {
		t.Fatalf("err = %v, want 'node_id' hint", err)
	}
}

func TestGatewayNodeBackendNoLiveNodes(t *testing.T) {
	registry := newFakeRegistry(staleNode("node-old"))
	backend := testBackend(registry, &fakeProxy{}, connectors.NewMemoryStore(connectors.Config{ID: "tg-1", Enabled: true}))
	_, err := backend.Handle(context.Background(), connectors.BackendRequest{ConnectorID: "tg-1", Text: "hi"}, nil)
	if err == nil || !strings.Contains(err.Error(), "无在线节点") {
		t.Fatalf("err = %v, want 无在线节点", err)
	}
}

func TestGatewayNodeBackendExplicitNodeOffline(t *testing.T) {
	registry := newFakeRegistry(liveNode("node-a"))
	store := connectors.NewMemoryStore(connectors.Config{ID: "tg-1", Enabled: true, Settings: map[string]string{"node_id": "node-ghost"}})
	backend := testBackend(registry, &fakeProxy{}, store)
	_, err := backend.Handle(context.Background(), connectors.BackendRequest{ConnectorID: "tg-1", Text: "hi"}, nil)
	if err == nil || !strings.Contains(err.Error(), "node-ghost") {
		t.Fatalf("err = %v, want explicit node missing", err)
	}
}

// ---------------------------------------------------------------------------
// 调用语义

func TestGatewayNodeBackendPassesSessionForSameNodeOnly(t *testing.T) {
	registry := newFakeRegistry(liveNode("node-a"))
	proxy := &fakeProxy{respond: func(string, *http.Request) (int, string) {
		return http.StatusOK, invokeResponse("completed", "", "sess-1", "ok")
	}}
	backend := testBackend(registry, proxy, connectors.NewMemoryStore(connectors.Config{ID: "tg-1", Enabled: true}))

	_, err := backend.Handle(context.Background(), connectors.BackendRequest{
		ConnectorID: "tg-1",
		Text:        "hi",
		Session:     connectors.SessionRef{NodeID: "node-a", SessionID: "sess-1"},
	}, nil)
	if err != nil {
		t.Fatalf("Handle(same node): %v", err)
	}
	var sent gatewayNodeInvokeRequest
	_ = json.Unmarshal(proxy.lastCall(t).body, &sent)
	if sent.SessionID != "sess-1" {
		t.Fatalf("same-node session_id = %q, want sess-1", sent.SessionID)
	}

	_, err = backend.Handle(context.Background(), connectors.BackendRequest{
		ConnectorID: "tg-1",
		Text:        "hi",
		Session:     connectors.SessionRef{NodeID: "node-b", SessionID: "sess-b"},
	}, nil)
	if err != nil {
		t.Fatalf("Handle(other node): %v", err)
	}
	sent = gatewayNodeInvokeRequest{}
	_ = json.Unmarshal(proxy.lastCall(t).body, &sent)
	if sent.SessionID != "" {
		t.Fatalf("cross-node session_id = %q, want empty", sent.SessionID)
	}
}

func TestGatewayNodeBackendTimeout(t *testing.T) {
	registry := newFakeRegistry(liveNode("node-a"))
	proxy := &fakeProxy{block: true}
	backend := testBackend(registry, proxy, connectors.NewMemoryStore(connectors.Config{ID: "tg-1", Enabled: true}))
	backend.timeout = 80 * time.Millisecond

	started := time.Now()
	_, err := backend.Handle(context.Background(), connectors.BackendRequest{ConnectorID: "tg-1", Text: "hi"}, nil)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "超时") && !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("err = %v, want timeout", err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("timeout not enforced: elapsed %s", elapsed)
	}
}

func TestGatewayNodeBackendPropagatesNodeErrors(t *testing.T) {
	registry := newFakeRegistry(liveNode("node-a"))

	// 200 + status=timeout：节点侧等待超时是业务失败，不应算成功。
	proxy := &fakeProxy{respond: func(string, *http.Request) (int, string) {
		return http.StatusOK, invokeResponse("timeout", "turn did not finish", "sess-1", "")
	}}
	backend := testBackend(registry, proxy, connectors.NewMemoryStore(connectors.Config{ID: "tg-1", Enabled: true}))
	_, err := backend.Handle(context.Background(), connectors.BackendRequest{ConnectorID: "tg-1", Text: "hi"}, nil)
	if err == nil || !strings.Contains(err.Error(), "timeout") || !strings.Contains(err.Error(), "turn did not finish") {
		t.Fatalf("err = %v, want status/reason propagated", err)
	}

	// 409 busy：HTTP 层错误应暴露状态码与响应体摘要。
	proxy = &fakeProxy{respond: func(string, *http.Request) (int, string) {
		return http.StatusConflict, `{"status":"busy","reason":"no active chat session"}`
	}}
	backend = testBackend(registry, proxy, connectors.NewMemoryStore(connectors.Config{ID: "tg-1", Enabled: true}))
	_, err = backend.Handle(context.Background(), connectors.BackendRequest{ConnectorID: "tg-1", Text: "hi"}, nil)
	if err == nil || !strings.Contains(err.Error(), "409") || !strings.Contains(err.Error(), "no active chat session") {
		t.Fatalf("err = %v, want HTTP 409 + body summary", err)
	}
}

// TestGatewayNodeBackendThroughHttptestNode 用 httptest 节点 + 转发型假代理验证
// 完整请求/响应链路（等价于隧道 agent 的本地转发）。
func TestGatewayNodeBackendThroughHttptestNode(t *testing.T) {
	var (
		mu       sync.Mutex
		lastBody gatewayNodeInvokeRequest
	)
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != gatewayNodeInvokePath {
			http.NotFound(w, r)
			return
		}
		var req gatewayNodeInvokeRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		lastBody = req
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, invokeResponse("completed", "", "sess-42", "来自节点的回复"))
	}))
	defer node.Close()

	registry := newFakeRegistry(liveNode("node-a"))
	backend := newGatewayNodeBackend(registry, &relayProxy{base: node.URL}, connectors.NewMemoryStore(connectors.Config{ID: "feishu-1", Enabled: true}))

	result, err := backend.Handle(context.Background(), connectors.BackendRequest{ConnectorID: "feishu-1", Text: "帮我看看构建"}, nil)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	mu.Lock()
	sent := lastBody
	mu.Unlock()
	if sent.Prompt != "帮我看看构建" {
		t.Fatalf("node received prompt %q", sent.Prompt)
	}
	if result.Session.NodeID != "node-a" || result.Session.SessionID != "sess-42" {
		t.Fatalf("session = %+v", result.Session)
	}
	if len(result.Reply) != 1 || result.Reply[0].Text != "来自节点的回复" {
		t.Fatalf("reply = %+v", result.Reply)
	}
}
