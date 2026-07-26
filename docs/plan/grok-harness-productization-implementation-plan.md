# Grok Harness 产品化实施方案

更新时间: 2026-07-25  
状态: Iteration A 完成（含 A6 Agent Source residual）；**Iteration B 核心已收口**（B1–B6，含 general.md）；**Iteration C 核心已收口**（C1–C3 + C4a doom-loop + C4b tool search + C4c optional OS sandbox）；**R1–R7 residual 已收口**；**R3 pure-advisory strip-on-persist residual 已收口**；旁路 **backtrack frontend residual 已收口**（audit 面板 + dialog edit_prompt + transcript 导航/内联编辑；下一波仅 R8+ 可选产品线）  
权威分析: [`docs/analysis/grok-build-architecture-learning.md`](../analysis/grok-build-architecture-learning.md)

## 0. 一句话目标

在 **已有 Skills Runtime + profile loader + multi-agent runtime** 之上，补齐 Grok 级 harness 产品化能力：

> **Agent 可移植定义 · Permission 管道 · Worktree 隔离 · Plan mode · Plugin/ACP（中期）**  
> **不重做 skill 加载器，不重写 team 编排。**

---

## 1. 背景与校正基线

### 1.1 错误基线（禁止再采用）

| 错误判断 | 为何错误 |
| --- | --- |
| 本项目缺 Skills / 默认不加载 | `skills_runtime.enabled` 默认 true；`.agents/skills` + loader/registry/router/executor/hot-reload/MCP/API/`skill__*` 已落地 |
| Agents 缺失或未用 | runtime multi-agent（`spawn_agent` / `spawn_team` / AgentControl）已在用；profile 加载器已实现 |
| 首要任务是从零建 skills/agents | 真差距在 harness 产品化，不是底座缺失 |

Skills 看起来“没用”的假阴性：

1. 暴露是 **route + top-k**，不是全量 dump  
2. `aicli exec` 常默认 **disable-tools**  
3. aicli 路径 **DiscoverOnly / lazy hydrate**（仍加载 summary）

### 1.2 Agents 三层（必须拆开）

| 层 | 状态 | 本方案动作 |
| --- | --- | --- |
| Runtime multi-agent：`spawn_agent` / `spawn_team` / AgentControl | 已在用 | 叠加 isolation / completion / role def |
| Profile 包：`profile.yaml` + `agents/*/agent.yaml` | 加载器已实现；仓库样例已补（如 coding） | 与 `AgentDefinition` 互转；继续扩样例可选 |
| Skill 元数据：`agents/openai.yaml` | 存在 | **不**当作角色 def |
| Grok 式可移植 Agent 角色 def | **已交付**（`internal/agentdef` + 项目 agents） | A 主交付已完成；MiniJinja / teammate 更深对齐见 §15 |

### 1.3 定位对照（产品决策用）

| | Grok Build | 本项目 |
| --- | --- | --- |
| 形态 | 本地 coding harness | 可服务化 multi-agent runtime |
| Skills | 产品面强 | runtime 更深，默认启用 |
| Agents | `.md` 角色 def 一等公民 | runtime 强；profile 有样例；**md AgentDefinition 已交付** |
| 安全 | OS sandbox + 多层 permission | 应用层 sandbox profiles + permission 管道 **已产品化**；Linux 可选 bwrap（默认 off） |
| 编排 | subagent + worktree | team 任务图 / mailbox / claims **更强**；worktree isolation **已可选** |

### 1.4 相关已有资产（实施时优先复用）

| 资产 | 路径 / 说明 |
| --- | --- |
| Profile 解析 | `backend/internal/profile` → `ResolvedAgent` |
| Profile → runtime 输入 | `backend/internal/profileinput` |
| Permission engine | `backend/internal/policy`（已有 hooks / rules / mode / ask） |
| Agent 循环 | `backend/internal/agent` |
| Chat actor | `backend/internal/chat` |
| Toolkit / toolbroker | `backend/internal/toolkit`, `toolbroker` |
| Team / AgentControl | `backend/internal/team`, `agentcontrol` |
| Skills Runtime | `backend/internal/skill`, `.agents/skills` |
| Task outcome 契约 | `docs/skill_runtime/team_task_outcome_contract.md` |
| Multi-agent 后续计划 | `docs/plan/multi-agent-next-stage-implementation-plan.md`（不重复其 registry/panel 主线） |

---

## 2. 总目标与非目标

### 2.1 总目标

1. 用户可用 **文件化 AgentDefinition**（markdown/yaml）声明角色，并被 `aicli chat` / API / `spawn_agent` / team teammate 统一消费。  
2. 工具授权成为 **可解释管道**（hooks → rules → grants → read-only auto → mode），而不仅是 `permission_mode` 开关。  
3. 子 agent 可选 **worktree 隔离**；plan mode 成为可审批产品流。  
4. 中期具备 plugin 打包与 ACP 子集嵌入能力，而不牺牲现有 HTTP/SSE 控制面优势。

### 2.2 非目标

- 不重写 Skills Runtime loader/registry/executor。  
- 不把本项目改造成 Grok TUI / Computer Hub 克隆。  
- 不替换 AgentControl / team task graph 主路径。  
- 不在 Iteration A 引入 OS 级 sandbox 强制（仅 profile 化应用层）。  
- 不把 skill 内 `agents/openai.yaml` 升级成角色系统。  
- 不在未完成 AgentDefinition 映射前大改全部 runtime API 路由。

---

## 3. 成功标准

### Iteration A 完成定义

- [x] 仓库存在可运行的 profile/agent 样例（≥1 profile，≥1 agent）。  
  - `examples/profiles/coding/` + 项目 `.agents/agents/explore.md` / `plan.md`
- [x] `AgentDefinition` 可从项目/用户目录 discovery，并映射为 `ResolvedAgent` 或等价 runtime binding。  
  - 包：`backend/internal/agentdef`（parse / discover / BuildBinding / ToResolvedAgent）
- [x] `aicli chat --agent <name>`（或等价入口）能加载文件化 agent 的 tools/permission/skills/prompt。  
  - 无 profile 时走 portable agentdef；显式 CLI 覆盖 def 默认
- [x] `spawn_agent` / team worker 可指定 agent def id 或内置 role。  
  - `spawn_agent.agent_type` → permission/read_only/model/provider 默认 + child tool policy；team teammate 更深对齐可后续收紧
- [x] Permission 决策 trace 至少输出：stage、decision、reason（debug/log 或 `/debug`）。  
  - `policy.Decision.Stage` / `Reason`（`hooks`/`rules`/`grants`/`readonly_auto`/`mode`/…）
- [x] Shell 只读命令表对 `git status` / `rg` / `ls` 类命令可 auto-allow（在 default 模式下可配置）。  
  - `IsShellReadOnlyCommand` + `DisableReadOnlyAuto`
