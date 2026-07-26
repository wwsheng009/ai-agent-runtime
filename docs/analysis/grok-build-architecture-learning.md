# Grok Build 架构分析与对本项目的学习建议

分析时间：2026-07-25  
对比对象：

- **参考项目**：`E:\projects\ai\grok-build`（xAI Grok Build / `grok` CLI，Rust monorepo）
- **本项目**：`E:\projects\ai\ai-agent-runtime`（Go runtime + `aicli` + frontend）

本文目标：提炼 Grok 在 **agent 工具系统** 上更成熟的架构/功能，并结合本项目现状给出可落地建议。  
原则：**学习机制与分层，不照搬语言栈或 UI**。本项目在 multi-agent team orchestration 上已有优势，应补的是 harness 产品化、安全边界、可扩展定义层与协议面。

> ## 校正说明（权威口径，2026-07-25 二次校正）
>
> 初版容易被误读为“本项目缺 Skills / Agents，或有了但默认不加载”。**这是错误基线。**
>
> | 初版易错判断 | 校正后事实 |
> | --- | --- |
> | Skills 弱/未启用 | **Skills Runtime 已落地且默认 `enabled=true`**，目录 `./.agents/skills`，含 loader/registry/router/executor/hot-reload/MCP/HTTP API/`skill__*` CLI 暴露 |
> | Agents 缺失或未加载 | **运行时多代理已在用**（`spawn_agent` / `spawn_team` / AgentControl）；**profile agent 包加载器已实现**，但仓库几乎无示例 `profile.yaml`/`agent.yaml`；skill 内 `agents/openai.yaml` 是 Codex 元数据，不是 Grok 式角色 def |
> | 主要差距是“补 skills 加载” | **主要差距是 harness 产品化**：可移植 markdown agent def、permission 管道、worktree isolation、plan-mode UX、OS sandbox、plugin 打包、ACP 嵌入、tool stream taxonomy |
>
> 一句话 resume cue：
>
> > **Grok 是打磨很深的本地 coding harness；本项目已有真实 Skills Runtime + multi-agent runtime。应学 Grok 的 agent-def / permission / isolation / plan / plugin / ACP 产品化，而不是重做 skill 加载器。**

> ## 实施状态回写（2026-07-25，相对二次校正后的执行结果）
>
> **权威实施与验收以** [`docs/plan/grok-harness-productization-implementation-plan.md`](../plan/grok-harness-productization-implementation-plan.md) **为准。**  
> 本文 §1–§5 仍保留「对照 Grok 时为什么做 / 学什么」的分析基线；§3.2 / §6 / §8 已回写为 **实施后状态**，避免再被读成「A/B 未做」。
>
> | 迭代 | 状态 | 深度备注 |
> | --- | --- | --- |
> | Iteration A（Agent def + permission + completion） | **核心已交付** | 无 MiniJinja；项目规则源 / CLI allow-deny 产品面仍可加深 |
> | Iteration B（worktree / plan / memory / sandbox profiles / hooks） | **核心已交付** | memory 为 keyword JSONL MVP；无 FTS/vector/云同步；无 best-of-n / persona IO contract |
> | Iteration C（toolprotocol / plugin / ACP / doom-loop / tool search / optional OS sandbox） | **核心已交付** | 均为子集/MVP；无 marketplace / Leader IPC / session load |
>
> 残余债与明确非目标见实施方案 **§15 需求覆盖矩阵与残余 backlog**。

---

## 1. 两边定位对照

| 维度 | Grok Build | ai-agent-runtime |
| --- | --- | --- |
| 主形态 | 终端 coding agent（TUI + headless + ACP） | 通用 Multi-Agent 执行运行时（CLI + HTTP + Web 控制台） |
| 技术栈 | Rust crate 切分很细 | Go monorepo，`backend/internal/*` 领域包 |
| 核心价值 | 单机/工作区 coding harness：编辑、沙箱、子 agent、插件、记忆 | 运行时编排：chat actor、team、mailbox、skills、provider 多协议 |
| 扩展面 | Agent def / Personas / Plugins / Hooks / MCP / LSP / Marketplace | Skills Runtime / toolkit / MCP / team tools / spawn_agent / spawn_team / profile agent 包 |
| 集成协议 | ACP（stdio / WS serve / relay）+ Leader IPC | HTTP/SSE runtime API + aicli 本地 actor |
| 安全 | OS 级 sandbox + 多层 permission + folder trust | 应用层 sandbox profiles + permission 管道已产品化；Linux 可选 bwrap（默认 off）；**folder trust 仍弱** |
| Skills | 目录发现 + slash 可调用 + plugin 打包；偏 harness 产品面 | **更深 runtime**：workflow DAG、embedding route、top-k exposure、hot-reload、quota/usage、admin API |
| Agents | `.grok/agents/*.md` 一等公民角色定义 + built-in subagents + personas | **四层并存**：(1) runtime multi-agent（强）；(2) profile/`agent.yaml`（有样例）；(3) skill 元数据 `agents/openai.yaml`（非角色 def）；(4) 可移植 `.agents/agents/*.md` **已落地** |

