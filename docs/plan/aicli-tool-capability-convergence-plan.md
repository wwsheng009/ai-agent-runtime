# aicli 工具能力与执行路径收敛实施方案

## 背景

当前 `aicli chat` 同时存在多条执行路径：

- `shared chat executor`：CLI-native 的旧聊天执行路径，核心在 `backend/cmd/aicli/commands/chat_core.go`、`chat_provider_turn.go`、`function_catalog.go`。
- `actor-first executor`：本地 runtime-native 路径，核心在 `backend/cmd/aicli/commands/chat_actor_executor.go`、`chat_actor_host.go`、`backend/internal/chat/actor.go`、`backend/internal/agent/loop.go`。
- `runtime-server executor`：远端 runtime 路径，核心在 `backend/cmd/aicli/commands/chat_runtime_server.go`。

这三条路径都可能向模型暴露工具、执行工具调用、写回会话历史和更新 session metadata。它们的底层实现不同，但对用户和模型来说都表现为“当前会话有哪些工具可用、调用工具后状态如何变化”。

`/goal` 的真实 mimo provider 测试暴露了这个架构风险：

1. `get_goal/update_goal` 最初只注册到 `FunctionCatalog`，因此 shared path 可见。
2. 真实 `chat --no-interactive` 默认走 actor-first，actor 的工具面来自 `MCPManager + ToolBroker`，所以实际请求中没有 `update_goal`。
3. 修复 actor 工具暴露后，mimo 已经实际调用 `update_goal`，工具结果也返回了 `complete`。
4. 但 actor 后续保存旧 session context 时，又把 `aicli.goal` 覆盖回 `active`。

这说明问题不只是 `/goal` 的实现缺口，而是工具能力、工具可用性判断和 session metadata 写入缺少统一边界。后续如果新增 `/task`、`/memory`、`/budget`、profile metadata、checkpoint 等模型可调用能力，很容易重复出现同类问题。

本方案的目标是用分阶段方式收敛这些横切能力，保留多 executor 架构，但消除工具注册和状态写入的路径分裂。

## 目标

### 直接目标

- 新增统一的工具可用性判断 API，避免系统提示和真实工具面不一致。
- 新增跨路径 tool surface parity 测试，确保关键工具在 shared 和 actor-first 中同时可见。
- 新增通用 post-turn reconciler 钩子，避免工具执行结果被旧 session metadata 覆盖。
- 将 `/goal` 当前的 actor 特例逐步迁移为通用能力收敛机制的试点。
- 为 runtime-server 明确 unsupported 行为，避免模型被错误提示调用不可用工具。

### 中期目标

- 建立统一 `ToolCapability` / `CapabilityRegistry`，让模型工具只声明一次。
- 通过 adapter 将同一份能力投影到：
  - shared path 的 `FunctionCatalog`
  - actor-first 的 `MCPManager`
  - runtime-server 的远端工具 contract
- 将 `FunctionCatalog` 从事实源降级为 shared path adapter。
- 将 `goalActorToolSurface` 这类特例替换为通用 capability-to-MCP adapter。

### 长期目标

- 明确 actor-first 是默认主路径，shared executor 是 fallback/legacy path。
- runtime-server 与 actor-first 使用同一套工具能力 contract。
- session metadata 修改统一走 storage-aware patch API，避免整份旧 session 覆盖最新状态。
- 新增模型工具时不需要分别接入 shared、actor-first、runtime-server 三套逻辑。

## 非目标

- 不在第一阶段删除 shared chat executor。
- 不在第一阶段重写 `ToolBroker`。
- 不在第一阶段一次性迁移所有工具到新 registry。
- 不在第一阶段强制 runtime-server 支持 `/goal update_goal`。
- 不在第一阶段改变用户可见的 slash command 行为。

## 当前问题分解

### 1. 工具注册分裂

当前 shared path 主要通过 `aicliFunctionCatalog` 暴露工具：

- `backend/cmd/aicli/commands/function_catalog.go`
- `backend/cmd/aicli/commands/chat_setup.go`
- `backend/cmd/aicli/commands/goal_functions.go`

actor-first 主要通过两类工具面暴露工具：

- `host.ToolSurface`，类型为 `runtimeskill.MCPManager`
- `apiAgent.GetToolBroker()`，类型为 `toolbroker.Broker`

相关位置：

- `backend/cmd/aicli/commands/chat_actor_host.go`
- `backend/internal/tools/agent_adapter.go`
- `backend/internal/toolbroker/broker.go`
- `backend/internal/agent/loop.go`

因此，单独调用：

```go
registerGoalFunctions(session)
```

只能保证 shared path 可见，不能保证 actor-first 可见。

### 2. 工具可用性判断分裂

系统提示、命令输出、debug、测试和真实 provider request 可能使用不同的判断方式。

`/goal` 当前判断逻辑在：

