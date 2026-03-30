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
