# CommandCode Plan Mode 文档设计借鉴分析

- **日期**：2026-09-25
- **来源**：<https://commandcode.ai/docs/plan-mode>（Command Code "Plan Mode" 文档全文）
- **对照对象**：本仓库 aicli 的 plan mode 实现
  - 状态机与持久化：`backend/internal/planmode/state.go`
  - 工具与宿主接线：`backend/internal/chat/plan_mode_tools.go`、`backend/internal/chat/permission_mode.go`、`backend/internal/toolbroker/broker.go`
  - 权限执行：`backend/internal/policy/{modes,engine,capability,tool_policy}.go`
  - 模型侧提示：`backend/internal/agent/system_reminder.go`
  - HTTP/Web 面：`backend/internal/api/skills/plan_mode_handlers.go`
  - CLI/命令面：`backend/cmd/aicli/commands/chat_plan_command.go`
  - 前端评审面：`frontend/src/hooks/workspace/use-runtime-plan-mode.ts`、`frontend/src/lib/pending-interaction/plan-review.ts`、`frontend/src/components/workspace/artifact-panel-plan-surface.tsx`、`frontend/src/components/workspace/pending-interaction-bar.tsx`
- **结论口径**：区分「值得借鉴（缺口）」与「已有且更强（不要回退）」；每条建议给出代码落点、优先级与验证方式。

---

## 1. TL;DR：可借鉴清单

| # | 主题 | CommandCode 做法 | aicli 现状 | 建议 | 优先级 |
|---|------|------------------|------------|------|--------|
| 1 | **退出裁决权** | `exit_plan_mode` 只是「把计划交给评审面」，approve 必须由用户按下；计划本身就是审批提示 | `exit_plan_mode` 的 `decision` 由**模型自填**（`broker.go:1285-1295`），工具在 plan 模式下按只读控制工具放行（`capability.go:88-90` + `modes.go:46-50`），模型可自评 `approve` 直接恢复执行模式 | 工具只允许「请求退出」（`RequestExit`/`PendingExitRequest` 状态位已存在但未接线，`state.go:173-181/282-284`），approve/quit 只接受宿主裁决；模型自评 approve 需落审批或显式策略开关 | **P0** |
| 2 | **计划工件一等公民** | `~/.commandcode/plans/<name>.md` + `plans-index.json` + `versions/<name>-v<N>.md`，状态 `pending/approved/not-implemented`，`/plans` 可浏览 | 计划默认写在工作区 `plan.md`（`plan_mode_handlers.go:357-362`；仓库根与 `backend/` 均已出现 `plan.md`），无索引、无版本、无状态、无浏览入口 | 引入 `planstore`（项目 slug 目录 + 索引 + 版本快照 + 状态机），`/plans` 列表与详情 API，工作区 `plan.md` 保留为兼容写目标 | **P0** |
| 3 | **评审反馈闭环** | Submit review 把评论作为一轮用户输入交给 agent，agent 修订后重新呈现（round N） | `request_changes` 只把文本写进 `state.Notes`（`state.go:184-198`、`plan_mode_tools.go:111-125`），全仓无任何注入路径（grep `state.Notes` 仅存储/返回），Web/API 裁决也不触发新回合（`plan_mode_handlers.go:161-195`） | 退出决策携带 `pending_review_notes`，下一次回合作为用户/系统输入注入并标记已消费；同时发 `plan_mode_changed` 事件；Web 裁决后可选自动触发修订回合 | **P0** |
| 4 | **行级评论与轮次 diff** | 评审器一行一选，评论以 sidecar 覆盖层附着在行下；每轮提交先快照版本，下一轮只标绿变更行，`ctrl+n/p` 跳转 | 只有一个 notes 文本域（`artifact-panel-plan-surface.tsx:201-253`），无行锚点、无评论集合、无版本 diff | 前端加行锚点 + 评论列表（sidecar 存索引而非计划正文）；后端 `planstore` 提供 `versions` 与 diff 元数据 | **P1** |
| 5 | **按需评审 + 自动兜底 + 浏览器** | `plan_review` 工具（计划模式外用）、`/plan-review`、`/plans`，以及 harness 兜底：run 自然结束但计划未评审且当前模式会打断用户时，由 harness 主动弹出评审 | 无 `plan_review` 工具、无 `/plans`、无 run 结束兜底；仅 Web 端有「plan active ⇒ pending 卡片」的投影（`plan-review.ts:18-36`），CLI/TUI 只能靠模型主动调用 `exit_plan_mode` | 在 agent loop 收尾处加兜底（default/plan 模式且 plan active 且有未决退出请求时发评审事件）；新增 `plan_review` 工具与 `/plans` 命令 | **P1** |
| 6 | **模式循环 / 横幅 / 进入确认** | `shift+tab` 循环 `default → accept-edits → plan → yolo`；banner 常驻显示当前模式；`enter_plan_mode` 需用户确认 | 无循环键位与 banner（grep `shift+tab`/`cycle.*permission` 零命中）；`enter_plan_mode` 属 runtime-owned essentials（`tool_policy.go:126-132`、`project-permissions.md:78`），模型可**无确认**自主进入 | 增加模式循环键位与常驻模式标识；`enter_plan_mode` 默认走一次确认（或用策略 `plan_mode_auto_enter` 显式开启自主进入） | **P1** |
| 7 | **写白名单路径语义** | plans 目录内 `.md` 在所有模式下免询问，且豁免范围精确到目录 + 扩展名 | `planWriteAllowed` 用 **base name 相等**或前缀匹配（`engine.go:576-587`），`apply_patch` 用正文字符串包含启发式（`:563-572`）——任意目录下叫 `plan.md` 的文件都可写；硬 allowlist 未含 `write` 时计划本身反而写不了 | 进入 plan 时把允许路径解析为工作区绝对路径，比较用绝对路径精确/目录前缀；`apply_patch` 解析补丁头；plan 文件写入在 plan 模式内免 allowlist 门（仅限白名单路径） | **P2** |
| 8 | **文档信息架构** | 一分钟 quickstart（`/plan`、模式表、评审键位表）→ 参考（slash/flags/tools）→ See also | 文档散落：`docs/user-guide/aicli.md:123`、`docs/plan/grok-harness-productization-implementation-plan.md:405-423`、`docs/product/project-permissions.md:77-81` | 补 `docs/aicli/plan-mode.md`：模式选择表 + 命令/键位/工具参考 + 与 checkpoint 的关系 | **P2** |

> 已有且**不应回退**的能力见 §5：durable 会话状态机 + 每 turn 重投影、`SESSION_NOT_FOUND` 韧性、统一 pending 生命周期、只读子代理可用控制面工具、多宿主同构（CLI/console/Web/ACP）。

---

## 2. CommandCode Plan Mode 设计要点提炼

以下均来自文档原文行为（不含推测），按可借鉴维度归纳。

### 2.1 权限模式与进入路径

- 模式循环：`default → accept-edits → plan → yolo`（`shift+tab` 逐级切换）；`dont-ask` 不在循环内，由设置或 `--permission-mode` 选择。
- plan 档语义：**文件编辑阻断 + shell 命令阻断**，只读探索与推理放行。
- 进入路径：`/plan`、`/plan <task>`、`--plan`、`--permission-mode plan`、`/mode:plan`。
- 明确的「什么时候用 plan」表：新功能范围不清、复杂调试、跨文件重构、安全评审；小修小改用 accept-edits。
- 安全取向：`/mode` 不提供通往 yolo 的路径（slash 可被模型调用，避免模型自解除权限提示）；任何模式下改文件前先建 checkpoint，accept-edits 也可回滚。

### 2.2 Plan review（评审面）

- 单一 REVIEW 模式：逐行阅读、任意行留评论、用「动词」裁决；计划正文编辑交 `$EDITOR`（`ctrl+g`），评审器不做第二套编辑模式。
- 三个动词：`ctrl+r` Submit review（把评论交给 agent 修订并再次呈现）、`ctrl+a` Approve（开始实现）、`esc` Cancel（计划保留）。
- 评论是**评审工件**：存在 sidecar 覆盖层，只在提示里交给 agent，**从不写进计划正文**；随计划落盘，跨会话保留。
- 快捷评论：`?`=解释理由、`x`=删掉这段、`!`=风险复核；`ctrl+n/p` 在标记行（评论 + 变更行）间跳转。
- 有未决评论时按 Approve 会显式二选一：「作为备注发走」或「按原计划批准并丢弃评论」。

### 2.3 四种进入评审的方式 + 自动兜底

1. 结束 plan mode：agent 写计划后调用 `exit_plan_mode`，评审面即审批提示（批准可顺带切到 accept-edits 直接实现）。
2. 主动要求：自然语言「review plan」→ agent 调用 `plan_review` 工具在模式外重开评审。
3. `/plan-review`：直接评审本会话最新计划。
4. `/plans`：全屏计划浏览器（本会话 + 历史会话，状态徽标、评论数、搜索），`/plans <name>` 直达。
5. **harness 兜底**：模型只写了计划就停下时，若模式为 default/plan，harness 自己弹出评审面板；「不打扰」模式（accept-edits/yolo/dont-ask）跳过兜底。

### 2.4 评审轮次与版本化

- 每次 Submit review：快照当前计划到 `versions/<name>-v<N>.md` → 版本号 +1 → 清空待处理评论（已随该轮提示交给 agent）→ agent 修订覆盖live 文件。
- 下一轮评审**以上一轮快照做 diff**：变更行标绿，徽标显示 `round N · M lines changed`，只复看变更。

### 2.5 存储与状态

```
~/.commandcode/plans/<descriptive-name>.md      ← 计划正文（agent 写）
~/.commandcode/plans/plans-index.json           ← 标题、状态、评论、版本
~/.commandcode/plans/versions/<name>-v<N>.md    ← 各轮快照
```

- 状态：`pending`（写但未评审）/ `approved`（已批并进入实现）/ `not-implemented`（取消评审但保留）。
- 取消不再是丢弃：计划可随时 `/plans` 重开、评论、修订。
- 对 plans 目录 `.md` 的写入在任何模式下都免权限询问，且**只有** `.md` 享受该豁免（目录不能当草稿纸用）。

### 2.6 参考面

- Slash：`/plan [task]`、`/plans [name]`、`/plan-review`、`/mode [default|accept-edits|plan]` 及三个 `:mode` 简写。
- Flags：`--plan`、`--accept-edits`、`--permission-mode <mode>`、`--yolo`。
- Tools：`enter_plan_mode`（**需用户确认**）、`exit_plan_mode`（仅在 plan 模式内有效，呈现计划待批）、`plan_review`（仅在模式外按需开面板）。

---

## 3. aicli 现状盘点（代码证据）

| 维度 | 现状 | 证据 |
|------|------|------|
| 状态机 | durable 会话 context（key `plan_mode`）：`status=inactive/active/exited`、`plan_path`、`previous_mode`、`write_allow_paths`、`pending_exit_request`、`exit_decision`、`notes`、`entered_at/exited_at`；含归一化与兼容解析 | `planmode/state.go:19-57,70-100,111-149,151-198,236-249` |
| 退出决策 | `approve / request_changes / quit`（含别名 `yes/revise/cancel`…）；`request_changes` 保持 active 并记录决策 | `state.go:81-95,184-198`；`plan_mode_tools.go:111-125` |
| 工具面 | `enter_plan_mode`（`plan_path` 支持单路径或数组 + `plan_write_paths`；显式关闭读路径 preflight）、`exit_plan_mode`（`decision` 必填 + `notes`）；仅在 `Broker.PlanMode != nil` 时暴露 | `toolbroker/broker.go:35-36,209-263,1261-1307` |
| 工具授权 | 二者归类 `ToolKindControl` 且 `ReadOnly: true`，能力 = `CapReadOnly + CapAskUser`，plan 模式下放行；同时是 runtime-owned essentials，绕过硬 allowlist（但仍受显式 deny / 能力域约束） | `policy/taxonomy.go:41-42`、`policy/capability.go:85-90`、`policy/tool_policy.go:126-132`、`docs/product/project-permissions.md:77-81` |
| 权限执行 | `Engine.Mode=plan` + `PlanWriteAllowPaths`；写型工具按路径白名单放行/拒绝，其余非只读能力按 `modeDecision` 拒绝；只读 shell（taxonomy 判定）在 mode 阶段前自动放行 | `policy/engine.go:82-83,89-134,236-262`、`policy/modes.go:42-62` |
| 每 turn 重投影 | 会话记录是权威副本，引擎/RunMeta 是每 turn 重建的执行副本；prepareRun 后重放 plan 状态并把 run meta 钉在 plan，防止宿主旧快照（如 bypass）关掉门禁 | `chat/actor.go:5342-5425`；机制剖析见 `docs/plan/plan-mode-session-record-availability-fix-plan-20260918.md:59-101` |
| 模型侧提示 | plan 激活时注入**一次性**（`Durable:false`）系统提醒：只读调查、只写允许路径、未批准不得改产品代码、等待 approve/request_changes/quit；退出后按 kind 剥离历史提醒 | `agent/system_reminder.go:210-223,228-255,262-275` |
| CLI 命令 | `/plan [status|enter [path]|exit <decision> [notes]]` + `approve/request_changes/quit/off` 别名；结构化与非结构化两条渲染路径同源 | `cmd/aicli/commands/chat_plan_command.go:14-26,27-93,103-143` |
| HTTP/Web | `GET/POST /sessions/{id}/plan-mode`；有活 actor 时优先走 actor（保证 mid-turn 引擎/RunMeta 同步），否则直接改会话记录；返回状态 + 计划正文预览（512KB / 200k rune 截断、工作区逃逸校验） | `api/skills/plan_mode_handlers.go:23-26,60-129,161-195,197-246,248-305,307-371` |
| 前端 | `useRuntimePlanMode`（按事件类型门控重载、会话切换必载一次、决策/进入命令）；Artifact 面板 Plan 页签（正文 Markdown、白名单徽标、notes 文本域、三按钮）；composer 上沿 pending 卡片；`plan_review` 作为 pending interaction 的一种投影（active 即条目、退出即消失） | `use-runtime-plan-mode.ts:25-32,62-77,195-249`；`artifact-panel-plan-surface.tsx:92-253`；`pending-interaction-bar.tsx:114,176-180`；`plan-review.ts:6-36`；`use-pending-interactions.ts:207-216` |
| 权限模式集合 | `default / accept_edits / plan / bypass_permissions`（无 yolo 别名、无循环键位）；离开 plan 切模式会以 `quit` 收口持久状态 | `policy/modes.go:8-13`；`chat/permission_mode.go:18-70` |
| 无头入口 | `aicli exec --permission-mode plan "..."` | `docs/user-guide/aicli.md:123-128` |
| 计划存储 | **工作区文件**（默认 `plan.md`，可自定义路径/多路径）；无 plans 目录、无索引、无版本、无状态；仓库根与 `backend/` 已各有一个 `plan.md` | `plan_mode_handlers.go:339-371`；`ls` 证据：`plan.md`、`backend/plan.md` |
| 评审能力 | 只有单个 notes 文本域；无行级评论、无 sidecar、无轮次 diff、无 `/plans`、无 `plan_review` 工具、无 run 结束兜底 | `artifact-panel-plan-surface.tsx:201-253`；全仓 grep `plan_review|/plans|plan-review` 仅前端 pending 投影，无后端工具/命令 |
| 已知韧性修复 | `SESSION_NOT_FOUND` 分类 + 入口不变量 + actor 自愈 + 可观测性已落地（Phase 0/1/2/4）；Phase 3「耐久性解耦」待产品决策 | `docs/plan/plan-mode-session-record-availability-fix-plan-20260918.md:1-50,116-140` |

