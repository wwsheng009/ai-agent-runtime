package connectors

// 回归（docs/bots/13 §4.2 L3）：IM 来源回合登记器——嵌套计数、并发平衡、
// nil 安全；aicli 与 runtime-server 宿主共用本实现。

import (
	"sync"
	"testing"
)

func TestTurnActivityTrackerBeginEndAndNesting(t *testing.T) {
	tracker := NewTurnActivityTracker()
	if tracker.Active("sess-1") {
		t.Fatal("empty tracker must report inactive")
	}
	end1 := tracker.Begin("sess-1")
	if !tracker.Active("sess-1") {
		t.Fatal("Begin must mark the session active")
	}
	end2 := tracker.Begin("sess-1")
	end1()
	if !tracker.Active("sess-1") {
		t.Fatal("nested Begin must keep session active until all ends")
	}
	end2()
	if tracker.Active("sess-1") {
		t.Fatal("end must clear the session")
	}

	// 空会话与 nil 接收者安全（宿主入口的 nil 守卫）。
	tracker.Begin(" ")()
	if tracker.Active("sess-2") {
		t.Fatal("unrelated session must stay inactive")
	}
	var nilTracker *TurnActivityTracker
	if nilTracker.Active("sess-1") {
		t.Fatal("nil tracker must report inactive")
	}
	nilTracker.Begin("sess-1")()
}

func TestTurnActivityTrackerConcurrentBalance(t *testing.T) {
	tracker := NewTurnActivityTracker()
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			end := tracker.Begin("sess-1")
			_ = tracker.Active("sess-1")
			end()
		}()
	}
	wg.Wait()
	if tracker.Active("sess-1") {
		t.Fatal("concurrent Begin/end must balance back to inactive")
	}
}
