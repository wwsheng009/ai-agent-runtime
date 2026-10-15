# aicli /goal 续跑语义对齐 Codex 实施方案

**状态**：草案（待评审后实施）
**日期**：2026-10-14
**前作**：`docs/plan/aicli-goal-command-implementation-plan.md`（MVP：命令 / 模型 / 持久化 / 预算字段 / 续跑雏形）
**对齐基线**：`E:\projects\ai\codex` 主树 `main@cc7ba33601`。仅存在于**未合入远端分支**的机制不纳入：`goal-watchdog-model`（模型看门狗）、`guardian-goal-continuation`、`goal-capacity-retry`、`exec-resume-active-goal`。
**调研依据**：双方代码实读与只读探索（关键 file:line 证据见附录 A/B；实施时以最新代码为准）。

---

## 1. 背景与问题

用户反馈：`/goal` 有时"任务未完成就停止"。逐行比对双方实现后确认，差距集中在六个机制：

| # | 本项目现状（问题） | 证据 |
|---|---|---|
| 1 | **每用户消息最多 5 轮隐藏续跑**，用尽即停（goal 保持 active、等下一次输入才恢复） | `chat_goal_auto_continue.go:14,43,74-75,197-211` |
| 2 | 续跑只在 `sendMessage` 收尾 3 个钩子触发；无 turn-idle 驱动；`/goal resume` 不触发 | `chat_send.go:134,144,157`；`goal.go:43-44`；`chat_command_result.go:1375-1376` |
| 3 | 静默停机门（最高危 `update_goal_unavailable`、`interrupted` 等），交互模式下用户无感知 | `chat_goal_auto_continue.go:140-179`（尤其 :162-164） |
| 4 | 单次 `ContinueGoal` 错误也消耗一次额度；无"宽容 3 连"语义 | `chat_goal_auto_continue.go:65-68` |
| 5 | 续跑提示词为单段话；缺 no-progress / 不缩目标 / blocked 审计 / 完成审计细则 | `chat_goal_auto_continue.go:15` vs Codex `ext/goal/templates/goals/continuation.md` |
| 6 | `TokenBudget/TokensUsed` 字段无生产写入方（`budget_limited` 不可达） | `internal/goal/goal.go:23-28,35-37`；全仓 grep 无赋值点 |

**Codex（main）对应语义**：

- 续跑由 **turn idle** 驱动：每次 turn 结束（含续跑 turn 自身结束）→ `on_thread_idle → continue_if_idle()`；**active 即续、无固定次数上限**（`ext/goal/src/runtime.rs:436-535`、`extension.rs:196-201`、`core/src/tasks/mod.rs:881`）。
- 停止是**确定性规则**：空响应 3 连 / 内置执行失败 3 连 → `blocked`；非取消类 turn 错误 → `blocked`；用量超限 → `usage_limited`；预算耗尽 → `budget_limited`（终态 + 收尾引导）；用户中断 → `paused`；排队输入 / plan 模式 → 让位或暂缓（`accounting.rs:229-246,121-173`；`runtime.rs:315-341`；`state/src/runtime/goals.rs:546-570`）。
- set/resume 为 active **立即触发续跑**，不必等下一次用户输入（`runtime.rs:226-241`、`thread_goal_processor.rs:250`）。
- 续跑提示词为结构化模板：持久性、不缩目标、no-progress/verified-wait 分类、完成审计逐项证据、blocked 审计 ≥3 连门槛、update_goal 纪律（`steering.rs:53-94`）。
- `token_budget=None` 且持续有活动时，除用户/模型显式结束或 turn 错误外**不存在自停路径**。

---

## 2. 目标与非目标

### 2.1 目标（分三期，可独立提交）

**P0 —— 续跑不再被截断（直接修复"未完成就停止"）**

1. 以条件循环替代 5 次硬上限：goal 为 active 且无让位条件时持续续跑；每轮结束后重新评估（对齐 `continue_if_idle` 语义）。
2. 新增可配置安全阀 `goal.maxConsecutiveContinuations`（默认 `0 = 不限制`，对齐 Codex；正整数为用户自选上限）。
3. 错误有界：连续错误 3 次即停（可见提示），防止"无上限 + 持续错误 = 无限重试"。
4. 触发放大：`/goal resume`（及一切"置 active"入口）立即触发续跑；pending-question 回答完成后回到续跑钩子。
5. 可见化：所有**终结性停止**（安全阀到顶、错误 3 连、`update_goal_unavailable`）在交互模式输出明确原因与恢复方式；非终结 reason 维持 debug。

**P1 —— 停止语义与提示词对齐**

6. 3 连容忍语义精确化：空响应/全无进展 3 连、连续失败执行 3 连 → 停止；成功续跑轮重置计数。
7. 新增 `blocked` 状态：系统写（上述停止）、用户 `/goal resume` 解除；对齐 Codex"状态变更只允许从 active 发起（用户 resume 除外）"。
8. 非取消类真实 turn 错误 → `blocked`（对齐 main；允许的受控偏差见 §9）。
9. 续跑提示词多段化：映射 `continuation.md` 的持久性 / 不缩目标 / 进度分类 / 完成审计 / 阻塞审计 / update_goal 纪律，并结合本仓后台 job 轮询（`write_stdin`/`job_*`）语义改写。
10. `renderActiveGoalGuidance` 同步补充"不缩目标 / 阻塞 ≥3 连"等一致性条款。

**P2 —— 预算记账与 `budget_limited` 收尾（接口先行）**

11. 每 turn 记账 `tokens_used`（含续跑 turn）；阈值触发 `budget_limited` + 注入收尾引导（对齐 `budget_limit.md` 语义）。
12. 预算来源：配置默认预算（`goal.defaultTokenBudget`，0=无）与/或 `/goal budget` 命令（命令形态待定）。

**测试**

13. 单测矩阵覆盖 P0/P1 语义（§8）；`scripts/run-conpty-goal-e2e.ps1` 扩展弱断言；全量回归 `cmd/aicli/commands` + `internal/goal` + `internal/config`。

### 2.2 非目标

- 不移植 Codex 未合入 main 的分支机制（watchdog / guardian / capacity-retry / exec-resume）。
- 不引入 Codex 的 SQLite `thread_goals` / app-server 架构；继续 session metadata 体系。
- 不改 exec / `--no-interactive` / ACP headless 的续跑行为（`no_interactive` 门保持；另行评估）。
- 不引入 `usage_limited` 状态（保留设计位，P2 之后再评估）。
- P1 不开放"模型写 blocked"（Codex 允许且有 ≥3 连门槛；本方案列为 P2 开放问题）。---

