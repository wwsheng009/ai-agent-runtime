# 子 Agent 工具面实测与优化清单（2026-09-27）

- 版本：v1.7（2026-09-27：F1–F8 全部落地，实施记录见 §6；复核修正见 §6.19）
- 状态：P1 已实施（F1/F2）；P2 已实施（F3/F4/F5）；P3 已实施（F6/F7/F8）
- 日期：2026-09-27
- 输入：本轮对 18 个 agent 生命周期工具的端到端实测（父会话 `session_20260927131313_GoMfCkG1`，本地 `aicli` CLI 宿主）
- 关联：`supervision-parent-child-control-optimization-plan-20260917.md`、`multi-agent-agentcontrol-convergence-plan.md`、`wait-budget-and-max-window-hardening-plan-20260926.md`、`supervised-turn-suspension-and-agent-task-control-plan-20260923.md`

---

## 0. 背景与测试范围

本轮目标：把 `spawn_agent / spawn_subagents / spawn_team`（创建）、`wait_agent / wait_team / read_agent_events / list_agents / subagent_status / subagent_inspect_task`（观测）、`send_message / followup_task / send_input / resolve_agent_approval`（通信/审批）、`close_agent / discard_agent_worktree / subagent_control / subagent_ack_lifecycle`（控制/清理）走一遍完整生命周期，记录"操作不顺利"的地方。

**测试边界**（结论适用范围）：

- 宿主：本地 `aicli` chat 会话（CLI 宿主，`cmd/aicli/commands/chat_supervision_tools.go`）；API 宿主（`internal/api/runtimeapi/supervision_tool_controller.go`）仅做代码比对，未实测（后续 F5/F6 批次已补 API 宿主回归用例，见 §6.5–§6.11、§6.13）。
- 难度路由：本地 `route disabled`（`easy(disabled) · unrouted`），本文不覆盖路由相关结论。
- 模型：子代理与父代理均为 `poolside/laguna-s-2.1-free`（provider `commandgo`）。

## 1. 实测结论总表

| # | 工具 | 结果 | 备注 |
| --- | --- | --- | --- |
| 1 | `spawn_agent` | ✅ | 支持 `read_only / isolation=worktree / difficulty / task_type`，返回 worktree 三元组 |
| 2 | `spawn_subagents` | ✅ | 批量 2 任务并行，返回 `batch_id` + `subagent_N` task id |
| 3 | `wait_agent` | ✅ | 支持单 id / ids 数组 / task id 解析；超时返回 `timed_out` + `execution_continues` |
| 4 | `wait_team` | ✅ | 团队终态返回完整 2/2 任务摘要 |
| 5 | `read_agent_events` | ✅ | 事件流可见 `tool_started / tool_finished / assistant.reasoning`；需按 `after_seq` 分页 |
| 6 | `list_agents` | ✅ | 仅列活跃子会话，清理后返回 0 |
| 7 | `send_message` | ✅（见 F4） | `delivered=true` 但不触发 turn；空闲子会话可能永不消费 |
| 8 | `followup_task` | ✅ | `triggered=true` 自动开新 turn，message_count 2→4 |
| 9 | `send_input` | ✅ | `interrupt=true` 实测中断运行中的子代理并投递新提示 |
| 10 | `resolve_agent_approval` | ✅ | 实测批准 `external_dir:admit`，子代理 resume 继续 |
| 11 | `close_agent` | ✅（见 F2） | 关闭同时返回末条输出；中断中的 run 分类不一致 |
| 12 | `discard_agent_worktree` | ✅ | `discarded, removed`，分支回收 |
| 13 | `apply_agent_worktree` | ✅ 已补覆盖（见 F8） | 原始实测未覆盖（只读子代理无变更）；§6.16 已补 worktree 层语义 + broker 契约用例 |
| 14 | `subagent_status` | ✅（见 F1） | digest + 全量行；行级 `allowed_actions` 与可执行性不一致 |
| 15 | `subagent_inspect_task` | ✅（见 F3） | `failed + result_available + do_not_retry` 语义并列 |
| 16 | `subagent_control` | ⚠️ 失败 | `notification ... is outside the caller scope`（见 F1） |
| 17 | `subagent_ack_lifecycle` | ⚠️ 失败 | 同上（见 F1） |
| 18 | `spawn_team` | ✅ | 2 teammate / 2 task，后台自动编排 |

**系统整体是完整的**：创建→观测→通信→审批→控制→清理全链路可用，事件审计与结果读取面齐全。以下 8 项是实测中暴露的具体摩擦点。

---

## 2. 发现清单

### F1 [P1] 通知"被建议处置"与实际"不可处置"不一致（scope 解析切换 + 终态 team fallback）

**现象**

1. `subagent_status(include_digest=true)` 给出 digest：`action_required: 1`，行内 `allowed_actions: ["inspect","acknowledge","defer","cancel","close"]`、`recommended_action: "cancel"`，`next_action` 明确要求："decide the action_required rows (notification_id + allowed_actions) with `subagent_control` or `subagent_ack_lifecycle`"。
2. 按该建议调用，两个工具**都失败**：

   ```
   [TOOL_BROKER_FAILURE] broker tool execution failed:
   supervision: action not allowed for current state:
   notification n-session_20260927132558_saXBmXP7-2947399e79bbb9f2 is outside the caller scope
   ```

3. 同一 snapshot 的 `scope` 显示 `RootSessionID=session_20260927131313_GoMfCkG1, RootTeamID=test-team-001`——即**同一批返回里**，读面同时展示会话域与团队域的行，而写面只接受团队域。

**证据（代码定位）**

