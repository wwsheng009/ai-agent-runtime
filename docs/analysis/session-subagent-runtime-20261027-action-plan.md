# 会话 `session_20261027184337_110OagGl` 子 Agent 运行核查与代码优化建议行动文档

- **文档日期**：2026-10-27
- **分析仓库**：`E:\projects\ai\ai-agent-runtime`
- **被分析工作区**：`E:\projects\sap\sap_network_core`
- **被分析会话**：`session_20261027184337_110OagGl`
- **调试端点**：`http://127.0.0.1:58301/debug/endpoints`
- **本轮变更边界**：只新增本行动文档，不修改运行时代码、测试代码或目标工作区。

> 本文把证据分成四类：
>
> - **[实测]**：直接来自目标会话持久化日志、`cache.analytics.v1` 快照或端点探测；
> - **[源码]**：直接来自 `ai-agent-runtime` 当前源码；
> - **[推断]**：由多项证据组合得到的高可信解释，但仍需要专项测试确认；
> - **[待验证]**：当前证据不足以确定，不能当成已修复或确定根因。

## 1. 结论摘要

### 1.1 总体评价

本次主会话的**顶层 turn 最终成功结束**，但运行效率偏低，子 Agent 可靠性明显不足：

1. **主会话执行结果 [实测]**：运行约 3,072,105 ms（约 51 分 12 秒），完成 87 个 LLM 请求、232 次工具调用，最终 `session_end.success=true`；工具错误 4 次，均被记录为已恢复（`recovered_tool_error_count=4`）。
2. **请求层 [实测]**：root-scope `cache.analytics.v1` 快照共有 103 个 LLM 请求，其中包含 16 个 child 请求；79 个命中、22 个 `reported_zero`、2 个 error，按请求条数计算 error rate 约 **1.94%**，但该指标不能等同于主 turn 失败率。
3. **主会话事件层 [实测]**：87 个 `llm.request.started` 与 87 个 `llm.request.finished` 成对出现，finished 事件均未标记失败；另有 3 次 provider wrapper transport retry。也就是说，`llm.request.finished` 的成功并不能证明整个 child task 成功。
4. **scheduler 子任务 [实测]**：3 个只读 scheduler task 全部终态失败（3/3，100%），每个只观察到 `attempt=1/max_attempts=2`，没有观察到 `attempt=2`。batch 创建约 30.512 秒后进入 `timed_out`，三个任务分别在约 29.1–29.6 秒完成为 `context deadline exceeded`。
5. **另一个直接 child [实测]**：`agent_controller` child 运行约 316,170 ms，使用约 636,070 tokens，cache read 185,600 tokens，并非“没有运行”；最终因只读策略阻止了非只读 PowerShell `Get-Item` 命令而失败，仍产生了部分分析结果。
6. **缓存 [实测+源码]**：child 并非完全没有 cache hit。三个 scheduler child 的 cache ratio 分别约 90.90%、45.83%、41.31%，但存在冷启动、`reported_zero` 与 error request。child 使用独立 session ID；源码生成的 prompt cache key 以 child session ID 和 epoch 为基础，因此 fork 历史复制不等于复用父 cache namespace。
7. **context [源码]**：默认 child 是 fresh context；系统支持显式 fork，但 fork 不是 live shared context，而是创建时对父会话**已完成轮次**做一次性深拷贝快照。`inherit_parent_when_missing` 属于 provider/model/reasoning 路由继承，不代表继承聊天历史，也不代表共享 prompt cache key。

### 1.2 严重性分级

| 优先级 | 问题 | 判断 |
|---|---|---|
| P0 | batch deadline 与 child route timeout 叠加，重试预算在首次失败时已耗尽 | 已由时间线强烈指向；需补充 deadline 观测字段确认具体取消者 |
| P0 | scheduler 在 `runErr != nil` 失败分支没有把 `result.Usage`/部分产出带回父任务回执 | 源码已确认，能解释“child 有 request usage 但 task usage=0” |
| P0 | `max_attempts=2` 只表示上限，不保证第二次执行；当前日志没有 attempt-2 的可诊断事件 | 源码与实测共同确认，需改进重试生命周期和可观测性 |
| P1 | request-level、attempt-level、task-level、batch-level success 混在分析侧，导致成功请求被聚合成失败 task | 已观测，需统一数据模型，不应简单改成“全局成功” |
| P1 | child cache lineage、parent/child key、epoch、fork snapshot 没有足够可视化字段 | 源码有部分 metadata，但 analytics 查询面不足 |
| P1 | provider `reported_zero` 与 confirmed miss 未分层，错误请求无 usage 时又容易被看成 cache 丢失 | 已观测，需扩展 usage/cache 状态枚举 |
| P1 | 只读 child 的工具策略在启动前已有警告，但没有在模型生成错误命令前以强约束方式减少失败 | 本次 direct child 已实测；应增加工具面 preflight 与失败分类 |
| P2 | 只读探索出现 12 个连续 step，主会话在 step 30 记录 exploration stall；事件渲染还有一次 route event suppression | 影响效率和诊断，不是本次 child 失败的唯一根因 |

## 2. 分析范围与证据边界

### 2.1 使用的持久化证据

主要证据文件：

```text
C:\Users\vince\.aicli\chat-logs\2026\10\27\session_20261027184337_110OagGl\
  chat\chat.json
  events\runtime-events.jsonl
  debug\debug.log
```

关键持久化统计：

- `runtime-events.jsonl`：2,228 行事件；
- `tool.requested/completed/reduced`：232/232/232；
- `tool.progress`：944；
- `llm.request.started/finished`：87/87；
- `subagent.task.started/completed`：3/3；
- `subagent.completed`：4；
- `llm.retry`：3；
- `context.mid_turn_compact.started/completed`：3/3；
- `subagent.batch.created/timed_out`：各 1；
- `context.usage_over_budget`：1；
- `tool_loop.exploration_stall_observed`：1。

此前通过调试端点取得的 `cache.analytics.v1` 快照用于 child cache 分析。当前复核时再次探测端点得到连接失败；因此本文不把端点掉线当作目标业务失败，而以持久化日志和已保存快照为准。

### 2.2 不应混淆的四种成功/失败口径

| 层级 | 代表对象 | 本次表现 | 正确解释 |
|---|---|---:|---|
| Provider request | 单个 `llm.request.*` | 主事件 87/87 finished 成功；root 聚合有 2 error | 请求是否拿到 provider 响应 |
| Attempt | 同一个 task 的第 N 次执行 | 只看到 scheduler attempt 1 | 是否真的发生过一次重试，不能由 `max_attempts` 推断 |
| Task | `original-analysis` 等 | 3/3 失败 | 子任务是否交付了可接受终态 |
| Batch | `batch_78ec9dfba4c2e361` | timed_out | 整批是否在 deadline 内完成并收敛 |

因此，“child 有成功 LLM request”与“child task 成功”可以同时成立：child 可能在任务收尾、工具链或 deadline 阶段失败，provider request 记录仍然成功。

