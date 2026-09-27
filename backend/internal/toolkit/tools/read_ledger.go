package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
)

const (
	// readLedgerMaxEntries bounds per-session memory; oldest entries are
	// evicted first.
	readLedgerMaxEntries = 4096
	// readLedgerHashMaxBytes caps the file size the ledger hashes exactly (one
	// full read). Larger files used to skip recording entirely, which made a
	// re-view unable to refresh the record: after an external change every
	// edit/multiedit on that file was refused forever (review M2).
	readLedgerHashMaxBytes int64 = 8 << 20
	// readLedgerSampleHashMaxBytes is the largest file the ledger verifies with
	// a head+tail sample. Beyond it the record keeps size/source/window but is
	// marked unverified, and the write side warns instead of refusing.
	readLedgerSampleHashMaxBytes int64 = 512 << 20
	// readLedgerSampleWindowBytes is the head/tail slice hashed in sampled mode.
	readLedgerSampleWindowBytes int64 = 1 << 20
)

const (
	readLedgerHashFull     = "full"
	readLedgerHashSampled  = "sampled"
	readLedgerHashUnverifd = "unverified"
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
	// HashScope records how far SHA256 can be trusted: full (whole file),
	// sampled (size + head/tail slice) or unverified (file too large to hash).
	// Only a full hash supports a hard stale refusal (review M2).
	HashScope string
	// Suspect marks a record whose content read and fingerprint read did not
	// observe the same file state (the file changed in between). The record
	// must never authorize a write: the model saw bytes that no longer match
	// anything the ledger can verify (2026-09-27 review H20).
	Suspect bool
}

type sessionReadLedger struct {
	sessionID string
	mu        sync.Mutex
	entries   map[string]fileReadRecord
	order     []string
	touched   time.Time
	// evicted is set by the idle sweep under the bucket lock. A bucket marked
	// evicted must not accept or serve records any more: callers re-fetch from
	// the map so a sweep cannot silently drop the record a caller is about to
	// write (2026-09-27 review H19).
	evicted atomic.Bool
}

var sessionReadLedgers sync.Map // sessionID -> *sessionReadLedger

func ledgerForSession(sessionID string) *sessionReadLedger {
	maybeSweepSessionReadState()
	for attempt := 0; attempt < 8; attempt++ {
		if existing, ok := sessionReadLedgers.Load(sessionID); ok {
			ledger := existing.(*sessionReadLedger)
			if !ledger.evicted.Load() {
				return ledger
			}
			sessionReadLedgers.CompareAndDelete(sessionID, existing)
			continue
		}
		created := &sessionReadLedger{sessionID: sessionID, entries: make(map[string]fileReadRecord)}
		actual, _ := sessionReadLedgers.LoadOrStore(sessionID, created)
		ledger := actual.(*sessionReadLedger)
		if !ledger.evicted.Load() {
			return ledger
		}
	}
	// Degenerate contention (the same bucket was evicted on every attempt):
	// hand back an unstored bucket so this call stays correct even if its
	// record is not shared with concurrent calls.
	return &sessionReadLedger{sessionID: sessionID, entries: make(map[string]fileReadRecord)}
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
	for attempt := 0; ; attempt++ {
		l.mu.Lock()
		if !l.evicted.Load() {
			l.touched = time.Now()
			if _, exists := l.entries[path]; !exists {
				l.order = append(l.order, path)
			}
			l.entries[path] = record
			for len(l.order) > readLedgerMaxEntries {
				oldest := l.order[0]
				l.order = l.order[1:]
				delete(l.entries, oldest)
			}
			l.mu.Unlock()
			return
		}
		l.mu.Unlock()
		if attempt >= 2 || l.sessionID == "" {
			return
		}
		l = ledgerForSession(l.sessionID)
	}
}

func (l *sessionReadLedger) lookup(path string) (fileReadRecord, bool) {
	path = normalizeLedgerPath(path)
	if path == "" {
		return fileReadRecord{}, false
	}
	for attempt := 0; ; attempt++ {
		l.mu.Lock()
		if !l.evicted.Load() {
			l.touched = time.Now()
			record, ok := l.entries[path]
			l.mu.Unlock()
			return record, ok
		}
		l.mu.Unlock()
		if attempt >= 2 || l.sessionID == "" {
			return fileReadRecord{}, false
		}
		l = ledgerForSession(l.sessionID)
	}
}

