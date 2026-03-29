package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	mcpconfig "github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
	mcpprotocol "github.com/wwsheng009/ai-agent-runtime/internal/mcp/protocol"
	mcpregistry "github.com/wwsheng009/ai-agent-runtime/internal/mcp/registry"
	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	runtimellm "github.com/wwsheng009/ai-agent-runtime/internal/llm"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
	runtimetools "github.com/wwsheng009/ai-agent-runtime/internal/tools"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

func TestAICLIChatActorExecutor_DocsPromptRegression_CoversWorkspaceToolPriorityBlockedReplanAndStreamFallback(t *testing.T) {
	workspaceRoot := t.TempDir()
	writeTestFile(t, filepath.Join(workspaceRoot, "docs", "agents", "README.md"), "Agents doc: teammates explore docs and report findings.")
	writeTestFile(t, filepath.Join(workspaceRoot, "docs", "guides", "getting-started.md"), "Guides doc: start with docs/guides/getting-started.md for the docs toolkit.")

	manager, userID, dir, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	runtimeSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}

	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer teamStore.Close()

	provider := newDocsTeamRegressionProvider(workspaceRoot)
	llmRuntime := runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{
		DefaultProvider: "test-provider",
		DefaultModel:    "test-model",
	})
	if err := llmRuntime.RegisterProvider("test-provider", provider); err != nil {
		t.Fatalf("RegisterProvider: %v", err)
	}
	if err := llmRuntime.RegisterProviderAlias("test-model", "test-provider"); err != nil {
		t.Fatalf("RegisterProviderAlias: %v", err)
	}

	toolManager := runtimetools.NewDefaultManagerWithRuntimeConfig(newDocsShadowMCPManager(), &runtimecfg.RuntimeConfig{
		Workspace: runtimecfg.WorkspaceConfig{Root: workspaceRoot},
	})
	session := &ChatSession{
		ProviderName:   "test-provider",
		PermissionMode: runtimepolicy.ModeDefault,
		Model:          "test-model",
		SessionManager: manager,
		RuntimeSession: runtimeSession,
		SessionUserID:  userID,
		SessionDir:     dir,
		ProfileRoot:    workspaceRoot,
		Stream:         true,
		OutputFormat:   "interactive",
		ChatExecutor:   newAICLIActorChatExecutor(),
	}

	host := newWorkspaceLocalOrchestrationTestHost(t, session, llmRuntime, teamStore, toolManager, workspaceRoot)
	defer host.Close()
	session.LocalRuntimeHost = host
	host.BaseSession = session

	var (
		linesMu sync.Mutex
		lines   []string
	)
	bridge := newChatRuntimeEventBridge(session)
	bridge.writeLine = func(line string) {
		linesMu.Lock()
		defer linesMu.Unlock()
		lines = append(lines, line)
	}
	session.RuntimeEventBridge = bridge

	prompt := "创建几个团队成员来探索docs目录的文档"
	response, err := session.ChatExecutor.Execute(context.Background(), session, prompt)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if !shouldDisplayFinalResponse(session, response) {
		t.Fatalf("expected actor-first stream fallback response to be displayable, got %q", response)
	}
	if !strings.Contains(response, "team_docs") {
		t.Fatalf("expected team id in response, got %q", response)
	}

	waitForChatTestCondition(t, 6*time.Second, func() bool {
		rootTask, rootErr := teamStore.GetTask(context.Background(), "task_docs_root")
		followupTask, followupErr := findTeamTaskByTitle(context.Background(), teamStore, "team_docs", "Summarize docs/guides/getting-started.md")
		teamEvents, listErr := teamStore.ListTeamEvents(context.Background(), team.TeamEventFilter{TeamID: "team_docs"})
		linesMu.Lock()
		snapshot := append([]string(nil), lines...)
		linesMu.Unlock()
		return rootErr == nil &&
			followupErr == nil &&
			listErr == nil &&
			rootTask != nil &&
			followupTask != nil &&
			rootTask.Status == team.TaskStatusBlocked &&
			followupTask.Status == team.TaskStatusDone &&
			containsAnyChatTimelineLine(snapshot, fmt.Sprintf("[task] completed %s @docs_api docs/guides/getting-started.md explains how to start using the docs toolkit", followupTask.ID)) &&
			hasTeamEventAssignee(teamEvents, "task.completed", followupTask.ID, "docs_api")
	}, func() string {
		linesMu.Lock()
		snapshot := append([]string(nil), lines...)
		linesMu.Unlock()
		teamEvents, listErr := teamStore.ListTeamEvents(context.Background(), team.TeamEventFilter{TeamID: "team_docs"})
		rootTask, _ := teamStore.GetTask(context.Background(), "task_docs_root")
		followupTask, _ := findTeamTaskByTitle(context.Background(), teamStore, "team_docs", "Summarize docs/guides/getting-started.md")
		return fmt.Sprintf("timeline=%v team_events=%v team_events_err=%v root_task=%+v followup_task=%+v recent_runtime_events=%v",
			snapshot, teamEvents, listErr, rootTask, followupTask, host.EventBus.Recent(30))
	})

	teamRecord, err := teamStore.GetTeam(context.Background(), "team_docs")
	if err != nil {
		t.Fatalf("GetTeam: %v", err)
	}
	if teamRecord == nil {
		t.Fatal("expected team_docs to exist")
	}
	if teamRecord.LeadSessionID != runtimeSession.ID {
		t.Fatalf("expected lead session %q, got %+v", runtimeSession.ID, teamRecord)
	}

	teammates, err := teamStore.ListTeammates(context.Background(), "team_docs")
	if err != nil {
		t.Fatalf("ListTeammates: %v", err)
	}
	if len(teammates) != 2 {
		t.Fatalf("expected 2 teammates, got %+v", teammates)
	}
	seenSessionIDs := map[string]struct{}{}
	for _, mate := range teammates {
		if strings.TrimSpace(mate.SessionID) == "" {
			t.Fatalf("expected non-empty teammate session id, got %+v", mate)
		}
		if strings.EqualFold(strings.TrimSpace(mate.SessionID), "current") {
			t.Fatalf("expected teammate session id to avoid current placeholder, got %+v", mate)
		}
		if _, exists := seenSessionIDs[mate.SessionID]; exists {
			t.Fatalf("expected unique teammate session ids, got %+v", teammates)
		}
		seenSessionIDs[mate.SessionID] = struct{}{}
	}

	rootTask, err := teamStore.GetTask(context.Background(), "task_docs_root")
	if err != nil {
		t.Fatalf("GetTask task_docs_root: %v", err)
	}
	if rootTask == nil || rootTask.Status != team.TaskStatusBlocked || rootTask.Summary != "waiting on focused API guide summary" {
		t.Fatalf("expected blocked root task summary to persist, got %+v", rootTask)
	}
	if len(rootTask.ReadPaths) != 1 || rootTask.ReadPaths[0] != "docs" {
		t.Fatalf("expected root task read_paths to target docs, got %+v", rootTask)
	}

	followupTask, err := findTeamTaskByTitle(context.Background(), teamStore, "team_docs", "Summarize docs/guides/getting-started.md")
	if err != nil {
		t.Fatalf("find follow-up task: %v", err)
	}
	if followupTask == nil || followupTask.Status != team.TaskStatusDone {
		t.Fatalf("expected follow-up task done, got %+v", followupTask)
	}
	if followupTask.Summary != "docs/guides/getting-started.md explains how to start using the docs toolkit" {
		t.Fatalf("unexpected follow-up task summary: %+v", followupTask)
	}
	if len(followupTask.ReadPaths) != 1 || followupTask.ReadPaths[0] != "docs/guides" {
		t.Fatalf("expected follow-up read_paths to target docs/guides, got %+v", followupTask)
	}

	linesMu.Lock()
	snapshot := append([]string(nil), lines...)
	linesMu.Unlock()
	followupStartedLine := fmt.Sprintf("[task] started %s @docs_api", followupTask.ID)
	followupCompletedLine := fmt.Sprintf("[task] completed %s @docs_api docs/guides/getting-started.md explains how to start using the docs toolkit", followupTask.ID)
	if !containsAllChatTimelineLines(snapshot,
		"[tool] ls",
		"[tool] view",
		"[task] started task_docs_root @docs_arch",
		"[task] blocked task_docs_root @docs_arch waiting on focused API guide summary",
		followupStartedLine,
		followupCompletedLine,
	) {
		t.Fatalf("expected timeline lines not found, got %v", snapshot)
	}
	if !containsOrderedChatTimelineLines(snapshot,
		"[task] started task_docs_root @docs_arch",
		"[task] blocked task_docs_root @docs_arch waiting on focused API guide summary",
		followupStartedLine,
		followupCompletedLine,
	) {
		t.Fatalf("expected ordered task progression in timeline, got %v", snapshot)
	}
	if countExactChatTimelineLine(snapshot, followupStartedLine) != 1 {
		t.Fatalf("expected follow-up task to start exactly once, got %v", snapshot)
	}
	if countExactChatTimelineLine(snapshot, fmt.Sprintf("[task] started %s @docs_arch", followupTask.ID)) != 0 {
		t.Fatalf("expected blocked teammate not to be reused for follow-up task, got %v", snapshot)
	}
	if containsAnyChatTimelineLine(snapshot, "[tool] background_task", "[tool] bash") {
		t.Fatalf("expected docs exploration to use direct read tools instead of shell/background tools, got %v", snapshot)
	}

	mainPrompt := provider.systemPrompt(runtimeSession.ID)
	if !strings.Contains(mainPrompt, "Current workspace root: "+workspaceRoot) {
		t.Fatalf("expected main system prompt to carry workspace root, got %q", mainPrompt)
	}
	teammatePrompt := provider.systemPrompt("team_docs__docs_arch")
	if !strings.Contains(teammatePrompt, "Current workspace root: "+workspaceRoot) {
		t.Fatalf("expected teammate system prompt to carry workspace root, got %q", teammatePrompt)
	}
	lsOutput := provider.toolOutput("call-docs-arch-ls")
	if !strings.Contains(lsOutput, "agents/") || !strings.Contains(lsOutput, "guides/") {
		t.Fatalf("expected local ls output from workspace docs, got %q", lsOutput)
	}
	if strings.Contains(lsOutput, "ghost workspace") {
		t.Fatalf("expected local toolkit ls to beat toolkit MCP shadow, got %q", lsOutput)
	}
	viewOutput := provider.toolOutput("call-docs-api-view")
	if !strings.Contains(viewOutput, "Guides doc: start with docs/guides/getting-started.md for the docs toolkit.") {
		t.Fatalf("expected local guide view output, got %q", viewOutput)
	}
	if strings.Contains(viewOutput, "ghost view") {
		t.Fatalf("expected local toolkit view to beat toolkit MCP shadow, got %q", viewOutput)
	}

	events, err := teamStore.ListTeamEvents(context.Background(), team.TeamEventFilter{TeamID: "team_docs"})
	if err != nil {
		t.Fatalf("ListTeamEvents: %v", err)
	}
	assertTeamEventAssignee(t, events, "task.started", "task_docs_root", "docs_arch")
	assertTeamEventAssignee(t, events, "task.blocked", "task_docs_root", "docs_arch")
	assertTeamEventAssignee(t, events, "task.started", followupTask.ID, "docs_api")
	assertTeamEventAssignee(t, events, "task.completed", followupTask.ID, "docs_api")

	reloaded, err := manager.Get(context.Background(), runtimeSession.ID)
	if err != nil {
		t.Fatalf("manager.Get: %v", err)
	}
	if !historyContainsAssistantMessage(reloaded.History, response) {
		t.Fatalf("expected initial assistant response to persist, got %+v", reloaded.History)
	}
}

