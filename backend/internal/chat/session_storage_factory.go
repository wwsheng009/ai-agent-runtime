package chat

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/aiclipaths"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

const (
	SessionStorageBackendFile   = "file"
	SessionStorageBackendSQLite = "sqlite"
)

// PersistentSessionStorageConfig controls the durable transcript store and
// the bounded prompt projection loaded into runtime memory.
type PersistentSessionStorageConfig struct {
	Backend               string
	Dir                   string
	Path                  string
	HotHistoryMessages    int
	HotHistoryBytes       int
	MaxHotMessageBytes    int
	HistoryPageMessages   int
	HistoryPageBytes      int
	MaxInlineMessageBytes int
	SQLiteCacheKiB        int
	// ReadPoolSize 是只读查询的连接池大小（WAL 下读读/读写并发）。
	// <=0 用默认值 4；=1 显式退回历史单连接行为（排障/极端内存受限）。
	// 写路径始终单连接，不受此值影响。
	ReadPoolSize     int
	BusyTimeout      time.Duration
	ImportLegacyJSON bool
	// SessionSnapshotTimeout 限制单次 SnapshotSession/Snapshot 的全程耗时；
	// 默认 min(BusyTimeout*6, 60s)，负值表示不额外加 deadline（沿用调用方 ctx）。
	SessionSnapshotTimeout time.Duration
	// SessionSnapshotMaxBytes 是会话快照的预检上限（估算字节数），
	// 0 表示不限制；超限时返回可解释错误且不产生残留文件（P2.13/G4）。
	SessionSnapshotMaxBytes int64
}

func DefaultPersistentSessionStorageConfig(dir string) PersistentSessionStorageConfig {
	return PersistentSessionStorageConfig{
		Backend:               SessionStorageBackendSQLite,
		Dir:                   strings.TrimSpace(dir),
		HotHistoryMessages:    128,
		HotHistoryBytes:       2 * 1024 * 1024,
		MaxHotMessageBytes:    128 * 1024,
		HistoryPageMessages:   100,
		HistoryPageBytes:      4 * 1024 * 1024,
		MaxInlineMessageBytes: 512 * 1024,
		SQLiteCacheKiB:        2048,
		ReadPoolSize:          sqliteSessionReadPoolDefaultSize,
		BusyTimeout:           5 * time.Second,
		ImportLegacyJSON:      true,
	}
}

func normalizePersistentSessionStorageConfig(cfg PersistentSessionStorageConfig) PersistentSessionStorageConfig {
	defaults := DefaultPersistentSessionStorageConfig(cfg.Dir)
	cfg.Backend = strings.ToLower(strings.TrimSpace(cfg.Backend))
	if cfg.Backend == "" {
		cfg.Backend = defaults.Backend
	}
	cfg.Dir = strings.TrimSpace(cfg.Dir)
	if cfg.Path == "" && cfg.Dir != "" {
		cfg.Path = filepath.Join(cfg.Dir, aiclipaths.DefaultSessionHistoryFileName)
	} else if cfg.Path != "" && !filepath.IsAbs(cfg.Path) && cfg.Dir != "" {
		cfg.Path = filepath.Join(cfg.Dir, cfg.Path)
	}
	if cfg.HotHistoryMessages <= 0 {
		cfg.HotHistoryMessages = defaults.HotHistoryMessages
	}
	if cfg.HotHistoryBytes <= 0 {
		cfg.HotHistoryBytes = defaults.HotHistoryBytes
	}
	if cfg.MaxHotMessageBytes <= 0 {
		cfg.MaxHotMessageBytes = defaults.MaxHotMessageBytes
	}
	if cfg.HistoryPageMessages <= 0 {
		cfg.HistoryPageMessages = defaults.HistoryPageMessages
	}
	if cfg.HistoryPageBytes <= 0 {
		cfg.HistoryPageBytes = defaults.HistoryPageBytes
	}
	if cfg.MaxInlineMessageBytes <= 0 {
		cfg.MaxInlineMessageBytes = defaults.MaxInlineMessageBytes
	}
	if cfg.SQLiteCacheKiB <= 0 {
		cfg.SQLiteCacheKiB = defaults.SQLiteCacheKiB
	}
	if cfg.BusyTimeout <= 0 {
		cfg.BusyTimeout = defaults.BusyTimeout
	}
	if cfg.SessionSnapshotTimeout == 0 {
		cfg.SessionSnapshotTimeout = snapshotTimeoutForBusyTimeout(cfg.BusyTimeout)
	} else if cfg.SessionSnapshotTimeout < 0 {
		cfg.SessionSnapshotTimeout = 0
	}
	return cfg
}

