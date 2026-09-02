package chat

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"
)

// SessionReplica controls a periodically refreshed read-replica of the master
// session-history SQLite database. The runtime server reads from this private
// copy so that aicli's write locks never block its queries.
type SessionReplica struct {
	storage  *switchableSessionStorage
	cfg      PersistentSessionStorageConfig
	srcPath  string
	dstPath  string
	interval time.Duration
	ctx      context.Context
	cancel   context.CancelFunc
	done     chan struct{}
	started  bool
	mu       sync.Mutex

	// lastSrcMod tracks the master file state from the last successful sync so
	// we can skip copying when nothing changed on disk.
	lastSrcMod  time.Time
	lastSrcSize int64
}

// OpenSessionReplica creates (or opens the existing) replica database at
// cfg.Path, synchronises it from srcPath, and wraps the result in a
// hot-swappable SessionStorage. The returned SessionReplica must be closed
// when the caller is shutting down.
//
// srcPath is the absolute path to the master session-history SQLite file.
func OpenSessionReplica(ctx context.Context, cfg PersistentSessionStorageConfig, srcPath string, interval time.Duration) (*SessionReplica, error) {
	if strings.TrimSpace(srcPath) == "" {
		return nil, fmt.Errorf("OpenSessionReplica: srcPath is required")
	}
	// Replica should never attempt legacy JSON import — the master is already
	// the authoritative source and we sync from it.
	cfg.ImportLegacyJSON = false

	// Best-effort first sync. If the master is locked or WAL not checkpointed
	// the replica stays empty; the background loop will retry.
	if err := SyncSessionReplica(ctx, srcPath, cfg.Path); err != nil {
		// Log but do not fail — the caller may still open an empty replica.
		log.Printf("[replica] first sync skipped: %v", err)
	}

	storage, err := OpenPersistentSessionStorage(cfg)
	if err != nil {
		return nil, fmt.Errorf("open replica session storage: %w", err)
	}

	replicaCtx, cancel := context.WithCancel(context.Background())
	if interval <= 0 {
		interval = SessionReplicaSyncIntervalDefault
	}

	return &SessionReplica{
		storage:  &switchableSessionStorage{inner: storage},
		cfg:      cfg,
		srcPath:  srcPath,
		dstPath:  cfg.Path,
		interval: interval,
		ctx:      replicaCtx,
		cancel:   cancel,
		done:     make(chan struct{}),
	}, nil
}

// Storage returns the hot-swappable SessionStorage. Pass this to
// SessionManager at construction time.
func (r *SessionReplica) Storage() SessionStorage { return r.storage }

// SwapNow performs an immediate synchronisation from the master and swaps the
// underlying storage. This is safe to call concurrently with active reads.
func (r *SessionReplica) SwapNow(ctx context.Context) {
	// Skip if the master has not changed since the last successful sync.
	if st, err := os.Stat(r.srcPath); err == nil {
		r.mu.Lock()
		unchanged := st.Size() == r.lastSrcSize && st.ModTime().Equal(r.lastSrcMod)
		r.mu.Unlock()
		if unchanged {
			return
		}
	}
	if err := SyncSessionReplica(ctx, r.srcPath, r.dstPath); err != nil {
		log.Printf("[replica] sync skipped: %v", err)
		return
	}
	if st, err := os.Stat(r.srcPath); err == nil {
		r.mu.Lock()
		r.lastSrcMod = st.ModTime()
		r.lastSrcSize = st.Size()
		r.mu.Unlock()
	}
	fresh, err := OpenPersistentSessionStorage(r.cfg)
	if err != nil {
		log.Printf("[replica] reopen after sync: %v", err)
		return
	}
	r.storage.Swap(fresh)
}

// Run starts the periodic sync loop in the background. It is safe to call
// multiple times (only the first invocation starts the loop).
func (r *SessionReplica) Run(ctx context.Context) {
	r.mu.Lock()
	if r.started {
		r.mu.Unlock()
		return
	}
	r.started = true
	r.mu.Unlock()

	go func() {
		defer close(r.done)
		ticker := time.NewTicker(r.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-r.ctx.Done():
				return
			case <-ticker.C:
				r.SwapNow(ctx)
			}
		}
	}()
}

// Close stops the sync loop and closes the underlying storage.
func (r *SessionReplica) Close() {
	r.cancel()
	select {
	case <-r.done:
	case <-time.After(3 * time.Second):
	}
	if r.storage != nil {
		_ = r.storage.CloseStorage()
	}
}
