package contextmgr

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

type fakeProjectMemoryV2Store struct {
	selection         memorystore.Selection
	calls             int
	disableStageSplit bool
	lastOptions       memorystore.InjectOptions
}

func (f *fakeProjectMemoryV2Store) SelectForInject(memorystore.InjectOptions) ([]memorystore.Note, error) {
	return nil, nil
}

func (f *fakeProjectMemoryV2Store) SelectForInjectV2(options memorystore.InjectOptions) (memorystore.Selection, error) {
	f.calls++
	f.lastOptions = options
	return f.selection, nil
}

func (f *fakeProjectMemoryV2Store) V2InjectionEnabled() bool      { return true }
func (f *fakeProjectMemoryV2Store) MemoryStageSplitEnabled() bool { return !f.disableStageSplit }

func TestBuildProjectMemoryStableAndRecallSplit(t *testing.T) {
	store := &fakeProjectMemoryV2Store{selection: memorystore.Selection{
		Summary:  &memorystore.Summary{ID: "sum_1", Revision: 7, Text: "Stable project summary."},
		Stable:   []memorystore.Record{{ID: "mem_pinned", Kind: "decision", Text: "Keep injections append-only.", Pinned: true}},
		Recall:   []memorystore.Record{{ID: "mem_recall", Kind: "workflow", Text: "Run context tests before merging."}},
		Revision: 7,
		IDs:      []string{"mem_pinned", "mem_recall"},
	}}
	manager := NewManager(DefaultBudget(), nil)
	manager.ProjectMemory = store

	result := manager.Build(context.Background(), BuildInput{
		TraceID:            "turn-1",
		SessionID:          "session-1",
		Goal:               "memory injection",
		ActiveGoalGuidance: "Keep the stable prefix unchanged.",
		History: []types.Message{
			*types.NewSystemMessage("system"),
			*types.NewUserMessage("implement memory"),
		},
		CountTokens: func(messages []types.Message) int { return len(messages) * 20 },
	})

	var stable, recall *types.Message
	for i := range result.Messages {
		switch result.Messages[i].Metadata.GetString("context_stage", "") {
		case "project_memory":
			stable = &result.Messages[i]
		case "project_memory_recall":
			recall = &result.Messages[i]
		}
	}
	require.NotNil(t, stable)
	require.NotNil(t, recall)
	require.Equal(t, 2, stable.Metadata.GetInt("memory_version", 0))
	require.Equal(t, "stable", stable.Metadata.GetString("memory_block_kind", ""))
	require.Equal(t, int64(7), metadataInt64(stable.Metadata, "memory_revision"))
	require.Equal(t, "recall", recall.Metadata.GetString("memory_block_kind", ""))
	require.Equal(t, "project_memory_recall", result.Messages[len(result.Messages)-1].Metadata.GetString("context_stage", ""))
	require.True(t, result.Metadata["memory_version"] == 2)
	require.Contains(t, store.lastOptions.Query, "memory injection")
	require.Contains(t, store.lastOptions.Query, "implement memory")
}

