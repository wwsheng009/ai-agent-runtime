# CommandCode Agents 文档设计借鉴分析

- **日期**：2026-09-26
- **来源**：<https://commandcode.ai/docs/agents>（Command Code "Custom Agents" 文档全文：内置代理、三种创建路径、`/agents` 管理器与手动向导、委派机制（并行/隔离/独立模型/一层深度）、Background runs 与 `agent_output`、加载来源与优先级、reserved names、frontmatter 全量参考、tools/models/reasoningEffort/maxTurns/permissionMode/background/showOutput 语义、完整示例）
- **对照对象**：本仓库 aicli 的 agents 实现（本次以只读方式完成代码盘点，全部结论附 `文件:行号`）：
  - 定义层：`backend/internal/agentdef/{definition,parse,discover,builtin,validate,build,teammate}.go`
  - 委派/调度：`backend/internal/agent/{agent,scheduler,child_factory,subagent_batch_coordinator,subagent_plan,subagent_retry,turn_budget,loop}.go`、`backend/internal/subagentbatch/**`
  - 工具面/策略：`backend/internal/toolbroker/{broker,spawn_agent_arg_types,spawn_agent_permission,agent_mailbox}.go`、`backend/internal/policy/tool_policy.go`
  - 宿主/CLI：`backend/cmd/aicli/commands/{chat_actor_host,chat_debug,chat_agent_transcript,chat_agent_cleanup,chat_profile,chat_setup,chat}.go`
  - 文档：`docs/aicli/agents.md`、`docs/multi-agents/**`
- **结论口径**：区分「值得借鉴（缺口）」「已有且更强（不要回退）」「同名异构（语义差异需权衡）」；每条建议给出代码落点、优先级与验证方式。

> 系列文档：`commandcode-plan-mode-design-borrowing-20260925.md`（plan mode）、`commandcode-mcp-design-borrowing-20260925.md`（MCP）、`commandcode-interactive-mode-design-borrowing-20260925.md`（交互模式）、`commandcode-permissions-design-borrowing-20260926.md`（权限）。

---

## 1. TL;DR：可借鉴清单

| # | 主题 | CommandCode 做法 | aicli 现状 | 建议 | 优先级 |
|---|------|------------------|------------|------|--------|
| 1 | **代理目录暴露与 description 路由** | `description` 是"何时委派给我"的匹配文本；代理文件在下一回合被扫描，模型能看到代理集合 | `Definition.Description` 仅在 body 为空时兜底成 prompt（`agentdef/build.go:147-153`）；`spawn_agent.agent_type` 是无 enum 的开放字符串（`toolbroker/broker.go:399`），`spawn_subagents` 条目**没有** `agent_type` 字段（`agent/loop.go:7105-7166`）；全仓未找到把 discovery Catalog 注入提示/工具 schema 的代码 | 会话启动或 spawn 工具描述里注入 catalog（name + description + source/scope + 安全摘要）；`spawn_subagents` 条目支持 `agent_type`；description 成为委派路由提示 | **P0** |
| 2 | **`/agents` 定义管理器与创建向导** | `/agents` 管理器：Ask CC 创建（自然语言写文件）或手动向导（location→identifier→prompt→description→tools 分类→model→confirm），按 User/Project/Default 分组，Default 只读 | 本仓库 `/agents` **同名不同义**：是运行中子代理协作面板（panel/pick/view/send/followup/routing/cleanup，`chat_debug.go:275-323`）；未找到代理定义的创建/编辑/删除/浏览入口（`agentdef` 只有读取路径） | 新增定义管理面：`/agents defs [list/show/validate]` + `aicli agents new`（模板/校验/分组）；自然语言创建交由主代理写 `.agents/agents/<name>.md`（配 skill 或工具约束） | **P0** |
| 3 | **per-definition `maxTurns`（默认 100）** | frontmatter 字段，封顶单个代理循环 | `Definition` 无该字段（`agentdef/definition.go:42-60`）；循环上限来自运行配置 `Config.MaxSteps`，非正数=不限（`agent/agent.go:28-34,149-153`）；`spawn_subagents` 只有 per-task `budget_tokens`/`timeout`（`loop.go:7150-7161`） | Definition 增加 `maxTurns`（或 `turnBudget`），在 child/teammate 构建时映射到 `LoopReActConfig.MaxSteps`；与 turn_budget（steps/wallclock/tokens，`agent/turn_budget.go:44-53`）共享水位判定 | **P1** |
| 4 | **per-definition `reasoningEffort` + 两级校验/回退** | 加载期未知档位丢弃并告警，运行期按解析出的模型校验，不支持则回退模型默认 | 本地 **事件级**支持 `reasoning_effort`（spawn_agent schema `broker.go:413`；spawn_subagents enum low/medium/high `loop.go:7125`；路由 profile 按 difficulty 决定 `child_factory.go:38-71,148`）；routing 层**已有**模型能力校验与 `ignore|downgrade|fail` 策略（`modelrouting/resolver.go:552-581`、`types.go:471-508`），但 `Definition` 无字段、`types.NormalizeReasoningEffort` 只做 TrimSpace（`types/thinking.go:33-35`）、routing 关闭时不执行兼容校验（`resolver.go:16-17` 直返） | Definition 增加 `reasoningEffort`；加载期做枚举校验（未知告警并清空）；组装 task hint 时复用现有 `applyReasoningCompatibility` 策略；确保 routing 关闭路径同样执行模型能力校验 | **P1** |
| 5 | **per-definition `background` + 单一 `agent_output` 收集语义** | `background:true` 使每次运行分离：`agent` 工具立即返回 `agent_id`，之后用一个 `agent_output` 完成 wait/status/kill | 本地 `spawn_subagents` 默认异步、SQLite 持久批处理、可跨重启恢复（`agent/agent.go:310-399`、`subagent_batch_coordinator.go:1-105`），`spawn_agent` 也天生异步（子 actor 异步执行）；但 `Definition` 无 `background`，`spawn_agent` schema 无 background/async 键（`spawn_agent_arg_types.go:44-72`），收集面被拆成 wait_agent/read_agent_events/read_agent_result/subagent_status/close_agent 等 20+ 工具（`broker.go:33-76`） | Definition 增加 `background`；`spawn_agent` 支持显式 background 键（当前语义已异步，补开关/文档即可）；对外维持细粒度工具的同时，提供 `agent_output` 式三合一便捷入口（或 `/agents` 面板聚合成一个"等待/状态/终止"动作） | **P1** |
| 6 | **`showOutput`（最终消息原文入 feed）** | `true` 时把子代理最终消息原文展示在 feed，而不是一行 "done" | 本地有有界父侧摘要（8KiB 预算、去重/冲突标注，`subagent_parent_summary.go:13-42`）、`read_agent_result`/`subagent_inspect_task` 有界读取、`/agents view` transcript；未找到 per-def/会话级"原文回显最终消息"开关 | Definition 增加 `showOutput`（或 session 开关/CLI flag），开启时把子代理最终 assistant 消息原文作为事件镜像进父 feed，默认仍走有界摘要 | **P1** |
| 7 | **tools 语义：省略=无工具、`"*"`=全部（含 MCP）、向导按类别选** | 安全默认（fail-closed）；类别分组：Read-only/Edit/Execution/Search/Other | 本地省略 `tools` = **继承全量**（`builtin.go:33-38` general 注释"inherit full toolkit"）；未找到 `"*"` 通配（`normalizeStringSlice` 只去重，`definition.go:115-137`）；无按 capability 分类的选择向导（能力分类体系已存在，`policy/capability.go`） | 支持 `tools: "*"`；对第三方/项目 def 的"省略工具"是否 fail-closed 给显式策略（至少文档+lint 警告）；为向导复用 capability taxonomy 做分组 | **P2** |
| 8 | **reserved names 与覆盖规则显式化** | 保留名（explore/plan/review/general）自定义文件被忽略；加载顺序 "first definition wins" | 本地允许 user/project/profile 覆盖 builtin（`discover.go:62-68,143-157`；文档 `docs/aicli/agents.md:102` "同名时项目/profile 可覆盖 builtin"），且无保留名概念（`validate.go` 不校验）；模型可见面没有覆盖告警 | 二选一：保持"可覆盖"但输出显著告警（Agent Source 已可见，`chat.go:1108-1115`）；或引入受保护的保留名（至少 `general`）防止误改默认行为。建议先做 lint/告警，不贸然禁止 | **P2** |
| 9 | **定义浏览的 scope 分组 + 热加载观测** | `/agents` 按 User agents / Project agents / Default agents(read-only) 分组；文件每回合重扫，增删改立即生效 | 本地有 `Agent Source: project · <path>` 元信息行（`chat_setup.go:737-743`、`chat.go:1108-1115`）、profile 只读视图会解析 agentdef（`api/runtimeapi/profiles_view_groups.go:151-198`）；但没有统一的 "有哪些 agent 定义/来自哪层/本回合是否重扫" 列表面；热加载实际发生在每次 `Resolve`（`toolbroker/broker.go:4157,4175`，无缓存），spawn 下一次即生效 | 增加 `aicli agents list`（builtin/user/project/profile 分组 + source path + 覆盖关系）与 `/agents defs` 只读视图；文档明确"定义在每次解析/spawn 时重扫，无需重启" | **P2** |
| 10 | **文档 IA：创建路径与后台章节** | 文档结构：Overview→Quick start→Built-in→Creating→How it works→Background runs→Loading→Reference→Full example | `docs/aicli/agents.md` 已有三层说明、字段表、发现顺序、CLI/spawn 接线、验收命令；**缺**"如何创建/维护代理定义"的推荐路径与"后台批处理/收集结果"章节 | 补 `README` 式 Quick start（Ask/手写两种路径）、`background/agent_output` 对应本地术语章节、`/agents` 两套语义（运行时 vs 定义管理）澄清 | **P2** |

