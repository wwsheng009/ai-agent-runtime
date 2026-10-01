package knowledge

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	knowledgelsp "github.com/wwsheng009/ai-agent-runtime/internal/knowledge/lsp"
	baselsp "github.com/wwsheng009/ai-agent-runtime/internal/lsp"
)

// ---- 脚本化 LSP 服务器（进程内，走真实 Content-Length 帧） ----

type scriptedServer struct {
	t        *testing.T
	encoding string

	mu       sync.Mutex
	handlers map[string]func(params json.RawMessage) (any, bool) // 返回 (result, handled)
	conn     net.Conn
	connOnce sync.Once
	doneOnce sync.Once
	done     chan struct{}
}

func newScriptedServer(t *testing.T, encoding string) *scriptedServer {
	t.Helper()
	return &scriptedServer{
		t:        t,
		encoding: encoding,
		handlers: map[string]func(json.RawMessage) (any, bool){},
		done:     make(chan struct{}),
	}
}

func (s *scriptedServer) on(method string, handler func(params json.RawMessage) (any, bool)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers[method] = handler
}

// dial 返回注入给 Manager 的 DialFunc，并给出 server 侧连接。
func (s *scriptedServer) dial() baselsp.DialFunc {
	return func(ctx context.Context, spec baselsp.ServerSpec, root string, logger baselsp.Logger) (*baselsp.DialResult, error) {
		clientConn, serverConn := net.Pipe()
		s.mu.Lock()
		s.conn = serverConn
		s.mu.Unlock()
		go func() {
			s.serve(serverConn)
			s.doneOnce.Do(func() { close(s.done) })
		}()
		return &baselsp.DialResult{
			Conn: clientConn,
			// 非零 PID：让内存上限检查在测试中可被注入的 MemoryProbe 触发。
			PID:  os.Getpid(),
			Kill: func() error { clientConn.Close(); return nil },
			Wait: func() error { <-s.done; return nil },
		}, nil
	}
}

// kill 模拟服务器崩溃（客户端侧观察到进程退出）。
func (s *scriptedServer) kill() {
	s.connOnce.Do(func() {
		s.mu.Lock()
		conn := s.conn
		s.mu.Unlock()
		if conn != nil {
			_ = conn.Close()
		}
	})
}

func (s *scriptedServer) serve(conn net.Conn) {
	reader := bufio.NewReader(conn)
	for {
		body, err := readFrame(reader)
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
				"capabilities": map[string]any{"positionEncoding": s.encoding, "textDocumentSync": 1},
				"serverInfo":   map[string]any{"name": "scripted", "version": "0.0.1"},
			})
		case "shutdown":
			s.respond(conn, msg.ID, nil)
		default:
			s.mu.Lock()
			handler := s.handlers[msg.Method]
			s.mu.Unlock()
			if handler == nil {
				s.respond(conn, msg.ID, nil)
				continue
			}
			var params json.RawMessage
			var envelope struct {
				Params json.RawMessage `json:"params"`
			}
			_ = json.Unmarshal(body, &envelope)
			params = envelope.Params
			result, handled := handler(params)
			if !handled {
				continue // 不响应：用于超时测试
			}
			s.respond(conn, msg.ID, result)
		}
	}
}

func (s *scriptedServer) respond(conn net.Conn, id json.RawMessage, result any) {
	payload, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	if err != nil {
		s.t.Errorf("marshal response: %v", err)
		return
	}
	_, _ = fmt.Fprintf(conn, "Content-Length: %d\r\n\r\n", len(payload))
	_, _ = conn.Write(payload)
}

func readFrame(reader *bufio.Reader) ([]byte, error) {
	length := -1
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if strings.HasPrefix(strings.ToLower(line), "content-length:") {
			_, _ = fmt.Sscanf(strings.TrimSpace(line[len("content-length:"):]), "%d", &length)
		}
	}
	if length < 0 {
		return nil, errors.New("missing content-length")
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(reader, body); err != nil {
		return nil, err
	}
	return body, nil
}

// ---- 测试辅助 ----

func newTestManager(t *testing.T, root string, server *scriptedServer, mutate func(*knowledgelsp.Options)) *knowledgelsp.Manager {
	t.Helper()
	opts := knowledgelsp.Options{
		Root:           root,
		Spec:           baselsp.ServerSpec{Name: "gopls", Command: "gopls"},
		MaxProcesses:   1,
		MemoryLimitMB:  512,
		StartupTimeout: 5 * time.Second,
		RequestTimeout: 2 * time.Second,
		Dial:           server.dial(),
		MemoryProbe:    func(int) (int64, error) { return 0, nil },
	}
	if mutate != nil {
		mutate(&opts)
	}
	return knowledgelsp.NewManager(opts)
}

