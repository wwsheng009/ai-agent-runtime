package commands

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	runtimeagent "github.com/wwsheng009/ai-agent-runtime/internal/agent"
	runtimegoal "github.com/wwsheng009/ai-agent-runtime/internal/goal"
	runtimellm "github.com/wwsheng009/ai-agent-runtime/internal/llm"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestChatToolAvailable_GoalToolSharedPath(t *testing.T) {
	session, cleanup := newGoalCommandTestSession(t)
	defer cleanup()
	session.ChatExecutor = newAICLISharedChatExecutor()
	session.FunctionCatalog = newAICLIFunctionCatalog("openai", nil)
	registerGoalFunctions(session)

	if !chatToolAvailable(session, getGoalFunctionName) {
		t.Fatal("expected shared path to expose get_goal")
	}
	if !chatToolAvailable(session, updateGoalFunctionName) {
		t.Fatal("expected shared path to expose update_goal")
	}
}

func TestChatToolAvailable_GoalToolActorPath(t *testing.T) {
	session, cleanup := newGoalCommandTestSession(t)
	defer cleanup()
	session.ChatExecutor = newAICLIActorChatExecutor()
	session.LocalRuntimeHost = &localChatRuntimeHost{
		ToolSurface: wrapGoalToolSurface(session, nil),
	}

	if !chatToolAvailable(session, getGoalFunctionName) {
		t.Fatal("expected actor path to expose get_goal")
	}
	if !chatToolAvailable(session, updateGoalFunctionName) {
		t.Fatal("expected actor path to expose update_goal")
	}
}

func TestChatToolAvailable_GoalToolRuntimeServerUnsupported(t *testing.T) {
	session, cleanup := newGoalCommandTestSession(t)
	defer cleanup()
	session.ChatExecutor = newAICLIRuntimeServerChatExecutor("http://127.0.0.1:1")
	session.FunctionCatalog = newAICLIFunctionCatalog("openai", nil)
	registerGoalFunctions(session)
	session.LocalRuntimeHost = &localChatRuntimeHost{
		ToolSurface: wrapGoalToolSurface(session, nil),
	}

	if chatToolAvailable(session, updateGoalFunctionName) {
		t.Fatal("expected runtime-server path to keep update_goal unsupported")
	}

	captureStdout(t, func() {
		handleCommand(session, "/goal runtime server unsupported guidance", false)
	})
	prompt := composeChatSystemPromptWithGuidance(session)
	if strings.Contains(prompt, "call update_goal") {
		t.Fatalf("did not expect runtime-server guidance to ask for update_goal, got %q", prompt)
	}
	if !strings.Contains(prompt, "Do not claim to have updated goal state unless the update_goal tool is available and has succeeded") {
		t.Fatalf("expected unsupported guidance to avoid false completion claims, got %q", prompt)
	}
}

func TestChatToolAvailable_DisabledTools(t *testing.T) {
	session, cleanup := newGoalCommandTestSession(t)
	defer cleanup()
	session.DisableTools = true
	session.ChatExecutor = newAICLISharedChatExecutor()
	session.FunctionCatalog = newAICLIFunctionCatalog("openai", nil)
	registerGoalFunctions(session)

	if chatToolAvailable(session, updateGoalFunctionName) {
		t.Fatal("expected disabled tools to hide update_goal")
	}
}

func TestChatToolAvailable_RespectsToolPolicy(t *testing.T) {
	session, cleanup := newGoalCommandTestSession(t)
	defer cleanup()
	session.ChatExecutor = newAICLIActorChatExecutor()
	session.LocalRuntimeHost = &localChatRuntimeHost{
		ToolSurface: wrapGoalToolSurface(session, nil),
	}
	session.ToolPolicy = runtimepolicy.NewToolExecutionPolicy([]string{getGoalFunctionName}, false)

	if !chatToolAvailable(session, getGoalFunctionName) {
		t.Fatal("expected allowlisted get_goal to be available")
	}
	if chatToolAvailable(session, updateGoalFunctionName) {
		t.Fatal("expected non-allowlisted update_goal to be unavailable")
	}

	captureStdout(t, func() {
		handleCommand(session, "/goal policy gated guidance", false)
	})
	prompt := composeChatSystemPromptWithGuidance(session)
	if strings.Contains(prompt, "call update_goal") {
		t.Fatalf("did not expect update_goal guidance when policy blocks it, got %q", prompt)
	}
}

