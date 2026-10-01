package knowledge

import (
	"context"
	"errors"
	"time"
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

	// FindWorkspace 按 root_path 查既有工作区行；ok=false 表示尚未登记。
	//
	// 与 EnsureWorkspace 分离，是为了让 reader（只读角色）也能读状态面：
	// 读者不得写库，但"这个 workspace 有没有被索引过"是纯读问题。
	FindWorkspace(ctx context.Context, rootPath string) (string, bool, error)

	// WorkspaceAdapterVersion 返回库内记录的索引 adapter 版本（未记录时为空串）。
	// Phase 4 交付 4：版本不一致时按配置触发 full_rebuild_on_adapter_change。
	WorkspaceAdapterVersion(ctx context.Context, workspaceID string) (string, error)

	// SetWorkspaceAdapterVersion 记录本次成功索引使用的 adapter 版本。
	SetWorkspaceAdapterVersion(ctx context.Context, workspaceID, version string) error

	// UpsertFile 记录文件的身份与内容哈希，返回稳定的文件 id。
	// 调用方以 (workspace, path) 为键；重复调用是幂等的。
	UpsertFile(ctx context.Context, rec FileRecord) (string, error)

	// DeleteFile 物理删除文件及其全部派生行（symbols / refs），单事务完成。
	// 索引路径不用它：文件消失走 MarkFilesDeleted 软删除；本方法留给 Phase 5 GC。
	DeleteFile(ctx context.Context, workspaceID, path string) error

	// FileByPath 返回已存储的文件记录；ok=false 表示未知。
	FileByPath(ctx context.Context, workspaceID, path string) (FileRecord, bool, error)

	// ListActiveFiles 返回该 workspace 全部未软删除的文件记录（按 path 升序）。
	// 增量索引用它做"库内 vs 磁盘"对账；reader 角色只读可用。
	ListActiveFiles(ctx context.Context, workspaceID string) ([]FileRecord, error)

	// MarkFilesDeleted 把路径集合标记为软删除（04 §5 Phase 1 交付 3）：
	// files.deleted_at 与 index_state=stale，其 symbols.deleted_at 同步标记，
	// 返回本次新标记的文件数；已删除路径幂等跳过。
	MarkFilesDeleted(ctx context.Context, workspaceID string, paths []string, at time.Time) (int, error)

	// GCDeleted 物理清理 deleted_at 早于 cutoff 的软删除文件及其派生行
	// （04 §5 Phase 5 交付 4）；只读 store 返回 ErrReadOnlyStore。
	GCDeleted(ctx context.Context, workspaceID string, cutoff time.Time) (GCReport, error)

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

	// RecordInvalidation 记录一次索引失效事件（变更源 / 适配器冲突等；
	// shadow 对比写 exploration_attribution，不写本表）。
	RecordInvalidation(ctx context.Context, ev InvalidationEvent) error

	// Stats 返回状态面与 shadow 差异率度量使用的行数汇总。
	Stats(ctx context.Context, workspaceID string) (Stats, error)

	// StartIndexJob 先落一条 index_jobs 运行记录并返回其 id。
	//
	// 契约（ADR-0007 §4.3 / 04 §4.1）：任何索引变更路径都必须**先写本表再执行**，
	// 不得绕过本表直接写 symbols。status 缺省 running，kind 缺省 light。
	StartIndexJob(ctx context.Context, job IndexJob) (string, error)

	// UpdateIndexJob 刷新运行进度（files_done）；用于长索引的中途上报。
	UpdateIndexJob(ctx context.Context, jobID string, filesDone int) error

	// FinishIndexJob 终结一条运行记录（status ∈ done|failed|cancelled），
	// 并写入 files_total / files_done / error 摘要 / finished_at。
	FinishIndexJob(ctx context.Context, jobID, status string, filesTotal, filesDone int, errMsg string) error

	// LatestIndexJob 返回该 workspace 最近一次运行记录；nil 表示从未跑过。
	// 纯读：reader 角色也可用它支撑状态面。
	LatestIndexJob(ctx context.Context, workspaceID string) (*IndexJob, error)

	// ---- 探索记忆（06 §4 Phase 2 W1 / S1）----
	//
	// 三张 exploration_* 表已在 0001 冻结，以下方法零迁移即可读写。
	// 写方法在 reader 角色下返回 ErrReadOnlyStore（硬失败，不静默降级）；
	// 读方法 reader 可用。节点的 knowledge_version 必填（非空）。

	// UpsertExplorationSession 以 upsert 语义登记一次探索会话，返回稳定 id。
	// 身份为 (workspace_id, session_id, task_id)，重复登记只刷新 updated_at。
	UpsertExplorationSession(ctx context.Context, sess ExplorationSession) (string, error)

	// AppendExplorationNode 追加一条探索节点，返回稳定 id。
	// 同一 exploration_id 内同一 target 重复写入是幂等的：use_count 累加、
	// last_used_at 刷新，其余可变量取最新观察值。
	AppendExplorationNode(ctx context.Context, node ExplorationNode) (string, error)

	// TouchExplorationNode 记录一次复用：use_count+1、last_used_at=at
	// （零值 at 取当前时间）；节点不存在时返回错误。
	TouchExplorationNode(ctx context.Context, nodeID string, at time.Time) error

	// AppendExplorationEdge 追加一条节点关系，返回稳定 id；同一四元组
	// (exploration_id, from, to, edge_type) 重复追加幂等（权重取最新值）。
	AppendExplorationEdge(ctx context.Context, edge ExplorationEdge) (string, error)

	// LookupExplorationNodes 返回工作区内的探索节点：给出 TaskID 查任务工作集，
	// 为空则跨任务按 Target 加权检索（exact > prefix > contains；Target 为空
	// 时按最近使用全量返回）/ Type 过滤；Limit <= 0 时使用 store 默认上限。
	// 纯读，reader 可用。
	LookupExplorationNodes(ctx context.Context, q ExplorationNodeQuery) ([]ExplorationNode, error)

	// LatestExplorationSession 返回 (workspace_id, session_id) 下最近更新的
	// 会话；ok=false 表示尚未登记。纯读，reader 可用。
	LatestExplorationSession(ctx context.Context, workspaceID, sessionID string) (ExplorationSession, bool, error)

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
