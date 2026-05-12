# runtime-server 与 aicli 共用会话机制改进方案

日期：2026-05-11

## 背景

本仓库目前有两个主要入口：

- `backend/cmd/runtime-server`
- `backend/cmd/aicli`

两者都复用了 `internal/chat`、`internal/bootstrap`、`internal/api/skills`、`internal/team`、`internal/agentcontrol` 等后端能力，但运行后生成的会话数据看起来并不完全一致。根因不是底层完全割裂，而是“会话历史”“运行态事件”“协作状态”“用户身份”“元数据键”和“执行宿主”分别由两个入口以不同默认值拼装。

本文目标是把两个入口收敛到同一套会话机制：同一份会话历史模型、同一套持久化路径解析、同一套运行态存储、同一套 artifact/checkpoint/background 数据面、同一套会话元数据规范，并且为 `aicli` 与 `runtime-server` 同时操作同一会话提供可控的进程所有权规则。

## 现状结论

### 已经共用的部分

两边的核心持久化会话模型已经是同一个：

- `backend/internal/chat/session.go`
  - `chat.Session`
  - `chat.SessionMetadata`
  - `History []types.Message`
- `backend/internal/chat/storage.go`
  - `SessionStorage`
  - `SessionManager`
- `backend/internal/chat/file_storage.go`
  - JSON 文件会话存储

也就是说，当前两个入口都可以读写同一种会话 JSON 文件格式。典型字段包括：

- `id`
- `userId`
- `state`
- `history`
- `metadata`
- `createdAt`
- `updatedAt`
- `expiresAt`

### runtime-server 的会话路径

`runtime-server` 启动路径在 `backend/cmd/runtime-server/main.go`：

- `newRuntimeServerApp` 加载 runtime config。
- 若 `runtimeConfig.Sessions.Dir` 为空，会通过 `resolveRuntimeServerSessionDir` 落到 `aiclipaths.DefaultSessionsDir()`。
- `bootstrap.NewManager` 再通过 `newSessionManager` 创建 `chat.NewFileStorage(dir)`。

因此 runtime-server 默认也会使用 `~/.aicli/sessions` 作为会话历史目录。

但 runtime-server 的运行态存储默认不是同一套文件：

- `sessionRuntime.storePath` 为空时，`Handler.refreshSessionRuntimeStore` 使用 `chat.NewInMemoryRuntimeStore(2048)`。
- `team.storePath` 为空时，`bootstrap.NewManager` 创建的 `team.NewSQLiteStore` 会使用内存 SQLite。
- `agentControl.*` 为空时，全局 AgentControl registry 也不会稳定落盘。
- `artifact` store 未配置时，checkpoint、tool artifact、context ledger 使用内存 SQLite。
- `background.storePath` 为空时，background manager 没有稳定 store，后台任务事件和输出恢复能力受限。

这会导致：会话历史文件可能在同一个目录，但 runtime events、pending approval、tool receipts、team mailbox、AgentControl registry 等运行态数据在 runtime-server 进程内存中，重启后消失，也不会与 `aicli` 本地 actor runtime 的 SQLite 文件一致。

### aicli 的会话路径

`aicli chat` 路径在 `backend/cmd/aicli/commands/chat_bootstrap.go` 和 `chat_session.go`：

- `prepareChatPersistence` 调用 `newChatSessionManager(opts.SessionDirFlag)`。
- `newChatSessionManager` 默认使用 `aiclipaths.DefaultSessionsDir()`，即 `~/.aicli/sessions`。
- 会话管理配置被本地覆盖为：
  - `MaxHistory = 200`
  - `CleanupInterval = 6h`
  - `IdleTimeout = 72h`

`aicli` 的 actor-first 本地运行态在 `backend/cmd/aicli/commands/chat_actor_host.go`：

- `buildLocalChatRuntimeStores` 固定使用 `<sessionDir>/runtime/chat_runtime.sqlite`。
- `applyLocalChatRuntimePersistenceDefaults` 会补齐：
  - `<sessionDir>/runtime/team_store.sqlite`
  - `<sessionDir>/runtime/agent_control.sqlite`

因此 `aicli` 默认更偏向“本地持久化完整运行态”，而 runtime-server 默认只持久化会话历史，运行态更偏内存。

### 数据看起来不同的直接原因

| 维度 | runtime-server | aicli | 影响 |
| --- | --- | --- | --- |
| 配置搜索 | `$HOME/.aicli/config.yaml -> ./.aicli/config.yaml -> ./config.yaml -> ./configs/config.yaml` | `$HOME/.aicli/config.yaml -> ./.aicli/config.yaml -> ./aicli.yaml -> ./configs/config.yaml` | 在项目根目录有 `aicli.yaml` 或 `config.yaml` 时，两边可能加载不同配置。 |
| 会话历史目录 | 默认 `~/.aicli/sessions`，可由 `runtime.sessions.dir` 覆盖 | 默认 `~/.aicli/sessions`，可由 `--session-dir` 覆盖 | 默认可能一致，但覆盖逻辑不同。 |
| 默认用户 | API 默认 `anonymous` 或 usage scope user | OS 用户名，或 `AICLI_SESSION_USER` 等环境变量 | 同目录下按 `userId` 列表过滤，互相看不到很常见。 |
| 会话元数据 | 写 `profile_reference/profile_name/profile_agent/profile_root` 等 API profile 键 | 写 `aicli_provider_name/aicli_protocol/aicli_model/aicli_stream/...` | 同一 JSON 格式下元数据语义不统一。 |
| instruction/system message | `ensureSessionInstructionMessages` 维护 system/developer instruction 前缀 | `ensureChatSystemPromptMessage` 与 profile/system prompt 逻辑在 CLI 内 | history 前缀可能不同。 |
| runtime state/events | 默认内存，配置后才 SQLite | 默认 `<sessionDir>/runtime/chat_runtime.sqlite` | agent 状态、事件、审批、tool receipt 不共享。 |
| team/AgentControl | 默认内存或未开启稳定全局 store | 默认 `<sessionDir>/runtime/*.sqlite` | 多 agent/team 数据不共享。 |
| artifact/checkpoint | 未显式配置时偏内存 | 跟本地 agent 配置和日志目录相关 | checkpoint、tool artifact refs、context ledger 不共享。 |
| background jobs | 未显式配置时没有稳定 store/log dir | 跟本地 runtime config 或默认 manager 相关 | `background_task` 列表、事件、输出恢复不共享。 |
| 本地日志/文件 artifact | runtime-server 只通过会话 metadata 找文件 | `~/.aicli/chat-logs`、`*.artifacts` 等本地目录 | 调试日志、HTTP artifact、生成图片文件可见性不一致。 |
| 执行宿主 | runtime-server HTTP handler/session hub | aicli 本地 session hub | 两个进程可同时写同一 session，缺少 ownership/lease。 |

## 目标

