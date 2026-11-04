# Parent Turn 最终化与迟到结果加固：实施执行计划

- 状态：`P0/P1/P2 tracked scope complete`（切面 1-9 已完成；evidence-aware stale claim recovery、ACP explicit continuation metadata、approval identity/attention wake 收口、claim lease heartbeat、self-check claim、versioned output drain ledger、metrics readout、claim retention/host maintenance、finalization sequence gate、FINALIZING phase、persisted/JSONL replayed resume-start event、progress-only wake classification、parked client semantics、CLI metrics/state readout、`job_start(finalization_policy=required)` 公共能力与真机 ConPTY 验收、Windows/PTY 与 timing 稳定性已补）
- 日期：2026-11-03
- 依据：`docs/plan/parent-turn-finalization-and-late-result-hardening-plan-20261103.md`
- 目标：先落地 P0（identity、Resume Admission、finalization gate、后台 terminal 安全默认），并完成支撑 P0 的 exactly-once claim 与回归测试；P1/P2 按本计划后续阶段推进。

## 0. 实施边界与已定决策

### 0.1 本轮范围

本轮实现以下可直接改变行为的闭环：

1. parent turn / episode / settlement identity 的 durable 传播；
2. `required_for_finalization` / `detached_notification_only` / `explicit_followup_only` policy；
3. 统一 `ResumeAdmission`，先覆盖 obligation settlement 与 background job terminal/monitor；
4. parent finalization gate 的最终复核与 durable phase 记录；
5. automatic resume 的 settlement claim（at-most-once）；
6. 旧记录缺失 identity 时的 NotifyOnly 安全默认；
7. 对应单元与包级回归测试。

### 0.2 暂不在本轮改变

以下保留到 P1，但在代码中预留字段与接口，不宣称已完成：

1. ACP 协议层“挂起但未终局”的新响应类型；
2. terminal output drain 的完整版本化吸收/丢弃指标；
3. 所有 progress/approval/self-check 入口的强制 admission（本轮先接入 terminal/settlement/job 路径，非 terminal 的旧入口保持兼容，下一阶段统一）；
4. 独立的跨进程最终化状态表。parent phase 先落在既有 durable `RuntimeState`，通过 actor 串行写与最终复核保证一致。

### 0.3 安全默认

| 情况 | 决策 |
| --- | --- |
| `required` + 匹配的 parked suspension + required obligations 全 terminal | `ResumeSameParkedTurn` |
| `required` + 无匹配 suspension / parent 已 finalized | `NotifyOnly` |
| `detached_notification_only` | `NotifyOnly` |
| `explicit_followup_only` | `NotifyOnly` |
| turn/episode identity 缺失的新 terminal/job wake | `NotifyOnly` |
| episode 不匹配或 settlement 已消费 | `RejectStale` / 去重 |
| parent 状态未知或 obligation resolver 读失败 | `RetryAdmission`，保留 durable wake |

核心原则：

```text
actor idle 不是 resume 的充分条件。
只有 matching parked turn + episode + required obligations terminal + CAS claim
才允许自动恢复同一个逻辑 turn。
```

---

## 1. P0-A：durable identity 与 policy

### 1.1 `subagentbatch.TurnSuspension` 增加 episode identity

文件：

- `backend/internal/subagentbatch/turn_suspension.go`
- `backend/internal/subagentbatch/sqlite_turn_suspension.go`
- `backend/internal/subagentbatch/sqlite_store.go`（migration v4）

改动：

1. `TurnSuspension` 增加：
   ```go
   EpisodeID string
   // ObligationPolicies 先只持久化非 required 的显式策略；
   // 空表示 required（兼容现有 batch/child/team obligation）。
   ObligationPolicies map[string]FinalizationPolicy
   ```
   为避免引入跨包类型，policy 在 `subagentbatch` 中定义为字符串常量：
   ```go
   type FinalizationPolicy string
   const (
       FinalizationPolicyRequired FinalizationPolicy = "required"
       FinalizationPolicyDetached FinalizationPolicy = "detached"
       FinalizationPolicyExplicitFollowup FinalizationPolicy = "explicit_followup"
   )
   ```
2. SQLite `turn_suspensions` 增加：
   ```sql
   episode_id TEXT NOT NULL DEFAULT ''
   obligation_policies_json TEXT NOT NULL DEFAULT '{}'
   ```
