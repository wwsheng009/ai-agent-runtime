package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// TestChatRuntimeEventBridge_PersistsLSPRequestFinished 钉死 M4 基线的数据源链路：
// lsp.request.finished 经会话 EventBus → bridge.handleEvent → appendEventLog，
// 必须以原始标量载荷落盘到 <session>/events/runtime-events.jsonl。
//
// 这条链路此前只有 fixture 级验证（离线脚本/归因包），没有任何测试证明「真实事件
// 会被写入」；一旦 bridge 的 turn-ownership 抑制误伤无 turn 身份的池事件，基线与
// 页签 F 会永远显示 n/a，且不报错。载荷字段（trigger/outcome/duration_ms 等）
// 是 §4.3 归因的字段口径，必须原样保留。
func TestChatRuntimeEventBridge_PersistsLSPRequestFinished(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime-events.jsonl")
	session := &ChatSession{RuntimeSession: &runtimechat.Session{ID: "sess-lsp-persist"}}
	session.LocalRuntimeHost = &localChatRuntimeHost{
		EventBus:    runtimeevents.NewBus(),
		BaseSession: session,
	}
	bridge := newChatRuntimeEventBridge(session)
	bridge.eventLogPathOverride = logPath
	// 无 UI 写入者：silence 掉控制台输出，测试只关心事件日志。
	bridge.writeLine = func(string) {}
	bridge.writeDelta = func(string) {}
	bridge.writeDocument = nil
	bridge.start()
	// 生产形态：轮次进行中且已有 turn 身份。无 turn_id 的会话级观测事件必须在
	// 该形态下落盘——修复前 shouldSuppressMismatchedPrimaryTurnEvent 会把它整批
	// 丢弃（真实运行日志表现为 reason="event turn does not match active run"）。
	bridge.BeginRun()
	bridge.runActive = true
	bridge.activeTurnID = "turn-live"

	session.LocalRuntimeHost.EventBus.Publish(runtimeevents.Event{
		Type:      runtimeevents.EventLSPRequestFinished,
		SessionID: "sess-lsp-persist",
		Payload: map[string]interface{}{
			"trigger":        "inline",
			"outcome":        "injected",
			"duration_ms":    12,
			"diag_count":     1,
			"appended_bytes": 64,
			"server":         "gopls",
		},
	})

	deadline := time.Now().Add(5 * time.Second)
	var raw []byte
	for {
		content, readErr := os.ReadFile(logPath)
		if readErr == nil && strings.Contains(string(content), runtimeevents.EventLSPRequestFinished) {
			raw = content
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("lsp.request.finished 未落盘（err=%v, log=%q）", readErr, string(content))
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, want := range []string{
		`"trigger":"inline"`,
		`"outcome":"injected"`,
		`"duration_ms":12`,
		`"diag_count":1`,
		`"appended_bytes":64`,
		`"server":"gopls"`,
	} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("落盘载荷缺少 %s：%s", want, string(raw))
		}
	}
}

// TestChatRuntimeEventBridge_TurnOwnershipKeepsSuppressingStaleTurnEvents 守卫
// 豁免的边界：LSP 观测事件放行不得削弱轮次归属门——带过期 turn_id 的轮次内事件
// 仍然必须被抑制，避免旧轮工具行污染当前视图。
func TestChatRuntimeEventBridge_TurnOwnershipKeepsSuppressingStaleTurnEvents(t *testing.T) {
	session := &ChatSession{RuntimeSession: &runtimechat.Session{ID: "sess-owner"}}
	bridge := newChatRuntimeEventBridge(session)
	bridge.runStarted = true
	bridge.runActive = true
	bridge.activeTurnID = "turn-1"

	stale := runtimeevents.Event{
		Type:      "tool.completed",
		SessionID: "sess-owner",
		Payload:   map[string]interface{}{"turn_id": "turn-0"},
	}
	if !bridge.shouldSuppressMismatchedPrimaryTurnEvent(stale) {
		t.Fatal("过期轮次的工具事件必须继续被抑制")
	}
	observation := runtimeevents.Event{
		Type:      runtimeevents.EventLSPRequestFinished,
		SessionID: "sess-owner",
	}
	if bridge.shouldSuppressMismatchedPrimaryTurnEvent(observation) {
		t.Fatal("LSP 观测事件不得被轮次归属门丢弃")
	}
}
