package mesh

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Journal event kinds (architecture §3.4). The journal is append-only NDJSON,
// one file per writer: mesh/journal/<node_id>.ndjson.
const (
	JournalNodeStarted        = "node.started"
	JournalNodeStopped        = "node.stopped"
	JournalSessionActivated   = "session.activated"
	JournalSessionDeactivated = "session.deactivated"
	JournalBusyChanged        = "busy.changed"
	JournalLeaseAcquired      = "lease.acquired"
	JournalLeaseReleased      = "lease.released"
	JournalLeaseReclaimed     = "lease.reclaimed"
	JournalLeaseDegraded      = "lease.degraded"
	JournalCallReceived       = "mesh.call.received"
	JournalCallCompleted      = "mesh.call.completed"
	JournalCallSent           = "mesh.call.sent"
	JournalSpawnRequested     = "mesh.spawn.requested"
	JournalSpawnCompleted     = "mesh.spawn.completed"
	JournalPeerObserved       = "mesh.peer.observed"
)

const (
	// JournalMaxBytes is the rotation threshold of a single node journal:
	// crossing it renames the file to <name>.1 (one generation, §3.4).
	JournalMaxBytes = 8 << 20
	// journalRotatedSuffix is appended to the rotated generation.
	journalRotatedSuffix = ".1"
)

// JournalEntry is one NDJSON line of the mesh journal.
type JournalEntry struct {
	TS        time.Time      `json:"ts"`
	NodeID    string         `json:"node_id"`
	Seq       uint64         `json:"seq"`
	Kind      string         `json:"kind"`
	SessionID string         `json:"session_id,omitempty"`
	Detail    map[string]any `json:"detail,omitempty"`
}

// Journal is one node's append-only event log. It also owns the node's event
// sequence counter, which the SSE fan-in shares ("seq 与 journal seq 同源同
// 计数器", architecture §6.3).
//
// All methods are nil-safe: a disabled journal (no mesh root) silently drops
// entries instead of failing the caller.
type Journal struct {
	mu       sync.Mutex
	path     string
	nodeID   string
	seq      uint64
	now      func() time.Time
	maxBytes int64
	failed   bool
	dirReady bool
	// secrets are literal strings scrubbed from every logged value (the write
	// token must never reach the journal — assertion M7).
	secrets []string
}

// OpenJournal returns the journal writer for nodeID (nil when the mesh root is
// unavailable: journaling degrades to a no-op).
func OpenJournal(paths Paths, nodeID string, now func() time.Time) *Journal {
	path := paths.JournalPath(nodeID)
	if path == "" {
		return nil
	}
	if now == nil {
		now = NowUTC
	}
	return &Journal{path: path, nodeID: nodeID, now: now, maxBytes: JournalMaxBytes}
}

// Path returns the journal file path ("" for a nil journal).
func (j *Journal) Path() string {
	if j == nil {
		return ""
	}
	return j.path
}

// NodeID returns the owning node id.
func (j *Journal) NodeID() string {
	if j == nil {
		return ""
	}
	return j.nodeID
}

