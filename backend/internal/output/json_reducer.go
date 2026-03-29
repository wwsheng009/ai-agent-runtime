package output

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// JSONReducer 压缩通用 JSON 输出，提取稳定结构摘要。
type JSONReducer struct{}

// Name 返回 reducer 名称。
func (r *JSONReducer) Name() string {
	return "json_summary"
}

// Reduce 解析 JSON 对象/数组，输出结构化摘要。
func (r *JSONReducer) Reduce(_ context.Context, input ReducedInput) (*Envelope, bool, error) {
	content := strings.TrimSpace(input.Text)
	if !looksLikeJSON(content) {
		return nil, false, nil
	}

	var payload interface{}
	decoder := json.NewDecoder(strings.NewReader(content))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return nil, false, nil
	}

	summary, metadata := summarizeJSONPayload(payload)
	if summary == "" {
		summary = "Parsed JSON output."
	}

	return &Envelope{
		ToolName:   input.Raw.ToolName,
		ToolCallID: input.Raw.ToolCallID,
		Summary:    summary,
		Error:      strings.TrimSpace(input.Raw.Error),
		Metadata:   metadata,
	}, true, nil
}

func looksLikeJSON(content string) bool {
	if content == "" {
		return false
	}
	switch content[0] {
	case '{', '[':
	default:
		return false
	}
	return json.Valid([]byte(content))
}

func summarizeJSONPayload(payload interface{}) (string, map[string]interface{}) {
	switch typed := payload.(type) {
	case map[string]interface{}:
		keys := mapKeys(typed)
		summary := fmt.Sprintf("Parsed JSON object with %d keys.", len(keys))
		fields := collectJSONFacts(typed)
		if len(fields) > 0 {
			summary += "\nFields: " + strings.Join(fields, "; ")
		}
		if len(keys) > 0 {
			summary += "\nKeys: " + strings.Join(limitStrings(keys, 8), ", ")
		}

		metadata := map[string]interface{}{
			"json_type": "object",
			"key_count": len(keys),
			"keys":      limitStrings(keys, 8),
		}
		if nestedKey, nestedCount, ok := detectJSONCollection(typed); ok {
			metadata["collection_key"] = nestedKey
			metadata["collection_count"] = nestedCount
		}
		return summary, metadata
	case []interface{}:
		summary := fmt.Sprintf("Parsed JSON array with %d items.", len(typed))
		itemFields := summarizeJSONArrayShape(typed)
		if len(itemFields) > 0 {
			summary += "\nItem fields: " + strings.Join(itemFields, ", ")
		}
		samples := summarizeJSONArraySamples(typed, 3)
		if len(samples) > 0 {
			summary += "\nSample items: " + strings.Join(samples, " | ")
		}
		return summary, map[string]interface{}{
			"json_type":   "array",
			"item_count":  len(typed),
			"item_fields": limitStrings(itemFields, 8),
		}
	default:
		value := summarizeLine(fmt.Sprint(typed), 220)
		if value == "" {
			value = "<empty>"
		}
		return fmt.Sprintf("Parsed JSON scalar: %s", value), map[string]interface{}{
			"json_type": "scalar",
		}
	}
}

func collectJSONFacts(payload map[string]interface{}) []string {
	orderedKeys := []string{
		"team_id", "task_id", "message_count", "from_agent", "to_agent",
		"answer", "question_id", "prompt",
		"status", "success", "message", "error", "summary",
		"count", "total", "id", "name", "type", "path",
		"rows", "items", "results", "files", "warnings",
	}

	facts := make([]string, 0, 6)
	for _, key := range orderedKeys {
		value, ok := payload[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case string:
			if text := summarizeLine(typed, 80); text != "" {
				facts = append(facts, fmt.Sprintf("%s=%s", key, text))
			}
		case bool, json.Number, float64, float32, int, int32, int64:
			facts = append(facts, fmt.Sprintf("%s=%v", key, typed))
		case []interface{}:
			facts = append(facts, fmt.Sprintf("%s[%d]", key, len(typed)))
		case map[string]interface{}:
			facts = append(facts, fmt.Sprintf("%s{%d keys}", key, len(typed)))
		}
		if len(facts) >= 6 {
			break
		}
	}

	return facts
}

func detectJSONCollection(payload map[string]interface{}) (string, int, bool) {
	for _, key := range []string{"items", "results", "rows", "files", "data", "tests"} {
		value, ok := payload[key]
		if !ok {
			continue
		}
		items, ok := value.([]interface{})
		if !ok {
			continue
		}
		return key, len(items), true
	}
	return "", 0, false
}

func summarizeJSONArrayShape(items []interface{}) []string {
	fieldCounts := make(map[string]int)
	objectCount := 0
	for _, item := range items {
		object, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		objectCount++
		for key := range object {
			fieldCounts[key]++
		}
	}
	if objectCount == 0 {
		return nil
	}

	keys := make([]string, 0, len(fieldCounts))
	for key, count := range fieldCounts {
		if count == objectCount {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	if len(keys) > 0 {
		return keys
	}

	for key := range fieldCounts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return limitStrings(keys, 8)
}

func summarizeJSONArraySamples(items []interface{}, limit int) []string {
	samples := make([]string, 0, limit)
	for _, item := range items {
		if len(samples) >= limit {
			break
		}
		switch typed := item.(type) {
		case map[string]interface{}:
			parts := collectJSONFacts(typed)
			if len(parts) == 0 {
				keys := mapKeys(typed)
				if len(keys) == 0 {
					continue
				}
				samples = append(samples, "keys="+strings.Join(limitStrings(keys, 4), ", "))
				continue
			}
			samples = append(samples, strings.Join(parts, "; "))
		case []interface{}:
			samples = append(samples, fmt.Sprintf("array[%d]", len(typed)))
		default:
			if text := summarizeLine(fmt.Sprint(typed), 80); text != "" {
				samples = append(samples, text)
			}
		}
	}
	return samples
}

func mapKeys(payload map[string]interface{}) []string {
	keys := make([]string, 0, len(payload))
	for key := range payload {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
