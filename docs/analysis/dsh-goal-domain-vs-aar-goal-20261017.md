# DSH goal 域逻辑分析与 AAR goal 目标设计（审查版）

> 创建日期：2026-10-17
> 分析对象：`E:\projects\ai\deepseek-harness\packages\goal`（goal / goal-round-driver / tool-goal / command-goal 四子包）与 `packages/experimental/tool-ralph`
> 对照对象：本项目 `backend/internal/goal`、`cmd/aicli/commands/{goal.go, goal_functions.go, chat_goal_auto_continue.go}`、`internal/api/runtimeapi/session_goal_driver.go`
> 性质：分析 + 逐项审查（含对先前口述结论的修正）+ 目标设计（v1 方向稿，未实施）

---

## 1. DSH goal 域架构事实（源码级）

### 1.1 包拓扑

| 子包 | 职责 | 关键文件 |
|---|---|---|
| `goal/` | 纯域模型 + 事件溯源投影 | `types.ts`（GoalPhase/GoalActivation/GoalSnapshot/GoalProjection）、`fold.ts`（事件回放规则）、`runtime.ts`/`client.ts` |
| `goal-round-driver/` | 同会话自动续跑驱动器 | `index.ts`（queued→claimed→admitted 状态机 + disarm 栅栏）、`prompt.ts`（`<goal_round>` 提示词） |
| `tool-goal/` | 模型侧工具 | `index.ts`（get_goal / create_goal / update_goal）、`authority.ts`（执行时权威校验）、`wrapup.ts`（终局收尾指令） |
| `command-goal/` | 用户侧 `/goal` | `index.ts`（show/create/edit/pause/resume/clear，含按 phase 的命令提示） |

### 1.2 域模型核心机制

1. **CAS 乐观锁**：`GoalRef { id, revision }`；每次持久化变更 revision+1；所有 mutation 携带精确 ref（`goalRef()` 拒绝空 id / 非正整数 revision）。
2. **durable 相位 × 进程局部激活正交**：`GoalPhase = active|paused|blocked|complete`（落库）；`GoalActivation = armed|disarmed`（进程局部、**永不持久化**；恢复/fork 后默认 disarmed，人类明示 resume 才 rearm）。投影 `GoalProjection` 刻意不含 activation。
3. **回合 = 被承认的事件**：`fold.ts` 回放时校验 `source.round === state.roundsStarted + 1`，否则抛错——轮数只能由按序 admitted 的 goal round 消息推进，不可跳变/伪造；轮数是 durable 计数。
4. **轮上限 → durable blocked 终态**：`roundsStarted >= maxGoalRounds` 时驱动器调 `goals.block(..., { code: 'round-limit' })`，失控面收敛为可预期终态。
5. **`GoalBlockReason { code, message }`**：机器可路由 + 人类可读的双字段阻塞说明（`round-limit` / `model-reported` 等 code）。

### 1.3 权威模型（tool-goal/authority.ts，执行时事件溯源鉴权）

- `goalToolExecution`：调用方必须是注册表中的**活 agent** 且 `status==='running'` 且是当前 driver 的 initiator，且存在 open turn（否则 `GOAL_TOOL_DRIVER_REQUIRED`）。
- `hasDirectHumanInput`：open turn 内存在 `user/message` 且 `source.kind==='user'` 的根代理消息。
- `isMatchingGoalRound`：open turn 内存在 `source = {kind:'goal', goalId, revision, round===roundsStarted}` 的消息。
- 两者皆无 → `GOAL_TOOL_AUTHORITY_REQUIRED`。

**action 权威矩阵**（update_goal，5 个 action）：