## 3. 运行历史指标与效率评估

### 3.1 主会话与 root-scope 请求

| 指标 | 实测值 | 说明 |
|---|---:|---|
| 主会话墙钟时间 | 3,072,105 ms（约 51.2 分钟） | `session_end` / `agent.turn.finished` |
| 主会话 LLM finished | 87 | 事件日志中 87 个成功 finished；没有直接 duration 字段 |
| root-scope analytics 请求 | 103 | 包含主会话 87 个请求和 child 16 个请求 |
| root-scope cache hit | 79 | 约 76.70%（按请求条数）；analytics 的 token 加权 `cache_hit_ratio` 为 72.10% |
| root-scope `reported_zero` | 22 | 约 21.36%，不应直接等同于真实 miss |
| root-scope request error | 2 | 约 1.94%；错误请求通常没有完整 usage |
| root-scope cache read tokens | 8,607,744 | provider reported aggregate |
| cache creation tokens | 0 | 更像 provider/adapter 没有上报或该 provider 不支持，待验证 |
| 平均请求耗时 | 28,152 ms | `cache.analytics.v1`，包含 root-scope请求 |
| 平均首 token | 16,517 ms | `cache.analytics.v1`，高首 token 延迟明显影响效率 |
| provider wrapper retry | 3 | 均为 `UPSTREAM_UNAVAILABLE` + `retry_reason=transport` |
| mid-turn compaction | 3 次 | 每次都发生在接近 240k prompt budget 的压力区间 |
| tool calls | 232 | 工具调用量高，主会话大量时间花在探索/证据整理 |
| tool error | 4 | 约 1.72%；session_end 显示 4 次均 recovered |
| unrecovered tool error | 0 | 顶层 session 最终没有未恢复工具错误 |

> **效率判断**：主会话的“成功”主要依赖长时间探索、工具错误恢复、三次压缩和等待/恢复机制，属于**结果可交付但成本偏高**。87 个 LLM 请求在约 51 分钟内完成，平均墙钟间隔约 35 秒；这不是单纯 provider 速度问题，也包含工具执行、压缩、子任务等待和上下文管理成本。

### 3.2 子 Agent 运行指标

#### scheduler batch

| task | duration | attempt | max attempts | status | error |
|---|---:|---:|---:|---|---|
| `original-analysis` | 29,133 ms | 1 | 2 | failed | `context deadline exceeded` |
| `current-analysis` | 29,151 ms | 1 | 2 | failed | `context deadline exceeded` |
| `docs-runtime-analysis` | 29,618 ms | 1 | 2 | failed | `context deadline exceeded` |

batch 时间线：

```text
10:48:28.286Z  subagent.batch.created，execution_mode=background
10:48:28.937Z  三个 subagent.task.started
10:48:58.308Z  original/current/docs 依次进入 failed completion
10:48:58.829Z  batch.timed_out，elapsed_ms=30512，failed_count=3
```

三个 task 基本同时在约 30 秒边界附近失败，符合“batch deadline / route timeout / provider context cancellation 叠加”的表现。当前日志没有记录 child context 的绝对 deadline、剩余预算、取消来源，故不能仅凭 `UPSTREAM_UNAVAILABLE` 断言是 provider 本身超时。

#### 直接 child `agent_controller`

该 child 的运行记录应单独看待：

- `duration_ms=316170`；
- `steps=8`；
- `usage_total_tokens=636070`；
- `usage_prompt_tokens=623667`；
- `usage_completion_tokens=12403`；
- `usage_cache_read_tokens=185600`；
- 终态失败原因为只读策略阻止了非只读 shell 命令；
- 该 child 产出了部分分析摘要，不能归入“没有运行”或“零 token”。

若按本次 `subagent.completed` 生命周期事件计，4 个 child 中 4 个终态均为失败；但这个 100% 是**task/lifecycle failure rate**，不能替代 provider request error rate，也不能否定 direct child 已产生的有效中间产出。

### 3.3 主会话失败率建议口径

建议在运营报表中同时展示以下指标，而不要只给一个“错误率”：

```text
provider_request_error_rate = provider_error_requests / provider_requests
attempt_retry_rate          = attempts_after_first / total_attempts
child_task_failure_rate      = failed_tasks / terminal_tasks
batch_timeout_rate           = timed_out_batches / terminal_batches
unrecovered_tool_rate        = unrecovered_tool_errors / tool_calls
partial_output_preservation = failed_tasks_with_usage_or_findings / failed_tasks
```

本次可计算的样本值：

- provider/root request error：2/103 ≈ 1.94%；
- scheduler task failure：3/3 = 100%；
- all observed child lifecycle failure：4/4 = 100%；
- scheduler attempt-2 observation：0/3；
- unrecovered top-level tool error：0/232；
- batch timeout：1/1，但样本量只有 1，不能外推长期 SLA。

## 4. 子 Agent context 机制核查

### 4.1 结论先行

| 问题 | 结论 |
|---|---|
| 普通 child 是否继承主会话历史？ | **默认不继承**。普通 fresh task 的 `History` 为 nil/空，child 使用自己的 session ID 和自己的 loop run。 |
| 是否每次新建 context？ | **每次创建新的 child Agent/loop/session 执行上下文**，但会复用运行时资源、事件总线、MCP manager、prompt builder，并克隆部分配置/策略；这不是父会话 live context 的共享。 |
| 是否支持继承主会话历史？ | **支持显式 fork**，但需要 capability 和 task 参数同时满足；不是默认行为。 |
| fork 是实时同步吗？ | **不是**。它是创建时父会话已完成轮次的冻结快照，父会话后续新增历史不会自动进入 child。 |
| `inherit_parent_when_missing` 是否表示历史继承？ | **不是**。它是 model routing 中 provider/model/reasoning 缺省值继承策略。 |
| fork 是否天然共享父 prompt cache key？ | **不能这样认为**。源码的 key 生成以本次 run 的 `sessionID`/epoch 为基础；历史快照和 cache namespace 是两个独立概念。 |

### 4.2 `subagent_spawn_batch` 路径

源码证据：

- `SubagentTask.Context` 的空值/`fresh` 表示 fresh，`fork` 表示请求父历史快照（`backend/internal/agent/scheduler.go:79-83`）。
- `SubagentRunOptions.ParentHistory` 由宿主注入；没有 host history 时 fork 会返回可操作错误，不会静默退化成 fresh（`scheduler.go:251-255`、`child_factory.go:199-216`）。
- coordinator 只有在批次中至少存在一个 `context=fork` 任务时，才读取父会话历史（`subagent_batch_coordinator.go:1466-1473`）。
- child factory 构造独立 child Agent，并生成 `subagent_<task>_<uuid>` 的独立 session ID（`child_factory.go:152-195`、`scheduler.go:1576-1579`）。
- scheduler 把 `spec.ForkHistory` 放入 `loop.run` 的 `History`，但不挂 `PersistHistory`，所以该 fork snapshot 不经 scheduler child session 的持久历史回写（`scheduler.go:1114-1116`）。

