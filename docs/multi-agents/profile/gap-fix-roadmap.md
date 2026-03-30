# Profile 后续实施路线图

> 日期：2026-03-15  
> 前置文档：[`implementation-status.md`](implementation-status.md)  
> 目标：将当前“已落地但未收尾”的 `profile` 体系推进到可稳定维护、可继续扩展的完成态
>
> 迁移说明（2026-03-30）：
>
> - 本文中的 runtime-owned 引用，如 `internal/api/skills/*`、旧 `internal/runtime/*`，当前应以 `E:\projects\ai\ai-agent-runtime\backend` 为准
> - 其中旧前缀 `internal/runtime/<subpkg>` 在独立 runtime 仓里通常已扁平化为 `internal/<subpkg>`
> - 本文中的 Markdown 链接已改为优先指向 `backend/` 下的当前实现；个别文字标签仍保留历史命名以说明最初设计分层

## 1. 总体策略

建议按下面顺序推进，而不是并行散改：

1. 先完成 schema 与共享构建逻辑收口
2. 再接通 `memory/context` 等资源消费
3. 然后补 CLI / API 集成测试
4. 最后再评估是否需要拆分独立 `aicli agent` 入口

原因：

- 当前 CLI 和 API 都已经依赖 `ResolvedAgent`
- `aicli chat` 已经承担本地 actor-first orchestration 入口，先稳定共享 runtime 更重要
- 先稳定 resolver 和 runtime build 流程，可以降低后续改动面

## 2. 优先级划分

| 优先级 | 主题 | 目标 |
| --- | --- | --- |
| P0 | schema 与共享构建收口 | 降低未来 profile 扩展成本 |
| P1 | `memory/context` 资源接线 | 让路径约定变成真实运行时能力 |
| P1 | 集成测试补齐 | 防止 profile 相关能力在后续重构中回退 |
| P2 | CLI 入口整理 | 稳定 `aicli chat` actor-first 路径，并决定是否补充独立 `aicli agent` 薄入口 |

## 3. P0: 补齐 schema 与共享构建收口

### 3.1 扩展 `ProfileSpec`

状态：首轮完成

目标：

- 让 `ProfileSpec` 明确支持文档里已经出现的核心逻辑层字段
- 即使暂时不直接执行，也要先把 schema 形式化，避免配置语义漂移

本轮已完成：

- 扩展了 [`internal/profile/spec.go`](../../../backend/internal/profile/spec.go)
- 新增了以下 raw spec 结构：
  - `RuntimeSpec`
  - `MCPSpec`
  - `SkillsSpec`
- 新增了 [`internal/profile/spec_test.go`](../../../backend/internal/profile/spec_test.go) 覆盖 YAML 解析

建议范围：

- 先建模，不立刻引入复杂 merge 语义
- 明确哪些字段是“声明性元数据”，哪些字段真正参与 resolver

剩余工作：

- 决定这些 raw spec 中哪些字段进入 resolver merge
- 决定哪些字段只是声明性元数据

### 3.2 抽出共享的 profile runtime builder

状态：首轮完成

现状问题：

- CLI 有一套 `resolveChatProfileState(...)`
- API 有一套 `resolveProfileRuntimeState(...)`
- 两边都在做：
  - prompt file load + compose
  - tool policy decode
  - sandbox config 解析

相关文件：

- [`cmd/aicli/commands/chat_profile.go`](../../../backend/cmd/aicli/commands/chat_profile.go)
- [`internal/api/skills/profile_support.go`](../../../backend/internal/api/skills/profile_support.go)

本轮已完成：

- 新增共享运行时包 [`backend/internal/profileinput/inputs.go`](../../../backend/internal/profileinput/inputs.go)
- CLI 与 API 已统一使用该包从 `ResolvedAgent` 构造：
  - `PromptText`
  - `ToolPolicy`
- 新增测试 [`backend/internal/profileinput/inputs_test.go`](../../../backend/internal/profileinput/inputs_test.go)

