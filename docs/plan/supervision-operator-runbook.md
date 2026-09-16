# 业务监督操作手册（Supervision Operator Runbook）

- 日期：2026-09-16
- 输入：`docs/analysis/supervision-business-supervision-gap-analysis-20260916.md`（方案 A–F）、`plan.md`（P0–P2 实施计划）
- 适用宿主：CLI（`aicli`，监督工具面已装配）与 runtime-server（HTTP 接口，模型工具面按宿主能力门控）
- 文档结构：第 1–6 章是"业务监督"（spawn → 巡查 → 汇报 → 收敛）操作手册；附录 A 保留原 P6-4
  "Supervisor 告警与恢复"内容（告警矩阵 / 排查路径），两者共同构成完整 runbook。

**一句话**：父 agent 用 `spawn_subagents` 派活，用 `supervision_descendants` 一次拿 N 个子 agent 的
状态矩阵，父 turn 的 preflight digest（含 P0-B progress 区块）汇报进度，batch 终态后用
`control_descendant close` 收敛子会话；**全过程不需要重复 `wait_agent` 轮询**。

---

## 0. 一分钟速览

| 阶段 | 主入口 | 关键约束 |
| --- | --- | --- |
| spawn | `spawn_subagents`（`execution_mode=wait\|background`） | `wait` 是同步语义，返回即完成；只有 `background` 才需要事后监督 |
| 巡查 | `supervision_descendants` | 一次调用返回 N 行矩阵；不要用重复 `wait_agent` 轮询；只读、scope 不可越权 |
| 汇报 | 父 turn preflight digest（`critical_unresolved` / `action_required` 行 + `progress` 区块） | 进度只在父 turn 内可见，**不产生唤醒**；"成功不唤醒"策略不变 |
| 收敛 | `control_descendant close`（配合 `ack_lifecycle`） | 关闭动作持久化审计（`action_id`）+ 回执通知；终态 batch 的 progress 行会提示 close |

> 本文所有配置键以代码实际键名为准；`agents:` 段为 camelCase、`supervision:` 段为 snake_case，
> 方案 D/C 原文键名的对照见 §4.4。

---

## 1. spawn：`spawn_subagents` 的 wait / background 语义差异

`spawn_subagents` 的 `execution_mode` 决定"父 agent 什么时候拿到结果"，也决定"要不要业务监督"：

| 维度 | `wait`（默认，历史语义） | `background` |
| --- | --- | --- |
| 返回时机 | 工具调用返回时子任务已跑完 | 立即返回 batch handle，子任务在父 turn 之后继续跑 |
| 结果携带 | 完整子报告（父 turn 直接可用） | 只有 batch 摘要；结果随后续 lifecycle / 快照 / digest 抵达 |
| 持久化 | 不落 durable batch | batch 持久化，由 supervisor 投递生命周期更新 |
| deadline | 受父子等待窗口约束 | `wait_timeout_sec` 覆盖 batch deadline；缺省用协调器 deadline |
| 幂等 | 不适用 | `batch_idempotency_key` 作用域=父会话：重复同一 key 返回既有 batch，不重复派活 |
| 监督面 | 不需要（父 turn 内已收敛） | 需要：`supervision_descendants` 巡查 + digest 汇报 + close 收敛 |
| 异常面 | 失败直接体现在工具结果里 | 失败/超时/阻塞以 critical 通知 + 唤醒父 turn 的方式暴露 |

**选择建议**：

- 短任务、结果马上要用 → `wait`；不要为了"监督"而把 `wait` 改成 `background`。
- 长任务、可并行、父 agent 期间还能干别的活 → `background`；随后按 §2 巡查。
- 回归契约：`wait` 模式语义与 `batch_idempotency_key` 幂等行为**不得改变**（见分析文档 §7 回归关注）。

### 1.1 spawn 之后父 agent 应该做什么