| 环节 | 位置 | 行为 |
| --- | --- | --- |
| CLI 宿主 scope 解析 | `backend/cmd/aicli/commands/chat_supervision_tools.go:91-110` | 当 `session.ActiveTeam` 非空且 lead 匹配时，`rootScopeID` 从会话 id **切换为 team id** |
| API 宿主 scope 解析 | `backend/internal/api/runtimeapi/supervision_tool_controller.go:42-56` | 同上；`leadTeamID`（`:58-88`）优先 active team，**非 active（含已 done）的 team 也作为 fallback 返回** |
| 写面 scope 校验 | `chat_supervision_tools.go:222-260`、`supervision_tool_controller.go:150-196` | `Scopes: []string{rootScopeID}`（只传单一 team scope） |
| 通知归属校验 | `backend/internal/supervision/local_control.go:326-327`、`:373-390` | `notificationInScopes`：通知的 root/target parent 必须命中给定 scope，否则报 `outside the caller scope` |

**根因**：本会话在 `spawn_team` 之后成为 `test-team-001` 的 lead，写面 scope 整体切换为 team 域；但早于团队创建、直接挂在本会话下的子代理通知，其归属域仍是会话 id。读面（`subagent_status`）不按"当前调用者可执行性"过滤或标注，照常列出并给出 `allowed_actions`，于是产生"建议你做 → 你做不了"的死胡同。此外 API 宿主的 `leadTeamID` 对**已终态 team 仍返回 fallback**，意味着这种 scope 切换可能长期持续（CLI 宿主 `session.ActiveTeam` 的解绑时机本轮未验证）。

**影响**

- 失败/异常通知在团队创建后无法 `cancel / acknowledge / close`，只能等其自然过期或恢复会话绑定；
- digest 的 `next_action` 与 `allowed_actions` 成为"会失败的指令"，浪费模型轮次，也误导审计；
- 与 `subagent_status` 声称的 `recommended_action=cancel` 直接矛盾。

**建议（按优先级）**

1. **写面 scope 改为并集**：绑定 team 时传 `[]string{sessionID, teamID}`——会话作为 team lead，既有权处置团队域行，也应保留对自身直接子会话行的处置权；该改动对两个宿主同构（`resolution()` 返回值语义扩展）。
2. **读面增加可执行性标注**：`subagent_status` 行增加 `actionable` / `out_of_scope` 与原因字段；digest 的 `next_action` 文案只在存在**可执行** action_required 行时出现。
3. **终态 team 的绑定策略**：明确"团队结束后 lead 的 scope 是否回退"；推荐仅在 active team 时切换，或维持双 scope（与建议 1 合流）。
4. **错误契约**：`outside the caller scope` 场景返回具体 `next_action`（如"该通知属于 <scope>，当前调用者解析为 <scope>；请以 `subagent_status` 刷新可执行行"），替换当前的通用文案。

**回归用例**：`resolution/ack/control` 三件套——创建 team 后分别对（a）team 域行、（b）会话域行执行 `acknowledge` 与 `cancel`，断言 (b) 成功（实施建议 1 后）。

---

### F2 [P1] `close_agent` 中断运行中的子代理被记为 `failed`（与同类中断的 `canceled` 分类不一致）

**现象**：两个子代理都是被父代理主动 `close_agent` 结束的，但在监督矩阵里的分类不同：

| 会话 | 结束方式 | `run_status` | `reason` | `result_status` |
| --- | --- | --- | --- | --- |
| `session_20260927132103_5cxCuAK7` | `send_input(interrupt=true)` 后再关闭 | `canceled` | `child session interrupted` | `stopped` |
| `session_20260927132558_saXBmXP7` | `wait_agent` 超时后直接 `close_agent` | **`failed`** | **`child session failed`** | **`failed`** |
| — | 同上，错误字段 | `error_class: "context canceled"` | | |

**证据**：`subagent_status` 行数据（`run_status/reason/result_status/error_class`）；`subagent_inspect_task` 对该会话返回 `status=failed, summary="context canceled", result_available=true, do_not_retry=true`。

**根因（推断，待代码确认）**：`close_agent` 在子会话 turn 进行中触发 cancel，run 终态落入 `failed` 而非 `canceled` 分支；`interrupted`（有心跳但被主动打断）与 `canceled by parent close` 未区分。这正是环境中"父代理主动收尾"最常见的路径，分类错误会把正常清理记为失败，污染 `terminal_delta`、失败计数与告警。

**建议**

1. 明确终态优先级：父代理主动 `close_agent` → `canceled`（附 `canceled_by=parent_close`），而非 `failed`；
2. `context canceled` 且存在父侧 close 记录时，不进入 failed 告警面；
3. 回归用例：对运行中的子代理直接 `close_agent`，断言 `run_status=canceled`、`result_status=stopped`。

---

### F3 [P2] 工具级硬错误使"正常完成"的子任务被标记 `failed`（failure 语义过粗）

**现象**：实测"查看不存在的文件"场景——子代理按预期收到 `TOOL_PATH_NOT_FOUND`（`retryable=false`），随后**正常产出最终回复**（解释错误并说明不可重试），但监督侧把该 run 记为：

```
result_status: failed
result_summary: "path not found: /nonexistent/path/to/file.txt"
error_class:    "path not found: /nonexistent/path/to/file.txt"
```

`subagent_inspect_task` 同时返回互斥语义：`status=failed` + `result_available=true` + `do_not_retry=true` + 警告"already produced a deliverable"。

**影响**

- 父代理无法区分三类完全不同的情况：① agent loop 崩溃；② 任务语义上按指令完成、但过程含一次硬工具错误；③ 被中断/取消；
- "failed 但别重试"的组合需要模型自行解读，容易误判为需要人工兜底；
- 批量编排（`spawn_subagents`）里这类子任务会拉低成功面统计。

**建议**