1. 两个入口默认解析到同一套会话历史目录。
2. 目标状态下，两个入口默认解析到同一套 runtime state/event/team/AgentControl/artifact/checkpoint/background SQLite 存储。
3. 两个入口写入相同的会话元数据规范，同时兼容旧键读取。
4. `aicli --list-sessions`、`/sessions` 与 runtime-server `/api/runtime/sessions` 在相同用户身份下看到同一批会话。
5. 任何同一 `session_id` 的 actor 执行都要有明确宿主，避免两个进程并发驱动同一会话。
6. 不破坏现有历史会话 JSON 文件；迁移应以兼容读取和增量补写为主。
7. 明确区分“核心共享会话状态”和“CLI 本地诊断日志”：前者进入统一 store，后者如不共享必须在诊断中标明。

## 非目标

- 不在第一阶段把会话历史从 JSON 文件改为 SQLite。当前 `chat.Session` JSON 文件已经是两边共同模型，先统一机制和运行态即可。
- 不删除 `aicli` 本地离线运行能力。即使引入 runtime-server 作为共享服务，`aicli` 仍应能在无 server 时回退到本地模式。
- 不一次性重写 `AgentChat`、`ChatSession` UI、adapter 协议转换链路。
- 不把 `~/.aicli/chat-logs` 下的调试日志强制迁入会话主存储。调试日志可以继续作为 CLI 本地归档，但必须与 session artifact/checkpoint 的共享存储边界区分清楚。

## 推荐架构

推荐采用“两层统一”：

1. **本地会话服务统一层**
   - 新增统一工厂，集中创建 `SessionManager`、`RuntimeStateStore`、`EventStore`、`TeamStore`、`AgentControl` store。
   - `runtime-server` 和 `aicli` 都调用同一个工厂，而不是各自拼默认路径。

2. **可选 runtime-server 执行宿主**
   - `aicli` 保留本地执行模式。
   - 增加 `server` 执行模式：当 runtime-server 可用时，`aicli` 通过 HTTP API 使用同一 session hub 执行 turn、spawn agent、wait agent、读 events。
   - 本地模式用于离线和兼容；server 模式用于真正跨进程共享会话运行态。

仅共享 JSON 文件和 SQLite 文件还不够。会话执行是有状态 actor：pending approval、streaming delta、tool receipt、mailbox wakeup 都跟进程内 `SessionHub` 有关。如果 `runtime-server` 和 `aicli` 同时对同一个 `session_id` 启动 actor，就算底层 SQLite 是同一个，也会出现并发写 history、事件顺序交错、审批状态互相覆盖的问题。因此最终应支持“单宿主执行”，至少要通过 lease 防止同一会话被两个 actor 同时驱动。

## 代码改造方案

### 1. 新增统一会话服务工厂

建议新增包：

```text
backend/internal/sessionruntime
```

核心类型：

```go
type Services struct {
    SessionManager *chat.SessionManager
    SessionStore   chat.SessionStorage
    RuntimeStore   chat.RuntimeStateStore
    EventStore     chat.EventStore
    ReceiptStore   chat.ToolReceiptStore
    TeamStore      team.Store
    AgentControl   *agentcontrol.RegistryService
    ArtifactStore  *artifact.Store
    Checkpoints    *checkpoint.Manager
    Background     *background.Manager
    LeaseStore     SessionLeaseStore
    Paths          ResolvedPaths
    Close          func() error
}

type Options struct {
    AgentConfig       *agentconfig.Config
    RuntimeConfig     *runtimecfg.RuntimeConfig
    RuntimeConfigFile string
    SessionDir        string
    UserID            string
    Mode              string // "server" | "cli-local" | "test"
}
```

统一路径规则：

| 资源 | 默认路径 |
| --- | --- |
| 会话历史 JSON | `~/.aicli/sessions` |
| session runtime SQLite | `<sessions.dir>/runtime/session_runtime.sqlite` |
| team SQLite | `<sessions.dir>/runtime/team_store.sqlite` |
| AgentControl registry/mailbox/agents SQLite | `<sessions.dir>/runtime/agent_control.sqlite` |
| AgentControl mailbox/agents 拆分覆盖 | 仅显式配置时使用 `agent_control_mailbox.sqlite` / `agent_control_agents.sqlite` 等独立路径 |
| artifact/checkpoint SQLite | `<sessions.dir>/runtime/artifacts.sqlite` |
| background jobs SQLite | `<sessions.dir>/runtime/background.sqlite` |
| background logs | `<sessions.dir>/runtime/background_logs` |
| CLI 调试日志 | `~/.aicli/chat-logs`，保持本地归档，不作为核心共享状态 |

实现要点：

- `sessions.dir` 为空时统一使用 `aiclipaths.DefaultSessionsDir()`。
- 相对路径按 runtime config 文件所在目录解析；没有 runtime config 文件时按当前工作目录解析。
- 支持 `~` 展开，复用 `aiclipaths.ExpandUserPath`。
- `runtimeConfig.SessionRuntime.StorePath` 为空时按 rollout 策略补默认 SQLite，不再长期保持 runtime-server 默认内存、aicli 默认文件的分叉。
- `runtimeConfig.Team.StorePath` 和 `runtimeConfig.AgentControl.*` 为空时同样补默认 SQLite。
- `artifact.StoreConfig.Path` 为空时补 `<sessions.dir>/runtime/artifacts.sqlite`，并注入 agent、context manager、output gateway、checkpoint manager。
- `runtimeConfig.Background.StorePath` 为空时补 `<sessions.dir>/runtime/background.sqlite`；`LogDir` 为空时补 `<sessions.dir>/runtime/background_logs`。
- `CLI` 本地 debug log、runtime-http artifact、local-shell artifact 不默认迁入 artifact store，但诊断中必须单独显示，避免和共享 session artifact 混淆。
- `SessionManagerConfig` 不要由两个入口各自硬编码。新增 `RuntimeConfig.Sessions` 字段时可以兼容旧配置：
  - `maxHistory`
  - `ttl`
  - `cleanupInterval`
  - `idleTimeout`
  - `autoArchive`

接入点：

- `backend/cmd/runtime-server/main.go`
  - `newRuntimeServerApp` 调用统一工厂。
  - `bootstrap.NewManager` 不再自己创建 session manager，或者允许通过 `Options.SessionServices` 注入。
- `backend/internal/bootstrap/manager.go`
  - `newSessionManager` 改为调用统一工厂中的 session manager 创建逻辑，或废弃为兼容包装。
- `backend/cmd/aicli/commands/chat_session.go`
  - `newChatSessionManager` 改为调用统一工厂。
- `backend/cmd/aicli/commands/chat_actor_host.go`
  - `buildLocalChatRuntimeStores`、`applyLocalChatRuntimePersistenceDefaults` 改为使用统一工厂返回的 runtime/team/agentControl/artifact/background store。
- `backend/internal/agent/agent.go`
  - `newArtifactStore` 支持外部注入或读取统一工厂解析后的 artifact store path，不再在未配置时默默落到内存 store。