func TestActorGoalToolSurfaceAppearsInProviderRequestMetadata(t *testing.T) {
	session, cleanup := newGoalCommandTestSession(t)
	defer cleanup()
	captureStdout(t, func() {
		handleCommand(session, "/goal provider request metadata", false)
	})

	provider := &capturingGoalLLMProvider{
		name: "test-provider",
		responses: []*runtimellm.LLMResponse{{
			Content: "done",
			Model:   "test-model",
		}},
	}
	llmRuntime := runtimellm.NewLLMRuntime(nil)
	if err := llmRuntime.RegisterProvider("test-provider", provider); err != nil {
		t.Fatalf("register provider failed: %v", err)
	}
	apiAgent := runtimeagent.NewAgentWithLLM(&runtimeagent.Config{
		Name:         "aicli-chat",
		Provider:     "test-provider",
		Model:        "test-model",
		MaxSteps:     1,
		SystemPrompt: composeChatSystemPromptWithGuidance(session),
	}, wrapGoalToolSurface(session, nil), llmRuntime)
	loop := runtimeagent.NewReActLoop(apiAgent, llmRuntime, &runtimeagent.LoopReActConfig{
		MaxSteps:        1,
		EnableThought:   true,
		EnableToolCalls: true,
	})

	result, err := loop.Run(context.Background(), "inspect goal tools")
	if err != nil {
		t.Fatalf("actor loop failed: %v", err)
	}
	if result == nil || !result.Success {
		t.Fatalf("expected successful actor loop result, got %+v", result)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("expected one provider request, got %d", len(provider.requests))
	}
	request := provider.requests[0]
	if got := request.Metadata["executor_path"]; got != "actor" {
		t.Fatalf("expected actor executor_path metadata, got %#v", got)
	}
	if !toolDefinitionsContain(request.Tools, getGoalFunctionName) || !toolDefinitionsContain(request.Tools, updateGoalFunctionName) {
		t.Fatalf("expected provider request tools to include goal tools, got %v", toolDefinitionNamesForTest(request.Tools))
	}
	surface, ok := request.Metadata["tool_surface"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected tool_surface metadata, got %#v", request.Metadata["tool_surface"])
	}
	names, ok := surface["names"].([]string)
	if !ok {
		t.Fatalf("expected tool_surface.names []string, got %#v", surface["names"])
	}
	if !stringSliceContains(names, getGoalFunctionName) || !stringSliceContains(names, updateGoalFunctionName) {
		t.Fatalf("expected tool_surface.names to include goal tools, got %v", names)
	}
}

