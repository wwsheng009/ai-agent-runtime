package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
)

const (
	// readLedgerMaxEntries bounds per-session memory; oldest entries are
	// evicted first.
	readLedgerMaxEntries = 4096
	// readLedgerHashMaxBytes caps the file size the ledger will hash on read.
	// Larger files skip ledger recording instead of paying a second full read.
	readLedgerHashMaxBytes int64 = 8 << 20
)

// fileReadWindow describes the window a view call actually rendered. It lets
// the write-side guard distinguish "read the whole file" from "read 80 of
// 4000 lines" and tell the model which part it has already seen
// (analysis §3.8: a misleading "has not been read yet" sends the model into a
// small-window re-read loop).
type fileReadWindow struct {
	Offset     int
	Limit      int
	LinesRead  int
	TotalLines int
	Truncated  bool
}

// fileReadRecord captures the last state a session observed for one path.
type fileReadRecord struct {
	SHA256   string
	Size     int64
	FullRead bool
	ReadAt   time.Time
	Source   string // view | write | edit | multiedit | append_write
	Window   fileReadWindow
}

type sessionReadLedger struct {
	mu      sync.Mutex
	entries map[string]fileReadRecord
	order   []string
}

var sessionReadLedgers sync.Map // sessionID -> *sessionReadLedger

func ledgerForSession(sessionID string) *sessionReadLedger {
	if existing, ok := sessionReadLedgers.Load(sessionID); ok {
		return existing.(*sessionReadLedger)
	}
	created := &sessionReadLedger{entries: make(map[string]fileReadRecord)}
	actual, _ := sessionReadLedgers.LoadOrStore(sessionID, created)
	return actual.(*sessionReadLedger)
}

func normalizeLedgerPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	return filepath.Clean(abs)
}

func fileBytesSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (l *sessionReadLedger) record(path string, record fileReadRecord) {
	path = normalizeLedgerPath(path)
	if path == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, exists := l.entries[path]; !exists {
		l.order = append(l.order, path)
	}
	l.entries[path] = record
	for len(l.order) > readLedgerMaxEntries {
		oldest := l.order[0]
		l.order = l.order[1:]
		delete(l.entries, oldest)
	}
}