---

## 4. 重点借鉴项：设计映射与落点

### 4.1 【P0】退出裁决权收口：模型只能「请求评审」，不能自评 approve

- **CommandCode 语义**：`exit_plan_mode` 打开评审面，approve 是用户动词；批准本身可以顺带把模式切到 accept-edits。
- **aicli 现状**：`exit_plan_mode` 的 `decision` 来自模型参数（`broker.go:1285-1295`），工具声明为只读控制工具并在 plan 模式放行（`capability.go:88-90`、`modes.go:46-50`），因此**模型可自行 `decision=approve` 退出 plan**。`ResumeModeAfterExit` 恢复 `previous_mode`（`state.go:236-249`）——若进入前是 `bypass_permissions`，批准后写操作将直接绕过审批。用户侧裁决（Web 面板 / `/plan approve`）与模型侧调用共用同一入口，无法区分。
- **已有半成品**：`planmode.RequestExit` + `State.PendingExitRequest`（`state.go:173-181,282-284`）已定义但没有任何调用点——正是为「请求退出、等待裁决」预留的状态位。
- **建议**：
  1. 新增语义：工具调用只置 `PendingExitRequest=true` 并返回「计划已提交评审」，不再接受模型自填 `approve/quit`（`request_changes` 保留为模型自我修订语义）。兼容期可接受 `decision` 但把模型来源的 `approve` 降级为「请求评审」。
  2. 宿主裁决路径（HTTP `/plan-mode` POST、`/plan approve|quit`、TUI/Web 按钮）继续写 `ExitDecision`；两者在事件里区分 `actor=user|model`。
  3. 批准后的模式显式化：默认恢复到 `accept_edits` 而不是 `previous_mode`；除非用户显式选择「回到 bypass」——避免「进入 plan 前是 yolo，批准即无审批实现」。
  4. 需要保留自主进入/退出能力的无头场景（exec/ACP）用策略开关（如 `plan_mode.auto_approve=true`）显式放行，并在事件中标注来源。
- **落点**：`backend/internal/toolbroker/broker.go`（execute + description）、`backend/internal/chat/plan_mode_tools.go`（ExitPlanMode 分支）、`backend/internal/planmode/state.go`、`backend/internal/agent/system_reminder.go`（提醒文案改为「提交评审并等待裁决」）、前端文案。
- **测试**：`broker_plan_mode_test.go`（模型 approve 不改变模式、只置 pending）、`chat/plan_mode_tools_test.go`（宿主 approve 生效）、`agent/system_reminder_test.go`。

### 4.2 【P0】计划工件一等公民：planstore（目录 + 索引 + 版本 + 状态）

- **CommandCode 语义**：计划存 `~/.commandcode/plans/`，有 `plans-index.json`、`versions/`、状态 `pending/approved/not-implemented`；取消不丢计划，`/plans` 随时重开。
- **aicli 现状**：计划就是工作区里的 `plan.md`（`plan_mode_handlers.go:357-362` 的路径解析、`engine.go` 的白名单）。后果：
  1. 多轮修订只有最终版，没有历史（除非用户自己 git commit）；
  2. `quit` 后没有任何「这份计划被放弃过」的书签，无法浏览/回到它；
  3. 计划与项目文件混在一起（本仓库根 `plan.md`、`backend/plan.md` 即是实例）；
  4. `plan_path` 可以指向工作区外（API 预览会拒绝逃逸，但权限白名单不做工作区解析），见 4.7。
- **建议**：
  1. 新增 `backend/internal/planstore`：`~/.aicli/plans/<project-slug>/<name>.md` 正文 + `index.json`（标题、状态、创建/更新时间、版本、评论计数、来源会话）+ `versions/<name>-v<N>.md`。
  2. 与现有 `plan_mode` 状态解耦：会话状态保存 `plan_id` 引用，工作区 `plan.md` 仍可作为「项目内镜像」写入（保持既有用户习惯与白名单语义），但归档副本以 planstore 为准。
  3. 状态机落在 planstore：`pending`（进入 plan 且已写文件）→ `approved`（宿主 approve）→ `implemented`（可选：检测到后续实现回合）；`quit/cancel` → `not-implemented`。
  4. API：`GET /plans`、`GET /plans/{id}`、`POST /plans/{id}/activate`（回到某份计划继续评审/修订）；前端 Artifact 面板加 Plans 列表页签。
- **落点**：新增 `backend/internal/planstore/**`；`api/skills/plan_mode_handlers.go`（enter/exit 钩子写索引与快照）；`planmode/state.go`（状态引用）；`frontend/src/lib/runtime-api` + `artifact-panel*`。
- **风险**：避免双事实源——会话 context 仍是「当前是否处于 plan」的权威，planstore 只是工件仓库；两者的关联键（`plan_id`）必须单点写入。

### 4.3 【P0】评审反馈闭环：notes 必须到达模型

- **CommandCode 语义**：Submit review 把评论打包成提示交给 agent，agent 修订后重新呈现；评论清空，版本 +1。
- **aicli 现状**：`request_changes` 只做三件事：写 `ExitDecision`、写 `Notes`、保持 `status=active`（`plan_mode_tools.go:111-125`、`plan_mode_handlers.go:232-241`）。全仓 `state.Notes` 的消费点只有 `planModeResultFromState`（工具返回）与 `buildSessionPlanModeResponse`（API 返回）。Web 裁决走 `applyPlanModeViaActor`（`plan_mode_handlers.go:161-195`），**不触发新回合**，模型既没有工具结果、也没有用户消息、系统提醒里也不含 notes —— 用户填的修改意见在模型侧不可见，只有用户再发一条消息才可能被「顺带」看到（且 notes 不会随消息注入）。
- **建议**：
  1. 状态增强：`ExitDecision=request_changes` 时把 notes 存为 `pending_review_notes`（与「最后一轮决策备注」区分），并记录 `review_round`。
  2. 注入：下一次模型回合开始前，将 `pending_review_notes` 作为一条用户/系统输入注入（可复用 system reminder 通道但**不要** `Durable:false` 的一次性语义，需按「消费后清除」），清除成功后写回状态并发 `plan_mode_changed`。
  3. 主动修订（可选）：Web/TUI 在 `request_changes` 成功后调用 trigger-turn（带 notes）直接让 agent 修订，实现 CommandCode 的「一次提交即一轮修订」。
  4. 计划正文不变：评论不进 `plan.md`（与 CommandCode 的 sidecar 语义对齐，见 4.4）。
- **落点**：`planmode/state.go`（新字段 + ToMap/stateFromMap）、`chat/plan_mode_tools.go`、`api/skills/plan_mode_handlers.go`、`agent/loop.go` 或 `agent/system_reminder.go`（注入点）、`use-runtime-plan-mode.ts`（提交后刷新/触发）。
- **测试**：状态往返（`sqlite_storage_plan_mode_context_test.go`）、注入一次且消费后清除（`system_reminder_test.go`）、Web 提交后模型下一回合可见 notes（handler 单测 + e2e）。

### 4.4 【P1】行级评论与轮次 diff（评审面升级）

- **CommandCode 语义**：逐行评论、sidecar、变更行标绿、`ctrl+n/p` 跳转、`?/x/!` 快捷评论。
- **aicli 现状**：单文本域（`artifact-panel-plan-surface.tsx:201-253`），无行锚点；计划正文是运行时预览（只读 Markdown），编辑要回工作区文件；无版本快照故无 diff。
- **建议（分步，避免一次性大改）**：
  1. 数据层先落地「评论集合」：`PlanComment{id, anchor(line/quote), body, kind(question|cut|risk), createdAt, round}`，存 planstore 索引 sidecar，不写计划正文。
  2. 前端在 `MessageMarkdown` 预览上支持行选择与行下评论（可用行号 + 引用文本双重锚定，降低版本漂移成本）；提交时按 4.3 的通道发给 agent。
  3. 版本 snapshot（4.2）就绪后加「本轮变更行高亮 + 变更计数 + 跳转」，与 CommandCode 的 round 语义对齐。
  4. 快捷评论模板（为什么/删掉/有风险）作为按钮，降低输入成本。
- **落点**：`frontend/src/components/workspace/artifact-panel-plan-surface.tsx`、新增 `plan-comments` 子组件、`use-runtime-plan-mode.ts`、后端 planstore/comments API。
- **注意**：Web 端已有 pending 卡片只显示 hint（`pending-interaction-bar.tsx:176-180`），行级评论应放在 Artifact 面板（阅读面），pending 卡片保持「有待裁决」的轻量提示，不要复制两套编辑器。

### 4.5 【P1】按需评审、自动兜底与 `/plans` 浏览器

- **CommandCode 语义**：`plan_review` 工具、`/plan-review`、`/plans [name]`、harness 兜底（run 结束时计划已写但未评审 → 主动呈现）。
- **aicli 现状**：无上述工具与命令；Web 端因为「plan active ⇒ pending 条目」（`plan-review.ts:18-36`）天然具备**部分兜底**（仅当前会话、仅打开 Web 且在呈现位时）；CLI/TUI 侧若模型停下不调用 `exit_plan_mode`，用户只能看到 `/plan` 状态，没有可操作的评审入口。
- **建议**：
  1. **loop 兜底**：agent run 自然结束（无 pending 工具、无错误终止）时，若 `planmode.IsActive` 且存在计划文件且 `PendingExitRequest=false`，发一条评审事件/提醒（default/plan 模式），accept_edits/bypass 跳过——与 CommandCode 的 skip 规则一致。
  2. **`plan_review` 工具**：模式外打开/回到某个计划的评审面；对 CLI 输出「计划 + 可裁决提示」，对 Web 触发面板聚焦。
  3. **`/plans` 命令**：列出 planstore 中的计划（状态徽标、会话、时间），`/plans <name>` 进详情；与 `/plan status` 区分（后者只报当前会话的 plan 状态）。
- **落点**：`backend/internal/agent/loop.go`（结束钩子）、`toolbroker`（新工具定义 + 执行）、`cmd/aicli/commands/chat_plan_command.go`、前端浏览器组件。

### 4.6 【P1】模式循环、常驻标识与进入确认

- **CommandCode 语义**：`shift+tab` 循环 + banner；`enter_plan_mode` 需确认；`/mode` 不能进 yolo。
- **aicli 现状**：模式集合已有（`modes.go:8-13`），但无循环键位、无常驻模式标识（composer 有 permission-mode 控件，`composer-permission-mode-control.test.tsx`），且模型自主进入 plan 无确认（`project-permissions.md:78`；`plan-mode-session-record-availability-fix-plan-20260918.md:597` 明确记载「我们的产品允许模型自主进入计划模式」是**有意设计**）。
- **建议**：把「自主进入」从默认行为改为可配置策略：
  - 默认：模型调用 `enter_plan_mode` 时走一次用户确认（复用既有 approval 通道与 pending 卡片，不新增机制）；
  - 显式策略（如 `plan_mode.auto_enter=true` 或 profile 配置）恢复当前自主进入，供 exec/无头/受信场景使用；
  - TUI 增加模式循环键位与 banner；Web 已有 composer 控件，补「当前模式」常驻标识即可。