```go
canCurrentChatPathUpdateGoal(session)
```

内部按 executor 类型分支：

```go
switch session.ChatExecutor.(type) {
case *aicliActorChatExecutor:
    return chatActorToolSurfaceHasGoalUpdate(session)
case *aicliRuntimeServerChatExecutor:
    return false
default:
    return catalog.Registry().Get(updateGoalFunctionName)
}
```

这类判断如果分散到每个功能里，会持续制造维护风险。

### 3. 工具执行分裂

shared path 执行工具：

```go
FunctionCatalog.ExecuteFunctionWithMeta(ctx, name, args)
```

actor-first 执行工具：

```go
agent.mcpManager.CallTool(...)
broker.ExecuteToolCall(...)
```

`ToolBroker` 自己维护大量静态 schema 和执行分发：

```go
func (b *Broker) Definitions() []types.ToolDefinition
func (b *Broker) execute(...)
```

如果某工具同时需要 shared 和 actor-first 支持，就容易出现 schema 不一致、metadata 不一致、policy 不一致、执行结果类型不一致。

### 4. session metadata 写入分裂

当前很多代码直接修改：

```go
session.Metadata.Context[key] = value
```

或者修改 CLI 层的：

```go
session.RuntimeSession
```

然后调用：

```go
syncRuntimeSessionFromChat(session)
```

actor-first 中还存在 actor 自己加载和保存的 session 对象。工具修改某个 `*runtimechat.Session` 并不保证最终保存点使用的是同一个对象。

这次 mimo 实测中就出现：

- `update_goal` 工具结果里 goal 已是 `complete`
- 但最终 session 文件中的 `aicli.goal` 又变回 `active`

原因是旧 actor session context 在后续保存时覆盖了工具写入后的 metadata。

### 5. 测试覆盖偏路径局部

已有测试能覆盖函数内部逻辑，例如 `executeUpdateGoalFunction`。但路径接入类问题需要更高层测试：

- shared path provider request 是否包含工具
- actor-first provider request 是否包含工具
- 工具调用后最终持久化 session 是否正确
- debug metadata 的 `tool_surface.names` 是否包含预期工具

如果只测工具函数本身，无法发现真实默认路径漏接。

## 目标架构

### 分层

目标架构分为四层：

```text
ToolCapability / CapabilityRegistry
    |
    +-- FunctionCatalog adapter         -> shared chat executor
    |
    +-- MCPManager adapter              -> actor-first mcpManager
    |
    +-- ToolBroker / runtime adapter    -> actor-first broker/runtime tools
    |
    +-- Runtime-server adapter          -> remote runtime tool contract
```

### 事实源

工具能力事实源应从：

```text
FunctionCatalog
MCPManager wrappers
ToolBroker.Definitions
各功能自己的特例注册函数
```

逐步收敛为：

```text
CapabilityRegistry
```

`FunctionCatalog`、`MCPManager`、runtime-server tool surface 都只是 registry 的投影视图。

### 状态写入

session metadata 写入从：

```text
直接修改某个 Session 对象，然后整份保存
```

逐步收敛为：

```text
storage-aware metadata patch
```

patch API 每次从 storage 加载最新 session，只修改指定 context key，然后保存，避免旧对象覆盖新状态。

## 阶段一：低风险防回归

阶段一不做大重构，目标是先让问题更早暴露，并把 `/goal` 当前特例整理成可复用机制。

### 1.1 新增统一工具可用性 API

新增文件：

```text
backend/cmd/aicli/commands/chat_tool_availability.go
```

建议 API：

```go
func chatToolAvailable(session *ChatSession, toolName string) bool
func chatSharedToolAvailable(session *ChatSession, toolName string) bool
func chatActorToolAvailable(session *ChatSession, toolName string) bool
func chatRuntimeServerToolAvailable(session *ChatSession, toolName string) bool
```

第一阶段语义必须严格限定：

- `chatToolAvailable` 表示“当前 executor 路径具备暴露该工具的能力”。
- 它不等价于“当前 turn 的 provider request 一定包含该工具”。
- 它不等价于“该工具用给定参数一定可执行成功”。
- 实际 provider request 是否包含工具，必须由 request metadata 或 fake provider request 测试兜底。

为了避免后续语义膨胀，阶段二以后应拆分为三个更明确的 API：

```go
func chatToolRegistered(session *ChatSession, toolName string) bool
func chatToolExposable(session *ChatSession, toolName string) bool
func chatToolExecutable(session *ChatSession, toolName string, args map[string]interface{}) error
```

其中：

- `Registered` 只表示能力存在于 registry 或 tool surface。
- `Exposable` 表示当前 executor、policy、DisableTools 和 path gating 允许暴露。
- `Executable` 表示一次具体调用可以通过 policy 和参数前置校验。