func TestAICLIChatActorExecutor_DocsListThenCreateTeamMembers_UsesObservedWorkspaceListing(t *testing.T) {
	workspaceRoot := findCommandsRepoRoot(t)
	expectedDocs := selectDocsTranscriptTargets(t, workspaceRoot)
	sessionLikeDir := t.TempDir()
	for _, name := range []string{"20260316_124811", "20260316_223402", "20260317_001228"} {
		mustMkdir(t, filepath.Join(sessionLikeDir, name))
	}
	previousWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	if err := os.Chdir(sessionLikeDir); err != nil {
		t.Fatalf("os.Chdir(%s): %v", sessionLikeDir, err)
	}
	defer func() {
		_ = os.Chdir(previousWD)
	}()
	for _, name := range expectedDocs {
		required := filepath.Join(workspaceRoot, "docs", name)
		if _, statErr := os.Stat(required); statErr != nil {
			t.Fatalf("expected transcript regression fixture %q to exist: %v", required, statErr)
		}
	}

	manager, userID, dir, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	runtimeSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}

	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer teamStore.Close()

	provider := &docsListTranscriptProvider{expectedDocs: expectedDocs}
	llmRuntime := runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{
		DefaultProvider: "test-provider",
		DefaultModel:    "test-model",
	})
	if err := llmRuntime.RegisterProvider("test-provider", provider); err != nil {
		t.Fatalf("RegisterProvider: %v", err)
	}
	if err := llmRuntime.RegisterProviderAlias("test-model", "test-provider"); err != nil {
		t.Fatalf("RegisterProviderAlias: %v", err)
	}

	toolManager := runtimetools.NewDefaultManagerWithRuntimeConfig(newDocsShadowMCPManager(), &runtimecfg.RuntimeConfig{
		Workspace: runtimecfg.WorkspaceConfig{Root: workspaceRoot},
	})
	session := &ChatSession{
		ProviderName:   "test-provider",
		PermissionMode: runtimepolicy.ModeDefault,
		Model:          "test-model",
		SessionManager: manager,
		RuntimeSession: runtimeSession,
		SessionUserID:  userID,
		SessionDir:     dir,
		ProfileRoot:    workspaceRoot,
		OutputFormat:   "interactive",
		ChatExecutor:   newAICLIActorChatExecutor(),
	}

	host := newWorkspaceLocalOrchestrationTestHost(t, session, llmRuntime, teamStore, toolManager, workspaceRoot)
	defer host.Close()
	session.LocalRuntimeHost = host
	host.BaseSession = session

	firstResponse, err := session.ChatExecutor.Execute(context.Background(), session, "查看docs目录的文件")
	if err != nil {
		t.Fatalf("first Execute failed: %v", err)
	}
	for _, name := range expectedDocs {
		want := name + "/"
		if !strings.Contains(firstResponse, want) {
			t.Fatalf("expected docs listing to contain %q, got %q", want, firstResponse)
		}
	}
	for _, unwanted := range []string{"agents/", "anthropic/"} {
		if strings.Contains(firstResponse, unwanted) {
			t.Fatalf("expected docs listing to avoid ghost MCP entries like %q, got %q", unwanted, firstResponse)
		}
	}

	secondResponse, err := session.ChatExecutor.Execute(context.Background(), session, "创建几个team member来探索文件列表")
	if err != nil {
		t.Fatalf("second Execute failed: %v", err)
	}
	if !strings.Contains(secondResponse, "team_list_docs") {
		t.Fatalf("expected transcript follow-up to create team_list_docs, got %q; history=%v", secondResponse, provider.historySnapshot())
	}
	if session.ActiveTeam == nil || session.ActiveTeam.TeamID != "team_list_docs" {
		t.Fatalf("expected active team binding for transcript flow, got %+v", session.ActiveTeam)
	}

	tasks, err := teamStore.ListTasks(context.Background(), team.TaskFilter{TeamID: "team_list_docs"})
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(tasks) != 3 {
		t.Fatalf("expected 3 transcript tasks, got %+v", tasks)
	}
	expectedReadPaths := make(map[string]string, len(expectedDocs))
	for _, name := range expectedDocs {
		expectedReadPaths["Explore docs/"+name] = "docs/" + name
	}
	for _, task := range tasks {
		wantPath, ok := expectedReadPaths[task.Title]
		if !ok {
			t.Fatalf("unexpected transcript task %+v", task)
		}
		if len(task.ReadPaths) != 1 || task.ReadPaths[0] != wantPath {
			t.Fatalf("expected task %q to target %q, got %+v", task.Title, wantPath, task)
		}
	}

	if provider.sawGhostHistory() {
		t.Fatalf("expected provider history grounding to use local docs list, saw ghost history in %+v", provider.historySnapshot())
	}
}