3. `ParkTurnSuspension` / `GetTurnSuspension` / `ListTurnSuspensions` / `ListAllTurnSuspensions` 全量读写新列。
4. `normalize` 清理 policy map，空 map 序列化为 `{}`。
5. 旧行 `episode_id=''` 保持可读；读取层不猜测 episode。

### 1.2 `supervision.WakePending` / `WakeRequest` 增加 identity 与 policy

文件：

- `backend/internal/supervision/types.go`
- `backend/internal/supervision/wake_scheduler.go`
- `backend/internal/supervision/sqlite_store.go`（migration v9）

新增字段（两边同名）：

```go
ParentEpisodeID    string
FinalizationPolicy string
SettlementID       string
SettlementCohortID string
```

SQLite `supervision_wake_pending` 增加四列，全部 `TEXT NOT NULL DEFAULT ''`。

`ScheduleWake`：

- 从 `WakeRequest` 原样写入 `WakePending`；
- 空 `ParentEpisodeID` 时调用新增的 `EpisodeHint`（见 1.5）；
- `NotifyKey` 计算保持不变，避免破坏已写入的 delivery ledger；新增 claim 提供 model-turn 去重。

### 1.3 `BackgroundJobTerminalInput` / `BackgroundJobMonitorInput` 增加 parent identity

文件：

- `backend/internal/supervision/background_job.go`
- `backend/internal/supervision/background_job_monitor.go`
- `backend/internal/supervision/background_job_test.go`
- `backend/internal/supervision/background_job_monitor_test.go`

新增：

```go
ParentTurnID      string
ParentEpisodeID   string
SettlementID      string
SettlementCohortID string
FinalizationPolicy string
```

规则：

1. 显式 policy 优先；
2. 有 parent turn/episode 但 policy 为空：默认 `detached_notification_only`；
3. 缺 parent turn/episode：强制 `detached_notification_only`，并在 input 上记录 `IdentityMissing=true`（仅用于审计/日志，不需要持久化新列，可通过 reason 前缀或 wake reason 派生）；
4. terminal projection 始终写 durable notification；
5. `Observed=true` 继续跳过 wake，但仍保留 notification；
6. wake request 带上上述 identity 与 policy。

### 1.4 后台 job 创建时捕获 parent identity

文件：

- 新增 `backend/internal/background/origin.go`
- `backend/internal/background/types.go`
- `backend/internal/background/manager.go`（`SubmitShell` / `OpenTerminal`）
- `backend/internal/toolbroker/broker.go`（`ToolJobStart`）

设计：

```go
type JobOrigin struct {
    RootScopeID       string
    ParentSessionID   string
    ParentTurnID      string
    ParentEpisodeID   string
    SettlementID      string
    SettlementCohortID string
    FinalizationPolicy FinalizationPolicy
}

func WithJobOrigin(ctx context.Context, origin JobOrigin) context.Context
func JobOriginFromContext(ctx context.Context) JobOrigin
```

- broker 在 `job_start` 分支从 `toolctx` 读取 session/turn/episode，并写入 context；
- `SubmitShell` / `OpenTerminal` 读取 origin，写入 `Job.Metadata` 保留键：
  ```text
  parent_turn_id
  parent_episode_id
  settlement_id
  settlement_cohort_id
  finalization_policy
  root_scope_id
  ```
- 直接调用 manager（测试、API 外部提交）无 origin：不报错，保持 legacy job，并视为 detached/missing identity；
- `requestFromJob` 不复用 origin 到新 job，避免 rerun 继承旧 turn。

### 1.5 episode context 传播

文件：

- `backend/internal/agent/turn_context.go`
- `backend/internal/toolctx/context.go`
- `backend/internal/agent/loop.go`（约 5897）
- `backend/internal/chat/actor.go`（runCtx 约 3103）
- `backend/internal/chat/actor_resume_episode.go`
- `backend/internal/chat/runtime_state.go`

改动：

1. `agent.WithEpisodeID` / `agent.EpisodeIDFromContext`；
2. `toolctx.WithEpisodeID` / `toolctx.EpisodeID`；
3. `ReActLoop` 保存 `episodeID`，在工具调用 ctx 继续传播；
4. `SessionActor` 在 run 开始时确定 episode：
   - 如果 `nextTurnID` 复用了挂起 turn，且 durable suspension 有 `EpisodeID`，复用该 episode；
   - 否则生成 `ep_<uuid>`；
   - `RuntimeState` 增加 `CurrentEpisodeID`、`SuspendedEpisodeID`、`FinalizedTurnID`、`FinalizedEpisodeID`、`TurnPhase`；
   - 设置 `CurrentTurnID` 的同一 `updateState` 中设置 `CurrentEpisodeID` 与 `TurnPhase=running`；