## 3. 对齐基线：Codex main 机制清单

| 机制 | Codex（main）实现与证据 | 本项目对应点 | 对齐动作 |
|---|---|---|---|
| 续跑触发 | turn idle 事件 → 扩展 `on_thread_idle` → `continue_if_idle()`（`tasks/mod.rs:881`、`extension.rs:196-201`、`runtime.rs:436-535`） | `sendMessage` 收尾调用循环（`chat_send.go:134,144,157`） | P0：循环条件化（等价"每轮结束再评估"）；保留 sendMessage 入口 |
| 次数限制 | 无固定上限；停止由状态/预算/阈值驱动 | 5 次/用户消息硬上限（`chat_goal_auto_continue.go:14,43`） | P0：默认不限 + 可配安全阀 |
| 让位条件 | 非空闲 / 排队 trigger turn / plan 模式 / 服务器 drain → 仅记日志，等下次 idle（`turn_input.rs:481-530`） | `queued_input` / `interrupted` / ctx 取消（已有） | 保留 |
| set/resume 触发 | 置 active 立即 `continue_if_idle`（`runtime.rs:226-241`、`thread_goal_processor.rs:250`） | `resume` 仅改状态；`/goal <objective>` 经 sendMessage 已触发 | P0：resume 后 kick 续跑 |
| 空响应容忍 | 自动续跑 turn 无活动且空/被中断：连续 3 次 → blocked（`accounting.rs:229-246`） | 无此概念：任何续跑轮都会继续消耗额度 | P1：3 连空响应/无进展 → blocked |
| 执行失败容忍 | 连续 3 个 turn 仅失败的内置 exec（无成功工具）→ blocked（`accounting.rs:121-173`） | 无 | P1：3 连全失败 → blocked（近似映射） |
| 真实错误 | 非取消类 turn 错误 → 立即 blocked（`runtime.rs:315-341`、`extension.rs:423-447`） | 错误恢复续跑 + 单次错误扣额度 | P0：3 连错误停（过渡）；P1：对齐立即 blocked |
| 用户中断 | TUI Esc/Ctrl+C → SetThreadGoalStatus(Paused)（`interaction.rs:686-697`） | `interrupted` 门（不置 paused） | 保留现状（不扩散改动面） |
| 预算 | token 预算（用户配置/建目标指定）+ 每轮记账；耗尽 → budget_limited + 收尾引导（`goals.rs:546-570`、`budget_limit.md`） | 字段存在无写点 | P2：记账 + 收尾 |
| 状态机 | active / paused / **blocked** / **usage_limited** / budget_limited / complete（`state/src/model/thread_goal.rs:12-21`） | active / paused / budget_limited / complete（`goal.go:23-28`） | P1：新增 blocked；usage_limited 暂缓 |
| 模型可写状态 | complete / blocked / paused（paused 仅在用户明确要求时；`tool.rs:252-259`、`spec.rs:60-83`） | 仅 complete（`capability.go:77,113-114`） | P1 保持仅 complete；模型写 blocked 列 P2 开放问题 |
| 续跑提示词 | 结构化模板（持久性/不缩目标/no-progress/完成审计/blocked 审计/update_goal 纪律；`steering.rs:53-94`） | 单段话（`chat_goal_auto_continue.go:15`）+ 独立注入的完成审计指引（`chat_session.go:2734-2767`） | P1：多段化对齐 |
| 预算提示 | `budget_limit.md` 收尾引导（每 goal 一次 steering） | 无 | P2 |
| 完成回执 | update_goal 成功回传预算报告（`tool.rs:529-538`） | `CompletionBudgetReport` 已实现（`capability.go:213-225`），但因无记账恒为空 | P2 记账后自然生效 |

---

## 4. 差异总表（现状 → 目标）

| 维度 | 本项目现状 | 本项目目标（对齐后） |
|---|---|---|
| 续跑次数 | 5 次/用户消息 | 默认不限；`goal.maxConsecutiveContinuations` 可配安全阀 |
| 失败续跑 | 任意错误扣 1 次额度 | P0：3 连错误停；P1：非取消错误 → blocked（立即） |
| 空转续跑 | 无识别 | 3 连空响应/无成功进展 → blocked |
| 停止可见性 | 仅上限提示；其余静默 | 终结性停止全部可见（原因+恢复方式） |
| resume | 仅改状态 | 立即续跑 |
| pending 回答后 | 不走续跑钩子 | 走统一续跑钩子 |
| 状态集 | active/paused/budget_limited/complete | + blocked（系统写、用户 resume 解除） |
| 提示词 | 单段 | 结构化多段（对齐 continuation.md 语义子集） |
| 预算 | 字段空转 | P2：记账 + budget_limited 收尾 |---

## 5. 设计

### 5.1 续跑驱动模型（P0 核心）

将 `maybeAutoContinueActiveGoal`（`chat_goal_auto_continue.go:25-80`）从"≤5 次 for 循环"改为"条件循环 + 停止规则矩阵"：

```
runGoalContinuationLoop(session):
  defer attempt 计数归零
  if ctx canceled → return（context_done）
  if executor 无 ContinueGoal 能力 → return（debug）
  loop:
    decision = shouldAutoContinueActiveGoalDecision(session)   # 现有 gate 保留
    if !decision.Continue → 归因（终态原因可见化）→ return
    if cap > 0 && attempts >= cap → 可见提示（可继续）→ return
    attempts++
    err, output = ContinueGoal(attemptCtx)                      # 一次完整隐藏 turn
    classifyTurn(err, output, toolActivity):
      - context.Canceled          → return（用户中断语义；不置 blocked）
      - err != nil                → errorStrikes++;  ≥3 → stopAction（P0 可见停止 / P1 blocked）
      - 无成功进展（output 空且无工具执行）→ noProgressStrikes++; ≥3 → stopAction
      - 其余                      → errorStrikes=0; noProgressStrikes=0（成功重置）
    # 循环回到顶部：下一轮 decision 会感知 complete / paused / blocked / queued_input 等变化
```

要点：

- **决策点收敛不变**：`shouldAutoContinueActiveGoalDecision`（:140-179）仍是唯一 gate；新增只作用于"额度与停止策略"。
- **上限语义变更**：`defaultGoalAutoContinuationLimit` 改名/保留为 **安全阀**（`0 = 不限制` 默认），取值为 `session.goalMaxContinuations`（由配置注入，§7）。
- **attempt 展示**：状态栏（`chat_interaction.go:3329-3343`）在 cap=0 时显示 `· attempt N`；cap>0 时显示 `· attempt N/M`。
- **停止规则矩阵**（P0 落地）：

