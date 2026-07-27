# aicli Agents 使用说明

> Iteration A harness productization  
> 实现包：`backend/internal/agentdef`  
> 相关入口：`aicli chat --agent`、`spawn_agent.agent_type`、profile 包内 `agents/*/agent.yaml`

## 1. 先分清「Agents」三层

本仓库里 “agent” 不是一个词义，请按层理解：

| 层 | 是什么 | 典型路径 / 入口 | 用途 |
| --- | --- | --- | --- |
| **1. Runtime multi-agent** | 会话协作与编排 | `spawn_agent` / `spawn_team` / AgentControl | 子会话、团队任务图、mailbox、审批 |
| **2. Profile 包 agent** | 项目/用户 profile 内角色包 | `profile.yaml` + `agents/<id>/agent.yaml` | 与 provider/model/skills 目录等一起打包的本地配置 |
| **3. Portable AgentDefinition** | Grok 式可移植角色 def | `.agents/agents/*.md`、`~/.aicli/agents/*`、builtin `explore`/`plan`/`general` | tools / permission / prompt / sandbox / completion 的统一角色定义 |
| （非角色层）Skill 元数据 | Codex skill 展示层 | skill 内 `agents/openai.yaml` | **不是**角色 def；只服务 skill UI/兼容元数据 |

产品主路径：**Portable AgentDefinition（层 3）** 与 **Runtime multi-agent（层 1）** 叠加；Profile 包（层 2）继续可用并经 adapter 进入同一 binding。

```text
                    ┌─────────────────────────────┐
                    │  Runtime multi-agent        │
                    │  spawn_agent / spawn_team   │
                    └─────────────▲───────────────┘
                                  │ agent_type / defaults
          ┌───────────────────────┴───────────────────────┐
          │                                               │
 ┌────────┴────────┐                           ┌──────────┴──────────┐
 │ Profile package │  adapter                  │ AgentDefinition md  │
 │ agents/*/yaml   │ ─────────────────────────►│ .agents/agents/*    │
 └─────────────────┘                           │ builtin explore/... │
                                               └─────────────────────┘
 skill agents/openai.yaml  →  NOT a role system (ignore for roles)
```

## 2. 如何写自定义 AgentDefinition

推荐 Markdown + YAML frontmatter（项目根）：

```text
.agents/agents/explore.md
.agents/agents/plan.md
.agents/agents/my-reviewer.md
```

示例：

```markdown
---
name: my-reviewer
description: Focused code reviewer
tools:
  - view
  - grep
  - glob
  - ls
  - shell
disallowedTools:
  - write
  - edit
  - apply_patch
permissionMode: plan
promptMode: extend
completionRequirement: none
sandbox: read-only
---
You review diffs carefully.
Prefer evidence with file paths.
Do not mutate the workspace.
```

字段要点：

| 字段 | 含义 |
| --- | --- |
| `tools` / `disallowedTools` | 工具 allow / deny（runtime ToolPolicy） |
| `permissionMode` | `default` / `accept_edits` / `plan` / `bypass_permissions` / `dont_ask` |
| `promptMode` | `extend`（追加）或 `full`（替换 role 段） |
| `completionRequirement` | `none` 或 `complete_task`（worker 需 outcome） |
| `sandbox` | 应用层 profile：`off` / `workspace` / `read-only` / `strict`（见下） |
| `skills` | 可选 skill id 白名单；空 = 走全局 skills route |
| `model` / `provider` | 可选覆盖 |

`sandbox` 产品语义（应用层，非 OS 隔离）：

| 值 | 行为 |
| --- | --- |
| `off` | 不启用 sandbox 路径/网络/命令限制 |
| `workspace` | 仅允许访问 session workspace 路径（可写） |
| `read-only` | workspace 路径只读 + tool ReadOnly + 危险命令 deny；`explore` 默认；未显式 `permissionMode` 时可投影为 plan |
| `strict` | read-only + 阻断网络 + 更严命令/env 限制 |

workspace root 尚未可知时会 **显式降级并告警**（stderr/log），不会静默宣称已 enforce。chat session 与子 agent 在 workspace 确定后会再次 materialize path bounds。

### 发现顺序（后写覆盖先写）

1. **builtin**：代码内嵌 `explore` / `plan` / `general`
2. **user**：`~/.aicli/agents/*`
3. **project**：`.agents/agents/*` 或 `.aicli/agents/*`
4. **profile 包**：`agents/*/agent.yaml`（adapter → 同一 runtime 对象）

同名时项目/profile 可覆盖 builtin。仓库已带：

- 项目 def：`.agents/agents/explore.md`、`.agents/agents/plan.md`、`.agents/agents/general.md`
- profile 样例：`examples/profiles/coding/`

`general.md` 约定：