- [x] Worker `completionRequirement` 与 team task outcome 对齐，未 complete 时有 reminder/recovery 路径。  
- [x] 文档澄清：skills 默认启用、top-k 暴露、agents 三层含义。  
  - `docs/aicli/agents.md` + `docs/skill_runtime/aicli_skills_usage.md`

### Iteration B 完成定义

- [x] `spawn_agent isolation=worktree` 可创建/回收隔离目录，完成 summary（可选 apply）。  
  - 包：`backend/internal/isolation/worktree`（Create/Remove/DiffStat/Apply）  
  - 接线：`toolbroker.SpawnAgentArgs.isolation` + local/API spawn bind `workspace_path`；completion 仅 annotate（**无 auto-apply / 无 auto-discard**）；`close_agent` 仍 cleanup  
  - 父侧工具面：`apply_agent_worktree` / `discard_agent_worktree`（local + API controllers + broker）  
  - 失败 fail-closed（无主树 fallback）；默认 `none` 保持现有 multi-agent 行为  
  - 隔离会话默认 claim 隔离根：`AgentSessionContextWritePaths=[worktree path]`（local + API）  
  - ambient team PathClaimManager 正式 Acquire/Release 已收口：`Acquire` 冲突检查 + `ReleaseTaskPathClaims` + registry `WithClaims` 生命周期侧效应（terminal/release/retry/renew/block）；原子 claim 仍走 `ClaimTaskWithPathClaims`  
- [x] 内置 `explore`（只读）与 `plan`（只写 plan）角色可用。  
- [x] Plan mode：enter → 写 `plan.md` → exit 审批（approve / request changes）。  
- [x] Project durable memory 最小可用（文件 + 检索入口）。  
- [x] 应用层 sandbox profiles（`off|workspace|read-only|strict`）可 enforce，无法 enforce 时显式降级告警。  
- [x] Hooks 补强：Stop 可阻断结束；StopFailure 异步通知；PreCompact 可跳过压缩；PostCompact 异步通知。

### Iteration C 完成定义

- [x] tool protocol/stream contract 统一一层（C1 包 + agent emit + CLI + session SSE live 已接线；C2/C3 已落地；C4a doom-loop / C4b tool search / C4c optional OS sandbox 已落地）。  
- [x] plugin 安装/信任/装载最小闭环（`internal/plugins` + `aicli plugin` + skill/hooks/agentdef 接线；无 marketplace）。  
- [x] `aicli agent stdio` ACP 子集：protocol 包 + host 接线 + permission 桥 + 单元测试（initialize/session/prompt/cancel/update）。  
- [x] C4a doom-loop product surface：`DoomLoopTracker` + warning/terminated 事件 + metrics（硬停仍 opt-in）。  
- [x] C4b tool search / dynamic listing：`ShouldList` + 大目录 `search_tool` 投影 + 单测。  
- [x] C4c optional Linux OS sandbox：`OSSandboxBackend` + bubblewrap（linux）/ stub（非 linux）+ fail-closed/explicit degrade。

---

## 4. 阶段拆分

```text
Iteration A (2–3 周)  定义层 + 权限管道 + 样例 + completion
        │
        ▼
Iteration B (3–5 周)  worktree 隔离 + plan mode + memory + sandbox profiles
        │
        ▼
Iteration C (中期)    tool protocol 抽出 + plugin + ACP + doom-loop + 可选 OS sandbox
```

---

## 5. Iteration A — 定义层 + 权限管道（P0）

> 前提：Skills Runtime / profile loader / team multi-agent **已存在**。本迭代只做产品化叠加。

### A1. `AgentDefinition` 格式与发现 — **done**

**目标**：Grok 式可移植角色 def，与现有 profile 包共存。

建议格式（Markdown frontmatter + body，YAML 等价亦可）：

```yaml
# .agents/agents/explore.md  (frontmatter)
name: explore
description: Read-only codebase explorer
tools:
  - view
  - grep
  - glob
  - ls
  - shell   # 受 read-only shell 表约束
disallowedTools:
  - write
  - edit
  - apply_patch
permissionMode: plan   # default|accept_edits|plan|bypass_permissions|dont_ask
skills: []             # 可选 skill id 白名单；空=走全局 skills route
model: null            # 可选覆盖
promptMode: extend     # extend|full
completionRequirement: none  # none|complete_task
sandbox: read-only
```

Body：role / system 指令（extend 时追加到默认 system；full 时替换 role 段）。

发现顺序（后写覆盖策略需在实现中固定并测）：

1. built-in（代码内嵌最小集：`explore` / `plan` / `general` 可先 stub）  
2. 用户：`~/.aicli/agents/*`  
3. 项目：`.agents/agents/*` 或 `.aicli/agents/*`  
4. profile 包内 `agents/*/agent.yaml`（经 adapter 转成同一 runtime 对象）

**新建模块建议**：

| 模块 | 职责 |
| --- | --- |
| `backend/internal/agentdef` | parse / validate / discover / list |
| `backend/internal/agentdef/build.go` | `AgentDefinition` → runtime binding |

**与 `ResolvedAgent` 映射**（核心字段）：

| AgentDefinition | ResolvedAgent / runtime |
| --- | --- |
| name | AgentID |
| body + promptMode | Prompts.Role / system merge |
| tools / disallowedTools | ToolPolicy allow/deny |
| permissionMode | session/agent effective permission mode |
| skills | SkillDirs 过滤或 session skill allowlist |
| model | Model / Provider 覆盖 |
| sandbox | ToolPolicy.Sandbox |
| completionRequirement | worker loop / team outcome 策略 |

**验收**：

- [x] unit：parse 合法/非法 frontmatter  
- [x] unit：discovery 多路径优先级  
- [x] unit：→ `ResolvedAgent` / `profileinput.ResolvedAgent` 字段对齐  
- [x] integration：`aicli chat` 或 test harness 加载自定义 agent 文件（`resolveChatAgentdefState` + chat_profile tests；`spawn_agent` agentdef defaults tests）

### A2. 仓库内 profile/agent 样例 — **done**

**目标**：消除“agents 未用”观感，作为 golden path。

建议目录（最终路径以 `internal/profile/paths.go` 约定为准，实施时对齐）：

```text
examples/profiles/coding/
  profile.yaml
  agents/
    default/
      agent.yaml
      prompts/role.md
    explore/
      agent.yaml
      prompts/role.md
  tools/policy.yaml   # 若约定存在
```

或项目根：

```text
.agents/agents/explore.md
.agents/agents/plan.md
```

**验收**：文档中一条命令可跑通样例加载；CI 可选 smoke（无真实 LLM 时至少 resolve+validate）。  
用户指南：[`docs/aicli/agents.md`](../aicli/agents.md)。

