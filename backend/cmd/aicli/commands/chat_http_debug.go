package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	runtimellm "github.com/wwsheng009/ai-agent-runtime/internal/llm"
)

const aicliRuntimeHTTPDebugBodyLimit = 32768

func newRuntimeHTTPDebugReporter(session *ChatSession) runtimellm.HTTPDebugReporter {
	if session == nil {
		return nil
	}
	return func(event runtimellm.HTTPDebugEvent) {
		if session.runtimeHTTPCapture != nil {
			session.runtimeHTTPCapture.SetArtifactDir(currentRuntimeHTTPArtifactDir(session))
			session.runtimeHTTPCapture.Record(event)
		}
		if _, err := writeRuntimeHTTPArtifact(session, event); err != nil {
			fmt.Fprintf(os.Stderr, "[runtime HTTP artifact 写入失败] %v\n", err)
		}
		if session.HTTPDebug && session.Logger != nil && session.Logger.logDir != "" {
			if err := session.Logger.WriteDebugInfo(session.Logger.logDir, formatRuntimeHTTPDebugEvent(event)); err != nil {
				fmt.Fprintf(os.Stderr, "[调试日志写入失败] %v\n", err)
			}
		}
	}
}

func formatRuntimeHTTPDebugEvent(event runtimellm.HTTPDebugEvent) string {
	var lines []string

	meta := []string{}
	if value := strings.TrimSpace(event.Source); value != "" {
		meta = append(meta, "source="+value)
	}
	if value := strings.TrimSpace(event.Phase); value != "" {
		meta = append(meta, "phase="+value)
	}
	if value := strings.TrimSpace(event.Provider); value != "" {
		meta = append(meta, "provider="+value)
	}
	if value := strings.TrimSpace(event.Protocol); value != "" {
		meta = append(meta, "protocol="+value)
	}
	if value := strings.TrimSpace(event.Model); value != "" {
		meta = append(meta, "model="+value)
	}
	if len(meta) > 0 {
		lines = append(lines, "[http-debug/runtime] "+strings.Join(meta, " "))
	}

	if method := strings.TrimSpace(event.Method); method != "" || strings.TrimSpace(event.URL) != "" {
		lines = append(lines, fmt.Sprintf("[http-debug/runtime] %s %s", firstNonEmptyChatValue(method, "?"), strings.TrimSpace(event.URL)))
	}

	if event.RequestBodyBytes > 0 {
		lines = append(lines, fmt.Sprintf("[http-debug/runtime] request_body_bytes=%d", event.RequestBodyBytes))
	}
	if debug := runtimellm.HTTPDebugRequestDiagnostics(event.RequestMetadata); len(debug) > 0 {
		if line := formatRuntimeHTTPFingerprintLine(debug); line != "" {
			lines = append(lines, "[http-debug/runtime] "+line)
		}
		if line := formatRuntimeHTTPShapeLine(debug); line != "" {
			lines = append(lines, "[http-debug/runtime] "+line)
		}
	}
	if metadata := compactRuntimeHTTPDebugMetadata(event.RequestMetadata); metadata != "" {
		lines = append(lines, fmt.Sprintf("[http-debug/runtime] request_metadata=%s", truncateUTF8Bytes(metadata, 4096)))
	}
	if body := strings.TrimSpace(event.RequestBody); body != "" {
		lines = append(lines, fmt.Sprintf("[http-debug/runtime] request_body=%s", truncateUTF8Bytes(body, aicliRuntimeHTTPDebugBodyLimit)))
	}

	if event.ResponseStatusCode > 0 || event.ResponseBodyBytes > 0 || strings.TrimSpace(event.Error) != "" {
		line := fmt.Sprintf("[http-debug/runtime] response_status=%d response_body_bytes=%d", event.ResponseStatusCode, event.ResponseBodyBytes)
		if errText := strings.TrimSpace(event.Error); errText != "" {
			line += fmt.Sprintf(" error=%q", errText)
		}
		lines = append(lines, line)
	}
	if preview := strings.TrimSpace(event.ResponseBodyPreview); preview != "" {
		lines = append(lines, fmt.Sprintf("[http-debug/runtime] response_body_preview=%s", truncateUTF8Bytes(preview, 4096)))
	}

	if len(lines) == 0 {
		return "[http-debug/runtime] no details"
	}
	return strings.Join(lines, "\n")
}

func compactRuntimeHTTPDebugMetadata(metadata map[string]interface{}) string {
	if len(metadata) == 0 {
		return ""
	}
	data, err := json.Marshal(metadata)
	if err != nil {
		return ""
	}
	return string(data)
}

func formatRuntimeHTTPFingerprintLine(debug map[string]interface{}) string {
	parts := make([]string, 0, 4)
	for _, key := range []string{"request_sha256", "cache_surface_sha256", "input_sha256", "tools_sha256"} {
		if value := strings.TrimSpace(runtimeHTTPDebugString(debug, key)); value != "" {
			parts = append(parts, key+"="+value)
		}
	}
	return strings.Join(parts, " ")
}

func formatRuntimeHTTPShapeLine(debug map[string]interface{}) string {
	parts := make([]string, 0, 4)
	for _, key := range []string{"message_count", "input_count", "tool_count", "instructions_length"} {
		if value := runtimeHTTPDebugInt(debug, key); value > 0 {
			parts = append(parts, fmt.Sprintf("%s=%d", key, value))
		}
	}
	if value := strings.TrimSpace(runtimeHTTPDebugString(debug, "prompt_cache_key")); value != "" {
		parts = append(parts, "prompt_cache_key="+value)
	}
	return strings.Join(parts, " ")
}

func runtimeHTTPDebugString(values map[string]interface{}, key string) string {
	if len(values) == 0 {
		return ""
	}
	value, ok := values[key]
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	default:
		return fmt.Sprint(typed)
	}
}

func runtimeHTTPDebugInt(values map[string]interface{}, key string) int {
	if len(values) == 0 {
		return 0
	}
	value, ok := values[key]
	if !ok || value == nil {
		return 0
	}
	switch typed := value.(type) {
	case int:
		return typed
	case int32:
		return int(typed)
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	default:
		return 0
	}
}