> 已有且**不应回退**的能力（详见 §5）：一层深度的多层硬闸（默认 `maxDepth=1` + `BlockDelegation` + 本地深度门禁）、SQLite 持久后台批处理与监督控制面、任务级重试/熔断/全局并发限制、read-only 能力域与权限天花板（父模式钉住）、profile 适配与 folder-trust、worktree 隔离、`completionRequirement`/`skills`/`promptMode`/`sandbox` 等本地扩展字段。

---

## 2. CommandCode 文档要点提炼

以下均来自文档原文行为（不含推测），按可借鉴维度归纳。

### 2.1 代理模型与内置代理

- 子代理 = 独立 worker：**独立 context window + 独立工具集 + 独立 system prompt +（可选）独立模型**；可以同时运行多个。
- 内置三个只读代理常驻：**General**（默认兜底，全部工具）、**Explore**（read_file/read_directory/grep）、**Plan**（read_file 等设计类工具）；不可编辑/删除。
- `agent` 工具委派未点名时默认落到 **General**。

### 2.2 三种创建路径

1. **Ask Command Code（推荐）**：用自然语言描述（"create a code-reviewer subagent that reviews diffs for bugs"），CC 起草 name/description/system prompt/工具集并写文件。
2. **`/agents` 管理器**：两个 create 动作 + 按 scope 分组列表（User agents / Project agents / Default agents(read-only)）。
3. **手动向导（Create manually）七步**：location（Project `.commandcode/agents/` 或 Personal `~/.commandcode/agents/`）→ identifier（唯一、非保留名）→ system prompt → description（**供 CC 匹配何时委派**）→ tools（类别开关：Read-only/Edit/Execution/Search，或 advanced 逐项）→ model（inherit 或固定）→ confirm 预览后保存。

- 新增/编辑的代理"下一回合（next turn）"即被拾取，无需重启。
- 也可直接编辑 Markdown 文件：frontmatter 配置，正文（body）就是该代理的 system prompt。

### 2.3 委派机制（How it works）

- **不是用户直接调用子代理**：用户描述任务，模型通过内置 `agent` 工具发起委派；子代理在自己的循环里执行，只把一个结果返回。
- **并行**：同一回合里发多个 `agent` 调用即可并跑（文档举例：5 个 explorer 或 3 个 agent 各改一个模块）。
- **隔离上下文**：子代理的读文件与推理留在自己的 context window，长探索不淹没主会话。
- **独立模型**：代理可固定自己的模型，慢 planner 与快 implementer 各自保留 prompt cache。
- **一层深度**：子代理不能再起子代理——`agent` 工具从子代理工具集移除（`agent_output` 同理不可授予）。

### 2.4 Background runs（后台运行）

- 运行可分离（detached）：以 background 启动（或代理声明 `background: true`）时，`agent` 工具**立即返回 `agent_id`**，主会话继续。
- 结果用 **`agent_output`** 收集：`wait`（等待）、`status`（查状态）、`kill`（终止）。

### 2.5 加载来源、优先级与保留名

| 来源 | 路径 | 说明 |
|------|------|------|
| Bundled（内置） | — | General / Explore / Plan，常驻只读 |
| Personal | `~/.commandcode/agents/` | 本机所有项目可用 |
| Project | `.commandcode/agents/` | 随仓库提交、团队共享 |

- 文档原文："They load in that order and the **first definition of a name wins**。"（按字面为首个定义生效；对内置保留名而言，自定义同名文件被忽略）
- **文件每回合重新扫描**：新增/编辑/删除立即生效。
- **保留名**：`explore`、`plan`、`review`、`general` 由内置行为占用；自定义文件用这些名字会被忽略。

### 2.6 frontmatter 全量字段