| action | direct-human | goal-round（自动续跑中） | 附加约束 |
|---|---|---|---|
| `edit` | ✅ | ❌ | objective/max_goal_rounds 仅此 action 合法 |
| `pause` / `resume` | ✅ | ❌ | **model 显式不可 resume paused**（`GOAL_TOOL_RESUME_PAUSED`：the user must resume it） |
| `complete` / `blocked` | ✅ | ✅ | blocked 需 `blocked_reason` 必填 + `roundsStarted ≥ blockedAfterConsecutiveRounds`（默认 3，否则 `GOAL_TOOL_BLOCK_THRESHOLD`） |

**终局 wrapup**：goal-round 权威下 complete/blocked 成功 → `exec.deferContext(renderWrapupContext(...))` 注入 `<goal_complete>`/`<goal_blocked>` 指令：强制向用户写一条收尾消息（结果/验证方式/产物），带 grounding 纪律（"只报告本会话证据建立的结论"），以软收尾取代硬停。

### 1.4 round-driver 调度纪律

- 每 agent 至多一个 in-flight 续跑；`readyToDrive` = fiber ACTIVE + agent idle + 无竞争 prompt（`competingQueued` 栅栏，用户输入即让位）。
- **所有异常路径（checkpoint 失败 / 冲突 / agent 消亡）一律 `disarm`**——宁可保守停止，不带病重试。
- 轮提示 `<goal_round>` 含 Objective JSON 序列化 + `Round: n/max` + "workspace is authoritative" 纪律。

### 1.5 tool-ralph（迭代内核，与 goal 的关系）

前台 Ralph 循环：每轮 fresh structured-output 子级 + 固定 report schema（status/summary/evidence/nextSteps/blocker，逐字段规范化校验 + 16K handoff 上限）+ 跨轮仅传有界 handoff；终态 complete/blocked/budget-limited/round-failed；`requireFreshProvider` 强制 fresh provider（继承父上下文的 provider 拒绝）。**ralph 无进展门控**——只有轮数预算；其描述明确"ordinary long-running same-session work belongs to goal tools"。

---

## 2. 对照审查：AAR 现状 × DSH 机制

### 2.1 审查方法说明

以下每项均按当前代码核实（2026-10-17 HEAD `014fe06d` 后）；先前口头分析中一处结论已**修正**（见 2.3 G1）。

### 2.2 已对齐项（无需动作）

| # | 机制 | AAR 现状 | 判定 |
|---|---|---|---|
| A1 | 模型写入面窄口子 | `update_goal` 仅 `complete\|blocked`（`capability.go:77`），pause/resume/edit 不进模型 schema | **比 DSH 更窄**，权威结果等价，维持 |
| A2 | blocked 连续轮阈值 | `NoProgressStrikeLimit=3`（`iterate_goal.go:39`）与 DSH `blockedAfterConsecutiveRounds=3` 同源 | 对齐 |
| A3 | 驱动器提示词单源 | `internal/goal/continuation.go` 三段提示词 + "任何一侧改动都要先改这里"逐字节约束 | 对齐（我们更强：双宿主共享常量） |
| A4 | CLI 让位 | actor-first 让位冷却（CLI 在场时服务端让位，`session_goal_driver.go:249-261`） | 等价 DSH competingQueued |
| A5 | 进展门控 | iterate_goal G6 stalled 终态（连续 no-progress ×3） | **超越 DSH**（ralph 仅轮数预算） |
| A6 | budget_limited 语义 | 预算耗尽 → budget_limited；提高预算 > 已用才复活（`goal.go:212-219` merge 守卫） | 与 DSH round-limit 终态同构（token 维度） |
| A7 | 恢复/fork 后不自动续跑的服务端形态 | 服务端 kick 仅由 set/resume/budget-raise 触发（`goalDriverKick`），无"启动即扫描 active goal 续跑"路径 | 与 DSH disarmed 语义实质一致（待 G4 复核 CLI 形态） |

### 2.3 差距项（按优先级，含修正）

