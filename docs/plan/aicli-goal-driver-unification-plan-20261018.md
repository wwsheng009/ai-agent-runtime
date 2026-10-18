# AAR Goal 驱动器统一方案（round-driver v1）

- 状态：草稿（v0.2 自审修订，待评审）
- 日期：2026-10-18
- 修订记录：
  - v0.1（2026-10-18）：初稿。
  - v0.2（2026-10-18）：按最佳实践自审修订——修正 D1/D2 的幽灵续跑风险（"任意消息解除"否决、改为两态+reason）、补 Non-Goals 与备选方案、补状态转换与终止性、明确 Phase 1 执行上下文与双通道去重、统一决策编号、补审计数据最小化与回滚开关。
- 关联文档：
  - `docs/analysis/dsh-goal-domain-vs-aar-goal-20261017.md`（G1–G6 差距与 G4 rearm 闸）
  - `docs/plan/aicli-goal-continuation-codex-alignment-plan-20261014.md`（continue_if_idle 对齐）
- 事故取证样本：`session_20261018191622_xGGs4QZJ`（工作目录 `E:\projects\ai\deskctl`，goal `goal_7b01a1ab-69fc-4de7-b352-280682369d94`）

---

## 0. 摘要

2026-10-18 事故（本方案取证样本）暴露的不是某一个判断 bug，而是一个结构问题：

> **goal 的"持续驱动责任"没有归属者。** 续跑决策以"边沿触发"的形式散落在若干调用点（以前台用户交互路径为主），没有任何组件对"goal active + armed + 会话 idle ⇒ 必须有驱动者"这条不变式负责。一旦时序组合落在覆盖盲区（恢复态 disarm → 用户 rearm → 该前台轮被 Esc 中断 → supervision 内部轮接管 → supervision 链耗尽），goal 就会保持 `active` 静默悬置，而 UI 仍显示 `Pursuing goal`。

本方案对齐 DSH `goal-round-driver` 的成熟形态（见 dsh 分析文档 §1.4），为 AAR 引入一个**会话级、常驻的 goal round-driver（决策核心 + 宿主适配器）**：

- 把"何时评估 goal"从 N 个调用点收敛为**一个状态机**，所有 run 终态、状态变更、就绪变化都是它的输入；
- 维持不变式：`goal.active && armed && idle && 无更高优先级让位 ⇒ 下一轮已被提交，或让位原因已显式登记`；
- 中断（Esc/cancel）只终止当前 run，不静默终止驱动；停止目标本体必须走 `/goal pause`；用户中断落为 `disarmed{user_interrupt}`（决策见 D1）；
- CLI 与 runtime-server 共享同一决策核心，杜绝两套平行控制流（当前：CLI 4 个调用点 vs 服务端 `session_end` 订阅，覆盖面天然不一致）。

**评审焦点（决策点）**：D1 用户中断语义（中断后停在何处、如何恢复）、D2 activation 状态模型（两态+reason vs 三态）、D3 kick 机制去留、D4 恢复态 rearm 机制、D5 supervision wake 与 goal 的优先级、D6 driver 审计事件、D7 系统异常（非用户）的自动恢复策略。详见 §8。

---

## 1. 事故取证

### 1.1 时间线（2026-10-18，本地时间 CST）

| 时刻 | 事件 | 证据 |
|---|---|---|
| 19:16 | 会话与 goal 创建（`goal_7b01a1ab…`，active）；CLI goal 循环正常驱动续跑 | session_history.sqlite；debug.log `[goal] auto continuation attempt` |
| 21:47:45 | CLI 进程重启（pid 37752 → 10104），会话恢复 | aicli.log `actor stopped … runtime_refresh:model`；mesh bindings `node-10104-…` |
| 21:47:46 | 恢复态默认 **disarmed**（G4 闸） | debug.log `continuation disarmed after restore` |
| 21:47:50 | 用户"继续" → **rearm**（只清闸，不排驱动；注释：靠"该前台轮收尾自然接上"） | debug.log `rearmed reason=user_continue_intent`；代码 `chat_goal_auto_continue.go:816-870` |
| 21:47:59 | context reconcile 注入 `RUN_STATE_MISSING` 纠正消息；"继续"轮首个 LLM 请求开始 | debug.log `context_reconciled`；`contextreconcile/reconcile.go:97` |
| 21:48:12 | **用户 Esc 中断**（`cancel_source=user_interrupt`）。该请求 12.4s、无任何流式输出即被取消；续跑循环按"真实取消 → 立即停止"退出；错误恢复按 `context_done` 跳过 | aicli.log `session run canceled … source=user_interrupt`；debug.log `[goal] error recovery skipped reason=context_done`；`chat_goal_auto_continue.go:558-566`（"Only real cancellation … must stop goal recovery"） |
| 21:48:12.760 | supervision 投递内部轮（输入 `[Child lifecycle preflight]`），此后全部轮次由 supervision 驱动 | session_messages seq=1126/1127/1130/1325 |
| 21:51–21:57 | 3 次 wake 投递失败重试（HTTP 429 / 用户再次中断），均由 supervision 重投递恢复 | runtime-events.jsonl `supervision.wake_delivery_failed` / `turn.resumed` |
| 21:57:48–22:13:41 | 长轮（15.6 min，step 94）成功完成 | trace_9a702ab4 |
| 22:13:42–22:14:07 | 末轮（新 preflight，snapshot_seq=22）成功，宣告"所有后台任务已终态" | trace_ac6231c2；session_messages seq=1325 |
| 22:14:08 起 | 全部静默：无新 wake、无 goal 评估、无 kick。goal 保持 active；进程 10104 仍存活监听 52030 | debug.log 最后写入 22:14:07；`Get-NetTCPConnection` 52030 = pid 10104 |

