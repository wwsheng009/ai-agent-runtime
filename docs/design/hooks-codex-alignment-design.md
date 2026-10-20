# Codex Hooks 机制对齐与本系统实现方案

- 状态：已实现（P0/P1 切片）
- 日期：2026-10-19
- 范围：`backend/internal/hooks`、`backend/internal/agent`、`backend/internal/chat`、`backend/internal/api/runtimeapi`、`backend/cmd/aicli/commands`
- 目标：在本系统中提供与 Codex/Claude 风格兼容的 hooks 能力，同时不破坏既有 `runtime.yaml hooks` 与插件 hooks。

---

## 1. 结论摘要

本系统原本已有 hooks 内核（`internal/hooks`）和若干生命周期接线，但它是一个**内部动作协议**：

- 事件名是 snake_case；
- handler 只有 `shell`/`http`；
- 输出只识别 `{"action":"block|modify|enrich|notify", ...}`；
- `session_start`、`user_prompt_submit` 等事件通过 `DispatchAsync` 触发，结果被丢弃；
- 没有声明式 `hooks.json` 发现、作用域、信任、异步观测与 `additionalContext` 注入。

Codex 的 hooks 则是**外部进程协议**：

- 12 类事件，PascalCase 名称；
- matcher 支持 `*`、精确/别名（`A|B`）与正则；
- handler 支持 `command` 与 `mcp_tool`，同步 handler 可控制流程，异步 handler 只观测；
- 输入是事件专属 JSON（stdin），输出是 JSON（stdout）+ 退出码 2（stderr 为阻塞原因）；
- `additionalContext` 注入模型上下文；`continue:false` 是终止而非“继续工作”；`systemMessage` 是用户可见提示；
- 支持多作用域（managed/user/project/plugin/session）、状态（enabled/trusted_hash）与信任审查。

本方案保留内部内核，增加**兼容解析层 + 同步控制路径 + 发现/合并层 + 诊断面**，把 Codex 语义映射到既有 `Decision` 通道。这样：

- 旧 `runtime.yaml hooks` 与旧插件 hooks 行为不变；
- 新 `hooks.json` 可直接使用 Codex 的写法；
- 阻断/改写/上下文注入/终止四类控制语义清晰分层；
- 所有控制效果仍经过既有权限、sandbox 与 hard-deny 边界。

---

## 2. Codex hooks 机制拆解

### 2.1 事件模型

Codex 暴露 12 个事件（`codex-rs/hooks/src/lib.rs`）：

| 事件 | 时机 | matcher 输入 | 控制能力 |
|---|---|---|---|
| `PreToolUse` | 工具执行前 | 工具名 | block / allow+updatedInput / additionalContext |
| `PermissionRequest` | 权限审批前 | 工具名 | allow / deny |
| `PostToolUse` | 工具执行后 | 工具名 | block（反馈）/ additionalContext |
| `PreCompact` | 压缩前 | 触发源 | Codex 仅观测；本系统扩展支持 block 跳过压缩 |
| `PostCompact` | 压缩后 | 触发源 | 无控制（观测） |
| `SessionStart` | 会话启动/恢复/清空/分叉/压缩 | source | continue:false / additionalContext |
| `SessionEnd` | 会话结束 | reason | 仅记录 exit/error（Codex 不解析 stdout） |
| `UserPromptSubmit` | 用户提交后、模型前 | 无 | block / continue:false / additionalContext |
| `SubagentStart` | 子代理启动 | agent_type | additionalContext |
| `SubagentStop` | 子代理结束 | agent_type | block / continue:false |
| `Stop` | 主代理准备结束 | 无 | block（继续工作）/ continue:false（终止） |
| `Interrupt` | 用户中断 | 无 | 仅 systemMessage |

Codex 的 `HOOK_EVENT_NAMES_WITH_MATCHERS` 只对其中 9 个事件评估 matcher；`UserPromptSubmit`、`Stop`、`Interrupt` 忽略 matcher。

