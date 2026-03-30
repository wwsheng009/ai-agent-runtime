# Profile 设计实施状态审计

> 日期：2026-03-16  
> 范围：`docs/multi-agents/profile/` 下 3 份设计文档与当前仓库实现的对应情况  
> 迁移说明（2026-03-30）：
>
> - 本文提到的 runtime HTTP 接线、`internal/api/skills/*`、以及旧 `internal/runtime/*` 相关实现，现应以 `E:\projects\ai\ai-agent-runtime\backend` 为准
> - 其中旧前缀 `internal/runtime/<subpkg>` 在独立 runtime 仓里通常已扁平化为 `internal/<subpkg>`
> - 本文中的 Markdown 链接已改为优先指向 `backend/` 下的当前实现；个别文字标签仍保留历史命名以说明设计演进
> 结论口径：
>
> - 已实现：设计点已有明确实现和调用链证据
> - 基本实现：主路径已通，但细节未完全覆盖
> - 部分实现：已有骨架或局部接线，离设计完成态还有差距
> - 未实现：当前仓库中没有对应实现

## 1. 总体结论

当前 `profile` 相关设计并非停留在文档阶段。

- `profile_system_implementation.md`：基本实现
- `profile_workspace_agent_design.md`：基本实现
- `aicli_profile_loading_flow.md`：已实现，且 `aicli chat` 已承载本地 actor-first orchestration

更具体地说：

- `internal/profile` 系统级解析层已经落地
- `aicli chat --profile --agent` 已经接到系统级 resolver
- `/api/agent/chat` 及 session actor 已接入同一套 profile 解析能力
- CLI / API 重复的 profile prompt 与 tool policy glue code 已收口到共享运行时包
- CLI / API 重复的 sandbox config merge helper 已收口到共享 executor 包
- prompt / tool policy 的共享运行时包已经落地
- profile 顶层 raw schema 已补到 `runtime` / `mcp` / `skills`
- `aicli chat` 已切到本地 actor-first runtime host，并支持本地 team orchestration
- `memory/context` 等目录的运行时消费、可选独立 `aicli agent` 薄入口仍未完成

## 2. 审计摘要

| 文档 | 总体状态 | 简述 |
| --- | --- | --- |
| `profile_system_implementation.md` | 基本实现 | `internal/profile`、`runtime/prompt`、`runtime/policy`、CLI/API 接线已存在 |
| `profile_workspace_agent_design.md` | 基本实现 | 目录约定、`--profile/--agent`、session/runtime/MCP/skills/tool policy 接线已存在 |
| `aicli_profile_loading_flow.md` | 已实现 | `aicli chat` 已支持 profile + 本地 actor-first orchestration；`aicli agent` 仅剩可选 future surface |

## 3. `profile_system_implementation.md`

总体判断：基本实现。

### 3.1 `internal/profile` 系统级模块

状态：已实现

代码证据：

- [`internal/profile/spec.go`](../../../backend/internal/profile/spec.go)
- [`internal/profile/paths.go`](../../../backend/internal/profile/paths.go)
- [`internal/profile/loader.go`](../../../backend/internal/profile/loader.go)
- [`internal/profile/merge.go`](../../../backend/internal/profile/merge.go)
- [`internal/profile/resolved.go`](../../../backend/internal/profile/resolved.go)
- [`internal/profile/resolver.go`](../../../backend/internal/profile/resolver.go)
- [`internal/profile/validate.go`](../../../backend/internal/profile/validate.go)
- [`internal/profile/registry.go`](../../../backend/internal/profile/registry.go)
- [`internal/profile/registry_config.go`](../../../backend/internal/profile/registry_config.go)

说明：

- 目录约定集中在 `paths.go`
- profile/agent/workspace/tool policy 解析集中在 `loader.go` + `resolver.go`
- 合并逻辑集中在 `merge.go`
- 稳定输出模型集中在 `resolved.go`

### 3.2 `ResolvedAgent` 作为跨入口 contract

状态：已实现

代码证据：

- [`internal/profile/resolved.go`](../../../backend/internal/profile/resolved.go)

已包含的核心字段：

- `ProfileName`
- `ProfileRoot`
- `AgentID`
- `Provider`
- `Model`
- `RuntimeConfig`
- `MCPConfig`
- `SkillDirs`
- `Prompts`
- `ToolPolicy`
- `Paths`

### 3.3 路径与优先级规则

状态：已实现

代码证据：

- [`internal/profile/resolver.go`](../../../backend/internal/profile/resolver.go)
- [`internal/profile/validate.go`](../../../backend/internal/profile/validate.go)

已落实规则：