补充事实：

- 21:48:12 之后 debug.log **再无任何 `[goal]` 行**——即 `maybeAutoContinueActiveGoal` 及共享决策函数一次都没有被调用（任何分支都会写日志）。
- 21:48 之后每一轮的输入都是 `[Child lifecycle preflight]`（supervision 摘要，`internal/supervision/digest.go:257-262`），没有一轮是 ContinuationPrompt。
- CLI 中断提示文案为"当前运行已停止；已保留 0 条工具观察，**可从现有会话继续**"——文案承认中断只是"停这一轮"，与 goal 层实际行为（驱动停机）矛盾。

### 1.2 关键代码证据（现状）

| 机制 | 位置 | 现状 |
|---|---|---|
| CLI 续跑唯一入口 | `chat_send.go:138/148/161`（前台轮成功/错误恢复/退化重试） | 仅用户前台 sendMessage 路径 |
| kick 消费 | `chat.go:1985`（主循环顶部，仅 `goalKickPending` 时） | arm 点只有 `/goal resume`、恢复态问答作答 |
| rearm | `chat_goal_auto_continue.go:854-870` | **故意不请求 kick**（依赖"该轮收尾"） |
| disarm after restore | `chat_goal_auto_continue.go:800-811` | 仅置位，不评估、不排队 |
| 中断→停机 | `chat_goal_auto_continue.go:558-566, 189-193` | 用户中断被定义为"必须停止 goal recovery" |
| 服务端 driver | `internal/api/runtimeapi/session_goal_driver.go:217-426` | 订阅 `chat.EventSessionEnd`；默认关闭（`serverAutoContinuation=false`）；"用户中断不记账，等用户输入" |
| 就绪 watcher（迷你驱动器雏形） | `chat_goal_auto_continue.go:1006-1038` | 轮询 actor 就绪 → 唤醒主循环；一次性、仅服务 kick |
| 统一终态事件 | `chat.EventSessionEnd`（每个 run 终态） | **已存在**，服务端已订阅，CLI 侧未用于 goal 评估 |

### 1.3 判定

1. **goal 未完成**：持久状态 `active`（`time_used_seconds=2786` ≈ 46m，与 UI "(46m)" 吻合）；模型按 completion audit 纪律拒绝标记完成。**不存在误判 complete**。
2. **"停止"不是任何合法终态**：不是 complete / blocked / budget_limited / paused，也不是用户显式 stop。停止原因是"没有驱动者"。
3. **显示只是症状**：`Pursuing goal (46m)` 的正确语义是"armed + 累计用时"，**与是否有驱动者无关**；`armed 但无驱动` 是当前允许出现的非法状态（见 §2.2）。

---

## 2. 问题陈述

### 2.1 评估时机矩阵（现状 vs 应有）

| 时机 | 现状 | 缺口 |
|---|---|---|
| 前台轮成功收尾 | ✓ `chat_send.go:161` | — |
| 前台轮错误恢复 | ✓ `chat_send.go:148` | 用户中断（context.Canceled）被排除 |
| 退化重试成功 | ✓ `chat_send.go:138` | — |
| kick 消费 | ✓ `chat.go:1985` | arm 点过窄（仅 /goal resume、恢复态作答） |
| 恢复完成 | 仅 disarm | ✗ 不评估、不排队（依赖后续前台轮） |
| **非前台 run 终态**（supervision wake / actor 内部提交 / 远程 / schedule） | 无 | ✗ **本例断点** |
| user_interrupt 之后 | 直接停机 | ✗ 无再评估 |
| 任意 idle 转换 | 无统一钩子 | ✗ |
| 服务端兜底 | 订阅 session_end | 默认关闭；且与 CLI 是两套平行控制流 |

### 2.2 三个结构性缺陷

**F1. 驱动责任无归属（边沿触发 × 散点调用）。**
连续 seconds 级细化：goal 的"active 即续"（continue_if_idle）是一个**电平语义的不变式**，当前实现却是"走到的调用点才检查"的边沿处理。任何新增驱动源或任何路径绕过（如 supervision 接管、恢复流程、中断退出）都会制造"具备续跑条件但无人评估"的空档。这就是"补丁打不完"的根源：每发现一条漏网路径就补一个调用点/kick，而不变式本身始终没有守护者。

**F2. 异常路径的失败方向不一致（静默悬置 vs 显式 disarm）。**
对照 DSH（dsh 分析文档 §1.4）："**所有异常路径一律 disarm**——宁可保守停止，不带病重试"。disarm 是显式状态：UI 可见、人类可再入（resume），系统永远不会停在"看似在跑、其实没人管"的中间态。
AAR 现状：异常（用户中断、非前台轮接管）→ **静默悬置**（保持 armed、保持 active、无驱动者、UI 仍显示 Pursuing）。fail-safe 方向反了。

**F3. 中断语义与 goal 语义耦合。**
`context.Canceled`（用户 Esc）被同时用于两个层面：终止当前 run（正当）+ 终止 goal 驱动循环（越权）。停止目标的正规信号应是 `/goal pause` 或 disarm；CLI 自己的提示文案"可从现有会话继续"也证明中断本应只作用于 turn。附带后果：用户中断后如果没有任何后续前台轮，目标就永久失去驱动。