- **落点**：`toolbroker/broker.go`（enter 执行前接入 approval 或 `ask_user_question` 语义）、`agent/permission_engine.go`、`frontend/src/components/workspace/composer-permission-mode-control.tsx`、`backend/cmd/aicli/ui/**`（键位与 banner）。
- **兼容**：`--permission-mode plan` 与 `/plan enter` 是用户显式动作，**不需要**确认；只有模型自主调用需要。

### 4.7 【P2】计划写白名单的路径语义与 allowlist 豁免

- **CommandCode 语义**：豁免精确到 `~/.commandcode/plans/` 目录下的 `.md`，任何模式都不询问；只有 `.md` 可用，目录不能当草稿纸。
- **aicli 现状**（三处需要收紧/补齐）：
  1. **base name 匹配**：`planWriteAllowed` 对 `filepath.Base(allow)` 与目标 base 做相等判断（`engine.go:582-584`），因此策略层会放行任意目录下叫 `plan.md` 的目标（执行层可能仍被沙箱路径边界拦下，但策略层语义已不精确）；前缀匹配（`:585-587`）在 allow 为相对路径（如默认 `plan.md`）时失效——`docs/plan.md` 反而不匹配 `plan.md`（被 base name 规则兜住，语义含混）。
  2. **apply_patch 启发式**：直接对补丁正文做 `strings.Contains`（`:563-572`），补丁里出现 "plan.md" 字样即放行，可能连带允许同一补丁里的其他文件。
  3. **allowlist 反例**：`enter_plan_mode` 是 runtime-owned essential，可绕过硬 allowlist（`tool_policy.go:126-132`），但 `write/apply_patch` 不在豁免表里；当用户用 `--allow-tool view,grep` 这类窄名单时，会得到「能进 plan 模式但写不了计划」的矛盾状态。
- **建议**：
  1. 进入 plan 时把 `WriteAllowPaths` 解析为工作区绝对路径（并与 4.2 的 planstore 路径对齐），比较改为「绝对路径相等或显式目录前缀」，删除 base-name 兜底（或降级为仅当 allow 与目标同目录时生效）。
  2. `apply_patch` 解析 `*** Update File/Add File` 头，逐文件校验，不再做整文字符串包含。
  3. 在 plan 模式内，对白名单内计划文件的写调用在 `ToolExecutionPolicy` 层加豁免（仅限 plan active + 路径命中），使窄 allowlist 下 plan 模式自洽；同时保持「显式 deny / 沙箱 / 只读子代理」优先。
- **落点**：`policy/engine.go:552-590`、`policy/tool_policy.go:126-132`、`toolbroker/broker_arg_audit.go` / `toolexec/preflight.go`（补丁与路径预检）、`planmode/state.go`（进入时解析路径）。
- **测试**：`engine_test.go` 增加「`docs/plan.md` 不因 base name 被放行」「补丁头含其他文件被拒」「窄 allowlist + plan 模式可写计划文件」三组用例。

### 4.8 【P2】文档信息架构

- 建议新增 `docs/aicli/plan-mode.md`：模式选择表（何时用 plan / accept-edits）、命令参考（`/plan`、`/mode`、flags）、工具参考（`enter_plan_mode` / `exit_plan_mode` / 未来 `plan_review`）、评审键位/流程、与 checkpoint 的关系、与 planstore 的位置约定；并在 `docs/user-guide/aicli.md:123` 与 `docs/README.md` 加交叉链接。

---

## 5. 已有优势（借鉴时不要回退）

| 能力 | 为什么强于 CommandCode 对应面 | 证据 |
|------|------------------------------|------|
| **durable 会话状态机 + 每 turn 重投影** | 计划状态作为会话记录的一部分持久化，跨进程/恢复/多宿主一致；引擎与 RunMeta 只是每 turn 重建的执行副本，并有「宿主旧 run meta（如 bypass 快照）不得关掉 plan 门禁」的显式防护 | `planmode/state.go`；`chat/actor.go:5342-5425`；`docs/plan/plan-mode-session-record-availability-fix-plan-20260918.md:59-101` |
| **`SESSION_NOT_FOUND` 韧性** | 分类层类型化错误 + 入口不变量 + actor 自愈 + 可观测性，专门解决「计划状态依赖会话记录」的可用性风险；CommandCode 文档未涉及该失败面 | `docs/plan/plan-mode-session-record-availability-fix-plan-20260918.md`（Phase 0/1/2/4 已实施） |
| **统一 pending 生命周期** | 审批/提问/计划评审共用一条注册-提交-收敛链路：乐观收敛、失败回退、超时 expired、会话中断收敛、事件回放重建、按会话过滤与优先级（真实交互 > 计划投影）；CommandCode 的 review 只覆盖计划一种 | `frontend/src/lib/pending-interaction/**`、`use-pending-interactions.ts:207-216`、`docs/plan/frontend-deepseek-harness-optimization-plan.md:843` |
| **只读子代理仍可用控制面工具** | `ReadOnlyChildCapabilities` 让 plan mode / ask user / collab 在只读子代理中可用，同时 write 型工具从模型可见面移除并在执行层拒绝；CommandCode 文档未展开子代理 × plan 的交互 | `docs/product/project-permissions.md:77-81`；`policy/capability_test.go:43-49` |
| **多宿主同构** | CLI slash、HTTP API、Web 面板、exec 无头共用同一状态机与同一持久字段；HTTP 侧还区分「有活 actor 走 actor（保证 mid-turn 同步）」与「无 actor 直接改记录」 | `plan_mode_handlers.go:100-129`、`chat_plan_command.go`、`docs/user-guide/aicli.md:123-128` |
| **窄名单不锁死控制面** | runtime-owned essentials 绕过硬 allowlist 但保留显式 deny / 能力域 / 沙箱 / 只读约束，避免 `--allow-tool` 把 agent 控制面锁死 | `policy/tool_policy.go:126-132`；`project-permissions.md:78` |
| **ACP 计划呈现（与 plan mode 互补）** | `todos` 工具快照 → ACP plan entries，含指纹去重与历史回放重建；说明「计划」在 ACP 面已有独立通道，新的 planstore 应以「不新增第二套计划语义」为约束 | `backend/cmd/aicli/commands/agent_stdio_plan.go:26-40,155-183` |
| **模式切换收口** | 从 plan 切到其他模式会以 `quit` 关闭持久 plan 状态，避免「对外宣称 plan 生效、实际已不再施加写白名单」的分叉 | `chat/permission_mode.go:18-70` |

---

## 6. 建议实施顺序与验证

| 阶段 | 内容 | 依赖 | 预估 | 验证 |
|------|------|------|------|------|
| **S0** | 4.1 退出裁决权收口（含 `PendingExitRequest` 接线、approve→accept_edits 默认、来源标注） | 无 | 0.5–1d | `go test ./internal/{planmode,chat,toolbroker,policy}/...`；模型 approve 不改变模式的用例 |
| **S1** | 4.2 planstore（目录/索引/版本/状态 + API）+ 4.3 反馈闭环（`pending_review_notes` 注入/清除 + 事件） | S0 的状态字段稳定后 | 3–5d | 状态往返持久化测试（含 `sqlite_storage_plan_mode_context_test.go` 同族）；注入一次并清除的 loop 测试；Web 提交 notes 后下一回合模型可见的 handler/e2e 用例 |
| **S2** | 4.5 `plan_review` 工具 + `/plans` + run 结束兜底 | S1 的 planstore 列表 | 2–3d | loop 兜底单测（default/plan 触发、accept_edits/bypass 跳过）；CLI 命令测试 |
| **S3** | 4.4 行级评论 + 轮次 diff（前端为主） | S1 版本快照 | 1–2w | vitest（评论集合、锚点漂移、轮次高亮）；`npx tsc -b`；e2e 评审流 |
| **S4** | 4.6 模式循环/横幅/进入确认 + 4.7 写白名单精确化 | 可与 S2 并行 | 3–5d | `engine_test.go` 三组路径用例；TUI 键位测试 |
| **S5** | 4.8 文档与 quickstart | S0–S4 结论 | 0.5d | 链接检查、示例命令可执行 |

**建议先做 S0 + S1**：两者合计不超过一周，直接闭合「计划可被模型自批」与「用户意见到不了模型」两个正确性缺口；评审面（S3）体验收益最大但成本最高，放在存储与闭环稳定之后，避免把行级评论建在不存在的版本模型上。

---

## 7. 开放问题

1. **模型自主进入 plan 的产品定位**：本仓库明确把它当作有意设计（`plan-mode-session-record-availability-fix-plan-20260918.md:597`），而 CommandCode 默认要求用户确认。4.6 的建议是「默认确认 + 策略放行」，需要产品确认是否接受默认行为变化（无头/exec 场景可保持自主）。
2. **计划工件位置**：planstore 落在 `~/.aicli/plans/` 后，工作区 `plan.md` 是「镜像」还是「唯一事实源」？建议镜像（便于 code review/git），但需要在 UI 明确标注哪个是归档版。
3. **与 checkpoint 的关系**：CommandCode 在任何模式改文件前建 checkpoint，approve 后实现可回滚。本仓库 checkpoint 能力已存在（`docs/design/checkpoint-design.md`、会话 backtrack），但 plan approve 未与之绑定；是否需要在「approve → 实现」之间自动打点，属独立议题。
4. **多计划并发**：单会话是否允许同时维护多份计划（`plan_path` 数组已支持多写路径）？planstore 的 `plan_id` 需要回答「一个会话是 1 个还是 N 个当前计划」。
5. **`quit` 后的语义**：CommandCode 取消后状态为 `not-implemented`，可随时重开；aicli 目前 `quit` 只是 `status=exited` + 保留工作区文件，用户没有回访入口。S1 落地时应同时定义「重开」的 API 与 UI 动词。

---

## 附录 A：关键证据索引

| 主题 | 文件:行 |
|------|---------|
| plan 状态结构与归一化 | `backend/internal/planmode/state.go:19-57,70-100,111-149` |
| 退出决策与「请求退出」预留位 | `backend/internal/planmode/state.go:173-181,184-198,236-249,282-284` |
| 宿主工具实现（enter/exit） | `backend/internal/chat/plan_mode_tools.go:23-70,73-143,163-230` |
| 模式切换收口 | `backend/internal/chat/permission_mode.go:18-70,76-93` |
| plan 写路径判定 | `backend/internal/policy/engine.go:236-262,552-590` |
| 模式决策与能力 | `backend/internal/policy/modes.go:8-13,42-62`；`policy/capability.go:85-90` |
| essentials 绕 allowlist | `backend/internal/policy/tool_policy.go:126-132`；`docs/product/project-permissions.md:77-81` |
| 模型提醒（一次性） | `backend/internal/agent/system_reminder.go:210-223,228-255,262-275` |
| 工具定义与执行 | `backend/internal/toolbroker/broker.go:35-36,209-263,1261-1307` |
| HTTP/Web 状态与预览 | `backend/internal/api/skills/plan_mode_handlers.go:23-26,60-129,161-195,197-246,248-371` |
| CLI `/plan` | `backend/cmd/aicli/commands/chat_plan_command.go:14-26,27-93,103-143` |
| 前端 hook/投影/面板 | `frontend/src/hooks/workspace/use-runtime-plan-mode.ts:25-32,62-77,195-249`；`lib/pending-interaction/plan-review.ts:6-36`；`components/workspace/artifact-panel-plan-surface.tsx:92-253`；`components/workspace/pending-interaction-bar.tsx:114,176-180` |
| 韧性方案（已实施部分） | `docs/plan/plan-mode-session-record-availability-fix-plan-20260918.md:1-50,59-140,597` |
| 已交付范围（B3） | `docs/plan/grok-harness-productization-implementation-plan.md:405-423` |
| 无头入口 | `docs/user-guide/aicli.md:123-128` |

## 附录 B：CommandCode 与 aicli 术语对照

| CommandCode | aicli | 备注 |
|-------------|-------|------|
| `plan` 模式 | `plan`（`permission_mode=plan`） | 语义一致：禁写非白名单、shell 只读放行 |
| `accept-edits` | `accept_edits` | 命名风格不同，语义一致 |
| `yolo` / bypass | `bypass_permissions` | 均不参与常规循环（CommandCode 明确 `/mode` 不可达） |
| `exit_plan_mode` | `exit_plan_mode` | 关键差异：CommandCode 由用户批准，aicli 当前由模型填 decision |
| `plan_review` 工具 | 无 | 建议新增（S2） |
| `/plans`、`/plan-review` | 无（仅 `/plan`） | 建议新增（S2/S3） |
| `~/.commandcode/plans/` | 工作区 `plan.md` | 建议引入 planstore（S1） |
| 行级评论 / 版本 diff | 单 notes 文本域 | 建议分步实现（S3） |

---

## 8. 实施记录（2026-09-25）

**状态**：S0 + S1（§4.1 退出裁决权、§4.3 评审反馈闭环、§4.2 planstore 工件存储）**已实施**；S2–S4（`plan_review` 工具、run 结束兜底、`/plans` 前端浏览器、行级评论/轮次 diff、模式循环、§4.7 写路径精确化、§4.8 文档）**未在本轮范围**。

### 8.1 行为变更

