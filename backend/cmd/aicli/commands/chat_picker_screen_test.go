package commands

import (
	"errors"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

// chat_picker_screen_test.go 覆盖批次 2 的 A 族收编契约：
//   - unified：picker 在框架租约内运行，close 序列由框架统一完成；
//   - 结果应用发生在租约释放之后（Run 闭包返回时租约仍在，返回后已释放）；
//   - legacy：回退到 chatPickerOpen/Close 内联实现（§6.1 回退路径）；
//   - 降级：能力不足时不进副屏、不吞输出（调用方静默返回）；
//   - 三阶段错误映射回 picker 既有文案与哨兵。

func testChatPickerScreenHooks() chatPickerLeaseHooks {
	return chatPickerLeaseHooks{
		Open:  func(leaseID uint64) ui.UIAction { return ui.OpenThemePicker{LeaseID: leaseID} },
		Close: func(leaseID uint64) ui.UIAction { return ui.CloseThemePicker{LeaseID: leaseID} },
	}
}

func TestChatPickerScreenUnifiedRunsInsideFrameworkLease(t *testing.T) {
	chatScreenTestSeamsInstall(t, true)
	session := newChatScreenTestSession(t)

	leaseDuringRun := false
	res := runChatPickerScreen(session, chatPickerScreen{
		ID:    "test.picker",
		Title: "测试选择器",
		Hooks: testChatPickerScreenHooks(),
		Run: func(s *ChatSession, lease ui.ScreenLease) error {
			if lease == nil || lease.ID() == 0 {
				t.Fatal("picker run 必须拿到框架租约")
			}
			leaseDuringRun = s.Surface.LeaseActive()
			return nil
		},
	})
	if res.Err != nil || res.Degraded {
		t.Fatalf("unified picker result = %+v，期望成功", res)
	}
	if !leaseDuringRun {
		t.Fatal("picker 交互体必须在租约存续期内运行")
	}
	if session.Surface.LeaseActive() {
		t.Fatal("picker 交互结束后租约泄漏")
	}
	snapshot := chatScreenCounterSnapshotForDebug()
	if snapshot.Opens != 1 || snapshot.Closes != 1 || snapshot.CloseConfirm != 1 {
		t.Fatalf("counters = %+v，期望 opens=1 closes=1 confirm=1", snapshot)
	}
}

func TestChatPickerScreenUnifiedRunErrorStillClosesLease(t *testing.T) {
	chatScreenTestSeamsInstall(t, true)
	session := newChatScreenTestSession(t)

	runErr := errors.New("选择器交互失败")
	res := runChatPickerScreen(session, chatPickerScreen{
		ID:    "test.picker",
		Title: "测试选择器",
		Hooks: testChatPickerScreenHooks(),
		Run: func(*ChatSession, ui.ScreenLease) error {
			return runErr
		},
	})
	if !errors.Is(res.Err, runErr) {
		t.Fatalf("run error = %v，期望 %v", res.Err, runErr)
	}
	if res.Phase != chatPickerPhaseRun {
		t.Fatalf("failure phase = %s，期望 run", res.Phase)
	}
	if session.Surface.LeaseActive() {
		t.Fatal("run 失败后租约泄漏")
	}
	snapshot := chatScreenCounterSnapshotForDebug()
	if snapshot.Opens != 1 || snapshot.Closes != 1 || snapshot.CloseError != 1 {
		t.Fatalf("counters = %+v，期望 opens=1 closes=1 error=1", snapshot)
	}
}

func TestChatPickerScreenUnifiedDegradeIsSilent(t *testing.T) {
	chatScreenTestSeamsInstall(t, false)
	session := newChatScreenTestSession(t)

	ran := false
	res := runChatPickerScreen(session, chatPickerScreen{
		ID:    "test.picker",
		Title: "测试选择器",
		Hooks: testChatPickerScreenHooks(),
		Run: func(*ChatSession, ui.ScreenLease) error {
			ran = true
			return nil
		},
	})
	if !res.Degraded || res.Err != nil {
		t.Fatalf("degraded result = %+v，期望 Degraded 且无错误", res)
	}
	if ran {
		t.Fatal("能力不足时不得运行 picker 交互体")
	}
	if session.Surface.LeaseActive() {
		t.Fatal("降级路径不得持有租约")
	}
	snapshot := chatScreenCounterSnapshotForDebug()
	if snapshot.Opens != 0 || snapshot.DegradeUnavailable != 1 {
		t.Fatalf("counters = %+v，期望 opens=0 degradeUnavailable=1", snapshot)
	}
}

// 批次 5（D-E）：legacy 开关值与未知值是同一语义——只记 unknown_env 警告，
// 交互体仍走统一框架（§6.1「批次 5 完成后删除分支与开关」）。
func TestChatPickerScreenRetiredEnvValueRunsUnified(t *testing.T) {
	chatScreenTestSeamsInstall(t, true)
	chatScreenFrameworkEnvLookup = func() string { return "legacy" }
	session := newChatScreenTestSession(t)

	leaseDuringRun := false
	res := runChatPickerScreen(session, chatPickerScreen{
		ID:    "test.picker",
		Title: "测试选择器",
		Hooks: testChatPickerScreenHooks(),
		Run: func(s *ChatSession, lease ui.ScreenLease) error {
			leaseDuringRun = s.Surface.LeaseActive() && lease != nil
			return nil
		},
	})
	if res.Err != nil || res.Degraded {
		t.Fatalf("retired picker env result = %+v，期望在统一框架内成功", res)
	}
	if !leaseDuringRun {
		t.Fatal("picker 交互体必须在租约内运行")
	}
	if session.Surface.LeaseActive() {
		t.Fatal("picker 租约泄漏")
	}
	snapshot := chatScreenCounterSnapshotForDebug()
	if snapshot.Opens != 1 || snapshot.Closes != 1 {
		t.Fatalf("retired env 值必须仍走统一入口并计数: %+v", snapshot)
	}
	if snapshot.DegradeUnknownEnv != 1 {
		t.Fatalf("retired env 值必须只记 unknown_env 警告: %+v", snapshot)
	}
}

func TestChatPickerScreenErrorTextMapsPhases(t *testing.T) {
	cases := []struct {
		name string
		res  chatPickerScreenResult
		want string
	}{
		{
			name: "open state uncommitted",
			res:  chatPickerScreenResult{Phase: chatPickerPhaseOpen, Err: errChatPickerStateUncommitted},
			want: "主题选择器状态未提交",
		},
		{
			name: "open render not ready",
			res:  chatPickerScreenResult{Phase: chatPickerPhaseOpen, Err: errChatPickerRenderNotReady},
			want: "主题选择器渲染未就绪",
		},
		{
			name: "open generic",
			res:  chatPickerScreenResult{Phase: chatPickerPhaseOpen, Err: errors.New("boom")},
			want: "打开主题选择器失败: boom",
		},
		{
			name: "close actor not idle",
			res:  chatPickerScreenResult{Phase: chatPickerPhaseClose, Err: errChatPickerActorNotIdle},
			want: "主题选择器关闭未就绪",
		},
		{
			name: "run failure",
			res:  chatPickerScreenResult{Phase: chatPickerPhaseRun, Err: errors.New("boom")},
			want: "主题选择器失败: boom",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := chatPickerScreenErrorText("主题选择器", tc.res)
			if err == nil || err.Error() != tc.want {
				t.Fatalf("error text = %v，期望 %q", err, tc.want)
			}
		})
	}
}

// 统一框架的框架哨兵必须映射回 picker 既有哨兵，保证批次 2 之前调用方的
// errors.Is 判定不变。
func TestChatPickerMapFrameworkErrKeepsPickerSentinels(t *testing.T) {
	cases := []struct {
		in   error
		want error
	}{
		{errChatScreenStateUncommitted, errChatPickerStateUncommitted},
		{errChatScreenRenderNotReady, errChatPickerRenderNotReady},
		{errChatScreenActorNotIdle, errChatPickerActorNotIdle},
	}
	for _, tc := range cases {
		mapped := chatPickerMapFrameworkErr(tc.in)
		if !errors.Is(mapped, tc.want) {
			t.Fatalf("mapped(%v) = %v，期望可匹配 %v", tc.in, mapped, tc.want)
		}
		if !strings.Contains(mapped.Error(), tc.want.Error()) {
			t.Fatalf("mapped(%v) = %v，错误文案应保留 %q", tc.in, mapped, tc.want)
		}
	}
}