1. 若本 turn 还有独立工作，继续做，不要立刻阻塞等待（参见提示词的 multi-agent collaboration guidance）。
2. 需要看整体进度时，调用一次 `supervision_descendants`（§2），而不是对每个子 agent 依次 `wait_agent`。
3. 只有在"必须等某一个 child 的具体结果才能继续"时，才对那一个 child 使用一次 `wait_agent`，
   并给出足够长的 timeout；反复用相同参数等同一个 child 会触发 polling 软刹车（§2.4）。

---

## 2. 巡查：`supervision_descendants` 一次拿 N 个子 agent 状态矩阵

### 2.1 为什么不要用重复 `wait_agent` 轮询

- `wait_agent` 一次只能盯**一个** child，而且是阻塞语义：3 个 background 子 agent 就要 3 次调用，
  还可能被拖到 timeout。`supervision_descendants` **一次调用**返回本会话 scope 内全部
  child/descendant 的执行状态 + 监督状态 + 处置线索，是"巡查原语"。
- `wait_agent` 的重复相同调用会进入 polling 软刹车：连续 3 次相同（工具+归一化参数指纹）的
  polling 调用开始注入非阻断 advisory；单轮累计阻塞等待超过 5 分钟还会升级为"waited too long"提示
  （见 §2.4）。
- `supervision_descendants` / `supervision_snapshot` 属于只读**巡查工具**，其重复调用
  既豁免 doom-loop 语义重复计数，也不参与 polling 软刹车 streak（相同参数重复巡查不会被刹车）。
- `wait_agent` 仍有两个正当用途：需要某个 child 的**具体产出**才能继续时的一次性等待；
  以及等待结束后的结果读取。巡查场景不要用它。

### 2.2 参数语义

| 参数 | 取值 | 默认 | 说明 |
| --- | --- | --- | --- |
| `mode` | `children` \| `descendants` | `descendants` | `children` 只看直接子级；`descendants` 看整棵子树。未知值直接报错，不会静默放宽 |
| `health` | `any` \| `abnormal` \| `action_required` | `any` | `abnormal` = stalled / timed_out / orphaned / invalid / terminating 等异常行；`action_required` = 只返回要求父 agent 决策的行 |
| `include_terminal` | boolean | `false` | 是否保留终态（closed / terminated）行；巡查收敛情况时打开 |
| `limit` | integer | 由宿主默认决定 | 截断返回行数；异常/待决策行优先保留，被裁剪时返回 `truncated=true`（不会静默丢行） |
| `after_seq` | integer | 0 | 调用方上次已见的事件序号（通常用上次的 `next_seq`）；比它新的终态行计入 `terminal_unacknowledged` |

### 2.3 返回字段怎么读

单行（`descendants[]`）关键字段：

| 字段 | 含义 |
| --- | --- |
| `kind` / `id` | 标的类型（会话/run/team）与 id |
| `parent_path` | 从根到该行的路径；`mode=children` 即按深度过滤 |
| `execution_status` / `run_status` | 执行面状态（agent control / durable run 的原始状态） |
| `supervision_state` | 监督面状态：`running` / `blocked` / `stalled` / `timed_out` / `orphaned` / `terminal` 等（以工具描述枚举为准） |
| `heartbeat_age_ms` / `progress_age_ms` | 心跳与进度新鲜度；两者背离（心跳新、进度旧）= 空转嫌疑 |
| `run_id` / `attempt` / `max_attempts` / 各类 deadline | P6-3 附带的 run 监督字段，用于定位重试与超时 |
| `reason` | 当前状态的判定原因（写给模型的短解释） |
| `action_required` | 该行是否需要父 agent 决策（true 才需要动作） |
| `recommended_action` | 宿主建议的下一步动作（如 `close` / `cancel` / `retry`），**建议**而非命令 |
| `allowed_actions` | 本宿主 + 服务端校验后真正**允许**的动作集合；`control_descendant` 会被再次校验，越权动作直接拒绝 |
| `next_action` | 当某条补救路径因宿主未装配而被过滤掉时的解释（为空表示没有过滤） |
| `notification_id` | 该行对应的监督通知 id；**只能原样取自返回值**，不要自己编造（见 §5.4） |
| `auto_action` | 运行时已在执行的动作（action_id + status），避免父 agent 重复 cancel/retry |
| `last_change_seq` | 该行最后变更序号，配合 `after_seq` 增量巡查 |