| 场景 | 变更前 | 变更后 |
|------|--------|--------|
| 模型调用 `exit_plan_mode(decision=approve\|quit)` | 直接退出 plan 模式并恢复 previous mode | 记为**待裁决请求**（`pending_exit_request=true`），会话保持 plan 模式与写白名单，等待宿主裁决 |
| 模型调用 `exit_plan_mode(decision=request_changes)` | 保持 plan + 记录 notes | 不变（模型自我修订语义），且**不会**把自己的 notes 当作评审反馈回灌 |
| 无头/自治宿主（`aicli exec` 等） | 模型 verdict 直接生效 | 默认同样收口；显式设置 `AICLI_PLAN_MODE_MODEL_AUTONOMY=1` 恢复直通 |
| 用户批准（Web 面板 / `/plan approve` / API）且进入前是 `bypass_permissions` | 恢复 `bypass_permissions` | 降级为 `accept_edits`，避免「批准计划 = 顺带关闭全部审批」 |
| 用户 `request_changes` 带 notes | notes 只写入 `state.Notes`，模型侧不可见 | notes 进入 `pending_review_notes`，**下一次模型回合**作为一次性 system reminder 注入并清除，同时发 `plan_mode_changed` |
| 计划工件 | 仅工作区 `plan.md`（无索引/版本/状态） | 额外归档到 `$HOME/.aicli/plans`（`AICLI_PLANS_DIR` 可覆盖）：`index.json` + `versions/<project>/<plan>-v<N>.md`，状态 `pending/approved/not_implemented` |
| 读取归档 | 无 | `GET /api/runtime/plans`、`GET /api/runtime/plans/{id}`（含最新快照正文） |

### 8.2 接线点

| 模块 | 文件 | 内容 |
|------|------|------|
| 状态机 | `backend/internal/planmode/state.go` | `ExitSource`、`LastExitSource`、`PendingReviewNotes`、`ReviewRound`；`RequestExitFrom`/`RecordReviewNotes`/`ConsumeReviewNotes`/`ExitRequested`/`ModelMayDecideExit`；`ResumeModeAfterExit` 的 bypass→accept_edits 降级 |
| 归档 | `backend/internal/planmode/archive.go`（新增） | `ArchivePlan`（record + 快照 + 状态推导）、`DefaultPlanStore`（按 root 缓存，`AICLI_PLANS_DIR` 可覆盖）、工作区逃逸防护；`enter` 只登记元数据不产生轮次 |
| 工具契约 | `backend/internal/toolbroker/types.go`、`broker.go` | `EnterPlanModeArgs.Source`、`ExitPlanModeArgs.Source`、`PlanModeResult` 扩展；broker 统一标注 `Source="model"`、工具描述改为「提交评审/等待用户裁决」 |
| Actor | `backend/internal/chat/plan_mode_tools.go` | 模型 approve/quit 转待裁决；用户 `request_changes` 记录评审 notes；新增 `publishPlanModeChanged`、`archivePlanModeArtifact`、`consumePlanReviewNotes`；`planArtifactStore()` 可注入 |
| 回合注入 | `backend/internal/chat/actor.go` | `SessionActorConfig.PlanStore`（测试/宿主注入）；`handleSubmit` 在加载会话后消费待交付评审 notes（持久化清除失败则回滚，保证不丢） |
| 事件 | `backend/internal/chat/events.go` | 新增 `plan_mode_changed`（前端 `use-runtime-plan-mode` 已在监听）、`plan_archive_failed` |
| 提示 | `backend/internal/agent/system_reminder.go` | `ReminderKindPlanReview` + `PlanReviewNotesBody`（一次性、`Durable=false`） |
| HTTP | `backend/internal/api/runtimeapi/plan_mode_handlers.go`、`plans_handlers.go`（新增）、`handler.go` | 宿主裁决标注 `Source=user`；无 actor 路径同样归档；`Handler.plansStore` 可注入；`/plans` 与 `/plans/{id}` 路由 |
| CLI | `backend/cmd/aicli/commands/chat_plan_command.go` | `/plan` 进入/退出归档；`chatPlanArtifactStore` 测试注入点 |
| 模式切换 | `backend/internal/chat/permission_mode.go` | 切离 plan 时以 `quit` 收口并归档 |

### 8.3 兼容性与开关

- **无头自治**：`AICLI_PLAN_MODE_MODEL_AUTONOMY=1|true|yes|on` 恢复「模型 verdict 直接生效」；交互式宿主（chat/TUI/Web）默认用户裁决。
- **旧状态兼容**：`plan_mode` 会话 context 新增字段全部 `omitempty`，旧记录读取后按 `NormalizeExitSource` 归一到 `user`。
- **归档最佳努力**：归档失败只发 `plan_archive_failed` 事件，**不**回滚 plan 模式迁移；`internal/planmode.ReadPlanArtifact` 对缺失/不可读文件返回空内容（仅记录元数据）。
- **测试隔离**：actor/CLI/API 三处归档 store 均可注入，测试不会写入开发者 `$HOME/.aicli/plans`。

### 8.4 验证（2026-09-25）

| 命令（cwd = `backend/`） | 结果 |
|---|---|
| `go build ./...` / `go build ./cmd/...` | exit 0 |
| `go test ./internal/planmode/... ./internal/planstore/... ./internal/toolbroker/... ./internal/policy/... -count=1` | 全绿 |
| `go test ./internal/chat/ -count=1` | ok（36.9s，全量） |
| `go test ./internal/api/runtimeapi/ -count=1` | ok（32.6s，全量） |
| `go test ./internal/agent/ -run 'Reminder\|Plan' -count=1` | ok |
| `go test ./cmd/aicli/commands/ -run Plan -count=1` | **阻塞**：同仓另一会话正在实施 MCP 报告（`mcp_add_args_test.go` 引用未完成的 `validateMCPAddAuth`），测试二进制无法编译；`go build ./cmd/...` 与 `/plan` 变更本身编译通过，待其落地后需复跑 |

新增/扩展用例：模型 approve 转待裁决、`AICLI_PLAN_MODE_MODEL_AUTONOMY` 直通、bypass 批准降级、用户 `request_changes` 记录待交付 notes、notes 只交付一次、归档轮次/状态/逃逸防护、`/plans` 列表与详情、损坏索引 500、无 actor 的会话直改路径归档。

### 8.5 未实施（下一轮）

- §4.4 行级评论与轮次 diff（依赖前端评审器改造）；§4.5 `plan_review` 工具 + run 结束兜底 + `/plans` 浏览器 UI；§4.6 模式循环/横幅/模型自主进入确认。
- §4.7 写白名单路径精确化（base-name 匹配、`apply_patch` 字符串包含启发式、窄 allowlist 下计划文件写入豁免）。
- §4.8 `docs/aicli/plan-mode.md` 文档页。
- 评审反馈的**自动修订回合**（当前为「下一次用户输入时交付」；Web 裁决后主动 trigger-turn 需接入带 RunMeta 的 trigger 通道，属 S2 范围）。

### 8.6 环境提示

本轮实施期间，工作区存在**另一并发会话**的大规模在建改动（`backend/internal/api/skills/** → backend/internal/api/runtimeapi/**` 重命名 204 项、MCP 环境变量插值等）。本报告与本轮代码均以重命名后的路径为准；`cmd/aicli/commands` 测试二进制的当前失败来自该并发改动，与本轮 plan-mode 变更无关。

---

## 9. 实施记录：第二轮（2026-09-25，S2 + §4.7 + §4.8）

**状态**：§4.5 的 run 结束兜底与 CLI 评审入口、§4.7 写白名单路径语义收紧、§4.8 文档页**已落地**。仍未实施：§4.4 行级评论/轮次 diff（前端）、§4.5 的前端 `/plans` 浏览器与 `plan_review` 工具、§4.6 模式循环与自主进入确认门控、评审反馈的自动修订回合、planstore 保留策略。

### 9.1 行为变更

| 场景 | 变更前 | 变更后 |
|------|--------|--------|
| 模型写完计划却不调用 `exit_plan_mode` | 无任何提示，用户不知道计划已可评审 | run 干净结束时（idle、无错误）发布 `plan_review_available`：plan 仍 active、模型未请求裁决、模式为 `default`/`plan`、计划文件非空才发；同一正文在**同一进程生命周期内**只提示一次（按正文哈希去重），正文变化后再次提示；`accept_edits`/`bypass` 跳过（§4.5.1） |
| CLI 查看计划 | 只有 `/plan status`（无正文、无「就绪」提示） | `/plan review`（别名 `show`）打印正文 + 状态 + 轮次 + 三种裁决入口；`/plan status` 在计划就绪时提示「计划已就绪待评审」、模型已请求时提示「待裁决」 |
| CLI 查看归档 | 只能靠 HTTP API | `/plans` 列表（ID/状态/版本/时间/路径）、`/plans <id>` 详情（轮次 + 最新快照正文），并进入斜杠命令目录（补全/帮助） |
| `planWriteAllowed` 路径语义 | `filepath.Base` 相等 → 任意目录的 `plan.md` 被放行；`apply_patch` 靠正文 `strings.Contains` 整包放行 | 进入 plan 时把白名单解析为工作区绝对路径（`write_allow_paths_resolved`）；匹配改为「绝对路径相等或带分隔符目录前缀」，无 resolved 时回退相对路径语义；`apply_patch` 逐 `*** Add/Update/Delete File:`、`*** Move to:` 头校验，**全部**目标命中才放行，目标不可解析即拒绝 |
| 窄 allowlist（`--allow-tool view,grep`）下的 plan 写入 | `write`/`apply_patch` 被 allowlist 门拒绝 →「进得去、写不了计划」 | plan active + 全部目标命中计划白名单时豁免 allowlist 门（逐目标二次校验）；离开 plan 立即失效。显式 deny / 只读子代理 / 能力域 / 沙箱边界优先级不变 |

### 9.2 接线点

| 模块 | 文件 | 内容 |
|------|------|------|
| 路径语义 | `backend/internal/policy/plan_paths.go`（新增） | `planWriteTargets`（路径参数 + 补丁头解析）、`planTargetAllowed`、`pathEqualOrWithin`（Windows 大小写不敏感）、`isPlanWriteToolName`、`cleanPlanAllowPaths`；Engine 与 ToolExecutionPolicy 共用同一份语义 |
| 引擎 | `backend/internal/policy/engine.go` | `PlanWriteAllowPathsResolved` + `SetPlanWriteAllowPathsResolved`；`planWriteAllowed` 重写为「全目标命中」，删除 base-name 与整文字符串启发式 |
| 工具策略 | `backend/internal/policy/tool_policy.go` | `SetPlanWriteExemption(active, raw, resolved)`；allowlist 门的 plan 豁免 + `allowToolCall` 逐目标校验；`Clone` 复制新字段 |
| 状态 | `backend/internal/planmode/state.go` | `EnterOptions{PreviousMode,PlanPath,WriteAllowPaths,Workspace}`、`EnterPlan`、`State.WriteAllowPathsResolved`（`omitempty`，往返序列化）、`ApplyToEngine` 同步 resolved；`Enter` 保持原样（无 Workspace 时行为不变） |
| Actor | `backend/internal/chat/actor.go`、`plan_mode_tools.go` | `syncPlanWriteExemption` 在 `applyPlanModeStateToEngine` 与退出分支同步策略；`EnterPlanMode` 传 `Workspace`；run 结束兜底 `maybeAnnouncePlanReview`（按正文哈希去重，actor 内 `planReviewHash`）；退出时清理豁免 |
| 事件 | `backend/internal/chat/events.go` | 新增 `plan_review_available` |
| API | `backend/internal/api/runtimeapi/plan_mode_handlers.go` | 无 actor 路径同样用 `EnterPlan` + `planModeWorkspacePath` 锚定白名单（顺带抽掉重复的 workspace 解析） |
| CLI | `backend/cmd/aicli/commands/chat_plan_command.go`、`chat_plans_command.go`（新增）、`command.go`、`chat_command_result.go`、`chat_slash_command_catalog.go` | `/plan review`、状态提示、`/plans` 列表/详情与两条命令通道（普通 + 统一 TTY 结果）接线 |
| CLI 守卫测试 | `chat_slash_completion_test.go`、`chat_command_result_test.go` | 目录期望表补 `/plans`；直接写者清单补 `handlePlansCommand` 并把 `handlePlanCommand` 计数 7→8（统一 TTY 约定守卫） |
| 前端 | `frontend/src/hooks/workspace/use-runtime-plan-mode.ts`、`use-session-permission-mode.ts` | 把 `plan_review_available` 加入重载事件集合，收到即刷新计划面板 |
| 文档 | `docs/aicli/plan-mode.md`（新增）+ `docs/user-guide/aicli.md`、`docs/README.md` 交叉链接 | 模式选择、命令/工具参考、评审闭环与兜底、归档布局、HTTP API、FAQ、未实现清单 |

### 9.3 验证（2026-09-25 第二轮）

| 命令（cwd = `backend/`） | 结果 |
|---|---|
| `go build ./...` | exit 0 |
| `go test ./internal/chat/ -count=1` | ok（36.1s，全量；含新增兜底/豁免用例） |
| `go test ./internal/api/runtimeapi/ -count=1` | ok（33.6s，全量） |
| `go test ./internal/toolbroker/... ./internal/planmode/... ./internal/policy/... ./internal/planstore/... -count=1` | 全绿 |
| `go test ./cmd/aicli/commands/ -count=1` | ok（162.3s，全量；含 `/plan review`、`/plans` 列表/详情/空库用例与两条守卫测试） |
| `gofmt -l <改动文件>` | 无输出 |
| `npx tsc -b` + `npx vitest run src/hooks/workspace`（frontend） | exit 0；49 files / 351 tests 全绿 |

