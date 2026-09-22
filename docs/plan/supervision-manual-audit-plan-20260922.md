# Supervision 手动核查（Manual Audit）调整方案

- 日期：2026-09-22
- 状态：已实施并验证（2026-09-22）
- 关联：
  - `docs/analysis/supervision-business-supervision-gap-analysis-20260916.md`（方案 A–F 实施核查）
  - `docs/plan/supervision-business-supervision-implementation-plan.md` §6.5（wake 交付语义）
  - `docs/plan/supervision-operator-runbook.md`

## 1. 背景与问题

2026-09-16 方案 §6.5 规则 2 的收口语义是：父会话 **turn 结束** 时由宿主主动 drain 一次
durable wake（必要时再跑一次 digest-only self-check），把子 Agent / Team 在父会话忙时
积压的关键生命周期事件变成一个自动 wake turn。

核查实施状态后确认该行为在当前产品形态下会干扰业务流程：

1. **占用业务回合槽**：核查 turn 与用户回合共用同一个 SessionActor。用户紧接着的输入
   被排在核查 turn 之后，出现"turn 结束后自己又跑了一轮"的观感。
2. **内联阻塞 turn 结束路径**：`internal/events/bus.go` 的 `Publish` 是同步调用，
   `EventSessionEnd` 回调里的 session 查询 + drain + digest 构建（30s ctx）直接跑在
   业务 turn 的结束路径上。
3. **缺少用户静默期闸门**：`WakeConsumer.Runnable` 只判断 actor 是否忙，没有"最近有
   用户输入 / 用户明确要求核查"的闸门，因此自动核查无法与业务意图对齐。

结论：**turn 结束后的技术核查不应是隐式自动行为，而应是用户显式发起的手动操作**
（TUI 子命令 / 前端提交），自动通道只保留"事件驱动的异常通知投递"（子会话完成、
审批请求），并且这些投递在父会话忙碌时天然排队，由下一次自然 turn 的 preflight
digest 兜底呈现。

## 2. 目标与非目标

目标：

- turn 结束后默认不再主动做任何技术核查（不 drain、不起 self-check turn）。
- 提供可用的手动入口：
  - aicli TUI：`/supervision`（`status` / `audit` / `wake` + 既有动作子命令）。
  - runtime-server HTTP：`GET /api/runtime/supervision/audit`（只读核查报告）、
    `POST /api/runtime/supervision/wake/drain`（显式投递），供前端/其它客户端调用。
- 保留回退能力：`supervision.turn_end_check: true` 恢复 2026-09-16 的 turn-end 闭合
  语义（无需回滚二进制）。

非目标：

- 不改变 preflight digest 注入：自然 turn 开始时仍会注入未决生命周期摘要（被动、无额外
  turn，不占用业务回合槽）。
- 不改变事件驱动的异常投递：子会话完成 / 审批请求投影时仍会尝试 `MaybeWakeParent`；
  父会话忙碌时 wake 保持 durable，改由下一次自然 turn 或手动 `/supervision wake` 消费。
- 不改动 `progress_check_interval` 周期巡查（本就是显式 opt-in、默认 0）。

## 3. 方案

### 3.1 配置开关（默认关）

`internal/supervision.Config` 新增：

```yaml
supervision:
  # nil / false（默认）：turn 结束后不做技术核查。
  # true：恢复 2026-09-16 §6.5 规则 2 的 turn-end drain + 可选 self-check。
  turn_end_check: false
```

- 语义：`nil`（未配置）与 `false` 等价，均为关闭；只有显式 `true` 才开启。
- 两个宿主读同一字段：CLI `localChatRuntimeHost.supervisionConfig`、
  runtime-server `handler.SetSupervisionConfig`。
- `wake_self_check_per_window` 仅在 `turn_end_check: true` 时有意义（self-check 只挂在
  turn-end 路径上）。

### 3.2 关闭自动路径

- CLI：`bindSupervisionWakeConsumer` 在 `TurnEndCheckEnabled() == false` 时**不订阅**
  `runtimechat.EventSessionEnd`（零回调、零开销）。