| 条件 | 行为（P0） | 行为（P1 终态） |
|---|---|---|
| goal 变为 complete/paused/blocked | 停止（静默 debug，属正常收敛） | 同左 |
| queued_input / interrupted / ctx canceled | 让位停止（静默） | 同左 |
| 安全阀到顶（cap>0） | 停止 + 可见提示（可继续） | 同左 |
| 连续错误 ≥3 | 停止 + 可见提示 | 非取消真实错误 → 立即 blocked（见 §9 偏差说明） |
| 空响应/无进展 ≥3 | 停止 + 可见提示 | → blocked |
| 全失败执行 ≥3 | （并入"无进展"计数） | → blocked |

- **执行位置**：循环仍由 `sendMessage` 收尾调用（`chat_send.go:134,144,157`）；每次调用内部完成 N 轮直到触发停止规则。这样无需引入新的调度器/事件总线即可获得与 `continue_if_idle` 等价的"每轮重新评估"语义。

### 5.2 触发面补齐（P0）

- **T1 `/goal resume` 立即续跑**：
  - 结构化路径（`chat_command_result.go:1371-1380`）与 legacy 路径（`goal.go:41-46`）在"状态成功置为 active"后调用新 helper `kickGoalContinuationIfActive(session)`；
  - helper 条件：交互模式（`!NoInteractive`）、executor 可用、无进行中 turn、无排队输入；否则仅记 debug（由用户下一条消息兜底）；
  - 复用 `maybeAutoContinueActiveGoal`（改名后）同一入口，attempt/停止规则同一套。
- **T2 pending-question 回答后回到续跑钩子**：`handleRestoredPendingAnswerLine`（`chat.go:2142-2147`）在回答提交成功后调用同一 helper（回答提交本身不经过 `sendMessage`）。
- **T3 `/goal <objective>`**：现状已经由 `SendObjective → sendMessage` 触发续跑，保持不变。
- **T4 显式不做**：后台定时器/空闲轮询自动 kick（Codex 由 core idle 事件驱动；本项目以"每轮结束再评估 + resume kick + 既有入口"达到等价效果，避免新增调度面）。

### 5.3 稳健性与可见性（P0/P1）

- **计数器**：`consecutiveErrorStrikes`、`consecutiveNoProgressStrikes` 为循环内局部变量（不落盘、跨轮有效、结束即弃）；成功轮重置两者。
- **"无进展"判定输入**（P1，实现时按可得性取用）：
  1. 复用 turn 级工具执行计数（`chat_send.go:96-99` 的 reset/计数机制）；actor 续跑路径若未维护，则在 runtime event bridge 层补"本轮工具事件数"（实施时核实，作为 TODO）；
  2. `ContinueGoal` 返回的 `execOut` 是否非空（弱信号，兜底）。
  - 判定式：`无成功进展 = (output 为空) && (本轮成功工具执行数 == 0)`；仅"失败执行"也计入 noProgress（对应 Codex executon unavailable 的近似映射）。
- **可见化文案**（交互模式经 `RenderLocalSupplement` / 非交互 stdout；每次停止最多输出一次）：
  - 安全阀：`[goal] 已连续自动续跑 N 次（配置上限），目标仍为 active；发送任意消息或调高上限可继续。`
  - 3 连错误：`[goal] 连续 3 轮续跑失败，已停止自动续跑；目标保持 active。（P1：已置为 blocked，/goal resume 可恢复）`
  - 无进展：`[goal] 连续 3 轮无实质进展，已停止自动续跑。（P1：blocked）`
  - 工具不可用：`[goal] 自动续跑已禁用：当前模式无法调用 update_goal（goal 无法被自动完成）。检查工具面/policy，或手动 /goal complete。`
- **P1：`blocked` 状态**（对齐 Codex `thread_goal.rs:12-21` 的 blocked）：
  - `internal/goal`：新增 `StatusBlocked`；转换规则：`active→blocked`（仅系统）、`blocked→active`（仅用户 `/goal resume`）、`blocked→complete` 禁止（先 resume，与现有 paused 拒绝模型完成一致）；`clear` 任意状态可用。
  - 写入点：续跑停止策略（错误/无进展 3 连；P1 终态下真实错误立即）；不做模型可写（开放问题 §11）。
  - 展示：状态栏（`chat_interaction.go`）与 `/goal status` 展示 `blocked`；`/goal` 命令文档更新。
  - 恢复：`/goal resume` → active → T1 立即续跑。
- **保留**：所有非终结 reason 维持 debug 日志（`writeSessionDebugInfo`），避免噪音。

### 5.4 提示词对齐（P1）

`goalAutoContinuationPrompt`（`chat_goal_auto_continue.go:15`）升级为多段结构化文本（可拆为独立常量/模板文件），语义逐段映射 Codex `continuation.md`：

| Codex continuation.md 段落 | 本项目改写要点 |
|---|---|
| L1-3 任务与数据边界 | 保留（objective 为数据非指令；本仓已在 `renderActiveGoalGuidance` 有 `<untrusted_objective>` 包装，续跑 prompt 用同源表述） |
| L10-12 持久性/不缩目标 | 明确"跨轮持续；不得把目标缩小为更小/更易测试的子集" |
| L14-17 预算 | 仅当 `TokenBudget>0` 时渲染（P2 生效后）；当前可省略该段 |
| L19-20 证据优先 | "以当前工作区与外部状态为准，不依赖记忆" |
| L22-25 No-progress 分类 | 结合本仓：`write_stdin`/`job_*` 轮询是"verified wait"；观察超时不算终态，应重新轮询同一 handle；不要把状态复述/未执行计划算进展 |
| L27-28 进度可视化 | 多步工作时使用本仓 plan/`update_plan`（若工具面可用；不可用时跳过） |
| L30-33 保真 | "不允许因更容易通过而替换成更窄/更小的方案" |
| L35-46 完成审计 | 逐项要求→权威证据；不完整/间接证据视为未完成；"完成审计通过前不得宣称完成" |
| L48-54 阻塞审计 | 同一阻塞 ≥3 个连续 goal-turn 才可申报；P1 模型不可写 blocked → 改为"报告阻塞并保持 active"；P2 开放后恢复原语义 |
| L56 update_goal 纪律 | "仅在完成审计通过（或用户明确要求暂停）时调用；不因停止工作而调用" |