一句话：

- **Grok** 是“极致打磨的 coding agent harness + 工具协议平台”。
- **本项目** 是“可服务化的 multi-agent runtime + 控制面”。
- 最值得学的不是 TUI，也不是 skill loader，而是 **Agent 可移植定义、工具协议分层、权限/沙箱管道、子 agent 隔离与 completion 约束、Leader/ACP 嵌入模型**。

---

## 2. Grok 系统架构（按 crate 分层）

### 2.1 仓库骨架

```
grok-build/
├── crates/codegen/          # 主产品闭包
│   ├── xai-grok-pager*      # TUI / 渲染 / composition root
│   ├── xai-grok-shell       # 会话宿主、leader、采样、扩展入口
│   ├── xai-grok-agent       # Agent 定义解析与 system prompt 装配
│   ├── xai-grok-tools       # 工具实现与 taxonomy
│   ├── xai-grok-workspace   # 工作区守护、信任、worktree、权限
│   ├── xai-grok-mcp / hooks / memory / sandbox / config ...
│   └── xai-acp-lib          # Agent Client Protocol
├── crates/common/           # 可复用协议/runtime 叶节点
│   ├── xai-tool-protocol    # 线协议 / JSON-RPC / 注册 / hook frame
│   ├── xai-tool-runtime     # Tool trait / dispatch / stream / search
│   ├── xai-tool-types
│   └── xai-computer-hub-*   # 工具服务化 / MCP adapter
└── third_party/             # 如 Mermaid 栈
```

关键分层思想：

1. **Protocol 与 Runtime 分离**  
   `xai-tool-protocol` 只管 wire types / ids / handshake / error codes；  
   `xai-tool-runtime` 只管 `Tool` trait、dispatch、streaming、notification。
2. **Agent 与 Host 分离**  
   `xai-grok-agent` 把 Agent 抽成可移植对象（tools + prompt + compaction + model + permission）；  
   `xai-grok-shell` 只是一种 host（TUI/headless/ACP 都可消费同一 Agent）。
3. **Workspace 作为独立能力面**  
   文件系统、VCS、folder trust、worktree、checkpoint/recovery 不塞进 tool 实现。
4. **Leader-Follower 共享会话状态**  
   单机一个 leader 管 agent state，TUI / IDE / headless 通过 socket 复用，避免多进程状态分裂。

### 2.2 运行时核心闭环

```
User/Client (TUI | ACP | -p headless)
        │
        ▼
  Leader / Session Host
        │ builds
        ▼
     Agent (definition + tools + prompt policy)
        │ tool calls
        ▼
 Permission Pipeline
  PreToolUse hooks → deny/ask/allow rules → remembered grants
  → built-in auto-approve (read-only) → permission mode policy
        │
        ▼
 OS Sandbox + Workspace ops
        │
        ▼
 Tool Runtime (stream progress/terminal) + Notifications
        │
        ▼
 Compaction / Memory / System reminders / Subagent summary
```

### 2.3 产品功能面（与 agent 工具系统强相关）