func TestGoalCapabilitySharedActorSchemaParity(t *testing.T) {
	session, cleanup := newGoalCommandTestSession(t)
	defer cleanup()
	session.ChatExecutor = newAICLISharedChatExecutor()
	session.FunctionCatalog = newAICLIFunctionCatalog("openai", nil)
	registerGoalFunctions(session)
	actorSurface := wrapGoalToolSurface(session, nil)

	for _, name := range []string{getGoalFunctionName, updateGoalFunctionName} {
		sharedFn, ok := session.FunctionCatalog.Registry().Get(name)
		if !ok {
			t.Fatalf("expected shared function %q", name)
		}
		actorInfo, err := actorSurface.FindTool(name)
		if err != nil {
			t.Fatalf("expected actor tool %q: %v", name, err)
		}
		if sharedFn.Name() != actorInfo.Name || sharedFn.Description() != actorInfo.Description {
			t.Fatalf("schema identity mismatch for %q: shared=%q/%q actor=%q/%q", name, sharedFn.Name(), sharedFn.Description(), actorInfo.Name, actorInfo.Description)
		}
		if !reflect.DeepEqual(sharedFn.Parameters(), actorInfo.InputSchema) {
			t.Fatalf("parameter schema mismatch for %q:\nshared=%#v\nactor=%#v", name, sharedFn.Parameters(), actorInfo.InputSchema)
		}
		metadataProvider, ok := sharedFn.(interface {
			DefinitionMetadata() map[string]interface{}
		})
		if !ok {
			t.Fatalf("expected shared function %q to expose definition metadata", name)
		}
		if !reflect.DeepEqual(metadataProvider.DefinitionMetadata(), actorInfo.Metadata) {
			t.Fatalf("metadata mismatch for %q: shared=%#v actor=%#v", name, metadataProvider.DefinitionMetadata(), actorInfo.Metadata)
		}
	}
}

func TestPostTurnReconcilersAndSync_PersistsGoalCompletion(t *testing.T) {
	session, cleanup := newGoalCommandTestSession(t)
	defer cleanup()
	captureStdout(t, func() {
		handleCommand(session, "/goal reconcile and sync", false)
	})
	goal, ok, err := runtimegoal.NewMetadataStore().Get(session.RuntimeSession)
	if err != nil || !ok || goal == nil {
		t.Fatalf("expected active goal, ok=%v err=%v", ok, err)
	}
	session.Messages = append(session.Messages, *runtimetypes.NewToolMessage("call-1", marshalIndentedJSON(map[string]interface{}{
		"goal": map[string]interface{}{
			"goal_id":            goal.GoalID,
			"session_id":         goal.SessionID,
			"objective":          goal.Objective,
			"status":             string(runtimegoal.StatusComplete),
			"completed_by":       "model",
			"completion_summary": "done",
			"created_at":         goal.CreatedAt,
			"updated_at":         goal.UpdatedAt,
		},
	})))

	if err := runPostTurnReconcilersAndSync(session); err != nil {
		t.Fatalf("post-turn reconcile failed: %v", err)
	}
	loaded, err := session.SessionManager.Get(context.Background(), session.RuntimeSession.ID)
	if err != nil {
		t.Fatalf("failed to load session: %v", err)
	}
	persistedGoal, ok, err := runtimegoal.NewMetadataStore().Get(loaded)
	if err != nil || !ok || persistedGoal == nil {
		t.Fatalf("expected persisted goal, ok=%v err=%v", ok, err)
	}
	if persistedGoal.Status != runtimegoal.StatusComplete || persistedGoal.CompletedBy != "model" || persistedGoal.CompletionSummary != "done" {
		t.Fatalf("unexpected persisted goal: %+v", persistedGoal)
	}
}

func TestGoalToolCompletionSurvivesStaleActorSessionSave(t *testing.T) {
	session, cleanup := newGoalCommandTestSession(t)
	defer cleanup()
	captureStdout(t, func() {
		handleCommand(session, "/goal survive stale actor save", false)
	})
	staleActive := session.RuntimeSession.Clone()

	toolOutput, err := executeUpdateGoalFunction(session, map[string]interface{}{
		"status":  "complete",
		"summary": "completed before stale save",
	})
	if err != nil {
		t.Fatalf("update_goal failed: %v", err)
	}
	if err := session.SessionManager.GetStorage().Save(context.Background(), staleActive); err != nil {
		t.Fatalf("failed to save stale active session: %v", err)
	}
	if err := restoreChatStateFromRuntimeSession(session, staleActive); err != nil {
		t.Fatalf("failed to restore stale active session: %v", err)
	}
	session.Messages = append(session.Messages, *runtimetypes.NewToolMessage("call-1", toolOutput))

	if err := runPostTurnReconcilersAndSync(session); err != nil {
		t.Fatalf("post-turn reconcile failed: %v", err)
	}
	loaded, err := session.SessionManager.Get(context.Background(), session.RuntimeSession.ID)
	if err != nil {
		t.Fatalf("failed to load session: %v", err)
	}
	goal, ok, err := runtimegoal.NewMetadataStore().Get(loaded)
	if err != nil || !ok || goal == nil {
		t.Fatalf("expected reconciled goal, ok=%v err=%v", ok, err)
	}
	if goal.Status != runtimegoal.StatusComplete || goal.CompletedBy != "model" || goal.CompletionSummary != "completed before stale save" {
		t.Fatalf("expected completed goal after stale save reconcile, got %+v", goal)
	}
}