fork snapshot 的具体规则：

1. 去掉父历史中的 system 消息，子级使用自己的 system prompt；
2. 去掉末尾未配平的 user 尾巴，只保留已完成对话前缀；
3. 深拷贝并冻结；
4. 默认快照 token 上限为 96,000，超限整体拒绝，不静默截断；
5. 父会话后续追加的 turn 不会进入已经创建的 child。

### 4.3 直接 `subagent_spawn` 路径

`backend/cmd/aicli/commands/chat_actor_registry.go` 中另有本地 actor/session 路径：

- `fork_turns=none` 或未启用 fork 时创建 `runtimechat.NewSession`，即 fresh session；
- `fork_turns=all`、`fork_turns=N` 或 `fork_context=true` 时先 `parentSession.Clone()`，`last:N` 再替换为最近消息；
- 无论 fresh 还是 fork，都会复制父/根 session、workspace、profile、depth、parent tool call 等元数据；这些元数据复制不等于聊天历史复制；
- `DefaultForkTurns` 的内建默认值是 `none`，因此默认仍是 fresh。

这解释了“看起来 child 继承了很多东西但模型却不记得父对话”的现象：workspace/profile/route/tool policy 可以继承，history 仍然可以是空的。

### 4.4 能力开关和默认状态

`localSubagentCapabilities` 将 `agents.forkContextEnabled` 映射到 `ForkContext` capability；`DefaultCapabilities()` 中 `ForkContext=false`。因此：

```text
普通 fresh：默认可用
context=fork：默认拒绝，除非部署显式开启 agents.capabilities.fork_context
fork + provider/model override：即使 fork 已开启也应拒绝路由覆盖，以保护 fork 的 route/cache 约束
```

这一设计是安全的，但当前运行日志没有记录本次 batch 是否请求了 `context=fork`。因此本次三个 scheduler child 应按 fresh 路径分析，不能把它们的 cache miss 解释为 fork 继承失败。

## 5. Cache 运行事实与“缓存丢失”判断

### 5.1 child cache 事实

此前保存的 `cache.analytics.v1` 分 child 统计如下：

| child session | requests | 有 usage 请求 | cache hit ratio | hit | reported_zero | error | cached/read tokens |
|---|---:|---:|---:|---:|---:|---:|---:|
| `subagent_current-analysis_613f1c9b3ce34c7d8ab77b7f117ae102` | 3 | 2 | 90.90% | 2 | 0 | 1 | 37,120 |
| `subagent_original-analysis_8ba7fbad96fb48d1837cc29d236c3f57` | 3 | 2 | 45.83% | 1 | 1 | 1 | 20,096 |
| `subagent_docs-runtime-analysis_a31041af36eb4796a7bcfaa5d53562a5` | 2 | 2 | 41.31% | 1 | 1 | 0 | 20,096 |

可确认：

- child 内部存在后续 cache hit；
- child cache 不是完全失效；
- 首次请求冷缓存是正常可能性；
- `reported_zero` 表示 provider 报告 cached/read 为 0，但不能仅凭该字段确认 provider 做了真实 cache miss；
- error request 没有完整 usage 时，会同时拉低 task usage 与 cache analytics 的可解释性；
- child 使用独立 session ID，因此默认不会天然复用父 session 的显式 cache key。

### 5.2 源码中的 key 机制

`backend/internal/agent/loop.go:5948-5958`：

```text
promptCacheKeyForEpoch(sessionID, epoch)
  epoch <= 0  -> sessionID
  epoch > 0   -> sessionID + "#prompt-cache-epoch-" + epoch
```

`think()` 在请求 metadata 中写入：

```text
prompt_cache_epoch
prompt_cache_key
session_id
parent_session_id / root_session_id / subagent_id（child lineage）
```

因此：

- fresh child 的 key 是 child session ID；
- parent 的 key 是 parent session ID；
- fork 只改变 `History`，不会自动把 child `sessionID` 替换成 parent session ID；
- compaction/history replacement 会增加 cache epoch，避免 provider 将新前缀误当旧前缀；
- 显式复制历史与复制 provider cache namespace 必须分开测试。

源码注释在 `subagent_context.go` 中提到 fork 的“KV Cache 保持”设计，但当前显式 key 函数仍以 child session ID 为主。这里存在**设计语义与可观测 key 之间的潜在不一致**：可能依赖 provider 的内容前缀缓存，也可能导致显式 namespace 无法复用。当前没有 fork 真机对照数据，不能直接判定 provider 是否真的复用了 KV cache。

### 5.3 对缓存问题的准确结论

不应写成“子 agent 缓存系统坏了”或“所有 child cache 都丢失”。更准确的结论是：

> child 使用独立 cache namespace；child 内部存在命中，但冷启动、reported_zero、error request 以及 task failure 对 usage 的吞噬，造成命中率和成本观测不稳定。fork history 继承目前不能证明会复用父显式 cache key。

## 6. 已确认的代码问题与优化建议

### 6.1 P0：修复任务级 retry 的 deadline 预算与观测

**证据**：

- `scheduler.go:1031-1035` 在 route timeout 上建立 `childCtx`；
- `scheduler.go:1099-1129` 只有 `runErr != nil` 才进入重试判断，并且 `shouldAutoRetrySubagentTask` 要求 `childCtx.Err()==nil`；
- 本次三个 task 均在 batch 约 30 秒边界失败，只有 attempt 1；
- `max_attempts=2` 只是上限，不是已执行次数。

**影响**：

- 第一轮失败后，剩余 deadline 不足以执行第二轮；
- 父侧看到 `retryable=true` 与 `retry_advice=retry_with_changed_inputs`，但实际没有 attempt 2，容易误导模型和运维；
- batch 级 timeout、task route timeout、provider response-header guard、retry backoff 之间没有清晰预算关系。

**建议修改文件**：

```text
backend/internal/agent/scheduler.go
backend/internal/agent/subagent_retry.go
backend/internal/agent/subagent_batch_coordinator.go
backend/internal/subagentbatch/（任务/批次状态模型）
```

**实施动作**：

1. 在每次 attempt 开始前计算并写入：`attempt_started_at`、`attempt_deadline`、`batch_deadline`、`remaining_ms`、`retry_budget_ms`。
2. 重试准入必须同时满足：剩余时间 > backoff + 最小可执行 attempt 时间；否则将原因标成 `retry_skipped_deadline_budget`，而不是只保留 `retryable=true`。
3. 将 batch deadline、route timeout、provider transport timeout 分层记录取消来源：`cancel_source=batch_deadline|task_timeout|provider_timeout|parent_cancel`。
4. 新增成对事件：`subagent.attempt.started`、`subagent.attempt.finished`；`subagent.completed` 只表示 task 最终终态，避免中间失败和最终失败共享同一事件名造成聚合歧义。
5. 若产品要求“至少一次重试”，必须从 batch 开始时预留 retry budget；否则产品语义应明确为“最多 N 次尝试”。

**验收**：