1. 拆字段：`run_outcome`（completed/interrupted/canceled/failed）与 `task_outcome`（succeeded/partial/failed）分离，工具错误计数单列（`tool_error_count`、`hard_error_codes[]`）；
2. `failed` 仅保留 agent loop 级失败；"按指令完成但工具返回硬错误"归入 `partial/succeeded_with_errors`；
3. `subagent_inspect_task` 与 `subagent_status` 同步使用该分类，`do_not_retry` 说明改为随分类生成。

---

### F4 [P2] `send_message` 对空闲子会话 `delivered=true` 但可能"永不消费"

**现象**：对已 idle 的子代理调用 `send_message`，返回 `delivered: true`（无 `triggered` 字段）；其后连续 `wait_agent`，`message_count` 始终不变（2→2），`output` 未更新——消息滞留邮箱。对照 `followup_task` 同场景 `triggered: true` 且 message_count 2→4、产出新回复。

**根因**：设计语义如此（"the child reads it on its next natural turn"）。问题是空闲子会话如果不再被唤醒，"下一次自然 turn"可能永远不来；而返回面只有 `delivered=true`，无法据此判定"这封信可能永远不会被拆"。

**影响**：调用方（尤其是模型自己）会误认为消息已被处理；在长链路编排中造成静默丢消息。

**建议**

1. 返回面区分 `queued_no_turn`（空闲且未触发 turn）与 `delivered+triggered`，并在前者附 `next_action`："如需处理请改用 `followup_task` / `send_input`"；
2. 或在空闲子会话上直接拒绝并给出可执行替代（fail-closed 更不容易被误读）；
3. 工具描述与返回值保持一致——"不触发新 turn"必须体现在返回值可判定性上，而不是全靠调用方记忆语义。

---

### F5 [P2] `isolation=worktree` + `read_only` 子代理访问主仓库内容需 `external_dir:admit` 审批

**现象**：worktree 子代理（cwd 为 `.aicli/agent-worktrees/<session>`）执行 `view README.md` 时进入 `waiting_approval`：

```
pending_approval_reason: "external_dir:admit"
pending_approval_risk_level: "low"
```

父代理经 `resolve_agent_approval`（`allowed=true, resolution=allowed`）后方可继续，子代理随后正常完成。审批链路本身工作正常，但**纯只读浏览**也要打断-审批-恢复一轮。

**影响**：worktree 隔离下最常用的"读主仓库文件"动作成为审批热点；批量 worktree 子代理会让父代理疲于审批。

**建议**

1. worktree 模式下自动把 repo root 纳入**只读**外部根（写路径仍受 worktree 限制），或在 spawn 时提供 `approve_repo_read` 选项；
2. 若保持审批，建议将同类批量审批合并（一次批准覆盖同批 N 个子代理的 `external_dir:admit`）；
3. 与安全边界联合评审（`subagent-readonly-boundary-transparency-plan-20260917.md`），不要简单放开写路径。

---

### F6 [P3] batch 收敛缺批量原语 + 多轨 id 缺别名契约

**现象**

1. `spawn_subagents` 批次完成后，系统提示"converge by closing the finished child sessions with `close_agent`: <id1>, <id2>…"——每个子会话一次调用，N 个子代理需 N 次 `close_agent`；无 `close_agents(batch_id=...)` / `converge_batch` 原语。
2. id 形态多轨并存：`spawn_subagents` 返回 `subagent_1`（task id）；`wait_agent` 返回并接受 `subagent_1`，但回传完整 `subagent_subagent_1_f24f…`（session id）；`read_agent_events` 等只读面需要完整 id/路径；`spawn_agent` 另有 `session_ref_*` 短 id 与 `/root/session_*` 路径。

**影响**：跨工具传参需要在 3-4 种 id 之间人工映射；批量清理的轮次成本随子代理数量线性增长。

**建议**

1. 提供批次级收敛原语（`close_agent` 接受 `batch_id`，或新增 `converge_batch`），一次调用关闭并汇总批次内全部终态子会话；
2. 所有返回结构统一携带 `aliases`（task_id / session_id / path / batch_id），并明确"任一别名在后续工具中均可解析"的契约。

---

### F7 [P3] `wait_agent` 超时返回的 `next_action` 未联动巡检原语与预算状态

**现象**：长任务（全仓 `func main` 搜索）下 `wait_agent` 超时返回：

```
timed_out: true, execution_continues: true, pending_count: 1
next_action: "continue_independent_work_before_waiting_again: wait timeout only ended
this observation; pending child execution continues. Do not immediately re-call wait_agent ..."
```

文案正确，但没有顺势指向 `read_agent_events(view=tool_progress)`（本环境的巡检原语）或 `subagent_status`，也未携带 `WaitBudget` 状态（`wait-budget-and-max-window-hardening-plan-20260926.md` 已落地预算判据）。

**影响**：父子长时间任务场景下，模型需要"记得"另开巡检面；预算是否接近耗尽不可从返回值直读，增加了无效重试的概率。

**建议**：超时返回体增加 `suggested_probe`（指向只读巡检工具）与预算余量字段（如 `wait_budget_remaining`），让 `next_action` 文案与 20260926 的预算判据同源生成。

---

### F8 [P3] `apply_agent_worktree` 缺端到端实测路径（测试覆盖缺口）

**现象**：本轮只读子代理（`read_only=true`）在 worktree 中无文件变更，`apply_agent_worktree` 不具备可执行条件，未能覆盖；`discard_agent_worktree` 已实测（`discarded, removed`）。

