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

// lazySessionStorage defers opening the durable backend until the first real
// storage operation. This keeps new-chat startup off the critical path of
// opening a large session_history.sqlite when no resume/list/load is needed.
type lazySessionStorage struct {
	cfg PersistentSessionStorageConfig

	once    sync.Once
	mu      sync.RWMutex
	inner   SessionStorage
	openErr error
	closed  bool
}

// OpenPersistentSessionStorage creates the configured durable session store.
// SQLite backends are opened lazily so chat bootstrap can prepare a manager
// without paying the DB open cost until the first Save/Load/List.
func OpenPersistentSessionStorage(cfg PersistentSessionStorageConfig) (SessionStorage, error) {
	cfg = normalizePersistentSessionStorageConfig(cfg)
	switch cfg.Backend {
	case SessionStorageBackendFile:
		return NewFileStorage(cfg.Dir)
	case SessionStorageBackendSQLite:
		if strings.TrimSpace(cfg.Path) == "" {
			return nil, fmt.Errorf("sqlite session storage path cannot be empty")
		}
		return &lazySessionStorage{cfg: cfg}, nil
	default:
		return nil, fmt.Errorf("unsupported session storage backend %q", cfg.Backend)
	}
}

// OpenEagerPersistentSessionStorage opens the durable store immediately.
// Prefer OpenPersistentSessionStorage for chat bootstrap; use this only when
// the caller needs a fully initialized backend up front (benchmarks/tests).
func OpenEagerPersistentSessionStorage(cfg PersistentSessionStorageConfig) (SessionStorage, error) {
	cfg = normalizePersistentSessionStorageConfig(cfg)
	switch cfg.Backend {
	case SessionStorageBackendFile:
		return NewFileStorage(cfg.Dir)
	case SessionStorageBackendSQLite:
		return NewSQLiteSessionStorage(cfg)
	default:
		return nil, fmt.Errorf("unsupported session storage backend %q", cfg.Backend)
	}
}

func (s *lazySessionStorage) ensure() (SessionStorage, error) {
	if s == nil {
		return nil, fmt.Errorf("lazy session storage is nil")
	}
	s.mu.RLock()
	if s.closed {
		s.mu.RUnlock()
		return nil, fmt.Errorf("sqlite session storage is closed")
	}
	if s.inner != nil || s.openErr != nil {
		inner, err := s.inner, s.openErr
		s.mu.RUnlock()
		return inner, err
	}
	s.mu.RUnlock()

	s.once.Do(func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.closed {
			s.openErr = fmt.Errorf("sqlite session storage is closed")
			return
		}
		store, err := NewSQLiteSessionStorage(s.cfg)
		if err != nil {
			s.openErr = err
			return
		}
		s.inner = store
	})

	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.inner, s.openErr
}

func (s *lazySessionStorage) Opened() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.inner != nil
}

func (s *lazySessionStorage) Dir() string {
	if s == nil {
		return ""
	}
	return s.cfg.Dir
}

func (s *lazySessionStorage) Path() string {
	if s == nil {
		return ""
	}
	if path := strings.TrimSpace(s.cfg.Path); path != "" {
		return path
	}
	if dir := strings.TrimSpace(s.cfg.Dir); dir != "" {
		return filepath.Join(dir, "session_history.sqlite")
	}
	return ""
}