- API：`bindSupervisionTurnEndConsumer` 的回调首行检查 `TurnEndCheckEnabled()`。之所以
  在事件时判定，是因为 `SetSupervisionWakeScheduler` 先于 `SetSupervisionConfig` 调用
  （见 `cmd/runtime-server/main.go`），订阅时配置尚未到达。
- `wakeSupervisedParent` / `selfCheckSupervisedParent` 保留，改为只被显式入口调用
  （CLI `/supervision wake`、API drain 端点）与事件驱动路径调用。

### 3.3 aicli 手动命令 `/supervision`

```
/supervision [status|audit|wake|list|ack|defer|resolve|control|watchdog] ...

/supervision                  # status 的别名：一屏状态摘要
/supervision audit            # 只读核查：digest + 后代状态矩阵 + 待投递 wake + 预算 + 建议
  [--limit N] [--team <team_id>] [--json]
/supervision wake             # 显式投递：把待处理 durable wake 变成一个父 turn
  [--dry-run] [--team <team_id>]
/supervision list|ack|defer|resolve|control|watchdog ...
                              # 委托既有 /debug supervision 实现（同一 store / CAS 语义）
```

设计要点：

- **audit 是只读的**：不 ack、不 drain、不起 turn；渲染 scope、digest 行、后代矩阵行、
  待投递 wake、wake 预算与可执行动作建议。机器可读出口为 `--json`。
- **wake 是显式投递**：等价于旧 turn-end 动作，但由用户触发；`--dry-run` 只列出将被
  投递的 wake 行，用于"先看后投"。
- 复用既有实现，不新增协议：`LocalControlService.Snapshot`（digest）、
  `localSupervisionToolController.SupervisionDescendants`（状态矩阵，含结果源装饰）、
  `Store.ListWakePending`（待投递行）、`WakeScheduler.BudgetState`（预算）、
  `handleChatDebugSupervisionCommand`（ack/defer/resolve/control）。
- 忙时排队策略：`status` / `audit` / `list` / `watchdog` 是只读诊断，可进 busy 队列；
  `wake` 与所有写动作子命令忙时拒绝，避免隐式延迟状态变更。

### 3.4 runtime-server 手动入口（前端路径）

| 方法 | 路径 | 语义 |
| --- | --- | --- |
| GET | `/api/runtime/supervision/audit` | 只读核查：digest + snapshot + pending_wakes + wake_budget |
| POST | `/api/runtime/supervision/wake/drain` | 显式投递：`{root_scope_id, parent_session_id, parent_team_id, dry_run}` |

- `audit` 与既有 `GET /supervision/digest`、`GET /supervision/snapshot` 共享 scope 解析
  与预算投影，不重复实现语义。
- `drain` 复用 `Handler.drainSupervisedParentWake`（与事件路径同一 admission 语义：
  `Runnable` 门 + 预算 + §6-F requeue），`dry_run=true` 只返回 `ListWakePending` 结果。
- 响应携带 `delivered` / `reason`（`no_pending` / `parent_busy` / `rate_limited` /
  `delivered`），前端据此提示用户，而不是静默吞掉。

### 3.5 兼容性与迁移

- 默认行为变化：升级后 turn 结束不再自动核查。原依赖自动核查的部署可显式
  `turn_end_check: true` 恢复。
- 存量 durable wake / 通知不会丢失：preflight digest 仍在每次自然 turn 注入；
  `/supervision audit` 可见全部待处理行。
- `/debug supervision ...` 保持原样，不破坏既有脚本与文档；`/supervision` 是其上层
  入口 + 新增 audit/wake 能力。

## 4. 测试与验收

- 配置：`turn_end_check` 缺省/显式 false/显式 true 的三态语义（`WithDefaults` 不改变
  显式值）。
- CLI：关闭时 `EventSessionEnd` 不触发任何投递；显式开启时恢复投递（钉住回退开关）。
- CLI 命令：`/supervision audit` 渲染 digest/矩阵/待投递 wake/预算且零副作用；
  `/supervision wake --dry-run` 不改变 wake 状态；`/supervision wake` 触发一次投递。
- API：`GET /supervision/audit` 组合字段齐全且 503 语义不变；
  `POST /supervision/wake/drain` 的 delivered / no_pending / dry_run 分支。
- 回归：`go build ./...` + `go test ./internal/supervision/... ./internal/api/skills/...
  ./cmd/aicli/commands/...`。