// snapshotTimeoutForBusyTimeout 是 P2.13/D4 的默认预算：比写锁等待上限宽裕，
// 但有明确上界，避免 context.Background() 下的无限挂起。
func snapshotTimeoutForBusyTimeout(busyTimeout time.Duration) time.Duration {
	if busyTimeout <= 0 {
		busyTimeout = 5 * time.Second
	}
	timeout := busyTimeout * 6
	if timeout > 60*time.Second {
		timeout = 60 * time.Second
	}
	return timeout
}

// SessionStoragePreviewLister avoids materializing histories for list views.
type SessionStoragePreviewLister interface {
	ListPreviews(ctx context.Context, userID string, limit, offset int) ([]*SessionPreview, error)
}

// SessionStorageMetadataPager pages session metadata without prompt history.
type SessionStorageMetadataPager interface {
	ListMetadataPage(ctx context.Context, userID string, limit, offset int) ([]*Session, error)
}

// SessionStorageRecentHistoryReader reads canonical history without loading a
// whole transcript into one Session object.
type SessionStorageRecentHistoryReader interface {
	GetRecentMessages(ctx context.Context, sessionID string, limit int) ([]types.Message, error)
}

// SessionHistoryPage is an exclusive-before cursor page over the canonical
// transcript. Sequence numbers are one-based and stable for a session.
type SessionHistoryPage struct {
	Messages      []types.Message
	Total         int
	FirstSeq      int
	LastSeq       int
	NextBeforeSeq int
	HasMore       bool
}

// SessionStorageHistoryPager reads canonical history without materializing
// the complete transcript. beforeSeq == 0 selects the newest page.
type SessionStorageHistoryPager interface {
	GetMessagePage(ctx context.Context, sessionID string, beforeSeq, limit int) (*SessionHistoryPage, error)
}

// SessionStorageHistoryStreamer visits canonical messages in ascending
// sequence order while keeping memory proportional to one message.
type SessionStorageHistoryStreamer interface {
	StreamMessages(ctx context.Context, sessionID string, visit func(seq int, message types.Message) error) error
}

type CanonicalMessageInfo struct {
	Role             string
	RoleKnown        bool
	ToolCallCount    int
	ToolResult       bool
	ContentPartCount int
	StatsKnown       bool
}

// SessionStorageCanonicalJSONStreamer exposes canonical JSON as a forward-only
// reader. The reader is valid only during visit and must be consumed there.
type SessionStorageCanonicalJSONStreamer interface {
	StreamMessageJSON(ctx context.Context, sessionID string, visit func(seq int, info CanonicalMessageInfo, payload io.Reader) error) error
}

// SessionStorageHistoryClearer explicitly clears the canonical transcript.
type SessionStorageHistoryClearer interface {
	ClearMessages(ctx context.Context, sessionID string) error
}

type SessionStorageMessageCounter interface {
	MessageCount(ctx context.Context, sessionID string) (int, error)
}

// SessionStorageIdleArchiver lets persistent stores update idle sessions in
// bounded batches without materializing every session in Go memory.
type SessionStorageIdleArchiver interface {
	ArchiveIdleSessions(ctx context.Context, before time.Time, batchSize int) (int, error)
}

// SessionStorageSnapshotter writes a transactionally consistent standalone
// snapshot. Implementations must not require callers to copy WAL sidecars.
type SessionStorageSnapshotter interface {
	Snapshot(ctx context.Context, destinationPath string) error
}

// SessionStorageSessionSnapshotter writes a standalone snapshot containing
// only one session, avoiding unrelated transcript disclosure and disk usage.
type SessionStorageSessionSnapshotter interface {
	SnapshotSession(ctx context.Context, sessionID, destinationPath string) error
}

// SessionStorageCloser is implemented by stores with owned resources.
type SessionStorageCloser interface {
	CloseStorage() error
}
