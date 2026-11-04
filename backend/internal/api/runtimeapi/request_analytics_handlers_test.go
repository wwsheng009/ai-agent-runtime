package runtimeapi

import (
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseAnalyticsRequestQueryLowCacheAndFilters(t *testing.T) {
	req := httptest.NewRequest(
		"GET",
		"/api/runtime/analytics/requests?from=2026-11-04&to=2026-11-04&provider=openai&success=false&cache_hit_ratio_lt=0.5&cache_reported=true&limit=200",
		nil,
	)
	query, err := parseAnalyticsRequestQuery(req)
	require.NoError(t, err)
	require.Equal(t, "openai", query.Provider)
	require.NotNil(t, query.Success)
	require.False(t, *query.Success)
	require.NotNil(t, query.CacheHitRatioBelow)
	require.Equal(t, 0.5, *query.CacheHitRatioBelow)
	require.True(t, query.CacheReportedOnly)
	require.Equal(t, 200, query.Limit)
	require.Equal(t, 2026, query.From.Year())
	require.Equal(t, 2026, query.To.Year())
}

func TestParseAnalyticsRequestQueryRejectsInvalidRatio(t *testing.T) {
	for _, target := range []string{
		"/api/runtime/analytics/requests?cache_hit_ratio_lt=1.2",
		"/api/runtime/analytics/requests?cache_hit_ratio_gte=-0.1",
		"/api/runtime/analytics/requests?cache_hit_ratio_min=0.8&cache_hit_ratio_max=0.2",
	} {
		req := httptest.NewRequest("GET", target, nil)
		_, err := parseAnalyticsRequestQuery(req)
		require.Error(t, err, target)
	}
}