- `backend/internal/api/skills/handler.go`
  - background manager、checkpoint manager、agent factory 统一使用 `Services` 中的 store 和路径。

### 2. 统一会话用户身份

当前 `aicli` 通过 `resolveChatSessionUserID` 使用 OS 用户名，runtime-server 的 session API 默认 `anonymous`。这会导致会话物理上在同一目录，但列表查询按 `userId` 过滤后互相不可见。

建议新增统一解析器：

```go
type IdentityResolver interface {
    ResolveSessionUserID(ctx context.Context, source RequestSource) string
}
```

统一优先级：

1. HTTP 请求显式 `user_id`。
2. 已认证请求的 usage scope / tenant scope / `X-Skills-User` 等既有来源。
3. CLI 显式 `--user`。
4. `AICLI_SESSION_USER`。
5. 统一 runtime config 的 `sessions.defaultUserId`。
6. CLI 本地模式 fallback 到 OS 用户。
7. runtime-server HTTP fallback 保持 `anonymous`。

本地开发可以通过统一 runtime config 固定同一个默认用户，例如：

```yaml
sessions:
  defaultUserId: local
```

配置归属：

- `sessions.defaultUserId` 放在 `internal/config.RuntimeConfig.Sessions`，而不是只放在 aicli 私有配置中。
- `aicli` 加载到 agent config 后，如果存在 `skills_runtime.config_file`，应以该 runtime config 为最终来源。
- `aicli` server 模式调用 runtime-server 时必须把解析后的 `user_id` 显式传给 HTTP API，不能依赖 server fallback。
- runtime-server 的多租户或认证场景中，认证身份优先级高于 `sessions.defaultUserId`，避免配置默认值覆盖请求身份。

兼容策略：

- runtime-server 保留 `anonymous` 作为未配置 HTTP 默认值，避免多租户场景突然泄露本机 OS 用户。
- `aicli` 调用 runtime-server API 时必须显式传 `user_id=session.SessionUserID`。
- 文档和 CLI 输出明确显示 `session_id` 与 `user_id`，避免误判“会话丢失”。

### 3. 统一会话元数据键

建议把会话执行上下文从 `cmd/aicli/commands` 和 `internal/api/skills` 的私有键迁移到公共包，例如：

```text
backend/internal/chat/sessionmeta
```

公共键建议：

```text
provider_name
provider_protocol
model
reasoning_effort
stream
disable_tools
profile_ref
profile_name
profile_agent
profile_root
workspace_path
client
entrypoint
message_count
token_count
context_token_count
context_window_token_count
```

新增 helper：

```go
type ExecutionContext struct {
    ProviderName       string
    ProviderProtocol   string
    Model              string
    ReasoningEffort    string
    Stream             *bool
    DisableTools       *bool
    ProfileRef         string
    ProfileName        string
    ProfileAgent       string
    ProfileRoot        string
    WorkspacePath      string
    Client             string
    Entrypoint         string
}

func ApplyExecutionContext(session *chat.Session, ctx ExecutionContext) bool
func ReadExecutionContext(session *chat.Session) ExecutionContext
```

兼容读取：

- `aicli_provider_name` -> `provider_name`
- `aicli_protocol` -> `provider_protocol`
- `aicli_model` -> `model`
- `aicli_reasoning_effort` -> `reasoning_effort`
- `aicli_stream` -> `stream`
- `profile_reference` -> `profile_ref`

写入策略：

- 新版本只写公共键。
- 读取时兼容旧键。
- 可选提供一次性迁移命令或后台补写，但不要求用户立即迁移旧 session JSON。

### 4. 统一 instruction/system message 管理

runtime-server 当前通过 `internal/api/skills/instruction_messages.go` 的 `ensureSessionInstructionMessages` 维护 system/developer instruction 前缀；`aicli` 当前通过 `ensureChatSystemPromptMessage`、profile prompt 和 chat setup 链路维护系统提示。

建议把 instruction 前缀管理下沉到公共包：

```text
backend/internal/prompt/sessioninstructions
```

公共能力：

- `BuildInstructionMessages(profileState, workspacePath, provider)` 或更底层的 layers 编译函数。
- `EnsureInstructionPrefix(session *chat.Session, instructions []types.Message) bool`。
- `InjectInstructionPrefix(messages []types.Message, instructions []types.Message) []types.Message`。
- `CountLeadingInstructionMessages(messages []types.Message) int`。

接入后：

- runtime-server `AgentChat` 继续使用同一逻辑。
- `aicli` restore/new session 时也使用同一逻辑写 system/developer 前缀。
- `aicli` 专属 UI 文案、欢迎信息、状态栏不进入会话 history。

这样可以避免两个入口对同一 session 首部 system/developer messages 的判断不一致。

### 5. 统一 runtime state、events、tool receipts

把 `sessionRuntime.storePath` 的默认行为收敛到文件 SQLite，并让两边使用同一路径。由于 runtime-server 当前未配置时是内存 store，这属于行为变更，应按 rollout 策略逐步启用。

当前差异：

- runtime-server：`Handler.refreshSessionRuntimeStore` 在未配置 `sessionRuntime.storePath/storeDSN` 时创建内存 store。
- aicli：`buildLocalChatRuntimeStores` 固定写 `<sessionDir>/runtime/chat_runtime.sqlite`。

推荐改为：

- 新默认路径：`<sessionDir>/runtime/session_runtime.sqlite`。
- 新增或约定持久化策略字段，例如：

```yaml
sessionRuntime:
  defaultPersistence: file # memory | file
```

- 阶段 A/B 默认仍可保持 `memory` 或仅在示例配置中显式写入，降低现有部署的磁盘写入变化风险。
- 阶段 B 后允许本地开发模板和 `aicli` 默认使用 `file`。
- 阶段 C/D 再评估是否把 runtime-server 未配置默认值切到 `file`。
- 为兼容旧 aicli，可短期保留对 `<sessionDir>/runtime/chat_runtime.sqlite` 的读取：
  - 如果新路径不存在但旧路径存在，则使用旧路径。
  - 一旦新路径配置明确存在，以新路径为准。
- `RuntimeStoreConfig` 可继续支持 `StoreDSN`，但文件默认应统一。

对应测试：

- runtime-server 显式启用 `sessionRuntime.defaultPersistence=file` 或配置 `storePath` 后，启动后 `GET /api/runtime/sessions/{id}/runtime/events` 能在重启后读到旧事件。
- aicli 本地 actor 运行后，runtime-server 指向同一 config 能读到同一 session 的 runtime events。
- runtime-server 写入 tool receipt 后，aicli 能通过同一 store 恢复。

### 6. 统一 team 与 AgentControl 存储

当前 team store 和 AgentControl store 的默认差异会导致 `spawn_team`、mailbox、AgentControl agents 在两边表现不同。

推荐由统一工厂补齐默认：