func (l *sessionReadLedger) lookup(path string) (fileReadRecord, bool) {
	path = normalizeLedgerPath(path)
	if path == "" {
		return fileReadRecord{}, false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	record, ok := l.entries[path]
	return record, ok
}

// recordFileReadFromDisk records the current disk state of path for the active
// session. It is best-effort: files above readLedgerHashMaxBytes are skipped so
// a read tool never pays for a second full-file scan on huge inputs.
func recordFileReadFromDisk(ctx context.Context, path string, fullRead bool, source string, window fileReadWindow) {
	if strings.TrimSpace(path) == "" {
		return
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > readLedgerHashMaxBytes {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	ledgerForSession(toolctx.SessionID(ctx)).record(path, fileReadRecord{
		SHA256:   fileBytesSHA256(data),
		Size:     int64(len(data)),
		FullRead: fullRead,
		ReadAt:   time.Now(),
		Source:   source,
		Window:   window,
	})
}

// sessionHasFullRead reports whether this session holds a complete view of the
// path (a full view read or a write it performed itself). The view dedup guard
// uses it to keep from-line-1 re-reads returning real content until the write
// side can rely on a full view (analysis §3.6).
func sessionHasFullRead(ctx context.Context, path string) bool {
	record, ok := ledgerForSession(toolctx.SessionID(ctx)).lookup(path)
	return ok && record.FullRead && record.SHA256 != ""
}

// recordFileWrite records bytes written by a tool from this session so a
// follow-up edit or overwrite is not mistaken for an external change.
func recordFileWrite(ctx context.Context, path string, data []byte, source string) {
	if strings.TrimSpace(path) == "" {
		return
	}
	ledgerForSession(toolctx.SessionID(ctx)).record(path, fileReadRecord{
		SHA256:   fileBytesSHA256(data),
		Size:     int64(len(data)),
		FullRead: true,
		ReadAt:   time.Now(),
		Source:   source,
	})
}

// staleWriteVerdict describes whether an existing file changed since the
// session last observed it.
type staleWriteVerdict struct {
	State       string // fresh | unread | stale
	CurrentSHA  string
	LastRecord  fileReadRecord
	HasRecord   bool
	LastReadAgo string
	// Partial marks a fresh verdict whose last view only rendered part of the
	// file: the overwrite is still allowed (bytes are unchanged), but the model
	// is told which window it has seen instead of a bare "fresh".
	Partial bool
}

const (
	staleWriteStateFresh  = "fresh"
	staleWriteStateUnread = "unread"
	staleWriteStateStale  = "stale"
)

// shouldRefuseStaleWrite keeps the hard guarantee scoped to files the model
// explicitly read (view). When the last observation came from our own write,
// an external change is surfaced as a warning instead of a hard refusal so
// common edit → formatter → edit loops do not dead-end.
func shouldRefuseStaleWrite(verdict staleWriteVerdict) bool {
	return verdict.State == staleWriteStateStale && verdict.LastRecord.Source == "view"
}

// evaluateStaleWrite compares current disk bytes against the session ledger.
// A missing record means the session never observed the file (allowed with a
// note); a mismatched hash means someone else changed it (refused).
func evaluateStaleWrite(ctx context.Context, path string, current []byte) staleWriteVerdict {
	verdict := staleWriteVerdict{State: staleWriteStateUnread, CurrentSHA: fileBytesSHA256(current)}
	record, ok := ledgerForSession(toolctx.SessionID(ctx)).lookup(path)
	if !ok {
		return verdict
	}
	verdict.HasRecord = true
	verdict.LastRecord = record
	verdict.LastReadAgo = time.Since(record.ReadAt).Round(time.Second).String()
	if record.SHA256 == verdict.CurrentSHA {
		verdict.State = staleWriteStateFresh
		verdict.Partial = record.Source == "view" && !record.FullRead && record.Window.LinesRead > 0
		return verdict
	}
	verdict.State = staleWriteStateStale
	return verdict
}

// staleWriteFailure refuses an overwrite after an external change and tells the
// model how to proceed. Only write accepts expected_sha256, so the recovery
// hint is tailored per tool: suggesting an argument edit/multiedit ignore would
// send the model into a retry loop.
func staleWriteFailure(path, currentSHA string, record fileReadRecord, expectedSHASupported bool) *toolkit.ToolResult {
	message := fmt.Sprintf(
		"文件自本会话上次读取（%s 前，来源 %s）后已被修改，已拒绝覆盖以防丢失他人改动。请先 view %s 基于最新内容重试。",
		readRecordAge(record),
		firstNonEmpty(record.Source, "unknown"),
		path,
	)
	if expectedSHASupported {
		message += fmt.Sprintf("如确认覆盖，请携带 expected_sha256=%s 显式确认。", currentSHA)
	} else {
		message += "（本工具不支持 expected_sha256 强制覆盖；如确需覆盖请先用 view 刷新认知。）"
	}
	result := writePreconditionFailure(message, currentSHA, record.SHA256)
	if result.Metadata == nil {
		result.Metadata = map[string]interface{}{}
	}
	result.Metadata["stale_write"] = true
	result.Metadata["last_read_at"] = record.ReadAt.UTC().Format(time.RFC3339)
	if record.Source != "" {
		result.Metadata["last_read_source"] = record.Source
	}
	return result
}

func readRecordAge(record fileReadRecord) string {
	if record.ReadAt.IsZero() {
		return "未知时间"
	}
	return time.Since(record.ReadAt).Round(time.Second).String()
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// staleWriteMetadata adds non-blocking ledger context (unread warnings) to a
// successful write result.
func staleWriteMetadata(verdict staleWriteVerdict) map[string]interface{} {
	if verdict.State == staleWriteStateUnread {
		return map[string]interface{}{
			"read_before_write":      "unread",
			"read_before_write_note": "本会话未通过 view 读取过该文件；如需更安全的覆盖，建议先 view 再写。",
		}
	}
	if verdict.State == staleWriteStateStale {
		meta := map[string]interface{}{
			"read_before_write":      "stale",
			"stale_write":            true,
			"read_before_write_note": "文件自本会话上次写入后已被外部修改，本次覆盖基于模型自己的版本；如需基于最新内容编辑，请先 view。",
			"last_read_at":           verdict.LastRecord.ReadAt.UTC().Format(time.RFC3339),
		}
		if verdict.LastRecord.Source != "" {
			meta["last_read_source"] = verdict.LastRecord.Source
		}
		return meta
	}
	if verdict.State == staleWriteStateFresh {
		meta := map[string]interface{}{
			"read_before_write": "fresh",
		}
		if verdict.LastRecord.Source != "" {
			meta["read_before_write_source"] = verdict.LastRecord.Source
		}
		if verdict.Partial {
			window := verdict.LastRecord.Window
			note := fmt.Sprintf(
				"本会话只读取过该文件的部分窗口（offset=%d limit=%d，已显示 %d 行",
				window.Offset, window.Limit, window.LinesRead,
			)
			if window.TotalLines > 0 {
				note += fmt.Sprintf("，共 %d 行", window.TotalLines)
			}
			if window.Truncated {
				note += "，且该窗口被截断"
			}
			note += "）；覆盖不会丢失他人改动（磁盘内容未变），但如需完整上下文请先完整 view。"
			meta["read_before_write"] = "partial"
			meta["read_before_write_note"] = note
			meta["seen_offset"] = window.Offset
			meta["seen_lines"] = window.LinesRead
			if window.TotalLines > 0 {
				meta["seen_total_lines"] = window.TotalLines
			}
		}
		return meta
	}
	return nil
}