整体字段：

- `summary`：`running= / blocked= / stalled= / timed_out= / orphaned= / invalid= / canceling= / terminal_unacknowledged= / action_required=` 的计数汇总。
- `truncated` + `next_seq`：结果被 `limit` 裁剪时为 true；`next_seq` 是下一次增量巡查的游标。
- 工具 summary 行（模型可见的一行压缩摘要）形如：
  `descendant matrix: 5 row(s), running=2 action_required=1; decide the action_required rows ...`。
- 标的已消失的行会带着控制面最后看到的状态返回，不会凭空消失；对这类行不需要动作。

### 2.4 polling guard 软刹车契约（巡查工具为什么不受影响）

- **适用范围**：只有"整批全是 polling/control 工具（`wait_agent`、`read_agent_events`、`wait_team` 等）"
  的调用批次才计入 streak；批次里出现真实工作（含 `supervision_descendants` 巡查）会**重置** streak。
- **双阈值**：连续 3 次相同指纹（工具 + 归一化参数，去掉 timeout_ms 等纯调度参数）→ 注入非阻断 advisory；
  单轮累计阻塞等待（各次 `timeout_ms` / `wait_ms` 求和）超过 5 分钟 → 升级为累计等待提示（即使每次参数都不同）。
- **非阻断**：advisory 只附在工具结果上，**不会阻止执行**；模型应跟随结果里的 `next_action` 调整策略。
- **巡查豁免**：`supervision_descendants` / `supervision_snapshot` 不参与 streak 计数，重复调用不会触发
  "waiting longer is not progress" 提示——这是工具描述对模型的承诺，也是巡查原语的定位。
- **成本纪律**：豁免不等于可以无限刷。优先用 `health=action_required` / `abnormal` 做定向巡查，
  用 `after_seq` 增量读，避免每次拉全量矩阵。

### 2.5 典型调用

```jsonc
// 全量矩阵（默认 descendants）：看"谁还在跑、谁已终态"
{ "include_terminal": true, "limit": 50 }

// 只看需要决策的行：有 notification_id / allowed_actions 的行
{ "health": "action_required", "limit": 20 }

// 只看异常行：stalled / timed_out / orphaned / invalid / terminating
{ "health": "abnormal", "limit": 20 }

// 增量巡查：游标来自上一次返回的 next_seq
{ "mode": "children", "after_seq": 128 }
```

---

## 3. 汇报：父 turn 的 preflight digest

### 3.1 digest 的构成

父 turn 开始时（preflight），宿主把该 scope 的监督通知投影成一段预算受限的 digest 注入提示词：

- 头部计数：`critical_unresolved` / `action_required`（还有 `auto_actions_in_progress`、
  `resolved_since_last_turn`、`stale_subjects`、`truncated`）。
- 通知行：每行带 `notification_id` + `version` + 状态 + `recommended_action` / `allowed_actions`，
  供模型用 `ack_lifecycle` / `control_descendant` 决策。
- `next_seq`：下次增量读取（`supervision_snapshot(after_seq=...)`）的游标。
- 标的已消失（stale）的行降级为信息行，不再计入 critical，也不需要动作。

**收敛语义**：ack 之后该行不再重复注入（N9 语义）；defer 到期的行会自动回到注入集合。

### 3.2 P0-B progress 区块（只有父 turn 内可见）

当存在本 scope 的 background batch / team 时，digest 追加一个 `progress:` 区块，形如：