func TestLocalGoalPersistHookPreservesLatestCompletedGoal(t *testing.T) {
	session, cleanup := newGoalCommandTestSession(t)
	defer cleanup()
	captureStdout(t, func() {
		handleCommand(session, "/goal preserve complete before stale save", false)
	})
	staleActive := session.RuntimeSession.Clone()
	if _, err := executeUpdateGoalFunction(session, map[string]interface{}{
		"status":  "complete",
		"summary": "completed before actor final save",
	}); err != nil {
		t.Fatalf("update_goal failed: %v", err)
	}

	hook := localGoalPersistHook(session.SessionManager.GetStorage())
	if hook == nil {
		t.Fatal("expected local goal persist hook")
	}
	prepared, err := hook(context.Background(), staleActive)
	if err != nil {
		t.Fatalf("persist hook failed: %v", err)
	}
	goal, ok, err := runtimegoal.NewMetadataStore().Get(prepared)
	if err != nil || !ok || goal == nil {
		t.Fatalf("expected preserved goal, ok=%v err=%v", ok, err)
	}
	if goal.Status != runtimegoal.StatusComplete || goal.CompletedBy != "model" || goal.CompletionSummary != "completed before actor final save" {
		t.Fatalf("expected completed goal to be preserved, got %+v", goal)
	}
}

func TestFormatPostTurnReconcileDebugIncludesNamesAndErrors(t *testing.T) {
	debug := formatPostTurnReconcileDebug(
		[]string{"goal_completion_from_tool_messages"},
		[]string{"goal_completion_from_tool_messages"},
		[]string{"goal_completion_from_tool_messages: failed"},
	)
	for _, want := range []string{"[post-turn-reconcile]", `"names"`, `"changed"`, `"errors"`, "goal_completion_from_tool_messages"} {
		if !strings.Contains(debug, want) {
			t.Fatalf("expected debug output to contain %q, got %q", want, debug)
		}
	}
}

func TestPostTurnReconcilersAndSync_PersistsChangesBeforeReturningError(t *testing.T) {
	session, cleanup := newGoalCommandTestSession(t)
	defer cleanup()
	previous := chatPostTurnReconcilers
	t.Cleanup(func() { chatPostTurnReconcilers = previous })
	chatPostTurnReconcilers = []chatPostTurnReconciler{
		{
			Name: "test_changed_error",
			Run: func(session *ChatSession) (bool, error) {
				session.RuntimeSession.SetContext("post_turn_sync_test", "persisted")
				return true, errors.New("intentional reconcile error")
			},
		},
	}

	err := runPostTurnReconcilersAndSync(session)
	if err == nil || !strings.Contains(err.Error(), "intentional reconcile error") {
		t.Fatalf("expected reconciler error to be returned after sync, got %v", err)
	}
	loaded, loadErr := session.SessionManager.Get(context.Background(), session.RuntimeSession.ID)
	if loadErr != nil {
		t.Fatalf("failed to load session: %v", loadErr)
	}
	if got, ok := loaded.GetContext("post_turn_sync_test"); !ok || got != "persisted" {
		t.Fatalf("expected changed metadata to persist despite reconciler error, got ok=%v value=%#v", ok, got)
	}
}

