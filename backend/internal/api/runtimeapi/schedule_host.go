package runtimeapi

import (
	"context"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
	"github.com/wwsheng009/ai-agent-runtime/internal/schedule"
)

// scheduleSessionDeliverer 以进程内方式把兜底提醒投递进目标会话 actor
// （D1：宿主恢复后 drain pending）。提交语义为"被 actor 接受"
// （SubmitPromptAsync），不等待 turn 完成——与 HTTP delegate 的 2xx 回执口径一致。
type scheduleSessionDeliverer struct {
	handler *Handler
}

func (d scheduleSessionDeliverer) Deliver(ctx context.Context, sessionID, text string) error {
	if d.handler == nil {
		return fmt.Errorf("schedule: handler not configured")
	}
	hub := d.handler.getSessionHub()
	if hub == nil {
		return fmt.Errorf("schedule: session hub not configured")
	}
	actor, err := hub.GetOrCreate(strings.TrimSpace(sessionID))
	if err != nil {
		return fmt.Errorf("schedule: get session actor %s: %w", sessionID, err)
	}
	if err := actor.SubmitPromptAsync(ctx, text, nil); err != nil {
		return fmt.Errorf("schedule: submit prompt to %s: %w", sessionID, err)
	}
	return nil
}

// attachScheduleDrainRuntime 给共享管理服务注册宿主侧兜底消费运行时：
// 会话激活时 drainSchedulePending 经 Service.Runtime() 消费 daemon 在宿主
// 离线期间落库的 pending 提醒。只读 store + 进程内 deliverer，不启动定时器。
//
// 没有这一步时 Service.Runtime() 恒为 nil，drainSchedulePending 直接返回，
// daemon 落库的兜底提醒永远不会被宿主补投。
func (h *Handler) attachScheduleDrainRuntime(service *schedule.Service, store schedule.Store) {
	if h == nil || service == nil || store == nil {
		return
	}
	service.SetRuntime(schedule.NewRuntime(schedule.RuntimeOptions{
		Store:     store,
		Service:   service,
		Deliverer: scheduleSessionDeliverer{handler: h},
		Logf:      func(format string, args ...any) { logger.Warn(fmt.Sprintf(format, args...)) },
	}))
}
