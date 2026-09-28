# 后台作业生命周期优化方案

> 状态：提案（待评审）｜范围：`backend/internal/background`（Manager / SQLite Store / Detached 监控）、`internal/toolbroker`（task_* 工具）、运行时配置
> 背景：多个 aicli 实例共享同一个 `background.sqlite`，出现「任务排队数天不执行」「aicli 一启动就全量恢复」「取消后自动复活」「孤儿进程占端口」等问题。

---

## 1. 现场证据（2026-09-28 实测）

| # | 问题 | 证据 | 代码点 |
|---|------|------|--------|
| 1 | 无 owner：N 个实例共享一个 store、各自内存调度（每实例 4 槽） | 6123 条 `recovered_queued` / 86 个任务；同一 job 秒级多次 `process_created`；终态互写 | `manager.go:1254 dispatchPending` |
| 2 | pending 无生命周期：无 deadline/owner | 任务排队 3 天（09-25→09-28） | store 无相关列 |
| 3 | 启动即恢复：启动时把库里所有 pending 重新入队、running 全量接管 | 每次新开 aicli 触发一批 `recovered_queued` | `manager.go:1545-1608`、`detached.go:131` |
| 4 | 取消非原子、非全局：只操作调用方内存 + 单 pid kill + 不清恢复计划 | `job_1b44c1da` 14:32:38 cancelled → 14:32:41 被改写 completed → 连续 6 轮复活至 14:49；跨实例取消报 `JOB_NOT_FOUND` | `manager.go:425`、`broker.go:1706` |
| 5 | 进程树控制弱：只记录单个 pid，重试派生多棵树 | 8791 端口两次被孤儿 node 占住 | `detached.go:747 taskkill /PID x /T` |
| 6 | 无版本/CAS：终态可被陈旧写覆盖 | cancelled→orphaned、completed→running 反复出现 | store 无 `state_version` |

## 2. 设计原则

**一 job 一 owner；显式生命周期；终态吸收；取消原子且全树；默认不自动恢复（opt-in）；队列有界；实例退出 = 任务收敛为可控终态，而不是等待被任意实例恢复。**

## 3. 目标状态机

```
queued ──dispatch──▶ running ──▶ {completed, failed, cancelled, timed_out, orphaned}
   │
   ├─ deadline 到 ──────▶ expired        （不执行，终态，用户可见）
   ├─ owner 实例退出 ───▶ interrupted    （终态；不自动跑）
   └─ 用户放弃 ─────────▶ abandoned
```

- 新增终态：`interrupted / expired / abandoned`（当前仅 `orphaned`，语义含糊）。
- 仅 `persist_across_restart=true`（新属性，默认 false）的常驻服务允许跨实例重启接管（租约抢锁）；其余一律不接管。
- 任何终态可由用户显式 `requeue`（= 新建 job）；系统永不自动重放。

## 4. 存储层改造（SQLite migration）

`background_jobs` 增列：

```sql
ALTER TABLE background_jobs ADD COLUMN owner_instance_id TEXT;
ALTER TABLE background_jobs ADD COLUMN owner_session_id  TEXT;
ALTER TABLE background_jobs ADD COLUMN state_version     INTEGER NOT NULL DEFAULT 0;  -- CAS
ALTER TABLE background_jobs ADD COLUMN queued_at         TEXT;
ALTER TABLE background_jobs ADD COLUMN deadline_at       TEXT;      -- 队列 TTL
ALTER TABLE background_jobs ADD COLUMN lease_expires_at  TEXT;      -- owner 租约
ALTER TABLE background_jobs ADD COLUMN runner_pid        INTEGER;
ALTER TABLE background_jobs ADD COLUMN process_group     TEXT;      -- 进程组/JobObject
ALTER TABLE background_jobs ADD COLUMN recovery_attempts INTEGER DEFAULT 0;
ALTER TABLE background_jobs ADD COLUMN next_recovery_at  TEXT;      -- 从 metadata 提升为列
ALTER TABLE background_jobs ADD COLUMN persist_across_restart INTEGER NOT NULL DEFAULT 0;
ALTER TABLE background_jobs ADD COLUMN cancel_source     TEXT;
```

新增实例表：