// idleSince reports whether this session bucket was last used before the TTL
// window; the sweeper drops such buckets so a long-lived process does not
// accumulate per-session state (review m8).
func (l *sessionReadLedger) idleSince(now time.Time, ttl time.Duration) bool {
	// Same contract as the view-dedup sweep: skip busy buckets instead of
	// blocking on them, so an amortized sweep can never deadlock a caller that
	// already holds the bucket (2026-09-27 review).
	if !l.mu.TryLock() {
		return false
	}
	defer l.mu.Unlock()
	if l.touched.IsZero() {
		return false
	}
	if now.Sub(l.touched) <= ttl {
		return false
	}
	// Mark under the lock so concurrent users re-fetch a fresh bucket instead
	// of writing a record the sweep is about to drop (2026-09-27 review H19).
	l.evicted.Store(true)
	return true
}

// ledgerHashScope decides how much of a file the ledger can verify.
func ledgerHashScope(size int64) string {
	switch {
	case size <= readLedgerHashMaxBytes:
		return readLedgerHashFull
	case size <= readLedgerSampleHashMaxBytes:
		return readLedgerHashSampled
	default:
		return readLedgerHashUnverifd
	}
}

// hashFileSample hashes the file size plus a head and tail slice. Size is part
// of the digest, so any length change is always detected; middle edits of a
// huge file can hide, which is why sampled records never hard-refuse a write.
func hashFileSample(path string, size int64) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hasher := sha256.New()
	fmt.Fprintf(hasher, "size=%d\n", size)
	window := readLedgerSampleWindowBytes
	if window > size {
		window = size
	}
	if window > 0 {
		head := make([]byte, window)
		if _, err := io.ReadFull(file, head); err != nil && err != io.ErrUnexpectedEOF {
			return "", err
		}
		hasher.Write(head)
	}
	if size > window {
		if _, err := file.Seek(-window, io.SeekEnd); err != nil {
			return "", err
		}
		tail := make([]byte, window)
		if _, err := io.ReadFull(file, tail); err != nil && err != io.ErrUnexpectedEOF {
			return "", err
		}
		hasher.Write(tail)
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// hashSampleBytes is hashFileSample's in-memory twin, used when the write side
// already holds the current file bytes: a sampled record must be compared
// against the same digest scheme, not against the full-file SHA (review M2).
func hashSampleBytes(data []byte) string {
	size := int64(len(data))
	hasher := sha256.New()
	fmt.Fprintf(hasher, "size=%d\n", size)
	window := readLedgerSampleWindowBytes
	if window > size {
		window = size
	}
	if window > 0 {
		hasher.Write(data[:window])
	}
	if size > window {
		hasher.Write(data[size-window:])
	}
	return hex.EncodeToString(hasher.Sum(nil))
}

// recordFileReadFromDisk records the current disk state of path for the active
// session. It is best-effort: files above readLedgerHashMaxBytes get a sampled
// digest, and files above readLedgerSampleHashMaxBytes are recorded with an
// explicitly unverified hash instead of being skipped (review M2).
func recordFileReadFromDisk(ctx context.Context, path string, fullRead bool, source string, window fileReadWindow) {
	recordFileReadFromDiskObserved(ctx, path, fullRead, source, window, nil)
}

// recordFileReadFromDiskObserved is recordFileReadFromDisk with the FileInfo
// the caller sampled before reading the content. When the file changed between
// that observation and the fingerprint read, the record is marked Suspect so
// it can never authorize an overwrite of content the model never saw (review
// H20).
func recordFileReadFromDiskObserved(ctx context.Context, path string, fullRead bool, source string, window fileReadWindow, observed os.FileInfo) {
	if strings.TrimSpace(path) == "" {
		return
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return
	}
	record := fileReadRecord{
		Size:      info.Size(),
		FullRead:  fullRead,
		ReadAt:    time.Now(),
		Source:    source,
		Window:    window,
		HashScope: ledgerHashScope(info.Size()),
	}
	if observed != nil && (observed.Size() != info.Size() || !observed.ModTime().Equal(info.ModTime())) {
		record.Suspect = true
	}
	switch record.HashScope {
	case readLedgerHashFull:
		data, err := os.ReadFile(path)
		if err != nil {
			return
		}
		record.SHA256 = fileBytesSHA256(data)
		record.Size = int64(len(data))
	case readLedgerHashSampled:
		sum, err := hashFileSample(path, info.Size())
		if err != nil {
			return
		}
		record.SHA256 = sum
	}
	ledger := ledgerForSession(toolctx.SessionID(ctx))
	// FullRead is monotonic: a full read followed by a partial window must not
	// demote the record, or the dedup guard and the write side lose the
	// "session has seen the whole file" fact (review m7). The older full read
	// only counts while it still describes the same bytes under the same
	// digest scheme: comparing a full SHA with a sampled digest across scopes
	// always differs and used to clear the flag on every partial view of a
	// large file (review H17).
	if !record.FullRead && record.SHA256 != "" {
		if existing, ok := ledger.lookup(path); ok && existing.FullRead &&
			existing.HashScope == record.HashScope && existing.SHA256 == record.SHA256 && !existing.Suspect {
			record.FullRead = true
		}
	}
	ledger.record(path, record)
}

// sessionHasFullRead reports whether this session holds a complete view of the
// path (a full view read or a write it performed itself). The view dedup guard
// uses it to keep from-line-1 re-reads returning real content until the write
// side can rely on a full view (analysis §3.6).
func sessionHasFullRead(ctx context.Context, path string) bool {
	record, ok := ledgerForSession(toolctx.SessionID(ctx)).lookup(path)
	return ok && record.FullRead && record.SHA256 != "" && record.HashScope != readLedgerHashUnverifd
}

// recordFileWrite records bytes written by a tool from this session so a
// follow-up edit or overwrite is not mistaken for an external change.
func recordFileWrite(ctx context.Context, path string, data []byte, source string) {
	if strings.TrimSpace(path) == "" {
		return
	}
	size := int64(len(data))
	scope := ledgerHashScope(size)
	record := fileReadRecord{
		Size:      size,
		FullRead:  true,
		ReadAt:    time.Now(),
		Source:    source,
		HashScope: scope,
	}
	// The digest must use the same scheme a later view will use for this file
	// size, otherwise the FullRead inheritance compares a full SHA with a
	// sampled digest, always finds them different and demotes the record
	// (review H17).
	switch scope {
	case readLedgerHashFull:
		record.SHA256 = fileBytesSHA256(data)
	case readLedgerHashSampled:
		record.SHA256 = hashSampleBytes(data)
	}
	ledgerForSession(toolctx.SessionID(ctx)).record(path, record)
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
	// Sampled marks a verdict backed only by the head/tail digest of a large
	// file: an external middle edit can hide, so it never hard-refuses.
	Sampled bool
}

const (
	staleWriteStateFresh      = "fresh"
	staleWriteStateUnread     = "unread"
	staleWriteStateStale      = "stale"
	staleWriteStateUnverified = "unverified"
)

// shouldRefuseStaleWrite keeps the hard guarantee scoped to files the model
// explicitly read (view). When the last observation came from our own write,
// an external change is surfaced as a warning instead of a hard refusal so
// common edit → formatter → edit loops do not dead-end.
func shouldRefuseStaleWrite(verdict staleWriteVerdict) bool {
	// A sampled record still proves the file changed at its size/head/tail, and
	// unlike the old skip-recording behavior a re-view refreshes it, so the
	// refusal cannot dead-end; only a record with no hash at all downgrades to a
	// warning (review M2).
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
	if record.Suspect {
		// The content read and the fingerprint read observed different file
		// states: the model's view cannot be matched against disk, so never
		// treat this record as fresh (review H20).
		verdict.State = staleWriteStateStale
		return verdict
	}
	if record.SHA256 == "" {
		// 超大文件账本只记录窗口、不做内容哈希：既不能宣称 fresh，也不能把
		// 重新 view 之后的写入永久拒掉（review M2）。
		verdict.State = staleWriteStateUnverified
		return verdict
	}
	// 采样记录必须用同一套摘要方案比对（大小 + 头/尾切片），拿全量 SHA 去比
	// 会让重新 view 之后依然永远 stale（review M2）。
	matches := false
	if record.HashScope == readLedgerHashSampled {
		verdict.Sampled = true
		matches = record.SHA256 == hashSampleBytes(current)
	} else {
		matches = record.SHA256 == verdict.CurrentSHA
	}
	if matches {
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
	if record.Suspect {
		result.Metadata["read_race_detected"] = true
		result.Metadata["read_race_note"] = "该文件在内容读取与指纹记录之间发生了变化，本会话看到的正文可能已不是磁盘当前版本；请重新 view 后再写。"
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
		if verdict.Sampled {
			meta["read_before_write_check"] = "sampled"
			meta["read_before_write_note"] = "该文件超过全量哈希上限，本会话只比对了头部/尾部采样与文件大小；已检测到外部变化并拒绝覆盖。请先 view（会刷新采样哈希）后重试。"
		}
		return meta
	}
	if verdict.State == staleWriteStateUnverified {
		return map[string]interface{}{
			"read_before_write":       "unverified",
			"read_before_write_check": "unverified",
			"read_before_write_note":  "该文件超过采样哈希上限，本会话无法校验其内容是否被外部修改；覆盖不会被拒绝，但请先 view 或人工确认。",
		}
	}
	if verdict.State == staleWriteStateFresh {
		meta := map[string]interface{}{
			"read_before_write": "fresh",
		}
		if verdict.LastRecord.Source != "" {
			meta["read_before_write_source"] = verdict.LastRecord.Source
		}
		if verdict.Sampled {
			meta["read_before_write_check"] = "sampled"
			meta["read_before_write_note"] = "该文件超过全量哈希上限，本会话只比对头部/尾部采样与文件大小；文件中部的外部改动不会被发现。"
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
