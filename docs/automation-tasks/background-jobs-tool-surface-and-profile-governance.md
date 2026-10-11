# 后台作业工具面与 Profile 治理方案

> 议题：是否为后台作业补充工具（创建/查看/列表/搜索结果）？是否用专门 cmd + skill 供外部 LLM API 调用？如何用配置 + profile 管理工具面、避免"工具面爆炸"？以及 **`task_*` 与 `schedule_*` 的命名/描述区分**。
>
> 结论速览：**补 2 个工具（`job_list`/`job_search`），不补"定义作业"工具；后台作业工具 canonical 改为 `job_*` 家族（`task_*` 保留为兼容别名），与 `schedule_*`（自动化任务）在名字与描述上彻底分开；桥接优先复用 MCP 单入口；工具面治理复用项目已有的 profile 工具策略 + `knowledge.code_tools` 式配置门控，并新增"工具分组"。**
>
> 本文与 [`analysis-and-implementation-plan.md`](./analysis-and-implementation-plan.md) 同属"自动化"族。

---

## 1. 现状核验（先看已有什么）

### 1.1 后台作业工具（`internal/toolbroker/broker.go:33-43`）

| 工具（现状） | 语义 | 状态 |
|---|---|---|
| `background_task` | 创建并启动作业 | ✅ 已有 |
| `task_output` | 读输出/健康/quiet/wait | ✅ 已有 |
| `write_stdin`（别名 `task_input`） | 交互式会话写 stdin | ✅ 已有 |
| `task_kill` | 取消（全树） | ✅ 已有 |
| `task_monitor` | 布一次到点检查 | ✅ 已有 |
| `task_control` | pause/resume/abandon/requeue | ✅ 已有 |
| 列表 / 搜索 | — | ❌ 缺失（见 §3） |

### 1.2 HTTP 面已完整（`internal/api/runtimeapi/background_handlers.go`）

`ListBackgroundJobs`（`session_id`/`status`/`limit`/`offset`）、`GetBackgroundJob`、`ListBackgroundJobEvents`、`GetBackgroundJobOutput`、`WriteBackgroundJobInput`、cancel/pause/resume/abandon/requeue。
→ **工具层落后于 HTTP 层**：列表/事件能力已有后端实现，只差工具封装。

### 1.3 工具面治理：项目**已有**的机制（不要重造）

| 机制 | 位置 | 能力 |
|---|---|---|
| Profile 工具策略 | `internal/profile/spec.go:79` `ToolPolicySpec{Allowlist,Denylist,ReadOnly,Sandbox}`（profile/agent/workspace/agentdef 四级） | 按**工具名**允许/拒绝 |
| 技能策略 | `spec.go:49` `SkillsSpec{Allowlist,Denylist}` | 按技能名允许/拒绝 |
| Agent 执行策略 | `internal/agent/tool_policy.go`、`tool_surface_binding.go` | 执行期硬门控 + 工具面指纹 |
| 工具来源标签 | `internal/toolresult/kind.go` `tool_source ∈ {broker, toolkit, mcp}` | 工具带来源 |
| MCP 工具级启停 | `internal/mcp/config/types.go` `IsToolEnabled`/`SetToolEnabled` | 逐 MCP 工具启停 |
| **配置门控先例** | `knowledge.code_tools`（off\|on，默认 off，`config/manager.go:1029`） | **"配置启用工具组"的现成范式** |
| **子集桥接先例** | `cmd/toolkit-mcp-server`（`-tools`/`-exclude`） | **"专门 cmd 供外部调用"的现成范式** |
| 别名归一先例 | `broker.go:66-71, 4069+`（`subagent_*` 家族重命名，legacy 拼写仍可调用） | **重命名 + 兼容别名的现成范式** |

---

## 2. 命名空间与描述规范（核心：`task_*` vs `schedule_*`）

### 2.1 问题：`task_*` 已被**三个域**重载

| 域 | 现有工具 | 关键参数 |
|---|---|---|
| **后台作业** | `background_task`, `task_output`, `task_kill`, `task_monitor`, `task_control`, `task_input`/`write_stdin` | `job_id`（**别名接受 `task_id`/`session_id`**） |
| **团队任务** | `read_task_spec`, `read_task_context`, `report_task_outcome`, `block_current_task`, `send_team_message` | `task_id`, `task_status` |
| **子代理任务** | `subagent_inspect_task`, `read_agent_result` | `task_id` |

项目自己就不得不为此加注释区分：`broker_arg_audit.go:87-90` 用 `job_id` vs `task_id` 解释归属；`broker.go:1814` 写着 `job_id is required (task_id/session_id are accepted as aliases)`。