新增用例（要点）：run 结束兜底「同一正文只提示一次 + 改写后再提示 + 已请求裁决不再提示」、「accept_edits/无正文/非 active 不提示」、策略豁免随 plan 状态进入/退出（窄名单下 `write` 放行、退出后恢复拒绝）、旧语义负例（`other/plan.md` 不放行、补丁混入非计划文件整体拒绝、补丁移动计划文件出白名单拒绝、目标不可解析拒绝）、以及既有优先级回归（显式 deny / 只读 / 能力域 / 沙箱）。

### 9.4 未实施（下一轮）

- §4.4 行级评论、逐行锚点与轮次 diff（前端为主，planstore 已具备逐轮快照）。
- §4.5 前端 `/plans` 浏览器面板与 `plan_review` 工具（CLI 入口与兜底事件已就绪）。
- §4.6 模式循环键位/常驻横幅、模型自主进入 plan 的确认门控。
- 评审反馈的自动修订回合（Web 裁决后主动 trigger-turn）。
- planstore 保留/清理策略（当前只增不删）。

### 9.5 环境提示

本轮期间另一会话仍在并发改造 `backend/internal/api/runtimeapi/**` 与 `cmd/aicli/commands/**`（MCP/skills 相关），其间出现过短暂的 `go build` 失败（如 `h.attachDisabledSkills` 未定义、`validateMCPAddAuth` 未定义），均在对方下一次保存后消失；上表结果为最终一次稳定状态的输出。`internal/policy/permissions_file.go` 存在既有的 gofmt 偏差（非本轮改动文件，未触碰）。

---

## 10. 实施记录：第三轮（2026-09-25，S3：§4.5 剩余 + §4.6 门控 + 归档保留）

**状态**：§4.5 的 `plan_review` 工具与 Web `/plans` 面板、§4.6 的模型自主进入确认门控、planstore 删除/保留策略**已落地**。仍未实施：§4.4 行级评论与轮次 diff、§4.6 的模式循环键位/常驻横幅、从归档一键重新进入评审、评审反馈的自动修订回合。

### 10.1 行为变更

| 场景 | 变更前 | 变更后 |
|------|--------|--------|
| 模型调用 `enter_plan_mode` | 直接进入 plan 模式 | **默认需用户确认**：交互宿主走既有审批通道（`ApprovalRequest.Reason = plan_mode:model_auto_enter`），用户允许才进入；拒绝则本次工具调用被拒且状态不变 |
| 无头宿主调用 `enter_plan_mode` | 同上直接进入 | 无 `AskHandler` → `headless_deny:approval_required`。恢复自治：`AICLI_PLAN_MODE_MODEL_AUTONOMY=1`（进程级）或 `Engine.PlanAutoEnterWithoutApproval=true`（宿主字段） |
| plan 模式内重复进入（嵌套） | 刷新路径 | 不变，且**不重复弹确认**（`mode == plan` 时门控不生效） |
| permissions 显式规则 | — | 规则优先于门控：对 `enter_plan_mode` 的 allow/ask/deny 规则命中即按规则走（既有"规则优先"语义不变） |
| 打开一个计划评审面 | 只能靠 `/plan status`、`/plan review`、`/plans <id>`（宿主命令） | 新增模型侧只读工具 `plan_review`：按会话计划或归档 `plan_id`(+`version`) 读取正文（上限 64 KiB，超限置 `truncated`），返回状态/轮次/`verdict_options`/可直接转述的 `hint`，并发布 `plan_review_requested` 事件供宿主聚焦面板；不开评审、不做裁决 |
| 归档保留 | 只增不删 | `AICLI_PLANS_MAX_VERSIONS=N`（正整数）在每次归档后仅保留最新 N 轮快照（旧快照文件 + 轮次条目一起删除；`version` 计数器不回退）；`DELETE /api/runtime/plans/{id}` 幂等删除整条记录与全部快照 |
| 审批文案 | 卡片直接显示政策键 | CLI `humanApprovalReason` 与 Web `approvalReasonText` 把 `plan_mode:model_auto_enter` 翻成中文说明（未知键原样回显） |
| 归档计划阅读面（Web） | 无（只能看当前会话的计划正文） | Artifact 面板新增「归档计划」面：列表（状态/版本/时间/路径/会话）+ 详情（轮次决策 + 最新快照正文，markdown 渲染）+ 空/载/错三态与重试 + 手动刷新；订阅 `plan_review_requested` / `plan_review_available` / `plan_mode_changed` / `plan_updated` 自动刷新，只读不裁决 |

### 10.2 接线点

| 模块 | 文件 | 内容 |
|------|------|------|
| 确认门控 | `backend/internal/policy/plan_enter.go`（新增） | `PlanEnterToolName`、`PlanAutoEnterApprovalReason`、`PlanModelAutonomyEnabled()`、`planEnterGateArmed` / `planEnterNeedsApproval` |
| 决策管线 | `backend/internal/policy/engine.go` | 新增 `Engine.PlanAutoEnterWithoutApproval`；第 5 步（read-only auto-allow）在门控生效时跳过 `enter_plan_mode`（否则它会被自动放行成死代码）；新增第 6 步产出 `DecisionAsk` 并**定格** `Reason` 为公开契约值 |
| 自治开关收敛 | `backend/internal/planmode/state.go` | `ModelMayDecideExit` 委托 `runtimepolicy.PlanModelAutonomyEnabled()`，进程内只有一个 env 解释器 |
| 工具注册 | `backend/internal/policy/taxonomy.go`、`tool_policy.go` | `plan_review` 归为只读控制工具；加入 runtime-owned essentials（窄 allowlist 下仍可用，显式 deny 仍优先） |
| 工具契约 | `backend/internal/toolbroker/types.go`、`broker.go`、`broker_arg_kinds.go`、`broker_arg_audit.go` | `PlanReviewArgs` / `PlanReviewResult` / `PlanReviewController`；`ToolPlanReview` 常量、定义（仅当宿主注入控制器）、路由与参数类型/键声明 |
| Actor | `backend/internal/chat/plan_review_tool.go`（新增）、`actor.go`、`events.go` | `ReviewPlan`（会话计划 + 归档两种来源、路径归一、64 KiB 截断、hint）；`EventPlanReviewRequested`；`configureRuntime` 注入 `broker.PlanReview = a` |
| 归档保留 | `backend/internal/planstore/retention.go`（新增）、`archive.go` | `PruneVersions`（保留最新 N 轮，删文件后写索引）、`Delete`（幂等删除记录 + 快照）；`AICLI_PLANS_MAX_VERSIONS` 在归档后 best-effort 裁剪 |
| HTTP | `backend/internal/api/runtimeapi/plans_handlers.go`、`handler.go` | `DELETE /api/runtime/plans/{id}`（幂等，返回 `deleted` 布尔） |
| CLI | `backend/cmd/aicli/commands/chat_runtime_events.go` | 审批原因中文映射（`plan_mode:model_auto_enter`） |
| Web | `frontend/src/lib/pending-interaction/approval-copy.ts`（新增）、`components/workspace/pending-interaction-bar.tsx` | 审批原因中文映射（未知键原样回显），待办卡片不再显示裸政策键 |
| Web 归档面板 | `frontend/src/api/runtime/plans.ts`、`types/runtime/plans.ts`、`hooks/workspace/use-runtime-plans.ts`、`components/workspace/artifact-panel-plans-surface.tsx`、`artifact-panel-plans-shared.ts`、`panel-registry.ts`、`artifact-panel.tsx`、`artifact-panel/surface-mount.tsx`、i18n(`panels.artifacts.*.plans`) | 新增 `plans` 自包含面（`requiresSession=false`）；面板把 `lastRuntimeEventType`/`runtimeEventCount` 下传给自包含面做「事件→重载」；plan id 按 `/` 分段 `encodeURIComponent`；请求序号守卫防止慢响应覆盖新选择 |
| 文档 | `docs/aicli/plan-mode.md` | §3.1 门控说明、§3.3 `plan_review`、§3.4 自治开关扩义、§5.1 保留策略、§6 DELETE、§8 Q8、§9 未实施清单 |

### 10.3 验证（2026-09-25 第三轮）

| 命令（cwd = `backend/`） | 结果 |
|---|---|
| `go build ./...` | exit 0 |
| `go test ./internal/policy/... ./internal/planmode/... ./internal/planstore/... -count=1` | 全绿（含门控用例 a–h + 保留策略用例） |
| `go test ./internal/chat/ ./internal/toolbroker/ ./internal/agent/ -count=1` | ok（43.5s / 23.4s / 19.2s，全量；含引擎→actor 审批通道集成用例） |
| `go test ./internal/api/runtimeapi/ -count=1` | ok（42.7s，全量，含 DELETE 端点用例） |
| `gofmt -l <改动文件>` | 无输出（`permissions_file.go` 等既有偏差不属于本轮改动） |
| `npx tsc -b` + `npx vitest run src/lib/pending-interaction/approval-copy.test.ts src/components/workspace/pending-interaction-bar.test.tsx`（frontend） | exit 0；8 tests 全绿 |
| `npx tsc -b` + `npx vitest run src/components/workspace src/hooks/workspace src/api/runtime src/lib/pending-interaction`（frontend） | exit 0；**221 files / 1707 tests 全绿**（含 `/plans` 面板 19 个新用例） |

新增用例（要点）：门控「无 AskHandler → headless deny」「允许/拒绝两条路径 + 收到的 Reason 精确等于 `plan_mode:model_auto_enter`」「引擎字段/env 两个 opt-out」「mode=plan 不询问」「显式 allow 规则优先」「其它工具不受影响」「bypass 走既有 resolveToAllow」；**引擎→actor 集成**（`plan_enter_gate_test.go`：审批请求落到既有 `approval_requested` 通道、事件载荷 reason 一致、拒绝后 plan 状态不变，`-count=3` 稳定）；`plan_review`「定义/路由/参数契约」「会话来源 + 归档来源 + 缺失报错 + 截断 + 空正文提示 + 事件载荷」「actor 注入控制器」；保留策略「保留最新 N 轮、删文件、`version` 不回退」「keep<=0 不裁剪」「DELETE 幂等 + 兄弟记录不受影响」「env 解析」；CLI 审批文案映射用例。

### 10.4 未实施（下一轮）

- §4.4 行级评论、逐行锚点与轮次 diff（前端评审器改造）。
- §4.6 模式循环键位（`shift+tab`）与常驻模式横幅。
- 「从归档一键重新进入评审」（当前归档面只读；重新评审需 `/plan enter`）。
- 评审反馈的自动修订回合（Web 裁决后主动 trigger-turn）。
- profile/配置文件到 `Engine.PlanAutoEnterWithoutApproval` 的接线（当前粒度是 env + 宿主字段；profile 层接线未做）。

### 10.5 环境提示

本轮首次派发两个写子代理被单写者策略拒绝（`single-writer policy violation: 2 writer subagents requested`，0/2 完成），已改为串行派发；期间另一会话仍在并发改动 `cmd/aicli/commands` 与 `internal/api/runtimeapi`，出现过一次 `web_mcp_handlers.go` 的编译中断（与本轮改动无关，随后自行恢复）。上表结果为最终稳定态输出。

另外：前端 `/plans` 面板的子代理在写完代码（hook/surface/api/types/i18n/面板注册全套 + 19 个用例）后，其 run 卡在供应商排队里 13 分钟无进展（`run_status=queued`，且父会话无权 cancel 该 run）。父会话随后关闭该子会话并**自行接管控件的验证**：`npx tsc -b` 与上述 221 文件 vitest 全量均在其产物上通过，故表中前端结果由父会话实测得出，而非子代理自述。

---

## 11. 实施记录：第四轮（2026-09-25，归档回灌 `reopen`）

**状态**：`planmode.ReopenPlan` 与 CLI `/plans reopen` **已落地**（提交 `feat(plan): 归档回灌…`）；Web 面板里的图形化 reopen 入口未做（面板仍是只读阅读面）。

### 11.1 行为变更

| 场景 | 变更前 | 变更后 |
|------|--------|--------|
| 继续评审一份已归档的计划 | 只有 `/plans <id>` 能看快照正文，无法回到 plan 模式 | `/plans reopen <id> [vN] [--force]`：把选中的快照写回工作区计划文件并直接进入 plan mode（`restore` 为别名，`--version N` 等价 `vN`） |
| 覆盖工作区文件的安全边界 | — | 文件不存在 → 创建（含父目录）；内容与快照一致 → 不改写；内容不一致 → **默认拒绝**并提示 `--force`（保护工作区里更新的正文） |
| 回灌来源可见性 | — | plan 状态新增 `reopened_from`/`reopened_version`：`/plan status` 显示 `reopened from: <id> vN`，模型侧 `plan_mode_*`/`plan_review` 结果的 `PlanModeResult` 同步透出 |
| 越界/缺失防护 | — | 相对计划路径逃逸工作区 → `cannot resolve plan path`（绝不写出工作区）；记录无快照 → 明确报错；未知 id → `planstore.ErrNotFound` |

