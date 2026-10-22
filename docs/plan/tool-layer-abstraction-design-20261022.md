# 工具层抽象设计：一份定义，多种调用表面

- 状态：Proposal / P0 revised（2026-10-22）；P1/P2 待补，待复审
- 日期：2026-10-22
- 范围：`internal/toolkit`、`internal/toolkit/tools`、`internal/remotetools`、`internal/tools`、`cmd/toolkit-mcp-server`、`cmd/aicli-remote-tools`，以及后续 CLI/HTTP 表面
- 目标：把“工具”抽成独立逻辑层，让同一份工具定义与实现可以被内置 Agent、MCP Server、远程工具服务、独立 CLI、HTTP/API 等表面复用

> 上一轮审核结论：**Changes requested**。本次已按审核报告补齐 P0 四项，
> P1/P2 仍未全部关闭，因此**仍不进入实现**，需复审后再放行。
> 审核报告：[`tool-layer-abstraction-design-20261022-review.md`](tool-layer-abstraction-design-20261022-review.md)。

### P0 修订状态

| 编号 | 项目 | 本次处理 | 状态 |
|------|------|----------|------|
| P0-1 | 远程输出 metadata 信任边界 | 新增 §9.3/§9.6：远程 `Meta` 视为不可信提示，本地硬上限 + clamp + tail 校验 | 已补，待复审 |
| P0-2 | Artifact 命名空间与所有权 | 新增 §9.7 与 `ArtifactRef`：local 裸 id、remote `remote:<target>:<id>`，跨 scope 拒绝 | 已补，待复审 |
| P0-3 | Capability / Policy Gate 顺序 | 重写 §10.1–§10.6：复用现有 `policy.Engine`，给出固定 0–18 顺序，禁止第二授权层 | 已补，待复审 |
| P0-4 | Streaming / Jobs / Interactive | 决策为 **v1 final-result only**；streaming/PTY/jobs 明确 local-only，见 §11.1 | 已补，待复审 |

---

## 1. 结论先行

当前仓库已经有一半抽象：

- `toolkit.Tool` 接口（Name / Description / Version / Parameters / Execute / CanDirectCall）；
- `toolkit.Registry`（注册、schema 规范化、按上下文列出）；
- `toolkit.NewToolkitMCPServer`（ToolResult → MCP content/meta）；
- `internal/tools.Manager`（本地 toolkit + MCP 工具合并）。

但**工具实现本身**仍然和“本地运行时”耦合：

- 本地工具内嵌 `sandboxPolicy`，直接依赖 `executor.Sandbox` 与 `toolctx`；
- 输出预算/截断逻辑在 `internal/toolkit/tools/tool_output_budget.go` 与 `internal/output`，远程工具却另写了一套 `foldRemoteOutput`；
- `internal/remotetools` 复制了 view/grep/glob/ls/shell/write/edit/patch；
- `cmd/toolkit-mcp-server` 自己硬编码工具列表，只暴露 stdio；
- `cmd/aicli-remote-tools` 又有自己的 registry 与工具集。

因此建议的目标不是“再写一套远程工具”，而是：

> **toolcore（共享工具语义与实现）+ 多个 surface adapter（内置、MCP、远程、CLI、HTTP）。**

工具定义、schema、执行逻辑、输出契约只写一次；路径、权限、命令策略、artifact、session 由运行时注入。

---

## 2. 现状与问题

### 2.1 已有抽象

| 能力 | 位置 | 现状 |
|------|------|------|
| Tool 接口 | `internal/toolkit/interface.go` | Name / Description / Version / Parameters / Execute / CanDirectCall |
| ToolResult | `internal/toolkit/interface.go` | text / binary / metadata；`MetadataWithOutputKind` |
| Registry | `internal/toolkit/registry.go` | schema 规范化、去重、按 context 列出 |
| MCP 投影 | `internal/toolkit/mcp_server.go` | `ToolResult` → MCP content + `Meta`；支持 ImageContent |
| MCP 客户端回读 | `internal/tools/manager.go` `formatMCPResult` | 克隆 `result.Meta` 到本地 metadata |
| 本地工具集 | `internal/toolkit/tools` | view/grep/glob/ls/shell/write/edit/patch/... |
| 远程工具集 | `internal/remotetools` | 另一套 view/grep/glob/ls/shell/... |

### 2.2 主要耦合点

1. **本地沙箱耦合**
   - `internal/toolkit/tools/sandbox_support.go` 的 `sandboxPolicy` 直接持有 `runtimeexecutor.Sandbox` 与 `basePath`；
   - `effectiveBasePath` 读取 `toolctx.WorkspaceRoot(ctx)`；
   - 工具实现直接调用本地路径校验、本地 shell 策略。

2. **输出契约重复**
   - 本地：`tool_output_budget.go`（view/grep 32 KiB、glob/ls 16 KiB、shell 32 KiB）、`internal/output`（reducers / reserved tail）、`toolresult` 元数据；
   - 远程：`foldRemoteOutput`、`boundedWriter`、各自的截断提示；
   - 结果：同一份内容在本地/远程可能被截断成不同形状，wire 大小与模型上下文不一致。

3. **搜索实现重复**
   - 本地 grep 优先 `internal/ripgrep`（rg），有 fallback、ignore、输出预算、partial 语义；
   - 远程 grep 是纯 Go `filepath.WalkDir` + `regexp.MatchString`，不读 `.gitignore`，文件大小/行长度限制不同；
   - `go list -deps ./cmd/aicli-remote-tools` 不含 `internal/toolkit/tools`，证明两套实现确实独立。

4. **入口重复**
   - `cmd/toolkit-mcp-server` 硬编码工具列表，只支持 stdio；
   - `cmd/aicli-remote-tools` 自己 build registry，支持 stdio/http；
   - 新增一个工具或输出策略时，需要改多处。

### 2.3 后果

- 行为漂移：同名工具在本地/远程的选项、ignore 规则、截断、错误码不同；
- 修复成本：一个 grep/view 修复要写两遍；
- 测试成本：两套 golden/benchmark；
- 抽象泄漏：远程工具逐渐长出自己的 profile、权限、输出逻辑；
- 新表面（CLI/HTTP/ACP）每接一个都要再拼一次工具列表。

---

## 3. 目标与非目标

### 3.1 目标

1. **一份工具定义**：名称、别名、描述、JSON Schema、版本、能力属性、输出预算只定义一次。
2. **一份工具实现**：view/grep/glob/ls/write/edit/apply_patch/shell 等核心逻辑只写一次。
3. **多表面复用**：
   - 内置 Agent（`internal/tools.Manager`）；
   - MCP Server（stdio / Streamable HTTP）；
   - 远程工具服务（`aicli-remote-tools`）；
   - 独立 CLI（`toolctl list/describe/run/serve`）；
   - HTTP/API / ACP / 测试 in-memory surface。
4. **运行时注入**：路径解析、权限检查、命令策略、env filter、artifact store、session state、clock/IDs 全部由 surface 提供。
5. **输出契约统一**：预算、截断、reducer、reserved tail、artifact 指针、metadata key 复用本地成熟实现。
6. **策略与能力分离**：工具只声明“我是什么”（read/write/exec/network），是否允许由 permission mode / target policy 决定。
7. **兼容迁移**：保留现有工具名、schema、`toolkit.Tool` 接口；分阶段替换，不搞大爆炸。
8. **信任边界明确**：远程 metadata 只作为提示；输出硬上限、artifact scope、gate 顺序都由本地/调用方决定。
9. **v1 范围收敛**：core 只承诺 final-result；interactive/PTY/jobs/streaming 明确 local-only。

### 3.2 非目标

- 不改变现有 permission mode 的语义；
- 不把远程工具权限再加一层独立授权表；
- 不强制所有工具立即迁移；
- 不实现 gzip/brotli 传输压缩（输出“压缩截取”指语义折叠，不是 HTTP gzip）；
- 不把本地 knowledge/code index/web search 等本地专属能力强行搬到远程；
- 不在 v1 把 interactive/PTY/jobs/streaming 纳入 core（见 §11.1）；
- 不做动态插件加载（不在本方案范围）。

---

## 4. 设计原则

| 原则 | 说明 |
|------|------|
| 一份定义 | schema/描述/能力/预算只维护一处 |
| 运行时注入 | core 不知道路径怎么解析、权限怎么判 |
| 策略在边界 | 工具不自行 approve/deny；surface/runtime 决定 |
| 输出在中间件 | 截断/reducer/artifact 是 pipeline 阶段，不是每个工具重复写 |
| 能力与模式分离 | capability 是工具属性；mode 是会话策略 |
| fail-closed | 未声明/未注入的运行时能力默认拒绝 |
| 可观测 | 每次调用在 surface 边界产生统一事件/审计 |
| 信任分层 | 远程 metadata 不可信；输出整形与 artifact 路由由本地 trust policy 决定 |
| 兼容优先 | 旧接口保留，adapter + alias 迁移 |
| 不复制 | 禁止再出现第三套 view/grep/shell 实现 |

---

## 5. 分层架构