### A3. Tool taxonomy（read_only / mutates / kind） — **done（核心路径）**

**目标**：权限管道与并行调度依赖稳定 taxonomy，而不是各工具散落判断。

建议在 toolkit 元数据统一：

```go
type ToolMeta struct {
    Name        string
    Kind        string   // read|search|edit|exec|network|control|...
    ReadOnly    bool
    MutatesFS   bool
    RequiresNet bool
    // optional: ShouldList, stream capabilities
}
```

落地策略：

1. 为现有核心工具标注（view/grep/glob/ls/shell/write/edit/apply_patch/spawn_* …）。  
2. `policy.CapabilityResolver` 优先读 taxonomy。  
3. parallel tool scheduler 继续依赖“只读 + engine 安全”判定，但数据源统一。

**主要触点**：

- `backend/internal/toolkit/**`  
- `backend/internal/policy/**`  
- `backend/internal/agent/tool_parallel_scheduler.go`

### A4. Permission pipeline 产品化 — **done（核心管道 + 单测）**

**当前**：`policy.Engine` 已有 hooks / rules / mode / ask 骨架。  
**已落地**：remembered grants、read-only auto、shell 只读表、stage/reason trace、dangerous 不记忆 always-allow、bypass 仍尊重 hook deny。  
**R1 已加深（2026-07-25）**：项目 `.aicli/permissions.yaml` + CLI `--allow-tool`/`--deny-tool` + session/`/debug` 展示；见 [`docs/product/project-permissions.md`](../product/project-permissions.md)。

目标管道（固定顺序，写进代码注释与文档）：

```text
1. PreToolUse hooks          → deny|ask|allow(+patch)
2. Rule engine               → deny > ask > allow（全局/项目/CLI）
3. Remembered grants         → per-project，排除 dangerous
4. Taxonomy read_only auto   → allow（可配置关闭）
5. permission_mode policy    → default|accept_edits|plan|bypass|dont_ask
6. Ask handler / headless    → 无 UI 则 deny 并回传模型（dont_ask）
```

实施项：

1. **规则源**  
   - 全局 config  
   - 项目 `.aicli/permissions.yaml`（或等价）  
   - CLI `--allow-tool` / `--deny-tool`（名称可调整，需与现有 flag 兼容）  
2. **Grants store**  
   - 项目级文件或 session meta；API：`remember(tool, pattern, scope)`  
   - dangerous list 永不写入 always-allow  
3. **Shell 只读表**  
   - 解析 argv0 + 子命令（至少覆盖 git/rg/findstr/dir/ls/Get-ChildItem/pwd 等）  
   - 未知或写操作 → 不走 read-only auto  
4. **决策 trace**  
   - `Decision.Reason` 结构化 stage 前缀，如 `rule_deny:` / `grant:` / `readonly_auto:` / `mode_bypass:`  
   - debug 事件或 log 字段 `permission_stage`  
5. **Plan mode 硬约束预埋**  
   - mode=plan 时：仅允许读 + 写 plan 文件路径（完整 UX 在 B）

**主要触点**：

- `backend/internal/policy/engine.go` 及 rules/grants 新文件  
- `backend/internal/agent/permission_engine.go`（别名层保持兼容）  
- `backend/internal/chat/actor.go`（注入 engine 配置）  
- hooks 现有 PreToolUse 路径

**验收**：

- 表驱动测试：每种 stage 的 allow/deny/ask  
- dangerous 命令 grant 被拒绝  
- headless + ask → deny 且 reason 明确  
- bypass 仍受 hooks deny 与（可选）hard deny rules 约束——**产品决策写死并测**

> 产品决策（默认建议，可在实现 PR 中确认）：  
> - `bypass_permissions` **不能**绕过 PreToolUse hook deny 与 hard deny rules  
> - `bypass` **可以**跳过 ask 与 remembered-grant 流程  

### A5. Worker `completionRequirement` 与 team outcome — **done**

**目标**：对齐 Grok worker 必须 `complete_task` 的 harness 约束，复用已有 team outcome contract。

实施项：

1. [x] AgentDefinition / spawn 选项：`completionRequirement: none|complete_task`  
2. [x] Team worker / 指定 child 默认 `complete_task`（可配置）  
3. [x] Loop 结束前检查：未调用 complete → system reminder + 有限次 recovery turn  
4. [x] 与 `report_task_outcome` / team task status 字段对齐，避免双写语义分叉  
5. [x] **A5 residual（2026-07-26）**：session `Success=false`（complete_task 未观察 tool）时仍解析 terminal structured JSON fallback；`task_status=blocked|handoff|done` 恢复为 orchestrator 可消费的 Success/Blocked，走 `BlockTask`/complete 而非硬 `task.failed`

**主要触点**：

- `backend/internal/agent/loop*.go` + `completion_requirement.go`  
- `backend/internal/team/teammate_runner.go`（`applyStructuredTaskOutcome`）  
- team worker 启动路径 / `spawn_agent` / `spawn_subagents` / session actor loop config  
- `docs/skill_runtime/team_task_outcome_contract.md`  
- 回归：`cmd/aicli/commands` docs team regression + `internal/team` structured recovery tests

### A6. 文档与可观测 — **done（文档主路径 + Agent Source residual）**

新增/更新：

1. [x] `docs/aicli/agents.md`  
   - 如何写自定义 agent  
   - 与 profile 包关系  
   - 与 skill `openai.yaml` 的区别  
   - `aicli chat --agent` / `spawn_agent.agent_type` / 优先级  
2. [x] Skills 暴露说明补强：`docs/skill_runtime/aicli_skills_usage.md`  
   - 默认启用  
   - top-k / route / registered vs exposed  
   - exec disable-tools 假阴性  
3. [x] 分析文档已校正；本 plan 为实施权威 checklist。  
4. [x] CLI 帮助：`aicli chat --agent` 示例与 flag 文案支持无 profile portable def。

CLI 可观测（最小 / 部分后续）：

- [x] session info + `/debug` 显示 resolved agent def **Agent Source**（`source · path`；`builtin:` 保留字面量，文件路径绝对化）  
  - 会话字段：`ChatSession.AgentSource` / `AgentSourcePath`（绑定 resolve 时写入，非 registry 新字段）  
  - profile 包固定 `source=profile`，路径优先 `agent.yaml` 否则 agent dir  
- [x] `/skills` / `/functions` 可对照 registered 与 exposure report（top-k 文档已说明）

---

## 6. Iteration B — 隔离 + Plan + Memory（P1）

### B1. Worktree isolation