5. `parkBackgroundTurn` / `parkTeamObligation` / `parkLocalAgentChildObligation` 写入 `EpisodeID: agent.EpisodeIDFromContext(ctx)`；
6. `joinParkedTurnRecord` 保留已有 episode；新 epoch 使用当前 episode；
7. 旧 suspension 无 episode 时：
   - 读取路径保留空串；
   - 第一次真正 resume 前由 actor 生成新 episode 并写回 suspension（显式标记为 migrated），但该 suspension 不再匹配旧的无 episode wake。

### 1.6 WakeScheduler 的 episode hint

文件：

- `backend/internal/supervision/wake_scheduler.go`

新增配置：

```go
type WakeSchedulerConfig struct {
    // ...
    EpisodeHint func(ctx context.Context, parentSessionID, turnID string) string
}
```

`ScheduleWake` 在 `ParentEpisodeID == "" && TurnID != ""` 时调用；CLI/API 都将其接到 “读取该 turn 的 suspension episode” 的同一 helper。读取失败返回空，后续 admission 走 `RetryAdmission`/`NotifyOnly`。

### 1.7 lifecycle projection 携带 episode

文件：

- `backend/internal/supervision/projection.go`
- 各 `LifecycleProjection` 构造点

`LifecycleProjection` 增加 `EpisodeID`。构造点无法可靠取得时留空，由 `EpisodeHint` 回查 suspension；仍为空则 admission 走 NotifyOnly/Retry。

---

## 2. P0-B：统一 Resume Admission

### 2.1 新文件与核心类型

新增：

- `backend/internal/supervision/resume_admission.go`
- `backend/internal/supervision/resume_admission_test.go`
- `backend/internal/supervision/resume_claim.go`
- `backend/internal/supervision/sqlite_resume_claim.go`

核心类型：

```go
type ResumeAdmissionDecision string

const (
    ResumeSameParkedTurn ResumeAdmissionDecision = "resume_same_parked_turn"
    NotifyOnly           ResumeAdmissionDecision = "notify_only"
    RejectStale          ResumeAdmissionDecision = "reject_stale"
    RetryAdmission       ResumeAdmissionDecision = "retry_admission"
)

type ResumeAdmissionInput struct {
    RootScopeID        string
    ParentSessionID    string
    ParentTurnID       string
    ParentEpisodeID    string
    ObligationID       string
    SettlementID       string
    SettlementCohortID string
    FinalizationPolicy string
    WakeID             string
    EventKind          string
    WakeReason         string
}

type ParentTurnState string

const (
    ParentTurnRunning   ParentTurnState = "running"
    ParentTurnParked    ParentTurnState = "parked"
    ParentTurnFinalized ParentTurnState = "finalized"
    ParentTurnCanceled  ParentTurnState = "canceled"
    ParentTurnUnknown   ParentTurnState = "unknown"
)

type ParentTurnSnapshot struct {
    SessionID          string
    Status             string
    CurrentTurnID      string
    CurrentEpisodeID   string
    SuspendedTurnID    string
    SuspendedEpisodeID string
    FinalizedTurnID    string
    FinalizedEpisodeID string
    Phase              ParentTurnState
}

type ParentTurnStateResolver interface {
    ResolveParentTurn(ctx context.Context, sessionID string) (*ParentTurnSnapshot, error)
}
```

`ResumeAdmissionService`：

```go
type ResumeAdmissionService struct {
    Suspensions subagentbatch.BatchStore
    ParentStates ParentTurnStateResolver
    AgentSessions subagentbatch.AgentSessionObligationResolver
    Teams subagentbatch.TeamObligationResolver
    Claims ResumeClaimStore
    Now func() time.Time
}
```

提供：

```go
func (s *ResumeAdmissionService) Evaluate(ctx, in) (ResumeAdmissionResult, error)
func (s *ResumeAdmissionService) Claim(ctx, eval) (ResumeClaim, bool, error)
func (s *ResumeAdmissionService) MarkSubmitted(ctx, claim) error
func (s *ResumeAdmissionService) Release(ctx, claim) error
```

`ResumeAdmissionResult` 包含 decision、reason、normalized turn/episode、suspension、claim key。

### 2.2 判定顺序

