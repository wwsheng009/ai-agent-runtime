package remotetools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// AccessRecord is one HTTP request, independent of whether it invoked a tool.
//
// It exists because the tool audit cannot see 401s, health probes, or /metrics
// scrapes. Headers are never recorded: Authorization must not reach disk.
type AccessRecord struct {
	TS         string `json:"ts"`
	RemoteAddr string `json:"remote_addr,omitempty"`
	Method     string `json:"method"`
	Path       string `json:"path"`
	Status     int    `json:"status"`
	Bytes      int64  `json:"bytes,omitempty"`
	DurationMS int64  `json:"duration_ms"`
	UserAgent  string `json:"user_agent,omitempty"`
}

// AccessLogSink writes access records. A nil sink is a no-op.
type AccessLogSink interface {
	Write(AccessRecord) error
	Close() error
}

type nopAccessLogSink struct{}

func (nopAccessLogSink) Write(AccessRecord) error { return nil }
func (nopAccessLogSink) Close() error             { return nil }

// fileAccessLogSink appends NDJSON to a 0600 file with simple size rotation.
type fileAccessLogSink struct {
	mu         sync.Mutex
	path       string
	maxBytes   int64
	maxBackups int
	file       *os.File
	size       int64
}

// NewAccessLogSink opens the optional HTTP access log. An empty path disables it.
func NewAccessLogSink(path string, maxSizeMB, maxBackups int) (AccessLogSink, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return nopAccessLogSink{}, nil
	}
	path = filepath.Clean(trimmed)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create access log dir: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open access log: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("chmod access log: %w", err)
	}
	info, statErr := file.Stat()
	var size int64
	if statErr == nil {
		size = info.Size()
	}
	maxBytes := int64(maxSizeMB) * 1024 * 1024
	if maxBytes <= 0 {
		maxBytes = 100 * 1024 * 1024
	}
	return &fileAccessLogSink{
		path:       path,
		maxBytes:   maxBytes,
		maxBackups: maxBackups,
		file:       file,
		size:       size,
	}, nil
}

func (s *fileAccessLogSink) Write(record AccessRecord) error {
	if s == nil || s.file == nil {
		return nil
	}
	if record.TS == "" {
		record.TS = time.Now().Format(time.RFC3339Nano)
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode access record: %w", err)
	}
	encoded = append(encoded, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.size+int64(len(encoded)) > s.maxBytes {
		if err := s.rotateLocked(); err != nil {
			fmt.Fprintf(os.Stderr, "aicli-remote-tools: access log rotation failed: %v\n", err)
		}
	}
	n, writeErr := s.file.Write(encoded)
	s.size += int64(n)
	if writeErr != nil {
		return fmt.Errorf("write access record: %w", writeErr)
	}
	return nil
}

func (s *fileAccessLogSink) rotateLocked() error {
	if err := s.file.Close(); err != nil {
		return err
	}
	s.file = nil
	if s.maxBackups > 0 {
		oldest := fmt.Sprintf("%s.%d", s.path, s.maxBackups)
		_ = os.Remove(oldest)
		for i := s.maxBackups - 1; i >= 1; i-- {
			src := fmt.Sprintf("%s.%d", s.path, i)
			dst := fmt.Sprintf("%s.%d", s.path, i+1)
			_ = os.Rename(src, dst)
		}
		_ = os.Rename(s.path, s.path+".1")
	} else {
		_ = os.Remove(s.path)
	}
	file, err := os.OpenFile(s.path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	s.file = file
	s.size = 0
	return nil
}

func (s *fileAccessLogSink) Close() error {
	if s == nil || s.file == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil {
		return nil
	}
	err := s.file.Close()
	s.file = nil
	return err
}
