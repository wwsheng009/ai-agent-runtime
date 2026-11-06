package background

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

const backgroundOutputRefLog = "log"

// backgroundEventPayloadForStorage keeps only output metadata in the event
// table when the job already has a durable log file. The full chunk remains in
// the log; ListEvents hydrates it back on read, and ReadOutput already reads the
// log directly. Jobs without a log keep the inline chunk (legacy/embedded).
func backgroundEventPayloadForStorage(ctx context.Context, tx *sql.Tx, jobID, eventType string, payload map[string]interface{}) (map[string]interface{}, error) {
	if !strings.EqualFold(strings.TrimSpace(eventType), "output") || payload == nil {
		return payload, nil
	}
	chunk, ok := payload["chunk"].(string)
	if !ok || chunk == "" {
		return payload, nil
	}
	offset, offsetOK := backgroundPayloadInt64(payload["offset"])
	size, sizeOK := backgroundPayloadInt64(payload["size"])
	if !offsetOK || !sizeOK || size <= 0 || int64(len(chunk)) != size {
		return payload, nil
	}

	var logPath string
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(log_path, '') FROM background_jobs WHERE id = ?`, jobID).Scan(&logPath); err != nil {
		if err == sql.ErrNoRows {
			return payload, nil
		}
		return nil, fmt.Errorf("read background job log path: %w", err)
	}
	logPath = strings.TrimSpace(logPath)
	if logPath == "" {
		return payload, nil
	}
	info, err := os.Stat(logPath)
	if err != nil || info.Size() < offset+size {
		// The log may not be flushed yet (or was removed). Keep the inline chunk
		// rather than persisting a reference that cannot be hydrated.
		return payload, nil
	}

	thin := make(map[string]interface{}, len(payload))
	for key, value := range payload {
		if key == "chunk" {
			continue
		}
		thin[key] = value
	}
	thin["output_ref"] = backgroundOutputRefLog
	thin["output_bytes"] = len(chunk)
	return thin, nil
}

func backgroundPayloadInt64(value interface{}) (int64, bool) {
	switch typed := value.(type) {
	case int:
		return int64(typed), true
	case int32:
		return int64(typed), true
	case int64:
		return typed, true
	case uint:
		return int64(typed), true
	case uint32:
		return int64(typed), true
	case uint64:
		return int64(typed), true
	case float64:
		return int64(typed), true
	case json.Number:
		parsed, err := typed.Int64()
		return parsed, err == nil
	default:
		return 0, false
	}
}

// backgroundOutputNeedsHydration reports whether an event carries only a log
// reference and still needs its chunk restored.
func backgroundOutputNeedsHydration(event *JobEvent) bool {
	if event == nil || event.Type != "output" || event.Payload == nil {
		return false
	}
	if chunk, ok := event.Payload["chunk"].(string); ok && chunk != "" {
		return false
	}
	if ref, _ := event.Payload["output_ref"].(string); ref != backgroundOutputRefLog {
		return false
	}
	_, offsetOK := backgroundPayloadInt64(event.Payload["offset"])
	size, sizeOK := backgroundPayloadInt64(event.Payload["size"])
	return offsetOK && sizeOK && size > 0
}

// hydrateBackgroundOutputPayload restores the chunk from the job log. It is
// deliberately best-effort: a missing/rotated log leaves the metadata intact
// instead of failing the whole event query.
func hydrateBackgroundOutputPayload(event *JobEvent, logFile *os.File) {
	if event == nil || logFile == nil || !backgroundOutputNeedsHydration(event) {
		return
	}
	offset, offsetOK := backgroundPayloadInt64(event.Payload["offset"])
	size, sizeOK := backgroundPayloadInt64(event.Payload["size"])
	if !offsetOK || !sizeOK || offset < 0 || size <= 0 {
		return
	}
	buf := make([]byte, size)
	n, err := logFile.ReadAt(buf, offset)
	if n <= 0 {
		return
	}
	if err != nil && n < int(size) {
		return
	}
	event.Payload["chunk"] = string(buf[:n])
	delete(event.Payload, "output_ref")
	delete(event.Payload, "output_bytes")
}