1. trim 所有 identity；
2. policy 规范化：空 => 根据 source 回退（见 0.3）；
3. `detached` / `explicit_followup`：直接 `NotifyOnly`；
4. `required` 但 turn 为空：`NotifyOnly`（缺 identity）；
5. parent snapshot 读取失败：`RetryAdmission`；
6. parent `finalized` / `canceled`：`NotifyOnly`；
7. parent `running`（有 live run）：`RetryAdmission`，记录 pending work，不并发起 turn；
8. 读取 suspension：
   - 读失败 => `RetryAdmission`；
   - 不存在 => `NotifyOnly`（迟到/已最终化）；
   - episode 为空：旧记录，`NotifyOnly`；
   - episode 不匹配 => `RejectStale`；
9. 校验 required obligations：
   - 使用 `TurnObligationsAllTerminalWithTeams` 的严格版；
   - resolver 缺失/读失败/pending => `RetryAdmission`；
10. 读取/创建 settlement claim：
    - claim 已 submitted => `NotifyOnly`（duplicate suppressed）；
    - claim 正被其他进程持有且未超租约 => `RetryAdmission`；
    - 可获取 => `ResumeSameParkedTurn`；
11. parent snapshot 必须是 `parked` 且 `SuspendedTurnID == TurnID`，否则 `RetryAdmission`/`NotifyOnly`。

### 2.3 settlement claim 的 durable 表

`supervision` SQLite migration v10：

```sql
CREATE TABLE IF NOT EXISTS supervision_resume_claims (
    claim_key TEXT PRIMARY KEY,
    root_scope_id TEXT NOT NULL DEFAULT '',
    parent_session_id TEXT NOT NULL DEFAULT '',
    parent_turn_id TEXT NOT NULL DEFAULT '',
    parent_episode_id TEXT NOT NULL DEFAULT '',
    settlement_id TEXT NOT NULL DEFAULT '',
    settlement_cohort_id TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL DEFAULT 'claimed',
    claimed_at TEXT NOT NULL,
    submitted_at TEXT,
    model_turn_id TEXT NOT NULL DEFAULT '',
    delivery_attempt INTEGER NOT NULL DEFAULT 0,
    version INTEGER NOT NULL DEFAULT 1
);
CREATE INDEX IF NOT EXISTS idx_supervision_resume_claims_settlement
    ON supervision_resume_claims(parent_session_id, parent_turn_id, parent_episode_id, settlement_cohort_id);
```

claim key：

```text
claim_key = hash(parent_session_id, parent_turn_id, parent_episode_id, settlement_cohort_id)
```

`Store` 增加：

```go
ClaimResume(ctx, claim ResumeClaim) (ResumeClaim, bool, error)
GetResumeClaim(ctx, claimKey string) (*ResumeClaim, error)
MarkResumeSubmitted(ctx, claimKey, modelTurnID string, at time.Time, expectedVersion int64) (bool, error)
ReleaseResumeClaim(ctx, claimKey string, expectedVersion int64) (bool, error)
```

`ClaimResume` 使用 `INSERT ... ON CONFLICT DO NOTHING` + 读回；返回到手方 `claimed=true`。`MarkResumeSubmitted` / `Release` 用 `version` CAS。

### 2.4 WakeScheduler 接入 admission

为避免“先花预算再被 admission 拒绝”，新增：

```go
func (s *WakeScheduler) DrainRunnableWithAdmission(
    ctx context.Context,
    parentSessionID, parentTeamID, rootScopeID string,
    runnable ParentRunnable,
    admission func(context.Context, WakePending) (ResumeAdmissionDecision, error),
) ([]WakePending, *Digest, error)
```

行为：

- 先做 runnable 检查；
- 对每条 pending wake 先评估 admission：
  - `NotifyOnly` / `RejectStale`：不 claim，不花预算；写 `WakeDelivered` 消费记录（`Consumption=notification_only|stale`），然后 `ResolveWakePending`；
  - `RetryAdmission`：保留未 claim 行；
  - `ResumeSameParkedTurn`：进入原有 budget + claim + digest 流程；
- 原有 `DrainRunnable` 保留为 `admission=nil` 的兼容包装，旧测试不大改；
- `MaybeWakeParent` 在有 admission 时调用新方法。

### 2.5 WakeDelivered 增加消费类型

文件：

- `backend/internal/supervision/types.go`
- `backend/internal/supervision/sqlite_store.go`（migration v9 一并加列）

`WakeDelivered` 增加：

```go
Consumption string // model_turn | notification_only | stale
Reason      string
```

SQLite 增列。旧行默认 `model_turn`，保持语义。