| 能力 | Grok 做法 | 价值 |
| --- | --- | --- |
| Agent 定义 | Markdown + YAML frontmatter（`.grok/agents/`） | 角色、工具白/黑名单、permissionMode、skills、completionRequirement 可文件化 |
| Personas | 子 agent 行为叠加层（IO contract / model / isolation） | 不改 agent type 也能换行为契约 |
| Subagents | `spawn_subagent` + explore/plan/general + worktree isolation | 独立上下文、并行、隔离写盘 |
| Plan Mode | enter/exit plan，仅允许写 plan 文件 | 先规划后实施，降低错误改动 |
| Hooks | Session/PreTool/PostTool/Stop/Subagent/Compact… 可 block | 安全闸门 + 自动格式化 + 不停机条件 |
| Plugins | skills+commands+agents+hooks+MCP+LSP 打包 | 团队能力一键分发 |
| Memory | 跨会话 Markdown + FTS5/vector 检索 | 项目约定与决策复用 |
| Sandbox | Landlock/Seatbelt/bwrap 配置化 profile | 真正的 OS 强制边界 |
| Permissions | deny > ask > allow + mode + remembered grants | 交互与 CI 都能用同一规则体系 |
| Background | bg shell / wait any|all / kill / monitor /loop | 长任务不阻塞对话 |
| ACP | stdio / serve / relay | IDE/自动化标准嵌入 |
| Tool taxonomy | ToolKind + canonical `_meta` + name overrides | 多 harness 工具语义统一 |
| Compat | Claude/Cursor settings/hooks 兼容 | 降低迁移成本 |

---

## 3. 本项目现状（相关能力）

### 3.1 已具备、且不弱于 Grok 的部分

1. **Multi-agent / Team orchestration（本项目优势）**  
   `team/`、`agentcontrol/`、mailbox、path claims、lead planner、task outcome contract、`spawn_team` 等，整体比 Grok 的 subagent 更偏“任务图编排”。这是 **runtime multi-agent**，不是“缺 agents”。
2. **Chat Actor + Session Runtime**  
   `chat/actor`、runtime store、SSE、checkpoint、compact reconciliation 已形成服务化闭环。
3. **Tool surface / broker / policy**  
   `toolkit`、`toolbroker`、`agent/tool_policy`、`permission_mode`、parallel tool scheduler、result contract 已有。
4. **Skills Runtime（深度强于 Grok 的产品面）——已落地且默认启用**  
   - 配置：`skills_runtime.enabled` 默认 `true`，`skill_dir: ./.agents/skills`  
   - 代码：`internal/skill/*`（loader/registry/router/executor/DAG/embedding route/hot-reload/MCP adapter）、`internal/bootstrap`、`api/skills`、`cmd/aicli/commands/skills_integration.go`  
   - CLI：`--skills-dir`、`--skills-mode auto|prefer|only`、`--skills-top-k`、`/skills` `/skill` `/functions`  
   - 系统 skills 目录已存在（如 `aicli`、`skill-creator`、`skill-installer`、若干 tool-facing skills）  
   - **易被误判为“未加载”的原因**（不是缺失）：
     1. 暴露是 **route + top-k**，不是全量 dump 到 prompt  
     2. `aicli exec` 常默认 `--disable-tools`（headless）  
     3. aicli 路径 `DiscoverOnly: true`（lazy prompt hydrate，仍会注册 summary）
5. **Profile agent 包加载器（代码就绪，示例稀疏）**  
   `internal/profile` 已实现 `profile.yaml` + `agents/<name>/agent.yaml` 解析、merge、`--profile/--agent` 与 API 接线；仓库 **几乎没有** 可直接演示的 profile 样例（测试里有 fixture）。**缺的是产品样例与 Grok 式 markdown 角色 def 一等公民，不是加载器不存在。**
6. **Hooks 雏形**  
   `internal/hooks` 已有 session/tool/subagent/checkpoint 等事件与 continue/block/modify。
7. **应用层 sandbox**  
   `executor.Sandbox` 支持 path/command/host 白黑名单与超时（应用策略，非内核强制）。
8. **HTTP/SSE Runtime API + Web 控制面 + 多 provider**  
   服务化与控制面是本项目定位优势，Grok 更偏本地 harness。

### 3.1.1 Agents 概念必须拆开（避免再混淆）

| 本项目“Agent”含义 | 状态（实施后） | 与 Grok 对照 |
| --- | --- | --- |
| Runtime multi-agent：`spawn_agent` / `spawn_team` / child session / AgentControl | **已实现且在用** | 对标 Grok subagent/host 协作；编排更强；worktree isolation **已可选** |
| Profile agent 包：`profile.yaml` + `agents/*/agent.yaml` | **加载器 + 仓库样例**（如 `examples/profiles/coding`） | 对标 Grok agent package/配置；与 md def 可互转 |
| Skill 元数据：`.agents/skills/*/agents/openai.yaml` | 存在，属 Codex skill 元数据 | **不是** Grok 式角色定义（刻意不升级） |
| Grok 式可移植 `AgentDefinition`（tools/permissionMode/skills/completionRequirement + body prompt） | **已交付**（`internal/agentdef` + `.agents/agents/*.md` + `chat --agent` / spawn defaults） | 无 MiniJinja 条件模板；team teammate 更深对齐仍可收紧 |