type docsTeamRegressionProvider struct {
	workspaceRoot string

	mu            sync.Mutex
	systemPrompts map[string]string
	toolOutputs   map[string]string
}

type docsListTranscriptProvider struct {
	mu           sync.Mutex
	history      []string
	expectedDocs []string
}

func newDocsTeamRegressionProvider(workspaceRoot string) *docsTeamRegressionProvider {
	return &docsTeamRegressionProvider{
		workspaceRoot: strings.TrimSpace(workspaceRoot),
		systemPrompts: make(map[string]string),
		toolOutputs:   make(map[string]string),
	}
}

func (p *docsTeamRegressionProvider) Name() string { return "test-provider" }

func (p *docsTeamRegressionProvider) Call(ctx context.Context, req *runtimellm.LLMRequest) (*runtimellm.LLMResponse, error) {
	sessionID := strings.TrimSpace(metadataString(req.Metadata, "session_id"))
	p.recordRequest(sessionID, req.Messages)

	lastUser := latestMessageByRole(req.Messages, "user")
	lastTool := latestMessageByRole(req.Messages, "tool")

	switch {
	case strings.TrimSpace(lastTool.ToolCallID) == "" &&
		strings.TrimSpace(lastUser.Content) == "创建几个团队成员来探索docs目录的文档":
		return &runtimellm.LLMResponse{
			Model: req.Model,
			ToolCalls: []runtimetypes.ToolCall{
				{
					ID:   "call-spawn-docs",
					Name: toolbroker.ToolSpawnTeam,
					Args: map[string]interface{}{
						"team_id":    "team_docs",
						"auto_start": true,
						"teammates": []interface{}{
							map[string]interface{}{"id": "docs_arch", "name": "Docs Architecture", "profile": "explorer"},
							map[string]interface{}{"id": "docs_api", "name": "Docs API", "profile": "explorer"},
						},
						"tasks": []interface{}{
							map[string]interface{}{
								"id":           "task_docs_root",
								"title":        "Explore docs root",
								"goal":         "Explore docs and decide which guide needs a focused summary next",
								"assignee":     "docs_arch",
								"read_paths":   []interface{}{"docs"},
								"deliverables": []interface{}{"docs summary"},
							},
						},
					},
				},
			},
		}, nil

	case strings.HasPrefix(strings.TrimSpace(lastUser.Content), "You are the team lead. Decompose the goal into a DAG plan."):
		return &runtimellm.LLMResponse{
			Content: `{"tasks":[{"id":"task_docs_guides","title":"Summarize docs/guides/getting-started.md","goal":"Read docs/guides/getting-started.md and summarize the getting started guidance","read_paths":["docs/guides"],"deliverables":["guide summary"]}],"summary":"follow up on blocked docs exploration"}`,
			Model:   req.Model,
		}, nil

	case strings.HasPrefix(strings.TrimSpace(lastUser.Content), "You are the team lead. Provide a concise final summary"):
		return &runtimellm.LLMResponse{
			Content: "docs 探索已完成，agents 说明协作方式，guides 给出起步路径。",
			Model:   req.Model,
		}, nil

	case strings.HasPrefix(strings.TrimSpace(lastUser.Content), "You are teammate"):
		return p.handleTeammateRequest(sessionID, req, lastTool)

	case strings.TrimSpace(lastTool.ToolCallID) == "call-spawn-docs" &&
		strings.TrimSpace(lastUser.Content) == "创建几个团队成员来探索docs目录的文档":
		return &runtimellm.LLMResponse{
			Content: "已创建 team_docs，两个成员会探索 docs 目录并持续回报。",
			Model:   req.Model,
		}, nil
	}

	return &runtimellm.LLMResponse{Content: "no-op", Model: req.Model}, nil
}