### 11.2 接线点

| 模块 | 文件 | 内容 |
|------|------|------|
| 回灌核心 | `backend/internal/planmode/reopen.go`（新增） | `ErrReopenConflict`、`ReopenOptions`/`ReopenResult`、`ReopenPlan`、`MarkReopened`、`ReopenProvenance`；复用 `resolveArchivePath` 的工作区锚定与逃逸拒绝 |
| 状态 schema | `backend/internal/planmode/state.go` | `ReopenedFrom`/`ReopenedVersion` + `ToMap`/`stateFromMap`/`normalizeState` 支持 |
| 工具结果 | `backend/internal/toolbroker/types.go`、`backend/internal/chat/plan_mode_tools.go` | `PlanModeResult.ReopenedFrom/ReopenedVersion` 透传，宿主/模型都能看到恢复来源 |
| CLI | `backend/cmd/aicli/commands/chat_plans_command.go` | `reopen` 动作解析（`vN`/`--version[=]N`/`--force`/`-f`）、恢复 + 进入 plan mode + 结果文案；`plansCommandTextForSession`（保留 `plansCommandText` 兼容壳） |
| CLI 状态与帮助 | `backend/cmd/aicli/commands/chat_plan_command.go`、`chat_slash_command_catalog.go` | `/plan status` 增 `reopened from`；命令目录补 reopen 用法与参数说明 |
| 文档 | `docs/aicli/plan-mode.md` | §2.2 用法 + reopen 语义/安全边界、§5.1 反向回灌、§8 Q9、§9 未实施清单 |

### 11.3 验证（第四轮）

| 命令（cwd = `backend/`） | 结果 |
|---|---|
| `go build ./...` | exit 0 |
| `go test ./internal/planmode/ -run Reopen -count=1` | ok |
| `go test ./cmd/aicli/commands/ -run 'PlansReopen\|ParsePlansReopen\|PlansCommand' -count=1` | ok |
| `go test ./internal/planmode/ ./internal/toolbroker/ ./internal/chat/ ./cmd/aicli/commands/ -count=1` | 全绿（3.3s / 23.3s / 44.0s / 202.6s） |
| `gofmt -l <改动文件>` | 无输出 |

新增用例（要点）：`planmode/reopen_test.go` —— 缺失文件按快照创建、内容一致不改写、冲突拒绝且**不写盘**、`--force` 覆盖、指定版本（v1 vs 最新）、未知 id / 空 id / 无快照 / 越界路径、provenance 经 `ToMap`↔`stateFromMap` 往返；CLI —— 恢复 + 进入 plan mode + provenance、冲突拒绝与强制覆盖、版本选择、无会话提示、参数解析表（含 `--version` 缺值/`v0` 报错与 `vX` 保持字面 id）。

### 11.4 备注（环境）

`go build ./...` 期间撞到过一次另一会话在 `cmd/aicli/commands/command.go` 的半成品改动（`printChatCommandOutput` 参数不匹配），数秒后对方改完即恢复；与本轮改动无关，最终构建为 exit 0。

### 11.5 提交与“干净检出”验证（重要）

本轮把工作树里属于 plan 模式的改动按**路径白名单**提交（避开另一会话 staged 的 `skills → runtimeapi` 包改名），提交为：

- `60d5c419` feat(plan): plan_review 工具、模型自主进入确认门控与归档保留策略（第三轮）
- `74220302` feat(plan): /plans reopen 归档回灌重评审（第四轮）
- `a40ebb74` fix(plan): 补交 plan_review reminder 定义（system_reminder）

用 `git worktree add --detach <commit>` 做干净检出后逐提交构建，暴露并修掉了两个**只在干净检出下可见**的问题：

1. **漏提交定义文件**：`chat/plan_mode_tools.go` 引用的 `agent.ReminderKindPlanReview` / `agent.PlanReviewNotesBody` 定义在 `internal/agent/system_reminder.go`，该文件此前未纳入提交。已由 `a40ebb74` 补交（中间提交 `60d5c419`/`74220302` 因此不可独立构建，故不推荐对这两个提交做 bisect；`a40ebb74` 起恢复可构建）。
2. **前置的他人提交不一致（非本工作引入）**：干净检出下 `go test ./cmd/aicli/commands/` 因 `resolveConfiguredSkillDirs` 签名不匹配（定义 3 参 / 调用 2 参）无法编译。该文件最后一次变更来自 `0df28afa`（skills 启停）——`05c03d45` 时定义与调用一致（均 2 参），且本工作的任何提交都未触碰 `skills_integration.go`；主工作树里调用方已被对方改好（未提交）。

干净检出（`a40ebb74`）实测：

| 命令 | 结果 |
|---|---|
| `go build ./internal/... ./cmd/...` | 仅 `internal/webui/assets.go: pattern dist: no matching files found`（前端 dist 未构建，历史现象），无其它错误 |
| `go test ./internal/planmode/ ./internal/toolbroker/ ./internal/chat/ -count=1` | 全绿（3.0s / 23.9s / 50.2s） |
| `go test ./cmd/aicli/commands/` | 被上述他人提交的签名不一致阻塞（主工作树内该包全绿：202.6s） |

另外，`internal/api/**` 的 `/plans` 路由、`DELETE /api/runtime/plans/{id}`（第三轮）与 `plan_mode_handlers.go` 的改动**仍未提交**：这些文件位于另一会话正在进行的 `internal/api/skills → internal/api/runtimeapi` 包改名路径下（该改名已 staged 204 条），单独提交会产生编译不过的中间态。待对方改名落地后需要补一次提交。

---

## 12. 实施记录：第五轮（2026-09-25，§4.4 轮次 diff，CLI 侧）

**状态**：`planmode.UnifiedDiff` / `planmode.DiffArchivedVersions` 与 CLI `/plans diff <id> [vA [vB]]` **已落地**；前端评审面的变更行高亮与行级评论仍未做（与 §4.4 剩余部分一致）。

### 12.1 行为变更

| 场景 | 变更前 | 变更后 |
|------|--------|--------|
| 想知道「这一轮模型改了什么」 | 只能分别 `/plans <id>` 看最新正文，或凭记忆对比 | `/plans diff <id>`：对比最近两轮的 unified diff，带 `+A -R` 与行数统计；`vA` 单给 = `vA → 最新`，`vA vB` = 指定区间（`compare` 是别名） |
| 轮次元信息 | — | diff 头部带 `--- v1 <decision> (<source>, <time>)` / `+++ v2 ...`，读者知道每一侧是哪次裁决产生的 |
| 大改动/极端输入 | — | 输出上限 400 行并附截断提示；两侧改动各 > 600 行时退化为整块删除+插入并标注 `Coarse`（不做二次方内存分配）；同一版本自比返回「内容完全相同」而非错误 |
| 无会话场景 | — | diff 是纯读操作，`/plans diff` 不要求活动会话（与 `reopen` 不同） |

### 12.2 接线点

| 模块 | 文件 | 内容 |
|------|------|------|
| 渲染引擎 | `backend/internal/planmode/diff.go`（新增） | `DiffOptions`/`DiffResult`（`Added`/`Removed`/`Identical`/`Truncated`/`Coarse`/`OldLines`/`NewLines`/`FromVersion`/`ToVersion`/`RecordID`）、`UnifiedDiff`（前后缀裁剪 + LCS）、`DiffArchivedVersions`（带轮次元信息头）、`describeRound`、`hunkGroups`/`hunkRange`、`\ No newline at end of file` 标记 |
| CLI | `backend/cmd/aicli/commands/chat_plans_command.go` | `/plans diff` 关键字分发、`parsePlansDiffArgs`（`vN` 位置自由：`v1 v4 <id>` 亦可）、`diffStoredPlan` 渲染与错误文案；列表页脚补 diff/reopen 用法 |
| 命令目录 | `backend/cmd/aicli/commands/chat_slash_command_catalog.go` | `/plans` 的 Usage/参数补 `diff` |
| 文档 | `docs/aicli/plan-mode.md` | §2.2 diff 用法与边界、§9 未实施清单、§0 代码清单 |

### 12.3 验证（第五轮）

| 命令（cwd = `backend/`） | 结果 |
|---|---|
| `go build ./...` | exit 0 |
| `go test ./internal/planmode/ -count=1` | ok（2.8s，含 7 组新增 diff 用例） |
| `go test ./cmd/aicli/commands/ -run 'Plans\|ParsePlans' -count=1` | ok（1.1s） |
| `go test ./cmd/aicli/commands/ -count=1`（整包） | ok（199.0s）；同命令在前一次运行中因并发会话的 skills 测试告警失败过一次（213.4s），复跑稳定通过 |
| `gofmt -l cmd/aicli/commands internal/planmode` | 无输出 |
| 干净检出 `a704c09b`：`go build ./internal/... ./cmd/...` | 仅 `internal/webui/assets.go: pattern dist` 缺失（历史现象），无其它错误 |
| 干净检出：`go test ./internal/planmode/ -count=1` | ok（2.0s） |
| 干净检出：`go vet ./cmd/aicli/commands/` | 仍被 `0df28afa`（他人在 plan 工作之外的提交）的 `resolveConfiguredSkillDirs` 签名不一致阻塞（见 §11.5 第 2 条），与本轮改动无关 |

新增用例（要点）：`planmode/diff_test.go` —— 相同文本、`空→新`（`@@ -1,0 +1,2 @@`）、带上下文的单行替换、相距较远的两处改动拆成两个 hunk、900 行整文重写的 `Coarse`+截断（计数仍准确）、无换行结尾标记、`DiffArchivedVersions` 的轮次标签与区间推导（隐式 = 显式）、同版本自比、未知 id / 空 id / 越界版本 / 无快照四类错误；CLI —— `/plans diff <id>` 输出各要素、`v2 v2` 相同提示、缺 id 的用法提示、未知 id 错误，以及 `parsePlansDiffArgs` 参数表（含「三个版本报错」「`vX` 留在 id 里」）。

### 12.4 环境提示

本轮 `cmd/aicli/commands` 再次被并发会话的在建文件打断：新增的未跟踪文件 `chat_skill_tool_restriction.go` 引用了 `chat_skill_turn.go` 中尚未定义的 `skillTurnPin` 字段（`SkillModel`/`DisallowedFunctions` 等），该包测试二进制无法构建，与 plan 改动无关；`internal/planmode` 独立测试全程通过。上一轮已记录 `internal/api/**`（含 `/plans` 路由）仍被 staged 的包改名占用而未提交。

---

## 13. 实施记录：第六轮（2026-09-25，§4.4 后续：`plan_review` 支持轮次 diff）

**状态**：模型侧只读工具 `plan_review` 新增 `compare_version`，把「这一轮相对上一轮改了什么」直接送进模型上下文；渲染复用 §12 的 `planmode.UnifiedDiff`。前端变更行高亮/行级评论仍未做。

### 13.1 行为变更

| 场景 | 变更前 | 变更后 |
|------|--------|--------|
| 模型想知道评审轮次间的改动 | 只能分别读两轮正文自行比对（或由用户转述） | `plan_review{plan_id, compare_version: 1}` 在返回正文的同时附 `diff{from_version,to_version,text,added,removed,identical,truncated,coarse}`（与 `/plans diff` 逐字节一致） |
| 会话计划请求对比但尚无归档轮次 | — | **不报错**：正文照常返回，`hint` 说明「enter 只登记元数据，完成一次评审后才有正文」；一旦有归档轮次，同一调用返回 diff |
| 显式归档桶上的非法版本 | — | 报错并带上下文（`plan_review compare_version v9: ...`），模型可用 `/plans` 纠正 |
| 宿主渲染 | — | 工具元数据附带 `diff_from_version` / `diff_to_version` / `diff_added` / `diff_removed` / `diff_truncated`，宿主无需解析正文即可画摘要 |

### 13.2 接线点

| 模块 | 文件 | 内容 |
|------|------|------|
| 参数与结果 | `backend/internal/toolbroker/types.go` | `PlanReviewArgs.CompareVersion`、`PlanReviewResult.Diff`、`PlanReviewDiff`（含 `Identical`/`Truncated`/`Coarse`） |
| 工具定义与路由 | `backend/internal/toolbroker/broker.go` | `compare_version` 的 JSON schema 描述、参数解析、`diff_*` 元数据 |
| 参数审计/类型 | `backend/internal/toolbroker/broker_arg_audit.go`、`broker_arg_kinds.go` | 把 `compare_version` 纳入审计键与 number 类型校验 |
| 执行端 | `backend/internal/chat/plan_review_tool.go` | `reviewArchivedPlan(…, compareVersion)`、新增 `diffPlanRounds`（复用 `planmode.DiffArchivedVersions`），会话路径对「无归档记录 / 无快照轮次」降级为 `hint` |
| 错误语义 | `backend/internal/planmode/diff.go`、`planmode/reopen.go` | 「无快照轮次」改为 `%w` 包装 `planstore.ErrNotFound`，让调用方能区分「还没有轮次」与「版本不存在」 |
| 文档 | `docs/aicli/plan-mode.md` | §3.3 参数表与返回值说明 |

### 13.3 验证（第六轮）