### 3.2 相对 Grok 的能力债（实施后：已交付 / 仍弱 / 明确不做）

> 下列不再用「缺 xxx」现在时描述已交付项。完整矩阵见实施方案 §15。

| 主题 | 状态 | 现状（实施后） | 残余影响 |
| --- | --- | --- | --- |
| 可移植 markdown Agent 角色定义 | **已交付** | discovery / parse / binding / 样例 / Agent Source 调试字段 | 无 MiniJinja；team 更深 def 对齐可收紧 |
| 工具协议分层 | **已交付（MVP）** | `internal/toolprotocol` + progress / SSE live / `protocol_result` | 非完整外部 tool server 平台 |
| 权限管道 | **核心已交付** | hooks → rules → grants → readonly auto → mode + trace | 项目文件规则源 / CLI `--allow/--deny` 产品面未满 |
| Sandbox | **应用层已交付；OS 可选** | profiles `off\|workspace\|read-only\|strict`；Linux bwrap 可选 | 非 Linux OS 强制仍弱；默认 off |
| Subagent isolation | **核心已交付** | worktree create/apply/discard + path claims | 无 best-of-n / persona IO contract |
| Plan Mode 产品流 | **已交付** | planmode 包 + `/plan` + agent tools + API + frontend Plan 页签 | 与 Grok UX 仍有差距，但闭环可用 |
| Cross-session memory | **MVP 已交付** | keyword JSONL + `/memory` + context 注入 | **明确不做** FTS/vector/云同步/session-end 自动摘要（除非重开） |
| 插件/打包 | **MVP 已交付** | `internal/plugins` + `aicli plugin` | **明确不做** marketplace（除非重开） |
| 标准嵌入协议 | **子集已交付** | `aicli agent stdio` ACP 子集 | 无 session/load、无 Leader IPC |
| Tool streaming | **核心已交付** | progress + live SSE fan-out | 全工具 terminal stream 完备度仍可加深 |
| Doom-loop / completionRequirement | **已交付** | completion recovery + doom-loop tracker（硬停 opt-in） | 默认软警告；与 Grok 硬策略不同 |
| Folder trust | **仍弱** | 未产品化 | 打开陌生仓的 hooks/MCP 信任风险仍在 |
| Profile / agent 样例 | **已补** | coding profile + explore/plan/general md | 默认 subagent routing 仍可能关闭（配置问题，非能力缺失） |
| 统一 system-reminder 通道 | **部分** | completion 等路径有 reminder | 非统一 ephemeral instruction 产品面 |
| frontend grants/memory/plugins 面板 | **仍弱 / 未立项硬门** | 仅 plan preview 已做 | Web 控制面 harness 面板不完整 |

---

## 4. 最值得学习的点（按优先级）

### P0 — 高 ROI，贴合本项目方向

#### 4.1 把 Agent 做成可移植对象 + 文件定义

Grok：

- `Agent = tools + system prompt + reminder/compaction policy + model + permission`
- 定义文件：`.grok/agents/*.md` frontmatter + body
- `promptMode: extend|full`，MiniJinja 模板变量（工具名条件注入）
- `completionRequirement`：worker 必须调用 `complete_task`，否则 reminder + recovery

对本项目建议（**rebase 到已有 profile + team + skills，不要从零做 agent/skills 系统**）：

1. 引入可移植 `AgentDefinition`（YAML/Markdown frontmatter 均可），并与现有 `internal/profile` ResolvedAgent **对齐/可互转**：
   - `name/description/tools/disallowedTools/permissionMode/skills/model`
   - `promptMode`、`completionRequirement`、`toolConfig.retry`
2. 发现路径（叠加，不替代 profile 包）：
   - 项目：`.agents/agents/*.md` 或 `.aicli/agents/*.md`
   - 用户：`~/.aicli/agents/*.md`
   - built-in 默认 + 现有 profile registry
3. `AgentBuilder` 输出统一运行时对象，供：
   - `aicli chat`
   - `runtime-server /api/agent`
   - `spawn_agent` / team teammate
4. 对 team worker 默认启用 `completionRequirement`（对齐你们已有 task outcome contract）。
5. 先补 **1–2 个仓库内 profile/agent 样例**，消除“agents 未加载”观感。