`NotifyOnly` 也写 `WakeDelivered`，以抑制同一 terminal epoch 的重复 wake；durable notification 不删除、不标 seen。

### 2.6 `WakeConsumer` 接入

文件：

- `backend/internal/supervision/wake_consumer.go`

新增：

```go
Admission *ResumeAdmissionService
```

`MaybeWakeParent`：

1. 如果 `Admission != nil`，用 `DrainRunnableWithAdmission`；
2. admission 已在 drain 内处理 NotifyOnly/Stale，不会进入 digest；
3. 对 `ResumeSameParkedTurn`：
   - 在 `buildResume` 前获取 claim；
   - claim 失败则 release wake、返回 nil；
   - `DeliverResume` 成功后 `MarkSubmitted`（model turn id 暂用 wakeID/actor 返回值；若 host 不返回 turn id，写空并记录 wakeID）；
   - `DeliverResume` 失败：`ReleaseResumeClaim` + release wake；
4. `Announce` 只对 admitted/delivered wakes；
5. `RecordWakeDelivery` 记录 `Consumption=model_turn`。

### 2.7 宿主装配

新增共享构造函数（supervision 内）：

```go
func NewResumeAdmissionService(cfg ResumeAdmissionConfig) *ResumeAdmissionService
```

CLI：

- `chat_actor_host.go` 构造 `supervisionWake` 时注入；
- `ResolveParentTurn` adapter 读取 `RuntimeStore.LoadState`；
- `Suspensions` 使用 `h.SubagentBatches`；
- agent/team resolver 使用现有 host 方法。

API：

- `supervision_batch_projector.go` 构造 consumer 时注入同一 service；
- adapter 读取 API runtime store / actor StateSummary。

要求：两个宿主只允许存在这一份 decision 逻辑；host 差异只允许出现在 snapshot 读取与投递实现。

---

## 3. P0-C：Parent finalization gate

### 3.1 RuntimeState 增加 durable phase

文件：

- `backend/internal/chat/runtime_state.go`
- `backend/internal/chat/actor.go`
- `backend/internal/chat/actor_resume_episode.go`

新增字段：

```go
TurnPhase            string // running|parked|finalizing|finalized|canceled
CurrentEpisodeID     string
SuspendedEpisodeID   string
FinalizedTurnID      string
FinalizedEpisodeID   string
```

`RuntimeStateSummary` 增加对应只读字段，`AcceptResume` 语义不变（仍只表示 actor 可接受同一会话的 resume episode，不承担 admission）。

### 3.2 run 收尾的最终化复核

文件：

- 新增 `backend/internal/chat/actor_finalization.go`
- `backend/internal/chat/actor.go`（收尾块 3397-3436）

实现顺序：

```text
loop 返回
  -> stop stall watchdog
  -> persist session / reconcile tool receipts
  -> snapshot turnID + episodeID
  -> re-read TurnSuspension(turnID)
  -> 若 record 存在：
       - 确保 state.SuspendedTurnID/SuspendedEpisodeID 写入
       - state.TurnPhase = parked
       - 不写 Finalized*
       - emit parent.turn.parked（一次 per turn+episode+parked_at）
  -> 若 record 不存在：
       - 二次读（短有界、仅 1 次重试，避免与 settle/clear 竞态）
       - 仍不存在：
           state.TurnPhase = finalized
           state.FinalizedTurnID/EpisodeID = 当前值
           emit parent.turn.finalized
  -> 再执行原 updateState（保持 CurrentTurnID 清理与状态收敛）
```

要求：

- record 存在时即使 obligation 已 terminal 也不得在 actor 内抢先清账本；结算路径负责清；
- `parent.turn.finalized` 只在最终化复核成功后发出；
- `MarkAssistantFinalResponseRendered` 仍由 host 在返回 ACP 前调用，但不再承担 gate 责任；
- ACP handler 继续返回 `EndTurn` 表示 request finished；本轮不新增第二个 ACP final response；
- 若 future host 将 ACP 接到 wake consumer，admission 会因 `FinalizationPolicy=required` + 无 suspension 而 NotifyOnly。

### 3.3 agent loop 的最终化信号

文件：

- `backend/internal/agent/agent.go`（`Result`）
- `backend/internal/agent/loop.go`（`settleParkedTurns` 与 run 尾部）

新增：

```go
type Result struct {
    // ...
    EpisodeID string
    TurnParked bool
}
```

在 `settleParkedTurns` 之后：