`renderActiveGoalGuidance`（`chat_session.go:2734-2767`）同步补充"不缩目标 / 阻塞 ≥3 连"两行，保持两处提示一致。

### 5.5 预算记账与 `budget_limited`（P2，接口先行）

- **记账来源**：会话 usage（turn 级 input/output token；子代理是否计入按本仓 usage 汇总口径裁定）。
- **记账点**：`sendMessage` 成功收尾与每次续跑 turn 收尾（`ContinueGoal` 返回后）累加 `goal.TokensUsed` 并落 metadata（节流写）。
- **阈值**：`TokensUsed >= TokenBudget`（`TokenBudget>0` 时）→ 置 `budget_limited` + 注入收尾引导（对齐 `budget_limit.md`："不再开新工作，收尾并给出下一步"）；续跑 decision 已天然拦截（非 active）。
- **预算来源**：配置 `goal.defaultTokenBudget`（默认 0=无）；`/goal budget <n>` 命令形态待定（开放问题）。
- **兼容**：`RemainingTokens` / `CompletionBudgetReport`（`capability.go:202-225`）已就绪，记账接通后 update_goal 回执自动携带预算报告。---

## 6. 改动清单（文件级）

| 文件 | 改动 | 阶段 |
|---|---|---|
| `backend/cmd/aicli/commands/chat_goal_auto_continue.go` | 条件循环、安全阀、strikes、可见化、提示词多段化 | P0/P1 |
| `backend/cmd/aicli/commands/chat_send.go` | 收尾钩子保持；（可选）抽取统一 `finishChatTurn` helper | P0 |
| `backend/cmd/aicli/commands/chat.go` | pending 回答路径接续跑；`goalMaxContinuations` 字段；attempt 归零逻辑不变 | P0 |
| `backend/cmd/aicli/commands/goal.go`、`chat_command_result.go` | resume/complete/clear 成功后 kick（仅 resume 需要） | P0 |
| `backend/cmd/aicli/commands/chat_session.go` | `renderActiveGoalGuidance` 补充条款 | P1 |
| `backend/internal/goal/goal.go` | `StatusBlocked` + 转换校验 + 展示文案 | P1 |
| `backend/internal/goal/capability.go` | blocked 相关拒绝文案（模型不可完成 blocked）；转换表更新 | P1 |
| `backend/cmd/aicli/commands/chat_interaction.go` | 状态栏 blocked/attempt 展示 | P0/P1 |
| `backend/internal/config/manager.go` | 新增 `goal` 配置节（maxConsecutiveContinuations；P2：defaultTokenBudget） | P0 |
| `backend/configs/runtime.yaml`、`runtime.win7.yaml` | 配置样例注释 | P0 |
| `backend/cmd/aicli/commands/chat_setup.go` | 配置 → session 注入 | P0 |
| `backend/cmd/aicli/commands/chat_goal_auto_continue_test.go` 等测试 | 新语义矩阵 + 旧断言更新（见 §8） | P0/P1 |
| `scripts/run-conpty-goal-e2e.ps1` | 新增 resume/续跑日志断言 | P0 |

> 注：以上行号引用基于 2026-10-14 工作区（含已提交 ae755c6d）；实施时先重读目标文件。

## 7. 配置与默认值

```yaml
# runtime.yaml（新增 goal 节；全部可选，缺省行为对齐 Codex）
goal:
  # 0 = 不限制（默认；对齐 Codex 的"active 即续"）。
  # 正整数 = 安全阀：每用户消息最多自动续跑 N 轮，到顶可见提示、目标保持 active。
  maxConsecutiveContinuations: 0
  # P2（预留）：默认 token 预算；0/缺省 = 无预算。
  # defaultTokenBudget: 0
```

- 默认 `0`（不限制）：与本方案对齐目标一致；风险与缓解见 §9。
- 解析与注入：`internal/config` → `ChatSession.goalMaxContinuations`（P0）。
- 兼容：未配置时行为 = 现状改造后的默认（不再 5 次截断）。

## 8. 测试计划

**单测（fake executor 确定性驱动，`chat_goal_auto_continue_test.go` 扩展）**

| # | 场景 | 断言 |
|---|---|---|
| 1 | 无上限：连续 8 轮成功续跑后 goal 才 complete | 8 轮全部执行；无上限提示（旧实现 5 轮截断的回归反转） |
| 2 | 第 3 轮将 goal 置 complete | 不再发起第 4 轮 |
| 3 | cap=3 配置 | 3 轮到顶 + 可见提示 + goal 保持 active |
| 4 | 错误 3 连 → 停；中途成功一轮重置计数 | P0：可见停止；P1：blocked |
| 5 | 空响应/无进展 3 连 → 停；有工具执行不判空 | P1：blocked；无进展判定正确 |
| 6 | `/goal resume` kick：交互模式触发一次循环；非交互不触发 | kick 条件与执行 |
| 7 | pending 回答后触发续跑循环 | 钩子被调用 |
| 8 | `update_goal_unavailable` | 可见提示一次，不刷屏 |
| 9 | ctx cancel / interrupt | 立即停（保持现有语义，不置 blocked） |
| 10 | 旧断言更新 | `chat_goal_auto_continue_test.go:99-130/225-244` 中"错误消耗 5 次数"的断言按新语义改写 |

**ConPTY e2e（`run-conpty-goal-e2e.ps1` 扩展，弱断言优先）**

- 既有 9 条断言保持；
- 新增：`/goal resume` 后出现续跑决策日志（`[goal] auto continuation attempt=1`）；
- 新增：连续续跑决策 ≥6 条（验证无 5 次截断；以决策日志计数，避免依赖模型具体行为）；
- 说明：强行为断言（轮数、strikes）放单测，e2e 只做管道/信号存在性验证。

**回归**

- `go test ./cmd/aicli/commands/`、`./internal/goal/`、`./internal/config/`；
- 抽查 `internal/chat`（actor continuation 路径不变）。

## 9. 风险与缓解