- agent 解析：显式 `agent` -> `profile.default_agent` -> 单目录推断 -> 报错
- runtime config：`profile/runtime.yaml` -> 全局 runtime
- MCP config：workspace MCP -> profile MCP -> 全局 MCP
- skill dirs：workspace -> agent -> profile -> global
- prompts：`prompts/system.md`、`role.md`、`tools.md`
- sessions：`agents/<agent>/sessions`

### 3.4 merge 规则

状态：基本实现

代码证据：

- [`internal/profile/resolver.go`](../../../backend/internal/profile/resolver.go)
- [`internal/profile/merge.go`](../../../backend/internal/profile/merge.go)

已实现部分：

- provider/model 使用高优先级覆盖低优先级
- tool policy 的 allowlist / denylist / read_only / sandbox 合并

说明：

- `sandbox` 的列表字段会去重合并，标量字段由高层覆盖低层
- `tool policy` 来源也会记录到 `Sources`

### 3.5 共享 prompt 包

状态：已实现

代码证据：

- `E:\projects\ai\ai-agent-runtime\backend\internal\prompt\files.go`
- `E:\projects\ai\ai-agent-runtime\backend\internal\prompt\compose.go`

说明：

- resolver 只返回 prompt 文件路径
- prompt 内容加载与拼装由共享包负责

### 3.6 共享 policy 包

状态：已实现

代码证据：

- `E:\projects\ai\ai-agent-runtime\backend\internal\policy\tool_policy.go`
- `E:\projects\ai\ai-agent-runtime\backend\internal\policy\filter.go`
- `E:\projects\ai\ai-agent-runtime\backend\internal\policy\engine.go`

说明：

- 已支持 tool allow/deny、read-only、sandbox、definition 过滤、tool call 校验

### 3.7 共享 profile runtime 输入构建器

状态：已实现

代码证据：

- `E:\projects\ai\ai-agent-runtime\backend\internal\profileinput\inputs.go`
- `E:\projects\ai\ai-agent-runtime\backend\internal\profileinput\inputs_test.go`

说明：

- CLI 与 API 现在统一通过共享运行时包将 `ResolvedAgent` 转换成：
  - `PromptText`
  - `ToolPolicy`
- 原先分散在 CLI / API 两侧的 prompt load + compose、tool policy decode、sandbox YAML decode 已收口

### 3.7 CLI / API 共同消费 resolved topology

状态：已实现

CLI 证据：

- [`cmd/aicli/commands/chat_profile.go`](../../../backend/cmd/aicli/commands/chat_profile.go)

API 证据：

- `E:\projects\ai\ai-agent-runtime\backend\internal\api\skills\profile_support.go`
- `E:\projects\ai\ai-agent-runtime\backend\internal\api\skills\handler.go`
- `E:\projects\ai\ai-agent-runtime\backend\internal\api\skills\session_runtime_support.go`

独立 runtime 接线证据：

- `E:\projects\ai\ai-agent-runtime\backend\cmd\runtime-server\main.go`

说明：

- CLI 与 API 都通过 `profilesys.ResolveRef(...)` 获得 `ResolvedAgent`
- 独立 `runtime-server` 启动时会把 profile support 注入 skills handler

### 3.8 状态/治理接口暴露 profile metadata

状态：已实现

代码证据：

- `E:\projects\ai\ai-agent-runtime\backend\internal\api\skills\handler.go`

测试证据：

- `E:\projects\ai\ai-agent-runtime\backend\internal\api\skills\handler_test.go`

说明：

- runtime status / health / traces / trace / validate 等接口已附带 `profile.reference` 和 `profile.resolved`

### 3.9 文档与实现的差异

状态：部分实现

差异点：

- 文档中的 raw schema 示例比当前 `ProfileSpec` 仍然更宽
- 当前 [`internal/profile/spec.go`](../../../backend/internal/profile/spec.go) 已建模：
  - `profile`
  - `runtime`
  - `providers`
  - `mcp`
  - `skills`
  - `tools`
  - `agents`
- 但这些新增 schema 目前主要还是声明性建模，尚未全部进入 resolver merge 和执行逻辑

## 4. `profile_workspace_agent_design.md`

总体判断：基本实现。

### 4.1 目录层级约定

状态：已实现

代码证据：

- [`internal/profile/paths.go`](../../../backend/internal/profile/paths.go)

已覆盖目录：

