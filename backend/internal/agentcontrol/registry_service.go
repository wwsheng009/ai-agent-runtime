package agentcontrol

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	_ "github.com/wwsheng009/ai-agent-runtime/internal/sqlitedriver"
)

const (
	// RegistryServiceModeDisabled means no durable registry store is configured.
	RegistryServiceModeDisabled = "disabled"
	// RegistryServiceModeSingleSQLite means mailbox and agent registries share
	// one SQLite database handle.
	RegistryServiceModeSingleSQLite = "single_sqlite"
	// RegistryServiceModeSplitSQLite means mailbox and agent registries use
	// separate SQLite stores.
	RegistryServiceModeSplitSQLite = "split_sqlite"
)

// ErrRegistryServiceClosed is returned when a caller uses a closed registry
// service or one of its public store handles after Close.
var ErrRegistryServiceClosed = errors.New("agent control registry service is closed")

// RegistryServiceConfig describes the durable AgentControl registry substrate.
// StorePath/StoreDSN are the preferred single-DB configuration. The mailbox
// and agent specific fields remain explicit overrides for compatibility.
type RegistryServiceConfig struct {
	StorePath        string
	StoreDSN         string
	MailboxStorePath string
	MailboxStoreDSN  string
	AgentStorePath   string
	AgentStoreDSN    string
}

// Normalize trims fields and applies StorePath/StoreDSN as defaults for the
// mailbox and agent registry stores.
func (c RegistryServiceConfig) Normalize() RegistryServiceConfig {
	c.StorePath = strings.TrimSpace(c.StorePath)
	c.StoreDSN = strings.TrimSpace(c.StoreDSN)
	c.MailboxStorePath = strings.TrimSpace(c.MailboxStorePath)
	c.MailboxStoreDSN = strings.TrimSpace(c.MailboxStoreDSN)
	c.AgentStorePath = strings.TrimSpace(c.AgentStorePath)
	c.AgentStoreDSN = strings.TrimSpace(c.AgentStoreDSN)
	if c.MailboxStorePath == "" && c.MailboxStoreDSN == "" {
		c.MailboxStorePath = c.StorePath
		c.MailboxStoreDSN = c.StoreDSN
	}
	if c.AgentStorePath == "" && c.AgentStoreDSN == "" {
		c.AgentStorePath = c.StorePath
		c.AgentStoreDSN = c.StoreDSN
	}
	return c
}

// Empty reports whether no durable registry store is configured.
func (c RegistryServiceConfig) Empty() bool {
	c = c.Normalize()
	return c.MailboxStorePath == "" && c.MailboxStoreDSN == "" &&
		c.AgentStorePath == "" && c.AgentStoreDSN == ""
}

// Key returns a stable lifecycle key for registry service caching.
func (c RegistryServiceConfig) Key() string {
	c = c.Normalize()
	return strings.Join([]string{
		c.StorePath,
		c.StoreDSN,
		c.MailboxStorePath,
		c.MailboxStoreDSN,
		c.AgentStorePath,
		c.AgentStoreDSN,
	}, "|")
}

func (c RegistryServiceConfig) sharedSQLiteConfig() (GlobalMailboxStoreConfig, bool) {
	c = c.Normalize()
	if c.MailboxStorePath != "" && c.AgentStorePath != "" && c.MailboxStorePath == c.AgentStorePath {
		return GlobalMailboxStoreConfig{Path: c.MailboxStorePath}, true
	}
	if c.MailboxStoreDSN != "" && c.AgentStoreDSN != "" && c.MailboxStoreDSN == c.AgentStoreDSN {
		return GlobalMailboxStoreConfig{DSN: c.MailboxStoreDSN}, true
	}
	return GlobalMailboxStoreConfig{}, false
}

// RegistryService owns the durable AgentControl registry stores for one
// process. When configured with StorePath/StoreDSN it opens a single SQLite DB
// that contains both the mailbox and identity graph tables.
type RegistryService struct {
	MailboxStore GlobalMailboxRegistryStore
	AgentStore   AgentRegistryStore

	mu        sync.Mutex
	cfg       RegistryServiceConfig
	mode      string
	startedAt time.Time
	closed    bool
	db        *sql.DB
	closers   []func() error
}