- [x] `spawn_agent` 增加 `isolation: none|worktree`（schema + NormalizeMode）  
- [x] 模块：`backend/internal/isolation/worktree`  
- [x] 生命周期：create → bind cwd/session paths → run → completion annotate（保留 worktree）→ parent `apply_agent_worktree` / `discard_agent_worktree` 或 `close_agent` cleanup  
  - completion **不** auto-apply / **不** auto-discard（避免挡住父侧显式 apply）  
  - apply 默认 remove worktree（`keep=false`）；`keep=true` 仅写 disposition  
  - discard remove + clear isolation path context  
- [x] Windows：git worktree 路径验证；失败明确 error，不静默 fallback 到污染主树  
- [x] 与 path claims / team write_paths 协同：隔离会话默认 claim 隔离根（`write_paths` session context = worktree root；local + API）  

### B2. 内置角色 explore / plan

| 角色 | tools | permission | completion |
| --- | --- | --- | --- |
| explore | 只读工具 + 只读 shell 表 | plan 或 default+readonly | none |
| plan | 读 + 写 `plan.md` | plan | 可选 complete_task |
| general | 继承父或 profile | 可配置 | team worker 默认 complete_task |

- [x] `.agents/agents/explore.md`：只读工具面 + `permissionMode: plan` + `sandbox: read-only`  
- [x] `.agents/agents/plan.md`：读 + 写 plan 路径 + `permissionMode: plan`  
- [x] plan 角色子 agent 启动时 `EnsurePlanWriteAllowPaths`（`chat_actor_host.applyLocalChildAgentdefToolPolicy`）  
- [x] `.agents/agents/general.md`：`permissionMode: default` + `sandbox: workspace` + **`completionRequirement: none`**（交互/general chat）  
  - team worker **不**靠 general.md 改 completion：`TeammateRunner` 仍强制 `RunMeta.CompletionRequirement=complete_task`

### B3. Plan mode 产品流

```text
/user or tool: enter_plan_mode
    → session.state = plan
    → 工具写路径限制为 plan.md（及显式 allow）
    → agent 产出计划
/user or tool: exit_plan_mode
    → UI/CLI 审批：approve | request_changes | quit
    → approve 后退出 plan 限制并可选开始执行
```

- [x] `backend/internal/planmode`：Enter/Exit/Load/Save/ApplyToEngine/ResumeModeAfterExit  
- [x] `policy.Engine.PlanWriteAllowPaths` + plan 模式写路径 allow/deny  
- [x] aicli slash `/plan`：`status|enter|exit(approve|request_changes|quit)` + 会话持久化  
- [x] actor/host 运行前 `applyPlanMode*` 强制 plan 写限制  
- [x] 单元测试：`planmode`、`policy` plan paths、`/plan` command、plan 下只读 shell 允许 / 变更 shell 拒绝  
- [x] 专用 tool：`enter_plan_mode` / `exit_plan_mode`  
  - `toolbroker.PlanModeController` + definitions（`PlanMode != nil` 时暴露）  
  - `SessionActor.EnterPlanMode` / `ExitPlanMode`：持久化 `planmode` 状态、live engine、`RunMeta.PermissionMode` mid-turn 更新  
  - policy taxonomy/caps：`CapReadOnly` + `CapAskUser`（plan 模式下可调用）  
  - 单测：broker definitions/execute、actor enter/nested/approve/request_changes/quit/bare-mode/RunMeta、policy allow under ModePlan  
- [x] frontend plan preview（session plan API client + `useRuntimePlanMode` + Artifact panel Plan 页签：approve / request_changes / quit）

触点（均已接线）：

- `permission_mode=plan` 已有枚举处  
- `backend/internal/chat/actor.go` 状态机  
- aicli slash `/plan`  
- frontend：`use-runtime-plan-mode` + `artifact-panel-plan-surface` + session plan API

### B4. Project durable memory（最小）✅

- 存储：项目 `.aicli/memory/notes.jsonl`（`backend/internal/memorystore`；可显式 root / profile fallback）  
- API：append / list / keyword search（无 embedding、无云同步）  
- 注入：`contextmgr` 动态层 `project_memory`，top-k + token budget；agent 从 `workspace_path` soft-attach  
- CLI：`/memory [status|add|list|search]`（slash catalog + args completion）  
- **不做**跨用户云同步 / FTS / vector / session-end 自动摘要（可后置）  

### B5. 应用层 sandbox profiles ✅

```text
off | workspace | read-only | strict
```

映射到现有 `executor` / tool policy paths + network flags；无法 enforce 时 **显式降级告警**，不静默当 strict。

落地要点：

- 解析器：`backend/internal/executor/sandbox_profile.go`  
  - `off`：disabled  
  - `workspace`：`AllowedPaths=[workspace]`  
  - `read-only`：tool ReadOnly + allow/readOnly paths + denied commands  
  - `strict`：read-only + `BlockNetwork` + env whitelist + 保守 command allowlist  
- `SandboxConfig` 增补 `Profile` / `BlockNetwork`；`CheckURL` 在 BlockNetwork 时拒绝全部 URL  
- 接线：  
  - `profileinput.BuildToolExecutionPolicyWithWorkspace` / `MaterializeSandboxForWorkspace`  
  - chat：agentdef/profile 载入 → session bootstrap 在 workspace 已知后 materialize + stderr/log 告警  
  - child agentdef（`chat_actor_host`）与 API `applyAgentExecutionPolicy` 同步 materialize  
- 降级策略（显式 warning，不 fail-open 静默）：  
  - workspace 无 root → `off`  
  - strict 无 root → effective `read-only`，**保留** network/command/env 限制  
  - read-only 无 root → 保留 ReadOnly/命令限制，path bounds 告警  
- 单测：`sandbox_profile_test.go` + `TestBuildToolExecutionPolicyWithWorkspace_NamedProfiles`  
- OS-level sandbox backend 见 **C4c**（bubblewrap + stub + off|auto|require）

### B6. Hooks 补强 ✅

- [x] 事件：`EventStop` / `EventStopFailure` / `EventPreCompact` / `EventPostCompact`（`internal/hooks/types.go`）  
- [x] Stop gate：成功无 tool-calls 终局时 `DecisionBlock` 可注入 recovery 并继续（有 step budget）→ `hooks.stop_blocked`  
- [x] StopFailure：非成功终局异步 `DispatchAsync`  
- [x] PreCompact：block → `session_compact_skipped` / `context.*.skipped` + `reason=pre_compact_hook_blocked`  
- [x] PostCompact：成功压缩后异步通知  
- [x] 单测：`TestReActLoop_StopHookBlockForcesContinuation`、`TestReActLoop_PreCompactHookBlockSkipsCompaction`

---

## 7. Iteration C — 平台化（P2）

### C1. Tool protocol 层 — **done (core + live SSE + protocol_result)**