```yaml
team:
  storePath: <sessions.dir>/runtime/team_store.sqlite

agentControl:
  storePath: <sessions.dir>/runtime/agent_control.sqlite
  # 可选：只有需要拆分物理库时才显式配置。
  # mailboxStorePath: <sessions.dir>/runtime/agent_control_mailbox.sqlite
  # agentStorePath: <sessions.dir>/runtime/agent_control_agents.sqlite
```

实现注意：

- 如果用户显式配置了 `storeDSN`，不要自动覆盖。
- 如果用户显式配置了统一 `agentControl.storePath`，可继续沿用单库模式。
- 如果配置了拆分的 mailbox/agent store，则按拆分配置。
- runtime-server 和 aicli 都必须通过同一规则解析相对路径。

### 7. 统一 artifact、checkpoint 与 background 存储

会话是否“看起来相同”不只取决于 history 和 runtime events。工具输出归档、上下文 ledger、checkpoint、后台任务也都带有 `session_id`，如果这些 store 仍然分叉，两边仍会出现状态不一致。

#### artifact 与 checkpoint

当前 `artifact.Store` 未配置时会使用内存 SQLite，`checkpoint.Manager` 又依赖同一个 artifact store 保存 checkpoint。推荐：

```yaml
artifact:
  storePath: <sessions.dir>/runtime/artifacts.sqlite

checkpoint:
  enabled: true
```

实现要点：

- 若现有 `RuntimeConfig` 没有 `artifact` 配置段，新增 `ArtifactConfig{StorePath, StoreDSN}`。
- `agent.Config.ArtifactStorePath` 和 `agent.Config.Options["artifact_store_*"]` 保持兼容，但统一工厂解析后的 runtime config 优先注入。
- `checkpoint.Manager` 必须使用统一 `ArtifactStore`，不要由各入口自行创建。
- context compaction、tool artifact refs、restore preview、checkpoint list/restore 都应使用同一 store。
- 对于 CLI debug log 中的 `runtime-http`、`local-shell` 文件，只作为本地诊断 artifact；如果要进入共享检索或 checkpoint，需要显式写入 `ArtifactStore`。

#### background jobs

当前 `background.Manager` 只有在 `StorePath/StoreDSN` 非空时才有稳定 store。推荐：

```yaml
background:
  storePath: <sessions.dir>/runtime/background.sqlite
  logDir: <sessions.dir>/runtime/background_logs
```

实现要点：

- `background_task` 创建、查询、事件读取都使用统一 background manager。
- `LogDir` 与 `StorePath` 一起进入诊断输出。
- runtime-server 与 aicli 本地模式的 background manager 使用相同 store 时，应明确并发恢复语义：已运行的 OS 进程不能被另一个宿主无损接管，但 job record、历史输出和 terminal events 应可查询。
- server 模式下，`background_task` 应由 runtime-server 执行，aicli 只作为客户端读取 job/output/events。

### 8. 增加会话执行 ownership / lease

共享存储不能自动解决两个进程同时驱动同一 session 的问题。建议在 `session_runtime_state` 旁新增 lease 表，或在现有 runtime state 中增加 owner 字段。

推荐新增表：

```sql
CREATE TABLE IF NOT EXISTS session_actor_leases (
  session_id TEXT PRIMARY KEY,
  owner_id TEXT NOT NULL,
  owner_kind TEXT NOT NULL,
  pid INTEGER,
  hostname TEXT,
  acquired_at TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  heartbeat_at TEXT NOT NULL
);
```

新增接口：

```go
type SessionLeaseStore interface {
    AcquireLease(ctx context.Context, req LeaseRequest) (*Lease, error)
    RenewLease(ctx context.Context, sessionID, ownerID string, ttl time.Duration) error
    ReleaseLease(ctx context.Context, sessionID, ownerID string) error
    GetLease(ctx context.Context, sessionID string) (*Lease, error)
}
```

规则：

- `SessionHub.GetOrCreate(sessionID)` 前必须 acquire lease。
- `/api/agent/chat`、`/api/runtime/sessions/{id}/runtime/commands`、aicli 本地 actor turn、会修改 history 的 non-actor fallback 都必须在提交 turn 前 acquire 或 validate lease。
- 纯读取接口，例如 list/history/events/get generated image，不需要 acquire lease。
- 只追加诊断日志但不修改 session history/runtime state 的本地日志写入不需要 lease。
- 同一 owner 可重入。
- lease 过期后其他进程可接管。
- `aicli` 交互式本地模式遇到被 runtime-server 持有的 session，应提示“该会话由 runtime-server 管理”，并建议使用 server 模式或显式 force。
- runtime-server 遇到被本地 `aicli` 持有的 session，应返回 409，并带当前 owner 信息。
- `force` 接管必须显式写 event，便于排查 history 交错和中断原因。

这是避免“同一套会话机制”演变成“同一份数据被两个 actor 乱写”的关键。

### 9. aicli 增加 server 执行模式

长期推荐让 runtime-server 成为共享运行态宿主，`aicli` 作为客户端复用它：

```bash
aicli chat --runtime-server http://127.0.0.1:8101
aicli exec --runtime-server http://127.0.0.1:8101 "..."
```

或配置：

```yaml
aicli:
  runtime:
    mode: auto # local | server | auto
    server_url: http://127.0.0.1:8101
```

`auto` 语义：

- server 健康可用时，通过 `/api/agent/chat`、`/api/runtime/sessions/*` 执行。
- server 不可用时，回退本地 actor-first。
- 回退行为必须在 stderr/status 中显示，避免用户以为已经共享运行态。
- 显式 `--runtime-server` 或 `mode: server` 时，如果 server 不可用应直接失败，不应静默回退本地模式。
- 未传 `--runtime-server` 且配置不是 `server/auto` 时，`aicli` 默认保持当前本地运行能力不变。

server 模式下 `aicli` 应：

- 创建/恢复 session 时调用 runtime-server session API。
- 提交 turn 时调用 `/api/agent/chat`，带上：
  - `session_id`
  - `user_id`
  - `profile`
  - `agent`
  - `provider`
  - `model`
  - `reasoning_effort`
  - `workspace_path`
  - `stream`
- 读取事件时使用 `/api/runtime/sessions/{id}/runtime/stream` 或 `/runtime/events`。
- spawn/wait agent 走 runtime-server 的 `/api/runtime/sessions/{id}/agents*`。

交互状态机：

1. `aicli` 创建或恢复 session，解析 `user_id`，向 server 传入完整 execution context。
2. `aicli` 通过 runtime command 或 agent chat API 提交用户输入。
3. 如果 server 返回 accepted/pending，`aicli` 持续订阅 runtime stream/events。
4. 遇到 `pending approval` 事件时，CLI 渲染审批提示，再调用 `/api/runtime/sessions/{id}/runtime/commands` 的 `approve_tool`。
5. 遇到 `pending question` 事件时，CLI 渲染问题提示，再调用同一 commands API 的 `answer_question`。
6. 遇到 assistant delta、reasoning delta、tool requested/completed、checkpoint completed 等事件时，复用现有本地 event renderer。
7. turn 完成后，CLI 刷新 session history 和 runtime state；如果 stream 中断，按 last event seq 续读 `/runtime/events`。