```text
┌─────────────────────────────────────────────────────────────┐
│ Surfaces                                                    │
│  builtin agent │ MCP stdio/http │ remote tools │ CLI │ HTTP │
└───────────────┬───────────────┬───────────────┬─────────────┘
                │               │               │
        ┌───────▼───────────────▼───────────────▼───────┐
        │ Surface Adapters                              │
        │  - schema projection / name prefix / alias    │
        │  - request identity / auth / audit            │
        │  - result mapping (MCP/CLI/HTTP)              │
        └───────────────────┬───────────────────────────┘
                            │
        ┌───────────────────▼───────────────────────────┐
        │ toolcore                                      │
        │  Definition / Handler / Registry              │
        │  Execution Pipeline                           │
        │   validate → policy → execute → shape → map   │
        └───────────────────┬───────────────────────────┘
                            │
        ┌───────────────────▼───────────────────────────┐
        │ Runtime Contracts (injected)                  │
        │  PathResolver / PermissionChecker             │
        │  CommandPolicy / EnvProvider                  │
        │  OutputShaper / ArtifactSink                  │
        │  SessionState / Clock / IDs                   │
        └───────────────────┬───────────────────────────┘
                            │
        ┌───────────────────▼───────────────────────────┐
        │ Implementations                               │
        │  LocalRuntime: executor.Sandbox + toolctx     │
        │  RemoteRuntime: Workspace jail + remote policy│
        │  CLIRuntime: flags + workspace                │
        │  TestRuntime: temp dir + allow-all            │
        └───────────────────────────────────────────────┘
```

依赖规则：

- Surfaces 依赖 toolcore；
- toolcore 只依赖 contracts 与标准库/共享基础包；
- **toolcore 不得 import** `internal/executor`、`internal/toolctx`、`internal/tools`、`internal/mcp`；
- Runtime 实现可以 import 这些包；
- 具体工具实现（view/grep/...）位于 toolcore，通过 Runtime 接口访问环境。

---

## 6. 核心契约

### 6.1 ToolDefinition

```go
type Capability string

const (
    CapReadFS        Capability = "read_fs"
    CapWriteFS       Capability = "write_fs"
    CapExecShell     Capability = "exec_shell"
    CapNetwork       Capability = "network"
    CapImage         Capability = "image"
    CapArtifact      Capability = "artifact"
    CapStateful      Capability = "stateful"
)

type Definition struct {
    Name        string
    Aliases     []string
    Description string
    Version     string
    InputSchema json.RawMessage

    // 工具属性：决定它“是什么”，不决定“是否允许”。
    Capabilities []Capability

    // 默认输出预算；0 表示走 runtime 的默认策略。
    DefaultOutputBudget int

    // 是否允许非 MCP 表面直接调用；不跳过 policy / sandbox。
    DirectCall bool

    // 可选：UI/审计/路由元数据。
    Metadata map[string]any
}
```

约束：

- `Capabilities` 只描述工具行为，不包含 allow/deny；
- v1 **不把** streaming、interactive PTY、background jobs 作为 core capability；
  这些能力只存在于本地 surface，见 §11.1；
- `CapStateful` 只表示工具有状态需求；远程 target 默认 stateless，
  未显式提供 target-scoped state 时应返回 `UNSUPPORTED`，不能假装支持；
- `Name` 是规范名；`Aliases` 仅用于兼容/模型习惯，注册表要能解析；
- schema 在注册时统一 canonicalize（复用 `internal/toolschema`）；
- 远程 prefix（`remote_238__`）、MCP server name、CLI 子命令名都由 surface 添加，不写进 Definition。

### 6.2 Handler 与 Call

```go
type Handler interface {
    Definition() Definition
    Execute(ctx context.Context, call *Call) (*Result, error)
}

type Call struct {
    Args    map[string]any
    Runtime Runtime
    Request RequestInfo
}

type RequestInfo struct {
    SessionID  string
    Principal  string
    RemoteAddr string
    ToolCallID string
    TraceID    string
    Surface    string // builtin | mcp | remote | cli | http
}
```

- Handler **不**读取 `toolctx`、不持有 `executor.Sandbox`、不解析命令行策略；
- Handler 只通过 `call.Runtime` 访问环境；
- Handler 返回 `Result`，不负责 MCP/CLI/HTTP 映射。

### 6.3 Result 与错误契约

```go
type Result struct {
    Success    bool
    OutputKind string // text | structured | binary | empty
    Content    string
    Data       []byte
    MIMEType   string

    // 结果的信任来源。由 Runtime/Surface 填充，Handler 不得伪造。
    Origin ResultOrigin

    Metadata map[string]any

    // 结构化错误（可选）
    ErrorCode  string
    NextAction string
    Retriable  bool
    Err        error
}

type ResultOrigin struct {
    Scope      string // local | remote | cli | test
    Target     string // remote target name, empty otherwise
    TrustLevel string // local | trusted_remote | untrusted_remote
}
```

- `ErrorCode` 使用稳定字符串（如 `PATH_DENIED`、`COMMAND_DENIED`、`TIMEOUT`、`COMMAND_FAILED`）；
- `Origin` 决定输出整形是否走远程信任边界；surface 必须覆盖 handler 提供的值；
- `NextAction` 是模型可执行的下一步；
- 表面映射：
  - MCP：`IsError` + `Meta.error_code/next_action/retriable`；
  - CLI：非零退出码 + JSON error；
  - HTTP：状态码 + JSON error；
- 工具不得直接写 stdout/stderr；日志走 runtime/observability。

### 6.4 Runtime 接口

```go
type Runtime interface {
    Paths() PathResolver
    Permissions() PermissionChecker
    Commands() CommandPolicy
    Env() EnvProvider
    Output() OutputShaper
    Artifacts() ArtifactSink
    Sessions() SessionState
    Clock() Clock
    IDs() IDGenerator
}

type PathResolver interface {
    Root() string
    Resolve(raw string) (string, error)
    Relative(abs string) string
}

type PermissionChecker interface {
    CheckPath(op Operation, abs string) error
}

type CommandPolicy interface {
    Check(command string) (rule string, err error)
}

type OutputShaper interface {
    Budget(tool string) int
    Shape(tool string, result *Result) (*Result, error)
}

type ArtifactSink interface {
    // Put 的 ref.ID 可为空；返回带 namespace 的完整 ref。
    Put(ctx context.Context, ref ArtifactRef, data []byte, meta ArtifactMeta) (ArtifactRef, error)
    // Open 只允许读取 ref 所声明的 scope/target，跨 scope 必须拒绝。
    Open(ctx context.Context, ref ArtifactRef, offset, limit int) (ArtifactChunk, error)
}

type ArtifactScope string

const (
    ArtifactScopeLocal  ArtifactScope = "local"
    ArtifactScopeRemote ArtifactScope = "remote"
)

type ArtifactRef struct {
    Scope  ArtifactScope
    Target string // remote target name; empty for local
    ID     string // art_<...>
}

type ArtifactChunk struct {
    Data   []byte
    Size   int64
    Offset int64
    EOF    bool
}

type SessionState interface {
    // 可选；nil 表示 stateless。
    IDs() SessionIDs
    Workspace() WorkspaceState
    Artifacts() ArtifactState
    // 本地扩展（read dedup / ledger / todos），不是跨表面契约。
    Extensions() ExtensionStore
}

type SessionIDs struct {
    SessionID  string
    ToolCallID string
    TurnID     string
    GoalID     string
    AgentDepth int
}

type WorkspaceState struct {
    Root         string
    AllowedRoots []string
    ReadOnlyRoots []string
}

type ArtifactState struct {
    Store     ArtifactSink
    ReadScope string
}

type ExtensionStore interface {
    Get(key string) (any, bool)
    Set(key string, value any)
}

type EnvProvider interface {
    Filter(env []string) []string
}
```

- `Operation`：`read` / `write` / `list` / `exec`；
- `Clock` / `IDs` 让测试可确定性；
- `Sessions()` 可以为 nil；远程 stateless HTTP 就是 nil；核心代码不得用
  `Extensions().Get/Set` 代替 typed accessor；
- `Result.Origin` 由 Runtime/Surface 填充；远程 MCP 客户端在 `formatMCPResult`
  处必须按 MCP server 的 trust level 重新标注，不得沿用远程自报的信任级别；
- 任何未注入的能力默认 fail-closed（返回 `UNSUPPORTED`/`DENIED`）。

### 6.5 Registry

Registry 负责：

- 注册/注销；
- 别名解析（`read` → `view`）；
- schema canonicalize + revision；
- 按 capability / surface / profile 列出；
- 冲突检测（同名、alias 冲突、canonical name 预算）。

```go
type Registry interface {
    Register(Handler) error
    Unregister(name string) error
    Resolve(name string) (Handler, bool)
    List(filter Filter) []Handler
    Schema(name string) (json.RawMessage, bool)
}
```

**注意**：profile（readonly/standard/ops/custom）、`tools:` allowlist、远程 prefix 都是 **surface filter**，不是 core 的一部分。

### 6.6 执行流水线

