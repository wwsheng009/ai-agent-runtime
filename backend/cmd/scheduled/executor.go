package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/api/runtimeapi"
	runtimebootstrap "github.com/wwsheng009/ai-agent-runtime/internal/bootstrap"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/runtimehost"
	"github.com/wwsheng009/ai-agent-runtime/internal/schedule"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

// headlessSessionUserID 是 daemon 为 headless 作业创建临时会话时使用的合成用户。
const headlessSessionUserID = "scheduled-daemon"

// scheduleExecutorAdapter 把 runtimehost.Executor 适配为 schedule.SessionExecutor /
// schedule.HeadlessExecutor（E3.3 内嵌执行 + J2 headless 执行）。
type scheduleExecutorAdapter struct {
	exec    runtimehost.Executor
	profile string
	// createSession 为 headless/workspace 目标创建临时会话（可空 → headless 不可用）。
	createSession func(ctx context.Context, userID string) (string, error)
	// archiveSession 在 headless turn 结束后归档临时会话（可空 → 不归档）。
	archiveSession func(ctx context.Context, sessionID string) error
}

var (
	_ schedule.SessionExecutor  = scheduleExecutorAdapter{}
	_ schedule.HeadlessExecutor = scheduleExecutorAdapter{}
)

// ExecuteTurn 内嵌执行一次绑定会话的提醒 turn。
func (a scheduleExecutorAdapter) ExecuteTurn(ctx context.Context, sessionID, prompt string) error {
	if a.exec == nil {
		return fmt.Errorf("scheduled: embedded executor not configured")
	}
	_, err := a.exec.SubmitTurn(ctx, runtimehost.TurnRequest{
		SessionID: sessionID,
		Prompt:    prompt,
		// 无人值守：fail-closed 权限模式（不弹审批，未授权即拒绝）。
		RunMeta: &team.RunMeta{PermissionMode: string(runtimepolicy.ModeDontAsk)},
		Profile: a.profile,
	})
	return err
}

// ExecuteHeadlessTurn 执行不绑会话的作业（J2）：先创建临时会话，再提交目标/计划。
func (a scheduleExecutorAdapter) ExecuteHeadlessTurn(ctx context.Context, req schedule.HeadlessTurnRequest) (schedule.HeadlessTurnResult, error) {
	if a.exec == nil || a.createSession == nil {
		return schedule.HeadlessTurnResult{}, fmt.Errorf("scheduled: headless executor not configured")
	}
	sessionID, err := a.createSession(ctx, headlessSessionUserID)
	if err != nil {
		return schedule.HeadlessTurnResult{}, fmt.Errorf("scheduled: create headless session: %w", err)
	}
	turn, err := a.exec.SubmitTurn(ctx, runtimehost.TurnRequest{
		SessionID: sessionID,
		Prompt:    buildHeadlessPrompt(req),
		RunMeta:   &team.RunMeta{PermissionMode: string(runtimepolicy.ModeDontAsk)},
		Profile:   req.Profile,
	})
	if err != nil {
		return schedule.HeadlessTurnResult{SessionID: sessionID}, err
	}
	// J2 收尾：turn 完成后归档临时会话，避免会话表膨胀（best-effort）。
	// 仅在 turn 已结束（Completed）时归档——挂起（等审批/输入）的会话留给人工/后续清理。
	if turn.Completed && a.archiveSession != nil {
		_ = a.archiveSession(ctx, sessionID)
	}
	return schedule.HeadlessTurnResult{SessionID: sessionID, Output: turn.Output}, nil
}

// buildHeadlessPrompt 把目标 + 计划渲染成一次执行的 prompt。
// 无 goal/plan 时退回作业正文（纯提醒语义）。
func buildHeadlessPrompt(req schedule.HeadlessTurnRequest) string {
	goal := strings.TrimSpace(req.Goal)
	prompt := strings.TrimSpace(req.Prompt)
	if goal == "" && len(req.Plan) == 0 {
		return prompt
	}
	var b strings.Builder
	if goal != "" {
		b.WriteString("[SCHEDULE JOB] goal: ")
		b.WriteString(goal)
		b.WriteString("\n")
	}
	if len(req.Plan) > 0 {
		b.WriteString("plan:\n")
		for i, step := range req.Plan {
			if trimmed := strings.TrimSpace(step); trimmed != "" {
				fmt.Fprintf(&b, "%d. %s\n", i+1, trimmed)
			}
		}
	}
	if prompt != "" {
		b.WriteString(prompt)
	}
	return strings.TrimSpace(b.String())
}

// headlessRuntime 是 daemon 内嵌执行核及其关闭函数。
type headlessRuntime struct {
	executor       runtimehost.Executor
	createSession  func(ctx context.Context, userID string) (string, error)
	archiveSession func(ctx context.Context, sessionID string) error
	close          func()
}

// buildHeadlessExecutor 组装内嵌执行核（E3.3 路径 A：bootstrap + runtimeapi.Handler，
// **不启 HTTP**）。
//
// 任一步失败都返回错误，调用方 fail-closed（不内嵌，退回 delegate/mailbox）。
func buildHeadlessExecutor(runtimeConfig *runtimecfg.RuntimeConfig, configPath string) (*headlessRuntime, error) {
	if runtimeConfig == nil {
		return nil, fmt.Errorf("runtime config is required for embedded execution")
	}
	manager, err := runtimebootstrap.NewManager(&runtimebootstrap.Options{
		Config: runtimeConfig,
		// DiscoverOnly：只发现 skill、不加载正文；不启热加载 watcher（daemon 长驻但
		// 不需要目录监听）。
		DiscoverOnly: true,
	})
	if err != nil {
		return nil, fmt.Errorf("bootstrap runtime: %w", err)
	}
	if err := manager.Validate(); err != nil {
		_ = manager.Stop()
		return nil, fmt.Errorf("validate runtime bootstrap: %w", err)
	}
	handler := runtimeapi.NewHandler(manager.Registry(), manager.Loader(), nil)
	manager.ApplyToSkillsHandler(handler)
	handler.SetRuntimeConfig(runtimeConfig, configPath)
	createSession := func(ctx context.Context, userID string) (string, error) {
		sessionManager := manager.SessionManager()
		if sessionManager == nil {
			return "", fmt.Errorf("session manager not configured")
		}
		session, err := sessionManager.Create(ctx, userID)
		if err != nil {
			return "", err
		}
		return session.ID, nil
	}
	archiveSession := func(ctx context.Context, sessionID string) error {
		sessionManager := manager.SessionManager()
		if sessionManager == nil {
			return fmt.Errorf("session manager not configured")
		}
		return sessionManager.Archive(ctx, sessionID)
	}
	return &headlessRuntime{
		executor:       handler.Executor(),
		createSession:  createSession,
		archiveSession: archiveSession,
		close:          func() { _ = manager.Stop() },
	}, nil
}
