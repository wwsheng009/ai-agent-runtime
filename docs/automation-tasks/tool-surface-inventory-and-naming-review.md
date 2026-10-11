# 工具面梳理与命名规范（最佳实践版）

> 范围：`backend/internal/toolbroker`（模型命令面）+ `backend/internal/toolkit/tools`（文件/代码工具）+ `internal/toolbroker/supervision_tools.go`（控制面）。
> 目的：**先全量梳理**，再按工程与 LLM 工具调用最佳实践**确定命名方案**，并给出迁移与强制校验。
> 结论：`schedule_*` 命名自洽；**`task_*` 被 3 个域重载**必须调整；统一为 **`<domain>_<verb>[_<object>]`**，旧名保留为兼容别名并分批退役。

---

## 1. 全量工具清单（按域，现状）

### 1.1 后台作业（background jobs）
`background_task`、`task_output`、`task_kill`、`task_monitor`、`task_control`、`write_stdin`（别名 `task_input`）。

### 1.2 计划任务（Schedule，本方案新增）
`schedule_create`、`schedule_list`、`schedule_update`、`schedule_delete`。✅ 自洽。

### 1.3 团队任务（workflow / team）
`spawn_team`、`wait_team`、`send_team_message`、`read_mailbox_digest`、`read_task_spec`、`read_task_context`、`report_task_outcome`、`block_current_task`。

### 1.4 子代理（subagent）
`spawn_agent`、`list_agents`、`wait_agent`、`read_agent_events`、`close_agent`、`resume_agent`、`resolve_agent_approval`、`apply_agent_worktree`、`discard_agent_worktree`、`send_message`、`followup_task`、`send_input`、`subagent_status`、`subagent_inspect_task`、`subagent_ack_lifecycle`、`subagent_control`。

### 1.5 计划模式（plan）
`enter_plan_mode`、`exit_plan_mode`、`plan_review`。

### 1.6 核心交互 / 工具
`ask_user_question`、`todos`。

### 1.7 文件与代码（toolkit，含别名）
`shell`/`bash`/`execute_shell_command`、`view`/`read`、`grep`/`rg`、`glob`、`ls`、`edit`、`write`、`append_write`、`apply_patch`、`multiedit`、`download`、`fetch`、`sourcegraph`、`web_search`、`artifact_read`、`aicli_exec`、`openai_image_generate`（别名 `image_generate`）、`code_search`/`code_inspect`/`code_navigate`/`code_references`/`code_callers`。

### 1.8 监督控制面
广告：`subagent_status`、`subagent_inspect_task`、`subagent_ack_lifecycle`、`subagent_control`；已退役仍可调用：`supervision_snapshot`、`supervision_descendants`、`read_agent_result`。

---

## 2. 命名问题诊断

| # | 问题 | 证据 | 域 |
|---|---|---|---|
| N1 | **`task_*` 被 3 个域重载** | `broker_arg_audit.go:87-90` 需注释区分 `job_id` vs `task_id`；`broker.go:1814` "job_id is required (task_id … as aliases)" | 作业+团队+子代理 |
| N2 | 作业域 3 词根 | `background_*` / `task_*` / `write_stdin` | 作业 |
| N3 | 团队域 4 词根 | `_team` / `team_` / `read_mailbox_*` / `task_` | 团队 |
| N4 | 子代理 2 套前缀 + 3 通用名 | `*_agent` / `subagent_*`；`send_message`/`followup_task`/`send_input` | 子代理 |
| N5 | `followup_task` 与 task 撞名 | 同上 | 子代理 |

---

## 3. 命名规范（最佳实践）

### 3.1 规则

1. **域前缀**：领域工具一律 `<domain>_...`；域取自**封闭集合** `{job, schedule, team, subagent, plan}`。
2. **形态**：`<domain>_<verb>[_<object>]`，snake_case，全小写，长度 ≤64（provider 函数名约束），不用缩写。
3. **动词/名词分工**（可读性规则）：
   - **动作/变更**用动词：`<domain>_<verb>`（如 `job_start`、`job_cancel`）。
   - **纯读取**可用名词：`<domain>_<object>`（如 `job_output`、`team_mailbox`、`subagent_status`）。
   - **带对象的动作**：`<domain>_<verb>_<object>`（如 `subagent_apply_worktree`、`team_task_report`）。
4. **动词取封闭集合**（优先）：`start, read, list, search, create/spawn, update, delete, cancel, wait, send, input, monitor, control, close, resume, approve, apply, discard, report, block, inspect, ack`。
5. **一名一能**：一个能力只有一个广告名（项目既有原则，见 `supervision_tools.go:129-137`）。
6. **核心工具不前缀**：高频通用工具（`view`/`grep`/`glob`/`ls`/`shell`/`edit`/`write`/…/`todos`/`ask_user_question`）保持无域前缀——它们是"原语"，加前缀反而损害模型既有偏好；**除核心原语外，一律域前缀**。
7. **描述自证域**：每条描述**首句必须出现域关键词**，并显式写出边界（"not X"）。

### 3.2 描述模板

```
<Verb> <object> for <domain>. <关键参数/返回>. <边界：这不是 X>.
```
- 作业：`Run a long-running background job and return a job_id. … This is not a reminder.`
- 计划任务：`Create a reminder in the current session … This is not a background job; nothing runs until it fires.`
- 团队：`… for the active team run. …`
- 子代理：`… a spawn_agent child session. …`

---

## 4. 目标命名映射表

### 4.1 后台作业 → `job_*`