func TestBuildProjectMemoryCorePrecedesSummaryAndKeepsCoreIDSeparate(t *testing.T) {
	store := &fakeProjectMemoryV2Store{selection: memorystore.Selection{
		Core: &memorystore.CoreMemory{
			ID: "core_1", Version: 3, Content: "Keep the project memory boundary stable.",
			SourceIDs: []string{"mem_evidence"},
		},
		Summary: &memorystore.Summary{
			ID: "sum_1", Revision: 4, Text: "Project summary follows core.",
			RecordIDs: []string{"mem_summary_source"},
		},
		Stable: []memorystore.Record{{
			ID: "mem_pinned", Kind: "decision", Text: "Preserve pinned decisions.", Pinned: true,
		}},
		IDs: []string{"core_1", "mem_summary_source", "mem_pinned"},
	}}
	manager := NewManager(DefaultBudget(), nil)
	manager.ProjectMemory = store

	result := manager.Build(context.Background(), BuildInput{
		TraceID: "turn-core", SessionID: "session-core", Goal: "continue memory work",
		History:     []types.Message{*types.NewSystemMessage("system"), *types.NewUserMessage("continue")},
		CountTokens: func(messages []types.Message) int { return len(messages) * 20 },
	})
	var stable *types.Message
	for i := range result.Messages {
		if result.Messages[i].Metadata.GetString("context_stage", "") == "project_memory" {
			stable = &result.Messages[i]
			break
		}
	}
	require.NotNil(t, stable)
	coreAt := strings.Index(stable.Content, "[reviewed project core v3]")
	summaryAt := strings.Index(stable.Content, "Project summary follows core.")
	pinnedAt := strings.Index(stable.Content, "Preserve pinned decisions.")
	require.NotEqual(t, -1, coreAt)
	require.Less(t, coreAt, summaryAt)
	require.Less(t, summaryAt, pinnedAt)
	require.Equal(t, "core_1", stable.Metadata.GetString("project_core_id", ""))
	require.Equal(t, true, result.Metadata["project_core_injected"])
	require.Equal(t, "core_1", result.Metadata["project_core_id"])
	require.Equal(t, 2, stable.Metadata.GetInt("project_memory_count", 0))
	require.Equal(t, []string{"mem_summary_source", "mem_pinned"},
		metadataStrings(stable.Metadata, "memory_ids"),
		"the synthetic core id must not enter the memory_entries citation/usage id set")
	require.Equal(t, []string{"mem_evidence"},
		metadataStrings(stable.Metadata, "project_core_source_ids"))
}

func TestBuildProjectMemoryGroupInjectedOncePerTurn(t *testing.T) {
	store := &fakeProjectMemoryV2Store{selection: memorystore.Selection{
		Stable: []memorystore.Record{{ID: "mem_stable", Kind: "decision", Text: "stable"}},
		IDs:    []string{"mem_stable"},
	}}
	manager := NewManager(DefaultBudget(), nil)
	manager.ProjectMemory = store
	history := []types.Message{
		*types.NewSystemMessage("system"),
		*types.NewUserMessage("implement memory"),
	}
	first := manager.Build(context.Background(), BuildInput{
		TraceID: "turn-1", SessionID: "session-1", Goal: "memory", History: history,
		CountTokens: func(messages []types.Message) int { return len(messages) * 20 },
	})
	require.Equal(t, 1, store.calls)
	second := manager.Build(context.Background(), BuildInput{
		TraceID: "turn-1", SessionID: "session-1", Goal: "memory",
		History:     first.Messages,
		CountTokens: func(messages []types.Message) int { return len(messages) * 20 },
	})
	require.Equal(t, 1, store.calls, "same turn must reuse the first memory plan")
	require.Len(t, second.Messages, len(first.Messages), "no second memory group should be appended")
}

func TestBuildProjectMemoryWithoutStageSplitMergesRecall(t *testing.T) {
	store := &fakeProjectMemoryV2Store{
		disableStageSplit: true,
		selection: memorystore.Selection{
			Stable: []memorystore.Record{{ID: "mem_pinned", Kind: "decision", Text: "stable", Pinned: true}},
			Recall: []memorystore.Record{{ID: "mem_recall", Kind: "workflow", Text: "query specific"}},
			IDs:    []string{"mem_pinned", "mem_recall"},
		},
	}
	manager := NewManager(DefaultBudget(), nil)
	manager.ProjectMemory = store
	result := manager.Build(context.Background(), BuildInput{
		TraceID: "turn-1", SessionID: "session-1", Goal: "query specific",
		History: []types.Message{
			*types.NewSystemMessage("system"),
			*types.NewUserMessage("implement memory"),
		},
		CountTokens: func(messages []types.Message) int { return len(messages) * 20 },
	})
	require.Equal(t, 1, store.calls)
	for _, message := range result.Messages {
		require.NotEqual(t, "project_memory_recall", message.Metadata.GetString("context_stage", ""))
	}
	var stable *types.Message
	for i := range result.Messages {
		if result.Messages[i].Metadata.GetString("context_stage", "") == "project_memory" {
			stable = &result.Messages[i]
		}
	}
	require.NotNil(t, stable)
	require.Contains(t, stable.Content, "query specific")
	require.NotEqual(t, true, result.Metadata["project_memory_recall_injected"])
}
