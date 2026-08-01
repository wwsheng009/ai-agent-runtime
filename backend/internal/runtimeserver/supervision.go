package runtimeserver

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

// SupervisionRuntimeHooks 由 cmd 层注入运行时适配器（agentcontrol / team
// orchestrator）。所有字段可空：空时控制面仍可完整工作，mutation action 会
// 以明确的 "executor not configured" 结果失败，而 inspect/acknowledge/defer
// 与全部通知、wake、digest 能力不受影响。
type SupervisionRuntimeHooks struct {
	// AgentRegistry is the durable AgentControl graph used by the standard
	// descendant provider and graph-backed authorization. Explicit list hooks
	// remain available for specialized hosts.
	AgentRegistry agentcontrol.AgentRegistryReader
	// TeamStore supplies the current Team status for descendants recorded in
	// durable Team edges.
	TeamStore team.Store
	// ListAgentDescendants 返回 root scope 下 child agent 的当前执行状态。
	ListAgentDescendants func(ctx context.Context, scope supervision.Scope) ([]supervision.DescendantState, error)
	// ListTeamDescendants 返回 root scope 下 child Team 的当前执行状态。
	ListTeamDescendants func(ctx context.Context, scope supervision.Scope) ([]supervision.DescendantState, error)
	// Authorize 覆盖默认 graph-backed root-scope 授权器；nil 时 agent
	// descendants and Team edges are used to reject cross-scope targets.
	Authorize func(ctx context.Context, rootScopeID, requestedByKind, requestedByID, targetKind, targetID string) error
	// Execute 执行已接受的 mutation action（cancel/close/cancel_subtree/
	// retry/reassign）。nil 时这些动作返回 failed 结果并保留在 durable
	// action 记录中，不假装成功。
	Execute func(ctx context.Context, a supervision.ActionRecord) (supervision.ActionResult, error)
}

// SupervisionControlPlane 是装配完成的 P2 控制面（doc 6.2-6.9）。
type SupervisionControlPlane struct {
	// Store 是 durable 持久化入口（通知/动作/wake/team edges）。
	Store supervision.Store
	// Actions 是统一控制动作入口（doc 6.6）。
	Actions *supervision.ActionService
	// Wakes 订阅 lifecycle inbox 的父 turn 唤醒调度（doc 6.5）。
	Wakes *supervision.WakeScheduler
	// Provider 聚合运行时 descendant 状态供 snapshot 使用（doc 6.2）。
	Provider supervision.DescendantProvider
}

// SetActionExecutor 在控制面装配后注入/替换运行时 mutation executor。Host
// 在 session hub / actor registry 就绪后调用一次（例如 aicli 的 CloseAgent
// 适配器依赖 host.ActorRegistry），替换默认的 "executor not configured"
// 占位实现。
func (p *SupervisionControlPlane) SetActionExecutor(executor supervision.ActionExecutor) {
	if p == nil || p.Actions == nil {
		return
	}
	p.Actions.SetExecutor(executor)
}

// Close 关闭底层 store。幂等。
func (p *SupervisionControlPlane) Close() error {
	if p == nil || p.Store == nil {
		return nil
	}
	return p.Store.Close()
}

// BuildSupervisionControlPlane 在 dataDir 下创建 durable SQLite 控制面并装配
// ActionService / WakeScheduler / DescendantProvider。cfg 零值字段使用默认
// 调参；hooks 允许 cmd 层接入真实运行时执行器与 descendant 投影。
func BuildSupervisionControlPlane(dataDir string, cfg supervision.Config, hooks SupervisionRuntimeHooks) (*SupervisionControlPlane, error) {
	store, err := openSupervisionStore(dataDir)
	if err != nil {
		return nil, err
	}
	cfg = cfg.WithDefaults()

	actions := supervision.NewActionService(
		store,
		&runtimeActionExecutor{execute: hooks.Execute},
		&rootScopeAuthorizer{
			authorize: hooks.Authorize,
			agents:    hooks.AgentRegistry,
			teams:     hooks.TeamStore,
			store:     store,
		},
	)
	wakes := supervision.NewWakeScheduler(store, cfg.WakeSchedulerConfig())
	provider := &supervisionDescendantProvider{
		store:       store,
		agents:      hooks.AgentRegistry,
		teams:       hooks.TeamStore,
		agentStates: hooks.ListAgentDescendants,
		teamStates:  hooks.ListTeamDescendants,
	}
	return &SupervisionControlPlane{
		Store:    store,
		Actions:  actions,
		Wakes:    wakes,
		Provider: provider,
	}, nil
}

func openSupervisionStore(dataDir string) (*supervision.SQLiteSupervisionStore, error) {
	dir := strings.TrimSpace(dataDir)
	if dir == "" {
		dir = os.TempDir()
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("supervision: create data dir: %w", err)
	}
	store, err := supervision.NewSQLiteSupervisionStore(&supervision.StoreConfig{
		Path: filepath.Join(dir, "supervision.db"),
	})
	if err != nil {
		return nil, fmt.Errorf("supervision: open store: %w", err)
	}
	return store, nil
}

