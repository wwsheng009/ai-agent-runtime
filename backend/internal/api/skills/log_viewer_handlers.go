package skills

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	errors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
)

const (
	defaultRuntimeLogLimit     = 200
	maxRuntimeLogLimit         = 500
	maxRuntimeLogHistoryBytes  = 8 * 1024 * 1024
	defaultRuntimeLogPollDelay = 750 * time.Millisecond
	maxRuntimeLogPollDelay     = 5 * time.Second
)

type runtimeLogEntryView struct {
	Cursor              int64                  `json:"cursor"`
	Raw                 map[string]interface{} `json:"raw,omitempty"`
	RawText             string                 `json:"raw_text"`
	Timestamp           string                 `json:"timestamp,omitempty"`
	Level               string                 `json:"level,omitempty"`
	Module              string                 `json:"module,omitempty"`
	Caller              string                 `json:"caller,omitempty"`
	Message             string                 `json:"message,omitempty"`
	RequestID           string                 `json:"request_id,omitempty"`
	TraceID             string                 `json:"trace_id,omitempty"`
	SessionID           string                 `json:"session_id,omitempty"`
	Provider            string                 `json:"provider,omitempty"`
	Model               string                 `json:"model,omitempty"`
	Method              string                 `json:"method,omitempty"`
	URL                 string                 `json:"url,omitempty"`
	ResponseStatusCode  int                    `json:"response_status_code,omitempty"`
	ResponseBodyPreview string                 `json:"response_body_preview,omitempty"`
	UpstreamError       string                 `json:"upstream_error,omitempty"`
	Fields              map[string]interface{} `json:"fields,omitempty"`
}

type runtimeLogFilter struct {
	Level string
	Query string
}

type runtimeLogsFilterView struct {
	Limit int    `json:"limit"`
	Level string `json:"level,omitempty"`
	Query string `json:"query,omitempty"`
}

// ListRuntimeLogs returns the latest runtime service logs.
func (h *Handler) ListRuntimeLogs(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeUsageAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}

	logFilePath := strings.TrimSpace(h.logFilePath)
	if logFilePath == "" {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "runtime log file is not configured"))
		return
	}

	limit, err := parseRuntimeLogLimit(r)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	filter := parseRuntimeLogFilter(r)

	entries, nextCursor, exists, err := readRecentRuntimeLogEntries(logFilePath, limit, filter)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}

	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"entries":     entries,
		"count":       len(entries),
		"exists":      exists,
		"file_path":   logFilePath,
		"next_cursor": nextCursor,
		"filters":     runtimeLogsFilterView{Limit: limit, Level: filter.Level, Query: filter.Query},
	})
}

// StreamRuntimeLogs streams runtime service logs via SSE.
func (h *Handler) StreamRuntimeLogs(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeUsageAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}

	logFilePath := strings.TrimSpace(h.logFilePath)
	if logFilePath == "" {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "runtime log file is not configured"))
		return
	}

	afterCursor := int64(0)
	if raw := strings.TrimSpace(r.URL.Query().Get("after")); raw != "" {
		parsed, parseErr := parseInt64(raw)
		if parseErr != nil || parsed < 0 {
			h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "invalid after value"))
			return
		}
		afterCursor = parsed
	}

	pollDelay := defaultRuntimeLogPollDelay
	if raw := strings.TrimSpace(r.URL.Query().Get("poll_ms")); raw != "" {
		parsed, parseErr := time.ParseDuration(raw + "ms")
		if parseErr != nil || parsed <= 0 {
			h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "invalid poll_ms value"))
			return
		}
		if parsed > maxRuntimeLogPollDelay {
			parsed = maxRuntimeLogPollDelay
		}
		pollDelay = parsed
	}

	filter := parseRuntimeLogFilter(r)

	h.prepareSSEHeaders(w)
	emitter := newSSEEmitter(w)

	exists, size, err := statRuntimeLogFile(logFilePath)
	if err != nil {
		emitter.Emit("error", map[string]interface{}{"error": err.Error()})
		return
	}
	if size < afterCursor {
		afterCursor = 0
	}
	emitter.Emit("ready", map[string]interface{}{
		"cursor":    afterCursor,
		"exists":    exists,
		"file_path": logFilePath,
	})

	ctx := r.Context()
	ticker := time.NewTicker(pollDelay)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			exists, size, statErr := statRuntimeLogFile(logFilePath)
			if statErr != nil {
				emitter.Emit("error", map[string]interface{}{"error": statErr.Error()})
				return
			}

			if !exists {
				continue
			}

			if size < afterCursor {
				afterCursor = 0
				emitter.Emit("reset", map[string]interface{}{
					"cursor":    afterCursor,
					"exists":    exists,
					"file_path": logFilePath,
					"reason":    "truncated",
				})
			}

			if size == afterCursor {
				continue
			}

			entries, nextCursor, readErr := readRuntimeLogEntriesFromOffset(logFilePath, afterCursor)
			if readErr != nil {
				emitter.Emit("error", map[string]interface{}{"error": readErr.Error()})
				return
			}

			for _, entry := range entries {
				if runtimeLogMatchesFilter(entry, filter) {
					emitter.Emit("log", entry)
				}
			}
			afterCursor = nextCursor
		}
	}
}

