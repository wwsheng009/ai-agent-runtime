package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestUsageAnalyticsRetentionDefaultsAndValidation(t *testing.T) {
	cfg := DefaultRuntimeConfig()
	require.True(t, cfg.UsageAnalytics.Retention.Enabled)
	require.Equal(t, 90, cfg.UsageAnalytics.Retention.Days)
	require.Equal(t, 6*time.Hour, cfg.UsageAnalytics.Retention.Interval)
	require.Equal(t, 2000, cfg.UsageAnalytics.Retention.BatchSize)
	require.NoError(t, ValidateUsageAnalyticsConfig(&cfg.UsageAnalytics))

	cfg.UsageAnalytics.Retention.Days = 0
	require.Error(t, ValidateUsageAnalyticsConfig(&cfg.UsageAnalytics))

	cfg.UsageAnalytics.Retention = UsageAnalyticsRetentionConfig{Enabled: false}
	require.NoError(t, ValidateUsageAnalyticsConfig(&cfg.UsageAnalytics))
}