// RegistryServiceHealth reports the durable registry service lifecycle state.
type RegistryServiceHealth struct {
	Mode              string    `json:"mode"`
	Closed            bool      `json:"closed"`
	ConfigKey         string    `json:"config_key,omitempty"`
	MailboxConfigured bool      `json:"mailbox_configured"`
	AgentConfigured   bool      `json:"agent_configured"`
	SharedDB          bool      `json:"shared_db"`
	StartedAt         time.Time `json:"started_at,omitempty"`
}

// NewRegistryService opens the durable AgentControl registry service.
func NewRegistryService(ctx context.Context, cfg RegistryServiceConfig) (*RegistryService, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cfg = cfg.Normalize()
	if cfg.Empty() {
		return &RegistryService{
			cfg:       cfg,
			mode:      RegistryServiceModeDisabled,
			startedAt: time.Now().UTC(),
		}, nil
	}
	if sharedCfg, ok := cfg.sharedSQLiteConfig(); ok {
		dsn, err := resolveGlobalMailboxDSN(&sharedCfg)
		if err != nil {
			return nil, err
		}
		db, err := sql.Open("sqlite3", dsn)
		if err != nil {
			return nil, fmt.Errorf("open agent control registry db: %w", err)
		}
		if isGlobalMailboxMemoryDSN(dsn) {
			db.SetMaxOpenConns(1)
			db.SetMaxIdleConns(1)
		}
		if err := configureAgentControlSQLiteDB(ctx, db, dsn); err != nil {
			_ = db.Close()
			return nil, err
		}
		service := &RegistryService{
			cfg:       cfg,
			mode:      RegistryServiceModeSingleSQLite,
			startedAt: time.Now().UTC(),
			db:        db,
		}
		mailboxStore, err := newSQLiteGlobalMailboxRegistryStoreWithDB(ctx, db, dsn)
		if err != nil {
			_ = db.Close()
			return nil, err
		}
		agentStore, err := newSQLiteGlobalAgentRegistryStoreWithDB(ctx, db, dsn)
		if err != nil {
			_ = db.Close()
			return nil, err
		}
		service.MailboxStore = mailboxStore
		service.AgentStore = agentStore
		service.closers = append(service.closers, agentStore.Close, mailboxStore.Close)
		return service, nil
	}

	service := &RegistryService{
		cfg:       cfg,
		mode:      RegistryServiceModeSplitSQLite,
		startedAt: time.Now().UTC(),
	}
	if cfg.MailboxStorePath != "" || cfg.MailboxStoreDSN != "" {
		store, err := NewSQLiteGlobalMailboxRegistryStore(&GlobalMailboxStoreConfig{
			Path: cfg.MailboxStorePath,
			DSN:  cfg.MailboxStoreDSN,
		})
		if err != nil {
			return nil, err
		}
		service.MailboxStore = store
		service.closers = append(service.closers, store.Close)
	}
	if cfg.AgentStorePath != "" || cfg.AgentStoreDSN != "" {
		store, err := NewSQLiteGlobalAgentRegistryStore(&GlobalAgentStoreConfig{
			Path: cfg.AgentStorePath,
			DSN:  cfg.AgentStoreDSN,
		})
		if err != nil {
			_ = service.Close()
			return nil, err
		}
		service.AgentStore = store
		service.closers = append(service.closers, store.Close)
	}
	return service, nil
}

