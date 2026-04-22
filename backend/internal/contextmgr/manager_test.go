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
		MaxPromptTokens:     200,
		MaxMessages:         5,
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
		TraceID:     "trace_ctx_1",
		SessionID:   "session-ctx",
		Goal:        "Find the error stack trace",
		History:     history,
		Memory:      mem,
		CountTokens: func(messages []types.Message) int { return len(messages) * 20 },
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
		CountTokens:  func(messages []types.Message) int { return len(messages) * 20 },
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
		SessionID: "session-compaction-segments",
		TaskID:    "task-compaction-segments",
		Goal:      "Summarize the root cause",
		History:   baseHistory,
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
		SessionID: "session-compaction-segments",
		TaskID:    "task-compaction-segments",
		Goal:      "Summarize the updated root cause",
		History:   extendedHistory,
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
		SessionID:   "session-tool-boundary",
		Goal:        "你好，创建两个团队成员，分别探索docs目录文件并汇报进度",
		History:     history,
		CountTokens: func(messages []types.Message) int { return len(messages) * 20 },
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
		SessionID:   "session-active-turn",
		TaskID:      "task-active-turn",
		Goal:        "继续分析当前实现",
		History:     history,
		CountTokens: func(messages []types.Message) int { return len(messages) * 20 },
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
		SessionID: "session-ledger",
		TaskID:    "task-ledger",
		Goal:      "Find the error stack trace",
		History:   history,
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
		SessionID: "session-ledger-segments",
		TaskID:    "task-ledger-segments",
		Goal:      "Summarize the root cause",
		History:   baseHistory,
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
		SessionID: "session-ledger-segments",
		TaskID:    "task-ledger-segments",
		Goal:      "Summarize the updated root cause",
		History:   extendedHistory,
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
		SessionID: "session-profile-ledger",
		TaskID:    "task-profile-ledger",
		Goal:      "Review the failure history",
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
		CountTokens: func(messages []types.Message) int { return len(messages) * 20 },
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
		CountTokens:  func(messages []types.Message) int { return len(messages) * 20 },
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

func TestTrimByTokenBudget_PrefersDroppingDynamicThenOldRawThenNewestStable(t *testing.T) {
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
	assert.Equal(t, "profile", trimmed[1].Metadata.GetString("context_stage", ""))
	assert.Equal(t, "user", trimmed[2].Role)
	assert.Equal(t, "latest user question", trimmed[2].Content)
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
