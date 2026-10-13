package runtimeapi

import (
	"context"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/runtimehost"
)

// handlerExecutor 把 runtimeapi.Handler 的既有执行路径适配为 runtimehost.Executor。
//
// E3.0：纯适配、**行为零变化**——内部仍走 getSessionHub / buildSessionActor /
// submitSessionPrompt，与 HTTP 路径同源。E3.2 起 Handler 将改为消费 Executor。
type handlerExecutor struct {
	handler *Handler
}

// Executor 返回会话执行核的稳定接口（E3.0：适配现有执行栈，行为零变化）。
// 适配器无状态，可安全重复调用。
func (h *Handler) Executor() runtimehost.Executor {
	if h == nil {
		return nil
	}
	return &handlerExecutor{handler: h}
}

func (e *handlerExecutor) actorFor(sessionID string) (*chat.SessionActor, error) {
	if e == nil || e.handler == nil {
		return nil, fmt.Errorf("runtimehost: handler not configured")
	}
	hub := e.handler.getSessionHub()
	if hub == nil {
		return nil, fmt.Errorf("runtimehost: session hub not configured")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, fmt.Errorf("runtimehost: session id is required")
	}
	return hub.GetOrCreate(sessionID)
}

// SubmitTurn 同步执行一次 turn，返回终态或"已接受未完成"中间态。
func (e *handlerExecutor) SubmitTurn(ctx context.Context, req runtimehost.TurnRequest) (runtimehost.TurnResult, error) {
	result := runtimehost.TurnResult{SessionID: strings.TrimSpace(req.SessionID)}
	actor, err := e.actorFor(req.SessionID)
	if err != nil {
		return result, err
	}
	submitResult, state, err, completed := submitSessionPrompt(actor, ctx, req.Prompt, req.RunMeta, submitOptionsForTurn(req)...)
	if err != nil {
		return result, err
	}
	result.Completed = completed
	result.State = state
	if submitResult != nil {
		result.Output = submitResult.Output
		result.Success = submitResult.Success
		result.Error = submitResult.Error
	}
	return result, nil
}

// SubmitAsync 提交后立即返回（"被 actor 接受"语义）。
func (e *handlerExecutor) SubmitAsync(ctx context.Context, req runtimehost.TurnRequest) error {
	actor, err := e.actorFor(req.SessionID)
	if err != nil {
		return err
	}
	return actor.SubmitPromptAsync(ctx, req.Prompt, req.RunMeta, submitOptionsForTurn(req)...)
}

// Hub 暴露底层 hub（过渡期访问器）。
func (e *handlerExecutor) Hub() *chat.SessionHub {
	if e == nil || e.handler == nil {
		return nil
	}
	return e.handler.getSessionHub()
}

// Close 停止 hub。注意：Handler 自身的生命周期仍由宿主负责；本方法供
// 把执行核当作独立资源管理的调用方使用（E3.0 尚无调用方）。
func (e *handlerExecutor) Close(context.Context) error {
	if e == nil || e.handler == nil {
		return nil
	}
	if hub := e.handler.getSessionHub(); hub != nil {
		hub.StopAll()
	}
	return nil
}

// submitOptionsForTurn 把图片路径转成 actor 提交选项（与 HTTP submit_prompt 同口径）。
// 注意：TurnRequest.Profile 在 E3.0 尚未生效（工具面收窄属 E3.1+），此处刻意忽略。
func submitOptionsForTurn(req runtimehost.TurnRequest) []chat.SubmitPromptOption {
	images := make([]string, 0, len(req.Images))
	for _, path := range req.Images {
		if trimmed := strings.TrimSpace(path); trimmed != "" {
			images = append(images, trimmed)
		}
	}
	if len(images) == 0 {
		return nil
	}
	return []chat.SubmitPromptOption{{
		ImagePaths:       images,
		ImageArtifactDir: RuntimeUploadRootDir(),
	}}
}