- `profile.yaml`
- `runtime.yaml`
- `mcp.yaml`
- `skills/`
- `agents/<agent>/agent.yaml`
- `agents/<agent>/skills/`
- `agents/<agent>/workspace/workspace.yaml`
- `agents/<agent>/workspace/skills/`
- `agents/<agent>/workspace/mcp.yaml`
- `agents/<agent>/prompts/`
- `agents/<agent>/tools/policy.yaml`
- `agents/<agent>/sessions/`
- `agents/<agent>/memory/`
- `agents/<agent>/context/`

### 4.2 真实 profile 样例

状态：未实现（仓库内未提交示例目录）

代码证据：

- 当前仓库未提交 `profiles/*/profile.yaml` 示例目录

说明：

- profile 目录约定和 resolver 已实现，但示例 profile 仍需要调用方自行创建
- 因此这里不再把“仓库内已有真实样例”作为当前代码证据

### 4.3 `aicli chat --profile --agent`

状态：已实现

代码证据：

- [`cmd/aicli/main.go`](../../../backend/cmd/aicli/main.go)
- [`cmd/aicli/commands/chat_options.go`](../../../backend/cmd/aicli/commands/chat_options.go)

说明：

- CLI 参数已暴露 `--profile`
- CLI 参数已暴露 `--agent`

### 4.4 `aicli chat` 启动流程先 resolve profile

状态：已实现

代码证据：

- [`cmd/aicli/commands/chat.go`](../../../backend/cmd/aicli/commands/chat.go)
- [`cmd/aicli/commands/chat_profile.go`](../../../backend/cmd/aicli/commands/chat_profile.go)

说明：

- chat 入口会先 resolve profile state
- 再把 provider/model/session dir 等默认值应用到 chat options
- 然后再 bootstrap session

### 4.5 runtime config / MCP / skills dir 接线

状态：已实现

runtime config 证据：

- [`cmd/aicli/commands/skills_integration.go`](../../../backend/cmd/aicli/commands/skills_integration.go)

MCP 证据：

- [`cmd/aicli/commands/mcp_integration.go`](../../../backend/cmd/aicli/commands/mcp_integration.go)

skills dir 证据：

- [`cmd/aicli/commands/chat_profile.go`](../../../backend/cmd/aicli/commands/chat_profile.go)
- [`cmd/aicli/commands/skills_integration.go`](../../../backend/cmd/aicli/commands/skills_integration.go)

说明：

- AICLI 已实际使用解析出的 runtime config path
- AICLI 已实际使用解析出的 MCP config path
- AICLI 已实际使用解析出的 skill dirs

### 4.6 prompt 注入

状态：已实现

代码证据：

- [`cmd/aicli/commands/chat_profile.go`](../../../backend/cmd/aicli/commands/chat_profile.go)
- [`cmd/aicli/commands/chat_setup.go`](../../../backend/cmd/aicli/commands/chat_setup.go)
- [`cmd/aicli/commands/chat_session.go`](../../../backend/cmd/aicli/commands/chat_session.go)

说明：

- profile prompt 文件会被加载并 compose
- compose 后的 system prompt 会被写入 chat session
- 运行中会保证 system message 处于消息头部

### 4.7 tool policy 生效

状态：已实现

CLI 证据：

- [`cmd/aicli/commands/chat_profile.go`](../../../backend/cmd/aicli/commands/chat_profile.go)
- [`cmd/aicli/commands/chat_setup.go`](../../../backend/cmd/aicli/commands/chat_setup.go)
- [`cmd/aicli/commands/function_catalog.go`](../../../backend/cmd/aicli/commands/function_catalog.go)

API 证据：

- `E:\projects\ai\ai-agent-runtime\backend\internal\api\skills\profile_support.go`
- `E:\projects\ai\ai-agent-runtime\backend\internal\api\skills\handler.go`

说明：

- CLI 会把 profile tool policy 注入 function catalog
- API 会把 profile tool policy 合并到 agent execution policy

### 4.8 session dir 按 profile 切换

状态：已实现

代码证据：

- [`cmd/aicli/commands/chat_profile.go`](../../../backend/cmd/aicli/commands/chat_profile.go)
- [`cmd/aicli/commands/chat_session.go`](../../../backend/cmd/aicli/commands/chat_session.go)

说明：

- profile 模式会默认使用 `profiles/<profile>/agents/<agent>/sessions`
- 无 profile 模式仍回退到 `~/.aicli/sessions`

### 4.9 `memory/` 和 `context/` 的运行时消费

状态：部分实现

代码证据：

- [`internal/profile/resolver.go`](../../../backend/internal/profile/resolver.go)

说明：

- resolver 已会把 `MemoryDir` 和 `ContextDir` 放入 `ResolvedPaths`
- 但当前没有找到 CLI 或 API 直接消费这两个路径的明确调用链

