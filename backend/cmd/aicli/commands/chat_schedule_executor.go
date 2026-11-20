package commands

import (
	"context"
	"fmt"
	"strings"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/runtimehost"
	"github.com/wwsheng009/ai-agent-runtime/internal/scheduleexec"
)

// aicliScheduleTurnExecutor adapts the local actor registry to the narrow
// scheduleexec.TurnExecutor seam. It is created only after the local runtime
// host has been attached, so the aicli scheduler never advertises execution
// capability before the shared actor core is available.
type aicliScheduleTurnExecutor struct {
	host *localChatRuntimeHost
}

var _ runtimehost.TurnWaiter = (*aicliScheduleTurnExecutor)(nil)

func (e *aicliScheduleTurnExecutor) SubmitTurnWait(ctx context.Context, req runtimehost.TurnRequest) (runtimehost.TurnResult, error) {
	return e.SubmitTurn(ctx, req)
}

func (e *aicliScheduleTurnExecutor) SubmitTurn(ctx context.Context, req runtimehost.TurnRequest) (runtimehost.TurnResult, error) {
	result := runtimehost.TurnResult{SessionID: strings.TrimSpace(req.SessionID)}
	if e == nil || e.host == nil {
		return result, fmt.Errorf("aicli schedule: local runtime host is not configured")
	}
	if e.host.ActorRegistry == nil {
		return result, fmt.Errorf("aicli schedule: local actor registry is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	sessionResult, err := e.host.ActorRegistry.SubmitPromptWithRoute(
		ctx,
		req.SessionID,
		req.Prompt,
		req.RunMeta,
		scheduleRouteOverride(req),
	)
	if err != nil {
		return result, err
	}
	if sessionResult == nil {
		return result, fmt.Errorf("aicli schedule: session result is nil")
	}
	result.Completed = true
	result.Output = sessionResult.Output
	result.Success = sessionResult.Success
	result.Error = sessionResult.Error
	return result, nil
}

func scheduleRouteOverride(req runtimehost.TurnRequest) *runtimechat.RunRouteOverride {
	provider := strings.TrimSpace(req.Provider)
	model := strings.TrimSpace(req.Model)
	effort := strings.TrimSpace(req.ReasoningEffort)
	if provider == "" && model == "" && effort == "" {
		return nil
	}
	return &runtimechat.RunRouteOverride{
		Provider:        provider,
		Model:           model,
		ReasoningEffort: effort,
	}
}

func newAICLIScheduleExecutor(
	runtimeCfg *config.RuntimeConfig,
	session *ChatSession,
	host *localChatRuntimeHost,
) (*scheduleexec.Executor, error) {
	if session == nil || session.SessionManager == nil {
		return nil, fmt.Errorf("aicli schedule: session manager is not configured")
	}
	if host == nil {
		return nil, fmt.Errorf("aicli schedule: local runtime host is not configured")
	}
	profile := ""
	if runtimeCfg != nil {
		profile = runtimeCfg.Schedule.WithDefaults().Execution.Profile
	}
	return scheduleexec.New(scheduleexec.Options{
		Executor:       &aicliScheduleTurnExecutor{host: host},
		Sessions:       session.SessionManager,
		Profile:        profile,
		HeadlessUserID: "scheduled-aicli",
	})
}