- 交互/general chat 默认 `completionRequirement: none`
- **team worker 不读 def 改 completion**：`TeammateRunner` 在 `RunMeta` 上强制 `complete_task`（`report_task_outcome` / `block_current_task`）

## 3. CLI：`aicli chat --agent`

### 不依赖 profile（Portable def）

```bash
# 从 backend/ 或仓库根运行均可；discovery 以 cwd 解析项目 .agents
aicli chat --agent explore
aicli chat --agent explore --provider nvidia --no-interactive -M "Locate the spawn_agent entrypoint"
```

行为：

1. 无 `--profile` 且无默认 profile 时，`--agent <name>` 走 `agentdef.Resolve`
2. 映射 tools / permission / prompt / sandbox 到 session
3. **显式 CLI 优先**：`--permission-mode`、`--yolo`、`--provider`、`--model` 覆盖 agentdef 默认

### 与 profile 一起

```bash
aicli chat --profile ./examples/profiles/coding --agent explore
aicli chat --profile coding --agent default
```

有 profile 时，`--agent` 仍是 **profile 内 agent id**（现有 profile 路径），不会改成“只看 portable def”。

### 优先级（实现约定）

| 来源 | 优先级 |
| --- | --- |
| 用户显式 CLI（`--permission-mode` / `--yolo` / provider / model） | 最高 |
| Profile 包 resolved agent | 中（有 profile 时） |
| Portable AgentDefinition / builtin | 无 profile 时作为 chat 默认；spawn 时填充空字段 |
| Runtime 全局默认 | 最低 |

### 可观测：Agent Source

会话绑定 agentdef/profile agent 后，会在 **session info**（启动 stderr / `printSessionInfo`）与 **`/debug`** 打印一行：

```text
Agent Source:      project · E:\repo\.agents\agents\explore.md
Agent Source:      builtin · builtin:explore
Agent Source:      profile · E:\repo\examples\profiles\coding\agents\coder\agent.yaml
```

| 字段 | 含义 |
| --- | --- |
| source class | `builtin` / `user` / `project` / `profile` |
| path | 胜出 def 文件路径；builtin 为 `builtin:<name>`（不绝对化）；profile 优先 `agent.yaml`，否则 agent dir |

实现上是 **session 绑定字段**（`ChatSession.AgentSource` / `AgentSourcePath`），不是 discovery registry 的额外 API。

## 4. `spawn_agent` 与 agent_type

子 agent 可只传角色名，其余由 def 补齐：

```json
{
  "agent_type": "explore",
  "message": "Find where permission ModePlan is enforced"
}
```

默认填充（字段为空且调用方未显式给出时）：

- `permission_mode`（explore → `plan`）
- `read_only`（sandbox read-only → true）
- `model` / `provider`（若 def 声明）

**显式参数永远赢**，例如：

```json
{
  "agent_type": "explore",
  "permission_mode": "default",
  "read_only": false,
  "message": "..."
}
```

不会被 explore 的 plan/read-only 默认覆盖。

`completion_requirement` 对普通 `spawn_agent` 固定为 `none`：schema 不再宣传 `complete_task`，显式 snake/camel 或 agentdef 解析出 `complete_task` 会在创建 child 前被拒绝，并提示改用 `spawn_team` / Team assignment。fork 会复制父上下文，但 route context 会覆盖为 `none`；真正的 teammate `complete_task` 只由 `TeammateRunner` 在绑定了 `TeamID` + `CurrentTaskID` 的 `RunMeta` 上注入。

子会话启动时还会按 def 叠加 tool allow/deny、read-only 与 **sandbox profile materialize**（local actor host；workspace 已知时写入 path bounds）。

> Team teammate 的 `profile` 在可解析为 portable agentdef 时与 `spawn_agent` 对齐：投影 `agent_type`、默认 `permission_mode` / `read_only`，RunMeta 采用 def 权限（如 `explore` → `plan`），local/API actor 叠加 tool allow/deny 与 sandbox。未设置 profile 或仅合成 `team_teammate` 时仍默认 `bypass_permissions`（无人值守兼容）。任务 run 的 `complete_task` 仍由 runner 边界强制，不从 def 推断。

## 5. 与 skill `agents/openai.yaml` 的区别

| | Portable AgentDefinition | Skill `agents/openai.yaml` |
| --- | --- | --- |
| 目的 | 会话角色：tools、权限、prompt、completion | skill 展示/兼容元数据（Codex） |
| 加载 | `internal/agentdef` discovery | skill loader companion |
| 是否改变 permission / tool surface | 是 | 否（不应当角色系统） |
| 示例路径 | `.agents/agents/explore.md` | `.agents/skills/*/agents/openai.yaml` |

**不要**把 skill 内 `openai.yaml` 当成 `aicli chat --agent` 的角色源。