type capturingGoalLLMProvider struct {
	name      string
	responses []*runtimellm.LLMResponse
	requests  []*runtimellm.LLMRequest
}

func (p *capturingGoalLLMProvider) Name() string {
	return p.name
}

func (p *capturingGoalLLMProvider) Call(ctx context.Context, req *runtimellm.LLMRequest) (*runtimellm.LLMResponse, error) {
	p.requests = append(p.requests, cloneGoalLLMRequest(req))
	if len(p.responses) == 0 {
		return &runtimellm.LLMResponse{Content: "done", Model: "test-model"}, nil
	}
	response := p.responses[0]
	p.responses = p.responses[1:]
	return response, nil
}

func (p *capturingGoalLLMProvider) Stream(ctx context.Context, req *runtimellm.LLMRequest) (<-chan runtimellm.StreamChunk, error) {
	p.requests = append(p.requests, cloneGoalLLMRequest(req))
	ch := make(chan runtimellm.StreamChunk, 2)
	go func() {
		defer close(ch)
		ch <- runtimellm.StreamChunk{Type: runtimellm.EventTypeText, Content: "done"}
		ch <- runtimellm.StreamChunk{Type: runtimellm.EventTypeDone, Done: true}
	}()
	return ch, nil
}

func (p *capturingGoalLLMProvider) CountTokens(text string) int {
	return len(text) / 4
}

func (p *capturingGoalLLMProvider) GetCapabilities() *runtimellm.ModelCapabilities {
	return &runtimellm.ModelCapabilities{
		MaxContextTokens:  128000,
		MaxOutputTokens:   4096,
		SupportsTools:     true,
		SupportsStreaming: true,
		SupportsJSONMode:  true,
	}
}

func (p *capturingGoalLLMProvider) CheckHealth(ctx context.Context) error {
	return nil
}

func cloneGoalLLMRequest(req *runtimellm.LLMRequest) *runtimellm.LLMRequest {
	if req == nil {
		return nil
	}
	cloned := *req
	cloned.Messages = append([]runtimetypes.Message(nil), req.Messages...)
	cloned.Tools = append([]runtimetypes.ToolDefinition(nil), req.Tools...)
	if req.Metadata != nil {
		cloned.Metadata = make(map[string]interface{}, len(req.Metadata))
		for key, value := range req.Metadata {
			cloned.Metadata[key] = value
		}
	}
	return &cloned
}

func toolDefinitionsContain(tools []runtimetypes.ToolDefinition, name string) bool {
	for _, tool := range tools {
		if tool.Name == name {
			return true
		}
	}
	return false
}

func toolDefinitionNamesForTest(tools []runtimetypes.ToolDefinition) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	return names
}

func stringSliceContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func TestUpdateGoalNegativePaths(t *testing.T) {
	session, cleanup := newGoalCommandTestSession(t)
	defer cleanup()

	if _, err := executeUpdateGoalFunction(session, map[string]interface{}{
		"status":  "complete",
		"summary": "no active goal",
	}); err == nil || !strings.Contains(err.Error(), "no goal") {
		t.Fatalf("expected no goal error, got %v", err)
	}

	captureStdout(t, func() {
		handleCommand(session, "/goal negative path", false)
	})
	if _, err := executeUpdateGoalFunction(session, map[string]interface{}{
		"status":  "paused",
		"summary": "invalid status",
	}); err == nil {
		t.Fatal("expected non-complete status to fail")
	}
	if _, err := executeUpdateGoalFunction(session, map[string]interface{}{
		"status":  "complete",
		"summary": "   ",
	}); err == nil {
		t.Fatal("expected empty summary to fail")
	}
	captureStdout(t, func() {
		handleCommand(session, "/goal pause", false)
	})
	if _, err := executeUpdateGoalFunction(session, map[string]interface{}{
		"status":  "complete",
		"summary": "paused goal",
	}); err == nil {
		t.Fatal("expected paused goal completion by model to fail")
	}
}
