# aicli /goal 命令实施方案

## 背景

参考项目 `E:\projects\ai\codex` 中的 `/goal` 是一个线程级目标管理能力。它不是普通用户消息，而是 TUI slash command：命令层负责创建、查看、暂停、恢复、清除目标，app-server 将目标持久化到 `thread_goals`，core runtime 在后续 turn 中读取 active goal，必要时自动续跑，并允许模型通过 `update_goal` 工具把目标标记为完成。

当前项目 `E:\projects\ai\ai-agent-runtime` 已具备实现该能力的基础：

- CLI slash command 入口在 `backend/cmd/aicli/commands/command.go` 的 `handleCommand`。
- slash command 目录、帮助和补全在 `backend/cmd/aicli/commands/chat_slash_command_catalog.go`、`chat_slash_help.go` 及相关测试中维护。
- chat 持久化会话是 JSON session，结构在 `backend/internal/chat/session.go`，可通过 `Session.Metadata.Context` 保存扩展状态。
- chat session 同步逻辑在 `backend/cmd/aicli/commands/chat_session.go` 的 `syncRuntimeSessionFromChat`。
- runtime actor 已支持 session 继续执行，入口在 `backend/internal/chat/actor.go` 的 `runLoop` 和 `continueLoop`。
- ReAct loop 已有 `RunWithSession`、`ContinueWithSession`、`BudgetTokens`、usage 累计、context preflight 和 context manager `Goal` 字段，位置在 `backend/internal/agent/loop.go`。

因此本项目不需要一开始引入 Codex 的 SQLite thread goal 体系。更稳妥的路线是先把 `/goal` 做成 session metadata 级功能，再逐步扩展到 runtime 注入、模型工具、预算和自动续跑。

## 目标

实现 `/goal` 命令，用于把当前会话绑定到一个长期任务目标，并让后续执行能够围绕该目标持续推进。

MVP 目标：

- 支持 `/goal` 查看当前会话目标。
- 支持 `/goal <objective>` 设置或替换当前目标。
- 支持 `/goal clear` 清除目标。
- 支持 `/goal pause` 暂停目标。
- 支持 `/goal resume` 恢复目标。
- 支持 `/goal complete` 手动标记完成。
- 目标随当前 runtime session 持久化，`/load`、`/resume` 后可恢复。

后续目标：

- active goal 自动注入模型上下文。
- 模型可调用目标工具查询或完成目标。
- 统计 goal 级 token/time 使用量。
- 支持 token budget 和 `budget_limited` 状态。
- 可选支持自动 continuation turn。
- runtime-server 暴露 goal API，供前端或其他客户端管理目标。

## 非目标

- 第一阶段不实现完整 Codex app-server/SQLite 架构。
- 第一阶段不自动无限续跑，避免误触发高成本循环。
- 第一阶段不改变现有 `/resume` 命令语义；`/goal resume` 仅恢复目标状态。
- 第一阶段不引入复杂交互确认菜单；命令行为保持 CLI 友好、可测试。

## Codex /goal 机制摘录

Codex 中 `/goal` 的核心路径如下：

- slash command 定义：`codex-rs/tui/src/slash_command.rs`
- 命令分发：`codex-rs/tui/src/chatwidget/slash_dispatch.rs`
- goal 菜单、校验、状态展示：`goal_menu.rs`、`goal_validation.rs`、`goal_status.rs`
- TUI event 到 app-server：`app_event.rs`、`event_dispatch.rs`、`thread_goal_actions.rs`
- 协议：`app-server-protocol/src/protocol/v2/thread.rs`
- app-server processor：`thread_goal_processor.rs`
- 状态模型和迁移：`state/migrations/0029_thread_goals.sql`、`state/src/model/thread_goal.rs`
- core runtime：`core/src/goals.rs`
- continuation/budget prompt 模板：`core/templates/goals/*.md`
- 模型工具：`core/src/tools/handlers/goal*.rs`

重要行为：

- `/goal` 无参数显示当前 goal。
- `/goal <objective>` 创建或替换 goal，objective 最大长度为 4000 字符。
- `/goal clear|pause|resume` 改变状态。
- goal 状态包括 `active`、`paused`、`budget_limited`、`complete`。
- active goal 会被 runtime 读取并注入上下文。
- core 可通过模型工具 `update_goal` 标记目标完成。

