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

// Index 在需要时（重）建索引；只有 owner 会真正执行，reader 与禁用层是空操作。
func (l *Layer) Index(ctx context.Context) (IndexResult, error) {
	if l == nil || l.store == nil || l.role != RoleOwner {
		return IndexResult{}, nil
	}
	return RunIndex(ctx, l.store, l.cfg)
}

// Stats 返回 store 的行数汇总；禁用或不可用时返回零值。
func (l *Layer) Stats(ctx context.Context) (Stats, error) {
	if l == nil || l.store == nil {
		return Stats{}, nil
	}
	wsID, err := l.ensureWorkspace(ctx)
	if err != nil {
		return Stats{}, err
	}
	return l.store.Stats(ctx, wsID)
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
func (l *Layer) ensureWorkspace(ctx context.Context) (string, error) {
	if l.workspaceID != "" {
		return l.workspaceID, nil
	}
	if strings.TrimSpace(l.cfg.Workspace) == "" {
		return "", errors.New("knowledge: workspace must be set")
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
