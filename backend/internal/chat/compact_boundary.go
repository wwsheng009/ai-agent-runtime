package chat

import (
	"strings"

	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// compactBoundaryContext 一次性携带恢复端需要的 checkpoint 边界事实。所有整数字段
// 用 int 落库（JSON 读回为 float64，恢复端按数值收敛读取，见 CLI 侧
// resumeCompactMetadataInt 的同类处理）。
type compactBoundaryContext struct {
	// MessageID 是 checkpoint 摘要消息的持久化身份（message_id）。恢复端用它
	// 在投影里精确定位摘要行；投影缺失时只依据 segment_end 计数切片。
	MessageID string `json:"message_id,omitempty"`
	// ReplacementTailMessageID 是 checkpoint 元数据里的替换尾部锚点
	// （replacement_tail_message_id）：锚点在摘要之后时，compact 视图从其后继
	// 消息开始展示。
	ReplacementTailMessageID string `json:"replacement_tail_message_id,omitempty"`
	// SegmentStart / SegmentEnd 与 checkpoint 元数据同源：被摘要覆盖的 canonical
	// 消息区间（非 system 计数口径）。恢复端在无锚点时用 canonical 总数 -
	// segment_end 估算 compact 之后的新增消息数。
	SegmentStart int `json:"segment_start,omitempty"`
	SegmentEnd   int `json:"segment_end,omitempty"`
}

// CompactBoundary 是 ContextCompactBoundary 对恢复端的导出形态（与
// compactBoundaryContext 字段一致）。
type CompactBoundary = compactBoundaryContext

// DecodeCompactBoundaryValue 把 JSON 反序列化出来的 context 值收敛为
// CompactBoundary（数字可能是 float64/int64；字符串键不变）。坏值一律返回
// false，恢复端按「无兜底边界」处理。
func DecodeCompactBoundaryValue(value interface{}) (CompactBoundary, bool) {
	switch typed := value.(type) {
	case CompactBoundary:
		if typed == (CompactBoundary{}) {
			return CompactBoundary{}, false
		}
		return typed, true
	case map[string]interface{}:
		boundary := CompactBoundary{
			MessageID:                looseString(typed["message_id"]),
			ReplacementTailMessageID: looseString(typed["replacement_tail_message_id"]),
			SegmentStart:             metadataLooseInt(typed["segment_start"]),
			SegmentEnd:               metadataLooseInt(typed["segment_end"]),
		}
		if boundary == (CompactBoundary{}) {
			return CompactBoundary{}, false
		}
		return boundary, true
	default:
		return CompactBoundary{}, false
	}
}

// looseString 收敛 context map 里的字符串值（JSON 读回恒为 string，防御性
// 容忍断言失败）。
func looseString(value interface{}) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

// compactBoundaryValue 从 checkpoint 消息元数据提取边界快照（读侧宽容：JSON
// 反序列化后数字可能是 float64/int64 等类型，统一收敛为 int）。
func compactBoundaryValue(message runtimetypes.Message) (compactBoundaryContext, bool) {
	boundary := compactBoundaryContext{
		MessageID:                strings.TrimSpace(message.Metadata.GetString("message_id", "")),
		ReplacementTailMessageID: strings.TrimSpace(message.Metadata.GetString("replacement_tail_message_id", "")),
		SegmentStart:             metadataLooseInt(message.Metadata["segment_start"]),
		SegmentEnd:               metadataLooseInt(message.Metadata["segment_end"]),
	}
	if boundary.MessageID == "" && boundary.ReplacementTailMessageID == "" && boundary.SegmentEnd <= 0 {
		return compactBoundaryContext{}, false
	}
	return boundary, true
}

// metadataLooseInt 收敛 JSON 反序列化可能出现的数值类型（存侧写 int，读侧可能
// 是 float64）。与 commands 包的 resumeCompactMetadataInt 同语义，但放在 chat
// 包以便存储层复用。
func metadataLooseInt(value interface{}) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int8:
		return int(typed)
	case int16:
		return int(typed)
	case int32:
		return int(typed)
	case int64:
		return int(typed)
	case uint:
		return int(typed)
	case uint8:
		return int(typed)
	case uint16:
		return int(typed)
	case uint32:
		return int(typed)
	case uint64:
		return int(typed)
	case float32:
		return int(typed)
	case float64:
		return int(typed)
	default:
		return 0
	}
}

// latestCompactBoundaryContext 扫描装载的 history（热投影或 canonical 皆可），
// 返回最近一次 compaction checkpoint 的边界快照。
func latestCompactBoundaryContext(history []runtimetypes.Message) (compactBoundaryContext, bool) {
	for index := len(history) - 1; index >= 0; index-- {
		message := history[index]
		if strings.TrimSpace(message.Role) == "system" {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(message.Metadata.GetString("context_stage", "")), "compaction") {
			continue
		}
		if boundary, ok := compactBoundaryValue(message); ok {
			return boundary, true
		}
		return compactBoundaryContext{}, false
	}
	return compactBoundaryContext{}, false
}

// syncCompactBoundaryContext 把最近一次 compaction checkpoint 镜像进会话上下文
// （ContextCompactBoundary）。调用约定：
//   - history 未装载（HistoryLoaded=false 的元数据壳写入）时不动该键：壳快照
//     携带的旧 boundary 可能落后于存储里的最新投影，清掉等于误删权威边界；
//   - 装载了 history 却找不到 checkpoint（compaction 边界已不存在，例如
//     backtrack 回退到 checkpoint 之前）时清除该键，防止恢复端用过期边界。
//
// 该函数只修改 session.Metadata.Context；metadata_json 的整体 upsert 由调用方
// 的写事务负责。
func syncCompactBoundaryContext(session *Session) {
	if session == nil || strings.TrimSpace(session.ID) == "" {
		return
	}
	if !session.HistoryLoaded {
		return
	}
	if boundary, ok := latestCompactBoundaryContext(session.History); ok {
		session.SetContext(ContextCompactBoundary, boundary)
		return
	}
	if session.Metadata.Context != nil {
		delete(session.Metadata.Context, ContextCompactBoundary)
	}
}