注意：

- 不要把 skills runtime bootstrap 本身塞回 `internal/profile`
- 这里只做“解析后的运行时输入准备”，不做真正的 manager 启动

剩余工作：

- 如有必要，再把 runtime config / MCP config / skill dirs 的轻量输入准备也收口到共享 builder

### 3.3 明确 profile tool policy 与 runtime sandbox 的叠加边界

状态：首轮完成

现状：

- CLI 在 [`cmd/aicli/commands/chat.go`](../../../backend/cmd/aicli/commands/chat.go) 中叠加 sandbox
- API 在 [`internal/api/skills/handler.go`](../../../backend/internal/api/skills/handler.go) 中叠加 sandbox

本轮已完成：

- 统一了 sandbox config 的共享 helper 到：
  - [`backend/internal/executor/sandbox_config.go`](../../../backend/internal/executor/sandbox_config.go)
- 新增测试：
  - [`backend/internal/executor/sandbox_config_test.go`](../../../backend/internal/executor/sandbox_config_test.go)
- CLI 与 API 已改为共享：
  - sandbox clone
  - sandbox overlay
  - sandbox active 判定

仍建议继续确认：

- profile read-only 是否强制提升 sandbox 为 enabled
- workspace path 是否默认加入 allowed/read_only paths
- denied commands 的默认值是否在 CLI / API 一致

验收标准：

- CLI 与 API 的 sandbox merge 结果一致
- 相同 profile 下，工具暴露和执行行为在两侧一致

## 4. P1: 接通 `memory/context` 资源消费

### 4.1 将 `MemoryDir` 接到 session / context runtime

状态：当前部分实现

现状：

- resolver 已返回 `MemoryDir`
- 但未见 CLI / API 对该路径的明确消费

相关文件：

- [`internal/profile/resolver.go`](../../../backend/internal/profile/resolver.go)
- [`internal/profile/resolved.go`](../../../backend/internal/profile/resolved.go)
- [`cmd/aicli/commands/chat_setup.go`](../../../backend/cmd/aicli/commands/chat_setup.go)
- [`internal/api/skills/handler.go`](../../../backend/internal/api/skills/handler.go)

建议改动：

- 为 chat/session runtime 增加 profile memory root 注入
- 让 session actor / agent context builder 能读取该目录下的标准资源

第一步建议只做：

- `memory.json`
- 简单的只读上下文装载

不要第一步就做：

- ledger/artifact 的复杂写入协议

验收标准：

- 在 profile `memory/` 下写入一个标准文件后，CLI 和 API 都能在 agent context 中看到对应内容

### 4.2 将 `ContextDir` 接到 workspace/context pack

状态：当前部分实现

建议改动：

- 在构建 context pack 的位置引入 profile `context/`
- 形成明确优先级：
  - agent `context/`
  - workspace context
  - 运行时推导 context

候选接入点：

- [`internal/api/skills/agent_context.go`](../../../backend/internal/api/skills/agent_context.go)
- [`internal/api/skills/handler.go`](../../../backend/internal/api/skills/handler.go)
- AICLI chat 的 system/context 初始化链

验收标准：

- `agents/<agent>/context/notes.md` 等资源能稳定进入运行时上下文
- 不依赖用户手动拼 system prompt

## 5. P1: 补齐集成测试

### 5.1 CLI profile 集成测试

状态：当前已有关键路径测试，但缺少更完整的端到端覆盖

现有测试：

- [`cmd/aicli/commands/chat_profile_test.go`](../../../backend/cmd/aicli/commands/chat_profile_test.go)
- [`cmd/aicli/commands/function_catalog_test.go`](../../../backend/cmd/aicli/commands/function_catalog_test.go)

仍建议新增：

- `aicli chat --profile` 完整 bootstrap 测试
- profile + session restore 测试
- profile + runtime config override + MCP override 联合测试