| 风险 | 缓解 |
|---|---|
| 无限续跑导致成本失控（cap=0 + 无预算） | 默认即 Codex 行为（用户可中断/暂停）；提供 `maxConsecutiveContinuations` 安全阀；strikes 约束空转；状态栏持续可见；文档提示可配置 |
| 与既有"错误恢复"语义冲突 | P0 采用 3 连错误停止（保留瞬态错误容错）；P1 对齐 main 的"真实错误→立即 blocked"时明确公告行为变更，并保留 context.Canceled 语义 |
| "无进展"判定在 actor 路径拿不到工具计数 | 实现备选：turn 计数复用 → bridge 事件计数 → `execOut` 弱判定；先弱判定 + TODO，不阻塞 P0 |
| resume kick 与用户输入竞争 | kick 前置检查（进行中 turn / 排队输入 / 非交互 → 跳过）；用户消息收尾兜底 |
| 外部工作流并发改动 | 本方案改动集中在 `cmd/aicli/commands/chat*` 与 `internal/goal`、`internal/config`，与 mesh/schedule 改动无文件交集；实施前重读目标文件 |
| 与 Codex 的受控偏差（记录在案） | ① P1 前错误采用 3 连而非立即 blocked（过渡）；② 用户中断不自动置 paused（保持现状，避免扩散）；③ 模型不可写 blocked（P1）；④ 无 idle 事件源，用"每轮结束再评估 + kick"等价替代 |

## 10. 里程碑与验收

| 里程碑 | 内容 | 验收 |
|---|---|---|
| **M1（P0）** | 条件循环+安全阀；错误 3 连停；resume kick；pending 收尾；`update_goal_unavailable`/上限可见化；config 注入 | 单测 1-4、6-9 通过；e2e 全绿；`maxConsecutiveContinuations` 生效 |
| **M2（P1）** | blocked 状态（系统写/用户 resume）；空响应/无进展 3 连；真实错误→blocked；提示词多段化；guidance 同步 | 单测 5、blocked 场景通过；`/goal status` 展示 blocked；resume 解除；10 完成 |
| **M3（P2）** | tokens_used 记账；budget_limited 收尾；默认预算配置 | 记账/阈值/收尾注入单测通过；update_goal 回执带预算报告 |

每阶段独立可交付、可提交、可回滚（配置默认值保证向后兼容）。

## 11. 开放问题（实施前可确认）

1. **安全阀默认值**：建议 `0`（完全对齐 Codex）；若要保守可改 `50`。文档默认取 0。
2. **模型写 blocked 是否开放**（Codex 允许，≥3 连门槛）：本方案 P1 不开放、列为 P2 候选。
3. **真实错误→blocked 的推进节奏**：M2 一步到位，或先观测一段时间 3 连错误策略的真实数据再切。
4. **`usage_limited` 是否引入**（供应商 429/配额）：建议观察需求后再定。
5. **`/goal budget` 命令形态**（P2）：`/goal budget <n>` vs 仅配置默认预算。

---

## 12. 实施记录

### M1（P0，2026-10-14 完成，待提交）

- **续跑循环条件化**：`chat_goal_auto_continue.go` 以条件循环替代 5 次硬上限；默认**不限制**（对齐 Codex，`goal.maxConsecutiveContinuations: 0`）；连续错误 **3 连**停止（成功一轮重置）；`context.Canceled` 立即停止（不计 strikes）。
- **可见化**：安全阀到顶提示（含继续方式）、3 连错误停止提示、`update_goal_unavailable`（每会话一次）统一经 `reportGoalAutoContinuationSupplement` 输出。
- **触发面补齐**：`/goal resume`（legacy `goal.go` 与结构化 `chat_command_result.go` 两条路径）经 `requestGoalContinuationKick` 落 flag，主循环开头 `maybeConsumeDeferredGoalKick` 消费（actor 未就绪时保留）；恢复态提问/审批作答经 `requestGoalContinuationKickAfterResumedAnswer` + 就绪 watcher（`wakeComposerRead` 唤醒，仅交互行编辑器模式）。
- **配置/注入**：`internal/config` 新增 `GoalConfig` + `runtime.yaml` / `runtime.win7.yaml` 样例；`prepareChatPersistence` → `ChatSession.goalMaxContinuations`。
- **状态栏**：不限制时 `· attempt N`，安全阀时 `· attempt N/M`。
- **测试**：单测 9 项全绿（unlimited-8 轮完成、cap=8、complete 停止、错误重试至完成、取消立即停、3 连错误停、kick 消费、kick 门禁、旧用例迁移）；`internal/config` 新用例；`run-conpty-goal-e2e.ps1` **10/10**（新增"默认无上限截断"断言）。
- **回归**：`cmd/aicli/commands` 仅外部 `/tools` catalog 用例失败（预先存在，外部工作流）；`internal/config`、`internal/goal` 全绿。
- **偏差说明**：脚本化 e2e 下的 resume-kick / 连续多轮续跑场景推迟到 M2——M1 尚无"无进展"容忍，脚本模式下残留 active goal 可能长时间不收敛；该场景当前由单测覆盖（`TestRequestAndConsumeGoalKickRunsContinuation` 等）。

### M2（P1，2026-10-14 完成，待提交）

- **`blocked` 状态**：`internal/goal` 新增 `StatusBlocked` 与转换（active→blocked 系统写；blocked→active 用户 resume；blocked→paused 拒绝；blocked→complete 允许用户、模型被 merge 守卫与 update_goal 工具双重拒绝，须先 resume）。
- **停止即置 blocked**：连续错误 3 连、连续空响应/无进展 3 连 → `markActiveGoalBlocked`（`MutationSystem` 持久化 + 会话状态恢复）+ 可见提示（`/goal resume` 解除）；置 blocked 失败时退化为"保持 active"的可见提示。
- **空响应/无进展判定**：`chatTurnToolExecutions` 前/后快照差值；本轮无文本产出且无工具执行才记 strike；成功轮/有工具执行重置；覆盖 CLI/shared 执行路径，计数不可得时按"有文本即非空转"保守处理。
- **提示词多段化**：`goalAutoContinuationPrompt` 对齐 `continuation.md` 语义子集（持久性/不缩目标/证据优先/verified-wait 判定/完成审计/update_goal 纪律）；`renderActiveGoalGuidance` 增补"不缩目标 / 阻塞复述"两行。
- **状态展示**：状态栏新增 `Goal blocked (/goal resume)`（compact=`Blocked`）。
- **受控偏差（保留并记录）**：真实错误采用 **3 连 → blocked**（Codex main 为立即 blocked）——保留瞬态故障容忍；后续可评估收紧。
- **测试**：commands 13 项定向全绿（含 `BlocksAfterThreeNoProgressRounds`、`NoProgressStrikesResetOnToolActivity`、`GoalStatusSegmentShowsBlocked`，错误 3 连用例改断言 blocked）；`internal/goal` 全绿（转换矩阵 + merge 守卫 + update_goal 拒绝 blocked）；全量命令包仅外部 `/tools` catalog 用例失败（预先存在）；`run-conpty-goal-e2e.ps1` **10/10**。

### M3（P2，2026-10-15 完成，待提交）

