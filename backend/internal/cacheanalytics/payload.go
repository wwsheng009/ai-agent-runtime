package cacheanalytics

import (
	"strconv"
	"strings"
	"time"
)

// 载荷字段容错提取辅助：runtime 事件载荷为 map[string]interface{}，字段类型
// 可能是 int/int64/float64/json.Number/string（不同发布点/序列化路径差异）。
// 缺失字段一律返回零值而非报错（§13 R2：字段容错）。

func payloadString(payload map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if payload == nil {
			break
		}
		raw, ok := payload[key]
		if !ok || raw == nil {
			continue
		}
		switch value := raw.(type) {
		case string:
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed
			}
		case []byte:
			if trimmed := strings.TrimSpace(string(value)); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

func payloadInt(payload map[string]interface{}, keys ...string) int {
	value, ok := payloadInt64(payload, keys...)
	if !ok {
		return 0
	}
	return int(value)
}

func payloadInt64(payload map[string]interface{}, keys ...string) (int64, bool) {
	if payload == nil {
		return 0, false
	}
	for _, key := range keys {
		raw, ok := payload[key]
		if !ok || raw == nil {
			continue
		}
		switch value := raw.(type) {
		case int:
			return int64(value), true
		case int32:
			return int64(value), true
		case int64:
			return value, true
		case uint:
			return int64(value), true
		case uint64:
			return int64(value), true
		case float64:
			return int64(value), true
		case json_Number:
			if parsed, err := value.Int64(); err == nil {
				return parsed, true
			}
		case string:
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				if parsed, err := strconv.ParseInt(trimmed, 10, 64); err == nil {
					return parsed, true
				}
			}
		case bool:
			if value {
				return 1, true
			}
			return 0, true
		}
	}
	return 0, false
}

func payloadFloat(payload map[string]interface{}, keys ...string) (float64, bool) {
	if payload == nil {
		return 0, false
	}
	for _, key := range keys {
		raw, ok := payload[key]
		if !ok || raw == nil {
			continue
		}
		switch value := raw.(type) {
		case float64:
			return value, true
		case float32:
			return float64(value), true
		case int:
			return float64(value), true
		case int64:
			return float64(value), true
		case json_Number:
			if parsed, err := value.Float64(); err == nil {
				return parsed, true
			}
		case string:
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				if parsed, err := strconv.ParseFloat(trimmed, 64); err == nil {
					return parsed, true
				}
			}
		}
	}
	return 0, false
}

func payloadBool(payload map[string]interface{}, keys ...string) bool {
	if payload == nil {
		return false
	}
	for _, key := range keys {
		raw, ok := payload[key]
		if !ok || raw == nil {
			continue
		}
		switch value := raw.(type) {
		case bool:
			return value
		case string:
			trimmed := strings.TrimSpace(value)
			if trimmed == "" {
				continue
			}
			if parsed, err := strconv.ParseBool(trimmed); err == nil {
				return parsed
			}
		case float64:
			return value != 0
		}
	}
	return false
}

// payloadTime 解析 RFC3339 / RFC3339Nano 时间字段；失败返回零值。
func payloadTime(payload map[string]interface{}, keys ...string) time.Time {
	raw := payloadString(payload, keys...)
	if raw == "" {
		return time.Time{}
	}
	if parsed, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return parsed
	}
	if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
		return parsed
	}
	return time.Time{}
}

// json_Number 避免直接依赖 encoding/json 的最小接口（json.Number 满足）。
type json_Number interface {
	Int64() (int64, error)
	Float64() (float64, error)
}
