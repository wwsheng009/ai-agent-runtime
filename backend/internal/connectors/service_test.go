package connectors

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors/api"
)

// ── 测试替身 ────────────────────────────────────────────────────────────────

type fakeProvider struct {
	mu sync.Mutex

	sent        []OutboundMessage
	sentChat    string
	streamFinal []OutboundMessage
	updates     []string
	typingStart int
	typingStop  int
	sendErr     error
	// streamFailErr 让 streamer.Fail 失败（失败回执发送失败的可观测用例）。
	streamFailErr error

	polling bool
	// pollExitDelay 让 RunPolling 在 ctx 取消后延迟返回，放大
	// "旧实例清理 vs 新实例登记" 的竞态窗口（Fix-4 用例）。
	pollExitDelay time.Duration
	// pollStarts/pollExits 统计 RunPolling 进入/退出次数（原子访问）。
	pollStarts int32
	pollExits  int32
}

func (p *fakeProvider) Name() string { return "fake" }

func (p *fakeProvider) Schema() api.ProviderSchema {
	return api.ProviderSchema{Provider: "fake", Label: "Fake"}
}

func (p *fakeProvider) Activate(context.Context, Config) (Activation, error) {
	return Activation{}, nil
}

func (p *fakeProvider) Deactivate(context.Context, Config) error { return nil }

func (p *fakeProvider) Send(_ context.Context, _ Config, chat string, msgs []OutboundMessage) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.sendErr != nil {
		return p.sendErr
	}
	p.sent = append(p.sent, msgs...)
	p.sentChat = chat
	return nil
}

func (p *fakeProvider) StartTyping(context.Context, Config, string) (TypingSession, error) {
	p.mu.Lock()
	p.typingStart++
	p.mu.Unlock()
	return &fakeTyping{provider: p}, nil
}

func (p *fakeProvider) StartStreaming(context.Context, Config, string) (StreamingSession, error) {
	return &fakeStreaming{provider: p}, nil
}

func (p *fakeProvider) SupportsPolling(Config) bool { return p.polling }

func (p *fakeProvider) RunPolling(ctx context.Context, _ Config, _ InboundHandler, _ PollingState) error {
	atomic.AddInt32(&p.pollStarts, 1)
	<-ctx.Done()
	if p.pollExitDelay > 0 {
		time.Sleep(p.pollExitDelay)
	}
	atomic.AddInt32(&p.pollExits, 1)
	return nil
}

func (p *fakeProvider) pollCounts() (int32, int32) {
	return atomic.LoadInt32(&p.pollStarts), atomic.LoadInt32(&p.pollExits)
}

func (p *fakeProvider) sentTexts() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, 0, len(p.sent))
	for _, msg := range p.sent {
		out = append(out, msg.Text)
	}
	return out
}

func (p *fakeProvider) streamUpdates() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.updates...)
}

func (p *fakeProvider) typingCounts() (int, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.typingStart, p.typingStop
}

func (p *fakeProvider) finalMessages() []OutboundMessage {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]OutboundMessage(nil), p.streamFinal...)
}

type fakeTyping struct {
	provider *fakeProvider
	once     sync.Once
}

func (t *fakeTyping) Stop(context.Context) error {
	t.once.Do(func() {
		t.provider.mu.Lock()
		t.provider.typingStop++
		t.provider.mu.Unlock()
	})
	return nil
}

type fakeStreaming struct {
	provider *fakeProvider
}

func (s *fakeStreaming) Update(_ context.Context, text string) error {
	s.provider.mu.Lock()
	defer s.provider.mu.Unlock()
	s.provider.updates = append(s.provider.updates, text)
	return nil
}

func (s *fakeStreaming) Final(_ context.Context, msgs []OutboundMessage) error {
	s.provider.mu.Lock()
	defer s.provider.mu.Unlock()
	s.provider.streamFinal = append(s.provider.streamFinal, msgs...)
	return nil
}

func (s *fakeStreaming) Fail(_ context.Context, text string) error {
	if s.provider.streamFailErr != nil {
		return s.provider.streamFailErr
	}
	s.provider.mu.Lock()
	defer s.provider.mu.Unlock()
	s.provider.updates = append(s.provider.updates, "FAIL:"+text)
	return nil
}

