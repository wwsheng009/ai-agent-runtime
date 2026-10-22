package remotetools

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Artifact retention bounds disk growth on the target. A remote artifact is a
// debugging convenience, not durable storage: the local runtime's artifact store
// remains the long-lived copy.
const (
	artifactRetention   = 72 * time.Hour
	artifactPrunePeriod = 6 * time.Hour
)

// ArtifactStore persists full tool payloads that were folded before reaching the
// model.
//
// The local shell tools archive the complete capture and hand the model an
// artifact_read pointer. A remote call had no equivalent, so a folded shell output
// lost its middle permanently (design §D6). This store restores that contract: the
// server archives the full capture under a content-addressed id and exposes
// remote_artifact_read to page it.
type ArtifactStore struct {
	root    string
	maxBytes int64
	lastPrune time.Time
}

// NewArtifactStore prepares the artifact root. An empty root disables archiving.
func NewArtifactStore(root string, maxBytes int64) (*ArtifactStore, error) {
	trimmed := strings.TrimSpace(root)
	if trimmed == "" {
		return nil, nil
	}
	abs, err := filepath.Abs(trimmed)
	if err != nil {
		return nil, fmt.Errorf("resolve artifact root: %w", err)
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return nil, fmt.Errorf("create artifact root: %w", err)
	}
	if maxBytes <= 0 {
		maxBytes = 64 * 1024 * 1024
	}
	return &ArtifactStore{root: abs, maxBytes: maxBytes}, nil
}

// Put stores one payload and returns its artifact id.
//
// The id is a SHA-256 prefix over the content, so identical captures deduplicate
// naturally: re-running the same failing build does not consume disk twice.
func (s *ArtifactStore) Put(payload []byte) (string, error) {
	if s == nil {
		return "", nil
	}
	if int64(len(payload)) > s.maxBytes {
		return "", newToolError(
			CodeOutputLimit,
			"narrow the command so the capture fits artifactMaxBytes",
			"payload is %d bytes, artifact limit is %d", len(payload), s.maxBytes,
		)
	}
	sum := sha256.Sum256(payload)
	id := "art_" + hex.EncodeToString(sum[:16])
	path := s.path(id)
	if _, err := os.Stat(path); err == nil {
		return id, nil // identical payload already archived
	}
	if err := s.pruneIfDue(); err != nil {
		fmt.Fprintf(os.Stderr, "aicli-remote-tools: artifact prune failed: %v\n", err)
	}
	if err := writeFileAtomicRaw(path, payload, 0o600); err != nil {
		return "", fmt.Errorf("write artifact: %w", err)
	}
	return id, nil
}

// Read returns a byte window of one artifact.
//
// offset is 0-based. limit is capped so one call cannot pull an entire multi-MB
// capture into the model context.
func (s *ArtifactStore) Read(id string, offset, limit int) (string, int64, bool, error) {
	if s == nil {
		return "", 0, false, newToolError(CodeUnsupported, "enable artifacts on the target", "artifact store is not enabled")
	}
	id = strings.TrimSpace(id)
	if !isArtifactID(id) {
		return "", 0, false, newToolError(CodeInvalidArgument, "pass the art_… id from the tool output", "invalid artifact id: %q", id)
	}
	file, err := os.Open(s.path(id))
	if err != nil {
		if os.IsNotExist(err) {
			return "", 0, false, newToolError(CodeNotFound, "the artifact may have expired (72h retention); re-run the command", "artifact not found: %s", id)
		}
		return "", 0, false, newToolError(CodeInternal, "retry the call", "open artifact: %v", err)
	}
	defer file.Close()

	info, statErr := file.Stat()
	if statErr != nil {
		return "", 0, false, newToolError(CodeInternal, "retry the call", "stat artifact: %v", statErr)
	}
	size := info.Size()
	if offset < 0 {
		offset = 0
	}
	if int64(offset) > size {
		offset = int(size)
	}
	if limit <= 0 {
		limit = 64 * 1024
	}
	if limit > artifactReadMaxBytes {
		limit = artifactReadMaxBytes
	}
	start := int64(offset)
	remaining := size - start
	if remaining <= 0 {
		return "", size, true, nil
	}
	readLen := int64(limit)
	if readLen > remaining {
		readLen = remaining
	}
	buffer := make([]byte, readLen)
	n, readErr := file.ReadAt(buffer, start)
	if readErr != nil && n == 0 {
		return "", size, false, newToolError(CodeInternal, "retry the call", "read artifact: %v", readErr)
	}
	data := buffer[:n]
	// Starting mid-rune would emit replacement characters; shift to a boundary.
	for len(data) > 0 && !isUTF8Start(data[0]) {
		data = data[1:]
	}
	eof := start+int64(n) >= size
	return string(data), size, eof, nil
}

// artifactReadMaxBytes bounds one read so the model cannot pull a whole capture.
const artifactReadMaxBytes = 64 * 1024

// path returns the on-disk path for an artifact id.
func (s *ArtifactStore) path(id string) string {
	return filepath.Join(s.root, id+".log")
}

// pruneIfDue removes artifacts older than the retention window.
func (s *ArtifactStore) pruneIfDue() error {
	if s == nil {
		return nil
	}
	if time.Since(s.lastPrune) < artifactPrunePeriod {
		return nil
	}
	s.lastPrune = time.Now()
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return err
	}
	cutoff := time.Now().Add(-artifactRetention)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(s.root, entry.Name()))
		}
	}
	return nil
}

// List returns artifact ids, newest first, for diagnostics.
func (s *ArtifactStore) List(limit int) ([]string, error) {
	if s == nil {
		return nil, nil
	}
	entries, err := os.ReadDir(s.root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	type item struct {
		name    string
		modTime time.Time
	}
	items := make([]item, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".log") {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			continue
		}
		items = append(items, item{name: strings.TrimSuffix(entry.Name(), ".log"), modTime: info.ModTime()})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].modTime.After(items[j].modTime) })
	ids := make([]string, 0, len(items))
	for _, entry := range items {
		ids = append(ids, entry.name)
		if limit > 0 && len(ids) >= limit {
			break
		}
	}
	return ids, nil
}

// isArtifactID validates the id shape before it reaches the filesystem.
//
// The id becomes a filename, so rejecting anything outside the generated alphabet
// is what prevents `../../etc/passwd` from being read through artifact_read.
func isArtifactID(id string) bool {
	if !strings.HasPrefix(id, "art_") || len(id) != len("art_")+32 {
		return false
	}
	for _, r := range id[len("art_"):] {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f':
		default:
			return false
		}
	}
	return true
}

// writeFileAtomicRaw writes bytes through a temp file and a rename.
func writeFileAtomicRaw(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, ".aicli-art-*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		_ = os.Remove(tempName)
		return err
	}
	if err := temp.Close(); err != nil {
		_ = os.Remove(tempName)
		return err
	}
	if err := os.Chmod(tempName, mode); err != nil {
		_ = os.Remove(tempName)
		return err
	}
	return os.Rename(tempName, path)
}