> 本项目已有 profile loader / role_defaults / spawn_agent / skills runtime；缺的是 **Grok 级统一定义格式 + discovery 产品化 + host 无关构建**，不是 agent/skills 子系统缺失。

#### 4.2 权限授权管道产品化（不是只剩 mode）

Grok 授权顺序非常清晰：

1. PreToolUse hooks（可 deny）
2. deny / ask / allow rules
3. remembered grants（项目级）
4. built-in read-only auto-approve
5. permission mode 策略

建议在本项目落地：

```text
hooks.pre_tool_use
  → rule engine (deny > ask > allow)
  → remembered grants (per project)
  → tool taxonomy read_only 自动放行
  → permission_mode (default|accept_edits|plan|bypass|dont_ask)
```

配套：

- 规则源：全局 config + 项目 config + CLI `--allow/--deny`
- shell 分段解析后的只读命令表（`git status`、`ls`、`rg`…）
- headless/CI 默认 `dont_ask`（无交互则 deny 并回传模型）
- dangerous command 不记住 “always allow”

#### 4.3 Tool Protocol / Runtime 分层

学习 Grok 的：

- `tool-protocol`：ids、capabilities、error wire、notification wire、JSON-RPC methods
- `tool-runtime`：`Tool` trait（run 或 execute stream）、dispatch、search index、progress

建议本项目：

1. 抽出 `pkg/toolprotocol`（或 `internal/toolprotocol`）：
   - ToolId / CallId / Kind / Scope / Capabilities
   - Result wire（content blocks + error code + mutation flags）
   - Notification kinds（progress / bg complete / question / permission）
2. `toolkit.Tool` 升级为：
   - 静态 `Description` + 可选 `ShouldList(ctx)`
   - `Execute` 支持 progress stream（SSE 已可承载）
   - `IsReadOnly` / `MutatesPaths` 进入 taxonomy，而不是散落各工具
3. toolbroker 只做会话策略与路由，不重复定义结果形状。

#### 4.4 Subagent 隔离：worktree + IO contract

Grok：

- subagent type：`explore` / `plan` / `general-purpose`
- persona：instructions + inputs/outputs + model + `default_isolation=worktree`
- best-of-n skill：多 worktree 并行方案评选

本项目建议：

1. `spawn_agent` 增加 `isolation: none|worktree`
2. worktree 会话写到隔离目录，完成后 summary + 可选 apply winner
3. persona/role 增加 **input/output contract**（文件路径契约），方便 team 任务链
4. 内置 `explore`（只读）与 `plan`（只写 plan）角色，默认工具面收紧

### P1 — 产品差异化与安全上限

#### 4.5 OS 级 Sandbox Profile

本项目 `executor.Sandbox` 是应用策略。Grok 的价值在 **fail-closed 的 OS 强制**。

建议分两阶段：

1. **Profile 化应用沙箱**（立刻可做）  
   `workspace | read-only | strict | off`，对应 allowed/denied/readOnly paths + network 策略。
2. **可选 OS 后端**（中期）  
   - Linux：bubblewrap / landlock / seccomp（有则启用，无则明确降级告警）  
   - macOS：seatbelt  
   - Windows：先保持应用层 + job object/process token 增强  

原则：deny 列表 fail-closed；无法 enforce 时拒绝启动高风险 profile，而不是静默失效。

#### 4.6 Plan Mode 产品流

学习 Grok：

- enter plan 需用户同意
- 除 plan 文件外全部写拒绝（即使 yolo）
- exit plan 打开审批 UI：approve / request changes / quit

本项目可落到：

- session 目录 `plan.md`
- tool：`enter_plan_mode` / `exit_plan_mode`（或 slash `/plan`）
- frontend + aicli 展示 plan preview 与审批
- 与现有 `permission_mode=plan` 打通

#### 4.7 Cross-session Memory

Grok：Markdown 文件 + SQLite FTS/vector，workspace 以 git origin hash 分桶。

本项目已有 embedding/index、artifact memory_entries、session memory。建议：

1. 明确两层：
   - **session working memory**（短）
   - **project durable memory**（跨会话，Markdown/JSONL + FTS）
2. session end 自动写轻量 summary（无 LLM 延迟）
3. `/flush` 或 skill 触发富记忆抽取
4. 新 session 自动 hybrid 检索注入 system reminder（可控开关）

#### 4.8 Hooks 事件完备 + Stop decision

你们已有 hooks manager。建议补齐 Grok 中高价值事件：