```sql
CREATE TABLE IF NOT EXISTS runtime_instances (
  instance_id  TEXT PRIMARY KEY,
  pid          INTEGER,
  host         TEXT,
  started_at   TEXT,
  heartbeat_at TEXT,
  state        TEXT
);
```

所有状态写改 **CAS（compare-and-swap）**：

```sql
UPDATE background_jobs
   SET status = ?, state_version = state_version + 1, ...
 WHERE id = ? AND state_version = ? AND status NOT IN (<terminal>);
```

影响行数 = 0 → 读回放弃。**终态在 SQL 层不可逆**，根治 cancelled→orphaned/completed 覆盖。

**迁移**：存量 pending（owner 未知）→ `interrupted`，清空恢复元数据；存量 running 探活，死的 → `orphaned`。避免升级后被全量拉起。

## 5. 实例注册与租约（治多实例）

- 每个 aicli / runtime-server 进程启动注册 `instance_id`，10s 心跳，TTL 60s。
- **调度、watchdog、状态写只允许 owner 实例**；其他实例只读。
- 新启动流程（替换 `manager.go:1545` 全量恢复）：

```
register instance
adopt:   仅本 instance 名下非终态 job（自身重启恢复）
others:  lease 有效 → skip
         lease 过期 + queued  → CAS interrupted            # 不入队！
         lease 过期 + running → 探活(pid + process_identity)
              活着 → 标记 lease_expired，等 reaper/用户决定
              死了 → CAS orphaned（仅显式 rerun 策略且次数未耗尽才重排）
绝不重新入队他人任务
```

## 6. 取消（原子 / 全局 / 全树）

1. `Cancel(jobID)` 直接对 store 做 CAS 事务：非终态 → `cancelled` + `cancel_source` + `finished_at`，**同一事务清 `next_recovery_at` / `recovery_attempts`**，写事件；提交后再杀进程（保留「先终态后 kill」顺序契约，见 `monitor_test.go:135`）。
2. **不依赖调用方内存**（修 `JOB_NOT_FOUND`）：owner 在则由 owner 杀；owner 不在则由调用方按 `process_group` 杀，失败进 reaper。
3. 恢复 / watchdog / dispatcher 每次动作前 **re-read store**，终态即停；`cancelled` 永不复活。

## 7. 队列与超时（治「一直 pending」）

- `queue_timeout`（默认 30m）：`deadline_at` 到 → `expired` 终态 + 用户可见通知。
- 每实例队列只含自己的 job；`task_output` 暴露 `queue_position / queued_at / deadline_at`。
- 显式操作：`pause / resume / cancel / requeue`。
- `max_concurrent_jobs = 4` 保持 per-instance；一 job 一 owner → 不再 N 倍重复执行。

## 8. 恢复策略（默认不恢复）

| 策略 | 行为 |
|------|------|
| `fail`（默认） | 进程丢失 → orphaned，**不重跑** |
| `rerun`（显式） | 仅声明者；`max_attempts` 默认 **3**（替代当前 -1 无限）；退避 30s/1m/2m/3m/5m；**取消即清**；每次重排前 CAS 校验 + 探活 |
| `persist`（新） | 常驻服务专用；允许跨重启接管（租约） |

watchdog 只运行在 owner 实例、只处理自己的 job。

## 9. 进程树与孤儿回收

- 启动时用 **Windows Job Object（KILL_ON_JOB_CLOSE）/ Unix setsid + pgid** 包裹；记录 `runner_pid + pgid + 子进程`。
- 新增 reaper（owner 实例，30s 周期）：
  - 终态 job 仍有活进程 → 按组杀；
  - running 但 pid 死 / 心跳超时 → 按策略收敛；
  - 杀不掉 → 重试 + 告警。
- 取消/失败后校验进程树消失（含端口释放），否则重试。

## 10. 可观测性

- job 列表新增：owner、queued_at、deadline、queue_position、attempts。
- 新事件：`interrupted / expired / abandoned / adopted / lease_expired / kill_failed`。
- expired / interrupted 时给用户明确提示（「排队超时未执行」），而不是静默。

## 11. 配置项