- [x] `internal/toolprotocol`：Id / Kind / Result / Error / Progress notification  
- [x] toolkit 保持执行权威；toolbroker 只路由（不替换）  
- [x] Agent 绑定 `toolprotocol.Reporter`（sequential / parallel / approved），工具经 `Report(ctx, Progress)` 发射 `tool.progress`  
- [x] aicli chat bridge 渲染 `tool.progress` 紧凑 timeline + 轻量 stage detail  
- [x] 默认 **不** 持久化 progress 到 session event store（高频 live-only）  
- [x] SSE：`StreamSessionRuntimeEvents?live=1` 订阅 in-process bus 转发 `tool.progress`（`live=true`，不落盘）  
- [x] `tool.completed` 附带紧凑 `protocol_result`（`toolprotocol.Result.EventMap`）

消费约定：

| 通道 | `tool.progress` |
| --- | --- |
| Agent event bus + retention | 有（Query / ListRuntimeEvents） |
| Session durable store | 无（`shouldPersistRuntimeSessionEvent` 排除） |
| StreamSessionRuntimeEvents（默认） | 无（只读 durable store） |
| StreamSessionRuntimeEvents（`?live=1`） | 有（bus fan-out，`live=true`，best-effort drop） |
| aicli chat runtime bridge | 有（active run 内 timeline + stage） |

`tool.completed.payload.protocol_result`：嵌套 portable Result 视图（ok/outcome/output_kind/summary/error/thin metadata）；扁平 disposition 字段仍权威，供 chat-log 离线分析。

### C2. Plugin 打包

- 单元：skills + hooks + mcp 描述 + agents  
- 安装路径、信任标记、热装载与 skills 目录协同  
- **不做**完整 marketplace（可后置）

**状态（2026-07-25）：最小闭环已落地**

| 面 | 实现 |
| --- | --- |
| 包模型 | `backend/internal/plugins`：`plugin.yaml` + skills/agents/hooks/mcp；默认 untrusted |
| 信任 | `~/.aicli/plugins/state.json`（或 `$AICLI_HOME/plugins`）；仅 trusted+enabled 贡献 |
| CLI | `aicli plugin install/list/trust/untrust/enable/disable` |
| 热装载 | `MergeSkillDirs` → `resolveConfiguredSkillDirs` / `resolveChatSkillDirs`；hooks → `chat_actor_host`；agents → `agentdef.ExtraDirs` |
| 约束 | 不重建 skill loader / team 编排；无 marketplace |

### C3. ACP 子集

- [x] protocol：`backend/internal/acp`（Conn/Server/types；handlers 异步防 cancel 死锁）  
- [x] host：`aicli agent stdio` → `acpSessionHost` 复用 exec/chat bootstrap  
- [x] bridge：runtime/chatcore events → `session/update`；approvals → `session/request_permission`  
- [x] 约束：stdout 仅 NDJSON；tools 默认开；ephemeral 默认 true  
- 目标：IDE/外部 host 可嵌入，不替代 HTTP runtime-server  
- 残余：端到端真实 LLM 联调（非单测）可选  
- R6：`session/load` 已实现（`LoadSession=true`；host 内存 reattach + 可选 durable load；回放 `session/update` 后返回 null）

### C4. Harness 打磨

#### C4a. Doom-loop productization — **done**

**目标**：把已有 semantic tool-call 指纹/软提醒/可选硬停产品化为稳定 harness 面，而不是重写 loop。

**已落地**：

1. `backend/internal/agent/doom_loop.go`
   - `DoomLoopTracker`：连续相同 tool+args batch 计数
   - 警告阈值 `DoomLoopWarningThreshold=4`（always-on soft warning）
   - 硬停：`agent.maxRepeatedToolCalls` / `LoopReActConfig.MaxRepeatedToolCalls`；默认 **0=关闭**（兼容 headless）
   - 豁免 polling/control 工具：`background_task` / `wait_agent` / `read_agent` / `list_agents` / `get_agents` / `get_goal` / `read_goal`
2. 稳定事件（dual-emit）：
   - legacy：`tool_loop.repeated_semantic_call_observed`
   - product：`tool_loop.doom_loop_warning` / `tool_loop.doom_loop_terminated`
   - payload 含 `phase`、`kind=semantic_tool_repeat`、`tool_call_fingerprint`、`repeat_count`
3. metrics：`doom_loop_total{phase=warning|terminated}`
4. 并行 partial/empty 全量重放仍走 disposition advisory（**不**单独当成另一类 doom-loop）
5. 单测：tracker unit + loop warning dual-emit + hard-stop termination event

**产品决策**：

- soft advisory（repeat≥2）与 warning 事件默认开启  
- hard stop 保持 config opt-in（不静默收紧 yolo/headless 路径）  
- 不做 Grok 式 mid-stream resample recovery（后续可选）

#### C4b. Tool search / dynamic listing（`ShouldList`） — **done**

**目标**：大工具目录下降低模型直接可见 surface，同时保留可搜索/可执行能力；小目录与 simple-goal 路径保持兼容。

**已落地**：

1. `backend/internal/toolkit/listable.go`
   - `ListToolsContext` / optional `ListableTool.ShouldList`
   - metadata：`should_list` / `list_when` / `core_tool` / `defer_loading`
   - 默认 listable（未实现接口且 metadata 未隐藏时）
2. `backend/internal/toolkit/search.go` + `search_tool.go`
   - 内存 BM25-lite 索引（`InMemoryToolSearchIndex`）
   - harness meta-tool `search_tool`（`DefaultToolSearchThreshold=24`）
3. Registry / MCP adapter 上下文过滤
   - `Registry.ListForContext` / `GetToolSchemasForContext`
   - `RegistryToMCPToolsForContext`
4. Agent loop 接线（`tool_list.go` + `loop.go` + eligibility catalog）
   - `filterToolDefinitionsByShouldList` 作用于 turn listing 与 eligibility catalog
   - 大目录：`projectToolSurfaceWithSearch` 保留 core + 注入 `search_tool`，投影 non-core（尤其 `defer_loading`）
   - **simple-goal projection 优先**于 search projection
   - eligibility catalog **不做** search projection（binding key 跟踪完整授权面）
   - `search_tool` 在 broker 工具之后、`spawn_subagents` 之前执行，检索 pre-projection catalog
5. 单测：metadata/`ListableTool` 过滤、registry hide、search 排序/空查询、`SearchTool.Execute` JSON/empty、projection 注入/simple-goal 优先、projected 工具可搜

**产品决策**：

- 小目录（<24）不注入 search，避免无谓 meta-tool  
- 投影只影响 model-facing list；被投影工具仍可执行 + 可搜索  
- 不重写 skill loader / team orchestration  

#### C4c. 可选 Linux OS sandbox backend — **done**