| 字段 | 类型 | 必填 | 默认 | 语义 |
|------|------|------|------|------|
| `name` | string | 是 | 文件名 | 代理 id；清洗为 `a-zA-Z0-9_-`；不得为保留名 |
| `description` | string | 否 | `""` | **何时使用该代理的匹配文本**，要求具体 |
| `tools` | string \| []string | 否 | **无工具** | `"*"`=全部（含 MCP）；否则逗号/空格分隔清单或 YAML 数组；**省略 = 没有工具**（fail-closed） |
| `disallowedTools` | string \| []string | 否 | — | 拒绝清单，在 `tools` 之后应用（**deny 优先**） |
| `model` | string | 否 | inherit | 任意 `/model` id；省略或 `inherit` 跟随会话模型；固定模型=独立 prompt cache |
| `reasoningEffort` | string | 否 | 模型默认 | **加载期**未知档位丢弃并告警（文件仍加载）；**运行期**按实际模型校验，不支持则回退模型默认（不夹到其它档位） |
| `maxTurns` | int | 否 | **100** | 代理循环上限 |
| `permissionMode` | string | 否 | inherit | `default`/`accept-edits`/`yolo`/`plan`/`dont-ask`（`auto-accept`、`bypass` 是别名）；**会话已在 plan/yolo 时会话优先** |
| `background` | bool | 否 | `false` | `true` 使每次运行分离，`agent` 立即返回 `agent_id`，结果走 `agent_output` |
| `showOutput` | bool | 否 | `false` | `true` 时把代理最终消息**原文**展示到 feed，而不是短 "done" 行 |

- 其它文档细节：工具 id 用 `/agents` 向导 advanced 列表中的名字；MCP 工具用原始名 `mcp__github__get_me`；`agent` 与 `agent_output` **不可授予**（一层深度的实现方式）。

---

## 3. aicli 现状盘点（代码证据）

### 3.1 定义层：解析、字段、校验与发现

| 维度 | 现状 | 证据 |
|------|------|------|
| 文件格式 | `.md`（YAML frontmatter + body）与 `.yaml/.yml` 纯配置；无扩展名时自动探测；`name` 缺省从文件名推导 | `agentdef/parse.go:31-86`（`:73-80` 文件名推导） |
| 字段全集 | `name, description, tools[], disallowedTools[], permissionMode, skills[], model, provider, promptMode, completionRequirement, sandbox` + body | `agentdef/definition.go:42-60` |
| 名字规范化 | 小写、空格/下划线→连字符；禁路径分隔符 | `definition.go:107-113`；`validate.go:15-20` |
| 校验 | promptMode ∈ extend/full；completionRequirement ∈ none/complete_task；permissionMode ∈ 5 模式（含 `dont-ask` 别名）；sandbox ∈ off/workspace/read-only/strict | `validate.go:22-53` |
| 发现顺序（后覆盖前） | builtin → `~/.aicli/agents/*` → 项目 `.agents/agents/*` 与 `.aicli/agents/*` → profile `agents/*/agent.yaml` → ExtraDirs；支持 folder-trust `SkipProjectRoot` | `discover.go:62-123`；覆盖语义 `discover.go:143-157`；`chat_folder_trust.go:129-130` |
| 目录布局 | 平铺 `*.md` 或 `agents/<id>/agent.yaml|yml|md` | `discover.go:188-205` |
| 内置代理 | explore/plan/general 三个代码内 stub，可被同名文件覆盖 | `builtin.go:5-44`；文档 `docs/aicli/agents.md:95-105` |
| profile 适配 | `agent.yaml`（camel+snake 别名）→ 同一 Definition；`prompts/role.md` / `system_prompt` 作为 body 兜底 | `agentdef/build.go:15-101` |
| 运行时绑定 | Definition → Binding（AgentID/Model/Provider/PermissionMode/PromptText/PromptMode/CompletionRequirement/ToolAllowlist/ToolDenylist/SkillAllowlist/Sandbox/ReadOnly）；profile 声明 `bypass_permissions` 被 D16 拒绝 | `build.go:104-172`（D16 `:129-134`）；`teammate.go:35-89` |
| 绑定消费 | session info/`--agent`、spawn 默认填充、profile 只读视图、teammate 默认 | `chat_profile.go:278-308`；`toolbroker/broker.go:4167-4204`；`api/runtimeapi/profiles_view_groups.go:151-198`；`agentdef/teammate.go:58-89` |
| 保留名 | **无**：同名用户/项目定义可覆盖 builtin；`validate.go` 不检查保留名 | `validate.go` 全文；`docs/aicli/agents.md:102` |

### 3.2 委派通道与生命周期（对照 CC 的 agent/agent_output）

本地有**两套并存的子代理通道**，加上团队通道：

| 通道 | 入口 | 运行形态 | 关键证据 |
|------|------|----------|----------|
| 单个子会话 | `spawn_agent`（broker 工具） | 宿主 actor 创建独立 ChatSession，异步执行 | `toolbroker/broker.go:40,390-424,4157-4204` |
| 批量计划任务 | `spawn_subagents`（agent loop 内建） | `SubagentScheduler` + `SubagentBatchCoordinator`（SQLite 批存储） | `agent/loop.go:7080-7174`；`agent/subagent_batch_coordinator.go:1-105` |
| 团队 | `spawn_team`/`wait_team`/team 任务图 | Team 调度与 RunMeta | `toolbroker/broker.go:52-59`；`agentdef/teammate.go` |

- **并行**：`spawn_subagents` 一次派发多任务，调度器信号量并发（默认每批 4），进程级 `GlobalLimiter` 限总额 | `agent/scheduler.go:104-148,208-221`；`cmd/aicli/commands/chat_actor_host.go:1993-2007`。
- **后台/分离**：批处理默认异步持久化：`SubagentBackgroundEnabled` 开关、协调器懒加载/注入、批 ID、心跳、deadline、幂等键、终态投递/恢复重放 | `agent/agent.go:310-399`；`subagent_batch_coordinator.go:29-105`。
- **收集/控制工具面**：`wait_agent/read_agent_events/read_agent_result/subagent_status/subagent_inspect_task/close_agent/resume_agent/send_message/followup_task/send_input/resolve_agent_approval/subagent_ack_lifecycle/subagent_control`（外加 worktree apply/discard） | 常量 `broker.go:33-76`；schema `broker.go:425-570`；`loop.go` 同级实现 |
- **一层深度（比 CC 更强）**：默认 `MaxDepth=1`；子策略 `BlockDelegation`；`IsDelegationToolName` 执行期硬拒；本地深度门禁对 `spawn_agent/spawn_subagents/spawn_team` 一起 deny | `scheduler.go:208-221`；`agent/child_factory.go:127-131`；`policy/tool_policy.go:120-122,492-499`；`cmd/aicli/commands/chat_actor_host.go:1708-1725` |
- **权限天花板**：子请求省略=继承父模式；`bypass` 父钉住 default/accept_edits 子；plan/dont-ask 父钉住一切；升权默认阻断并回 pin（`AICLI_AGENTS_ALLOW_PERMISSION_ESCALATION=1` 才放行） | `toolbroker/spawn_agent_permission.go:33-131` |
- **任务级重试/熔断**：只读瞬时失败重试（默认 2 次、封顶 5、指数退避+jitter）；连续失败打开熔断 | `agent/scheduler.go:140-148`；`agent/subagent_retry.go`（子代理报告核验） |
- **隔离上下文**：子代理独立 session/actor/提示词/循环配置；fork 需显式 `fork_context/fork_turns` | `agent/child_factory.go:60-71,119-150`；`toolbroker/broker.go:420-421` |

