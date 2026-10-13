# spawn_subagents 存活槽位配额方案（live-slot quota）

状态：实现中
范围：`backend/internal/subagentbatch`、`backend/internal/subagentquota`（新）、
`backend/internal/agent`、`backend/cmd/aicli/commands`、`backend/internal/api/runtimeapi`

## 1. 问题

主 agent 有两条创建子 agent 的路径，但只有一条受 `agents.maxThreads` 约束：

| 路径 | 入口 | 是否受 `agents.maxThreads` 约束 |
| --- | --- | --- |
| 单发 `spawn_agent` | `reserveOrRegisterLocalAgentSpawn` / `reserveOrRegisterAgentSpawn` → `ReserveAgentControlAgentSpawn` | 是（注册表 `agent_control_agents` 原子 COUNT，超限 `ThreadLimitMessage` 拒绝） |
| 批量 `spawn_subagents` | `SubagentBatchCoordinator.StartBackground` → `SubagentScheduler.RunChildren` → `ChildAgentFactory.Build` | **否** |

批量路径只有“并发度”限制，没有“存活槽位”限制：

- 每批 `agents.maxConcurrent`（默认 4）：只约束**一批内同时执行**的任务数；
- 进程级 `SubagentConcurrencyLimiter`（由 `agents.maxThreads` 构造）：
  注释明确“a slot is held only for the duration of one child execution and is
  released by the goroutine that acquired it”，即只约束**同时执行**的子任务数；
- `agents.maxDepth`、可选的 `maxConcurrentQueueDepth` / `maxConcurrentQueueTimeoutMs`
  （默认 0=关闭）、失败熔断。

结果：一个会话可以无限次派发 `spawn_subagents`。每次派发都在
`subagent_batches` / `subagent_tasks` 账本里留下子会话行（`subagent_<taskID>_<uuid>`），
执行结束只是把任务置为终态，行不会消失，`/agents` 列表（`includeClosed=true`）把它们
全部列出。现场表现为“创建了太多子 agent”，而并发限制器每次执行结束就释放槽位，从未
触及配额。

## 2. 目标

1. 让 `agents.maxThreads` 同时约束**两条路径的存活子 agent 总数**（per root scope），
   对齐 codex 的 `AgentRegistry.reserve_spawn_slot` / `AgentLimitReached` 语义。
2. 超限时给出可操作诊断（复用 `agentcontrol.ThreadLimitMessage`：
   `reuse_existing_child_or_close_idle` + 现有占用者摘要）。
3. 不引入新的持久化身份行，不改变 `/agents` 列表的既有去重/渲染契约。
4. 关闭方式与既有语义一致：`agents.maxThreads == -1` 显式不限；`0` 回落默认（6）。

## 3. 设计

### 3.1 存活计数 = 注册表子行 + 非终态批任务

存活槽位由两处来源相加：

- 注册表：`agentcontrol.ListAgentControlAgents(RootSessionID)` → `QuotaChildren`
  （过滤 root/closed/stale），与单发路径 `enforceSpawnLimits` 完全同口径；
- 批任务账本：`subagentbatch.LiveTaskCounter.CountLiveTasks(RootScopeID)`
  —— 统计状态**非终态**（pending/ready/running）的任务行。

终态任务（succeeded/failed/failed_with_result/canceled/timed_out/skipped）与
closed/stale 注册表行不计入，因此**释放是隐式的**：子任务到达终态即自动归还槽位，
无需显式 release，也不会泄漏。

### 3.2 准入门控（batch 路径）

新增接口（`internal/agent`，宿主实现）：

```go
// SubagentLiveSlotGate 在持久化一个批次之前做存活槽位准入。
type SubagentLiveSlotGate interface {
    AdmitSubagentBatch(ctx context.Context, parentSessionID, rootScopeID string, requested int) error
}
```