本系统另有原生扩展事件（不在 Codex 12 类中）：

- `PostToolUseFailure`：工具失败时追加的异步通知；
- `StopFailure`：run 非成功终态通知；
- `CheckpointCreated`、`RewindCompleted`、`BacktrackCompleted`。

这些事件只用于本系统内部扩展，不会出现在 Codex 的 `hooks.json` 事件名中。

### 2.2 配置与发现

Codex 的发现来源（`engine/discovery.rs`）：

1. managed requirements 中的 `managed_hooks`（最高优先级，可要求必须加载）；
2. 配置层栈（低到高）：packaged defaults、system、user、project、MDM、enterprise、session flags；
3. 每层可来自 `hooks.json`（配置目录下）或 `config.toml` 的 `hooks` 表；
4. 插件 hooks（`PluginHookSource`），注入 `PLUGIN_ROOT`/`PLUGIN_DATA` 环境变量；
5. `allow_managed_hooks_only` 可禁用 user/project/session hooks；
6. `bypass_hook_trust` 可跳过信任审查（仅受控场景）。

同一层同时存在 `hooks.json` 与 `config.toml hooks` 时会给出警告，但两者都会加载。

### 2.3 Handler 与执行模式

`HookHandlerConfig`：

- `command`：字符串命令；Windows 可用 `commandWindows` 覆盖；`timeout` 秒；`async`；`statusMessage`；`additionalContextLimit`。
- `mcp_tool`：`server`/`tool`/`input`，当前 Codex 对 `SessionEnd` 不支持 MCP hook。

执行模式：

- 同步（默认）：阻塞等待，可产生控制效果；
- 异步（`async:true`）：不阻塞，控制效果被忽略，只上报观测结果；
- executor-scoped hooks 永远异步且不进入用户可见 hook 摘要。

### 2.4 输入/输出协议

输入（stdin）是事件专属 JSON，公共字段包括：

```json
{
  "session_id": "...",
  "turn_id": "...",
  "transcript_path": "...",
  "cwd": "...",
  "hook_event_name": "PreToolUse",
  "model": "...",
  "permission_mode": "..."
}
```

输出（stdout）支持：

- 空输出：无效果；
- 纯文本：在支持的事件上作为 `additionalContext`；
- JSON 对象：
  - 通用：`continue`、`stopReason`、`suppressOutput`、`systemMessage`；
  - 事件专属：`decision`（`block`）、`reason`；
  - `hookSpecificOutput`：`hookEventName`、`additionalContext`、`permissionDecision`、`permissionDecisionReason`、`updatedInput`。

退出码：

- `0`：成功；
- `2`：对支持阻断的事件表示 block，stderr 为原因；
- 其他非零：失败（默认 fail-open）。

### 2.5 关键语义

- `Stop` 的 `decision:block` 表示“不要结束，继续工作”；`continue:false` 表示“终止处理”。两者不能混用。
- `PreToolUse` 的 `permissionDecision:allow` 不是绕过权限，而是“hook 不反对”；Codex 仍会走后续审批/策略。
- `additionalContext` 只进入模型上下文，不写入 durable 历史。
- 异步 handler 不产生控制效果。
- 输出 JSON 形状错误、字段类型错误、`decision:block` 缺少 reason 等都属于 invalid output，默认不阻断。

---

## 3. 本系统现状与差距

### 3.1 已有能力

`backend/internal/hooks`：

- `Manager.Dispatch`：按事件与 matcher 顺序执行，聚合 `continue/block/modify/notify/enrich`；
- `ShellExecutor`/`HTTPExecutor`；
- `matchesHook`：工具名、路径 glob、命令 glob；
- 插件 hooks：`plugins.ActiveHooks()` 合并；
- 生命周期接线：`pre_tool_use`、`permission_request`、`post_tool_use`、`stop`、`pre_compact`、`post_compact`、`subagent_start/stop`、`session_start/end`、`user_prompt_submit`、`checkpoint/rewind/backtrack`。

