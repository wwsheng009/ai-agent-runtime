# 后台作业 P0 止血实施计划（CAS + 跨实例取消 + 启动不恢复）

> 关联设计文档：`docs/background-job-lifecycle.md`
> 状态：✅ 已实施（2026-09-28，测试全绿；见文末「实施记录」）
> 目标：消除「一直 pending → aicli 启动全量恢复 → 取消复活 → 终态被覆盖」四类症状的主体部分。

## 0. 前置（已完成）

- 存量清理已执行：
  - 备份：`C:\Users\vince\.aicli\.backups\background-20260928-230745.sqlite`（103MB，一致性快照）
  - 3 个 job 的恢复元数据已清理；2 个被竞态覆盖的 `cancelled` 状态已归位（`job_6254babe…`、`job_f19affd7…`）
  - 变更报告：`.aicli/tmp/cleanup_report.json`
- 现场状态：当前 `0 running / 0 pending`，两个常驻服务（8931/8791）已关停。

## 1. 范围（P0）

1. **store 层 CAS + 终态吸收**：终态不可被任何陈旧写覆盖。
2. **Cancel 走 store**：跨实例生效、修复 `JOB_NOT_FOUND`、同事务清恢复计划。
3. **启动恢复改造**：pending → `interrupted`（不入队）；running 死亡 → `orphaned`（不重排）；rerun 仅限运行期且次数受限（默认 3）。
4. **迁移与开关**：可回退。

非目标（留给 P1/P2）：owner_instance/租约、队列 deadline/expired、Job Object/进程组、reaper、pause/resume/requeue。

## 2. 改动清单

### 2.1 store（`backend/internal/background/store.go`、`types.go`）

- `Job` 增加字段 `StateVersion int64`（json: `state_version`）。
- `init()`：迁移加列（PRAGMA table_info 检查后执行）：
  `ALTER TABLE background_jobs ADD COLUMN state_version INTEGER NOT NULL DEFAULT 0`
- 新增 `UpdateJobCAS(ctx, job Job, expectedVersion int64) (updated bool, err error)`：

```sql
UPDATE background_jobs
   SET ..., state_version = state_version + 1
 WHERE id = ? AND state_version = ?
   AND status NOT IN ('completed','failed','cancelled','timed_out','orphaned','interrupted','expired','abandoned')
```

  0 行 → `updated=false`（调用方 re-read 决定重试或放弃）。
- 保留 `UpdateJob` 供迁移/维护路径（注释标注：非并发安全，仅维护使用）。

### 2.2 manager（`backend/internal/background/manager.go`）

- **所有运行期状态写**（`completeJobWithMessage` / `failJobWithCodeAndError` / `markTimedOut` / `orphanJob` / `markCancelled` / running/startup 等）改走 CAS：
  - 成功 → 更新内存 `StateVersion`；
  - 冲突（0 行）→ re-read store：终态 → 放弃并 `managed.scheduled=false`；非终态 → 用新版本重试一次。
- **`cancelJobWithSource`**：
  1. `store.GetJob` 取权威行（不再依赖内存命中 → 修 `JOB_NOT_FOUND`）；
  2. 已终态 → 返回 already-finished；
  3. CAS 写 `cancelled` + `cancel_source` + `finished_at`，**同事务删除 metadata 恢复键**（`recovery_attempt` / `recovery_max_attempts` / `next_recovery_at` / `recovery_reason`）；
  4. 本实例内存有该 job → 原 kill 路径（保持「先终态后 kill」顺序契约）；否则按 store 中 `pid` 尽力 `terminateJobProcess`，失败追加事件 `kill_failed`（P2 reaper 兜底）。
- **启动恢复**：
  - `StatusPending`：默认**不入队**；CAS → `interrupted`，事件 `recovered_pending_ignored`，message = "owner process gone; not auto-resumed"。配置 `recover_pending_on_start=true` 时保留旧行为（过渡开关）。
  - `StatusRunning`：进程存活 → 继续 attach 监控（保持 running）；进程死亡 → CAS → `orphaned`，**不再**走 `recovered_requeued`/rerun 重排。
- `resumeDetachedRecovery` 启动路径不再重排；运行期 watchdog 的 `scheduleDetachedRecovery` 保留，但受次数上限限制。

### 2.3 detached（`backend/internal/background/detached.go`）

- `scheduleDetachedRecovery`：保持 rerun 语义，次数上限取 config（默认 3）。
- `recoverDetachedRunningJob`：仅进程存活时 attach。

### 2.4 config（`backend/internal/config/manager.go` + 传递点）

- `Background.RecoveryMaxAttempts`：`-1` → `3`。
- 新增 `Background.RecoverPendingOnStart bool`（默认 `false`），经 `cmd/aicli/commands/chat_actor_host.go` / `internal/api/runtimeapi/handler.go` 传入 `background.Config`。

### 2.5 测试

- `store_test`：CAS 拒绝过期版本；终态行不可更新。
- `manager_test`：
  - 双 manager 共享同一 store：A `cancel` 后 B 的 complete/orphan 写入被拒，最终状态 = `cancelled`；
  - 启动含 pending 的 store → `interrupted`、无 `running` 事件、无 `recovered_queued`；
  - 启动含 dead running rerun job → `orphaned`、无重排；
  - 内存缺失时 `CancelJob` 仍成功（store 路径）。