阶段一暂不强制拆分，但 `chatToolAvailable` 的注释必须写明它是 path-level exposure capability，不得在代码中当作具体调用授权使用。

行为：

- `session == nil` 返回 `false`
- `session.DisableTools == true` 返回 `false`
- shared path 查询 `FunctionCatalog.Registry()`
- actor-first 查询 `LocalRuntimeHost.ToolSurface.FindTool`
- runtime-server 第一阶段返回 `false`，后续再接远端 tool surface 查询

迁移：

- 将 `canCurrentChatPathUpdateGoal(session)` 内部改为调用：

```go
chatToolAvailable(session, updateGoalFunctionName)
```

后续系统提示、debug、测试都使用同一 API。

验收：

- `/goal` active guidance 在 shared path 中正确提示 `update_goal`
- actor-first 中只有真实 tool surface 暴露 `update_goal` 时才提示
- runtime-server 中不提示模型调用 `update_goal`

### 1.2 新增通用 post-turn reconciler

新增文件：

```text
backend/cmd/aicli/commands/chat_post_turn_reconcile.go
```

建议接口：

```go
type chatPostTurnReconciler interface {
    ReconcilePostTurn(session *ChatSession) error
}
```

或者先用函数列表：

```go
type chatPostTurnReconcileFunc func(session *ChatSession) error

func runPostTurnReconcilers(session *ChatSession) error
```

第一阶段注册：

```go
goalPostTurnReconciler
```

将当前：

```go
reconcileGoalCompletionFromToolMessages(session)
```

改造成 reconciler：

```go
func reconcileGoalCompletionFromToolMessages(session *ChatSession) error
```

actor executor 中替换为：

```go
warnIfChatSessionSyncFails(session, "actor post-turn reconcile", runPostTurnReconcilers(session))
```

shared executor 也必须在工具循环结束后调用同一 hook，保证行为一致。

shared executor 的插入点和持久化顺序必须明确：

- 插入位置应在 shared chat loop 完成、assistant/tool history 已同步回 `ChatSession` 后。
- 插入位置应在最终 response 返回给调用方之前。
- 如果 shared executor 已经调用过 `syncRuntimeSessionFromChat(session)`，reconciler 修改 metadata 后必须再次调用 `syncRuntimeSessionFromChat(session)`。
- reconciler 失败只输出 warning，不得让原本成功的模型响应失败。
- continuation path 和普通 `Execute` path 都必须调用同一 post-turn hook。

建议封装为：

```go
func runPostTurnReconcilersAndSync(session *ChatSession, label string) error
```

内部流程：

1. 调用 `runPostTurnReconcilers(session)`。
2. 如果 reconciler 有变更，调用 `syncRuntimeSessionFromChat(session)`。
3. 返回聚合错误，调用方用 `warnIfChatSessionSyncFails` 处理。

actor-first 的插入点：

- `syncRuntimeSessionBackIntoCLI(session)` 之后。
- `applyChatTokenUsage(session, result.Usage)` 之前或之后都可以，但必须在最终 `syncRuntimeSessionFromChat(session)` 前完成。
- 如果 reconciler 修改 metadata，必须保证后续保存不会再把旧 metadata 覆盖回来。

验收：

- actor-first 工具结果中的 `complete` 不会被旧 metadata 覆盖
- shared path 调用 reconciler 不破坏现有行为
- reconciler 无结果时无副作用

### 1.3 新增 tool surface parity 测试

新增文件：

```text
backend/cmd/aicli/commands/chat_tool_surface_parity_test.go
```

覆盖：

```go
TestChatToolAvailable_GoalToolSharedPath
TestChatToolAvailable_GoalToolActorPath
TestChatToolAvailable_GoalToolRuntimeServerUnsupported
TestChatToolAvailable_DisabledTools
```

断言：

- shared path `get_goal/update_goal` 可用
- actor-first path `get_goal/update_goal` 可用
- runtime-server path 当前不可用
- `DisableTools` 时都不可用

### 1.4 新增 provider request metadata 级测试

已有 `agent.loop` 会把 tool surface summary 放入 request metadata：

```text
tool_surface.names
tool_count
tools_sha256
```

新增 actor-first fake provider 测试，断言真实 provider request 中包含：

```text
get_goal
update_goal
```

建议放在：

```text
backend/cmd/aicli/commands/chat_actor_host_test.go
```

或新建：

```text
backend/cmd/aicli/commands/chat_actor_goal_tool_surface_test.go
```

验收标准：

- 不只检查 `ToolSurface.ListTools()`
- 必须检查 fake provider 收到的 request tools 或 metadata 中包含目标工具

### 1.5 明确 runtime-server unsupported 行为

当前 runtime-server 不支持 `update_goal`，应明确测试：

- active goal guidance 不提示 `call update_goal`
- 模型不能被系统提示误导为工具可用
- `/goal --json` 仍可由 slash command 管理本地 session metadata

