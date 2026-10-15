package runtimeapi

import (
	"testing"
	"time"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// TestGetRuntimeEventBusDoesNotSelfDeadlock 钉住 2026-10-15 的回归：
// subscribeGoalDriver 曾自行经 getRuntimeEventBus() 拉总线，而它被放在
// attachRuntimeEventBridge 的 Once 内调用——Once 内重入同一个 Once 会永久
// 自锁（sync.Once 文档明确禁止）：首次取总线即挂死，测试包只能等 -timeout
// 的 10 分钟墙后带 goroutine dump 退出。修复：总线由 Once 作用域注入
// subscribeGoalDriver(bus)，Once 内不再回调 getRuntimeEventBus。
func TestGetRuntimeEventBusDoesNotSelfDeadlock(t *testing.T) {
	handler := NewHandler(nil, nil, nil)
	result := make(chan *runtimeevents.Bus, 1)
	go func() { result <- handler.getRuntimeEventBus() }()
	select {
	case bus := <-result:
		if bus == nil {
			t.Fatal("getRuntimeEventBus() 返回 nil 总线")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("getRuntimeEventBus() 自锁：重入 attachRuntimeEventBridge 的 Once（subscribeGoalDriver 总线注入回归）")
	}
}