```
progress:
- batch X: 2/3 completed, 1 running (last progress 12s ago)
  - task-3: running; session=child-3; last progress 5s ago; grep
- review-batch: 3/3 completed, 1 failed (terminal; close the finished children with close_agent if they are no longer needed)
```

渲染规则（与实际实现一致）：

- 组行：`- <label|group_id|batch>: <completed>/<total> completed`，随后按需追加
  `, <failed> failed`、`, <pending> pending`、`, <running> running`；
  非终态组追加 ` (last progress <Ns|Nm|Nh> ago)`；终态组追加
  ` (terminal; close the finished children with close_agent if they are no longer needed)`
  （终态行的 done 通知是 `resolution=closed`，`control_descendant` 会被 evaluator 拒绝，见 §4.1/§4.2）。
- 子行（仅未终态任务）：`  - <task_id|child_session_id>: <state>; session=<child_session_id>; last progress <age> ago; <last message>`；
  无进度时显示 `no progress yet`。
- 预算：最多 3 个 batch 组、每组最多 4 个 running 子任务；被裁剪时输出
  `(more running children omitted; use supervision_descendants for the full matrix)`
  与 `(more batches omitted; ...)`，并置 `progress_truncated`。

**三条硬性语义**（回归契约）：

1. **只在父 turn 内可见**：progress 是读投影，不落新表；父 agent 不在 turn 内时它不推送任何东西。
2. **不产生唤醒**：进度本身不触发父 turn；唤醒仍只由 lifecycle 通知（失败 / 审批 / 阻塞等）驱动，
   "成功不唤醒"策略不变。
3. **未接线时字节级不变**：宿主没有装配 progress source 时，注入文本与改动前完全一致。

### 3.3 父 agent 的汇报纪律

- 巡查（§2）之后用一两句话向用户汇报矩阵结论：`N 个在跑 / M 个已完成 / K 个需要决策`，
  异常行给出 `supervision_state` 与原因，不要贴原始 JSON。
- 若 progress 行显示终态，按 §4 收敛并汇报"已关闭哪些子会话"。
- 若 progress 行迟迟不出现：先确认 batch 是否 `background`、宿主是否装配了 progress source
  （见 §5.4）。

---

## 4. 收敛：batch 终态后 close 子会话

### 4.1 识别终态

- progress 区块出现 `(terminal; close the finished children with close_agent ...)`；
- 或 `supervision_descendants` 中该行 `supervision_state=terminal` / `execution_status` 为终态；
- `include_terminal=true` 时终态行会留在矩阵里，`terminal_unacknowledged` 计数提示"已终态但尚未收敛"。

### 4.2 `close_agent`：终态 batch 行的首选路径

Batch done 行本身是 `resolution=closed`，evaluator 对已关闭行只允许 `inspect`（§4.1），
因此收敛提示直接指向 broker 工具，并列出批量内**成功完成**的子会话 id：

```
close_agent(id="<child session id>")
```

- 提示文本：`converge by closing the finished child sessions with close_agent: child-1, child-2`
  （没有可用子会话 id 时只给指令）；失败 / 超时的子会话不进提示，留在现场交给父 agent 判断。
- 开启 `agents.autoCloseCompleted`（§4.4）后，这一步由宿主自动完成，并生成可 ack 的回执通知。
- 终态 batch 的 progress 行是同一口径：
  `(terminal; close the finished children with close_agent if they are no longer needed)`。

### 4.2.1 `control_descendant close`：仍有未决通知行时

```
control_descendant(notification_id="<从快照/矩阵原样取得>", action="close", reason="batch terminal; child finished and no longer needed")
```

- 标的由 `notification_id` 决定（不是由模型指定 scope）；只有本会话 scope 内的通知可被控制。
- `action` 只能是该通知服务端计算出的 `allowed_actions` 子集；越权动作直接拒绝。
- `reason` 必填（durable 审计）；`cascade=target|descendants` 控制是否级联冻结子树（默认 `target`）。
- `expected_version` 可选；服务端 CAS 校验，冲突时不要盲目重试——先重读快照拿新版本。
- 动作先持久化再执行：返回 `action_id` + `status`，即使执行失败也能凭 `action_id` 复查审计记录。
- 语义上 close 针对的是**已完成/不再需要的子会话**；已 `resolution=closed` 的行不可再 close
  （对这类行用 `ack_lifecycle resolve` 收敛通知，而不是再发控制动作）。

