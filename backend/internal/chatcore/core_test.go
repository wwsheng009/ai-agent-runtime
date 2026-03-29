package chatcore

import (
	"context"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

type fakeAgentExecutor struct {
	config              *agent.Config
	reactResult         *agent.Result
	orchestrationResult *agent.OrchestrationResult
	runReActCalled      bool
	orchestrateCalled   bool
	capturedPrompt      string
	capturedLoopConfig  *agent.LoopReActConfig
	capturedSession     agent.HistorySession
	capturedOrchReq     *agent.OrchestrationRequest
}

func (f *fakeAgentExecutor) GetConfig() *agent.Config { return f.config }

func (f *fakeAgentExecutor) RunReActWithSession(ctx context.Context, llmRuntime *llm.LLMRuntime, prompt string, session agent.HistorySession, loopConfig *agent.LoopReActConfig) (*agent.Result, error) {
	f.runReActCalled = true
	f.capturedPrompt = prompt
	f.capturedLoopConfig = loopConfig
	f.capturedSession = session

	messages := append(session.GetMessages(), *types.NewAssistantMessage("react done"))
	session.ReplaceHistory(messages)
	return f.reactResult, nil
}

func (f *fakeAgentExecutor) Orchestrate(ctx context.Context, req *agent.OrchestrationRequest) (*agent.OrchestrationResult, error) {
	f.orchestrateCalled = true
	f.capturedOrchReq = req
	return f.orchestrationResult, nil
}

func TestExecuteNonStream_ReActUsesPreparedHistoryAndUpdatesSession(t *testing.T) {
	executor := &fakeAgentExecutor{
		config: &agent.Config{Name: "api-agent", MaxSteps: 4},
		reactResult: &agent.Result{
			Success: true,
			Output:  "react done",
			Steps:   2,
		},
	}
	baseSession := chat.NewSession("user-1")
	baseSession.AddMessage(*types.NewAssistantMessage("old message"))

	preparedHistory := []types.Message{
		*types.NewSystemMessage("system prompt"),
		*types.NewAssistantMessage("history seed"),
	}

	result, err := ExecuteNonStream(context.Background(), ExecuteRequest{
		Agent:           executor,
		LLMRuntime:      llm.NewLLMRuntime(nil),
		Session:         baseSession,
		SessionUserID:   "user-1",
		Prompt:          "analyze this repo",
		PreparedHistory: preparedHistory,
		EnableReAct:     true,
		ReActConfig: &agent.LoopReActConfig{
			MaxSteps:        4,
			EnableThought:   true,
			EnableToolCalls: true,
		},
	})
	if err != nil {
		t.Fatalf("ExecuteNonStream failed: %v", err)
	}
	if !executor.runReActCalled {
		t.Fatal("expected RunReActWithSession to be called")
	}
	if executor.orchestrateCalled {
		t.Fatal("did not expect Orchestrate to be called")
	}
	if executor.capturedPrompt != "analyze this repo" {
		t.Fatalf("unexpected prompt: %s", executor.capturedPrompt)
	}
	if result.ReactResult == nil || result.ReactResult.Output != "react done" {
		t.Fatalf("unexpected react result: %+v", result.ReactResult)
	}
	if result.UpdatedSession == nil {
		t.Fatal("expected updated session")
	}
	messages := result.UpdatedSession.GetMessages()
	if len(messages) != 4 {
		t.Fatalf("expected 4 session messages, got %d: %#v", len(messages), messages)
	}
	if messages[0].Role != "system" || messages[0].Content != "system prompt" {
		t.Fatalf("unexpected prepared history in session: %#v", messages)
	}
	if messages[2].Role != "user" || messages[2].Content != "analyze this repo" {
		t.Fatalf("expected user prompt in session history: %#v", messages)
	}
	if messages[3].Role != "assistant" || messages[3].Content != "react done" {
		t.Fatalf("expected assistant output appended: %#v", messages)
	}
}

func TestExecuteNonStream_OrchestrateForwardsRequest(t *testing.T) {
	executor := &fakeAgentExecutor{
		config: &agent.Config{Name: "api-agent", MaxSteps: 4},
		orchestrationResult: &agent.OrchestrationResult{
			Source: "agent_route",
			Mode:   agent.OrchestrationRoutePreferred,
		},
	}
	orchReq := &agent.OrchestrationRequest{
		Prompt:   "route this",
		Provider: "CODEX_03",
		Model:    "gpt-5.2-codex",
	}

	result, err := ExecuteNonStream(context.Background(), ExecuteRequest{
		Agent:                executor,
		Prompt:               "route this",
		OrchestrationRequest: orchReq,
	})
	if err != nil {
		t.Fatalf("ExecuteNonStream failed: %v", err)
	}
	if executor.runReActCalled {
		t.Fatal("did not expect RunReActWithSession to be called")
	}
	if !executor.orchestrateCalled {
		t.Fatal("expected Orchestrate to be called")
	}
	if result.OrchestrationResult == nil || result.OrchestrationResult.Source != "agent_route" {
		t.Fatalf("unexpected orchestration result: %+v", result.OrchestrationResult)
	}
	if executor.capturedOrchReq != orchReq {
		t.Fatalf("expected orchestration request passthrough")
	}
}