建议测试文件：

```text
backend/cmd/aicli/commands/chat_runtime_server_goal_test.go
```

## 阶段二：以 /goal 为试点引入 CapabilityRegistry

阶段二目标是消除 `/goal` 双注册特例。

### 2.1 新增 capability 基础包

建议新增：

```text
backend/internal/aiclitools/capability.go
backend/internal/aiclitools/registry.go
```

核心类型：

```go
type ExposurePath string

const (
    ExposureShared        ExposurePath = "shared"
    ExposureActor         ExposurePath = "actor"
    ExposureRuntimeServer ExposurePath = "runtime_server"
)

type Capability struct {
    Name        string
    Description string
    Parameters  map[string]interface{}
    Metadata    map[string]interface{}
    Exposure    []ExposurePath
    Execute     func(ctx context.Context, session ToolSessionContext, args map[string]interface{}) (ToolResult, error)
}

type ToolResult struct {
    Output   interface{}
    Metadata map[string]interface{}
}
```

Capability 执行不得长期依赖闭包捕获 `*ChatSession`。闭包捕获当前 CLI session 可以作为阶段二迁移兼容手段，但不允许作为新增持久化状态工具的长期模式。原因是 actor-first 中 CLI session、actor session 和 storage session 可能不是同一个对象，闭包捕获会重新引入 stale session 覆盖风险。

应新增路径无关的 session 上下文接口：

```go
type ToolSessionContext interface {
    SessionID() string
    RuntimeSession() *runtimechat.Session
    SessionStorage() runtimechat.SessionStorage
    RefreshRuntimeSession(ctx context.Context, updated *runtimechat.Session) error
    ExecutorPath() ExposurePath
}
```

第一版实现可以位于 `backend/cmd/aicli/commands`，包装现有 `ChatSession`：

```go
type chatToolSessionContext struct {
    session *ChatSession
    path    aiclitools.ExposurePath
}
```

硬性规则：

- 读取当前 session 可使用 `RuntimeSession()`。
- 写入持久化 metadata 必须优先使用 `SessionStorage()` 加 storage-aware patch。
- 写入完成后必须调用 `RefreshRuntimeSession` 刷新 CLI 内存对象。
- Capability 不应直接调用 `syncRuntimeSessionFromChat`，同步由 adapter 或 executor hook 负责。
- 第一版不要过度抽象，只需满足 `/goal`。

### 2.2 新增 FunctionCatalog adapter

新增：

```text
backend/internal/aiclitools/function_adapter.go
```

提供：

```go
func FunctionFromCapability(cap Capability) functions.Function
```

shared path 继续看到普通 `functions.Function`。

adapter 负责把 shared executor 的 `ChatSession` 包装为 `ToolSessionContext` 后传入 capability。它不应把 capability 的 execute 闭包直接绑定到构造时的 session 指针，避免 resume 后拿到旧 session。

### 2.3 新增 MCPManager adapter

新增：

```text
backend/internal/aiclitools/mcp_adapter.go
```

提供：

```go
type CapabilityMCPManager struct {
    Registry *Registry
    Next     skill.MCPManager
}
```

实现：

```go
ListTools() []skill.ToolInfo
FindTool(name string) (skill.ToolInfo, error)
CallTool(ctx interface{}, mcpName, toolName string, args map[string]interface{}) (interface{}, error)
```

替代当前 `/goal` 专用：

```go
goalActorToolSurface
wrapGoalToolSurface
```

MCP adapter 也必须在每次 `CallTool` 时解析当前 session context，而不是只在 adapter 构造时捕获可能过期的 session。第一版可以通过 `chatToolSessionContext` 包装 CLI `ChatSession`，阶段三接入 patch API 后再减少对 CLI session 指针的依赖。

### 2.4 迁移 /goal 工具定义

新增或调整：

```text
backend/cmd/aicli/commands/goal_capability.go
```

将 `get_goal/update_goal` 的 schema 和执行逻辑变成路径无关能力：

```go
func goalCapabilities(session *ChatSession) []aiclitools.Capability
```

然后：

- shared path 从 capability 生成 function
- actor-first 从 capability 生成 MCP tool
- 旧 `goalFunction` 保留为兼容薄 adapter，最终可删除

### 2.5 验收

- 删除或弱化 `goalActorToolSurface` 后，mimo 真实测试仍通过
- shared path goal 工具测试仍通过
- actor-first request metadata 仍包含 `get_goal/update_goal`
- `go test ./cmd/aicli/commands` 通过
- `go build ./cmd/aicli` 通过

## 阶段三：session metadata patch API

阶段三解决状态覆盖根因。

### 3.1 新增 patch API

建议新增：

```text
backend/internal/sessionmeta/patch.go
backend/internal/sessionmeta/patch_test.go
```

接口：