- 回归：`go test ./internal/background/... ./internal/config/...` 全绿。

## 3. 迁移与兼容

- 旧库自动加列（`state_version=0` 起步），无需手工操作。
- 升级窗口：老版本进程仍会无视 CAS 写库 → **升级后建议一次性重启所有 aicli 实例**。
- 回滚：行为开关 `recover_pending_on_start`；代码 revert 即可。

## 4. 验收标准

1. 跨实例取消生效，且 5 分钟内不复活（无新 `process_created`）；
2. aicli 重启后：pending 不再批量入队（→ interrupted），running 死亡不重排；
3. 终态不可覆盖（`cancelled` 不会被任何陈旧写改回）；
4. rerun 运行期上限 3；
5. `go test ./internal/background/...` 全绿。

## 5. 实施顺序

1. types/store：CAS + 迁移；
2. manager：写路径全部 CAS；
3. cancel 走 store；
4. 启动恢复改造；
5. config 默认值 + 开关；
6. 测试补齐；
7. 全量回归。

> 工作区存在大量未提交改动，本计划只做增量、不触碰无关文件；改动集中在 `backend/internal/background`、`backend/internal/config` 及对应测试。

## 实施记录（2026-09-28）

- **store**：`state_version` 迁移 + `UpdateJobCAS`（版本校验 + 终态吸收）；`GetJob`/`ListJobs`/`upsertJob`/`PruneJobs` 同步扩展；新增 `interrupted / expired / abandoned` 终态并纳入 `IsTerminalStatus`。
- **manager**：全部运行期写路径改 CAS（`persistJobStateCAS`）；CAS 失败且库侧已终态时**无条件采纳库侧终态**，并跳过失败方的终态事件（`terminalTransitionVisible`）——消除「cancelled 之后又冒出 orphaned」。
- **取消**：`cancelJobWithSource` 在内存缺失时走 `cancelStoredJob`（跨实例生效、修复 `JOB_NOT_FOUND`、同事务清恢复计划、按记录的 pid 尽力杀进程并记 `kill_failed`）。
- **启动恢复**：pending → `interrupted`（事件 `recovered_pending_ignored`，默认不再入队）；running 进程已死 → `orphaned`（不再走 rerun 重排）；`RecoverPendingOnStart` 开关保留旧行为。
- **恢复上限**：`RecoveryMaxAttempts` 默认 3（原 -1 无限）；detached 恢复路径接入 CAS 并受上限约束。
- **配置/接线**：`BackgroundConfig.RecoverPendingOnStart`（yaml/json）+ `chat_actor_host.go` / `runtimeapi handler.go` 构造与配置 key。
- **测试**：新增 5 个用例（CAS 守卫、启动中断/孤儿化、opt-in 恢复、跨实例取消存活、store 回退取消），更新 3 个存量用例；`go test ./internal/background/`、`./internal/config/`、`./internal/toolbroker/`、`runtimeapi -run Background` 全绿；`go vet`、`go build ./...` 通过。

**上线注意**：新行为只在**新构建/新启动**的 aicli 进程生效；旧进程不遵守 CAS。建议一次性重启所有 aicli 实例（重启也会清空各实例内存里的旧队列）。

## P1 实施记录（2026-09-28，同批实施）

- **store**：新增 `runtime_instances` 表与 `owner_instance_id / lease_expires_at / queued_at / deadline_at` 四列（自动迁移）；新增 `UpsertRuntimeInstance`、`MarkRuntimeInstanceStopped`、`RenewJobLeases`、`ReleaseJobLeases`、`ListRuntimeInstances`；租约续期不 bump `state_version`。
- **manager（所有权）**：实例身份（`InstanceID` 可配，默认 `inst_<uuid>`）；**懒注册**（仅在提交任务或已有库打开时注册，空启动不会创建库文件）；心跳循环（默认 10s）刷新实例心跳并续租（TTL 60s）；`Close()` 标记实例 stopped 并**立即释放租约**，对端无需等 TTL 即可接管仍存活的 detached 进程。
- **manager（恢复门控）**：`ownedByLivePeer`（owner 非空且租约未过期）→ 完全不动（不恢复、不派发、不重写）；无主/租约过期 → 沿用 P0 语义（pending→interrupted、死亡 running→orphaned）。
- **manager（队列窗口）**：提交时写入 `queued_at/deadline_at`（`queueTimeout` 默认 30m，负值关闭）；watchdog 每 tick 扫描**未派发**的 pending 任务，超期 → `expired` 终态 + `expired` 事件；`task_output` 新增 `queued_at/deadline_at` 与剩余时间提示；恢复重排会刷新队列窗口（避免旧 deadline 立即过期）。
- **manager（派发复读）**：`dispatchPending` 在 `markScheduled` 前 re-read store——终态采纳、非本实例拥有则丢弃、超期则过期、同步 `state_version`。
- **配置/接线**：`InstanceID / LeaseTTL / HeartbeatInterval / QueueTimeout`（`background.Config`、`RuntimeConfig.Background`、两处构造、handler 配置 key）。
- **测试**：新增 6 个用例（store 实例/租约/所有权往返、活体 owner 跳过、过期租约中断、队列超期 expired、派发前取消不启动、默认值）；`go test ./internal/background/` 全绿（~125s），`./internal/config/`、`./internal/toolbroker/`、`runtimeapi -run Background` 全绿；`go vet`、`go build ./...` 通过。
