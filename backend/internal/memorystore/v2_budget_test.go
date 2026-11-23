package memorystore

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSelectForInjectV2CategoryBudgetsHaveHardCapsAndKeepCoreFirst(t *testing.T) {
	store, err := New(Config{
		Root: t.TempDir(), Mode: "on", ProjectKey: "category-budget",
		InjectionMode: "notes", PinnedBudgetRatio: 0.2,
		CategoryBudgets: InjectionCategoryBudgets{
			Core: 0.20, Task: 0.15, Retrieved: 0.40,
			Procedure: 0.15, Reference: 0.10,
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	ctx := context.Background()
	source := seedActiveDerivedMemory(t, store, "safe source for the reviewed core", "reference")
	candidate, err := store.CreateCoreCandidate(ctx, CoreMemoryCandidateOptions{
		Content:   "Keep reviewed project constraints ahead of ordinary recall.",
		SourceIDs: []string{source.ID},
	})
	require.NoError(t, err)
	_, err = store.ActivateCore(ctx, candidate.ID, "admin:test")
	require.NoError(t, err)

	for _, fixture := range []struct {
		kind string
		text string
	}{
		{"task", strings.Repeat("task constraint ", 20)},
		{"task", strings.Repeat("task followup ", 20)},
		{"procedure", strings.Repeat("procedure step ", 20)},
		{"procedure", strings.Repeat("procedure check ", 20)},
		{"reference", strings.Repeat("reference note ", 20)},
		{"reference", strings.Repeat("reference detail ", 20)},
		{"decision", strings.Repeat("retrieved decision ", 20)},
		{"fact", strings.Repeat("retrieved fact ", 20)},
	} {
		seedActiveDerivedMemory(t, store, fixture.text, fixture.kind)
	}

	selection, err := store.SelectForInjectV2(InjectOptions{
		Limit: 20, TokenBudget: 200,
	})
	require.NoError(t, err)
	require.NotNil(t, selection.Core, "reviewed Core must be selected before ordinary recall")
	require.NotEmpty(t, selection.CategoryBudget)
	require.NotEmpty(t, selection.CategoryUsed)
	require.LessOrEqual(t, selection.TokenEstimate, 200)
	totalCategoryUsed := 0
	foundCategoryDrop := false
	for category, used := range selection.CategoryUsed {
		require.LessOrEqual(t, used, selection.CategoryBudget[category],
			"category %s exceeded its effective hard cap", category)
		totalCategoryUsed += used
	}
	for _, dropped := range selection.Dropped {
		if dropped.Reason == "category_budget" && dropped.Category != "" {
			foundCategoryDrop = true
			break
		}
	}
	require.Equal(t, selection.TokenEstimate, totalCategoryUsed)
	require.True(t, foundCategoryDrop, "budget drops should identify their category for why-not/trace")
	require.LessOrEqual(t, selection.CategoryUsed[InjectionCategoryCore],
		selection.CategoryBudget[InjectionCategoryCore])
}

func TestSelectForInjectV2CategoryBudgetReleasesEmptyCategoriesToRetrieved(t *testing.T) {
	store, err := New(Config{
		Root: t.TempDir(), Mode: "on", ProjectKey: "category-reflow",
		InjectionMode: "notes",
		CategoryBudgets: InjectionCategoryBudgets{
			Core: 0.20, Task: 0.15, Retrieved: 0.40,
			Procedure: 0.15, Reference: 0.10,
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	seedActiveDerivedMemory(t, store, "ordinary retrieved decision", "decision")
	selection, err := store.SelectForInjectV2(InjectOptions{Limit: 5, TokenBudget: 100})
	require.NoError(t, err)
	require.Equal(t, 100, selection.CategoryBudget[InjectionCategoryRetrieved],
		"unused category allocations should be reusable by retrieved memory")
	require.Zero(t, selection.CategoryBudget[InjectionCategoryCore])
	require.Zero(t, selection.CategoryBudget[InjectionCategoryTask])
	require.Zero(t, selection.CategoryBudget[InjectionCategoryProcedure])
	require.Zero(t, selection.CategoryBudget[InjectionCategoryReference])
	require.LessOrEqual(t, selection.TokenEstimate, 100)
}

func TestSelectForInjectV2CategoryBudgetsRespectMaxItemsAndTotalBudget(t *testing.T) {
	store, err := New(Config{
		Root: t.TempDir(), Mode: "on", ProjectKey: "category-max-items",
		InjectionMode: "notes",
		CategoryBudgets: InjectionCategoryBudgets{
			Core: 0.20, Task: 0.15, Retrieved: 0.40,
			Procedure: 0.15, Reference: 0.10,
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	task := seedActiveDerivedMemory(t, store, "task deadline release gate", "task")
	reference := seedActiveDerivedMemory(t, store, "reference note API behavior", "reference")
	seedActiveDerivedMemory(t, store, "retrieved decision project rationale", "decision")

	selection, err := store.SelectForInjectV2(InjectOptions{Limit: 1, TokenBudget: 400})
	require.NoError(t, err)
	require.Len(t, selection.Recall, 1, "max-items must remain a hard item count")
	require.Equal(t, task.ID, selection.Recall[0].ID,
		"task memory must keep priority over reference/retrieved candidates; selected=%+v task=%+v budgets=%+v",
		selection.Recall[0], task, selection.CategoryBudget)
	require.LessOrEqual(t, selection.TokenEstimate, 400,
		"selected memories plus the reviewed Core/stable stages may not exceed the total budget")
	require.LessOrEqual(t, selection.CategoryUsed[InjectionCategoryTask],
		selection.CategoryBudget[InjectionCategoryTask])
	require.LessOrEqual(t, selection.CategoryUsed[InjectionCategoryReference],
		selection.CategoryBudget[InjectionCategoryReference])
	totalUsed := 0
	for _, used := range selection.CategoryUsed {
		totalUsed += used
	}
	require.Equal(t, selection.TokenEstimate, totalUsed)
	require.Contains(t, selection.Dropped, DroppedRecord{
		ID: reference.ID, Reason: "memory_budget", Category: InjectionCategoryReference,
	}, "max-items drops retain the category needed by why-not")
}

func TestSelectForInjectV2ExplicitZeroCategoryBudgetBlocksMatchingCandidate(t *testing.T) {
	store, err := New(Config{
		Root: t.TempDir(), Mode: "on", ProjectKey: "category-zero-cap",
		InjectionMode: "notes", CategoryBudgetsEnabled: true,
		CategoryBudgets: InjectionCategoryBudgets{
			Task: 0.25, Retrieved: 0.75, Reference: 0,
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	seedActiveDerivedMemory(t, store, "task: preserve the release gate", "task")
	seedActiveDerivedMemory(t, store, "retrieved: project design rationale", "decision")
	reference := seedActiveDerivedMemory(t, store, "reference: archived API behavior", "reference")

	selection, err := store.SelectForInjectV2(InjectOptions{Limit: 10, TokenBudget: 200})
	require.NoError(t, err)
	require.Zero(t, selection.CategoryBudget[InjectionCategoryReference],
		"an explicit zero must stay a hard cap when the category has eligible candidates")
	require.Zero(t, selection.CategoryUsed[InjectionCategoryReference])
	require.NotContains(t, selection.IDs, reference.ID)
	require.Contains(t, selection.Dropped, DroppedRecord{
		ID: reference.ID, Reason: "category_budget", Category: InjectionCategoryReference,
	})
	require.LessOrEqual(t, selection.TokenEstimate, 200)
}

func TestPrioritizeRecallCandidatesPreservesDeclaredPruningOrder(t *testing.T) {
	records := []Record{
		{Kind: "reference"},
		{Kind: "decision"},
		{Kind: "procedure"},
		{Kind: "task"},
	}
	candidates := []recallCandidate{
		{recordIndex: 0, score: 100},
		{recordIndex: 1, score: 90},
		{recordIndex: 2, score: 80},
		{recordIndex: 3, score: 1},
	}

	ordered := prioritizeRecallCandidates(candidates, records)
	got := make([]string, 0, len(ordered))
	for _, candidate := range ordered {
		got = append(got, injectionCategoryForRecord(records[candidate.recordIndex]))
	}
	require.Equal(t, []string{
		InjectionCategoryTask, InjectionCategoryRetrieved,
		InjectionCategoryProcedure, InjectionCategoryReference,
	}, got)
}