**F4（附加）。双宿主两套平行控制流。**
CLI 侧（`maybeAutoContinueActiveGoal` 散点）与服务端（`sessionGoalDriverEvaluate` 订阅）各自实现门控/strike/提交；覆盖面、让位判定（CLI 冷却窗 vs 前台路径）天然不一致。决策"提示词单源"（A3）已做到，但**控制流没有单源**。

**F5（附加）。activation 瞬态与持久 goal 的时间不一致。**
`goalContinuationDisarmed` 是进程内 atomics、不落库；CLI 进程每次重启都回到 disarmed。goal 作为"持久任务"（落库）与它的"驱动许可"（进程内）在时间轴上不闭合，用户每次重启都要再次表达继续意图。

### 2.3 被违反的不变式（本方案要恢复的红线）

> **INV-1**：`goal.Status == active && activation == armed && session == idle && 无更高优先级让位（用户输入 / 挂起审批 / supervision 待处理）` ⇒ 驱动轮已被提交，或让位原因已显式登记（可观测、可审计）。

推论：
- `armed` 的含义必须是"driver 在管理你"，而不是"闸门碰巧开着"；
- 不存在"既没在跑、也没有让位原因"的状态（当前事故状态）。

---

## 3. 设计目标

| 编号 | 目标 | 对齐 |
|---|---|---|
| G-A | 单一决策核心（策略层）+ 宿主适配器（提交手段），CLI 与 server 共享 | DSH round-driver；本文 F4 |
| G-B | 电平触发：所有 run 终态 / 状态变更 / 就绪变化统一汇聚为 driver 输入 | continue_if_idle；本文 F1 |
| G-C | 让位必须显式：每条"不驱动"决策都带原因（日志 + 可选 UI） | DSH `readyToDrive`/让位；本文 F2 |
| G-D | 中断只终止 run；goal 级停止必须显式（pause/disarm） | 本文 F3 |
| G-E | 行为兼容：prompt、strike、round-limit、budget、wrap-up、iterate 升级、kick API 语义不回归 | 现有测试面 |
| G-F | 可观测：driver 决策可审计（trigger + 判定输入 + 结果），UI 状态显式化（Pursuing / disarmed-原因可见） | 现场可诊断性 |

### 3.1 非目标（Non-Goals）

- 不扩展模型 goal 工具面（保持 `get_goal` / `update_goal(complete|blocked)` 窄口子）。
- 不引入模型自建 goal；goal 的用户建权语义不变。
- 不改变 continuation 提示词/收尾文案（`internal/goal/continuation.go` 的逐字节约束保持）。
- 不在此方案内解决 activation 落库（进程内语义保持；"重启回 disarmed"的体验问题列为后续项）。
- 不改变子代理会话门控（base-session 门控保持）。
- 不改变 strike/round-limit/budget 的数值语义（仅单源化）。

### 3.2 备选方案与取舍（Alternatives Considered）

| 备选 | 描述 | 结论 |
|---|---|---|
| A. 只补挂载点（不建 driver） | 在现有 4 个调用点之外再加事件订阅与 kick | 否：不解决边沿触发本质（F1），下一条新路径仍可能漏 |
| B. CLI 完全交由服务端 driver | CLI 不实现本地驱动 | 否：纯本地/离线 CLI 无 server；延迟敏感交互不适合远程往返 |
| C. 全量照搬 DSH（含事件溯源/CAS） | phase×activation + round-driver + revision CAS 一次到位 | 分阶段：G1–G3 已另有方案；本文聚焦 driver 控制流（F1–F5） |
| D. 仅加 INV-1 探针告警 | 只观测不修复 | 否：事故仍会发生，只是事后可见 |

---

## 4. 方案：goal round-driver

### 4.1 组件边界

```
internal/goal/driver/            ← 新增：纯策略核心（无 IO、可单测）
  view.go        DriverView（决策输入快照）
  decision.go    Decide(view) -> Decision（纯函数，规则单源）
  audit.go       Decision 记录结构（trigger/reason/action/evidence）
  loop.go        Loop：事件汇聚 + 单 in-flight 管理 + 宿主回调驱动

宿主适配器（薄层，只提供能力与提交手段）：
  CLI    : cmd/aicli/commands/goal_driver.go      （挂 ChatSession / localChatRuntimeHost）
  Server : internal/api/runtimeapi/session_goal_driver.go   （重构为同一核心的适配器）
```

设计约束：

- `Decide` 为纯函数：输入 DriverView、输出 Decision，**所有"何时继续/何时让位/终态收敛"的规则只在这里**。CLI 与 server 不允许再有各自的分支拷贝。
- `Loop` 负责：接收 trigger、组装 DriverView（经宿主回调读会话/goal/actor 状态）、调用 Decide、执行 Decision（提交轮/登记让位/终态处理）、记账。
- 宿主差异仅剩三处：读会话存储、提交轮的手段、UI 呈现。其余全部共享。

### 4.2 状态模型（driver 视图）

