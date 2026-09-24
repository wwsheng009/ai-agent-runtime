package main

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/http2"
)

// TestNewChatWebH2CHandler_NegotiatesCleartextHTTP2 验证调试/Web 端点的 h2c 包装
// 真的能把明文 HTTP/2 协商起来（§4.2.1 连接预算：多条常驻 SSE 复用一条 TCP
// 连接，绕开 HTTP/1.1 单 host 约 6 条并发连接的上限），同时不改变普通 HTTP/1.1
// 客户端的行为（浏览器对明文 HTTP/2 尚不启用，必须保持逐字兼容）。
//
// 回归点：过去 Handler 是裸 mux（h2c.NewHandler 缺失），h2c 客户端只能退回
// HTTP/1.1，多标签页下连接池被常驻流占满、后续 /web/api/* 永久排队。
func TestNewChatWebH2CHandler_NegotiatesCleartextHTTP2(t *testing.T) {
	// 与 commands.HandleChatWebAPIEvents 同形的 SSE 响应头：Connection 在 h2 下
	// 属于逐跳头（必须被剥离），服务端不得因此 5xx——这是「多路复用后 SSE 仍可用」
	// 的前提，也是 h2c 包装最容易踩的坑。
	handler := newChatWebH2CHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		_, _ = io.WriteString(w, "event: connected\ndata: {\"ok\":true}\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	srv := httptest.NewUnstartedServer(handler)
	srv.Start()
	t.Cleanup(srv.Close)

	// 1) HTTP/1.1 客户端（浏览器现状）：协议与响应头与包装前一致。
	h1 := probeDebugEndpoint(t, srv.URL, http.DefaultTransport)
	if h1.proto != "HTTP/1.1" {
		t.Fatalf("HTTP/1.1 客户端协商到 %q，h2c 必须是纯增量（不得强制升级）", h1.proto)
	}
	if h1.status != http.StatusOK || !strings.Contains(h1.body, "event: connected") {
		t.Fatalf("HTTP/1.1 响应 = %d %q", h1.status, h1.body)
	}
	if h1.connectionHeader != "keep-alive" {
		t.Fatalf("HTTP/1.1 响应的 Connection = %q，want keep-alive（历史行为）", h1.connectionHeader)
	}

	// 2) h2c 客户端（prior knowledge，等价于 Go 客户端/脚本/代理）：同一 handler
	// 协商出明文 HTTP/2，且 SSE 帧照常送达。
	h2 := probeDebugEndpoint(t, srv.URL, &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
	})
	if !strings.HasPrefix(h2.proto, "HTTP/2") {
		t.Fatalf("h2c 未协商成功：proto = %q（连接预算依赖它复用流）", h2.proto)
	}
	if h2.status != http.StatusOK || !strings.Contains(h2.body, "event: connected") {
		t.Fatalf("h2c 响应 = %d %q", h2.status, h2.body)
	}
	// RFC 7540 §8.1.2.2：逐跳头在 h2 下必须被剥离（而不是让 handler 失败）。
	if h2.connectionHeader != "" {
		t.Fatalf("h2c 响应的 Connection = %q，want 空（逐跳头应被剥离）", h2.connectionHeader)
	}
}

// debugEndpointProbe 是一次端点探测结果（协议/状态/正文/逐跳头）。
type debugEndpointProbe struct {
	proto            string
	status           int
	body             string
	connectionHeader string
}

func probeDebugEndpoint(t *testing.T, url string, rt http.RoundTripper) debugEndpointProbe {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	client := &http.Client{Transport: rt, Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读取响应失败: %v", err)
	}
	return debugEndpointProbe{
		proto:            resp.Proto,
		status:           resp.StatusCode,
		body:             string(body),
		connectionHeader: resp.Header.Get("Connection"),
	}
}