```go
type ContextPatch struct {
    SessionID     string
    SetContext    map[string]interface{}
    DeleteContext []string
}

func ApplyContextPatch(ctx context.Context, storage runtimechat.SessionStorage, patch ContextPatch) (*runtimechat.Session, error)
```

行为：

1. 从 storage 加载最新 session。
2. 确保 `Metadata.Context` 初始化。
3. 删除 `DeleteContext` 指定 key。
4. 写入 `SetContext` 指定 key。
5. 保存最新 session。
6. 返回保存后的 session clone。

硬性规则：

- `ApplyContextPatch` 不得保存调用方传入的整份旧 session。
- `ApplyContextPatch` 必须保留最新 session 的 History、Title、State、Metadata 中非目标 key。
- 如果 storage 暂不支持 CAS/version，第一版至少必须做到 key-level merge。
- patch 保存后返回的 session 必须作为后续 CLI 内存状态刷新来源。

为后续并发控制预留字段：

```go
type ContextPatch struct {
    SessionID         string
    SetContext        map[string]interface{}
    DeleteContext     []string
    ExpectedUpdatedAt string
    MergePolicies     map[string]MergePolicy
}
```

第一版可以不强制使用 `ExpectedUpdatedAt`，但字段和测试应预留。后续如果 file storage 或 SQLite storage 增加 session version/CAS，可直接接入。

### 3.2 支持 key-level merge policy

第一阶段 patch 可直接 replace。第二步为关键 metadata 加策略：

```go
type MergePolicy interface {
    MergeContextValue(key string, oldValue, newValue interface{}) (interface{}, error)
}
```

`aicli.goal` 建议策略：

- `complete` 不允许被旧 `active` 覆盖
- `budget_limited` 不允许被旧 `active` 覆盖，除非用户明确 resume
- 用户命令优先于模型工具，但必须走合法状态转换

Goal merge policy 必须携带 mutation actor，否则无法判断用户命令、模型工具和系统预算更新的优先级。

```go
type GoalMutationActor string

const (
    GoalMutationUser   GoalMutationActor = "user"
    GoalMutationModel  GoalMutationActor = "model"
    GoalMutationSystem GoalMutationActor = "system"
)
```

建议状态转换矩阵：

| From | To | Actor | 允许条件 |
| --- | --- | --- | --- |
| none | active | user | `/goal <objective>` 新建 |
| active | active | user | `/goal <objective>` 替换目标，生成新 goal id 或明确更新 |
| active | paused | user | `/goal pause` |
| active | budget_limited | system | token/time budget 达到限制 |
| active | complete | user | `/goal complete` |
| active | complete | model | `update_goal` 成功，且工具真实可用 |
| paused | active | user | `/goal resume` |
| paused | complete | user | `/goal complete` |
| paused | complete | model | 不允许，paused goal 不应由模型自动完成 |
| budget_limited | active | user | `/goal resume` |
| budget_limited | complete | user | `/goal complete` |
| budget_limited | complete | model | 允许，但必须经 `update_goal` 工具且保留 budget report |
| complete | active | any | 不允许，必须 clear 后新建 |
| complete | paused | any | 不允许 |
| complete | budget_limited | any | 不允许 |
| any | cleared | user | `/goal clear` |

防回退规则：

- 已持久化的 `complete` 不能被旧 `active/paused/budget_limited` 覆盖。
- 已持久化的 `budget_limited` 不能被旧 `active` 覆盖。
- 同一 goal id 上，较新的合法状态优先。
- 不同 goal id 上，必须以用户显式创建/替换为准，模型不得创建新 goal。

### 3.3 改造 runtimegoal.MetadataStore

当前：

```go
store.Put(session.RuntimeSession, goal)
```

长期应提供 storage-aware API：

```go
func (s MetadataStore) PutPersistent(ctx context.Context, storage runtimechat.SessionStorage, sessionID string, goal SessionGoal) (*runtimechat.Session, error)
func (s MetadataStore) DeletePersistent(ctx context.Context, storage runtimechat.SessionStorage, sessionID string) (*runtimechat.Session, error)
```

CLI 使用后刷新内存：

```go
updated, err := store.PutPersistent(ctx, storage, sessionID, goal)
restoreChatStateFromRuntimeSession(session, updated)
```

这样工具调用不再只修改某个可能过期的 session 指针。

### 3.4 验收

新增测试：

```text
TestApplyContextPatch_PreservesUnrelatedContext
TestApplyContextPatch_UsesLatestStoredSession
TestGoalMetadataPatch_DoesNotRegressCompleteToActive
TestGoalToolCompletionSurvivesActorSessionSave
```

真实验证：

- mimo 调用 `update_goal`
- `/goal --json` 最终仍为 `complete`
- session JSON 中 `aicli.goal.status` 不会回退

## 阶段四：runtime-server 对齐

阶段四目标是让 runtime-server 不再成为第三套工具面。