- **token 记账**：`accountActiveGoalTurnUsage` 按 goal id 维护会话 token 增量基线（restore 覆写后重锚，避免负增量丢记账），增量经 `MutationSystem` 落 `TokensUsed`；仅在 active / budget_limited 窗口累计。
- **预算来源**：`goal.defaultTokenBudget` 配置（新建 goal 缺省注入）+ 新增 `/goal budget [n]` 命令（查看 / 设置 / 清除，`n=0` 清除）；legacy 与结构化两条路径 + `--json` 兼容。
- **`budget_limited` 与收尾轮**：预算耗尽 → `markActiveGoalBudgetLimited`（系统写）+ `ContinueGoalWithPrompt(goalBudgetWrapUpPrompt)`（对齐 `budget_limit.md`：不再开新工作，只收尾给出下一步）+ 可见提示；`aicliGoalContinuationExecutor` 扩展 `ContinueGoalWithPrompt`（actor / shared / runtime-server 三实现 + fake）。
- **停止语义**：入口记账与每轮记账后均检查；恢复已超预算的目标立即收敛（不再空跑一轮）。
- **merge 守卫**：`budget_limited → active` 仅在预算已提高到超过已用时允许（对齐 Codex"必须提高预算"）；`/goal resume` 未提高预算时不复活。
- **测试**：`TestSendMessage_AccountsGoalUsageAndStopsAtBudget`（每轮 100 / 预算 250 → 2 轮正常 + 1 轮收尾、断言 wrap-up 提示词与 budget_limited）、预算命令三态（设置 / 未提高预算 resume 不复活 / 提高后恢复 / 清除）、非法与缺 goal、goal 包 merge 守卫矩阵；`run-conpty-goal-e2e.ps1` 扩展预算命令场景 → **11/11**。
- **回归**：`internal/goal`、`internal/config`、全量命令包（仅外部 `/tools` catalog 用例失败，预先存在）。

### M4（打磨批次，2026-10-15 完成，待提交）

- **模型写 blocked（对齐 Codex）**：`update_goal` 新增 `status=blocked`（仅允许从 active 发起；`summary` 作为阻塞说明落新字段 `BlockedReason`）；continuation 提示词与 `renderActiveGoalGuidance` 增加"同一阻塞 ≥3 连才可申报 blocked"审计条款；`/goal status` 展示 `Blocked reason`，resume / 其它状态变更 / 预算复活时清空。
- **预算可见性**：`renderActiveGoalGuidance` 在配置预算时逐轮注入 `Token budget: used / budget (remaining)`；耗尽语义提示"wrap up instead of starting new work"。
- **帮助与补全**：`/goal` catalog Usage 与参数表、参数补全候选新增 `budget`（`/goal [status|clear|pause|resume|complete|budget|<objective>]`）。
- **环境变量安全阀**：`AICLI_GOAL_MAX_CONTINUATIONS`（runtime.yaml 未显式配置时的覆盖；正整数生效，对齐 `AICLI_TURN_AUTO_RETRY_LIMIT` 先例）——支撑 e2e 把"不限制"收敛为有界多轮场景。
- **e2e 场景二**：等待型目标（不完成、不工具）多轮续跑有界（安全阀=2）→ 上限提示；`/goal resume` 后 kick 触发新一轮 → 断言"首轮 ≤2 次 + resume 后 ≥1 次 = ≥3 次 attempt"；实测 attempt 行=4（2+2），达到上限提示两次，**16/16 断言全绿**。
- **测试**：`internal/goal` 新增 blocked 写入成功（状态+BlockedReason 落库）与非 active 拒绝；commands 新增 env 回退用例（显式配置优先 / 非法值回落不限制）；补全断言纳入 `budget`。
- **回归**：`internal/goal`、commands 定向全绿；全量命令包仅外部 `/tools` catalog 用例失败（预先存在）。

### 验收补强：ConPTY 交互式实测（2026-10-15，完成待提交）

- 新增驱动工具 `backend/cmd/goal-conpty-driver`：真实 ConPTY（Win32 `CreatePseudoConsole`，复用 `conpty-probe` 管线）驱动 `aicli chat` **交互式 TUI**，按步骤注入真实控制字符（CR）并等待输出标记（PTY 滚动窗口 + `debug.log` + transcript 多源）；支持"忙时命令确认门"自动应答。补齐 `run-conpty-goal-e2e.ps1`（script/headless 路径）之外的**行编辑器路径**（含 resume kick 的 `wakeComposerRead` watcher）。
- 实测（`AICLI_GOAL_MAX_CONTINUATIONS=2`，等待型目标）：启动 1s → 设置目标 → **首轮 2 次续跑 + 上限提示** → pause（摘要展示 `Tokens: 78457`，M3 记账实测生效）→ resume → **kick 触发第二轮 2 次续跑 + 上限提示**（debug：attempt 行=4、cap 行=2）→ clear → `/exit` 干净退出（exit=0）。驱动 **9/9 步骤 PASS**。
- 实测备注：① 交互式 TUI 空闲时**不重绘**属正常（此前"加载静止"疑点排除）；② 忙时输入 slash 命令会进入既有"Phase A 确认门（y/N）"，非缺陷；驱动已按此适配。

---

### runtime-server 缺口修复：逐轮 goal 指引注入（gap 1，2026-10-15 完成）

- **缺口**：runtime-server actor 只在构建期装配 agent（`PrepareAgent` 仅在 `BuildSessionActor` 调用一次），没有 run 级刷新，因此服务端 turn 拿不到 `active_goal_guidance`（CLI 本地 actor 由 `localChatPrepareRunHook` 每轮注入）。影响：服务端普通 turn 缺 objective 包裹 / 完成审计模板 / 预算行 / blocked 条款（续跑轮由 CLI 侧多段 prompt 兜底，模型也可 `get_goal` 自取）。
- **修复**：
  - `internal/goal`：抽出共享渲染器 `RenderTurnGuidance` / `EscapePromptText` / `GuidanceOptionKey`；CLI 本地路径（`renderActiveGoalGuidance`）改为调用同一实现，文案逐字不变。
  - `internal/runtimehost`：新增 `Deps.OnRunPrepare`（在 `PrepareRun` / 默认取租约**之后**追加执行），并抽出 `composePrepareRun` 固化「先租约、后宿主刷新」顺序契约（宿主无需自行管理租约；钩子报错与租约失败同口径终止 run）。
  - `internal/api/runtimeapi`：`SessionDeps` 捕获 `preparedAgent` 并装配 `OnRunPrepare`，每次 run 前按**实时会话**（run 时 `loadSession` 的结果）刷新 `active_goal_guidance`——`/goal` 的 set/pause/resume/clear/complete 无需重建 actor 即在下一轮生效；无 goal / 非 active / 元数据损坏时清除该 option 或降级（fail-open，不让 goal 读取失败终止 turn）。
  - 与 CLI 同口径限定**基础会话**（`isBaseSessionContext`：无 agent_type / depth / read_only）——子代理会话不继承父会话的持久目标。