## 当前项目适配设计

### 数据模型

新增内部模型，建议放在 `backend/internal/goal`：

```go
type GoalStatus string

const (
    GoalStatusActive        GoalStatus = "active"
    GoalStatusPaused        GoalStatus = "paused"
    GoalStatusBudgetLimited GoalStatus = "budget_limited"
    GoalStatusComplete      GoalStatus = "complete"
)

type SessionGoal struct {
    GoalID          string     `json:"goal_id" yaml:"goal_id"`
    SessionID       string     `json:"session_id" yaml:"session_id"`
    Objective       string     `json:"objective" yaml:"objective"`
    Status          GoalStatus `json:"status" yaml:"status"`
    TokenBudget     int        `json:"token_budget,omitempty" yaml:"token_budget,omitempty"`
    TokensUsed      int        `json:"tokens_used,omitempty" yaml:"tokens_used,omitempty"`
    TimeUsedSeconds int64      `json:"time_used_seconds,omitempty" yaml:"time_used_seconds,omitempty"`
    CreatedAt       time.Time  `json:"created_at" yaml:"created_at"`
    UpdatedAt       time.Time  `json:"updated_at" yaml:"updated_at"`
    CompletedAt     *time.Time `json:"completed_at,omitempty" yaml:"completed_at,omitempty"`
}
```

约束：

- `Objective` trim 后不能为空。
- `Objective` 最大 4000 字符，保持与 Codex 一致。
- `GoalID` 使用 `goal_YYYYMMDDHHMMSS_<random>` 或 UUID。
- 同一 runtime session 第一阶段只维护一个 current goal。

### 存储策略

推荐第一阶段将 goal 保存在 `runtimechat.Session.Metadata.Context`：

```json
{
  "aicli.goal": {
    "goal_id": "goal_...",
    "session_id": "session_...",
    "objective": "...",
    "status": "active",
    "tokens_used": 1234,
    "created_at": "...",
    "updated_at": "..."
  }
}
```

理由：

- 与当前 JSON session store 完全兼容。
- `/load`、`/resume` 自动恢复。
- 不需要新增迁移或外部数据库。
- `syncRuntimeSessionFromChat` 已经集中处理 metadata 写回。

实现上仍应抽象 `GoalStore`，避免未来从 metadata 切换到独立文件或 SQLite 时污染命令层：

```go
type Store interface {
    Get(session *chat.Session) (*SessionGoal, bool, error)
    Put(session *chat.Session, goal SessionGoal) error
    Clear(session *chat.Session) error
}
```

第一版实现为 `MetadataStore`，读取和写入 `Session.Metadata.Context["aicli.goal"]`。

### 命令交互

在 `backend/cmd/aicli/commands/command.go` 的 `handleCommand` 中新增：

```go
if commandMatches(cmdLower, "/goal") {
    return handleGoalCommand(session, command)
}
```

新增文件建议：

- `backend/cmd/aicli/commands/goal.go`
- `backend/cmd/aicli/commands/goal_test.go`

同时更新：

- `backend/cmd/aicli/commands/chat_slash_command_catalog.go`：加入 `/goal` 的 command metadata，确保补全、目录和 UI 提示一致。
- `backend/cmd/aicli/commands/chat_slash_help.go`：帮助文本加入 `/goal`。
- `backend/cmd/aicli/commands/chat_slash_completion_test.go`：覆盖 `/goal`、`/goal clear`、`/goal pause`、`/goal resume`、`/goal complete` 的补全行为。

命令语义：

- `/goal`：显示当前 goal 摘要；无 goal 时显示“当前会话未设置 goal”。
- `/goal <objective>`：设置新 goal；如果已有 active/paused goal，直接替换并打印原 goal 状态。
- `/goal clear`：清除当前 goal。
- `/goal pause`：将 active goal 改为 paused。
- `/goal resume`：将 paused 或 `budget_limited` goal 改为 active。
- `/goal complete`：将当前 goal 改为 complete。
- `/goal status`：等价于 `/goal`，便于显式脚本调用。
- `/goal --json`：可选增强，输出结构化 goal 状态。
- `/goal --budget 200000 <objective>`：第二阶段或第三阶段加入。

持久化前置条件：

