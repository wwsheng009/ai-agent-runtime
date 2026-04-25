package adapter

import (
	"encoding/json"
	"strconv"
	"strings"
)

func applyOpenAICompatibleRequestMetadata(request map[string]interface{}, config RequestConfig) {
	if len(config.Metadata) == 0 {
		return
	}

	if responseFormat, ok := openAICompatibleMetadataObject(config.Metadata, "response_format"); ok {
		request["response_format"] = responseFormat
	}
	if streamOptions, ok := openAICompatibleMetadataObject(config.Metadata, "stream_options"); ok && config.Stream {
		request["stream_options"] = streamOptions
	}
	if stop, ok := openAICompatibleMetadataStopValue(config.Metadata["stop"]); ok {
		request["stop"] = stop
	}
	if topP, ok := openAICompatibleMetadataNumber(config.Metadata, "top_p"); ok {
		request["top_p"] = topP
	}
	if frequencyPenalty, ok := openAICompatibleMetadataNumber(config.Metadata, "frequency_penalty"); ok {
		request["frequency_penalty"] = frequencyPenalty
	}
	if presencePenalty, ok := openAICompatibleMetadataNumber(config.Metadata, "presence_penalty"); ok {
		request["presence_penalty"] = presencePenalty
	}
	if toolChoice, ok := openAICompatibleMetadataValue(config.Metadata, "tool_choice"); ok {
		request["tool_choice"] = toolChoice
	}
	if thinking, ok := openAICompatibleMetadataObject(config.Metadata, "thinking"); ok {
		request["thinking"] = thinking
	}
	if extraBody, ok := openAICompatibleMetadataObject(config.Metadata, "extra_body"); ok {
		mergeOpenAICompatibleExtraBody(request, extraBody)
	}
}

func mergeOpenAICompatibleExtraBody(request map[string]interface{}, extraBody map[string]interface{}) {
	for key, value := range extraBody {
		if _, exists := request[key]; exists {
			continue
		}
		request[key] = value
	}
}

func openAICompatibleMetadataObject(metadata map[string]interface{}, key string) (map[string]interface{}, bool) {
	value, ok := openAICompatibleMetadataValue(metadata, key)
	if !ok {
		return nil, false
	}
	object, ok := value.(map[string]interface{})
	if !ok || len(object) == 0 {
		return nil, false
	}
	return object, true
}

func openAICompatibleMetadataValue(metadata map[string]interface{}, key string) (interface{}, bool) {
	if len(metadata) == 0 {
		return nil, false
	}
	raw, ok := metadata[key]
	if !ok || raw == nil {
		return nil, false
	}
	return normalizeOpenAICompatibleMetadataValue(raw)
}

func normalizeOpenAICompatibleMetadataValue(raw interface{}) (interface{}, bool) {
	switch value := raw.(type) {
	case nil:
		return nil, false
	case string:
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return nil, false
		}
		if decoded, ok := decodeOpenAICompatibleJSONText(trimmed); ok {
			return decoded, true
		}
		return trimmed, true
	case []byte:
		return decodeOpenAICompatibleJSONBytes(value)
	case json.RawMessage:
		return decodeOpenAICompatibleJSONBytes([]byte(value))
	case map[string]interface{}, []interface{}, []string, bool, float64, float32, int, int32, int64, uint, uint32, uint64:
		return canonicalizeOpenAICompatibleValue(value)
	default:
		return canonicalizeOpenAICompatibleValue(value)
	}
}

func decodeOpenAICompatibleJSONText(raw string) (interface{}, bool) {
	return decodeOpenAICompatibleJSONBytes([]byte(raw))
}

func decodeOpenAICompatibleJSONBytes(raw []byte) (interface{}, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	var decoded interface{}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, false
	}
	return decoded, true
}

func canonicalizeOpenAICompatibleValue(raw interface{}) (interface{}, bool) {
	payload, err := json.Marshal(raw)
	if err != nil || len(payload) == 0 {
		return nil, false
	}
	var decoded interface{}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return nil, false
	}
	return decoded, true
}

func openAICompatibleMetadataNumber(metadata map[string]interface{}, key string) (float64, bool) {
	if len(metadata) == 0 {
		return 0, false
	}
	raw, ok := metadata[key]
	if !ok || raw == nil {
		return 0, false
	}

	switch value := raw.(type) {
	case float64:
		return value, true
	case float32:
		return float64(value), true
	case int:
		return float64(value), true
	case int32:
		return float64(value), true
	case int64:
		return float64(value), true
	case uint:
		return float64(value), true
	case uint32:
		return float64(value), true
	case uint64:
		return float64(value), true
	case json.Number:
		if parsed, err := value.Float64(); err == nil {
			return parsed, true
		}
	case string:
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return 0, false
		}
		if parsed, err := strconv.ParseFloat(trimmed, 64); err == nil {
			return parsed, true
		}
	}

	return 0, false
}

func openAICompatibleMetadataStopValue(raw interface{}) (interface{}, bool) {
	switch value := raw.(type) {
	case nil:
		return nil, false
	case string:
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return nil, false
		}
		if decoded, ok := decodeOpenAICompatibleJSONText(trimmed); ok {
			return openAICompatibleMetadataStopValue(decoded)
		}
		return trimmed, true
	case []string:
		if len(value) == 0 {
			return nil, false
		}
		return append([]string(nil), value...), true
	case []interface{}:
		if len(value) == 0 {
			return nil, false
		}
		stops := make([]string, 0, len(value))
		for _, item := range value {
			text, ok := item.(string)
			if !ok || strings.TrimSpace(text) == "" {
				return nil, false
			}
			stops = append(stops, text)
		}
		if len(stops) == 0 {
			return nil, false
		}
		return stops, true
	default:
		if normalized, ok := normalizeOpenAICompatibleMetadataValue(value); ok {
			return openAICompatibleMetadataStopValue(normalized)
		}
		return nil, false
	}
}
