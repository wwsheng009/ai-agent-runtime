// Package knowledge 实现 runtime 的知识层（Knowledge Layer）。
//
// 本文件是层的公开入口：模式解析、所有权仲裁与 Layer 句柄。
// 除非 `knowledge.mode` 选择 shadow/on，否则该层完全惰性：
//
//	off     （默认）不打开数据库、不注册 code.* 工具、不注入上下文，
//	               行为与没有知识层的 runtime 逐字节一致。
//	shadow         构建并服务索引，但不向提示词注入任何内容。
//	on             索引 + 工具面 + 上下文注入。
//
// 关键不变量：nil *Layer 是合法且"已禁用"的句柄——所有方法对 nil 安全，
// 调用方永远不必写 `if layer != nil`。
package knowledge

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Role 描述本进程与 workspace store 的关系（ADR-0001）。
type Role string

const (
	// RoleNone 表示层未启用。
	RoleNone Role = "none"
	// RoleOwner 表示本进程持有 store 写锁，可以建索引。
	RoleOwner Role = "owner"
	// RoleReader 表示另一个存活进程持有写锁，本进程只读。
	RoleReader Role = "reader"
)

// Layer 是一个 workspace 上的知识层句柄；nil 表示层已禁用。
type Layer struct {
	cfg   Config
	role  Role
	store Store
	owner *ownership
	// workspaceID 是 workspaces 行的 id；首次使用时惰性登记。
	workspaceID string
	// planVersion 缓存 Planner 的 WorkspaceVersion 采样（TTL 与 W2 同源）。
	planVersion versionCache

	// Phase 5 交付 1/2：编辑触发的变更队列（惰性创建、随 Layer 关闭）。
	// 挂在 Layer 而不是 Activation：agent 侧只持有 Layer，且同一 workspace
	// 的多个 Activation 共享一个串行队列（不会出现两个 worker 抢同一 store）。
	queueMu     sync.Mutex
	queue       *ChangeQueue
	queueClosed bool

	// Phase 5 交付 1（变更源 2）：外部变更校正（git + stat）的节流与观测状态。
	syncMu                 sync.Mutex
	lastGitSyncAt          time.Time
	externalSyncCount      int
	externalSyncDirtyCount int
	gitMu                  sync.Mutex
	gitSource              *GitChangeSource

	// Phase 5 交付 4：GC 状态（状态面摘要 + 自动触发的冷却窗口）。
	gcMu            sync.Mutex
	gcRuns          int
	lastGCAttemptAt time.Time
	lastGCReport    GCReport
	lastGCError     string
	// gcSizeFn 是触发判定的库大小来源注入口（测试用）；nil 时走 store 主文件大小。
	gcSizeFn func() int64
}