func (p *docsTeamRegressionProvider) handleTeammateRequest(sessionID string, req *runtimellm.LLMRequest, lastTool runtimetypes.Message) (*runtimellm.LLMResponse, error) {
	switch strings.TrimSpace(sessionID) {
	case "team_docs__docs_arch":
		switch strings.TrimSpace(lastTool.ToolCallID) {
		case "":
			return singleToolResponse(req.Model, "call-docs-arch-spec", toolbroker.ToolReadTaskSpec, map[string]interface{}{})
		case "call-docs-arch-spec":
			return singleToolResponse(req.Model, "call-docs-arch-ls", "ls", map[string]interface{}{
				"path":  filepath.Join(p.workspaceRoot, "docs"),
				"depth": 2,
			})
		case "call-docs-arch-ls":
			return singleToolResponse(req.Model, "call-docs-arch-view", "view", map[string]interface{}{
				"file_path": filepath.Join(p.workspaceRoot, "docs", "agents", "README.md"),
				"limit":     20,
			})
		case "call-docs-arch-view":
			return &runtimellm.LLMResponse{
				Content: "Scanned docs root and agents intro.\n\n```json\n{\"task_status\":\"blocked\",\"summary\":\"waiting on focused API guide summary\",\"blocker\":\"need a dedicated summary for docs/guides/getting-started.md\"}\n```",
				Model:   req.Model,
			}, nil
		}

	case "team_docs__docs_api":
		switch strings.TrimSpace(lastTool.ToolCallID) {
		case "":
			return singleToolResponse(req.Model, "call-docs-api-context", toolbroker.ToolReadTaskContext, map[string]interface{}{})
		case "call-docs-api-context":
			return singleToolResponse(req.Model, "call-docs-api-view", "view", map[string]interface{}{
				"file_path": filepath.Join(p.workspaceRoot, "docs", "guides", "getting-started.md"),
				"limit":     20,
			})
		case "call-docs-api-view":
			return &runtimellm.LLMResponse{
				Content: "Focused guide reviewed.\n\n```json\n{\"task_status\":\"done\",\"summary\":\"docs/guides/getting-started.md explains how to start using the docs toolkit\"}\n```",
				Model:   req.Model,
			}, nil
		}
	}

	return &runtimellm.LLMResponse{Content: "no-op", Model: req.Model}, nil
}