func parseRuntimeLogLimit(r *http.Request) (int, error) {
	limit := defaultRuntimeLogLimit
	if r == nil {
		return limit, nil
	}
	raw := strings.TrimSpace(r.URL.Query().Get("limit"))
	if raw == "" {
		return limit, nil
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed <= 0 {
		return 0, errors.New(errors.ErrValidationFailed, "invalid limit value")
	}
	if parsed > maxRuntimeLogLimit {
		parsed = maxRuntimeLogLimit
	}
	return parsed, nil
}

func parseRuntimeLogFilter(r *http.Request) runtimeLogFilter {
	if r == nil {
		return runtimeLogFilter{}
	}
	return runtimeLogFilter{
		Level: strings.ToLower(strings.TrimSpace(r.URL.Query().Get("level"))),
		Query: strings.ToLower(strings.TrimSpace(r.URL.Query().Get("query"))),
	}
}

func readRecentRuntimeLogEntries(path string, limit int, filter runtimeLogFilter) ([]runtimeLogEntryView, int64, bool, error) {
	exists, size, err := statRuntimeLogFile(path)
	if err != nil {
		return nil, 0, false, err
	}
	if !exists || size <= 0 {
		return []runtimeLogEntryView{}, 0, exists, nil
	}

	start := int64(0)
	if size > maxRuntimeLogHistoryBytes {
		start = size - maxRuntimeLogHistoryBytes
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, 0, false, err
	}
	defer file.Close()

	section := io.NewSectionReader(file, start, size-start)
	buffer, err := io.ReadAll(section)
	if err != nil {
		return nil, 0, false, err
	}

	entries, _, err := decodeRuntimeLogBuffer(buffer, start, start > 0)
	if err != nil {
		return nil, 0, false, err
	}

	filtered := make([]runtimeLogEntryView, 0, len(entries))
	for _, entry := range entries {
		if runtimeLogMatchesFilter(entry, filter) {
			filtered = append(filtered, entry)
		}
	}

	if len(filtered) > limit {
		filtered = filtered[len(filtered)-limit:]
	}
	reverseRuntimeLogEntries(filtered)

	return filtered, size, exists, nil
}

func readRuntimeLogEntriesFromOffset(path string, offset int64) ([]runtimeLogEntryView, int64, error) {
	exists, size, err := statRuntimeLogFile(path)
	if err != nil {
		return nil, offset, err
	}
	if !exists {
		return nil, 0, nil
	}
	if size < offset {
		offset = 0
	}
	if size == offset {
		return nil, offset, nil
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, offset, err
	}
	defer file.Close()

	section := io.NewSectionReader(file, offset, size-offset)
	buffer, err := io.ReadAll(section)
	if err != nil {
		return nil, offset, err
	}

	return decodeRuntimeLogBuffer(buffer, offset, false)
}

func decodeRuntimeLogBuffer(buffer []byte, startCursor int64, trimPartialFirstLine bool) ([]runtimeLogEntryView, int64, error) {
	if len(buffer) == 0 {
		return nil, startCursor, nil
	}

	startIndex := 0
	if trimPartialFirstLine && buffer[0] != '\n' {
		if newlineIndex := bytes.IndexByte(buffer, '\n'); newlineIndex >= 0 {
			startIndex = newlineIndex + 1
		} else {
			return nil, startCursor, nil
		}
	}

	entries := make([]runtimeLogEntryView, 0)
	lineStart := startIndex
	nextCursor := startCursor + int64(startIndex)

	for index := startIndex; index < len(buffer); index++ {
		if buffer[index] != '\n' {
			continue
		}

		lineEnd := index
		if lineEnd > lineStart && buffer[lineEnd-1] == '\r' {
			lineEnd--
		}
		rawLine := strings.TrimSpace(string(buffer[lineStart:lineEnd]))
		nextCursor = startCursor + int64(index+1)
		lineStart = index + 1
		if rawLine == "" {
			continue
		}
		entries = append(entries, buildRuntimeLogEntry(rawLine, nextCursor))
	}

	return entries, nextCursor, nil
}

func buildRuntimeLogEntry(rawLine string, cursor int64) runtimeLogEntryView {
	entry := runtimeLogEntryView{
		Cursor:  cursor,
		RawText: rawLine,
		Message: rawLine,
	}

	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(rawLine), &payload); err != nil {
		return entry
	}

	entry.Raw = payload
	entry.Timestamp = runtimeLogString(payload, "timestamp")
	entry.Level = strings.ToLower(runtimeLogString(payload, "level"))
	entry.Module = runtimeLogString(payload, "module")
	entry.Caller = runtimeLogString(payload, "caller")
	entry.Message = runtimeLogString(payload, "message")
	entry.RequestID = runtimeLogString(payload, "request_id")
	entry.TraceID = runtimeLogString(payload, "trace_id")
	entry.SessionID = runtimeLogString(payload, "session_id")
	entry.Provider = firstRuntimeLogString(payload, "upstream_provider", "provider")
	entry.Model = firstRuntimeLogString(payload, "upstream_model", "model")
	entry.Method = runtimeLogString(payload, "method")
	entry.URL = runtimeLogString(payload, "url")
	entry.ResponseStatusCode = runtimeLogInt(payload, "response_status_code")
	entry.ResponseBodyPreview = runtimeLogString(payload, "response_body_preview")
	entry.UpstreamError = runtimeLogString(payload, "upstream_error")
	entry.Fields = runtimeLogExtraFields(payload)
	return entry
}

