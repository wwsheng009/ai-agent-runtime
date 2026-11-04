# 父 Turn 最终化门与异步迟到结果收敛方案

- 状态：`P0/P1/P2 tracked scope complete`（evidence-aware stale claim recovery、ACP explicit continuation contract、approval identity/attention wake 收口、legacy wake/self-check 防线、versioned output drain ledger、metrics readout、required background job 公共能力与真机 ConPTY 验收、nested child self-park 跨重启恢复、Windows/PTY 与 timing 稳定性均已落地）
- 修订：v1.1（2026-11-03 补充后台作业 `background_task` / `task_output` / terminal wake 链路）
- 日期：2026-11-03
- 适用范围：`backend/internal/agent`、`backend/internal/supervision`、`backend/internal/subagentbatch`、`backend/internal/background`、CLI/API runtime host、ACP prompt handler
- 关联问题：主 Agent 已返回后，子 Agent 或后台作业才完成，生命周期事件再次自动唤醒父 Agent，造成二次结论、重复模型 turn 或旧结果污染新 turn
- 关联方案：
  - `docs/plan/spawn-subagents-async-supervisor-plan.md`
  - `docs/plan/supervised-turn-suspension-and-agent-task-control-plan-20260923.md`
  - `docs/plan/supervision-parent-child-control-optimization-plan-20260917.md`
  - `docs/analysis/deepseek-harness-vs-aar-agent-system.md`
  - `docs/analysis/commandcode-tools-design-borrowing-20260926.md`

---

## 1. 摘要

当前项目已经具备异步子任务、durable batch、obligation、托管 turn、生命周期投影、wake 队列、重试和 replay/recovery 等基础设施。问题不在于缺少 supervisor，而在于以下几个生命周期被错误地当成了同一个状态：

```text
同步请求结束
agent loop 结束
父 turn 可以最终化
required child/job 全部结算
最终响应已经渲染
```

尤其是 ACP `PromptResponse{StopReason: EndTurn}` 或 executor 返回，只能证明本次同步请求结束；它不能自动证明 detached subagent/background job 已经完成，也不能成为后续 wake 重新启动父模型的充分条件。

本方案的核心结论是：

```text
parked parent
    -> 只恢复匹配的同一逻辑 turn

finalized parent
    -> 迟到结果只进入 notification / artifact / result store

explicit user follow-up
    -> 开启新的 episode，再读取已结算结果
```

因此需要补齐四个边界：

1. **Parent finalization gate**：父 turn 在最终响应前，必须确认 required obligations 已经结算，并经过稳定性检查。
2. **Required / detached policy**：子任务创建时明确它是否阻止父 turn 最终化。
3. **Turn / episode identity**：wake、settlement、model turn 必须绑定身份，不能仅按 session 或 actor idle 路由。
4. **Resume admission 与 exactly-once**：子任务完成只能提交“待处理生命周期工作”，不能直接假设父 Agent 仍接受自动 resume。

本方案不建议简单关闭自动 wake，也不建议无条件等待全部后台作业。目标是把“应当恢复的托管 turn”和“只能通知的迟到结果”分开。

**后台作业同样属于本问题范围。** 当前 job terminal projection 已经具备 event whitelist、durable notification、epoch/notify-key 去重，以及 `task_output(wait=exit)` 活跃等待期间的 `terminal_observed` 抑制；但这些机制主要解决 job-level 终态可靠性和“模型已经看过终态”的重复 wake，尚未解决父 turn/episode 级别的最终化准入。`background_task` 返回后父 turn 已结束、job 随后 terminal 的路径，仍可能从 `ScheduleWake` 经 `WakeConsumer` 进入 `SubmitPromptAsync`，因此必须使用同一套 finalization/admission 方案。

---

## 2. 问题定义与现状结论

### 2.1 同步请求结束不等于父 turn 最终完成

ACP/CLI 的 prompt handler 在 `sendMessageWithImages` / `sendMessage` 或 executor 返回后，会返回 `PromptResponse`。正常路径通常使用 `StopReasonEndTurn`，这表示：

```text
当前 prompt 的同步处理已经返回
```

但下列事实可能仍然存在：

```text
required obligation 尚未 terminal
background batch 仍在 running
child session 尚未完成
terminal output 仍在 drain
wake 已落库但尚未被 parent admission
```

所以必须显式区分：

| 状态 | 含义 | 是否允许迟到 child 自动重新启动模型 |
| --- | --- | --- |
| `request_finished` | 本次同步请求返回 | 不能据此判断 |
| `turn_suspended` | 父逻辑 turn 仍托管等待 obligation | 仅允许匹配的同一 turn resume |
| `turn_finalizing` | 正在执行最终化检查 | 不允许 |
| `turn_finalized` | 父逻辑 turn 已经完成最终结论 | 不允许，只能通知 |

### 2.2 异步 batch 脱离父请求继续运行是有意设计

`SubagentBatchCoordinator.StartBackground` 先持久化 batch/task，再使用脱离父请求取消边界的 worker context 启动后台执行。父 prompt 返回后，worker 可以继续运行；batch terminal 后再进行 lifecycle projection、delivery 和 wake。

这意味着以下时序是合法的：

```text
parent request returns
    -> child batch continues
    -> child batch becomes terminal later
```

因此不能通过“父请求返回时取消所有 child”来修复问题。正确做法是：后台任务完成后根据其 obligation policy 和父 turn 状态选择 `resume` 或 `notify-only`。

### 2.3 当前已有 parked turn 与严格 obligation resolver

当前托管 turn 方案已经有以下基础：

- `TurnSuspension` 绑定 `TurnID`、`SessionID`、`RootScopeID` 和 obligation IDs；
- suspension 先 durable 写入，成功后才发布 suspended event；
- 同一 `(session, turn)` 的多次 dispatch 可以合并 obligation/resume queue；
- obligation resolver 不会把缺失记录、未接线 resolver 或读取错误乐观视为完成；
- resume 使用“所有 obligation 已知且 terminal”的严格判定；
- `agent.turn.finished` 与 `turn.suspended` 已经是不同语义。

这些机制解决了“父 turn 已经 park，但 child 完成后无人恢复”的问题；本方案补的是另一条边界：

```text
父 turn 已经可以最终化或已经 finalized 后，child completion 不能再无条件触发自动 resume。
```

### 2.4 当前 wake 投递保证不等于最终答案唯一

当前存在多个可能触发 drain/wake 的入口，包括：

- child/batch terminal projection；
- session end 或 parent idle drain；
- API batch terminal convergence；
- fallback scanner；
- startup/recovery replay；
- background job terminal/monitor event。

这些路径可以共享 durable wake、claim、delivery retry 和去重，但这类机制主要保证：

```text
生命周期事实不会轻易丢失
```

它不能单独保证：

```text
同一个 settlement 不会触发第二个模型 turn
finalized parent 不会被重新 resume
旧 episode 的结果不会投递给新 episode
```

所以必须把“wake 的可靠投递”和“resume 的准入”拆成两个层次。

### 2.5 Background job terminal 也不是 parent join

后台 job 有独立的 `pending/running/paused` 与 terminal 状态，manager 还需要处理 descendants 持有 stdout/stderr 的 `WaitDelay` 以及 terminal side effects。monitor 是异步观察/提醒，不是父 prompt 的同步 join。

因此：

```text
job terminal
```

只能说明 job 的执行状态已终结，不能直接推出：

```text
当前父 turn 必须再次调用模型
```

### 2.6 后台作业已经有 job-level 防重复，但缺 parent-turn admission

当前后台作业链路的实际行为是：

```text
job terminal
  -> CLI/API background event handler
  -> ProjectBackgroundJobTerminal
  -> durable notification
  -> 非 Observed 时 ScheduleWake
  -> wakeSupervisedParent / drainSupervisedParentWake
  -> WakeConsumer.MaybeWakeParent
  -> DeliverResume / SubmitPromptAsync
```

已存在的保护包括：

- 终态事件使用共享 whitelist，`output` 等高频事件不会直接调度 wake；
- `ProjectBackgroundJobTerminal` 以 job + terminal epoch 写入幂等 notification，并通过 `DeriveNotifyKey` 抑制同一 epoch 的重复 wake；
- `task_output(wait=output|exit)` 登记 in-flight waiter；manager 在 terminal event 到达 host handler 之前写入 `terminal_observed`，projection 仍保留 durable 证据但跳过额外 wake；
- manager 在 terminal transition 中解除 monitor，并在必要时等待/处理进程树和 stdout/stderr 的终态副作用；
- CLI 与 API 都把 parent busy/rate-limited 的 wake 保留在 durable 队列中，而不是直接丢弃。

这些保护不能替代 parent finalization gate，原因是 job terminal projection 当前没有完整的父 turn 身份与最终化 policy。`BackgroundJobTerminalInput` 目前主要携带：

```text
root_scope_id
target_parent_session_id
job_id
status / epoch
observed
```

但缺少或没有强制使用：

```text
parent_turn_id
parent_episode_id
settlement_id / settlement_cohort_id
finalization_policy
```

同时，CLI/API 的 runnable/admission 主要回答“父 actor 当前是否接受 resume”，不能单独证明“这个 job 是否属于当前 parked turn，或父 turn 是否已经 finalized”。因此下列时序仍属于风险场景：

#### 场景 A：`background_task` 返回后父 turn 已结束

```text
background_task -> 返回 job_id
父模型生成答案 -> request/turn 返回
job 后来 terminal -> 非 Observed -> ScheduleWake
父 actor idle -> 自动 SubmitPromptAsync
```

这会把 detached job notification 误当成新的父模型 continuation。

#### 场景 B：`task_output(wait=exit)` 超时后 job 才完成

```text
wait=exit -> timeout -> waiter 注销
父 turn 随后 finalized
job terminal -> 已无 active waiter -> terminal_observed 不成立
terminal wake -> 仍可能自动开启新模型 turn
```

`terminal_observed` 只覆盖 waiter 活跃期间的竞态，不覆盖“waiter 已结束但 parent 已 finalized”的竞态。

#### 场景 C：父 actor busy 时 job 完成，之后自然 drain

```text
job terminal -> wake durable pending
父 actor busy -> 暂不投递
父 turn 结束/下次 preflight -> drain 旧 wake
```

如果没有 `parent_turn_id + parent_episode_id + finalization_policy` admission，旧 job 仍可能在父 turn 已最终化后被误升级为新模型 turn。

因此后台作业的结论是：

```text
job-level exactly-once / observed suppression 已有
parent model-turn admission exactly-once 尚缺
```

---

## 3. 参考实现带来的约束

### 3.1 DeepSeek Harness：最终退出需要稳定性检查

DeepSeek Harness 的 headless 生命周期不是看到 `idle` 就立即退出，而是组合检查：

```text
agent idle
+ waitForChildren
+ session sequence 在等待期间保持稳定
+ 没有新的 continuation/event
+ 最终 output drain 完成
```

其关键启示：

1. `idle` 不是任务树完成；
2. `waitForChildren` 应位于 finalization 边界，而不是散落在 child callback 中；
3. 子任务结算应使用明确的 `subagent-settled` notice，而不是伪装成普通用户 prompt；
4. terminal 后 producer 的迟到 output 不能继续覆盖已确定结果；
5. 已 finalized 的父级不应因为 late result 自动重新开一轮模型。

### 3.2 Codex：completion 进入 mailbox，再由 scheduler 决定是否续跑