## 6. 与 Skills 的关系（避免假阴性）

- Skills **默认启用**（`skills_runtime.enabled` 默认 true；chat 默认暴露 tools/skills）。
- 暴露是 **route + top-k**，不是全量 dump。详见 [`docs/skill_runtime/aicli_skills_usage.md`](../skill_runtime/aicli_skills_usage.md)。
- `aicli exec` 对**纯文本 headless** 常默认 `--disable-tools`，这不是 “skills 没装”，而是 headless 安全默认；需要工具时用 `--enable-tools` / `--yolo` / 非 default permission / 带 profile|agent。
- AgentDefinition 的 `skills: []` 表示走全局 route；非空则是可选白名单（与全局 top-k 叠加，不替代 loader）。

## 7. Permission 可观测（A4 摘要）

决策管道顺序固定：

```text
hooks → rules → grants → readonly_auto → mode → ask / headless_deny
```

- `Decision.Stage` / `Decision.Reason` 带 stage 前缀（如 `readonly_auto:shell_readonly`）。
- Shell 只读表对 `git status` / `rg` / `ls` / `pwd` 等可 auto-allow（default 模式；可关 `DisableReadOnlyAuto`）。
- `bypass_permissions` **不能**绕过 hook deny / hard deny rules。

## 8. 最小验收命令

```powershell
# 从 backend 目录
cd E:\projects\ai\ai-agent-runtime\backend

# unit：agentdef chat / spawn wiring
go test ./cmd/aicli/commands -count=1 -run "TestResolveChatProfileState_Agent|TestResolveChatProfileState_Unknown"
go test ./internal/toolbroker -count=1 -run "TestBroker_Execute_SpawnAgentAppliesAgentdef|TestBroker_Execute_SpawnAgentExplicit"
go test ./internal/agentdef -count=1
go test ./internal/policy -count=1 -run "TestEngineShellReadOnly|TestEngineTaxonomyReadOnly|TestIsShellReadOnly|TestEngineBypass"

# 手工 smoke（需可用 provider）
# aicli chat --agent explore --no-interactive -M "List top-level packages under backend/internal"
```

## 9. ACP 宿主：`aicli agent stdio`

`aicli agent stdio` 把 aicli 作为外部 Agent 协议宿主，在 **stdin/stdout** 上跑 ACP 子集（JSON-RPC 2.0 over NDJSON）。  
它**不是** `aicli chat --agent` 的别名：

| | `aicli chat --agent` | `aicli agent stdio` |
| --- | --- | --- |
| 用途 | 人类/脚本 chat 绑定 Portable AgentDefinition 或 profile agent | IDE/外部客户端嵌入 |
| stdin | 普通用户输入 / 非交互 message | **协议消息流**（不是 prompt 文本） |
| stdout | 人类可读对话输出 | **仅**协议 NDJSON |
| 日志/诊断 | 终端 + log-dir | stderr / 日志文件 |

当前支持的方法（子集）：

- client → agent：`initialize`、`session/new`、`session/prompt`、`session/cancel`、`session/load`
- agent → client：`session/update`、`session/request_permission`
- capability：`loadSession=true`（`session/load` 回放历史为 `session/update`，结果为 `null`）

`session/load` 解析顺序：

1. 进程内已附着的 session（`session/new` 之后同 id 可再 load）
2. 持久化会话（需非 ephemeral，例如 `--session-dir`）；默认 `--ephemeral` 时只支持内存 reattach

MCPServers 参数暂不支持。

```bash
aicli agent stdio --provider openai --model gpt-4o
aicli agent stdio --profile default --permission-mode default
aicli agent stdio --yolo --enable-tools
aicli agent stdio --session-dir ~/.aicli/sessions
```

权限 / profile / agentdef 语义与 chat 共用同一套概念（见上文第 3–7 节）；headless 工具代理与输出契约见 [`docs/aicli/exec.md`](./exec.md)。  
安装侧命令索引见 [`docs/aicli/install.md`](./install.md#agent-acp-宿主概览)。

## 10. 相关文档

- 实施计划 / checklist：[`docs/plan/grok-harness-productization-implementation-plan.md`](../plan/grok-harness-productization-implementation-plan.md)
- 架构校正：[`docs/analysis/grok-build-architecture-learning.md`](../analysis/grok-build-architecture-learning.md)
- Skills 使用：[`docs/skill_runtime/aicli_skills_usage.md`](../skill_runtime/aicli_skills_usage.md)
- Profile 加载：[`docs/multi-agents/profile/aicli_profile_loading_flow.md`](../multi-agents/profile/aicli_profile_loading_flow.md)
- `aicli exec` headless 默认：[`docs/aicli/exec.md`](./exec.md)
- 安装与 CLI 索引：[`docs/aicli/install.md`](./install.md)