### 3.3 管理面与展示

| 维度 | 现状 | 证据 |
|------|------|------|
| `/agents`（CLI slash） | **运行中协作面板**：graph、pick、target、panel/follow、view transcript、send/followup、approve/deny/answer、routing test、cleanup | `chat_debug.go:269-323`；catalog `chat_slash_command_catalog.go:194-235` |
| `/agent` | 查看子代理 transcript（`/agents view` 等价） | `chat_slash_command_catalog.go:236-247`；`chat_agent_transcript.go:110` |
| `/agents cleanup` | 回收可安全关闭的子代理，释放 `agents.maxThreads` 配额 | `chat_agent_cleanup.go:62`；catalog `:229-233` |
| 启动选择/绑定 | `aicli chat --agent <name>`（无 profile → portable agentdef；有 profile → profile 内 agent id）；显式 CLI 参数（provider/model/permission-mode）优先 | `cmd/aicli/commands/chat_command.go:85`；`docs/aicli/agents.md:112-145` |
| 定义来源可观测 | session info / `/debug` 打印 `Agent Source: project · <path>` | `chat_setup.go:737-743`；`chat.go:1108-1115`；`chat_debug_document.go:222-224,801-806` |
| 定义列表/创建/编辑 | **未找到**：`/agents` 动词表中无 create/new/list-defs；无 `aicli agents`（`agent` 命令是 ACP 宿主 `aicli agent stdio`）；portable agentdef 无写路径（profile 有 `profile create`，但那是 profile 包生成） | `chat_debug.go:280-322`；`cmd/aicli/commands/agent.go:12-18`；`profile_create.go:30-79` |
| 自然语言创建代理 | **未找到**（skill-creator 面向 skill 包） | `.agents/skills/skill-creator/**` |
| 热加载 | 每次 `agentdef.Resolve` 都重新 Discover（无缓存）→ 下一次 spawn 即生效；`--agent` 在启动时解析一次 | `toolbroker/broker.go:4157,4175`；`chat_profile.go:278-308`；`discover.go:69`（无 cache） |
| 子代理结果展示 | 父侧有界摘要（默认 8KiB、按结论去重、冲突标注、溢出留 deref 指针）+ `read_agent_result` 有界读取 + transcript 面板；**无** per-def 原文回显开关 | `agent/subagent_parent_summary.go:13-42,63-101`；`chat_agent_transcript.go:110` |

### 3.4 字段对照总表（CommandCode ↔ aicli）

| CommandCode 字段 | aicli 对应 | 结论 |
|------------------|------------|------|
| `name` | `name`（更强的规范化+文件名推导） | ✅ 已有 |
| `description` | `description`（仅兜底 prompt，**未用于委派匹配/展示**） | ⚠️ 语义弱化（见 4.1） |
| `tools` | `tools`（省略=继承全量，无 `"*"`） | ⚠️ 语义相反（见 4.7） |
| `disallowedTools` | `disallowedTools` | ✅ 已有 |
| `model` | `model` + `provider` | ✅ 已有且更细 |
| `reasoningEffort` | 无 def 字段；spawn/路由支持事件级 | ❌ 缺 def 字段（见 4.4） |
| `maxTurns` | 无 def 字段；`Config.MaxSteps`（非正=不限） | ❌ 缺 def 字段（见 4.3） |
| `permissionMode` | `permissionMode`（5 模式 + 别名 + 父模式天花板 + D16 profile 约束） | ✅ 已有且更强 |
| `background` | 无 def 字段；批处理通道默认异步持久化 | ⚠️ 缺 per-def 开关（见 4.5） |
| `showOutput` | 无 | ❌ 缺（见 4.6） |
| — | `provider`、`skills`、`promptMode`、`completionRequirement`、`sandbox` | ✅ 本地扩展，保留 |

---

## 4. 重点借鉴项：设计映射与落点

### 4.1 【P0】代理目录暴露与 description 路由

- **CommandCode 语义**：代理文件每回合被扫描；`description` 就是"何时委派给我"的匹配文本，工具集/模型/权限都随代理走；模型能感知可用代理集合。
- **aicli 缺口**：
  1. `spawn_agent.agent_type` 是无 enum 的开放字符串（`broker.go:399`），工具描述只写"orchestration role hint"；`spawn_subagents` 条目**完全没有** `agent_type` 字段（`loop.go:7105-7166`），批量通道只能给 `role`（已废弃）/`task_type`，无法引用 `.agents/agents/*.md` 角色。
  2. `Definition.Description` 只被用作 body 为空时的 prompt 兜底（`build.go:147-153`），既没有进入任何模型可见清单，也不参与路由。
  3. 全仓检索 `Catalog.List|Available agents|available_agents|AgentCatalog` 无命中——没有任何地方把 discovery 结果交给模型。
- **设计映射**：
  1. 在会话/循环构建工具面时（`agent/tool_surface_binding.go` 附近的 `spawn_subagents` 注入点，以及 broker 定义生成处）追加"可用代理"清单：`name — description (source: builtin|user|project|profile, permissionMode/read-only 摘要)`；实现上可在 `agentdef.Catalog` 增加 `ModelVisibleEntries()`，**只取 description 的短文本**，避免 prompt 膨胀。
  2. `spawn_agent` schema 的 `agent_type` 说明改为"必须引用可用清单中的 name"（enum 可动态注入时用 enum，否则在 description 中列名）。
  3. `spawn_subagents` 的 task 条目增加 `agent_type`（可选）；`decodeSubagentTasks` 时解析为 Definition 默认（与 `applySpawnAgentAgentdefDefaults` 对齐：permission/read_only/model/provider/skills/sandbox），任务显式字段仍优先（与 `docs/aicli/agents.md:174-191` 的"显式参数永远赢"一致）。
  4. 路由按 description 做轻量匹配是可选增强；保守做法是只"暴露+让模型自选"，别急着做关键词匹配引擎。
- **验证**：`go test ./internal/agentdef ./internal/agent ./internal/toolbroker`；新增表驱动测试：项目 def 出现在工具描述/清单中；`spawn_subagents` 传 `agent_type: explore` 时 read_only/permission_mode 默认生效且显式字段覆盖；未知名给出可操作错误（列出可选名）。

### 4.2 【P0】`/agents` 定义管理器与创建向导（含模型自建）