协议补齐要求：

- server API 响应需要稳定表达 `pending`、`state`、`run_id`、`last_event_seq`。
- `approve_tool` 要支持 patched args，与本地工具审批能力一致。
- `answer_question` 要支持多轮问题，不能只处理单次阻塞输入。
- CLI 要把 server 模式下的 tool execution owner 显示为 `runtime-server`，避免用户误以为工具在本地进程执行。

本地模式仍保留，用于：

- 无 server 的离线场景。
- 测试。
- 需要完全独立进程状态的临时实验。

## 分阶段实施计划

### 阶段 A：诊断与配置收敛

目标：先让用户能明确看到两边到底在使用哪些路径和用户身份。

改动：

1. 增加 session diagnostics：
   - `aicli chat --session-diagnostics`
   - 或复用 `/status`、`/debug` 输出。
2. runtime-server `GET /api/runtime/status` 增加：
   - `sessions.dir`
   - `sessionRuntime.storePath`
   - `team.storePath`
   - `agentControl.*`
   - `artifact.storePath`
   - `checkpoint.enabled`
   - `background.storePath`
   - `background.logDir`
   - CLI 本地 `chat-logs` 路径
   - 当前配置文件路径
3. 对齐配置搜索顺序，至少在文档和诊断中明确：
   - runtime-server 当前查 `config.yaml`。
   - aicli 当前查 `aicli.yaml`。
4. 给 `backend/configs/runtime.yaml` 增加显式持久化配置示例，避免默认内存行为不透明。

验收：

- 用户能一眼看到两个入口的 session dir、runtime store、team store、artifact store、background store、user id 是否一致。
- 不改变既有行为，风险最低。

### 阶段 B：统一会话服务工厂

目标：消除两个入口各自拼默认 store 的分叉。

改动：

1. 新增 `internal/sessionruntime`。
2. runtime-server、aicli chat、本地 actor host 改用统一工厂。
3. 默认补齐 session runtime/team/AgentControl/artifact/background SQLite 路径。
4. 保留旧 `<sessionDir>/runtime/chat_runtime.sqlite` 兼容读取。
5. 新增 `sessionRuntime.defaultPersistence` 或等价开关，避免直接改变 runtime-server 现有部署默认行为。

验收：

- 未配置任何 store 时，两边解析到同一批文件。
- runtime-server 显式启用 file persistence 后，重启后 runtime events 不丢失。
- artifact/checkpoint/background 的诊断路径与实际 store 一致。
- `cd backend && go test ./internal/chat/... ./internal/bootstrap/... ./internal/api/skills/... ./cmd/aicli/commands/...` 通过。

### 阶段 C：统一元数据与身份

目标：让同一 session 在两边展示一致。

改动：

1. 新增公共 session metadata helper。
2. aicli 写公共键，兼容读旧 `aicli_*` 键。
3. runtime-server 写公共键，兼容读旧 `profile_*` 键。
4. 新增可配置 `sessions.defaultUserId`。
5. aicli 调 runtime-server 时显式传当前 `SessionUserID`。

验收：

- `aicli --list-sessions` 与 `/api/runtime/sessions?user_id=<same>` 列表一致。
- provider/model/profile/reasoning 在两边恢复一致。
- 旧 session 文件仍可恢复。

### 阶段 D：引入 actor lease

目标：确保同一 session 同一时间只有一个执行宿主。

改动：

1. `SQLiteRuntimeStore` 增加 lease 表迁移。
2. `SessionHub.GetOrCreate` 或上层 actor factory acquire lease。
3. actor 停止时 release lease。
4. 长运行 turn 定时 renew lease。
5. `/api/agent/chat` 和 non-actor fallback 在写 history 前 validate lease。
6. lease 冲突返回可解释错误。

验收：

- 两个进程同时运行同一 `session_id`，第二个收到 409 或 CLI 友好提示。
- 一个进程持有 actor lease 时，另一个进程不能通过 `/api/agent/chat` 直接追加同一 session history。
- lease 过期后可接管。
- 正常退出会释放 lease。

### 阶段 E：aicli server 模式

目标：把 runtime-server 作为共享运行态宿主，完成真正跨进程统一。

改动：

1. 新增 runtime-server client。
2. `aicli chat/exec` 支持 `--runtime-server` 和配置 `aicli.runtime.mode`。
3. server 模式下用 HTTP API 进行：
   - create/list/load sessions
   - submit turn
   - stream events
   - approve tool / answer question
   - spawn/send/wait/close agents
4. server 模式下 background task、tool execution、checkpoint 都由 runtime-server 宿主执行。
5. 保留 local fallback。

验收：

- 同一 session 由 runtime-server 执行，aicli 可恢复、继续、查看 events。
- Web/API 创建的 session 可被 aicli 加载。
- aicli 创建的 session 可被 runtime-server API 继续执行。
- pending approval/question 在 server 模式下能完整往返。
- `--runtime-server` 显式 server 模式不可用时直接失败；`auto` 模式才允许提示后回退本地。

## 迁移策略

### 历史 JSON 会话

不做强制迁移。读取时兼容旧键，写回时补公共键。

建议提供可选命令：

```bash
aicli sessions migrate --dry-run
aicli sessions migrate
```

迁移动作：

- 补写公共 metadata 键。
- 保留旧键，至少一个小版本周期内不删除。
- 可选把旧 runtime DB 从 `chat_runtime.sqlite` 迁移或复制到 `session_runtime.sqlite`。
- 可选扫描 generated image metadata 中的本地 `saved_path`，报告 server 是否可读，不自动复制用户文件。

### runtime store 文件

兼容顺序：

1. 用户显式配置的 `sessionRuntime.storePath/storeDSN`。
2. 新默认 `<sessions.dir>/runtime/session_runtime.sqlite`。
3. 旧 aicli 默认 `<sessions.dir>/runtime/chat_runtime.sqlite`。

如果 2 和 3 同时存在，不自动合并，诊断中提示用户显式选择或运行迁移命令。

### artifact/checkpoint store 文件

兼容顺序：

1. 用户显式配置的 `artifact.storePath/storeDSN`，或 agent config 中既有 `artifactStorePath` / `options.artifact_store_*`。
2. 新默认 `<sessions.dir>/runtime/artifacts.sqlite`。
3. 未配置时的旧内存 store 不可迁移，只能从历史 session metadata 和本地文件路径做 best-effort 诊断。

迁移原则：

- checkpoint、memory ledger、tool artifact refs 进入统一 artifact store。
- 旧 session history 中已有的 `artifact_refs`、generated image `saved_path` 保持原样读取。
- 不自动搬运 CLI debug log、HTTP request/response artifact、local shell raw output 文件；如需共享检索，应提供显式导入命令。