### 4.1 验证记录（2026-09-22）

- 新增/扩展测试：
  - `backend/internal/supervision/config_turn_end_test.go`：`turn_end_check`
    缺省 / 显式 false / 显式 true 三态解析与 `WithDefaults` 语义。
  - `backend/cmd/aicli/commands/supervision_manual_audit_test.go`：`/supervision
    status|audit|wake` 渲染 digest/矩阵/待投递 wake 且零副作用；`wake` 缺省
    dry-run 不改写 wake；`--deliver` 触发一次投递。
  - `backend/cmd/aicli/commands/wake_consumer_host_test.go`：CLI 侧
    `EventSessionEnd` 三态对照（默认不投递且 wake 保持 durable，显式开启投递）。
  - `backend/internal/api/skills/supervision_manual_audit_test.go`：
    `GET /supervision/audit` 只读、缺 scope 报错、`turn_end_check` 字段回显；
    `POST /supervision/wake/drain` 的 dry_run / parent_busy(409) / delivered /
    缺 scope 分支；API 侧 turn_end 三态（计数型 wake 投递器）。
- 命令与结果：
  - `go build ./...` → 通过。
  - `go test ./internal/supervision/ -count=1` → ok。
  - `go test ./internal/api/skills/ -run
    'TestSupervisionAuditEndpoint|TestSupervisionWakeDrainEndpoint|TestSupervisionTurnEndDrainSwitchOnAPISide'
    -count=1` → ok。
  - `go test ./cmd/aicli/commands/ -count=1` → ok（整包 94.6s）。
  - `go vet ./cmd/aicli/commands/ ./internal/api/skills/ ./internal/supervision/`
    → 无输出。
- 顺带修复（本方案范围外的既有失败）：`cmd/aicli/commands/logger.go` 的
  `LegacyRuntimeEventsLogPaths()` 在新式会话 ID（`session_YYYYMMDDHHMMSS_suffix`）
  下因 `raw != base` 去重丢掉「更早嵌套布局」候选，导致
  `TestChatRuntimeEventBridge_EventLogPrefersLegacyLayoutWhenPresent` 在干净 HEAD
  上即失败（已用 `git worktree` 检出 HEAD 复核确认）。现改为始终返回两个候选
  （仅在路径串完全相同时去重），与函数注释声明的 `/resume` 兼容契约一致。

### 4.2 追加修复：`/supervision` 参数补全（用户实测反馈）

- 现象：输入 `/supervision` 时补全弹窗只有一行命令摘要（`/supervision  手动核查
  supervision 生命周期…`），敲空格后弹窗直接消失，子命令只能靠 `/help` 记忆。
- 根因：`/supervision` 只注册了 catalog 与 handler，
  `chat_slash_argument_completion.go` 的 `CompleteSlashArgs` 没有对应 case，
  参数模式返回 nil（弹窗关闭）；catalog 的 `Args` 字段当前不参与补全渲染。
- 修复：新增 `completeSupervisionSlashArgs`，按 `parseChatSupervisionRequest`
  与委托的 `/debug supervision` 用法逐位给出候选——一级子命令
  （status/audit/wake/list/ack/defer/resolve/control/watchdog/help）、各子命令
  开关（wake 专属 `--deliver/--dry-run`；只读三件套 `--team/--limit/--json`；
  委托子命令的 `--note/--until/--reason/--state/--action/--cascade/
  --expected-version`），以及 `--state|--action|--cascade|--until` 的枚举取值；
  自由文本/数值取值位（如 `--note `）关闭弹窗而不是给出错误候选。
- 测试：`TestChatSlashArgumentCompletionSupervision`（覆盖子命令列表、`wa`→
  `wake ` Tab 补全后弹窗切换、wake 开关隔离、`--state/--action` 取值、
  ack 的 `<notification_id>` 占位与自由文本关闭）。
- 回归：`go build ./...` → 通过；`go test ./cmd/aicli/commands/ -count=1` → ok
  （93.4s）；`go vet ./cmd/aicli/commands/` → 无输出。

### 4.3 线上闭环验证：`--expected-version` CAS 成功路径（2026-09-22）

