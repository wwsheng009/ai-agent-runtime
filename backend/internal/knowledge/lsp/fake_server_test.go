package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	baselsp "github.com/wwsheng009/ai-agent-runtime/internal/lsp"
)

// fake_server_test.go 是本包的最小脚本化 LSP 服务器（进程内，走真实
// Content-Length 帧）。与 knowledge 包的同名夹具刻意分开：knowledge 导入本包，
// 本包不能再导入 knowledge，共享夹具会构成 import 环。

type fakeServer struct {
	mu   sync.Mutex
	conn net.Conn
	once sync.Once
	done chan struct{}
}

// newFakeClient 起一个脚本化服务器并返回连好的 *baselsp.Client。
func newFakeClient(t *testing.T) *baselsp.Client {
	t.Helper()
	dial := newFakeServer(t).dial()
	client, err := baselsp.NewClient(baselsp.ClientOptions{
		Spec:            baselsp.ServerSpec{Name: "gopls", Command: "fake-lsp"},
		Root:            t.TempDir(),
		Dial:            dial,
		StartupTimeout:  5 * time.Second,
		ShutdownTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return client
}

func newFakeServer(t *testing.T) *fakeServer {
	t.Helper()
	return &fakeServer{done: make(chan struct{})}
}

func (s *fakeServer) dial() baselsp.DialFunc {
	return func(ctx context.Context, spec baselsp.ServerSpec, root string, logger baselsp.Logger) (*baselsp.DialResult, error) {
		clientConn, serverConn := net.Pipe()
		s.mu.Lock()
		s.conn = serverConn
		s.mu.Unlock()
		go func() {
			s.serve(serverConn)
			close(s.done)
		}()
		return &baselsp.DialResult{
			Conn: clientConn,
			PID:  os.Getpid(),
			Kill: func() error { return clientConn.Close() },
			Wait: func() error { <-s.done; return nil },
		}, nil
	}
}

func (s *fakeServer) serve(conn net.Conn) {
	reader := bufio.NewReader(conn)
	for {
		body, err := readFakeFrame(reader)
		if err != nil {
			return
		}
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.Unmarshal(body, &msg); err != nil {
			continue
		}
		if msg.ID == nil {
			if msg.Method == "exit" {
				return
			}
			continue // 通知：initialized / didOpen / didChange
		}
		switch msg.Method {
		case "initialize":
			s.respond(conn, msg.ID, map[string]any{
				"capabilities": map[string]any{"positionEncoding": "utf-16", "textDocumentSync": 1},
				"serverInfo":   map[string]any{"name": "fake", "version": "0"},
			})
		case "shutdown":
			s.respond(conn, msg.ID, nil)
		default:
			s.respond(conn, msg.ID, nil)
		}
	}
}

func (s *fakeServer) respond(conn net.Conn, id json.RawMessage, result any) {
	payload, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id), "result": result})
	if err != nil {
		return
	}
	_, _ = conn.Write([]byte("Content-Length: " + strconv.Itoa(len(payload)) + "\r\n\r\n"))
	_, _ = conn.Write(payload)
}

func readFakeFrame(reader *bufio.Reader) ([]byte, error) {
	length := 0
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			break
		}
		if rest, ok := strings.CutPrefix(trimmed, "Content-Length:"); ok {
			n, err := strconv.Atoi(strings.TrimSpace(rest))
			if err == nil {
				length = n
			}
		}
	}
	if length <= 0 {
		return nil, io.EOF
	}
	buf := make([]byte, length)
	if _, err := io.ReadFull(reader, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

