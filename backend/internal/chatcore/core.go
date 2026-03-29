package chatcore

import (
	"context"
	"fmt"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// AgentExecutor is the minimal agent surface required by the shared chat core.
type AgentExecutor interface {
	GetConfig() *agent.Config
	RunReActWithSession(ctx context.Context, llmRuntime *llm.LLMRuntime, prompt string, session agent.HistorySession, loopConfig *agent.LoopReActConfig) (*agent.Result, error)
	Orchestrate(ctx context.Context, req *agent.OrchestrationRequest) (*agent.OrchestrationResult, error)
}

// ExecuteRequest describes the prepared inputs required for one non-stream execution turn.
type ExecuteRequest struct {
	Agent                AgentExecutor
	LLMRuntime           *llm.LLMRuntime
	Session              *chat.Session
	SessionUserID        string
	Prompt               string
	PreparedHistory      []types.Message
	EnableReAct          bool
	ReActConfig          *agent.LoopReActConfig
	OrchestrationRequest *agent.OrchestrationRequest
}

// ExecuteResult returns the raw runtime outcome plus any updated session state.
type ExecuteResult struct {
	UpdatedSession      *chat.Session
	ReactResult         *agent.Result
	OrchestrationResult *agent.OrchestrationResult
}

// ExecuteNonStream runs a prepared non-stream chat turn through either ReAct or orchestration.
func ExecuteNonStream(ctx context.Context, req ExecuteRequest) (*ExecuteResult, error) {
	if req.Agent == nil {
		return nil, fmt.Errorf("agent is required")
	}
	if req.EnableReAct {
		if req.LLMRuntime == nil {
			return nil, fmt.Errorf("llm runtime is required for react execution")
		}
		session := req.Session
		if session == nil {
			session = chat.NewSession(req.SessionUserID)
		}
		execSession := session.Clone()
		execSession.ReplaceHistory(req.PreparedHistory)
		execSession.AddMessage(*types.NewUserMessage(req.Prompt))

		reactResult, err := req.Agent.RunReActWithSession(ctx, req.LLMRuntime, req.Prompt, execSession, req.ReActConfig)
		if err != nil {
			return nil, err
		}
		return &ExecuteResult{
			UpdatedSession:      execSession,
			ReactResult:         reactResult,
			OrchestrationResult: nil,
		}, nil
	}

	if req.OrchestrationRequest == nil {
		return nil, fmt.Errorf("orchestration request is required when react is disabled")
	}

	orchResult, err := req.Agent.Orchestrate(ctx, req.OrchestrationRequest)
	if err != nil {
		return nil, err
	}
	return &ExecuteResult{
		UpdatedSession:      nil,
		ReactResult:         nil,
		OrchestrationResult: orchResult,
	}, nil
}