**建议**：补一条 runbook 用例——`isolation=worktree` + 可写子代理，修改一个受控文件（如 `docs/.worktree-smoke.txt`）→ `wait_agent` → `apply_agent_worktree(keep=false)` → 断言主树出现该变更、worktree 回收；再补一条"主树同路径有本地修改 → 拒绝应用并返回 conflicts + next_action"的反向用例（对应工具契约中的 force 语义）。

---

## 3. 优先级与落地建议

| 优先级 | 编号 | 主题 | 预估触点 |
| --- | --- | --- | --- |
| P1 | F1 | 通知可处置性一致性（双 scope + 读面标注 + 终态 team 策略） | 两个宿主的 `resolution/ack/control` + snapshot/digest 投影 |
| P1 | F2 | `close_agent` 中断分类（failed → canceled） | 子会话终态归因 + 监督上报 |
| P2 | F3 | failure 分类拆分（run/task/tool 三层） | `subagent_inspect_task` / `subagent_status` 结果契约 |
| P2 | F4 | `send_message` 空闲语义可判定 | toolbroker 返回面 + 工具描述 |
| P2 | F5 | worktree 只读外部根审批减负 | 审批策略 / spawn 选项 |
| P3 | F6 | 批量收敛 + id 别名契约 | toolbroker 工具面 |
| P3 | F7 | `wait_agent` 超时契约联动巡检与预算 | toolbroker + `agentcontrol/wait_budget.go` 读取面 |
| P3 | F8 | `apply_agent_worktree` 实测用例 | 测试/runbook |

## 4. 验收与回归测试建议

| 用例 | 断言 |
| --- | --- |
| 创建 team 后处置会话域通知（F1） | `acknowledge`/`cancel` 成功；或读面显式标注 `actionable=false` 且 digest 不再建议 |
| team 终态后的 scope 行为（F1） | lead 的 scope 解析可预测（回退或双 scope，二选一并写入契约） |
| 运行中 `close_agent`（F2） | `run_status=canceled`、`result_status=stopped`、不进入 failed 告警 |
| 硬工具错误 + 正常收尾（F3） | 分类非 `failed`，或至少 `task_outcome` 与 `run_outcome` 分离可读 |
| 空闲子代理 `send_message`（F4） | 返回值可判定"不会触发 turn"，并给出 `followup_task` 替代 |
| worktree 只读读主仓库（F5） | 免审批或一次批量审批；写路径仍被限制 |
| 批次收敛（F6） | 一次调用关闭批次内全部终态子会话；别名均可解析 |
| `wait_agent` 超时（F7） | 返回体携带巡检建议与预算余量，且与 20260926 判据一致 |
| worktree 应用/冲突（F8） | apply 成功主树可见；主树冲突时拒绝且返回 conflicts + next_action |

## 5. 附录：证据索引

- 失败响应原文（F1）：`[TOOL_BROKER_FAILURE] ... notification n-session_20260927132558_saXBmXP7-2947399e79bbb9f2 is outside the caller scope`（`subagent_ack_lifecycle`、`subagent_control` 各一次）
- digest 建议原文（F1）：`next_action=decide the action_required rows (notification_id + allowed_actions) with subagent_control or subagent_ack_lifecycle`
- 相关会话：`session_20260927132558_saXBmXP7`（failed/context canceled）、`session_20260927132103_5cxCuAK7`（canceled/interrupted）、`session_20260927133235_v8d5CazD`（failed/path not found）、`session_20260927133004_f6zcqwLx`（worktree discard 实测）
- 批次：`batch_727ab892152acd39`（2/2 completed，收敛提示逐个子会话 close）
- 团队：`test-team-001`（2/2 done；团队创建是 F1 scope 切换的触发条件）
- 代码定位：`backend/internal/supervision/local_control.go:326-327`、`:373-390`；`backend/cmd/aicli/commands/chat_supervision_tools.go:91-110`、`:222-260`；`backend/internal/api/runtimeapi/supervision_tool_controller.go:42-56`、`:58-88`、`:150-196`

> 说明：本文所有"现象/证据"均为 2026-09-27 本机实测原文；"根因"中标注（推断）的条目尚未做代码级确认，落地前需按 F1/F2 的回归用例先复现定位。

---

## 6. 实施记录（2026-09-27）：F1 / F2 落地

### 6.1 F1：ack/control 决策 scope 并集（会话 + 团队）

**口径对齐**：仓库内 `/debug supervision` 与 `/supervision` 命令本就使用并集口径（`chatDebugSupervisionScopes`：会话 id + active team id，`chat_debug_supervision.go:204-219`）。本次修复把模型工具路径对齐到同一口径，消除"第二套判定标准"。

| 改动 | 文件 | 内容 |
| --- | --- | --- |
| CLI 宿主 | `backend/cmd/aicli/commands/chat_supervision_tools.go` | 新增 `decisionScopes()`（caller session + lead team，去重）；`AckLifecycle` / `ControlDescendant` 改用并集 scope |
| API 宿主 | `backend/internal/api/runtimeapi/supervision_tool_controller.go` | 新增 `decisionScopes()`（parent session + led team）；`AckLifecycle` / `ControlDescendant` 改用并集 scope |

读面（`SupervisionSnapshot` / `SupervisionDescendants` / `ReadAgentResult`）与 `resolution()` 语义不变，继续按 lead=team scope 投影；写面并集不放大权限：两个 scope 都是调用者自身（会话）与其所领导团队（team），模型依旧无法指定 scope。

**回归测试**

- CLI：`TestLocalSupervisionToolController_DecisionScopesCoverSessionAndTeam`（断言并集；会话域通知 ack 成功；外部会话仍 `ErrActionNotAllowed`）
- API：`TestHandlerSupervisionToolController_TeamLeadRetainsSessionScopeForAck`（同上语义）

### 6.2 F2：close_agent 中断分类修正

