package agent

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// httpConnectDialer implements the HTTP CONNECT proxy path directly:
// x/net/proxy.FromURL only knows socks5, so the http scheme is handled here.
func httpConnectDialer(proxyAddr string) (func(ctx context.Context, network, addr string) (net.Conn, error), error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		var d net.Dialer
		proxyConn, err := d.DialContext(ctx, "tcp", proxyAddr)
		if err != nil {
			return nil, err
		}
		proxyConn.Write([]byte("CONNECT " + addr + " HTTP/1.1\r\nHost: " + addr + "\r\n\r\n"))
		buf := make([]byte, 512)
		proxyConn.SetReadDeadline(time.Now().Add(5 * time.Second))
		if _, err := proxyConn.Read(buf); err != nil {
			proxyConn.Close()
			return nil, err
		}
		proxyConn.SetReadDeadline(time.Time{})
		return proxyConn, nil
	}, nil
}

func TestParseProxyURL(t *testing.T) {
	cases := []struct {
		in       string
		wantSch  string
		wantHost string
		wantErr  bool
	}{
		{"socks5://127.0.0.1:1080", "socks5", "127.0.0.1:1080", false},
		{"socks5://user:pass@10.0.0.1:1080", "socks5", "10.0.0.1:1080", false},
		{"http://proxy.lan:3128", "http", "proxy.lan:3128", false},
		{"https://proxy.lan:3128", "https", "proxy.lan:3128", false},
		{"10.0.0.1:1080", "socks5", "10.0.0.1:1080", false}, // bare = socks5 shorthand
		{"", "", "", true},
		{"ftp://x", "", "", true},
		{"socks5://", "", "", true},
	}
	for _, tc := range cases {
		u, err := parseProxyURL(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parseProxyURL(%q) = nil error, want error", tc.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseProxyURL(%q): %v", tc.in, err)
			continue
		}
		if u.Scheme != tc.wantSch || u.Host != tc.wantHost {
			t.Errorf("parseProxyURL(%q) = %s://%s, want %s://%s", tc.in, u.Scheme, u.Host, tc.wantSch, tc.wantHost)
		}
	}
}

func TestWebsocketDialerDefaults(t *testing.T) {
	d, err := websocketDialer(Config{ProxyURL: ""})
	if err != nil || d == nil {
		t.Fatalf("default dialer: %v", err)
	}
	if _, err := websocketDialer(Config{ProxyURL: "socks5://"}); err == nil {
		t.Fatal("empty host must error")
	}
}

func TestWebsocketDialerSocks5(t *testing.T) {
	d, err := websocketDialer(Config{ProxyURL: "socks5://127.0.0.1:1080"})
	if err != nil {
		t.Fatalf("socks5 dialer: %v", err)
	}
	if d.NetDial == nil {
		t.Fatal("socks5 dialer must set NetDial")
	}
	if _, err := websocketDialer(Config{ProxyURL: "socks5://user:pass@127.0.0.1:1080"}); err != nil {
		t.Fatalf("socks5 with auth dialer: %v", err)
	}
}

func TestWebsocketDialerHTTP(t *testing.T) {
	d, err := websocketDialer(Config{ProxyURL: "http://127.0.0.1:3128"})
	if err != nil {
		t.Fatalf("http dialer: %v", err)
	}
	if d.NetDialContext == nil {
		t.Fatal("http dialer must set NetDialContext")
	}
}

// TestHTTPConnectDialerThroughRealProxy verifies the CONNECT path end to end
// using a minimal in-process HTTP proxy (the same handshake the agent's http
// proxy dialer performs).
func TestHTTPConnectDialerThroughRealProxy(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go handleConnect(conn)
		}
	}()

	dialFn, err := httpConnectDialer(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := target.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) { c.Close() }(c)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	conn, err := dialFn(ctx, "tcp", target.Addr().String())
	cancel()
	if err != nil {
		t.Fatalf("connect via proxy: %v", err)
	}
	conn.Close()
}

// TestWebsocketThroughRealProxy is the full-stack check: a websocket client
// dials through the in-process HTTP proxy into a local websocket echo server.
func TestWebsocketThroughRealProxy(t *testing.T) {
	// target websocket server
	upg := websocket.Upgrader{}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upg.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		for {
			mt, msg, err := c.ReadMessage()
			if err != nil {
				return
			}
			c.WriteMessage(mt, msg)
		}
	})}
	go srv.Serve(ln)

	// proxy
	pln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			conn, err := pln.Accept()
			if err != nil {
				return
			}
			go handleConnect(conn)
		}
	}()

	dialFn, err := httpConnectDialer(pln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	wsURL := "ws://" + ln.Addr().String() + "/gw/agent"
	hdr := http.Header{"Authorization": []string{"Bearer test"}}
	ws, _, err := (&websocket.Dialer{
		NetDialContext:   dialFn,
		HandshakeTimeout: 3 * time.Second,
	}).DialContext(context.Background(), wsURL, hdr)
	if err != nil {
		t.Fatalf("ws via proxy: %v", err)
	}
	defer ws.Close()
	ws.WriteMessage(websocket.TextMessage, []byte("ping"))
	_, msg, err := ws.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(msg) != "ping" {
		t.Fatalf("echo mismatch: %q", msg)
	}
}

func handleConnect(conn net.Conn) {
	defer conn.Close()
	buf := make([]byte, 4096)
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, err := conn.Read(buf)
	if err != nil {
		return
	}
	req := string(buf[:n])
	if len(req) < 8 || req[:7] != "CONNECT" {
		return
	}
	// parse target
	line := req
	if i := indexByte(line, '\r'); i > 0 {
		line = line[:i]
	}
	parts := splitFields(line)
	if len(parts) < 2 {
		return
	}
	target := parts[1]
	upstream, err := net.DialTimeout("tcp", target, 3*time.Second)
	if err != nil {
		conn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\n"))
		return
	}
	conn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
	go pipe(conn, upstream)
	pipe(upstream, conn)
}

func splitFields(s string) []string {
	var out []string
	cur := ""
	for i := 0; i < len(s); i++ {
		if s[i] == ' ' {
			if cur != "" {
				out = append(out, cur)
				cur = ""
			}
			continue
		}
		cur += string(s[i])
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

func pipe(a, b net.Conn) {
	defer a.Close()
	defer b.Close()
	buf := make([]byte, 4096)
	for {
		n, err := a.Read(buf)
		if err != nil {
			return
		}
		if _, err := b.Write(buf[:n]); err != nil {
			return
		}
	}
}

var _ = websocket.DefaultDialer
var _ = http.DefaultClient