func (p *docsTeamRegressionProvider) Stream(ctx context.Context, req *runtimellm.LLMRequest) (<-chan runtimellm.StreamChunk, error) {
	ch := make(chan runtimellm.StreamChunk, 1)
	close(ch)
	return ch, nil
}

func (p *docsTeamRegressionProvider) CountTokens(text string) int { return len(text) }

func (p *docsTeamRegressionProvider) GetCapabilities() *runtimellm.ModelCapabilities {
	return &runtimellm.ModelCapabilities{SupportsTools: true, SupportsStreaming: true}
}

func (p *docsTeamRegressionProvider) CheckHealth(ctx context.Context) error { return nil }

func (p *docsTeamRegressionProvider) recordRequest(sessionID string, messages []runtimetypes.Message) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if prompt := strings.TrimSpace(systemPromptFromMessages(messages)); prompt != "" && strings.TrimSpace(sessionID) != "" {
		p.systemPrompts[sessionID] = prompt
	}
	if tool := latestMessageByRole(messages, "tool"); strings.TrimSpace(tool.ToolCallID) != "" {
		p.toolOutputs[strings.TrimSpace(tool.ToolCallID)] = strings.TrimSpace(tool.Content)
	}
}

func (p *docsTeamRegressionProvider) systemPrompt(sessionID string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.systemPrompts[strings.TrimSpace(sessionID)]
}

func (p *docsTeamRegressionProvider) toolOutput(toolCallID string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.toolOutputs[strings.TrimSpace(toolCallID)]
}

func (p *docsListTranscriptProvider) Name() string { return "test-provider" }

func (p *docsListTranscriptProvider) Call(ctx context.Context, req *runtimellm.LLMRequest) (*runtimellm.LLMResponse, error) {
	lastUser := latestMessageByRole(req.Messages, "user")
	lastTool := latestMessageByRole(req.Messages, "tool")
	p.recordHistory(req.Messages)

	switch {
	case strings.TrimSpace(lastTool.ToolCallID) == "" && strings.TrimSpace(lastUser.Content) == "查看docs目录的文件":
		return singleToolResponse(req.Model, "call-list-docs", "ls", map[string]interface{}{
			"path":  "docs",
			"depth": 1,
		})
	case strings.TrimSpace(lastTool.ToolCallID) == "call-list-docs" &&
		strings.TrimSpace(lastUser.Content) == "查看docs目录的文件":
		return &runtimellm.LLMResponse{
			Content: p.summarizeDocsListToolMessage(lastTool.Content),
			Model:   req.Model,
		}, nil
	case strings.TrimSpace(lastTool.ToolCallID) == "call-spawn-list-based":
		return &runtimellm.LLMResponse{Content: "已基于刚才看到的文件列表创建 team_list_docs。", Model: req.Model}, nil
	case strings.TrimSpace(lastTool.ToolCallID) == "call-spawn-ghost":
		return &runtimellm.LLMResponse{Content: "误用了不存在的 ghost docs 列表。", Model: req.Model}, nil
	case strings.TrimSpace(lastUser.Content) == "创建几个team member来探索文件列表":
		if p.sawLocalDocsHistory(req.Messages) {
			teammates := make([]interface{}, 0, len(p.targetDocs()))
			tasks := make([]interface{}, 0, len(p.targetDocs()))
			for index, name := range p.targetDocs() {
				agentID := fmt.Sprintf("agent-%d", index+1)
				teammates = append(teammates, map[string]interface{}{
					"id":   agentID,
					"name": "Explorer " + strings.ToUpper(string(rune('A'+index))),
				})
				tasks = append(tasks, map[string]interface{}{
					"id":         fmt.Sprintf("explore-%s", strings.ReplaceAll(name, "_", "-")),
					"title":      "Explore docs/" + name,
					"goal":       "Inspect docs/" + name,
					"assignee":   agentID,
					"read_paths": []interface{}{"docs/" + name},
				})
			}
			return &runtimellm.LLMResponse{
				Model: req.Model,
				ToolCalls: []runtimetypes.ToolCall{
					{
						ID:   "call-spawn-list-based",
						Name: toolbroker.ToolSpawnTeam,
						Args: map[string]interface{}{
							"team_id":    "team_list_docs",
							"auto_start": false,
							"teammates":  teammates,
							"tasks":      tasks,
						},
					},
				},
			}, nil
		}
		return &runtimellm.LLMResponse{
			Model: req.Model,
			ToolCalls: []runtimetypes.ToolCall{
				{
					ID:   "call-spawn-ghost",
					Name: toolbroker.ToolSpawnTeam,
					Args: map[string]interface{}{
						"team_id":    "team_wrong_docs",
						"auto_start": false,
						"tasks": []interface{}{
							map[string]interface{}{"id": "explore-agents", "title": "Explore docs/agents", "goal": "Inspect docs/agents", "read_paths": []interface{}{"docs/agents"}},
						},
					},
				},
			},
		}, nil
	default:
		return &runtimellm.LLMResponse{Content: "no-op", Model: req.Model}, nil
	}
}

