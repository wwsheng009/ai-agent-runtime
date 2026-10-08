package commands

import (
	"errors"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

// chat_screen_framework_test.go 覆盖批次 0 的验收项：
//   - 生命周期：open → 渲染 → close（release → actor idle），租约无泄漏；
//   - Esc 契约：默认 TopLevelClose，StepBack 被 Spec 校验拒绝（D-B）；
//   - 降级：能力不足 / 嵌套 / legacy 开关都走内联文档，不吞输出（D-F / I6/I9）；
//   - 兼容映射：旧 Open* 字段（B 族）翻译为框架 Spec，降级内容与旧实现一致（D-E）；
//   - 计数器：/debug display 可见（§8.2）。

type chatScreenTestSeams struct {
	capability  func(*ChatSession) bool
	documentRun func(*ChatSession, ui.ScreenLease, ui.DebugOverlayOptions) error
	listRun     func(*ChatSession, ui.ScreenLease, ui.FullScreenListOptions) (ui.FullScreenListResult, error)
	envLookup   func() string
}

func chatScreenTestSeamsInstall(t *testing.T, capable bool) *chatScreenTestSeams {
	t.Helper()
	prev := &chatScreenTestSeams{
		capability:  chatScreenCapability,
		documentRun: chatScreenDocumentRunner,
		listRun:     chatScreenListRunner,
		envLookup:   chatScreenFrameworkEnvLookup,
	}
	chatScreenCapability = func(*ChatSession) bool { return capable }
	chatScreenFrameworkEnvLookup = func() string { return "" }
	t.Cleanup(func() {
		chatScreenCapability = prev.capability
		chatScreenDocumentRunner = prev.documentRun
		chatScreenListRunner = prev.listRun
		chatScreenFrameworkEnvLookup = prev.envLookup
		chatScreenOpenActive.Store(0)
		resetChatScreenCountersForTest()
	})
	resetChatScreenCountersForTest()
	chatScreenOpenActive.Store(0)
	return prev
}

func newChatScreenTestSession(t *testing.T) *ChatSession {
	t.Helper()
	surface := ui.NewFixedBottomSurface(nil)
	surface.EnableForTest(72, 18)
	// L1-c：租约一律要求 unified transport（raw DEC 1049 分支已退役），
	// screen-framework 用例面注入最小 transport。
	surface.SetAlternateScreenLeaseTransport(&surfaceLeaseTransportForTest{})
	session := &ChatSession{Surface: surface}
	session.Interaction = newTestChatInteractionCoordinator(t, session)
	return session
}

func documentScreenSpec(id string) chatScreenSpec {
	return chatScreenSpec{
		ID:    id,
		Title: "测试文档屏",
		Kind:  chatScreenDocument,
		Doc:   textLinesDocument([]string{"第一行", "第二行"}),
	}
}

func listScreenSpec(id string) chatScreenSpec {
	return chatScreenSpec{
		ID:    id,
		Title: "测试列表屏",
		Kind:  chatScreenList,
		Rows: []chatScreenRow{
			{Title: "甲"},
			{Title: "乙"},
		},
	}
}

func TestChatScreenDocumentLifecycleReleasesLeaseOnEsc(t *testing.T) {
	chatScreenTestSeamsInstall(t, true)
	session := newChatScreenTestSession(t)

	var gotTitle string
	chatScreenDocumentRunner = func(_ *ChatSession, _ ui.ScreenLease, options ui.DebugOverlayOptions) error {
		gotTitle = options.Title
		return nil // Esc / q：文档屏的正常退出
	}

	outcome := openChatScreen(session, documentScreenSpec("test.document"))
	if outcome.Degraded {
		t.Fatalf("document screen must not degrade: %+v", outcome)
	}
	if outcome.Result != chatScreenClosedEsc {
		t.Fatalf("document Esc result = %q，期望 %q", outcome.Result, chatScreenClosedEsc)
	}
	if gotTitle != "测试文档屏" {
		t.Fatalf("runner title = %q", gotTitle)
	}
	if session.Surface.LeaseActive() {
		t.Fatal("lease leaked after document screen close")
	}
	if active := chatScreenOpenActiveForTest(); active != 0 {
		t.Fatalf("framework active screens = %d，期望 0", active)
	}
	snapshot := chatScreenCounterSnapshotForDebug()
	if snapshot.Opens != 1 || snapshot.Closes != 1 || snapshot.CloseEsc != 1 {
		t.Fatalf("counters = %+v，期望 opens=1 closes=1 esc=1", snapshot)
	}
}

func TestChatScreenListConfirmReturnsIndexAfterLeaseRelease(t *testing.T) {
	chatScreenTestSeamsInstall(t, true)
	session := newChatScreenTestSession(t)

	chatScreenListRunner = func(_ *ChatSession, _ ui.ScreenLease, options ui.FullScreenListOptions) (ui.FullScreenListResult, error) {
		if len(options.Items) != 2 {
			t.Fatalf("list items = %d，期望 2", len(options.Items))
		}
		return ui.FullScreenListResult{Index: 1}, nil
	}

	outcome := openChatScreen(session, listScreenSpec("test.list"))
	if outcome.Result != chatScreenClosedConfirm || outcome.Index != 1 {
		t.Fatalf("confirm outcome = %+v，期望 confirm index=1", outcome)
	}
	if session.Surface.LeaseActive() {
		t.Fatal("选择结果返回时租约必须已释放（I3）")
	}
}

func TestChatScreenListCancelIsEsc(t *testing.T) {
	chatScreenTestSeamsInstall(t, true)
	session := newChatScreenTestSession(t)

	chatScreenListRunner = func(*ChatSession, ui.ScreenLease, ui.FullScreenListOptions) (ui.FullScreenListResult, error) {
		return ui.FullScreenListResult{Cancelled: true}, nil
	}

	outcome := openChatScreen(session, listScreenSpec("test.list.cancel"))
	if outcome.Result != chatScreenClosedEsc || outcome.Index != -1 {
		t.Fatalf("cancel outcome = %+v，期望 esc/-1", outcome)
	}
	if session.Surface.LeaseActive() {
		t.Fatal("取消路径也必须释放租约")
	}
}

func TestChatScreenEscPolicyStepBackRejected(t *testing.T) {
	chatScreenTestSeamsInstall(t, true)
	session := newChatScreenTestSession(t)

	spec := documentScreenSpec("test.step_back")
	spec.Esc = chatScreenEscStepBack
	outcome := openChatScreen(session, spec)
	if outcome.Err == nil {
		t.Fatal("Esc=StepBack 必须被 Spec 校验拒绝（D-B：首期不允许）")
	}
	if session.Surface.LeaseActive() {
		t.Fatal("非法 Spec 不应取到租约")
	}
}

func TestChatScreenDegradesInlineWhenCapabilityMissing(t *testing.T) {
	chatScreenTestSeamsInstall(t, false)
	session := newChatScreenTestSession(t)

	outcome := openChatScreen(session, documentScreenSpec("test.degrade"))
	if !outcome.Degraded || outcome.DegradeReason != "unavailable" {
		t.Fatalf("outcome = %+v，期望 unavailable 降级", outcome)
	}
	if session.Surface.LeaseActive() {
		t.Fatal("降级路径不应持有租约")
	}
	snapshot := chatScreenCounterSnapshotForDebug()
	if snapshot.DegradeUnavailable != 1 {
		t.Fatalf("degrade counter = %+v", snapshot)
	}
}

func TestChatScreenNestedOpenIsFailClosed(t *testing.T) {
	chatScreenTestSeamsInstall(t, true)
	session := newChatScreenTestSession(t)

	chatScreenOpenActive.Store(1)
	defer chatScreenOpenActive.Store(0)

	outcome := openChatScreen(session, documentScreenSpec("test.nested"))
	if !errors.Is(outcome.Err, ErrScreenNested) {
		t.Fatalf("nested outcome err = %v，期望 ErrScreenNested（I9）", outcome.Err)
	}
	if !outcome.Degraded || outcome.DegradeReason != "nested" {
		t.Fatalf("nested outcome = %+v", outcome)
	}
	if session.Surface.LeaseActive() {
		t.Fatal("嵌套拒绝不得取租约")
	}
}

func TestChatScreenRetiredEnvValueRunsUnified(t *testing.T) {
	chatScreenTestSeamsInstall(t, true)
	chatScreenFrameworkEnvLookup = func() string { return "legacy" }
	session := newChatScreenTestSession(t)
	chatScreenDocumentRunner = func(*ChatSession, ui.ScreenLease, ui.DebugOverlayOptions) error { return nil }

	outcome := openChatScreen(session, documentScreenSpec("test.retired_env"))
	if outcome.Degraded {
		t.Fatalf("退休开关值必须按 unified 执行：%+v", outcome)
	}
	if session.Surface.LeaseActive() {
		t.Fatal("close 后租约必须已释放")
	}
	snapshot := chatScreenCounterSnapshotForDebug()
	if snapshot.Opens != 1 {
		t.Fatalf("退休开关值不得阻断开屏：%+v", snapshot)
	}
	if snapshot.DegradeUnknownEnv != 1 {
		t.Fatalf("退休开关值必须记 unknown_env 警告：%+v", snapshot)
	}
}

func TestChatScreenUnknownEnvCountsAndRunsUnified(t *testing.T) {
	chatScreenTestSeamsInstall(t, true)
	chatScreenFrameworkEnvLookup = func() string { return "bogus" }
	session := newChatScreenTestSession(t)
	chatScreenDocumentRunner = func(*ChatSession, ui.ScreenLease, ui.DebugOverlayOptions) error { return nil }

	outcome := openChatScreen(session, documentScreenSpec("test.unknown_env"))
	if outcome.Degraded {
		t.Fatalf("unknown env 应按默认 unified 执行：%+v", outcome)
	}
	if got := chatScreenCounterSnapshotForDebug().DegradeUnknownEnv; got != 1 {
		t.Fatalf("unknown_env 计数 = %d，期望 1", got)
	}
}

func TestChatScreenAccountSpecDegradeDocumentMatchesPlain(t *testing.T) {
	chatScreenTestSeamsInstall(t, false)
	session := newChatScreenTestSession(t)

	req := AccountScreenRequest{Report: chatAccountReport{Provider: "test-provider"}}
	spec := chatScreenAccountSpec(session, req)
	if spec.ID != "account.screen" || !spec.ForceInline {
		t.Fatalf("spec = %+v，期望 account.screen 内联降级", spec)
	}
	want := ui.RenderDocumentPlain(accountScreenFallbackResult(req).Document())
	if got := ui.RenderDocumentPlain(spec.Doc); got != want {
		t.Fatalf("降级文档与旧实现不一致\n got: %q\nwant: %q", got, want)
	}
}

func TestChatScreenWebEndpointsSpecDegradesInline(t *testing.T) {
	chatScreenTestSeamsInstall(t, false)
	session := newChatScreenTestSession(t)

	spec := chatScreenWebEndpointsSpec(session)
	if spec.ID != "web.endpoints" || !spec.ForceInline {
		t.Fatalf("spec = %+v，期望 web.endpoints 内联降级", spec)
	}
	if strings.TrimSpace(ui.RenderDocumentPlain(spec.Doc)) == "" {
		t.Fatal("web endpoints 降级文档不得为空")
	}
}

func TestChatScreenCountersVisibleInDebugDisplay(t *testing.T) {
	chatScreenTestSeamsInstall(t, false)
	session := newChatScreenTestSession(t)

	doc := ui.RenderDocumentPlain(buildChatDebugDisplayDocument(session))
	if !strings.Contains(doc, "Chat Screen Framework (batch 0)") {
		t.Fatal("/debug display 必须包含副屏框架计数器区块（§8.2）")
	}
	if !strings.Contains(doc, "Framework Mode:") {
		t.Fatal("计数区块必须报告当前开关模式")
	}
}
