package mesh

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Journal watch (`aicli-mesh watch`, architecture §7.2 / §6.6).
//
// 设计红线：watch 只读文件，**不依赖任何节点存活**——这正是它与
// `/web/api/mesh/events`（SSE 扇入，需要双方进程都在）的分工：进程全退之后
// 仍然能复盘（§6.6 表格）。
const (
	// WatchDefaultSince is the default replay window (`--since 10m`).
	WatchDefaultSince = 10 * time.Minute
	// WatchDefaultInterval is the follow-mode poll interval. The architecture
	// pins journal tail polling to 500ms–1s (§6.6); the lower bound keeps the
	// perceived latency sub-second without busy-looping.
	WatchDefaultInterval = 500 * time.Millisecond
	// journalLiveSuffix is the live journal file of one node.
	journalLiveSuffix = ".ndjson"
	// journalRotatedSuffixExt is the single rotated generation (journal.go).
	journalRotatedSuffixExt = journalLiveSuffix + journalRotatedSuffix
)

// ErrMeshPathsUnavailable is returned when the mesh root cannot be resolved
// (fail-closed: watch never falls back to the current directory).
var ErrMeshPathsUnavailable = errors.New("mesh root unavailable (set AICLI_MESH_DIR / AICLI_HOME)")

// WatchOptions describes one `aicli-mesh watch` invocation.
type WatchOptions struct {
	// Since limits the replay window; <= 0 means "everything on disk".
	Since time.Duration
	// NodeID filters by node id (exact or case-insensitive prefix).
	NodeID string
	// SessionID filters by session id (exact or case-insensitive prefix).
	SessionID string
	// Follow keeps tailing after the replay (default for the CLI).
	Follow bool
	// Interval is the follow poll interval (WatchDefaultInterval when 0).
	Interval time.Duration
	// Limit caps the replay to the newest N events (0 = unlimited).
	Limit int
	// Now is the clock (tests inject a fake).
	Now func() time.Time
}

// WatchEvent is one journal line plus the file it was read from.
type WatchEvent struct {
	Entry JournalEntry
	File  string
}

func (o WatchOptions) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return NowUTC()
}

func (o WatchOptions) interval() time.Duration {
	if o.Interval > 0 {
		return o.Interval
	}
	return WatchDefaultInterval
}

// JournalFiles lists the journal files of this machine: the live
// `journal/<node>.ndjson` of every node plus the one rotated generation
// (`<node>.ndjson.1`, journal.go). includeRotated=false is used by follow mode,
// where re-reading the rotated generation would duplicate entries.
//
// A missing journal directory is not an error: a machine that never ran a node
// has nothing to watch.
func JournalFiles(paths Paths, includeRotated bool) ([]string, error) {
	if !paths.Enabled() {
		return nil, ErrMeshPathsUnavailable
	}
	entries, err := os.ReadDir(paths.Journal)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		switch {
		case strings.HasSuffix(name, journalLiveSuffix):
		case includeRotated && strings.HasSuffix(name, journalRotatedSuffixExt):
		default:
			continue
		}
		out = append(out, filepath.Join(paths.Journal, name))
	}
	sort.Strings(out)
	return out, nil
}

// CollectJournalEvents reads the replay window once: every matching journal
// file, filtered by --node / --session / --since, merged in timestamp order
// (ties broken by node id then seq so the output is deterministic), de-duped by
// (node_id, seq) and capped to the newest Limit events.
//
// A torn tail line (a writer died mid-append) is skipped by
// ReadJournalEntries; a fully unreadable file fails the call — the caller
// reports it instead of silently showing a partial audit.
func CollectJournalEvents(paths Paths, opts WatchOptions) ([]WatchEvent, error) {
	files, err := JournalFiles(paths, true)
	if err != nil {
		return nil, err
	}
	cutoff := time.Time{}
	if opts.Since > 0 {
		cutoff = opts.now().Add(-opts.Since)
	}
	events := make([]WatchEvent, 0, 64)
	seen := make(map[string]struct{})
	for _, file := range files {
		if !journalFileMatchesNode(file, opts.NodeID) {
			continue
		}
		entries, err := ReadJournalEntries(file, 0)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if !watchEntryMatches(entry, opts, cutoff) {
				continue
			}
			key := watchEventKey(entry)
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			events = append(events, WatchEvent{Entry: entry, File: file})
		}
	}
	sortWatchEvents(events)
	if opts.Limit > 0 && len(events) > opts.Limit {
		events = events[len(events)-opts.Limit:]
	}
	return events, nil
}