**根因修正**：终态事件载荷同时携带 `success=false` 与 `status=stopped`（`internal/chat/actor.go:3017-3026`），旧分类先看 `success` 因而把"请求的关闭"误报为 critical 失败；改为 **stop 状态优先**：

1. 新增 `isStoppedSessionStatusText()`（stopped/interrupted/canceled/cancelled）；
2. `localAgentCompletionStatus`（CLI）/ `agentCompletionStatus`（API）：先判 stop 状态 → `stopped`；再回退 `success=false` → `failed`；真正失败（status=idle + success=false）分类不变。

| 改动 | 文件 |
| --- | --- |
| CLI | `backend/cmd/aicli/commands/chat_actor_registry.go`（`localAgentCompletionStatus` + 辅助函数） |
| API | `backend/internal/api/runtimeapi/session_runtime_support.go`（`agentCompletionStatus` + 辅助函数） |

修复后：close_agent 取消的运行 → 通知 `agent_interrupted`（warning）、`run_status=canceled`、`result_status=stopped`；不再进入 failed/critical 与失败告警面（`ProjectAgentCompletion` → `finalizeChildExecutionRuns` → `childRunTerminalStatus` 同链路生效）。

**回归测试**

- CLI：`TestLocalAgentCompletionStatus_StopStatusWinsOverFailureHeuristic`
- API：`TestAgentCompletionStatus_StopStatusWinsOverFailureHeuristic`

### 6.3 验证证据（2026-09-27）

- `go build ./internal/supervision/ ./internal/api/runtimeapi/ ./cmd/aicli/commands/` → exit 0
- `go vet` 同上三个包 → 干净
- `go test ./internal/supervision/ -count=1` → ok
- `go test ./cmd/aicli/commands/ -run 'Supervision|Completion|Close' -count=1` → ok
- `go test ./internal/api/runtimeapi/ -run 'Supervision|Completion|SupervisedRun|AgentStatus' -count=1` → ok
- 新增用例单独运行全绿（含既有 `TestLocalSupervisionToolController_SnapshotThenAckConverges`、`TestHandlerSupervisionToolController_TeamLeadUsesTeamScope` 回归）

### 6.4 未完成项

无。F1–F8 全部实施，记录见 §6.5–§6.8、§6.10、§6.13、§6.16。

### 6.5 F4：send_message 无 turn 语义可判定（2026-09-27 第二批）

**问题**：`send_message` 承诺"不触发新 turn"，但回执只有 `delivered=true`；目标空闲时消息可能永远不被消费，调用方无法从返回值判定这一风险。

**改动**（`backend/internal/toolbroker/broker.go`，`ToolSendMessage/ToolFollowupTask` 分支）：

- `send_message` 且未触发 turn 时，回执新增 `turn_started=false` 与 `next_action`：
  - 目标空闲（`result.Status.Status == "idle"`）：额外 `no_turn_expected=true`，引导"改用 `followup_task` 开 turn，或 `send_input` 中断/steer"；
  - 其它：提示"消息将在下一个自然 turn 被读取"。
- `followup_task` 不受影响（它本身触发 turn，保持原回执）。

**测试**：`TestBrokerSendMessageSummaryStatesNoTurn`（toolbroker 包，自定义 `idleSendMessageController` 覆盖 `SendMessage` 返回值；断言 idle 场景 `no_turn_expected` 与 `followup_task` 引导，`followup_task` 回执无 `turn_started`）。

### 6.6 F7：wait_agent 超时契约联动巡检原语与预算可见性（2026-09-27 第二批）

**问题**：超时返回的 `next_action` 只讲"别马上重等"，不指向便宜巡检原语；`WaitBudget` 只在耗尽（suspend）时才可见，模型无法预判额度。

**改动**：

| 文件 | 内容 |
| --- | --- |
| `backend/internal/toolbroker/types.go` | `AgentWaitResult` 新增 `wait_budget_consecutive` / `wait_budget_limit`；新增 `StampAgentWaitBudget()`（limit<=0 与 nil 为 no-op）；`SuspendAgentWaitResultForBudget` 同步盖章；两条 timeout `next_action` 追加"先取便宜证据：`read_agent_events(view=tool_progress, after_seq=…)` / `subagent_status`" |
| `backend/internal/toolbroker/broker.go` | wait 回执在 `wait_budget_limit>0` 时输出 `wait_budget_consecutive` / `wait_budget_limit` |
| `backend/cmd/aicli/commands/chat_actor_registry.go` | `Wait()` 在 `Observe` 后调用 `StampAgentWaitBudget`（未耗尽也可见） |
| `backend/internal/api/runtimeapi/session_runtime_support.go` | API 宿主等待路径同口径盖章 |

**测试**：`TestStampAgentWaitBudgetExposesObservedBudget`（盖章不改判定；nil/limit<=0 no-op；suspend 路径携带计数）、`TestFinalizeAgentWaitResultTimedOutNamesCheapProbes`（两条超时引导均含 `read_agent_events`/`subagent_status`，且保留原有前缀语义）。

### 6.7 第二批验证证据（2026-09-27）

- `go build ./internal/toolbroker/ ./cmd/aicli/commands/ ./internal/api/runtimeapi/` → exit 0
- `go test ./internal/toolbroker/ -run '…F4/F7 新用例…|WaitBudget|MessageSemantics|FinalizeAgentWaitResult' -count=1` → ok
- `go test ./cmd/aicli/commands/ -run 'Wait|Budget|SendMessage|Completion' -count=1` → ok
- `go test ./internal/api/runtimeapi/ -run 'Wait|Budget|Message|Completion' -count=1` → ok
- 全量 `go test ./internal/toolbroker/ -count=1` 中有 1 个**与本改动无关的既有失败**：
  `TestBrokerExecuteSpawnTeamNormalizesExistingReadPathsAgainstWorkspaceRoot`（Windows 风格 `.\docs\aicli` 在 Linux workspace root 下未归一化）。
  已用 `git stash` 暂存本轮 toolbroker 改动复跑确认：该用例在**未改动**状态同样失败（pre-existing / 平台相关），非本批次引入。