func (p *docsListTranscriptProvider) Stream(ctx context.Context, req *runtimellm.LLMRequest) (<-chan runtimellm.StreamChunk, error) {
	ch := make(chan runtimellm.StreamChunk, 1)
	close(ch)
	return ch, nil
}

func (p *docsListTranscriptProvider) CountTokens(text string) int { return len(text) }

func (p *docsListTranscriptProvider) GetCapabilities() *runtimellm.ModelCapabilities {
	return &runtimellm.ModelCapabilities{SupportsTools: true}
}

func (p *docsListTranscriptProvider) CheckHealth(ctx context.Context) error { return nil }

func (p *docsListTranscriptProvider) recordHistory(messages []runtimetypes.Message) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.history = p.history[:0]
	for _, message := range messages {
		if strings.TrimSpace(message.Content) == "" {
			continue
		}
		p.history = append(p.history, strings.TrimSpace(message.Content))
	}
}

func (p *docsListTranscriptProvider) historySnapshot() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	cloned := make([]string, len(p.history))
	copy(cloned, p.history)
	return cloned
}

func (p *docsListTranscriptProvider) sawGhostHistory() bool {
	for _, item := range p.historySnapshot() {
		if strings.Contains(item, "📁 agents/") || strings.Contains(item, "docs/agents") || strings.Contains(item, "anthropic/") {
			return true
		}
	}
	return false
}

func (p *docsListTranscriptProvider) sawLocalDocsHistory(messages []runtimetypes.Message) bool {
	var joined strings.Builder
	for _, message := range messages {
		if strings.TrimSpace(message.Content) == "" {
			continue
		}
		joined.WriteString("\n")
		joined.WriteString(message.Content)
	}
	content := joined.String()
	for _, name := range p.targetDocs() {
		if !strings.Contains(content, name+"/") {
			return false
		}
	}
	return true
}

func (p *docsListTranscriptProvider) summarizeDocsListToolMessage(content string) string {
	parts := make([]string, 0, 3)
	for _, name := range p.targetDocs() {
		entry := name + "/"
		if strings.Contains(content, entry) {
			parts = append(parts, entry)
		}
	}
	if len(parts) == 0 {
		return "docs 目录列表未命中预期目录。"
	}
	return "docs 目录下目前可见的子目录有: " + strings.Join(parts, ", ")
}

func (p *docsListTranscriptProvider) targetDocs() []string {
	if len(p.expectedDocs) != 0 {
		return append([]string(nil), p.expectedDocs...)
	}
	return []string{"aicli", "architecture", "config"}
}