### 4.3 `ack_lifecycle`：通知级收敛

| decision | 必填 | 效果 |
| --- | --- | --- |
| `acknowledge` | `note` | 接受该风险/已处理，行不再重复注入 digest |
| `defer` | `reason` + `until`（RFC3339 或 Go duration，如 `30m`） | 延后到期前不注入，到期自动恢复 |
| `resolve` | `state` = `closed` / `recovered` / `failed` | 设置终态 resolution，退出未决集合 |

带 `expected_version` 时做 CAS；版本冲突的正确反应是重新读取快照，而不是重复提交同一请求。

### 4.4 两档配置键

| 配置键（实际键名） | 取值 | 默认 | 作用 |
| --- | --- | --- | --- |
| `agents.autoCloseCompleted` | `off` \| `completed` \| `batch_terminal` | `off` | 关闭时行为与现状完全一致（人工/模型按 §4.2 收敛）。开启后，batch 终态会自动 close 已完成的子会话，并产出**可 ack 的回执通知**（可用 `ack_lifecycle` 收敛，动作有 audit）。语义（与实现一致）：`completed` 只在 batch 干净完成（状态 completed、无失败）时收敛**任务成功**的子会话；`batch_terminal` 在 batch 以任何终态（含 failed / timed_out）结束时收敛其中的成功子会话；failed / timed_out / canceled 的子会话**永不自动关闭**（现场留给父 agent 判断） |
| `supervision.progress_check_interval` | Go duration（如 `30s`、`2m`） | `0`（关闭） | opt-in 周期巡查。开启后仅在"存在 running background batch 且父会话空闲"时，按间隔把**轻量 progress 摘要**注入父 turn，并受既有 wake 预算 / 去抖约束；不引入"每 5s 扫描全部会话"的常驻轮询开销。0 = 关闭，行为与现状完全一致 |

**键名对照（以当前代码为准，两段风格不同）**：

- 方案 C 原文 `agents.autoCloseCompleted` = **实际键 `agents.autoCloseCompleted`**（`internal/config` 中
  `AgentsConfig` 的 YAML tag 就是 camelCase，与同段 `maxDepth`、`defaultWaitTimeoutMs` 一致）。
  当前代码**没有** `agents.auto_close_completed` 这个键，也没有别名：YAML 里写 snake_case 会被当作
  未知字段静默忽略，策略回落 `off`。
- 方案 D 原文 `supervision.progressCheckInterval` = **实际键 `supervision.progress_check_interval`**
  （`internal/supervision/config.go` 的 YAML/JSON tag 为 snake_case，与同段 `wake_budget_mode`、
  `execution_deadline` 一致）。
- 两段键名风格不同的原因是它们属于不同配置结构（`agents:` 段历来 camelCase，`supervision:` 段统一
  snake_case），不是迁移遗漏；以本手册加粗的实际键名为准。

### 4.5 推荐值与 token 预算

`supervision.progress_check_interval`：

- **推荐**：`30s`–`2m`。典型后台 batch 的心跳/进度时间戳是分钟级粒度，比 30s 更密的巡查只会重复同一行；
  长任务（>30m）可取 `1m`–`2m` 进一步省预算。
- **不建议**：小于 `30s`（巡查频率高于进度粒度，纯浪费 token 与 wake 预算）、以及默认值以外"忘了关"的长期开启。
- **token 预算量级**：progress 区块上限为 3 个 batch 组 × (1 组行 + ≤4 子行)，满配约 8–12 行文本；
  按中文/英文混合估算约 200–400 token，常态（1 个 batch + 1–2 个 running 任务）约 60–150 token。
  此外 preflight 既有的 `DigestMaxItems` / `DigestMaxChars` 预算仍生效，超出以 `truncated` 标记而不是无限注入。
  实际消耗以所用模型 tokenizer 为准，以上仅为容量上界估算。