- **测试**：`internal/goal` 渲染器（active / 非 active / 空 objective / 预算夹紧 / 转义 / update_goal 可用性两分支）；`internal/runtimehost` 顺序契约（先租约后宿主刷新、租约失败不执行刷新、`PrepareRun` 覆盖优先、空钩子直通）；`internal/api/runtimeapi` 注入单测 + `buildSessionActor → SubmitPrompt` **端到端**（首轮含 objective/审计/update_goal；改目标后第二轮无需重建 actor 即刷新）+ `isBaseSessionContext` 门控。
- **验证**：`go build ./...`、`go test ./internal/goal/... ./internal/runtimehost/...`、`go test ./internal/api/runtimeapi/`（全量）全绿；commands 包 goal/parity/freeze/runtime-server 子集全绿。
- **后续**：gap 2（web/控制台 goal 管理 HTTP 面）见下节。

### web/控制台 goal 管理 HTTP 面（gap 2，2026-10-15 完成）

- **端点**：`GET /sessions/{id}/goal`（读取当前 goal + remaining_tokens；无 goal 返回 200 + `goal=null`）、`POST /sessions/{id}/goal`（`{"action":"set|pause|resume|complete|clear|budget","objective":...,"token_budget":N,"summary":...}`，响应含 `applied`/`message`/`goal`）。
- **同口径**：动作与 CLI `/goal` 一一对应；校验、状态机与 `GoalMergePolicy(actor=user)` 全部复用 `internal/goal`（complete 终态、`budget_limited→active` 仅在预算提高到超过已用时成立、用户可完成 paused/blocked 目标）；`set` 的缺省预算取服务端 `goal.defaultTokenBudget`。
- **共享**：新增 `internal/goal.ApplyUserStatusUpdate`（完成字段 / 阻塞原因清理），CLI `updateGoalStatusCommand` 改为共用同一实现，避免两条路径的字段规则漂移。
- **授权**：写端点沿用会话写授权（回环 / admin token / admin role）→ 非回环无凭证 403；GET 为只读（与 plan mode 读端点同级，不额外设限）。
- **边界（如实登记）**：本端点只管理**状态**，不触发续跑——goal 的自动续跑循环当前由 CLI 驱动（`aicliGoalContinuationExecutor`），纯 web 会话不会自行续跑；控制台可在 resume 后按需投递一条消息。服务端 auto-continuation 是独立议题。
- **提交**：`03ec0a26`。`runtimeapi/handler.go` 的 4 行路由以**部分暂存**（`git apply --cached` 单 hunk）提交，未卷入并行 schedule/mesh/bots 工作流在该文件里的未提交改动。
- **测试**：`internal/goal` 状态规范化单测；HTTP 面生命周期（set→get→pause→resume→budget→complete→clear）；校验矩阵（缺 action / 未知 action / 缺 objective / 负预算 / 无 goal 的 pause|resume|complete|budget / 非法 JSON / 未知会话 404）；complete 终态（含用新 objective 替换）；budget_limited 守卫（未提高预算 resume 不复活 → 提高后自动 active）；非回环无凭证 403。

#### 前端接线（控制台 goal 面板，2026-10-15 完成）

- **共享 API**：`frontend/src/api/runtime/goal.ts`——`fetchSessionGoal(sessionId)` / `updateSessionGoal(sessionId, payload)`，复用 `shared.ts` 的 `fetchRuntimeJson`/`buildRuntimeUrl`（路径 ID 编码、404 归一为 `RuntimeApiError`）与 session-goal 解析器 `parseSessionGoalEnvelope`（工具结果与 REST 信封同口径解析）。
- **Hook**：`frontend/src/hooks/workspace/use-runtime-goal.ts`——`useRuntimeSessionGoal`：挂载时 GET 一次作为**权威快照**，暴露 `run(action, payload)` 写动作；sessionId 切换保护（响应到达时若会话已切换则丢弃，不污染新会话视图）；`applied=false`（GoalMergePolicy 守卫保留原状态）透传为 notice，不静默不清空。
- **组件**：`frontend/src/components/workspace/session-goal-indicator.tsx` 重写——SSE 观测与权威快照按时间戳取新（不双写）；相位标签 + objective + token 用量；按相位渲染有效动作按钮（pause/resume/complete/clear），动作后以服务端响应为准刷新；`data-testid: topbar-session-goal*`。挂载点 `workspace-shell-topbar.tsx:214` 传 `selectedThread.sessionId`，组件签名不变（`{sessionId?}`）。
- **store/derive**：`lib/session-goal/store.ts` 新增 `setAuthoritativeSessionGoal` / `isSessionGoalAuthoritative` / `useSessionGoalAuthoritative`（权威快照 vs SSE 观测按时间戳取新）；`lib/session-goal/derive.ts` 抽出 `parseSessionGoalEnvelope` 供 REST 复用。
- **i18n**：zh-CN/en-US `workspace/base.ts`——derivedNote 措辞更新 + authoritativeNote（权威快照来源说明）+ 四动作标签；`verify-frontend-i18n` 1001 资源 0 违规。
- **测试与验证**：`api/runtime/goal.test.ts` 7 例（路径编码 / 信封解析 / POST snake_case 请求体 / applied=false 透传 / clear→absent / 404→RuntimeApiError）；`session-goal.test.ts` 追加权威快照 5 例；组件测试 7 例——goal 相关 33/33 通过；全量 `npx vitest run` 392 文件 / 3287 例通过；`npx tsc -b` 无本特性错误；eslint（本特性 7 文件）0 错误；indicator 重写后约 190 行，`verify-max-lines` 达标（脚本现存 4 个超限文件均为其他工作流在途文件，非本特性引入）。
- **边界（如实登记）**：控制台动作按钮只调用状态端点，与后端同边界——不触发续跑；resume 后仍需 CLI 续跑循环或消息投递驱动下一轮。组件级动作交互的端到端渲染测试为可选后续。**（gap 3 落地后此边界已被服务端驱动器覆盖：开关开启时 set/resume/budget-raise applied=true 即触发服务端续跑，见下节。）**