// Open 解析配置并返回 Layer。
//
// ModeOff 返回 (nil, nil)：调用方可以继续使用 nil layer，无需任何分支。
// shadow/on 会打开（必要时创建并迁移）workspace store，并完成单写者仲裁：
// 抢到锁的是 owner（可写、可建索引），否则降级为 reader（只读）。
func Open(ctx context.Context, cfg Config) (*Layer, error) {
	cfg = cfg.Normalize()
	switch cfg.Mode {
	case ModeOff:
		return nil, nil
	case ModeShadow, ModeOn:
	default:
		return nil, fmt.Errorf("%w: %q", ErrInvalidMode, cfg.Mode)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if err := cfg.ensureDir(); err != nil {
		return nil, err
	}

	own, err := acquireOwnership(cfg)
	if err != nil {
		return nil, err
	}
	store, err := OpenStore(ctx, cfg.storePath(), own.readOnly())
	if err != nil {
		_ = own.release()
		return nil, err
	}
	return &Layer{cfg: cfg, role: own.role(), store: store, owner: own}, nil
}

// Mode 返回配置的模式（nil Layer 为 off）。
func (l *Layer) Mode() Mode {
	if l == nil {
		return ModeOff
	}
	return l.cfg.Mode
}

// Config 返回解析后的配置副本。
func (l *Layer) Config() Config {
	if l == nil {
		return DefaultConfig()
	}
	return l.cfg
}

// Enabled 报告层是否参与任何执行路径。
func (l *Layer) Enabled() bool { return l.Mode() != ModeOff }

// Injects 报告层是否可以向提示词注入内容（mode=on 且 store 可用）。
func (l *Layer) Injects() bool {
	return l != nil && l.cfg.Mode == ModeOn && l.store != nil
}

// Role 返回本进程相对 workspace store 的角色。
func (l *Layer) Role() Role {
	if l == nil || l.role == "" {
		return RoleNone
	}
	return l.role
}

// Workspace 返回被索引的工作区根目录。
func (l *Layer) Workspace() string {
	if l == nil {
		return ""
	}
	return l.cfg.Workspace
}

// Store 暴露底层 store（禁用时为 nil）。
func (l *Layer) Store() Store {
	if l == nil {
		return nil
	}
	return l.store
}

// DBPath 返回 store 的落盘路径（禁用时为空）；状态面与诊断日志用它展示落点。
func (l *Layer) DBPath() string {
	if l == nil {
		return ""
	}
	return l.cfg.storePath()
}

// Index 在需要时（重）建索引；只有 owner 会真正执行，reader 与禁用层是空操作。
func (l *Layer) Index(ctx context.Context) (IndexResult, error) {
	if l == nil || l.store == nil || l.role != RoleOwner {
		return IndexResult{}, nil
	}
	return RunIndex(ctx, l.store, l.cfg)
}

// errWorkspaceNotIndexed 表示 store 已打开但该 workspace 尚无登记行
// （owner 还没跑过索引）。它不是失败，而是 reader 的常见初始状态。
var errWorkspaceNotIndexed = errors.New("knowledge: workspace is not indexed yet")

// Stats 返回 store 的行数汇总；禁用、不可用或尚未索引时返回零值。
func (l *Layer) Stats(ctx context.Context) (Stats, error) {
	if l == nil || l.store == nil {
		return Stats{}, nil
	}
	wsID, err := l.ensureWorkspace(ctx)
	if errors.Is(err, errWorkspaceNotIndexed) {
		// reader 看到"还没索引过"是正常状态（owner 尚未建行），不是错误。
		return Stats{}, nil
	}
	if err != nil {
		return Stats{}, err
	}
	return l.store.Stats(ctx, wsID)
}

// Plan 返回认知层的复用/探索决策（06 §4 Phase 2 W4）。
//
// nil / mode=off 返回空 Plan（Reason=disabled）：不查 store、不 panic。
// shadow/on 走只读路径；workspace 未登记（索引不可用）或 store 失败时返回
// Degraded + Reason，不冒泡错误（04 §4.1 Degrade-Not-Fail）。
// PlanInput.Current 为空时按 DefaultReuseVersionTTL 采样并缓存 WorkspaceVersion，
// 采样时刻随观测返回，供 W3 Gate 做 TTL 滞后兜底；采样走 ObserveVersion——
// 它是 Phase 5 的判定点：先做一次外部变更校正（git + stat，节流、尽力而为），
// 发现变更时把本次版本标为未稳定（#pending），旧知识立即不可复用（fail-closed）。
// 校正只读 git/磁盘并做非阻塞入队，store 写入发生在队列 goroutine（不在本调用内）。
func (l *Layer) Plan(ctx context.Context, in PlanInput) (Plan, error) {
	if l == nil || l.store == nil || l.cfg.Mode == ModeOff {
		return Plan{Reason: PlanReasonDisabled}, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	wsID, err := l.planWorkspaceID(ctx)
	if err != nil {
		if errors.Is(err, errWorkspaceNotIndexed) {
			return Plan{Degraded: true, Reason: PlanReasonIndexUnavailable}, nil
		}
		return Plan{Degraded: true, Reason: classifyStoreError(ctx, err)}, nil
	}
	if strings.TrimSpace(in.WorkspaceID) == "" {
		in.WorkspaceID = wsID
	}
	if strings.TrimSpace(in.Current.Version) == "" {
		now := in.Now
		if now.IsZero() {
			now = time.Now()
		}
		obs, err := l.ObserveVersion(ctx, in.VersionTTL, now)
		if err != nil {
			return Plan{Degraded: true, Reason: classifyStoreError(ctx, err)}, nil
		}
		in.Current = obs
	}
	return NewPlanner(PlannerOptions{Reader: l.store, Config: l.cfg.Planner}).Plan(ctx, in)
}

// planWorkspaceID 只读解析 workspaces 行 id。
//
// 与 ensureWorkspace 不同，它不调用 EnsureWorkspace（owner 会写库），保证
// Planner 路径纯读（04 §4.2）；未登记时返回 errWorkspaceNotIndexed。
func (l *Layer) planWorkspaceID(ctx context.Context) (string, error) {
	if l.workspaceID != "" {
		return l.workspaceID, nil
	}
	if strings.TrimSpace(l.cfg.Workspace) == "" {
		return "", errors.New("knowledge: plan: workspace must be set")
	}
	id, ok, err := l.store.FindWorkspace(ctx, l.cfg.Workspace)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", errWorkspaceNotIndexed
	}
	return id, nil
}

// FindSymbols 解析符号名。
func (l *Layer) FindSymbols(ctx context.Context, q SymbolQuery) ([]Symbol, error) {
	if l == nil || l.store == nil {
		return nil, nil
	}
	return l.store.FindSymbols(ctx, q)
}

// FindRefs 返回符号的引用点。
func (l *Layer) FindRefs(ctx context.Context, q RefQuery) ([]Reference, error) {
	if l == nil || l.store == nil {
		return nil, nil
	}
	return l.store.FindRefs(ctx, q)
}

// Search 在索引上做全文检索。
func (l *Layer) Search(ctx context.Context, q SearchQuery) ([]SearchHit, error) {
	if l == nil || l.store == nil {
		return nil, nil
	}
	return l.store.Search(ctx, q)
}

// RecordInvalidation 记录一次索引失效事件（reader 角色会返回 ErrReadOnlyStore）。
func (l *Layer) RecordInvalidation(ctx context.Context, reason, scopeJSON string) error {
	if l == nil || l.store == nil {
		return nil
	}
	if l.role != RoleOwner {
		// 保持既有语义：写操作在 reader 上硬失败，不静默丢弃。
		return ErrReadOnlyStore
	}
	wsID, err := l.ensureWorkspace(ctx)
	if err != nil {
		return err
	}
	return l.store.RecordInvalidation(ctx, InvalidationEvent{
		WorkspaceID: wsID,
		Reason:      reason,
		ScopeJSON:   scopeJSON,
	})
}

// ensureWorkspace 登记（或读取）workspace 行并返回其 id。
//
// owner 走 upsert（登记并刷新 updated_at）；reader **只查不写**——最常见的接入
// 形态是 aicli 作为 reader 挂在 runtime-server 后面，如果读状态也要写库，reader
// 会直接拿到 ErrReadOnlyStore 而完全不可用（Phase 1 交付 5 的状态面依赖此路径）。
// 尚未登记（owner 还没索引过）时返回 errWorkspaceNotIndexed。
func (l *Layer) ensureWorkspace(ctx context.Context) (string, error) {
	if l.workspaceID != "" {
		return l.workspaceID, nil
	}
	if strings.TrimSpace(l.cfg.Workspace) == "" {
		return "", errors.New("knowledge: workspace must be set")
	}
	if l.role != RoleOwner {
		id, ok, err := l.store.FindWorkspace(ctx, l.cfg.Workspace)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", errWorkspaceNotIndexed
		}
		l.workspaceID = id
		return id, nil
	}
	id, err := l.store.EnsureWorkspace(ctx, Workspace{RootPath: l.cfg.Workspace})
	if err != nil {
		return "", err
	}
	l.workspaceID = id
	return id, nil
}

