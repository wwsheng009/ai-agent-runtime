package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	runtimellm "github.com/wwsheng009/ai-agent-runtime/internal/llm"
)

type runtimeHTTPArtifactEnvelope struct {
	Sequence           int             `json:"sequence"`
	CapturedAt         string          `json:"captured_at"`
	Source             string          `json:"source,omitempty"`
	Phase              string          `json:"phase,omitempty"`
	Provider           string          `json:"provider,omitempty"`
	Protocol           string          `json:"protocol,omitempty"`
	Model              string          `json:"model,omitempty"`
	Method             string          `json:"method,omitempty"`
	URL                string          `json:"url,omitempty"`
	ResponseStatusCode int             `json:"response_status_code,omitempty"`
	Error              string          `json:"error,omitempty"`
	BodyBytes          int             `json:"body_bytes,omitempty"`
	BodyFormat         string          `json:"body_format,omitempty"`
	BodyPreview        string          `json:"body_preview,omitempty"`
	BodyJSON           json.RawMessage `json:"body_json,omitempty"`
	BodyText           string          `json:"body_text,omitempty"`
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
		Method:             strings.TrimSpace(event.Method),
		URL:                strings.TrimSpace(event.URL),
		ResponseStatusCode: event.ResponseStatusCode,
		Error:              strings.TrimSpace(event.Error),
	}

	body, preview, byteCount := runtimeHTTPArtifactBody(event)
	envelope.BodyBytes = byteCount
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
			return append([]byte(nil), event.RequestBodyRaw...), strings.TrimSpace(event.RequestBody), len(event.RequestBodyRaw)
		}
		body := strings.TrimSpace(event.RequestBody)
		if body == "" {
			return nil, "", event.RequestBodyBytes
		}
		return []byte(body), body, firstNonZero(event.RequestBodyBytes, len(body))
	default:
		if len(event.ResponseBodyRaw) > 0 {
			return append([]byte(nil), event.ResponseBodyRaw...), strings.TrimSpace(event.ResponseBodyPreview), len(event.ResponseBodyRaw)
		}
		preview := strings.TrimSpace(event.ResponseBodyPreview)
		if preview == "" {
			return nil, "", event.ResponseBodyBytes
		}
		return []byte(preview), preview, firstNonZero(event.ResponseBodyBytes, len(preview))
	}
}

func currentRuntimeHTTPArtifactDir(session *ChatSession) string {
	if session == nil {
		return ""
	}
	if session.Logger != nil {
		if dir := session.Logger.RuntimeHTTPArtifactDir(); dir != "" {
			return dir
		}
	}
	sessionPath := currentRuntimeSessionPath(session)
	if sessionPath == "" {
		return ""
	}
	baseName := strings.TrimSuffix(filepath.Base(sessionPath), filepath.Ext(sessionPath))
	if baseName == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(sessionPath), baseName+".artifacts", "runtime-http")
}

func currentChatLogFile(session *ChatSession) string {
	if session == nil || session.Logger == nil {
		return ""
	}
	return session.Logger.SessionLogPath()
}

func currentDebugLogFile(session *ChatSession) string {
	if session == nil || session.Logger == nil {
		return ""
	}
	return session.Logger.DebugLogPath()
}

func firstNonZero(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}