- 检查当前 turn 是否仍有 suspension；
- 设置 `Result.TurnParked`；
- emit `agent.turn.parked` 或 `agent.turn.finalized`（同一 turn+episode 去重由事件消费侧或 loop 的幂等标记保证）；
- 不改变最终 assistant 文本生成；本 gate 只决定“是否允许把逻辑 turn 视为 finalized”。

### 3.4 detached obligation 不阻塞 finalization

- 现有 `TurnSuspension` 只包含 batch/child/team required obligations；
- background job 不写入 suspension；
- `FinalizationPolicy=detached` 的 job 不进入 finalization gate；
- 本轮不把 `background_task` 默认加入 required；要成为 required 必须显式构造 policy，后续 API 再开放。

---

## 4. P0-D：后台 job terminal 路径收敛

### 4.1 CLI / API adapter

文件：

- `backend/cmd/aicli/commands/chat_actor_background_supervision.go`
- `backend/internal/api/runtimeapi/background_supervision.go`

改动：

1. `projectLocalBackgroundJobTerminal` / API `projectBackgroundJobTerminal` 从 job 读取 `JobOrigin`；
2. 填充 `BackgroundJobTerminalInput` 的 turn/episode/policy/settlement；
3. `project...Monitor` 也填充 identity；monitor 默认 `detached`；
4. `Observed=true` 仍跳过 wake；本次不改变 job-level 观察优化；
5. terminal 后不再因为 actor idle 就无条件 `wakeSupervisedParent`；改为：
   - `Observed` => 不排/不投；
   - 非 Observed => 排 wake，随后由 admission 决定 resume 或 NotifyOnly；
6. 旧 job 缺 origin：policy 回退 detached，admission NotifyOnly；
7. 保留 durable notification 和 result/Job store 证据。

### 4.2 `task_output(wait=exit)` 超时后的迟到终态

- waiter 注销后 terminal 投影仍走同一 adapter；
- 没有 active waiter => `Observed=false` => durable wake；
- parent finalized / no suspension => admission NotifyOnly；
- 不因为 waiter 不存在而直接调用 `SubmitPromptAsync`；
- 不重新打开 obligation。

### 4.3 API/CLI 一致性测试

对同一组 admission matrix 用共享 service 单测，再分别验证两个 adapter 的传参和投递次数。

### 4.4 Required background job obligation

`job_start` 公开 `finalization_policy=detached|required|explicit_followup`，默认
`detached`：

1. broker 从 `toolctx` 捕获 turn/episode，并把 policy 与 parent identity 写入
   `JobOrigin`、`Job.Metadata` 和工具结果；
2. `required` 在启动前 fail closed：缺少 turn/episode、或 job store 不 durable
   时拒绝创建 job，避免产生无法恢复的挂起状态；
3. loop 用 `job:<job_id>` obligation 写入既有 `TurnSuspension`；job 已 terminal
   或 policy 不是 required 时不 park；
4. `background.Manager` 提供 `BackgroundJobTerminal` / `JobStoreDurable`，
   CLI/API 把同一实例接入 loop、admission、settled wake、wait feedback、
   wait ledger、resume prompt、recovery；
5. resolver set 保存在 Agent 上，`NewReActLoop` 自动补齐未显式配置的字段，
   `ChildAgentFactory` 把同一组 resolver 传给 in-process child agent；
6. obligation resolver 变为统一 `subagentbatch.ObligationResolvers`，strict
   admission 对 running/missing job 返回 `RetryAdmission`，terminal 后才允许
   同 turn/episode resume；
7. 旧 job 无 origin 仍按 detached notification-only 处理；`requeue` 不继承旧
   origin；required 不能收养已打开的 interactive job。

新增回归见主方案 §11.9。

### 4.5 Nested child self-park / cross-restart resume

1. `SubagentResult` 暴露 `TurnParked` / `TurnID` / `EpisodeID`；child loop
   收尾时仍存在 required suspension 即返回 `TurnParked=true`。
2. `BatchParked` / `TaskParked` 进入 subagentbatch 状态图，保持非 terminal；
   task 保留 partial result capsule 但 `finished_at` 为空。
3. child session 身份在 durable batch 内稳定：优先复用 task 行
   `ChildSessionID`，首次运行按 `batch_id + task_id` 生成；turn/episode 继续
   使用 `turn_child_<task_id>` / `ep_child_<task_id>`。
4. `ChildAgentFactory` 复用父 coordinator 的 durable store/projector，并继承
   child/team/job resolver；child coordinator 用同一 store 写嵌套 suspension。
