package tools

// Unchanged-window dedup for view (analysis §3.6).
//
// Re-reading the exact same window of a file that has not changed is pure
// waste: the content is already in the conversation. The dangerous part is the
// stub pointing at a result the context no longer holds (compaction), so a hit
// is consumed: the stub says "call again and the next call returns the full
// content", and that next call really does, because the entry is gone after
// the first hit. Worst case costs one turn; the alternative costs unboundedly.
//
// Two guards keep this compatible with the read/write ledger invariant:
//   - only complete text windows are remembered (no byte-budget stop, no line
//     clamp, no tail read), because a stub must never hide clamped content;
//   - a from-line-1 re-read is never stubbed until the session ledger holds a
//     full view, so write's read-before-write check can still rely on it.
//
// AICLI_VIEW_DEDUP=off|0|false|disable turns the cache off (kill switch).

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
)

// viewDedupMaxEntries bounds per-session memory; oldest entries are evicted
// first, mirroring the read ledger.
const viewDedupMaxEntries = 2048

type viewDedupEntry struct {
	Path        string
	Offset      int
	Limit       int
	LinesRead   int
	TotalLines  int
	EOF         bool
	ModTimeNano int64
	Size        int64
	ReadAt      time.Time
}

type sessionViewDedup struct {
	mu      sync.Mutex
	entries map[string]viewDedupEntry
	order   []string
	touched time.Time
}

var sessionViewDedups sync.Map // sessionID -> *sessionViewDedup

func viewDedupForSession(sessionID string) *sessionViewDedup {
	maybeSweepSessionReadState()
	if existing, ok := sessionViewDedups.Load(sessionID); ok {
		return existing.(*sessionViewDedup)
	}
	created := &sessionViewDedup{entries: make(map[string]viewDedupEntry)}
	actual, _ := sessionViewDedups.LoadOrStore(sessionID, created)
	return actual.(*sessionViewDedup)
}

func viewDedupKey(path string, offset, limit int) string {
	return normalizeLedgerPath(path) + "\x00" + strconv.Itoa(offset) + "\x00" + strconv.Itoa(limit)
}

// viewDedupDisabled is the kill switch. The env var is read per call so tests
// (and operators) can flip it without a restart.
func viewDedupDisabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("AICLI_VIEW_DEDUP"))) {
	case "0", "off", "false", "disable", "disabled":
		return true
	}
	return false
}

// idleSince reports whether this session bucket was last used before the TTL
// window (review m8).
func (s *sessionViewDedup) idleSince(now time.Time, ttl time.Duration) bool {
	// The sweep is opportunistic: a bucket another goroutine is actively using
	// must be skipped, never waited on. A blocking Lock here could re-enter a
	// mutex the caller already holds (2026-09-27 review).
	if !s.mu.TryLock() {
		return false
	}
	defer s.mu.Unlock()
	if s.touched.IsZero() {
		return false
	}
	return now.Sub(s.touched) > ttl
}

// viewDedupCommit deletes a peeked dedup entry once the stub is really
// delivered. It is nil-safe and re-checks identity, so a stale commit after the
// file changed (or the entry was evicted) is a no-op.
type viewDedupCommit func()

