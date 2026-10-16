package tools

import (
	"os"
	"strconv"
	"strings"
	"time"

	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
)

// resolveWebSearchTimeout resolves one of the two timeout budgets (P1-2):
// env override (milliseconds) > runtime config (milliseconds) > built-in
// default. An invalid or non-positive env value falls through to the config
// value (never silently disables the budget); values above the deployment
// ceiling are clamped.
func resolveWebSearchTimeout(envName string, configuredMs int, fallback, ceiling time.Duration) time.Duration {
	if raw := strings.TrimSpace(os.Getenv(envName)); raw != "" {
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil && parsed > 0 {
			return clampWebSearchTimeout(time.Duration(parsed)*time.Millisecond, ceiling)
		}
	}
	if configuredMs > 0 {
		return clampWebSearchTimeout(time.Duration(configuredMs)*time.Millisecond, ceiling)
	}
	return fallback
}

func clampWebSearchTimeout(value, ceiling time.Duration) time.Duration {
	if ceiling > 0 && value > ceiling {
		return ceiling
	}
	return value
}

func webSearchChainTimeoutMsFromConfig(cfg *runtimecfg.RuntimeConfig) int {
	if cfg == nil {
		return 0
	}
	return cfg.WebSearch.ChainTimeoutMs
}

func webSearchRequestTimeoutMsFromConfig(cfg *runtimecfg.RuntimeConfig) int {
	if cfg == nil {
		return 0
	}
	return cfg.WebSearch.RequestTimeoutMs
}