Codex 的 child/task completion 不直接调用父模型，而是先进入 mailbox 或 pending work，再由 scheduler 检查：

```text
active turn
pending trigger
thread idle
turn identity
```

只有满足 admission 条件才会启动下一轮。任务完成时还会校验它所属的 active turn，避免旧 task 的完成事件清理或影响新 turn。

当前项目应借鉴这一点：

> child completion callback 只能产生结构化待处理工作；是否 resume 必须由统一的 parent admission 决定。

---

## 4. 目标语义与术语

### 4.1 Parent turn、episode 与 request

本方案使用三个层次：

- **request**：一次 ACP/HTTP/CLI 同步调用的传输边界；
- **turn**：父 Agent 的一个逻辑工作回合，可以在等待 obligation 时挂起并恢复；
- **episode**：一个可被自动续跑的 turn 实例。turn 被最终化后，迟到结果不能继续修改该 episode；用户后续明确输入开启新 episode。

建议所有自动 wake 至少携带：

```text
root_scope_id
parent_session_id
parent_turn_id
parent_episode_id
obligation_id / obligation_cohort_id
child_run_id
settlement_id
wake_id
```

### 4.2 Obligation policy

每个 child、batch 或 background job 在创建时声明以下一种策略：

#### `required_for_finalization`

结果可能影响当前父答案：

```text
obligation pending
    -> 父 turn 不得最终化
    -> 父 turn 可进入 PARKED_WAITING_OBLIGATIONS
obligation terminal
    -> 允许恢复同一 turn
    -> 生成唯一最终答案
```

#### `detached_notification_only`

任务是附加工作，不阻止当前父答案：

```text
父 turn 可以 finalized
任务之后 terminal
    -> 保存结果
    -> 写 notification / artifact
    -> 不自动调用模型
```

#### `explicit_followup_only`

任务结果只有在用户明确要求时才重新交给模型：

```text
任务 terminal
    -> 只保存结果
用户明确要求汇总/查看
    -> 新 episode 读取结果
```

旧 API 未提供 policy 时，应保留兼容默认值，并通过调用场景逐步迁移。对于现有托管 `wait` 语义，默认应映射为 `required_for_finalization`；对于明确的 background/detached 语义，默认映射为 `detached_notification_only`，除非调用方显式声明需要 parent follow-up。

### 4.3 父 turn 状态机

```text
RUNNING
  ├─ required obligation pending
  │    └─ PARKED_WAITING_OBLIGATIONS
  │           └─ matching settlement -> RESUMING
  ├─ no required obligation pending
  │    └─ FINALIZING
  │           ├─ stable -> FINALIZED
  │           └─ new required work -> PARKED_WAITING_OBLIGATIONS
  └─ explicit cancel -> CANCELED

FINALIZED
  └─ late child/job result -> LATE_RESULT_NOTIFIED

FINALIZED + explicit user input
  └─ NEW_EPISODE -> RUNNING
```

禁止以下转换：

```text
FINALIZED + late settlement -> automatic RESUMING
```

也禁止旧 episode 的 settlement 直接进入新 episode：

```text
old episode settlement -> new episode automatic resume
```

除非新 episode 明确声明要消费该结果。

### 4.4 核心不变量

| 编号 | 不变量 |
| --- | --- |
| I1 | `required_for_finalization` obligation 非 terminal 时，父逻辑 turn 不得进入 `FINALIZED`。 |
| I2 | 父 turn 必须在 durable state 中记录 `turn_id`、`episode_id` 和 obligation cohort。 |
| I3 | 自动 resume 只能恢复匹配的 parked turn，不得隐式创建新 episode。 |
| I4 | 同一 `(parent_turn_id, parent_episode_id, settlement_cohort_id)` 最多产生一个 automatic resume model turn。 |
| I5 | `FINALIZED` 之后的 child/job settlement 只能走 notification/artifact/result store。 |
| I6 | wake claim/delivery 去重不能替代 model-turn admission 去重。 |
| I7 | terminal 状态只在必要 output drain 完成后对外宣告稳定；terminal 后迟到 output 不得覆盖已归档结果。 |
| I8 | 旧 `turn_id`、旧 `episode_id` 或旧 `settlement_id` 的事件必须可识别为 stale，并保留审计证据。 |
| I9 | 所有自动恢复入口必须使用同一 admission 函数，不能由宿主各自解释 idle。 |
| I10 | 用户明确 follow-up 是开启新 episode 的唯一默认方式；生命周期通知本身不是用户输入。 |

---

## 5. 方案设计

### 5.1 Parent finalization gate

新增一个逻辑层面的 finalization gate。名称可在实现阶段调整，但职责必须集中，不能由 ACP handler、CLI host、API host、batch projector 和 fallback scanner 各自复制一份判定。

建议抽象为：

```go
type FinalizationDecision int

const (
    FinalizeAllowed FinalizationDecision = iota
    FinalizeParkRequired
    FinalizeRetry
)

type ParentFinalizationInput struct {
    RootScopeID       string
    ParentSessionID   string
    ParentTurnID      string
    ParentEpisodeID   string
    DurableEventSeq   int64
    RenderedResponse  bool
}

func EvaluateParentFinalization(
    ctx context.Context,
    input ParentFinalizationInput,
) (FinalizationDecision, error)
```

实现不要求一次引入完全相同的接口，但必须满足以下行为：

```text
1. 读取父 turn 当前 durable state
2. 找出属于本 turn/episode 的 required obligations
3. 缺失记录、resolver 错误、未知 terminal 状态按 pending/retry 处理
4. drain 当前已经可见的 child/job lifecycle 事件
5. flush 必要的 terminal output / result projection
6. 重新读取 required obligations 与 durable event sequence
7. sequence 稳定且所有 required obligations terminal，才允许 CAS -> FINALIZED
8. CAS 失败说明存在并发收尾/恢复，当前调用不得重复提交响应
```

推荐的最终化流程：

```text
agent loop 认为自己没有更多同步工作
    -> snapshot durable sequence S1
    -> load parent turn + episode
    -> resolve required obligations
    -> 若存在 pending：durably park，返回 PARKED
    -> drain child/job events（有界）
    -> flush result/output projection
    -> 再次 resolve required obligations
    -> snapshot durable sequence S2
    -> S1 == S2 且 obligations 全 terminal
    -> CAS parent state FINALIZING -> FINALIZED
    -> MarkAssistantFinalResponseRendered(turn, episode)
    -> 提交最终传输结果
```

如果第二次检查发现新的 required obligation 被 materialize，必须回到 `PARKED_WAITING_OBLIGATIONS`，不能先发送 final response 再补记 obligation。

### 5.2 `MarkAssistantFinalResponseRendered` 的定位

`MarkAssistantFinalResponseRendered` 应被保留为事实记录，但不能单独承担 finalization gate。建议将其扩展或绑定到：

```text
parent_turn_id
parent_episode_id
response_id / render_seq
finalization_cas_version
rendered_at
```

语义上分为两步：

```text
FINALIZED
    -> 允许渲染最终响应
RESPONSE_RENDERED
    -> 记录传输/渲染事实
```

如果渲染发生在最终化 CAS 之前，late settlement 仍可能被错误地当成 resume 依据。因此生产路径必须保证：

```text
required obligations gate passed
    before final response is considered final
```

对于仅属于 detached/background 的任务，父响应可以正常 finalized；这些任务不应进入 required obligation 集合。

### 5.3 Required obligation 与 detached job 的建立

在 dispatch 时创建并持久化 policy，而不是等 child terminal 后再猜测 policy：

```text
obligation_id
parent_turn_id
parent_episode_id
subject_kind: batch | agent_session | team | background_job
subject_id
settlement_cohort_id
finalization_policy: required | detached | explicit_followup
created_at
deadline_at / lease information
version
```

建立顺序：

```text
1. 生成 obligation_id 与 cohort_id
2. 写入 obligation/batch/task 记录
3. 将 policy 与 parent turn identity 一起 durable persist
4. persist 成功后再启动 worker 或发布 started event
5. worker 启动失败也必须产生 terminal obligation 结果
```

不能出现：

```text
worker 已经启动
但 obligation 尚未落库
```

否则 finalization gate 无法判断父 turn 是否仍受该任务约束。

### 5.4 统一 Resume Admission

所有自动唤醒入口最终都应调用同一 admission 函数。建议语义如下：

```go
type ResumeAdmission int

const (
    ResumeSameParkedTurn ResumeAdmission = iota
    NotifyOnly
    RejectStale
    RetryAdmission
)

type WakeAdmissionInput struct {
    RootScopeID       string
    ParentSessionID   string
    ParentTurnID      string
    ParentEpisodeID   string
    ObligationID      string
    SettlementID      string
    WakeID            string
    EventType         string
}
```

判断顺序：

```text
1. 校验 root/session/turn/episode identity
2. 读取 wake、settlement、obligation 的 durable 状态
3. 判断 settlement 是否已经被 resume_claimed 或 notification_claimed
4. 读取 parent turn 状态
5. 读取 policy 与 obligation cohort
6. 根据状态和 policy 返回 admission
```

判定矩阵：

| 父状态 | required settlement | detached/explicit settlement | 旧 identity |
| --- | --- | --- | --- |
| `RUNNING` | 记录 pending work，不并发 resume | `NotifyOnly` | `RejectStale` |
| `PARKED_WAITING_OBLIGATIONS` | `ResumeSameParkedTurn` | `NotifyOnly` | `RejectStale` |
| `RESUMING` | `RetryAdmission` 或合并到当前 resume | `NotifyOnly` | `RejectStale` |
| `FINALIZING` | 不启动模型，等待 finalization CAS 结果 | `NotifyOnly` | `RejectStale` |
| `FINALIZED` | `NotifyOnly` | `NotifyOnly` | `RejectStale` |
| `CANCELED` | 按取消策略记录结果，默认 `NotifyOnly` | `NotifyOnly` | `RejectStale` |
| 无匹配 suspension | 不自动新开模型，记录通知 | `NotifyOnly` | `RejectStale` |

非终态 attention wake 的明确默认值：

```text
approval（approval_required / question / permission）
  turn+episode 缺失                         -> NotifyOnly
  parent finalized/canceled                 -> NotifyOnly
  parent busy                               -> RetryAdmission
  turn+episode 匹配 suspended               -> ResumeLegacy（不做 settlement claim）
  turn+episode 与 durable parent 不匹配      -> RejectStale

progress / wait_feedback
  -> 保留既有 bounded legacy idle 行为（显式例外）

其他未知非终态 legacy wake
  turn/episode 缺失                         -> NotifyOnly
  identity 与 parent snapshot 不匹配         -> RejectStale
```

approval 是 attention wake，不是 settlement：它不进入 same-parked-turn 的
exactly-once claim，但仍必须携带 durable parent identity，不能仅凭 actor idle
自动续跑。

重点是：

```text
parent actor idle
```

不再是自动 resume 的充分条件。只有：

```text
matched parked suspension
+ required obligation settlement
+ same turn/episode identity
+ no competing resume/finalization
```

才允许自动 resume。

### 5.5 Resume 的 exactly-once 边界

wake 的 claim、delivery、model turn submit 和最终响应渲染是不同阶段，应分别持久化状态：

```text
settlement_id
parent_turn_id
parent_episode_id
resume_claimed_at
resume_submitted_at
resume_model_turn_id
resume_delivery_attempt
final_response_rendered_at
notification_claimed_at
notification_delivered_at
```