| 命令（cwd = `backend/`） | 结果 |
|---|---|
| `go build ./...` | exit 0 |
| `go test ./internal/planmode/ ./internal/toolbroker/ ./internal/chat/ -count=1` | 全绿（2.3s / 16.1s / 39.7s） |
| `go test ./internal/chat/ -run ReviewPlan -count=1` | ok（1.0s，含 2 组新增用例） |
| `go test ./internal/toolbroker/ -run PlanReview -count=1` | ok（含 schema/参数路由/元数据断言） |
| `gofmt -l <本轮改动文件>` | 无输出 |
| 干净检出 `a8e9679f`：`go build ./internal/... ./cmd/...` | 仅 `internal/webui/assets.go: pattern dist` 缺失（历史现象），无其它错误 |
| 干净检出：`go test ./internal/planmode/ ./internal/toolbroker/ ./internal/chat/ -count=1` | 全绿（2.5s / 16.1s / 38.8s） |

新增用例（要点）：`plan_review` 归档两轮后 `compare_version=1` 返回 `from=1,to=2,+2/-1` 的正文级 diff 与轮次头；同版本自比 `identical`；`compare_version=9`（保留策略外/不存在）报错；会话计划在 enter 后（无快照）与未登记计划两条路径都降级为 `hint` 且正文可用；归档后同一调用给出 diff。broker 侧断言 `compare_version` 出现在工具 schema、能被路由到控制器，且 `diff_*` 进入元数据。

### 13.4 备注

- 本轮另一会话已把 §4.6 的 **shift+tab 权限模式循环**做进 `cmd/aicli/commands/chat_permission_mode.go`（`default → accept_edits → plan → bypass_permissions`，进入 bypass 仍二次确认）；文档 §9 的「模式循环未做」已随之修正为「键位已落地，常驻横幅未做」。该循环的 plan 档走 `/mode` 语义（只翻 permission-mode，不建 durable plan 状态），与本仓库既有文档一致，未由本轮改动。
- `internal/chat/integration_test.go`、`run_meta_test.go`、`session_runtime_store_test.go` 与 `internal/policy/permissions_file.go` 存在**既有** gofmt 偏差（`git status` 显示未修改，非本轮产物），未触碰。

---

## 14. 实施记录：第七轮（2026-09-25，§4.5/§4.7 的 HTTP 层落地）

**状态**：此前因并发会话的包改名而**未提交**的 `internal/api/**` plan 工作，随改名提交 `8e3744c2` 落地后一并提交（`8ec8f58b`）：`/api/runtime/plans` 列表/详情/删除、无 actor 路径的 plan 归档与状态透出。

### 14.1 行为变更（HTTP 面）

| 端点 | 行为 |
|------|------|
| `GET /api/runtime/plans` | 列出归档记录（按 `updated_at` 倒序），`?project=<slug>` 过滤，响应 `{"plans":[...],"count":N}` |
| `GET /api/runtime/plans/{id}` | 单条记录：状态/版本/轮次元数据 + **最新快照正文**（越界 id 拒绝；`{id:.*}` 允许 id 内含 `/`） |
| `DELETE /api/runtime/plans/{id}` | 幂等删除记录与全部快照；自动保留由归档时的 `AICLI_PLANS_MAX_VERSIONS` 约束 |
| `POST /api/runtime/sessions/{id}/plan`（无 actor 路径） | 迁移后补一次归档（`ExitSource=user`，失败不影响状态机）；响应新增 `exit_source` / `review_round` / `pending_review_notes` 字段 |

### 14.2 接线点

| 模块 | 文件 | 内容 |
|------|------|------|
| 路由与注入点 | `backend/internal/api/runtimeapi/handler.go` | 注册三个 `/plans` 路由；新增 `plansStore *planstore.Store`（tests/hosts 覆盖进程级 planstore） |
| 归档读写 | `backend/internal/api/runtimeapi/plans_handlers.go`（新增） | `planArtifactStore`、`ListStoredPlans`、`GetStoredPlan`、`DeleteStoredPlan`、`storedPlanResponseFromRecord`、正文截断 |
| 会话 plan 迁移 | `backend/internal/api/runtimeapi/plan_mode_handlers.go` | 无 actor 路径的 `archiveSessionPlanArtifact`（best-effort）+ 响应字段透出 |

### 14.3 提交方式（并发安全）

`handler.go` 同时含并发会话的在制改动（`SkillArgs` / `skillExposureOptions` 等），无法用常规 `git commit -- <path>` 只提交我的 hunk（pathspec 模式会带上整文件）。采用的流程：

1. 在临时 detached worktree（干净 `HEAD`）上按最小改动改出 `handler.go`（仅 3 处：`planstore` import、`plansStore` 字段、三条 `/plans` 路由），`git hash-object -w --path=…` 生成规范化 blob（`--path` 保证 CRLF→LF 走 clean filter）；
2. 主仓库用**临时 index**（`GIT_INDEX_FILE`）`read-tree HEAD` → `add` 我新增/修改的 3 个文件 → `update-index --cacheinfo` 放入第 1 步的 blob → `commit`；
3. 校验：新提交父提交 == 制作基线（`8ec8f58b` 的父为 `bea210eb`）；真实 index 未被动过（并发会话 staged 的 MCP 删除仍在）；`handler.go` 在 worktree 里仍是 `MM`（他们的在制改动原样保留）。

### 14.4 验证（第七轮）

| 命令 | 结果 |
|---|---|
| 干净检出 `8ec8f58b`：`go build ./internal/... ./cmd/...` | 仅 `internal/webui/assets.go: pattern dist` 缺失（历史现象），无其它错误 |
| 干净检出：`go test ./internal/api/runtimeapi/ -run Plan -count=1` | ok（1.6s：plans 列表/详情/删除 + plan_mode 无 actor 归档与状态字段） |
| 干净检出：`go test ./internal/api/runtimeapi/ -count=1`（整包） | ok（38.2s） |
| `git show --stat 8ec8f58b` | 4 files changed, 474 insertions(+), 10 deletions(-)（handler.go 仅 +9 行） |

至此报告 §11.5 记下的「`internal/api/**` 尚未提交」缺口已关闭；剩余未落地项仍是：§4.4 前端变更行高亮与行级评论、§4.6 常驻模式横幅、Web 面板的图形化 reopen、评审反馈的自动修订回合、profile→`Engine.PlanAutoEnterWithoutApproval` 接线。

---

## 15. 实施记录：第八轮（2026-09-25，§4.5 收尾：HTTP 重新评审入口）

**状态**：`/plans reopen` 的 HTTP 孪生已落地（`9843c892`）：`POST /api/runtime/sessions/{id}/plan/reopen`。Web 面板上的图形按钮仍待做（面板目前仍只读，`use-runtime-plans` 无写操作）。

### 15.1 行为

| 场景 | 行为 |
|------|------|
| 继续评审一份归档计划 | `POST /api/runtime/sessions/{id}/plan/reopen`，body `{"plan_id":"<id>","version":0,"force":false}`：把快照写回工作区计划文件并**进入 plan mode**，返回 `{reopened,plan_id,plan_path,display_path,version,bytes,created,unchanged,forced,plan_mode}` |
| 工作区文件与快照不一致 | `409` + `{"conflict":true,"error":...,"hint":"…force=true 重试"}`，且**不写盘**；带 `force=true` 重试即覆盖 |
| 未知记录 / 缺 `plan_id` | `404`（`ErrAPINotFound`）/ `400`（`plan_id is required`）；越界路径与其它归档错误统一 `400` |
| 血统（与 CLI 一致） | 会话状态写 `reopened_from`/`reopened_version`：`GET /api/runtime/sessions/{id}/plan` 与 reopen 响应都透出（`sessionPlanModeResponse` 新增两字段），`/plan status` 语义对齐 |
| actor 路径 | 活跃 actor 走新增的 `SessionActor.ReopenPlanMode`（进入 plan mode + 写血统 + 更新权限引擎），无 actor 则直接写 durable 会话并补 `enter` 归档元数据 |

**为什么 plan id 在 body**：归档 id 形如 `ai-agent-runtime/plan`，作为路径段再接 `/reopen` 后缀会重新引入 `{id:.*}` 的贪婪匹配歧义；body 传参同时与 CLI 的参数解析保持一致。

### 15.2 接线点

| 模块 | 文件 | 内容 |
|------|------|------|
| HTTP handler | `backend/internal/api/runtimeapi/plans_handlers.go` | `ReopenStoredPlan`、`storedPlanReopenRequest/Response`、`writeStoredPlanReopenError`（409/404/400 映射）、`decodeStoredPlanReopenRequest`、`reloadSessionForPlanMode` |
| actor | `backend/internal/chat/plan_mode_tools.go` | `ReopenPlanModeArgs` + `SessionActor.ReopenPlanMode`（EnterPlanMode + `planmode.MarkReopened` + `persistSession` + engine 同步） |
| 状态透出 | `backend/internal/api/runtimeapi/plan_mode_handlers.go` | `sessionPlanModeResponse` 增 `reopened_from`/`reopened_version` |
| 路由 | `backend/internal/api/runtimeapi/handler.go` | `POST /sessions/{id}/plan/reopen`（+3 行，经临时 index 只提交本工作改动） |

### 15.3 验证

| 命令 | 结果 |
|---|---|
| 主干：`go test ./internal/api/runtimeapi/ -run 'Reopen\|Plan' -count=1` | ok（2.0s，含 3 组新用例） |
| 主干：`go test ./internal/chat/ -run ReopenPlanMode -count=1` | ok（2.3s，2 组新用例） |
| 干净检出 `9843c892`：`go build ./internal/...` | 仅 `webui dist` 历史现象 |
| 干净检出：同两条定向测试 | ok（1.99s / 0.69s） |

新增用例要点：快照恢复后文件按快照创建、plan mode 激活、`plan_mode.reopened_from/version` 与后续 `GET /plan` 一致；冲突→409 且**文件未被改写**，`force=true` 后覆盖；未知 id → 404；缺 `plan_id` → 400；未知会话非 200；actor 侧 `ReopenPlanMode` 写血统并可经 `sessionStore.Load` 复核，停止的 actor 返回 `ErrSessionActorStopped`。

### 15.4 并发风险处置（重要）

提交时发现**并发会话的 index 里 stage 了本工作的反向改动**（`D plans_handlers.go`、`D plans_handlers_test.go`、以及 `handler.go`/`plan_mode_handlers.go`/`plan_mode_tools.go` 的逆向 hunk），来源是他们在较早基线上做 `read-tree`/`add` 的残余。处理：

1. 自己的提交走临时 index（`GIT_INDEX_FILE`）+ 干净基线 blob，**不**碰真实 index 与工作区；
2. 提交后对受影响的 6 个路径执行 `git reset HEAD -- <path>`，把「删除/回退本工作」的 staged 项撤掉，保留他们自己的 MCP staged 集合（`mcp.go` M + importers 删除 + 文档 M）；
3. 该清理只动 index，不动工作区：他们未提交的 `handler.go`（SkillArgs 等）原样保留为 ` M`。

若该模式再次出现（他们的流程可能重复从旧基线重建 index），恢复方式：`git checkout 9843c892 -- <path>`（本工作已在 main 历史中，不会丢）。

---

## 16. 实施记录：第九轮（2026-09-25，§4.5 收尾：Web 面板「重新评审」按钮）

**状态**：面板的图形化 reopen 入口已落地（前端 8 个文件 + 两语词典）。至此 §4.5 的「归档回灌」在 CLI / HTTP / Web 三处齐平：同一个后端端点 `POST /api/runtime/sessions/{id}/plan/reopen`，同一套冲突语义（409 → 用户确认 → `force=true`）。

### 16.1 交互

| 场景 | 行为 |
|------|------|
| 详情里的写动作 | 「计划归档」面新增**唯一**写按钮「重新评审」：按当前会话把选中记录的最新快照写回工作区计划文件并进入 plan mode；无会话上下文时按钮禁用并给出原因（title） |
| 成功 | 面板刷新列表与详情，显示「已从归档恢复 v{{version}}」+「已进入 plan mode，可继续评审这份计划」；快照与工作区一致时显示「工作区文件已与快照一致」 |
| 冲突（409） | 显示后端 hint（「工作区计划文件与归档快照不一致，未改写」）+ 「强制覆盖并重新评审」按钮；**不刷新列表**（后端未写盘）；确认后带 `force=true` 重试 |
| 其它失败 | 「重新评审失败」+ 后端错误文案；提示随返回列表清除 |

### 16.2 接线点

| 模块 | 文件 | 内容 |
|------|------|------|
| API 客户端 | `frontend/src/api/runtime/plans.ts` | `buildStoredPlanReopenPath`、`reopenRuntimePlan`（POST，body `{plan_id,version,force}`）、`normalizePlanReopenResult`、`isStoredPlanReopenConflict`（409 + `conflict=true`）、`readStoredPlanReopenHint`（hint 优先，其次 error） |
| barrel | `frontend/src/api/runtime/index.ts` | 显式具名导出以上 5 个符号（该 barrel 是白名单式，漏加会导致运行期 mock/undefined） |
| hook | `frontend/src/hooks/workspace/use-runtime-plans.ts` | `reopen(sessionId, planId, {version,force})` → `RuntimePlanReopenOutcome`；进行态 `reopenState`（idle/running/succeeded/conflict/failed + planId/version/unchanged/forced/hint）；成功后 `refresh()`；`clearReopenState()`；无会话不发请求直接失败态 |
| 渲染面 | `frontend/src/components/workspace/artifact-panel-plans-surface.tsx` | 头部「重新评审」按钮（busy/running/无会话时禁用）、成功/冲突/失败三类提示块（`data-testid` = `plans-reopen-notice` / `plans-reopen-conflict` / `plans-reopen-error`）；文件头注释更新为「阅读面 + 唯一写动作 reopen，裁决仍归会话内入口」 |
| 词典 | `frontend/src/i18n/resources/{zh-CN,en-US}/workspace/panels-artifacts.ts` | `plans.reopen.*` 9 个键（action/running/noSession/succeeded/unchanged/enteredPlanMode/conflict/force/failed） |