```text
call(name, args)
  │
  ├─ 0. surface filter: profile/tools/prefix/target enabled（只决定“是否可见”）
  ├─ 1. resolve: alias → canonical, prefix strip, collision check
  ├─ 2. schema validate (toolargs/toolschema)
  ├─ 3. permission hook（deny 硬拒；modify 后回到 2 重新校验）
  ├─ 4. static policy（现有 ToolExecutionPolicy）：
  │     deny → delegation → allowlist → capability scope → read-only
  ├─ 5. rules（first match wins；deny 是硬拒）
  ├─ 6. remembered grants（session/project）
  ├─ 7. external-directory gate
  ├─ 8. read-only auto allow
  ├─ 9. shell breaker / sensitive-write gate
  ├─ 10. plan-entry gate
  ├─ 11. permission mode（default/accept_edits/plan/bypass/dont_ask）
  ├─ 12. callback override（patch 后必须重新校验）
  ├─ 13. ask 解析：dont_ask 拒绝；bypass 放行但 HardAsk 除外；
  │     无 AskHandler 时 headless deny
  ├─ 14. execution preflight：路径 sandbox + command policy + env filter
  ├─ 15. handler.Execute(ctx, call)
  ├─ 16. output shape：归档 → fold/reducer → trust clamp → metadata
  ├─ 17. surface map（MCP / CLI / HTTP / builtin）
  └─ 18. observability（audit / metrics / access log）
```

关键决策：

- **步骤 0 是 surface filter，不是授权**：profile/tools 只决定工具是否存在；
- **步骤 3–13 必须复用现有 `internal/policy.Engine`**，不得在 toolcore 里另写一套
  allow/deny/mode/approval；`ToolExecutionPolicy.AllowToolCallWithContext`
  负责步骤 14 的具体参数/路径/命令复核；
- **输出整形在流水线**，不是每个工具自己写；
- **审计在 surface/runtime 边界**，不是每个工具自己写；
- 工具可以声明 `DefaultOutputBudget` 或覆盖 shape 策略，但不能绕过 artifact 归档；
- 能力 gate 与 output shape 的顺序固定：先拒绝，再执行，再整形；
- `skip_render_truncation` 只在步骤 16 生效，且远程结果必须先过 trust clamp；
- 任何 direct call / CLI 调用也必须从步骤 0 走到 14，`CanDirectCall`
  只影响“能否被 MCP 直接暴露”，不是授权开关。

---

## 7. Runtime 实现矩阵

| Runtime | PathResolver | Permission | CommandPolicy | Output | Artifact | Session |
|---------|--------------|------------|---------------|--------|----------|---------|
| **Local** | `executor.Sandbox` + `toolctx.WorkspaceRoot` | 本地 sandbox + permission mode | 本地 shell 策略 | `internal/output` + `tool_output_budget` | 本地 artifact store | read dedup / ledger / todos |
| **Remote** | `remotetools.Workspace` | `remotetools.Sandbox` | allowlist/pattern/metachar/shellrisk | 共享 output core | `remotetools.ArtifactStore` | stateless（默认） |
| **CLI** | `--workspace` + `--allowed-root` | `--allow/--deny` + 只读默认 | `--allow-command` / `--allow-pattern` | 共享 output core | `--artifact-dir`（可选） | 无 |
| **Test** | temp dir | allow-all 或测试策略 | allow-all/固定 | 共享 output core | in-memory | in-memory |

原则：

- 本地/远程**共享工具实现**，不共享策略默认值；
- `LocalRuntime` 保持现有 `executor.Sandbox` 行为；
- `RemoteRuntime` 保持远程 fail-closed（空 allowlist = 全拒）；
- `CLIRuntime` 默认只读；写/执行需要显式 flag；
- `TestRuntime` 只用于测试，不得进入生产入口。

**v1 的 streaming/jobs 范围（P0-4）**：

| 能力 | Local | Remote | CLI |
|------|-------|--------|-----|
| final-result shell | ✅ core | ✅ core | ✅ core |
| stdout/stderr 流式推送 | ✅ 现有本地 surface | ❌ 不支持 | ❌ 不支持 |
| interactive stdin / PTY | ✅ 现有本地 surface | ❌ 不支持 | ❌ 不支持 |
| background job / detach | ✅ 现有本地 surface | ❌ 不支持 | ❌ 不支持 |

- v1 的 core `Handler` 只承诺 final-result；`bash` 的 interactive/detach 与
  `job_*` 继续留在本地 `toolkit/tools`，不进入 core registry；
- 远程 `shell` 只接受一次性命令，返回最终 exit code + stdout/stderr；
  不暴露 `detach`、`stdin`、PTY、`job_*`；
- 如果未来远程必须支持流式/交互，再新增 `StreamSink` / `JobRuntime` 契约与
  `streaming` / `jobs` capability，作为 v2 扩展，不在本方案中伪装支持。

---

## 8. 调用表面

### 8.1 内置 Agent（本地）

- `internal/tools.Manager` 从 core registry 取 Handler；
- 用 `LocalRuntime` 注入；
- 现有 `toolkit.Tool` 接口保留，core Handler 通过 adapter 暴露为 `toolkit.Tool`；
- permission mode / capability gate 仍由 `internal/policy.Engine` 决定，
  core 不复制判定逻辑；
- `formatToolkitResult` 与 output core 保持同一契约。

### 8.2 MCP Server

- `toolkit.NewToolkitMCPServer` 消费 core registry；
- 目标支持 stdio 与 Streamable HTTP（复用 `mcp.NewStreamableHTTPHandler`）；
  **现状是** `cmd/toolkit-mcp-server` 只启动 stdio，HTTP 由
  `cmd/aicli-remote-tools` 自己实现，统一 serve 是后续工作；
- 工具名前缀、server name、auth、audit、access log 是 surface 职责；
- `ToolResult.Metadata` → `CallToolResult.Meta`；`Data` → `ImageContent`；
- 现有 `cmd/toolkit-mcp-server` 与 `cmd/aicli-remote-tools` 都可以退化成薄入口。

MCP 信任规则（P0-1）：

- 客户端不得信任远程自报的 `skip_render_truncation` /
  `model_visible_budget_bytes` / `reserved_tail_bytes` / `artifact_id`；
- 客户端在 `formatMCPResult` 处按 server 配置标注
  `ResultOrigin{Scope:"remote", Target:<server>, TrustLevel:...}`；
- `untrusted_remote` 默认只允许只读工具；写类工具显式拒绝；
- MCP Roots 只能收窄 Runtime 的 workspace/allowlist，不能扩大；
- `tools/list` 先过 surface filter（profile/tools/allow/deny），再暴露给模型。

建议统一入口：

```bash
tool serve --mcp stdio --profile readonly --workspace /srv/app
tool serve --mcp http  --listen 127.0.0.1:18765 --token-env AICLI_REMOTE_TOKEN
tool serve --remote  --profile ops --workspace /home/n9e/itsm/repo
```

### 8.3 远程工具服务

- 与 MCP 表面共用 core registry/Handler；
- `RemoteRuntime` 注入：
  - `Workspace` jail；
  - 远程 command policy；
  - audit/access log；
  - artifact store；
  - HTTP/stdio 生命周期；
- profile/tools filter 在 surface 层；
- 现有 `aicli-remote-tools` 保留为兼容入口，内部改成 `tool serve --remote` 的 wrapper。

远程 v1 明确不支持：

- interactive stdin / PTY；
- `detach` / background job / `job_*`；
- stdout/stderr 流式推送；
- 跨请求 shell 会话状态。

远程 `shell` 只返回 final result；客户端侧的远程 `artifact_read` 只能读
`remote:<target>:art_...` 命名空间；target 内部的 `remote_artifact_read`
只接受裸 `art_...`（见 §9.7）。

### 8.4 独立 CLI

建议新增 `cmd/toolctl`（名称可评审）：

```bash
toolctl list
toolctl describe view
toolctl run view --file main.go --offset 1 --limit 80 --json
toolctl run grep --pattern TODO --path . --json
toolctl serve --mcp stdio
toolctl serve --mcp http --listen 127.0.0.1:18765
```

- `run`：直接调用 Handler，输出 JSON（Result + metadata + error code）；
- `list/describe`：从 registry 生成 schema/能力清单；
- `serve`：MCP 表面；
- 默认只读；写/执行必须显式 `--allow-write` / `--allow-command`；
- `--allow-write` / `--allow-command` 只映射到既有 mode/capability 与
  `AllowToolCallWithContext`，不是第二套授权表；
- v1 的 `run` 只支持 final-result；不提供 interactive PTY / job 子命令；
- exit code：0 成功，2 参数错误，3 策略拒绝，4 执行失败，5 超时。

### 8.5 HTTP/API / ACP / 测试

- HTTP/API：core registry + 一个 surface adapter；复用 JSON Result 契约；
- ACP：把 tool list/call 映射到 ACP 方法；
- 测试：in-memory surface，直接调用 Handler + TestRuntime，不需要起 MCP。

### 8.6 表面职责边界

| 职责 | core | surface |
|------|------|---------|
| 工具 schema/实现 | ✅ | ❌ |
| 名称 prefix / alias | ❌ | ✅ |
| profile/tools filter | ❌ | ✅ |
| 路径/命令权限 | ❌ | ✅ |
| 输出 fold/reducer | ✅（通过 OutputShaper） | ❌ |
| 远程 metadata trust clamp / artifact scope | ✅（output core） | ✅（标注 Origin/Trust） |
| MCP/CLI/HTTP 映射 | ❌ | ✅ |
| 审计/access/metrics | ❌（只发事件） | ✅ |
| session state | 接口 | ✅ |