### background store 与日志

兼容顺序：

1. 用户显式配置的 `background.storePath/storeDSN/logDir`。
2. 新默认 `<sessions.dir>/runtime/background.sqlite` 与 `<sessions.dir>/runtime/background_logs`。
3. 未配置时的旧内存 manager 不可迁移。

迁移原则：

- 已完成 job 的 record、events、log 文件可复制或导入。
- 正在运行的 OS 进程不跨宿主迁移；切换宿主时应把旧 job 标记为 `unknown`、`orphaned` 或按现有 restart policy 恢复。
- server 模式下新 job 只由 runtime-server 创建和执行。

### 配置兼容

`RuntimeConfig.Sessions` 可以扩展字段，但保持 `dir` 原语义：

```yaml
sessions:
  dir: ~/.aicli/sessions
  defaultUserId: local
  maxHistory: 200
  ttl: 168h
  cleanupInterval: 1h
  idleTimeout: 72h
  autoArchive: true

sessionRuntime:
  defaultPersistence: file
  storePath: ~/.aicli/sessions/runtime/session_runtime.sqlite

artifact:
  storePath: ~/.aicli/sessions/runtime/artifacts.sqlite

background:
  storePath: ~/.aicli/sessions/runtime/background.sqlite
  logDir: ~/.aicli/sessions/runtime/background_logs
```

旧配置只含 `sessions.dir` 时继续可用。

## 风险与取舍

- **共享文件不等于共享 actor**：如果没有 lease 或 server 模式，两个进程仍可能同时写同一 session。必须把 ownership 作为核心要求。
- **默认用户变化风险**：runtime-server 不应贸然从 `anonymous` 改成本机 OS 用户。共享列表应通过显式 `user_id` 或 `sessions.defaultUserId` 完成。
- **SQLite 并发**：runtime store、team store、AgentControl store 需要 busy timeout 和事务边界。已有 team store 使用 `_txlock=immediate` 与 `_busy_timeout=5000`，session runtime store 也应补同等级 DSN option。
- **旧路径兼容**：`chat_runtime.sqlite` 不能直接删除，否则旧 aicli 运行态不可见。
- **配置搜索差异**：如果不统一 config 搜索顺序，用户会误以为会话机制不一致，实际是两个入口加载了不同配置。
- **默认持久化行为变更**：runtime-server 从内存改为文件会改变磁盘写入、数据保留和隐私边界。应先通过显式配置、模板或 feature flag 启用，再考虑默认切换。
- **artifact 文件可读性**：history metadata 中的 `saved_path` 是本机路径，server 进程未必有权限读取。诊断必须显示不可读路径，迁移命令只做显式复制或导入。
- **background 运行中任务不可透明接管**：共享 store 能共享 job record 和历史输出，但不能保证另一个进程接管已启动的 OS 进程。
- **server 模式交互完整性**：`aicli --runtime-server` 如果只实现 submit/stream 而不实现 approval/question command loop，会破坏本地交互体验。

## 建议优先级

1. 先做阶段 A 和 B：成本低，能立刻减少“看起来不同”的问题。
2. 再做阶段 C：统一列表、恢复、状态展示。
3. 阶段 D 是共享执行的安全底线。
4. 阶段 E 是最终体验：`aicli` 作为 runtime-server 的 CLI 客户端，共享同一个 session hub。

短期最小可落地组合：

- 统一 `sessions.dir`。
- 显式配置或 feature flag 持久化 `sessionRuntime.storePath`。
- aicli 与 runtime-server 使用相同 `user_id`。
- 统一 metadata helper。
- 诊断输出 artifact/background/checkpoint 路径，先暴露差异。

长期推荐组合：

- 统一会话服务工厂。
- SQLite runtime/team/AgentControl/artifact/background 默认持久化。
- 覆盖 actor 与 non-actor turn 写入的 lease。
- `aicli --runtime-server` server 模式。

## 实施状态（2026-05-12）

本轮实施已经把“共享会话文件/运行态存储”推进到“server 模式共用 runtime-server 的 session actor”：

- `aicli --runtime-server <url>` 的主执行路径已切到 runtime command API：
  - `POST /api/runtime/sessions/{id}/runtime/commands`，`type=submit_prompt`
  - `GET /api/runtime/sessions/{id}/runtime/events`
  - `GET /api/runtime/sessions/{id}/runtime/state`
  - 仅在 command API 返回 `404/405` 时兼容回退旧 `/api/agent/chat`。
- CLI server 模式会在提交 prompt 前同步当前 `ChatSession` 到 runtime-server session storage，并写入公共 metadata：
  - `client=aicli`
  - `entrypoint=aicli`
  - `profile_ref/profile_agent`
  - `provider_name/model/reasoning_effort`
  - `stream/disable_tools`
  - `workspace_path`
  - team/selected agent/permission mode 相关键。
- runtime-server 的 `buildSessionActor` 已从公共 metadata 恢复执行上下文：
  - profile/provider/model/workspace
  - stream
  - reasoning_effort
  - disable_tools
  - child agent type/requested model。
- CLI runtime event bridge 已支持 server 模式的远程交互回路：
  - 收到 `approval_requested` 后调用 command API `approve_tool`。
  - 收到 `question_asked` 后调用 command API `answer_question`。
  - `answer_question` 允许空 answer，用于非交互或用户未输入时解除 pending question。