### 6.8 F3：failure_kind 分类（2026-09-27 第三批）

**问题**：`subagent_inspect_task` 对失败任务只给 `status=failed` + `errors[]`，"agent loop 崩溃 / 被中断 / 工具硬失败 / 供应商失败"无从区分，父代理只能读错误正文自行判断。

**改动**（`backend/internal/supervision/agent_result.go`）：

- `ReadResultPayload` 新增 `failure_kind`（omitempty），词表：`canceled | timeout | tool_error | provider_error | policy_refused | failed | unknown`；
- 新增 `ClassifyReadResultFailure(record)`：从 `Status` + `Errors[].Code/Message` 推导，保守优先（无法归类 → `unknown`，裸 failed → `failed`，成功记录 → 空）；
- `BuildReadResultPayload` 统一接线（`NoResultRecordedPayload` 等直构路径不受影响）；
- **预算联动**：`enforceReadResultBudget` 将 `failure_kind` 作为最低优先级收缩项（列表摘除后仍超预算时才清除），保证 `max_chars` 契约不被新增字段破坏（回归 `TestBuildReadResultPayloadMaxCharsBudget` 已复绿）。

**测试**：`TestClassifyReadResultFailure`（9 个分类用例，含实测两个现场：`context canceled` → canceled、`path not found` → tool_error、policy refusal → policy_refused）、`TestBuildReadResultPayloadCarriesFailureKind`（payload 接线 + 成功为空）。

### 6.9 第三批验证证据（2026-09-27）

- `go build ./internal/... ./cmd/...` → exit 0（全量后端包编译）
- `go test ./internal/supervision/ -count=1` → ok（含新增 F3 用例与既有 `MaxCharsBudget` 回归）
- `go test ./internal/toolbroker/ -run 'ReadResult|InspectTask|StampAgentWaitBudget|SendMessage' -count=1` → ok
- `go test ./internal/api/runtimeapi/ -run 'ReadResult|InspectTask|Subagent' -count=1` → ok
- `go test ./cmd/aicli/commands/ -run 'ReadResult|InspectTask|Supervision' -count=1` → ok

### 6.10 F6：批次收敛一次调用 + 多轨 id 解析（2026-09-27 第四批）

**问题**：批次完成后收敛提示逐个列子会话，N 个子代理需 N 次 `close_agent`；`batch_id` 不是任何解析路径的合法输入。

**改动**（复用"多目标 close"既有机制，不新增工具）：

| 文件 | 内容 |
| --- | --- |
| `backend/internal/toolbroker/broker.go` | `close_agent` 新增 `batch_id` 参数（schema + 解析回落 `id`/`session_id` 之后）；描述明确"一次调用关闭整批" |
| `backend/cmd/aicli/commands/chat_actor_registry.go` | `resolveLocalAgentCloseTargets` 在注册表解析之后、会话列表回落之前新增批次解析：`ListTasks` → 去重 `ChildSessionID`（无子会话任务忽略），返回 `(首个会话, 全部会话, ok)`；`Close` 沿用既有循环逐个 stop+标记 closed，聚合 `ClosedCount/ClosedSessionIDs` |
| `backend/internal/api/runtimeapi/session_runtime_support.go` | API 宿主同口径 `resolveCloseTargetsFromBatch`（`peekSubagentBatchStore` 避免读路径建库） |
| `backend/cmd/aicli/commands/chat_actor_batch_converge.go` | 收敛提示改为一次调用形态：`converge with one close_agent call on the batch: close_agent(batch_id="…")`，子会话清单降级为 fallback |

**契约**：`close_agent(batch_id=...)` ≡ `close_agent(id=<batch id>)`；不存在的 id 仍回落到普通会话解析（best-effort，不改变原有报错语义）；批次内**运行中**子会话也会被关闭——"关闭批次"即显式停止，不做静默筛选。

**测试**

- broker：`TestBrokerCloseAgentAcceptsBatchID`（宿主收到 batch id；schema 文档化 `batch_id`）
- CLI：`TestLocalAgentCloseTargetsResolveBatchID`（`convergingBatchTasks`：child-1/child-2 去重展开，task-3 无子会话、task-4 重复条目不泄漏；未知 id 不按批次解析）；既有 `TestLocalBatchConvergeHintRendersToolAndChildSessions` 更新为钉住一次调用形态
- API：`TestSessionAgentControllerResolveCloseTargetsFromBatch`（对等能力）

### 6.11 第四批验证证据（2026-09-27）

- `go build ./internal/toolbroker/ ./cmd/aicli/commands/ ./internal/api/runtimeapi/` → exit 0
- `go test ./internal/toolbroker/ -run 'CloseAgent|Close|Batch' -count=1` → ok
- `go test ./cmd/aicli/commands/ -run 'Close|Batch|Converge|Supervision' -count=1` → ok
- `go test ./internal/api/runtimeapi/ -run 'Close|Batch|Subagent' -count=1` → ok
- 新增 3 个用例单独运行全绿（F6 broker/CLI/API 各一）

### 6.12 剩余项（已过期：F5 → §6.13，F8 → §6.16）

- ~~**F5** worktree 只读审批减负：涉及安全边界，待与 `subagent-readonly-boundary-transparency-plan-20260917.md` 联合评审后实施；~~ → 已按「只读放行、写路径保持受限」的最小授权口径实施（§6.13）。
- ~~**F8** `apply_agent_worktree` 实测用例：待补 e2e/runbook（主树可见 + 冲突拒绝 + next_action）。~~ → 已补 worktree 层语义 + broker 契约用例（§6.16）；真实会话 e2e/runbook 仍未做。