- 背景：§4.1 的单元测试覆盖了 `defer`/`ack`/`resolve` 的版本冲突与成功路径，
  但均为 fake-host 单测。需要在真实宿主（`/web/api/invoke` +
  `supervision.db` 只读直读）上验证 CAS 成功路径确实生效。
- 目标行：`subagent.batch.timed_out` 投影出来的 `agent_run` 行（state=timed_out,
  unacknowledged+unresolved），在 DB 中可存活数天（09-18/09-19 的同类行至今仍为
  unresolved），不会被自动收敛（不同于 `agent_session` 行会被
  `ProjectAgentCompletion` 在 ~40s 内收敛为 closed）。
- 信道演进：
  - probe5（屏幕增量解析）：父会话忙时 delta 混入预输入噪声，`first-{..last-}` 解析失败。
  - probe6（invoke 互斥锁）：`/web/api/invoke` 互斥——有 invoke 在飞时其它 invoke
    一律 409。用 invoke 发起 spawn 注入 → 自己锁死自己。
  - probe7（忙时命令门）：`chat_input_queue.go:1151-1195` 的忙时命令门——只有
    `session.Interaction.IsReady()` 才放行任意 slash 命令；忙时白名单只含只读命令，
    `chatSupervisionSubcommandQueueSafe` 明确把 ack/defer/resolve/control 排除。
  - probe8（基线偏移）：注入的 spawn prompt 被模型回退到已存在的旧 batch，没有新通知
    生成；`batch_rows_since(baseline_ts)` 查询的基线晚于已有行的 created_at。
  - probe9（成功）：直接使用 DB 中已有的两条 fresh batch_* 行，在会话 Ready 窗口内
    串行执行 6 条命令。
  - probe10（control 根因）：在新会话中创建 fresh batch 行，捕获 `control --action
    close` 的完整响应文本，定位到 scope 鉴权失败。
- 最终结果（probe9 + probe10）：

  | 步骤 | 命令 | 期望 | 实际 | 判定 |
  |------|------|------|------|------|
  | P2 | `control`(stale v+7) | 冲突 | v 不变 | ✅ CAS 冲突 |
  | P3 | `defer`(v=3) | 成功 | v→4, deferred | ✅ CAS 成功 |
  | P4 | `ack`(stale v=3) | 冲突 | v 不变 | ✅ CAS 冲突 |
  | P5 | `ack`(fresh v=4) | 成功 | v→5, acknowledged | ✅ CAS 成功 |
  | P6 | `control`(stale v+9) | 冲突 | v 不变 | ✅ CAS 冲突 |
  | P7 | `control --action close`(v=4) | 成功 | 错误: outside root scope | ❌ 见下方 |
  | P10 | `resolve --state closed`(v=1) | 成功 | v→2, closed | ✅ CAS 成功 |

  - P7 根因：`runtimeserver/supervision.go:218-239` 的 `Authorize` 函数对
    `SubjectAgentSession` 和 `SubjectAgentRun` 统一走 `agent_control_agents` 图鉴权
    （`ListAgentControlAgents` → 匹配 `AgentID`/`SessionID`）。`agent_run`（batch）在
    `agent_control_agents` 中无对应行 → 鉴权失败。这是**设计如此**：`control` 操作
    实际 agent（cancel/close/retry），需要 agent 图中的存在性证明；`defer`/`ack`/
    `resolve` 是通知级操作，绕过 agent 图鉴权（走 `LocalControlService` 直接修改行）。
  - 因此：对 `agent_run` 行的 CAS 收敛应使用 `resolve --state closed` 而非
    `control --action close`。两者的 `--expected-version` 机制完全相同。
- 持久化证据：
  - `backend/.scratch/remote_supervision_probe9_cas_cli_result.json`（P2–P8 完整记录）
  - `backend/.scratch/remote_supervision_probe10_result.json`（P5/P7 完整响应文本）
  - `supervision.db` 中 `supervision_actions` 表：`act_local_ack_…`（P5 的 ack 动作，
    expected_version=4, status=completed）

## 5. 回滚

- 行为回滚：`supervision.turn_end_check: true`。
- 代码回滚：本次改动集中在一个配置字段、两个订阅点与新增命令/端点，回滚提交即可；
  无数据迁移、无 schema 变更。