- server 模式的输出收敛到 runtime events 渲染路径，避免 actor event 已渲染 assistant final 后再由 chat loop 重复打印。
- runtime events API 已返回 `latest_seq`，`aicli` 取 server event baseline 时只请求轻量事件页，不再为计算当前高水位拉取全量事件。
- `aicli` server 模式等待 turn 完成时已优先消费 `/runtime/stream` SSE；旧 runtime-server 没有 stream 端点或 stream 提前断开时，会回退到 `/runtime/events?wait_ms=` 长轮询。
- runtime events API 已支持 `wait_ms` 长轮询，作为 SSE 不可用时的兼容路径，减少 200ms 短轮询带来的空请求。
- `/api/runtime/sessions/{id}/runtime/stream` 已优先使用 `EventWatcherStore` 唤醒 SSE 输出，保留低频 fallback poll，避免 stream handler 自身空转。
- session runtime store 已具备 lease 表、acquire/renew/release 生命周期；`/api/agent/chat` 在 session lease 被 actor 持有时返回冲突，避免 non-actor 路径并发写同一 session。
- `sessionruntime.ResolvePaths` 已在默认单库 `agentControl.storePath` 模式下展开有效 mailbox/agent 路径，runtime status 的 `session_persistence` 可以直接看到 AgentControl registry、mailbox、agents 实际共用的 SQLite 文件；显式拆分配置仍保持优先。
- 新增/更新的测试已覆盖：
  - `aicli` server 模式使用 runtime command 主路径提交 prompt 并刷新 history。
  - server 模式 pending approval/question 通过 command API 完成往返。
  - runtime-server actor 从共享 metadata 恢复 provider/model/workspace/stream/reasoning/disable_tools。
  - runtime command API 接受空 answer。
  - runtime-server event 渲染路径不重复输出 final response。
  - `aicli` 优先使用 runtime stream SSE，并在旧 server 不支持 stream 时回退 events 长轮询。
  - artifact checkpoint SQLite store 可在关闭并重新打开后读取 checkpoint、ledger、checkpoint_files 和 blob 内容，覆盖共享 artifact/checkpoint 数据面的持久化底座。
  - background SQLite store 可在关闭并重新打开后读取 job、metadata、restart policy、events 和按 `after_seq` 续读事件，覆盖共享 background 数据面的持久化底座。
  - 同一 `sessions.dir` 下，runtime-server 显式 `sessionRuntime.defaultPersistence=file` 与 aicli 本地模式会解析到相同的 session runtime、team、AgentControl、artifact、background store 和 background log 路径。
  - runtime-server handler 级跨实例验证已覆盖：本地侧先写入共享 `session_runtime.sqlite`，新的 handler 只根据同一 runtime config 重新打开后，可通过 `/api/runtime/sessions/{id}/runtime/events` 读到事件和 `latest_seq`。
  - runtime-server handler 级跨实例验证已覆盖：本地侧先写入共享 `artifacts.sqlite` 中的 checkpoint/checkpoint_files，新的 handler 只根据同一 runtime config 和 session storage 重新打开后，可通过 `/api/runtime/sessions/{id}/checkpoints` 与 `/files` 读到 checkpoint 与文件记录。
  - runtime-server handler 级跨实例验证已覆盖：本地侧先写入共享 `background.sqlite` 和 background log 文件，新的 handler 只根据同一 runtime config 重新打开后，可通过 `/api/runtime/background/jobs`、`/events`、`/output` 读到 job、event 和输出。
- `aicli --runtime-server` 显式 server 模式不可用时会直接失败；`runtime.mode=auto` / `--runtime-server auto` 才会把 options 回退到 local 模式，已补回归测试防止误把显式 server 模式静默降级。
- runtime-server 已新增会话用户发现 API：
  - `GET /api/runtime/sessions/users`
  - 响应按实际 session store 中的 `userId` 聚合，不返回完整 history。
  - 每个用户返回 `session_count`、`active_count`、`idle_count`、`closed_count`、`archived_count`、`recoverable_count`、`latest_updated_at`、`display_name`、`source`。
  - 响应同时返回 `default_user_id`，用于前端理解当前 server fallback，但不再要求用户只能通过 `sessions.defaultUserId` 固定默认用户。
- 前端工作区侧栏已接入会话用户选择：
  - 首次加载时调用 `/api/runtime/sessions/users` 发现实际存在的会话用户。
  - 选择优先级为：当前已选择用户仍存在时保持不变；否则优先选择有会话的 `default_user_id`；否则选择最近更新的用户；最后回退到浏览器 runtime client user。
  - 选中用户后继续复用现有 `GET /api/runtime/sessions?user_id=<selected>` 查询，不改变后端 session JSON 格式。
  - 选中用户写入浏览器 localStorage；用户列表接口不可用时仍保留当前选中用户或浏览器 runtime client user，避免前端不可用。
  - 工作区提交新会话或继续会话时使用当前选中的会话用户作为 `user_id`，使“查看”和“续写/新建”保持同一个用户命名空间。
- 前端会话侧栏进一步从下拉选择优化为树形菜单：
  - 每个 session user 单独占用一级菜单项，显示用户标识、会话数量和默认用户标记。
  - 当前选中的用户展开为子树；切换用户后再按该用户加载会话，避免一次性拉取所有用户的全部 session。
  - 用户子树按会话所属目录分二级菜单，目录来源优先读取 session metadata context 中的 `workspace_path` / `workspacePath`，并兼容 `cwd`、`workdir`、`working_dir`、`profile_root` 等历史或近似字段。
  - 目录节点下再展示会话列表；无目录信息的会话归入“未绑定目录 / Unscoped sessions”。
- 这次前端用户选择优化解决了“配置 `sessions.defaultUserId` 比较死板”的主要体验问题：runtime-server 可以默认继续保守使用 `anonymous`，但前端用户可以直接看到 `aicli` 写入的 OS 用户、Web Console 用户等实际会话命名空间，并手动切换。

当前仍保留的工程缺口：

- server 模式 CLI 已能消费 `/runtime/stream` SSE；后续可以继续补更细粒度的断线续读指标和用户可见诊断，例如显示 stream fallback 原因与最后 event seq。
- `latest_seq` 已随 events 响应返回；后续如需要更清晰的 API 语义，可再增加独立 `last_seq` 或 `head` endpoint。
- runtime events、artifact/checkpoint、background 已补 store 级跨 reopen 与 handler 级跨实例回归测试；仍需继续补 runtime-server 真实进程与 aicli 命令进程指向同一 config 时的端到端共享验证。
- 统一 session services 工厂还没有完全替代所有旧入口；现阶段已经抽出 `internal/sessionruntime` 路径/身份能力，但 bootstrap、aicli local host、runtime-server handler 仍有部分兼容包装和局部默认逻辑。
- CLI 本地诊断日志仍保持本地归档，不属于核心共享 session 状态；需要在诊断输出中继续清晰标注。
- `/api/runtime/sessions/users` 当前基于 session store 聚合，适合本地 runtime 管理界面；如果后续 runtime-server 暴露到多租户或公网环境，应结合现有 auth/scope policy 限制可见用户范围，避免把所有 session 用户枚举给非管理员请求。

## 关键代码位置

当前需要重点改动或复用的位置：