**G1. goal 乐观锁（修正先前结论）**
先前称"无并发防护"——**不准确**。`GoalMergePolicy.MergeContextValue`（`goal.go:190-227`）已按 actor 裁决：complete 不可逆、model 不可 complete paused/blocked、model 不能换 goal、budget 复活需预算>已用。**真实缺口**：同一 actor 的并发同向写是 last-writer-wins（无版本比对）；三方（CLI 命令 / 模型工具 / 服务端驱动器）高频交错时，TokensUsed 计数与状态可能互相覆盖。DSH 的 revision CAS 把"丢失更新"变成显式冲突错误。
**判定**：值得借鉴；我们已有 `supervision.UpdateExecutionRunCAS`（`expectedVersion` 条件更新）先例可直接复用形态。
**优先级**：中高（正确性，但当前单会话并发窗口小）。

**G2. 轮数上限缺失（与 token 预算正交）**
我们只有 token 预算（budget_limited）与 strike 停止线（blocked by system）；无 **durable 总轮数上限**。DSH `maxGoalRounds` 把"无限续跑"风险变成可预期 blocked 终态（code: round-limit），且 `roundsStarted` 是持久计数（我们 goal 域无轮数字段，续跑轮数只存在于驱动器内存/debug 日志）。
**判定**：借鉴价值高——观测性（轮数可见）+ 失控面收敛双收益；与现有 budget_limited 机制同构，实现成本低。
**优先级**：高。

**G3. 终局 wrapup 缺失**
模型 `update_goal complete` 后 turn 自然结束，无收尾消息引导；用户看不到"结果如何验证/产物在哪/下一步看什么"的结构化交代。DSH 的 wrapup（含 grounding 纪律）成本低收益高；我们 `BudgetWrapUpPrompt` 已是同型机制，缺 complete/blocked 两个终态的对称实现。
**判定**：借鉴价值高（用户体验 + 防幻觉收尾）。
**优先级**：高。

**G4. CLI 的 armed/disarmed 语义缺口（已取证，2026-10-17）**
服务端形态已安全（A7：kick 仅由 set/resume/budget-raise 触发）。**CLI 侧已取证**：`maybeAutoContinueActiveGoal` 在每次用户消息 turn 终态后调用（`chat_send.go:157`），`shouldAutoContinueActiveGoalDecision` 只要求 `goal.Status == active`（`chat_goal_auto_continue.go:440-441`）——即**恢复含 active goal 的会话后，用户发出的任意一条消息（如"看一下当前状态"）都会在 turn 结束后触发自动续跑**。与 DSH"恢复后默认 disarmed、人类明示（/goal resume）才 rearm"存在实质差距：用户意图是查询，系统却启动了自主工作流（幽灵续跑）。
**判定**：需补 CLI 侧 rearm 闸——resume 加载 active goal 时置 disarmed（进程内标记，不落库）；`/goal resume` 或用户带继续意图的消息清除该闸；decision 路径在 disarmed 时返回 `Reason: "goal_disarmed"`（debug 可见）。`/goal status` 可同步显示"已恢复但未 rearm"提示。
**优先级**：中高（从"待复核"上调：已证实真实用户场景可误触发）。

**G5. 执行时权威校验（调用上下文鉴权）**
我们信任工具面可用性（`chatToolAvailable`），不校验调用发生的 turn 内是否有 human 输入或本 goal 的续跑轮。DSH 用 open-turn source 溯源防"非人类轮次的伪权威调用"。我们是单会话模型、actor 远程路径有独立的权限面，风险低于 DSH 的多 agent 图，但 `subagent_*`/后台任务若将来获得 goal 工具面，此缺口即真实。
**判定**：记录为设计约束（"goal 工具仅限根会话工具面"），暂不实现事件溯源校验；若未来工具面下放子代理则必须补。
**优先级**：低（约束 + 备忘）。