- `Stop` / `StopFailure`（可 block stop，例如测试未过）
- `PostToolUseFailure`
- `PreCompact` / `PostCompact`
- `PermissionDenied`

并统一：

- 项目 hooks 需 folder trust
- Claude/Cursor hooks 兼容（可选，降低生态摩擦）

#### 4.9 Plugin 打包模型

把现有 skills + hooks + MCP + agent defs 合成 plugin 目录约定：

```text
my-plugin/
  plugin.json          # optional
  skills/
  agents/
  hooks/hooks.json
  .mcp.json
  commands/
```

安装位置：用户级 / 项目级 / session 临时。  
这比单独分发 skill 更适合团队。

### P2 — 平台化与嵌入

#### 4.10 ACP / stdio Agent Server

Grok 用 ACP 让 IDE/编辑器嵌入。本项目 HTTP/SSE 强，但本地 IDE 集成弱。

建议：

1. 先做 **兼容子集**：`initialize` / `session/new` / `session/prompt` / `session/update` / permission request
2. `aicli agent stdio` 作为入口，内部复用 chat actor
3. 后续再考虑 leader socket（单机共享 runtime）

#### 4.11 Tool Search / Dynamic listing

Grok 有 tool search index、`should_list`、use_tool 动态启用。  
当 MCP 工具很多时，全量塞进 prompt 会炸上下文。

建议：

- 核心工具常驻
- 长尾 MCP 工具可检索后按需启用
- turn 级 `ShouldList(ctx)`（权限模式、角色、是否 plan 等）

#### 4.12 Doom-loop / 空转防护

Grok 有 doom-loop recovery 测试与策略。  
本项目 team/worker 更需要：

- 同工具同参重复 N 次 → 提醒/熔断
- 编辑连续失败 → 换策略提醒
- 无 `complete_task`/`report_task_outcome` 结束 → recovery reminder
- 并行 batch 失败不要误判为 doom-loop

#### 4.13 System Reminder 机制

Grok 大量用 `<system-reminder>` 注入短暂策略（persona、completion、goal continuation），不污染长期 system prompt。

建议本项目统一：

- ephemeral instruction channel（不入长期 memory）
- 用于：permission 变更、plan mode、worker completion、tool 失败恢复

---

## 5. 不建议照搬 / 需谨慎的点

1. **不要把本项目改成 Rust TUI 中心**  
   你们的差异化在 runtime-server、team orchestration、Web 控制面。
2. **不要一次性做完整 Computer Hub / 远程 workspace daemon**  
   成本极高；先把本地 protocol + policy 做干净。
3. **不要为了兼容 Claude/Cursor 牺牲自有契约**  
   兼容层应是 adapter，核心仍是本项目 result/event contract。
4. **不要把 team 模型退化成“只有 subagent”**  
   Grok subagent 强在隔离与并行；你们 team 强在任务图、依赖、path claims、mailbox。应 **互补**：team 编排 + subagent isolation。
5. **Windows 上 OS sandbox 现实有限**  
   先把 profile/policy/审批做好，再谈内核强制。

---

## 6. 落地路线与验收状态（3 个迭代）

> 历史建议路线；**勾选状态已按 2026-07-25 实施结果回写**。细节与验收证据见实施方案 §3 / §5–§7 / §14。

### Iteration A（2–3 周）：定义层 + 权限管道 — **已交付**

> 前提：**Skills Runtime / profile loader / team multi-agent 已存在**。本迭代只做产品化叠加，不重做 loader。

- [x] `AgentDefinition` 文件格式 + discovery + builder（与 `internal/profile` ResolvedAgent 对齐/可互转）
- [x] 仓库内补 1–2 个 profile/agent 样例（消除“agents 未用”观感）
- [x] agent/tool taxonomy：`read_only` / `mutates` / kind
- [x] 统一 permission pipeline（hooks → rules → grants → mode）
- [x] shell 只读命令表 + dangerous list
- [x] worker `completionRequirement` 与 team outcome 对齐
- [x] 文档：用户如何写自定义 agent；澄清 skills 默认启用与 top-k 暴露

### Iteration B（3–5 周）：隔离 + Plan + Memory — **核心已交付**

- [x] `spawn_agent` worktree isolation（apply/discard 工具面；无 auto-apply）
- [x] explore/plan/general 内置角色
- [x] plan mode 文件流 + 审批（CLI + agent tools + API + frontend preview）
- [x] project durable memory（**keyword JSONL MVP**；**非** FTS/embedding——相对初版建议已收窄）
- [x] Stop hook 可阻断结束（+ StopFailure / Pre|PostCompact）
- [x] sandbox profiles（应用层完整化）

