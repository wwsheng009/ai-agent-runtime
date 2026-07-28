package contextmgr

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/artifact"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/memory"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
	"github.com/wwsheng009/ai-agent-runtime/internal/workspace"
)

func TestResolveBudget_ProfileAndOverrides(t *testing.T) {
	compact := ResolveBudget(BudgetProfileCompact, Budget{})
	if compact.MaxPromptTokens != 8000 || compact.KeepRecentMessages != 5 {
		t.Fatalf("unexpected compact budget: %+v", compact)
	}

	extended := ResolveBudget(BudgetProfileExtended, Budget{
		MaxRecallResults:    9,
		MaxObservationItems: 10,
	})
	if extended.MaxPromptTokens != 20000 || extended.MaxMessages != 40 {
		t.Fatalf("unexpected extended base budget: %+v", extended)
	}
	if extended.MaxRecallResults != 9 || extended.MaxObservationItems != 10 {
		t.Fatalf("expected overrides to apply, got %+v", extended)
	}
}

func TestResolveBudget_AcceptsLayerAliases(t *testing.T) {
	hot := ResolveBudget(BudgetProfileHot, Budget{})
	compact := ResolveBudget(BudgetProfileCompact, Budget{})
	if hot != compact {
		t.Fatalf("expected hot alias to resolve to compact budget, got hot=%+v compact=%+v", hot, compact)
	}

	warm := ResolveBudget(BudgetProfileWarm, Budget{})
	balanced := ResolveBudget(BudgetProfileBalanced, Budget{})
	if warm != balanced {
		t.Fatalf("expected warm alias to resolve to balanced budget, got warm=%+v balanced=%+v", warm, balanced)
	}

	cold := ResolveBudget(BudgetProfileCold, Budget{})
	extended := ResolveBudget(BudgetProfileExtended, Budget{})
	if cold != extended {
		t.Fatalf("expected cold alias to resolve to extended budget, got cold=%+v extended=%+v", cold, extended)
	}
}