- **CommandCode 语义**：`/agents` 管理器 + 手动向导七步 + Ask CC 自然语言创建；按 scope 分组；Default 只读；新增即下一回合生效。
- **aicli 缺口**：本地 `/agents` 全部动词是**运行时协作**（`chat_debug.go:280-322`：pick/send/followup/target/panel/view/approve/deny/answer/routing/cleanup），没有任何"列出我有哪些代理定义/新建/校验/查看来源"的定义管理面；也没有把自然语言创建落到 `.agents/agents/*.md` 的既有流程（skill-creator 只管 skill）。
- **设计映射**：
  1. **只读浏览**（低成本）：`aicli agents list [--json]` 与 `/agents defs`：调用 `agentdef.Discover`，按 builtin/user/project/profile 分组输出 name、description、source path、覆盖关系、permissionMode/sandbox/read-only 摘要；复用现有 `Agent Source` 格式化（`chat_setup.go:737-743`）。
  2. **校验**：`aicli agents lint [path...]` 或 `/agents defs validate`：直接跑 `agentdef.ParseFile`+`Validate`+`BuildBinding`，输出 warning（如"tools 省略=继承全量，请显式声明"、"与 builtin 同名将覆盖 general"、"model/reasoningEffort 无法在本机解析"）。
  3. **创建**：`aicli agents new <name> [--scope project|user] [--template explorer|reviewer|writer]` 生成带注释的 frontmatter 模板（字段与 `docs/aicli/agents.md` 一致），写 `.agents/agents/<name>.md` 或 `~/.aicli/agents/<name>.md`；写入前做 name 规范化与保留名/同名检查。
  4. **模型自建**：提供 slash 引导（如 `/agents defs new <name> <用途描述>`）或在系统提示中授权主代理使用 write 工具写 `.agents/agents/*.md`，并用 `aicli agents lint` 作为校验回路；若担心提示注入，限制只能写 `.agents/agents/**` 且强制 lint。
- **注意**：本地 `/agents` 已被运行时协作占用，建议**新动词 `defs`**（或独立 `aicli agents` 子命令），避免与 CC 同名异构继续叠加歧义。文档需同时澄清两套 `/agents` 语义。
- **验证**：CLI 单测（分组/覆盖标注/JSON）；向导落盘幂等；lint 对非法 enum、保留名、重复名、不可解析引用的表驱动用例。

### 4.3 【P1】per-definition `maxTurns`

- **CommandCode 语义**：`maxTurns` 默认 100，封顶代理自身循环。
- **aicli 缺口**：`Definition` 无字段（`definition.go:42-60`）；`Config.MaxSteps` 非正=不限（`agent/agent.go:149-153`）；`turn_budget.go` 是**每轮** steps/wallclock/tokens 水位（`turn_budget.go:44-53`），不是"代理生命周期回合数"。`spawn_subagents` 有 per-task `budget_tokens`/`timeout`（`loop.go:7150-7161`），等价物最近但语义不同（token/墙钟 vs 步数）。
- **设计映射**：
  1. `Definition` 增加 `maxTurns int`（0=继承宿主默认；建议默认值与现网一致，避免行为突变），`BuildBinding` 透传。
  2. 消费点：`ChildAgentFactory.Build` 构造 `LoopReActConfig`（`child_factory.go:138-150`）时以 `min(def.maxTurns, 宿主 MaxSteps)` 覆盖；teammate 同理（`agentdef.PortableSessionDefaults` 增字段）。
  3. 与 turn_budget 对齐：把 `maxTurns` 映射为 `TurnBudgetSpec.MaxSteps`，软着陆/硬停沿用现有水位逻辑（`:98-130`）。
- **验证**：单测 def maxTurns=3 时子代理第 3 步硬停且事件字段正确；未声明时保持现状；上限/负数归一化用例。

### 4.4 【P1】per-definition `reasoningEffort` 与两级校验/回退

- **CommandCode 语义**：加载期未知档位丢弃并告警（文件仍加载）；运行期按实际解析出的模型校验，不支持回退模型默认（不夹到别的档位）。
- **aicli 现状（比"完全缺失"更细）**：
  1. `Definition` **无字段**；`spawn_agent` 的 `reasoning_effort` 是事件级参数（`broker.go:413`、`spawn_agent_arg_types.go:57-58`），`spawn_subagents` 只广告 low/medium/high（`loop.go:7125`），modelrouting 的 route profile 可给每个 difficulty 配置 reasoning（`child_factory.go:38-71,148`）。
  2. `types.NormalizeReasoningEffort` **只 TrimSpace**（`types/thinking.go:33-35`），但 modelrouting 层**已有**模型能力校验与三态策略：`applyReasoningCompatibility` 支持 `fail | downgrade | ignore`（默认 ignore，写 `reasoning_effort_unsupported_ignored` / `..._downgraded` warning），能力未知时写 `reasoning_effort_capability_unknown`（`resolver.go:552-581`；策略归一 `types.go:471-508`）。
  3. **缺口在两处**：(a) def 无法声明、加载期无校验；(b) routing 关闭时 `Resolve` 直接返回 `resolveDisabled`，不执行 `applyReasoningCompatibility`，推理档校验被整体跳过（`resolver.go:15-18,61-89`）。
- **设计映射**：
  1. `Definition` 增加 `reasoningEffort`；`BuildBinding` 透传；spawn/teammate 默认填充（与 model/provider 同一处：`applySpawnAgentAgentdefDefaults:4198-4203`）。
  2. **加载期校验**：agentdef 层维护已知档位集合（至少 low/medium/high，可加 minimal/xhigh 等模型实际档位），未知值→warning 并清空（文件仍加载），与 CC 一致。
  3. **运行期校验**：在 modelrouting 决议出 provider/model 之后（`child_factory.go:49-58`）查模型能力表（`internal/modelcard` 已存在 catalog，可复用）决定是否支持；不支持→回退模型默认并写 `RouteWarnings`（本地已有 route warning 通道，`spawn_agent_permission.go:11-31` 是范式）。
  4. 为 `NormalizeReasoningEffort` 增加可选严格校验函数（不要破坏现有透传语义，新增 `ValidateReasoningEffort(provider, model, effort)`）。
- **验证**：表驱动：def 写 `meduim`（typo）→ 加载告警且回退；def 写 `high` 但 resolved model 只支持 low/medium → 回退默认并带 warning；显式 spawn 参数覆盖 def；route 回执（`subagent_route_receipt.go`）含 reasoning 来源。

### 4.5 【P1】per-definition `background` + `spawn_agent` 后台开关 + 单一 `agent_output` 收集面

- **CommandCode 语义**：`background:true` 时每次运行分离，`agent` 立即返回 `agent_id`；用一个 `agent_output` 完成 wait/status/kill。
- **aicli 缺口（与现状相反的部分）**：
  - 本地**没有** def 级 `background` 字段；
  - `spawn_agent` schema/参数校验里**没有** background/async/detach 键（`spawn_agent_arg_types.go:44-72`）；
  - `spawn_agent` 实际天生异步（宿主创建子会话后异步提交），但产品语义上缺少"立即返回 + 后收集"的标准化契约说明；
  - 收集面是 10+ 个工具（`broker.go:33-76`），没有 CC 那种单一入口。
- **设计映射**：
  1. `Definition` 增加 `background`；`spawn_agent` 增加 `background`/`async` 兼容键（默认值与现有异步行为一致，纯语义/文档化增强）；`spawn_subagents` 已有异步批处理，无需改内核。
  2. **可选**：新增 `agent_output` 便捷工具（或在 `read_agent_result` 上扩展 `action=wait|status|kill`），内部路由到 `wait_agent`/`subagent_status`/`close_agent`；保留细粒度工具给高级调用方。`agent_output` 名称与 CC 对齐，降低跨工具提示迁移成本。
  3. `/agents` panel 已是本地更强的聚合视图（`chat_debug.go:294-297`），可把 wait/status/kill 三个动作挂进面板作为交互补充。