→ 再引入 `schedule_*`（自动化任务）后，模型面对 `task_*`（作业）、`task_*`（团队）、`schedule_*`（提醒）三套，"task"一词**零信息量**，正是"工具面爆炸 + 语义混淆"的根因。

### 2.2 建议：后台作业 canonical 改为 `job_*` 家族

理由：后台作业的**参数（`job_id`）、HTTP 域（`/api/runtime/background/jobs`）、Go 类型（`background.Job`）**早已统一叫 **job**；只有**工具名**还停在 `task_*`。改名让"名"与"域"对齐，并把 `task_*` 词根留给团队任务。

| 现有名 | 建议 canonical | 兼容别名（`normalizeToolName` 映射） |
|---|---|---|
| `background_task` | **`job_start`** | `background_task` |
| `task_output` | **`job_output`** | `task_output` |
| `task_kill` | **`job_cancel`** | `task_kill` |
| `task_monitor` | **`job_monitor`** | `task_monitor` |
| `task_control` | **`job_control`** | `task_control` |
| `write_stdin` / `task_input` | **`job_input`** | `write_stdin`, `task_input` |
| （新增） | **`job_list`** | — |
| （新增） | **`job_search`** | — |

- `schedule_*` 保持**独立家族**（自动化任务），不与作业共用任何词根。
- 团队任务工具保持 `read_task_spec` 等（`task_*` 词根归团队）；后续可选统一为 `team_task_*`（不在本方案范围）。

### 2.3 描述规范（三族描述必须自证域）

| 族 | 首句必须含 | 禁止 |
|---|---|---|
| 作业 `job_*` | "background **job**" / `job_id` | 不用 "task" 作主语；不写 "reminder" |
| 自动化任务 `schedule_*` | "**reminder**" / "schedule" / "自动化任务" | 不写 "job"、"task"（除非说明"在会话里触发一次 run"） |
| 团队任务 | "**team** task" / "team run" | 不写 "job" |

示例（作业）：
> `job_start`: "Run a long-running **background job** and return a **job_id**. Pass that exact `job_id` to `job_output`; never guess or synthesize an id."

示例（自动化任务）：
> `schedule_create`: "Create a **reminder** in the current session that delivers `prompt` when due. This is **not** a background job; nothing runs until the reminder fires and the session processes the message."

### 2.4 迁移与兼容（沿用既有先例）

1. 新增 `job_*` 常量为 canonical，`Definitions()` 只广告 `job_*`；
2. `normalizeToolName`（`broker.go:4069`）加入 `backgroundtask→job_start`、`taskoutput→job_output` 等映射，**旧拼写仍可调用**（与 `subagent_*` 重命名同一纪律，`broker.go:66-71`）；
3. `broker_arg_audit.go` 的 `task_id` 提示改为 "job tools address by `job_id`; team tools address by `task_id`"；作业工具对 `task_id` 别名保留但**在 `next_action` 里提示改用 `job_id`**（复用现有 advisory 机制），逐步退役；
4. `broker_arg_kinds.go` / `broker_arg_audit_test.go` 同步新名；
5. **影响**：工具面指纹变化 → 活跃会话下一次 turn 重建工具面（prompt cache 失效一次，可接受，与 MCP 热重载同类）。

---

## 3. 是否应该补工具：逐项评估

| 候选工具 | 建议 | 理由 |
|---|---|---|
| `job_list`（`task_list`） | ✅ **补** | 高频、低 token（返回摘要）；HTTP 已有；让模型不必靠 `job_output` 猜 id |
| `job_search`（`task_search`） | ✅ **补（有界）** | 搜索作业输出/事件；**强制分页 + 结果上限**，否则把大量输出灌进上下文 |
| `job_define`（定义模板） | ⛔ **不补为工具** | "定义"属**部署期配置**（profile / 配置模板），运行时创建可复用命令模板会引入权限与审计盲区 |
| `job_events` | ➖ 可选 | 并入 `job_search`（按 `job_id` 过滤），不单开工具 |
| 聚合统计 | ➖ 不补 | 属运维面（HTTP/UI） |

**判据**：模型**运行时**需要且**结果有界**才进工具面；配置/运维/聚合走 HTTP/UI。

---

## 4. 工具分组（Tool Group）：治理"工具面爆炸"的核心

### 4.1 为什么需要"组"

现有 `ToolPolicySpec.Allowlist/Denylist` 只能**逐名**列举。工具持续增长时：profile 作者要维护长串名字（易漏易漂移），且无法表达"要基础能力、不要管理能力"。→ 引入**工具组**作为稳定契约，工具增减由组内维护。

### 4.2 建议分组

