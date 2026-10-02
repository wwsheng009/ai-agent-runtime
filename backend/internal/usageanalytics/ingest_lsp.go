package usageanalytics

import (
	"encoding/json"
	"strings"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// LSP 观测事件的采集（§4.3 基线的事实源）。
//
// 为什么走库而不是继续读 chat-logs 日志：
//
//  1. 日志有保留策略，会清理老会话。以日志为事实源，基线数字会随轮转无声退化
//     （docs/analysis/nondb-error-deep-analysis-and-optimization-20260927.md 记录
//     184 个采样会话里 146 个文件已被清理），而 UI 不会提示覆盖率不足。
//  2. lsp.server.state 被 contract.go 声明为 ChannelLiveOnly，落盘面确实已经
//     把它挡掉（实测今天的日志里 0 条），所以冷启动指标只靠日志永远补不回来。
//
// 关键点：collector 订阅的是**实时总线**（bus.SubscribeCancelable），与事件是否
// 落盘无关。因此这里既不需要改通道契约，也不会让每会话 JSONL 体积增长。
// 「通道 = 落盘体积」与「入库 = 分析可复算性」是正交的两件事，此前被混为一谈。
//
// 采集幂等：重复投递按自然键 upsert，不重复计数。

// onLSPRequestFinished 采集一次 LSP 后写请求。
func (c *collector) onLSPRequestFinished(event runtimeevents.Event) {
	if c == nil || c.store == nil {
		return
	}
	payload := event.Payload
	sessionID := strings.TrimSpace(firstNonEmpty(payloadString(payload, "session_id"), event.SessionID))
	at := event.Timestamp
	if at.IsZero() {
		at = c.now()
	}
	server := payloadString(payload, "server")
	pathFingerprint := payloadString(payload, "path_fingerprint")

	// cold_probe_classified 记的是"这个键存在过"，不是它的值。
	//
	// 这是 eventbridge/observer.go:57-60 特意保住的一个语义：冷探针事件必须显式
	// 携带 cold_fast_fail（true=重复探针，false=首探针），而 Go 的 map 不区分
	// "false" 与"未设置"。若只存布尔值，首探针(false)与老构建的缺字段样本会被
	// 混为一谈。所以这里记"键是否出现"，不记布尔值本身。
	coldProbeClassified := 0
	coldFastFail := 0
	if raw, exists := payload["cold_fast_fail"]; exists {
		coldProbeClassified = 1
		if flag, ok := raw.(bool); ok && flag {
			coldFastFail = 1
		}
	}

	var record []byte
	if encoded, err := json.Marshal(payload); err == nil {
		record = encoded
	}
	if err := c.store.execWithLockRetry(`
INSERT INTO usage_lsp_requests (
  session_id, path_fingerprint, server, started_unix_nano, trigger, outcome,
  duration_ms, diag_count, total_diag_count, new_diag_count,
  appended_bytes, appended_diag_bytes, appended_note_bytes, appended_empty_bytes,
  omitted_items, omitted_by_chars, attempted_members,
  cold_probe_classified, cold_fast_fail,
  tool_call_id, turn_id, reason_category, diag_fingerprint, record_json
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(session_id, path_fingerprint, server, started_unix_nano) DO UPDATE SET
  trigger = CASE WHEN excluded.trigger <> '' THEN excluded.trigger ELSE usage_lsp_requests.trigger END,
  outcome = CASE WHEN excluded.outcome <> '' THEN excluded.outcome ELSE usage_lsp_requests.outcome END,
  duration_ms = CASE WHEN excluded.duration_ms > 0 THEN excluded.duration_ms ELSE usage_lsp_requests.duration_ms END,
  diag_count = MAX(usage_lsp_requests.diag_count, excluded.diag_count),
  total_diag_count = MAX(usage_lsp_requests.total_diag_count, excluded.total_diag_count),
  new_diag_count = MAX(usage_lsp_requests.new_diag_count, excluded.new_diag_count),
  appended_bytes = MAX(usage_lsp_requests.appended_bytes, excluded.appended_bytes),
  appended_diag_bytes = MAX(usage_lsp_requests.appended_diag_bytes, excluded.appended_diag_bytes),
  appended_note_bytes = MAX(usage_lsp_requests.appended_note_bytes, excluded.appended_note_bytes),
  appended_empty_bytes = MAX(usage_lsp_requests.appended_empty_bytes, excluded.appended_empty_bytes),
  omitted_items = MAX(usage_lsp_requests.omitted_items, excluded.omitted_items),
  omitted_by_chars = MAX(usage_lsp_requests.omitted_by_chars, excluded.omitted_by_chars),
  attempted_members = MAX(usage_lsp_requests.attempted_members, excluded.attempted_members),
  cold_probe_classified = MAX(usage_lsp_requests.cold_probe_classified, excluded.cold_probe_classified),
  cold_fast_fail = MAX(usage_lsp_requests.cold_fast_fail, excluded.cold_fast_fail),
  tool_call_id = CASE WHEN excluded.tool_call_id <> '' THEN excluded.tool_call_id ELSE usage_lsp_requests.tool_call_id END,
  turn_id = CASE WHEN excluded.turn_id <> '' THEN excluded.turn_id ELSE usage_lsp_requests.turn_id END,
  reason_category = CASE WHEN excluded.reason_category <> '' THEN excluded.reason_category ELSE usage_lsp_requests.reason_category END,
  diag_fingerprint = CASE WHEN excluded.diag_fingerprint <> '' THEN excluded.diag_fingerprint ELSE usage_lsp_requests.diag_fingerprint END,
  record_json = CASE WHEN excluded.record_json IS NOT NULL AND length(excluded.record_json) > 0
                     THEN excluded.record_json ELSE usage_lsp_requests.record_json END`,
		sessionID,
		pathFingerprint,
		server,
		at.UnixNano(),
		payloadString(payload, "trigger"),
		payloadString(payload, "outcome"),
		payloadInt(payload, "duration_ms"),
		payloadInt(payload, "diag_count"),
		payloadInt(payload, "total_diag_count"),
		payloadInt(payload, "new_diag_count"),
		payloadInt(payload, "appended_bytes"),
		payloadInt(payload, "appended_diag_bytes"),
		payloadInt(payload, "appended_note_bytes"),
		payloadInt(payload, "appended_empty_bytes"),
		payloadInt(payload, "omitted_items"),
		payloadInt(payload, "omitted_by_chars"),
		payloadInt(payload, "attempted_members"),
		coldProbeClassified,
		coldFastFail,
		payloadString(payload, "tool_call_id"),
		payloadString(payload, "turn_id"),
		payloadString(payload, "reason_category"),
		payloadString(payload, "diag_fingerprint"),
		record,
	); err != nil {
		c.reportWriteFailure("usage_lsp_requests", err)
	}
}

// onLSPServerState 采集池成员的**首个发布延迟**（冷启动观测）。
//
// 只取 first_publish_ms > 0 的事件：那是"启动→首个诊断发布"的一次性观测，
// 其余状态迁移（starting/ready/...）对基线无贡献，不入库以免表按生命周期噪声增长。
// 每个 (session, server) 只保留最早的一次观测 —— 基线口径就是"取首个发布"。
func (c *collector) onLSPServerState(event runtimeevents.Event) {
	if c == nil || c.store == nil {
		return
	}
	payload := event.Payload
	firstPublishMS := payloadInt(payload, "first_publish_ms")
	if firstPublishMS <= 0 {
		return
	}
	sessionID := strings.TrimSpace(firstNonEmpty(payloadString(payload, "session_id"), event.SessionID))
	server := payloadString(payload, "server")
	if sessionID == "" || server == "" {
		return
	}
	at := event.Timestamp
	if at.IsZero() {
		at = c.now()
	}
	if err := c.store.execWithLockRetry(`
INSERT INTO usage_lsp_first_publish (
  session_id, server, first_publish_ms, observed_unix_nano
) VALUES (?,?,?,?)
ON CONFLICT(session_id, server) DO UPDATE SET
  first_publish_ms = CASE
    WHEN excluded.observed_unix_nano < usage_lsp_first_publish.observed_unix_nano
      THEN excluded.first_publish_ms
    ELSE usage_lsp_first_publish.first_publish_ms END,
  observed_unix_nano = MIN(usage_lsp_first_publish.observed_unix_nano, excluded.observed_unix_nano)`,
		sessionID, server, firstPublishMS, at.UnixNano(),
	); err != nil {
		c.reportWriteFailure("usage_lsp_first_publish", err)
	}
}