---

## 9. 输出契约：直接复用本地成熟实现

这是本方案优先级最高、收益最大的一部分。

### 9.1 复用现有资产

| 资产 | 位置 | 复用方式 |
|------|------|----------|
| 每工具预算 | `internal/toolkit/tools/tool_output_budget.go` | 作为 `OutputShaper` 的默认表 |
| 折叠/截断/reducer | `internal/output` | `RenderToolResultContentForModel`、go test/log/JSON/table reducers |
| 元数据 key | `internal/toolresult` | `skip_render_truncation`、`model_visible_budget_bytes`、`reserved_tail_bytes` |
| artifact 归档 | `internal/artifact` / `remotetools.ArtifactStore` | `ArtifactSink` + `ArtifactRef` 命名空间（§9.7） |
| shell head+tail | `ownShellOutputWindow` / `shellOutputBudgetBytes` | 共享 `shellexec` |

现有预算：

| 工具 | 预算 |
|------|------|
| view | 32 KiB（batch 聚合 64 KiB） |
| grep | 32 KiB |
| glob | 16 KiB |
| ls | 16 KiB |
| shell | 32 KiB |
| artifact_read | 32 KiB |

远程当前使用 `foldRemoteOutput` + 256 KiB 默认，和本地不一致；应改为共享预算 + 硬上限。

关键拆分：远程 `MaxOutputBytes` 当前同时承担“capture 上限”和“模型可见窗口”
两个职责（`remotetools/workspace.go` 默认 256 KiB）。v1 必须拆成：

- `CaptureLimit`：保留现有 capture 上限（shell 256 KiB，view/grep 2 MiB）；
- `ModelVisibleCap`：按工具预算 + `RemoteOutputHardCap`（默认 64 KiB）；
- 归档使用完整 capture，模型只看到 `ModelVisibleCap` 内的内容。

否则会出现两种相反的问题：capture 被过早截断导致 artifact 不完整，或
256 KiB 直接进入模型上下文。

注意本地 render 还有独立 backstop：

- `internal/output` 默认 `modelToolTextByteBudget = 12 KiB`；
- tool-declared window 的硬 ceiling 是 `64 KiB`，floor 是 `4 KiB`；
- view/grep/glob/ls/shell 的 16–32 KiB 是 **tool-owned window**，不是对远程
  无条件放行的授权。

### 9.2 输出流水线

```text
Handler.Result (full content)
  │
  ├─ 1. 如果配置了 artifact sink：先归档完整 capture，得到 ArtifactRef
  ├─ 2. 如果 Origin=remote：Meta allowlist + budget clamp + tail 校验（§9.6）
  ├─ 3. 按有效 cap 做 head/tail fold 或 reducer
  ├─ 4. 加合法 reserved tail（receipt/diagnostics）
  ├─ 5. 写 metadata：
  │     is_truncated / suggested_next_offset / artifact_ref
  │     skip_render_truncation / model_visible_budget_bytes / reserved_tail_bytes
  └─ 6. 交给 surface 映射
```

顺序不能反：**先归档，再折叠**，否则折叠后的中间内容无法恢复。

### 9.3 跨 MCP 的 metadata 传播与信任边界（P0-1）

现有链路是通的，但**信任模型是错的**：

```text
core ToolResult.Metadata
  → toolkit.buildMCPCallToolResult: Meta = result.MetadataWithOutputKind()
  → 本地 tools.formatMCPResult: metadata = cloneMetadataMap(result.Meta)
  → render 层读取 skip_render_truncation，直接 pass-through
```

代码证据：

- `internal/toolkit/mcp_server.go:147-151`：远程 metadata 原样投影到 `Meta`；
- `internal/tools/manager.go:969-1010`：`formatMCPResult` 直接
  `cloneMetadataMap(result.Meta)`，没有 trust 过滤；
- `internal/output/tool_result_content.go:526-568`：`skip_render_truncation`
  被解释为“producer owns its window”，不再 fold；
- `internal/output/reserved_tail_test.go:267-285`：明确验证该 pass-through。

这意味着一个被攻陷/配置错误的远程 MCP server 可以返回：

```json
{
  "skip_render_truncation": true,
  "model_visible_budget_bytes": 999999999
}
```

再附带一个巨大 body，**绕过本地 render 层的模型上下文预算**。

因此，本方案把“跨 MCP metadata”定义为：

> **metadata 是不可信提示（untrusted hint），不是授权（authorization）。**

硬性规则：

1. 远程结果的 `Origin` 由本地 surface 标注，不能由远程自报；
2. `skip_render_truncation` 只在以下两种情况下生效：
   - `Origin.TrustLevel == local`（本地可信工具）；
   - 远程 body 已通过 §9.6 的硬上限与 tail 校验；
3. 远程 `Meta` 必须先过 key allowlist（`skip_render_truncation`、
   `model_visible_budget_bytes`、`reserved_tail_bytes`、`is_truncated`、
   `suggested_next_offset`、`artifact_ref`、`artifact_id`、`output_kind`、
   `error_code`、`next_action`、`retriable`）；未知 key 直接丢弃，
   nested `tool_metadata` 同样过滤；`Meta` 总大小上限 64 KiB，
   超过时丢弃整个 `Meta`，按“无声明”处理；
4. `model_visible_budget_bytes` 只能收窄，不能超过本地 tool budget /
   64 KiB ceiling；
5. `reserved_tail_bytes` 必须同时满足：`0 < n <= cap`，且 body 确实以
   该长度的尾部块结束；否则忽略；
6. `artifact_id` / `artifact_ref` 必须带命名空间，见 §9.7；
7. 远程声明的 `is_truncated` / `suggested_next_offset` 只作为显示提示，
   不能改变本地是否 fold；
8. 任何冲突都以本地 trust policy 为准，而不是远程声明。

实现落点：`internal/tools/manager.go` 的 `formatMCPResult` 在返回前调用
`output.SanitizeRemoteResult(origin, body, metadata)`，由它执行 §9.6。
**禁止只改 `RenderToolResultContentForModel`** 而让 `formatMCPResult`
继续无条件透传 `skip` / budget / artifact id。

必须补的契约测试：

```text
remote Result(meta) → MCP Meta → local formatMCPResult
  → output.RenderToolResultContentForModel

1. 正常 remote artifact_ref 保留，body 不被二次 fold；
2. remote skip + 1 MiB body → 本地仍然按硬上限 fold；
3. remote budget=1<<30 → 本地 clamp 到 tool budget/ceiling；
4. remote reserved_tail_bytes 超出 cap → 忽略；
5. remote bare art_... → 归到该 remote target，不能落到 local store；
6. remote:other:art_... → ARTIFACT_SCOPE_DENIED。
```

### 9.4 二进制输出

- `ToolResult.Data` + `MIMEType` → MCP `ImageContent`（现有 `buildMCPCallToolResult` 已支持）；
- CLI：`--json` 输出 base64，或 `--output <file>`；
- HTTP：`Content-Type` + 二进制 body；
- 文本与二进制同时存在时，文本作为说明，二进制作为内容。

### 9.5 “压缩截取”的语义边界

- 本方案的“压缩截取”= **语义折叠/截断/reducer**，不是 HTTP gzip；
- gzip 是传输层，当前未启用；
- 两者可以叠加，但不能混淆：先整形，再由传输层决定是否 gzip。

### 9.6 远程输出硬上限与校验规则（P0-1）

本节是 §9.3 的可执行规则。所有实现必须按此顺序计算，不得先信 `Meta`。

```text
toolWindow =
  Definition.DefaultOutputBudget > 0 ? Definition.DefaultOutputBudget
  : tool_output_budget[tool] > 0      ? tool_output_budget[tool]
  : output.ModelToolTextByteBudget()   // 默认 12 KiB

hardCap =
  min(toolWindow, RemoteOutputHardCap) // 默认 64 KiB，可配置更低

declared =
  Meta["model_visible_budget_bytes"]  // 只读数字，不是授权

cap =
  declared > 0
    ? min(declared, hardCap)
    : hardCap
```

`RemoteOutputHardCap` 由 surface 配置，默认 64 KiB；只能调低，不能高于
`internal/output` 的 ceiling。远程 body 即使带合法 tail，也不得超过该硬上限。
远程路径不应用本地 4 KiB floor；否则一个小 budget 反而会被放大。

规则：

1. `cap` 永远是远程 body 的模型可见上限；`declared` 不能超过 `hardCap`；
   sanitize 后本地 metadata 必须重写为有效 `cap`，不得保留远程超大值；
2. `skip_render_truncation=true` 只有在
   `len(body) <= hardCap` 且（`len(body) <= cap` 或
   `verifiedTail > 0 && len(body) <= cap + verifiedTail &&
   cap + verifiedTail <= hardCap`）时才生效；否则本地忽略该 flag，按 `cap` 重新 fold；
   实现可先把 body cap 降为 `min(cap, hardCap - verifiedTail)` 以保留合法 tail，
   但总输出永远不得超过 `hardCap`；