推荐使用 CAS：

```text
PENDING
  -> RESUME_CLAIMED
  -> RESUME_SUBMITTED
  -> RESUME_SETTLED
```

如果 `RESUME_CLAIMED` 后进程崩溃，recovery 必须依据 `resume_model_turn_id`、parent turn state 和 mailbox/event evidence 判断是重试还是标记已提交；不能简单删除 claim 再重新起模型。

不变量：

```text
unique(parent_turn_id, parent_episode_id, settlement_cohort_id)
    -> at most one automatic resume model turn
```

数据库唯一索引或等价的 durable CAS 优先于进程内 mutex。进程内 mutex 只能降低竞争，不能解决多宿主、重启或 replay 场景。

### 5.6 Late result 的 notification-only 路由

当父 turn 已经 finalized，late child/job result 仍然必须保留证据，不得静默丢弃。建议：

```text
settlement terminal
    -> upsert notification(subject, settlement_id, epoch)
    -> 保存 result summary / artifact refs / error evidence
    -> 标记 notification delivery 状态
    -> 不调用 SubmitPromptAsync
```

通知内容应明确：

```text
这是一个已完成父 turn 的迟到结果
原 parent_turn_id / episode_id
child/job 状态
result summary 或 artifact 引用
用户可以如何显式取回
```

对于 `explicit_followup_only`，通知可以提供稳定的 result/artifact ID，但不得把结果伪装成普通用户消息或自动控制提示。

### 5.7 Session-end、batch convergence、fallback 的统一收口

保留现有 drain/replay 机制，但所有入口改为：

```text
project lifecycle fact
    -> Schedule/claim durable wake
    -> ResumeAdmission
        -> same parked turn resume
        -> notification only
        -> stale audit
        -> retry
```

特别是：

- `EventSessionEnd` 不得仅因为 session idle 就启动新模型；
- API batch terminal projector 不得绕过 admission 直接 `SubmitPromptAsync`；
- fallback scanner 只负责发现未处理事实，不拥有“新建模型 turn”的权限；
- replay/recovery 必须使用同一 settlement/episode 去重键；
- parent busy 时可保留 durable wake，但 parent finalized 后不能因为下一次自然 drain 将它误变成 resume。

### 5.8 Terminal output drain

子任务或 background job 的 terminal 处理应区分：

```text
execution terminal
result projection terminal
output drain complete
notification/wake delivery complete
```

建议终态结果至少带：

```text
terminal_at
output_drained_at
result_version
late_output_count
late_output_discarded
```

producer 在 terminal 后继续写 output 时：

- 如果仍处于允许的 final drain 窗口，按顺序吸收并更新结果版本；
- drain 完成后，迟到 output 只记录计数/告警，不覆盖最终结果；
- 迟到 output 不能重新打开 obligation，也不能触发 automatic resume。

这一点同时适用于 batch task 的最后输出和 background manager 的 stdout/stderr descendants 场景。

### 5.9 后台作业专用语义：`terminal_observed` 不是 finalization gate

后台作业应复用 5.1–5.7 的父 turn 语义，但保留现有 job-level 观察优化。推荐的职责边界如下：

| 条件 | durable job/notification | 是否 ScheduleWake | 是否允许自动模型 turn |
| --- | --- | --- | --- |
| `task_output(wait=exit)` 活跃期间 job terminal，`terminal_observed=true` | 必须保留 | 默认否 | 否；当前 wait 结果已携带证据 |
| waiter 已结束，父 turn 仍 `RUNNING` | 必须保留 | 可记录 pending work | 不并发启动第二个 turn |
| waiter 超时后 job terminal，父 turn 已 `PARKED_WAITING_OBLIGATIONS` 且 job 为 required | 必须保留 | 是 | 仅恢复匹配的同一 turn/episode |
| waiter 超时后 job terminal，父 turn 已 `FINALIZED` | 必须保留 | 可作为 notification 投影 | 否 |
| detached job terminal，父 turn 任意状态 | 必须保留 | 可按通知策略调度/合并 | 默认否，除非 admission 明确允许 |
| `task_monitor` 的 progress check | 必须保留有界证据 | 使用 progress wake 规则 | 不得把 monitor 当作 terminal resume |

`terminal_observed` 的准确含义是：

```text
本次 terminal 事实将通过一个仍在进行的 task_output wait 到达模型
```

它不是：

```text
父 turn 已经 finalized
```

也不是：

```text
这个 job 可以绕过 parent-turn admission 直接启动新模型 turn
```

因此，`Observed=true` 时可以跳过一次冗余 `ScheduleWake`，但仍要写入 durable notification；`Observed=false` 时也只能表示“有一个待判定的 terminal fact”，不能直接等价为“允许 resume”。所有后台终态事件都必须经过 `ResumeAdmission`：

```text
ProjectBackgroundJobTerminal
    -> durable notification/result
    -> (Observed 优化：可抑制重复 wake)
    -> ResumeAdmission
        -> required + matching parked episode: same-turn resume
        -> finalized/detached/explicit-followup: notification only
        -> running: pending work / coalesce
        -> stale: audit and suppress
```

后台 job 创建时应捕获 parent identity，而不是只在 terminal 时按当前 session 反推：

```text
job_id
root_scope_id
owner_session_id
parent_session_id
parent_turn_id
parent_episode_id
settlement_id / settlement_cohort_id
finalization_policy
```

旧 job 只有 `session_id` 或缺少 policy 时，安全默认是 `NotifyOnly`；不能因为对应 actor 当前 idle 就把它升级为自动 resume。

---

## 6. 分阶段实施计划

### P0-1：建立身份与 policy 账本

目标：先让所有需要做决定的事实可被唯一识别。

工作项：

1. 盘点现有 `TurnSuspension`、batch/task、agent session、background job、wake 和 lifecycle notification 的 identity 字段；
2. 为 parent turn 补齐或明确 `turn_id`、`episode_id`、`settlement_cohort_id`；
3. 为 obligation 增加 `finalization_policy`；
4. 为 terminal projection 生成稳定 `settlement_id`；
5. 持久化前完成 identity/policy 校验；
6. 对旧数据提供兼容读取：缺失 identity 的记录只能进入 `NotifyOnly` 或 `RetryAdmission`，不能安全地自动 resume。
7. 后台 job 创建时把 `parent_turn_id`、`parent_episode_id`、`settlement_cohort_id` 和 `finalization_policy` 写入 job metadata 或等价 durable 关联；terminal 时禁止只按当前 `session_id` 反推父 turn。
8. 将 `terminal_observed` 定义为 job-level observation hint，不把它当作 parent finalization 或 resume authorization。

DoD：

```text
同一 child terminal event 在重放时得到相同 settlement_id
旧/缺失 episode 的 wake 不会启动模型
policy 能从 durable record 读取，而不是依赖内存 map
后台 job terminal replay 能保留原 settlement/epoch，且缺失 parent identity 时只能 NotifyOnly
```

### P0-2：统一 Resume Admission

目标：消除各宿主和各扫描器的自定义 resume 判定。

工作项：

1. 抽出 host-neutral admission service；
2. 将 CLI、API、recovery、fallback、batch projector 接入同一服务；
3. 将 `AcceptsResume` 拆分为：
   - `AcceptsNewTurn()`；
   - `AcceptsResumeOf(turnID, episodeID)`；
   - `AcceptsNotification()`；
4. parent finalized 时只允许 notification；
5. admission 失败时保留 durable wake，区分 stale、busy、retry、notify-only；
6. 只有 admission 成功后才允许将 wake 标记为 resume submitted。
7. `ProjectBackgroundJobTerminal`、CLI relay、API adapter、session-end drain 和 fallback scanner 均通过同一 admission；`Observed` 只作为重复 wake 优化输入。

DoD：

```text
所有自动模型启动点可检索到统一 admission 调用
任何 child completion callback 不直接调用 SubmitPromptAsync
同一 settlement 在并发 scanner 下最多产生一个 model turn
job terminal 在 parent finalized、detached 或缺失 identity 时不会自动创建 model turn
```

### P0-3：Parent finalization gate

目标：required child/job 未完成时不再把父逻辑 turn 标记为最终完成。

工作项：

1. 明确 agent loop 收尾点和 ACP/CLI/API response 提交点；
2. 在最终响应之前调用统一 finalization gate；
3. pending required obligation 自动 durable park；
4. finalization 使用 CAS，避免并发 finalizer；
5. finalization 前执行有界 event/result drain；
6. `MarkAssistantFinalResponseRendered` 绑定 turn/episode，并只在 gate 成功后调用；
7. 保留 detached job 的正常 finalization 语义；detached job 不加入 required obligation 集合。
8. 对 `background_task` 和 `task_output(wait=exit)` 路径验证 job terminal 与父 turn finalization 的竞态。

DoD：

```text
required obligation pending -> 不进入 FINALIZED
all required terminal + stable sequence -> 只允许一次 FINALIZED
finalized 后 late settlement -> 不会自动 resume
background job terminal -> 先做 parent-turn admission，不以 idle 作为充分条件
```

### P1-1：Transport / ACP 语义收敛

这是必须单独评审的兼容性工作。当前 ACP `EndTurn` 是同步 prompt response 的传输语义，而逻辑 turn 可能需要等待并恢复。

需要明确以下一种协议策略：

1. **强 gate 策略**：required obligation pending 时，不发送表示最终完成的 `EndTurn`；保持请求/会话处于可恢复状态，直到 obligation settle；
2. **挂起响应策略**：协议允许返回“已接受、仍有托管工作”的非终局响应，之后通过同一 session 的 continuation 机制提交结果；
3. **显式恢复策略**：传输请求结束，但 durable turn 状态为 suspended，后续恢复必须使用协议明确支持的 continuation，而不能伪造第二个最终 response。

选择不能由 wake consumer 隐式决定。若 ACP 只支持 `EndTurn`/`Cancelled` 等终局 response，则至少必须：

```text
不把 EndTurn 后的自动 resume 当成同一个 ACP response
不重复渲染第二个 final response
将 late result 降级为 notification，直到用户明确发起 follow-up
```

DoD：

```text
ACP 客户端不会收到两个互相竞争的最终 response
request_finished 与 logical_turn_finalized 在日志和状态上可区分
```

### P1-2：Terminal output / result drain

目标：避免 terminal 标记与最后一批输出乱序。

工作项：

1. 检查 background manager `WaitDelay` 与 child output drain 的边界；
2. 将 result projection 放在必要 output drain 之后；
3. 对 terminal 后 producer output 增加丢弃/吸收指标；
4. late output 不重开 obligation，不触发 resume；
5. replay 时以 result version/settlement ID 去重。

### P1-3：Exactly-once automatic resume

目标：把 wake 投递去重扩展到模型调用边界。

工作项：

1. 建立 settlement consumption 状态机；
2. 给 automatic resume model turn 分配稳定 idempotency key；
3. `resume_claimed -> resume_submitted` 使用 durable CAS；
4. 崩溃恢复根据 model turn evidence 继续或标记完成；
5. 为 notification-only 和 automatic resume 使用不同的消费状态，避免互相覆盖。

### P2：可观测性、迁移与清理

建议新增指标/日志字段：

