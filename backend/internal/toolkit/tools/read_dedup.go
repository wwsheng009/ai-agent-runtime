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
}

var sessionViewDedups sync.Map // sessionID -> *sessionViewDedup

func viewDedupForSession(sessionID string) *sessionViewDedup {
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

// viewDedupStub returns a consumed stub when the exact same complete window of
// an unchanged file was already returned to this session. The entry is deleted
// on hit (consume-on-hit), so a retry after context compaction gets the real
// content instead of a second stub.
func viewDedupStub(ctx context.Context, path string, info os.FileInfo, offset, limit int) (*toolkit.ToolResult, bool) {
	if viewDedupDisabled() || info == nil || offset < 0 {
		return nil, false
	}
	ledgerPath := normalizeLedgerPath(path)
	if ledgerPath == "" {
		return nil, false
	}
	sessionID := toolctx.SessionID(ctx)
	if sessionID == "" {
		// 空串桶会把"已读"状态跨调用共享，把模型从未见过的窗口 stub 掉；
		// 没有会话标识时直接不做去重。
		return nil, false
	}
	state := viewDedupForSession(sessionID)
	state.mu.Lock()
	defer state.mu.Unlock()
	key := viewDedupKey(ledgerPath, offset, limit)
	entry, ok := state.entries[key]
	if !ok {
		return nil, false
	}
	if entry.ModTimeNano != info.ModTime().UnixNano() || entry.Size != info.Size() {
		delete(state.entries, key)
		return nil, false
	}
	if offset == 0 && !sessionHasFullRead(ctx, ledgerPath) {
		return nil, false
	}
	delete(state.entries, key)
	stub := &toolkit.ToolResult{
		Success:    true,
		OutputKind: toolresult.KindText,
		Content: fmt.Sprintf(
			"unchanged: %s offset %d limit %d (lines_read=%d) was already returned in this session and the file has not changed since. "+
				"The content is still in the conversation; if it is no longer visible (e.g. after context compaction), call view again and the next call returns the full content.",
			path, offset, limit, entry.LinesRead,
		),
		Metadata: map[string]interface{}{
			"file_path":      ledgerPath,
			"dedup_hit":      true,
			"dedup_consumed": true,
			"offset":         offset,
			"limit":          limit,
			"lines_read":     entry.LinesRead,
			"file_size":      entry.Size,
			"eof":            entry.EOF,
			"is_truncated":   false,
		},
	}
	return stub, true
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