// runtimeActionExecutor 把 runtime 侧副作用适配为 supervision.ActionExecutor
// 契约（doc 6.6 constraint 7：close/cancel 必须产生 resolution notification，
// 由 ActionService 在 Execute 返回后统一发出）。
type runtimeActionExecutor struct {
	execute func(ctx context.Context, a supervision.ActionRecord) (supervision.ActionResult, error)
}

func (e *runtimeActionExecutor) Execute(ctx context.Context, a supervision.ActionRecord) (supervision.ActionResult, error) {
	// 记账型动作不需要 runtime 副作用，直接完成（ack/defer 的 CAS 已在
	// store 层执行；inspect 由 snapshot/digest 查询支撑）。
	switch a.Action {
	case supervision.ActionInspect, supervision.ActionAcknowledge, supervision.ActionDefer:
		return supervision.ActionResult{
			Status: supervision.ActionCompleted,
			Result: "durable " + string(a.Action) + " recorded",
		}, nil
	}
	if e.execute != nil {
		return e.execute(ctx, a)
	}
	return supervision.ActionResult{
		Status: supervision.ActionFailed,
		Result: fmt.Sprintf("%s not executed: runtime executor not configured (needs agentcontrol/team wiring)", a.Action),
	}, nil
}

// rootScopeAuthorizer enforces that an agent or Team target belongs to the
// requested durable root scope before a mutation record can be accepted.
// Production hosts should still inject identity-aware authorization through
// hooks.Authorize; this fallback deliberately refuses unknown graph targets.
type rootScopeAuthorizer struct {
	authorize func(ctx context.Context, rootScopeID, requestedByKind, requestedByID, targetKind, targetID string) error
	agents    agentcontrol.AgentRegistryReader
	teams     team.Store
	store     supervision.Store
}

func (a *rootScopeAuthorizer) Authorize(ctx context.Context, rootScopeID, requestedByKind, requestedByID, targetKind, targetID string) error {
	if a != nil && a.authorize != nil {
		return a.authorize(ctx, rootScopeID, requestedByKind, requestedByID, targetKind, targetID)
	}
	rootScopeID = strings.TrimSpace(rootScopeID)
	requestedByID = strings.TrimSpace(requestedByID)
	targetID = strings.TrimSpace(targetID)
	if rootScopeID == "" {
		return errors.New("supervision: missing root scope id")
	}
	if requestedByID == "" {
		return errors.New("supervision: missing requester id")
	}
	if strings.TrimSpace(targetKind) == "" || targetID == "" {
		return errors.New("supervision: missing target")
	}
	switch supervision.SubjectKind(strings.TrimSpace(targetKind)) {
	case supervision.SubjectAgentSession, supervision.SubjectAgentRun:
		if a == nil || a.agents == nil {
			// The graph-backed check is mandatory once a runtime supplies an
			// AgentControl graph. Minimal/test-only hosts retain the legacy
			// conservative requester validation above.
			return nil
		}
		records, err := a.agents.ListAgentControlAgents(ctx, agentcontrol.AgentFilter{
			RootSessionID: rootScopeID,
			IncludeClosed: true,
		})
		if err != nil {
			return fmt.Errorf("supervision: read agent graph authorization: %w", err)
		}
		for _, record := range records {
			if strings.EqualFold(strings.TrimSpace(record.AgentID), targetID) ||
				strings.EqualFold(strings.TrimSpace(record.SessionID), targetID) {
				return nil
			}
		}
		return errors.New("supervision: target agent is outside the requested root scope; not authorized")
	case supervision.SubjectTeam, supervision.SubjectTeamTask:
		if a == nil || a.store == nil {
			return errors.New("supervision: team graph authorization is not configured")
		}
		teamID := targetID
		if targetKind == string(supervision.SubjectTeamTask) {
			if a.teams == nil {
				return errors.New("supervision: team store is required for task authorization")
			}
			task, err := a.teams.GetTask(ctx, targetID)
			if err != nil {
				return fmt.Errorf("supervision: read task for authorization: %w", err)
			}
			if task == nil {
				return errors.New("supervision: target task does not exist; not authorized")
			}
			teamID = strings.TrimSpace(task.TeamID)
			if teamID == "" {
				return errors.New("supervision: target task has no team; not authorized")
			}
		}
		if strings.EqualFold(rootScopeID, teamID) {
			return nil
		}
		ancestors, err := a.store.ListTeamAncestors(ctx, teamID)
		if err != nil {
			return fmt.Errorf("supervision: read team graph authorization: %w", err)
		}
		for _, edge := range ancestors {
			if strings.EqualFold(strings.TrimSpace(edge.RootScopeID), rootScopeID) ||
				strings.EqualFold(strings.TrimSpace(edge.RootTeamID), rootScopeID) {
				return nil
			}
		}
		return errors.New("supervision: target team is outside the requested root scope; not authorized")
	}
	return nil
}