3. `reserved_tail_bytes` 必须满足 `0 < n <= cap` 且 body 确实以该尾部块结束；
   不满足时按 0 处理，不得把整个 body 当成 tail；
4. `is_truncated` / `suggested_next_offset` 只用于提示，不改变 fold 决策；
5. `artifact_ref` 必须先通过 §9.7 的 scope/target 校验，再写入 metadata；
6. 远程 body 被 fold 时，必须保留 artifact 指针；没有 artifact 时给出
   `next_action` 让模型缩小请求；
7. 本地可信工具的现有行为不变；远程结果的 `Origin.TrustLevel` 默认为
   `trusted_remote`（带 token/TLS 的专用 target）或 `untrusted_remote`
   （普通 MCP server），两者都不得绕过上面的硬上限。

必须覆盖的恶意 metadata 测试：

| 输入 | 期望 |
|------|------|
| `skip=true` + 1 MiB body | 忽略 skip，fold 到 cap |
| `model_visible_budget_bytes=1<<30` | clamp 到 hardCap |
| `reserved_tail_bytes=1<<30` | 忽略 tail |
| `reserved_tail_bytes` 指向不存在的尾部 | 忽略 tail，正常 fold |
| `is_truncated=false` + 超大 body | 仍按 cap fold |
| capture=300 KiB + artifact | 模型 body <= tool cap；artifact 保存完整 capture |
| 远程注入未知 metadata key | 丢弃，不进入本地 metadata |
| 远程裸 `art_...` | 归到该 remote target，不落到 local |
| `remote:other:art_...` | `ARTIFACT_SCOPE_DENIED` |

### 9.7 Artifact 命名空间与所有权（P0-2）

本地与远程是两套存储，不能共用裸 `art_` id：

| 来源 | 存储 | 线上 ID | 读取入口 |
|------|------|---------|----------|
| 本地 | `internal/artifact.Store`（SQLite） | `art_<...>`（兼容保留） | 本地 `artifact_read` |
| 远程 | `remotetools.ArtifactStore`（文件，72h） | `remote:<target>:art_<...>` | 该 target 的 `remote_artifact_read` |

规则：

1. `ArtifactRef` 的 canonical 字符串：
   - local：`art_<...>`（不改成 `local:`，避免破坏现有输出/artifact_read）；
   - remote：`remote:<target>:art_<...>`；
   - local 的 `Target` 必须为空，remote 的 `Target` 必须非空；不满足即
     `ARTIFACT_SCOPE_DENIED`；
2. `artifact_id` 字段仅作为兼容别名；新代码写 `artifact_ref`，
   `artifact_id` 只保留裸 id 部分；
3. 本地 `artifact_read` 只解析裸 `art_...`；收到 `remote:` 前缀返回
   `ARTIFACT_SCOPE_DENIED`；
4. 远程 `remote_artifact_read` 只解析裸 `art_...`；收到 `remote:` 前缀
   直接拒绝，避免递归 namespace；
5. 仅当 `Origin.Scope == remote` 时，远程 MCP 客户端在 `formatMCPResult`
   看到裸 `art_...` 才按当前 server name 补成 `remote:<server>:art_...`；
   `Origin.Scope == local` 的裸 id 仍归 local store，**不得**误判为远程；
6. `remote:<target>` 与当前 Runtime 的 target 不一致时返回
   `ARTIFACT_SCOPE_DENIED`；
7. 本地 `Store.GetOwned` 继续按 session/lineage 校验；远程 store 按
   target 目录隔离；两者都不能通过裸 id 跨 scope 读取。

`ArtifactSink.Put` 返回 `ArtifactRef`，输出层写两处：

```json
{
  "artifact_ref": "remote:238:art_...",
  "artifact_id": "art_..."
}
```

`artifact_read` 只接受 `artifact_ref` 或裸 `artifact_id`；两者同时存在时
`artifact_ref` 优先，且必须与当前 Runtime 的 target 匹配。

---

## 10. 权限、能力与安全

### 10.1 能力与模式分离

```text
Capability（工具属性）: read_fs / write_fs / exec_shell / network / ...
Mode（会话策略）:      default / accept_edits / plan / bypass / dont_ask
Target policy（远程）:  profile / tools / allowlist / pattern / metachar / shellrisk
```

- 工具只声明 capability；
- 是否 allow/ask/deny 由 runtime/surface 决定；
- 不再为远程工具新增独立授权表；
- capability 是跨表面统一分类的基础。

必须区分四类东西，禁止混写：

| 类别 | 例子 | 作用 | 是否授权 |
|------|------|------|----------|
| Capability | `read_fs` / `exec_shell` | 工具行为声明 | ❌ |
| Surface filter | profile / `tools:` / prefix / `untrusted_remote` | 工具是否可见/可暴露 | ❌ |
| Session policy | mode / allowlist / deny / approval | 本次是否 allow/ask/deny | ✅ |
| Runtime policy | sandbox / command policy / env filter | 具体路径/命令是否可执行 | ✅ |

`profile` / `tools:` 只回答“这个工具是否存在”，不能替代 permission mode、
deny list 或 sandbox。

### 10.2 各运行时策略

| Runtime | 路径 | 命令 | 写 | 审批 |
|---------|------|------|----|------|
| Local | `executor.Sandbox` + `toolctx` | 本地 shell 策略 | mode / deny-tool | `policy.Engine` + AskHandler |
| Remote | `Workspace` jail + symlink 复检 | allowlist/pattern/deny/metachar/shellrisk | profile/tools + mode | 远程 target 不弹本地审批；按 target 配置 fail-closed |
| CLI | `--workspace`/`--allowed-root` | `--allow-command`/`--allow-pattern` | 默认只读；显式 `--allow-write` | `--yes` 只确认 ask；不跳过 hard deny |
| Test | temp dir | 显式 | 显式 | 显式 |

远程 target 的 `profile`/`tools` 只裁剪工具面；真正的 allow/deny 仍由
调用方 `policy.Engine`（若存在）与远程 `Sandbox.CheckPath/CheckCommand` 决定。

### 10.3 安全不变量

1. 空 allowlist = 全拒（远程/CLI），不是全放；
2. deny 永远优先于 allow；
3. 路径越界、`..`、symlink 逃逸必须拒绝；
4. 命令元字符与破坏性分类器在 allow 之后仍然生效；
5. env 默认不继承，只透传 allowlist；
6. 超时杀整个进程组；
7. 输出硬上限 + artifact 归档；
8. 策略错误不得被工具实现吞掉；
9. 工具 core 不得直接读写进程环境/全局变量；
10. 未注入的 Runtime 能力 fail-closed；
11. 远程 `Meta` 不可信，必须过 §9.6 的硬上限；
12. artifact 必须按 §9.7 的 scope/target 路由，跨 scope 拒绝；
13. `CanDirectCall`、`--yes`、`bypass_permissions` 都不能绕过
    hard deny、HardAsk、sandbox、command policy；
14. `untrusted_remote` 不得执行 write-like 工具；这是
    `ToolExecutionPolicy.AllowToolInfo` 的硬规则，不是第二张授权表。

### 10.4 工具 schema 与参数安全

- 所有参数在进入 Handler 前统一 schema 校验；
- 未知参数拒绝（`additionalProperties: false`）；
- 路径参数由 `PathResolver` 兜底；
- 命令参数由 `CommandPolicy` 兜底；
- CLI 与 MCP 使用同一份 schema，避免“CLI 能传、MCP 不能传”。

### 10.5 Gate 顺序矩阵（P0-3）

下表是**规范顺序**，实现必须复用现有 `internal/policy.Engine` 与
`ToolExecutionPolicy`，不得在 core 里重写一套。

| # | 检查 | 负责层 | 结果 |
|---|------|--------|------|
| 0 | surface filter：profile / `tools:` / prefix / server trust | surface | 不可见则 `TOOL_NOT_FOUND` |
| 1 | canonical name + alias + collision | registry/surface | 冲突则拒绝加载/调用 |
| 2 | schema validate（含 hook patch 后重跑） | toolargs/surface | `INVALID_ARGS` |
| 3 | permission hook：deny / modify | `policy.Engine` | deny 硬拒；modify 回步骤 2 |
| 4 | static policy：deny → delegation → allowlist → capability scope → read-only → untrusted_remote write deny | `ToolExecutionPolicy.AllowTool` / `AllowToolInfo` | `TOOL_DENIED` / `CAPABILITY_DENIED` |
| 5 | rules（first match wins；deny 硬拒） | `policy.Engine` | `RULE_DENY` |
| 6 | remembered grants（session/project） | `policy.Engine` | allow / 继续 |
| 7 | external-directory gate | `policy.Engine` | ask/deny |
| 8 | read-only auto allow | `policy.Engine` | allow / 继续 |
| 9 | shell breaker / sensitive-write | `policy.Engine` | HardAsk / deny |
| 10 | plan-entry gate | `policy.Engine` | ask / allow |
| 11 | permission mode：default / accept_edits / plan / bypass / dont_ask | `policy.Engine` | allow/ask/deny |
| 12 | callback override（patch 后重新校验） | host callback | 不能绕过 hard constraints |
| 13 | ask 解析：dont_ask 拒绝；bypass 放行但 HardAsk 除外；无 AskHandler = headless deny | `policy.Engine` | allow/deny |
| 14 | execution preflight：路径 sandbox + command policy + env filter | `ToolExecutionPolicy.AllowToolCallWithContext` + Runtime | `PATH_DENIED` / `COMMAND_DENIED` |
| 15 | execute | Handler | 结果/错误 |
| 16 | output shape：归档 → fold → trust clamp → metadata | OutputShaper | 模型可见结果 |
| 17 | surface map：MCP / CLI / HTTP / builtin | surface | 协议结果 |
| 18 | audit / access / metrics | surface/runtime | 事件 |