// basicProvider 只实现最基础的 Provider（不支持 typing/streaming/polling）。
type basicProvider struct {
	mu   sync.Mutex
	sent []OutboundMessage
}

func (p *basicProvider) Name() string { return "basic" }

func (p *basicProvider) Schema() api.ProviderSchema {
	return api.ProviderSchema{Provider: "basic", Label: "Basic"}
}

func (p *basicProvider) Activate(context.Context, Config) (Activation, error) {
	return Activation{}, nil
}

func (p *basicProvider) Deactivate(context.Context, Config) error { return nil }

func (p *basicProvider) Send(_ context.Context, _ Config, _ string, msgs []OutboundMessage) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sent = append(p.sent, msgs...)
	return nil
}

func (p *basicProvider) sentTexts() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, 0, len(p.sent))
	for _, msg := range p.sent {
		out = append(out, msg.Text)
	}
	return out
}

type fakeBackend struct {
	mu sync.Mutex

	handled   []BackendRequest
	session   SessionRef
	reply     []OutboundMessage
	err       error
	onHandle  func(req BackendRequest, sink ReplySink)
	block     chan struct{} // 非空时阻塞，模拟慢 agent
	inflight  int32
	maxInflyt int32
}

func (b *fakeBackend) Name() string { return "fake-backend" }

func (b *fakeBackend) Handle(ctx context.Context, req BackendRequest, sink ReplySink) (BackendResult, error) {
	current := atomic.AddInt32(&b.inflight, 1)
	for {
		previous := atomic.LoadInt32(&b.maxInflyt)
		if current <= previous || atomic.CompareAndSwapInt32(&b.maxInflyt, previous, current) {
			break
		}
	}
	defer atomic.AddInt32(&b.inflight, -1)

	b.mu.Lock()
	b.handled = append(b.handled, req)
	block := b.block
	handler := b.onHandle
	b.mu.Unlock()

	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return BackendResult{}, ctx.Err()
		}
	}
	if handler != nil {
		handler(req, sink)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.err != nil {
		return BackendResult{}, b.err
	}
	return BackendResult{Session: b.session, Reply: b.reply}, nil
}

func (b *fakeBackend) requests() []BackendRequest {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]BackendRequest(nil), b.handled...)
}

func testConfig(id, provider string, mutate ...func(*Config)) Config {
	cfg := Config{
		ID:       id,
		Provider: provider,
		Name:     id,
		Enabled:  true,
		Mode:     "polling",
		Secrets:  map[string]string{"bot_token": "token"},
	}
	for _, fn := range mutate {
		fn(&cfg)
	}
	return cfg
}