### 3.2 主要差距

| 差距 | 影响 |
|---|---|
| 无 `hooks.json` 声明式解析 | 不能直接复用 Codex/Claude hooks |
| 无 Codex 输出协议 | 无法表达 `additionalContext`/`continue:false`/`updatedInput` |
| `session_start`/`user_prompt_submit` 异步 | block/context 永远无效 |
| 无作用域/状态/信任 | 不能安全加载项目与插件 hooks |
| 无异步观测 | 异步 hook 结果被静默丢弃 |
| 无诊断面 | 无法回答“加载了哪些 hooks、来自哪里” |
| `post_tool_use` 无模型反馈 | 无法把 hook 结果带回下一步 |

---

## 4. 总体设计

### 4.1 分层

```text
┌─────────────────────────────────────────────────────────────┐
│ Hosts: aicli / runtime-server                               │
│  - 选择 project root / folder trust / plugin catalog        │
│  - 合并 runtime.yaml + hooks.json + plugin hooks            │
└───────────────────────────┬─────────────────────────────────┘
                            │ []HookConfig
┌───────────────────────────▼─────────────────────────────────┐
│ internal/hooks.Manager                                      │
│  - 事件/matcher 选择                                         │
│  - sync: DispatchSync（控制效果）                            │
│  - async: DispatchAsync + AsyncObserver（仅观测）            │
│  - 兼容解析：Codex JSON / 原生 action / 退出码 2             │
└───────────────────────────┬─────────────────────────────────┘
                            │ Decision
┌───────────────────────────▼─────────────────────────────────┐
│ Runtime integration                                         │
│  - agent: prompt-only additionalContext                     │
│  - chat actor: session_start / user_prompt_submit 前置门     │
│  - loop: pre/post tool、stop gate、compact                   │
│  - permission engine: permission_request                    │
└─────────────────────────────────────────────────────────────┘
```

**单一核心约束（2026-10-19 去重）**：`internal/hooks` 是唯一的发现/合并/排序/
managed-only/state/trust 规则实现。aicli 与 runtime-server **不得**各自再写一套
合并逻辑；宿主只负责收集环境输入（project root、folder trust、plugin catalog、
legacy plugin hooks），然后调用：

- `hooks.MergeForHostFromEnv(HostSources)`：运行时使用；
- `hooks.ResolveForHostFromEnv(HostSources)`：诊断/写侧使用（同时返回 Sources/Warnings）。

审计后已消除的重复：

- aicli `mergeDeclarativeHookFiles` 与 runtime-server `mergeDeclarativeAgentHooks` 的重复合并；
- `GET /harness/hooks` 与写侧白名单各自的发现/合并；
- `dispatchLocalAgentHook` / `dispatchAgentHook` 直接 `NewManager(runtime.yaml.Hooks)` 绕过声明式/插件 hooks。

### 4.2 兼容策略：双协议、单内核

不新建第二套 Manager，而是扩展 `HookConfig` 与执行器：

- `HookConfig` 增加 `Async`、`StatusMessage`、`AdditionalContextLimit`、`Source`、`SourcePath`、`Matcher`、`Enabled`；
- `ExecConfig` 增加 `Command`、`CommandWindows`（保留 `Cmd`）；
- `Manager` 增加 `DispatchSync`、`AsyncObserver`、`RawExecutor`；
- 执行器返回原始 stdout/stderr/exit code，由 Manager 统一按事件解析；
- 解析器同时接受 Codex JSON 与原生 `action`，保证旧配置不回归。

### 4.3 控制语义映射

