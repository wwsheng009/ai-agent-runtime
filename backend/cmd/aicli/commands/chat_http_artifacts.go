package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	runtimellm "github.com/wwsheng009/ai-agent-runtime/internal/llm"
)

const (
	runtimeHTTPArtifactMaxFiles = 256
	runtimeHTTPArtifactMaxBytes = 64 * 1024 * 1024
	runtimeHTTPMetadataMaxKeys  = 128
	runtimeHTTPMetadataMaxItems = 128
	runtimeHTTPMetadataMaxDepth = 8
	runtimeHTTPMetadataMaxText  = 16 * 1024
)

type runtimeHTTPArtifactEnvelope struct {
	Sequence           int                    `json:"sequence"`
	CapturedAt         string                 `json:"captured_at"`
	Source             string                 `json:"source,omitempty"`
	Phase              string                 `json:"phase,omitempty"`
	Provider           string                 `json:"provider,omitempty"`
	Protocol           string                 `json:"protocol,omitempty"`
	Model              string                 `json:"model,omitempty"`
	Attempt            int                    `json:"attempt,omitempty"`
	MaxAttempts        int                    `json:"max_attempts,omitempty"`
	Method             string                 `json:"method,omitempty"`
	URL                string                 `json:"url,omitempty"`
	RequestMetadata    map[string]interface{} `json:"request_metadata,omitempty"`
	ResponseStatusCode int                    `json:"response_status_code,omitempty"`
	Error              string                 `json:"error,omitempty"`
	RetryReason        string                 `json:"retry_reason,omitempty"`
	RetryDelayMS       int64                  `json:"retry_delay_ms,omitempty"`
	BodyBytes          int                    `json:"body_bytes,omitempty"`
	BodyCapturedBytes  int                    `json:"body_captured_bytes,omitempty"`
	BodyTruncated      bool                   `json:"body_truncated,omitempty"`
	BodyFormat         string                 `json:"body_format,omitempty"`
	BodyPreview        string                 `json:"body_preview,omitempty"`
	BodyJSON           json.RawMessage        `json:"body_json,omitempty"`
	BodyText           string                 `json:"body_text,omitempty"`
}

func writeRuntimeHTTPArtifact(session *ChatSession, event runtimellm.HTTPDebugEvent) (string, error) {
	if session == nil || session.runtimeHTTPCapture == nil {
		return "", nil
	}
	dir := currentRuntimeHTTPArtifactDir(session)
	if strings.TrimSpace(dir) == "" {
		return "", nil
	}
	session.runtimeHTTPCapture.SetArtifactDir(dir)
	path, sequence := session.runtimeHTTPCapture.NextArtifactPath(event.Phase, event.Source)
	if path == "" {
		return "", nil
	}

	envelope := buildRuntimeHTTPArtifactEnvelope(sequence, event)
	data, err := json.MarshalIndent(envelope, "", "  ")
	if err != nil {
		return "", fmt.Errorf("序列化 runtime HTTP artifact 失败: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return "", fmt.Errorf("创建 runtime HTTP artifact 目录失败: %w", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return "", fmt.Errorf("写入 runtime HTTP artifact 失败: %w", err)
	}
	session.runtimeHTTPCapture.RecordArtifactPath(event.Phase, path)
	if err := pruneRuntimeHTTPArtifacts(dir, runtimeHTTPArtifactMaxFiles, runtimeHTTPArtifactMaxBytes); err != nil {
		return path, fmt.Errorf("清理 runtime HTTP artifact 失败: %w", err)
	}
	return path, nil
}

func buildRuntimeHTTPArtifactEnvelope(sequence int, event runtimellm.HTTPDebugEvent) runtimeHTTPArtifactEnvelope {
	envelope := runtimeHTTPArtifactEnvelope{
		Sequence:           sequence,
		CapturedAt:         time.Now().Format(time.RFC3339Nano),
		Source:             strings.TrimSpace(event.Source),
		Phase:              strings.TrimSpace(event.Phase),
		Provider:           strings.TrimSpace(event.Provider),
		Protocol:           strings.TrimSpace(event.Protocol),
		Model:              strings.TrimSpace(event.Model),
		Attempt:            event.Attempt,
		MaxAttempts:        event.MaxAttempts,
		Method:             strings.TrimSpace(event.Method),
		URL:                strings.TrimSpace(event.URL),
		RequestMetadata:    cloneRuntimeHTTPArtifactMetadata(event.RequestMetadata),
		ResponseStatusCode: event.ResponseStatusCode,
		Error:              strings.TrimSpace(event.Error),
		RetryReason:        strings.TrimSpace(event.RetryReason),
		RetryDelayMS:       event.RetryDelayMS,
	}

	body, preview, byteCount := runtimeHTTPArtifactBody(event)
	envelope.BodyBytes = byteCount
	envelope.BodyCapturedBytes = len(body)
	envelope.BodyTruncated = byteCount > len(body)
	envelope.BodyPreview = preview
	if len(body) == 0 {
		return envelope
	}
	if json.Valid(body) {
		envelope.BodyFormat = "json"
		envelope.BodyJSON = append(json.RawMessage(nil), body...)
		return envelope
	}
	envelope.BodyFormat = "text"
	envelope.BodyText = string(body)
	return envelope
}

func runtimeHTTPArtifactBody(event runtimellm.HTTPDebugEvent) ([]byte, string, int) {
	switch strings.ToLower(strings.TrimSpace(event.Phase)) {
	case "request":
		if len(event.RequestBodyRaw) > 0 {
			return append([]byte(nil), event.RequestBodyRaw...), strings.TrimSpace(event.RequestBody), firstNonZero(event.RequestBodyBytes, len(event.RequestBodyRaw))
		}
		body := strings.TrimSpace(event.RequestBody)
		if body == "" {
			return nil, "", event.RequestBodyBytes
		}
		return []byte(body), body, firstNonZero(event.RequestBodyBytes, len(body))
	default:
		if len(event.ResponseBodyRaw) > 0 {
			return append([]byte(nil), event.ResponseBodyRaw...), strings.TrimSpace(event.ResponseBodyPreview), firstNonZero(event.ResponseBodyBytes, len(event.ResponseBodyRaw))
		}
		preview := strings.TrimSpace(event.ResponseBodyPreview)
		if preview == "" {
			return nil, "", event.ResponseBodyBytes
		}
		return []byte(preview), preview, firstNonZero(event.ResponseBodyBytes, len(preview))
	}
}

func pruneRuntimeHTTPArtifacts(dir string, maxFiles int, maxBytes int64) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	type artifactFile struct {
		path    string
		name    string
		size    int64
		modTime time.Time
	}
	files := make([]artifactFile, 0, len(entries))
	var totalBytes int64
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".json") {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}
		files = append(files, artifactFile{
			path: filepath.Join(dir, entry.Name()), name: entry.Name(),
			size: info.Size(), modTime: info.ModTime(),
		})
		totalBytes += info.Size()
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].modTime.Equal(files[j].modTime) {
			return files[i].name < files[j].name
		}
		return files[i].modTime.Before(files[j].modTime)
	})
	for len(files) > 0 && ((maxFiles > 0 && len(files) > maxFiles) || (maxBytes > 0 && totalBytes > maxBytes)) {
		oldest := files[0]
		if err := os.Remove(oldest.path); err != nil && !os.IsNotExist(err) {
			return err
		}
		totalBytes -= oldest.size
		files = files[1:]
	}
	return nil
}