- 通过注入第一次 `UPSTREAM_UNAVAILABLE` 的 fake executor，能看到 attempt 1、attempt 2、最终 task 事件；
- deadline 不足时只看到 attempt 1，并明确 `retry_skipped_deadline_budget`；
- batch timeout 不再覆盖/吞掉 task 的真实取消来源。

### 6.2 P0：保留失败 attempt 的 usage 和部分产出

**证据**：

`backend/internal/agent/scheduler.go:1143-1165` 的 `runErr != nil` 分支构建 `SubagentResult` 时没有设置 `Usage: result.Usage`，也没有带回 `Findings/Patches/Observations`；而成功/`runErr==nil` 分支在 `1221-1245` 会复制 `result.Usage` 并收集观察结果。

这直接解释了：

- cache analytics 能看到 child 有 request usage；
- task/lifecycle analytics 却可能显示 usage=0；
- provider 实际消耗与父任务回执消耗不一致；
- 失败 child 已经读出的 findings 没有交付给父 Agent。

**建议**：

1. 在失败分支复制 `result.Usage`；
2. 统一收集 `result.Observations`，按 read-only/write task 分别保留 findings/patches；
3. 增加 `partial_output=true`、`partial_findings_count`、`partial_patch_count`、`usage_by_attempt`；
4. 父级 task result 与 terminal event 都写 `usage_total_tokens`、`usage_prompt_tokens`、`usage_completion_tokens`、`usage_cache_read_tokens`；
5. 不因 task 失败而丢弃已产生的结构化报告、artifact refs 和工具证据；
6. 将“task failed but partial output available”作为独立 completion reason，避免父模型只能二选一地把结果当全成或全败。

**验收**：

- provider 在第 2 个 step 返回 `result, context.DeadlineExceeded` 时，父回执仍包含该 attempt 的 usage 和 findings；
- task usage 与该 child request analytics 的 usage 可按 request/attempt ID 对账；
- 失败 child 的部分结果可被父 Agent 继续消费。

### 6.3 P1：统一 request/attempt/task/batch analytics

**当前问题**：本次 `docs-runtime-analysis` 存在成功 cache request，但 task lifecycle 仍为失败。这不是矛盾，而是当前报表没有同时呈现四层状态。

**建议数据字段**：

```text
root_session_id
parent_session_id
batch_id
task_id
child_session_id
attempt
llm_request_id
provider_request_id
trace_id
context_mode=fresh|fork
fork_snapshot_hash
prompt_cache_key
parent_prompt_cache_key
prompt_cache_epoch
request_status
attempt_status
task_status
batch_status
failure_category
error_code
cancel_source
partial_output
usage_*
cache_status
```

**聚合规则**：

- request success 不自动提升 task success；
- task success 必须由 completion contract/`result.Success` 决定；
- batch success 必须所有 required tasks 终态成功；
- intermediate attempt 不计入最终 task failure，但计入 retry/conflict；
- failed task 的 partial usage 必须计入成本，不得因为 terminal failure 被清零。

### 6.4 P1：修复 cache lineage 与 `reported_zero` 观测

**建议修改文件**：

```text
backend/internal/agent/loop.go
backend/internal/cacheanalytics/（usage/cache ingest 与 query）
backend/internal/usageanalytics/（统一 usage ledger）
backend/internal/modelrouting/（可选的 prompt-cache route gate）
```

**实施动作**：

1. 在 child request metadata 中明确记录 `cache_lineage_mode=fresh|fork|resume`；
2. 记录 parent key、child key、epoch、fork snapshot hash 和 prompt fingerprint；
3. 将 cache 状态拆为：`hit`、`confirmed_miss`、`reported_zero`、`not_reported`、`request_error`；
4. provider adapter 保留原始 usage 字段与归一化字段，避免把缺省零值伪装成真实 miss；
5. 为 fork 增加 provider/model/工具面完全相同与不同的对照测试；
6. 不直接把所有 child key 改成 parent session key，避免并行 child 互相污染或错误复用；应先定义“可复用前缀”的稳定 hash，再生成带 lineage 的 key。

**建议的 key 方案**：

```text
cache_prefix_identity = hash(provider, model, system_prompt, stable_tool_surface, fork_snapshot_hash)
cache_namespace       = root_session_id + "/" + cache_prefix_identity + "/epoch-<n>"
child_observation_key = child_session_id + "/attempt-<n>"
```

这只是设计建议，落地前需以实际 provider 的 prompt-cache contract 为准；不能假设所有 provider 都支持跨 session key 复用。

### 6.5 P1：把 context 模式变成可审计契约

目前 fresh/fork 语义在源码中较完整，但运行历史没有足够字段证明每次 child 的实际模式。建议：

- spawn admission 事件写入 `requested_context`、`effective_context`、`capability_granted`、`refusal_code`；
- fork 成功写入 `parent_history_message_count`、`fork_snapshot_message_count`、`fork_snapshot_tokens`、`fork_snapshot_hash`；
- fresh 明确写入 `parent_history_injected=false`，不要依赖缺失字段推断；
- fork 超限/parent history unavailable 时保留结构化拒绝，不要静默降级 fresh；
- direct `subagent_spawn` 与 batch `subagent_spawn_batch` 使用同一套 context receipt，避免两条路径语义漂移。

### 6.6 P1/P2：减少只读 child 的无效工具失败

本次 direct child 的最终错误为 read-only policy 阻止非只读 PowerShell 命令。当前系统已经在 route warning 中提示“read_only 会剥离写工具”，但提示不一定阻止模型继续生成不可执行命令。

建议：

1. child 首个 system/prompt banner 明确列出**实际可用工具**和禁止工具；
2. 对 read-only child 在工具 schema 层完全不暴露 shell/write-like tool，而不是仅在执行时拒绝；
3. 对 `tool not found`、`not allowed for this agent`、`STALE_CONTEXT` 使用确定性 `tool_error` 分类，不要回落到 `provider_error` 并错误建议重试；
4. 达到连续只读拒绝阈值时，立即返回结构化 `complete_locally_or_change_tool`，并携带此前 findings；
5. 目标项目分析任务若需要执行 `Get-Item` 等只读 shell 命令，应把工具策略声明为允许的 read-only shell 或改用 `glob/view`，不能让模型在不匹配的 allowlist 上盲试。

### 6.7 P2：处理探索 stall 与 UI 事件诊断

**实测**：主会话 step 30 出现 12 个连续只读 step；debug log 有一次 `subagent.route.resolved` 因 `event turn does not match active run` 被 suppress。

建议：

- 把 exploration stall 从“观察事件”升级为包含当前 goal、最近工具、重复指纹、剩余预算的诊断事件；
- 对只读探索设置阶段性证据门槛：若连续 N 步没有新文件/新符号/新 artifact，自动生成中间摘要或请求父 Agent 改变任务；
- UI render suppression 必须保留 event id、active turn id、event turn id、source session，不能只留一条 warning；
- render suppression 不要计入 child execution failure，但应进入 observability error budget。