### 6.13 F5：worktree 子代理读主仓库免审批（2026-09-27 第五批）

**安全决策（按最小授权口径）**：采纳建议 1 的**只读**变体——把主仓库登记为「只读外部根」，只豁免读能力（`view`/`grep`/`glob` 等不带 `CapWriteFS` 的调用），**写路径仍逐次走 `external_dir:admit` 审批**；未采纳"批量审批合并"（建议 2），因为只读豁免已经在源头消除审批热点，不需要放宽审批粒度；未触碰写路径（建议 3 的红线）。

**改动**

| 层 | 文件 | 内容 |
| --- | --- | --- |
| 上下文 | `internal/toolctx/read_only_roots.go`（新增） | `WithReadOnlyRoots` / `ReadOnlyRoots`：与 `AllowedRoots`（读写皆放行）分离的按-run 只读根集合；去重、空集不改 ctx、返回值只读拷贝 |
| 策略门 | `internal/policy/external_dirs.go` | `externalPathExempt` 在 `!CapWriteFS` 分支中有序追加 ctx 只读根判定：读豁免、写仍 ask |
| Agent 绑定 | `internal/agent/tool_exec_middleware.go`、`approved_tool.go`、`loop.go` | 新增 `toolReadOnlyRootsForAgent`（读 agent 选项 `read_only_roots`），在普通工具调用与 approved 重放两条路径都绑定到 toolctx（与 allowed_roots 同位置） |
| CLI 宿主 | `cmd/aicli/commands/chat_actor_host.go` | `chatReadOnlyRoots(session)`：仅当 `isolation=worktree` 且 `worktree_repo_root` 非空时返回该根；在 `buildLocalChatAgent` 与每轮同步（与 allowed_roots 同块）两处注入 `agentConfig.Options["read_only_roots"]` |
| API 宿主 | `internal/api/runtimeapi/session_runtime_support.go`、`handler.go` | `apiSessionReadOnlyRoots(session)` 同口径；在 `agentConfig.Options["workspace_path"]` 注入点旁注入 `read_only_roots` |

**契约**

- 只读根只对**读**生效：策略门先判 `CapWriteFS`，写调用视同普通外部目录（一次 `external_dir:admit`，记录进会话准入根）；
- 仅 worktree 隔离子会话生效，基会话/其它隔离模式/缺 `worktree_repo_root` 一律为空，不扩大普通会话的读边界；
- 与 `allowed_roots`（会话级准入集合）正交：登记只读根不写会话准入集合，`/add-dir` 不受影响。

**测试**

- `internal/toolctx`：`TestReadOnlyRootsRoundTrip`、`TestReadOnlyRootsAreIndependentFromAllowedRoots`
- `internal/policy`：`TestExternalDirGateContextReadOnlyRootsExemptReadsOnly`（读 → `StageReadonlyAuto` + 0 次审批；写 → 1 次审批且仍记录准入）
- `internal/agent`：`TestToolCallContextBindsReadOnlyRoots`（普通路径 + approved 重放均绑定；不混入 allowed roots）
- `cmd/aicli/commands`：`TestChatReadOnlyRootsForWorktreeChild`（worktree+repo root 才返回；基会话/非 worktree/缺根为空）
- `internal/api/runtimeapi`：`TestAPISessionReadOnlyRootsForWorktreeChild`（API 对等）

### 6.14 第五批验证证据（2026-09-27）

- `go build ./cmd/aicli/commands/ ./internal/api/runtimeapi/ ./internal/toolctx/ ./internal/policy/ ./internal/agent/` → exit 0
- `go test ./internal/toolctx/ ./internal/policy/ -count=1` → ok（整包）
- `go test ./internal/agent/ -run 'ReadOnlyRoots|AllowedRoots|ToolCallContext' -count=1` → ok
- `go test ./cmd/aicli/commands/ -run 'BuildLocalChatAgent|ReadOnlyRoots|AllowedRoots|Worktree' -count=1` → ok
- `go test ./internal/api/runtimeapi/ -run 'ReadOnlyRoots|Worktree|AgentChat' -count=1` → ok
- `gofmt -l` 全部改动文件 → 干净

### 6.15 剩余项（已过期：F8 → §6.16）

- ~~**F8** `apply_agent_worktree` 实测用例：apply 成功主树可见 / 主树冲突时拒绝且返回 `conflicts` + `next_action`；待补 e2e/runbook。~~ → 已补用例（§6.16）；真实会话 e2e/runbook 仍未做。

### 6.16 F8：apply_agent_worktree 实测用例（2026-09-27 第六批）

**结论**：核心语义（成功主树可见 / 主树冲突拒绝 + `conflicts` + `next_action` / `force=true` 显式覆盖）在 `internal/isolation/worktree` 已有实测用例锚定：

- `TestApplyRefusesMainTreeConflicts`：`ApplyWithReport` 在 `force=false` 下返回 `*ApplyConflictError`，`conflicts=[README.md]`、`Applied=false`、`next_action` 含 `force=true`，主树本地修改原样保留、任何路径都未落地；随后 `force=true` 覆盖成功（`applied+forced`）。
- 同文件 scope 用例：`paths` 过滤只落地指定路径，`skipped_paths` 显式报告被排除的子改动且不落地。

**本批补充**（broker 工具面契约，`internal/toolbroker/tool_contract_f8_test.go`）：