// viewDedupPeek returns a stub when the exact same complete window of an
// unchanged file was already returned to this session. It does NOT consume the
// entry: the caller must run the returned commit after actually handing the
// stub to the model, so a batch item dropped by the aggregate cap does not burn
// the entry (review m13). Consume-on-delivery still preserves the §3.6
// guarantee: the stub tells the model to call again, and the next call returns
// the real content.
func viewDedupPeek(ctx context.Context, path string, info os.FileInfo, offset, limit int) (*toolkit.ToolResult, bool, viewDedupCommit) {
	if viewDedupDisabled() || info == nil || offset < 0 {
		return nil, false, nil
	}
	ledgerPath := normalizeLedgerPath(path)
	if ledgerPath == "" {
		return nil, false, nil
	}
	sessionID := toolctx.SessionID(ctx)
	if sessionID == "" {
		// 空串桶会把"已读"状态跨调用共享，把模型从未见过的窗口 stub 掉；
		// 没有会话标识时直接不做去重。
		return nil, false, nil
	}
	// sessionHasFullRead 会走 ledgerForSession → 摊销清扫；清扫会 Range 所有桶
	// 并调用 idleSince（需要同一把 state.mu）。在持锁状态下调用它会自我死锁：
	// 第 512 次读取会稳定挂住（2026-09-27 review）。因此在加锁之前求值。
	hasFullRead := offset != 0 || sessionHasFullRead(ctx, ledgerPath)
	state := viewDedupForSession(sessionID)
	state.mu.Lock()
	defer state.mu.Unlock()
	state.touched = time.Now()
	key := viewDedupKey(ledgerPath, offset, limit)
	entry, ok := state.entries[key]
	if !ok {
		return nil, false, nil
	}
	if entry.ModTimeNano != info.ModTime().UnixNano() || entry.Size != info.Size() {
		delete(state.entries, key)
		return nil, false, nil
	}
	if offset == 0 && !hasFullRead {
		return nil, false, nil
	}
	metadata := map[string]interface{}{
		"file_path":      ledgerPath,
		"dedup_hit":      true,
		"dedup_consumed": true,
		"offset":         offset,
		"limit":          limit,
		"lines_read":     entry.LinesRead,
		"file_size":      entry.Size,
		"eof":            entry.EOF,
		"is_truncated":   !entry.EOF,
	}
	if entry.TotalLines > 0 {
		metadata["total_lines"] = entry.TotalLines
	}
	// A stub replaces the delivery, so it must keep the continuation contract of
	// the window it stands in for: reporting is_truncated=false without a
	// suggested_next_offset made a repeat read of a truncated window look like a
	// complete one and removed the only route to the rest (2026-09-27 review).
	if !entry.EOF && entry.LinesRead > 0 {
		metadata["suggested_next_offset"] = offset + entry.LinesRead
	}
	// Metadata alone does not reach the model for a successful tool result (the
	// body is passed through as-is), so the continuation must also be stated in
	// the stub text.
	continuation := ""
	if !entry.EOF && entry.LinesRead > 0 {
		continuation = fmt.Sprintf(
			" The window is truncated (is_truncated=true); continue with view offset=%d limit<=%d.",
			offset+entry.LinesRead, viewDefaultLimit,
		)
	}
	stub := &toolkit.ToolResult{
		Success:    true,
		OutputKind: toolresult.KindText,
		Content: fmt.Sprintf(
			"unchanged: %s offset %d limit %d (lines_read=%d) was already returned in this session and the file has not changed since. "+
				"The content is still in the conversation; if it is no longer visible (e.g. after context compaction), call view again and the next call returns the full content.%s",
			path, offset, limit, entry.LinesRead, continuation,
		),
		Metadata: metadata,
	}
	modTimeNano := entry.ModTimeNano
	size := entry.Size
	commit := func() {
		state.mu.Lock()
		defer state.mu.Unlock()
		current, exists := state.entries[key]
		if !exists || current.ModTimeNano != modTimeNano || current.Size != size {
			return
		}
		delete(state.entries, key)
	}
	return stub, true, commit
}

// recordViewWindowRead remembers one complete text window so a later identical
// read can be stubbed. Incomplete windows (byte-budget stop, line clamp, tail
// reads) are never remembered.
func recordViewWindowRead(ctx context.Context, path string, info os.FileInfo, offset, limit, linesRead, totalLines int, eof bool) {
	if viewDedupDisabled() || info == nil || offset < 0 || linesRead <= 0 {
		return
	}
	ledgerPath := normalizeLedgerPath(path)
	if ledgerPath == "" {
		return
	}
	sessionID := toolctx.SessionID(ctx)
	if sessionID == "" {
		return
	}
	state := viewDedupForSession(sessionID)
	state.mu.Lock()
	defer state.mu.Unlock()
	state.touched = time.Now()
	key := viewDedupKey(ledgerPath, offset, limit)
	if _, exists := state.entries[key]; !exists {
		state.order = append(state.order, key)
	}
	state.entries[key] = viewDedupEntry{
		Path:        ledgerPath,
		Offset:      offset,
		Limit:       limit,
		LinesRead:   linesRead,
		TotalLines:  totalLines,
		EOF:         eof,
		ModTimeNano: info.ModTime().UnixNano(),
		Size:        info.Size(),
		ReadAt:      time.Now(),
	}
	for len(state.order) > viewDedupMaxEntries {
		oldest := state.order[0]
		state.order = state.order[1:]
		delete(state.entries, oldest)
	}
}
