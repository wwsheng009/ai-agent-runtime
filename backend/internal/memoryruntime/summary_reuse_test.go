package memoryruntime

import (
	"context"
	"database/sql"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
	"github.com/wwsheng009/ai-agent-runtime/internal/sqliteutil"
)

// This generator is deliberately offline and records the actual bounded
// inputs. Reconstructing it simulates a restart without preserving any cache.
type summaryReuseGenerator struct {
	calls  int
	inputs []ProjectSummaryInput
	err    error
}

func (f *summaryReuseGenerator) SummarizeProject(_ context.Context, input ProjectSummaryInput) (ProjectSummaryOutput, error) {
	f.calls++
	f.inputs = append(f.inputs, input)
	if f.err != nil {
		return ProjectSummaryOutput{}, f.err
	}
	text, _, _ := deterministicSummary(input.Records, input.TokenBudget)
	return ProjectSummaryOutput{Text: text, Model: "offline-summary"}, nil
}

func newSummaryReuseConsolidator(t *testing.T, mode string) (*Consolidator, *memorystore.Store, *summaryReuseGenerator) {
	t.Helper()
	store, err := memorystore.New(memorystore.Config{Root: t.TempDir(), Mode: "on", InjectionMode: mode})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	generator := &summaryReuseGenerator{}
	c := NewConsolidator(store, Config{
		Mode: "shadow", SummaryTokenBudget: 400, Summarizer: generator,
		ExtractionModel: "extraction-model", ExtractionProvider: "offline",
		ExtractionReasoningEffort: "low",
	})
	now := time.Now().UTC()
	c.Clock = func() time.Time { return now }
	return c, store, generator
}

func insertSummaryReuseCandidate(t *testing.T, store *memorystore.Store, text string) memorystore.Record {
	t.Helper()
	record, err := store.InsertCandidate(context.Background(), memorystore.Candidate{
		SessionID: "summary-reuse", Kind: "decision", Text: text,
		Confidence: 0.9, SourceHash: "offline-source", SourceRevision: 1,
	})
	require.NoError(t, err)
	return record
}

func advanceSummaryReuseCycle(c *Consolidator) {
	next := c.now().Add(c.Config.ConsolidationInterval)
	c.Clock = func() time.Time { return next }
}

func summaryReuseSQL(t *testing.T, store *memorystore.Store) *sql.DB {
	t.Helper()
	db, err := sqliteutil.OpenFile(store.DBPath(), true)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestConsolidatorSummaryReuseAcrossCyclesAndRestart(t *testing.T) {
	for _, mode := range []string{"summary", "hybrid", "notes"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			c, store, generator := newSummaryReuseConsolidator(t, mode)
			insertSummaryReuseCandidate(t, store, "Use C++ v1.2 and /cache/a-b.")
			first, err := c.Run(ctx)
			require.NoError(t, err)
			require.NotEmpty(t, first.SummaryID)
			require.Equal(t, 1, generator.calls)
			original, err := store.ActiveSummary(ctx)
			require.NoError(t, err)
			require.NotEmpty(t, original.Metadata[summaryInputFingerprintKey])
			require.Equal(t, projectSummaryTemplateVersion, original.Metadata["summary_template_version"])

			// Same-bucket behavior remains a no-op, not a cached result.
			same, err := c.Run(ctx)
			require.NoError(t, err)
			require.Empty(t, same.SummaryID)
			require.Equal(t, 1, generator.calls)

			for cycle := 0; cycle < 2; cycle++ {
				advanceSummaryReuseCycle(c)
				reused, err := c.Run(ctx)
				require.NoError(t, err)
				require.Equal(t, first.SummaryID, reused.SummaryID)
				require.Equal(t, first.SummaryText, reused.SummaryText)
				require.Equal(t, first.SummaryModel, reused.SummaryModel)
				require.Equal(t, first.Revision, reused.Revision)
				require.Equal(t, first.TokenCount, reused.TokenCount)
				require.Zero(t, reused.Rebuilt)
				require.Equal(t, 1, generator.calls, "unchanged buckets must not call the generator")
			}
			jobs, err := store.ListJobs(ctx, 10)
			require.NoError(t, err)
			require.Len(t, jobs, 3)
			for _, job := range jobs {
				require.Equal(t, memorystore.JobStatusSucceeded, job.Status)
				require.Equal(t, original.InputWatermark, job.LastSuccessWatermark)
			}
			summaries, err := store.ListSummaries(ctx, 10)
			require.NoError(t, err)
			require.Len(t, summaries, 1)
			require.Equal(t, original.GeneratedAt, summaries[0].GeneratedAt)

			root, projectKey, cfg, now := store.Root(), store.ProjectKey(), c.Config, c.now()
			require.NoError(t, store.Close())
			reopened, err := memorystore.New(memorystore.Config{
				Root: root, ProjectKey: projectKey, Mode: "on", InjectionMode: mode,
			})
			require.NoError(t, err)
			t.Cleanup(func() { _ = reopened.Close() })
			restartedGenerator := &summaryReuseGenerator{}
			cfg.Summarizer = restartedGenerator
			restarted := NewConsolidator(reopened, cfg)
			restarted.Clock = func() time.Time { return now.Add(cfg.ConsolidationInterval) }
			reused, err := restarted.Run(ctx)
			require.NoError(t, err)
			require.Equal(t, first.SummaryID, reused.SummaryID)
			require.Equal(t, first.SummaryText, reused.SummaryText)
			require.Zero(t, restartedGenerator.calls, "reuse must survive process-local state loss")
		})
	}
}

