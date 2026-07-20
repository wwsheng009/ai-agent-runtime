package llm

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

func TestRetryConfigAdapterPreservesScheduleAndUnlimitedRetries(t *testing.T) {
	cfg := &agentconfig.Config{
		Providers: agentconfig.ProvidersConfig{
			MaxRetries: -1,
			Backoff: agentconfig.BackoffConfig{
				MaxInterval:   6 * time.Minute,
				Randomization: 0.1,
				Schedule:      []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 3 * time.Minute, 5 * time.Minute},
			},
		},
	}

	tuning := RetryTuningFromAgentConfig(cfg)
	require.Equal(t, cfg.Providers.Backoff.Schedule, tuning.Schedule)
	require.Equal(t, -1, ProviderMaxRetriesFromAgentConfig(cfg))
	require.Equal(t, -1, ProviderMaxRetriesFromAgentConfig(nil))
}
