package connectors

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

// ── 测试替身：实现 CommandBackend 的假后端 ──────────────────────────────────

type fakeCommandBackend struct {
	*fakeBackend

	mu       sync.Mutex
	approval []CommandRequest
	answers  []CommandRequest

	approveResult CommandResult
	approveErr    error
	answerResult  CommandResult
	answerErr     error
}

func (b *fakeCommandBackend) DecideApproval(_ context.Context, req CommandRequest) (CommandResult, error) {
	b.mu.Lock()
	b.approval = append(b.approval, req)
	b.mu.Unlock()
	return b.approveResult, b.approveErr
}

func (b *fakeCommandBackend) AnswerQuestion(_ context.Context, req CommandRequest) (CommandResult, error) {
	b.mu.Lock()
	b.answers = append(b.answers, req)
	b.mu.Unlock()
	return b.answerResult, b.answerErr
}

func (b *fakeCommandBackend) approvalRequests() []CommandRequest {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]CommandRequest(nil), b.approval...)
}

func (b *fakeCommandBackend) answerRequests() []CommandRequest {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]CommandRequest(nil), b.answers...)
}

// namedFailProvider 包装 fakeProvider 以自定义通道名（复用 sendErr 注入失败）。
type namedFailProvider struct {
	*fakeProvider
	name string
}

func (p *namedFailProvider) Name() string { return p.name }

// ── 命令路由（FR-7）────────────────────────────────────────────────────────

func TestServiceApprovalCommandRoutesToCommandBackend(t *testing.T) {
	provider := &fakeProvider{}
	backend := &fakeCommandBackend{
		fakeBackend:   &fakeBackend{},
		approveResult: CommandResult{Handled: true, Reply: []OutboundMessage{{Text: "已批准"}}},
	}
	store := NewMemoryStore(testConfig("c1", "fake"))
	service := newTestService(t, store, backend, provider)
	if err := store.PutBinding(Binding{ConnectorID: "c1", Chat: "chat-1", Session: SessionRef{SessionID: "sess-1"}}); err != nil {
		t.Fatal(err)
	}

	if err := service.HandleInbound(context.Background(), inbound("c1", "chat-1", "m1", "/approve req-9")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "approve reply", func() bool { return len(provider.sentTexts()) > 0 })

	if got := provider.sentTexts(); len(got) != 1 || got[0] != "已批准" {
		t.Fatalf("sent = %v, want [已批准]", got)
	}
	requests := backend.approvalRequests()
	if len(requests) != 1 {
		t.Fatalf("DecideApproval calls = %d, want 1", len(requests))
	}
	req := requests[0]
	if req.Command != "approve" || req.ConnectorID != "c1" || req.Chat != "chat-1" {
		t.Fatalf("request = %+v, want command=approve c1/chat-1", req)
	}
	if req.Session != (SessionRef{SessionID: "sess-1"}) {
		t.Fatalf("session = %+v, want sess-1", req.Session)
	}
	if req.UserID != "u1" || req.MessageID != "m1" {
		t.Fatalf("request = %+v, want user u1 message m1", req)
	}
	if len(req.Args) != 1 || req.Args[0] != "req-9" || req.Text != "req-9" {
		t.Fatalf("args = %v text = %q, want [req-9] / req-9", req.Args, req.Text)
	}
	if len(backend.fakeBackend.requests()) != 0 {
		t.Fatalf("命令不应落入普通 Backend.Handle: %+v", backend.fakeBackend.requests())
	}
	stats, ok := service.Stats("c1")
	if !ok {
		t.Fatal("stats 应存在（命令路径写入日志环）")
	}
	if stats.Inbound != 0 || stats.Outbound != 0 {
		t.Fatalf("命令路径不应计入普通 turn 计数: %+v", stats)
	}
}