// NextSeq allocates the next event sequence number of this node. The SSE
// fan-in uses the same counter so frames and journal lines stay comparable.
func (j *Journal) NextSeq() uint64 {
	if j == nil {
		return 0
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.seq++
	return j.seq
}

// SetSecrets registers literal strings that must never appear in the journal
// (the write token). Safe to call repeatedly.
func (j *Journal) SetSecrets(values ...string) {
	if j == nil {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		j.secrets = append(j.secrets, value)
	}
}

// Append writes one entry and returns its sequence number (0 when the entry
// was dropped). Failures disable the journal for the rest of the process —
// losing audit data must never affect the chat (degradation matrix §4.7).
func (j *Journal) Append(kind, sessionID string, detail map[string]any) uint64 {
	if j == nil {
		return 0
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.failed {
		return 0
	}
	j.seq++
	entry := JournalEntry{
		TS:        j.now(),
		NodeID:    j.nodeID,
		Seq:       j.seq,
		Kind:      strings.TrimSpace(kind),
		SessionID: strings.TrimSpace(sessionID),
		Detail:    j.sanitizeDetailLocked(detail),
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return 0
	}
	data = append(data, '\n')
	if err := j.ensureDirLocked(); err != nil {
		j.failed = true
		return 0
	}
	if err := j.rotateIfNeededLocked(); err != nil {
		j.failed = true
		return 0
	}
	file, err := os.OpenFile(j.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		j.failed = true
		return 0
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		j.failed = true
		return 0
	}
	if err := file.Close(); err != nil {
		j.failed = true
		return 0
	}
	return entry.Seq
}

// Failed reports whether journaling gave up (diagnostics only).
func (j *Journal) Failed() bool {
	if j == nil {
		return false
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.failed
}

func (j *Journal) rotateIfNeededLocked() error {
	info, err := os.Stat(j.path)
	if err != nil || info.Size() < j.maxBytes {
		return nil
	}
	rotated := j.path + journalRotatedSuffix
	_ = os.Remove(rotated)
	return os.Rename(j.path, rotated)
}

// ensureDirLocked creates the journal directory on first use (once per
// process). A journal writer that is constructed directly — without going
// through Paths.EnsureDirs — must still be able to append.
func (j *Journal) ensureDirLocked() error {
	if j.dirReady {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(j.path), 0o700); err != nil {
		return err
	}
	j.dirReady = true
	return nil
}

// sanitizeDetailLocked replaces secret-keyed values and scrubs registered
// secrets out of every string value (including nested maps/slices).
func (j *Journal) sanitizeDetailLocked(detail map[string]any) map[string]any {
	if len(detail) == 0 {
		return nil
	}
	out := make(map[string]any, len(detail))
	for key, value := range detail {
		if isSecretKey(key) {
			out[key] = "[redacted]"
			continue
		}
		out[key] = j.scrubLocked(value)
	}
	return out
}

func (j *Journal) scrubLocked(value any) any {
	switch typed := value.(type) {
	case string:
		return j.scrubStringLocked(typed)
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, nested := range typed {
			if isSecretKey(key) {
				out[key] = "[redacted]"
				continue
			}
			out[key] = j.scrubLocked(nested)
		}
		return out
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, j.scrubLocked(item))
		}
		return out
	default:
		return value
	}
}

func (j *Journal) scrubStringLocked(value string) string {
	for _, secret := range j.secrets {
		if secret != "" && strings.Contains(value, secret) {
			value = strings.ReplaceAll(value, secret, "[redacted]")
		}
	}
	return value
}

func isSecretKey(key string) bool {
	lower := strings.ToLower(strings.TrimSpace(key))
	return strings.Contains(lower, "token") ||
		strings.Contains(lower, "secret") ||
		strings.Contains(lower, "password")
}

// ReadJournalEntries reads at most limit entries from a journal file, newest
// first is NOT applied — the order is the file order. Malformed lines are
// skipped (the file is append-only; a torn tail line must not break the read).
func ReadJournalEntries(path string, limit int) ([]JournalEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(data), "\n")
	entries := make([]JournalEntry, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var entry JournalEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		entries = append(entries, entry)
		if limit > 0 && len(entries) >= limit {
			break
		}
	}
	return entries, nil
}

// FormatJournalEntry renders one entry as a single human-readable line.
func FormatJournalEntry(entry JournalEntry) string {
	ts := entry.TS.UTC().Format(time.RFC3339)
	detail := ""
	if len(entry.Detail) > 0 {
		if data, err := json.Marshal(entry.Detail); err == nil {
			detail = " " + string(data)
		}
	}
	session := ""
	if entry.SessionID != "" {
		session = " " + entry.SessionID
	}
	return fmt.Sprintf("%s #%d %s%s%s", ts, entry.Seq, entry.Kind, session, detail)
}