```text
parent.finalization.blocked_required_obligation
parent.finalization.cas_conflict
parent.finalization.stable_sequence_retry
supervision.resume.admitted
supervision.resume.notify_only_finalized
supervision.resume.reject_stale
supervision.resume.duplicate_suppressed
supervision.resume.model_turn_created
supervision.resume.model_turn_recovered
settlement.late_output_discarded
settlement.notification_only
background_job.terminal_observed_suppressed_wake
background_job.terminal_after_finalization_notify_only
background_job.missing_parent_identity_notify_only
background_job.stale_episode_suppressed
```

debug/digest 至少可显示：

```text
parent turn / episode
required obligation total / terminal
detached result total
finalization state
last resume admission decision
pending wake reason
stale settlement count
```

---

## 7. 回归测试与验收矩阵

### 7.1 Finalization gate

1. required child pending 时，父收尾被阻止并创建 suspension；
2. child terminal 后，父恢复同一 `turn_id` / `episode_id`；
3. required child terminal 但 durable resolver 暂时读不到时，不得乐观 final；
4. finalization 两次读取 sequence 不稳定时，进入 retry/park，不得 final；
5. 并发 finalizer 只有一个 CAS 成功；
6. detached background job pending 时，父仍可 finalized；
7. explicit follow-up job 不阻止父 finalization；
8. `background_task` 返回 job handle 后，job pending 不阻止 detached parent finalization。

### 7.2 Late result 与 admission

9. parent finalized 后 child terminal 只落 notification/result，不提交模型 turn；
10. parent finalized 后 fallback scanner 也只能 notification；
11. session-end drain、batch projector、fallback 同时处理同一 settlement 时，只产生一个动作；
12. `turn_id` 匹配但 `episode_id` 不匹配时判 stale；
13. `episode_id` 匹配但 settlement 已消费时不重复 resume；
14. 无匹配 suspension 的 required result 不得直接创建新 turn；
15. parent 正在 `RUNNING` 时，child completion 只记录 pending work，不并发启动第二个模型 turn；
16. parent `RESUMING` 时重复 wake 被合并或抑制；
17. `background_task` 后父 turn finalized，job terminal 只产生 notification，不调用 `SubmitPromptAsync`；
18. `task_output(wait=exit)` 活跃期间 terminal 的 `terminal_observed` 只抑制重复 wake，不抑制 durable notification；
19. `task_output(wait=exit)` 超时、父 turn finalized 后 job terminal 仍是 notification-only；
20. waiter 已结束但父仍 `RUNNING` 时，job terminal 不并发启动第二个 parent model turn；
21. 缺少 `parent_turn_id` / `parent_episode_id` 的旧 job terminal 默认 `NotifyOnly`；
22. job terminal 绑定旧 episode 时判 stale，不投递给当前新 episode；
23. CLI 与 API 的 background terminal projection 对同一 admission 矩阵给出相同结果。

### 7.3 Exactly-once 与故障恢复

24. wake claim 后进程崩溃，recovery 不产生重复 model turn；
25. model turn 已提交但 delivery receipt 丢失，recovery 能通过 turn evidence 收敛；
26. notification delivery 失败可重试，但不升级为 automatic resume；
27. terminal projection replay 不改变 settlement ID，不增加模型调用；
28. 多宿主并发消费同一 settlement 时，只有一个 CAS 获得 resume claim；
29. 同一 background job terminal epoch replay 不增加 notification、wake 或 model turn；
30. `Observed=true` 与 `Observed=false` 的 notification identity 保持相同，只有 wake 行为按观察状态变化。

### 7.4 Terminal output

31. terminal 前最后一批 output 被 drain 后才对外公布稳定结果；
32. terminal 后 output 在 drain 窗口内按版本合并；
33. drain 完成后的迟到 output 被计数并丢弃，不覆盖结果；
34. 迟到 output 不重新打开 obligation、不触发 wake/resume；
35. background job descendants 持有 stdout/stderr 时，`WaitDelay` 与最终结果状态一致；
36. monitor check 在 job terminal 后自动解除，不会补发 terminal 后的 progress wake；
37. terminal drain 完成后的 late output 不重新打开 job obligation 或 parent admission。

### 7.5 ACP / CLI / API

38. ACP 不会因为 child/job late result 发送第二个 final response；
39. CLI local host 与 API host 对同一 admission 矩阵给出相同结果；
40. interactive session 恢复不会在启动阶段绕过 admission drain；
41. headless/recovery 模式不会因 replay 产生重复结论；
42. 原有无异步 obligation 的普通 prompt 行为保持不变。

### 7.6 测试命名建议

实现阶段可按现有包组织测试，名称不要求完全一致，但应覆盖类似语义：

```text
TestFinalizationBlocksPendingRequiredObligation
TestFinalizationAllowsDetachedJob
TestSettlementResumesOnlyMatchingParkedEpisode
TestFinalizedParentLateSettlementIsNotificationOnly
TestDuplicateSettlementCreatesAtMostOneResumeTurn
TestStaleEpisodeWakeIsRejected
TestConcurrentResumeAdmissionUsesCAS
TestRecoveryAfterResumeClaimDoesNotDuplicateModelTurn
TestLateOutputAfterDrainDoesNotReopenSettlement
TestACPDoesNotEmitSecondFinalResponseAfterLateChildCompletion
TestBackgroundJobAfterFinalizedParentIsNotificationOnly
TestBackgroundTaskMissingParentIdentityDoesNotResume
TestTaskOutputObservedTerminalSkipsWakeKeepsNotification
TestTaskOutputTimeoutThenLateTerminalDoesNotResumeFinalizedParent
TestBackgroundTerminalReplayDoesNotCreateSecondModelTurn
TestBackgroundMonitorDisarmsOnTerminal
```

---

## 8. 兼容性、迁移与回滚

### 8.1 旧记录兼容

旧 batch/job/wake 记录可能没有 `episode_id`、`settlement_id` 或 `finalization_policy`。安全默认值：

```text
缺少 parent identity -> 不自动 resume，进入 notification/review
缺少 settlement identity -> 使用可验证的历史 event epoch 派生临时 key；派生失败则不自动 resume
缺少 policy -> 由既有 execution mode 映射；无法判断时 NotifyOnly
```

不能为了保持旧行为而把无法证明归属的 late wake 自动投递给当前 idle actor。

approval 旧行没有 `parent_turn_id` / `parent_episode_id` 时默认 `NotifyOnly`；
不保留“idle 即自动唤醒”的兼容分支，因此不需要新增旧 approval 灰度开关。
progress / wait-feedback 的 bounded legacy idle 行为是显式例外，不属于 approval。

### 8.2 灰度开关

建议提供：

```text
supervision.parent_finalization_gate
supervision.resume_admission_v2
supervision.resume_exactly_once
supervision.late_result_notification_only
supervision.terminal_output_drain_guard
supervision.background_job_parent_admission
supervision.background_job_missing_identity_notify_only
```

灰度顺序：

1. 只记录 admission decision，不改变动作；
2. 对 finalized parent 启用 notification-only；
3. 对 required parked turn 启用 same-episode resume；
4. 启用 finalization gate；
5. 启用 exactly-once resume CAS；
6. 默认开启并移除旧的分散判定。

### 8.3 回滚

回滚不能删除 durable identity、settlement 或 notification。若关闭新 admission：

```text
已有 finalized parent 的 late result 继续 notification-only
已有 parked turn 继续保留 suspension
新建任务回退到兼容 execution mode，但必须继续写 policy/identity
```

这样可以防止回滚后重新把历史 late event 当成新 wake。

---

## 9. 风险与未决决策

### 9.1 ACP 的“挂起但未终局”表达（决策：显式 continuation）

决策（2026-11-04）：**不新增 ACP stop reason**。`EndTurn` 继续表示本次
`session/prompt` 请求结束；逻辑 turn 的 parked 状态通过 ACP 的 `_meta`
扩展表达，下一次显式 `session/prompt` 才是 continuation 请求。

```text
PromptResponse.stopReason = end_turn       # transport request finished
_meta.ai-agent-runtime.turn_phase=parked  # logical turn not final
_meta.ai-agent-runtime.parked=true
_meta.ai-agent-runtime.final=false
_meta.ai-agent-runtime.continuation=explicit_prompt
_meta.ai-agent-runtime.resume_turn_id / resume_episode_id
```

约束：

- ACP host 不接自动 supervision wake consumer，late result 不会产生第二个 final
  response；
- 下一次显式 prompt 由 durable actor state 反向判定是否复用同一
  turn/episode，不依赖 actor 当前是否 idle；
- finalized/canceled 状态返回 `final=true` / `continuation=new_episode`；
- 客户端不理解 `_meta` 时可以忽略，仍保持 ACP v1 的 `end_turn` 兼容性。

安全底线保持不变：

```text
EndTurn 之后不自动发送第二个最终 response
late result 只作为 durable notification
用户显式 follow-up 才能启动 continuation/new episode
```

### 9.2 成功 child 是否默认唤醒

本方案不要求所有成功 child 都触发模型调用。是否 wake 由 policy 决定：

```text
required + parked -> resume
required + running -> pending work
 detached -> notification
explicit_followup -> result store
```

这样不会把“成功不唤醒”的现有预算策略与“required 任务必须可续跑”混为一谈。

### 9.3 多个 required child 的 cohort 语义

同一父 turn 一次 dispatch 的多个 child，建议使用 `settlement_cohort_id` 合并成一次 resume。单个 child terminal 不应在 cohort 未完成时连续启动多个父模型 turn。

### 9.4 Parent turn 内新增 obligation

finalization gate 二次检查期间可能发生新的 dispatch。必须规定：

```text
新 obligation 在 FINALIZED CAS 前成功落账 -> 阻止 finalization
FINALIZED CAS 后才创建 -> 视为新 episode/explicit follow-up 关联，不得修改已完成 episode
```

因此 obligation 创建必须与 finalization CAS 共享可验证的版本/epoch 规则。

### 9.5 通知与控制提示的边界

`subagent-settled`、job terminal、late result 应作为结构化 notice/event 投影，不应无条件拼接成普通用户消息或控制面 prompt。只有 admission 允许 resume 时，才生成带控制面标记的同一 turn continuation。

### 9.6 后台作业默认 policy 与旧 job 迁移

后台 job 的产品语义必须在 dispatch API 层确定，不能由 terminal handler 根据 job 是否成功、父 actor 是否 idle 临时猜测。建议默认策略为：

```text
background_task 普通提交       -> detached_notification_only
明确等待并要求纳入当前答案     -> required_for_finalization
只保存结果、等待用户查看       -> explicit_followup_only
```

旧 job 通常只有 `session_id`、job status 和 terminal epoch，无法可靠恢复它属于哪个 parent turn/episode。迁移时：

1. 可验证存在 parent identity 和 policy 的 job，补齐 settlement/关联记录；
2. 只有 session identity、没有 turn/episode 的 job，保留 durable notification，但默认 `NotifyOnly`；
3. 无法确定 terminal epoch 或旧 replay 身份的 job，不自动补发 resume wake；
4. 用户明确 follow-up 后，才允许新 episode 读取这些历史 job 结果；
5. migration/replay 不能把“当前 actor idle”作为旧 job 自动恢复的理由。

该默认会牺牲少量旧任务的自动汇报，但优先保证不会把不属于当前 episode 的结果注入新结论。

---

## 10. 完成标准

本方案落地后，以下陈述必须都成立：