func TestServiceDenyCommandWithoutArgsUsesPendingSemantics(t *testing.T) {
	provider := &fakeProvider{}
	backend := &fakeCommandBackend{fakeBackend: &fakeBackend{}, approveResult: CommandResult{Handled: true}}
	service := newTestService(t, NewMemoryStore(testConfig("c1", "fake")), backend, provider)

	if err := service.HandleInbound(context.Background(), inbound("c1", "chat-1", "m1", "/deny")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "deny request", func() bool { return len(backend.approvalRequests()) == 1 })

	req := backend.approvalRequests()[0]
	if req.Command != "deny" {
		t.Fatalf("command = %q, want deny", req.Command)
	}
	if len(req.Args) != 0 || req.Text != "" {
		t.Fatalf("无参数应透传空 Args/Text: %+v", req)
	}
	if !req.Session.IsZero() {
		t.Fatalf("未绑定时 session 应为零值: %+v", req.Session)
	}
	if got := provider.sentTexts(); len(got) != 0 {
		t.Fatalf("Handled 且无 Reply 时不应回复: %v", got)
	}
}

func TestServiceAnswerCommandPassesArgsAndContent(t *testing.T) {
	provider := &fakeProvider{}
	backend := &fakeCommandBackend{
		fakeBackend:  &fakeBackend{},
		answerResult: CommandResult{Handled: true, Reply: []OutboundMessage{{Text: "已提交"}}},
	}
	store := NewMemoryStore(testConfig("c1", "fake"))
	service := newTestService(t, store, backend, provider)
	if err := store.PutBinding(Binding{ConnectorID: "c1", Chat: "chat-1", Session: SessionRef{NodeID: "n1", SessionID: "sess-2"}}); err != nil {
		t.Fatal(err)
	}

	if err := service.HandleInbound(context.Background(), inbound("c1", "chat-1", "m1", "/answer 2 请用中文回答")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "answer reply", func() bool { return len(provider.sentTexts()) > 0 })

	requests := backend.answerRequests()
	if len(requests) != 1 {
		t.Fatalf("AnswerQuestion calls = %d, want 1", len(requests))
	}
	req := requests[0]
	if req.Command != "answer" {
		t.Fatalf("command = %q, want answer", req.Command)
	}
	if len(req.Args) != 2 || req.Args[0] != "2" || req.Args[1] != "请用中文回答" {
		t.Fatalf("args = %v, want [2 请用中文回答]", req.Args)
	}
	if req.Text != "2 请用中文回答" {
		t.Fatalf("text = %q, want 原始参数文本", req.Text)
	}
	if req.Session != (SessionRef{NodeID: "n1", SessionID: "sess-2"}) {
		t.Fatalf("session = %+v, want n1/sess-2", req.Session)
	}
	if got := provider.sentTexts(); len(got) != 1 || got[0] != "已提交" {
		t.Fatalf("sent = %v, want [已提交]", got)
	}
	if len(backend.fakeBackend.requests()) != 0 {
		t.Fatal("命令不应落入普通 Backend.Handle")
	}
}

func TestServiceCommandWithoutCommandBackendGetsClearNotice(t *testing.T) {
	provider := &fakeProvider{}
	backend := &fakeBackend{}
	service := newTestService(t, NewMemoryStore(testConfig("c1", "fake")), backend, provider)

	for _, text := range []string{"/approve", "/deny req-1", "/answer 1 yes"} {
		if err := service.HandleInbound(context.Background(), inbound("c1", "chat-1", "m-"+text, text)); err != nil {
			t.Fatalf("%s: %v", text, err)
		}
	}
	waitFor(t, "三条未闭环提示", func() bool { return len(provider.sentTexts()) == 3 })

	for _, got := range provider.sentTexts() {
		if !strings.Contains(got, "未接入") {
			t.Fatalf("提示应明确说明宿主未接入: %q", got)
		}
	}
	if len(backend.requests()) != 0 {
		t.Fatalf("未实现 CommandBackend 时命令不应落入普通 Handle: %+v", backend.requests())
	}
	stats, _ := service.Stats("c1")
	if stats.Inbound != 0 || stats.Outbound != 0 {
		t.Fatalf("命令路径不应计入普通 turn 计数: %+v", stats)
	}
}