- **验证**：`spawn_agent` 带 background 时返回回执含 session_id/alias/status（已有字段，`broker.go:1761-1767` 附近）；`agent_output(wait)` 语义等价 wait_agent；`kill` 等价 close_agent；单测保证不改变现有异步调度语义。

### 4.6 【P1】`showOutput`：最终消息原文入 feed

- **CommandCode 语义**：默认只显示一行 "done"；`showOutput:true` 时才把最终消息原文渲染进 feed。
- **aicli 缺口**：父侧默认是**有界摘要**（8KiB 预算、按结论去重、冲突标注、溢出留指针，`subagent_parent_summary.go:13-101`），要看原文得额外 `read_agent_result`/`/agents view`；没有按 def 或会话切换"原文回显"的开关。
- **设计映射**：
  1. `Definition` 增加 `showOutput`（bool）；会话级再加 `--show-subagent-output`（可选）。
  2. 消费点：批任务终态渲染 / spawn_agent ready output 渲染处，若开启则把最终 assistant 消息原文作为事件载荷（可截断上限+artifact 归档，复用现有输出网关），否则维持摘要。
  3. UI 侧：CLI 渲染为折叠块/`/agents view` 自动跟随；Web 侧沿用消息流。
- **验证**：开启时 feed 事件包含原文（或 art 指针）；关闭时 payload 体积不回归（对比现有 `subagent_parent_summary_test.go` 的预算断言）。

### 4.7 【P2】tools 语义：省略=无工具、`"*"`、向导按类别选

- **CommandCode 语义**：省略 `tools` = **无工具**；`"*"`=全部（含 MCP）；向导按 Read-only/Edit/Execution/Search 类别开关。
- **aicli 现状**：省略=继承全量（general 注释，`builtin.go:33-38`）；`normalizeStringSlice` 不识别 `"*"`（`definition.go:115-137`）；工具能力分类已有（`policy/capability.go`、`policy/capability_scope.go`），只读子代理按 capability 过滤（`policy/tool_policy.go:120-147`）。
- **建议**：
  1. `"*"` 显式支持（映射为 allowlist 关闭/全量）；省略的默认语义是否改 fail-closed 需要兼容性评估——至少 `aicli agents lint` 对"省略 tools"给出 warning，并在文档中高亮差异。
  2. 向导/模板按 capability 分类生成 tools 列表（read-only / edit / execution / search），与只读过滤逻辑共享分类表，避免两套清单漂移。
- **验证**：`tools: "*"` 下 MCP 工具可见；lint 对未声明 tools 的项目 def 输出 warning；向导生成的清单能通过 `tool_vocabulary_contract_test.go` 的工具名契约。

### 4.8 【P2】保留名/覆盖规则与 scope 分组展示

- **CommandCode 语义**：保留名自定义文件被忽略；按 scope 分组展示；Default 只读。
- **aicli 现状**：允许覆盖内置（`discover.go:143-157`；`docs/aicli/agents.md:102`），无保留名；`Agent Source` 行与 profile 视图可观测来源但无覆盖告警。
- **建议**：
  1. 不改现有覆盖能力（团队定制 built-in 是合理需求），但在 Discover 时记录被覆盖的 builtin 并在 `agents list`/session info 显示 `overrides: builtin:general`；
  2. 对 `general` 这类语义关键名，覆盖时 lint 给强 warning（要求显式 `--override-builtin` 或 frontmatter 标记），防止静默改掉默认兜底行为；
  3. 分组输出直接按 Source 枚举（builtin/user/project/profile）实现，与 CC 展示对齐。
- **验证**：覆盖场景单测（project 覆盖 general 时有 warning/标记）；列表分组输出快照测试。

### 4.9 【P2】文档 IA 与命名澄清

- 在 `docs/aicli/agents.md` 增加：Quick start（Ask/手写两条路径）、创建与校验命令、后台批处理与结果收集（对应 CC background/agent_output 章节）、`/agents`（运行时协作）与 `aicli agents`（定义管理，若落地）的边界、逐字段示例含 `reasoningEffort/maxTurns/background/showOutput`（字段落地后）。
- 同步更新 `docs/multi-agents/README.md` 的入口索引，避免与 profile 文档混淆。

### 4.10 盘点中发现的本地一致性缺口（建议随行修复，非 CC 借鉴）

以下问题与 CommandCode 无关，但在本次对照盘点中由并行只读子代理发现并经本文二次核验；它们直接削弱"按代理配置"的可靠性，建议在 Wave 2 顺带修复：

1. **routing 关闭时 `spawn_subagents` 丢弃 per-task provider**：`Resolver.Resolve` 在 `!RoutingEnabled` 时直接返回 `resolveDisabled`（`modelrouting/resolver.go:16-17`），该分支只采纳 `task.Model`，**不读 `task.Provider`**（`resolver.go:61-89`）。因此 agentdef/任务里的 provider 在未启用路由时会静默回落到父 provider；`applyExplicitOverrides`（含 provider 白名单逻辑，`:377-415`）与 `applyReasoningCompatibility`（`:517,552-581`）都只在 routing 开启路径执行。修复：在 `resolveDisabled` 中补齐 `task.Provider`（经 `resolveProviderName` + 白名单）与 reasoning 兼容校验，或让两条路径共享同一个 finalize。
2. **agentdef 的 `skills` 字段未接线**：`Definition.Skills` 被校验与投影到 `Binding.SkillAllowlist`（`definition.go:48,74`；`build.go:166`），但全仓无消费者读取 `binding.SkillAllowlist`；`resolveChatAgentdefState` 构建 `chatProfileState` 时**未设置** `ProfileSkillSelection`（`chat_profile.go:295-348`），而 skills 过滤只读 `session.ProfileSkillSelection`（`chat_actor_host.go:1196`、`skills_integration.go:1242`）。`docs/aicli/agents.md:214-215` 声称"非空则是可选白名单"，与代码不符——要么接线（推荐，与 4.2 lint 联动），要么修正文档并标注未实现。
3. **`spawn_agent` schema 与 fail-closed 参数表漂移**：`spawnAgentToolArgKeys` 允许 `prompt` 与四个监督超时键（`broker.go:3342-3350`），执行路径也确实读取超时键，但模型可见 schema **未声明**它们（`broker.go:391-422,578-609`）——模型无法得知这些能力，只能靠隐式注入。修复：把 `prompt` 别名与 `timeout_sec/progress_timeout_sec/approval_timeout_sec/cancel_grace_sec` 补进 schema 描述，或在参数表中移除无人调用的键，保持单一事实源。

> 另注（信息面）：`read_agent_result` 已标记退役但仍可调用（`broker.go:62`；实现 `supervision/agent_result.go`），新一代巡检入口是 `subagent_status`/`subagent_inspect_task`（`broker.go:69-75`）；文档与提示词应统一指向新入口，避免模型在十几个采集工具之间摇摆（这也正是 4.5 建议提供聚合入口的动因）。

---

## 5. 已有且不应回退的能力（对照 CommandCode 的优势）