`SubagentBatchCoordinatorConfig.LiveSlotGate` 可选注入；`StartBackground` 在
`CreateBatch` **之前**调用 `AdmitSubagentBatch(ctx, opts.ParentSessionID, opts.RootScopeID, len(tasks))`：

- 通过 → 正常创建批次；
- 拒绝 → 直接返回错误，**不落任何批次/任务行**（避免留下幽灵行）；
- `IdempotencyKey != ""` 时跳过准入：幂等重放必须返回既有批次，不能被配额二次拒绝；
- 恢复路径 `admitExistingBatch` 不走准入：崩溃恢复不能被配额阻塞。

### 3.3 闸门实现（`internal/subagentquota`）

新包 `internal/subagentquota`，依赖 `agentcontrol` + `subagentbatch`，不反向依赖宿主：

```go
type Gate struct { /* registry counter + batch counter + maxThreads + per-root mutex */ }

func New(registry RegistryCounter, batches subagentbatch.LiveTaskCounter,
         rawMaxThreads, defaultMax int) *Gate

func (g *Gate) AdmitSubagentBatch(ctx, parentSessionID, rootScopeID string, requested int) error
```

- `rawMaxThreads` 经 `agentcontrol.ResolveMaxThreads` 解析：`<0` → 不限（Gate 返回 nil）；
  `0` → `defaultMax`；`>0` → 配额。
- `scope := rootScopeID`，为空回落到 `parentSessionID`。
- `live = registryQuotaChildren(scope) + CountLiveTasks(scope)`；
  `live + requested > maxThreads` → 返回 `ThreadLimitMessage(maxThreads, live, nil)`。
- 每 scope 一把 mutex，串行化准入，缩小并发批次同时通过造成的超发窗口。
- 计数错误 fail-closed（返回错误），与既有 `enforceSpawnLimits` 一致。

### 3.4 宿主接线

- CLI：`chat_actor_host.go` 构造 `NewSubagentBatchCoordinator` 时注入
  `LiveSlotGate`（registry = `host.AgentRegistryStore`，batches = `host.SubagentBatches`）。
- API：`supervision_batch_store.go` 的 `subagentBatchCoordinator` 注入
  `LiveSlotGate`（registry = `h.getAgentControlAgentStore()`，batches = `h.getSubagentBatchStore()`）。
- 配额来源：两宿主都从 `NormalizeAgentsConfig(runtimeConfig.Agents)` 读 `MaxThreads`。

### 3.5 与既有机制的关系

- 保留 `SubagentConcurrencyLimiter`（并发度）——它解决的是“同时跑几个”；
  本方案新增的是“累计活几个”，二者互补。
- 保留 `agents.autoCloseCompleted`（终态子会话收敛）——它决定是否真正 close 子会话；
  本方案不依赖它，因为槽位释放只取决于任务是否终态。
- 不新增配置项，复用 `agents.maxThreads`。

### 3.6 优化：终态批次留存（retention）

槽位闸门只约束**存活**子 agent；终态批次行（`/agents` 列表里显示为 idle/stopped 的历史）
仍会随会话时长无限累积。为对齐注册表已有的 `agents.registryTerminalRetention`
（默认 30d，负值=永不清除），批账本也接入同一留存窗口：

- `subagentbatch.TerminalBatchPruner`（可选接口）：`PruneTerminalBatches(ctx, olderThan, limit)`
  删除 `finished_at < olderThan` 的**终态**批次及其任务行；非终态批次永不入选。
- sqlite 实现在一个写事务内先删 `subagent_tasks` 再删 `subagent_batches`，
  单次上限 `DefaultTerminalBatchPruneLimit=500`，避免一个超长事务。
- 接线：两个宿主在既有 P2-9 对账的 `Purge` 钩子里，用同一个
  `localRegistryTerminalRetention()` / `agentRegistryTerminalRetention()` 窗口
  best-effort 调用；`retention <= 0` 时跳过（保留=opt-out）。
