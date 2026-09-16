package chat

import (
	"context"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/planmode"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
)

// SetPermissionMode 把运行中的会话切换到另一个「非 plan」权限模式。
//
// plan 模式拥有独立的持久生命周期（EnterPlanMode/ExitPlanMode + plan 文件路径），
// 因此目标模式为 plan 时必须走 plan 模式入口，本方法显式拒绝。
// 从 plan 模式切走时，这里会关闭持久 plan 状态（decision=quit），
// 避免会话继续对外宣称 plan 生效、但实际已不再施加 plan 写白名单。
func (a *SessionActor) SetPermissionMode(ctx context.Context, sessionID string, mode runtimepolicy.Mode) error {
	if a == nil {
		return fmt.Errorf("session actor is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if a.IsStopped() {
		return ErrSessionActorStopped
	}
	if err := a.ensurePlanModeSessionID(sessionID); err != nil {
		return err
	}
	normalized, ok := runtimepolicy.ParseMode(string(mode))
	if !ok {
		return fmt.Errorf("unsupported permission mode %q", mode)
	}
	if normalized == runtimepolicy.ModePlan {
		return fmt.Errorf("permission mode %s requires the plan mode entry point", runtimepolicy.ModePlan)
	}

	session, err := a.loadSession(ctx)
	if err != nil {
		return err
	}
	clearPlanStateForPermissionSwitch(session)

	a.applySessionPermissionMode(session, normalized)
	if err := a.persistSession(ctx, session); err != nil {
		return err
	}
	if engine := a.agentPermissionEngine(); engine != nil {
		engine.Mode = normalized
	}
	a.syncLivePermissionMode(ctx, string(normalized))
	return nil
}

// clearPlanStateForPermissionSwitch 在切离 plan 模式时收口持久 plan 状态。
func clearPlanStateForPermissionSwitch(session *Session) {
	if session == nil {
		return
	}
	state := planmode.Load(session)
	if !planmode.IsActive(state) {
		return
	}
	exited, err := planmode.Exit(state, planmode.ExitQuit, "permission mode switched")
	if err != nil {
		return
	}
	planmode.Save(session, exited)
}

// SetSessionPermissionMode 在无运行 actor 时把权限模式落到持久会话上下文。
//
// 与 SetPermissionMode 的区别：这里只做会话级写入（调用方负责 Update），
// 因此 plan 模式同样需要调用方走 plan 模式入口，此处显式拒绝。
func SetSessionPermissionMode(session *Session, mode runtimepolicy.Mode) (runtimepolicy.Mode, error) {
	normalized, ok := runtimepolicy.ParseMode(string(mode))
	if !ok {
		return runtimepolicy.ModeDefault, fmt.Errorf("unsupported permission mode %q", mode)
	}
	if normalized == runtimepolicy.ModePlan {
		return runtimepolicy.ModeDefault, fmt.Errorf("permission mode %s requires the plan mode entry point", runtimepolicy.ModePlan)
	}
	if session == nil {
		return runtimepolicy.ModeDefault, fmt.Errorf("session is required")
	}
	clearPlanStateForPermissionSwitch(session)
	text := strings.TrimSpace(string(normalized))
	session.SetContext(planModePermissionModeKey, text)
	session.SetContext(planModeRequestedPermissionModeKey, text)
	session.SetContext(planModeEffectivePermissionModeKey, text)
	return normalized, nil
}