**目标**：在 B5 应用层 sandbox profiles 之上叠加可选 OS 级进程隔离；有 backend 才声称 isolation，无则 **显式降级** 或 **fail-closed**，绝不静默“看起来 sandboxed”。

**已落地**：

1. `backend/internal/executor/os_sandbox.go`
   - `OSSandboxBackend` / `OSSandboxRequest` / `OSSandboxLaunch`
   - 模式：`off`（默认）| `auto`（有则 wrap，无则 warning）| `require`（无则 fail-closed）
   - `Sandbox.PrepareOSCommand` / `WithOSBackend` / `CollectOSSandboxWarnings`
2. Linux bubblewrap backend（`//go:build linux`）
   - `os_sandbox_linux.go`：`bwrap` LookPath + Wrap
   - `os_sandbox_bwrap_plan.go`：纯函数 argv 规划（namespace / ro host roots / path binds / optional `--unshare-net`）
3. 非 Linux stub（`//go:build !linux`）
   - `Available=false`；`Wrap` 明确报错；Windows CI 不依赖 bwrap
4. 接线
   - `SandboxConfig.OSSandbox` + clone/overlay/active/profile map decode（`osSandbox`/`os_sandbox`）
   - `Sandbox.ExecuteCommand` 与 `toolkit/tools/bash` 走 `PrepareOSCommand`
   - profile materialize / API agent sandbox 收集 OS degrade warnings
   - config validation：非法 `osSandbox` 拒绝
5. 单测：mode 归一化、auto degrade、require fail-closed、wrap 成功/失败、platform default backend、bwrap plan binds/network、overlay/decode

**产品决策**：

- 默认 `off`：既有部署保持纯应用层，不强制 OS 隔离  
- `auto` 显式 warning，不静默宣称 isolation  
- `require` 在 backend 不可用或 wrap 失败时 fail-closed  
- 不做 full rootfs / Computer Hub / 远程 workspace daemon；thin adapter + policy  
- 应用层 path/network/command 策略始终有效，OS wrap 仅加强进程隔离  

---

## 8. 模块级改造清单

### 8.1 新增

| 模块 | Iteration | 职责 |
| --- | --- | --- |
| `internal/agentdef` | A | 定义解析/发现/校验/列表 |
| `internal/agentdef/build` | A | 构建 runtime binding |
| `internal/tooltaxonomy` 或 toolkit meta | A | Kind/read_only/mutates |
| `internal/policy/grants` | A | remembered grants |
| `internal/policy/shellreadonly` | A | shell 只读判定 |
| `internal/isolation/worktree` | B | git worktree 隔离 |
| `internal/planmode` | B | plan.md 生命周期 |
| `internal/memorystore` | B | project memory |
| `internal/plugins` | C | plugin 装载 |
| `cmd/aicli agent` | C | ACP/stdio 入口 |

### 8.2 改造

| 现有 | 改造 |
| --- | --- |
| `internal/profile` | 与 agentdef 互转；不删除现有包语义 |
| `internal/policy.Engine` | 固定管道顺序；trace；grants；readonly auto |
| `internal/agent` loop | completionRequirement；permission trace |
| `internal/chat/actor` | agent def 解析入口；plan state |
| `toolkit` / `toolbroker` | taxonomy；progress（C 加深） |
| `team` / `agentcontrol` | isolation 字段；role def id；outcome 对齐 |
| `executor/sandbox` | profile 化（B） |
| `hooks` | Stop/Failure 等（B） |

---

## 9. 建议实施顺序（Iteration A 两周切片）

### Week 1 — done

1. [x] `agentdef` parse + validate + discover（含单测）  
2. [x] 映射到 `ResolvedAgent` / profileinput  
3. [x] 仓库样例 + 加载文档草稿  
4. [x] toolkit 核心工具 taxonomy 标注  

### Week 2 — done（A residual）

5. [x] permission 管道：grants + readonly auto + shell 表 + trace 测试（规则源文件产品化可加深）  
6. [x] chat/spawn 接线 agent def id（`chat --agent` 无 profile；`spawn_agent` defaults + child tool policy）  
7. [x] completionRequirement 最小闭环（team worker）  
8. [x] 文档定稿：agents 三层、skills 假阴性、自定义 agent 指南  
9. [x] 单测 smoke：chat agentdef resolve + spawn agentdef defaults + policy readonly/bypass  

### 并行注意

- 不阻塞 `multi-agent-next-stage` 的 registry/panel 工作；agentdef 对 AgentControl 只增加可选元数据字段。  
- 任何 permission 行为变更需有兼容开关或 changelog（避免 yolo/bypass 用户路径静默变严或变松）。

---

## 10. 测试与验收矩阵

| 层级 | 内容 |
| --- | --- |
| Unit | agentdef parse；discovery 优先级；policy 各 stage；shell readonly 表；mapping 字段 |
| Integration | 加载样例 profile/agent；spawn_agent 带 def；worker 未 complete 触发 reminder |
| CLI smoke | `/skills` 计数；加载 agent 后 tools 面收敛；plan mode 写限制（B） |
| 回归 | 现有 multi-agent / skills API / permission_mode 枚举测试全绿 |
| 非功能 | 决策 reason 稳定可断言；错误信息不含敏感路径泄露策略与现网一致 |

---

## 11. 风险与缓解

| 风险 | 缓解 |
| --- | --- |
| 双轨定义（profile 包 vs markdown def）认知混乱 | 文档强制三层图；runtime 统一 binding 类型；样例只推一种主路径 |
| Permission 变严导致旧脚本失败 | 默认保持兼容；新规则 opt-in；trace 可诊断 |
| Worktree 在 Windows 不稳定 | B 阶段先 feature flag；失败明确报错 |
| 范围膨胀到重写 toolkit | C 才抽 protocol；A 只加 meta 字段 |
| 与 multi-agent 计划抢同一文件 | 本 plan 触碰 agentcontrol 仅加可选字段；大改走既有 multi-agent plan |

---

## 12. 里程碑交付物

| 里程碑 | 交付 |
| --- | --- |
| A | `internal/agentdef` + 样例 + permission 管道增强 + completionRequirement + 用户文档 — **delivered**（见 §3 / A1–A6） |
| B | worktree isolation + explore/plan 角色 + plan mode UX + memory MVP + sandbox profiles + lifecycle hooks — **B1–B6 delivered**（ambient PathClaim formal lifecycle closed） |
| C | toolprotocol 层 + plugin MVP + ACP stdio 子集 + doom-loop(C4a) + tool search(C4b) + optional OS sandbox(C4c) — **C core delivered** |

---

## 13. 参考