- `/goal` 查看命令允许在无 `RuntimeSession` 时执行，并显示“当前会话未设置 goal”。
- `/goal <objective>`、`pause`、`resume`、`complete`、`clear` 需要当前会话启用 runtime session 持久化。
- 如果 `session == nil`、`session.RuntimeSession == nil` 或 `session.SessionManager == nil`，写操作应返回明确错误：`当前会话未启用持久化，无法设置 goal`。
- MVP 不自动创建 runtime session，避免 slash command 隐式改变 `/new`、`--ephemeral` 或非持久化 chat 的生命周期。

输出示例：

```text
Goal: active
Objective: 实现 /goal 命令并补齐测试
Tokens: 12034
Updated: 2026-05-13 08:40:11
```

### session 生命周期集成

需要在以下位置处理 goal：

- `createNewRuntimeConversation`：新会话默认无 goal。
- `loadRuntimeConversation` / `resumeLatestRuntimeConversation`：无需额外逻辑，goal 随 `RuntimeSession.Metadata.Context` 恢复。
- `syncRuntimeSessionFromChat`：如果命令层修改了 `session.RuntimeSession.Metadata.Context["aicli.goal"]`，现有同步会保留；如果后续在 `ChatSession` 增加 `Goal` 字段，则必须在此函数双向同步。
- `printCurrentRuntimeSession`：可选显示 `Goal:` 一行，建议只显示状态和 objective 摘要，避免污染 session 元信息输出。
- `/clear`：只清空历史，不应清除 goal；否则长期目标会因清上下文丢失。
- `/new`：创建新 session，新 session 没有 goal。

第一阶段不建议在 `ChatSession` 结构上直接加复杂字段。命令执行时直接操作 `session.RuntimeSession` 的 metadata，并调用 `syncRuntimeSessionFromChat` 保存。

## Runtime 行为分阶段

### Phase 1：命令和持久化

范围：

- 新增 `internal/goal` 模型、校验、metadata store。
- 新增 `/goal` 命令处理。
- 目标写入当前 runtime session JSON。
- 补齐命令和 store 单测。

不改变模型请求，不自动续跑。

### Phase 2：active goal 上下文注入

目标：active goal 对模型可见，但不改变 turn 调度。

推荐实现：

- 在 chat 发送消息前读取 active goal。
- 将 goal 以 developer/system guidance 追加到现有系统提示组合中，或通过 ReAct loop options 传入 context manager 的独立 persistent goal 字段。
- 当前 `ReActLoop.think` 已将 `goal` 参数传给 `contextmgr.BuildInput.Goal`，但该参数实际来自当前用户 prompt，并被上下文管理器用于当前 turn 的检索和裁剪。不能用长期 goal 覆盖它。

建议改造：

```go
type loopRunOptions struct {
    PersistentGoal string
    ...
}
```

然后在 `think` 中同时保留当前 prompt 和长期 goal。不要使用 `firstNonEmpty(options.PersistentGoal, goal)` 这类覆盖逻辑。

推荐方案 A：扩展 context manager 输入模型：

```go
type BuildInput struct {
    Goal           string
    PersistentGoal string
    ...
}
```

调用时：

```go
Goal:           goal,                    // 当前用户 prompt
PersistentGoal: options.PersistentGoal,  // /goal 设置的长期目标
```

推荐方案 B：如果暂不改 `contextmgr.BuildInput`，则在 ReAct loop 内组合成明确文本：

```go
Goal: renderGoalForContextManager(options.PersistentGoal, goal)
```

组合文本必须包含两个标签，例如：

```text
Persistent goal:
...

Current request:
...
```

这样 context manager 仍能看到当前请求，不会把长期目标误当成本 turn 的唯一目标。

对 CLI 本地 chat，可在 `sendMessage` 或 actor 调用前从 session metadata 取 active goal 并放入 actor/run options。对 `SessionActor.runLoop`，可扩展 actor 配置或 context metadata。无论走 CLI 本地执行、actor-first 还是 runtime-server，都应复用同一套 goal 读取逻辑。

### Phase 3：模型目标工具

新增模型工具：

- `get_goal`：返回当前 session goal。
- `update_goal`：允许模型将 active goal 标记为 `complete`，可附带 summary。

安全边界：

- 模型不能直接 `clear`、`pause`、`resume` 用户 goal。
- 模型不能创建或替换用户 goal，除非后续显式允许。
- `update_goal` 第一版只接受 `status=complete`，避免模型绕过用户控制。

