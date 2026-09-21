package entity

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// JSONMap is the minimal JSON object representation required by skills usage ledger APIs.
type JSONMap map[string]interface{}

// Scan implements sql.Scanner.
func (j *JSONMap) Scan(value interface{}) error {
	if value == nil {
		*j = JSONMap{}
		return nil
	}

	var raw []byte
	switch typed := value.(type) {
	case []byte:
		raw = typed
	case string:
		raw = []byte(typed)
	default:
		return fmt.Errorf("unsupported JSONMap value type %T", value)
	}

	if len(raw) == 0 {
		*j = JSONMap{}
		return nil
	}

	return json.Unmarshal(raw, j)
}

// Value implements driver.Valuer.
func (j JSONMap) Value() (driver.Value, error) {
	if len(j) == 0 {
		return nil, nil
	}
	return json.Marshal(j)
}

// Time wraps time.Time so the JSON shape matches the gateway ledger responses.
type Time time.Time

// MarshalJSON implements json.Marshaler.
func (t Time) MarshalJSON() ([]byte, error) {
	return []byte(fmt.Sprintf("\"%s\"", time.Time(t).UTC().Format(time.RFC3339Nano))), nil
}

// UnmarshalJSON implements json.Unmarshaler.
func (t *Time) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*t = Time(time.Time{})
		return nil
	}

	trimmed := strings.Trim(string(data), "\"")
	if trimmed == "" {
		*t = Time(time.Time{})
		return nil
	}

	parsed, err := parseEntityTime(trimmed)
	if err != nil {
		return err
	}

	*t = Time(parsed)
	return nil
}

// Scan implements sql.Scanner.
func (t *Time) Scan(value interface{}) error {
	if value == nil {
		*t = Time(time.Time{})
		return nil
	}

	switch typed := value.(type) {
	case time.Time:
		*t = Time(typed)
		return nil
	case []byte:
		parsed, err := parseEntityTime(string(typed))
		if err != nil {
			return err
		}
		*t = Time(parsed)
		return nil
	case string:
		parsed, err := parseEntityTime(typed)
		if err != nil {
			return err
		}
		*t = Time(parsed)
		return nil
	default:
		return fmt.Errorf("cannot scan %T into entity.Time", value)
	}
}

// Value implements driver.Valuer.
func (t Time) Value() (driver.Value, error) {
	return time.Time(t).UTC(), nil
}

// IsZero reports whether the wrapped time is zero.
func (t Time) IsZero() bool {
	return time.Time(t).IsZero()
}

func parseEntityTime(raw string) (time.Time, error) {
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999-07:00",
		"2006-01-02 15:04:05-07:00",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05.999999",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05.999999999",
		"2006-01-02T15:04:05.999999",
		"2006-01-02T15:04:05",
		"2006-01-02",
	}

	trimmed := strings.TrimSpace(raw)
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, trimmed); err == nil {
			return parsed, nil
		}
		if parsed, err := time.ParseInLocation(layout, trimmed, time.UTC); err == nil {
			return parsed, nil
		}
	}

	return time.Time{}, fmt.Errorf("invalid time format: %s", raw)
}

// TokenUsageHistory is the minimal ledger record consumed by the migrated skills handler.
type TokenUsageHistory struct {
	ID           string  `json:"id"`
	RequestID    string  `json:"request_id"`
	ModelID      string  `json:"model_id"`
	ProviderID   string  `json:"provider_id"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	TotalTokens  int     `json:"total_tokens"`
	MessageCount int     `json:"message_count"`
	MaxTokens    int     `json:"max_tokens"`
	Success      bool    `json:"success"`
	StatusCode   int     `json:"status_code"`
	Metadata     JSONMap `json:"metadata,omitempty"`
	CreatedAt    Time    `json:"created_at"`

	// 知识层度量（Phase 0 交付 2，04 §5）。零值表示该请求没有知识层参与，
	// 因此 `mode=off` 时这些字段在 JSON 里不出现（omitempty），响应与改动前
	// 逐字节一致。
	//
	// 语义（04 §7.2 / §7.3）：
	//   ExplorationTokens  花在“找路”上的 token（搜索/列举/重复打开）
	//   ReuseTokens        复用既有结论（索引命中、缓存）省下的 token
	//   IndexLookupCount   查询知识库的次数（分母）
	//   IndexHit           其中命中并可直接使用的次数（分子）
	//   FallbackCount      知识库不可用/未命中而回退到既有工具的调用次数
	//   UnsafeReuseCount   复用 stale 或低置信内容的次数（硬门槛 = 0）
	//   ToolCallsPerTask   该任务的工具调用数（护栏：不得增加）
	//   RepeatedReadCount  同一 session 内对同一 file/symbol 的重复读取次数
	//   KnowledgeVersionMismatchCount 版本不一致却仍被使用的次数（硬门槛 = 0）
	ExplorationTokens             int `json:"exploration_tokens,omitempty"`
	ReuseTokens                   int `json:"reuse_tokens,omitempty"`
	IndexLookupCount              int `json:"index_lookup_count,omitempty"`
	IndexHit                      int `json:"index_hit,omitempty"`
	FallbackCount                 int `json:"fallback_count,omitempty"`
	UnsafeReuseCount              int `json:"unsafe_reuse_count,omitempty"`
	ToolCallsPerTask              int `json:"tool_calls_per_task,omitempty"`
	RepeatedReadCount             int `json:"repeated_read_count,omitempty"`
	KnowledgeVersionMismatchCount int `json:"knowledge_version_mismatch_count,omitempty"`
}

// TableName keeps compatibility with optional SQL-backed implementations.
func (TokenUsageHistory) TableName() string {
	return "token_usage_history"
}

// BeforeCreate mirrors the gateway behavior for stores that choose to call it manually.
func (t *TokenUsageHistory) BeforeCreate() {
	if t.ID == "" {
		t.ID = uuid.NewString()
	}
	if t.CreatedAt.IsZero() {
		t.CreatedAt = Time(time.Now())
	}
	if t.TotalTokens == 0 {
		t.TotalTokens = t.InputTokens + t.OutputTokens
	}
}