| Codex 输出 | 内部 Decision | 运行时行为 |
|---|---|---|
| `decision:block` + reason | `DecisionBlock` | 阻断/要求继续（事件相关） |
| `continue:false` | `DecisionStop` | 终止当前 turn/session |
| `permissionDecision:deny` | `DecisionBlock` | 拒绝工具/审批 |
| `permissionDecision:allow` | `DecisionContinue` | 不阻断；仍走原有策略 |
| `updatedInput` | `DecisionModify` | 替换工具参数 |
| `additionalContext` | `DecisionEnrich` + `ExtraContext` | 仅注入模型请求 |
| `systemMessage` | `Decision.Message` | 用户可见提示/反馈 |
| 退出码 2 + stderr | `DecisionBlock` | 同 block |
| 其他非零 | fail-open（默认） | 不阻断 |

`DecisionStop` 是新增动作，专门区分“停止”与“继续工作”，避免把 Codex 的 `continue:false` 误当成 stop gate 的 block。

---

## 5. 数据流

### 5.1 发现与合并

宿主在构建 agent 时：

1. 读取 `runtime.yaml hooks`（原生，最高优先）；
2. 读取 user scope `~/.aicli/hooks.{json,yaml,yml}`；
3. 若 workspace 受信任，读取 project scope `<root>/.aicli/hooks.*` 与 `<root>/hooks.*`；
4. 读取 trusted+enabled 插件的 hooks 文件；
5. 应用各文件 `state` 的 `enabled` 覆盖；
6. 合并顺序：runtime → user → project → plugin → extra；同事件按声明顺序执行，第一个 block 胜出。

项目作用域遵循 folder trust：

- aicli：`projectScopeAllowed()`；
- runtime-server：`workspaceFolderTrust()`，server 永不弹窗，未决即失败关闭。

### 5.2 事件时序

```text
SessionStart (sync)
   ├─ continue:false → 终止，发布 session_end(success=false)
   └─ additionalContext → 注入本轮请求

UserPromptSubmit (sync)
   ├─ block/continue:false → 终止，发布 session_end(success=false)
   └─ additionalContext → 注入本轮请求

PreToolUse (sync)
   ├─ block → 工具不执行，错误回注
   ├─ updatedInput → 替换 args 后继续
   └─ additionalContext → 注入下一次模型请求（prompt-only）

PermissionRequest (sync)
   ├─ deny → 拒绝
   └─ allow → 不阻断，继续策略

工具执行

PostToolUse (sync)
   ├─ 并行批次在整批结束后统一触发（按工具逐个）
   ├─ block/reason → 作为下一步反馈
   └─ additionalContext → 追加到该 tool result 的 prompt 视图

Stop (sync)
   ├─ block → 注入恢复提醒，继续 step
   └─ continue:false → 终止，Output/Error 记录原因

Interrupt (async)
   └─ 仅观测，不改变取消流程

SessionEnd (async)
   └─ 仅记录 exit/error（Codex 不解析 SessionEnd stdout）
```

### 5.3 `additionalContext` 注入

- `user_prompt_submit`/`session_start` 的 context 通过 `agent.WithHookAdditionalContext` 挂到 run context；
- loop 在构建初始历史时把它包装成 `system-reminder`（`Durable=false`）追加到请求视图；
- `DurableMessagesForPersist` 在落库前剥离，保证不污染 durable 历史；
- `post_tool_use` 的 context 追加到对应 tool result 的 prompt 视图，durable 侧同样剥离。

---

## 6. 安全与信任

### 6.1 边界

- hooks 只能通过 `Decision` 影响运行时，不能直接调用内部 API；
- `permission_request` 的 allow 不绕过 hard deny、sandbox、capability、tool allow/deny；
- `PreToolUse` 的 `updatedInput` 会重新经过权限/策略校验；
- 异步 handler 的控制效果被丢弃，避免“后台进程改变前台流程”；
- hook 进程有超时与进程树终止（沿用 `ProcessGuard`）。

### 6.2 信任

- user scope：默认加载；
- project scope：必须 folder trust（aicli `/trust` 或 server 已存决定）；
- plugin scope：必须 trusted + enabled；
- `state.enabled=false` 可禁用单个 handler；
- `trusted_hash` 已强制校验：哈希不匹配的 handler 在发现阶段被丢弃并告警。