- `TestBrokerApplyAgentWorktreePlumbsArgs`：`id/paths/keep/force` 原样到达宿主控制器；结果面 `applied/kept` 出现在 summary。
- `TestBrokerApplyAgentWorktreeSurfacesRefusedApply`：宿主拒绝时的可执行错误（冲突路径 + `force=true` 指引）原样上抛（`ErrorIs` 不折叠为裸 git 失败），默认 apply 保持非强制（`Force=false`）。

**接线确认**：API 宿主 `session_runtime_support.go` 的 `ApplyWorktree` 调 `handle.ApplyWithReport`，拒绝时按 H14 直接返回携带冲突与 next_action 的错误；broker 原样透传（`return nil, nil, err`）。

### 6.17 第六批验证证据（2026-09-27）

- `go test ./internal/isolation/worktree/ -run 'TestApply' -count=1` → ok
- `go test ./internal/toolbroker/ -run 'TestBrokerApplyAgentWorktree' -count=1` → ok
- 全清单收口：F1–F8 全部实施并各自带回归用例（§6.5–§6.8、§6.10、§6.13、§6.16）

### 6.18 发布前门禁结果（Linux 宿主，2026-09-27）

**方法**：对六个改动包跑整包测试；所有失败用例都在 HEAD（`e03123c8`）的干净 git worktree 上复跑做基线对照，区分「本次改动引入」与「既有红灯」。

**结果**

- 本清单六个批次的改动包与定向用例（agent / toolctx / policy / supervision / isolation·worktree / 双宿主工具面）**全绿**；新增红灯 0。
- 门禁发现 3 个**既有**红灯（与 F1–F8 无关），按「路径分隔符跨平台归一」修复并复跑转绿：
  1. `TestBrokerExecuteSpawnTeamNormalizesExistingReadPathsAgainstWorkspaceRoot`：`resolveSpawnTaskPath` / `normalizeSpawnPaths` 统一 `\` → `/`，`.\docs\aicli` 在 Linux 上不再退化为字面文件名；
  2. `TestResolvePlanPreviewPathRejectsTraversal`：`resolvePlanPreviewPath` 先归一分隔符，`..\secret.md` 正确命中「escapes workspace」（安全侧收益）；
  3. `TestHandleChatWebAPISessions_ScopeAllMergesPeers`：`chatWebWorkspaceName` 改为分隔符无关取末段（`E:\ws\one` → `one`），测试种子与产品同口径。
- 剩余 7 个红灯同样在 HEAD 基线复现（**既有**，Linux 平台性期望差异，建议独立工作流处理，不在本清单范围）：
  - `internal/toolbroker`：`TestTaskMonitorReportsFinishedJobAsContent`（后台 `echo ok` 任务在本机终态 failed，用例期望 completed）、`TestReliabilityEvalBrokerTimeoutRetryUsesNewInvocationWithoutDuplicateSideEffect`、`TestBrokerBackgroundAliasSurvivesManagerRestart`（后两者为本机后台任务生命周期环境差异，已在 HEAD 基线复现）；
  - `cmd/aicli/commands`：`TestRunChatLoopInteractiveInitialPromptSubmitsOnceAndStaysInteractive`、`TestRunChatLoop_DrainsQueuedLinesAfterTeamSettlesBeforePrompt`、`TestBuildChatSurfaceStatusLine_DedupesProjectWhenSameAsDirectory`、`TestComposeLocalChatSystemPrompt_IncludesWorkspaceGuidance`（Windows 路径/交互终端相关期望在 Linux 上不成立）。

### 6.19 复核修正（2026-09-27 复核批次）

复核（工作区逐项核验 + 完整 `go test ./internal/toolbroker/` + HEAD 基线对照）发现并修复 1 个本清单引入的红灯与 1 处格式漂移：

1. **F6 参数面漂移（本清单引入的红灯）**：`close_agent` 的 schema/描述宣传 `batch_id`（`broker.go`），执行分支也读取（`broker.go:2489`），但参数审计表遗漏——`TestBrokerToolArgKeys_MatchToolDefinitions` 失败（`advertises "batch_id" ... but the broker never reads it`），且 live 注记逻辑会把 `batch_id` 误报为 ignored argument。修复：`broker_arg_audit.go` 的 `ToolCloseAgent` allowlist 与 `broker_arg_kinds.go` 的 kind 表补入 `batch_id`（string），并在 `TestBroker_Execute_SupportedArgumentsAreNotReported` 增加 `batch_id` 断言（不再被视为 unsupported）。
2. **gofmt 漂移**：`internal/supervision/agent_result.go`（F3 新增字段破坏结构体对齐）已恢复 gofmt 干净。
3. **§6.18 红灯清单勘误**：toolbroker 的既有红灯实为 3 个（已更新该清单），其中 2 个后台任务用例在 HEAD `e03123c8` 基线同样失败。

**复核验证（2026-09-27）**：

- `go test ./internal/toolbroker/ -run 'TestBrokerToolArgKeys|TestBrokerToolArgKinds|TestBrokerCloseAgentAcceptsBatchID|TestBroker_Execute_SupportedArgumentsAreNotReported|TestBroker_Execute_ReportsIgnoredArguments|TestAnnotateIgnoredBrokerToolArgs|TestBrokerSendMessageSummaryStatesNoTurn|TestStampAgentWaitBudgetExposesObservedBudget|TestFinalizeAgentWaitResultTimedOutNamesCheapProbes|TestBrokerApplyAgentWorktree' -count=1` → 全 PASS；
- `go test ./internal/toolbroker/ -count=1` → 仅剩 3 个已在 HEAD 基线复现的既有红灯，无新增；
- `go test ./internal/supervision/ -count=1` → ok；
- `go vet ./internal/toolbroker/ ./internal/supervision/` → 干净；计划相关改动文件 `gofmt -l` → 干净。