结论：

- 目前更像“路径已保留，后续功能待接”

## 5. `aicli_profile_loading_flow.md`

总体判断：已实现，且 `aicli chat` 已承担本地 actor-first orchestration 主路径。

### 5.1 `aicli chat` actor-first 主流程

状态：已实现

代码证据：

- [`cmd/aicli/commands/chat.go`](../../../backend/cmd/aicli/commands/chat.go)
- [`cmd/aicli/commands/chat_profile.go`](../../../backend/cmd/aicli/commands/chat_profile.go)
- [`cmd/aicli/commands/chat_setup.go`](../../../backend/cmd/aicli/commands/chat_setup.go)
- [`cmd/aicli/commands/chat_actor_host.go`](../../../backend/cmd/aicli/commands/chat_actor_host.go)
- [`cmd/aicli/commands/chat_actor_executor.go`](../../../backend/cmd/aicli/commands/chat_actor_executor.go)
- [`cmd/aicli/commands/chat_runtime_events.go`](../../../backend/cmd/aicli/commands/chat_runtime_events.go)
- [`cmd/aicli/commands/chat_team_binding.go`](../../../backend/cmd/aicli/commands/chat_team_binding.go)

说明：

- `aicli chat` 已能在 profile 模式下装配本地 `SessionActor` host
- profile prompt / runtime config / MCP / skill dirs / tool policy / session dir 都会进入运行时
- route-first skills、ReAct/tool loop、team tools、runtime event bridge 都走同一个本地 host

### 5.2 `/api/agent/chat` profile 流程

状态：已实现

代码证据：

- `E:\projects\ai\ai-agent-runtime\backend\internal\api\skills\handler.go`
- `E:\projects\ai\ai-agent-runtime\backend\internal\api\skills\profile_support.go`

说明：

- 请求体接受 `profile` / `agent`
- API 侧会 resolve profile runtime state
- prompt、runtime config、skill registry、embedding router、MCP adapter、tool policy 都会被应用

### 5.3 session actor 复用 profile 上下文

状态：已实现

代码证据：

- `E:\projects\ai\ai-agent-runtime\backend\internal\api\skills\session_runtime_support.go`

说明：

- session actor 会从 session context 中恢复 `profile_reference` / `profile_agent`
- 然后再次 resolve profile

### 5.4 auto profile fallback

状态：已实现

代码证据：

- `E:\projects\ai\ai-agent-runtime\backend\internal\api\skills\profile_support.go`

说明：

- 支持从 `<workspace>/.gagent/profiles/<name>` 回退查找
- 支持从 `~/.gagent/profiles/<name>` 回退查找

### 5.5 独立 `aicli agent`

状态：未实现

代码证据：

- 当前仓库中没有 `aicli agent` 命令实现
- 搜索结果只出现在设计文档和 roadmap 中：
  - [`docs/multi-agents/profile/aicli_profile_loading_flow.md`](aicli_profile_loading_flow.md)
  - [`docs/multi-agents/profile/profile_system_implementation.md`](profile_system_implementation.md)
  - [`docs/multi-agents/agents/plan/refactor_roadmap.md`](../agents/plan/refactor_roadmap.md)

结论：

- 独立 `aicli agent` 仍然只是 future design
- 但它已经不是多 agent CLI 的必要前置条件，因为本地 orchestration 已在 `aicli chat` 落地

### 5.6 多 agent orchestration in aicli

状态：已实现

代码证据：

- [`cmd/aicli/commands/chat_actor_registry.go`](../../../backend/cmd/aicli/commands/chat_actor_registry.go)
- [`cmd/aicli/commands/chat_team_binding.go`](../../../backend/cmd/aicli/commands/chat_team_binding.go)
- [`cmd/aicli/commands/chat_runtime_events.go`](../../../backend/cmd/aicli/commands/chat_runtime_events.go)
- `E:\projects\ai\ai-agent-runtime\backend\internal\tools\agent_adapter.go`
- `E:\projects\ai\ai-agent-runtime\backend\internal\toolbroker\broker.go`

说明：

- 当前 `aicli chat` 会默认走本地 actor-first runtime host，而不是旧的单 agent shared loop
- `spawn_team`、team mailbox、ambient team binding、restart restore 都已接到 CLI chat 主路径
- 多 agent 相关能力仍然可以经由 Skills API 使用，但不再局限于 API

## 6. 测试覆盖

### 6.1 `internal/profile`

状态：已覆盖核心规则

测试文件：

- [`internal/profile/resolver_test.go`](../../../backend/internal/profile/resolver_test.go)
- [`internal/profile/merge_test.go`](../../../backend/internal/profile/merge_test.go)
- [`internal/profile/registry_test.go`](../../../backend/internal/profile/registry_test.go)