// supervisionDescendantProvider 聚合 durable team edges 与 runtime 注入的
// agent/team 投影（doc 6.2/6.7：snapshot 同时聚合 child Agent 与 child Team）。
type supervisionDescendantProvider struct {
	store       supervision.Store
	agents      agentcontrol.AgentRegistryReader
	teams       team.Store
	agentStates func(ctx context.Context, scope supervision.Scope) ([]supervision.DescendantState, error)
	teamStates  func(ctx context.Context, scope supervision.Scope) ([]supervision.DescendantState, error)
}

func (p *supervisionDescendantProvider) ListDescendants(ctx context.Context, scope supervision.Scope) ([]supervision.DescendantState, error) {
	var out []supervision.DescendantState
	if p.agentStates != nil {
		states, err := p.agentStates(ctx, scope)
		if err != nil {
			return nil, fmt.Errorf("supervision: agent descendant projection: %w", err)
		}
		out = append(out, states...)
	}
	if p.agents != nil && strings.TrimSpace(scope.RootSessionID) != "" {
		records, err := p.agents.ListAgentControlAgents(ctx, agentcontrol.AgentFilter{
			RootSessionID: scope.RootSessionID,
			IncludeClosed: true,
		})
		if err != nil {
			return nil, fmt.Errorf("supervision: list agent descendants: %w", err)
		}
		for _, record := range records {
			if strings.EqualFold(strings.TrimSpace(record.AgentPath), "/root") {
				continue
			}
			out = append(out, descendantStateFromAgentRecord(record))
		}
	}
	if p.teamStates != nil {
		states, err := p.teamStates(ctx, scope)
		if err != nil {
			return nil, fmt.Errorf("supervision: team descendant projection: %w", err)
		}
		out = append(out, states...)
	}
	// durable team-edge 表兜底：root team 的直属 child Team 一定可见，
	// 即使内存投影尚未就绪。
	if p.store != nil && strings.TrimSpace(scope.RootTeamID) != "" {
		edges, err := p.store.ListChildTeams(ctx, scope.RootTeamID)
		if err != nil {
			return nil, fmt.Errorf("supervision: list child teams: %w", err)
		}
		for _, e := range edges {
			if strings.TrimSpace(e.Status) == supervision.TeamEdgeStatusClosed {
				continue
			}
			state := supervision.DescendantState{
				Kind:            supervision.SubjectTeam,
				ID:              e.ChildTeamID,
				ParentPath:      []string{e.RootTeamID},
				ExecutionStatus: e.Status,
				Reason:          "team edge " + e.EdgeID,
			}
			if p.teams != nil {
				record, getErr := p.teams.GetTeam(ctx, e.ChildTeamID)
				if getErr != nil {
					return nil, fmt.Errorf("supervision: get child team %s: %w", e.ChildTeamID, getErr)
				}
				if record != nil {
					state.ExecutionStatus = string(record.Status)
					state.SupervisionState = supervisionStateForTeamStatus(record.Status)
				}
			}
			if state.SupervisionState == "" {
				state.SupervisionState = supervision.SupervisionRunning
			}
			out = append(out, state)
		}
	}
	return dedupeDescendantStates(out), nil
}

func descendantStateFromAgentRecord(record agentcontrol.AgentRecord) supervision.DescendantState {
	state := supervision.DescendantState{
		Kind:            supervision.SubjectAgentSession,
		ID:              strings.TrimSpace(record.SessionID),
		ExecutionStatus: strings.TrimSpace(record.Status),
		ParentPath:      agentParentPath(record),
	}
	if state.ID == "" {
		state.ID = strings.TrimSpace(record.AgentID)
	}
	if record.Closed() {
		state.SupervisionState = supervision.SupervisionTerminated
	} else {
		state.SupervisionState = supervision.SupervisionRunning
	}
	return state
}

func agentParentPath(record agentcontrol.AgentRecord) []string {
	path := strings.Trim(strings.TrimSpace(record.AgentPath), "/")
	if path == "" || path == "root" {
		return nil
	}
	parts := strings.Split(path, "/")
	if len(parts) <= 2 {
		if parent := strings.TrimSpace(record.ParentSessionID); parent != "" {
			return []string{parent}
		}
		return nil
	}
	return append([]string(nil), parts[:len(parts)-1]...)
}

func supervisionStateForTeamStatus(status team.TeamStatus) supervision.SupervisionState {
	switch status {
	case team.TeamStatusFailed:
		return supervision.SupervisionInvalid
	case team.TeamStatusCanceled:
		return supervision.SupervisionTerminated
	case team.TeamStatusDone, team.TeamStatusPartiallyCompleted:
		return supervision.SupervisionTerminated
	case team.TeamStatusPaused:
		return supervision.SupervisionBlocked
	default:
		return supervision.SupervisionRunning
	}
}

func dedupeDescendantStates(states []supervision.DescendantState) []supervision.DescendantState {
	seen := make(map[string]int, len(states))
	out := make([]supervision.DescendantState, 0, len(states))
	for _, state := range states {
		key := string(state.Kind) + "|" + strings.TrimSpace(state.ID)
		if key == "|" {
			continue
		}
		if index, exists := seen[key]; exists {
			out[index] = state
			continue
		}
		seen[key] = len(out)
		out = append(out, state)
	}
	return out
}