func currentRuntimeHTTPArtifactDir(session *ChatSession) string {
	if session == nil {
		return ""
	}
	if session.Logger != nil {
		if dir := session.Logger.RuntimeHTTPArtifactDir(); dir != "" {
			return resolveAbsoluteChatPath(dir)
		}
	}
	artifactRoot := currentRuntimeSessionArtifactRoot(session)
	if artifactRoot == "" {
		return ""
	}
	return resolveAbsoluteChatPath(filepath.Join(artifactRoot, "runtime-http"))
}

func currentChatLogFile(session *ChatSession) string {
	if session == nil || session.Logger == nil {
		return ""
	}
	return resolveAbsoluteChatPath(session.Logger.SessionLogPath())
}

func currentDebugLogFile(session *ChatSession) string {
	if session == nil || session.Logger == nil {
		return ""
	}
	return resolveAbsoluteChatPath(session.Logger.DebugLogPath())
}

func firstNonZero(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func cloneRuntimeHTTPArtifactMetadata(input map[string]interface{}) map[string]interface{} {
	return cloneRuntimeHTTPArtifactMetadataDepth(input, 0)
}

func cloneRuntimeHTTPArtifactMetadataDepth(input map[string]interface{}, depth int) map[string]interface{} {
	if len(input) == 0 {
		return nil
	}
	keys := make([]string, 0, len(input))
	for key := range input {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) > runtimeHTTPMetadataMaxKeys {
		keys = keys[:runtimeHTTPMetadataMaxKeys]
	}
	cloned := make(map[string]interface{}, len(keys)+1)
	for _, key := range keys {
		cloned[key] = cloneRuntimeHTTPArtifactValue(input[key], depth+1)
	}
	if omitted := len(input) - len(keys); omitted > 0 {
		cloned["_artifact_metadata_omitted_keys"] = omitted
	}
	return cloned
}

func cloneRuntimeHTTPArtifactValue(value interface{}, depth int) interface{} {
	if depth >= runtimeHTTPMetadataMaxDepth {
		return "[artifact metadata depth omitted]"
	}
	switch typed := value.(type) {
	case string:
		return truncateUTF8Bytes(typed, runtimeHTTPMetadataMaxText)
	case map[string]interface{}:
		return cloneRuntimeHTTPArtifactMetadataDepth(typed, depth)
	case []interface{}:
		limit := min(len(typed), runtimeHTTPMetadataMaxItems)
		cloned := make([]interface{}, limit)
		for index := 0; index < limit; index++ {
			cloned[index] = cloneRuntimeHTTPArtifactValue(typed[index], depth+1)
		}
		return cloned
	case []string:
		limit := min(len(typed), runtimeHTTPMetadataMaxItems)
		cloned := make([]string, limit)
		for index := 0; index < limit; index++ {
			cloned[index] = truncateUTF8Bytes(typed[index], runtimeHTTPMetadataMaxText)
		}
		return cloned
	case []map[string]interface{}:
		limit := min(len(typed), runtimeHTTPMetadataMaxItems)
		cloned := make([]map[string]interface{}, limit)
		for index := 0; index < limit; index++ {
			cloned[index] = cloneRuntimeHTTPArtifactMetadataDepth(typed[index], depth+1)
		}
		return cloned
	default:
		return typed
	}
}