已覆盖内容：

- 缺失 `profile.yaml`
- default agent fallback
- 显式 agent override
- 缺失 agent 报错
- runtime / MCP / skills 优先级
- prompt file 检测
- tool policy merge

### 6.2 `prompt`

状态：已覆盖

测试文件：

- `E:\projects\ai\ai-agent-runtime\backend\internal\prompt\files_test.go`

### 6.3 CLI profile 接线

状态：已覆盖关键路径

测试文件：

- [`cmd/aicli/commands/chat_profile_test.go`](../../../backend/cmd/aicli/commands/chat_profile_test.go)
- [`cmd/aicli/commands/function_catalog_test.go`](../../../backend/cmd/aicli/commands/function_catalog_test.go)
- [`cmd/aicli/commands/chat_setup_test.go`](../../../backend/cmd/aicli/commands/chat_setup_test.go)
- [`cmd/aicli/commands/chat_team_binding_test.go`](../../../backend/cmd/aicli/commands/chat_team_binding_test.go)
- [`cmd/aicli/commands/chat_runtime_events_test.go`](../../../backend/cmd/aicli/commands/chat_runtime_events_test.go)
- [`cmd/aicli/commands/chat_local_orchestration_integration_test.go`](../../../backend/cmd/aicli/commands/chat_local_orchestration_integration_test.go)

已覆盖内容：

- profile state 解析
- prompt compose
- tool policy 默认值
- session dir 默认值
- tool exposure / execution 被 policy 限制
- actor-first bootstrap
- ambient team binding / restart restore
- approval/question bridge
- 本地 team orchestration follow-up

### 6.4 API profile 接线

状态：已覆盖关键路径

测试文件：

- `E:\projects\ai\ai-agent-runtime\backend\internal\api\skills\handler_test.go`

已覆盖内容：

- profile system prompt 注入
- session context 写入 profile metadata
- session actor 复用 profile context
- workspace auto profile fallback
- runtime status / health / trace 接口附带 profile metadata

## 7. 定向验证记录

本次审计实际执行并通过了以下测试：

```powershell
Set-Location 'E:\projects\ai\ai-agent-runtime\backend'
go test ./internal/profile
go test ./cmd/aicli/commands -run "TestResolveChatProfileState_AppliesDefaultsPromptAndPolicy|TestEnsureChatSystemPromptMessage_PrependsAndReplaces"
go test ./internal/prompt
go test ./internal/api/skills -run "TestAgentChat_ProfileInjectsSystemPrompt|TestAgentChat_ProfileSetsSessionContext|TestSessionActor_UsesProfileContextFromSession|TestAgentChat_ProfileAutoRoutesToWorkspaceProfile|TestAgentChat_ProfileAutoPrefersWorkspaceProfileOverRegistry|TestGetRuntimeStatus_IncludesProfileMetadata|TestGetRuntimeHealth_IncludesProfileMetadata|TestGetRuntimeTraces_IncludesProfileMetadata|TestGetRuntimeTrace_IncludesProfileMetadata|TestValidateRuntimeConfig_IncludesProfileMetadata"
```

## 8. 剩余缺口

以下项目建议继续跟踪：

1. 扩展 `ProfileSpec`
   - 将文档中的 inline `runtime` / `mcp` / `skills` 等 schema 正式建模

2. 接通 `memory/` 与 `context/`
   - 当前 resolver 已暴露路径
   - 但 CLI / API 未见明确消费逻辑

3. 决定是否仍需要独立 `aicli agent`
   - 当前多 agent CLI 流程已经在 `aicli chat` 落地
   - 如果未来新增独立命令，应只做同一 runtime 的薄封装

4. 统一更多 bootstrap 入口
   - 当前 CLI / API 都已复用 resolver
   - 但仍存在 API glue code，可继续向统一 bootstrap 抽象收敛

## 9. 最终结论

截至 2026-03-16，`docs/multi-agents/profile/` 这组设计的实施状态应描述为：

- `profile 系统核心能力`：已实施
- `AICLI actor-first profile / 本地 orchestration 流程`：已实施
- `API profile 接入`：已实施
- `profile schema 全量化`：部分实施
- `profile prompt / tool policy 共享运行时 builder`：已实施
- `memory/context 等目录的运行时消费`：部分实施
- `多 agent CLI（通过 aicli chat）`：已实施
- `独立 aicli agent`：未实施，但已降为可选 future surface

不建议再将这组设计标记为“未实现”或“仅设计中”。