### 6.3 风险

| 风险 | 缓解 |
|---|---|
| 项目 hooks 执行任意命令 | folder trust + 默认未信任失败关闭 |
| hook 卡住 turn | timeout（默认 3s；hooks.json 可配） |
| hook 输出巨大 | `additionalContextLimit`：超阈值写入 artifact，模型只看到指针 |
| 异步 hook 泄漏控制 | 执行模式隔离，async 只观测 |
| 旧配置回归 | 双协议解析 + 兼容测试 |

---

## 7. 实现清单

### 7.1 新增文件

- `backend/internal/hooks/declarative.go`：Codex `hooks.json` 解析、作用域发现、state、合并。
- `backend/internal/hooks/codex_protocol.go`：Codex 输出解析与 Decision 映射。
- `backend/internal/hooks/executor_mcp.go`：`mcp_tool` handler（注入式 MCP bridge）。
- `backend/internal/hooks/run_recorder.go`：进程内 hooks 运行记录环形缓冲。
- `backend/internal/hooks/state_write.go`：`state.enabled` 写回（JSON + YAML）。
- `backend/internal/hooks/declarative_test.go`、`executor_codex_test.go`、`executor_mcp_test.go`、`state_write_test.go`：协议/发现/MCP/写回测试。
- `backend/internal/api/runtimeapi/harness_hooks_handlers.go`：`GET/POST /harness/hooks` 诊断与启用/禁用。
- `backend/internal/chat/actor_hooks_test.go`：同步阻断端到端测试。
- `backend/internal/chat/session_hook_source_test.go`：clear/close 来源与幂等测试。
- `backend/internal/agent/hook_context_test.go`：context 注入/不落库测试。
- `backend/internal/agent/hook_pre_tool_use_test.go`：`updatedInput` 改写端到端测试。
- `backend/internal/api/runtimeapi/harness_hooks_handlers_test.go`：诊断与写侧权限测试。
- `frontend/src/components/workspace/settings/harness-settings-page/hooks-section.tsx`：Hooks 面板。

### 7.2 修改文件

- `backend/internal/hooks/config.go`：扩展 handler 字段与字符串命令。
- `backend/internal/hooks/types.go`：新增 `EventInterrupt`、`DecisionStop`。
- `backend/internal/hooks/manager.go`：`DispatchSync`、异步 observer、RawExecutor、Codex matcher、合并逻辑。
- `backend/internal/hooks/matcher.go`：Codex matcher 输入解析。
- `backend/internal/hooks/executor_shell.go`：原始输出、退出码、字符串命令。
- `backend/internal/hooks/executor_http.go`：原始响应、非 2xx 语义。
- `backend/internal/agent/turn_context.go`：`WithHookAdditionalContext`。
- `backend/internal/agent/lifecycle_hooks.go`：同步 `DispatchUserPromptSubmit`/`DispatchSessionStart`、Stop 区分。
- `backend/internal/agent/hooks.go`：`post_tool_use` 同步反馈与 payload 补全。
- `backend/internal/agent/loop.go`：context 注入、post-tool 反馈、stop 终止。
- `backend/internal/agent/agent.go`：异步 observer 事件。
- `backend/internal/chat/actor.go`：turn 前置同步 hook、block 终态、interrupt hook。
- `backend/internal/config/manager.go`：事件名规范化与字符串命令校验。
- `backend/internal/plugins/package.go`/`discover.go`：`HooksFilePath` 与 `ActiveHooksFiles`。
- `backend/cmd/aicli/commands/plugin_runtime.go`、`chat_actor_registry.go`、`chat_actor_host.go`：宿主输入收集、子代理事件共用合并、close hook。
- `backend/internal/api/runtimeapi/handler.go`、`session_runtime_support.go`：server 侧合并、close hook、子代理事件共用合并。
- `backend/internal/events/contract.go`：登记 `hooks.status`（live-only）。
- `frontend/src/api/runtime/harness.ts`、`types/runtime/harness.ts`、`hooks/workspace/use-harness-control-plane.ts`、settings/i18n：Hooks 面板接线。

