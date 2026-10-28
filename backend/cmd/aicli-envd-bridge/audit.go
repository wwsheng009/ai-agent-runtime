package main

import (
	"encoding/json"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// auditEntry is one JSONL record of a tool call.
//
// Only metadata is recorded: argument and result contents can carry file bodies,
// command output or secrets, so they never reach the audit file.
type auditEntry struct {
	Timestamp     string `json:"ts"`
	Tool          string `json:"tool"`
	Profile       string `json:"profile"`
	OK            bool   `json:"ok"`
	DurationMS    int64  `json:"duration_ms"`
	ArgumentBytes int    `json:"argument_bytes"`
	Target        string `json:"target,omitempty"`
	User          string `json:"user,omitempty"`
}

// auditLog appends one JSON object per line to a file.
//
// The MCP loop is single-threaded today, but the mutex keeps a future concurrent
// dispatcher from interleaving half lines.
type auditLog struct {
	mu sync.Mutex
	w  io.Writer
	c  io.Closer
}

// openOptionalAudit opens the audit file when a path is configured.
func openOptionalAudit(path string) (*auditLog, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, nil
	}
	return newAuditLog(path)
}

// newAuditLog opens path in append mode with owner-only permissions.
func newAuditLog(path string) (*auditLog, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	return &auditLog{w: f, c: f}, nil
}

// newAuditLogWriter is the test seam: it records to any writer.
func newAuditLogWriter(w io.Writer) *auditLog {
	return &auditLog{w: w}
}

// record appends one line. A failure is reported to the caller so it can be
// surfaced on stderr without failing the tool call itself.
func (a *auditLog) record(entry auditEntry) error {
	if a == nil || a.w == nil {
		return nil
	}
	entry.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	payload, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	payload = append(payload, '\n')

	a.mu.Lock()
	defer a.mu.Unlock()
	_, err = a.w.Write(payload)
	return err
}

// Close closes the underlying file when there is one.
func (a *auditLog) Close() error {
	if a == nil || a.c == nil {
		return nil
	}
	return a.c.Close()
}
