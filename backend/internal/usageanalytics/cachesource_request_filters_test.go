package usageanalytics

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"

	cacheanalytics "github.com/wwsheng009/ai-agent-runtime/internal/cacheanalytics"
)

func TestCacheSourceRequestsFilterProviderStatusAndDuration(t *testing.T) {
	store, err := Open(Config{Path: filepath.Join(t.TempDir(), "usage_analytics.sqlite")})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = store.Close() }()

	collector := newCollector(store, nil, nil)
	finished := time.Now()
	records := []cacheanalytics.CacheRequestRecord{
		{
			LLMRequestID: "fast",
			SessionID:    "s-filters",
			Provider:     "OpenAI",
			Model:        "gpt-4o",
			Status:       cacheanalytics.RequestStatusSuccess,
			CacheStatus:  cacheanalytics.CacheStatusHit,
			DurationMS:   100,
			FinishedAt:   &finished,
		},
		{
			LLMRequestID: "slow",
			SessionID:    "s-filters",
			Provider:     "anthropic",
			Model:        "claude-sonnet",
			Status:       cacheanalytics.RequestStatusSuccess,
			CacheStatus:  cacheanalytics.CacheStatusHit,
			DurationMS:   2000,
			FinishedAt:   &finished,
		},
		{
			LLMRequestID: "failed",
			SessionID:    "s-filters",
			Provider:     "openai",
			Model:        "gpt-4o",
			Status:       cacheanalytics.RequestStatusError,
			CacheStatus:  cacheanalytics.CacheStatusError,
			DurationMS:   300,
			FinishedAt:   &finished,
		},
	}
	for _, record := range records {
		collector.upsertRequest(record)
	}
	source := NewCacheSource(store, nil, false)

	overview, err := source.Overview("s-filters")
	if err != nil {
		t.Fatalf("overview dimensions: %v", err)
	}
	if !reflect.DeepEqual(overview.Providers, []string{"anthropic", "OpenAI"}) {
		t.Fatalf("provider dimensions = %#v", overview.Providers)
	}
	if !reflect.DeepEqual(overview.Models, []string{"claude-sonnet", "gpt-4o"}) {
		t.Fatalf("model dimensions = %#v", overview.Models)
	}

	providerPage, err := source.Requests("s-filters", cacheanalytics.RequestQuery{Provider: "openai", Limit: 50})
	if err != nil {
		t.Fatalf("provider filter: %v", err)
	}
	if providerPage.Total != 2 {
		t.Fatalf("provider filter total = %d, want 2", providerPage.Total)
	}

	modelPage, err := source.Requests("s-filters", cacheanalytics.RequestQuery{Model: "CLAUDE-SONNET", Limit: 50})
	if err != nil {
		t.Fatalf("model filter: %v", err)
	}
	if modelPage.Total != 1 || modelPage.Requests[0].LLMRequestID != "slow" {
		t.Fatalf("model filter mismatch: %+v", modelPage)
	}

	statusPage, err := source.Requests("s-filters", cacheanalytics.RequestQuery{Status: "error", Limit: 50})
	if err != nil {
		t.Fatalf("status filter: %v", err)
	}
	if statusPage.Total != 1 || statusPage.Requests[0].LLMRequestID != "failed" {
		t.Fatalf("status filter mismatch: %+v", statusPage)
	}

	durationMin, durationMax := int64(500), int64(3000)
	durationPage, err := source.Requests("s-filters", cacheanalytics.RequestQuery{
		DurationMSMin: &durationMin,
		DurationMSMax: &durationMax,
		Limit:         50,
	})
	if err != nil {
		t.Fatalf("duration filter: %v", err)
	}
	if durationPage.Total != 1 || durationPage.Requests[0].LLMRequestID != "slow" {
		t.Fatalf("duration filter mismatch: %+v", durationPage)
	}
}
