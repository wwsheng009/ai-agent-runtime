package skills

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

func TestGetRuntimeLogs_ReturnsFilteredEntries(t *testing.T) {
	logFilePath := filepath.Join(t.TempDir(), "gateway.log")
	require.NoError(t, writeRuntimeLogLines(logFilePath,
		`{"level":"info","timestamp":"2026-04-05T00:32:56Z","module":"gateway","caller":"main.go:201","message":"runtime started","listen":"127.0.0.1:8101"}`,
		`{"level":"error","timestamp":"2026-04-05T00:33:12Z","module":"gateway","caller":"handler.go:6268","message":"LLM upstream request failed","request_id":"trace_1","upstream_provider":"openai","upstream_model":"gpt-5","url":"https://api.example.com/v1/responses","response_status_code":503,"response_body_preview":"temporary unavailable"}`,
		`{"level":"error","timestamp":"2026-04-05T00:33:14Z","module":"gateway","caller":"handler.go:6268","message":"LLM upstream request failed","request_id":"trace_2","upstream_provider":"anthropic","upstream_model":"claude-3-7-sonnet","url":"https://api.example.com/v1/messages","response_status_code":502,"response_body_preview":"gateway timeout","session_id":"session-2"}`,
	))

	handler := newRuntimeLogTestHandler()
	handler.SetRuntimeLogFilePath(logFilePath)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/logs?limit=2&level=error&query=trace_2", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))

	assert.Equal(t, true, payload["exists"])
	assert.Equal(t, float64(1), payload["count"])
	assert.NotEmpty(t, payload["file_path"])
	assert.Greater(t, payload["next_cursor"].(float64), 0.0)

	entries := payload["entries"].([]interface{})
	require.Len(t, entries, 1)

	entry := entries[0].(map[string]interface{})
	assert.Equal(t, "error", entry["level"])
	assert.Equal(t, "trace_2", entry["request_id"])
	assert.Equal(t, "anthropic", entry["provider"])
	assert.Equal(t, "claude-3-7-sonnet", entry["model"])
	assert.Equal(t, "session-2", entry["session_id"])
	assert.Equal(t, float64(502), entry["response_status_code"])
	assert.Equal(t, "LLM upstream request failed", entry["message"])

	filters := payload["filters"].(map[string]interface{})
	assert.Equal(t, float64(2), filters["limit"])
	assert.Equal(t, "error", filters["level"])
	assert.Equal(t, "trace_2", filters["query"])
}

func TestStreamRuntimeLogs_EmitsReadyAndLogEvents(t *testing.T) {
	logFilePath := filepath.Join(t.TempDir(), "gateway.log")
	require.NoError(t, writeRuntimeLogLines(logFilePath,
		`{"level":"info","timestamp":"2026-04-05T00:32:56Z","module":"gateway","caller":"main.go:201","message":"runtime started"}`,
	))

	handler := newRuntimeLogTestHandler()
	handler.SetRuntimeLogFilePath(logFilePath)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/logs/stream?after=0&poll_ms=25", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	ctx, cancel := context.WithCancel(req.Context())
	defer cancel()
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		router.ServeHTTP(rec, req)
	}()

	time.Sleep(60 * time.Millisecond)
	require.NoError(t, appendRuntimeLogLine(logFilePath,
		`{"level":"error","timestamp":"2026-04-05T00:33:12Z","module":"gateway","caller":"handler.go:6268","message":"LLM upstream request failed","request_id":"trace_stream","upstream_provider":"openai","upstream_model":"gpt-5","response_status_code":503}`,
	))
	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stream handler did not exit after context cancellation")
	}

	body := rec.Body.String()
	assert.Contains(t, body, "event: ready")
	assert.Contains(t, body, "event: log")
	assert.Contains(t, body, `"request_id":"trace_stream"`)
	assert.Contains(t, body, `"provider":"openai"`)
	assert.Contains(t, body, `"model":"gpt-5"`)
}

func newRuntimeLogTestHandler() *Handler {
	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	return NewHandler(registry, nil, mcpManager)
}

func writeRuntimeLogLines(path string, lines ...string) error {
	content := strings.Join(lines, "\n")
	if content != "" {
		content += "\n"
	}
	return os.WriteFile(path, []byte(content), 0644)
}

func appendRuntimeLogLine(path, line string) error {
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0644)
	if err != nil {
		return err
	}
	defer file.Close()

	_, err = file.WriteString(strings.TrimRight(line, "\r\n") + "\n")
	return err
}