### 4.1 增加远端 tool surface 查询

阶段四开始前必须先审查现有 runtime-server API，避免新增 endpoint 与现有风格冲突。前置分析包括：

- `runtime command submit` 响应是否已有 tools 或 run metadata。
- runtime state 是否已有 `FrozenTurnTools` 或等价 tool surface。
- runtime events 是否已经携带 `tool_surface` metadata。
- 现有 state/query endpoint 是否可扩展返回 tool surface。
- 远端 runtime 是否可以直接消费本地 capability registry，还是必须由服务端独立构建 tool surface。

如果现有 API 无法稳定表达工具面，再新增 API：

```text
GET /runtime/sessions/{session_id}/tools
```

返回：

```json
{
  "tools": [
    {
      "name": "update_goal",
      "description": "...",
      "parameters": {},
      "metadata": {}
    }
  ]
}
```

CLI runtime-server executor 缓存后：

```go
chatRuntimeServerToolAvailable(session, toolName)
```

即可真实判断远端是否支持。

### 4.2 增加远端 capability contract

runtime-server 可直接消费同一 capability registry，或通过远端 runtime 自身生成 tool surface。

关键要求：

- tool name 一致
- schema 一致
- metadata.source 一致或可解释
- unsupported 时显式返回不支持，而不是静默缺失

### 4.3 runtime-server 支持 /goal

当 runtime-server 已能暴露和执行 `update_goal` 后：

- `canCurrentChatPathUpdateGoal` / `chatToolAvailable` 不再对 runtime-server 固定返回 false
- active goal guidance 可提示 `update_goal`
- 增加远端工具调用后 `/goal --json` 为 `complete` 的测试

## 测试策略

### 单元测试

必须覆盖：

- capability registry 注册和查找
- capability 到 function schema 转换
- capability 到 MCP tool info 转换
- `chatToolAvailable` 在三类 executor 下的行为
- post-turn reconciler 的幂等性
- metadata patch 的 key-level 更新

### 集成测试

必须覆盖：

- shared executor 可见并执行 `update_goal`
- actor-first provider request 包含 `update_goal`
- actor-first 工具调用后最终 session 持久化为 `complete`
- runtime-server unsupported 时不会提示模型调用 `update_goal`

### 负向测试

必须覆盖：

- 没有 active goal 时，模型调用 `update_goal` 应失败且不写 metadata。
- `update_goal` 传入 `status=paused` 应失败。
- `update_goal` 缺少 `summary` 或 summary 为空应失败。
- `DisableTools=true` 时 provider request 不包含 `get_goal/update_goal`。
- tool policy 禁止 `update_goal` 时，系统提示不应说可调用，实际调用也应失败。
- runtime-server unsupported 时，即使 prompt 要求调用 `update_goal`，最终 session 也不能被标记为 complete。
- paused goal 不应被模型通过 `update_goal` 自动完成。
- 已 complete goal 不应被旧 actor/session context 覆盖回 active。

### Schema parity 测试

Capability 引入后必须增加 schema parity 测试：

- shared function schema 与 actor MCP tool schema 的 `name`、`description`、`parameters` 一致。
- `metadata.source` 可解释且符合路径预期。
- required fields、enum、additionalProperties 不因 adapter 不同而漂移。

### 真实外部 LLM 测试

继续使用 mimo provider 作为真实工具调用验证：

```powershell
.\aicli.exe chat --provider mimo_anthropic --model mimo-v2.5-pro --no-interactive --session-dir $dir --user mimo-goal -M "/goal Mark this goal complete when the user asks you to do so, using update_goal."

.\aicli.exe chat --provider mimo_anthropic --model mimo-v2.5-pro --no-interactive --session-dir $dir --user mimo-goal --resume --debug-http --request-timeout 180s -M "Please complete the persistent goal now. You must call update_goal with status complete and summary 'mimo live test completed'. After the tool call, reply with exactly LIVE_GOAL_DONE."

.\aicli.exe chat --provider mimo_anthropic --model mimo-v2.5-pro --no-interactive --session-dir $dir --user mimo-goal --resume -M "/goal --json"
```

验收标准不是模型文本，而是：

```json
"status": "complete",
"completed_by": "model",
"completion_summary": "mimo live test completed"
```

同时检查 debug artifact：

```text
tool_surface.names contains update_goal
response contains tool_use name=update_goal
```

### 可观测性要求

每条 executor 路径的 debug metadata 应尽量稳定暴露以下字段：

```text
executor_path: shared | actor | runtime_server
tool_surface.names
tool_surface.sources
tool_surface.count
tools_sha256
post_turn_reconciler.names
post_turn_reconciler.errors
```

阶段一至少要求 actor-first 和 shared path 的测试能定位当前 executor path，并能从 request metadata 或 debug reporter 中确认 `tool_surface.names`。post-turn reconciler 的错误必须进入 debug log 或 warning 输出，不能静默吞掉。