// Mode returns the registry service storage mode.
func (s *RegistryService) Mode() string {
	if s == nil {
		return RegistryServiceModeDisabled
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mode == "" {
		return RegistryServiceModeDisabled
	}
	return s.mode
}

// Health verifies that the registry service is open and its shared database,
// when present, can be reached.
func (s *RegistryService) Health(ctx context.Context) (RegistryServiceHealth, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if s == nil {
		return RegistryServiceHealth{Mode: RegistryServiceModeDisabled, Closed: true}, ErrRegistryServiceClosed
	}
	s.mu.Lock()
	health := RegistryServiceHealth{
		Mode:              s.mode,
		Closed:            s.closed,
		ConfigKey:         s.cfg.Key(),
		MailboxConfigured: s.MailboxStore != nil,
		AgentConfigured:   s.AgentStore != nil,
		SharedDB:          s.db != nil,
		StartedAt:         s.startedAt,
	}
	db := s.db
	s.mu.Unlock()
	if health.Mode == "" {
		health.Mode = RegistryServiceModeDisabled
	}
	if health.Closed {
		return health, ErrRegistryServiceClosed
	}
	if db != nil {
		if err := db.PingContext(ctx); err != nil {
			return health, fmt.Errorf("agent control registry service health check failed: %w", err)
		}
	}
	return health, nil
}

// Close releases all registry service resources.
func (s *RegistryService) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	closers := append([]func() error(nil), s.closers...)
	db := s.db
	s.closed = true
	s.MailboxStore = closedGlobalMailboxRegistryStore{}
	s.AgentStore = closedAgentRegistryStore{}
	s.closers = nil
	s.db = nil
	s.mu.Unlock()

	var firstErr error
	for i := len(closers) - 1; i >= 0; i-- {
		if closers[i] == nil {
			continue
		}
		if err := closers[i](); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if db != nil {
		if err := db.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func configureAgentControlSQLiteDB(ctx context.Context, db *sql.DB, dsn string) error {
	if db == nil {
		return fmt.Errorf("agent control registry db is not initialized")
	}
	dsn = strings.TrimSpace(dsn)
	if !isGlobalMailboxMemoryDSN(dsn) {
		db.SetMaxOpenConns(8)
		db.SetMaxIdleConns(4)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		return fmt.Errorf("configure agent control registry foreign keys: %w", err)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA busy_timeout = 5000"); err != nil {
		return fmt.Errorf("configure agent control registry busy timeout: %w", err)
	}
	if !isGlobalMailboxMemoryDSN(dsn) {
		if _, err := db.ExecContext(ctx, "PRAGMA journal_mode = WAL"); err != nil {
			return fmt.Errorf("configure agent control registry wal mode: %w", err)
		}
	}
	return nil
}

type closedGlobalMailboxRegistryStore struct{}

func (closedGlobalMailboxRegistryStore) ListAgentControlMailboxRecords(context.Context, MailboxRecordFilter) ([]MailboxRecord, error) {
	return nil, ErrRegistryServiceClosed
}

func (closedGlobalMailboxRegistryStore) LastAgentControlMailboxRecordSeq(context.Context, MailboxRecordFilter) (int64, error) {
	return 0, ErrRegistryServiceClosed
}

func (closedGlobalMailboxRegistryStore) AppendGlobalMailboxRecord(context.Context, string, MailboxRecord) (int64, error) {
	return 0, ErrRegistryServiceClosed
}

func (closedGlobalMailboxRegistryStore) AppendPrimaryGlobalMailboxRecord(context.Context, MailboxRecord) (MailboxRecord, error) {
	return MailboxRecord{}, ErrRegistryServiceClosed
}

func (closedGlobalMailboxRegistryStore) WatchAgentControlMailboxWake(context.Context, MailboxWakeFilter) (<-chan MailboxWakeEvent, func()) {
	ch := make(chan MailboxWakeEvent)
	close(ch)
	return ch, func() {}
}

func (closedGlobalMailboxRegistryStore) LastAgentControlMailboxWakeSeq(context.Context, MailboxWakeFilter) (int64, error) {
	return 0, ErrRegistryServiceClosed
}

func (closedGlobalMailboxRegistryStore) MaterializeMailboxRecords(context.Context, []NamedMailboxRegistrySource, MailboxRecordFilter) (int64, error) {
	return 0, ErrRegistryServiceClosed
}

func (closedGlobalMailboxRegistryStore) Close() error {
	return nil
}

type closedAgentRegistryStore struct{}

func (closedAgentRegistryStore) ListAgentControlAgents(context.Context, AgentFilter) ([]AgentRecord, error) {
	return nil, ErrRegistryServiceClosed
}

func (closedAgentRegistryStore) UpsertAgentControlAgent(context.Context, AgentRecord) (AgentRecord, error) {
	return AgentRecord{}, ErrRegistryServiceClosed
}

func (closedAgentRegistryStore) CloseAgentControlAgentSubtree(context.Context, string, string, time.Time) (int64, error) {
	return 0, ErrRegistryServiceClosed
}

func (closedAgentRegistryStore) Close() error {
	return nil
}