```text
1. “request finished” 与 “parent turn finalized” 在状态和日志中可区分。
2. required obligation 未终态时，父逻辑 turn 不能 FINALIZED。
3. detached job 不会阻塞父 turn，也不会在晚到时自动重开模型。
4. parked turn 的 child settlement 只能恢复匹配的同一 turn/episode。
5. finalized parent 的 late settlement 只产生 durable notification/result evidence。
6. 所有自动 wake 入口共享同一 ResumeAdmission。
7. 同一个 settlement cohort 最多产生一个 automatic resume model turn。
8. 旧 turn/episode 的 late event 不会污染新 turn。
9. terminal output drain 完成前不会过早宣告稳定结果；drain 后 late output 不覆盖结果。
10. ACP、CLI、API、recovery 和 fallback 路径在上述语义上保持一致。
11. `terminal_observed` 只抑制已经由活跃 `task_output` wait 覆盖的冗余 wake，不能替代 finalization gate。
12. `background_task` 在父 turn finalized 后完成，只产生 notification/result evidence，不自动调用模型。
13. `task_output(wait=exit)` 超时后 job 迟到完成，不会因为 waiter 已注销而绕过 parent-turn admission。
14. 缺失后台 job parent identity 的旧记录默认只能 NotifyOnly，不能投递给当前 idle actor。
```

最终行为应稳定为：

```text
required child pending
    -> parent turn park

required child settled
    -> same logical turn resume once

parent finalized
    -> late child/job result notification only

user explicitly asks to continue
    -> new episode reads stored result
```

---

## 11. 实施记录

本文件记录的是 2026-11-03 对当前 runtime、Codex 和 DeepSeek Harness 生命周期语义的分析与目标方案。v1.1 补充了后台 job 的 terminal projection、`task_output(wait=exit)` / `terminal_observed`、CLI/API wake 入口及旧 job 迁移语义。本轮仍未修改实现代码，也未宣称上述 P0/P1/P2 已落地。

落地后应在此节追加：

- 实际修改的文件与 schema migration；
- finalization gate、admission、settlement 状态机的最终接口；
- ACP transport 决策；
- 测试命令与结果；
- 灰度开关默认值；
- 已知残留风险与回滚点。
### 11.1 P0 实施记录（2026-11-03/04）

状态：P0 已落地并通过核心包测试；P1/P2 仍按本文后续阶段推进。

#### 已落地内容

1. **parent identity / episode identity**
   - `subagentbatch.TurnSuspension` 新增 `EpisodeID` 与 `ObligationPolicies`；
   - `chat.RuntimeState` 新增 `CurrentEpisodeID`、`SuspendedEpisodeID`、`TurnPhase`、`FinalizedTurnID`、`FinalizedEpisodeID`；
   - `agent` / `toolctx` context 新增 episode 传播；
   - `SessionActor` 在挂起 turn 恢复时复用 parked episode，用户显式 follow-up 时生成新 episode；
   - `loop.parkBackgroundTurn` / `parkTeamObligation`、CLI/API child obligation 写入 episode。

2. **wake / settlement identity**
   - `WakePending` / `WakeRequest` 新增 `ParentTurnID`、`ParentEpisodeID`、`FinalizationPolicy`、`SettlementID`、`SettlementCohortID`；
   - `BackgroundJobTerminalInput` / `BackgroundJobMonitorInput` 同步携带上述字段；
   - 后台 job 创建时通过 `background.JobOrigin` 从 tool run context 捕获 parent identity，写入 job metadata；
   - `ProjectBackgroundJobTerminal` 默认 policy 为 `detached`；缺失 identity 时不升级为自动 resume；
   - `ProjectAgentCompletion`、batch terminal、run terminal 等终态投影声明 `required` policy；
   - `ScheduleSettledTurnWakeWithTeams` 写入稳定 `SettlementID/SettlementCohortID`。

3. **统一 Resume Admission**
   - 新增 `supervision.ResumeAdmissionService`，CLI/API 共用同一 decision logic；
   - 判定矩阵已实现：required + matching parked turn/episode + required obligations terminal => resume；detached/explicit/finalized/no suspension/missing identity => NotifyOnly；episode mismatch => RejectStale；busy/unreadable/pending => RetryAdmission；
   - wake 未携带 turn 时，可凭 `obligation_id` 在 durable suspension 账本中唯一反推 turn/episode；歧义不猜测；
   - `DrainRunnableWithAdmission` 在预算/claim 前消费 NotifyOnly/Stale wake，不启动模型 turn；
   - `WakeConsumer` 在投递前执行 durable settlement claim。

4. **exactly-once automatic resume**
   - `supervision` migration v10 新增 `supervision_resume_claims`；
   - claim key = `(parent_session_id, parent_turn_id, parent_episode_id, settlement_cohort_id)`；同一 park epoch 的多个 wake 共享一个 claim，新 park epoch 使用新 cohort；
   - `claimed -> submitted` 使用 version CAS；`claimed -> released` 仅在投递失败/明确可重试时使用；
   - get claim 失败按 `RetryAdmission` 处理，不再 fail-open 成 resume。

5. **terminal observed / replay**
   - `terminal_observed` 仍只抑制冗余 wake，durable notification 保留；
   - `Observed=true` 现在同时写入 delivery consumption ledger（`consumption=observed`），因此后续同 epoch replay 即使 `Observed=false` 也不会再排 wake；
   - `WakeDelivered` 新增 `Consumption` / `Reason`，区分 `model_turn`、`notification_only`、`stale`、`observed`。

6. **parent finalization / phase**
   - `SessionActor` 收尾时重新读取 suspension，存在则写 `TurnPhase=parked`，不存在才写 `finalized`/`canceled`；
   - suspension 读失败时保守地保持 turn 打开，不乐观 finalize；
   - `agent.Result` 新增 `EpisodeID` / `TurnParked`，loop run-end settlement 后给出最终化事实；
   - ACP 仍以 `EndTurn` 表示 request finished；本轮不新增第二个 ACP final response。

#### Schema migrations

- `subagentbatch` v4：`turn_suspensions.episode_id`、`obligation_policies_json`、episode index；
- `supervision` v9：wake parent episode/policy/settlement 列；`supervision_wake_delivered.consumption/reason`；
- `supervision` v10：`supervision_resume_claims` 表与 settlement index。

全部 additive + 零值默认；旧行保持可读。

#### 测试结果

已通过：

```text
go test ./internal/subagentbatch/...
go test ./internal/supervision/...
go test ./internal/background/...
go test ./internal/toolbroker/...
go test ./internal/agent/...
go test ./internal/chat/...
go test ./internal/runtimeserver/...
go test ./internal/api/runtimeapi/...
go vet ./internal/supervision/... ./internal/subagentbatch/... ./internal/background/... ./internal/toolbroker/... ./internal/agent/... ./internal/chat/... ./internal/runtimeserver/... ./internal/api/runtimeapi/... ./cmd/aicli/commands/...
go test -run '^$' ./...
```

新增重点用例：

```text
TestResumeAdmissionMatrix
TestResumeAdmissionClaimIsExactlyOnce
TestResumeAdmissionAllowsNextParkedEpochInSameEpisode
TestDrainRunnableWithAdmissionConsumesNotifyOnlyWithoutTurn
TestProjectBackgroundJobTerminal_CarriesParentIdentityAndPolicy
TestProjectBackgroundJobTerminal_DefaultsToDetached
TestProjectBackgroundJobTerminal_ObservedJobSkipsTheWake（扩展 replay 抑制断言）
TestProjectAgentCompletionCarriesRequiredAdmissionPolicy
TestBackgroundJobAfterFinalizedParentIsNotificationOnly（API）
TestLocalBackgroundJobAfterFinalizedParentIsNotificationOnly（CLI）
TestSubmitPromptOnSuspendedTurnResumesSameTurnID（扩展 episode/phase 断言）
TestSubmitPromptAfterFinalizedStartsNewEpisode
TestSQLiteBatchStoreTurnSuspensionRoundTrip（扩展 episode/policy 断言）
TestSubmitShellPersistsJobOrigin
```

全量 `go test ./...` 仍存在与本方案无关的既有失败，包括：

```text
cmd/contractgen generated-file check
internal/configwriteguard
internal/toolctl
internal/toolkit（shell job runtime 未配置）
internal/background PTY / timeout 相关环境用例
cmd/aicli/commands TestChatRuntimeEventBridge_ReplayRestoresInteraction
```

其中最后一项在本轮改动前即与本方案无代码交集（未修改 `chat_runtime_events.go` / 对应测试），单独复跑仍失败。核心包与本次新增测试均通过。

#### 残留风险

1. progress/approval/self-check/session-end 的 admission 仍未全部强制；本轮已把终态 lifecycle / obligation settlement / background job 接入，其余非终态旧入口保持兼容，留待 P1。
2. settlement claim 在 `claimed` 后、`MarkSubmitted` 前崩溃时采取“不再重复起模型”的保守策略；基于 model turn evidence 的恢复收敛尚未实现。
3. ACP 非终局响应协议未改动；当前依赖 ACP 不接自动 wake consumer 来保证不产生第二个 final response。
4. terminal output drain 的版本化吸收/丢弃指标未完整实现。
5. 后台 job 目前默认为 detached；`required_for_finalization` job 的创建 API 尚未开放（当前只有 `task_output(wait=exit)` 同步等待语义）。

### 11.2 P1 增量实施记录（2026-11-04）

#### Resume claim 崩溃恢复

- 新增 `Store.RecoverStaleResumeClaims(ctx, claimedBefore)`；
- `ResumeAdmissionService` 新增 `ClaimStaleAfter`（默认 5 分钟）与 `RecoverStaleClaims`；
- 每次 `WakeConsumer.MaybeWakeParent` 排空前先收敛超时仍为 `claimed` 的 claim：
  ```text
  claimed (超时) -> submitted (model_turn_id=recovered_unknown)
  ```
- 该恢复只防止重复模型 turn，不重试投递；durable notification/result evidence 保留。
- 新增测试：
  ```text
  TestResumeAdmissionRecoversStaleClaimAsSubmitted
  ```

#### 非终态 legacy wake 的 finalized 防线

`ResumeAdmissionService` 对 policy 为空、非 terminal/settlement 的旧 wake（progress / wait-feedback 等）增加父状态检查：

```text
parent finalized/canceled  -> NotifyOnly
parent running/waiting     -> RetryAdmission
parent idle/unknown        -> ResumeLegacy（保留既有 progress 行为）
```

因此：

- progress check / wait feedback 在 finalized parent 上不会再启动模型 turn；
- turn-end self-check 的直投路径也先经过 admission：NotifyOnly/Stale 先消费；ResumeSameParkedTurn 使用 durable claim，投递成功后记录 delivery 并消费 wake；存在 Retry 或无可投递内容时不启动额外 turn；
- 既有 idle progress 行为保持不变。

新增测试：

```text
TestResumeAdmissionMatrix/legacy progress is allowed for idle non-finalized parent
TestResumeAdmissionMatrix/legacy progress is notification only after finalization
TestResumeAdmissionMatrix/legacy progress waits while parent is busy
TestSelfCheck_FinalizedParentConsumesNotifyOnlyWithoutTurn
TestSelfCheck_ClaimsAndConsumesAdmittedSettlement
TestLateOutputAfterTerminalIsDiscardedAndCounted
```

#### Terminal output / ACP 状态