func TestConsolidatorSummaryReuseRebuildsChangedRecords(t *testing.T) {
	for _, sameCycle := range []bool{false, true} {
		name := "next_cycle"
		if sameCycle {
			name = "same_cycle_stale_rebuild"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			c, store, generator := newSummaryReuseConsolidator(t, "summary")
			record := insertSummaryReuseCandidate(t, store, "Use C++ v1.2.")
			first, err := c.Run(ctx)
			require.NoError(t, err)
			corrected := "Use C# v1-2."
			_, err = store.UpdateRecord(ctx, record.ID, memorystore.RecordPatch{Text: &corrected})
			require.NoError(t, err)
			if !sameCycle {
				advanceSummaryReuseCycle(c)
			}
			rebuilt, err := c.Run(ctx)
			require.NoError(t, err)
			require.Equal(t, 2, generator.calls)
			require.NotEqual(t, first.SummaryID, rebuilt.SummaryID)
			require.Equal(t, first.Revision+1, rebuilt.Revision)
			require.Contains(t, rebuilt.SummaryText, corrected)
			require.NotContains(t, rebuilt.SummaryText, "C++")
			if sameCycle {
				require.Equal(t, 1, rebuilt.Rebuilt)
			}
			advanceSummaryReuseCycle(c)
			reused, err := c.Run(ctx)
			require.NoError(t, err)
			require.Equal(t, rebuilt.SummaryID, reused.SummaryID)
			require.Equal(t, 2, generator.calls, "a completed rebuild must also be reusable")
		})
	}
}

func TestConsolidatorSummaryReuseChangesGenerationSettings(t *testing.T) {
	tests := map[string]func(*Config){
		"model":             func(cfg *Config) { cfg.ConsolidationModel = "summary-v2" },
		"provider":          func(cfg *Config) { cfg.ConsolidationProvider = "provider-v2" },
		"effort":            func(cfg *Config) { cfg.ConsolidationReasoningEffort = "high" },
		"fallback_model":    func(cfg *Config) { cfg.ExtractionModel = "fallback-v2" },
		"fallback_provider": func(cfg *Config) { cfg.ExtractionProvider = "fallback-provider-v2" },
		"fallback_effort":   func(cfg *Config) { cfg.ExtractionReasoningEffort = "medium" },
		"budget":            func(cfg *Config) { cfg.SummaryTokenBudget++ },
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			c, store, generator := newSummaryReuseConsolidator(t, "summary")
			insertSummaryReuseCandidate(t, store, "Use stable summary settings.")
			first, err := c.Run(ctx)
			require.NoError(t, err)
			change(&c.Config)
			same, err := c.Run(ctx)
			require.NoError(t, err)
			require.Empty(t, same.SummaryID, "configuration must not defeat same-bucket job idempotency")
			require.Equal(t, 1, generator.calls)

			advanceSummaryReuseCycle(c)
			changed, err := c.Run(ctx)
			require.NoError(t, err)
			require.NotEqual(t, first.SummaryID, changed.SummaryID)
			require.Equal(t, 2, generator.calls)
			advanceSummaryReuseCycle(c)
			reused, err := c.Run(ctx)
			require.NoError(t, err)
			require.Equal(t, changed.SummaryID, reused.SummaryID)
			require.Equal(t, 2, generator.calls)
		})
	}
}