```
DriverView {
  Goal:       missing | active{rounds,maxRounds,tokenBudget,tokensUsed} | paused | blocked | budget_limited | complete
  Activation: armed | disarmed{reason}          ← 见 D2（reason: restore | user_interrupt | …；不引入第三态）
  Session:    idle | running | waiting_approval | waiting_question | interrupted
  Pending:    none | user_input | approval | question | supervision_wake
  Strikes:    {errors, no_progress, escalation_attempted, doom_loop}
  Host:       cli | server   (仅用于双驱动互斥，不参与策略)
}
```

说明：

- `Activation` 仍为进程内状态（对齐 DSH），但**语义收紧**：`armed` 表示"driver 在管理该 goal"；`disarmed{reason}` 表示"已明确让位给人类（原因用于 UI/审计），等显式 rearm"。
- 不再存在"armed 但无人驱动"的中间态：决策若让位，必须落到 `disarmed` 或登记 pending 让位原因（INV-1）。

### 4.3 事件输入（triggers）

| Trigger | 来源 | 现状（复用/新增） |
|---|---|---|
| `run_terminal` | `chat.EventSessionEnd`（**所有来源的 run 终态**，含 supervision 内部轮、远程、schedule） | 服务端已订阅；**CLI 侧新增订阅** |
| `user_input` | sendMessage 入口 / 输入队列事件 | 已有（rearm 挂钩此处） |
| `goal_state_changed` | `/goal` 命令、模型 `update_goal`、CAS 写入 | 部分存在 |
| `answer_recorded` | 恢复态提问/审批作答 | 已有 kick 路径 |
| `actor_ready` | 就绪 watcher（将现有 `startGoalKickReadinessWatcher` 泛化） | 已有雏形 |
| `supervision_wake_pending` | wake consumer / `turn.resumed` | 已有独立仲裁，改为通知 driver |
| `timer_tick` | 低频兜底（防事件丢失；默认关闭或 60s+） | 新增（防御性） |

汇聚约束：所有 trigger 进入 `Loop.Notify(trigger)`；**Loop 保证幂等 + 单 in-flight**（对齐 DSH"每 agent 至多一个续跑"）。

### 4.4 决策规则（优先级顺序，单源）

`Decide(view)` 按序执行，命中即返回（返回结构含 `Reason` 供审计）：

| # | 条件 | 动作 | Reason 示例 |
|---|---|---|---|
| 1 | 会话 running / waiting_approval / waiting_question / pending user_input | 让位，等待对应事件 | `busy` / `pending_approval` / `pending_input` |
| 2 | supervision 待处理 wake（已 claim 未 delivered，或投递失败重试中） | 让位（wake 自身驱动轮） | `supervision_pending` |
| 3 | goal missing 或非 active | 终态处理（complete/blocked wrap-up、reset strikes） | `goal_complete` / `goal_blocked` / `goal_paused` / `goal_missing` |
| 4 | activation = disarmed（原因细分：restore / user_interrupt） | 让位；UI 显示对应原因 | `goal_disarmed` / `user_interrupt_disarmed` |
| 5 | token 预算耗尽 | 置 `budget_limited` + 一次性 wrap-up | `budget_exhausted` |
| 6 | RoundsStarted ≥ MaxRounds（>0） | 置 `blocked(round-limit)` | `round_limit` |
| 7 | error/no-progress strike 达阈值 | 置 `blocked`（共享阈值） | `consecutive_errors` / `no_progress` |
| 8 | 以上均否 | **提交续跑轮**（默认/升级/收尾 prompt 选择沿用共享层） | `goal_active` |

规则要点：

- 规则 3/5/6/7 的终态收敛逻辑与 CLI/server 现有实现**语义不变**（仅单源化），确保行为兼容（G-E）。
- 规则 2 是把"supervision 优先"显式化：唤醒轮优先于 goal 续跑，避免两套轮抢占同一会话；wake 处理完后 `run_terminal` 会自然回到 driver（这正是本事故中缺失的"回到评估"）。
- 规则 4 是 INV-1 的兜底：让位必须有身份（disarmed 或 pending-*），否则必须驱动。

**状态转换与终止性：**

- 提交失败（actor 拒绝 / 存储错误）：不改变 goal 态；记 warning（`submit_failed`）并按 `actor_ready` 重试；同一 trigger 序列的连续提交失败计入 error strike（有界）。
- wrap-up 轮一次性：complete/blocked/预算收尾以幂等标记提交，重复 trigger 不重复提交。
- 预算复活：`budget_limited` 仅在预算提高到 > 已用（`GoalMergePolicy` 现有守卫）时回 `active`，由规则 5 判定。
- 终止性：只有 goal 终态（complete/blocked/budget_limited）或 `disarmed` 允许停止驱动；任何运行期错误都必须回到规则 1–7 重新判定，不允许静默终止。

### 4.5 驱动循环（提交、回收、记账）

