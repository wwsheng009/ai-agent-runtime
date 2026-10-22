package remotetools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// AuditRecord is one NDJSON line describing one tool decision.
//
// It deliberately records hashes and structured summaries rather than raw file
// contents or full command text: an audit log that leaks secrets is a
// compliance problem of its own (design §7.5 / review S5).
type AuditRecord struct {
	TS           string   `json:"ts"`
	Principal    string   `json:"principal,omitempty"`
	Transport    string   `json:"transport"`
	RemoteAddr   string   `json:"remote_addr,omitempty"`
	SessionID    string   `json:"session_id,omitempty"`
	ToolCallID   string   `json:"tool_call_id,omitempty"`
	Target       string   `json:"target,omitempty"`
	Tool         string   `json:"tool"`
	Decision     string   `json:"decision"`
	Reason       string   `json:"reason,omitempty"`
	Workspace    string   `json:"workspace,omitempty"`
	Paths        []string `json:"paths,omitempty"`
	// Program is the argv[0] basename for shell/job_start records. It is safe to
	// log: it is the same identity the command policy matched.
	Program string `json:"program,omitempty"`
	// CommandHash is a stable hash of the normalized command. It allows
	// correlating repeated attempts without storing secrets from the command line.
	CommandHash string `json:"command_hash,omitempty"`
	// CommandPreview is opt-in (audit.commandPreviewBytes). It may contain
	// credentials, so it is empty by default.
	CommandPreview string `json:"command_preview,omitempty"`
	// PolicyRule names the allow/deny rule that produced the decision, e.g.
	// "allowedCommandPatterns:deploy238_*.sh".
	PolicyRule   string   `json:"policy_rule,omitempty"`
	ExitCode     int      `json:"exit_code,omitempty"`
	DurationMS   int64    `json:"duration_ms,omitempty"`
	BytesOut     int      `json:"bytes_out,omitempty"`
	Truncated    bool     `json:"truncated,omitempty"`
	PolicyVer    string   `json:"policy_version,omitempty"`
	ExecutionEnv string   `json:"execution_scope,omitempty"`
}

// AuditSink writes audit records. A nil sink is a no-op.
type AuditSink interface {
	Write(AuditRecord) error
	Close() error
}

// nopAuditSink discards every record.
type nopAuditSink struct{}

func (nopAuditSink) Write(AuditRecord) error { return nil }
func (nopAuditSink) Close() error            { return nil }

// fileAuditSink appends NDJSON to a 0600 file with simple size-based rotation.
//
// Rotation is deliberately simple (rename to .1, .2, ... up to MaxBackups)
// rather than a full lumberjack dependency: the remote binary should stay small,
// and audit volume per target is low.
type fileAuditSink struct {
	mu         sync.Mutex
	path       string
	maxBytes   int64
	maxBackups int
	file       *os.File
	size       int64
}

// NewAuditSink opens the audit sink described by cfg. An empty path returns a
// no-op sink.
func NewAuditSink(cfg AuditConfig) (AuditSink, error) {
	path := filepath.Clean(cfg.Path)
	if cfg.Path == "" {
		return nopAuditSink{}, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create audit dir: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open audit file: %w", err)
	}
	// A previous run may have left the file wider than the policy allows.
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("chmod audit file: %w", err)
	}
	info, statErr := file.Stat()
	var size int64
	if statErr == nil {
		size = info.Size()
	}
	maxBytes := int64(cfg.MaxSizeMB) * 1024 * 1024
	if maxBytes <= 0 {
		maxBytes = 100 * 1024 * 1024
	}
	return &fileAuditSink{
		path:       path,
		maxBytes:   maxBytes,
		maxBackups: cfg.MaxBackups,
		file:       file,
		size:       size,
	}, nil
}

// Write appends one record, rotating first when the file would exceed the cap.
func (s *fileAuditSink) Write(record AuditRecord) error {
	if s == nil || s.file == nil {
		return nil
	}
	if record.TS == "" {
		record.TS = time.Now().Format(time.RFC3339Nano)
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode audit record: %w", err)
	}
	encoded = append(encoded, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.size+int64(len(encoded)) > s.maxBytes {
		if err := s.rotateLocked(); err != nil {
			// Rotation failure must not drop the record silently: keep writing to
			// the current file so the audit trail stays continuous.
			fmt.Fprintf(os.Stderr, "aicli-remote-tools: audit rotation failed: %v\n", err)
		}
	}
	n, writeErr := s.file.Write(encoded)
	s.size += int64(n)
	if writeErr != nil {
		return fmt.Errorf("write audit record: %w", writeErr)
	}
	return nil
}

// rotateLocked renames the current file and opens a fresh one.
func (s *fileAuditSink) rotateLocked() error {
	if err := s.file.Close(); err != nil {
		return err
	}
	s.file = nil
	if s.maxBackups > 0 {
		// Shift .N → .N+1 from the oldest end so the newest rotation wins.
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

// Close releases the underlying file.
func (s *fileAuditSink) Close() error {
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