---

## 8. 测试与验证

已通过：

```text
go build ./internal/hooks ./internal/agent ./internal/chat ./internal/policy ./internal/api/runtimeapi ./internal/events ./cmd/aicli/commands
go test ./internal/hooks ./internal/policy -p 1
go test ./internal/agent -run 'Hook|Compact|Stop|PreToolUse' -p 1
go test ./internal/chat -run 'Hook|Compact|Clear|Close|InterruptEventCarriesPartialProduct' -p 1
go test ./internal/api/runtimeapi -run 'TestGetHarnessHooks|TestUpdateHarnessHook' -p 1
go run ./cmd/contractgen -check
npx tsc -b && npx vitest run src/components/workspace/settings/harness-settings-page.test.tsx
```

说明：

- `cmd/aicli/commands` 全量测试包体量大、耗时超过单次窗口，未全量执行；已用 `go build` 覆盖编译。
- 新增测试覆盖：Codex 声明式解析、legacy 兼容、matcher、输出协议、退出码 2、异步不控制、state 禁用、context 注入不落库、actor 前置阻断、诊断 API。

---

## 9. 与 Codex 的差异与后续

### 9.1 已实现

- 12 类事件常量与 matcher；
- command handler（字符串命令、Windows 覆盖、timeout、async、statusMessage、additionalContextLimit）；
- Codex JSON 输出与退出码 2，包括 `PermissionRequest` 的嵌套 `decision.behavior`；
- Codex 公共输入字段（`hook_event_name`/`model`/`permission_mode`/`cwd`/`tool_input`/`tool_use_id`/`tool_response`/`turn_id`）；
- `additionalContext` 注入；
- `continue:false` 终止（含 `PostToolUse` 的“停止并反馈”）；
- 多作用域发现、state、folder trust；
- 异步观测事件；
- 诊断 API；
- `mcp_tool` handler（通过 `Manager.SetMCPToolCaller` 注入 MCP bridge）；
- `additionalContextLimit` 的 artifact spill（超过阈值写入 artifact，返回指针）；
- `trusted_hash` 校验（不匹配的 handler 被丢弃并告警）；
- `allow_managed_hooks_only`（`AICLI_ALLOW_MANAGED_HOOKS_ONLY`）；
- `SessionStart source=fork`（分支会话首次 turn）。
- `GET/POST /harness/hooks` 只读诊断 + user/project handler 启用/禁用（写回 `state.enabled`，JSON 文件）。
- `hooks.status` runtime event（started/completed/failed + statusMessage），供前端/TUI 运行态。
- `GET /harness/hooks` 返回 `recent_runs`（进程内有界环形缓冲，最近 100 条），面板展示最近运行记录。
- `SessionStart` 来源补齐 `clear`（清空历史）与 `compact`（压缩后下一 turn）；`fork` 已在分支创建时写入。
- hooks 文件 `state.enabled` 写回支持 JSON 与 YAML（YAML 通过 `yaml.Node` 保留注释与结构）。
- `SessionManager.SetSessionCloseHook`：会话关闭时派发 `SessionEnd`（`scope=session`）；turn 终态仍为 `scope=turn`。

审查修复（2026-10-19 第二轮）：

- `PermissionRequest` 的 allow/deny 改从 `hookSpecificOutput.decision.behavior` 读取；
- `PreToolUse` 的 `updatedInput` 仅允许与 `permissionDecision:allow` 同时出现；
- `DispatchSync` 遇到 `DecisionStop` 立即返回；
- `mergeDecision` 保护 `DecisionModify` 不被后续 `DecisionEnrich` 覆盖；
- 异步 observer 与 matcher 正则缓存加锁；
- `SessionStart` 改为每会话只触发一次（`hooks.session_started` 持久标记）；
- `Stop` 的 `stop_hook_active` 按本轮是否已因 stop hook 继续来设置。

