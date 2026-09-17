package commands

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/usageanalytics"
)

// TestLocalHostPipelinePersistsToolObservability 是 P0（G1+G2）的本地链路端到端
// 证据：真实 host 路径（localChatRuntimeHost）同时挂上
//
//	① A 通道桥（总线 → session_events）
//	② usageanalytics 采集器（总线 → usage_analytics.sqlite）
//
// 后，agent loop 发出的 tool.requested/tool.completed 必须同时出现在两处。
func TestLocalHostPipelinePersistsToolObservability(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage_analytics.sqlite")
	t.Setenv(usageanalytics.EnvDBPath, dbPath)

	store := runtimechat.NewInMemoryRuntimeStore(64)
	bus := runtimeevents.NewBus()
	host := &localChatRuntimeHost{EventBus: bus, EventStore: store, RuntimeStore: store}
	host.bindRuntimeEventPersistence()
	service := ensureLocalUsageService(host)
	if service == nil {
		t.Fatal("本地分析服务应可挂载（P0-1 启动即 attach）")
	}
	t.Cleanup(func() {
		// 先关分析库再清临时目录（Windows 上 sqlite 句柄会阻塞删除）。
		service.Close()
	})

	started := time.Now().UTC()
	bus.Publish(runtimeevents.Event{
		Type:      "tool.requested",
		SessionID: "session-pipeline",
		Payload: map[string]interface{}{
			"tool_call_id": "call-pipeline",
			"logical_tool": "view",
			"turn_id":      "turn-pipeline",
			"step":         1,
		},
		Timestamp: started,
	})
	bus.Publish(runtimeevents.Event{
		Type:      "tool.completed",
		SessionID: "session-pipeline",
		Payload: map[string]interface{}{
			"tool_call_id": "call-pipeline",
			"logical_tool": "view",
			"turn_id":      "turn-pipeline",
			"step":         1,
			"ok":           false,
			"outcome":      "failed",
			"error_code":   "TOOL_TIMEOUT",
			"retryable":    true,
			"duration_ms":  321,
		},
		Timestamp: started.Add(time.Second),
	})

	// ① 会话事件库：落盘别名为 tool_started / tool_finished（前端回放契约）。
	events, err := store.ListEvents(context.Background(), "session-pipeline", 0, 0)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	types := map[string]bool{}
	for _, event := range events {
		types[event.Type] = true
	}
	if !types[runtimechat.EventToolStarted] || !types[runtimechat.EventToolFinished] {
		t.Fatalf("会话事件库缺少工具生命周期事件（G2）：%v", types)
	}

	// ② 分析库：tool 生命周期必须写入 usage_tool_calls（G1）。
	stats, err := service.Store().ToolStats(usageanalytics.ToolStatsQuery{SessionID: "session-pipeline"})
	if err != nil {
		t.Fatalf("ToolStats: %v", err)
	}
	if len(stats.Tools) != 1 {
		t.Fatalf("usage_tool_calls 期望 1 行，实际 %d: %+v", len(stats.Tools), stats.Tools)
	}
	tool := stats.Tools[0]
	if tool.ToolName != "view" || tool.Calls != 1 || tool.Failures != 1 {
		t.Fatalf("工具聚合不符: %+v", tool)
	}
	if tool.ErrorTop == nil || len(tool.ErrorTop) == 0 || tool.ErrorTop[0].ErrorCode != "TOOL_TIMEOUT" {
		t.Fatalf("错误码未落库: %+v", tool.ErrorTop)
	}
}