固定语义：

- **步骤 0 不是授权**；`profile/tools` 只决定“是否存在”；
- **步骤 4 的 deny 恒优先**；`AllowlistEnabled && empty` 等于全拒；
- **步骤 11 的 bypass 只跳过 mode 的 ask/deny**，不跳过步骤 4、9、14；
- **步骤 13 的 HardAsk 不被 bypass 解决**；`dont_ask` 把 ask 变成 deny；
- **步骤 14 必须检查实际目标参数**，不能只信工具名；
- 步骤 5 的显式 allow rule 按现有语义可以短路步骤 9；这是用户显式策略，
  本方案不偷偷改成“shell breaker 恒优先”；若要改，单独提安全变更；
- **direct call / CLI 从步骤 0 走到 14**；`CanDirectCall` 只表示“可被
  非 MCP 表面直接调用”，不是跳过 gate 的开关；
- 远程 `Sandbox.CheckCommand` 是步骤 14 的一部分，不是另一个 allow 表。

### 10.6 各表面的同一性

| 表面 | 前置过滤 | 授权/审批 | 执行前检查 |
|------|----------|-----------|------------|
| builtin Agent | profile / tools / deny | `policy.Engine` + AskHandler | `executor.Sandbox` |
| MCP Server | `tools/list` + profile + trust | 同 engine；server 端不得自授权 | Runtime preflight |
| remote target | `profile` / `tools` | 调用方 engine + target fail-closed | `Workspace` + `Sandbox.CheckCommand` |
| CLI | flags + workspace | 同 engine；`--yes` 仅确认 ask | `AllowToolCallWithContext` |
| direct call | 调用方声明 | 同 engine + approval | 同 Runtime |

同一工具在不同表面允许的**结果**可以不同（例如远程没有 PTY），但
**判定顺序和拒绝语义必须相同**。

---

## 11. 会话状态与执行边界

### 11.1 v1 范围决策（P0-4）

**决策：v1 的 core 只承诺 final-result；streaming / interactive PTY /
background jobs 明确 local-only。**

理由：

- 当前远程 HTTP 是 `Stateless: true`，没有稳定 session；
- 远程部署/构建的主路径只需要“命令 + 最终 exit code + 输出”，不需要 PTY；
- 交互/流式/job 会引入 stdin 生命周期、断连重放、job 所有权、取消语义，
  不适合与“抽取 core”同一个阶段完成；
- 先保证 final-result 的本地/远程一致性，比伪装支持全部 shell 能力更安全。

边界：

| 能力 | v1 归属 |
|------|---------|
| view / grep / glob / ls / write / edit / patch | core |
| shell（一次性命令、final result） | core |
| artifact archive / read | core interface + local/remote store |
| interactive stdin / PTY | 本地 `toolkit/tools`，不进 core |
| stdout/stderr 流式事件 | 本地 surface，不进 core |
| detach / background job / `job_*` | 本地 `toolkit/tools`，不进 core |
| 远程跨请求 shell 状态 | 不支持 |

未来若远程必须支持流式/交互，作为 v2 扩展新增：

```go
type StreamSink interface {
    Stdout([]byte) error
    Stderr([]byte) error
    Progress(Progress)
}

type JobRuntime interface {
    Start(ctx context.Context, spec JobSpec) (JobRef, error)
    Output(ctx context.Context, ref JobRef, offset int) (JobOutput, error)
    Cancel(ctx context.Context, ref JobRef) error
    List(ctx context.Context) []JobRef
}
```

并同时增加 `streaming` / `jobs` capability 与远程 session 所有权。v1 不实现，
也不在 `Definition.Capabilities` 中声明。

### 11.2 本地 `toolctx` 能力矩阵

本地会话态远不止 workspace/read ledger：

| 能力 | 位置 | Local | Remote | CLI |
|------|------|-------|--------|-----|
| session / toolcall / turn / goal / depth | `toolctx` | ✅ | 请求级 | 进程级 |
| workspace root | `toolctx.WorkspaceRoot` | ✅ | target base workspace | `--workspace` |
| allowed roots / read-only roots | `toolctx` + policy | ✅ | workspace jail | flags |
| artifact store | `toolctx.ArtifactStore` | ✅ SQLite | ✅ 文件（72h） | 可选 |
| artifact read scope | `toolctx.ArtifactReadScope` | ✅ session/lineage | ✅ target | 进程级 |
| shell output artifact dir | `toolctx.ShellOutputArtifactDir` | ✅ | 远程 store | 临时目录 |
| generated image output dir | `toolctx.GeneratedImageOutputDir` | ✅ | 按 target 配置 | 可选 |
| file change notifier | `toolctx.FileChangeNotifier` | ✅ | ❌ | ❌ |
| read dedup / read ledger | `toolkit/tools` | ✅ | ❌ | ❌ |
| todos | `toolkit/tools` | ✅ | ❌ | ❌ |
| shell session state | `toolkit/tools` | ✅ | ❌ 不跨请求 | ❌ |

原则：

- 远程默认 stateless，不承诺 dedup/ledger/todos/file change notifier；
- `SessionState` 的核心能力用 typed accessor 暴露，`Get/Set(any)` 只作为
  扩展 bag，不能作为唯一接口；
- 没有稳定 session 时，不伪造“与本地完全一致”；
- 远程 `artifact_read` 的 scope 由 target 决定，不依赖 session。

### 11.3 远程 shell 的 final-result 契约

远程 `shell` 的 v1 输入/输出固定为：

```text
input:  command, cwd?, env?（仅 allowlist）, timeout?
output: exit_code, stdout, stderr, duration_ms, truncated,
        artifact_ref?, error_code?, next_action?
```

规则：

- 不接受 `stdin` / `detach` / `pty` / `job_id` 参数；
- 取消/超时必须杀整个进程组；
- 输出先归档再折叠；
- `exit_code != 0` 不是协议错误，除非调用方明确要求非零即失败；
- 429/断连/KeepAlive 由 MCP client 处理，不进入工具语义；
- 远程不保存跨请求 shell 状态；每次调用都是新进程。

---

## 12. 可观测性

### 12.1 统一事件

每次调用在 surface/runtime 边界产生一条事件：

```json
{
  "surface": "remote",
  "tool": "remote_238__grep",
  "capability": ["read_fs"],
  "session_id": "...",
  "tool_call_id": "call_...",
  "principal": "bearer:9b0c0c18",
  "remote_addr": "172.18.3.100:61450",
  "decision": "allow",
  "policy_rule": "allowedCommandPatterns:...",
  "origin": "remote",
  "trust_level": "trusted_remote",
  "duration_ms": 12,
  "bytes_out": 4096,
  "truncated": false,
  "artifact_ref": "remote:238:art_...",
  "error_code": ""
}
```

### 12.2 分工

| 层 | 职责 |
|----|------|
| toolcore | 产生执行结果/错误码；不写日志 |
| Runtime | 路径/命令决策、artifact、session |
| Surface | audit/access/metrics、identity、transport |
| CLI | JSON 输出 + exit code |
| MCP | `Meta` + `IsError` |
| HTTP | status code + JSON |

### 12.3 指标

- calls_total / failed / denied；
- bytes_in / bytes_out（整形前后）；
- latency；
- per-tool / per-surface / per-capability；
- artifact 命中/未命中；
- 远程 KeepAlive/重连/429。
- remote metadata clamp/reject 计数；
- artifact scope denied 计数。

避免在 toolcore 里重复埋点；统一由 surface/runtime 记录。

---

## 13. 迁移路线

迁移原则：**小步、可回滚、每阶段都能独立构建与验证**。不要一次性重写所有工具。

### Phase 0：冻结契约与基线

交付：

- `internal/toolcore` 空壳 + `Definition`/`Handler`/`Runtime`/`Result` 接口；
- `toolkit.Tool` → `toolcore.Handler` 的兼容 adapter；
- `ArtifactRef` / `ResultOrigin` / trust clamp 的契约测试骨架；
- 一组 conformance fixtures（文本/二进制/大文件/长行/ignore/symlink/图片/notebook/Unicode/CRLF）；
- 现有本地工具输出基线快照。

验收：

- 现有工具行为不变；
- `go test ./...` 通过；
- 新接口只新增、不破坏。

### Phase 1：输出 core（最高优先级）

交付：

- 抽 `internal/toolcore/output`：
  - 预算表；
  - head/tail fold；
  - reducers；
  - reserved tail；
  - metadata keys；
  - `ArtifactSink` + `ArtifactRef` 命名空间；
  - remote trust clamp（§9.6）；