- background manager 的终态事件分类仍只由 terminal 事件触发 wake；output 事件不进入 supervision terminal 投影，因此迟到 output 不会重开 obligation；
- CLI relay 回归扩展为 terminal 后继续注入 output frame，断言 notification 数不增加；
- background terminal transition 写入 `output_drained_at`；terminal 后到达的 producer output 只累计 `late_output_count` / `late_output_discarded`，不写入 buffer、不追加 output event；
- `TaskOutputResult` 暴露 `output_drained_at` / `late_output_count` / `late_output_discarded`；
- ACP host 未接入自动 wake consumer，`EndTurn` 仍只表示 request finished；P1-1 的“第二个 final response”防线保持为显式恢复策略；
- `WaitDelay`、`terminal_observed`、monitor disarm 与 replay 抑制已有测试覆盖，本轮未改变其语义。

P1 增量验证通过：

```text
go test ./internal/supervision/...
go test ./internal/api/runtimeapi/...
go test ./cmd/aicli/commands/ -run 'SelfCheck'
go test ./cmd/aicli/commands/ -run 'TestLocalBackgroundJob|TestLocalSupervisionProgress|...'
go vet 相关包
```

`internal/background` 的 PTY 用例 `TestOpenTerminalPTYSurvivesCallerContextCancel` 仍受当前 Windows/PTY 环境影响；与本次 supervision admission 改动无交集。

### 11.3 P2 可观测性增量（2026-11-04）

`/supervision/metrics` 的 `MetricsSnapshot` 新增：

```json
{
  "resume_admission": {
    "claimed": 0,
    "submitted": 0,
    "recovered": 0,
    "released": 0,
    "model_turns": 0,
    "notification_only": 0,
    "stale": 0,
    "observed": 0
  }
}
```

数据来源：

- `claimed/submitted/recovered/released`：`supervision_resume_claims`；
- `model_turns/notification_only/stale/observed`：`supervision_wake_delivered.consumption`。

新增可选接口：

```go
type ResumeAdmissionMetricsReader interface {
    ReadResumeAdmissionMetrics(ctx context.Context, since, until time.Time) (ResumeAdmissionMetrics, error)
}
```

旧 store / 测试 fake 不实现时，指标带 note 降级，不影响原有 §7.3 读数。

新增测试：

```text
TestMetricsSnapshotResumeAdmissionLedger
```

#### Resume claim retention

- `supervision_resume_claims` 新增 retention：
  ```text
  ClaimRetention      默认 7 天
  ClaimPruneInterval  默认 1 小时（恢复排空时机会式触发）
  ```
- 只清理 `submitted` / `released`；`claimed` 永不删除，避免重新打开 duplicate resume 窗口；
- `PruneResumeClaims` 使用 `COALESCE(submitted_at, claimed_at)` 作为保留时间基准。

新增测试：

```text
TestResumeAdmissionPrunesTerminalClaimsOnly
```

#### Host 周期 claim maintenance

- CLI 的 wake fallback tick 与 API 的默认 wait-feedback sweep 现在即使没有
  pending wake / 已挂起父会话，也会调用
  `ResumeAdmissionService.RecoverStaleClaims`；
- 这样长期空闲的宿主不会永久保留崩溃遗留的 `claimed` 行，也不会错过
  `ClaimPruneInterval` 控制的机会式 retention；
- claim 维护失败是 best-effort，不阻塞同 tick 的 wait-feedback 与 wake 排空。

新增测试：

```text
TestLocalWakeFallbackRecoversStaleClaimsWithoutPendingWakes
TestAPIWaitFeedbackSweepRecoversStaleClaims
```

### 11.4 Finalization durable sequence 稳定性检查（2026-11-04）

actor 在标记 `ParentTurnPhaseFinalized` 前新增 durable event sequence gate：

```text
S1 = EventStore.LastEventSeq(session)
读取 §6.12 suspension ledger
静默窗口（2ms）
S2 = EventStore.LastEventSeq(session)
S1 != S2 -> 重新读取 suspension ledger
发现新 suspension -> PARKED（优先于 finalized）
sequence 稳定 -> 再读取一次 suspension ledger（batch 账本独立于事件序列）
连续漂移 -> 有界重试（3 次）后保留审计证据
```

实现要点：

- `EventStore` 额外实现 `EventSequenceStore` 时启用；自定义/旧 store 不实现时退化为原有单次 suspension 读取，不会永久阻塞 turn；
- 序列漂移本身不等于 obligation，不能单独据此 park；每次漂移后重新读取 durable suspension，只有真实挂起记录才阻止 finalization；
- 即使 sequence 稳定，也会在最后一次快照后重新读取 suspension ledger：batch 挂起记录与 runtime event 是不同 durable plane，不能假设挂起一定会推进事件序列；
- 有界重试耗尽且没有 materialize 任何 required suspension 时，finalization 继续执行，并写入：
  ```text
  parent.finalization.stable_sequence_retry
  ```
  载荷包含 `stable`、`attempts`、`sequence_before`、`sequence_after`、可选 `error`；
- 这样可捕获“loop 已返回但尾部事件仍在落盘”的窗口，同时避免无关 mailbox/事件流量把会话永久卡在 finalizing。

新增测试：

```text
TestFinalizationSequenceGateObservesQuietWindow
TestFinalizationSequenceGateRetriesMovingSequence
TestFinalizationSequenceGateRevalidatesNewSuspension
TestFinalizationSequenceGateRevalidatesSuspensionAfterStableSequence
TestFinalizationSequenceGateReadFailureIsAuditable
TestFinalizationSequenceGateSkipsUnsupportedStore
TestFinalizationSequenceGateEmitsRetryAuditEvent
```

### 11.5 CLI supervision metrics / state 读数（2026-11-04）

`/debug supervision metrics [--team <team_id>] [--limit N]` 复用
`supervision.CollectMetricsSnapshot`，与 HTTP `/supervision/metrics` 同源：

```text
resume_admission claimed/submitted/recovered/released
                 model_turns/notification_only/stale/observed
cancel           total/forced/decision_window_expired + ratios
report_latency   samples/p50_ms/p95_ms/max_ms/unavailable
```

默认窗口为最近 24 小时；`--limit` 控制 execution-row window 读取上限。
输出是只读的，不修改 notification、wake 或 claim 状态。

新增测试：

```text
TestChatDebugSupervisionMetricsShowsResumeAdmission
```

另新增只读命令：

```text
/debug supervision state
```

输出 durable parent turn 状态与证据：

```text
status / turn_phase
current_turn / current_episode
suspended_turn / suspended_episode
finalized_turn / finalized_episode
suspension ledger（turn / episode / obligations / resume_queue）
obligation progress（total / terminal / all_terminal）
pending wake 数量与原因
unresolved notification 数量
```

`RuntimeState` 字段明确按派生缓存展示；挂起义务以 durable batch 记录为准。

新增测试：

```text
TestChatDebugSupervisionStateShowsFinalizationAndObligations
```

### 11.6 Claim lease heartbeat（2026-11-04）

为解决长模型回合超过 `ClaimStaleAfter` 后被其他宿主误判为崩溃遗留的问题，
`claimed` claim 增加租约心跳：

```text
ClaimRenewInterval = ClaimStaleAfter / 3
WakeConsumer delivery 期间周期刷新 claimed_at
```

实现约束：

- `RenewResumeClaim` 只更新 `claimed_at`，**不递增 version**：心跳不是状态迁移，
  持有者后续 `MarkSubmitted` / `Release` 的 CAS token 仍然有效；
- 只续租 `state='claimed'` 的行；已 submitted/released 的 claim 返回 false；
- 心跳覆盖普通 wake drain 与 turn-end self-check 两条投递路径；
- 心跳失败是 best-effort；进程真正崩溃后仍由 stale recovery 收敛为
  `submitted/recovered_unknown`，不会重开模型 turn。

新增测试：

```text
TestResumeAdmissionRenewsClaimBeforeRecovery
TestWakeClaimRenewalHeartbeat
```

### 11.7 FINALIZING durable phase（2026-11-04）

在 sequence gate 运行前先把 owning turn 持久化为：

```text
TurnPhase = finalizing
```

语义：

- `finalizing` 不是终态，只表示“同步请求已返回、逻辑 turn 正在执行最终化检查”；
- 只有 `CurrentTurnID` 匹配的 turn 可以进入该阶段；
- 已处于 `parked` / `finalized` / `canceled` 的状态不会被重新打开；
- gate 完成后仍按原规则收敛到 `parked` / `finalized` / `canceled`；
- 最终状态写入拒绝覆盖已经收敛的终态，避免并发 finalizer 互相回写 identity；
- `/debug supervision state` 可直接观察到该阶段。

新增测试：

```text
TestMarkTurnFinalizingIsScopedAndIdempotent
```

### 11.8 Parked / resume observability 收敛（2026-11-04）

真实 ConPTY 验证暴露的客户端语义问题已收敛：

- `assistant_message` / `session_end` 新增：
  ```text
  episode_id
  turn_phase = parked | finalizing | finalized | canceled
  turn_parked
  final
  ```
  parked 状态下 `final=false`，客户端不再需要仅凭 `session_end` 猜测逻辑 turn
  是否已经结束。
- `parent.turn.parked` / `parent.turn.finalized` / `parent.turn.canceled`
  使用同一套逻辑回合字段（`episode_id` / `turn_phase` / `turn_parked` /
  `final`），phase 事件本身也能独立表达"挂起但未终局"。
- `session_start` 新增：
  ```text
  episode_id
  continuation = user | user_resume | supervision_wake
  ```
  保留旧 `resume` 字段兼容性；`resume=false` 不再表示"一定是新 turn"。
  progress-only digest 也统一携带 `[子任务与后台作业进展]` 控制面头，避免
  P2-D 唤醒被误分类成 `continuation=user`。
- `turn.resumed` 明确区分阶段：
  ```text
  phase = started   # wake 投递成功，resume episode 开始
  phase = settled   # run 内结清挂起记录
  ```
  两个阶段都携带 `turn_id`；能解析到挂起记录时同时携带 `episode_id`，客户端无需
  等下一次 `session_start` 才能把恢复边沿与 parked/finalized 事件对上。
- CLI 与 API 宿主的 wake-start `turn.resumed` 都直接写入 session EventStore
  （带 `seq`）后再发布总线，避免只依赖事件桥订阅时序导致审计缺失。
- CLI 事件桥不再让 turn 所有权守卫吞掉 `turn.resumed`：started 边沿会同步进入
  `runtime-events.jsonl`，客户端重放同时看到恢复开始与 settled 闭合；turn-end
  self-check 也复用同一 `ResumeAnnouncement(phase=started)` 里程碑。

新增/更新测试：

```text
TestRunEndStampsSuspendedTurnIDForParkedTurn（扩展 parked payload 断言）
TestContinuationKindClassifiesWakeWithoutChangingResume
TestBuildDigest_ProgressOnlyStartsWithControlPlaneHeader
TestLocalTurnResumedEventPayload（扩展 phase/seq/持久化断言）
TestPublishTurnResumedPersistsStartPhase
TestChatRuntimeEventBridge_WakeStartResumeReachesEventLog
TestSelfCheck_ClaimsAndConsumesAdmittedSettlement（扩展 started 播报断言）
```

最终真机证据（ConPTY，`magpie / auto/deepseek-v4.1-flash-1017 / max`）：