### CI 建议

短期 CI 至少运行：

```powershell
go test ./cmd/aicli/commands
go test ./internal/goal
go test ./internal/chat
go test ./internal/toolkit/tools
go build ./cmd/aicli
```

`go test ./...` 当前存在既有无关失败：

```text
internal/api/skills TestAgentChat_ReActReturnsStructuredFailureWhenMaxStepsIsReached
expected usage >= 1, got 0
```

该失败不应阻塞本方案阶段一，但应在独立任务中修复。

## 新增工具能力的开发规则

后续新增任何模型可调用工具，必须满足以下 checklist：

- 工具有单一 capability 定义。
- shared path 可见。
- actor-first path 可见。
- runtime-server path 要么支持，要么显式 unsupported。
- 系统提示只在工具真实可用时提到工具。
- debug metadata 的 `tool_surface.names` 包含预期工具。
- 工具调用后最终 session storage 状态正确。
- 禁用 tools 时不暴露。
- tool policy 禁止时不暴露或不可执行。
- resume 后工具状态仍正确。

## 具体实施任务清单

### 阶段一任务

- [x] 新增 `chat_tool_availability.go`。
- [x] 将 `/goal` guidance 判断改为调用 `chatToolAvailable`。
- [x] 在 `chatToolAvailable` 注释中明确它只表示 path-level exposure capability。
- [x] 新增 `chat_post_turn_reconcile.go`。
- [x] 将 `reconcileGoalCompletionFromToolMessages` 改为通用 reconciler。
- [x] actor executor 和 shared executor 在 turn 结束时调用 `runPostTurnReconcilersAndSync` 或等价封装。
- [x] 明确 shared executor 普通执行和 goal continuation 的 post-turn 插入点。
- [x] 新增 `chat_tool_surface_parity_test.go`。
- [x] 新增 actor-first request metadata 工具面测试。
- [x] 新增 runtime-server `/goal update_goal` unsupported 测试。
- [x] 新增负向测试：无 active goal、非法 status、空 summary、DisableTools、tool policy 禁止。
- [x] 新增 debug metadata 可观测性断言，至少覆盖 executor path 和 tool surface names。
- [x] 运行 `go test ./cmd/aicli/commands`。
- [x] 运行 `go build ./cmd/aicli`。
- [x] 使用 mimo provider 复测 `/goal` 完成链路。

### 阶段二任务

- [x] 新增 `backend/internal/aiclitools/capability.go`。
- [x] 新增 `backend/internal/aiclitools/registry.go`。
- [x] 新增 `ToolSessionContext`，禁止新增持久化工具长期闭包捕获 `ChatSession`。
- [x] 新增 capability-to-function adapter。
- [x] 新增 capability-to-MCP adapter。
- [x] 新增 shared/actor schema parity 测试。
- [x] 将 `/goal` 工具定义迁移为 capability。
- [x] shared path 从 capability 注册 `goalFunction`。
- [x] actor-first path 用通用 MCP adapter 替换 `goalActorToolSurface`。
- [x] 删除或保留为兼容薄层的 `goalActorToolSurface`。
- [x] 跑完整 commands 测试和 mimo 实测。

### 阶段三任务

- [x] 新增 `sessionmeta.ContextPatch`。
- [x] 新增 `ApplyContextPatch`。
- [x] 为 `ContextPatch` 预留 `ExpectedUpdatedAt` 和 `MergePolicies`。
- [x] 为 `aicli.goal` 增加状态防回退 merge policy。
- [x] 为 `aicli.goal` 增加 mutation actor 和状态转换矩阵测试。
- [x] 改造 `runtimegoal.MetadataStore`，提供 persistent patch API。
- [x] `/goal` 命令和 `update_goal` 工具使用 persistent patch API。
- [x] 减少 goal-specific reconcile 对最终正确性的依赖。
- [x] 增加 actor stale context 覆盖回归测试。

### 阶段四任务

- [x] 审查现有 runtime-server command/state/event API 是否已能表达 tool surface。
- [x] runtime-server 暴露 session tool surface 查询。
- [x] CLI runtime-server executor 使用远端 tool surface 判断工具可用性。
- [x] runtime-server 支持 `get_goal/update_goal` capability。
- [x] active goal guidance 在 runtime-server 支持后自动提示 `update_goal`。
- [x] 增加 runtime-server 真实工具完成测试。
- [x] actor-first/runtime-server 自动 completion audit 通过隐藏 continuation 触发，且隐藏审计提示不持久化为用户消息。

## 风险与控制

### 风险：大重构影响现有 chat 行为

控制：

- 阶段一只加 API 和测试，不大改工具执行路径。
- 阶段二只迁移 `/goal` 一个试点。
- 保留旧 adapter，确认稳定后再删除特例。