工具注册位置需按当前工具体系选择：

- CLI builtin functions：`backend/cmd/aicli/functions`
- chat function catalog 集成：`backend/cmd/aicli/commands/function_catalog.go`
- runtime tool broker：如需 actor-first 和 runtime-server 共用，应优先接入内部 tool registry，而不是只接 CLI 直接命令。

实施定案：

- `get_goal` / `update_goal` 应优先接入正常 chat tool call 使用的统一工具路径，保证 actor-first、本地 chat 和 runtime-server 行为一致。
- CLI `/call get_goal` 可作为调试入口，但不能是唯一实现路径。
- 如果当前工具暴露仍由 `aicliFunctionCatalog` 汇总，则目标工具应作为 builtin tool 注册到 catalog，并通过现有 actor/runtime-server 桥接路径暴露给模型。
- Phase 3 的验收测试必须覆盖至少两条路径：普通 chat tool call 与 runtime-server/actor-first 执行。

### Phase 4：usage、预算和 budget_limited

目标：

- active goal 累计 token 使用量。
- 支持 `/goal --budget <tokens> <objective>`。
- 当 `tokens_used >= token_budget`，状态改为 `budget_limited`，不再自动续跑。

数据来源：

- `backend/internal/agent/loop.go` 已在 `totalUsage.Add(usage)` 累计 usage。
- `llm.request.finished` runtime event 已带 `usage_*_tokens`。
- CLI 层也有 `/status` token usage 相关逻辑。

推荐做法：

- 在 ReAct loop result 返回时把 `result.Usage.TotalTokens` 增量计入 goal。
- 对 actor-first runtime，可在 `SessionActor` run 完成后更新 goal store。
- 预算限制使用 `loopRunOptions.BudgetTokens` 作为单次 run 限制，并同时维护 goal 总预算。
- 触发 budget limit 后发送 runtime event：`goal.budget_limited`。

### Phase 5：自动 continuation

目标：接近 Codex 行为，当 active goal 未完成且模型完成了一轮响应后，runtime 可以自动继续。

必须加防护：

- 限制最大 continuation 次数，例如每次用户输入后最多 1 到 3 次。
- 限制 wall time，例如 10 分钟。
- 遇到 tool approval、等待用户输入、空回复、重复 prompt fingerprint、budget_limited、complete 时停止。
- 长期目标完成判断仍由模型根据上下文审计后调用 `update_goal(status=complete)` 完成，运行时不做硬编码语义匹配。
- 理想实现应调用 `ContinueWithSession`，不要追加新的 user prompt。

当前项目已有 `ContinueWithSession`，因此实现难点不在执行能力，而在停止条件和用户可见状态。

当前实施状态：

- `sendMessage` 在普通响应成功后调用 `maybeAutoContinueActiveGoal`。
- 仅当当前 session 存在持久化 active goal、处于交互模式、工具未禁用、非 JSON 输出、未中断且没有排队输入时触发。
- 当前默认每次用户输入后最多自动续跑 1 次，避免隐藏循环和成本失控。
- shared chat executor 使用隐藏 continuation instruction 触发审计；该 instruction 会进入本次 provider history，但会在写回 session history 前剥离，不作为用户消息持久化。
- 自动续跑失败只记录 warning/debug，不让首轮用户响应变成失败。
- shared、actor-first 和 runtime-server 均通过各自 executor 的 `ContinueGoal` 触发隐藏 completion audit；只有当前路径确认暴露 `update_goal` 时才会自动续跑，避免提示或触发模型调用不可用工具。
- 当 `update_goal` 标记完成后，会立即刷新 system prompt，避免完成后的持久化历史继续携带 active goal guidance。

## Runtime Server API

如需要前端或外部客户端管理 goal，建议新增 API：

- `GET /api/runtime/sessions/{id}/goal`
- `PUT /api/runtime/sessions/{id}/goal`
- `PATCH /api/runtime/sessions/{id}/goal`
- `DELETE /api/runtime/sessions/{id}/goal`

事件：

- `goal.updated`
- `goal.cleared`
- `goal.completed`
- `goal.paused`
- `goal.resumed`
- `goal.budget_limited`

API 层应复用 `internal/goal.Store`，不要重复解析 metadata。

## 测试计划

### Unit tests