### Iteration C（中期）：平台化 — **核心已交付**

- [x] tool protocol 包抽出 + streaming progress 统一
- [x] plugin 打包与安装（**无 marketplace**）
- [x] `aicli agent stdio` ACP 子集
- [x] tool search / dynamic listing（C4b：ShouldList + search_tool projection）
- [x] doom-loop harness（C4a：tracker + product events；硬停 opt-in）
- [x] （可选）Linux OS sandbox backend（C4c：OSSandboxBackend + bubblewrap/stub + off|auto|require）

---

## 7. 对本项目架构的模块落点（实施后）

### 7.1 新增/强化模块

| 模块 | 职责 | 状态 |
| --- | --- | --- |
| `internal/agentdef` | 解析/发现/校验 agent markdown/yaml | **已落地** |
| tool taxonomy（toolkit/policy 侧） | ToolKind、read-only、canonical meta | **已落地**（未单独拆 `tooltaxonomy` 包亦可） |
| `internal/policy`（管道增强） | rules、grants、mode policy、dangerous commands | **已落地**（未新建独立 `permission` 包） |
| `internal/isolation/worktree` | git worktree 创建/回收/apply | **已落地** |
| `internal/planmode` | plan.md 生命周期与审批状态 | **已落地** |
| `internal/memorystore` | project memory 文件 + keyword index | **已落地（MVP）** |
| `internal/plugins` | plugin 发现/信任/装载 | **已落地（MVP）** |
| `internal/toolprotocol` | wire types / result / progress | **已落地** |
| `internal/acp` + `aicli agent stdio` | ACP/stdio 入口 | **已落地（子集）** |

### 7.2 现有模块改造点

| 现有 | 改造意图 | 状态 |
| --- | --- | --- |
| `agent/loop` | permission pipeline、completionRequirement、reminders、doom-loop | **核心已接** |
| `policy.Engine` | 规则管道 + grants + readonly auto + plan paths | **核心已接** |
| `toolkit` | capabilities + stream + should_list | **核心已接** |
| `toolbroker` | 路由/会话绑定；plan/worktree 控制器 | **核心已接** |
| `hooks` | Stop/Failure/Compact；PostToolUseFailure | **主事件已接**；folder trust **未做** |
| `executor/sandbox` | profile 化 + optional OS backend | **已接** |
| `chat/actor` | plan mode 状态机；agentdef 绑定 | **已接** |
| `team/*` | isolation 字段 / outcome；persona IO **未做** | **部分** |
| frontend | plan 审批 **已做**；grants/memory/plugins 面板 **未做** | **部分** |

---

## 8. 能力对照清单（速查：实施前建议 → 实施后）

| 能力 | Grok | 本项目（实施前基线） | 本项目（实施后） | 备注 |
| --- | --- | --- | --- | --- |
| Skills Runtime / 加载与路由 | 中强 | **强** | **强** | 保持；勿重做 loader |
| Runtime multi-agent / Team 任务图 | 中 | **强** | **强** | 保持优势 |
| 可移植 markdown Agent 角色定义 | **强** | 弱 | **中强 / 已交付** | 无 MiniJinja |
| Profile/`agent.yaml` 包 | 中 | 中强代码 / 弱样例 | **中强 + 有样例** | coding profile 等 |
| 权限规则管道 | 强 | 中 | **中强 / 核心已交付** | 规则源产品面可加深 |
| OS Sandbox | 强 | 弱 | **中（应用层强；OS 可选）** | Linux bwrap；默认 off |
| Worktree 隔离 | 强 | 弱 | **中强 / 已交付** | 无 best-of-n |
| Plan Mode 产品流 | 强 | 弱 | **中强 / 已交付** | CLI+API+前端 |
| Hooks 完备度 | 强 | 中 | **中强** | folder trust 仍缺 |
| Cross-session Memory | 强 | 中弱 | **中（keyword MVP）** | 非 FTS/vector |
| Plugin 生态 | 强 | 弱 | **中（本地 MVP）** | 无 marketplace |
| MCP | 强 | 强 | **强** | + dynamic list |
| ACP/IDE 嵌入 | 强 | 弱 | **中（stdio 子集）** | 无 Leader IPC |
| Tool streaming | 强 | 中 | **中强** | protocol + live SSE |
| HTTP Runtime API | 弱/不同定位 | 强 | **强** | 保持优势 |
| Web 控制台 | Dashboard 向 | 强（Teams 控制面） | **强**；harness 面板部分 | plan 已做；grants/memory/plugins 未做 |
| Provider 多协议 | 相对聚焦 | 强 | **强** | 保持优势 |