- **关闭时的行为**：`0` 时不注入任何 progress 摘要、不注册 ticker，host 行为与本次改动前一致。
- **生效宿主**：CLI 与 runtime-server 两个宿主都读同一开关（API 侧的启动点是
  `Handler.SetSupervisionConfig`，见 `internal/api/skills/supervision_progress_check.go`）。
  多会话的 API 宿主一轮最多巡查 16 个"有 active batch"的父会话，且只为宿主已知（已有 actor）
  的会话注入；其余会话仍由下一轮自然 turn 的 preflight digest 覆盖。

`agents.autoCloseCompleted`：

- **推荐**：默认保持 `off`；需要"无人值守"的长批次再开 `batch_terminal`，只想在批次干净完成时收敛则用 `completed`。
- 开启后每个自动 close 都会写审计（action 记录）并生成可 ack 的回执通知——这是与"模型手动收敛"之间
  的可观测差异；关闭则完全依赖 §4.2。
- 风险：close 时机 vs 用户"先看结果再关"的期望；因此该档必须显式开启，不能默认打开。

---

## 5. 排障

### 5.1 CLI：`/debug supervision`

```
/debug supervision list [--all] [--limit N] [--team <team_id>]
    列出当前会话 root scope（以及活动团队）的监督通知与版本号。
/debug supervision ack <notification_id> --note <text> [--expected-version N]
/debug supervision defer <notification_id> --until <30m|2h|RFC3339> [--reason <text>] [--expected-version N]
/debug supervision resolve <notification_id> --state <closed|recovered|failed> [--expected-version N]
/debug supervision control <notification_id> --action <cancel|close|cancel_subtree|retry|reassign> --reason <text> [--cascade target|descendants] [--expected-version N]
/debug supervision watchdog
    本地执行看门狗状态。
```

- CLI 与模型侧 `ack_lifecycle` / `control_descendant` **是同一实现**（同一 durable store、同一 CAS 语义）；
  排查时两边应看到同一份通知与版本号。
- `list` 是"父 agent 现在能看到什么"的权威对照视图；模型侧 digest 行数受预算裁剪，`list` 不受。

### 5.2 HTTP：runtime-server 快照接口