func waitFor(t *testing.T, what string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func newTestService(t *testing.T, store Store, backend Backend, provider Provider, deps ...func(*Deps)) *Service {
	t.Helper()
	options := Deps{Store: store, Backend: backend, Logf: func(string, ...any) {}}
	for _, fn := range deps {
		fn(&options)
	}
	service := New(options)
	if provider != nil {
		if err := service.Register(provider); err != nil {
			t.Fatal(err)
		}
	}
	return service
}

func inbound(connectorID, chat, messageID, text string) InboundMessage {
	return InboundMessage{
		ConnectorID: connectorID,
		Provider:    "fake",
		Chat:        chat,
		MessageID:   messageID,
		UserID:      "u1",
		Text:        text,
		ReceivedAt:  time.Now(),
	}
}

// ── 用例 ───────────────────────────────────────────────────────────────────

func TestServiceReplyBindsSessionAndTyping(t *testing.T) {
	provider := &fakeProvider{}
	backend := &fakeBackend{session: SessionRef{SessionID: "sess-1"}, reply: []OutboundMessage{{Text: "你好"}}}
	store := NewMemoryStore(testConfig("c1", "fake"))
	service := newTestService(t, store, backend, provider)

	if err := service.HandleInbound(context.Background(), inbound("c1", "chat-1", "m1", "hi")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "reply", func() bool { return len(provider.finalMessages()) > 0 })
	waitFor(t, "typing stopped", func() bool {
		starts, stops := provider.typingCounts()
		return starts == 1 && stops == 1
	})

	binding, ok := store.GetBinding("c1", "chat-1")
	if !ok || binding.Session.SessionID != "sess-1" {
		t.Fatalf("binding = %+v, ok=%v", binding, ok)
	}
	if requests := backend.requests(); len(requests) != 1 || !requests[0].Session.IsZero() {
		t.Fatalf("requests = %+v", requests)
	}
	final := provider.finalMessages()
	if len(final) != 1 || final[0].Text != "你好" {
		t.Fatalf("stream final = %+v", final)
	}
	if texts := provider.sentTexts(); len(texts) != 0 {
		t.Fatalf("final-mode send should not be used when streaming is active: %v", texts)
	}
}

func TestServiceUsesExistingBinding(t *testing.T) {
	provider := &fakeProvider{}
	backend := &fakeBackend{session: SessionRef{SessionID: "sess-1"}, reply: []OutboundMessage{{Text: "ok"}}}
	store := NewMemoryStore(testConfig("c1", "fake"))
	if err := store.PutBinding(Binding{ConnectorID: "c1", Chat: "chat-1", Session: SessionRef{SessionID: "sess-1"}}); err != nil {
		t.Fatal(err)
	}
	service := newTestService(t, store, backend, provider)
	if err := service.HandleInbound(context.Background(), inbound("c1", "chat-1", "m1", "again")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "reply", func() bool { return len(backend.requests()) == 1 })
	if got := backend.requests()[0].Session.SessionID; got != "sess-1" {
		t.Fatalf("backend session = %q, want sess-1", got)
	}
}

func TestServiceDedupesRepeatedMessages(t *testing.T) {
	provider := &fakeProvider{}
	backend := &fakeBackend{session: SessionRef{SessionID: "s1"}}
	store := NewMemoryStore(testConfig("c1", "fake"))
	service := newTestService(t, store, backend, provider)

	msg := inbound("c1", "chat-1", "same-id", "hi")
	if err := service.HandleInbound(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	if err := service.HandleInbound(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "first handle", func() bool { return len(backend.requests()) == 1 })
	time.Sleep(30 * time.Millisecond)
	if got := len(backend.requests()); got != 1 {
		t.Fatalf("backend handled %d times, want 1 (dedup)", got)
	}
}

func TestServiceRejectsUnauthorizedUser(t *testing.T) {
	provider := &fakeProvider{}
	backend := &fakeBackend{session: SessionRef{SessionID: "s1"}}
	store := NewMemoryStore(testConfig("c1", "fake", func(cfg *Config) {
		cfg.Policy.AllowedUsers = []string{"owner"}
	}))
	service := newTestService(t, store, backend, provider)

	msg := inbound("c1", "chat-1", "m1", "hi")
	msg.UserID = "intruder"
	if err := service.HandleInbound(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	if len(backend.requests()) != 0 {
		t.Fatalf("unauthorized user reached the backend: %+v", backend.requests())
	}
	if len(provider.sentTexts()) != 0 {
		t.Fatalf("unauthorized user got a reply: %v", provider.sentTexts())
	}
}

func TestServiceHelpCommandHandledLocally(t *testing.T) {
	provider := &fakeProvider{}
	backend := &fakeBackend{}
	store := NewMemoryStore(testConfig("c1", "fake"))
	service := newTestService(t, store, backend, provider)

	if err := service.HandleInbound(context.Background(), inbound("c1", "chat-1", "m1", "/help")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "help reply", func() bool { return len(provider.sentTexts()) == 1 })
	if len(backend.requests()) != 0 {
		t.Fatalf("help should not reach the backend")
	}
	if texts := provider.sentTexts(); texts[0] != helpText(testConfig("c1", "fake")) {
		t.Fatalf("help text = %q", texts[0])
	}
}

func TestServiceNewCommandRebindsNextMessage(t *testing.T) {
	provider := &fakeProvider{}
	backend := &fakeBackend{session: SessionRef{SessionID: "s2"}}
	store := NewMemoryStore(testConfig("c1", "fake"))
	if err := store.PutBinding(Binding{ConnectorID: "c1", Chat: "chat-1", Session: SessionRef{SessionID: "s1"}}); err != nil {
		t.Fatal(err)
	}
	service := newTestService(t, store, backend, provider)

	if err := service.HandleInbound(context.Background(), inbound("c1", "chat-1", "m1", "/new")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "new reply", func() bool { return len(provider.sentTexts()) == 1 })

	if err := service.HandleInbound(context.Background(), inbound("c1", "chat-1", "m2", "hi")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "backend call", func() bool { return len(backend.requests()) == 1 })
	if session := backend.requests()[0].Session; !session.IsZero() {
		t.Fatalf("/new 后应重新创建会话，但 backend 收到旧会话 %+v", session)
	}
}

func TestServiceThrottlesStreamingDeltas(t *testing.T) {
	provider := &fakeProvider{}
	backend := &fakeBackend{
		session: SessionRef{SessionID: "s1"},
		reply:   []OutboundMessage{{Text: "最终答复"}},
		onHandle: func(_ BackendRequest, sink ReplySink) {
			_ = sink.Delta(context.Background(), "片段1")
			_ = sink.Delta(context.Background(), "片段2")
		},
	}
	store := NewMemoryStore(testConfig("c1", "fake"))
	service := newTestService(t, store, backend, provider, func(deps *Deps) {
		deps.DeltaInterval = time.Hour // 只有首帧放行
	})

	if err := service.HandleInbound(context.Background(), inbound("c1", "chat-1", "m1", "hi")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "final", func() bool { return len(provider.finalMessages()) == 1 })
	updates := provider.streamUpdates()
	if len(updates) != 1 || updates[0] != "片段1" {
		t.Fatalf("updates = %v, want 仅首帧", updates)
	}
}

func TestServiceFinalModeUsesSend(t *testing.T) {
	provider := &basicProvider{}
	backend := &fakeBackend{session: SessionRef{SessionID: "s1"}, reply: []OutboundMessage{{Text: "一次性回复"}}}
	store := NewMemoryStore(testConfig("c1", "basic"))
	service := newTestService(t, store, backend, provider)

	if err := service.HandleInbound(context.Background(), inbound("c1", "chat-1", "m1", "hi")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "send", func() bool { return len(provider.sentTexts()) == 1 })
	if texts := provider.sentTexts(); texts[0] != "一次性回复" {
		t.Fatalf("sent = %v", texts)
	}
}

func TestServiceFailurePathReportsToUser(t *testing.T) {
	provider := &fakeProvider{}
	backend := &fakeBackend{err: errors.New("agent offline")}
	store := NewMemoryStore(testConfig("c1", "fake"))
	service := newTestService(t, store, backend, provider)

	if err := service.HandleInbound(context.Background(), inbound("c1", "chat-1", "m1", "hi")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "failure notice", func() bool { return len(provider.streamUpdates()) == 1 })
	if got := provider.streamUpdates()[0]; got == "" || !contains(got, "agent offline") {
		t.Fatalf("failure update = %q", got)
	}
}

func TestServiceQueueIsSerialPerChat(t *testing.T) {
	provider := &fakeProvider{}
	release := make(chan struct{})
	backend := &fakeBackend{session: SessionRef{SessionID: "s1"}, block: release}
	store := NewMemoryStore(testConfig("c1", "fake"))
	service := newTestService(t, store, backend, provider)

	for i := 0; i < 2; i++ {
		if err := service.HandleInbound(context.Background(), inbound("c1", "chat-1", fmt.Sprintf("m%d", i), "hi")); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, "first handle", func() bool { return len(backend.requests()) == 1 })
	if got := atomic.LoadInt32(&backend.maxInflyt); got != 1 {
		t.Fatalf("max inflight = %d, want 1 (serial per chat)", got)
	}
	close(release)
	waitFor(t, "second handle", func() bool { return len(backend.requests()) == 2 })
}

func TestServiceQueueFullRejects(t *testing.T) {
	provider := &fakeProvider{}
	release := make(chan struct{})
	backend := &fakeBackend{session: SessionRef{SessionID: "s1"}, block: release}
	store := NewMemoryStore(testConfig("c1", "fake"))
	service := newTestService(t, store, backend, provider, func(deps *Deps) { deps.QueueSize = 1 })

	if err := service.HandleInbound(context.Background(), inbound("c1", "chat-1", "m0", "hi")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "first handle", func() bool { return len(backend.requests()) == 1 })
	if err := service.HandleInbound(context.Background(), inbound("c1", "chat-1", "m1", "hi")); err != nil {
		t.Fatalf("second enqueue should fit in queue: %v", err)
	}
	if err := service.HandleInbound(context.Background(), inbound("c1", "chat-1", "m2", "hi")); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("third enqueue error = %v, want ErrQueueFull", err)
	}
	close(release)
}

func TestServiceStartRegistersPolling(t *testing.T) {
	provider := &fakeProvider{polling: true}
	backend := &fakeBackend{}
	store := NewMemoryStore(testConfig("c1", "fake"))
	service := newTestService(t, store, backend, provider)

	ctx, cancel := context.WithCancel(context.Background())
	if err := service.Start(ctx); err != nil {
		t.Fatal(err)
	}
	cancel()
	service.Stop(context.Background())
}

func TestServiceDisabledConnectorIgnoresInbound(t *testing.T) {
	provider := &fakeProvider{}
	backend := &fakeBackend{}
	store := NewMemoryStore(testConfig("c1", "fake", func(cfg *Config) { cfg.Enabled = false }))
	service := newTestService(t, store, backend, provider)

	if err := service.HandleInbound(context.Background(), inbound("c1", "chat-1", "m1", "hi")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if len(backend.requests()) != 0 || len(provider.sentTexts()) != 0 {
		t.Fatalf("disabled connector must stay silent")
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle || indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// ── P0/P1 回归用例 ─────────────────────────────────────────────────────────

// serviceRunning 报告某连接器当前是否登记了轮询实例（测试内省）。
func serviceRunning(s *Service, id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.running[id]
	return ok
}

// TestServiceRequestCtxDoesNotStopPolling 覆盖 Fix-1：REST 风格短命请求 ctx
// （r.Context()，handler 返回即取消）不得成为轮询生命周期。
func TestServiceRequestCtxDoesNotStopPolling(t *testing.T) {
	provider := &fakeProvider{polling: true}
	backend := &fakeBackend{}
	store := NewMemoryStore(testConfig("c1", "fake"))
	service := newTestService(t, store, backend, provider)

	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer service.Stop(context.Background())
	waitFor(t, "initial polling start", func() bool {
		starts, _ := provider.pollCounts()
		return starts == 1
	})

	// 模拟管理面保存配置：ReloadConnector 收到的是请求 ctx，返回后即取消。
	reqCtx, cancel := context.WithCancel(context.Background())
	if err := service.ReloadConnector(reqCtx, "c1"); err != nil {
		t.Fatal(err)
	}
	cancel()
	waitFor(t, "reloaded polling start", func() bool {
		starts, _ := provider.pollCounts()
		return starts == 2
	})
	time.Sleep(50 * time.Millisecond)
	if _, exits := provider.pollCounts(); exits != 1 {
		t.Fatalf("请求 ctx 取消不得停掉轮询：exits=%d, want 1（仅 Reload 停掉的旧实例）", exits)
	}
	if !serviceRunning(service, "c1") {
		t.Fatal("请求 ctx 取消后轮询登记项应仍在")
	}
}

// TestServiceTaskSurvivesRequestCtxCancellation 覆盖 Fix-1：webhook 202 /
// handler 返回后请求 ctx 取消，已入队的任务仍应完整处理（不因 ctx.Err 失败）。
func TestServiceTaskSurvivesRequestCtxCancellation(t *testing.T) {
	provider := &fakeProvider{}
	release := make(chan struct{})
	backend := &fakeBackend{
		session: SessionRef{SessionID: "s1"},
		reply:   []OutboundMessage{{Text: "已完成"}},
		block:   release,
	}
	store := NewMemoryStore(testConfig("c1", "fake"))
	service := newTestService(t, store, backend, provider)
	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer service.Stop(context.Background())

	reqCtx, cancel := context.WithCancel(context.Background())
	if err := service.HandleInbound(reqCtx, inbound("c1", "chat-1", "m1", "hi")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "task started", func() bool { return len(backend.requests()) == 1 })
	cancel() // handler 返回：请求 ctx 取消
	time.Sleep(30 * time.Millisecond)
	if updates := provider.streamUpdates(); len(updates) != 0 {
		t.Fatalf("任务不应被请求 ctx 取消（收到失败回执）：%v", updates)
	}

	close(release)
	waitFor(t, "task finished", func() bool { return len(provider.finalMessages()) == 1 })
	if got := provider.finalMessages()[0].Text; got != "已完成" {
		t.Fatalf("final = %q, want 已完成", got)
	}
}

// TestServiceDedupeKeyScopedByChat 覆盖 Fix-2：telegram 的 message_id 仅在
// chat 内唯一，多 chat 复用同一 ID 必须各自处理；同一 chat 内仍去重。
func TestServiceDedupeKeyScopedByChat(t *testing.T) {
	provider := &fakeProvider{}
	backend := &fakeBackend{session: SessionRef{SessionID: "s1"}, reply: []OutboundMessage{{Text: "ok"}}}
	store := NewMemoryStore(testConfig("c1", "fake"))
	service := newTestService(t, store, backend, provider)

	if err := service.HandleInbound(context.Background(), inbound("c1", "chat-1", "m1", "来自 chat-1")); err != nil {
		t.Fatal(err)
	}
	if err := service.HandleInbound(context.Background(), inbound("c1", "chat-2", "m1", "来自 chat-2")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "both chats handled", func() bool { return len(backend.requests()) == 2 })

	if err := service.HandleInbound(context.Background(), inbound("c1", "chat-1", "m1", "来自 chat-1")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	if got := len(backend.requests()); got != 2 {
		t.Fatalf("同一 chat 内重复消息应去重：backend handled %d times, want 2", got)
	}
}

// TestServiceQueueFullDoesNotClaimInbound 覆盖 Fix-5：队列满（宿主回 429）时
// 消息不得被账本预认领，平台重试仍可入队处理。
func TestServiceQueueFullDoesNotClaimInbound(t *testing.T) {
	provider := &fakeProvider{}
	release := make(chan struct{})
	backend := &fakeBackend{session: SessionRef{SessionID: "s1"}, block: release}
	store := NewMemoryStore(testConfig("c1", "fake"))
	service := newTestService(t, store, backend, provider, func(deps *Deps) { deps.QueueSize = 1 })

	if err := service.HandleInbound(context.Background(), inbound("c1", "chat-1", "m0", "hi")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "first handle", func() bool { return len(backend.requests()) == 1 })
	// 队列容量 1：m1 占用缓冲位（等待 worker），m2 触发 ErrQueueFull。
	if err := service.HandleInbound(context.Background(), inbound("c1", "chat-1", "m1", "hi")); err != nil {
		t.Fatal(err)
	}
	if err := service.HandleInbound(context.Background(), inbound("c1", "chat-1", "m2", "hi")); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("third enqueue error = %v, want ErrQueueFull", err)
	}
	// ErrQueueFull 路径未认领账本：同 chat+messageID 仍可被认领（即平台重试可再入队）。
	if claimed, err := store.ClaimInbound("c1", "chat-1|m2"); err != nil || !claimed {
		t.Fatalf("queue-full 消息不应被预认领（claimed=%v err=%v）", claimed, err)
	}

	close(release)
	waitFor(t, "queued message handled", func() bool { return len(backend.requests()) == 2 })
}

// TestServiceStalePollerCleanupKeepsNewerRegistration 覆盖 Fix-4：旧轮询实例
// 退出时的清理不得误删 Reload 后新登记的 cancel（否则新实例无法被 Stop 取消）。
// 刻意不 Start：baseCtx 回落 Background，取消只能经登记项生效，放大误删后果。
func TestServiceStalePollerCleanupKeepsNewerRegistration(t *testing.T) {
	provider := &fakeProvider{polling: true, pollExitDelay: 30 * time.Millisecond}
	backend := &fakeBackend{}
	store := NewMemoryStore(testConfig("c1", "fake"))
	service := newTestService(t, store, backend, provider)

	if err := service.StartConnector(context.Background(), "c1"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "initial polling start", func() bool {
		starts, _ := provider.pollCounts()
		return starts == 1
	})

	for i := 0; i < 10; i++ {
		service.StopConnector("c1")
		if err := service.ReloadConnector(context.Background(), "c1"); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "reloaded polling start", func() bool {
			starts, _ := provider.pollCounts()
			return starts == int32(i+2)
		})
		// 旧实例在 ctx 取消后延迟 30ms 才走清理；无代际校验时会误删新登记项。
		time.Sleep(60 * time.Millisecond)
		if !serviceRunning(service, "c1") {
			t.Fatalf("iteration %d: 旧实例清理误删了 Reload 后的新登记", i)
		}
	}

	starts, _ := provider.pollCounts()
	service.StopConnector("c1")
	waitFor(t, "registered poller stopped by StopConnector", func() bool {
		_, exits := provider.pollCounts()
		return exits == starts
	})
}

// ── Fix-S2：白名单 fail-closed 安全闸 ──────────────────────────────────────

// TestServiceRequireAllowedUsersSkipsEmptyWhitelist 覆盖 Fix-S2：宿主开启
// fail-closed 闸时，启用但白名单为空的连接器不得 Activate/轮询，并留下含
// 连接器 id 与处置说明（allowed_users）的告警日志。
func TestServiceRequireAllowedUsersSkipsEmptyWhitelist(t *testing.T) {
	provider := &fakeProvider{polling: true}
	backend := &fakeBackend{}
	store := NewMemoryStore(testConfig("c1", "fake"))

	var logs []string
	service := newTestService(t, store, backend, provider, func(deps *Deps) {
		deps.RequireAllowedUsers = true
		deps.Logf = func(format string, args ...any) {
			logs = append(logs, fmt.Sprintf(format, args...))
		}
	})

	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer service.Stop(context.Background())

	// 闸生效时不会进入 RunPolling；留出调度窗口再断言。
	time.Sleep(50 * time.Millisecond)
	if starts, exits := provider.pollCounts(); starts != 0 || exits != 0 {
		t.Fatalf("空白名单连接器不得轮询：starts=%d exits=%d", starts, exits)
	}
	if serviceRunning(service, "c1") {
		t.Fatal("空白名单连接器不应登记轮询实例")
	}
	joined := strings.Join(logs, "\n")
	if !strings.Contains(joined, "c1") || !strings.Contains(joined, "allowed_users") {
		t.Fatalf("应记录含连接器 id 与 allowed_users 处置说明的告警，logs=%v", logs)
	}
}

// TestServiceRequireAllowedUsersStartsWithWhitelist 覆盖 Fix-S2：配置了非空
// 白名单时闸不拦截，照常启动轮询。
func TestServiceRequireAllowedUsersStartsWithWhitelist(t *testing.T) {
	provider := &fakeProvider{polling: true}
	backend := &fakeBackend{}
	store := NewMemoryStore(testConfig("c1", "fake", func(cfg *Config) {
		cfg.Policy.AllowedUsers = []string{"owner"}
	}))
	service := newTestService(t, store, backend, provider, func(deps *Deps) {
		deps.RequireAllowedUsers = true
	})

	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer service.Stop(context.Background())
	waitFor(t, "polling start with whitelist", func() bool {
		starts, _ := provider.pollCounts()
		return starts == 1
	})
}

// TestServiceWithoutGateStartsEmptyWhitelist 覆盖 Fix-S2：RequireAllowedUsers
// 为 false（runtime-server 兼容口径）时空白名单连接器照常启动。
func TestServiceWithoutGateStartsEmptyWhitelist(t *testing.T) {
	provider := &fakeProvider{polling: true}
	backend := &fakeBackend{}
	store := NewMemoryStore(testConfig("c1", "fake"))
	service := newTestService(t, store, backend, provider) // 默认 RequireAllowedUsers=false

	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer service.Stop(context.Background())
	waitFor(t, "polling start without gate", func() bool {
		starts, _ := provider.pollCounts()
		return starts == 1
	})
}