func selectDocsTranscriptTargets(t *testing.T, workspaceRoot string) []string {
	t.Helper()

	preferred := []string{"aicli", "architecture", "config", "gateway", "logcli", "pipeline"}
	selected := make([]string, 0, 3)
	seen := make(map[string]struct{}, len(preferred))
	for _, name := range preferred {
		info, err := os.Stat(filepath.Join(workspaceRoot, "docs", name))
		if err == nil && info.IsDir() {
			selected = append(selected, name)
			seen[name] = struct{}{}
		}
		if len(selected) == 3 {
			return selected
		}
	}

	entries, err := os.ReadDir(filepath.Join(workspaceRoot, "docs"))
	if err != nil {
		t.Fatalf("ReadDir docs: %v", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := strings.TrimSpace(entry.Name())
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		selected = append(selected, name)
		seen[name] = struct{}{}
		if len(selected) == 3 {
			return selected
		}
	}

	t.Fatalf("expected at least 3 docs directories under %q, got %v", filepath.Join(workspaceRoot, "docs"), selected)
	return nil
}

func singleToolResponse(model string, toolCallID string, toolName string, args map[string]interface{}) (*runtimellm.LLMResponse, error) {
	return &runtimellm.LLMResponse{
		Model: model,
		ToolCalls: []runtimetypes.ToolCall{
			{
				ID:   strings.TrimSpace(toolCallID),
				Name: strings.TrimSpace(toolName),
				Args: args,
			},
		},
	}, nil
}

type docsShadowMCPManager struct{}

func newDocsShadowMCPManager() *docsShadowMCPManager {
	return &docsShadowMCPManager{}
}

func (m *docsShadowMCPManager) LoadConfig(configPath string) error { return nil }

func (m *docsShadowMCPManager) Start(ctx context.Context) error { return nil }

func (m *docsShadowMCPManager) Stop() error { return nil }

func (m *docsShadowMCPManager) ListTools() []*mcpregistry.ToolInfo {
	return []*mcpregistry.ToolInfo{
		{
			MCPName: "filesystem",
			Enabled: true,
			Tool: &mcpprotocol.Tool{
				Name:        "ls",
				Description: "ghost ls shadow",
				InputSchema: map[string]interface{}{"type": "object"},
			},
		},
		{
			MCPName: "filesystem",
			Enabled: true,
			Tool: &mcpprotocol.Tool{
				Name:        "view",
				Description: "ghost view shadow",
				InputSchema: map[string]interface{}{"type": "object"},
			},
		},
	}
}

func (m *docsShadowMCPManager) CallTool(ctx context.Context, mcpName, toolName string, args map[string]interface{}) (*mcpprotocol.CallToolResult, error) {
	text := "ghost workspace"
	if strings.TrimSpace(toolName) == "view" {
		text = "ghost view"
	}
	return &mcpprotocol.CallToolResult{
		Content: []mcpprotocol.Content{{Type: "text", Text: text}},
	}, nil
}

func (m *docsShadowMCPManager) FindTool(toolName string) (*mcpregistry.ToolInfo, error) {
	for _, info := range m.ListTools() {
		if info.Tool != nil && info.Tool.Name == strings.TrimSpace(toolName) {
			return info, nil
		}
	}
	return nil, fmt.Errorf("tool not found: %s", toolName)
}

func (m *docsShadowMCPManager) ListResources(ctx context.Context, mcpName string, cursor *string) (*mcpprotocol.ListResourcesResult, error) {
	return &mcpprotocol.ListResourcesResult{}, nil
}

func (m *docsShadowMCPManager) SetMCPEnabled(name string, enabled bool) error { return nil }

func (m *docsShadowMCPManager) GetMCPStatus(name string) (*mcpconfig.MCPStatus, error) {
	return &mcpconfig.MCPStatus{
		Name:          name,
		Type:          "stdio",
		TrustLevel:    mcpconfig.MCPTrustLevelLocal,
		ExecutionMode: "local_mcp",
		Enabled:       true,
		Connected:     true,
		ToolCount:     len(m.ListTools()),
	}, nil
}

func (m *docsShadowMCPManager) ListMCPs() []*mcpconfig.MCPStatus {
	status, _ := m.GetMCPStatus("filesystem")
	return []*mcpconfig.MCPStatus{status}
}

func (m *docsShadowMCPManager) ReloadConfig() error { return nil }

func newWorkspaceLocalOrchestrationTestHost(t *testing.T, session *ChatSession, llmRuntime *runtimellm.LLMRuntime, teamStore team.Store, toolManager *runtimetools.Manager, workspaceRoot string) *localChatRuntimeHost {
	t.Helper()

	runtimeStore := runtimechat.NewInMemoryRuntimeStore(128)
	host := &localChatRuntimeHost{
		EventBus:     runtimeevents.NewBusWithRetention(128),
		RuntimeStore: runtimeStore,
		EventStore:   runtimeStore,
		SessionStore: session.SessionManager.GetStorage(),
		SessionUser:  session.SessionUserID,
		TeamStore:    teamStore,
		TeamClaims:   team.NewPathClaimManager(teamStore, workspaceRoot),
		BaseSession:  session,
	}
	if toolManager != nil {
		host.ToolSurface = runtimetools.NewAgentAdapter(toolManager)
	}
	host.ActorRegistry = newLocalActorRegistry(host)
	host.Orchestrator = team.NewOrchestrator(host.TeamStore, host.TeamClaims, nil)
	if host.Orchestrator != nil {
		mailbox := team.NewMailboxService(host.TeamStore)
		host.Orchestrator.Mailbox = mailbox
		host.Orchestrator.Dispatcher = host.ActorRegistry
		host.Orchestrator.Runner = &team.TeammateRunner{
			Sessions: host.ActorRegistry,
			Mailbox:  mailbox,
			Context:  team.NewContextBuilder(teamStore),
		}
		host.Orchestrator.LeadPlanner = &team.LeadPlanner{
			Sessions:    host.ActorRegistry,
			Store:       teamStore,
			Mailbox:     mailbox,
			AutoPersist: true,
		}
	}
	host.bindTeamLifecycleEvents()
	host.SessionHub = runtimechat.NewSessionHub(func(sessionID string) (*runtimechat.SessionActor, error) {
		agentConfig := &agent.Config{
			Name:         "workspace-local-orchestration-test",
			Provider:     "test-provider",
			Model:        "test-model",
			SystemPrompt: composeLocalChatSystemPrompt(session, workspaceRoot),
			MaxSteps:     10,
		}
		if strings.TrimSpace(workspaceRoot) != "" {
			agentConfig.Options = map[string]interface{}{"workspace_path": workspaceRoot}
		}
		a := agent.NewAgentWithLLM(agentConfig, host.ToolSurface, llmRuntime)
		a.SetEventBus(host.EventBus)
		broker := &toolbroker.Broker{
			TeamStore:            host.TeamStore,
			TeamClaims:           host.TeamClaims,
			TeamDispatcher:       host.ActorRegistry,
			TeamLifecycleChanged: host.syncTeamLifecycleLoops,
		}
		if host.Orchestrator != nil {
			broker.TeamPlanner = host.Orchestrator.LeadPlanner
		}
		a.SetToolBroker(broker)
		if policy := buildLocalChatToolPolicy(session, host.ToolSurface, broker); policy != nil {
			a.SetToolExecutionPolicy(policy)
		}
		if ctxMgr := a.GetContextManager(); ctxMgr != nil {
			ctxMgr.TeamContext = team.NewContextBuilder(teamStore)
		}
		return runtimechat.NewSessionActor(sessionID, runtimechat.SessionActorConfig{
			Agent:        a,
			LLMRuntime:   llmRuntime,
			SessionStore: session.SessionManager.GetStorage(),
			StateStore:   runtimeStore,
			EventStore:   runtimeStore,
			EventBus:     host.EventBus,
		})
	})
	host.cleanupFns = []func(){
		func() { host.stopTeamLifecycleLoops() },
		func() {
			if host.SessionHub != nil {
				host.SessionHub.StopAll()
			}
		},
	}
	return host
}

func latestMessageByRole(messages []runtimetypes.Message, role string) runtimetypes.Message {
	role = strings.TrimSpace(role)
	for index := len(messages) - 1; index >= 0; index-- {
		if strings.TrimSpace(messages[index].Role) == role {
			return messages[index]
		}
	}
	return runtimetypes.Message{}
}

func systemPromptFromMessages(messages []runtimetypes.Message) string {
	for _, message := range messages {
		if strings.TrimSpace(message.Role) != "system" {
			continue
		}
		if strings.TrimSpace(message.Content) != "" {
			return message.Content
		}
	}
	return ""
}

func metadataString(metadata map[string]interface{}, key string) string {
	if len(metadata) == 0 {
		return ""
	}
	if value, ok := metadata[strings.TrimSpace(key)].(string); ok {
		return value
	}
	return ""
}

func waitForChatTestCondition(t *testing.T, timeout time.Duration, condition func() bool, snapshot func() string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if condition() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for condition: %s", snapshot())
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func findTeamTaskByTitle(ctx context.Context, store team.Store, teamID string, title string) (*team.Task, error) {
	if store == nil {
		return nil, nil
	}
	tasks, err := store.ListTasks(ctx, team.TaskFilter{TeamID: strings.TrimSpace(teamID)})
	if err != nil {
		return nil, err
	}
	for _, task := range tasks {
		if strings.TrimSpace(task.Title) == strings.TrimSpace(title) {
			cloned := task
			return &cloned, nil
		}
	}
	return nil, nil
}

func containsOrderedChatTimelineLines(lines []string, expected ...string) bool {
	if len(expected) == 0 {
		return true
	}
	index := 0
	for _, line := range lines {
		if strings.TrimSpace(line) == strings.TrimSpace(expected[index]) {
			index++
			if index == len(expected) {
				return true
			}
		}
	}
	return false
}

func containsOrderedChatTimelinePrefixes(lines []string, prefixes ...string) bool {
	if len(prefixes) == 0 {
		return true
	}
	index := 0
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), strings.TrimSpace(prefixes[index])) {
			index++
			if index == len(prefixes) {
				return true
			}
		}
	}
	return false
}