- 远程 `view/grep/glob/ls/shell` 改用共享 output；
- 删除 `foldRemoteOutput` 的重复逻辑；`boundedWriter` 若仍用于进程 capture，
  只保留 capture 职责，不再决定模型输出形状；
- 补 MCP metadata 跨边界契约测试（含恶意 metadata / artifact scope）。

验收：

- 同一输入，本地/远程 body + metadata 一致（路径/artifact id 归一化后）；
- 不再二次 fold；
- 远程 wire 大小与本地面向模型预算一致；
- artifact 归档先于折叠；
- 远程 `skip` / 超大 budget / 虚假 tail 全部被本地拒绝或 clamp；
- 裸 `art_...` 不会跨到 local store，`remote:other:` 被拒绝。

### Phase 2：只读 core

交付：

- `toolcore/viewcore`：行窗口、编码、二进制/图片、长行 clamp、batch；
- `toolcore/searchcore`：rg runner + fallback、glob、ignore、行预算；
- `toolcore/lscore`；
- 本地/远程 adapter 只注入 `PathResolver`/`OutputShaper`；
- 远程 grep/glob 复用 `internal/ripgrep`，无 rg 时 fallback。

验收：

- grep/view/glob/ls conformance 全绿；
- 大仓库 benchmark 与本地差距在阈值内；
- `.gitignore`、hidden、binary、大文件、长行行为一致或明确标注差异。

### Phase 3：写/编辑/patch core

交付：

- `toolcore/mutatecore`：write/edit/append/apply_patch、atomic write、编码、mutation guard；
- 本地/远程 adapter 注入 `PathResolver` + `PermissionChecker`；
- 复用现有 patch parser 与 receipt/output 契约。

验收：

- 本地/远程写结果、错误码、receipt 一致；
- 路径 jail、symlink、特殊文件、编码行为一致；
- 幂等/并发写测试通过。

### Phase 4：shell core（final-result only）

交付：

- `toolcore/shellexec`：进程组、超时、env filter、capture writer、artifact tee、head/tail；
- 本地/远程 adapter 注入 `CommandPolicy`；
- 复用 shell output reducers（go test/log/JSON/table）。

不交付（v1 local-only，见 §11.1）：

- interactive stdin / PTY；
- detach / background job / `job_*`；
- stdout/stderr 流式事件。

验收：

- 本地/远程 shell 输出、截断、artifact、exit code、超时行为一致；
- 远程 allowlist/pattern/metachar/shellrisk 不放松；
- 断连/取消杀进程组测试通过；
- 远程 schema 不包含 `stdin` / `detach` / `pty` / `job_id`。

### Phase 5：CLI 与统一 serve

交付：

- `cmd/toolctl`（或 `aicli tool`，见开放问题）；
- `toolctl list/describe/run/serve`；
- `tool serve --mcp stdio|http`；
- `cmd/toolkit-mcp-server`、`cmd/aicli-remote-tools` 变薄 wrapper（保留兼容）。

验收：

- CLI、MCP、远程三种表面调用同一工具，结果一致；
- CLI JSON 输出含 metadata/error_code；
- 退出码契约稳定；
- CLI 不提供 interactive PTY / job 子命令；
- 旧命令仍可用。

### Phase 6：收敛与删除重复

交付：

- `internal/remotetools` 只保留 RemoteRuntime + server shell；
- `internal/toolkit/tools` 只保留 LocalRuntime adapter/本地专属能力；
- interactive / PTY / detach / `job_*` 明确留在本地专属实现；
- 删除重复的 view/grep/glob/ls/shell/write/edit/patch 实现；
- 更新文档与测试。

验收：

- 代码搜索不再出现第三套同名工具实现；
- 本地/远程工具差异只由 Runtime/feature matrix 解释。

---

## 14. 文件级改动地图

| 路径 | 动作 |
|------|------|
| `internal/toolcore/contract.go` | 新增 Definition/Handler/Runtime/Result/Capability/ArtifactRef/ResultOrigin |
| `internal/toolcore/registry.go` | 新增 core registry（或适配 `toolkit.Registry`） |
| `internal/toolcore/output/` | 新增共享输出整形（预算/fold/reducer/artifact/trust clamp） |
| `internal/toolcore/viewcore/` | 新增 view 核心 |
| `internal/toolcore/searchcore/` | 新增 grep/glob 核心 |
| `internal/toolcore/lscore/` | 新增 ls 核心 |
| `internal/toolcore/mutatecore/` | 新增 write/edit/append/patch 核心 |
| `internal/toolcore/shellexec/` | 新增 shell 执行核心 |
| `internal/toolkit/interface.go` | 保留 `Tool` 兼容接口；增加 adapter |
| `internal/toolkit/tools/*` | 改为 LocalRuntime adapter 或薄 wrapper；interactive/PTY/job 留在这里 |
| `internal/remotetools/*` | 改为 RemoteRuntime + surface；删除重复工具实现；artifact 使用 namespaced ref |
| `internal/remotetools/artifact.go` | 增加 target scope；输出 `remote:<target>:art_...` |
| `internal/remotetools/workspace.go` | 拆分 `CaptureLimit` 与 `ModelVisibleCap`，不再让 `MaxOutputBytes` 兼任两者 |
| `internal/tools/manager.go` | 从 core registry 消费；`formatMCPResult` 增加 trust 过滤与 artifact 归属 |
| `internal/toolkit/mcp_server.go` | 继续做 MCP 投影；补 metadata/binary 契约测试 |
| `cmd/toolkit-mcp-server` | 保留兼容；内部改 `tool serve --mcp stdio` |
| `cmd/aicli-remote-tools` | 保留兼容；内部改 `tool serve --remote` |
| `cmd/toolctl` | 新增 CLI 表面 |
| `internal/output`, `internal/toolresult` | 保持共享；必要时抽到 `toolcore/output` 并保留旧名 alias |

---

## 15. 测试与性能保证

### 15.1 功能一致性

1. **Conformance corpus**
   - 同一 fixture 同时跑 Local/Remote/CLI/MCP；
   - 归一化路径、artifact id、时间戳后比较 body + metadata；
   - 覆盖：文本、二进制、图片、notebook、symlink、`.gitignore`、hidden、大文件、长行、Unicode、CRLF、空文件。
2. **Golden tests**
   - 每个工具的预算/截断/reducer/reserved tail 快照；
   - MCP metadata 跨边界；
   - CLI JSON/exit code。
3. **Capability matrix**
   - 本地专属能力显式 `UNSUPPORTED`；
   - 不允许静默降级。
4. **Security tests**
   - 路径穿越、symlink、设备文件、命令注入、元字符、deny 优先、env filter、超时、进程组清理；
   - 本地/远程策略分别测，不能只测一边。
5. **Trust boundary tests（P0-1）**
   - 远程 `skip_render_truncation` + 超大 body → 本地仍 fold；
   - `model_visible_budget_bytes` 超大 → clamp 到 hardCap；
   - 虚假/超长 `reserved_tail_bytes` → 忽略；
   - 远程 `is_truncated=false` + 超大 body → 仍 fold；
   - 本地可信工具的 pass-through 行为不变。
6. **Artifact scope tests（P0-2）**
   - 本地裸 `art_...` 可读；
   - `Origin=local` + 裸 `art_...` 归 local；
   - `Origin=remote` + 裸 `art_...` 归当前 remote target；
   - `remote:238:art_...` 只能读 238；
   - `remote:other:art_...` → `ARTIFACT_SCOPE_DENIED`；
   - 远程裸 `art_...` 自动归到当前 target，不落到 local store；
   - 远程 `artifact_read` 收到 `remote:` 前缀 → 拒绝；
   - 本地 `Store.GetOwned` 的 session/lineage 检查仍生效。
7. **Gate order tests（P0-3）**
   - deny 优先于 allow/contractual/plan exemption；
   - bypass 仍被 hard deny / HardAsk / sandbox / command policy 拦住；
   - dont_ask 把 ask 变 deny；
   - direct call / CLI 与 builtin 走同一顺序；
   - profile/tools 过滤不改变 allow/deny 结果。
8. **Local-only matrix tests（P0-4）**
   - remote schema 不含 `stdin`/`detach`/`pty`/`job_id`；
   - remote 调用这些参数返回 `INVALID_ARGS`/`UNSUPPORTED`；
   - CLI 不暴露 job/PTY 子命令；
   - 本地 interactive/detach/job 回归测试保持通过。

### 15.2 性能

1. **grep/glob**
   - 远程优先 rg，fallback pure Go；
   - 流式输出直接进入预算，避免全量 buffer；
   - benchmark：10k/100k 文件、100MB 仓库、binary/hidden/ignore。
2. **view**
   - 大文件长行、2000 行窗口、UTF-8 clamp；
   - 与本地同 fixture 的 alloc/耗时对比。
3. **shell**
   - 大输出 fold、artifact tee、超时/取消；
   - 进程组清理。
4. **表面开销**
   - MCP stdio/http、CLI、本地直调；
   - 目标：接口抽象带来的额外开销在 p95 < 5%（以 benchmark 为准）。
5. **CI 门槛**
   - 关键 benchmark 设回归阈值；
   - pprof 在远程 binary 保留开关。

### 15.3 跨平台

