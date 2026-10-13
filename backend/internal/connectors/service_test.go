package connectors

import (
	"context"
	"errors"
	"fmt"
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

	polling bool
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
	<-ctx.Done()
	return nil
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