- 因此 `/agents` 的历史行数也被同一窗口收敛，而不是只靠手动清理。

## 4. 已知边界

- 作用域按 `rootScopeID`（批路径即发起工具的 agent 会话）；嵌套子 agent 再派发时按
  其自身 scope 计数，比真实 root 更保守。顶层主 agent（本问题场景）与注册表配额同域。
- 幂等键存在时跳过准入（保证重放幂等）；无幂等键的正常派发受配额约束。
- 计数跨两张表（注册表 + 批账本），非单事务；每 scope mutex 把超发窗口收敛到
  “多进程同时准入”的极端情况。

## 5. 验证

- `subagentbatch`：`CountLiveTasks` 正确排除终态、按 scope 过滤、跨批次求和；
  `PruneTerminalBatches` 只删过期终态批次（含任务行），保留近期终态与运行中批次。
- `subagentquota`：注册表 + 批任务求和、终态不计、`-1` 不限、超限报错含 next_action。
- `agent`：`StartBackground` 在 gate 拒绝时不落库；gate 通过时正常创建；幂等重放不被拒绝。
- 两宿主 `go build ./...`、`go vet` 通过，`cmd/aicli/commands` 与
  `internal/api/runtimeapi` 全量测试通过。

## 6. 立即收敛既有历史

留存窗口（默认 30d）只约束长期增长；当前会话已累积的终态行不会立即消失。
需要立即收敛时：`/agents cleanup` 走既有注册表回收（`agentcontrol.ReclaimAgentQuota`），
批次账本可等下一次对账节拍（默认 10m）按窗口裁剪，或临时调小
`agents.registryTerminalRetention` 触发一次对账。

## 7. ConPTY 真机验证

驱动脚本：`backend/scripts/live-slot-quota-conpty-test.py`（pywinpty 真 ConPTY +
本地 mock OpenAI provider，无真实模型）。它写入 `$HOME/.aicli/config.yaml`（provider）
与 `$HOME/.aicli/runtime.yaml`（`agents.maxThreads`——注意 `agents.*` 属于 runtime.yaml
层，写进 config.yaml 会被全局加载器告警并忽略），然后用 `aicli chat --yolo --trust`
派发 `spawn_subagents`，从 mock 请求日志（权威）与终端屏幕取证。

判定基于工具回执文本，而不是终端重绘：拒绝时回执必须含
`agent spawn thread limit reached`、`max_threads=<N>`、`active_children=<live>`、
`next_action=reuse_existing_child_or_close_idle`；放行时回执含 `batch_id`。

三个场景 + 红绿对照（`backend/aicli-quota.exe` = 含本修复的构建）：

| 场景 | 配置 | 期望 | 结果 |
| --- | --- | --- | --- |
| single-reject | maxThreads=1, agents=2 | 拒绝（0+2>1） | PASS |
| single-admit | maxThreads=6, agents=2 | 放行（0+2≤6，回执含 batch_id） | PASS |
| cumulative | maxThreads=2, agents=2，第一批子任务保持存活时再派第二批 | 拒绝（live=2，2+2>2） | PASS |
| 红绿对照 | 修复前 `aicli-4x.exe`，maxThreads=1, agents=2 | 未加闸门 → 放行 → 用例失败 | FAIL（符合预期） |

产物目录：`output/conpty-live-slot-quota/<timestamp>/{screen-visible.txt,mock-requests.jsonl}`。
红绿对照证明用例能区分“有闸门 / 无闸门”，不是空过。

复现命令：

```
python backend/scripts/live-slot-quota-conpty-test.py --expect reject --max-threads 1 --agents 2
python backend/scripts/live-slot-quota-conpty-test.py --expect admit  --max-threads 6 --agents 2
python backend/scripts/live-slot-quota-conpty-test.py --scenario cumulative --max-threads 2 --agents 2 --child-delay 8
```