### 服务端 auto-continuation（gap 3，2026-10-15 完成）

- **动因**：gap 2 收尾时确认的结构性错位——turn 终态发生在 runtime-server 的 actor 里，而续跑决策循环整个住在 CLI 进程；纯 web/connector/schedule 会话没有"turn 结束后仍在场"的驱动者，goal 停留在状态管理（面板有 resume 按钮但没有下一轮）。Codex main 的循环住在宿主进程（`on_thread_idle`），对齐语义要求"runtime 在哪个进程，循环就在哪个进程"。
- **共享策略层**：`internal/goal/continuation.go`——`ContinuationPrompt` / `BudgetWrapUpPrompt`（与 CLI 字面量**逐字节一致**：`chat_skill_mentions` 按整串相等识别系统生成的续跑提示）、`ContinuationErrorStrikeLimit` / `ContinuationNoProgressStrikeLimit`（×3）、`ContinuationCLICooldownWindow`（5 分钟）、`EvaluateGoalContinuation` / `BudgetExhausted` / `SystemBlockedReason`。CLI `chat_goal_auto_continue.go` 的常量改为引用共享层（包内别名，100+ 调用点零改动）。
- **驱动器**：`runtimeapi/session_goal_driver.go`——订阅总线 `session_end`（actor 路径终态：connectors/schedule/CLI 远程/团队）+ web 直连成功终态显式上报（`/api/agent/chat` 的 SSE 终态不进总线，流式/非流式两个成功分支调用 `observeWebTerminal`）。链路：门控 → strike 记账 → 记账 → goal 态判定 → `SubmitPromptAsync(goal.ContinuationPrompt)` 提交续跑轮（run_meta.source=`driver:goal` 归因）。
- **门控矩阵**：配置开关 `goal.serverAutoContinuation`（缺省 false = 行为零变化）；CLI 冷却窗让位——命令通道 submit_prompt/continue 入口是 CLI 远程模式在场的直接证据（每轮必经），观测到即刷新 5 分钟窗口并清 strike，**让位判定先于 strike 记账**（CLI 轮不污染服务端计数）；基础会话门控（`isBaseSessionContext`，与 gap-1 注入同口径）；挂起审批/工具/提问时不续跑。
- **kick-on-set/resume**：goal 端点 set/resume/budget-raise **applied=true** 时调用 `goalDriverKick`（对齐 Codex `:226-241` 的 set/resume 触发语义）——空闲 web 会话没有任何终态事件，kick 是唯一唤醒路径。开关关闭或 CLI 在场时 kick 内部让位，行为同 gap-2。
- **记账与容忍**：usage 按轮入 goal（`usageTotalTokens`：TotalTokens 优先，缺省退化 prompt+completion）；超预算置 budget_limited 并跑一次收尾轮（共享 `BudgetWrapUpPrompt`，对齐 CLI wrap-up）；连续错误 ×3 / 连续空转 ×3（近似：steps≤1 且无输出——服务端终态载荷无工具计数，与 CLI 的"无文本无工具"口径的偏差已登记）→ 置 blocked（`SystemBlockedReason` 文案与 CLI 同源）。
- **提交**：`f3b96291`（共享层）/ `4a5073f7`（配置开关 + RunMeta.Source）/ `5d1fe3e0`（驱动器）/ `cebf95aa`（CLI 别名化）/ `44bdde8e`（runtime 配置启用，win7 覆盖档同步）。
- **测试**：`internal/goal/continuation_test.go` 8 例（决策矩阵 / 预算判定 / prompt 锚点 / blocked 文案 / 冷却窗常量）；`session_goal_driver_test.go` 5 例（开关缺省关闭 / CLI 让位先于 strike / 阈值同源 / noteCLI 刷新 / usage 回退口径）。`go build ./internal/... ./cmd/aicli/...` 全绿；goal/config/team 包与 commands 子集（Goal/SkillMention）全绿。
- **边界（如实登记）**：①真机端到端（开关开启后 web 会话 set→自动续跑）本轮未完成——runtime-server 在本机启动后卡在 MCP 初始化之后 4 分钟以上未监听（上次 gap2 验收同命令约 55s 就绪，环境差异非代码回归；`go build`/单测均绿），待环境恢复后补跑；②空转判定是近似（steps≤1），工具计数在服务端终态载荷中不可得；③CLI 本地模式的循环不变（那些会话没有 server）；④双驱动依赖命令通道入口作为 CLI 在场信号，CLI 直连 actor 模式（不经 runtime-server）的会话不受服务端驱动器影响（无 actor）。

---

## 附录 A：Codex main 关键证据索引

- 续跑：`codex-rs/ext/goal/src/runtime.rs:436-535`（continue_if_idle）、`:226-241`（set/resume 触发）、`:490-503`（turn 血缘）
- 生命周期：`extension.rs:180-203`（on_thread_idle）、`:314-378`（turn stop 记账）、`:423-447`（stop reason 映射）
- 容忍阈值：`accounting.rs:229-246`（空响应×3）、`:121-173`（exec 失败×3）、`:676-691`（active 清理）
- 提示词：`steering.rs:53-94` + `ext/goal/templates/goals/continuation.md`、`budget_limit.md`
- 状态机：`state/src/model/thread_goal.rs:12-21`；`state/src/runtime/goals.rs:271-470`（转换）、`:546-570`（预算记账）
- 工具：`ext/goal/src/tool.rs:204-318`（create/update_goal）；`spec.rs:35-83`（描述与门槛）
- 基线：`main@cc7ba33601`；未合入分支（watchdog/guardian/capacity-retry/exec-resume）明确不在对齐范围

## 附录 B：本项目关键证据索引

- 续跑循环与 gate：`backend/cmd/aicli/commands/chat_goal_auto_continue.go:14-15,25-80,140-179,197-211`
- 触发钩子：`backend/cmd/aicli/commands/chat_send.go:96-99,134,141-152,157-159`
- 命令入口：`backend/cmd/aicli/commands/goal.go:41-46`；`chat_command_result.go:1371-1380`
- pending 路径：`backend/cmd/aicli/commands/chat.go:2142-2147`
- 状态模型：`backend/internal/goal/goal.go:23-28,30-43`；`capability.go:77,108-167,227-248`
- 状态栏：`backend/cmd/aicli/commands/chat_interaction.go:3288-3343`
- 目标指引注入：`chat_session.go:2734-2767`；`chat_actor_host.go:2345-2350,2577-2585`
- 工具可用性：`chat_tool_availability.go:13-92`；`goal_actor_tools.go:12-25`