```yaml
background:
  recover_on_start: false        # 新增，默认关闭
  queue_timeout: 30m             # 新增
  rerun_max_attempts: 3          # 调整（原 -1 无限）
  lease_ttl: 60s                 # 新增
  orphan_reaper_interval: 30s    # 新增
  max_concurrent_jobs: 4         # 保持（per instance）
```

## 12. 分阶段落地

| 阶段 | 内容 | 工作量 |
|------|------|--------|
| **P0 止血** ✅ 2026-09-28 | CAS + 终态不可覆盖；cancel 走 store；启动恢复改为「标记 interrupted」；存量清理 | 0.5–1 天 |
| **P1 控制面** ✅ 2026-09-28 | owner_instance + 租约 + 实例注册；队列 deadline/expired；取消清恢复计划；dispatcher re-read | 1–2 天 |
| **P2 进程树** | Job Object / pgid；reaper；kill 校验 | ~1 天 |
| **P3 体验** | pause / resume / requeue；事件与 UI；配置项 | ~0.5 天 |

**回归测试清单**：多实例并发同一 job 只跑一次；取消后 5 分钟不复活（跨实例）；pending 超时 → expired；rerun 超限停止；启动不恢复他人任务；终态不可覆盖；取消/失败后 30s 内进程树消失；孤儿进程/端口自动清理。

## 13. 验收标准

1. 取消后跨实例 5 分钟不复活；
2. pending 超时即终态；
3. aicli 重启不恢复他人任务（只标 interrupted / orphaned）；
4. 终态不可被任何陈旧写覆盖；
5. 取消/失败后 30s 内进程树 + 端口清理完毕；
6. 多实例下同一 job 恰好执行一次。

## 14. 实施状态（2026-09-28）

- **P0 已实施**：`state_version` + `UpdateJobCAS`（终态吸收）；写路径全量 CAS；cancel 走 store（跨实例、修 `JOB_NOT_FOUND`、清恢复计划）；启动恢复 pending→`interrupted`、死亡 running→`orphaned`（不重排）；`RecoveryMaxAttempts` 默认 3；`RecoverPendingOnStart` 开关。
- **P1 已实施**：`runtime_instances` 实例表；job 绑定 `owner_instance_id` 与租约（心跳 10s / TTL 60s，Clean 关闭立即释放租约）；恢复只处理「无主或租约过期」的任务（活体持有者完全不动）；队列窗口 `queued_at/deadline_at`（默认 30m，仅对未派发任务生效，超期 → `expired`）；派发前 re-read store（终态采纳 / 非本实例丢弃 / 同步版本）。
- **待实施**：P2（进程组/Job Object、reaper）与 P3（pause/resume/requeue、事件与 UI 补全）。

> 新配置：`background.instanceId / leaseTTL / heartbeatInterval / queueTimeout`（默认 60s / 10s / 30m；`recoverPendingOnStart` 默认 false）。

## 附 A：存量数据清理（一次性）

1. 备份：`VACUUM INTO` 生成一致性快照至 `~/.aicli/.backups/`。
2. 终态 job：删除 metadata 中的恢复字段（`next_recovery_at` / `recovery_reason` / `recovery_attempt` / `recovery_max_attempts` 等）。
3. 竞态覆盖归位：存在 `cancelled` 事件但被 watchdog 改写为 `orphaned` 的 job → 恢复为 `cancelled`（保留 cancel_source）。
4. running 且进程已死 → `orphaned`；pending（owner 未知）→ `interrupted`。
5. 输出变更报告（JSON）留档。

## 附 B：代码锚点索引

- 启动恢复：`backend/internal/background/manager.go:1545-1608`
- 取消：`manager.go:417-470 cancelJobWithSource`、`manager.go:1090 markCancelled`
- 终态写入：`manager.go:927 completeJobWithMessage`、`:1021 markTimedOut`、`:1054 orphanJob`
- 恢复：`detached.go:131 recoverDetachedRunningJob`、`detached.go:403 scheduleDetachedRecovery`、`:747 terminateProcess`
- 调度：`manager.go:1224 dispatchPendingSafely`、`:1254 dispatchPending`、`:1242 notifyDispatcher`
- 工具入口：`internal/toolbroker/broker.go:1687 ToolTaskKill`（alias 解析 + CancelJob）
- 存储：`internal/background/store.go:721 init`（建表）