func TestManager_BuildCompactsAndRecalls(t *testing.T) {
	store, err := artifact.NewStore(nil)
	if err != nil {
		t.Fatalf("create artifact store: %v", err)
	}
	defer func() { _ = store.Close() }()

	artifactID, err := store.Put(context.Background(), artifact.Record{
		SessionID: "session-ctx",
		ToolName:  "run_command_readonly",
		Content:   "first line\nunique-stack-trace\nmore detail",
		Summary:   "stack trace summary",
	})
	if err != nil {
		t.Fatalf("store artifact: %v", err)
	}
	if artifactID == "" {
		t.Fatal("expected artifact id")
	}

	mem := memory.NewMemory(10)
	mem.Add(*types.NewObservation("step_1", "read_logs").WithOutput("parser failed at frame 12").MarkSuccess())

	history := []types.Message{
		*types.NewSystemMessage("system prompt"),
		*types.NewUserMessage("Investigate the failure"),
		*types.NewAssistantMessage("I will inspect the logs."),
		*types.NewToolMessage("call-1", "tool output A"),
		*types.NewAssistantMessage("I saw a stack trace in earlier output."),
		*types.NewUserMessage("Summarize the root cause"),
	}

	manager := NewManager(Budget{
		MaxPromptTokens:     300,
		MaxMessages:         12,
		KeepRecentMessages:  2,
		MaxRecallResults:    2,
		MaxObservationItems: 2,
	}, store)
	bus := runtimeevents.NewBus()
	var eventTypes []string
	var traceIDs []string
	bus.Subscribe("", func(event runtimeevents.Event) {
		eventTypes = append(eventTypes, event.Type)
		traceIDs = append(traceIDs, event.TraceID)
	})
	manager.Events = bus
	manager.Agent = "test-agent"

	result := manager.Build(context.Background(), BuildInput{
		TraceID:   "trace_ctx_1",
		SessionID: "session-ctx",
		Goal:      "Find the error stack trace",
		History:   history,
		Memory:    mem,
		// 6 raw * 80 = 480 > MaxPromptTokens 300; inject stages stay cheap under final trim.
		CountTokens:            testTokenCounterStageAware(80, 15),
		EnablePromptCompaction: true,
	})

	if len(result.Messages) == 0 {
		t.Fatal("expected managed messages")
	}

	var foundCompaction bool
	var foundLedger bool
	var foundRecall bool
	var foundWarmMemory bool
	for _, message := range result.Messages {
		if strings.Contains(message.Content, "Compacted context from earlier turns") {
			foundCompaction = true
		}
		if strings.Contains(message.Content, "Decision ledger:") {
			foundLedger = true
		}
		if strings.Contains(message.Content, "Relevant recalled artifacts:") {
			foundRecall = true
		}
		if strings.Contains(message.Content, "Recent observations:") {
			foundWarmMemory = true
		}
	}

	if !foundCompaction && !foundLedger {
		t.Fatal("expected compaction or ledger message to be injected")
	}
	if !foundRecall {
		t.Fatal("expected recall message to be injected")
	}
	if !foundWarmMemory {
		t.Fatal("expected warm memory message to be injected")
	}
	if got := result.Metadata["recall_injected"]; got != true {
		t.Fatalf("expected recall_injected metadata, got %v", got)
	}
	if got := result.Metadata["ledger_injected"]; got != true {
		t.Fatalf("expected ledger_injected metadata, got %v", got)
	}
	if !containsEvent(eventTypes, "context.compact.started") {
		t.Fatal("expected context.compact.started event")
	}
	if !containsEvent(eventTypes, "context.compact.completed") {
		t.Fatal("expected context.compact.completed event")
	}
	if !containsEvent(eventTypes, "recall.performed") {
		t.Fatal("expected recall.performed event")
	}
	for _, traceID := range traceIDs {
		if traceID != "trace_ctx_1" {
			t.Fatalf("expected trace_ctx_1, got %s", traceID)
		}
	}
	if checkpoint, err := store.LatestCheckpoint(context.Background(), "session-ctx"); err != nil || checkpoint == nil {
		t.Fatalf("expected persisted checkpoint, got checkpoint=%v err=%v", checkpoint, err)
	}
	entries, err := store.LoadMemoryEntries(context.Background(), "session-ctx", nil, 10)
	if err != nil {
		t.Fatalf("load memory entries: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("expected persisted memory entries after build")
	}
}

func TestManager_BuildPreservesFullHistoryByDefault(t *testing.T) {
	store, err := artifact.NewStore(nil)
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	manager := NewManager(Budget{
		MaxPromptTokens:     80,
		MaxMessages:         5,
		KeepRecentMessages:  2,
		MaxRecallResults:    0,
		MaxObservationItems: 0,
	}, store)
	manager.Strategy.RecallMode = RecallModeDisabled
	manager.Strategy.WorkspaceMode = WorkspaceModeDisabled

	bus := runtimeevents.NewBus()
	var eventTypes []string
	bus.Subscribe("", func(event runtimeevents.Event) {
		eventTypes = append(eventTypes, event.Type)
	})
	manager.Events = bus

	history := []types.Message{
		*types.NewSystemMessage("system prompt"),
		*types.NewUserMessage("first request"),
		*types.NewAssistantMessage("first answer"),
		*types.NewUserMessage("second request"),
		*types.NewAssistantMessage("second answer"),
		*types.NewUserMessage("latest request"),
	}

	result := manager.Build(context.Background(), BuildInput{
		TraceID:     "trace-default",
		SessionID:   "session-default",
		Goal:        "latest request",
		History:     history,
		CountTokens: func(messages []types.Message) int { return len(messages) * 100 },
	})

	require.Len(t, result.Messages, len(history))
	for index := range history {
		assert.Equal(t, history[index].Role, result.Messages[index].Role)
		assert.Equal(t, history[index].Content, result.Messages[index].Content)
	}
	_, compacted := result.Metadata["compacted_messages"]
	assert.False(t, compacted)
	assert.NotContains(t, eventTypes, "context.compact.started")
	assert.NotContains(t, eventTypes, "context.compact.completed")
	metrics := result.Metadata["context_layer_metrics"].(map[string]interface{})
	hot := metrics["hot"].(map[string]interface{})
	cold := metrics["cold"].(map[string]interface{})
	assert.Equal(t, false, cold["compact_enabled"])
	assert.Equal(t, 0, hot["trimmed_messages"])
}

func TestManager_BuildRecallsFallbackQueryWhenInitialSearchErrors(t *testing.T) {
	store, err := artifact.NewStore(nil)
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	_, err = store.Put(context.Background(), artifact.Record{
		SessionID: "session-recall-fallback",
		ToolName:  "read_logs",
		Content:   "header\nunique-stack-trace\nframe 1\nframe 2",
		Summary:   "stack trace summary",
	})
	require.NoError(t, err)

	manager := NewManager(Budget{
		MaxPromptTokens:     8000,
		MaxMessages:         12,
		KeepRecentMessages:  6,
		MaxRecallResults:    2,
		MaxObservationItems: 2,
	}, store)
	manager.Strategy.WorkspaceMode = WorkspaceModeDisabled

	result := manager.Build(context.Background(), BuildInput{
		SessionID: "session-recall-fallback",
		Goal:      "Find the error stack trace.",
		History: []types.Message{
			*types.NewSystemMessage("system prompt"),
			*types.NewUserMessage("Find the error stack trace."),
			*types.NewAssistantMessage("I will inspect the logs."),
			*types.NewToolMessage("call-1", "reduced log output"),
		},
		CountTokens: func(messages []types.Message) int { return len(messages) * 20 },
	})

	var foundRecall bool
	for _, message := range result.Messages {
		if strings.Contains(message.Content, "Relevant recalled artifacts:") &&
			strings.Contains(message.Content, "unique-stack-trace") {
			foundRecall = true
			break
		}
	}
	require.True(t, foundRecall, "expected fallback recall query to inject artifact preview")
	require.Equal(t, true, result.Metadata["recall_injected"])
}

func TestManager_Build_CompactProfilePrefersSummaryAndSkipsRecall(t *testing.T) {
	store, err := artifact.NewStore(nil)
	if err != nil {
		t.Fatalf("create artifact store: %v", err)
	}
	defer func() { _ = store.Close() }()

	_, err = store.Put(context.Background(), artifact.Record{
		SessionID: "session-compact",
		ToolName:  "read_logs",
		Content:   "first line\nunique-stack-trace\nmore detail",
		Summary:   "stack trace summary",
	})
	if err != nil {
		t.Fatalf("store artifact: %v", err)
	}

	manager := NewManagerWithProfile(BudgetProfileCompact, Budget{
		MaxPromptTokens:     400,
		MaxMessages:         6,
		KeepRecentMessages:  2,
		MaxRecallResults:    2,
		MaxObservationItems: 3,
	}, store)

	observations := []types.Observation{
		*types.NewObservation("step_1", "read_logs").WithOutput("ok result").MarkSuccess(),
		*types.NewObservation("step_2", "run_tests").MarkFailure("failed assertion"),
	}
	history := []types.Message{
		*types.NewSystemMessage("system prompt"),
		*types.NewUserMessage("Investigate the failure"),
		*types.NewAssistantMessage("I will inspect the logs."),
		*types.NewToolMessage("call-1", "tool output A"),
		*types.NewAssistantMessage("I saw a stack trace in earlier output."),
		*types.NewUserMessage("Summarize the root cause"),
	}

	result := manager.Build(context.Background(), BuildInput{
		TraceID:      "trace_ctx_compact",
		SessionID:    "session-compact",
		Goal:         "Find the error stack trace",
		History:      history,
		Observations: observations,
		// 6 msgs * 80 = 480 > MaxPromptTokens 400; after keepRecent estimate drops under budget.
		CountTokens:            func(messages []types.Message) int { return len(messages) * 80 },
		EnablePromptCompaction: true,
	})

	var foundSummary bool
	var foundLedger bool
	var foundRecall bool
	var warmMemoryContent string
	for _, message := range result.Messages {
		if strings.Contains(message.Content, "Compacted context from earlier turns") {
			foundSummary = true
		}
		if strings.Contains(message.Content, "Decision ledger:") {
			foundLedger = true
		}
		if strings.Contains(message.Content, "Relevant recalled artifacts:") {
			foundRecall = true
		}
		if strings.Contains(message.Content, "Recent observations:") {
			warmMemoryContent = message.Content
		}
	}

	if !foundSummary {
		t.Fatal("expected compact profile to use summary compaction")
	}
	if foundLedger {
		t.Fatal("did not expect ledger injection under compact profile")
	}
	if foundRecall {
		t.Fatal("did not expect recall injection under compact profile")
	}
	if !strings.Contains(warmMemoryContent, "failed assertion") {
		t.Fatalf("expected failure observation in warm memory, got %q", warmMemoryContent)
	}
	if strings.Contains(warmMemoryContent, "ok result") {
		t.Fatalf("did not expect successful observation in compact profile warm memory, got %q", warmMemoryContent)
	}
}

func TestManager_BuildAppendsCompactionSegmentsInsteadOfRewritingPrefix(t *testing.T) {
	store, err := artifact.NewStore(nil)
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	manager := NewManagerWithProfile(BudgetProfileCompact, Budget{
		MaxPromptTokens:     4000,
		MaxMessages:         8,
		KeepRecentMessages:  2,
		MaxRecallResults:    2,
		MaxObservationItems: 2,
	}, store)

	baseHistory := []types.Message{
		*types.NewSystemMessage("system prompt"),
		*types.NewUserMessage("Investigate the failure"),
		*types.NewAssistantMessage("I will inspect the logs."),
		*types.NewToolMessage("call-1", "tool output A"),
		*types.NewAssistantMessage("I saw a stack trace in earlier output."),
		*types.NewUserMessage("Summarize the root cause"),
	}

	first := manager.Build(context.Background(), BuildInput{
		SessionID:              "session-compaction-segments",
		TaskID:                 "task-compaction-segments",
		Goal:                   "Summarize the root cause",
		History:                baseHistory,
		EnablePromptCompaction: true,
	})

	firstCompactions := compactionMessagesFromResult(first.Messages)
	require.Len(t, firstCompactions, 1)
	firstContent := firstCompactions[0].Content

	extendedHistory := append([]types.Message{}, baseHistory[:len(baseHistory)-1]...)
	extendedHistory = append(extendedHistory,
		*types.NewAssistantMessage("I confirmed the fallback path hits the same stack trace."),
		*types.NewUserMessage("Summarize the updated root cause"),
	)

	second := manager.Build(context.Background(), BuildInput{
		SessionID:              "session-compaction-segments",
		TaskID:                 "task-compaction-segments",
		Goal:                   "Summarize the updated root cause",
		History:                extendedHistory,
		EnablePromptCompaction: true,
	})

	secondCompactions := compactionMessagesFromResult(second.Messages)
	require.Len(t, secondCompactions, 2)
	assert.Equal(t, firstContent, secondCompactions[0].Content)
	assert.Contains(t, secondCompactions[1].Content, "Compacted context from earlier turns (continued):")

	checkpoints, err := store.ListCheckpoints(context.Background(), "session-compaction-segments", 10, 0)
	require.NoError(t, err)
	require.Len(t, checkpoints, 2)
}

func TestBuildObservationMessage_StableMapSerialization(t *testing.T) {
	mapA := map[string]interface{}{
		"weather": map[string]interface{}{
			"text": "sunny",
			"temp": 22,
		},
		"city": "beijing",
	}
	mapB := map[string]interface{}{
		"city": "beijing",
		"weather": map[string]interface{}{
			"temp": 22,
			"text": "sunny",
		},
	}

	msgA := buildObservationMessage([]types.Observation{
		{
			Tool:    "weather_lookup",
			Success: true,
			Output:  mapA,
		},
	}, 4)
	msgB := buildObservationMessage([]types.Observation{
		{
			Tool:    "weather_lookup",
			Success: true,
			Output:  mapB,
		},
	}, 4)

	require.NotNil(t, msgA)
	require.NotNil(t, msgB)
	assert.Equal(t, msgA.Content, msgB.Content)
	assert.Contains(t, msgA.Content, `{"city":"beijing","weather":{"temp":22,"text":"sunny"}}`)
}

func TestManager_Build_ExtendedProfileUsesBroadRecall(t *testing.T) {
	store, err := artifact.NewStore(nil)
	if err != nil {
		t.Fatalf("create artifact store: %v", err)
	}
	defer func() { _ = store.Close() }()

	_, err = store.Put(context.Background(), artifact.Record{
		SessionID: "session-extended",
		ToolName:  "read_notes",
		Content:   "root cause evidence appears in archived notes",
		Summary:   "archived evidence",
	})
	if err != nil {
		t.Fatalf("store artifact: %v", err)
	}

	manager := NewManagerWithProfile(BudgetProfileExtended, Budget{
		MaxPromptTokens:     8000,
		MaxMessages:         8,
		KeepRecentMessages:  2,
		MaxRecallResults:    2,
		MaxObservationItems: 2,
	}, store)

	history := []types.Message{
		*types.NewSystemMessage("system prompt"),
		*types.NewUserMessage("Review prior investigation notes"),
		*types.NewAssistantMessage("I will review archived notes."),
		*types.NewToolMessage("call-1", "notes loaded"),
		*types.NewUserMessage("Summarize the root cause"),
	}

	result := manager.Build(context.Background(), BuildInput{
		TraceID:     "trace_ctx_extended",
		SessionID:   "session-extended",
		Goal:        "Summarize the root cause",
		History:     history,
		CountTokens: func(messages []types.Message) int { return len(messages) * 20 },
	})

	var foundRecall bool
	for _, message := range result.Messages {
		if strings.Contains(message.Content, "Relevant recalled artifacts:") {
			foundRecall = true
			break
		}
	}
	if !foundRecall {
		t.Fatal("expected extended profile to use broad recall")
	}
}

func TestManager_Build_DoesNotSplitToolCallHistoryAtRecentBoundary(t *testing.T) {
	manager := NewManagerWithProfile(BudgetProfileCompact, Budget{
		MaxPromptTokens:    4000,
		MaxMessages:        16,
		KeepRecentMessages: 8,
	}, nil)

	history := []types.Message{
		*types.NewUserMessage("查看当前目录的文档"),
		{
			Role: "assistant",
			ToolCalls: []types.ToolCall{
				{
					ID:   "call_ls_1",
					Name: "ls",
					Args: map[string]interface{}{"path": ".", "depth": 2},
				},
			},
			Metadata: types.NewMetadata(),
		},
		*types.NewToolMessage("call_ls_1", "目录: ."),
		{
			Role: "assistant",
			ToolCalls: []types.ToolCall{
				{ID: "call_glob_1", Name: "glob", Args: map[string]interface{}{"pattern": "**/*.md"}},
				{ID: "call_glob_2", Name: "glob", Args: map[string]interface{}{"pattern": "**/*.txt"}},
				{ID: "call_glob_3", Name: "glob", Args: map[string]interface{}{"pattern": "**/README*"}},
				{ID: "call_glob_4", Name: "glob", Args: map[string]interface{}{"pattern": "**/*.rst"}},
			},
			Metadata: types.NewMetadata(),
		},
		*types.NewToolMessage("call_glob_1", "docsArchive/a.md"),
		*types.NewToolMessage("call_glob_2", "data/test.txt"),
		*types.NewToolMessage("call_glob_3", "tests/README.md"),
		*types.NewToolMessage("call_glob_4", "未找到匹配的文件"),
		*types.NewAssistantMessage("当前目录下可见的文档主要有这些。"),
		*types.NewUserMessage("你好，创建两个团队成员，分别探索docs目录文件并汇报进度"),
	}

	result := manager.Build(context.Background(), BuildInput{
		SessionID: "session-tool-boundary",
		Goal:      "你好，创建两个团队成员，分别探索docs目录文件并汇报进度",
		History:   history,
		// 10 msgs * 401 > MaxPromptTokens 4000; after keepRecent (~9) estimate falls under budget
		// so final trim does not drop the protected tool-call prefix.
		CountTokens:            func(messages []types.Message) int { return len(messages) * 401 },
		EnablePromptCompaction: true,
	})

	require.NotEmpty(t, result.Messages)
	require.Equal(t, "assistant", result.Messages[0].Role)
	require.Len(t, result.Messages[0].ToolCalls, 1)
	assert.Equal(t, "call_ls_1", result.Messages[0].ToolCalls[0].ID)
	require.Len(t, result.Messages, 9)
	assert.Equal(t, "tool", result.Messages[1].Role)
	assert.Equal(t, "call_ls_1", result.Messages[1].ToolCallID)
}

func TestRecentWindowStart_ProtectsActiveUserTurnFromCompaction(t *testing.T) {
	messages := []types.Message{
		*types.NewUserMessage("previous request"),
		*types.NewAssistantMessage("previous answer"),
		*types.NewUserMessage("current request"),
		{
			Role: "assistant",
			ToolCalls: []types.ToolCall{
				{
					ID:   "call_view_1",
					Name: "view",
					Args: map[string]interface{}{"file_path": "README.md"},
				},
			},
			Metadata: types.NewMetadata(),
		},
		*types.NewToolMessage("call_view_1", "README preview"),
		*types.NewAssistantMessage("继续分析中"),
	}

	start := recentWindowStart(messages, 2)
	assert.Equal(t, 2, start)
}

func TestTrimFlexibleMessageCount_PreservesActiveUserTurnSuffix(t *testing.T) {
	rawMessages := []types.Message{
		*types.NewAssistantMessage("previous answer"),
		*types.NewUserMessage("current request"),
		{
			Role: "assistant",
			ToolCalls: []types.ToolCall{
				{
					ID:   "call_view_1",
					Name: "view",
					Args: map[string]interface{}{"file_path": "README.md"},
				},
			},
			Metadata: types.NewMetadata(),
		},
		*types.NewToolMessage("call_view_1", "README preview"),
	}

	trimmedRaw, trimmedDynamic := trimFlexibleMessageCount(rawMessages, []types.Message{
		{
			Role:    "assistant",
			Content: "Workspace recall",
			Metadata: types.Metadata{
				"context_stage": "workspace",
			},
		},
	}, 2)

	require.Len(t, trimmedRaw, 3)
	assert.Equal(t, "user", trimmedRaw[0].Role)
	assert.Equal(t, "current request", trimmedRaw[0].Content)
	assert.Equal(t, "assistant", trimmedRaw[1].Role)
	assert.Equal(t, "tool", trimmedRaw[2].Role)
	assert.Empty(t, trimmedDynamic)
}

func TestActiveUserTurnHasReplay(t *testing.T) {
	assert.False(t, activeUserTurnHasReplay([]types.Message{
		*types.NewUserMessage("current request"),
	}))

	assert.True(t, activeUserTurnHasReplay([]types.Message{
		*types.NewUserMessage("current request"),
		{
			Role: "assistant",
			ToolCalls: []types.ToolCall{
				{
					ID:   "call_view_1",
					Name: "view",
					Args: map[string]interface{}{"file_path": "README.md"},
				},
			},
			Metadata: types.NewMetadata(),
		},
		*types.NewToolMessage("call_view_1", "README preview"),
	}))
}

func TestManager_Build_DoesNotCompactActiveUserTurn(t *testing.T) {
	store, err := artifact.NewStore(nil)
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	manager := NewManager(Budget{
		MaxPromptTokens:     8000,
		MaxMessages:         16,
		KeepRecentMessages:  2,
		MaxRecallResults:    2,
		MaxObservationItems: 2,
	}, store)
	manager.Strategy.WorkspaceMode = WorkspaceModeDisabled
	manager.Strategy.RecallMode = RecallModeDisabled

	history := []types.Message{
		*types.NewSystemMessage("system prompt"),
		*types.NewUserMessage("继续分析当前实现"),
		{
			Role: "assistant",
			ToolCalls: []types.ToolCall{
				{
					ID:   "call_readme_1",
					Name: "view",
					Args: map[string]interface{}{"file_path": "README.md"},
				},
			},
			Metadata: types.NewMetadata(),
		},
		*types.NewToolMessage("call_readme_1", "README preview"),
		{
			Role: "assistant",
			ToolCalls: []types.ToolCall{
				{
					ID:   "call_agents_1",
					Name: "view",
					Args: map[string]interface{}{"file_path": "AGENTS.md"},
				},
			},
			Metadata: types.NewMetadata(),
		},
		*types.NewToolMessage("call_agents_1", "AGENTS preview"),
	}

	result := manager.Build(context.Background(), BuildInput{
		SessionID:              "session-active-turn",
		TaskID:                 "task-active-turn",
		Goal:                   "继续分析当前实现",
		History:                history,
		CountTokens:            func(messages []types.Message) int { return len(messages) * 20 },
		EnablePromptCompaction: true,
	})

	for _, message := range result.Messages {
		stage := message.Metadata.GetString("context_stage", "")
		if stage == "ledger" || stage == "compaction" {
			t.Fatalf("did not expect active user turn to be compacted, got stage=%s messages=%+v", stage, result.Messages)
		}
	}
	require.Len(t, result.Messages, len(history))
	assert.Equal(t, "user", result.Messages[1].Role)
	assert.Equal(t, "继续分析当前实现", result.Messages[1].Content)
}

func TestManager_Build_SuppressesVolatileContextDuringActiveUserTurnReplay(t *testing.T) {
	manager := NewManager(DefaultBudget(), nil)
	manager.Strategy.WorkspaceMode = WorkspaceModeSignals
	manager.Strategy.MinWorkspaceQueryLength = 4
	manager.Workspace = stubWorkspaceBuilder{
		ctx: &workspace.WorkspaceContext{
			Summary: "workspace summary",
			Files:   []string{"README.md"},
			Symbols: []workspace.SymbolInfo{{Name: "SearchDocs"}},
		},
	}

	history := []types.Message{
		*types.NewSystemMessage("system prompt"),
		*types.NewUserMessage("search docs"),
		{
			Role: "assistant",
			ToolCalls: []types.ToolCall{
				{
					ID:   "call_view_1",
					Name: "view",
					Args: map[string]interface{}{"file_path": "README.md"},
				},
			},
			Metadata: types.NewMetadata(),
		},
		*types.NewToolMessage("call_view_1", "README preview"),
	}

	result := manager.Build(context.Background(), BuildInput{
		SessionID: "session-active-turn-volatile",
		Goal:      "search docs",
		History:   history,
		Observations: []types.Observation{
			*types.NewObservation("step_1", "view").WithOutput("README preview").MarkSuccess(),
		},
	})

	for _, message := range result.Messages {
		if strings.Contains(message.Content, "Recent observations:") {
			t.Fatalf("did not expect warm memory during active turn replay, got %+v", result.Messages)
		}
		if strings.Contains(message.Content, "Workspace recall:") {
			t.Fatalf("did not expect workspace recall during active turn replay, got %+v", result.Messages)
		}
	}
	if got := result.Metadata["observation_injected"]; got != nil {
		t.Fatalf("expected observation_injected to stay unset, got %v", got)
	}
	if got := result.Metadata["workspace_context_injected"]; got != nil {
		t.Fatalf("expected workspace_context_injected to stay unset, got %v", got)
	}

	layerMetrics := result.Metadata["context_layer_metrics"].(map[string]interface{})
	warm := layerMetrics["warm"].(map[string]interface{})
	workspaceMetrics := layerMetrics["workspace"].(map[string]interface{})
	assert.Equal(t, true, warm["suppressed_for_active_turn"])
	assert.Equal(t, true, workspaceMetrics["suppressed_for_active_turn"])
}

type stubWorkspaceBuilder struct {
	ctx *workspace.WorkspaceContext
}

func (s stubWorkspaceBuilder) Build(query string) *workspace.WorkspaceContext {
	return s.ctx
}

func TestManager_Build_IncludesWorkspaceRecall(t *testing.T) {
	manager := NewManager(DefaultBudget(), nil)
	manager.Strategy.WorkspaceMode = WorkspaceModeSignals
	manager.Strategy.MinWorkspaceQueryLength = 4
	manager.Workspace = stubWorkspaceBuilder{
		ctx: &workspace.WorkspaceContext{
			Summary: "workspace summary",
			Files:   []string{"main.go"},
			Symbols: []workspace.SymbolInfo{{Name: "SearchDocs"}},
			Chunks: []workspace.CodeChunk{
				{
					FilePath:  "main.go",
					StartLine: 1,
					EndLine:   2,
					Content:   "func SearchDocs() {}",
				},
			},
		},
	}

	history := []types.Message{
		*types.NewSystemMessage("system prompt"),
		*types.NewUserMessage("search docs"),
	}

	result := manager.Build(context.Background(), BuildInput{
		SessionID: "session-workspace",
		Goal:      "search docs",
		History:   history,
	})

	var foundWorkspace bool
	for _, message := range result.Messages {
		if strings.Contains(message.Content, "Workspace recall:") {
			foundWorkspace = true
			break
		}
	}
	if !foundWorkspace {
		t.Fatal("expected workspace recall message to be injected")
	}
	if got := result.Metadata["workspace_context_injected"]; got != true {
		t.Fatalf("expected workspace_context_injected metadata, got %v", got)
	}
	if got := result.Metadata["workspace_summary"]; got != "workspace summary" {
		t.Fatalf("expected workspace_summary metadata, got %v", got)
	}
}

func TestManager_Build_IncludesProjectMemory(t *testing.T) {
	root := t.TempDir()
	store, err := memorystore.New(memorystore.Config{Root: root})
	require.NoError(t, err)

	_, err = store.Append(memorystore.AppendNoteOptions{
		Text:   "Prefer worktree isolation for parallel agents",
		Tags:   []string{"isolation", "worktree"},
		Source: "manual",
	})
	require.NoError(t, err)

	manager := NewManager(DefaultBudget(), nil)
	manager.ProjectMemory = store

	result := manager.Build(context.Background(), BuildInput{
		SessionID: "session-project-memory",
		Goal:      "worktree isolation parallel agents",
		History: []types.Message{
			*types.NewSystemMessage("system prompt"),
			*types.NewUserMessage("worktree isolation parallel agents"),
		},
	})

	var found bool
	for _, message := range result.Messages {
		if message.Metadata.GetString("context_stage", "") == "project_memory" {
			found = true
			if !strings.Contains(message.Content, "Project durable memory") {
				t.Fatalf("expected project memory header, got %q", message.Content)
			}
			if !strings.Contains(message.Content, "worktree isolation") {
				t.Fatalf("expected note body in project memory, got %q", message.Content)
			}
			break
		}
	}
	if !found {
		t.Fatal("expected project memory message to be injected")
	}
	if got := result.Metadata["project_memory_injected"]; got != true {
		t.Fatalf("expected project_memory_injected metadata, got %v", got)
	}
	if got := result.Metadata["project_memory_count"]; got != 1 {
		t.Fatalf("expected project_memory_count=1, got %v", got)
	}
}

func TestManager_Build_WorkspaceSignalsSkipGenericGreeting(t *testing.T) {
	manager := NewManager(DefaultBudget(), nil)
	manager.Strategy.WorkspaceMode = WorkspaceModeSignals
	manager.Strategy.MinWorkspaceQueryLength = 4
	manager.Workspace = stubWorkspaceBuilder{
		ctx: &workspace.WorkspaceContext{
			Summary: "workspace summary",
			Files:   []string{"main.go"},
		},
	}

	result := manager.Build(context.Background(), BuildInput{
		SessionID: "session-workspace-greeting",
		Goal:      "hello",
		History: []types.Message{
			*types.NewSystemMessage("system prompt"),
			*types.NewUserMessage("hello"),
		},
	})

	for _, message := range result.Messages {
		if strings.Contains(message.Content, "Workspace recall:") {
			t.Fatalf("did not expect workspace recall for generic greeting, got %+v", result.Messages)
		}
	}
	if got := result.Metadata["workspace_context_injected"]; got != nil {
		t.Fatalf("expected workspace_context_injected to be unset, got %v", got)
	}
}

func TestManager_BuildReusesCheckpointWithoutDuplicatingLedger(t *testing.T) {
	store, err := artifact.NewStore(nil)
	if err != nil {
		t.Fatalf("create artifact store: %v", err)
	}
	defer func() { _ = store.Close() }()

	history := []types.Message{
		*types.NewSystemMessage("system prompt"),
		*types.NewUserMessage("Investigate the failure"),
		*types.NewAssistantMessage("Decision: inspect the first failing test."),
		*types.NewToolMessage("call-1", "panic: parser failed"),
		*types.NewUserMessage("Summarize the root cause"),
	}

	manager := NewManager(Budget{
		MaxPromptTokens:     8000,
		MaxMessages:         6,
		KeepRecentMessages:  2,
		MaxRecallResults:    2,
		MaxObservationItems: 2,
	}, store)
	input := BuildInput{
		SessionID:              "session-ledger",
		TaskID:                 "task-ledger",
		Goal:                   "Find the error stack trace",
		History:                history,
		EnablePromptCompaction: true,
	}

	first := manager.Build(context.Background(), input)
	second := manager.Build(context.Background(), input)
	if len(first.Messages) == 0 || len(second.Messages) == 0 {
		t.Fatal("expected non-empty managed messages")
	}

	entries, err := store.LoadMemoryEntries(context.Background(), "session-ledger", nil, 20)
	if err != nil {
		t.Fatalf("load memory entries: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("expected memory entries to exist")
	}

	checkpoint, err := store.LatestCheckpoint(context.Background(), "session-ledger")
	if err != nil {
		t.Fatalf("load latest checkpoint: %v", err)
	}
	if checkpoint == nil {
		t.Fatal("expected checkpoint")
	}
	if first.Metadata["checkpoint_id"] != second.Metadata["checkpoint_id"] {
		t.Fatalf("expected checkpoint reuse, got first=%v second=%v", first.Metadata["checkpoint_id"], second.Metadata["checkpoint_id"])
	}
}

func TestManager_BuildAppendsLedgerSegmentsInsteadOfRewritingPrefix(t *testing.T) {
	store, err := artifact.NewStore(nil)
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	manager := NewManager(Budget{
		MaxPromptTokens:     8000,
		MaxMessages:         8,
		KeepRecentMessages:  2,
		MaxRecallResults:    2,
		MaxObservationItems: 2,
	}, store)

	baseHistory := []types.Message{
		*types.NewSystemMessage("system prompt"),
		*types.NewUserMessage("Investigate the failure"),
		*types.NewAssistantMessage("Decision: inspect the first failing test."),
		*types.NewToolMessage("call-1", "panic: parser failed"),
		*types.NewAssistantMessage("Conclusion: the parser panic starts in the config loader."),
		*types.NewUserMessage("Summarize the root cause"),
	}

	first := manager.Build(context.Background(), BuildInput{
		SessionID:              "session-ledger-segments",
		TaskID:                 "task-ledger-segments",
		Goal:                   "Summarize the root cause",
		History:                baseHistory,
		EnablePromptCompaction: true,
	})

	firstLedgers := ledgerMessagesFromResult(first.Messages)
	require.Len(t, firstLedgers, 1)
	firstContent := firstLedgers[0].Content

	extendedHistory := append([]types.Message{}, baseHistory[:len(baseHistory)-1]...)
	extendedHistory = append(extendedHistory,
		*types.NewAssistantMessage("Decision: confirm whether the same loader panic affects the fallback path."),
		*types.NewUserMessage("Summarize the updated root cause"),
	)

	second := manager.Build(context.Background(), BuildInput{
		SessionID:              "session-ledger-segments",
		TaskID:                 "task-ledger-segments",
		Goal:                   "Summarize the updated root cause",
		History:                extendedHistory,
		EnablePromptCompaction: true,
	})

	secondLedgers := ledgerMessagesFromResult(second.Messages)
	require.Len(t, secondLedgers, 2)
	assert.Equal(t, firstContent, secondLedgers[0].Content)
	assert.Contains(t, secondLedgers[1].Content, "Decision ledger (continued):")
	assert.NotEqual(t, secondLedgers[0].Metadata["checkpoint_id"], secondLedgers[1].Metadata["checkpoint_id"])

	checkpoints, err := store.ListCheckpoints(context.Background(), "session-ledger-segments", 10, 0)
	require.NoError(t, err)
	require.Len(t, checkpoints, 2)
}

func TestManager_Build_InjectsProfileContextLayer(t *testing.T) {
	manager := NewManagerWithProfile(BudgetProfileBalanced, Budget{
		MaxPromptTokens:     1200,
		MaxMessages:         8,
		KeepRecentMessages:  4,
		MaxRecallResults:    2,
		MaxObservationItems: 3,
	}, nil)

	result := manager.Build(context.Background(), BuildInput{
		SessionID: "session-profile",
		Goal:      "Review the current state",
		History: []types.Message{
			*types.NewSystemMessage("Base system prompt"),
			*types.NewUserMessage("Review the current state"),
		},
		Profile: map[string]interface{}{
			"name":  "dev",
			"agent": "tester",
			"resources": map[string]interface{}{
				"memory": map[string]interface{}{
					"content": `{"summary":"cached profile memory"}`,
				},
				"notes": map[string]interface{}{
					"content": "Profile investigation notes.",
				},
			},
		},
		CountTokens: func(messages []types.Message) int { return len(messages) * 20 },
	})

	require.NotEmpty(t, result.Messages)
	found := false
	for _, message := range result.Messages {
		if message.Role == "assistant" &&
			message.Metadata.GetString("context_stage", "") == "profile" &&
			strings.Contains(message.Content, "Profile context:") &&
			strings.Contains(message.Content, "cached profile memory") &&
			strings.Contains(message.Content, "Profile investigation notes.") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected profile context message to be injected")
	}
	if got := result.Metadata["profile_context_injected"]; got != true {
		t.Fatalf("expected profile_context_injected metadata, got %v", got)
	}
	layers, ok := result.Metadata["context_layers"].(LayerPlan)
	require.True(t, ok)
	if layers.ProfileContext.Name != "profile" {
		t.Fatalf("expected profile layer spec, got %+v", layers.ProfileContext)
	}
	metrics, ok := result.Metadata["context_layer_metrics"].(map[string]interface{})
	require.True(t, ok)
	profileMetrics, ok := metrics["profile"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, true, profileMetrics["injected"])
	assert.Equal(t, 2, profileMetrics["resource_count"])
}

func TestManager_Build_PersistsProfileSourceRefsIntoLedger(t *testing.T) {
	store, err := artifact.NewStore(nil)
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	manager := NewManager(Budget{
		MaxPromptTokens:     2000,
		MaxMessages:         6,
		KeepRecentMessages:  2,
		MaxRecallResults:    2,
		MaxObservationItems: 2,
	}, store)

	result := manager.Build(context.Background(), BuildInput{
		SessionID:              "session-profile-ledger",
		TaskID:                 "task-profile-ledger",
		Goal:                   "Review the failure history",
		EnablePromptCompaction: true,
		History: []types.Message{
			*types.NewSystemMessage("system prompt"),
			*types.NewUserMessage("Investigate the profile guidance"),
			*types.NewAssistantMessage("Decision: use the profile memory snapshot first."),
			*types.NewAssistantMessage("The prior notes mention a failing path."),
			*types.NewUserMessage("Summarize the root cause"),
		},
		Profile: map[string]interface{}{
			"name":        "dev",
			"memory_path": "E:/profiles/dev/agents/tester/memory/memory.json",
			"notes_path":  "E:/profiles/dev/agents/tester/context/notes.md",
			"resources": map[string]interface{}{
				"memory": map[string]interface{}{"content": `{"summary":"cached profile memory"}`},
				"notes":  map[string]interface{}{"content": "Profile investigation notes."},
			},
		},
		// 5 msgs * 450 > MaxPromptTokens 2000.
		CountTokens: func(messages []types.Message) int { return len(messages) * 450 },
	})

	entries, err := store.LoadMemoryEntries(context.Background(), "session-profile-ledger", nil, 20)
	require.NoError(t, err)
	require.NotEmpty(t, entries)

	foundEntryRef := false
	for _, entry := range entries {
		if containsSourceRef(entry.SourceRefs, "profile-resource:memory:") &&
			containsSourceRef(entry.SourceRefs, "profile-resource:notes:") {
			foundEntryRef = true
			break
		}
	}
	assert.True(t, foundEntryRef, "expected profile source refs in persisted memory entries")

	foundLedgerMessage := false
	for _, message := range result.Messages {
		if message.Metadata.GetString("context_stage", "") != "ledger" {
			continue
		}
		refs := extractArtifactRefs(message.Metadata)
		if containsSourceRef(refs, "profile-resource:memory:") &&
			containsSourceRef(refs, "profile-resource:notes:") &&
			strings.Contains(message.Content, "source=profile_memory") &&
			strings.Contains(message.Content, "source=profile_notes") {
			foundLedgerMessage = true
			break
		}
	}
	assert.True(t, foundLedgerMessage, "expected ledger message to expose profile provenance")
}

func TestManager_Build_RecallMessageExposesArtifactProvenance(t *testing.T) {
	store, err := artifact.NewStore(nil)
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	_, err = store.Put(context.Background(), artifact.Record{
		SessionID: "session-profile-recall",
		ToolName:  "read_notes",
		Summary:   "profile recall summary",
		Content:   "profile notes mention a failing integration path",
		Metadata: map[string]interface{}{
			"source_refs": []string{
				"profile-resource:memory:E:/profiles/dev/agents/tester/memory/memory.json",
				"profile-resource:notes:E:/profiles/dev/agents/tester/context/notes.md",
			},
			"profile": "dev",
		},
	})
	require.NoError(t, err)

	manager := NewManagerWithProfile(BudgetProfileExtended, Budget{
		MaxPromptTokens:     2000,
		MaxMessages:         8,
		KeepRecentMessages:  4,
		MaxRecallResults:    3,
		MaxObservationItems: 2,
	}, store)

	result := manager.Build(context.Background(), BuildInput{
		SessionID: "session-profile-recall",
		Goal:      "Find the integration path from profile notes",
		History: []types.Message{
			*types.NewSystemMessage("system prompt"),
			*types.NewUserMessage("Find the integration path from profile notes"),
		},
		CountTokens: func(messages []types.Message) int { return len(messages) * 20 },
	})

	found := false
	for _, message := range result.Messages {
		if message.Metadata.GetString("context_stage", "") != "recall" {
			continue
		}
		refs := extractArtifactRefs(message.Metadata)
		if containsSourceRef(refs, "profile-resource:memory:") &&
			containsSourceRef(refs, "profile-resource:notes:") &&
			strings.Contains(message.Content, "source=profile_memory") &&
			strings.Contains(message.Content, "source=profile_notes") {
			recallArtifacts, ok := message.Metadata["recall_artifacts"].([]map[string]interface{})
			if !ok || len(recallArtifacts) == 0 {
				t.Fatalf("expected recall_artifacts metadata, got %#v", message.Metadata["recall_artifacts"])
			}
			found = true
			break
		}
	}
	assert.True(t, found, "expected recall message to expose artifact provenance")
}

func TestManager_Build_LongSessionLayerMetricsDifferAcrossProfiles(t *testing.T) {
	store, err := artifact.NewStore(nil)
	if err != nil {
		t.Fatalf("create artifact store: %v", err)
	}
	defer func() { _ = store.Close() }()

	_, err = store.Put(context.Background(), artifact.Record{
		SessionID: "session-long",
		ToolName:  "read_logs",
		Content:   "first line\nunique-stack-trace\nmore detail",
		Summary:   "stack trace summary",
	})
	if err != nil {
		t.Fatalf("store artifact: %v", err)
	}

	history := []types.Message{*types.NewSystemMessage("system prompt")}
	for index := 0; index < 8; index++ {
		history = append(history,
			*types.NewUserMessage("Investigate failure wave " + string(rune('A'+index))),
			*types.NewAssistantMessage("Decision: inspect failing area and keep evidence."),
			*types.NewToolMessage("call-"+string(rune('a'+index)), "tool output with artifact refs and stack trace"),
		)
	}
	history = append(history, *types.NewUserMessage("Summarize the root cause from archived evidence"))

	observations := []types.Observation{
		*types.NewObservation("step_1", "read_logs").WithOutput("ok result").MarkSuccess(),
		*types.NewObservation("step_2", "run_tests").MarkFailure("failed assertion"),
		*types.NewObservation("step_3", "git_log").WithOutput("recent revert noted").MarkSuccess(),
	}

	compactManager := NewManagerWithProfile(BudgetProfileHot, ResolveBudget(BudgetProfileHot, Budget{}), store)
	extendedManager := NewManagerWithProfile(BudgetProfileCold, ResolveBudget(BudgetProfileCold, Budget{}), store)

	input := BuildInput{
		TraceID:      "trace_ctx_profiles",
		SessionID:    "session-long",
		TaskID:       "task-long",
		Goal:         "Find the error stack trace from archived evidence",
		History:      history,
		Observations: observations,
		// 26 msgs * 900 exceeds compact(8000) and extended(20000); after keepRecent estimate drops under.
		CountTokens:            func(messages []types.Message) int { return len(messages) * 900 },
		EnablePromptCompaction: true,
	}

	compactResult := compactManager.Build(context.Background(), input)
	extendedResult := extendedManager.Build(context.Background(), input)

	compactLayers, ok := compactResult.Metadata["context_layers"].(LayerPlan)
	if !ok {
		t.Fatalf("expected compact context_layers to be LayerPlan, got %T", compactResult.Metadata["context_layers"])
	}
	extendedLayers, ok := extendedResult.Metadata["context_layers"].(LayerPlan)
	if !ok {
		t.Fatalf("expected extended context_layers to be LayerPlan, got %T", extendedResult.Metadata["context_layers"])
	}
	if compactLayers.Profile != BudgetProfileCompact {
		t.Fatalf("expected compact canonical profile, got %s", compactLayers.Profile)
	}
	if extendedLayers.Profile != BudgetProfileExtended {
		t.Fatalf("expected extended canonical profile, got %s", extendedLayers.Profile)
	}
	if compactLayers.Hot.MaxMessages >= extendedLayers.Hot.MaxMessages {
		t.Fatalf("expected extended hot layer to keep more recent messages, compact=%d extended=%d", compactLayers.Hot.MaxMessages, extendedLayers.Hot.MaxMessages)
	}

	compactMetrics := compactResult.Metadata["context_layer_metrics"].(map[string]interface{})
	extendedMetrics := extendedResult.Metadata["context_layer_metrics"].(map[string]interface{})

	compactWarm := compactMetrics["warm"].(map[string]interface{})
	extendedWarm := extendedMetrics["warm"].(map[string]interface{})
	if compactWarm["selected_items"] != 1 {
		t.Fatalf("expected compact profile to keep only failed observations, got %v", compactWarm["selected_items"])
	}
	if extendedWarm["selected_items"] != 3 {
		t.Fatalf("expected extended profile to keep all observations, got %v", extendedWarm["selected_items"])
	}

	compactCold := compactMetrics["cold"].(map[string]interface{})
	extendedCold := extendedMetrics["cold"].(map[string]interface{})
	if compactCold["ledger_injected"] != false {
		t.Fatalf("expected compact profile to skip ledger injection, got %v", compactCold["ledger_injected"])
	}
	if compactCold["recall_injected"] != false {
		t.Fatalf("expected compact profile to skip recall injection, got %v", compactCold["recall_injected"])
	}
	if extendedCold["ledger_injected"] != true {
		t.Fatalf("expected extended profile to inject ledger, got %v", extendedCold["ledger_injected"])
	}
	if extendedCold["recall_injected"] != true {
		t.Fatalf("expected extended profile to inject recall, got %v", extendedCold["recall_injected"])
	}
	if extendedCold["recall_count"] == 0 {
		t.Fatalf("expected extended profile recall count > 0, got %v", extendedCold["recall_count"])
	}
}

func TestTrimByTokenBudget_PreservesActiveTurnSnapshotBeforeOldRawAndStable(t *testing.T) {
	messages := []types.Message{
		*types.NewSystemMessage("system prompt"),
		{
			Role:    "assistant",
			Content: "Profile context",
			Metadata: types.Metadata{
				"context_stage": "profile",
			},
		},
		{
			Role:    "assistant",
			Content: "Decision ledger (continued): segment 2",
			Metadata: types.Metadata{
				"context_stage": "ledger",
			},
		},
		*types.NewAssistantMessage("recent assistant reply"),
		*types.NewUserMessage("latest user question"),
		{
			Role:    "assistant",
			Content: "Workspace recall",
			Metadata: types.Metadata{
				"context_stage": "workspace",
			},
		},
	}

	trimmed := trimByTokenBudget(messages, Budget{
		MaxPromptTokens: 30,
	}, func(messages []types.Message) int {
		return len(messages) * 10
	}, nil)

	require.Len(t, trimmed, 3)
	assert.Equal(t, "system", trimmed[0].Role)
	assert.Equal(t, "user", trimmed[1].Role)
	assert.Equal(t, "latest user question", trimmed[1].Content)
	assert.Equal(t, "workspace", trimmed[2].Metadata.GetString("context_stage", ""))
	assert.True(t, trimmed[2].Metadata.GetBool(metaContextSnapshot, false))
}

func TestTrimByTokenBudget_KeepsLastUserWhenStableContextExceedsBudget(t *testing.T) {
	messages := []types.Message{
		*types.NewSystemMessage("system prompt"),
		{
			Role:    "assistant",
			Content: "Profile context",
			Metadata: types.Metadata{
				"context_stage": "profile",
			},
		},
		{
			Role:    "assistant",
			Content: "Decision ledger (continued): newest segment",
			Metadata: types.Metadata{
				"context_stage": "ledger",
			},
		},
		*types.NewUserMessage("latest user question"),
	}

	trimmed := trimByTokenBudget(messages, Budget{
		MaxPromptTokens: 30,
	}, func(messages []types.Message) int {
		return len(messages) * 10
	}, nil)

	require.Len(t, trimmed, 3)
	assert.Equal(t, "system", trimmed[0].Role)
	assert.Equal(t, "profile", trimmed[1].Metadata.GetString("context_stage", ""))
	assert.Equal(t, "user", trimmed[2].Role)
	assert.Equal(t, "latest user question", trimmed[2].Content)
}

func TestTrimByTokenBudget_KeepsPostUserWorkspaceAsFrozenSnapshot(t *testing.T) {
	messages := []types.Message{
		*types.NewSystemMessage("system prompt"),
		{
			Role:    "assistant",
			Content: "Profile context",
			Metadata: types.Metadata{
				"context_stage": "profile",
			},
		},
		{
			Role:    "assistant",
			Content: "recent assistant reply",
		},
		*types.NewUserMessage("latest user question"),
		{
			Role:    "assistant",
			Content: "Workspace recall:\nSummary: workspace summary",
			Metadata: types.Metadata{
				"context_stage": "workspace",
			},
		},
	}

	// Budget fits 4 messages. Post-user workspace is already part of the active
	// turn snapshot, so old raw history is dropped before that immutable anchor.
	trimmed := trimByTokenBudget(messages, Budget{
		MaxPromptTokens: 40,
	}, func(messages []types.Message) int {
		return len(messages) * 10
	}, nil)

	require.Len(t, trimmed, 4)
	assert.Equal(t, "system", trimmed[0].Role)
	assert.Equal(t, "profile", trimmed[1].Metadata.GetString("context_stage", ""))
	assert.Equal(t, "user", trimmed[2].Role)
	assert.Equal(t, "latest user question", trimmed[2].Content)
	assert.Equal(t, "workspace", trimmed[3].Metadata.GetString("context_stage", ""))
	assert.True(t, trimmed[3].Metadata.GetBool(metaContextSnapshot, false))
}

func TestTrimByTokenBudget_DropsWholeToolReplayBlockInsteadOfLeavingOrphanTools(t *testing.T) {
	messages := []types.Message{
		*types.NewSystemMessage("system prompt"),
		*types.NewUserMessage("previous user turn"),
		{
			Role:      "assistant",
			Content:   "Let me inspect the diff.",
			ToolCalls: []types.ToolCall{{ID: "call_1", Name: "git_diff"}, {ID: "call_2", Name: "git_diff"}},
		},
		*types.NewToolMessage("call_1", "diff part 1"),
		*types.NewToolMessage("call_2", "diff part 2"),
		*types.NewUserMessage("继续"),
	}

	trimmed := trimByTokenBudget(messages, Budget{
		MaxPromptTokens: 20,
	}, func(messages []types.Message) int {
		return len(messages) * 10
	}, nil)

	require.Len(t, trimmed, 2)
	assert.Equal(t, "system", trimmed[0].Role)
	assert.Equal(t, "user", trimmed[1].Role)
	assert.Equal(t, "继续", trimmed[1].Content)
	for _, message := range trimmed {
		assert.NotEqual(t, "tool", message.Role, "trimmed history should not contain orphan tool messages")
	}
}

func TestTrimMessageCount_DropsWholeToolReplayBlockInsteadOfLeavingOrphanTools(t *testing.T) {
	messages := []types.Message{
		*types.NewSystemMessage("system prompt"),
		*types.NewUserMessage("previous user turn"),
		{
			Role:      "assistant",
			Content:   "Let me inspect the diff.",
			ToolCalls: []types.ToolCall{{ID: "call_1", Name: "git_diff"}, {ID: "call_2", Name: "git_diff"}},
		},
		*types.NewToolMessage("call_1", "diff part 1"),
		*types.NewToolMessage("call_2", "diff part 2"),
		*types.NewUserMessage("继续"),
	}

	trimmed := trimMessageCount(messages, 4)

	require.Len(t, trimmed, 2)
	assert.Equal(t, "system", trimmed[0].Role)
	assert.Equal(t, "user", trimmed[1].Role)
	assert.Equal(t, "继续", trimmed[1].Content)
	for _, message := range trimmed {
		assert.NotEqual(t, "tool", message.Role, "trimmed history should not contain orphan tool messages")
	}
}

func TestTrimMessageCount_PreservesUserAnchorInsideLongCompletedTurn(t *testing.T) {
	messages := []types.Message{
		*types.NewSystemMessage("system prompt"),
		*types.NewUserMessage("inspect the session failure"),
		{
			Role:      "assistant",
			ToolCalls: []types.ToolCall{{ID: "call_1", Name: "shell"}},
		},
		*types.NewToolMessage("call_1", "first result"),
		{
			Role:      "assistant",
			ToolCalls: []types.ToolCall{{ID: "call_2", Name: "shell"}},
		},
		*types.NewToolMessage("call_2", "second result"),
		{
			Role:      "assistant",
			ToolCalls: []types.ToolCall{{ID: "call_3", Name: "shell"}},
		},
		*types.NewToolMessage("call_3", "third result"),
		*types.NewAssistantMessage("the history window is the cause"),
		*types.NewUserMessage("如何修复"),
	}

	trimmed := trimMessageCount(messages, 6)

	require.LessOrEqual(t, len(trimmed), 6)
	require.GreaterOrEqual(t, len(trimmed), 3)
	assert.Equal(t, "system", trimmed[0].Role)
	assert.Equal(t, "user", trimmed[1].Role)
	assert.Equal(t, "inspect the session failure", trimmed[1].Content)
	assert.Equal(t, "user", trimmed[len(trimmed)-1].Role)
	assert.Equal(t, "如何修复", trimmed[len(trimmed)-1].Content)
	assertToolReplayBlocksComplete(t, trimmed)
}

func TestTrimMessageCount_PreservesFrozenTurnContextWithUserAnchor(t *testing.T) {
	goal := *types.NewDeveloperMessage("Persistent goal.\n\nkeep snapshot")
	goal.Metadata = types.Metadata{
		"context_stage":    contextStageActiveGoal,
		"context_snapshot": true,
		"context_turn_id":  "turn-1",
	}
	todo := *types.NewAssistantMessage("Current todos:\n- freeze context")
	todo.Metadata = types.Metadata{
		"context_stage":    "todo_state",
		"context_snapshot": true,
		"context_turn_id":  "turn-1",
	}
	messages := []types.Message{
		*types.NewSystemMessage("system prompt"),
		*types.NewUserMessage("inspect the session failure"),
		goal,
		todo,
		{
			Role:      "assistant",
			ToolCalls: []types.ToolCall{{ID: "call_1", Name: "shell"}},
		},
		*types.NewToolMessage("call_1", "first result"),
		{
			Role:      "assistant",
			ToolCalls: []types.ToolCall{{ID: "call_2", Name: "shell"}},
		},
		*types.NewToolMessage("call_2", "second result"),
		{
			Role:      "assistant",
			ToolCalls: []types.ToolCall{{ID: "call_3", Name: "shell"}},
		},
		*types.NewToolMessage("call_3", "third result"),
		*types.NewAssistantMessage("the history window is the cause"),
		*types.NewUserMessage("continue with the next turn"),
	}

	trimmed := trimMessageCount(messages, 6)

	require.LessOrEqual(t, len(trimmed), 6)
	require.GreaterOrEqual(t, len(trimmed), 4)
	require.Equal(t, "system", trimmed[0].Role)
	require.Equal(t, "user", trimmed[1].Role)
	require.Equal(t, "inspect the session failure", trimmed[1].Content)
	require.Equal(t, contextStageActiveGoal, trimmed[2].Metadata.GetString(metaContextStage, ""))
	require.True(t, trimmed[2].Metadata.GetBool(metaContextSnapshot, false))
	require.Equal(t, "todo_state", trimmed[3].Metadata.GetString(metaContextStage, ""))
	require.True(t, trimmed[3].Metadata.GetBool(metaContextSnapshot, false))
	require.Equal(t, "user", trimmed[len(trimmed)-1].Role)
	require.Equal(t, "continue with the next turn", trimmed[len(trimmed)-1].Content)
	assertToolReplayBlocksComplete(t, trimmed)
}

func assertToolReplayBlocksComplete(t *testing.T, messages []types.Message) {
	t.Helper()
	for index, message := range messages {
		if message.Role != "assistant" || len(message.ToolCalls) == 0 {
			continue
		}
		results := make(map[string]bool, len(message.ToolCalls))
		for next := index + 1; next < len(messages) && messages[next].Role == "tool"; next++ {
			results[messages[next].ToolCallID] = true
		}
		for _, call := range message.ToolCalls {
			assert.True(t, results[call.ID], "tool call %s must retain its adjacent result", call.ID)
		}
	}
}

func ledgerMessagesFromResult(messages []types.Message) []types.Message {
	ledgers := make([]types.Message, 0)
	for _, message := range messages {
		if message.Metadata.GetString("context_stage", "") != "ledger" {
			continue
		}
		ledgers = append(ledgers, message)
	}
	return ledgers
}

func compactionMessagesFromResult(messages []types.Message) []types.Message {
	compactions := make([]types.Message, 0)
	for _, message := range messages {
		if message.Metadata.GetString("context_stage", "") != "compaction" {
			continue
		}
		compactions = append(compactions, message)
	}
	return compactions
}

func containsSourceRef(refs []string, prefix string) bool {
	for _, ref := range refs {
		if strings.HasPrefix(ref, prefix) {
			return true
		}
	}
	return false
}

func containsEvent(events []string, want string) bool {
	for _, event := range events {
		if event == want {
			return true
		}
	}
	return false
}

// testTokenCounterStageAware builds a CountTokens used by contextmgr Build tests that must
// satisfy two opposing constraints after budget-gated keepRecent:
//
//  1. Raw history (no context_stage) is priced at rawCost so the estimate can exceed
//     MaxPromptTokens and engage keepRecent / compact / ledger injection.
//  2. Injected layers tagged with context_stage (compact/recall/warm_memory/ledger/...)
//     are priced at injectedCost so the final trimByTokenBudget/MaxMessages pass does not
//     drop them immediately after injection.
//
// Prefer this over a constant high return (e.g. 10000) or a flat len*N counter when the
// test asserts that injected stages survive Build. Keep intentional flat counters for
// boundary math tests that carefully choose N relative to MaxPromptTokens.
func testTokenCounterStageAware(rawCost, injectedCost int) TokenCounter {
	if rawCost < 0 {
		rawCost = 0
	}
	if injectedCost < 0 {
		injectedCost = 0
	}
	return func(messages []types.Message) int {
		total := 0
		for _, message := range messages {
			if message.Metadata.GetString("context_stage", "") != "" {
				total += injectedCost
				continue
			}
			total += rawCost
		}
		return total
	}
}

func TestManager_Build_DoesNotKeepRecentWhenUnderTokenBudget(t *testing.T) {
	history := []types.Message{*types.NewSystemMessage("system prompt")}
	for i := 0; i < 12; i++ {
		history = append(history,
			*types.NewUserMessage("long-running step"),
			*types.NewAssistantMessage("continuing analysis with tool results"),
		)
	}
	// 1 system + 24 non-system messages; KeepRecentMessages defaults to 8.
	manager := NewManager(Budget{
		MaxPromptTokens:    10000,
		MaxMessages:        40,
		KeepRecentMessages: 8,
	}, nil)

	result := manager.Build(context.Background(), BuildInput{
		SessionID: "session-under-budget",
		Goal:      "continue multi-step work",
		History:   history,
		// Under MaxPromptTokens: must preserve full history, not hard keepRecent=8.
		CountTokens:            func(messages []types.Message) int { return len(messages) * 20 },
		EnablePromptCompaction: true,
	})

	if applied, _ := result.Metadata["hot_keep_recent_applied"].(bool); applied {
		t.Fatalf("expected hot keepRecent not applied under budget, metadata=%v", result.Metadata["hot_keep_recent_applied"])
	}
	require.GreaterOrEqual(t, len(result.Messages), len(history),
		"under-budget Build must not silently drop long history via keepRecent")
	// Original early user/assistant turns must still be present (not only last 8).
	foundEarly := false
	for _, message := range result.Messages {
		if message.Role == "user" && strings.Contains(message.Content, "long-running step") {
			foundEarly = true
			break
		}
	}
	if !foundEarly {
		t.Fatal("expected early history messages to survive under-budget Build")
	}
}

func TestShouldApplyPromptCompactionPressure(t *testing.T) {
	budget := Budget{MaxPromptTokens: 100}
	messages := []types.Message{*types.NewUserMessage("x")}

	if !shouldApplyPromptCompactionPressure(messages, budget, nil) {
		t.Fatal("nil counter should treat explicit compaction as opted-in")
	}
	if shouldApplyPromptCompactionPressure(messages, budget, func([]types.Message) int { return 50 }) {
		t.Fatal("under-budget counter must not apply keepRecent pressure")
	}
	if !shouldApplyPromptCompactionPressure(messages, budget, func([]types.Message) int { return 150 }) {
		t.Fatal("over-budget counter must apply keepRecent pressure")
	}
}

func TestTokenCounterStageAware(t *testing.T) {
	raw := *types.NewUserMessage("raw")
	injected := *types.NewAssistantMessage("inject")
	injected.Metadata = types.Metadata{"context_stage": "recall"}
	counter := testTokenCounterStageAware(80, 15)
	if got := counter([]types.Message{raw, raw, injected}); got != 175 {
		t.Fatalf("expected 80+80+15=175, got %d", got)
	}
	if got := counter([]types.Message{injected, injected}); got != 30 {
		t.Fatalf("expected 15+15=30, got %d", got)
	}
}

func TestManager_BuildFreezesDynamicTailAndKeepsExactPrefix(t *testing.T) {
	manager := NewManager(DefaultBudget(), nil)
	manager.Strategy.RecallMode = RecallModeDisabled
	manager.Strategy.WorkspaceMode = WorkspaceModeDisabled
	manager.Strategy.ObservationMode = ObservationModeAll

	todo := *types.NewAssistantMessage("Current todos:\n- freeze context")
	todo.Metadata = types.Metadata{"context_stage": "todo_state"}

	history := []types.Message{
		*types.NewSystemMessage("stable system"),
		*types.NewUserMessage("inspect the runtime"),
		todo,
	}

	first := manager.Build(context.Background(), BuildInput{
		TraceID:            "turn-1",
		SessionID:          "session-prefix",
		Goal:               "inspect the runtime",
		History:            history,
		ActiveGoalGuidance: "Persistent goal.\n\nfinish cache work",
	})
	require.NotEmpty(t, first.Messages)
	require.True(t, first.Metadata["active_goal_injected"].(bool))

	userIdx := -1
	goalIdx := -1
	todoIdx := -1
	for index, message := range first.Messages {
		stage := message.Metadata.GetString("context_stage", "")
		switch stage {
		case "todo_state":
			todoIdx = index
			require.Equal(t, todo, message, "existing history must not be mutated merely to add snapshot metadata")
		case "active_goal":
			goalIdx = index
			require.Equal(t, "developer", message.Role)
			require.Contains(t, message.Content, "finish cache work")
			require.True(t, message.Metadata.GetBool("context_snapshot", false))
		}
		if message.Role == "user" && message.Content == "inspect the runtime" {
			userIdx = index
		}
	}
	require.GreaterOrEqual(t, userIdx, 0)
	require.Greater(t, todoIdx, userIdx)
	require.Greater(t, goalIdx, userIdx)

	// Simulate the first model step appending assistant + tool traffic after the
	// frozen snapshot. The next Build must keep the previous managed prefix exact.
	historyAfterTools := append(cloneMessages(first.Messages),
		*types.NewAssistantMessage("calling tools"),
		*types.NewToolMessage("call-1", "tool output"),
	)
	// Drop the leading system message so Build re-reads the durable history shape
	// used by the loop (system may also come from mergeConfiguredSystemPrompt).
	// Keep full first.Messages because that is what reusablePromptHistory stores.
	second := manager.Build(context.Background(), BuildInput{
		TraceID:            "turn-1",
		SessionID:          "session-prefix",
		Goal:               "inspect the runtime",
		History:            historyAfterTools,
		ActiveGoalGuidance: "Persistent goal.\n\nfinish cache work UPDATED",
	})
	require.True(t, second.Metadata["turn_context_snapshot_reused"].(bool))
	require.GreaterOrEqual(t, len(second.Messages), len(first.Messages)+2)
	require.Equal(t, first.Messages, second.Messages[:len(first.Messages)])
	require.Equal(t, "calling tools", second.Messages[len(first.Messages)].Content)
	require.Equal(t, "tool", second.Messages[len(first.Messages)+1].Role)

	// Goal text must stay frozen from the first snapshot, not rewrite in place.
	foundUpdatedGoal := false
	for _, message := range second.Messages {
		if message.Metadata.GetString("context_stage", "") == "active_goal" &&
			strings.Contains(message.Content, "UPDATED") {
			foundUpdatedGoal = true
		}
	}
	require.False(t, foundUpdatedGoal, "frozen goal snapshot must not rewrite mid-turn")
}

func TestManager_BuildKeepsDynamicStageSnapshotBeforeToolReplay(t *testing.T) {
	manager := NewManager(DefaultBudget(), nil)
	manager.Strategy.RecallMode = RecallModeDisabled
	manager.Strategy.WorkspaceMode = WorkspaceModeDisabled

	history := []types.Message{
		*types.NewSystemMessage("stable system"),
		*types.NewUserMessage("inspect the runtime"),
	}
	for _, stage := range []string{"fact_ledger", "recall", "team", "todo_state"} {
		message := *types.NewAssistantMessage("frozen " + stage)
		message.Metadata = types.Metadata{
			metaContextStage:    stage,
			metaContextSnapshot: true,
			metaContextTurnID:   "turn-dynamic-prefix",
		}
		history = append(history, message)
	}

	first := manager.Build(context.Background(), BuildInput{
		TraceID:            "turn-dynamic-prefix",
		SessionID:          "session-dynamic-prefix",
		Goal:               "inspect the runtime",
		History:            history,
		ActiveGoalGuidance: "Persistent goal.\n\nkeep dynamic stages fixed",
	})
	require.Equal(t, history, first.Messages[:len(history)])

	secondHistory := append(cloneMessages(first.Messages),
		*types.NewAssistantMessage("calling tools"),
		*types.NewToolMessage("call-1", "tool output"),
	)
	second := manager.Build(context.Background(), BuildInput{
		TraceID:            "turn-dynamic-prefix",
		SessionID:          "session-dynamic-prefix",
		Goal:               "inspect the runtime",
		History:            secondHistory,
		ActiveGoalGuidance: "Persistent goal.\n\nUPDATED must remain frozen",
	})

	require.Equal(t, secondHistory, second.Messages[:len(secondHistory)])
	for index, stage := range []string{"fact_ledger", "recall", "team", "todo_state"} {
		message := second.Messages[2+index]
		require.Equal(t, stage, message.Metadata.GetString(metaContextStage, ""))
		require.True(t, message.Metadata.GetBool(metaContextSnapshot, false))
		require.Equal(t, "turn-dynamic-prefix", message.Metadata.GetString(metaContextTurnID, ""))
	}
}

func TestManager_BuildAppendsMissingStagesAfterReplayWithoutMovingPrefix(t *testing.T) {
	// Stages that were not part of the earlier request may still be appended after
	// assistant/tool replay. That is cache-safe because it only grows the suffix.
	// Stages already present must stay frozen in place.
	manager := NewManager(DefaultBudget(), nil)
	manager.Strategy.RecallMode = RecallModeDisabled
	manager.Strategy.WorkspaceMode = WorkspaceModeDisabled
	manager.Strategy.ObservationMode = ObservationModeFailures

	goal := *types.NewDeveloperMessage("Persistent goal.\n\noriginal")
	goal.Metadata = types.Metadata{
		"context_stage":    "active_goal",
		"context_snapshot": true,
		"context_turn_id":  "turn-legacy",
	}
	history := []types.Message{
		*types.NewSystemMessage("stable system"),
		*types.NewUserMessage("inspect the runtime"),
		goal,
		*types.NewAssistantMessage("calling tools"),
		*types.NewToolMessage("call-1", "tool output"),
	}
	prefix := cloneMessages(history)
	result := manager.Build(context.Background(), BuildInput{
		TraceID:            "turn-legacy",
		SessionID:          "session-legacy",
		Goal:               "inspect the runtime",
		History:            history,
		ActiveGoalGuidance: "Persistent goal.\n\nUPDATED should not rewrite",
	})

	require.Nil(t, result.Metadata["active_goal_injected"])
	require.True(t, result.Metadata["active_goal_suppressed_for_snapshot"].(bool))
	require.GreaterOrEqual(t, len(result.Messages), len(prefix))
	require.Equal(t, prefix, result.Messages[:len(prefix)])

	goalCount := 0
	for _, message := range result.Messages {
		if message.Metadata.GetString("context_stage", "") == "active_goal" {
			goalCount++
			require.Contains(t, message.Content, "original")
			require.NotContains(t, message.Content, "UPDATED")
			require.True(t, message.Metadata.GetBool("context_snapshot", false))
		}
	}
	require.Equal(t, 1, goalCount)
}

func TestManager_BuildPreservesArbitraryHistoryOrderWithoutCompaction(t *testing.T) {
	manager := NewManager(Budget{
		MaxPromptTokens:     12000,
		MaxMessages:         24,
		KeepRecentMessages:  8,
		MaxRecallResults:    3,
		MaxObservationItems: 6,
	}, nil)

	lateSystem := types.NewSystemMessage("late runtime notice")
	lateSystem.Metadata["context_stage"] = "runtime_notice"
	goalOwned := types.NewAssistantMessage("result from the previous goal")
	goalOwned.Metadata["goal_id"] = "goal-old"
	history := []types.Message{
		*types.NewSystemMessage("stable instructions"),
		*types.NewUserMessage("first turn"),
		*types.NewAssistantMessage("first answer"),
		*lateSystem,
		*goalOwned,
		*types.NewUserMessage("second turn"),
	}

	result := manager.Build(context.Background(), BuildInput{
		TraceID:                "turn-second",
		GoalID:                 "goal-new",
		History:                history,
		EnablePromptCompaction: true,
		CountTokens:            func([]types.Message) int { return 100 },
	})

	require.Len(t, result.Messages, len(history))
	require.Equal(t, history, result.Messages)
	require.Equal(t, 0, result.Metadata["goal_scoped_messages_filtered"])
	require.Equal(t, false, result.Metadata["hot_keep_recent_applied"])
}

func TestManager_BuildNewUserTurnAppendsSnapshotWithoutRewritingPriorTurn(t *testing.T) {
	manager := NewManager(DefaultBudget(), nil)
	manager.Strategy.RecallMode = RecallModeDisabled
	manager.Strategy.WorkspaceMode = WorkspaceModeDisabled

	first := manager.Build(context.Background(), BuildInput{
		TraceID:            "turn-1",
		SessionID:          "session-multi-turn-prefix",
		Goal:               "finish the cache fix",
		History:            []types.Message{*types.NewSystemMessage("stable system"), *types.NewUserMessage("start")},
		ActiveGoalGuidance: "Persistent goal.\n\nfirst snapshot",
	})
	require.NotEmpty(t, first.Messages)

	secondHistory := append(cloneMessages(first.Messages),
		*types.NewAssistantMessage("first turn complete"),
		*types.NewUserMessage("continue"),
	)
	second := manager.Build(context.Background(), BuildInput{
		TraceID:            "turn-2",
		SessionID:          "session-multi-turn-prefix",
		Goal:               "finish the cache fix",
		History:            secondHistory,
		ActiveGoalGuidance: "Persistent goal.\n\nsecond snapshot",
	})

	require.Greater(t, len(second.Messages), len(secondHistory))
	require.Equal(t, secondHistory, second.Messages[:len(secondHistory)])
	require.Equal(t, contextStageActiveGoal, second.Messages[len(secondHistory)].Metadata.GetString(metaContextStage, ""))
	require.True(t, second.Messages[len(secondHistory)].Metadata.GetBool(metaContextSnapshot, false))
	require.Equal(t, "turn-2", second.Messages[len(secondHistory)].Metadata.GetString(metaContextTurnID, ""))
	require.Contains(t, second.Messages[len(secondHistory)].Content, "second snapshot")

	goalSnapshots := 0
	for _, message := range second.Messages {
		if message.Metadata.GetString(metaContextStage, "") == contextStageActiveGoal {
			goalSnapshots++
		}
	}
	require.Equal(t, 2, goalSnapshots)
}

func TestSplitManagedMessagesAdoptsPostUserDynamicContextAsRawHistory(t *testing.T) {
	snapshot := *types.NewAssistantMessage("frozen recall")
	snapshot.Metadata = types.Metadata{
		"context_stage":    "recall",
		"context_snapshot": true,
	}
	dynamic := *types.NewAssistantMessage("fresh recall")
	dynamic.Metadata = types.Metadata{"context_stage": "recall"}

	system, stable, dynamicMsgs, raw := splitManagedMessages([]types.Message{
		*types.NewSystemMessage("sys"),
		*types.NewUserMessage("hi"),
		snapshot,
		*types.NewAssistantMessage("answer"),
		dynamic,
	})
	require.Len(t, system, 1)
	require.Empty(t, stable)
	require.Empty(t, dynamicMsgs)
	require.Equal(t, []string{"hi", "frozen recall", "answer", "fresh recall"}, []string{raw[0].Content, raw[1].Content, raw[2].Content, raw[3].Content})
	require.True(t, raw[3].Metadata.GetBool(metaContextSnapshot, false))
}