**G6. `create_goal` 与 `blocked` 的 durable reason（产品决策项）**
- `create_goal`：DSH 允许模型在 direct-human turn 内自主建 goal。我们模型无建权。**建议不借**：扩大自动续跑触发面，且 `/goal` 用户建权语义已清晰。
- `GoalBlockReason {code,message}` 结构化双字段：我们 `BlockedReason` 是单字符串。**小改进**：系统写入的 blocked 可带 code（`consecutive_errors` / `no_progress` / `round-limit` / `model-reported`），供 UI/analysis 面机器路由。可与 G2 一并做。
**优先级**：低-中（随 G2 顺带）。

### 2.4 不借鉴项（明确决策）

| DSH 机制 | 不借理由 |
|---|---|
| `update_goal` 5-action 宽 schema | pause/resume/edit 属用户主权；模型 schema 越窄越安全（A1 已是更优形态） |
| ralph 256 轮缺省 | 本地成本；我们 16+64 上限 + stalled 门控更贴合单机 |
| 模型自建 goal | 见 G6 |
| activation 独立字段 | 我们以"kick 只由用户命令触发"达到同等 fail-safe，无需双字段 |

---

## 3. 目标设计：AAR goal 域 v2（G1–G3 实施 + G4 复核）

### 3.1 设计目标（按优先级）

1. **G2+G6 轮数上限与结构化 blocked**：`SessionGoal` 增 `MaxRounds int` / `RoundsStarted int` / `BlockedCode string`；驱动器 admit 一轮时 `RoundsStarted++`（随 PutPersistent 落库）；`RoundsStarted >= MaxRounds`（MaxRounds>0 时）→ 系统写 `blocked`（BlockedCode=`round-limit`，BlockedReason=`Goal reached its configured limit of N rounds`）。默认 `MaxRounds=0` 不启用（行为零变化），`goal.maxRounds` runtime.yaml 配置缺省值。
2. **G3 终局 wrapup**：`update_goal` 模型路径 complete/blocked 成功后（goal-round 语境下）注入一条 defer 型收尾指令（新增共享常量 `CompleteWrapUpPrompt` / `BlockedWrapUpPrompt` 于 `internal/goal/continuation.go`，逐字节同文案约束与既有三段一致），含 grounding 纪律；CLI 与服务端驱动器消费同源。
3. **G1 goal CAS**：`SessionGoal` 增 `Revision int64`；`PutPersistent` 变体 `PutPersistentCAS(ctx, storage, sessionID, goal, expectedRevision, actor)`——sessionmeta patch 条件更新（expectedRevision 不匹配返回显式冲突错误）；三方调用点（CLI / 模型工具 / 服务端驱动器）先 Get 后 CAS，冲突时重读重试（有界一次）。旧 `PutPersistent` 保留为 CAS(expected=当前) 包装，兼容不破坏。
4. **G4 rearm 闸**：CLI resume 加载 active goal 时置 disarmed（进程内标记，不落库）；`/goal resume` 或用户带继续意图的消息 rearm；decision 增加 `goal_disarmed` 停止原因（debug 可见）；`/goal status` 显示"已恢复未 rearm"提示。

### 3.2 不变式（设计红线）

- 模型工具面保持 `get_goal` / `update_goal(complete|blocked)` 两工具窄口子，不扩 action。
- 所有新字段 additive；旧会话元数据缺字段按零值语义（MaxRounds=0=不限轮、Revision=0=旧格式，首次 CAS 写入时补 1）。
- 提示词/错误文案新增必须进 `internal/goal` 共享层（双宿主逐字节一致约束延伸）。
- 用户主权命令（pause/resume/clear/budget）不经模型，状态机守卫不放松。

### 3.3 验收口径（DoD）

- G2：驱动器 admit 路径单测（RoundsStarted 递增 + round-limit blocked）；runtime.yaml 配置探针；`/goal status` 显示轮数。
- G3：wrapup 注入单测（complete/blocked 两态、CLI+服务端双路径同文案）；真实终端验证一轮 complete 收尾消息。
- G1：并发 CAS 冲突单测（两写者交错、后写者得显式冲突并重读重试）；旧格式 goal 首写迁移路径测试。
- G4：resume-then-disarm 守卫单测（恢复后首消息不续跑、`/goal resume` 后续跑恢复、`goal_disarmed` 停止原因 debug 可见）。