| 现名 | canonical | 兼容别名 |
|---|---|---|
| `background_task` | `job_start` | `background_task` |
| `task_output` | `job_output` | `task_output` |
| `task_kill` | `job_cancel` | `task_kill` |
| `task_monitor` | `job_monitor` | `task_monitor` |
| `task_control` | `job_control` | `task_control` |
| `write_stdin` / `task_input` | `job_input` | `write_stdin`, `task_input` |
| （新增） | `job_list` | — |
| （新增） | `job_search` | — |

### 4.2 计划任务 → `schedule_*`（不变）

`schedule_create` / `schedule_list` / `schedule_update` / `schedule_delete`。

### 4.3 团队任务 → `team_*`

| 现名 | canonical | 兼容别名 |
|---|---|---|
| `spawn_team` | `team_spawn` | `spawn_team` |
| `wait_team` | `team_wait` | `wait_team` |
| `send_team_message` | `team_send` | `send_team_message` |
| `read_mailbox_digest` | `team_mailbox` | `read_mailbox_digest` |
| `read_task_spec` | `team_task_spec` | `read_task_spec` |
| `read_task_context` | `team_task_context` | `read_task_context` |
| `report_task_outcome` | `team_task_report` | `report_task_outcome` |
| `block_current_task` | `team_task_block` | `block_current_task` |

### 4.4 子代理 → `subagent_*`

| 现名 | canonical | 兼容别名 |
|---|---|---|
| `spawn_agent` | `subagent_spawn` | `spawn_agent` |
| `list_agents` | `subagent_list` | `list_agents` |
| `wait_agent` | `subagent_wait` | `wait_agent` |
| `read_agent_events` | `subagent_events` | `read_agent_events` |
| `close_agent` | `subagent_close` | `close_agent` |
| `resume_agent` | `subagent_resume` | `resume_agent` |
| `send_message` | `subagent_send` | `send_message` |
| `followup_task` | `subagent_followup` | `followup_task` |
| `send_input` | `subagent_input` | `send_input` |
| `resolve_agent_approval` | `subagent_approve` | `resolve_agent_approval` |
| `apply_agent_worktree` | `subagent_apply_worktree` | `apply_agent_worktree` |
| `discard_agent_worktree` | `subagent_discard_worktree` | `discard_agent_worktree` |
| `subagent_status` | **不变** | — |
| `subagent_inspect_task` | **不变** | — |
| `subagent_ack_lifecycle` | **不变** | — |
| `subagent_control` | **不变** | — |

### 4.5 计划模式 → `plan_*`（可选，低优先）

`plan_enter`（was `enter_plan_mode`）、`plan_exit`（was `exit_plan_mode`）、`plan_review`（不变）。

### 4.6 核心工具（不变）

`view`/`grep`/`glob`/`ls`/`shell`/`bash`/`edit`/`write`/`append_write`/`apply_patch`/`multiedit`/`download`/`fetch`/`sourcegraph`/`web_search`/`artifact_read`/`aicli_exec`/`todos`/`ask_user_question` + `code_*`。

---

## 5. 迁移与退役策略

1. **canonical 常量为新名**；`Definitions()` 只广告新名（数据驱动过滤，同 `retiredSupervisionToolNames`，`supervision_tools.go:160`）。
2. **`normalizeToolName` 加映射**（`broker.go:4069`）：旧拼写仍可调用（同 `subagent_*` 收敛纪律，`broker.go:66-71`）。
3. **参数别名退役**：作业工具对 `task_id` 的别名保留，但经 `annotateIgnoredBrokerToolArgs` 的 `next_action` 提示改用 `job_id`。
4. **审计表同步**：`broker_arg_kinds.go` / `broker_arg_audit.go` / `broker_arg_audit_test.go`。
5. **退役窗口**：旧名进入 `retiredToolNames`（不广告、可调用）后，至少保留 2 个发布周期再考虑移除。
6. **工具面指纹**：改名改变指纹 → 活跃会话下一 turn 重建工具面（prompt cache 失效一次，可接受）。

---

## 6. 强制校验（新增命名契约测试）

新增 `backend/internal/toolbroker/tool_naming_contract_test.go`，对**所有广告的工具名**断言：

- 匹配 `^(job|schedule|team|subagent|plan)_[a-z0-9_]+$` **或**属于**核心原语白名单**（§4.6）；
- 长度 ≤64、全小写、无连续下划线；
- 域 ∈ 封闭集合、动作段 ∈ 封闭动词集（域工具）；
- 无两名同能力（广告名集合内无重复语义，配 `broker_arg_audit_test.go` 的 schema/实现漂移检查）。

这样后续新增工具会被契约测试拦下，**防止命名再次漂移**。

---

## 7. 分期建议

| 批次 | 内容 | 风险 |
|---|---|---|
| B0 | `schedule_*` 新增（无历史包袱） | 低 |
| B1 | **作业 `task_*`→`job_*`** + `job_list`/`job_search` | 中（N1/N2 最严重，收益最大） |
| B2 | **团队 → `team_*`** | 中 |
| B3 | **子代理 → `subagent_*`**（含高频通用名 `send_message`/`followup_task`/`send_input`） | 高（影响面最大，最后做） |
| B4 | 计划模式 `plan_*`（可选） | 低 |

每批：改 canonical + 加别名 + 更新审计表 + 跑契约测试与 `broker_arg_audit_test` + 更新 `docs/tool-catalog`。

---

## 8. 结论

1. **计划任务（`schedule_*`）无需调整**。
2. **命名统一为 `<domain>_<verb>[_<object>]`**，域封闭集合 `{job, schedule, team, subagent, plan}`，动词封闭集合（§3.1）；纯读取允许 `<domain>_<object>`。
3. **必须调整**：作业→`job_*`、团队→`team_*`、子代理→`subagent_*`；**核心原语不加前缀**。
4. **旧名一律保留为兼容别名**，分批退役；新增**命名契约测试**防漂移。