func TestServiceCommandNotHandledRepliesClearNotice(t *testing.T) {
	provider := &fakeProvider{}
	backend := &fakeCommandBackend{fakeBackend: &fakeBackend{}}
	service := newTestService(t, NewMemoryStore(testConfig("c1", "fake")), backend, provider)

	if err := service.HandleInbound(context.Background(), inbound("c1", "chat-1", "m1", "/approve")); err != nil {
		t.Fatal(err)
	}
	if err := service.HandleInbound(context.Background(), inbound("c1", "chat-1", "m2", "/answer 1 是")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "两条未处理提示", func() bool { return len(provider.sentTexts()) == 2 })

	texts := provider.sentTexts()
	if !strings.Contains(texts[0], "没有待处理的审批") {
		t.Fatalf("approve 提示 = %q", texts[0])
	}
	if !strings.Contains(texts[1], "没有待回答的提问") {
		t.Fatalf("answer 提示 = %q", texts[1])
	}
}

func TestServiceCommandBackendErrorRepliesFailure(t *testing.T) {
	provider := &fakeProvider{}
	backend := &fakeCommandBackend{
		fakeBackend: &fakeBackend{},
		approveErr:  errors.New("approval channel offline"),
	}
	service := newTestService(t, NewMemoryStore(testConfig("c1", "fake")), backend, provider)

	if err := service.HandleInbound(context.Background(), inbound("c1", "chat-1", "m1", "/approve")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "失败回执", func() bool { return len(provider.sentTexts()) > 0 })

	got := provider.sentTexts()[0]
	if !strings.Contains(got, "本次处理失败") || !strings.Contains(got, "approval channel offline") {
		t.Fatalf("失败回执 = %q", got)
	}
	stats, _ := service.Stats("c1")
	if stats.Errors != 1 {
		t.Fatalf("errors = %d, want 1", stats.Errors)
	}
}

func TestServiceHelpListsApprovalCommands(t *testing.T) {
	provider := &fakeProvider{}
	service := newTestService(t, NewMemoryStore(testConfig("c1", "fake")), &fakeBackend{}, provider)

	if err := service.HandleInbound(context.Background(), inbound("c1", "chat-1", "m1", "/help")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "help", func() bool { return len(provider.sentTexts()) > 0 })

	got := provider.sentTexts()[0]
	for _, want := range []string{"/approve", "/deny", "/answer"} {
		if !strings.Contains(got, want) {
			t.Fatalf("help 缺少 %s: %q", want, got)
		}
	}
}

// ── 注意力出站（FR-8）──────────────────────────────────────────────────────

func TestNotifySessionFanoutToAllBoundChats(t *testing.T) {
	provider := &fakeProvider{}
	store := NewMemoryStore(testConfig("c1", "fake"), testConfig("c2", "fake"))
	service := newTestService(t, store, &fakeBackend{}, provider)

	putBinding := func(connectorID, chat, sessionID string) {
		t.Helper()
		if err := store.PutBinding(Binding{ConnectorID: connectorID, Chat: chat, Session: SessionRef{SessionID: sessionID}}); err != nil {
			t.Fatal(err)
		}
	}
	putBinding("c1", "chat-a", "sess-1")
	putBinding("c2", "chat-b", "sess-1")
	putBinding("c1", "chat-c", "sess-2")

	msgs := []OutboundMessage{{Text: "任务已完成"}}
	delivered, err := service.NotifySession(context.Background(), SessionRef{SessionID: "sess-1"}, msgs)
	if err != nil {
		t.Fatalf("NotifySession: %v", err)
	}
	if delivered != 2 {
		t.Fatalf("delivered = %d, want 2", delivered)
	}
	for _, chat := range []string{"chat-a", "chat-b"} {
		got := provider.sentFor(chat)
		if len(got) != 1 || got[0].Text != "任务已完成" {
			t.Fatalf("%s sent = %+v, want 任务已完成", chat, got)
		}
	}
	if got := provider.sentFor("chat-c"); len(got) != 0 {
		t.Fatalf("未绑定 sess-1 的 chat-c 不应收到消息: %+v", got)
	}

	// 无绑定 / 零值 session / 空消息：均为 (0, nil)。
	if delivered, err := service.NotifySession(context.Background(), SessionRef{SessionID: "sess-404"}, msgs); delivered != 0 || err != nil {
		t.Fatalf("无绑定 = (%d, %v), want (0, nil)", delivered, err)
	}
	if delivered, err := service.NotifySession(context.Background(), SessionRef{}, msgs); delivered != 0 || err != nil {
		t.Fatalf("零值 session = (%d, %v), want (0, nil)", delivered, err)
	}
	if delivered, err := service.NotifySession(context.Background(), SessionRef{SessionID: "sess-1"}, nil); delivered != 0 || err != nil {
		t.Fatalf("空消息 = (%d, %v), want (0, nil)", delivered, err)
	}
}

