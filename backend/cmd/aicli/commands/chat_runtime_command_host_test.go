package commands

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

type recordingChatEventStore struct {
	mu     sync.Mutex
	events []runtimeevents.Event
}

func (s *recordingChatEventStore) AppendEvent(_ context.Context, event runtimeevents.Event) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, event)
	return int64(len(s.events)), nil
}

func (s *recordingChatEventStore) ListEvents(context.Context, string, int64, int) ([]runtimeevents.Event, error) {
	return nil, nil
}

func (s *recordingChatEventStore) snapshot() []runtimeevents.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]runtimeevents.Event, len(s.events))
	copy(out, s.events)
	return out
}

func (s *recordingChatEventStore) runtimeInteractions() []runtimeevents.Event {
	var out []runtimeevents.Event
	for _, event := range s.snapshot() {
		if event.Type == chatEventRuntimeInteraction {
			out = append(out, event)
		}
	}
	return out
}

// newRuntimeHostTestSession 构造带统一渲染面 + 审计存储的宿主测试会话。
func newRuntimeHostTestSession(t *testing.T) (*ChatSession, *chatInteractionCoordinator, *bytes.Buffer, *recordingChatEventStore) {
	t.Helper()
	store := &recordingChatEventStore{}
	session := &ChatSession{
		LocalRuntimeHost: &localChatRuntimeHost{EventStore: store},
		RuntimeSession:   &runtimechat.Session{ID: "runtime-host-test"},
	}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	coordinator := newTestChatInteractionCoordinator(t, session)
	session.Interaction = coordinator

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(72, 18)
	coordinator.SetSurface(surface)
	var output bytes.Buffer
	if !coordinator.enableUnifiedRendererWithWriter(&output) {
		t.Fatal("unified renderer did not attach")
	}
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)
	output.Reset()
	return session, coordinator, &output, store
}

// T5/宿主 inline：注册表中 non-first-batch 的 read 命令在忙时通道立即执行。
func TestRuntimeCommandHostInlinesRegisteredReadCommand(t *testing.T) {
	t.Setenv(chatBusyCommandEnv, "on")
	t.Setenv(runtimeInteractionEnv, "auto")
	session, coordinator, output, store := newRuntimeHostTestSession(t)
	release := beginChatInputShadowLevel(session, chatInputOwnerBusyCapture)
	defer release()

	if !runtimeCommandHostFor(session).SubmitBusy("/reasoning status") {
		t.Fatal("/reasoning status（inline/read）应在宿主内立即执行")
	}
	if !session.commandMu.TryLock() {
		t.Fatal("宿主执行后 commandMu 必须已释放")
	}
	session.commandMu.Unlock()

	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)
	if strings.TrimSpace(output.String()) == "" {
		t.Fatal("inline 命令结果未到达统一渲染面")
	}

	events := store.runtimeInteractions()
	if len(events) != 1 {
		t.Fatalf("应产生 1 条审计事件，实际 %d", len(events))
	}
	payload := events[0].Payload
	if payload["result"] != "executed" || payload["mode"] != "inline" || payload["effect"] != "read" {
		t.Fatalf("审计事件内容不符：%+v", payload)
	}
	if _, ok := payload["duration_ms"]; !ok {
		t.Fatalf("审计事件缺少耗时：%+v", payload)
	}
}

// T6：screen 档在 P2-4 载体落地前降级 queue（不占有，调用方回退入队）。
func TestRuntimeCommandHostDegradesScreenMode(t *testing.T) {
	t.Setenv(chatBusyCommandEnv, "on")
	t.Setenv(runtimeInteractionEnv, "auto")
	session, _, _, store := newRuntimeHostTestSession(t)

	if runtimeCommandHostFor(session).SubmitBusy("/todos") {
		t.Fatal("screen 档在载体落地前必须降级（返回 false）")
	}
	events := store.runtimeInteractions()
	if len(events) != 1 {
		t.Fatalf("应产生 1 条审计事件，实际 %d", len(events))
	}
	if events[0].Payload["result"] != "degraded" || events[0].Payload["mode"] != "screen" {
		t.Fatalf("降级审计内容不符：%+v", events[0].Payload)
	}
}

// T32：inline 且生效域非 read 的命令执行后必须给出 Notice（生效提示）。
func TestRuntimeCommandHostEmitsNoticeForNextTurnEffect(t *testing.T) {
	t.Setenv(chatBusyCommandEnv, "on")
	t.Setenv(runtimeInteractionEnv, "auto")
	session, coordinator, output, _ := newRuntimeHostTestSession(t)
	release := beginChatInputShadowLevel(session, chatInputOwnerBusyCapture)
	defer release()

	if !runtimeCommandHostFor(session).SubmitBusy("/title 忙时改标题") {
		t.Fatal("/title <text>（inline/next-turn）应在宿主内执行")
	}
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)
	if !strings.Contains(output.String(), "下一回合生效") {
		t.Fatalf("next-turn 命令未给出生效提示：%q", output.String())
	}
	// P2-5a：next-turn 语义必须由 Phase A 的会话写承载（无需 actor 重建），
	// 否则忙时执行会静默丢效应。
	if session.RuntimeSession == nil {
		t.Fatal("测试会话缺少 RuntimeSession")
	}
	if got := strings.TrimSpace(session.RuntimeSession.Metadata.Title); got != "忙时改标题" {
		t.Fatalf("next-turn 效应未落盘：title = %q", got)
	}
}