func TestConsolidatorSummaryReuseUpgradesLegacyOnce(t *testing.T) {
	ctx := context.Background()
	c, store, generator := newSummaryReuseConsolidator(t, "summary")
	record := insertSummaryReuseCandidate(t, store, "Preserve the legacy source.")
	record, err := store.PromoteCandidate(ctx, record.ID)
	require.NoError(t, err)
	text, ids, tokens := deterministicSummary([]memorystore.Record{record}, c.Config.SummaryTokenBudget)
	legacy, err := store.CompileSummary(ctx, memorystore.Summary{
		Text: text, TokenEstimate: tokens, Model: "offline-summary",
		Metadata: map[string]interface{}{"compiler": "llm_v1"},
	}, ids)
	require.NoError(t, err)
	upgraded, err := c.Run(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, generator.calls)
	require.NotEqual(t, legacy.ID, upgraded.SummaryID)
	require.Equal(t, legacy.Revision+1, upgraded.Revision)
	advanceSummaryReuseCycle(c)
	reused, err := c.Run(ctx)
	require.NoError(t, err)
	require.Equal(t, upgraded.SummaryID, reused.SummaryID)
	require.Equal(t, 1, generator.calls)
	summaries, err := store.ListSummaries(ctx, 10)
	require.NoError(t, err)
	require.Len(t, summaries, 2)
}

func TestConsolidatorSummaryReuseRejectsInvalidSummary(t *testing.T) {
	mutations := map[string]string{
		"stale":             `UPDATE memory_summaries SET freshness_status = 'stale' WHERE id = ?`,
		"not_active":        `UPDATE memory_summaries SET status = 'superseded' WHERE id = ?`,
		"watermark":         `UPDATE memory_summaries SET input_watermark = 'invalid' WHERE id = ?`,
		"missing_watermark": `UPDATE memory_summaries SET input_watermark = '' WHERE id = ?`,
		"missing_links":     `DELETE FROM memory_summary_items WHERE summary_id = ?`,
		"wrong_record_ids":  `UPDATE memory_summaries SET record_ids_json = '["absent"]' WHERE id = ?`,
		"source_count":      `UPDATE memory_summaries SET source_count = 0 WHERE id = ?`,
		"content_hash":      `UPDATE memory_summaries SET content_hash = 'invalid' WHERE id = ?`,
		"tokens":            `UPDATE memory_summaries SET summary_tokens = 0 WHERE id = ?`,
		"empty_text":        `UPDATE memory_summaries SET summary = '' WHERE id = ?`,
		"invalid_structure": `UPDATE memory_summaries SET summary = 'unstructured text' WHERE id = ?`,
		"old_fingerprint":   `UPDATE memory_summaries SET metadata_json = '{"compiler":"llm_v1","input_fingerprint":"v0:old"}' WHERE id = ?`,
		"bad_metadata":      `UPDATE memory_summaries SET metadata_json = '{' WHERE id = ?`,
	}
	for name, mutation := range mutations {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			c, store, generator := newSummaryReuseConsolidator(t, "summary")
			insertSummaryReuseCandidate(t, store, "Keep the valid original source.")
			first, err := c.Run(ctx)
			require.NoError(t, err)
			_, err = summaryReuseSQL(t, store).ExecContext(ctx, mutation, first.SummaryID)
			require.NoError(t, err)
			advanceSummaryReuseCycle(c)
			rebuilt, err := c.Run(ctx)
			require.NoError(t, err)
			require.NotEqual(t, first.SummaryID, rebuilt.SummaryID)
			require.Equal(t, 2, generator.calls)
			selection, err := store.SelectForInjectV2(memorystore.InjectOptions{TokenBudget: 1000, Now: c.now(), SkipTrace: true})
			require.NoError(t, err)
			require.NotNil(t, selection.Summary)
			require.Equal(t, rebuilt.SummaryID, selection.Summary.ID)
			advanceSummaryReuseCycle(c)
			reused, err := c.Run(ctx)
			require.NoError(t, err)
			require.Equal(t, rebuilt.SummaryID, reused.SummaryID)
			require.Equal(t, 2, generator.calls)
		})
	}
}

