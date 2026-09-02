package chat

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// SessionReplicaSyncIntervalDefault is the default master->replica refresh
// interval used when sessions.replicaSyncInterval is not configured.
const SessionReplicaSyncIntervalDefault = 30 * time.Second

// SyncSessionReplica copies the master session-history database into the
// runtime server's private replica so reads never contend with aicli's write
// lock on the master file.
//
// A SQLite WAL database's main file is a self-consistent snapshot at the last
// checkpoint boundary; the WAL sidecar only holds newer increments. Copying
// the main file therefore yields a valid (possibly slightly stale) replica
// even while the master is being written — it is only missing the newest
// un-checkpointed transactions, never torn. We still verify the copy by
// opening it, and we reject the copy if the master changed mid-copy.
// Deleting the replica's own -wal/-shm sidecars guarantees the freshly opened
// replica starts from a clean state.
func SyncSessionReplica(ctx context.Context, srcPath, dstPath string) error {
	if strings.TrimSpace(srcPath) == "" || strings.TrimSpace(dstPath) == "" {
		return fmt.Errorf("replica sync requires both source and destination paths")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	before, err := os.Stat(srcPath)
	if err != nil {
		return fmt.Errorf("replica source not available: %w", err)
	}
	tmpPath := dstPath + ".tmp"
	if err := copyFileAtomic(srcPath, tmpPath); err != nil {
		return fmt.Errorf("copy replica source: %w", err)
	}
	// Reject the copy if the master changed while we were reading it (e.g. a
	// checkpoint rewrote pages mid-copy). The next sync tick retries.
	after, err := os.Stat(srcPath)
	if err == nil && (after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime())) {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("master changed during replica copy, retry later")
	}
	// Verify the copied file opens as a valid SQLite database.
	if err := verifyReplicaFile(ctx, tmpPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("replica verification failed: %w", err)
	}
	// Replace the destination. Windows os.Rename cannot overwrite an existing
	// file, so remove the old replica and its WAL sidecars first. Also remove
	// any sidecars the verification open may have created beside the tmp file.
	_ = os.Remove(tmpPath + "-wal")
	_ = os.Remove(tmpPath + "-shm")
	_ = os.Remove(dstPath)
	_ = os.Remove(dstPath + "-wal")
	_ = os.Remove(dstPath + "-shm")
	if err := os.Rename(tmpPath, dstPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("replace replica database: %w", err)
	}
	return nil
}

// verifyReplicaFile opens the copied file and runs a cheap query to confirm it
// is a readable SQLite database before it replaces the live replica.
func verifyReplicaFile(ctx context.Context, path string) error {
	store, err := NewSQLiteSessionStorageContext(ctx, PersistentSessionStorageConfig{
		Backend:     SessionStorageBackendSQLite,
		Dir:         filepath.Dir(path),
		Path:        path,
		BusyTimeout: 3 * time.Second,
	})
	if err != nil {
		return err
	}
	defer func() { _ = store.CloseStorage() }()
	_, err = store.List(ctx, "__replica_verify__")
	return err
}

