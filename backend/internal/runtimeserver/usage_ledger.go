package runtimeserver

import (
	"fmt"
	"strings"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/usageledger"
)

func BuildUsageLedgerStore(cfg *config.Config) (*usageledger.SQLiteStore, error) {
	if cfg == nil || cfg.SkillsRuntime == nil || !cfg.SkillsRuntime.UsageLedgerEnabled {
		return nil, nil
	}

	driver := strings.TrimSpace(cfg.Database.Driver)
	dsn := strings.TrimSpace(cfg.Database.DSN)
	if dsn == "" {
		return nil, fmt.Errorf("skills usage ledger is enabled but database.dsn is empty")
	}

	store, err := usageledger.NewSQLiteStore(&usageledger.Config{
		Driver: driver,
		DSN:    dsn,
	})
	if err != nil {
		return nil, fmt.Errorf("initialize skills usage ledger store: %w", err)
	}
	return store, nil
}