建议文件：

- `cmd/aicli/commands/chat_integration_profile_test.go`

验收标准：

- 单测能覆盖从 `--profile` 输入到 session/runtime 初始化完成的整条链路

### 5.2 API profile 集成测试补面

状态：已有关键测试，但仍可补全

现有测试：

- [`internal/api/skills/handler_test.go`](../../../backend/internal/api/skills/handler_test.go)

仍建议新增：

- profile + MCP auto connect 测试
- profile + tool policy denylist / sandbox 限制测试
- profile + runtime config scope 选择测试

验收标准：

- `/api/agent/chat` 的 profile 行为有独立、稳定、可回归的测试矩阵

### 5.3 backward compatibility 测试

状态：建议补齐

目标：

- 明确保障“无 profile”时行为不变

建议覆盖：

- 无 `--profile` 时 session dir 仍为 `~/.aicli/sessions`
- API 未传 `profile` 时仍使用默认 runtime
- 全局 skills / MCP 配置在 no-profile 模式下不受影响

## 6. P2: 评估 CLI 入口整理

### 6.1 `aicli chat` actor-first 路径收口

状态：已实现，需要持续收尾

目标：

- 以 `aicli chat` 作为默认本地 orchestration host
- 让 profile / agent / workspace 解析和本地 actor/team 运行时保持一致
- 把独立 `aicli agent` 降级为可选薄入口，而不是多 agent CLI 的唯一前提

建议改动：

- 继续复用 [`cmd/aicli/commands/chat.go`](../../../backend/cmd/aicli/commands/chat.go) 及其 actor-first host
- 确保 `--profile` / `--agent` / `--permission-mode` / `--yolo` 在本地 orchestration 路径上有稳定语义
- 保持 route-first skills、team tools、session restore 使用同一套 profile 解析结果

第一阶段先做：

- 完成本地 actor/team runtime 的文档与验收
- 补齐 permission-mode、ambient team continuity、restart restore 的回归测试

第二阶段再做：

- 如果后续仍需要独立 `aicli agent`，只作为复用同一 runtime 的薄 CLI 外壳
- 视 UX 需求再决定是否提供更聚焦的 timeline / preset flags

验收标准：

- `aicli chat --profile <name> --agent <name>` 能直接驱动本地 actor-first orchestration
- `spawn_team`、后续 team tool、会话恢复都不需要离开 `aicli chat`
- 独立 `aicli agent` 即使暂未实现，也不再构成多 agent CLI 缺口

### 6.2 可选独立 CLI 外壳

状态：可选 future work

建议改动：

- 如果引入 `aicli agent`，优先复用 `aicli chat` 的 runtime host、timeline renderer 和 session metadata
- 不再重复实现另一套 profile/runtime bootstrap

重点事件：

- planning started / completed
- subagent started / completed
- patch decision
- final result

## 7. 下一步推荐顺序

建议按以下顺序提交：

1. 接通 `memory/context` 的最小只读消费
2. 补齐 CLI/API 集成测试
3. 收尾 `aicli chat` actor-first 文档与验收
4. 仅在确有 UX 需求时再评估独立 `aicli agent`

## 8. 不建议现在做的事

以下内容建议暂缓：

1. 在 `internal/profile` 中直接启动 skills runtime / MCP manager
   - 会破坏 resolver 的职责边界

2. 先做复杂的 profile schema merge
   - 应先明确哪些字段是真正执行字段，哪些只是配置元数据

3. 在未补齐共享 builder 前同时扩展 CLI 和 API
   - 会继续复制 glue code

## 9. 最小可交付版本

如果只做一轮收尾，建议目标定在下面这组最小闭环：

1. 接通 `memory.json` 和 `context/notes.md`
2. 新增 CLI/API profile 集成测试

完成这 2 项后，`profile` 体系就可以视为“稳定完成，等待多 agent CLI 扩展”，而不是“核心可用但仍有明显缺口”。