// Phase 4 交付 2/3：语义通道端到端（位置编码边界 + 崩溃/超时降级）。
func TestLSPSemanticAdapterDefinitionAndReferences(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeTree(t, root, "demo/target.go", "// 😀 note\nfunc Target() {}\n")

	server := newScriptedServer(t, "utf-16")
	server.on("textDocument/definition", func(json.RawMessage) (any, bool) {
		return []map[string]any{{
			"uri":   baselsp.PathToURI(root + "/demo/target.go"),
			"range": map[string]any{"start": map[string]int{"line": 0, "character": 5}},
		}}, true
	})
	server.on("textDocument/references", func(json.RawMessage) (any, bool) {
		return []map[string]any{{
			"targetUri":            baselsp.PathToURI(root + "/demo/target.go"),
			"targetSelectionRange": map[string]any{"start": map[string]int{"line": 1, "character": 5}},
		}}, true
	})

	manager := newTestManager(t, root, server, nil)
	t.Cleanup(func() { _ = manager.Close(ctx) })
	if err := manager.Ensure(ctx); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	adapter := NewLSPSemanticAdapter(manager, root)
	if !adapter.Available() {
		t.Fatal("语义通道应可用")
	}

	// utf-16 character=5 落在 "// 😀" 之后：canonical 字节列应为 7
	// （1+1+1+4），若直接透传协议列会得到 5 —— 这条断言钉住 ADR-0006 §4.4。
	defs, err := adapter.Definition(ctx, "demo/target.go", 0, 0)
	if err != nil {
		t.Fatalf("Definition: %v", err)
	}
	if len(defs) != 1 {
		t.Fatalf("defs = %+v, want 1", defs)
	}
	if defs[0].Line != 0 || defs[0].Col != 7 {
		t.Fatalf("definition canonical = (%d,%d), want (0,7)", defs[0].Line, defs[0].Col)
	}
	if defs[0].Source != SourceLSP || defs[0].Confidence != ConfidenceSemantic {
		t.Fatalf("source/confidence = %q/%v", defs[0].Source, defs[0].Confidence)
	}
	if !strings.Contains(defs[0].Snippet, "😀") {
		t.Fatalf("snippet 应来自目标行: %q", defs[0].Snippet)
	}

	refs, err := adapter.References(ctx, "demo/target.go", 1, 0)
	if err != nil {
		t.Fatalf("References: %v", err)
	}
	if len(refs) != 1 || refs[0].Path != "demo/target.go" || refs[0].Line != 1 || refs[0].Col != 5 {
		t.Fatalf("references = %+v", refs)
	}
}

func TestLSPSemanticAdapterDegradesWhenUnavailable(t *testing.T) {
	ctx := context.Background()
	adapter := NewLSPSemanticAdapter(nil, t.TempDir())
	if adapter.Available() {
		t.Fatal("nil manager 必须不可用")
	}
	if _, err := adapter.Definition(ctx, "a.go", 0, 0); !errors.Is(err, ErrSemanticUnavailable) {
		t.Fatalf("err = %v, want ErrSemanticUnavailable", err)
	}
}