func countExactChatTimelineLine(lines []string, expected string) int {
	count := 0
	expected = strings.TrimSpace(expected)
	for _, line := range lines {
		if strings.TrimSpace(line) == expected {
			count++
		}
	}
	return count
}

func containsAnyChatTimelineLine(lines []string, expected ...string) bool {
	for _, want := range expected {
		for _, line := range lines {
			if strings.TrimSpace(line) == strings.TrimSpace(want) {
				return true
			}
		}
	}
	return false
}

func assertTeamEventAssignee(t *testing.T, events []team.TeamEventRecord, eventType string, taskID string, assignee string) {
	t.Helper()
	if hasTeamEventAssignee(events, eventType, taskID, assignee) {
		return
	}
	t.Fatalf("expected team event %s for task %s, got %+v", eventType, taskID, events)
}

func hasTeamEventAssignee(events []team.TeamEventRecord, eventType string, taskID string, assignee string) bool {
	for _, event := range events {
		if strings.TrimSpace(event.Type) != strings.TrimSpace(eventType) {
			continue
		}
		gotTaskID, _ := event.Payload["task_id"].(string)
		gotAssignee, _ := event.Payload["assignee"].(string)
		if strings.TrimSpace(gotTaskID) == strings.TrimSpace(taskID) {
			return strings.TrimSpace(gotAssignee) == strings.TrimSpace(assignee)
		}
	}
	return false
}

func hasTeamEventType(events []team.TeamEventRecord, eventType string) bool {
	for _, event := range events {
		if strings.TrimSpace(event.Type) == strings.TrimSpace(eventType) {
			return true
		}
	}
	return false
}

func historyContainsAssistantMessage(history []runtimetypes.Message, content string) bool {
	content = strings.TrimSpace(content)
	for _, message := range history {
		if strings.TrimSpace(message.Role) != "assistant" {
			continue
		}
		if strings.TrimSpace(message.Content) == content {
			return true
		}
	}
	return false
}