## 7. 代码实施行动计划

### 阶段 A：先修复可靠性与成本对账（P0）

1. 修复 scheduler failed branch 的 usage/partial result 传递；
2. 增加 attempt lifecycle 事件和 deadline/cancel source 字段；
3. 明确 batch、task、provider 三层 timeout 的预算公式；
4. 增加失败 attempt usage 的 ledger 对账；
5. 用 fake provider 重现“第一次请求拿到 usage，随后 context deadline”的情况。

**阶段 A 交付文件**：

```text
backend/internal/agent/scheduler.go
backend/internal/agent/subagent_retry.go
backend/internal/agent/subagent_batch_coordinator.go
backend/internal/agent/*_test.go
backend/internal/usageanalytics/*
```

### 阶段 B：统一 context/cache lineage（P1）

1. 统一 direct spawn 与 batch spawn 的 context receipt；
2. 增加 fresh/fork 对照集成测试；
3. 增加 parent/child cache key、epoch、snapshot hash 的请求元数据；
4. 细分 `reported_zero`、confirmed miss、not reported；
5. 保证 fork snapshot 超限和 history unavailable 的拒绝可观测；
6. 明确是否要 provider 级跨 session prompt cache 复用，并为不支持该能力的 provider 关闭错误预期。

**阶段 B 交付文件**：

```text
backend/internal/agent/child_factory.go
backend/internal/agent/subagent_context.go
backend/internal/agent/loop.go
backend/cmd/aicli/commands/chat_actor_registry.go
backend/cmd/aicli/commands/chat_actor_host.go
backend/internal/cacheanalytics/*
```

### 阶段 C：工具策略和探索效率（P1/P2）

1. read-only 工具 schema preflight；
2. 工具失败分类和 next_action 统一；
3. exploration stall 自动摘要/改派策略；
4. render suppression 诊断字段补齐；
5. 通过小任务、窄工具面和低 reasoning effort 进行灰度对比。

## 8. 测试与验收清单

> 2026-10-27 复核：勾选已按实际实施与测试证据更新（每条附证据）；完整复核与提交链见 §13。

### 8.1 Retry/deadline

- [x] 第一次 provider transport error，剩余预算足够时，真实产生 attempt 2；（E2E：TestSubagentAttemptEventsPairOnTransientRetry，provider 恰 2 次调用）
- [x] 剩余预算不足时不盲目重试，事件明确记录跳过原因；（单测：`retry_skipped_deadline_budget` / TestSubagentRetryBudgetAvailable|BudgetMS；`retry_skipped` 字段贯通终态事件与 hook）
- [x] `max_attempts=2` 的报表能区分“上限为 2”和“实际执行 2 次”；（`subagent.attempt.started/finished` 成对事件 + usage_subagents 的 attempt/max_attempts 收敛）
- [x] batch timeout、task timeout、provider timeout 的 `cancel_source` 可区分；（provider 档 E2E：TestSubagentAttemptCancelSourceAndRetrySkipOnProviderTimeout；task/batch/parent 档单测：TestSubagentCancelSource）
- [x] provider response-header guard 不会静默吞掉 task deadline 之前的 retry budget。（`4fa3ddc5`：真实 provider + 挂起上游两档 E2E——① guard 快速失败后 attempt 2 仍在 0.34s 内执行并成功（`retryable=true`、`retry_skipped` 空、`cancel_source` 空、`remaining_ms` 仍 >10s，远小于 30s 任务 deadline）；② 上游持续挂起时显式 `max_attempts_reached` 收口、不误报 `task_timeout`；provider 侧机制由 llm 包 streak/transport-budget 用例固定。连接清理观察见 §13.5）

### 8.2 Usage/partial output

- [x] `runErr != nil` 且 `result != nil` 时，父回执包含 usage；（ac970024：applySubagentResultOutput + 跨 attempt 聚合，单测锁定）
- [x] failed task 的 findings/patches/artifact refs 保留；（同上；partial_* 字段贯通终态事件与 SubagentStop hook）
- [x] 每个 attempt 的 usage 可按 `llm_request_id` 对账；（`usage_by_attempt` 事件（ac970024）+ 调度 attempt 盖进请求 metadata 与载荷、cache 记录新增 `task_attempt` 列（ab6a830e），request↔attempt 可直接关联）
- [x] task failure 不再把成本聚合成 0；（失败分支取回 Usage + usage_by_attempt，单测 TestApplySubagentResultOutput*）
- [x] cache error request 没有 usage 时显示 `usage_unavailable_reason`，不写成真实零消耗；（ab6a830e：`request_error`/`usage_not_reported` 三档 + 空串语义，单测锁定）

### 8.3 Fresh/fork context

- [x] fresh child 的第一条请求不含父历史消息；（fresh 构造语义 + route receipt `parent_history_injected=false`，E2E 断言）
- [x] `context=fork` 在 capability 关闭时稳定拒绝；（既有准入拒绝 + subagent_context_test）
- [x] capability 开启时只注入父级已完成、配平、去 system 的快照；（subagent_context_test：system 剥离/深拷贝冻结/首轮等价 fresh）
- [x] 父会话后续新增消息不会改变已创建 child 的 snapshot hash；（0b71fb31：forkSnapshotHash 稳定性 + 冻结深拷贝测试）
- [x] snapshot 超过 96,000 token 时整体拒绝，不静默截断；（既有测试 + 结构化 next_action 拒绝）
- [x] direct spawn 与 batch spawn 对 `none/all/last:N` 的语义一致；（`859651b9`：batch 侧新增 `fork_messages` 窗口参数 = direct `fork_turns=N` 同语义「最近 N 条消息」（配平后、token 上限前截窗）；`fresh`/`none` 与 `context=""` 等价、`fork`/`all` 全量；receipt/parent key 统一（221d3c22））
- [x] `inherit_parent_when_missing` 只影响路由缺省，不影响 history/context mode。（源码语义与既有测试口径未变）

### 8.4 Cache

- [x] fresh/fork/resume 的 cache lineage 字段完整；（fresh/fork 具备 `context_mode`/`fork_snapshot_hash`/`parent_prompt_cache_key`；resume 按 2026-10-28 拍板的**窄读法**落地为标签：`cache_lineage_mode=resume`（`8c152f4d`，口径见 §13.6）——不新增 spawn 模式）
- [x] parent key、child key、epoch、fingerprint 可从 request event 关联；（4ab293f3 + 既有 `prompt_cache_key`/epoch/fingerprint）
- [x] provider 报告 cached=0 时能区分 `reported_zero` 与 `confirmed_miss`；（`bf7a6b0a`：`CacheMissReported/CacheMissTokens` 仅在 provider 显式给出 miss 字段时置位，六档分类 + 分布列 + `/usage`「确认未命中」标签；「输入总量 − 命中」推导值不伪装成确认 miss）
- [x] prompt history replacement 必须递增 epoch，并验证旧 key 不被重用；（a1b7022c：epoch 持久化防回退 + 单测）
- [x] 同一个 child 的 retry 是否复用预期前缀有明确 provider-specific 测试；（`2c2f3104`：`TestSubagentRetryReusesExpectedCachePrefix`——attempt 1 注入瞬时失败 → attempt 2 复用同 `prompt_cache_key`/epoch、`messages`/`tools` 逐项一致、provider/model 一致，且 attempt 2 的缓存命中（`usage_cache_read_tokens`）计入任务回执；provider 侧请求体字节稳定性另见 llm 包 `TestProviderRetryWidenedMaxTokensKeepsCachePrefix`（扩容重试不得改写信封前缀，并行线程在途））
- [x] 不同 child 并发时不会因错误共享 key 产生 cache 污染。（`2c2f3104`：`TestConcurrentChildrenKeepDistinctCacheKeys`——同批两个目标文字完全相同的 read_only child 各自持有按 `subagent_<task>_<uuid>` 派生的 cache key，互不相同且不等于父会话 key：按 session 而非内容隔离）