5. `SubagentBatchCoordinator.ResumeParkedBatches` 只重跑 obligations 已终态的
   parked tasks，CAS 抢占 `parked -> running`；已有 sibling 保持原结果。扫描会
   沿 child suspension 中的 batch obligation 纳入嵌套 parked 批次，逐层恢复。
6. `RecoverStaleBatches` 将 parked 视为可恢复非终态，并在“task parked 已写、
   batch parked 未写”的崩溃窗口把 running batch 回退为 parked。
7. CLI base session 周期扫描；CLI/API 子会话 materialize 时做一次有界扫描。

回归：主方案 §11.10。

---

## 5. P0-E：迁移、灰度与回滚

### 5.1 SQLite migrations

- `subagentbatch` v4：`episode_id`、`obligation_policies_json`
- `supervision` v9：wake identity/policy 列、`supervision_wake_delivered` 的 consumption/reason
- `supervision` v10：`supervision_resume_claims`

全部 additive + 零值默认；旧库启动自动迁移。

### 5.2 灰度开关

第一轮通过构造参数控制，不新增全局 YAML 项：

```go
type ResumeAdmissionConfig struct {
    Enabled bool
    EnforceTerminalOnly bool // 本轮 true
    LegacyMissingIdentityPolicy string // notify_only
}
```

默认：

- 新 CLI/API 宿主启用 `Enabled=true`；
- `EnforceTerminalOnly=true`，只对 terminal / obligation_settled / background job / 有 policy 的 wake 强制 admission；
- 非 terminal 的 progress/admin 旧入口保持 legacy，下一阶段切换。

### 5.3 回滚

- 关闭 admission：新 job 仍写 identity/policy，terminal 仍写 notification，但不强制 NotifyOnly；
- 不删除 settlement claim、wake identity、suspension episode；
- 回滚后不能把历史已 finalized 的 settlement 重新当新 turn 投递；
- 若 claim 表损坏，admission 对 required 返回 `RetryAdmission`，不自动 resume。

---

## 6. 测试计划

### 6.1 supervision

- `TestResumeAdmissionRequiredMatchingParkedEpisode`
- `TestResumeAdmissionFinalizedParentNotifyOnly`
- `TestResumeAdmissionDetachedNotificationOnly`
- `TestResumeAdmissionStaleEpisodeRejected`
- `TestResumeAdmissionMissingIdentityNotifyOnly`
- `TestResumeAdmissionPendingObligationRetries`
- `TestResumeAdmissionConcurrentClaimCAS`
- `TestDrainRunnableWithAdmissionDoesNotStartTurnForNotifyOnly`
- `TestWakeDeliveredConsumptionKinds`
- `TestBackgroundJobTerminalProjectionCarriesParentIdentity`
- `TestBackgroundJobTerminalMissingIdentityIsNotifyOnly`
- `TestBackgroundJobTerminalReplayDoesNotScheduleSecondWake`

### 6.2 subagentbatch

- `TestTurnSuspensionEpisodeRoundTrip`
- `TestTurnSuspensionLegacyMissingEpisodeStaysReadable`
- `TestTurnSuspensionPolicyRoundTrip`
- `TestParkTurnSuspensionRejectsMissingTurnID`（已有，保持）

### 6.3 agent

- `TestLoopParkRecordsEpisodeID`
- `TestLoopFinalizationReportsParkedWhenSuspensionExists`
- `TestLoopFinalizationReportsFinalizedWhenSuspensionCleared`
- `TestSettlementClearsSuspensionBeforeFinalizedEvent`

### 6.4 chat/actor

- `TestActorReusesEpisodeAcrossAutomaticResume`
- `TestActorGeneratesNewEpisodeAfterFinalization`
- `TestActorFinalizationParksPendingRequiredObligation`
- `TestActorFinalizationAllowsDetachedJob`
- `TestActorFinalizationClearsSuspensionCacheAfterSettlement`
- `TestActorDoesNotReuseLegacySuspensionWithoutEpisode`

### 6.5 CLI / API

- `TestBackgroundJobAfterFinalizedParentIsNotificationOnly`
- `TestTaskOutputObservedTerminalSkipsWakeKeepsNotification`
- `TestTaskOutputTimeoutThenLateTerminalDoesNotResumeFinalizedParent`
- `TestBackgroundTaskMissingParentIdentityDoesNotResume`
- `TestCLIAndAPIAdmissionMatrixMatch`
- `TestBackgroundMonitorDoesNotResumeFinalizedParent`

### 6.6 ACP