- **提交**：`Decision.action = submit_continuation` → 宿主适配器提交一轮（CLI：隐藏续跑轮 / server：`goalDriverSubmitContinuationWithPrompt`）；提交前后做 actor 就绪 + 会话 CAS 校验（对齐现有 `chatActorAcceptingWork`）。
- **回收**：该轮终态 → `run_terminal` trigger → 记账（usage 入 goal）、strike 更新、wrap-up 判定 → 再次 `Decide`。
- **单 in-flight**：Loop 持有 in-flight 标记；已提交未回收期间到达的 trigger 只更新视图（合并），不并发提交。
- **执行上下文（两段式）**：终态事件/Notify 只做"请求"（入队），真正提交在宿主就绪上下文消费（复用 kick"请求→延迟消费"两段式的教训：直接起轮会与渲染/输入竞争）。CLI 侧由主循环或就绪 watcher 消费；server 侧由 handler goroutine 消费。
- **去重**：以 run/turn id 为键，同一终态只评估一次；前台轮收尾的内联调用与统一事件订阅同期只允许一个评估生效（Phase 1 硬性要求）。
- **输入优先**：提交/运行期间沿用 `startBusyQueuedInputCapture`（用户输入永远可入队/作答，不被续跑吞掉）。
- **错误与中断**：
  - `run_terminal` 携带 `cancel_source`，按 D1 分类：
    - `user_interrupt` → `disarmed{reason=user_interrupt}`（显式、可见；rearm 时立即驱动）；
    - 系统取消（actor stop / runtime refresh / session switch）→ activation 不变，直接回到 Decide（若 active+armed+idle → 再提交）；
    - 运行错误（provider 失败等）→ strike 记账，回到 Decide（现有语义）。
- **轮上限/预算/wrap-up**：保持现有语义（`AdmitGoalRound`、`BudgetWrapUpPrompt`、`Complete/BlockedWrapUpPrompt`）。

### 4.6 让位与可观测（审计）

- 每次 Decide 产出一条审计记录：

```json
{"schema":"goal.driver.decision.v1","ts":"...","session_id":"...","trigger":"run_terminal",
 "view":{"goal_status":"active","activation":"armed","session":"idle","pending":"supervision_wake","strikes":{"no_progress":0}},
 "action":"defer","reason":"supervision_pending","evidence_refs":["event:turn.resumed:..."]}
```

- 落点：会话 events（`goal.driver.decision`，采样开关，正常量级），debug.log 摘要一行（替代现有散落 `[goal] …` 行）。
- 数据最小化：审计不携带 goal objective 全文与工具输出内容，仅引用句柄（goal_id / event_id）。
- **INV-1 守护探针**：`armed && active && idle && pending=none` 但连续 N 秒（如 30s）无提交 → 打 warning（`goal.driver.stalled`），供现场诊断；这是本次事故"静默悬置"不可能再出现的直接保证（至少可被观测）。
- UI 语义（G-F）：
  - `Pursuing goal (…)`：in-flight 或 pending 已提交（driver 在场）；
  - `Goal idle (/goal resume)`：disarmed；原因细分（恢复 / 用户中断），文案明确"未完成、已停、如何继续"。

### 4.7 中断语义（关键决策 D1）

现状（`chat_goal_auto_continue.go:558-566`）：`context.Canceled` → "Only real cancellation … must stop goal recovery"，且停机**无身份**（既不 disarm、也无任何让位登记，静默悬置）。

候选方案（按终态来源区别对待）：

- **D1-a（推荐）**：
  - 用户中断（`cancel_source=user_interrupt`）→ `disarmed{reason=user_interrupt}`（显式、可见、等用户明示）。UI：`Goal idle · user interrupt (/goal resume)`。**不**自动续跑（尊重"停"）；**不**静默保持 armed（消除"armed 但无人驱动"的非法态）。
  - 解除：`/goal resume` 或显式继续意图（沿用 G4 短语表）；**解除即 Notify 并立即驱动**——这是本次事故的核心修复点（任何 disarmed→armed 转换都必须立即驱动，不再依赖后续某轮收尾）。
  - 系统取消（actor stop / runtime refresh / session switch）→ activation 不变，回到 Decide（有界重试）。
  - 运行错误（provider 失败等）→ strike 记账（现有阈值），超限 → blocked；否则回到 Decide。
  - 停止目标本体 → `/goal pause`（goal 级操作），与"停轮"分离。
- **D1-b（被否）**：中断后保持 armed 自动续跑——用户按 Esc 想停，隐藏轮马上又起，"幽灵续跑"回归；且"停不住"比"停得显式"更糟。
- **D1-c（被否）**：中断冻结以"任意用户消息"解除——查询类消息（"看看状态"）会触发续跑，同样属于幽灵续跑，与 G4 修复初衷矛盾。
- **与 DSH 的差异说明**：DSH 对"所有异常一律 disarm"（其 round-driver 无 strike 自愈面）；AAR 保留 strike 自动恢复面（provider 类错误自愈），仅把**用户中断**落为 disarm——与"用户显式在场"语义对齐，而非放宽。

### 4.8 与现有机制的迁移映射

| 现有机制 | 去向 |
|---|---|
| `maybeAutoContinueActiveGoal` 循环体 | 规则 5–8 的执行段（strike/预算/escalation 语义保留，移入规则单源） |
| `chat_send.go:138/148/161` 三个调用点 | 由统一 `run_terminal` trigger 取代（与事件订阅以 run/turn id 去重，只保留一个生效） |
| `goalKickPending` / `runGoalContinuationKick` | API 保留（`/goal resume` 等调用点不变）；内部改为 `Notify`，不再是唯一唤醒路径 |
| `startGoalKickReadinessWatcher` | 泛化为 `actor_ready` trigger（保留轮询作为兜底） |
| `disarmGoalContinuationAfterRestore` | 保留：恢复时 setting `disarmed` |
| `rearmGoalContinuation` | 改为 `Notify(activation_changed)`，**必须触发一次 Decide**（消除"依赖下一轮收尾"的赌注） |
| `shouldAutoContinueAfterGoalTurnError`（context_done 跳过） | 由 D1 规则替代：user_interrupt → `disarmed{user_interrupt}`；系统取消回到 Decide；运行错误走 strike |
| `session_goal_driver.go`（server 控制流） | 重构为该核心的 server 适配器；strike 记账收敛进 Loop |
| CLI 冷却窗 / `serverAutoContinuation` | 收敛为驱动者租约（§4.9）；配置语义保留 |
| `[goal] …` debug 行 | 收敛为 `goal.driver.decision` 审计 + 单行摘要 |