| 模块 | 文件 | 当前职责 | 建议动作 |
| --- | --- | --- | --- |
| runtime-server 启动 | `backend/cmd/runtime-server/main.go` | 加载 agent config/runtime config，创建 handler/bootstrap | 改用统一 session services，并把解析路径暴露到 status。 |
| bootstrap | `backend/internal/bootstrap/manager.go` | 创建 `SessionManager`、LLM runtime、team store | 支持注入 session services；移除重复默认逻辑。 |
| 会话模型 | `backend/internal/chat/session.go` | `chat.Session` 与 metadata | 保持模型，新增公共 metadata helper。 |
| 会话存储 | `backend/internal/chat/file_storage.go` | JSON 文件存储 | 保持格式，补诊断和迁移工具即可。 |
| runtime store | `backend/internal/chat/session_runtime_store.go` | SQLite/in-memory runtime state/events/tool receipts | 增加 DSN option、lease 表与默认路径接入。 |
| artifact store | `backend/internal/artifact/store.go` | artifact、memory ledger、checkpoint SQLite | 接入统一默认路径，避免未配置时两端使用不同内存 store。 |
| checkpoint manager | `backend/internal/checkpoint/manager.go` | mutation checkpoint 捕获与恢复 | 使用统一 ArtifactStore。 |
| background manager | `backend/internal/background/manager.go` | background task、job events、output logs | 接入统一 store/log dir，并暴露诊断。 |
| agent | `backend/internal/agent/agent.go` | 创建 artifact store、context/output/checkpoint 依赖 | 支持注入统一 artifact/checkpoint 依赖。 |
| runtime HTTP handler | `backend/internal/api/skills/handler.go` | sessions、agent chat、runtime/background store refresh | 写公共 metadata，持久化 runtime/background/artifact store，并在所有写 history 的入口校验 lease。 |
| session agent API | `backend/internal/api/skills/session_runtime_support.go` | session actor、spawn/wait/read events | acquire lease，使用统一 store，暴露 pending approval/question 事件给 server 模式 CLI。 |
| runtime command API | `backend/internal/api/skills/session_runtime_handlers.go` | submit prompt、approve tool、answer question | 作为 `aicli --runtime-server` 交互命令通道。 |
| aicli 会话管理 | `backend/cmd/aicli/commands/chat_session.go` | CLI 会话创建、恢复、同步 | 改用统一工厂和公共 metadata。 |
| aicli bootstrap | `backend/cmd/aicli/commands/chat_bootstrap.go` | 准备 persistence/runtime state | 接入统一 identity resolver。 |
| aicli actor host | `backend/cmd/aicli/commands/chat_actor_host.go` | 本地 SessionHub/runtime/team/AgentControl store | 移除本地私有默认路径，改用统一 services。 |
| aicli logger/artifacts | `backend/cmd/aicli/commands/logger.go` | CLI 本地 debug log 与 runtime-http/local-shell artifact | 保持本地归档，但诊断中和共享 artifact store 明确区分。 |

## 测试计划

### 单元测试

- `internal/sessionruntime`
   - 默认路径解析。
   - runtime config 相对路径解析。
   - `~` 展开。
   - 显式 DSN 优先级。
   - 旧 `chat_runtime.sqlite` 兼容选择。
   - artifact/background 默认路径与显式配置优先级。
   - `sessionRuntime.defaultPersistence` 为 `memory/file` 时的路径选择。
- `internal/chat`
   - metadata helper 兼容旧键。
   - lease acquire/renew/release。
   - lease 过期接管。
- `internal/artifact` / `internal/checkpoint`
   - 统一 artifact store 能保存和读取 checkpoint。
   - 旧 metadata refs 不被迁移逻辑破坏。
- `internal/background`
   - 默认 store/log dir 解析。
   - job events 和 output logs 重启后可读。
- `internal/api/skills`
   - runtime-server 显式启用 file persistence 时使用文件 SQLite。
   - `/api/agent/chat` 写公共 metadata。
   - `/api/agent/chat` 在 lease 冲突时不追加 history。
   - `/api/runtime/sessions` 按相同 user id 能列出 CLI 创建的 session。
   - runtime command API 支持 approve/answer 的 server-mode 往返。
- `cmd/aicli/commands`
   - `newChatSessionManager` 与 runtime-server 解析同一路径。
   - 恢复 runtime-server 创建的 session。
   - 读取旧 `aicli_*` metadata 键。
   - `--runtime-server` 显式模式不可用时失败，`auto` 模式提示后回退。
   - pending approval/question 事件能调用 server command API。

### 集成测试

1. 启动 runtime-server，创建 session：

```bash
runtime-server serve --config <shared-config> --listen 127.0.0.1:8101
curl -X POST http://127.0.0.1:8101/api/runtime/sessions -d '{"user_id":"local","title":"shared"}'
```

验证：

- `aicli chat --list-sessions --session-query shared` 能看到该 session。

2. 用 aicli 创建 session：

```bash
aicli chat --no-interactive --message "hello" --session-dir <shared-dir>
```

验证：

- `GET /api/runtime/sessions?user_id=<same>` 能看到。
- `GET /api/runtime/sessions/{id}/history` 能读取同一 history。

3. actor runtime 事件共享：

- aicli 本地执行一轮带工具调用的会话。
- runtime-server 读取 `/api/runtime/sessions/{id}/runtime/events`。
- runtime-server 执行一轮 agent chat。
- aicli `/status` 或诊断读取同一 event store。

4. artifact/checkpoint 共享：

- aicli 本地执行一轮会产生 checkpoint 或 artifact refs 的工具调用。
- runtime-server 指向同一 config 后能读取 checkpoint 列表或 restore preview。
- runtime-server 产生的 checkpoint 能被 aicli 诊断或恢复流程识别。

5. background 共享：

- runtime-server 启动 `background_task`。
- aicli server 模式读取同一 job、events 和 output。
- aicli 本地模式使用同一 config 创建已完成 job 后，runtime-server 能查询历史 record 和 log。

6. lease 冲突：

- 进程 A acquire 同一 session actor lease。
- 进程 B 尝试执行同一 session。
- 断言返回 409 或 CLI 友好错误。
- 进程 B 通过 `/api/agent/chat` non-actor 路径提交同一 session 也必须被拒绝。

7. server 模式交互：

- `aicli chat --runtime-server <url>` 提交需要审批的工具调用。
- CLI 收到 pending approval，调用 command API approve 后继续。
- CLI 收到 pending question，调用 command API answer 后继续。
- server 不可用时，显式 `--runtime-server` 失败；`mode:auto` 输出回退提示并使用本地 actor。

## 当前推荐落点

如果只做一个短期修复，优先顺序应是：

1. 增加诊断输出，显示两边实际使用的：
   - config path
   - session dir
   - user id
   - runtime store path
   - team store path
   - agentControl store path
   - artifact store path
   - checkpoint enabled/store source
   - background store path
   - background log dir
   - CLI debug log/artifact dir
2. 引入 `sessionRuntime.defaultPersistence` 或等价开关，在示例配置中显式把 runtime-server `sessionRuntime.storePath` 指向 `<sessions.dir>/runtime/session_runtime.sqlite`。
3. 把 aicli 本地 runtime store 默认从 `chat_runtime.sqlite` 迁到同一新路径，并兼容旧路径。
4. 为 artifact/checkpoint/background 增加统一默认路径解析，但先通过显式配置或模板启用，避免直接改变现有 server 部署。
5. aicli 与 runtime-server 使用相同 `user_id`。
6. 增加公共 metadata helper，新写入公共键，旧键只读兼容。
7. 下一步实现 lease 时，必须覆盖 actor 与 `/api/agent/chat` 直接写 history 两类入口。

这些步骤能先解释和修复大部分“两个命令生成的数据看起来不一样”的问题，并且不会忽略 artifact/checkpoint/background 这些实际影响体验的数据面；后续再做 lease 和 server 模式，解决跨进程同时执行的正确性。