func TestConsolidatorSummaryReuseRetriesModelFallback(t *testing.T) {
	ctx := context.Background()
	c, store, generator := newSummaryReuseConsolidator(t, "summary")
	insertSummaryReuseCandidate(t, store, "Keep a safe deterministic fallback.")
	generator.err = context.DeadlineExceeded
	fallback, err := c.Run(ctx)
	require.NoError(t, err)
	require.Equal(t, "deterministic", fallback.SummaryModel)
	generator.err = nil
	advanceSummaryReuseCycle(c)
	recovered, err := c.Run(ctx)
	require.NoError(t, err)
	require.NotEqual(t, fallback.SummaryID, recovered.SummaryID)
	require.Equal(t, "offline-summary", recovered.SummaryModel)
	require.Equal(t, 2, generator.calls)
	advanceSummaryReuseCycle(c)
	reused, err := c.Run(ctx)
	require.NoError(t, err)
	require.Equal(t, recovered.SummaryID, reused.SummaryID)
	require.Equal(t, 2, generator.calls)
}

func TestConsolidatorSummaryReuseDeterministicAndGeneratorTransition(t *testing.T) {
	ctx := context.Background()
	c, store, generator := newSummaryReuseConsolidator(t, "summary")
	c.Config.Summarizer = nil
	insertSummaryReuseCandidate(t, store, "Use deterministic summaries when no model is configured.")
	first, err := c.Run(ctx)
	require.NoError(t, err)
	advanceSummaryReuseCycle(c)
	reused, err := c.Run(ctx)
	require.NoError(t, err)
	require.Equal(t, first.SummaryID, reused.SummaryID)
	require.Zero(t, generator.calls)
	c.Config.Summarizer = generator
	advanceSummaryReuseCycle(c)
	generated, err := c.Run(ctx)
	require.NoError(t, err)
	require.NotEqual(t, first.SummaryID, generated.SummaryID)
	require.Equal(t, 1, generator.calls)
}

func TestConsolidatorSummaryReuseChangesSelectedSources(t *testing.T) {
	for _, mutation := range []string{"new_source", "revision_only", "tags", "expired", "source_stale"} {
		t.Run(mutation, func(t *testing.T) {
			ctx := context.Background()
			c, store, generator := newSummaryReuseConsolidator(t, "summary")
			record := insertSummaryReuseCandidate(t, store, "Use an independently versioned compiler.")
			insertSummaryReuseCandidate(t, store, "Keep an independent workflow baseline.")
			first, err := c.Run(ctx)
			require.NoError(t, err)
			switch mutation {
			case "new_source":
				insertSummaryReuseCandidate(t, store, "Track additional deployment requirements.")
			case "revision_only":
				_, err = store.UpdateRecord(ctx, record.ID, memorystore.RecordPatch{})
			case "tags":
				tags := []string{"compiler"}
				_, err = store.UpdateRecord(ctx, record.ID, memorystore.RecordPatch{Tags: &tags})
			case "expired":
				_, err = summaryReuseSQL(t, store).ExecContext(ctx, `UPDATE memory_entries SET valid_until = ? WHERE id = ?`, c.now().Add(time.Hour).Format(time.RFC3339Nano), record.ID)
			case "source_stale":
				// Simulate lagged summary invalidation: the summary still claims
				// fresh, but one of its linked sources is no longer eligible.
				_, err = summaryReuseSQL(t, store).ExecContext(ctx, `UPDATE memory_entries SET freshness_status = 'stale' WHERE id = ?`, record.ID)
			}
			require.NoError(t, err)
			advanceSummaryReuseCycle(c)
			changed, err := c.Run(ctx)
			require.NoError(t, err)
			require.NotEqual(t, first.SummaryID, changed.SummaryID)
			require.Equal(t, 2, generator.calls)
			if mutation == "expired" || mutation == "source_stale" {
				require.Len(t, generator.inputs[1].Records, 1)
				require.NotEqual(t, record.ID, generator.inputs[1].Records[0].ID)
			}
		})
	}
}

