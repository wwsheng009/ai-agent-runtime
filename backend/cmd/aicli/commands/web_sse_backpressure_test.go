package commands

// 背压安全 SSE 发射器（web_handlers.go chatWebSSEStream）与锁外动态状态
// 发布通道（chat_interaction.go webStatusLane）的回归测试。
//
// 历史事故：writeEvent 曾在持锁状态下无界 Flush()，停滞的 web 客户端经
// 同步 Bus.Publish 把 agent 批次、UI actor、停滞看门狗与主循环同时钉死，
// 会话状态行 "Analyzing (5m 15s)" 永不刷新。以下测试保证：发布者永不因
// 慢客户端阻塞（丢帧计数）、停滞客户端被写截止时间判死并触达退订回调、
// 帧序列单调有序，且动态状态发布在持锁路径外完成。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// ---------------------------------------------------------------------------
// 测试辅助：token 门控 writer
// ---------------------------------------------------------------------------

// tokenSSEWriter 每次 Write 都需要一个令牌（测试侧发放），用于制造
// “卡住的 socket”或逐步放行帧；closed 关闭后 Write 立即失败。
type tokenSSEWriter struct {
	tokens chan struct{}
	closed chan struct{}
	mu     sync.Mutex
	buf    bytes.Buffer
}

func newTokenSSEWriter() *tokenSSEWriter {
	return &tokenSSEWriter{tokens: make(chan struct{}), closed: make(chan struct{})}
}

func (t *tokenSSEWriter) Header() http.Header { return http.Header{} }
func (t *tokenSSEWriter) WriteHeader(int)     {}
func (t *tokenSSEWriter) Flush()              {}

func (t *tokenSSEWriter) Write(p []byte) (int, error) {
	select {
	case <-t.tokens:
	case <-t.closed:
		return 0, io.ErrClosedPipe
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf.Write(p)
	return len(p), nil
}

func (t *tokenSSEWriter) body() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.buf.String()
}

func (t *tokenSSEWriter) release() { t.tokens <- struct{}{} }
func (t *tokenSSEWriter) stop()    { close(t.closed) }

// errorSSEWriter 前 failAfter 次写成功，之后每次写返回模拟超时错误
// （模拟 TCP 写截止时间到期），用于确定性验证 writeFrame 错误判死路径。
type errorSSEWriter struct {
	failAfter int64
	writes    atomic.Int64
}

func (e *errorSSEWriter) Header() http.Header { return http.Header{} }
func (e *errorSSEWriter) WriteHeader(int)     {}
func (e *errorSSEWriter) Flush()              {}
func (e *errorSSEWriter) Write(p []byte) (int, error) {
	if e.writes.Add(1) > e.failAfter {
		return 0, fmt.Errorf("simulated write timeout")
	}
	return len(p), nil
}

// sseFrameBody 解析 "event: X\ndata: {...}\n\n" 块中的 data JSON。
func sseFrameData(body string, idx int) map[string]interface{} {
	blocks := strings.Split(body, "\n\n")
	if idx >= len(blocks) {
		return nil
	}
	for _, line := range strings.Split(blocks[idx], "\n") {
		if strings.HasPrefix(line, "data: ") {
			var m map[string]interface{}
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &m); err == nil {
				return m
			}
		}
	}
	return nil
}