// Close 释放所有权并关闭 store；对 nil Layer 安全且幂等。
func (l *Layer) Close() error {
	if l == nil {
		return nil
	}
	// 先停变更队列：它的 worker 可能在写 store，必须在关库之前退出。
	l.queueMu.Lock()
	queue := l.queue
	l.queue = nil
	l.queueClosed = true
	l.queueMu.Unlock()
	if queue != nil {
		queue.Close()
	}
	var firstErr error
	if l.store != nil {
		if err := l.store.Close(); err != nil {
			firstErr = err
		}
		l.store = nil
	}
	if l.owner != nil {
		if err := l.owner.release(); err != nil && firstErr == nil {
			firstErr = err
		}
		l.owner = nil
	}
	l.role = RoleNone
	return firstErr
}

// MarkChanged 把编辑过的文件交给变更队列（Phase 5 交付 1 的 edit hook 入口）。
//
// 门控与 Recorder 同口径：仅 mode=shadow|on 且本进程是 owner 时真正入队；
// off / reader 是 no-op（reader 没有写权限，标记它没有意义）。nil-safe 且非阻塞——
// 调用方（工具层/编辑路径）不需要分支，也不会被索引拖慢。
func (l *Layer) MarkChanged(paths ...string) {
	if l == nil || len(paths) == 0 {
		return
	}
	queue := l.ChangeQueue()
	if queue == nil {
		return
	}
	queue.Mark(paths...)
}

// ChangeQueue 返回本层的变更队列（惰性创建）；off / reader / 无 store 时为 nil。
// 同一 Layer 多次调用返回同一实例；队列随 Layer.Close 关闭（Close 会等它退出）。
func (l *Layer) ChangeQueue() *ChangeQueue {
	if l == nil || l.store == nil {
		return nil
	}
	if l.cfg.Mode != ModeShadow && l.cfg.Mode != ModeOn {
		return nil
	}
	if l.role != RoleOwner {
		return nil
	}
	l.queueMu.Lock()
	defer l.queueMu.Unlock()
	if l.queueClosed {
		return nil
	}
	if l.queue == nil {
		l.queue = NewChangeQueue(l.cfg, l.store, ChangeQueueOptions{
			// 索引落地后工作区版本必然变化：失效版本缓存，避免 30s TTL 把新版本
			// 压住（Phase 5 交付 3：版本向量参与复用判定）。
			OnResult: func(IndexResult) { l.planVersion.invalidate() },
		})
	}
	return l.queue
}