### 4.9 双宿主统一与互斥（驱动者租约）

- 同一 `Decide`，两个 `Loop` 实例（CLI 宿主 / server 宿主）。
- 互斥从"冷却窗启发式"升级为**驱动者租约（lease）**：CLI 活跃期间周期性续约（续约点 = 命令通道入口/轮提交/心跳，建议心跳 30s；租约窗口先沿用 5min 冷却窗，实测调整）；租约过期且 server 开启 → server 接管；租约状态可观测（审计事件）。
- 显式化"无人驱动"组合：`CLI 不在场 && serverAutoContinuation=false` 时，driver 应记录 `reason=no_driver_configured`（而不是静默）。
- 子代理会话不驱动（保持 base-session 门控，gap-1 口径）。

---

## 5. 分阶段实施

### Phase 0 — 复现与护栏（不改变行为）

- 用本次事故路径写集成复现（手工脚本或 E2E）：`恢复 → rearm → 首轮中断 → 注入 supervision wake 轮 → wake 链耗尽`，断言当前 bug 行为（goal active + 无驱动）。
- 落地 INV-1 探针的最小版本：`armed && active && idle` 超过阈值（默认 30s）无提交 → warning（可只加日志）。

### Phase 1 — 统一挂载点（最小可行驱动闭环）

- 前置验证：确认 CLI 内嵌宿主的**所有来源** run 终态（含 supervision 内部轮）均发布 `chat.EventSessionEnd`；订阅生效前不改变任何行为；
- 订阅与去重：按 run/turn id 去重；与前台轮收尾的内联调用互斥（同一终态只允许一次评估）——**Phase 1 硬性前置，防止双提交**；
- 执行上下文：沿用两段式"请求→就绪消费"（§4.5），不复用前台渲染上下文；
- 互斥前提：维持现有"CLI 在场时 server 让位"条件；默认配置（serverAutoContinuation=false）下无双驱动；
- 评估复用现有续跑执行体（把 `maybeAutoContinueActiveGoal` 的入口从"仅前台调用"扩展为"可被终态事件调用"）；
- `rearm`/中断解除立即 Notify（不再等下一轮收尾）；
- 新增灰度开关（`goal.driver.enabled`，本阶段默认关闭），可随时回落旧路径；
- 保留 kick/watch/disarm 全部现有语义（本阶段只补挂载面，不搬家）。
- 验收：本事故组合场景不再静默悬置；双通道去重用例通过；既有单测全绿。

### Phase 2 — 抽取决策核心（单源化）

- 建 `internal/goal/driver`：`DriverView` + `Decide` + `Loop` + 审计；
- CLI 与 server 适配器接入；删除两套控制流中的重复门控/决策分支；
- strike/预算/round-limit/wrap-up/escalation 全部收敛为规则；
- kick/disarm/rearm 完成向 trigger 的适配（API 兼容层保留）。

### Phase 3 — 语义与体验收敛

- D1 中断语义落地（disarmed{user_interrupt} 与原因显示）；
- 驱动者租约替代冷却窗；
- `goal.driver.decision` / `goal.driver.stalled` 审计接线（会话 events + 控制台）；
- 评估 `serverAutoContinuation` 默认值与派发口径。

---

## 6. 验收与测试矩阵

| 层级 | 用例 | 断言 |
|---|---|---|
| 单元 | `Decide` 规则表全组合（busy/pending/disarmed/预算/轮限/strike/active） | 每条判决带 Reason；优先级与本文 §4.4 表一致 |
| 单元 | 迁移回归：原 `maybeAutoContinueActiveGoal` 行为矩阵（升级/空转/错误/预算 wrap-up） | 语义不变（逐字节 prompt 约束保持） |
| 集成 | **事故复现**：恢复→rearm→首轮 user_interrupt→supervision 轮→wake 耗尽 | goal 不静默悬置：要么驱动，要么显式 disarmed/pending |
| 集成 | 中断分类：user_interrupt → disarmed(user_interrupt)；显式继续意图/`/goal resume` → 解除并立即驱动；系统取消 → 保持激活；运行错误 → strike | UI/审计一致 |
| 集成 | 非前台轮（wake）终态后：若 goal armed+active+idle → 立即续跑 | 断点消除 |
| 集成 | 双宿主互斥：CLI 在场 server 不驱动；CLI 退出租约过期 server 接管 | 无双驱动、无空档 |
| 集成 | 轮上限 / 预算 / wrap-up / round-limit blocked | 与现状一致 |
| 集成 | 同一 run 终态双通道触发（前台内联 + 事件订阅） | 仅一次评估、至多一次提交 |
| 集成 | 提交失败（actor 拒绝/存储错误）→ 就绪重试 | 不改 goal 态；有界重试；计入 error strike |
| 集成 | 事件丢失 → timer_tick 兜底 | 探针/兜底恢复驱动或告警 |
| 迁移 | Phase 1 与 Phase 2 行为等价性 | 决策矩阵回归 +（可选）影子对比 |
| 观测 | `goal.driver.decision` 审计可回放；`stalled` 探针可触发 | INV-1 可审计 |
| 真机 | CLI 全流程（含 Esc、/goal resume、恢复重启） | 状态栏原因显示正确（Pursuing / disarmed-原因） |