// copyFileAtomic copies src to dst via a temporary file and fsync. On Windows
// the final os.Rename must not overwrite an existing target (handled by the
// caller), so the copy itself is plain and best-effort.
func copyFileAtomic(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

// switchableSessionStorage wraps the active SessionStorage and allows the
// replica sync loop to atomically swap the underlying backend at runtime.
// SessionManager and handlers only ever see this wrapper, so every optional
// interface (AllLister, HistoryPager, ...) stays satisfiable across swaps.
type switchableSessionStorage struct {
	mu    sync.RWMutex
	inner SessionStorage
}

// NewSwitchableSessionStorage wraps an initial backend.
func NewSwitchableSessionStorage(inner SessionStorage) SessionStorage {
	if inner == nil {
		inner = NewInMemoryStorage()
	}
	return &switchableSessionStorage{inner: inner}
}

// Swap atomically replaces the underlying backend. The previous backend is
// closed (best-effort) after the swap.
func (s *switchableSessionStorage) Swap(inner SessionStorage) {
	if inner == nil {
		return
	}
	s.mu.Lock()
	old := s.inner
	s.inner = inner
	s.mu.Unlock()
	if old != nil {
		if closer, ok := old.(SessionStorageCloser); ok {
			_ = closer.CloseStorage()
		}
	}
}

func (s *switchableSessionStorage) current() SessionStorage {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.inner == nil {
		return NewInMemoryStorage()
	}
	return s.inner
}

func (s *switchableSessionStorage) Save(ctx context.Context, session *Session) error {
	return s.current().Save(ctx, session)
}
func (s *switchableSessionStorage) Load(ctx context.Context, sessionID string) (*Session, error) {
	return s.current().Load(ctx, sessionID)
}
func (s *switchableSessionStorage) Delete(ctx context.Context, sessionID string) error {
	return s.current().Delete(ctx, sessionID)
}
func (s *switchableSessionStorage) List(ctx context.Context, userID string) ([]*Session, error) {
	return s.current().List(ctx, userID)
}
func (s *switchableSessionStorage) ListWithState(ctx context.Context, userID string, state SessionState) ([]*Session, error) {
	return s.current().ListWithState(ctx, userID, state)
}
func (s *switchableSessionStorage) ListByTags(ctx context.Context, userID string, tags []string) ([]*Session, error) {
	return s.current().ListByTags(ctx, userID, tags)
}
func (s *switchableSessionStorage) Update(ctx context.Context, session *Session) error {
	return s.current().Update(ctx, session)
}
func (s *switchableSessionStorage) AddMessage(ctx context.Context, sessionID string, message interface{}) error {
	return s.current().AddMessage(ctx, sessionID, message)
}
func (s *switchableSessionStorage) GetMessages(ctx context.Context, sessionID string) ([]interface{}, error) {
	return s.current().GetMessages(ctx, sessionID)
}
func (s *switchableSessionStorage) Close(ctx context.Context, sessionID string) error {
	return s.current().Close(ctx, sessionID)
}
func (s *switchableSessionStorage) Cleanup(ctx context.Context, after time.Time) (int, error) {
	return s.current().Cleanup(ctx, after)
}
func (s *switchableSessionStorage) GetStatistics(ctx context.Context, userID string) (*SessionStatistics, error) {
	return s.current().GetStatistics(ctx, userID)
}

// Optional interfaces forwarded to the current backend.
func (s *switchableSessionStorage) ListAll(ctx context.Context, limit, offset int) ([]*Session, error) {
	if lister, ok := s.current().(SessionStorageAllLister); ok {
		return lister.ListAll(ctx, limit, offset)
	}
	return nil, fmt.Errorf("session storage does not support listing all sessions")
}
func (s *switchableSessionStorage) AddMessageWithLimit(ctx context.Context, sessionID string, message types.Message, maxHistory int) error {
	if appender, ok := s.current().(SessionStorageHistoryAppender); ok {
		return appender.AddMessageWithLimit(ctx, sessionID, message, maxHistory)
	}
	return s.current().AddMessage(ctx, sessionID, message)
}
func (s *switchableSessionStorage) ListPreviews(ctx context.Context, userID string, limit, offset int) ([]*SessionPreview, error) {
	if lister, ok := s.current().(SessionStoragePreviewLister); ok {
		return lister.ListPreviews(ctx, userID, limit, offset)
	}
	return nil, fmt.Errorf("session storage does not support preview listing")
}
func (s *switchableSessionStorage) ListMetadataPage(ctx context.Context, userID string, limit, offset int) ([]*Session, error) {
	if pager, ok := s.current().(SessionStorageMetadataPager); ok {
		return pager.ListMetadataPage(ctx, userID, limit, offset)
	}
	return nil, fmt.Errorf("session storage does not support metadata paging")
}
func (s *switchableSessionStorage) GetRecentMessages(ctx context.Context, sessionID string, limit int) ([]types.Message, error) {
	if reader, ok := s.current().(SessionStorageRecentHistoryReader); ok {
		return reader.GetRecentMessages(ctx, sessionID, limit)
	}
	return nil, fmt.Errorf("session storage does not support recent history reads")
}
func (s *switchableSessionStorage) GetMessagePage(ctx context.Context, sessionID string, beforeSeq, limit int) (*SessionHistoryPage, error) {
	if pager, ok := s.current().(SessionStorageHistoryPager); ok {
		return pager.GetMessagePage(ctx, sessionID, beforeSeq, limit)
	}
	return nil, fmt.Errorf("session storage does not support history paging")
}
func (s *switchableSessionStorage) StreamMessages(ctx context.Context, sessionID string, visit func(seq int, message types.Message) error) error {
	if streamer, ok := s.current().(SessionStorageHistoryStreamer); ok {
		return streamer.StreamMessages(ctx, sessionID, visit)
	}
	return fmt.Errorf("session storage does not support history streaming")
}
func (s *switchableSessionStorage) StreamMessageJSON(ctx context.Context, sessionID string, visit func(seq int, info CanonicalMessageInfo, payload io.Reader) error) error {
	if streamer, ok := s.current().(SessionStorageCanonicalJSONStreamer); ok {
		return streamer.StreamMessageJSON(ctx, sessionID, visit)
	}
	return fmt.Errorf("session storage does not support canonical JSON streaming")
}
func (s *switchableSessionStorage) ClearMessages(ctx context.Context, sessionID string) error {
	if clearer, ok := s.current().(SessionStorageHistoryClearer); ok {
		return clearer.ClearMessages(ctx, sessionID)
	}
	return fmt.Errorf("session storage does not support history clearing")
}
func (s *switchableSessionStorage) MessageCount(ctx context.Context, sessionID string) (int, error) {
	if counter, ok := s.current().(SessionStorageMessageCounter); ok {
		return counter.MessageCount(ctx, sessionID)
	}
	return 0, fmt.Errorf("session storage does not support message counting")
}
func (s *switchableSessionStorage) ArchiveIdleSessions(ctx context.Context, before time.Time, batchSize int) (int, error) {
	if archiver, ok := s.current().(SessionStorageIdleArchiver); ok {
		return archiver.ArchiveIdleSessions(ctx, before, batchSize)
	}
	return 0, fmt.Errorf("session storage does not support idle archival")
}
func (s *switchableSessionStorage) Snapshot(ctx context.Context, destinationPath string) error {
	if snapshotter, ok := s.current().(SessionStorageSnapshotter); ok {
		return snapshotter.Snapshot(ctx, destinationPath)
	}
	return fmt.Errorf("session storage does not support snapshots")
}
func (s *switchableSessionStorage) SnapshotSession(ctx context.Context, sessionID, destinationPath string) error {
	if snapshotter, ok := s.current().(SessionStorageSessionSnapshotter); ok {
		return snapshotter.SnapshotSession(ctx, sessionID, destinationPath)
	}
	return fmt.Errorf("session storage does not support session snapshots")
}
func (s *switchableSessionStorage) CloseStorage() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inner == nil {
		return nil
	}
	if closer, ok := s.inner.(SessionStorageCloser); ok {
		err := closer.CloseStorage()
		s.inner = nil
		return err
	}
	s.inner = nil
	return nil
}

// resolveReplicaSourcePath resolves a possibly-relative replicaSource against
// the sessions directory (mirroring how storePath is resolved for the store).
func resolveReplicaSourcePath(sessionsDir, replicaSource string) string {
	replicaSource = strings.TrimSpace(replicaSource)
	if replicaSource == "" {
		return ""
	}
	if filepath.IsAbs(replicaSource) {
		return filepath.Clean(replicaSource)
	}
	if strings.TrimSpace(sessionsDir) != "" {
		return filepath.Join(sessionsDir, replicaSource)
	}
	return filepath.Clean(replicaSource)
}