func TestProjectSummaryFingerprintTracksEffectiveInput(t *testing.T) {
	c := NewConsolidator(nil, Config{SummaryTokenBudget: 400, ExtractionReasoningEffort: "low"})
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	record := memorystore.Record{
		ID: "source", Scope: memorystore.ScopeProject, ProjectKey: "project", Kind: "decision",
		Text: "Use C++ v1.2 and /cache/a-b.", Confidence: 0.9, Tags: []string{"compiler"},
		Revision: 2, SourceRevision: 3, SourceHash: "source-hash", ContentHash: "content-hash",
		Status: memorystore.StatusActive, Freshness: memorystore.FreshnessFresh,
		SourceKind: "session_extract", UpdatedAt: now, ValidUntil: now.Add(24 * time.Hour),
	}
	baseline := c.prepareSummary([]memorystore.Record{record})
	require.NotEmpty(t, baseline.fingerprint)
	for i := 0; i < 3; i++ {
		require.Equal(t, baseline.fingerprint, c.prepareSummary([]memorystore.Record{record}).fingerprint)
	}
	changes := map[string]func(*memorystore.Record){
		"technical_text":  func(r *memorystore.Record) { r.Text = "Use C# v1-2 and /cache/a/b." },
		"revision":        func(r *memorystore.Record) { r.Revision++ },
		"content_hash":    func(r *memorystore.Record) { r.ContentHash = "changed" },
		"source_hash":     func(r *memorystore.Record) { r.SourceHash = "changed" },
		"source_revision": func(r *memorystore.Record) { r.SourceRevision++ },
		"freshness":       func(r *memorystore.Record) { r.Freshness = memorystore.FreshnessStale },
		"status":          func(r *memorystore.Record) { r.Status = memorystore.StatusSuperseded },
		"kind":            func(r *memorystore.Record) { r.Kind = "fact" },
		"tags":            func(r *memorystore.Record) { r.Tags = []string{"new-tag"} },
		"confidence":      func(r *memorystore.Record) { r.Confidence = 0.8 },
		"expiry":          func(r *memorystore.Record) { r.ValidUntil = r.ValidUntil.Add(time.Hour) },
		"revalidation":    func(r *memorystore.Record) { r.RevalidateAfter = now },
		"deleted":         func(r *memorystore.Record) { r.DeletedAt = now },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			changed := record
			change(&changed)
			require.NotEqual(t, baseline.fingerprint, c.prepareSummary([]memorystore.Record{changed}).fingerprint)
		})
	}
	usageOnly := record
	usageOnly.UseCount++
	usageOnly.LastUsedAt = now.Add(time.Hour)
	usageOnly.UpdatedAt = now.Add(time.Hour)
	usageOnly.ValidUntil = record.ValidUntil.In(time.FixedZone("equivalent", 8*60*60))
	require.Equal(t, baseline.fingerprint, c.prepareSummary([]memorystore.Record{usageOnly}).fingerprint,
		"usage bookkeeping and time-zone representation do not change a selected input")
	unserializable := record
	unserializable.Confidence = math.NaN()
	require.Empty(t, c.prepareSummary([]memorystore.Record{unserializable}).fingerprint)
}

func TestProjectSummaryFingerprintUsesBudgetedRecordsAndOrder(t *testing.T) {
	c := NewConsolidator(nil, Config{SummaryTokenBudget: 400})
	selected := memorystore.Record{ID: "selected", Kind: "decision", Text: "Use C++.", Confidence: 0.9}
	_, _, tokens := deterministicSummary([]memorystore.Record{selected}, 400)
	c.Config.SummaryTokenBudget = tokens + 1
	excluded := memorystore.Record{ID: "excluded", Kind: "decision", Text: strings.Repeat("Long input. ", 100), Confidence: 0.8}
	input := c.prepareSummary([]memorystore.Record{excluded, selected})
	require.Equal(t, []string{selected.ID}, input.ids)
	excluded.Text += "A change beyond the selected budget."
	excluded.Revision++
	require.Equal(t, input.fingerprint, c.prepareSummary([]memorystore.Record{selected, excluded}).fingerprint)

	c.Config.SummaryTokenBudget = 800
	other := memorystore.Record{ID: "other", Kind: "decision", Text: "Use a separate compiler.", Confidence: 0.8}
	ordered := c.prepareSummary([]memorystore.Record{selected, other})
	require.Equal(t, ordered.fingerprint, c.prepareSummary([]memorystore.Record{other, selected}).fingerprint,
		"scan order must not affect the deterministic selection")
	reversed := ordered
	reversed.records = slices.Clone(ordered.records)
	slices.Reverse(reversed.records)
	require.NotEqual(t, ordered.fingerprint, c.summaryInputFingerprint(reversed),
		"the actual prompt record order is part of generation identity")
}