- 架构学习与校正（**为什么**）：`docs/analysis/grok-build-architecture-learning.md`  
- 本方案 §15：需求覆盖矩阵与残余 backlog（**验收 / 非目标 / 下一波**）  
- Profile 实施状态：`docs/multi-agents/profile/implementation-status.md`  
- Skills 使用：`docs/skill_runtime/aicli_skills_usage.md`  
- Team outcome：`docs/skill_runtime/team_task_outcome_contract.md`  
- Multi-agent 下一阶段（并行、不替代）：`docs/plan/multi-agent-next-stage-implementation-plan.md`  
- Grok 参考树：`E:\projects\ai\grok-build`（agent def / permissions / plan / sandbox / subagents 用户指南）

---

## 14. 变更记录

| 日期 | 说明 |
| --- | --- |
| 2026-07-25 | 初版：基于二次校正基线（skills/agents 已存在）制定 A/B/C 实施方案，写入 `docs/plan` |
| 2026-07-25 | B1 worktree isolation MVP 落地（无 completion auto-apply） |
| 2026-07-25 | B2 explore/plan agentdef + B3 planmode/`/plan`/PlanWriteAllowPaths/actor 接线与测试落地 |
| 2026-07-25 | B6 lifecycle hooks：Stop block / StopFailure / Pre|PostCompact 接线 + loop 单测 |
| 2026-07-25 | B1 residual：隔离会话默认 `write_paths` claim 隔离根（local + API spawn） |
| 2026-07-25 | Iteration B 核心收口（B1–B6）；剩余 optional apply 工具面与 ambient team PathClaim Acquire 后置 |
| 2026-07-25 | B1 residual 收口：`apply_agent_worktree` / `discard_agent_worktree` 工具面 + local/API controllers；completion annotate-only；close 仍 cleanup |
| 2026-07-25 | ambient PathClaim residual 收口：`PathClaimManager.Acquire` 冲突检查 + `ReleaseTaskPathClaims` + registry `WithClaims`（release/retry/terminal/renew/block）；interrupt/dependency_failure/API handlers 走 manager/registry |
| 2026-07-25 | C1 residual：`tool.completed.protocol_result` + session SSE `?live=1` bus progress fan-out |
| 2026-07-25 | C1 收口（protocol 层 + CLI + SSE live）；C2 plugin / C3 ACP / C4 harness 仍开放 |
| 2026-07-25 | C2 plugin MVP：`internal/plugins` + `aicli plugin` + skill/hooks/agentdef 接线（无 marketplace） |
| 2026-07-25 | C3 ACP 子集：`internal/acp` + `aicli agent stdio` host/bridge + permission RPC + 单测 |
| 2026-07-25 | C4a doom-loop productization：`DoomLoopTracker` + dual-emit warning/terminated 事件 + `doom_loop_total` metrics；硬停仍 opt-in |
| 2026-07-25 | C4b tool search / dynamic listing：`ListableTool`/`ShouldList` + BM25-lite `search_tool` + large-catalog projection（simple-goal 优先；eligibility 不投影）+ 单测 |
| 2026-07-25 | B3 residual 收口：agent tools `enter_plan_mode` / `exit_plan_mode`（broker + SessionActor + policy caps + 单测）；顺带修复 wait_agent `next_action` 过严断言 |
| 2026-07-25 | A6 residual：session info + `/debug` **Agent Source**（source · path）；B2 residual：项目 `.agents/agents/general.md`（def `none`，team worker 仍 runner 强制 complete_task） |
| 2026-07-25 | B3 frontend plan preview：session plan API client + `useRuntimePlanMode` + Artifact panel Plan 页签（approve/request_changes/quit） |
| 2026-07-25 | 文档对齐：分析文档回写实施后状态；本方案新增 §15 需求覆盖矩阵与残余 backlog；scrub B3「可后置」过时触点 |
| 2026-07-25 | R1 收口：项目 `.aicli/permissions.yaml` + CLI `--allow-tool`/`--deny-tool` 产品面；chat/exec/actor/`/call`/`/debug` 接线；`docs/product/project-permissions.md` |
| 2026-07-25 | R2 收口：`internal/foldertrust` + CLI early resolve + plugin/agentdef/MCP gates + `/trust`/`--trust`；`docs/product/folder-trust.md` |
| 2026-07-25 | R3 收口：统一 system-reminder / ephemeral instruction 通道（`agent/system_reminder.go`）；completion / stop-hook / doom-loop / plan 共 envelope + kind metadata + `system_reminder.injected` |
| 2026-07-25 | R4 收口：team teammate ↔ agentdef 更深字段对齐（permission/read_only 投影 + API/local tool allow/deny/sandbox） |
| 2026-07-25 | R5 收口：frontend Settings Harness 面板（permissions/grants/memory/plugins MVP）+ harness control-plane API client/hook；`docs/product/harness-settings.md` |
| 2026-07-26 | R6 收口：ACP `session/load`（`LoadSession=true` + host 内存 reattach / durable resume + 历史 `session/update` 回放）；`docs/aicli/agents.md` / install 同步 |
| 2026-07-25 | R7 收口：Tool terminal stream 完备度 — `toolprotocol.TerminalStreamWriter` + shell/aicli_exec OutputMirror tee + MCP start/result phase/stream + ACP `tool.progress` content + chat stream render；live-only 合同不变 |
| 2026-07-26 | A5 residual 收口：`applyStructuredTaskOutcome` 在 complete_task 未满足（session Success=false）时仍解析 structured JSON fallback；blocked/handoff/done 恢复 Success 并走 BlockTask/complete；docs team regression + full `cmd/aicli/commands` / R7 packages green |
| 2026-07-26 | R3 residual 收口：pure advisory（doom-loop / disposition / exploration / runtime_advisory）prompt-only + strip-on-persist；`DurableToolResultPayloads` / `DurableMessagesForPersist`；recovery kinds 仍 durable；`go test ./internal/agent` green |
| 2026-07-26 | Backtrack Phase 6 residual：frontend Restore 面板 **Backtrack audit**（tombstone 列表/详情接到 `useRuntimeCheckpoints` 已有 audit 数据；Restore tab 徽标；`artifact-panel-shared` helpers + 单测）；见 `docs/plan/session-user-turn-backtrack-plan.md` |
| 2026-07-26 | Backtrack Phase 6 residual 收口：transcript 内联导航（Esc/↑↓/Enter/双击）+ bubble **Edit** 内联编辑；`resolveSeededBacktrackEditPrompt` seed dialog；plan-mode reload event-key 去重；WIP 拆分指南 `docs/plan/wip-commit-split-guide.md` |

---

## 15. 需求覆盖矩阵与残余 backlog

> **文档权威**：分析文档 = 为什么学 / 对照 Grok；**本方案 = 做什么 / 验收与 backlog**。  
> 下列矩阵回答「校正后的需求是否被本方案覆盖、交付深度如何、还剩什么」。

