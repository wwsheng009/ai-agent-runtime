package usageanalytics

import (
	"encoding/json"
	"strings"
	"time"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// 渲染围栏拒绝类别：与 CLI 侧显式 RunState（idle/running/closed）对齐，
// 载荷与落库共用同一组字面量。
const (
	renderFenceDropClassIdle           = "idle"
	renderFenceDropClassClosed         = "closed"
	renderFenceDropClassActiveMismatch = "active_mismatch"
)

// onRenderFenceDropped 采集渲染围栏的 late action 拒绝计数（P1-1b）。
//
// 这些拒绝不进入 agent 错误码体系，因此按类别落进独立诊断表，由 ErrorPatterns
// 以 source="fence" 呈现；重复投递按 (session_id, turn_id, dropped_class) 幂等累加。
func (c *collector) onRenderFenceDropped(event runtimeevents.Event) {
	if c == nil || c.store == nil {
		return
	}
	payload := event.Payload
	sessionID := strings.TrimSpace(firstNonEmpty(payloadString(payload, "session_id"), event.SessionID))
	turnID := strings.TrimSpace(payloadString(payload, "turn_id"))
	reason := strings.TrimSpace(payloadString(payload, "last_reason"))
	at := event.Timestamp
	if at.IsZero() {
		at = time.Now().UTC()
	}
	classes := []struct {
		class string
		count int
	}{
		{renderFenceDropClassIdle, payloadInt(payload, "idle")},
		{renderFenceDropClassClosed, payloadInt(payload, "closed")},
		{renderFenceDropClassActiveMismatch, payloadInt(payload, "active_mismatch")},
	}
	for _, item := range classes {
		if item.count <= 0 {
			continue
		}
		var record []byte
		if encoded, err := json.Marshal(map[string]interface{}{
			"session_id":    sessionID,
			"turn_id":       turnID,
			"dropped_class": item.class,
			"count":         item.count,
			"reason":        reason,
		}); err == nil {
			record = encoded
		}
		if err := c.store.execWithLockRetry(`
INSERT INTO usage_render_fence_drops (
  session_id, turn_id, dropped_class, reason, count, first_at_unix_nano, last_at_unix_nano, record_json
) VALUES (?,?,?,?,?,?,?,?)
ON CONFLICT(session_id, turn_id, dropped_class) DO UPDATE SET
  count = usage_render_fence_drops.count + excluded.count,
  reason = CASE WHEN excluded.reason <> '' THEN excluded.reason ELSE usage_render_fence_drops.reason END,
  last_at_unix_nano = MAX(usage_render_fence_drops.last_at_unix_nano, excluded.last_at_unix_nano),
  record_json = CASE WHEN excluded.record_json IS NOT NULL AND length(excluded.record_json) > 0
                     THEN excluded.record_json ELSE usage_render_fence_drops.record_json END`,
			sessionID, turnID, item.class, reason,
			item.count, at.UnixNano(), at.UnixNano(), record); err != nil {
			c.reportWriteFailure("usage_render_fence_drops", err)
		}
	}
}