### 风险：CapabilityRegistry 过度抽象

控制：

- 第一版只满足 session-bound CLI tools。
- 不强行迁移 `ToolBroker` 的 team/agent 工具。
- schema 和 execute 保持简单结构。

### 风险：metadata patch 与现有 sync 逻辑冲突

控制：

- patch API 先只用于 `aicli.goal`。
- 保留 `syncRuntimeSessionFromChat`。
- 增加 stale object 覆盖测试。

### 风险：runtime-server 过早对齐成本高

控制：

- 阶段一明确 unsupported。
- 阶段四再接远端 tool surface。
- 不阻塞 actor-first 和 shared path 收敛。

## 回滚策略

阶段一：

- `chatToolAvailable` 和 parity tests 只增加判断和测试，不应改变工具执行行为。
- 如果 post-turn reconciler 引起回归，可临时只在 actor-first 路径启用，但必须保留 shared path 测试为 pending/failing task，不得删除。

阶段二：

- `/goal` 迁移到 capability 时，保留现有 `goalFunction` 和 `goalActorToolSurface` 兼容层。
- 新增 adapter 应可通过小范围开关回退，例如保留 `wrapGoalToolSurface` 调用路径直到 mimo 实测通过。
- 如果 `CapabilityMCPManager` 出现问题，actor-first 可以临时回退到 `goalActorToolSurface`。

阶段三：

- patch API 初期只用于 `aicli.goal`，不替换全局 `syncRuntimeSessionFromChat`。
- 如果 patch API 出现问题，可回退到当前 `MetadataStore.Put` + post-turn reconciler 组合。
- 回退期间必须保留 stale context 覆盖测试，标记为待恢复，不得删除。

阶段四：

- runtime-server tool surface 查询应是增量能力。
- 如果远端查询失败，CLI 应按 unsupported 处理，而不是假设工具可用。

## 临时实现生命周期

### `goalActorToolSurface`

删除或弱化条件：

- 通用 `CapabilityMCPManager` 已覆盖 `get_goal/update_goal`。
- actor-first provider request metadata 测试确认包含 `get_goal/update_goal`。
- shared/actor schema parity 测试通过。
- mimo provider 真实测试通过。
- `DisableTools` 和 tool policy 负向测试通过。

在上述条件满足前，`goalActorToolSurface` 可作为兼容 fallback 保留。

### `reconcileGoalCompletionFromToolMessages`

弱化条件：

- `runtimegoal.MetadataStore` 已使用 persistent patch API。
- `aicli.goal` merge policy 已阻止 `complete -> active` 回退。
- actor stale session context 覆盖测试通过。
- 删除或禁用 goal-specific reconciler 后，mimo provider 真实测试仍通过。

在上述条件满足前，goal-specific reconciler 必须保留，作为 actor-first stale metadata 的兜底防线。

## 验收标准

阶段一完成后：

- `chatToolAvailable` 成为 `/goal` guidance 的唯一工具可用性判断入口。
- `chatToolAvailable` 的语义被明确限定为 path-level exposure capability。
- actor-first 和 shared path 都有 `/goal` 工具可见性测试。
- post-turn reconciler 统一调用。
- shared executor 普通执行和 continuation 执行都覆盖 post-turn reconciler。
- 负向测试覆盖非法工具调用和 disabled/policy 场景。
- debug metadata 能定位 executor path 和 tool surface names。
- mimo provider 真实测试通过。

阶段二完成后：

- `/goal` 工具只在 capability 层声明一次。
- Capability 执行使用 `ToolSessionContext`，不依赖长期闭包捕获 `ChatSession`。
- shared path 和 actor-first path 都通过 adapter 获取 `get_goal/update_goal`。
- shared/actor schema parity 测试通过。
- `goalActorToolSurface` 不再是必要特例。

阶段三完成后：

- `aicli.goal` 写入走 storage-aware patch。
- actor stale session context 不能把 `complete` 覆盖回 `active`。
- `aicli.goal` 状态转换矩阵和 mutation actor 测试通过。
- goal-specific reconciler 只作为兜底，不是 correctness 的唯一保障。

阶段四完成后：

- runtime-server 可以查询真实 tool surface。
- runtime-server 支持或显式拒绝每个 capability。
- `/goal update_goal` 在 runtime-server 支持后与 actor-first 行为一致。

## 推荐优先级

最高优先级：

1. `chatToolAvailable`
2. tool surface parity tests
3. post-turn reconciler 通用化
4. actor-first request metadata 测试

第二优先级：

1. `/goal` capability 试点
2. generic capability-to-MCP adapter
3. generic capability-to-function adapter

第三优先级：

1. session metadata patch API
2. goal 状态防回退 merge policy
3. runtime-server tool surface contract

这个顺序可以先防止继续出问题，再逐步消除根因，避免一次性重构造成过大风险。