// T30：未登记命令 → queue（不占有、不执行、不丢输入）。
func TestRuntimeCommandHostQueuesUnknownCommand(t *testing.T) {
	t.Setenv(chatBusyCommandEnv, "on")
	t.Setenv(runtimeInteractionEnv, "auto")
	session, _, _, store := newRuntimeHostTestSession(t)

	if runtimeCommandHostFor(session).SubmitBusy("/totally-unknown-command") {
		t.Fatal("未登记命令不得被占有")
	}
	events := store.runtimeInteractions()
	if len(events) != 1 {
		t.Fatalf("应产生 1 条审计事件，实际 %d", len(events))
	}
	payload := events[0].Payload
	if payload["result"] != "queued" || payload["mode"] != "queue" || payload["registered"] != false {
		t.Fatalf("未登记命令审计内容不符：%+v", payload)
	}
}

// block 最小集：/exit 在宿主层被拒绝并给出提示，且不产生退出动作。
func TestRuntimeCommandHostRejectsBlockCommand(t *testing.T) {
	t.Setenv(chatBusyCommandEnv, "on")
	t.Setenv(runtimeInteractionEnv, "auto")
	session, coordinator, output, store := newRuntimeHostTestSession(t)

	if !runtimeCommandHostFor(session).SubmitBusy("/exit") {
		t.Fatal("block 命令应被占有（拒绝 + 提示），不得入队")
	}
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)
	if !strings.Contains(output.String(), "忙时不可退出") {
		t.Fatalf("block 提示未到达渲染面：%q", output.String())
	}
	events := store.runtimeInteractions()
	if len(events) != 1 || events[0].Payload["result"] != "rejected" || events[0].Payload["mode"] != "block" {
		t.Fatalf("block 审计内容不符：%+v", events)
	}
}

// T32：session/process 生效域在宿主层不可达。
func TestRuntimeCommandHostEffectReachability(t *testing.T) {
	if runtimeHostEffectReachable(runtimeEffectSession) {
		t.Fatal("session 生效域必须被宿主拒绝")
	}
	if runtimeHostEffectReachable(runtimeEffectProcess) {
		t.Fatal("process 生效域必须被宿主拒绝")
	}
	for _, effect := range []runtimeEffectScope{runtimeEffectRead, runtimeEffectLive, runtimeEffectNextTurn, runtimeEffectNextCall} {
		if !runtimeHostEffectReachable(effect) {
			t.Fatalf("%s 生效域应可达", effect)
		}
	}
}

// T5/T6/T30/T31/D14：注册表默认接管路由策略；显式关闭总闸时才回退 P1 行为（T18）。
func TestChatBusyPolicyRegistryMappingWhenP2Enabled(t *testing.T) {
	t.Setenv(chatBusyCommandEnv, "on")

	t.Setenv(runtimeInteractionEnv, "auto")
	cases := map[string]chatBusyCommandPolicy{
		"/model status": chatBusyPolicyScreen, // 批次 3：read 变体迁副屏 + 白名单
		"/debug status": chatBusyPolicyImmediate,
		"/model":        chatBusyPolicyDeferred,
		// P2-4b：首批白名单 S 档（screen+read）转由副屏通道消费；
		// 非首批 screen 命令仍 deferred。
		"/todos":         chatBusyPolicyScreen,
		"/history":       chatBusyPolicyScreen,
		"/debug display": chatBusyPolicyScreen,
		"/skills":        chatBusyPolicyScreen, // 批次 4：只读文档变体纳入白名单
		"/exit":          chatBusyPolicyReject,
		"/unknown-cmd":   chatBusyPolicyDeferred,
	}
	for line, want := range cases {
		if got := chatSlashCommandBusyPolicyFor(line); got != want {
			t.Fatalf("auto 模式下 %q 策略为 %s，期望 %s", line, got, want)
		}
	}

	t.Setenv(runtimeInteractionEnv, "readonly")
	if got := chatSlashCommandBusyPolicyFor("/stream on"); got != chatBusyPolicyDeferred {
		t.Fatalf("readonly 下 live 命令应降级，实际 %s", got)
	}
	if got := chatSlashCommandBusyPolicyFor("/model status"); got != chatBusyPolicyScreen {
		t.Fatalf("readonly 下 read 命令应保持 screen 档，实际 %s", got)
	}

	t.Setenv(runtimeInteractionEnv, "off")
	if got := chatSlashCommandBusyPolicyFor("/model status"); got != chatBusyPolicyDeferred {
		t.Fatalf("off 下应全部降级 queue，实际 %s", got)
	}
	if got := chatSlashCommandBusyPolicyFor("/exit"); got != chatBusyPolicyReject {
		t.Fatalf("off 下 block 不受影响，实际 %s", got)
	}

	// 全局档未显式设置 = auto（默认，D14）：注册表仍然接管。
	t.Setenv(runtimeInteractionEnv, "")
	if got := chatSlashCommandBusyPolicyFor("/model status"); got != chatBusyPolicyScreen {
		t.Fatalf("全局档未设置时 /model status 应走注册表 screen，实际 %s", got)
	}
	if got := chatSlashCommandBusyPolicyFor("/todos"); got != chatBusyPolicyScreen {
		t.Fatalf("全局档未设置时 /todos 应走注册表 screen，实际 %s", got)
	}
	// 批次 3/4：/help 由主屏内联迁入只读 ScreenDocument 并进入忙时副屏白名单，
	// 因此注册表接管后忙时档位由 immediate 变为 screen（行为差异见实施记录 §1.4/§1.5）。
	if got := chatSlashCommandBusyPolicyFor("/help"); got != chatBusyPolicyScreen {
		t.Fatalf("全局档未设置时 /help 应走注册表 screen（只读副屏），实际 %s", got)
	}
}