- `internal/goal`：
  - create/update/clear。
  - objective trim 和 4000 字符限制。
  - unknown status 拒绝。
  - metadata JSON map decode 兼容。
- `commands/goal_test.go`：
  - `/goal` 无 goal。
  - 无 runtime session 时，`/goal` 可查看，写操作返回明确错误。
  - `/goal <objective>` 创建 goal。
  - `/goal clear` 清除。
  - `/goal pause`、`/goal resume`、`/goal complete` 状态转换。
  - oversize objective 返回错误。
- slash command：
  - `/goal` 出现在 catalog/help。
  - `/goal` 子命令补全行为稳定。

### Integration tests

- 设置 goal 后 session JSON 中存在 `metadata.context["aicli.goal"]`。
- `/load <session-id>` 后 `/goal` 能显示原 goal。
- `/clear` 不清除 goal。
- `/new` 创建的新 session 不继承旧 goal。

### Runtime tests

第二阶段后补：

- active goal 被注入 context manager，但当前用户 prompt 仍保留在 `BuildInput.Goal` 或等价字段中。
- paused/complete goal 不注入。
- `update_goal` 工具只能 complete。
- `get_goal` / `update_goal` 通过正常 chat tool call 和 actor-first/runtime-server 路径可用。
- budget 达到上限后状态变为 `budget_limited`。
- auto continuation 遇到完成、暂停、等待输入和禁用工具时停止。
- sendMessage 对 active goal 会触发一次 completion audit continuation；模型调用 `update_goal(status=complete)` 后状态持久化为 complete。

## 实施顺序

1. 新增 `backend/internal/goal`：模型、校验、metadata store、格式化摘要。
2. 新增 `backend/cmd/aicli/commands/goal.go`：命令解析和状态转换。
3. 在 `handleCommand` 接入 `/goal`。
4. 更新 slash command catalog、help 和 completion tests。
5. 补齐 Phase 1 单测和 session 持久化测试。
6. 在 `printCurrentRuntimeSession` 可选显示 goal 简要状态。
7. 实施 Phase 2：active goal 注入 ReAct/context manager，保留当前 prompt 与 persistent goal 的独立语义。
8. 实施 Phase 3：模型工具 `get_goal`、`update_goal`，接入正常 chat tool call 的统一路径。
9. 实施 Phase 4：usage 和 budget。
10. 实施 Phase 5：受限自动 continuation。第一版在 CLI `sendMessage` 层完成，后续再升级为 actor/runtime 原生 continuation。
11. 如有前端或远程控制需求，再补 runtime-server API。

## 风险与处理

- 目标被误当普通 prompt：需要区分当前用户 prompt 和 persistent goal，避免复用 `think(... goal string ...)` 的旧参数语义。
- 无持久化 session 行为不一致：MVP 明确写操作需要 `RuntimeSession` 和 `SessionManager`，不自动创建 session。
- slash command 元数据遗漏：`handleCommand`、catalog、help、completion 必须同步更新。
- 自动续跑成本失控：当前默认每个用户 turn 最多 1 次；后续如放宽次数，必须补时间、token、重复 prompt 等停止条件。
- session metadata 结构演进：为 `aicli.goal` 增加版本字段也可行，但 MVP 可先保持结构简单。
- 模型工具越权：模型只能 complete，不允许 clear/pause/resume/create。
- `/clear` 语义冲突：明确 `/clear` 清历史不清 goal，清 goal 必须用 `/goal clear`。
- Windows 命令长度：实施时避免大 inline 脚本，Go 测试命令保持短命令；大补丁按文件分块提交。

## MVP 验收标准

- 运行 `aicli chat` 后可通过 `/goal 实现目标` 设置目标。
- `/goal` 能显示当前目标状态和内容摘要。
- `/goal pause/resume/complete/clear` 行为正确。
- 未启用 runtime session 持久化时，`/goal` 查看不报错，写操作给出明确错误。
- 退出后重新 `/load` 或 `/resume` 同一 session，goal 仍存在。
- session JSON 中可看到 `metadata.context["aicli.goal"]`。
- `/goal` 出现在 slash help/catalog/completion 中。
- 现有 `/clear` 不删除 goal。
- active goal 会在普通响应后触发一次自动 completion audit；模型可通过 `update_goal` 自动将目标标记为完成。
- 新增测试覆盖命令解析、状态转换和持久化。