### 9.2 能力边界与已知差异

| 能力 | 说明 |
|---|---|
| `session_start` 的 `compact` 时机 | 压缩后的下一 turn 触发；Codex 为压缩后立即触发 |
| `session_end` 的会话级语义 | turn 终态发 `scope=turn`；aicli 与 runtime-server 的 `SessionManager.Close` 均发 `scope=session` |
| `additionalContextLimit` 估算 | 按 `字符数/4` 粗略估算 token，非精确分词 |
| `statusMessage` 实时 UI | 已发 `hooks.status` 并在面板展示最近运行记录；尚无逐条 running 动画 |
| hooks 文件写回范围 | 仅 user/project 作用域；plugin/managed 由各自信任存储治理 |
| `PostToolUse` 的 `updatedMCPToolOutput` | Codex 当前也不支持，未实现 |
| `mcp_tool` 的 `SessionEnd` | Codex 也不支持；本系统跳过并告警 |
| `SessionEnd` 输出解析 | Codex 不解析 SessionEnd stdout；本系统同样只记录 exit/error |

### 9.3 建议下一步

1. 补 `SessionEnd` 的 suppressOutput 展示（Codex 当前不解析 SessionEnd stdout，优先级低）。

### 9.4 运维开关

| 环境变量 | 作用 |
|---|---|
| `AICLI_MANAGED_HOOKS_FILES` | 管理员 hooks 文件列表（`;`/`:` 分隔，按 OS 规则） |
| `AICLI_ALLOW_MANAGED_HOOKS_ONLY` | `1/true/yes/on` 时只加载 managed hooks，忽略 user/project/plugin/runtime hooks |

managed 文件使用与 `hooks.json` 相同的格式；`state.trusted_hash` 会校验 handler 的可执行声明哈希。

---

## 10. 关键不变量

1. **旧配置不回归**：`runtime.yaml hooks` 与旧插件 hooks 的语义、事件名、输出格式保持兼容。
2. **控制效果只来自同步 handler**：异步 handler 永远不能 block/modify/stop。
3. **allow 不是 bypass**：`permissionDecision:allow` 只表示“不反对”，后续策略与 hard deny 仍执行。
   - `PermissionRequest` 的 `allow` 在本系统中同样不会短路审批：它只表示 hook 不反对，静态策略、规则与 hard deny 继续执行。Codex 的 allow 可直接放行，这是有意的安全差异。
4. **上下文不落库**：`additionalContext` 只进入请求视图，`DurableMessagesForPersist` 负责剥离。
5. **block ≠ stop**：block 要求继续工作，stop 终止处理。
6. **项目作用域失败关闭**：folder trust 未决时不加载 project hooks。
7. **hook 失败默认 fail-open**：除非 handler 显式 `on_error: fail_closed`。

---

## 11. 实施备注（2026-10-19）

实施期间工作区被外部操作以 `stash@{0}: wip-hooks-parallel` 暂存并回滚过一次。

> 面向使用者的配置位置、事件表、handler 写法、输出协议与调试入口，见
> [docs/user-guide/hooks.md](../user-guide/hooks.md)。

本次改动已从该 stash 按文件恢复：

- tracked 改动：`internal/hooks/*`、`internal/agent/{hooks,lifecycle_hooks,turn_context,loop,agent}.go`、`internal/chat/actor.go`、`internal/api/runtimeapi/handler.go`、`internal/config/manager.go`、`internal/plugins/{package,discover}.go`、`cmd/aicli/commands/plugin_runtime.go`；
- untracked 新文件：`internal/hooks/{declarative,codex_protocol}.go`、`internal/api/runtimeapi/harness_hooks_handlers.go`、测试文件。

该 stash 同时包含另一个并行任务（compactruntime/tokenestimate/图片 token 估算）的改动；恢复时只取上述 hooks 相关路径，未删除或回滚并行任务文件。若后续需要清理 stash，请先确认并行任务已合并。