```text
output/conpty-real-e2e/run-20261104-113825/verification-summary.json
verdict = true

required subagent:
  parent.turn.parked -> turn.resumed(phase=started) -> session_start(continuation=supervision_wake)
  -> parent.turn.finalized（same turn_id + same episode_id）

detached job:
  parent.turn.finalized before job completion
  terminal wake = notification_only
  resume_claims = 0
  no second session_start after finalize
```

### 11.9 Required background job 公共能力（2026-11-04）

`job_start` 现在显式支持父 turn 最终化策略：

```text
finalization_policy = detached | required | explicit_followup
default              = detached
```

落地内容：

1. **工具面与结果面**
   - `job_start` schema、参数审计和类型校验都接受 `finalization_policy`；
   - `required` 在启动前校验 submitting `turn_id` / `episode_id` 和 durable job
     store，缺失时 fail closed，不做静默降级；
   - 返回值与 `Job.Metadata` 同时携带 policy、`parent_turn_id`、
     `parent_episode_id`，模型可以看见本次 job 是否阻止当前 turn 收尾；
   - `interactive` 命中已打开会话时，`required` 不允许“收养”旧 job（旧 job
     的 origin 属于另一个 turn），返回可修复错误。

2. **Durable obligation**
   - 新增 `job:<job_id>` obligation 身份；它与 batch / `agent_session:` /
     `team:` 共用 `TurnSuspension`，无需新表或数据迁移；
   - agent loop 在 `job_start` 结果返回后按 policy 写挂起记录；已 terminal 的
     job 和 `detached` / `explicit_followup` 不 park；
   - 写挂起前要求 suspension store、job store 和 resolver 同时可用；任一缺失
     只投影 I9 降级，不留下无法结算的 durable suspension。
   - job terminal 投影补齐 `interrupted` / `expired` / `abandoned`，避免
     required job 在这些终态下永久悬挂。

3. **统一 resolver / admission**
   - `background.Manager` 实现 `BackgroundJobTerminal`（durable 行 + terminal
     判定）与 `JobStoreDurable`（I9 probe）；
   - `subagentbatch.ObligationResolvers` 统一承载 child / team / job 三类非
     batch obligation；settle、strict resume、wait feedback、wait ledger、
     resume prompt 和 recovery 都使用同一 resolver set；
   - `ResumeAdmissionService` 的 required + matching parked turn + episode +
     terminal job 判定走同一 CAS claim；running / missing job 保持
     `RetryAdmission`，不会因 actor idle 启动第二个模型 turn；
   - CLI 与 API 宿主都把同一个 background manager 接入 loop 与 admission；
     API 的延迟 manager 构造不会让早先接线的 obligation source 丢失 job 行。
   - resolver set 同时保存在 Agent 上，`NewReActLoop` 为未显式配置的字段补齐
     默认值，`ChildAgentFactory` 把同一组 resolver 传给 in-process child
     agent；嵌套 turn 不再因为缺少 job/team/child reader 而只能降级。

4. **兼容性**
   - 旧 job 没有 origin 时继续按 detached / notification-only 处理；
   - `job_control(requeue)` 不复用旧 origin，避免历史 turn 被重新绑定；
   - required job 的 `task_output(wait=exit)` 观察到终态仍只跳过冗余 wake，
     不改变 durable notification / result 证据。

新增/更新测试：

```text
TestParseJobFinalizationPolicy
TestManagerBackgroundJobTerminalAndDurability
TestManagerJobStoreDurability
TestBrokerJobStartRequiredPolicyCapturesParentIdentity
TestBrokerJobStartDefaultPolicyStaysDetached
TestBrokerJobStartRequiredFailsClosedWithoutResumeIdentity
TestBrokerJobStartRequiredFailsClosedWithoutDurableJobStore
TestBrokerJobStartRejectsUnknownFinalizationPolicy
TestBackgroundJobTerminalDisposition（补齐 interrupted/expired/abandoned）
TestBackgroundJobObligationsJoinSettleAndResume
TestParkBackgroundJobObligationAndSettle
TestParkBackgroundJobObligationsFromResultsHonorsPolicyAndStatus
TestParkBackgroundJobObligationDegradesWithoutDurableJobStore
TestResumeAdmissionMatrix（required job resume / running retry）
TestBatchObligationSource_ProjectsRequiredBackgroundJobs
TestLocalResumeAdmissionWiresRequiredBackgroundJobs
TestNewReActLoopInheritsAgentObligationResolvers
TestChildAgentBuildInheritsObligationResolvers
```

回归结果：

```text
go test -p 1 ./internal/subagentbatch ./internal/background ./internal/toolbroker \
  ./internal/supervision ./internal/agent ./internal/api/runtimeapi -count=1
  => all ok
go test ./cmd/aicli/commands -count=1
  => ok
```

包级并行时 `internal/background` 的两个既有 timing 测试在机器高负载下会偶发
超时；单独运行或 `-p 1` 串行回归均通过。

required job 的宿主级 loop/resolver 覆盖主会话、API/CLI 直连 loop 与
in-process child loop。嵌套子代理的跨重启自主恢复见 §11.10。

#### Required job 真机 ConPTY 验收（2026-11-04）

使用真实 ConPTY、真实 `job_start` 工具调用和真实 PowerShell job 进程完成
required / detached 对照验收。

首次运行 `output/conpty-real-e2e/run-20261104-163559` 暴露的是验收环境问题：
`backend/.tmp/aicli-conpty-real.exe` 为旧构建，`job_start` 实际忽略了
`finalization_policy` 并按 detached 落库。按当前源码重建该二进制后重跑通过；
这不是 runtime 逻辑失败。

最终证据：

```text
required:
  output/conpty-real-e2e/run-20261104-164116/background-job-required/result.json
  output/conpty-real-e2e/run-20261104-164116/verification-summary.json

detached control:
  output/conpty-real-e2e/run-20261104-164224/background-job-detached/result.json
```

required 断言：

- `parent.turn.parked` 携带 `turn_00a97cc6-...` / `ep_e6aad9fa-...`，且
  `final=false`；
- job metadata：`finalization_policy=required`，parent turn/episode 与 parked
  完全相同；
- job 先 terminal，随后 `turn.resumed(phase=started)`，最后
  `parent.turn.finalized`，turn/episode 全程不变；
- wake consumption=`model_turn`，`supervision_resume_claims=1` 且
  `state=submitted`、`model_turn_id` 等于同一 turn；
- 恢复回合最终回复包含 job marker，finalize 后没有新的模型 `session_start`。

detached 对照断言：

- job policy=`detached`，parent 在 job terminal 前 finalized；
- wake consumption=`notification_only`，`resume_claims=0`；
- job 输出保留 marker，finalize 后没有第二个 `session_start` 或模型 turn。

验收结论：

```text
verdict = true
required job  -> park -> terminal -> resume same turn/episode -> finalize
detached job  -> finalize first -> notification_only -> no resume
```

### 11.10 嵌套 child agent 自己 park 后的跨重启恢复（2026-11-04）

嵌套 child 的逻辑 turn 可能把自身 park 在 `job:` / 子批次 / team / 更深 child
obligation 上。此前 child 返回的是普通失败或由外层 batch 直接收敛，重启后没有
worker 再进入同一逻辑 turn。现在把它建模为**非终态任务**，并复用既有
`TurnSuspension` 控制面，不新增表：

```text
child loop 结束后仍有 required obligation
  -> SubagentResult.TurnParked = true (+ turn_id / episode_id)
  -> batch task = parked（保留 partial result capsule，不写 finished_at）
  -> batch = parked（非 terminal；父 turn 的 batch obligation 保持打开）
  -> obligation 全终态后
  -> ResumeParkedBatches CAS 取得 parked -> running
  -> 只重跑 parked task，不重跑已完成 sibling
  -> 同一 child session + turn_id + episode_id
  -> task/batch 收敛到真实终态，再由既有 terminal wake 恢复父 turn
```

落地内容：

1. **身份稳定**
   - durable batch task 的 child session id 由 `batch_id + task_id` 确定；
     `ChildSessionID` 写回 task 行，恢复时优先复用已持久化的 id；
   - child turn/episode 继续使用 `turn_child_<task_id>` /
     `ep_child_<task_id>`，保证重启后命中原 `TurnSuspension`。
2. **非终态状态机**
   - `BatchParked` / `TaskParked` 已加入 batch/task 状态图；
   - `TaskStatus.Terminal()` 与 `BatchStatus.Terminal()` 仍为 false；
   - parked task 不占 live-slot（恢复时重新经过 scheduler 的并发/写者闸门）。
3. **child coordinator 共享**
   - `ChildAgentFactory` 复用父 coordinator 的 durable store / projector；
   - 同一组 `ObligationResolvers` 同时注入 child agent、loop 和 child
     coordinator，嵌套 job/team/child 的 settle 判定与 resume 判定同源。
4. **恢复入口**
   - `SubagentBatchCoordinator.ResumeParkedBatches`：有界扫描 parked batch，
     `TurnObligationsAllTerminalWithResolvers` 判定 child suspension 是否可恢复，
     CAS `parked -> running` 后只执行 parked tasks；扫描会沿 child suspension 的
     batch obligation 递归纳入不同 `parent_session_id` 的嵌套 parked 批次，
     先恢复最深层可恢复叶子，下一次扫描再满足直接父级；
   - `StartParkedBatchResumer`：CLI base session 低频周期扫描；
     API/CLI 子会话 materialize 时做一次有界扫描，避免每个子 actor 常驻轮询；
   - `RecoverStaleBatches` 不再把 parked batch 当 orphan；若崩溃发生在 task
     parked 之后、batch parked 写入之前，运行中的 task 只要有确定性 child
     suspension 就回退为 parked，而不是标记 orphaned；
   - parked batch 超过 deadline 仍按 timed_out 收敛。
5. **事件与观测**
   - 新增 tail-only `subagent.task.parked` / `subagent.batch.parked`；
   - supervision 的 active batch/task 投影、progress 查询、gateway list 与
     obligation state 均把 parked 视为运行中的非终态。

新增/更新测试：

```text
TestDurableChildSessionIdentityIsStable
TestChildLoopReturnsTurnParkedForOutstandingSuspension
TestChildAgentBuildUsesDurableBatchSessionIdentity
TestParkedChildTaskKeepsBatchNonTerminal
TestParkedBatchSurvivesRestartAndResumesOnlyParkedTask
TestParkedBatchStaysParkedWhileRequiredObligationRuns
TestParkedResumeFollowsNestedBatchObligation
TestRecoverStaleRunningBatchWithParkedTaskDemotesToParked
TestRecoverExpiredParkedBatchTimesOutParkedTask
TestValidateBatchTransition / TestValidateTaskTransition（parked 图）
TestTerminalStatesAreAbsorbing（parked 仍非 terminal）
```

边界：child task 的 resume prompt 由 durable partial result capsule（summary /
findings / patches）重建，不重放未被 scheduler 持久化的逐步 tool transcript；
child 需要先从 workspace / artifact / 既有 durable 结果读取现场再继续。这样避免
把完整子对话复制进 batch 账本，同时保持同一 session/turn/episode 和“只恢复未
终态工作”的语义。

### 11.11 Resume claim 证据化崩溃恢复（2026-11-04）

`claimed` 超时不再一律收敛为 `submitted + recovered_unknown`。恢复扫描现在先读
durable parent turn 证据，再决定：