---

## 7. 风险与兼容

| 风险 | 缓解 |
|---|---|
| 事件丢失导致不驱动 | `timer_tick` 低频兜底 + `stalled` 探针 |
| 双驱动（CLI+server） | 驱动者租约互斥；提交前 CAS/就绪校验 |
| 行为变化破坏既有测试/用户习惯 | Phase 1 先加挂载不改语义；Phase 2 单源化保持判定矩阵不变；灰度开关 |
| Esc 体验"停不住/被偷袭" | D1-a：Esc → disarmed{user_interrupt}（不会自动续跑），状态显式可见 |
| activation 瞬态 + 进程重启 | §4.7 两态+reason 落为进程内；重启回 disarmed 的现状保留（"最近人类活动"启发列为后续项） |
| Phase 1 双通道触发/双驱动 | 去重键 + 互斥前提（§5）；灰度开关可回滚 |
| 渐进启用与回滚 | `goal.driver.enabled` 分阶段放量（关→灰度→默认开）；关闭时回落现有路径（行为零变化） |
| supervision 与 goal 抢占 | 规则 2（wake 优先）+ 单 in-flight |

---

## 8. 开放问题（评审决策项）

| 编号 | 问题 | 推荐 | 备选 |
|---|---|---|---|
| D1 | 用户中断（Esc）后的语义 | **`disarmed{user_interrupt}`**：显式停、可见、rearm 立即驱动；系统取消保持激活；运行错误走 strike | 保持 armed 自动续（幽灵风险）；"任意消息解除"（幽灵风险）；DSH 式全异常 disarm（失去 strike 自愈） |
| D2 | activation 状态模型 | **两态 + reason**（`armed | disarmed{restore|user_interrupt}`，进程内）；不引入第三态 | 三态 held（冗余）；无 reason 两态（UI 无法区分原因） |
| D3 | kick 机制去留 | 保留 API，内部并入 trigger（兼容 `/goal resume` 等调用方） | 保留独立机制（双轨，不建议） |
| D4 | 恢复态 rearm 机制 | 保留 disarmed + 显式继续意图；**任何 rearm（恢复/中断）都立即驱动**（同一路径） | 恢复后直接 armed（G4 事故回归）；一律短语（体验差） |
| D5 | supervision wake 与 goal 续跑的优先级 | **wake 优先、goal 让位**（规则 2），wake 处理后自然回 driver | goal 优先（会饿死 wake） |
| D6 | 审计事件与落盘 | `goal.driver.decision` 入会话 events（采样开关 + 数据最小化）；`stalled` 探针默认开 warning | 仅 debug 日志（不推荐） |
| D7 | 系统异常（非用户）的自动恢复策略 | 保持 armed + strike 自愈（延续现有语义）；仅用户中断落 disarm | DSH 原教旨：异常一律 disarm（要求用户每次重新明示） |
| D8 | driver 发布形态与回退（已裁决 2026-10-18） | **默认启用**；不进公开配置面；应急回退 `AICLI_GOAL_DRIVER_DISABLED`（临时）；退役：Phase 2 单源化 + 观察期后移除 | 公开配置项（长期双路径，否决）；完全无回退（过渡期裸奔，否决） |

---

## 附：本方案与既有问题项的衔接

- G4（CLI rearm 闸）：保留，但其"rearm 不排驱动"的赌注由 §4.8 的 `Notify` 修复；短语表适用于所有 disarmed 解除（恢复态 D4 与用户中断 D1）。
- G2/G3（round-limit、wrap-up）：归入规则 6 与终态处理，语义不变。
- A3/A4（提示词单源、CLI 让位）：升级为"决策单源 + 租约互斥"。
- 本文 F1–F5 的每个缺陷对应到 §4.4 的规则或 §4.9 的租约，均有落点，不留"再补一个调用点"式尾巴。## 9. 实施记录（Phase 1，2026-10-18）

节点：**Phase 0/1 的最小闭环已实施**（默认灰度关闭 `goal.driverEnabled: false`，行为零变化）；§4 的完整状态机（决策单源化、审计事件、租约）仍为后续阶段。

### 9.1 已落地变更

| 文件 | 变更 |
|---|---|
| `cmd/aicli/commands/chat_goal_driver.go`（新增） | 统一挂载点：`wireLocalGoalDriver` 订阅 `chat.EventSessionEnd`；`observeGoalDriverTerminal` 单入口评估（turn 去重 / disarmed 让位 / user_interrupt 冻结 / kick 两段式请求 + 就绪 watcher + 唤醒）；disarm 原因记录；应急回退 env `AICLI_GOAL_DRIVER_DISABLED`（默认启用，见 §9.4/D8） |
| `cmd/aicli/commands/chat.go` | ChatSession：`goalDriverEnabled`、disarm reason（`setGoalContinuationDisarmed`/`clearGoalContinuationDisarmReason`）、终态去重表（容量 128 FIFO） |
| `cmd/aicli/commands/chat_goal_auto_continue.go` | rearm 立即请求驱动（接力修复）；kick 消费门增加 disarmed 兜底；restore disarm 统一走 `setGoalContinuationDisarmed("restore")` |
| `cmd/aicli/commands/chat_actor_host.go` | 宿主装配 `wireLocalGoalDriver()`（与 supervision wake consumer 并列） |
| `cmd/aicli/commands/chat_setup.go` | 会话注入 `goalDriverEnabledFromEnv()`（默认启用） |
| `cmd/aicli/commands/chat_goal_driver_test.go`（新增） | driver 单测 + 事故场景集成（12 用例） |