模型工具面按宿主能力门控，HTTP 侧始终可以用读模型核对：

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/runtime/supervision/digest?root_scope_id=<id>` | preflight digest 读模型（含 `next_seq`、预算字段） |
| GET | `/api/runtime/supervision/snapshot?root_session_id=<id>&root_team_id=<team>` | 6.2 快照；支持 `mode` / `after_seq` / `health` / `include_terminal` / `limit`，与工具参数同源 |
| GET | `/api/runtime/supervision/actions[?root_scope_id=&target_kind=&target_id=&action=&status=&limit=]` | durable 控制动作审计列表 |
| GET | `/api/runtime/supervision/actions/{id}` | 单个动作的执行结果（`action_id` 复查入口） |
| POST | `/api/runtime/supervision/notifications/{id}/ack` \| `/defer` | 通知级收敛（等价 ack_lifecycle） |
| POST | `/api/runtime/supervision/actions` | 控制动作请求（等价 control_descendant 的 durable 写入） |

- `snapshot` 响应同时带回 `wake_budget`（P1-6 可见性）与 `agent_registry_reconcile` 缓存摘要，
  用于区分"通知没产生"与"通知产生但被预算/去抖挡住"。
- team lead 场景同时传 `root_session_id` 与 `root_team_id`；重复 scope 由宿主去重。

**核对顺序建议**：模型报告异常 → `/debug supervision list` 看通知与版本 → HTTP `snapshot` 看矩阵原始字段
→ 若怀疑预算/唤醒，看 `digest` 的计数与 `wake_budget` → 最后才动 ack/control。

### 5.3 `notification_id` 使用规则（不要编造）

- `notification_id` **只能原样取自**：`supervision_snapshot` 的 items、`supervision_descendants` 行、
  `/debug supervision list`、HTTP snapshot 响应。模型不得凭 subject id / 会话 id 猜测或拼接。
- 同一 subject 在 digest 与矩阵里的 `notification_id` 应一致；不一致说明读到了不同 scope，先对齐 scope。
- `stale`（标的已消失）的行不需要动作；对它发控制动作会因标的缺失而失败。
- 写入前若拿到过 `version`，带上 `expected_version` 做 CAS；冲突时**重新读取快照**再决定，
  不要用同一参数重试。`acknowledge` 必须带 `note`，`defer` 必须带 `reason` + `until`，
  `control_descendant` 必须带 `reason`——这是审计要求，缺参会直接被拒。

### 5.4 常见问题

| 现象 | 先查什么 | 处置 |
| --- | --- | --- |
| 同一 critical 行每轮都注入 | `/debug supervision list` 看 decision/resolution 是否仍 unresolved、是否被 defer 到期 | `ack_lifecycle acknowledge/defer/resolve`（带 note/reason），不要再等一轮 |
| 父 turn 看不到 progress 行 | batch 是否为 `background`；宿主是否装配 progress source；digest 是否被 `DigestMaxItems`/`DigestMaxChars` 裁剪 | 用 `supervision_descendants` 直接读矩阵；确认装配后重试 |
| 子会话终态后一直不关 | `agents.autoCloseCompleted` 是否为 `off`（默认） | 按 §4.2 手动 close，或开启该配置 |
| 开启周期巡查后没有变化 | `supervision.progress_check_interval` 是否 >0；是否存在 running background batch；父会话是否空闲；wake 预算是否耗尽 | 看 HTTP `snapshot` 的 `wake_budget`；调大间隔或等待预算窗口 |
| 模型看不到 supervision 工具 | 宿主是否装配 supervision controller（工具按宿主能力门控） | CLI 与 runtime-server API 宿主均已装配；API 宿主在无 durable store 时 supervision 面保持 nil（与 CLI 宿主一致），此时用 `/debug supervision` 与 HTTP 读模型 |
| 重复巡查被"刹车"提示 | 该批次是否混入了真正的 polling 工具（`wait_agent` 等） | 巡查工具本身不计数；减少混批、用 `after_seq` 增量读 |

---

## 6. 相关文档与代码索引

| 主题 | 位置 |
| --- | --- |
| 差距分析与方案 A–F | `docs/analysis/supervision-business-supervision-gap-analysis-20260916.md` |
| P0–P2 实施计划 | `plan.md` |
| 模型工具定义（描述/参数） | `backend/internal/toolbroker/supervision_tools.go` |
| 快照读模型（6.2） | `backend/internal/supervision/snapshot.go` |
| progress 投影与渲染（P0-B） | `backend/internal/supervision/progress.go`、`batch_progress.go`、`digest.go` |
| 父 turn preflight 注入 | `backend/cmd/aicli/commands/chat_actor_host.go`（preflight digest 注入点） |
| polling 软刹车 | `backend/internal/agent/polling_guard.go`；doom-loop 豁免见 `doom-loop.go` |
| CLI 控制入口 | `backend/cmd/aicli/commands/chat_debug_supervision.go` |
| HTTP 控制面 | `backend/internal/api/skills/supervision_handlers.go` |
| 提示词引导 | `backend/internal/prompt/environment_context.go`（multi-agent collaboration guidance） |
| 宿主装配 | `backend/internal/runtimeserver/supervision.go` |

---

## 附录 A：Supervisor 告警与恢复（原 P6-4 Runbook 内容保留）

> 本节面向 aicli / runtime 运维人员，说明 Child Agent 监督（supervision）与 Team 调度的
> 告警含义、排查路径与恢复动作。对应 `spawn-agent-team-supervision-timeout-recovery-plan.md`
> 的 P6-4 实施项。

### A.1 健康判定矩阵

| Heartbeat | Progress | Execution deadline | 判定 | 动作 |
|---|---|---|---|---|
| 新鲜 | 新鲜 | 未到 | healthy | 无 |
| 新鲜 | stale | 未到 | stalled | 告警；超过 grace 后 cancel |
| stale | 任意 | 未到 | orphan suspected | 校验 owner/session lease；进入 cancel/recovery |
| 任意 | 任意 | 已到 | execution timed out | cancel；终止或按策略 retry |
| stale | stale | 已到 | orphaned timeout | fencing + reclaim + retry/fail |
| 新鲜 | 等待审批/输入 | 未到 | blocked but healthy | 使用独立 approval/input deadline |
| 任意 | 任意 | 任意 | invalid | 停止新动作；隔离或 cancel；critical 通知父/Lead |

### A.2 告警清单（`EvaluateAlerts` 输出）

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

### A.3 排查路径

#### A.3.1 `outbox_backlog`

1. 查看 CLI agent graph：`/debug`（或 `/agents panel`）确认 child 是否已 terminal。
2. 若 child 已 terminal 但 parent mailbox 无 completion，检查 completion dispatcher
   日志中 `parent mailbox not found` 类错误。
3. 确认父 Session 存在且未被删除；必要时手动重放 outbox。

#### A.3.2 `critical_notification_stale`

1. `wait_agent <path>` 输出中查看 `supervision_state` / `reason`。
2. 检查父会话是否 `waiting_approval` / 正在 compact（此时 wake 会排队，不丢通知）。
3. 若父会话已恢复但通知仍 unresolved，检查 preflight digest 是否注入、ack 是否写回。

#### A.3.3 `run_progress_stalled`

1. `wait_agent <path>` 查看 `last_progress_at` 与 `progress_deadline`。
2. `/agents panel follow` 观察 heartbeat 是否仍新鲜（区分 stalled vs orphan）。
3. 若 heartbeat 新鲜但 progress stale：child 在空转（如等待外部输入）；检查其
   当前工具调用与 pending 状态。
4. 超过 grace 后 supervisor 会请求 cancel；若需立即干预，使用 action 命令 cancel。

#### A.3.4 `run_orphan_suspected`

1. 确认对应 host/worker 进程是否存活；若已崩溃，等待 lease TTL + grace 后接管。
2. 旧 owner 恢复后不得继续 claim（fencing token 校验）。
3. 若 lease 频繁过期但进程健康，检查时钟偏移与 lease 续租循环日志。

#### A.3.5 `wake_pending_stale`

1. 确认父会话状态（running / waiting approval / compact）。
2. 若父会话可运行但 wake 未 claim，检查 wake scheduler 是否启动、debounce 是否过长。
3. 手动触发父会话一个 turn 即可消费 wake。

### A.4 单视图判断（P6 验收）

- 仍健康：graph 行 `run_status=running` + `heartbeat=几秒前` + `progress=几秒前`。
- 等待审批：`run_status=waiting_approval` + `approval_deadline=in ...`。
- 无进展：`progress=<分钟级 ago>`，heartbeat 仍新鲜。
- owner 丢失：`run_status=orphaned` 或 `heartbeat=stale` + `run_orphan_suspected` 告警。
- 正在回收：`run_status=cancel_requested|canceling` + `cancel_deadline=in ...`。

### A.5 相关命令

- `/debug`：agent graph（run/attempt/deadline/heartbeat/progress 字段）。
- `/agents panel`、`/agents panel full`：TUI 摘要行（紧凑监督字段）。
- `wait_agent <path>` / `wait_team`：JSON snapshot 含 execution run 监督字段。
- 告警评估：`supervision.EvaluateAlerts(ctx, store, rootScopeID, cfg)`。