```text
current turn/episode 匹配 claim       -> recovered_submitted
finalized turn/episode 匹配 claim     -> recovered_submitted
suspension 在 claim 之后重新 park     -> recovered_submitted
suspension 仍是 claim 前的原始 park   -> recovered_unsubmitted -> released
parent state / suspension 读不到或歧义 -> recovery_pending，保持 claimed
未接 parent-state resolver             -> recovered_unknown（兼容旧行为）
```

实现内容：

1. **Schema v11**
   - `supervision_resume_claims.recovery_outcome`：
     `recovered_submitted | recovered_unsubmitted | recovery_pending |
     recovered_unknown`；
   - `supervision_resume_claims.recovered_at`；
   - additive migration，旧行读出空 outcome，不改变旧 claim 语义。
2. **证据化恢复**
   - `ResumeAdmissionService.RecoverStaleClaimsDetailed` 逐行读取 stale claim，
     用 `ParentTurnSnapshot` 的 current/finalized identity 和 suspension
     `ParkedAt` 判定是否真正启动过 resume；
   - 只有“原始 suspension 仍在且未重新 park”才释放 claim 重试；
   - 无法证明时保留 `claimed` 并写 `recovery_pending`，不再静默丢失自动恢复，
     也不再盲目重复模型 turn；
   - released claim 重新 claim 时使用新的 lease 时间，避免旧 `claimed_at`
     立刻再次过期。
3. **正常提交身份**
   - `MarkSubmitted` 优先记录 claim 的 `ParentTurnID`，wake ids 只作为无 turn
     identity 的兼容 fallback。
4. **指标**
   - `resume_admission` 新增：
     `recovered_submitted`、`recovered_unsubmitted`、`recovery_pending`、
     `recovered_unknown`；
   - `recovered` 只统计已收敛的恢复结果，不把 pending 混入成功恢复。

新增/更新测试：

```text
TestResumeAdmissionRecoversStaleClaimFromDurableTurnEvidence
TestResumeAdmissionReleasesStaleClaimWhenOriginalSuspensionIsUnchanged
TestResumeAdmissionRecoversStaleClaimFromReparkedSuspension
TestResumeAdmissionKeepsStaleClaimPendingWhenEvidenceIsUnavailable
TestResumeAdmissionFallsBackToLegacyRecoveryWithoutParentStateResolver
TestResumeClaimModelTurnIDPrefersLogicalTurn
TestMetricsSnapshotResumeAdmissionLedger（扩展 recovered_* 分项）
TestAPIWaitFeedbackSweepRecoversStaleClaims（改为 current-turn 证据）
TestLocalWakeFallbackRecoversStaleClaimsWithoutPendingWakes（改为 current-turn 证据）
```

边界：`recovery_pending` 是安全的不确定状态，不是自动重试状态；需要后续证据或
人工/运维释放。它保证“不知道是否启动”时既不重复模型 turn，也不静默删除唯一
恢复记录。

### 11.12 ACP 显式 continuation 契约（2026-11-04）

P1-1 按“显式 continuation”落地，不新增 ACP stop reason：

- `PromptResponse.stopReason` 继续只用 `end_turn` / `cancelled` 等 ACP v1 值；
- 新增可选 `PromptResponse._meta`，键为 `ai-agent-runtime`，载荷：
  ```json
  {
    "turn_id": "turn_...",
    "episode_id": "ep_...",
    "turn_phase": "parked",
    "parked": true,
    "final": false,
    "continuation": "explicit_prompt",
    "resume_turn_id": "turn_...",
    "resume_episode_id": "ep_..."
  }
  ```
- finalized/canceled 返回 `final=true`、`continuation=new_episode`；
- runtime state 不可读时不写 `_meta`，不猜测 turn identity；
- ACP host 明确不接 supervision wake consumer，late result 只保留在
  durable notification/result evidence 中，不产生第二个 final response；
- 下一次显式 `session/prompt` 由 actor 的 durable suspension 反向复用
  同一 turn/episode；客户端不需要理解内部 supervision 账本。

新增测试：

```text
TestACPParkedTurnContinuationIsExplicitPrompt
TestACPFinalizedTurnContinuationIsNewEpisode
TestACPParkedTurnMetaFallsBackToDurableSuspensionFields
TestACPTurnMetaPrefersCurrentTurnOverPreviousFinalization
TestACPPromptResponseOmitsMetaWithoutRuntimeState
```

验证：

```text
go test ./internal/acp -count=1
go test ./cmd/aicli/commands -run '^TestACP(Parked|Finalized|Prompt)' -count=1
```

### 11.13 Approval identity / legacy wake 收口（2026-11-04）

P1-3 决策：approval 是 **attention wake**，不是 settlement。它可以唤醒空闲父
turn，但必须携带 durable `parent_turn_id` / `parent_episode_id`，不能仅凭
actor idle 自动续跑。

实现内容：

1. **ApprovalNotice / wake identity**
   - `ApprovalNotice` 新增 `ParentTurnID` / `ParentEpisodeID`；
   - `ProjectApprovalRequest` 把它们写入 `WakeRequest.TurnID` /
     `ParentTurnID` / `ParentEpisodeID`；
   - CLI/API approval bridge 从 parent runtime snapshot 的
     `WakeIdentity()` 填充；snapshot 不可读或没有身份时留空。
2. **Admission**
   - approval wake：
     ```text
     missing turn/episode       -> NotifyOnly
     parent finalized/canceled  -> NotifyOnly
     parent busy                -> RetryAdmission
     matching suspended identity-> ResumeLegacy（不做 settlement claim）
     identity mismatch          -> RejectStale
     ```
   - progress / wait-feedback 保留既有 bounded legacy idle 行为；
   - 其他无 policy 的非终态 wake 缺少 turn/episode 时默认 `NotifyOnly`，
     identity 与 parent snapshot 不匹配时 `RejectStale`。
3. **旧记录**
   - 旧 approval wake 无 parent identity 时默认通知，不自动投递给 idle actor；
   - 不新增 legacy approval 灰度开关，避免保留无法证明归属的自动续跑。

新增/更新测试：

```text
TestProjectApprovalRequest_RequestsParentWake（断言 parent identity）
TestParentTurnSnapshotWakeIdentity
TestResumeAdmissionMatrix/approval with matching parked identity resumes legacy
TestResumeAdmissionMatrix/approval without parent identity is notification only
TestResumeAdmissionMatrix/approval after finalization is notification only
TestResumeAdmissionMatrix/approval waits while parent is busy
TestResumeAdmissionMatrix/approval episode mismatch rejects stale
TestResumeAdmissionMatrix/unknown legacy non-terminal without identity is notification only
```

### 11.14 Versioned terminal output drain ledger（2026-11-04）

P2-1 把原先分散的 `output_drained_at` / `late_output_count` 升级为共享的
版本化账本：

```text
internal/outputdrain.Ledger
  version           # 1 at terminal boundary; +1 per late decision
  phase             # absorbing | draining | drained | late_discarded
  terminal_epoch
  drained_at
  absorbed_bytes
  drained_bytes
  late_bytes
  late_discarded
  conflict_policy   # discard_late | reject_terminal_write
```

实现内容：

1. **Background job**
   - terminal transition 初始化 `output_drain`，version=1，phase=drained，
     absorbed/drained bytes 取 terminal 前接受字节数；
   - terminal 后 producer 写入走 `AddLate`，version 递增、late bytes 累加，
     phase=late_discarded，并写入 `output_drain` + 旧版 flat keys；
   - terminal job row 不能被 `UpdateJobCAS` 改写，因此新增窄路径
     `UpdateJobOutputDrain`，只更新 `metadata_json`，保证 late 计数跨重启保留；
   - `TaskOutputResult` 同时暴露 `output_drain` 与旧字段。
2. **Batch task capsule**
   - `TaskResult` 增加同一 `output_drain` 结构；
   - `RecordTaskResult` 按 task version 生成 ledger：parked=draining，
     terminal=drained；
   - batch task 的 conflict policy 是 `reject_terminal_write`：terminal
     overwrite 直接返回版本冲突，不允许静默覆盖稳定 capsule。
3. **Supervision metrics**
   - `MetricsSnapshot` 新增 `output_drain.background_jobs` /
     `output_drain.batch_tasks`：records、versions、absorbed/drained/late bytes、
     late_discarded、conflict policy 分布；
   - CLI `/debug supervision metrics` 与 API `/supervision/metrics` 接入
     background manager / batch store；未接线时写 note 降级；
   - `cmd/supervision-metrics` 仍可在无宿主源时读取 supervision store 的其余指标。

新增/更新测试：

```text
TestLedgerLateWriteIsVersionedAndCounted
TestLedgerNormalizeKeepsMonotonicDefaults
TestLateOutputAfterTerminalIsDiscardedAndCounted（扩展 version/phase/bytes）
TestOutputDrainLedgerPersistsAcrossStoreReload
TestRecordTaskResult...（扩展 TaskResult.OutputDrain + metrics）
TestMetricsSnapshotCollectsOutputDrainSources
```

验证：

```text
go test ./internal/outputdrain ./internal/background ./internal/subagentbatch ./internal/supervision -count=1
go test ./internal/api/runtimeapi -count=1
go test ./cmd/aicli/commands -count=1
```

已知环境性失败保持 P2-2 口径：`internal/background` 的 PTY/timing 用例在完整
并行负载下偶发失败，单独串行复跑通过；不是本次 drain 逻辑回归。

### 11.15 Windows/PTY 与高负载 timing 稳定性（2026-11-04）

P2-2 的两个已知失败已收敛，不再以“环境问题”跳过真实回归。

1. **PTY echo fixture**
   - 旧用例固定使用 `more`，并在 PowerShell / Git Bash 混合环境下依赖错误的
     shell 语法；
   - fixture 现在使用 `runtimeexecutor.DefaultUserShell()` 选择命令，与未配置
     `shell:` 时的 background manager 默认解析一致；
   - PowerShell 使用 `[Console]::ReadLine()` 循环（不是 `$input` 管道）；
   - echo 断言改为有界轮询 `writeStdinUntilContains`，失败时输出
     `last_err` 与完整 `seen` 流；不会把缺失 echo 静默 skip。
2. **timeout 与 launch 的竞态**
   - 如果 deadline 在 `cmd.Start` 或 detached launcher 启动期间到期，旧路径会
     落成 `failed(context deadline exceeded)`；
   - 现在两条 launch 路径都区分 `DeadlineExceeded`，收敛为 `timed_out`，
     与进程已启动后的 timeout 语义一致；
   - 新增 `TestJobTimeoutDuringLaunchIsTimedOut` 确定性覆盖该窗口。
3. **回归/CI**
   - CI 基线已使用 `go test -p=1 ./... -count=1 -timeout=600s`；
   - 增加本地 `make test-background`，固定 `-p 1 ./internal/background -count=1`；
   - 已知 PTY / timeout 用例连续复跑 3 次通过，完整
     `go test -p 1 ./internal/background -count=1` 通过。

新增/更新测试：

```text
TestOpenTerminalPTYSurvivesCallerContextCancel
TestManagerPersistsTimeoutBudgetAndStructuredTimeoutOutcome
TestJobTimeoutDuringLaunchIsTimedOut
```

验证：

```text
go test ./internal/background -run 'TestOpenTerminalPTYSurvivesCallerContextCancel|TestManagerPersistsTimeoutBudgetAndStructuredTimeoutOutcome|TestJobTimeoutDuringLaunchIsTimedOut' -count=3
go test -p 1 ./internal/background -count=1
```