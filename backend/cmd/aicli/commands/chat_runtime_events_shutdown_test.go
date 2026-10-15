package commands

import (
	"testing"
	"time"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// TestChatRuntimeEventBridgeShutdownStopsWorkerAndUnsubscribes 回归：bridge
// 此前没有关闭路径 —— worker goroutine for-range eventQueue 永不退出（生产
// 代码从不 close 队列），EventBus 全量订阅句柄被丢弃。会话被替换/丢弃后，
// 旧 bridge 的 goroutine 驻留进程，共享总线每条事件仍会扇出给它。
func TestChatRuntimeEventBridgeShutdownStopsWorkerAndUnsubscribes(t *testing.T) {
	session := &ChatSession{}
	bridge := newChatRuntimeEventBridge(session)
	if bridge == nil {
		t.Fatal("bridge not created")
	}

	bus := runtimeevents.NewBus()
	session.LocalRuntimeHost = &localChatRuntimeHost{EventBus: bus}

	bridge.start()
	bridge.startProcessor()

	// Shutdown 前：worker 存活（能消费投递）且订阅在总线上（事件能进队）。
	bus.Publish(runtimeevents.Event{Type: "shutdown_probe_event"})
	if !waitForBridgeDrain(bridge, 2*time.Second) {
		t.Fatal("event did not reach the bridge before shutdown")
	}

	bridge.Shutdown()
	bridge.Shutdown() // 幂等

	// Worker goroutine 必须退出：drained 后 backlog 为空，停机窗口直接返回。
	if !bridge.waitWorkerExit(2 * time.Second) {
		t.Fatal("bridge worker goroutine did not exit after shutdown")
	}

	// Shutdown 后：退订生效，再发布的事件不得到达 bridge（Handle 不再被调）。
	before := bridge.drainSnapshot().Processed
	bus.Publish(runtimeevents.Event{Type: "shutdown_probe_event"})
	time.Sleep(50 * time.Millisecond)
	after := bridge.drainSnapshot().Processed
	if after != before {
		t.Fatalf("bridge still received events after shutdown: before=%d after=%d", before, after)
	}
}

// TestChatRuntimeEventBridgeShutdownBeforeStart 覆盖竞态：会话在能力面
// （LocalRuntimeHost）挂载之前就被丢弃，Shutdown 先于 start 完成；随后
// start 不得再把订阅挂上总线。
func TestChatRuntimeEventBridgeShutdownBeforeStart(t *testing.T) {
	session := &ChatSession{}
	bridge := newChatRuntimeEventBridge(session)
	if bridge == nil {
		t.Fatal("bridge not created")
	}

	bridge.Shutdown()

	bus := runtimeevents.NewBus()
	session.LocalRuntimeHost = &localChatRuntimeHost{EventBus: bus}
	bridge.start()
	bridge.startProcessor()

	bus.Publish(runtimeevents.Event{Type: "late_start_probe_event"})
	time.Sleep(50 * time.Millisecond)
	if processed := bridge.drainSnapshot().Processed; processed != 0 {
		t.Fatalf("late-started bridge subscribed after shutdown: processed=%d", processed)
	}
	if !bridge.waitWorkerExit(2 * time.Second) {
		t.Fatal("worker started after shutdown did not exit")
	}
}

// waitForBridgeDrain 等待 enqueued==processed 且无 deferred/critical 滞留。
func waitForBridgeDrain(bridge *chatRuntimeEventBridge, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		snap := bridge.drainSnapshot()
		if !snap.undeliveredData() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return !bridge.drainSnapshot().undeliveredData()
}