### 15.1 覆盖矩阵（分析 P0/P1/P2 ↔ 方案条目）

| 需求主题（分析） | 方案条目 | 交付状态 | 深度 / 备注 |
| --- | --- | --- | --- |
| 可移植 AgentDefinition + discovery + host 绑定 | A1–A3、A6 residual | **已交付** | 无 MiniJinja；`chat --agent` / spawn defaults / Agent Source |
| 仓库 profile/agent 样例 | A1、B2、A6/B2 residual | **已交付** | coding profile + explore/plan/general.md |
| Permission 管道（hooks→rules→grants→readonly→mode）+ trace | A4 | **核心已交付** | 项目文件规则源 / CLI `--allow/--deny` 产品面仍可加深 |
| Shell 只读表 + dangerous | A4 | **已交付** | 与 readonly auto 联动 |
| Tool taxonomy（read_only / mutates / kind） | A 侧 toolkit 标注；C1 加深 | **已交付** | 未强制独立 `tooltaxonomy` 包 |
| completionRequirement ↔ team outcome | A5 | **已交付** | team worker 强制 complete_task；structured JSON fallback 在 complete_task 缺失时仍恢复 blocked/done；general.md = none |
| Worktree isolation + apply/discard + claims | B1 + residuals | **已交付** | 无 completion auto-apply；无 best-of-n |
| 内置 explore / plan / general 角色 | B2 | **已交付** | |
| Plan mode 文件流 + 审批（CLI/agent/API/UI） | B3 + residuals | **已交付** | frontend Plan 页签已闭环 |
| Project durable memory | B4 | **MVP 已交付** | keyword JSONL；**非** FTS/vector/云同步 |
| 应用层 sandbox profiles | B5 | **已交付** | off/workspace/read-only/strict + 显式降级 |
| Lifecycle hooks Stop/Compact/… | B6 | **已交付** | folder trust 见 R2 |
| Folder trust（项目 plugins/agentdef/MCP） | R2 | **已交付** | 默认 off（`AICLI_FOLDER_TRUST`）；CLI early resolve |
| Tool protocol + streaming / SSE live | C1 | **已交付（MVP）** | 非完整外部 tool server |
| Plugin 安装/信任/装载 | C2 | **已交付（MVP）** | **无 marketplace** |
| ACP stdio 子集 | C3 + R6 | **已交付（子集）** | session/load 已支持；无 Leader IPC / 无 MCPServers |
| Doom-loop harness | C4a | **已交付** | 硬停 opt-in；默认可警告 |
| Tool search / dynamic listing | C4b | **已交付** | ShouldList + search_tool |
| Optional OS sandbox | C4c | **已交付（可选）** | Linux bwrap；非 Linux stub；默认 off |
| 统一 system-reminder / ephemeral instruction | R3 | **已交付** | `<system-reminder kind>` envelope + kinds；completion/stop-hook/doom-loop/plan 共通道；pure advisory strip-on-persist（R3 residual） |
| frontend harness 面板（permissions/grants/memory/plugins） | R5 | **已交付（MVP）** | Settings Harness；permissions 只读；无 marketplace/FTS |
| Skills Runtime 加深 / 重做 loader | 非目标 | **不做** | 保持现有 runtime |
| 重写 team 编排 / TUI 克隆 | 非目标 | **不做** | |
| skill `agents/openai.yaml` 升角色系统 | 非目标 | **不做** | |

### 15.2 明确非目标 / 默认不重开

除非产品明确重开 scope，否则 **不要** 把下列当作「未完成主线」：

1. Marketplace / 插件商店  
2. Memory FTS / vector / 跨用户云同步 / session-end 自动摘要  
3. MiniJinja（或等价）agent prompt 条件模板  
4. Grok Leader IPC / 完整 ACP session load  
5. best-of-n multi-worktree 评选  
6. persona 级 IO contract 产品面  
7. 把 skill 内 `agents/openai.yaml` 升级成角色 def  
8. 重做 Skills loader 或替换 team task graph  

### 15.3 Deferred residual backlog（可选下一波）

按 ROI 粗排；**均非 A/B/C 核心门禁**：

| ID | 项 | 建议触发条件 |
| --- | --- | --- |
| R1 | 项目 permission 规则源文件 + CLI `--allow/--deny` 产品面 | **done（2026-07-25）** — `policy/permissions_file.go` + chat/exec overlay + `/debug`；见 product note |
| R2 | Folder trust（项目 hooks/MCP 信任模型） | **done（2026-07-25）** — `internal/foldertrust` + CLI early resolve + plugin/agentdef/MCP gates + `/trust`/`--trust`；见 `docs/product/folder-trust.md` |
| R3 | 统一 system-reminder / ephemeral instruction 通道 | **done（2026-07-25）** — `agent/system_reminder.go` + completion/stop-hook/doom-loop/plan producers；`system_reminder.injected`；**R3 residual（2026-07-26）** pure advisory prompt-only / strip-on-persist（tool-result 进 prompt，durable history 剥离；recovery 仍 durable） |
| R4 | team teammate ↔ agentdef 更深字段对齐 | **done（2026-07-25）** — profile→agentdef permission/read_only 投影；RunMeta 用 def 权限（无 profile 仍 bypass）；API/local tool allow/deny/sandbox 对齐 spawn_agent |
| R5 | frontend grants / memory / plugins 面板 | **done（2026-07-25）** — Settings Harness + `/api/runtime/harness/*` client + control-plane hook；permissions 只读 / grants remember-revoke / memory keyword list-search-append / local plugins trust-enable-disable；无 marketplace/FTS；见 `docs/product/harness-settings.md` |
| R6 | ACP session/load 与更多 methods | **done（2026-07-25）** — `loadSession=true` + host `LoadSession` 内存/durable 解析 + 历史回放；MCPServers/Leader IPC 仍不做 |
| R7 | Tool terminal stream 全工具完备度 | **done（2026-07-25）** — `TerminalStreamWriter` + shell/aicli_exec mirror tee → `tool.progress`；MCP phase start/finish + result stream；ACP mid-call content；chat `• Stream` render；非 shell toolkit 仍 out of scope；无远程 MCP progressive protocol |
| R8 | Memory FTS/embedding（若重开） | keyword MVP 不够用且接受复杂度 |
| R9 | Marketplace（若重开） | 本地 plugin MVP 不够分发 |
| R10 | best-of-n / persona IO（若重开） | 并行方案评选成为产品诉求 |

### 15.4 分析文档同步义务

更新本方案验收状态时，应同步 [`docs/analysis/grok-build-architecture-learning.md`](../analysis/grok-build-architecture-learning.md) 的：

- 文首「实施状态回写」  
- §3.2 能力债表  
- §6 迭代勾选  
- §8 能力对照  

避免分析文档再次被读成「A/B 未做」。