| 组 | 成员 | 默认 |
|---|---|---|
| `jobs.core` | `job_start`, `job_output`, `job_cancel` | **开** |
| `jobs.interactive` | `job_input`, `job_monitor` | 关 |
| `jobs.query` | `job_list`, `job_search` | 关 |
| `jobs.admin` | `job_control` | 关 |
| `schedule.core` | `schedule_create`, `schedule_list`, `schedule_delete` | 按 profile |
| `schedule.admin` | `schedule_update` | 按 profile |
| `team` | `spawn_team`/`wait_team`/team 工具 | 开 |

### 4.3 配置门控（对齐 `knowledge.code_tools`）

```yaml
background:
  tools:
    group: core          # off | core | query | full（默认 core，零行为变化）
    enable: [job_list]   # 组之上细粒度覆盖（与 group 求交）
    disable: [job_monitor]
```

- `off` → 一个作业工具都不注册（对齐 `knowledge.code_tools=off` 的零行为变化纪律）；
- `core`（默认）→ 仅 core，**不改变现有默认工具面**；
- 加载期校验：未知 group **必须加载期拒绝**（对齐 `ParseCodeTools`）。

---

## 5. cmd + skill 桥接（供外部 LLM API 调用）

- ❌ 为作业单独写 `cmd/jobs-mcp-server` → 与 `toolkit-mcp-server` 重复、工具面分裂。
- ✅ **扩展 `toolkit-mcp-server` 增加 `--groups jobs.core,jobs.query`**（复用 §4 分组），一次解决所有内置工具子集暴露。
- ✅ 新增/扩展 **skill**（如 `.agents/skills/background-jobs/SKILL.md`）说明工作流（start→轮询/监控→读输出→取消），指向 MCP 或 runtime HTTP；skill 做**用法说明**，不替代工具定义。
- 外部 LLM API 走 MCP；本机 aicli 走内置工具面——**同一分组契约**。

---

## 6. Profile 管理设计

### 6.1 三级模板（对齐 `internal/profile/templates/`）

| 模板 | 工具组 |
|---|---|
| `minimal` | 无 jobs/schedule |
| `coding` | `jobs.core` + `jobs.interactive` + `schedule.core` |
| `ops` | `jobs.full` + `schedule.admin` + `team` |

### 6.2 语义

```yaml
# profile.yaml
tools:
  denylist: [job_search, job_control]   # 已有字段，无需新增
```

**解析链**：部署组 → profile allow/deny → agent/workspace 覆盖 → 只读子代理能力域（现有 `agent.ToolExecutionPolicy`）。profile 只能**收窄**部署组；`denylist` 永远优先于 `allowlist`。

---

## 7. 分期与验收

> 本节是**工具面治理的独立批次**（J0–J3），与 Schedule 的 P0–P6 路线图无关，不要混淆。

| 批次 | 交付 | 验收 |
|---|---|---|
| **J0** | `job_*` 重命名 + `task_*` 兼容别名；`job_list` | 旧拼写仍可调用；模型按会话列出作业；工具面指纹更新一次 |
| **J1** | `job_search`（有界分页）；工具分组 + `background.tools.group` | 搜索强制分页与上限；`group: off` 零工具；未知 group 加载期拒绝 |
| **J2** | `toolkit-mcp-server --groups`；profile 模板 + skill | 外部 MCP 客户端按组拿子集；三模板可 `profile create` |
| **J3** | 观测：工具面指纹/分组命中统计 | 复用 `tool_surface_binding.go` 指纹 |

---

## 8. 与自动化任务（Schedule）方案的一致性

- `schedule_*` 采用**同一分组机制**（`schedule.core`/`schedule.admin`），不各自发明门控；
- 两者共用"部署组 → profile → agent"解析链与 `tool_source` 标签；
- **创建类能力**（`job_start`、`schedule_create`）在只读子代理能力域中默认收紧（对齐 `policy/capability_scope.go`）。

---

## 9. 直接回答

1. **命名区分**：后台作业 canonical 改 `job_*`（与 `job_id`/`background_jobs` 域对齐），`task_*` 保留为兼容别名；`task_*` 词根归团队任务；`schedule_*` 独立家族。**三族描述首句必须自证域**（§2.3）。
2. **是否补工具**：补 `job_list`、`job_search`；**不补**"定义作业"工具（属配置）。
3. **cmd + skill**：**扩展 MCP 单入口**（`toolkit-mcp-server --groups`），不按域新建 cmd；skill 做用法说明。
4. **配置启用**：`background.tools.group`（off\|core\|query\|full），加载期校验，默认 `core`——照抄 `knowledge.code_tools`。
5. **profile 管理**：复用现有 `ToolPolicySpec{Allowlist,Denylist}`，profile 只能收窄；提供 minimal/coding/ops 模板。
6. **避免爆炸**：引入**工具组**作为一等契约，配合来源标签与工具面指纹做观测。
