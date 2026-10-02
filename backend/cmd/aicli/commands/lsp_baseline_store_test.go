package commands

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/usageanalytics"
)

// seedLSPBaselineTestDB 把 JSONL fixture 灌进临时分析库，并把它设为
// lspBaselineStorePath 的返回���，返回可传给 chatLSPBaselineText /
// chatWebLSPBaselineBodyForDays 的 roots 参数（已无意义，保留是为了不改调用签名）。
//
// 事实源迁到分析库后，测试也必须造库里的数据：继续写 JSONL 再指望命令读到它，
// 测的就不是生产链路了。fixture 仍用 JSONL 文本表达，是为了与
// internal/lsp/baseline 的互锁 fixture 保持同构、可读。
func seedLSPBaselineTestDB(t *testing.T, fixture string) string {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "usage_analytics.sqlite")

	bus := runtimeevents.NewBus()
	service, err := usageanalytics.Attach(bus, usageanalytics.Options{
		Config: usageanalytics.Config{Path: dbPath},
	})
	if err != nil {
		t.Fatalf("attach analytics: %v", err)
	}
	for _, line := range strings.Split(fixture, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var event struct {
			Type      string                 `json:"type"`
			SessionID string                 `json:"session_id"`
			Timestamp time.Time              `json:"timestamp"`
			Payload   map[string]interface{} `json:"payload"`
		}
		if json.Unmarshal([]byte(line), &event) != nil {
			continue // 写坏的行不是数据
		}
		bus.Publish(runtimeevents.Event{
			Type: event.Type, SessionID: event.SessionID,
			Timestamp: event.Timestamp, Payload: event.Payload,
		})
	}
	service.Close()

	original := lspBaselineStorePath
	lspBaselineStorePath = func() string { return dbPath }
	t.Cleanup(func() { lspBaselineStorePath = original })
	return dbPath
}
