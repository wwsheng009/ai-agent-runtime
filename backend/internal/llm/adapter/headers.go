package adapter

import "strings"

const anthropicInterleavedThinkingBeta = "interleaved-thinking-2025-05-14"

func mergeHeaderMaps(base, overrides map[string]string) map[string]string {
	result := cloneHeaderMap(base)
	for key, value := range overrides {
		setHeaderValueCaseInsensitive(result, key, value)
	}
	return result
}

func cloneHeaderMap(headers map[string]string) map[string]string {
	if len(headers) == 0 {
		return map[string]string{}
	}

	cloned := make(map[string]string, len(headers))
	for key, value := range headers {
		cloned[key] = value
	}
	return cloned
}

func getHeaderValueCaseInsensitive(headers map[string]string, key string) string {
	for existingKey, value := range headers {
		if strings.EqualFold(existingKey, key) {
			return value
		}
	}
	return ""
}

func setHeaderValueCaseInsensitive(headers map[string]string, key, value string) {
	for existingKey := range headers {
		if strings.EqualFold(existingKey, key) {
			headers[existingKey] = value
			return
		}
	}
	headers[key] = value
}

func mergeCommaSeparatedHeaderValue(existing, addition string) string {
	addition = strings.TrimSpace(addition)
	if addition == "" {
		return strings.TrimSpace(existing)
	}

	seen := make(map[string]string)
	order := make([]string, 0)

	addValue := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = value
		order = append(order, value)
	}

	for _, part := range strings.Split(existing, ",") {
		addValue(part)
	}
	addValue(addition)

	return strings.Join(order, ", ")
}
