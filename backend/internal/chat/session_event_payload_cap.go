package chat

import (
	"encoding/json"
	"fmt"
	"strings"
)

// session_events 载荷字节级上限（plan P1-2 / H2）。
//
// 既有保留策略只按**条数**裁剪（EventRetention 2048 / MailboxRetention 2048，
// 见 pruneRuntimeRowsTx），挡不住**单条大载荷**：一条 assistant.reasoning /
// assistant_delta 的 payload 可以比整个被裁剪后的日志还大，907 MB 的
// session_runtime.sqlite 里约 75% 字节就是这类流式增量。这里补上写入路径上的
// 字节级约束。
//
// 持久语义：每条 session_events 行的 payload_json 最多
// maxSessionEventPayloadBytes 字节。超限载荷被替换为一个有界 stub，**行本身
// 保留**（session_id / seq / type / trace_id / agent_name / tool_name /
// created_at 全部不变），stub 显式记录 payload_truncated=true、
// payload_bytes（原始 JSON 字节长度）、payload_cap（生效上限）以及一小段头部
// 预览，使回放与审计能把「被裁剪」与「空载荷」区分开，而不是静默丢行或写入
// 伪造的空载荷。
//
// 未做 artifact 指针归档：本包（internal/chat）的写入路径上没有任何
// artifact store 可达（artifact.Store 挂在 SessionActor / output.Gateway 上，
// 不在 runtime store 的依赖里），接进来需要跨包大改造，故按计划采用裁剪 stub
// 这一最小修复。

// DefaultMaxSessionEventPayloadBytes is the default per-event payload byte cap
// applied on every session_events append path.
//
// Durable semantics: it bounds payload_json of one durable event row, so a
// single oversized payload can no longer defeat the row-count retention; an
// over-cap payload is persisted as a bounded marker stub instead of the full
// body (see boundSessionEventPayload).
const DefaultMaxSessionEventPayloadBytes = 256 * 1024

// sessionEventPayloadPreviewBytes bounds the head preview kept inside a stub.
// It is a fixed small constant so the stub never scales with the original
// payload.
const sessionEventPayloadPreviewBytes = 512

// sessionEventPayloadStub is the persisted replacement for an over-cap payload.
// The field names are the durable markers replay/audit code keys on:
// payload_truncated / payload_bytes / payload_cap.
type sessionEventPayloadStub struct {
	Truncated bool   `json:"payload_truncated"`
	Bytes     int    `json:"payload_bytes"`
	Cap       int    `json:"payload_cap"`
	Preview   string `json:"payload_preview,omitempty"`
}

// normalizeMaxSessionEventPayloadBytes maps host configuration onto the cap:
// 0 uses DefaultMaxSessionEventPayloadBytes, a negative value disables the cap
// (legacy unbounded writes), and a positive value is used as-is so tests and
// hosts can lower it.
func normalizeMaxSessionEventPayloadBytes(capBytes int) int {
	switch {
	case capBytes == 0:
		return DefaultMaxSessionEventPayloadBytes
	case capBytes < 0:
		return 0
	default:
		return capBytes
	}
}

// boundSessionEventPayload returns the payload_json bytes to persist for one
// session_events row.
//
// Durable semantics: payloads within the cap are returned verbatim; an over-cap
// payload becomes a bounded stub that keeps the row and its truncation markers,
// so the stored bytes stay bounded without losing the fact that a payload
// existed. A non-positive cap means "no cap" and is returned unchanged.
func boundSessionEventPayload(payloadJSON []byte, capBytes int) []byte {
	if capBytes <= 0 || len(payloadJSON) <= capBytes {
		return payloadJSON
	}
	stub := sessionEventPayloadStub{
		Truncated: true,
		Bytes:     len(payloadJSON),
		Cap:       capBytes,
	}
	if preview := sessionEventPayloadPreview(payloadJSON, capBytes); preview != "" {
		stub.Preview = preview
	}
	encoded, err := json.Marshal(stub)
	if err != nil {
		// Unreachable for this shape (no field can fail to encode), but the cap
		// is the point of this path: never fall back to the unbounded payload.
		return []byte(fmt.Sprintf(`{"payload_truncated":true,"payload_bytes":%d,"payload_cap":%d}`, stub.Bytes, stub.Cap))
	}
	if len(encoded) > capBytes && stub.Preview != "" {
		// A pathological preview (escaping) can exceed a very small cap: the
		// markers are mandatory, the preview is not.
		stub.Preview = ""
		if retry, retryErr := json.Marshal(stub); retryErr == nil {
			encoded = retry
		}
	}
	return encoded
}

// sessionEventPayloadPreview returns a bounded UTF-8-safe head of the original
// payload so a truncated event still shows what kind of payload it carried. The
// budget is a fraction of the cap, so the stub stays well below it.
func sessionEventPayloadPreview(payloadJSON []byte, capBytes int) string {
	budget := capBytes / 8
	if budget > sessionEventPayloadPreviewBytes {
		budget = sessionEventPayloadPreviewBytes
	}
	if budget <= 0 || len(payloadJSON) == 0 {
		return ""
	}
	if budget > len(payloadJSON) {
		budget = len(payloadJSON)
	}
	// ToValidUTF8 also drops a rune that the byte cut left incomplete.
	return strings.ToValidUTF8(string(payloadJSON[:budget]), "")
}

// sessionEventPayloadByteCap resolves the effective cap for this store. It is
// read on the append path, so hosts can lower it per store (tests) without a
// global change.
func (s *SQLiteRuntimeStore) sessionEventPayloadByteCap() int {
	if s == nil {
		return 0
	}
	return normalizeMaxSessionEventPayloadBytes(s.cfg.MaxEventPayloadBytes)
}
