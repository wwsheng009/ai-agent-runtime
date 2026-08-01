# Supervisor 运维 Runbook（P6-4）

本文档面向 aicli / runtime 运维人员，说明 Child Agent 监督（supervision）与
Team 调度的告警含义、排查路径与恢复动作。对应
`spawn-agent-team-supervision-timeout-recovery-plan.md` 的 P6-4 实施项。

## 1. 健康判定矩阵

| Heartbeat | Progress | Execution deadline | 判定 | 动作 |
|---|---|---|---|---|
| 新鲜 | 新鲜 | 未到 | healthy | 无 |
| 新鲜 | stale | 未到 | stalled | 告警；超过 grace 后 cancel |
| stale | 任意 | 未到 | orphan suspected | 校验 owner/session lease；进入 cancel/recovery |
| 任意 | 任意 | 已到 | execution timed out | cancel；终止或按策略 retry |
| stale | stale | 已到 | orphaned timeout | fencing + reclaim + retry/fail |
| 新鲜 | 等待审批/输入 | 未到 | blocked but healthy | 使用独立 approval/input deadline |
| 任意 | 任意 | 任意 | invalid | 停止新动作；隔离或 cancel；critical 通知父/Lead |

## 2. 告警清单（`EvaluateAlerts` 输出）

告警为只读视图，从 durable store 派生，评估器本身不修改状态。

| Code | Severity | 含义 | 常见原因 |
|---|---|---|---|
| `outbox_backlog` | warning | completion outbox 未投递条目 ≥ 阈值，或最旧条目超过 stale age | 父 mailbox 不存在、投递循环被限流、store 故障 |
| `critical_notification_stale` | critical | critical 级生命周期通知长时间 unresolved | 父会话不可运行、digest 未注入、ack 丢失 |
| `run_progress_stalled` | warning | child run 超过 progress deadline 无进展 | child 卡死、工具调用挂起、输出未回写 |
| `run_orphan_suspected` | critical | run 的 owner lease 过期（heartbeat stale） | host 崩溃、owner 进程被杀、lease 续租失败 |
| `wake_pending_stale` | warning | wake pending 行长时间未被 claim | 父会话 busy/compact 未结束、wake scheduler 未运行 |

默认阈值：outbox backlog ≥ 5 条或最旧 2m；critical unresolved > 2m；
wake pending unclaimed > 2m。可在 `AlertConfig` 中调整。

## 3. 排查路径

### 3.1 `outbox_backlog`

1. 查看 CLI agent graph：`/debug`（或 `/agents panel`）确认 child 是否已 terminal。
2. 若 child 已 terminal 但 parent mailbox 无 completion，检查 completion dispatcher
   日志中 `parent mailbox not found` 类错误。
3. 确认父 Session 存在且未被删除；必要时手动重放 outbox。

### 3.2 `critical_notification_stale`

1. `wait_agent <path>` 输出中查看 `supervision_state` / `reason`。
2. 检查父会话是否 `waiting_approval` / 正在 compact（此时 wake 会排队，不丢通知）。
3. 若父会话已恢复但通知仍 unresolved，检查 preflight digest 是否注入、ack 是否写回。

### 3.3 `run_progress_stalled`

1. `wait_agent <path>` 查看 `last_progress_at` 与 `progress_deadline`。
2. `/agents panel follow` 观察 heartbeat 是否仍新鲜（区分 stalled vs orphan）。
3. 若 heartbeat 新鲜但 progress stale：child 在空转（如等待外部输入）；检查其
   当前工具调用与 pending 状态。
4. 超过 grace 后 supervisor 会请求 cancel；若需立即干预，使用 action 命令 cancel。

### 3.4 `run_orphan_suspected`

1. 确认对应 host/worker 进程是否存活；若已崩溃，等待 lease TTL + grace 后接管。
2. 旧 owner 恢复后不得继续 claim（fencing token 校验）。
3. 若 lease 频繁过期但进程健康，检查时钟偏移与 lease 续租循环日志。

### 3.5 `wake_pending_stale`

1. 确认父会话状态（running / waiting approval / compact）。
2. 若父会话可运行但 wake 未 claim，检查 wake scheduler 是否启动、debounce 是否过长。
3. 手动触发父会话一个 turn 即可消费 wake。

## 4. 单视图判断（P6 验收）

- 仍健康：graph 行 `run_status=running` + `heartbeat=几秒前` + `progress=几秒前`。
- 等待审批：`run_status=waiting_approval` + `approval_deadline=in ...`。
- 无进展：`progress=<分钟级 ago>`，heartbeat 仍新鲜。
- owner 丢失：`run_status=orphaned` 或 `heartbeat=stale` + `run_orphan_suspected` 告警。
- 正在回收：`run_status=cancel_requested|canceling` + `cancel_deadline=in ...`。

## 5. 相关命令

- `/debug`：agent graph（run/attempt/deadline/heartbeat/progress 字段）。
- `/agents panel`、`/agents panel full`：TUI 摘要行（紧凑监督字段）。
- `wait_agent <path>` / `wait_team`：JSON snapshot 含 execution run 监督字段。
- 告警评估：`supervision.EvaluateAlerts(ctx, store, rootScopeID, cfg)`。
