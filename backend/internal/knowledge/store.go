package knowledge

import (
	"context"
	"errors"
)

// Store 是知识层的持久化契约。
//
// 实现必须满足设计文档描述的单写者 / 多读者拓扑（ADR-0001、owner 仲裁）：
// 同一时刻至多一个进程持有写锁，读者以只读方式打开同一文件，因此读操作
// 永远不会被写者阻塞。
//
// 所有方法都接受 context，保证挂起的文件系统或僵死的子进程无法卡住 agent 循环；
// 调用方应传入请求的 deadline。
type Store interface {
	// SchemaVersion 返回 schema_migrations 中的最高版本号。
	SchemaVersion(ctx context.Context) (int, error)

	// EnsureWorkspace 以 upsert 语义登记工作区，返回其 id。
	EnsureWorkspace(ctx context.Context, ws Workspace) (string, error)

	// UpsertFile 记录文件的身份与内容哈希，返回稳定的文件 id。
	// 调用方以 (workspace, path) 为键；重复调用是幂等的。
	UpsertFile(ctx context.Context, rec FileRecord) (string, error)

	// DeleteFile 删除文件及其全部派生行（symbols / refs），单事务完成。
	DeleteFile(ctx context.Context, workspaceID, path string) error

	// FileByPath 返回已存储的文件记录；ok=false 表示未知。
	FileByPath(ctx context.Context, workspaceID, path string) (FileRecord, bool, error)

	// ReplaceSymbols 原子替换一个文件的符号集合。符号 id 由 StableKey 派生，
	// 因此增量重建不会改变 id。
	ReplaceSymbols(ctx context.Context, fileID string, syms []Symbol) error

	// ReplaceRefs 原子替换一个文件的出向引用集合。
	ReplaceRefs(ctx context.Context, fileID string, refs []Reference) error

	// FindSymbols 在符号表中解析名字。
	FindSymbols(ctx context.Context, q SymbolQuery) ([]Symbol, error)

	// FindRefs 解析指向某个符号身份的引用点。
	FindRefs(ctx context.Context, q RefQuery) ([]Reference, error)

	// Search 在 symbols_fts 上做全文检索。
	Search(ctx context.Context, q SearchQuery) ([]SearchHit, error)

	// RecordInvalidation 记录一次索引失效事件（shadow 对比、外部变更）。
	RecordInvalidation(ctx context.Context, ev InvalidationEvent) error

	// Stats 返回状态面与 shadow 差异率度量使用的行数汇总。
	Stats(ctx context.Context, workspaceID string) (Stats, error)

	// Close 释放句柄；必须幂等。
	Close() error
}

// ErrStoreClosed 由 Close 之后调用的 Store 方法返回。
var ErrStoreClosed = errors.New("knowledge: store closed")

// ErrReadOnlyStore 由只读（reader 角色）Store 的写方法返回。
var ErrReadOnlyStore = errors.New("knowledge: store is read-only (another process owns it)")

// ErrNotImplemented 标记 Phase 0/1 边界上尚未落地的能力。
//
// 保留它是为了让"配置了 shadow/on 但实现未到"的场景显式失败，
// 而不是静默降级成 off。
var ErrNotImplemented = errors.New("knowledge: not implemented in this phase")