## 9. 运行参数和灰度建议

1. **先保持 `ForkContextEnabled=false`**，完成 fresh/fork 观测和测试后再灰度开启；不要在缺少 lineage 指标时直接打开 fork。
2. **将 scheduler batch deadline 临时提高到明显大于单次 route timeout + retry backoff**，或显式关闭 task 自动 retry 做对照，确认失败是否来自预算竞争；不要仅靠提高全局 timeout 掩盖问题。
3. 对 read-only 分析 child 使用窄目标、明确 `read_paths`/`tools_whitelist` 和合理的 `MaxTurns`，避免把大项目全量扫描交给一个 30 秒窗口。
4. 将 provider transport/header timeout、模型 reasoning effort、child `BudgetTokens` 作为独立实验变量记录；不要把所有 `UPSTREAM_UNAVAILABLE` 归因于同一个 provider 根因。
5. 先以 report-only 方式上线新的 analytics 字段，待 request/task 对账稳定后，再启用自动 failover、自动重派或自动收敛策略。
6. 对失败 child 默认保留可恢复的 session/terminal record；自动关闭只应在成功且 batch 完整收敛时启用。

## 10. 待验证项和限制

以下内容在当前证据下不能写成确定根因：

- `UPSTREAM_UNAVAILABLE` 的底层原因是否全部是 provider 网络/响应头超时；事件只确认了 transport retry 和部分 `context deadline exceeded`；
- 三个 scheduler task 的第二次尝试是否曾在另一个未关联日志中短暂启动；当前目标事件流没有 `attempt=2`，应以新增 attempt event 作为最终判据；
- fork 模式是否被本次目标会话实际请求；当前三个 scheduler task 的事件没有 `context=fork` 字段，不能反推；
- provider 是否会在不同显式 prompt cache key 下按内容前缀复用 KV cache；需要 provider-specific 对照实验；
- `reported_zero` 是真实 miss、provider 不支持上报、还是 adapter 丢字段；需要保留原始 provider usage/response；
- 端点停止响应的原因；它不应计入目标业务或 child task failure。

## 11. 最终行动结论

本次最值得优先处理的不是简单“把 cache 命中率调高”，而是先让系统能够回答以下四个问题：

1. **这个 child 到底执行了几次 attempt，第二次为什么没有执行？**
2. **它虽然失败了，已经消耗了多少 token、读到了哪些 findings？**
3. **它是 fresh 还是 fork，实际注入了多少父历史，使用了哪个 cache key/epoch？**
4. **provider request 成功但 task 失败时，失败发生在请求、工具、completion contract、deadline 还是 batch 收尾？**

推荐顺序为：

```text
先修 retry/deadline 预算与 attempt 事件
  -> 再修失败 usage/partial output 传递
  -> 再统一 task/request/cache analytics
  -> 再验证 fresh/fork 与 cache lineage
  -> 最后优化工具面、探索 stall 和 UI 诊断
```

在完成 P0 前，不建议以“child cache 丢失”作为结论，也不建议通过盲目增加重试次数或把所有 child 改为共享父 cache key 来缓解问题；这两种做法都可能放大 token 成本、并发压力和 cache 污染风险。

## 12. 本轮工作区变更说明

本轮只应新增本文件：

```text
docs/analysis/session-subagent-runtime-20261027-action-plan.md
```

源代码没有按本行动文档直接修改；后续实施须按阶段 A/B/C 单独提交、测试和灰度。

## 13. 实施与复核记录（2026-10-27）

本节由 2026-10-27 的实施/复核会话追写：先完成"检查实施状态"的逐项核查，随后按 §7 阶段 A → B（部分）逐步提交并跑测试。原始正文（§1–§12）保持不动；§8 勾选已按本节证据更新。

### 13.1 提交链（本仓库 main）

| 提交 | 内容 | 对应条目 |
|---|---|---|
| `9a06910a` | 保留失败子代理 attempt 的 usage/部分产出并记录 retry 跳过与取消来源 | §6.2 / §6.1（首版） |
| `ac970024` | 收口失败 attempt 的产出聚合与重试预算观测 | §6.1-2/-3 / §6.2 |
| `6c99e44d` | system 头部锚定冻结 WorkspaceAnchor，消除 prompt-cache epoch 断裂 | §6.4 邻接（缓存稳定性） |
| `a1b7022c` | 持久化 prompt-cache 世代推进，防跨 run 回退复用旧 key | §6.4（epoch 不回退） |
| `d0aafde3` | execution_mode schema 移除后的断言同步 | 测试卫生 |
| `64cee823` | 记录 attempt 时序与重试预算观测字段（§6.1-1） | §6.1-1 |
| `faeb6d8d` | subagent.attempt.started/finished 成对生命周期事件（§6.1-4） | §6.1-4（事件面） |
| `c0e1f634` | fake-executor 注入首次 UPSTREAM_UNAVAILABLE，锁定 attempt 1/2 成对事件 | §8.1 验收 |
| `66457451` | 闭合 §6.1-4 过渡期：ingest 订阅 attempt.finished；中间尝试不再发 subagent.completed | §6.1-4（收口） |
| `91ebd018` | usageanalytics attempt 观测增量列（cancel_source/retry_skipped/partial_output）与 provider 超时 E2E | §6.3 / §8.1 |
| `0b71fb31` | context receipt 与 fork 快照指纹（§6.4/§6.5） | §6.4 / §6.5 |
| `4ab293f3` | parent_prompt_cache_key 贯通（§6.4 父/子 key 对账） | §6.4 |
| `99dfadd6` | 本文件 §8 勾选更新与 §13 实施复核记录 | 文档 |
| `221d3c22` | direct spawn 的 context receipt 与父 key 接线（§6.5/§6.4 统一） | §6.5 / §8.3 |
| `ab6a830e` | usage_unavailable_reason 与 task_attempt 对账列（§8.2 收尾） | §8.2 |
| `bf7a6b0a` | confirmed_miss 状态分层与 provider miss 计数（§6.4/§8.4） | §6.4 / §8.4 |
| `859651b9` | batch 路径 fork 窗口 `fork_messages`（§8.3 direct/batch 语义一致） | §8.3 |
| `2c2f3104` | §8.4 cache 专项：重试前缀复用 + 并发 child key 隔离 | §8.4 |
| `0bebe723` | task/batch deadline 档 E2E 注入与取消归因 | §8.1 / §6.1-3 |
| `8c152f4d` | resume cache lineage 窄读法：标签接线 + 三宿主打标 + 分析字段 | §8.4 / §13.6 |
| `4fa3ddc5` | provider header-guard 专项：任务级重试预算保留 + 显式收口两档 E2E | §8.1 |