// LSP 崩溃 100% 降级不阻断（验收门槛）：崩溃后 Available=false，
// 后续查询立即返回 ErrSemanticUnavailable，而不是阻塞或 panic。
func TestLSPSemanticAdapterDegradesAfterCrash(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeTree(t, root, "demo/a.go", "package demo\n")

	server := newScriptedServer(t, "utf-8")
	manager := newTestManager(t, root, server, nil)
	t.Cleanup(func() { _ = manager.Close(ctx) })
	if err := manager.Ensure(ctx); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	adapter := NewLSPSemanticAdapter(manager, root)
	if !adapter.Available() {
		t.Fatal("启动后应可用")
	}

	server.kill()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && adapter.Available() {
		time.Sleep(10 * time.Millisecond)
	}
	if adapter.Available() {
		t.Fatal("服务器退出后 Available 必须为 false")
	}
	started := time.Now()
	if _, err := adapter.References(ctx, "demo/a.go", 0, 0); !errors.Is(err, ErrSemanticUnavailable) {
		t.Fatalf("崩溃后 err = %v, want ErrSemanticUnavailable", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("崩溃后查询必须立即返回，实际 %v", elapsed)
	}
	if manager.DegradeReason() == "" {
		t.Fatal("降级必须可观测（DegradeReason 非空）")
	}
}

// 请求超时：服务器不响应时必须按 RequestTimeout 返回错误，而不是永久挂起。
func TestLSPSemanticAdapterRequestTimeout(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeTree(t, root, "demo/a.go", "package demo\n")

	server := newScriptedServer(t, "utf-8")
	server.on("textDocument/definition", func(json.RawMessage) (any, bool) { return nil, false })
	manager := newTestManager(t, root, server, func(o *knowledgelsp.Options) {
		o.RequestTimeout = 300 * time.Millisecond
	})
	t.Cleanup(func() { _ = manager.Close(ctx) })
	if err := manager.Ensure(ctx); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	adapter := NewLSPSemanticAdapter(manager, root)
	started := time.Now()
	if _, err := adapter.Definition(ctx, "demo/a.go", 0, 0); err == nil {
		t.Fatal("超时必须返回错误")
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("超时未生效：%v", elapsed)
	}
}

// ---- 进程管理（交付 3）：锁 / 复用 / 上限 / 回收 ----

func TestManagerLockHeldByLiveProcessDegrades(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	server := newScriptedServer(t, "utf-8")
	manager := newTestManager(t, root, server, nil)

	// 预置一个"活进程"持有的锁（用父进程 pid 模拟另一个 aicli 会话）。
	lockPath := manager.LockPath()
	if err := writeLockFile(lockPath, knowledgelsp.LockRecord{
		PID:       otherLivePID(),
		RootURI:   strings.ReplaceAll(root, `\`, "/"),
		Command:   "gopls",
		StartedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("write lock: %v", err)
	}

	err := manager.Ensure(ctx)
	if !errors.Is(err, knowledgelsp.ErrLockHeld) {
		t.Fatalf("Ensure err = %v, want ErrLockHeld", err)
	}
	if manager.Client() != nil {
		t.Fatal("锁被活进程持有时不得启动新进程（ADR-0002 D1/D7）")
	}
	if manager.DegradeReason() == "" {
		t.Fatal("降级原因必须可观测")
	}
}

func TestManagerStaleLockTakenOverAndReleased(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	server := newScriptedServer(t, "utf-8")
	manager := newTestManager(t, root, server, nil)

	if err := writeLockFile(manager.LockPath(), knowledgelsp.LockRecord{
		PID:     999999, // 不存在的进程 → 锁过期可接管
		RootURI: root,
		Command: "gopls",
	}); err != nil {
		t.Fatalf("write lock: %v", err)
	}

	if err := manager.Ensure(ctx); err != nil {
		t.Fatalf("Ensure(stale lock): %v", err)
	}
	raw, err := readLockFile(manager.LockPath())
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	if raw.PID != livePID() {
		t.Fatalf("接管后锁 pid = %d, want %d", raw.PID, livePID())
	}

	if err := manager.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := readLockFile(manager.LockPath()); err == nil {
		t.Fatal("Close 后必须释放自己持有的锁")
	}
}

func TestManagerReusesLiveClientAndReapsOnMemoryLimit(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	server := newScriptedServer(t, "utf-8")

	manager := newTestManager(t, root, server, func(o *knowledgelsp.Options) { o.SkipLock = true })
	if err := manager.Ensure(ctx); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	first := manager.Client()
	if err := manager.Ensure(ctx); err != nil {
		t.Fatalf("Ensure(second): %v", err)
	}
	if manager.Client() != first {
		t.Fatal("已有活客户端时必须复用，不得重复启动")
	}

	// 内存超限：启动即回收并降级（≤ memory_limit_mb 的验收路径）。
	over := newTestManager(t, root, server, func(o *knowledgelsp.Options) {
		o.SkipLock = true
		o.MemoryLimitMB = 1
		o.MemoryProbe = func(int) (int64, error) { return 64 * 1024 * 1024, nil }
	})
	err := over.Ensure(ctx)
	if !errors.Is(err, knowledgelsp.ErrMemoryLimit) {
		t.Fatalf("Ensure(over memory) err = %v, want ErrMemoryLimit", err)
	}
	if over.Client() != nil {
		t.Fatal("超限进程必须被回收，不得保留")
	}
	if over.DegradeReason() == "" {
		t.Fatal("内存降级必须可观测")
	}
}

func TestManagerRejectsInvalidProcessLimit(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	server := newScriptedServer(t, "utf-8")
	manager := newTestManager(t, root, server, func(o *knowledgelsp.Options) {
		o.SkipLock = true
		o.MaxProcesses = -1
	})
	if err := manager.Ensure(ctx); !errors.Is(err, knowledgelsp.ErrProcessLimit) {
		t.Fatalf("err = %v, want ErrProcessLimit", err)
	}
}

// ---- 小工具 ----

func livePID() int { return os.Getpid() }

// otherLivePID 返回一个"别人"的活进程 pid（父进程：go test 运行器）。
func otherLivePID() int { return os.Getppid() }

func writeLockFile(path string, record knowledgelsp.LockRecord) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	payload, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return os.WriteFile(path, payload, 0o644)
}

func readLockFile(path string) (knowledgelsp.LockRecord, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return knowledgelsp.LockRecord{}, err
	}
	var record knowledgelsp.LockRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return knowledgelsp.LockRecord{}, err
	}
	return record, nil
}