func TestNotifySessionPartialFailureAggregates(t *testing.T) {
	ok := &fakeProvider{}
	bad := &namedFailProvider{fakeProvider: &fakeProvider{sendErr: errors.New("token revoked")}, name: "fake-fail"}
	store := NewMemoryStore(testConfig("c1", "fake"), testConfig("c2", "fake-fail"))
	service := newTestService(t, store, &fakeBackend{}, ok)
	if err := service.Register(bad); err != nil {
		t.Fatal(err)
	}
	if err := store.PutBinding(Binding{ConnectorID: "c1", Chat: "chat-a", Session: SessionRef{SessionID: "sess-1"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutBinding(Binding{ConnectorID: "c2", Chat: "chat-b", Session: SessionRef{SessionID: "sess-1"}}); err != nil {
		t.Fatal(err)
	}

	delivered, err := service.NotifySession(context.Background(), SessionRef{SessionID: "sess-1"}, []OutboundMessage{{Text: "待审批"}})
	if delivered != 1 {
		t.Fatalf("delivered = %d, want 1（失败不阻断其它绑定）", delivered)
	}
	if err == nil {
		t.Fatal("部分失败应返回聚合错误")
	}
	for _, want := range []string{"c2", "chat-b", "token revoked"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("聚合错误缺少 %q: %v", want, err)
		}
	}
	if got := ok.sentFor("chat-a"); len(got) != 1 || got[0].Text != "待审批" {
		t.Fatalf("健康绑定仍应收到消息: %+v", got)
	}
}

func TestNotifyAttentionValidatesAndDelivers(t *testing.T) {
	provider := &fakeProvider{}
	store := NewMemoryStore(testConfig("c1", "fake"))
	service := newTestService(t, store, &fakeBackend{}, provider)
	if err := store.PutBinding(Binding{ConnectorID: "c1", Chat: "chat-1", Session: SessionRef{SessionID: "s1"}}); err != nil {
		t.Fatal(err)
	}

	if err := service.NotifyAttention(context.Background(), AttentionNotice{Text: "x"}); err == nil {
		t.Fatal("缺少 session 应报错")
	}
	if err := service.NotifyAttention(context.Background(), AttentionNotice{Session: SessionRef{SessionID: "s1"}, Text: "   "}); err == nil {
		t.Fatal("空文本应报错")
	}
	if err := service.NotifyAttention(context.Background(), AttentionNotice{
		Kind:    AttentionKindApproval,
		Session: SessionRef{SessionID: "s1"},
		Text:    "有新审批待处理",
	}); err != nil {
		t.Fatalf("NotifyAttention: %v", err)
	}
	if got := provider.sentFor("chat-1"); len(got) != 1 || got[0].Text != "有新审批待处理" {
		t.Fatalf("sent = %+v, want 有新审批待处理", got)
	}
}