// durableFileExists reports whether the configured on-disk sqlite file is already
// present. Background housekeeping must open existing history after restart even
// when the process has not yet performed a Save/Load.
func (s *lazySessionStorage) durableFileExists() bool {
	path := strings.TrimSpace(s.Path())
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

// shouldOpenForMaintenance returns true when housekeeping can safely touch the
// durable backend: either it is already open in this process, or an existing
// file is present from a previous run. A brand-new chat must not create an empty
// session_history.sqlite just because a cleanup timer fired.
func (s *lazySessionStorage) shouldOpenForMaintenance() bool {
	return s.Opened() || s.durableFileExists()
}

func (s *lazySessionStorage) Save(ctx context.Context, session *Session) error {
	store, err := s.ensure()
	if err != nil {
		return err
	}
	return store.Save(ctx, session)
}

func (s *lazySessionStorage) Load(ctx context.Context, sessionID string) (*Session, error) {
	store, err := s.ensure()
	if err != nil {
		return nil, err
	}
	return store.Load(ctx, sessionID)
}

func (s *lazySessionStorage) Delete(ctx context.Context, sessionID string) error {
	store, err := s.ensure()
	if err != nil {
		return err
	}
	return store.Delete(ctx, sessionID)
}

func (s *lazySessionStorage) List(ctx context.Context, userID string) ([]*Session, error) {
	store, err := s.ensure()
	if err != nil {
		return nil, err
	}
	return store.List(ctx, userID)
}

func (s *lazySessionStorage) ListAll(ctx context.Context, limit, offset int) ([]*Session, error) {
	store, err := s.ensure()
	if err != nil {
		return nil, err
	}
	if lister, ok := store.(SessionStorageAllLister); ok {
		return lister.ListAll(ctx, limit, offset)
	}
	return nil, fmt.Errorf("session storage does not support listing all sessions")
}

func (s *lazySessionStorage) ListWithState(ctx context.Context, userID string, state SessionState) ([]*Session, error) {
	store, err := s.ensure()
	if err != nil {
		return nil, err
	}
	return store.ListWithState(ctx, userID, state)
}

func (s *lazySessionStorage) ListByTags(ctx context.Context, userID string, tags []string) ([]*Session, error) {
	store, err := s.ensure()
	if err != nil {
		return nil, err
	}
	return store.ListByTags(ctx, userID, tags)
}

func (s *lazySessionStorage) Update(ctx context.Context, session *Session) error {
	store, err := s.ensure()
	if err != nil {
		return err
	}
	return store.Update(ctx, session)
}

func (s *lazySessionStorage) AddMessage(ctx context.Context, sessionID string, message interface{}) error {
	store, err := s.ensure()
	if err != nil {
		return err
	}
	return store.AddMessage(ctx, sessionID, message)
}

func (s *lazySessionStorage) GetMessages(ctx context.Context, sessionID string) ([]interface{}, error) {
	store, err := s.ensure()
	if err != nil {
		return nil, err
	}
	return store.GetMessages(ctx, sessionID)
}

func (s *lazySessionStorage) Close(ctx context.Context, sessionID string) error {
	store, err := s.ensure()
	if err != nil {
		return err
	}
	return store.Close(ctx, sessionID)
}

func (s *lazySessionStorage) Cleanup(ctx context.Context, after time.Time) (int, error) {
	// Background timers must not force a first open of a large history DB
	// just to discover there is nothing to clean. After restart, though, an
	// existing session_history.sqlite still needs maintenance.
	if !s.shouldOpenForMaintenance() {
		return 0, nil
	}
	store, err := s.ensure()
	if err != nil {
		return 0, err
	}
	return store.Cleanup(ctx, after)
}

func (s *lazySessionStorage) GetStatistics(ctx context.Context, userID string) (*SessionStatistics, error) {
	store, err := s.ensure()
	if err != nil {
		return nil, err
	}
	return store.GetStatistics(ctx, userID)
}

func (s *lazySessionStorage) ListPreviews(ctx context.Context, userID string, limit, offset int) ([]*SessionPreview, error) {
	store, err := s.ensure()
	if err != nil {
		return nil, err
	}
	if lister, ok := store.(SessionStoragePreviewLister); ok {
		return lister.ListPreviews(ctx, userID, limit, offset)
	}
	return nil, fmt.Errorf("session storage does not support preview listing")
}

func (s *lazySessionStorage) ListMetadataPage(ctx context.Context, userID string, limit, offset int) ([]*Session, error) {
	store, err := s.ensure()
	if err != nil {
		return nil, err
	}
	if pager, ok := store.(SessionStorageMetadataPager); ok {
		return pager.ListMetadataPage(ctx, userID, limit, offset)
	}
	sessions, listErr := store.List(ctx, userID)
	if listErr != nil {
		return nil, listErr
	}
	if offset >= len(sessions) {
		return []*Session{}, nil
	}
	if offset > 0 {
		sessions = sessions[offset:]
	}
	if limit > 0 && len(sessions) > limit {
		sessions = sessions[:limit]
	}
	return sessions, nil
}

func (s *lazySessionStorage) GetRecentMessages(ctx context.Context, sessionID string, limit int) ([]types.Message, error) {
	store, err := s.ensure()
	if err != nil {
		return nil, err
	}
	if reader, ok := store.(SessionStorageRecentHistoryReader); ok {
		return reader.GetRecentMessages(ctx, sessionID, limit)
	}
	return nil, fmt.Errorf("session storage does not support recent history reads")
}

func (s *lazySessionStorage) GetMessagePage(ctx context.Context, sessionID string, beforeSeq, limit int) (*SessionHistoryPage, error) {
	store, err := s.ensure()
	if err != nil {
		return nil, err
	}
	if pager, ok := store.(SessionStorageHistoryPager); ok {
		return pager.GetMessagePage(ctx, sessionID, beforeSeq, limit)
	}
	return nil, fmt.Errorf("session storage does not support history paging")
}

func (s *lazySessionStorage) StreamMessages(ctx context.Context, sessionID string, visit func(seq int, message types.Message) error) error {
	store, err := s.ensure()
	if err != nil {
		return err
	}
	if streamer, ok := store.(SessionStorageHistoryStreamer); ok {
		return streamer.StreamMessages(ctx, sessionID, visit)
	}
	return fmt.Errorf("session storage does not support history streaming")
}

func (s *lazySessionStorage) StreamMessageJSON(ctx context.Context, sessionID string, visit func(seq int, info CanonicalMessageInfo, payload io.Reader) error) error {
	store, err := s.ensure()
	if err != nil {
		return err
	}
	if streamer, ok := store.(SessionStorageCanonicalJSONStreamer); ok {
		return streamer.StreamMessageJSON(ctx, sessionID, visit)
	}
	return fmt.Errorf("session storage does not support canonical JSON streaming")
}

func (s *lazySessionStorage) ClearMessages(ctx context.Context, sessionID string) error {
	store, err := s.ensure()
	if err != nil {
		return err
	}
	if clearer, ok := store.(SessionStorageHistoryClearer); ok {
		return clearer.ClearMessages(ctx, sessionID)
	}
	return fmt.Errorf("session storage does not support history clearing")
}

func (s *lazySessionStorage) MessageCount(ctx context.Context, sessionID string) (int, error) {
	store, err := s.ensure()
	if err != nil {
		return 0, err
	}
	if counter, ok := store.(SessionStorageMessageCounter); ok {
		return counter.MessageCount(ctx, sessionID)
	}
	return 0, fmt.Errorf("session storage does not support message counting")
}

func (s *lazySessionStorage) ArchiveIdleSessions(ctx context.Context, before time.Time, batchSize int) (int, error) {
	if !s.shouldOpenForMaintenance() {
		return 0, nil
	}
	store, err := s.ensure()
	if err != nil {
		return 0, err
	}
	if archiver, ok := store.(SessionStorageIdleArchiver); ok {
		return archiver.ArchiveIdleSessions(ctx, before, batchSize)
	}
	return 0, fmt.Errorf("session storage does not support idle archival")
}

func (s *lazySessionStorage) Snapshot(ctx context.Context, destinationPath string) error {
	store, err := s.ensure()
	if err != nil {
		return err
	}
	if snapshotter, ok := store.(SessionStorageSnapshotter); ok {
		return snapshotter.Snapshot(ctx, destinationPath)
	}
	return fmt.Errorf("session storage does not support snapshots")
}

func (s *lazySessionStorage) SnapshotSession(ctx context.Context, sessionID, destinationPath string) error {
	store, err := s.ensure()
	if err != nil {
		return err
	}
	if snapshotter, ok := store.(SessionStorageSessionSnapshotter); ok {
		return snapshotter.SnapshotSession(ctx, sessionID, destinationPath)
	}
	return fmt.Errorf("session storage does not support session snapshots")
}

func (s *lazySessionStorage) CloseStorage() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
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