- `TestACPDoesNotEmitSecondFinalResponseAfterLateChildCompletion`
- `TestACPEndTurnStillMeansRequestFinished`
- 复用 `recordingACPEmitter`，断言 emitter 只收到一次最终 assistant/final response；late terminal 不触发任何 send。

### 6.7 必要回归

- 原 `wake_consumer_test.go` / `settled_turn_wake_test.go` / `background_supervision_test.go` 全量通过；
- 现有 `/supervision wake` 显式入口不因 admission 被误判；
- no-obligation 普通 prompt 行为不变。

---

## 7. 验证命令

按改动包分批运行：

```powershell
go test ./internal/subagentbatch/...
go test ./internal/supervision/...
go test ./internal/background/...
go test ./internal/agent/...
go test ./internal/chat/...
go test ./internal/api/runtimeapi/...
go test ./cmd/aicli/commands/...
```

最终：

```powershell
go test ./...
go vet ./internal/supervision/... ./internal/subagentbatch/... ./internal/background/... ./internal/chat/... ./internal/agent/...
git diff --check
```

（若仓库既有测试存在与本改动无关的失败，需在实施记录中单列，不静默声称全绿。）

---

## 8. 实施顺序与提交切面

1. **切面 1**：subagentbatch episode schema + `TurnSuspension.EpisodeID` 读写 + 测试。
2. **切面 2**：agent/toolctx episode 传播 + loop park 写入 + actor run 状态 + 测试。
3. **切面 3**：background `JobOrigin` + broker 捕获 + manager 持久化 + 测试。
4. **切面 4**：supervision wake schema + projection identity + 测试。
5. **切面 5**：`ResumeAdmissionService` 纯逻辑 + matrix 测试。
6. **切面 6**：resume claim store + CAS 测试。
7. **切面 7**：`DrainRunnableWithAdmission` + `WakeConsumer` 接入 + CLI/API 装配。
8. **切面 8**：actor/loop finalization phase + gate 事件 + 测试。
9. **切面 9**：background terminal/monitor adapter 传参 + NotifyOnly 行为测试。
10. **切面 10**：全量测试、文档实施记录、残留风险清单。

每个切面保持可编译；切面 5 之前不改变实际投递行为，切面 7 开始启用 admission。

---

## 9. 完成标准（本轮）

1. 新的 background job terminal 在 parent finalized / detached / missing identity 时只产生 durable notification，不启动模型 turn。
2. `task_output(wait=exit)` 观察到 terminal 时仍保留 notification，只跳过冗余 wake。
3. required obligation 未终态时，父 turn 被标记 parked，不进入 finalized。
4. required obligation 全终态后，仅 matching turn+episode 可自动 resume，且同一 cohort 最多一次。
5. stale episode、旧无 identity job、duplicate settlement 均不产生新模型 turn。
6. ACP 不出现第二个 final response。
7. 原 no-obligation prompt 与显式 `/supervision wake` 行为保持不变。
8. `go test` 目标包通过，并在主方案文档 §11 追加实施记录。

---

## 10. 已知残留风险 / 后续阶段

1. approval 已强制携带 parent identity 并走 attention-wake admission；progress / wait-feedback 保留显式 bounded legacy idle 行为；其他无 identity 的非终态 wake 默认 NotifyOnly。self-check/session-end 仍复用既有 admission 入口。
2. ~~resume claim 崩溃恢复窗口~~ 已收敛：主方案 §11.11 增加 evidence-aware stale recovery；current/finalized/re-park 证据收敛为 `recovered_submitted`，原始 suspension 未变化则 `recovered_unsubmitted` 安全重试，无法证明时保留 `recovery_pending`。
3. ~~terminal output drain 的版本化吸收/丢弃指标~~ 已收敛：共享 `outputdrain.Ledger`，background job 与 batch task capsule 统一 version/phase/absorbed/drained/late/conflict policy；terminal job metadata-only 持久化，supervision metrics 已接入 background/batch 源。
4. ~~ACP 非终局响应协议未改动~~ 已决策并落地：`EndTurn` 保持 request-scoped；`PromptResponse._meta.ai-agent-runtime` 暴露 parked/final/turn/episode，下一次显式 prompt 作为 continuation；ACP 仍不接自动 wake，不产生第二 final response。见主方案 §9.1 / §11.12。
5. ~~旧 wake 行若既无 turn 也无 policy，且不属于 terminal/job~~ 已收敛：approval/未知非终态缺 identity 默认 NotifyOnly；progress / wait-feedback 是显式 bounded legacy 例外。