### 13.2 逐条对照（§6）

- **§6.1-1 attempt 时序字段**：`attempt_started_at/attempt_deadline/batch_deadline/remaining_ms/retry_budget_ms` 已入 `SubagentResult`、成功/失败终态、中间 attempt 事件与 SubagentStop hook（`64cee823`）。
- **§6.1-2 retry 预算闸门**：`subagentRetryBudgetAvailable` + `retrySkipReason`（`retry_skipped_deadline_budget`/`max_attempts_reached`/`write_task_no_auto_retry`/`non_transient_failure`/`retry_skipped_parent_cancel`）落事件与报表（`ac970024`）。
- **§6.1-3 cancel_source 分层**：`task_timeout/batch_deadline/parent_cancel/provider_timeout/provider_cancel`（context cause 优先；`ac970024`）；provider 档 E2E（`91ebd018`）；task/batch 档 E2E（`0bebe723`：真实 `TimeoutSec=1`/`BatchDeadline=+1s` 注入 → 归因精确命中、`retry_skipped_*` 显式记录、批次终态 failed/timed_out 各自可解释）。
- **§6.1-4 成对 attempt 事件**：常量 → 契约注册（A+D）→ known 目录 → 发射点（started/finished）→ ingest 订阅 → 过渡期收口（`faeb6d8d`/`66457451`）；`subagent.completed` 自此只表示 task 终态。
- **§6.1-5 预算语义**：**未定稿**——建议维持"最多 N 次尝试"（跳过原因已可观测）；"至少一次"需产品决策与 batch 起始预留预算。
- **§6.2 失败 usage/部分产出**：`applySubagentResultOutput` 统一收口 + 跨 attempt 聚合 + partial 计数（`9a06910a`/`ac970024`）。
- **§6.3 统一 analytics**：usageanalytics 增 `cancel_source/retry_skipped/partial_output` 列（含幂等迁移与读取面）；cacheanalytics 增 `context_mode/fork_snapshot_hash/parent_prompt_cache_key`；`batch_id/task_id/attempt` 既有。四层状态分列（request/attempt/task/batch）仍建议在查询侧显式化。
- **§6.4 cache lineage**：`context_mode`（fresh/fork）、`fork_snapshot_hash`（冻结快照稳定指纹）、`parent_prompt_cache_key`（spawn 时刻父 epoch 派生）、epoch 持久化防回退（`0b71fb31`/`4ab293f3`/`a1b7022c`）；`confirmed_miss` 状态分层（`bf7a6b0a`）：六档 `hit`/`write`/`confirmed_miss`/`reported_zero`/`not_reported`/`error`，`CacheMissReported` 仅在 provider 显式给出 miss 字段（如 DeepSeek `prompt_cache_miss_tokens`）时置位，「输入总量 − 命中」推导值不伪装成确认 miss；`request_error` 命名未采纳（保留 `error` 枚举 + `usage_unavailable_reason=request_error`），provider-specific 前缀实验**未做**。
- **§6.5 context receipt**：`requested_context/effective_context/parent_history_injected/fork_snapshot_messages/fork_snapshot_hash` 入 route.resolved（`0b71fb31`）；capability/refusal 字段部分既有（`capability_refusals`）；direct spawn 与 batch 的 receipt 已同字段名统一（direct 落子会话上下文并转发进完成镜像，`221d3c22`）。
- **§6.6 只读工具面**：既有能力已覆盖（schema 层剥离、确定性 tool_error 分类、边界横幅、连续拒绝硬停）；本轮未改。
- **§6.7 stall/渲染诊断**：stall 事件保持轻量（2026-10-24 产品决策移除软提醒）；render suppression 字段未扩。**未做**。

### 13.3 验收证据（§8）

- 安静窗口多包全绿记录：`internal/agent`、`internal/events`、`internal/runtimeobserve`、`internal/cacheanalytics`、`internal/usageanalytics`（各提交信息内含时长与 exit code）。
- 2026-10-28 扫尾回归（`-p 1` + `GOGC=40`，覆盖 confirmed_miss/fork 窗口/spec 往返/deadline E2E 三批改动的跨包面）：`internal/events` 3.732s、`internal/runtimeobserve` 1.044s、`internal/cacheanalytics` 1.388s、`internal/usageanalytics` 5.416s、`internal/subagentbatch` 0.909s、`internal/agent` 全包 22.020s、`cmd/aicli/commands` 聚焦（usage 渲染 + spawn receipt；`-vet=off` 因并行线程 `internal/remotetargets` vet 依赖在途）3.294s——全部 exit 0，无环境异常（磁盘 C 11.8GB / E 12.3GB）。
- 关键 E2E：`TestSubagentAttemptEventsPairOnTransientRetry`（attempt 1 失败 → attempt 2 成功，provider 恰 2 次调用）；`TestSubagentAttemptCancelSourceAndRetrySkipOnProviderTimeout`（`cancel_source=provider_timeout` + `retry_skipped=max_attempts_reached`）；`TestReActLoop_Run_EmitsRuntimeEvents`（端到端事件流含 attempt 成对）。
- 迁移回归：`TestStoreMigrateAddsSubagentAttemptColumns`（降级删列 → 重开补列 → 写读）。
- cache 状态分层：`TestBuildTerminalRecordClassifiesConfirmedMiss`（确认 miss 与 reported_zero 边界五档）、`TestCacheStatusDistributionAccountsConfirmedMiss`、`TestCacheSourceOverviewCacheStatusDistributionAlignsWithSQL`（六档 SQL 列序对齐）、`TestReActLoop_Run_DistinguishesZeroAndUnreportedProviderCacheUsage`（载荷层 confirmed_miss）、`TestResolveUnifiedTokenUsage_DeepSeekMissReported` / `..._OpenAIDerivedUncachedIsNotMissReported`（显式上报 vs 推导不伪装）。
- batch fork 窗口：`TestBuildForkHistoryWithWindowKeepsMostRecentCompletedMessages`（截窗 = 最近 N 条 / 窗口≥长度等价全量）、`...AppliesBeforeTokenCeiling`（窗口先于上限：缩小窗口可让全量超限的父历史通过）、`TestBuildForkHistoryForTaskAppliesForkMessages`（任务字段接线）、`TestSubagentContextReceipt`（请求窗口与实际注入条数分离）、decode 三档（接受数值/拒收「有窗口无 fork」/非正值 warning + 字符串拒绝）、`TestBatchTaskSpecRoundTripPreservesForkContext`（durable spec 往返 + 旧 spec 字节兼容）。
- cache 前缀专项（`2c2f3104`，agent 全包 22.338s）：`TestSubagentRetryReusesExpectedCachePrefix`（attempt 1 失败 → attempt 2 同 key/epoch + messages/tools/provider/model 一致 + 命中计入回执）、`TestConcurrentChildrenKeepDistinctCacheKeys`（同目标并发 child 的 key 按 session 隔离且不等于父 key）。
- deadline 档 E2E（`0bebe723`，agent 全包 23.906s）：`TestTaskDeadlineE2EAttributionAndRetryGate`（1s 任务 deadline → `cancel_source=task_timeout` + `retry_skipped_*` + provider 恰 1 次调用 + 批次 failed）、`TestBatchDeadlineE2EAttribution`（1s 批次 deadline → `cancel_source=batch_deadline` + 批次 timed_out）；两例任务行均为 `failed_with_result`（取消仍保留可回收报告）。
- resume 标签窄读法（`8c152f4d`）：`TestRunWithSessionCarriesCacheLineageResume`（带标记/未标记/未知值三档接线）、`TestCollectorCarriesCacheLineageMode`（started → 记录 additive 字段）、`TestStampResumeCacheLineageInSession`（有历史打标并回写 / 空会话 / 读失败 / 写失败四档）、`TestRestoreChatPersistenceState_...`（CLI 打标 + 空会话负例）；回归：agent 25.863s / cacheanalytics 2.273s / runtimeapi 94.609s / commands resume 子集 7.214s。
- header-guard 专项（`4fa3ddc5`，agent 全包 27.635s）：`TestHeaderGuardFailureKeepsTaskRetryBudget`（真实 provider + 挂起上游：attempt 1 guard 失败 → attempt 2 在 0.34s 内成功，预算保留四断言全过）、`TestHeaderGuardExhaustionIsExplicitNotSilent`（持续挂起 → 显式 `max_attempts_reached`、不误报 `task_timeout`，0.49s）。