func TestProjectSummaryFingerprintGenerationFallbacks(t *testing.T) {
	records := []memorystore.Record{{ID: "source", Kind: "fact", Text: "Use the configured stage settings."}}
	c := NewConsolidator(nil, Config{SummaryTokenBudget: 400})
	baseline := c.prepareSummary(records).fingerprint
	c.Config.ExtractionReasoningEffort = ""
	require.Equal(t, baseline, c.prepareSummary(records).fingerprint, "empty effort falls back to low")
	c.Config.ConsolidationModel = " stage-model "
	c.Config.ConsolidationProvider = " stage-provider "
	c.Config.ConsolidationReasoningEffort = " high "
	explicit := c.prepareSummary(records).fingerprint
	c.Config.ConsolidationModel = "stage-model"
	c.Config.ConsolidationProvider = "stage-provider"
	c.Config.ConsolidationReasoningEffort = "high"
	c.Config.ExtractionModel = "ignored-fallback"
	c.Config.ExtractionProvider = "ignored-fallback"
	c.Config.ExtractionReasoningEffort = "medium"
	c.Config.WindowSummaryReasoningEffort = "low"
	require.Equal(t, explicit, c.prepareSummary(records).fingerprint,
		"only effective project-summary routing should affect its fingerprint")

	// Inspect a real summarizer's configuration snapshot without ever invoking
	// its runtime (Runtime is nil). The fingerprint must notice snapshot edits.
	summarizer := &LLMProjectSummarizer{Config: c.Config}
	c.Config.Summarizer = summarizer
	actual := c.prepareSummary(records).fingerprint
	summarizer.Config.ConsolidationModel = "snapshot-model-v2"
	require.NotEqual(t, actual, c.prepareSummary(records).fingerprint)
}

func TestProjectSummaryFingerprintRuntimeDefaults(t *testing.T) {
	// No providers are registered and no calls are made: only local runtime
	// configuration is inspected.
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{DefaultProvider: "provider-a", DefaultModel: "model-a"})
	summarizer := &LLMProjectSummarizer{Runtime: runtime}
	c := NewConsolidator(nil, Config{Summarizer: summarizer})
	records := []memorystore.Record{{ID: "source", Kind: "fact", Text: "Use the runtime's default model."}}
	first := c.prepareSummary(records).fingerprint
	runtime.SetDefaultModel("model-b")
	second := c.prepareSummary(records).fingerprint
	require.NotEqual(t, first, second)
	runtime.SetDefaultProvider("provider-b")
	require.NotEqual(t, second, c.prepareSummary(records).fingerprint)
	summarizer.Config.ConsolidationProvider = "explicit-provider"
	summarizer.Config.ConsolidationModel = "explicit-model"
	explicit := c.prepareSummary(records).fingerprint
	runtime.SetDefaultProvider("ignored-provider")
	runtime.SetDefaultModel("ignored-model")
	require.Equal(t, explicit, c.prepareSummary(records).fingerprint)
}

func TestConsolidatorSummaryReuseIgnoresUnselectedChanges(t *testing.T) {
	ctx := context.Background()
	c, store, generator := newSummaryReuseConsolidator(t, "summary")
	selected := insertSummaryReuseCandidate(t, store, "Use C++.")
	_, _, tokens := deterministicSummary([]memorystore.Record{selected}, 400)
	c.Config.SummaryTokenBudget = tokens + 1
	excluded, err := store.InsertCandidate(ctx, memorystore.Candidate{
		SessionID: "budget-excluded", Kind: "decision", Text: strings.Repeat("Unselected input. ", 100),
		Confidence: 0.8, SourceHash: "long-source", SourceRevision: 1,
	})
	require.NoError(t, err)
	first, err := c.Run(ctx)
	require.NoError(t, err)
	require.Len(t, generator.inputs, 1)
	require.Len(t, generator.inputs[0].Records, 1)
	require.Equal(t, selected.ID, generator.inputs[0].Records[0].ID)
	changed := excluded.Text + "Unselected tail revision."
	_, err = store.UpdateRecord(ctx, excluded.ID, memorystore.RecordPatch{Text: &changed})
	require.NoError(t, err)
	advanceSummaryReuseCycle(c)
	reused, err := c.Run(ctx)
	require.NoError(t, err)
	require.Equal(t, first.SummaryID, reused.SummaryID)
	require.Equal(t, 1, generator.calls)
}