func pollBool(t *testing.T, timeout time.Duration, what string, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

// ---------------------------------------------------------------------------
// 发布者永不阻塞（丢帧计数）
// ---------------------------------------------------------------------------

func TestChatWebSSEStream_PublisherNeverBlocks(t *testing.T) {
	w := newTokenSSEWriter()
	s := newChatWebSSEStream(w, w)
	s.writeTimeout = time.Hour // 测试不依赖写截止时间
	s.start(nil)
	defer s.Close()

	// 首个帧会卡在 token 上：writer goroutine 停住，队列开始溢出。
	start := time.Now()
	const n = 100_000
	for i := 0; i < n; i++ {
		s.writeEvent("status", map[string]interface{}{"i": i}, "probe")
	}
	elapsed := time.Since(start)
	if elapsed > 2*time.Second {
		t.Fatalf("enqueue %d frames took %v; publisher must never block on a stalled writer", n, elapsed)
	}
	if s.Dropped() == 0 {
		t.Fatalf("Dropped() = 0, want > 0 (queue must overflow while writer is stalled)")
	}
	// 停滞期间 keepalive 与 lastEventAge 也必须是安全无锁读。
	if age := s.lastEventAge(); age < 0 {
		t.Fatalf("lastEventAge() = %v, want >= 0", age)
	}

	// 放行一帧：确认写路径仍能继续（writer 未死）。
	w.release()
	if !pollBool(t, 2*time.Second, "frame written after release", func() bool {
		return strings.Contains(w.body(), "event: status")
	}) {
		t.Fatal("writer did not progress after token release")
	}
	w.stop() // 让可能仍阻塞的 Write 失败退出，writer goroutine 收尾
}

// ---------------------------------------------------------------------------
// 写错误确定性判死：onDead 恰好一次、Done 关闭、判死后入队不阻塞
// ---------------------------------------------------------------------------

func TestChatWebSSEStream_WriteErrorMarksDeadAndCallsOnDead(t *testing.T) {
	w := &errorSSEWriter{failAfter: 2} // 第 3 帧起写失败
	s := newChatWebSSEStream(w, w)
	s.writeTimeout = time.Hour // 不依赖真实 deadline
	var onDead atomic.Int64
	s.start(func() { onDead.Add(1) })
	defer s.Close()

	for i := 0; i < 100; i++ {
		s.writeEvent("status", map[string]interface{}{"i": i}, "probe")
	}

	if !pollBool(t, 2*time.Second, "onDead called exactly once", func() bool {
		return onDead.Load() == 1
	}) {
		t.Fatalf("onDead count = %d, want exactly 1", onDead.Load())
	}
	if !s.Closed() {
		t.Fatal("stream not marked dead after write error")
	}
	if !pollBool(t, 2*time.Second, "Done closed", func() bool {
		select {
		case <-s.Done():
			return true
		default:
			return false
		}
	}) {
		t.Fatal("Done never closed after write error")
	}

	// 判死后 enqueue 依旧非阻塞（再次写失败不会二次触发 onDead）。
	start := time.Now()
	for i := 0; i < 100_000; i++ {
		s.writeEvent("status", map[string]interface{}{"i": i}, "probe")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("enqueue after dead took %v; must be non-blocking", elapsed)
	}
	if got := onDead.Load(); got != 1 {
		t.Fatalf("onDead count = %d after post-dead writes, want still 1", got)
	}
}

// ---------------------------------------------------------------------------
// 停滞客户端集成路径：流必须被判死（写截止时间或连接收尾），发布者不阻塞
// ---------------------------------------------------------------------------

func TestChatWebSSEStream_StalledClientMarksDeadAndUnsubscribes(t *testing.T) {
	streamCh := make(chan *chatWebSSEStream, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, _ := w.(http.Flusher)
		s := newChatWebSSEStream(w, flusher)
		s.writeTimeout = 200 * time.Millisecond
		s.start(nil)
		// 与生产 handler 相同：返回前必须先停掉 writer goroutine，
		// 否则与 net/http 的 finishRequest/putBufioWriter 数据竞争。
		defer func() {
			s.Close()
			<-s.writerDone
		}()
		select {
		case streamCh <- s:
		default:
		}
		s.writeEvent("connected", map[string]interface{}{"ok": true}, "connected")
		t0 := time.Now()
		go func() {
			<-r.Context().Done()
			t.Logf("handler ctx done at +%v err=%v", time.Since(t0), r.Context().Err())
		}()
		<-r.Context().Done() // 挂住 handler 直到客户端断开
		t.Logf("handler returning at +%v", time.Since(t0))
	}))
	defer srv.Close()

	addr := strings.TrimPrefix(srv.URL, "http://")
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	// 必须发送请求行，net/http 才会调用 handler。
	if _, err := fmt.Fprintf(conn, "GET /events HTTP/1.1\r\nHost: %s\r\nAccept: text/event-stream\r\nConnection: close\r\n\r\n", addr); err != nil {
		t.Fatalf("write request: %v", err)
	}

	var s *chatWebSSEStream
	select {
	case s = <-streamCh:
	case <-time.After(3 * time.Second):
		t.Fatal("handler did not expose stream")
	}

	// 客户端从不读取：持续入队直到流判死。判死由两条路径达成：
	// (a) 写阻塞超过 200ms 截止时间 → 超时错误 → fail；
	// (b) Windows CancelIoEx 把服务器的挂起读一起取消 → 连接收尾 →
	//     handler 返回 → Close。两条路径都会让 Closed() 为真（onDead
	//     只在路径 a 触发，路径 b 由 handler 自行退订，等价安全）。
	deadline := time.Now().Add(30 * time.Second)
	enqueued := 0
	// 停滞探测器：writer 若连续 2s 无进展，抓全量 goroutine 栈定位卡点。
	go func() {
		for {
			time.Sleep(2 * time.Second)
			if s.Closed() || s.lastEventAge() > 2*time.Second {
				buf := make([]byte, 8<<20)
				n := runtime.Stack(buf, true)
				t.Logf("WRITER STALL detect: closed=%v lastEventAge=%v\n%s", s.Closed(), s.lastEventAge(), buf[:n])
				return
			}
		}
	}()
	for !s.Closed() && time.Now().Before(deadline) {
		for i := 0; i < 4096 && !s.Closed(); i++ {
			s.writeEvent("status", map[string]interface{}{"i": i}, "probe")
			enqueued++
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !s.Closed() {
		t.Fatalf("stream not dead after %d enqueues (dropped=%d): stalled client must be marked dead", enqueued, s.Dropped())
	}
	if !pollBool(t, 2*time.Second, "stream Done closed", func() bool {
		select {
		case <-s.Done():
			return true
		default:
			return false
		}
	}) {
		t.Fatal("stream Done never closed after dead")
	}
	if s.Dropped() == 0 {
		t.Fatalf("Dropped() = 0, want > 0 (frames dropped while client stalled)")
	}
	// 判死后 enqueue 依旧非阻塞（发布者永不阻塞的最终保证）。
	start := time.Now()
	for i := 0; i < 100_000; i++ {
		s.writeEvent("status", map[string]interface{}{"i": i}, "probe")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("enqueue after dead took %v; must be non-blocking", elapsed)
	}
}

// ---------------------------------------------------------------------------
// 帧按 FIFO 单调序列 + _event 信封
// ---------------------------------------------------------------------------

func TestChatWebSSEStream_SequenceInOrderAndEnvelope(t *testing.T) {
	w := newTokenSSEWriter()
	s := newChatWebSSEStream(w, w)
	s.writeTimeout = time.Hour
	s.start(nil)
	defer s.Close()

	s.writeEvent("status", map[string]interface{}{"i": 1}, "src-a")
	s.writeEvent("status", map[string]interface{}{"i": 2}, "src-b")
	s.writeEvent("status", map[string]interface{}{"i": 3}, "src-c")

	for step := 1; step <= 3; step++ {
		w.release()
		if !pollBool(t, 2*time.Second, "frame released", func() bool {
			return strings.Count(w.body(), "event: status") >= step
		}) {
			t.Fatalf("frame %d not written", step)
		}
		body := w.body()
		blocks := strings.Split(body, "\n\n")
		for i := 0; i < step; i++ {
			d := sseFrameData(body, i)
			if d == nil {
				t.Fatalf("frame %d missing data JSON", i)
			}
			env, ok := d["_event"].(map[string]interface{})
			if !ok {
				t.Fatalf("frame %d missing _event envelope: %v", i, d)
			}
			seq := int(env["sequence"].(float64))
			if seq != i+1 {
				t.Fatalf("frame %d sequence = %d, want %d (FIFO monotonic)", i, seq, i+1)
			}
			if env["schema_version"] != chatWebSchemaVersion {
				t.Fatalf("frame %d schema_version = %v", i, env["schema_version"])
			}
			if got := int(d["i"].(float64)); got != i+1 {
				t.Fatalf("frame %d payload i = %d, want %d", i, got, i+1)
			}
		}
		// Split 对尾部定界符会产生一个空块，只统计非空块。
		nonEmpty := 0
		for _, b := range blocks {
			if strings.TrimSpace(b) != "" {
				nonEmpty++
			}
		}
		if nonEmpty > step {
			t.Fatalf("more frames than released: %d frames after %d releases", nonEmpty, step)
		}
	}
}

// ---------------------------------------------------------------------------
// 锁外动态状态发布通道：保序、发布不阻塞持锁调用方
// ---------------------------------------------------------------------------

func TestChatInteractionCoordinator_DynamicStatusLaneOrdered(t *testing.T) {
	bus := runtimeevents.NewBus()
	session := newWebTestSession()
	session.LocalRuntimeHost = &localChatRuntimeHost{EventBus: bus}
	coord := newChatInteractionCoordinator(session)
	defer coord.Shutdown()

	ch := make(chan runtimeevents.Event, 8)
	unsub := bus.SubscribeCancelable("", func(ev runtimeevents.Event) {
		if ev.Type == chatWebDynamicStatusBusEvent {
			select {
			case ch <- ev:
			default:
			}
		}
	})
	defer unsub()

	// 两个不同状态依次发布；若发布回压到持锁路径，这里会直接超时挂死。
	start := time.Now()
	coord.SetRetrying("step=1 attempt=1")
	coord.SetRetrying("step=1 attempt=2")
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("SetRetrying x2 took %v; publish must not block the locked caller", elapsed)
	}

	want := []string{
		"◦ Retrying step=1 attempt=1",
		"◦ Retrying step=1 attempt=2",
	}
	for i, wantText := range want {
		var ev runtimeevents.Event
		select {
		case ev = <-ch:
		case <-time.After(3 * time.Second):
			t.Fatalf("dynamic status event %d never arrived (lane publish stalled)", i)
		}
		payload := ev.Payload
		if payload["active"] != true {
			t.Fatalf("event %d active = %v, want true", i, payload["active"])
		}
		if payload["text"] != wantText {
			t.Fatalf("event %d text = %q, want %q (order must be preserved)", i, payload["text"], wantText)
		}
		if payload["interruptible"] != true {
			t.Fatalf("event %d interruptible = %v, want true", i, payload["interruptible"])
		}
		if _, ok := payload["started_at"]; !ok {
			t.Fatalf("event %d missing started_at", i)
		}
	}
}