### 3.4 明确不做（本设计边界）

- 不引入 create_goal、不扩 update_goal action、不实现 open-turn 事件溯源鉴权（记录约束即可）、不改 ralph 式迭代内核（G6 门控已超越）。

---

## 4. 审查结论

- 分析部分的事实基础经源码复核成立；先前口述的两处不精确已修正（G1 的 merge 防护现状、A7 的服务端恢复形态）。
- 借鉴优先级排序（取证后更新）：**G4（rearm 闸，已证实幽灵续跑场景）> G2（轮数上限）> G3（wrapup）> G1（CAS）> G6（blocked code）> G5（约束备忘）**；G4/G2/G3 构成 v2 实施批，G1 随后，其余为决策记录。
- 本文档为设计稿，未动任何代码；实施前需按 §3.4 边界再过一轮方案评审。

## 5. 实施状态（2026-10-17 更新）

| 项 | 状态 | 提交 | 落地形态 |
|---|---|---|---|
| G4 rearm 闸 | ✅ 已实施（2026-10-18 补齐可见性、意图入口与入口收敛） | `31efd9a0` + 2026-10-18 修复 | 三条恢复路径（resume latest / load by id / bootstrap `--session`）统一置进程内 `goalContinuationDisarmed`；decision 保留 `goal_disarmed` 停止原因；rearm 单点 `applyGoalStatusChange`（legacy `handleGoalCommand` 与结构化统一分派共用，2026-10-18 真机根因：structured resume 曾缺 rearm）与显式继续意图消息（`goalContinueIntentPhrases`，查询类不命中）；`/goal status` 提示由 `goalStatusTrailingHints` 单点产出；disarmed 期间状态栏显示 `Goal idle (/goal resume)`；kick 全链路日志（request/refuse/held/dropped/consumed）+ 清道夫（排空落地重估、queued_input 瞬时让位转延迟 kick、actor 未就绪挂就绪 watcher）；入口/恢复 parity 回归测试钉住。无 goal / 非 active 会话零行为变化 |
| G2 轮数上限 | ✅ 已实施 | `9ea2566f` | `SessionGoal` 增 `max_rounds`/`rounds_started`/`blocked_code`（additive，零值=不限）；`internal/goal.AdmitGoalRound` 单点 admit 语义，双宿主驱动器提交续跑轮前记账；达上限置 `blocked`（code=round-limit）；`/goal status` 显示轮数；`/goal resume` 为用户出口 |
| G3 终局 wrapup | ✅ 已实施 | `252bf012` | 共享层新增 `CompleteWrapUpPrompt`/`BlockedWrapUpPrompt`（含 grounding 纪律、禁再调工具）；CLI post-turn reconciler 检测模型终态 → 续跑循环退出前消费一次收尾轮；服务端驱动器同条件直接提交收尾轮；用户命令终态与预算收尾路径不受影响 |
| G1 goal CAS | ⏸ 未实施（后续批） | — | 设计保留（`Revision` + CAS 式 PutPersistent，复用 supervision `UpdateExecutionRunCAS` 形态）；当前 `GoalMergePolicy` 的 actor 裁决已覆盖主要状态机风险 |
| G6 blocked code | ✅ 随 G2 落地 | `9ea2566f` | `BlockedCode` 字段 + `BlockedCodeRoundLimit`（模型申报的 blocked 维持纯文本 reason，与 DSH `model-reported` code 的差异记录为后续可选项） |
| G5 执行时权威校验 | 📌 约束备忘 | — | 维持设计约束："goal 工具仅限根会话工具面"；工具面若下放子代理必须先补 open-turn 溯源校验 |

回归口径：`internal/goal`、`internal/api/runtimeapi` 全量与 `cmd/aicli/commands` 的 goal 族测试全绿（G2/G3 提交前逐项验证）。