### 9.2 行为语义（Phase 1，默认形态）

- **统一评估**：所有来源 run 终态（前台轮 / supervision wake 内部轮 / actor 内部提交）→ `observeGoalDriverTerminal` → 请求一次性 kick（"请求→主循环两段式消费"，沿用 busy-input capture 与就绪 watcher）。
- **中断冻结**：`user_interrupt` 终态 → 作废在途 kick + `disarmed{user_interrupt}`（状态栏显示 `Goal idle (/goal resume)`）；显式继续（`/goal resume` 或继续意图）→ rearm 且**立即**请求驱动（不再依赖"后续某轮正常收尾"）。
- **去重**：同一 run 终态只评估一次（`turn_id` 键；前台内联路径与订阅路径互斥）。
- **应急回退**：`AICLI_GOAL_DRIVER_DISABLED=1`（隐藏、临时）时订阅/冻结/rearm 驱动全部短路，行为与旧版本一致。

### 9.3 测试证据

- `TestGoalDriverSurvivesInterruptAndSupervisionHandoff`：事故组合场景（恢复→rearm→首轮中断→监督轮接管→显式继续恢复驱动），全程无"armed 且无驱动者"悬置态；
- `TestGoalDriverTerminalDrivesNonForegroundContinuation`：非前台轮终态真实启动续跑（事故断点消除）；
- `TestWireLocalGoalDriverSubscribesRunTerminals`：订阅端到端（含 foreign session 过滤）；
- 去重 / 冻结 / 消费门 / rearm 双路径 / disabled no-op / env 回退等 14 个 driver 用例全部通过；
- `go build ./...` 全仓通过；`go test ./cmd/aicli/commands/` 全量回归通过（ok，191s，默认启用调整后复跑）；`go test ./internal/config/` 通过。

### 9.4 发布形态与回退（D8 裁决，2026-10-18）

- **默认启用**：统一驱动（所有来源 run 终态评估 + 中断冻结 + rearm 立即驱动）是"防静默悬置"正确性修复，不以 opt-in 形态发布；不进入公开配置面，避免长期双路径。
- **应急回退**（隐藏、仅排障）：`AICLI_GOAL_DRIVER_DISABLED=1` → 退回旧行为（续跑仅由前台交互路径驱动）。
- **退役条件**：Phase 2 决策单源化落地 + 一个观察期后，移除该回退开关，使统一驱动成为唯一形态（届时本节回退条目与代码一并删除）。

遗留（映射 §5 Phase 2/3）：决策核心单源化（`internal/goal/driver`）、中断 UI 文案按 reason 细分、`goal.driver.decision` 审计事件接线、驱动者租约替代冷却窗、INV-1 `stalled` 探针。### 9.5 真机验证（2026-10-18，session_20261018232712_yM77VVFe）

环境：`aicli-4x`（含本实施全部改动构建），**默认启用、无任何配置**；通过 `/web/api/input|turn|statusbar` 与 debug.log 取证。核心链路 6/6 通过：

| # | 链路 | 证据 |
|---|---|---|
| 1 | 默认启用（无配置） | debug.log 首行 `[goal] driver subscribed run terminal events` |
| 2 | 目标创建即驱动 | `/goal …` → 目标轮自动运行；状态栏 `Pursuing goal` |
| 3 | 运行中中断 → 显式冻结 | `[goal] continuation frozen reason=user_interrupt`；错误恢复跳过；状态栏切 **`Goal idle (/goal resume)`** |
| 4 | 冻结期"停得住" | 14s 观察窗口无自动重启、无驱动请求 |
| 5 | 显式继续 → rearm + 立即驱动 | `rearmed reason=user_continue_intent` 与 `kick requested` **同毫秒**；新轮启动、状态栏回 `Pursuing goal` |
| 6 | 完成收敛 | `terminal wrap-up requested` → 收尾轮 → 状态栏 `Goal achieved` → **`kick dropped reason=goal_missing`**（完成后无幽灵续跑） |
| 7 | 终态统一评估（正向日志） | `[goal] driver requested continuation after run terminal`（非 kick 场景的轮终态） |

真机另暴露并修复两个问题：

- **F1 日志精确性**：kick 被拒（如 `goal_missing`）时 `driver requested continuation` 仍被打印。修复：`requestGoalDriverKick` 返回 `bool`，仅受理成功才记录。
- **F2 既有 G3 缺陷（真机首次暴露）**：goal 终态后，reconcile 的粘性 `completed` 使每轮收尾都重新 Store wrap-up prompt → **每次新的前台消息后都会补跑一个隐藏收尾轮**（实测 probe 两次均复现：多一个 4s 收尾轮）。修复：按“`goal_id|status|updated_at`”做终态去重（同一终态只触发一次；resume 后重新终态 `updated_at` 前移可再次触发）。新增回归用例 `TestRequestGoalTerminalWrapUpDeduplicatesSameTerminalState`。

> F2 的真机复验需下一次构建（当前运行中的二进制不含该修复）；单测已覆盖去重与重新终态两条路径。