func runtimeLogExtraFields(payload map[string]interface{}) map[string]interface{} {
	if len(payload) == 0 {
		return nil
	}

	knownFields := map[string]struct{}{
		"caller":                {},
		"level":                 {},
		"message":               {},
		"method":                {},
		"model":                 {},
		"module":                {},
		"provider":              {},
		"request_id":            {},
		"response_body_preview": {},
		"response_status_code":  {},
		"session_id":            {},
		"timestamp":             {},
		"trace_id":              {},
		"upstream_error":        {},
		"upstream_model":        {},
		"upstream_provider":     {},
		"url":                   {},
	}

	fields := make(map[string]interface{})
	for key, value := range payload {
		if _, known := knownFields[key]; known {
			continue
		}
		fields[key] = value
	}
	if len(fields) == 0 {
		return nil
	}
	return fields
}

func runtimeLogMatchesFilter(entry runtimeLogEntryView, filter runtimeLogFilter) bool {
	if filter.Level != "" && !strings.EqualFold(strings.TrimSpace(entry.Level), filter.Level) {
		return false
	}
	if filter.Query == "" {
		return true
	}
	return strings.Contains(strings.ToLower(entry.RawText), filter.Query)
}

func runtimeLogString(payload map[string]interface{}, key string) string {
	if payload == nil {
		return ""
	}
	value, ok := payload[key]
	if !ok {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case json.Number:
		return typed.String()
	default:
		if encoded, err := json.Marshal(typed); err == nil {
			return strings.TrimSpace(string(encoded))
		}
		return strings.TrimSpace(fmt.Sprint(typed))
	}
}

func firstRuntimeLogString(payload map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if value := runtimeLogString(payload, key); value != "" {
			return value
		}
	}
	return ""
}

func runtimeLogInt(payload map[string]interface{}, key string) int {
	if payload == nil {
		return 0
	}
	value, ok := payload[key]
	if !ok {
		return 0
	}
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			return int(parsed)
		}
	case string:
		if parsed, err := strconv.Atoi(strings.TrimSpace(typed)); err == nil {
			return parsed
		}
	}
	return 0
}

func reverseRuntimeLogEntries(entries []runtimeLogEntryView) {
	for left, right := 0, len(entries)-1; left < right; left, right = left+1, right-1 {
		entries[left], entries[right] = entries[right], entries[left]
	}
}

func statRuntimeLogFile(path string) (bool, int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, 0, nil
		}
		return false, 0, err
	}
	if info.IsDir() {
		return false, 0, errors.New(errors.ErrConfigInvalid, "runtime log path points to a directory")
	}
	return true, info.Size(), nil
}