- Windows/Linux 都要跑 conformance；
- 路径大小写、分隔符、symlink、可执行后缀、进程组语义分别覆盖；
- 远程 Linux binary 与本地 Windows 客户端组合测试。

---

## 16. 风险与开放问题

### 16.1 风险

| 风险 | 说明 | 缓解 |
|------|------|------|
| 抽象泄漏 | 工具参数出现 runtime 专属字段 | schema 评审 + conformance |
| 策略错位 | core 里偷偷做本地 sandbox 判断 | 禁止 core import executor/toolctx；接口注入 |
| 双重截断 | 远程已 fold，本地 render 再 fold | `skip_render_truncation` 契约测试 |
| 状态差异 | 远程 stateless，本地有 dedup/ledger | capability matrix；不伪造一致 |
| 迁移范围大 | 一次性重写风险高 | 分阶段 + 兼容 adapter + 回滚 |
| 性能回退 | 接口/中间件开销 | benchmark + pprof + 流式处理 |
| 二进制/图片 | MCP/CLI 映射差异 | 统一 Result + 各 surface 映射测试 |
| 本地专属能力 | code index/knowledge/web search | 明确 local-only |
| shell 安全 | 共享执行 core 误带本地默认 | 策略完全注入；远程 fail-closed |
| 兼容性 | 旧工具名/schema 变化 | aliases + 版本化 + 双跑 |
| 远程 metadata 注入 | 伪造 skip/budget/tail 绕过本地预算 | §9.6 硬上限 + 恶意 metadata 测试 |
| artifact 跨 scope | 裸 `art_` 被本地/其他 target 读取 | §9.7 namespace + `GetOwned` + 拒绝测试 |
| 第二授权层 | core 又写一套 allow/deny/mode | 只复用 `policy.Engine`；§10.5 顺序矩阵 |
| v1 范围蔓延 | 把 PTY/job/streaming 混入 core | §11.1 明确 local-only；schema 拒绝相关参数 |
| gate 顺序漂移 | 不同 surface 各走一套顺序 | §10.5/§10.6 同一性测试 |

### 16.2 开放问题

1. **包名与 CLI 名**：`toolcore` / `toolctl` 是否合适？是否并入 `aicli tool`？
2. **接口归属**：保留 `toolkit.Tool` 作为兼容接口，还是让 core 成为唯一接口？
3. **SessionState**：v1 已决定远程 stateless；stateful/dedup/ledger 是否作为 v2，待定。
4. **profile/filter**：放在 surface 还是 core registry？建议 surface。
5. **图片/notebook**：远程是否要求与本地完全一致？MCP 已支持 ImageContent，但 CLI/HTTP 需要各自映射。
6. **jobs/artifacts**：v1 只统一 artifact **接口与命名空间**，不统一存储实现；
   本地 SQLite、远程文件（72h）。jobs 为 local-only，不进入 core。
7. **直接 import 整个 tools 包**：结论是不建议；抽 core 更稳。若未来 binary 大小完全无所谓，也应保留 Runtime 注入边界。
8. **工具版本/schema 演进**：是否需要 `Definition.Version` + deprecated aliases？
9. **审计归属**：core 发事件 vs surface 写文件；建议 surface 写，core 只发结构化事件。
10. **性能目标**：p95 开销阈值定多少？建议先用 benchmark 基线，再定 3–5%。

---

## 17. 第一批落地建议（可独立 PR）

### PR 1：Output core + remote 接入

1. 新增 `internal/toolcore/output`：
   - 预算表（复用 `tool_output_budget.go` 数值）；
   - fold/reducer/reserved tail；
   - metadata key 常量；
   - `ArtifactSink` + `ArtifactRef`；
   - `ResultOrigin` + `RemoteOutputHardCap` + trust clamp。
2. `internal/remotetools` 的 `view/grep/glob/ls/shell` 改用共享 output；
3. `internal/tools/manager.go` 的 `formatMCPResult` 增加远程 trust 过滤与
   artifact 归属；裸 `art_...` 按 server name 归到 remote target；
4. `internal/remotetools/artifact.go` 输出 `remote:<target>:art_...`；
5. 删除 `foldRemoteOutput` 的重复逻辑；`boundedWriter` 如仍用于 capture，
   只保留 capture 职责；
6. 补契约测试：
   - remote Result(meta) → MCP Meta → local formatMCPResult → render 不二次 fold；
   - artifact 先归档后折叠；
   - 恶意 skip/budget/tail 被 clamp/忽略；
   - 跨 scope artifact 被拒绝；
   - 本地/远程同输入输出一致。
7. 重建 `aicli-remote-tools`，部署 238，跑 conformance。

验收：

- 输出预算与本地一致；
- 远程 wire 大小下降/稳定；
- 无二次截断；
- 恶意 metadata 不能突破 64 KiB ceiling；
- artifact 不跨 local/remote/target；
- 现有测试全绿。

### PR 2：Read-only core

- view/grep/glob/ls 共享 core；
- 远程复用 `internal/ripgrep` + fallback；
- conformance corpus + benchmark。

### PR 3：CLI surface

- `toolctl list/describe/run`；
- 复用 core + CLIRuntime；
- `--yes` 只确认 ask，不跳过 deny / HardAsk / sandbox；
- JSON/exit code 契约。

### 估算（粗略）

| 阶段 | 估算 |
|------|------|
| Phase 1 output（含 trust/artifact） | 2–3 天 |
| Phase 2 read-only | 3–5 天 |
| Phase 3 mutation | 3–5 天 |
| Phase 4 shell | 3–5 天 |
| Phase 5 CLI/serve | 2–3 天 |
| Phase 6 收敛/测试 | 2–4 天 |

估算只用于排期，不构成承诺；实际取决于 conformance 覆盖范围。

---

## 18. 相关文档

- 本方案审核：[`tool-layer-abstraction-design-20261022-review.md`](tool-layer-abstraction-design-20261022-review.md)
- `docs/plan/aicli-remote-tools-ssh-http-design-20261021.md`
- `docs/plan/aicli-remote-tools-permissions.md`
- `docs/plan/aicli-tool-capability-convergence-plan.md`
- `docs/plan/artifact-read-contract-and-tool-surface-closure-analysis-20261018.md`
- `docs/user-guide/aicli-remote-tools.md`

---

## 附录 A：238 当前映射（示例）

当前 238 的 surface filter：

```yaml
profile: custom
tools:
  - view
  - ls
  - glob
  - grep
  - shell
  - artifact_read
```

映射到本方案：

| 层 | 当前 |
|----|------|
| core registry | 当前 14 个内置工具（read + write + shell + job + artifact）；v1 只迁移 read/write/final shell/artifact |
| RemoteRuntime | `/home/n9e/itsm/repo` + `/tmp` + `/var/log` jail；远程命令策略；audit；ArtifactStore |
| Remote surface filter | `profile: custom` + `tools` → 只保留 6 个 |
| 写工具 | 被 filter 裁掉（不是实现不存在） |
| job_* | 当前 `jobs.root` 为空 + tools 未列 → 不暴露；v1 仍为 local-only，不进入 core |
| shell | 保留；由 RemoteRuntime 的 allowlist/pattern/metachar/shellrisk 管控 |

因此“增加更多工作”在本方案里是：

- 增加 surface filter 条目（如 `write`、`job_*`）；
- 或修改 RemoteRuntime 策略（allowlist/pattern）；
- 不需要再写一份 view/grep/shell。

---

## 附录 B：当前重复的代码证据

```powershell
# 远程工具 binary 不包含本地 toolkit/tools
go list -deps ./cmd/aicli-remote-tools | Select-String 'toolkit/tools'
# → 无输出

# 本地 aicli 包含 toolkit/tools
go list -deps ./cmd/aicli | Select-String 'toolkit/tools'
# → .../internal/toolkit/tools

# 本地 grep 使用 ripgrep，远程 grep 自己 walk
Select-String -Path internal/toolkit/tools/grep.go -Pattern 'internal/ripgrep'
# → 命中

Select-String -Path internal/remotetools/search.go -Pattern 'internal/ripgrep'
# → 无命中（remote grep 只用 regexp + WalkDir）
```

补充：

- 远程 `search.go` 只 import `internal/toolkit`（BaseTool/Result），没有 import `internal/toolkit/tools`；
- 远程 `shell.go` 有独立的 `foldRemoteOutput` / `boundedWriter`；
- 本地输出治理在 `internal/toolkit/tools/tool_output_budget.go` + `internal/output` + `internal/toolresult`。

这些就是本方案要消除的重复点。

---

## 附录 C：术语

| 术语 | 含义 |
|------|------|
| Tool / Handler | 一个工具的定义 + 执行实现 |
| Definition | 名称、schema、描述、能力、预算 |
| Runtime | 工具运行所需的环境接口（路径/权限/命令/输出/artifact/session） |
| Surface | 调用工具的外部形态：builtin/MCP/remote/CLI/HTTP |
| Adapter | Surface 与 core 之间的映射层 |
| Capability | 工具行为属性（read_fs/write_fs/exec_shell/...） |
| Policy | 是否允许调用的策略（mode/target policy） |
| OutputShaper | 统一的预算/折叠/reducer/artifact 输出治理 |