// WatchJournal replays the window and, when Follow is set, keeps tailing every
// live journal file until ctx is cancelled. emit is called in file order per
// poll (the replay is already merged and sorted); an emit error stops the watch
// and is returned unchanged (the CLI maps it to a failure exit code).
//
// Cancelling ctx is not an error: watch is a foreground command and Ctrl-C is
// its normal way to end (the CLI turns context.Canceled into exit code 0).
func WatchJournal(ctx context.Context, paths Paths, opts WatchOptions, emit func(WatchEvent) error) error {
	events, err := CollectJournalEvents(paths, opts)
	if err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(events))
	for _, event := range events {
		seen[watchEventKey(event.Entry)] = struct{}{}
		if err := emit(event); err != nil {
			return err
		}
	}
	if !opts.Follow {
		return nil
	}

	files, err := JournalFiles(paths, false)
	if err != nil {
		return err
	}
	offsets := make(map[string]int64, len(files))
	for _, file := range files {
		if info, statErr := os.Stat(file); statErr == nil {
			offsets[file] = info.Size()
		}
	}

	ticker := time.NewTicker(opts.interval())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}

		files, err = JournalFiles(paths, false)
		if err != nil {
			return err
		}
		for _, file := range files {
			if !journalFileMatchesNode(file, opts.NodeID) {
				continue
			}
			entries, next, err := readJournalEntriesFrom(file, offsets[file])
			if err != nil {
				if os.IsNotExist(err) {
					// The node exited and gc removed its journal: keep watching
					// the others instead of failing the whole stream.
					delete(offsets, file)
					continue
				}
				return err
			}
			offsets[file] = next
			for _, entry := range entries {
				if !watchEntryMatches(entry, opts, time.Time{}) {
					continue
				}
				key := watchEventKey(entry)
				if _, dup := seen[key]; dup {
					continue
				}
				seen[key] = struct{}{}
				if err := emit(WatchEvent{Entry: entry, File: file}); err != nil {
					return err
				}
			}
		}
	}
}

// WatchEventKey identifies one journal entry across files and polls: a node
// process writes its own file with a monotonic seq, so (node_id, seq) is unique
// for the lifetime of that process (a restart gets a new node id).
func watchEventKey(entry JournalEntry) string {
	return entry.NodeID + "#" + formatSeq(entry.Seq)
}

func formatSeq(seq uint64) string {
	if seq == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for seq > 0 {
		pos--
		buf[pos] = byte('0' + seq%10)
		seq /= 10
	}
	return string(buf[pos:])
}

func watchEntryMatches(entry JournalEntry, opts WatchOptions, cutoff time.Time) bool {
	if !cutoff.IsZero() && entry.TS.Before(cutoff) {
		return false
	}
	if filter := strings.TrimSpace(opts.NodeID); filter != "" && !hasFoldPrefix(entry.NodeID, filter) {
		return false
	}
	if filter := strings.TrimSpace(opts.SessionID); filter != "" && !hasFoldPrefix(entry.SessionID, filter) {
		return false
	}
	return true
}

// journalFileMatchesNode applies the --node filter at file level: journal files
// are named after their writer, so a non-matching file never needs to be read.
func journalFileMatchesNode(file, filter string) bool {
	filter = strings.TrimSpace(filter)
	if filter == "" {
		return true
	}
	name := filepath.Base(file)
	name = strings.TrimSuffix(name, journalRotatedSuffixExt)
	name = strings.TrimSuffix(name, journalLiveSuffix)
	return hasFoldPrefix(name, filter)
}

func sortWatchEvents(events []WatchEvent) {
	sort.SliceStable(events, func(i, j int) bool {
		left, right := events[i].Entry, events[j].Entry
		if !left.TS.Equal(right.TS) {
			return left.TS.Before(right.TS)
		}
		if left.NodeID != right.NodeID {
			return left.NodeID < right.NodeID
		}
		return left.Seq < right.Seq
	})
}

// readJournalEntriesFrom reads the complete lines appended after offset and
// returns the new offset. A partial trailing line (writer mid-append) is left
// for the next poll: consuming it would drop the entry from the stream.
func readJournalEntriesFrom(path string, offset int64) ([]JournalEntry, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, offset, err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return nil, offset, err
	}
	if offset < 0 || offset > info.Size() {
		// Truncated or replaced (rotation): restart from the beginning.
		offset = 0
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return nil, offset, err
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return nil, offset, err
	}
	consumed := int64(len(data))
	if idx := lastNewlineIndex(data); idx < 0 {
		return nil, offset, nil
	} else {
		consumed = int64(idx + 1)
		data = data[:consumed]
	}
	entries := make([]JournalEntry, 0, 16)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var entry JournalEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		entries = append(entries, entry)
	}
	return entries, offset + consumed, nil
}

func lastNewlineIndex(data []byte) int {
	for i := len(data) - 1; i >= 0; i-- {
		if data[i] == '\n' {
			return i
		}
	}
	return -1
}
