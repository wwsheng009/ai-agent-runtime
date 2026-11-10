package contextmgr

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

type fakeProjectMemoryV2Store struct {
	selection memorystore.Selection
	calls     int
}

func (f *fakeProjectMemoryV2Store) SelectForInject(memorystore.InjectOptions) ([]memorystore.Note, error) {
	return nil, nil
}

func (f *fakeProjectMemoryV2Store) SelectForInjectV2(memorystore.InjectOptions) (memorystore.Selection, error) {
	f.calls++
	return f.selection, nil
}

func (f *fakeProjectMemoryV2Store) V2InjectionEnabled() bool { return true }

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