### 8.1 初版判断 → 校正后 → 实施后（diff 表）

| 主题 | 初版易错表述 | 校正后 | 实施后（2026-07-25） |
| --- | --- | --- | --- |
| Skills | “弱 / 像没加载” | **默认启用 + 完整 runtime**；top-k / DiscoverOnly / exec disable-tools 造成假阴性 | 仍成立；未重做 loader |
| Agents | “缺失或未用” | **runtime multi-agent 在用**；profile 加载器就绪但样例稀；缺 md AgentDefinition 产品化 | md AgentDefinition **已交付**；样例已补 |
| 学习重点 | 好像要补 skill/agent 底座 | **补 harness 产品化**：def / permission / isolation / plan / plugin / ACP | A/B/C **核心已交付**；残余见方案 §15 |
| Iteration A | 可能被读成“从零建 skills/agents” | **在现有 skills + profile + team 上**做 AgentDefinition + permission pipeline | 按校正路线完成 |
| 文档权威 | 仅分析文档 | 分析 = 为什么；方案 = 做什么 | **验收与 backlog 以实施方案为准** |

---

## 9. 结论（校正后 + 实施后）

Grok Build 最值得本项目学的，不是“又一个 coding CLI”，更不是 skill loader，而是它把 **harness 产品面**做成了：

1. **可移植 Agent 定义**（host 无关 markdown/frontmatter）  
2. **清晰的工具协议/runtime 分层**  
3. **可组合的安全管道**（hooks + rules + grants + mode + sandbox）  
4. **可隔离的并行执行**（subagent + worktree + plan）  
5. **可分发的扩展单元**（plugin = skills/hooks/mcp/agents）  
6. **可嵌入的标准协议**（ACP + leader）

本项目已按  
**Agent 定义（叠 profile） → 权限管道 → 隔离与 Plan → Memory/Plugin → ACP**  
完成 A/B/C **核心产品化**（MVP/子集深度，非 Grok 对等）。应继续巩固 **Team orchestration / Skills Runtime / Runtime API / 多 provider / Web 控制面** 护城河；下一波仅处理方案 §15 中的 **deferred residual**，**不要**默认重开 marketplace / FTS / vector / 云同步 / MiniJinja。

**禁止错误路线**：把本项目描述成“缺 skills/agents 所以要从零建设”，或把分析文档仍读成“A/B 未做”。正确口径是：

> **Grok 级本地 agent harness 主线已 MVP 产品化 + 本项目既有 Skills Runtime、multi-agent team orchestration、服务化控制面；残余是加深与明确非目标，不是主线缺失。**

---

## 10. 参考路径（源码）

Grok：

- `crates/codegen/xai-grok-agent/` — Agent 定义与 builder（`.grok/agents/*.md`）
- `crates/codegen/xai-grok-tools/` + `crates/common/xai-tool-runtime/` + `xai-tool-protocol/`
- `crates/codegen/xai-grok-shell/src/leader/` — Leader IPC
- `crates/codegen/xai-grok-workspace/` — trust / worktree / workspace ops
- `crates/codegen/xai-grok-pager/docs/user-guide/` — 产品行为真源
  - `08-skills.md` `09-plugins.md` `10-hooks.md` `13-memory.md` `15-agent-mode.md` `16-subagents.md`
  - `18-sandbox.md` `19-plan-mode.md` `22-permissions-and-safety.md`

本项目：

- Skills：`backend/internal/skill/` `backend/internal/bootstrap/` `backend/internal/api/skills/` `.agents/skills/` `docs/skill_runtime/`
- Agents / multi-agent：`backend/internal/team/` `agentcontrol/` `chat/` `profile/` `agentdef/` `docs/multi-agents/`
- Harness 产品化：`isolation/worktree/` `planmode/` `memorystore/` `plugins/` `toolprotocol/` `acp/` `policy/`
- 工具与安全：`backend/internal/agent/` `toolkit/` `toolbroker/` `hooks/` `executor/`
- 实施方案（验收权威）：`docs/plan/grok-harness-productization-implementation-plan.md`
- 本文：`docs/analysis/grok-build-architecture-learning.md`