| 能力 | 本地实现 | 证据 |
|------|----------|------|
| **一层深度 + 可配置深度** | 默认 maxDepth=1；策略/可见面/创建前/深度计数四层冗余；CC 仅"从子代理工具集移除" | `scheduler.go:208-221`；`agent.go:565-597`；`child_factory.go:127-131`；`policy/tool_policy.go:120-122,492-499`；`chat_actor_host.go:1708-1725` |
| **持久后台批处理** | SQLite 批存储 + 协调器：心跳、deadline、幂等键、终态投递/恢复重放、孤儿保护；CC 的 background 是会话内分离运行 | `subagent_batch_coordinator.go:1-105`；`agent/agent.go:310-399` |
| **监督控制面** | 20+ 工具：等待/事件/状态/深度检查/邮箱/输入/审批代答/生命周期 ack/控制/回收/恢复/worktree | `broker.go:33-76` |
| **任务级可靠性与治理** | 只读任务自动重试（指数退避+jitter）、连续失败熔断、全局并发限制、队列背压、单 writer 约束、路由审计 | `scheduler.go:104-148`；`subagent_retry.go`；`subagent_route_audit.go` |
| **权限天花板** | 父模式为会话树上限，升权默认阻断；plan/dont-ask 全程钉住；bypass 不绕过 hooks/硬 deny | `spawn_agent_permission.go:57-131`；`policy/engine.go`（权限报告 §3.7） |
| **只读能力域** | read-only 子代理在能力域、工具面、执行期三层硬边界，approval/bypass 不能放宽 | `policy/capability_scope.go`；`policy/tool_policy.go:120-147` |
| **定义来源扩展** | builtin/user/project/profile/ExtraDirs + folder-trust `SkipProjectRoot`；yaml 与 md 双格式；snake/camel 别名 | `discover.go:14-30,62-123`；`build.go:15-35` |
| **安全扩展字段** | `sandbox`（off/workspace/read-only/strict + D16 profile 约束）、`completionRequirement`（team worker 完成契约）、`promptMode`（extend/full）；`skills` 字段已声明/投影但运行时未接线（见 §4.10） | `build.go:104-172`；`teammate.go:58-89` |
| **隔离工作区** | `isolation=worktree` + apply/discard 工具（CC 无对应能力） | `broker.go:50-51,546-570` |
| **团队通道** | `spawn_team/wait_team/read_task_spec/report_task_outcome` 等，Team 与 subagent 双轨 | `broker.go:52-61` |
| **运行时可观测** | `Agent Source`、`/debug`、`/agents routing test`、usage analytics（`usage_subagents`） | `chat_setup.go:737-743`；`chat_debug.go:318-367`；`docs/analysis/session-analytics-baseline-20260917.md` |

**结论**：CommandCode 在代理**定义的产品化/管理面**上领先（目录暴露、创建向导、per-def 旋钮、简单收集语义）；aicli 在**执行内核、安全边界、持久化与监督**上显著更强。借鉴应以"补产品化外壳、不动安全内核"为原则。

---

## 6. 建议落地顺序与验证

### Wave 1（P0，低成本高收益：可见性与创建）
1. `agentdef.Catalog` 增加模型可见投影 + `spawn_subagents` 支持 `agent_type`（4.1）。
2. `aicli agents list/lint` 只读命令与 `/agents defs` 浏览（4.2 的 1/2 步）。
3. 文档澄清 `/agents`（运行时）vs 定义管理入口（4.9）。

**验证**：
```powershell
cd E:\projects\ai\ai-agent-runtime\backend
go test ./internal/agentdef/... -count=1
go test ./internal/agent/... -count=1 -run "TestSubagent|TestSpawn"
go test ./internal/toolbroker/... -count=1 -run "TestBroker_Execute_SpawnAgent"
go test ./cmd/aicli/commands/... -count=1 -run "TestChatAgent|TestStructuredAgent"
```

### Wave 2（P1，per-def 旋钮与展示）
4. `Definition` 增加 `maxTurns`（4.3）→ `reasoningEffort` + 两级校验（4.4）→ `background`（4.5）→ `showOutput`（4.6）。
5. `agent_output` 便捷工具（或 `read_agent_result` 扩展 action）。

**验证**：per-def 覆盖矩阵单测；`aicli chat --agent <def>` 冒烟；子代理事件/回执断言（route_warnings、预算水位、终态投递）。

### Wave 3（P2，语义与体验）
6. `tools: "*"` + lint warning（4.7）。
7. 覆盖告警与 scope 分组（4.8）。
8. 向导/模板（`aicli agents new`）与文档补齐（4.2 第 3/4 步 + 4.9）。

**验证**：`go test ./internal/agentdef/... ./cmd/aicli/commands/...`；手工 smoke：新项目 `aicli agents list` 显示 builtin/user/project 分组；创建→编辑→下一回合 spawn 生效。

---

## 7. 盘点方法与限制

- 本文对 CommandCode 侧结论全部来自 <https://commandcode.ai/docs/agents> 原文（2026-09-26 抓取）；未参考其实现代码，字段/默认值以文档为准。
- 对 aicli 侧结论全部来自本仓库只读代码盘点，关键断言附 `文件:行号`；标注"未找到"的结论检索了 `agentdef`、`agent`、`toolbroker`、`cmd/aicli/commands`、`internal/policy`、`internal/api/runtimeapi` 等目录（关键词含 `Catalog.List|Available agents|agent catalog|AgentCatalog`、`reasoning|MaxTurns|showOutput|background`、`create.*agent`、`/agents`）。
- 并行使用只读子代理做了三路独立取证（工具/生命周期、管理面、字段链路）：其报告与本文结论一致，并贡献了 §4.10 的三项一致性缺口线索与若干行号；`scheduler.go` 默认值（并发 4/深度 1/单 writer/熔断 2）、agents 配置默认值、`Agent Source` 展示、routing 推理兼容策略、skills 接线缺失、routing 关闭时 provider 丢弃、spawn_agent schema 漂移等关键断言均已由本文二次核验。
- 报告不修改任何生产代码；落点建议按优先级排列，Wave 1 可直接进入实施评审。

### 附：CommandCode 与 aicli 关键语义差异速查

| 维度 | CommandCode | aicli |
|------|-------------|-------|
| 内置代理可覆盖 | 否（保留名忽略） | 是（user/project/profile 可覆盖） |
| `tools` 省略 | 无工具（fail-closed） | 继承全量（文档未显式声明，`builtin.go:33-38` 注释为继承） |
| 委派入口 | 单一 `agent` + `agent_output` | `spawn_agent` / `spawn_subagents` / `spawn_team` + 20+ 监督工具 |
| 后台语义 | per-run detach，`background:true` | 批处理通道持久异步；单通道天生异步但无开关 |
| 定义管理 UX | `/agents` 管理器 + 向导 + Ask | 无（只有运行时 `/agents` 面板） |
| 深度控制 | 硬编码一层 | 默认一层 + 可配置 maxDepth + 多层硬闸 |
| 结果展示 | `showOutput` 原文开关 | 有界摘要 + 按需读取 + transcript |
| 权限模型 | 5 模式 + 规则/断路器 | 5 模式 + 父模式天花板 + 能力域/沙箱（更强） |

