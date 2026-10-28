package agent

import (
	"strings"
	"testing"
	"time"
)

// waitForWindowAction 等一条投递；执行器是异步单队列，同步断言会假阳性。
func waitForWindowAction(t *testing.T, delivered chan WindowAction) WindowAction {
	t.Helper()
	select {
	case action := <-delivered:
		return action
	case <-time.After(2 * time.Second):
		t.Fatal("window action was never delivered to the handler")
	}
	return WindowAction{}
}

func TestApplyPendingDispatchesWindowActionsToHandler(t *testing.T) {
	delivered := make(chan WindowAction, 4)
	controller := New(Config{Enabled: true}, nil, WithWindowAction(func(action WindowAction) {
		delivered <- action
	}))

	controller.ApplyPending([]string{
		"tail-gating:on",
		"watch:session_2",
		"window:open-terminal:session_1",
	})
	action := waitForWindowAction(t, delivered)
	if action.Kind != "open-terminal" || action.SessionID != "session_1" {
		t.Fatalf("action = %+v", action)
	}
	// 同一条 ack 里的 watch 语义不受窗口动作影响（共用 Pending 但不能互相吞掉）。
	if !controller.TailWanted("session_2") {
		t.Fatal("tail gating lost the watch action")
	}
}

func TestApplyPendingIgnoresMalformedAndUnknownWindowActions(t *testing.T) {
	delivered := make(chan WindowAction, 4)
	controller := New(Config{Enabled: true}, nil, WithWindowAction(func(action WindowAction) {
		delivered <- action
	}))

	controller.ApplyPending([]string{
		"window:explode:session_1", // kind 不在词表
		"window:close",             // 会话级动作缺 ID
		"window:resume:session_9",  // 合法
	})
	action := waitForWindowAction(t, delivered)
	if action.Kind != "resume" || action.SessionID != "session_9" {
		t.Fatalf("action = %+v, want only the well-formed resume", action)
	}
	select {
	case extra := <-delivered:
		t.Fatalf("unexpected extra action %+v", extra)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestApplyPendingWithoutHandlerIsHarmless(t *testing.T) {
	// 未注册执行器（旧宿主/嵌入式调用）：window:* 必须被安静忽略，且不能起 goroutine。
	controller := New(Config{Enabled: true}, nil)
	controller.ApplyPending([]string{"window:close:session_1", "watch:session_1"})
	handled, dropped := controller.WindowActionStats()
	if handled != 0 || dropped != 0 {
		t.Fatalf("stats = (%d,%d), want (0,0)", handled, dropped)
	}
	if !controller.TailWanted("session_1") {
		t.Fatal("watch action must still apply")
	}
}

func TestWindowActionsAreSerialised(t *testing.T) {
	// 单执行器：同一条 ack 里两个动作必须串行进入实现，否则 open 与 resume 会
	// 对同一会话并发起窗口。
	started := make(chan string, 4)
	finished := make(chan string, 4)
	controller := New(Config{Enabled: true}, nil, WithWindowAction(func(action WindowAction) {
		started <- action.SessionID
		time.Sleep(10 * time.Millisecond)
		finished <- action.SessionID
	}))

	controller.ApplyPending([]string{"window:open-terminal:s1", "window:resume:s2", "window:close:s3"})
	seen := make([]string, 0, 3)
	for index := 0; index < 3; index++ {
		first := <-started
		<-finished
		seen = append(seen, first)
	}
	if strings.Join(seen, ",") != "s1,s2,s3" {
		t.Fatalf("delivery order = %v, want s1,s2,s3 (serial, in ack order)", seen)
	}
}

func TestWindowActionQueueDropsInsteadOfBlockingHeartbeat(t *testing.T) {
	release := make(chan struct{})
	controller := New(Config{Enabled: true}, nil, WithWindowAction(func(WindowAction) {
		<-release // 第一个动作卡住，把缓冲填满
	}))

	for index := 0; index < windowActionQueueDepth+4; index++ {
		controller.ApplyPending([]string{"window:close:session_slow"})
	}
	// 心跳读循环绝不能被慢执行器拖住：ApplyPending 必须立刻返回。
	done := make(chan struct{})
	go func() {
		controller.ApplyPending([]string{"window:close:session_final"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("ApplyPending blocked behind a slow window handler")
	}
	close(release)
}
