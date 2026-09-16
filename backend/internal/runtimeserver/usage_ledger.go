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

// ResolveUsageLedgerStore 构建可选的 usage ledger store，并把「启用但初始化失败」
// 表达为 reason 而不是致命错误。
//
// 与 BuildUsageLedgerStore 的差别只在语义：usage ledger 是可选的观测 / 治理能力
// （见 docs/skill_runtime/skills_usage_quota.md），不是 runtime-server 可用性的前置依赖。
// 因此调用方（cmd/runtime-server）用本函数降级为「账本接口 503 + 启动告警」，
// 而不是让整个进程起不来；需要 fail-fast 的场景仍可直接调用 BuildUsageLedgerStore。
//
// 返回值三态：
//   - store != nil, reason == ""：账本可用；
//   - store == nil, reason == ""：配置未启用账本（正常的关闭态）；
//   - store == nil, reason != ""：已启用但不可用，reason 是可直接排障的原因。
func ResolveUsageLedgerStore(cfg *config.Config) (store *usageledger.SQLiteStore, reason string) {
	built, err := BuildUsageLedgerStore(cfg)
	if err != nil {
		return nil, err.Error()
	}
	return built, ""
}