### 13.4 未完成项与建议

1. **§6.1-5 预算语义**：待产品拍板（建议维持现状并写入报表口径）。
2. `request_error` 命名决策与 provider-specific 前缀对照实验（`confirmed_miss` 枚举细分已在 `bf7a6b0a` 落地，`usage_unavailable_reason` 与 `task_attempt` 关联列已在 `ab6a830e` 落地）。
3. §8.4 验收清单现已全部勾选（resume 字段完整按窄读法闭环：`8c152f4d`；两项 cache 专项：`2c2f3104`）；仅余 provider-specific 前缀对照实验（并入第 2 项）。
4. provider response-header guard 专项已闭环（`4fa3ddc5`：任务级重试预算保留 + 持续挂起的显式收口两档）；task/batch deadline 档 E2E 亦已闭环（`0bebe723`）。
5. `resume` 已按**窄读法**以标签口径落地（`8c152f4d`，口径见 §13.6）；「resume 作为 spawn 模式」经 2026-10-28 拍板明确**不做**——重启需先产品级定义越权读取与缓存命名空间错误共享两类风险。
6. §8.2 的 `request↔attempt` 与 `usage_unavailable_reason` 两项已闭环（`ab6a830e`，见 §8 勾选）。

### 13.5 备注

- 复核同时确认：`subagent.completed` 的中间态消费面已按 §6.1-4 收口迁移；bus 摘要 `SubagentCompleted` 计数从"尝试数"回归"任务数"。
- 观察（未修，超出本行动范围）：`subagentbatch.TaskSpec` 仍只往返部分任务字段（`WritePaths`/`ReportSchema`/`HandoffIn`/`AgentType`/`MaxTurns` 与只读过滤审计未入 spec）；这些字段在「幂等 admission replay」窗口内同样不随 durable spec 重建。本轮只补齐 fork 语义必需的 `Context`/`ForkMessages`。
- 工作区其他并行线程（envd / sandbox / remote-tools 文档与代码）不在本行动文档范围内，未在任何提交中触碰。
- 观察（待 provider 层复核，`4fa3ddc5` 附带证据）：header-guard 超时后，测试装置侧 `httptest.Server.Close` 曾等待 ≥1 分钟仍未结束——服务端 2 条 TCP 连接保持 active，且其 request context 未被取消（首次诊断时 handler 阻塞在 `<-r.Context().Done()` 直至 test 超时，goroutine dump 已留档于该次运行输出）。请 llm 包侧复核：guard 超时时客户端 transport 是否按预期断开/归还连接（涉及 `ResponseHeaderTimeout` 触发路径与 HTTP/2 变体）。测试装置已改用有界睡眠规避该不确定性，不影响 §8.1 结论。

### 13.6 resume cache lineage 口径（2026-10-28 拍板：窄读法）

**决策**：`resume` 不新增为 spawn context 模式；它作为**既有续写路径的 lineage 标签**落地（`8c152f4d`）。依据：§8.4 的审计目标是"解释命中/未命中"，标签即可达成；新增 `context=resume`（子代理接着另一个会话的历史跑）会打开跨会话历史复用面——越权读取他人会话历史、与目标会话共享缓存命名空间造成错误复用两类风险都需产品级定义，而当前 fork 已覆盖"子代理需要父上下文"的主场景，无观测证据支持该扩展。

**字段关系（互补，不重复表达）**：

| 字段 | 取值 | 语义 | 生产者 |
|---|---|---|---|
| `context_mode` | `fresh` / `fork` | 子代理 spawn 的上下文来源（空历史 / 父快照） | 调度器（既有） |
| `cache_lineage_mode` | `resume` | 本次运行续接的是持久化会话；前缀 key 连续性由 session id + 持久化 epoch 既有机制保证 | 宿主在续写入口打标，loop 读取并随 `llm.request.started` 发射 |

**三条续写路径与打标点**：

| 路径 | 打标点 | 条件 |
|---|---|---|
| CLI 启动期 `--session` / `--resume` / 启动选择器 | `restoreChatPersistenceState` | 会话确有持久历史 |
| CLI 会话内 `/resume`、`/load`（latest / picker / 按 ID） | `loadRuntimeConversation` / `resumeLatestRuntimeConversation` | 同上 |
| 控制面子代理 `subagent_resume` | `sessionAgentController.Resume` | 存储在 SessionStore 且有条目（批子代理不落 store 时 naturally no-op） |

**诚实边界（写死为纪律，均有测试锁定）**：

1. 只在确有持久化历史时打标——空会话贴 resume 会把"全新会话"误读成"续接未命中"；
2. 打标必须回写存储（actor/executor 每轮从 store 重载会话）；写失败只告警、不阻塞 resume；
3. loop 只认已知取值，未知字符串一律按未观测处理（不猜测、不外传任意标签）；
4. 零行为变更：不动缓存键/epoch/任何执行路径；标签仅进入 cache analytics 记录（`CacheLineageMode`，record_json additive 字段）。