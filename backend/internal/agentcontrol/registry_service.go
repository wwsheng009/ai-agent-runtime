package agentcontrol

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	_ "github.com/wwsheng009/ai-agent-runtime/internal/sqlitedriver"
)

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

	db      *sql.DB
	closers []func() error
}

// NewRegistryService opens the durable AgentControl registry service.
func NewRegistryService(ctx context.Context, cfg RegistryServiceConfig) (*RegistryService, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cfg = cfg.Normalize()
	if cfg.Empty() {
		return &RegistryService{}, nil
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
		service := &RegistryService{db: db}
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
		return service, nil
	}

	service := &RegistryService{}
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

// Close releases all registry service resources.
func (s *RegistryService) Close() error {
	if s == nil {
		return nil
	}
	var firstErr error
	for i := len(s.closers) - 1; i >= 0; i-- {
		if s.closers[i] == nil {
			continue
		}
		if err := s.closers[i](); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if s.db != nil {
		if err := s.db.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