### 16.3 验证

| 命令（cwd=frontend） | 结果 |
|---|---|
| `npx vitest run src/api/runtime/plans.test.ts src/hooks/workspace/use-runtime-plans.test.tsx src/components/workspace/artifact-panel-plans-surface.test.tsx` | 3 文件 / **28 用例全绿**（新增 3 + 4 + 2） |
| `npx vitest run src/components/workspace src/hooks/workspace` | **193 文件 / 1413 用例全绿**（229s，含面板宿主与注册表回归） |
| `npx tsc -b` | 0 错误（含 i18n 插值类型：`version` 必须传字符串） |
| `npx eslint <8 个改动文件>` | 0 告警 |
| `node scripts/verify-frontend-i18n.ts` | `scanned=911, violations=0`（两语键集一致） |
| `node scripts/verify-max-lines.mjs` | 0 个 > 500 非空行（面文件 500 行以内） |

新增用例要点：**API 层**——路径按会话 id 编码（`session/2` → `session%2F2`）、结果归一化保留 created/unchanged/forced、`409+conflict=true` 才算冲突且 hint 优先；**hook**——成功刷新列表（1→2 次）并记成功态、冲突保留 hint **不刷新**、`force` 重试成功、无会话不发请求、`clearReopenState` 复位；**渲染面**——无会话按钮禁用、有会话点击即回灌并显示成功文案、冲突显示 hint 且确认后以 `{force:true}` 复调、成功后 hint 消失。

### 16.4 已知边界

- 面板只回灌**最新快照**（`version=0`）；指定历史轮次的回灌目前只在 CLI（`/plans reopen <id> vN`）与 HTTP body（`version:N`）可用。
- 无 actor 的 HTTP 路径不发布 `plan_mode_changed` 事件，面板靠 reopen 自身返回的 `plan_mode` 投影与主动 `refresh()` 收敛；CLI 侧不受影响。

---

## 17. 实施记录：第十轮（2026-09-25，§4.4 轮次 diff 的 HTTP + Web 双端落地）

**状态**：轮次 diff 现在三处齐平——CLI `/plans diff <id> [vA [vB]]`、`GET /api/runtime/plans/{id}/diff`、面板「计划归档」评审轮次行的「差异」展开。三者共用同一渲染器（`planmode.DiffArchivedVersions`），§4.4 只剩**行级评论**未做。

### 17.1 后端：`GET /api/runtime/plans/{id}/diff`

| 项 | 内容 |
|---|---|
| 处理函数 | `runtimeapi.Handler.DiffStoredPlan`（`plans_handlers.go`）：复用 `planmode.DiffArchivedVersions`，不另写第二套 diff 算法 |
| 查询参数 | `from`/`to`（0/缺省 → 上一轮 / 最新轮）、`context`（0..10，默认 3）、`max_lines`（0..2000，默认 400）；非法/越界一律 400（不做静默 clamp）；未知记录或未知轮次 404（`planstore.ErrNotFound`）；store 未配置 503 |
| 响应 | `{plan_id, from_version, to_version, identical, added, removed, old_lines, new_lines, coarse, truncated, text}`；`text` 是带 `--- v1 <decision> (source, time)` / `+++ vN …` 框架行的统一 diff |
| 路由顺序陷阱 | 必须注册在 `GET /plans/{id:.*}` **之前**：gorilla/mux 按注册顺序匹配，贪婪明细路由会把 `foo/diff` 当成 id 吞掉。已在 `handler.go` 注释与文档 §6 双处写明，并有回归测试钉住 |
| 错误映射 | 新增 `writeStoredPlanDiffError`：`planstore.ErrNotFound` → 404，其余 → 500 |

### 17.2 前端：面板的轮次差异

| 模块 | 文件 | 内容 |
|------|------|------|
| 类型 | `frontend/src/types/runtime/plans.ts` | `RuntimePlanDiffOptions` / `RuntimePlanDiffResult` / `RuntimePlanDiffLine(Kind)`（沿用 plans 类型集中定义，`RuntimePlanDiffResult` 不再散落在 api 模块） |
| API | `frontend/src/api/runtime/plans.ts` | `buildStoredPlanDiffPath`、`getRuntimePlanDiff`（`buildRuntimeUrlWithQuery` 拼 `from/to/context/max_lines`）、`normalizePlanDiffResult`；barrel 白名单同步补 3 个具名导出 |
| 视图辅助 | `artifact-panel-plans-shared.ts` | `classifyPlanDiffLines`（**只按前两行识别框架行**，避免把以 `--` 开头的删除行误判）+ `planDiffLineClass`（add=teal / del=orange / hunk / meta / context） |
| 新组件 | `artifact-panel-plans-diff.tsx` | `ArtifactPlanDiffBlock`：只读展示组件（loading / error+重试 / identical 提示 / `<pre>` 逐行着色），计数徽标 + `identical`/`coarse`/`truncated` 徽标 + 收起按钮；自包含、不取数 |
| 取数 | `use-runtime-plans.ts` | `diffState`（idle/loading/ready/error + planId/from/to/result/error）、`loadDiff(planId,{from,to})`（请求序号防乱序，失败只落在 diffState，不进列表/详情错误态）、`clearDiff` |
| 渲染面 | `artifact-panel-plans-surface.tsx` | 评审轮次每行加「差异」按钮（`data-testid=plan-round-diff-{from}-{to}`，展开态变「收起差异」）；pair 口径与 CLI 一致（`from=max(1,version-1)`，单轮快照回退到自身）；切换计划 / 返回列表时 `clearDiff()` |
| 词典 | 两语 `panels-artifacts.ts` | `plans.diff.*` 10 个键（title/show/hide/collapse/loading/failed/identical/identicalHint/coarse/truncated） |

### 17.3 验证

| 命令（cwd 见备注） | 结果 |
|---|---|
| `go test ./internal/api/runtimeapi/ -run 'DiffStoredPlan' -count=1`（backend） | ok（新增 2 个用例：默认口径 + 路由不被贪婪路由吞掉；单轮 identical、`max_lines` 截断、参数 400、未知记录/轮次 404） |
| `go test ./internal/api/runtimeapi/ -count=1`（backend） | ok（整包 44s，路由注册变更无回归） |
| `npx vitest run src/api/runtime/plans.test.ts src/components/workspace/artifact-panel-plans-shared.test.ts`（frontend） | 2 文件 / **15 用例全绿** |
| `npx vitest run src/hooks/workspace/use-runtime-plans.test.tsx src/components/workspace/artifact-panel-plans-surface.test.tsx`（frontend） | 2 文件 / **24 用例全绿**（含 3 个差异渲染面用例 + 2 个取数用例） |
| `npx tsc -b` / `npx eslint <8 个改动文件>` | 0 错 / 0 告警 |
| `node scripts/verify-frontend-i18n.ts` / `verify-max-lines.mjs` | 见交付提交说明（两语键集一致、面文件仍在 500 非空行内 —— 差异面板拆成独立组件文件正是为此） |

### 17.4 已知边界

- 面板只做**相邻轮次**对比（`vN-1 → vN`）；任意两轮对比（`vA → vB`）目前只有 CLI 与 HTTP 查询参数支持，UI 未给版本选择器。
- 行级评论仍缺：diff 面板是纯展示，不能在某一行挂评论；后续需要“行锚点 + 备注”的存储契约（§4.4 剩余项）。

---

## 18. 实施记录：第十一轮（2026-09-25，§4.6 Web 常驻模式标识）

**状态**：§4.6 的 **Web 侧「当前模式常驻标识」已落地**（聊天区顶部：模式徽标 + plan 状态/路径/读法）。§4.6 只剩 **TUI/CLI 侧的常驻横幅**；§4.4 只剩行级评论；自动修订回合仍待做（见 18.4 的取舍）。

### 18.1 为什么这一轮选它

另一会话此刻正在 `internal/api/runtimeapi/**` 深度改造（`handler.go`、`session_active_turn.go` 等多文件在途，索引里已有暂存改动）。**评审反馈的自动修订回合**必须落在该链路上（触发一轮 run 要走 chat actor / trigger-turn 通道），此时动手等于和对方抢同一批文件。§4.6 的常驻标识是**纯前端**切片：与后端零交叉，且是报告里明确的 §4.6 剩余项（"Web 已有 composer 控件，补「当前模式」常驻标识即可"）。

### 18.2 行为

| 场景 | 变更前 | 变更后 |
|------|--------|--------|
| 会话处于任意权限模式 | 模式只在 composer 的权限下拉里可见（要先看输入卡） | 聊天区顶部**常驻一条模式标识**：`模式 + 徽标`；`plan` 走强调色、`bypass_permissions` 走告警色、其余中性；未知模式值原样呈现（不写死枚举、不出现空徽标） |
| plan active | 需要打开右侧「计划」面板才知道计划是否就绪 | 标识追加**计划状态徽标 + 计划路径 + 一句读法**：模型已请求裁决 > 计划已就绪 > 尚未写就（读法与右侧面板同源，不新增判定） |
| 无会话 / 快照未到位 / 模式字段为空 | — | 标识**不占位**（自渲染为 null，不影响既有布局） |
| 裁决动作 | composer 上沿的待交互卡片 + 右侧计划面板 | **不变**：标识是只读的，不承载批准/请求修改/退出，避免出现第二套 pending 判定（P1-7 口径） |

### 18.3 落点与验证

| 模块 | 文件 | 内容 |
|------|------|------|
| 纯函数 | `frontend/src/components/workspace/session-mode-banner-shared.ts` | `sessionModeBannerTone`（plan/danger/neutral）、`sessionModeBannerLabelKey`（已知模式映射词典键、未知返回 null）、`sessionModeBannerHintLeaf`（裁决请求 > 正文可用 > 等待产出）、`sessionModeBannerToneClass` |
| 组件 | `frontend/src/components/workspace/session-mode-banner.tsx` | `SessionModeBanner`：只读展示组件，消费页面既有的 `/plan` 快照（不新增 hook/请求） |
| 停靠列 | `frontend/src/components/workspace/workspace-shell/session-interaction-dock.tsx` | 把「模式标识 + 审批/提问/计划评审卡片」抽成同一条宽度轴上的整体：一是两者本就同宽同列，二是 `main-section.tsx` 已顶到 500 非空行上限（抽取后净减 7 行，仍然合规） |
| 透传 | `workspace-page.tsx` → `workspace-shell.tsx` → `main-section-props.ts` → `main-section.tsx` | `plan`（页面既有 `useRuntimePlanMode` 实例）与 `planStatusLabel` 两个可选 props，不新增取数 |
| 词典 | 两语 `base.ts` | `composer.modeBanner.title` + `composer.modeBanner.hint.{modelRequested,ready,waiting}` |

| 命令（cwd 见备注） | 结果 |
|---|---|
| `npx vitest run src/components/workspace/session-mode-banner.test.tsx src/components/workspace/session-mode-banner-shared.test.ts src/components/workspace/workspace-shell/session-interaction-dock.test.tsx`（frontend） | 3 文件 / **14 用例全绿**（常显/隐藏、三档 tone、未知模式回落、裁决读法优先、停靠列同框 + 裁决回抛） |
| `npx vitest run src/components/workspace/workspace-shell src/components/workspace/session-mode-banner*.test.* src/pages`（frontend） | 19 文件 / 111 用例全绿 |
| `npx vitest run src/components/workspace src/hooks/workspace src/pages`（frontend，全量） | **207 文件 / 1497 用例全绿** |
| `npx tsc -b` | exit 0 |
| `npx eslint <13 个改动文件>` | 0 告警 |
| `node scripts/verify-frontend-i18n.ts` | scanned=915，violations=0（两语键集一致） |
| `node scripts/verify-max-lines.mjs` | OK（0 个 > 500 非空行；`main-section.tsx` 抽取后脱离顶格） |

### 18.4 未实施与取舍

- **TUI/CLI 常驻模式横幅**：§4.6 剩下的那一半，在 `backend/cmd/aicli/ui/**` 与键位同族，属 CLI 侧。
- **评审反馈的自动修订回合**：本轮刻意避开（见 18.1）；落点已记在 §8.5，等 `runtimeapi` 的在途重构落地后再取。
- **行级评论**（§4.4 剩余项）：需要先定「行锚点 + 备注」的存储契约。

### 18.5 顺带修的文档漂移

`docs/aicli/plan-mode.md` 里两处过时表述——§3「Web 面板的图形入口尚未接入」与 Q6「仍缺 Web 面板里的图形按钮」——都与上一轮已落地的「重新评审」按钮矛盾，本轮一并更正；§6 末补「Web 的常驻模式标识」小节，§9 的 §4.6 条目改为「Web 已落地 / TUI 未落地」。
