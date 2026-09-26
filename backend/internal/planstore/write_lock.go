package planstore

// 跨进程写锁的接入契约（配合 agentconfig.LockConfigFileWrite / ...WriteAll 使用）。
//
// 为什么 planstore 要接入这把锁：store 的默认根是 $HOME/.aicli/plans，同一台
// 机器上会有多个 aicli 进程写同一份 index.json 与同一份 comments 日志（TUI、
// 内嵌 runtime-server、并发会话）。s.mu 只覆盖**进程内**；跨进程的读-改-写若
// 各写各的，后写者会用自己读到的旧快照整体覆盖先写者——丢轮次、丢评论，Windows
// 上并发 rename 还会直接报 Access is denied（见 concurrency_test.go 的
// TestConcurrentStoresOnSameRootDoNotLoseUpdates）。
//
// 约定与 agentconfig 一致：
//   - **导出的写入入口自己取锁**（Record / Snapshot / SetStatus / PruneVersions /
//     Delete / AppendComment / DeleteComment），且锁必须覆盖整个「读-改-写」（含读取）；
//   - 一次事务涉及多份文件（Delete：index + 评论日志）必须走 LockConfigFileWriteAll，
//     由它按归一化锁键去重 + 字典序取锁，避免交叉死锁；
//   - 包内 `...Locked` 变体（saveIndex / writeCommentsLocked / readCommentsLocked）
//     假定调用方已持锁；
//   - 只读入口（Get / List / Comments / ReadVersion / ReadLatest）不取锁：原子
//     rename 保证它们只会读到旧版或新版的完整文件；
//   - 锁调用点必须直接写 agentconfig.LockConfigFileWrite(All) 的全名：
//     configwriteguard 只按这个名字判定「受保护」（防止用同名伪装助手绕过守卫）。
//
// 锁目录不可用时 agentconfig 会按设计降级为仅进程内互斥（不 panic、不创建目录）。

// commentLogAbsPath 返回某条记录的评论日志绝对路径，仅供取锁使用。路径解析
// 失败（或 id 为空）时返回空串：LockConfigFileWrite("") 是 no-op 释放函数，
// 调用方无需为该情况单独分支。
func (s *Store) commentLogAbsPath(id string) string {
	rel := commentsRelPath(id)
	if rel == "" {
		return ""
	}
	abs, err := s.resolveSnapshotPath(rel)
	if err != nil {
		return ""
	}
	return abs
}