---

## 8. 实施记录（2026-09-26）

本节记录按 §4 借鉴项落地的实际进度；"已落地"均带代码落点与验证命令，"部分/延期"写明原因，避免形成假开关。

| 项 | 状态 | 落点 | 验证 |
|----|------|------|------|
| 4.1 目录暴露 + description 路由 + `agent_type` | ✅ 已落地 | `agentdef/modelview.go`（模型可见投影/摘要）、`agentdef/discover.go`（`Catalog` 增加 `Overridden`/`Warnings`）、`agent/agentdef_bridge.go`（`AgentDefinitionsSummary` + 任务默认值）、`loop.go`（`spawn_subagents` schema 增加 `agent_type`/`max_turns`，描述注入可用角色）、`toolbroker/broker.go`（`spawn_agent.agent_type` 说明改为便携定义语义） | `go test ./internal/agent -run "TestApplyAgentdef|TestDecodeSubagentTasksAcceptsAgentType|TestSpawnSubagentsSchemaAdvertises"` |
| 4.2 `/agents defs` + `aicli agents` 管理器 | ✅ 已落地（模板式创建，非交互向导） | `cmd/aicli/commands/agents.go`（list/show/lint/new + JSON）、`chat_agent_defs.go`（`/agents defs`）、`chat_debug.go` 动词分发、`chat_slash_command_catalog.go`、`main.go` 注册 | `go test ./cmd/aicli/commands -run "TestAgents|TestNormalizeAgents"`；手工 smoke：`aicli agents new/list/show/lint` |
| 4.3 per-def `maxTurns` | ✅ 批量通道已落地 | `agentdef/definition.go`+`build.go`（字段/校验/绑定）、`agent/scheduler.go`（`SubagentTask.MaxTurns`）、`loop.go`（`max_turns` 解码/广告）、`child_factory.go`（取 def 与运行配置的较小值） | `TestApplyAgentdefTaskDefaults*`、`TestDecodeSubagentTasksAcceptsAgentTypeAndMaxTurns` |
| 4.4 per-def `reasoningEffort` + 两级校验 | ✅ 已落地 | `definition.go`（已知档位集合，未知加载期丢弃+warning）、`build.go`/profile adapter、`broker.go`（spawn_agent 默认填充）、`chat_profile.go`（`--agent` 默认，CLI 显式优先）、`modelrouting/resolver.go`（routing 关闭路径也执行 `applyReasoningCompatibility`） | `TestDefinitionNewFieldsParseAndNormalize`、`TestDefinitionUnknownReasoningEffortDropsWithWarning`、`TestResolveDisabled*` |
| 4.5 per-def `background` | ⚠️ 部分（声明/目录/审计） | `definition.go`+`build.go`+catalog/lint 可见；运行时子代理无阻塞路径可切换，字段语义与限制写入 `docs/aicli/agents.md` | `aicli agents show <name>` 可见；lint 校验 |
| 4.6 `showOutput` | ⚠️ 部分（声明/校验/目录） | 同上；原文回显渲染仍走现有摘要 + `read_subagent`/transcript 路径，未改展示层 | `aicli agents show <name>` 可见 |
| 4.7 tools 语义（`"*"` + lint 提示） | ✅ 已落地 | `definition.go`（`ToolWildcard`/`HasExplicitTools`）、`build.go`（wildcard → allow-all + `ToolsWildcard` 标记）、`lint.go`（省略 tools 的 info 提示、deny 冲突提示） | `TestDefinitionToolsWildcardMapsToAllowAll`、`TestAgentsTemplateContentIsLintClean` |
| 4.8 保留名/覆盖显式化 | ✅ 已落地 | `builtin.go`（`ReservedAgentNames`/`IsReservedAgentName`）、`discover.go`（`Overridden` 记录）、`lint.go`（覆盖为 info：可见但不导致 lint 失败，真实错误才是 warning）、`agents list`/`/agents defs` 标注 `overrides <source>` | `TestCatalogRecordsOverridesAndWarnings`、`TestAgentsListShowsCreatedDefinition` |
| 4.9 文档 IA | ✅ 已落地 | `docs/aicli/agents.md`：新字段表、tools 语义差异、`aicli agents` 创建/校验、`spawn_subagents.agent_type`、skills 接线修正、验收命令 | 文档审阅 |
| 4.10-1 routing 关闭丢弃 provider | ✅ 已修复 | `modelrouting/resolver.go`：`resolveDisabled` 现按显式 provider 开关应用或记 `explicit_provider_override_denied` warning | `TestResolveDisabledReportsDroppedProvider`、`TestResolveDisabledHonorsAllowedProviderOverride` |
| 4.10-2 agentdef `skills` 未接线 | ✅ 已修复 | `chat_profile.go`：`resolveChatAgentdefState` 把 `binding.SkillAllowlist` 写入 `resolved.Skills` → `applyProfileStateToChatSession` 投影到 session | 手工验证：`aicli chat --agent <def-with-skills>` 启动后 `/profile status` 可见 allowlist（或单测 `TestResolveChatProfileState_Agent` 扩展） |
| 4.10-3 spawn_agent schema 漂移 | ✅ 已修复 | `broker.go`：两处 schema 副本补齐 `prompt` 与 `timeout_sec/progress_timeout_sec/approval_timeout_sec/cancel_grace_sec`，`agent_type` 说明对齐便携定义 | `go test ./internal/toolbroker -run "SpawnAgent"` |

**明确延期（需要单独设计与回归面）**

1. **`agent_output` 三合一便捷工具（4.5）**：现有 `wait_agent`/`read_agent_events`/`read_agent_result`/`subagent_status`/`close_agent` 组合已覆盖能力；新增别名工具需要动工具注册表、capability taxonomy 与提示词契约，收益主要是迁移熟悉度，暂缓。
2. **`showOutput` 原文回显渲染（4.6）**：需要把开关穿过 `SubagentTask → 批终态投递 → CLI/Web 渲染`，并定义与 8KiB 有界摘要（`subagent_parent_summary.go`）的共存策略；在展示层改造前不启用假开关。
3. **`background` 前台/后台分支（4.5）**：P3/C4-1 已删除阻塞路径，运行时只有异步语义；字段仅在目录/审计层对齐 CommandCode。
4. **`spawn_agent` 单会话与 `--agent` 的 `maxTurns`**：字段已在批量通道与绑定中可用；单会话循环预算仍由运行配置（`Config.MaxSteps`/turn budget）决定，接线需要改 actor 会话循环配置面。

**验证现状**：`go build ./...` 通过；`internal/agentdef`、`internal/modelrouting`、`internal/toolbroker`（SpawnAgent 相关子集）、`internal/agent`（相关子集）、`cmd/aicli/commands`（新增用例）测试通过；真实二进制 `aicli agents new/list/show/lint` 冒烟通过。未运行全仓全量测试（本仓库体量大，按包定向回归；全量可在 CI 执行）。
