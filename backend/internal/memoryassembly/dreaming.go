package memoryassembly

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
)

// RunDreamingStage runs one bounded automatic Dreaming pass. It creates only a
// reviewable patch and coalesces while a previous Dreaming patch is still
// proposed, so a periodic scheduler cannot spam the review queue.
func RunDreamingStage(
	ctx context.Context,
	store *memorystore.Store,
	limit, maxTokens int,
	shadow bool,
	retentionDays int,
	analyzer memorystore.DreamAnalyzer,
) error {
	if store == nil {
		return fmt.Errorf("memory dreaming: store is nil")
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	if maxTokens <= 0 {
		maxTokens = 800
	}
	if maxTokens > 4000 {
		maxTokens = 4000
	}
	if retentionDays <= 0 {
		retentionDays = 30
	}
	if _, err := store.PruneShadowDreamRuns(ctx, time.Now().UTC().Add(-time.Duration(retentionDays)*24*time.Hour)); err != nil {
		return err
	}
	runs, err := store.ListDreamRuns(ctx, 1)
	if err != nil {
		return err
	}
	if len(runs) > 0 && runs[0].Status == memorystore.DreamRunRunning {
		return nil
	}
	episodes, err := store.ListEpisodes(ctx, memorystore.EpisodeFilter{Limit: limit})
	if err != nil {
		return err
	}
	if len(episodes) < 2 {
		return nil
	}
	sourceHash := memorystore.DreamSourceHash(episodes)
	if !shadow {
		exists, err := store.DreamSourceExists(ctx, sourceHash)
		if err != nil {
			return err
		}
		if exists {
			return nil
		}
	}
	run, err := store.RunDreamPipeline(ctx, memorystore.DreamRunOptions{
		Limit: limit, MaxTokens: maxTokens, CreatedBy: "maintenance", Analyzer: analyzer, Shadow: shadow,
	})
	if err != nil {
		return err
	}
	if run.Status == memorystore.DreamRunFailed {
		return fmt.Errorf("memory dreaming: run failed: %s", strings.TrimSpace(run.Error))
	}
	return nil
}
