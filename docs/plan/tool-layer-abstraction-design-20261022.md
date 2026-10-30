# 工具层抽象设计：一份定义，多种调用表面

- 状态：Proposal / P0+P1 design frozen（2026-10-22）；P2 backlog；PR0 next
- 日期：2026-10-22
- 范围：`internal/toolkit`、`internal/toolkit/tools`、`internal/remotetools`、`internal/tools`、`cmd/toolkit-mcp-server`、`cmd/aicli-remote-tools`，以及后续 CLI/HTTP 表面
- 目标：把“工具”抽成独立逻辑层，让同一份工具定义与实现可以被内置 Agent、MCP Server、远程工具服务、独立 CLI、HTTP/API 等表面复用

> 设计门：P0/P1 内容已冻结，进入 **PR0 契约验证**；不再重复设计复审。
> 新发现仅在**可复现 P0 安全缺陷**时重开设计，其他进入 PR backlog。
> PR1 开发门槛：PR0 + P0 测试；生产/canary 门槛：独立安全评审 + PR1 证据。
> 审核报告：[`tool-layer-abstraction-design-20261022-review.md`](tool-layer-abstraction-design-20261022-review.md)。

### P0 修订状态

| 编号 | 项目 | 本次处理 | 状态 |
|------|------|----------|------|
| P0-1 | 远程输出 metadata 信任边界 | 新增 §9.3/§9.6：远程 `Meta` 视为不可信提示，本地硬上限 + clamp + tail 校验 | 已补，design frozen；PR1 验证 |
| P0-2 | Artifact 命名空间与所有权 | 新增 §9.7 与 `ArtifactRef`：local 裸 id、remote `remote:<target>:<id>`，跨 scope 拒绝 | 已补，design frozen；PR1 验证 |
| P0-3 | Capability / Policy Gate 顺序 | 重写 §10.1–§10.6：复用现有 `policy.Engine`，给出固定 0–18 顺序，禁止第二授权层 | 已补，design frozen；PR3 验证 |
| P0-4 | Streaming / Jobs / Interactive | 决策为 **v1 final-result only**；streaming/PTY/jobs 明确 local-only，见 §11.1 | 已补，design frozen；PR1 验证 |

### P1 修订状态

| 项目 | 位置 | 状态 |
|------|------|------|
| 工具清单与迁移矩阵 | §13.0 | 已补 |
| MCP surface 规格 | §8.7 | 已补 |
| 错误/重试/幂等 | §10.7 | 已补 |
| 可观测性接口 | §12.4 | 已补 |
| 版本与 rollout | §13.7 | 已补 |
| 威胁模型 | §16.3 | 已补 |
| 性能语料与初始阈值 | §15.2.1/§15.2.2 | 已补 |
| CLI/direct 完整契约 | §8.4.1/§10.6 | 已补；approval/JSON/exit code |
| 网络类工具迁移计划 | §13.8 | 已明确 v1 deferred、N1–N5 顺序 |
| P0/P1/PR1–3 验收矩阵 | §17.4 | 已补；文档门/实现门分离 |
| 最佳实践验收/发布模型 | §17.5 | 已补；DoR/DoD、测试分层、追溯、安全评审、canary/no-go |

P1 表内“已补”表示随 P0/P1 **design freeze** 冻结；后续差异走 PR backlog，
不再以重复设计复审推进。

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
- `InputSchema` 是 canonical JSON；旧 `toolkit.Tool.Parameters()` 的
  `map[string]interface{}` 由 adapter marshal + canonicalize 后填入，
  反向读取时 unmarshal，保证两岸 schema 完全一致；
- `Version` 使用语义化版本；参数/语义变更必须升版本（见 §13.7）。

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

兼容 adapter（`toolkit.Tool` ↔ `toolcore.Handler`）：

- `Name` / `Description` / `Version` 直接映射；
- `Parameters()` ↔ `InputSchema`：marshal + canonicalize / unmarshal；
- `Execute(ctx, args)` ↔ `Execute(ctx, call)`：args 由 `toolargs` 归一；
- `CanDirectCall()` ↔ `Definition.DirectCall`：只影响暴露，不影响授权。

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
- 完整错误码/重试/幂等映射见 §10.7；
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
- `Clock` 用于 duration/timeout/deadline 的可测试时间源；`IDs` 用于
  `ToolCallID`/request id/artifact 临时 id；TestRuntime 必须可确定性注入；
- 任何未注入的能力默认 fail-closed（返回 `UNSUPPORTED`/`DENIED`）。

### 6.5 Registry

Registry 负责：

- 注册/注销；
- 别名解析（`read` → `view`）；
- schema canonicalize + revision；
- 按 capability / namespace / names 列出；
- 冲突检测（同名、alias 冲突、canonical name 预算）。

```go
type Registry interface {
    Register(Handler) error
    Unregister(name string) error
    Resolve(name string) (Handler, bool)
    List(filter Filter) []Handler
    Schema(name string) (json.RawMessage, bool)
}

type Filter struct {
    Capabilities []Capability
    Names        []string
    Namespace    string // optional; e.g. "code" or "remote:238"
}
```

**注意**：profile（readonly/standard/ops/custom）、`tools:` allowlist、远程 prefix
都是 **surface filter**，不是 core `Registry.Filter` 的一部分；surface 先
`List`，再按 profile/tools 裁剪。

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
- `detach` / background job / `job_*`（v1 core 不支持；现有 remote
  `job_*` 仅作 legacy 扩展，默认关闭）；
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
- exit code：0 成功，2 参数错误，3 策略拒绝，4 执行失败，5 超时，
  6 限流/不可用。

#### 8.4.1 Direct / CLI 契约

- `CanDirectCall` 只表示“可被非 MCP 表面直接调用”，不是权限；direct 调用
  仍走 §10.5 的 0–14；
- `run` 每次只执行一个工具调用；参数用 `--args-json` 或显式 flag；
- `--json` 固定输出：

```json
{
  "tool": "view",
  "ok": true,
  "output": "...",
  "mime_type": "text/plain",
  "metadata": {},
  "error_code": "",
  "next_action": "",
  "retriable": false,
  "artifact_ref": ""
}
```

- 二进制结果输出 base64 + `mime_type`；大文本按 output core 的预算折叠；
- `--yes` 只确认 `ask`，不得跳过 deny/HardAsk/sandbox；
- 不提供 `--dangerously-bypass`；如未来需要，必须单独安全评审；
- stderr 只放诊断/日志，stdout 只放结果，保证可管道消费。

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

### 8.7 MCP Surface 规格（P1）

#### 8.7.1 Transport 与认证

| 模式 | 当前 | 目标 | 认证 |
|------|------|------|------|
| stdio | `cmd/toolkit-mcp-server` | 统一 `tool serve --mcp stdio` | 进程管道；trust 由 server 配置决定 |
| Streamable HTTP | `cmd/aicli-remote-tools` | 统一 `tool serve --mcp http` | 回环默认；非回环必须 token |

规则：

- HTTP 默认只监听回环；非回环监听必须配置 `AICLI_REMOTE_TOKEN` 或等价
  secret，不允许把 token 放在 URL query；
- 跨网段/公网必须 TLS 或 mTLS；token 轮换不要求重启进程；
- **stdio 不自动等于 trusted local**：本地 `toolkit-mcp-server` 是 local；
  通过 SSH 启动的 `aicli-remote-tools --stdio` 仍是 remote，按 target 配置；
- HTTP 的 `trustLevel` 由 target 配置决定：`trusted_remote` /
  `untrusted_remote`；
- `untrusted_remote` 默认只读；write-like 工具在
  `ToolExecutionPolicy.AllowToolInfo` 处硬拒；
- 远程 metadata 仍按 §9.3/§9.6 处理，认证不等于可信输出。

#### 8.7.2 Roots 与会话

- `roots/list` 只能**收窄** workspace / allowed roots；每个 root 必须落在
  server 配置的允许根内，不能扩大；
- 请求未声明 roots 时使用 base workspace；
- HTTP 默认 stateless：不承诺 dedup/ledger/todos/file change notifier；
- KeepAlive 30s；`CallTool` 前同步 `ensureClient` 重连；
- 断连后不自动重放已经发出的写/执行请求；只有明确的只读调用才可在
  重连后重试一次；
- 429 必须带 `Retry-After`，客户端不得紧循环重试。

#### 8.7.3 名称、schema 与列表

- canonical name：`mcp__<server>__<tool>`；远程：`remote_<target>__<tool>`；
- canonical name 上限 64 字符；超长用稳定 hash 截断，不静默截断；
- 本地 builtin 与远程同名冲突时，远程工具拒绝加载（现有
  `validateRemoteToolNameCollision` 行为）；
- `tools/list` 只列 canonical name；`read`/`rg`/`bash` 等别名可调用但不重复列出；
- 描述默认上限 4 KiB，schema 默认上限 64 KiB，单 server 默认最多 256 个工具；
- 不可信 server 的 description/schema 只作为数据；不得把其中文本当指令执行；
- `tools/list` 先过 profile/tools/deny/trust filter，再给模型。

#### 8.7.4 调用、错误与限制

- `tools/call` 与 builtin 走同一 gate（§10.5），server 端不得自行授权；
- 工具业务失败用 `IsError=true` + `Meta.error_code/next_action/retriable`；
  协议/传输失败用 JSON-RPC/HTTP 错误，不混用；
- HTTP 状态：`400` 参数、`401/403` 认证/策略、`404` 工具、`408` 超时、
  `409` 状态冲突、`429` 并发/限流、`500` 内部、`503` 不可用；
- 默认并发上限 8；达到上限返回 429 + `Retry-After`；
- 请求体、`Meta`、输出 body 均有硬上限；超限不得靠“客户端会截断”兜底；
- `MaxMCPResponseBytes`（默认 8 MiB，可调低）必须在 JSON decode **之前**
  由 transport 层执行；超限返回 `REMOTE_RESPONSE_TOO_LARGE`，不进入 fold、
  不重试；
- access log 记录 principal/token hash、remote addr、tool、decision、
  duration、bytes、error_code、artifact_ref；不记录原始 token/完整命令。

**现状**：`cmd/toolkit-mcp-server` 仍只 stdio；HTTP 由
`cmd/aicli-remote-tools` 独立实现。`tool serve` 统一入口是目标，不是现状。

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

`origin` 必须由调用方显式传入，不能从 `source` 字符串猜：

```go
type MCPResultContext struct {
    Server         string // mcp server / target name
    TrustLevel     string // local | trusted_remote | untrusted_remote
    ExecutionScope string // local | remote
}
```

映射来源是 `internal/config` 的 `MCPTrustLevel` / `ExecutionMode`
（`mcpregistry.ToolInfo` 已有 `MCPTrustLevel` 字段），不是远程 `Meta`：

| server 配置 | Origin |
|-------------|--------|
| 本地 toolkit stdio | `local` |
| SSH 启动的 remote stdio | `remote` + `trusted_remote` |
| HTTP loopback + token | `remote` + `trusted_remote` |
| HTTP 非回环 + TLS/mTLS | `remote` + `trusted_remote` |
| 普通第三方 MCP | `remote` + `untrusted_remote` |

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
7. 同一 body 在 Origin=local 与 Origin=remote 下行为不同：local 可 pass-through，
   remote 必须 clamp；
8. 伪造 source 字符串不能改变 TrustLevel；trust 只能来自 server config。
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

`RemoteOutputHardCap` 只约束**模型可见 body**；wire 层另有
`MaxMCPResponseBytes`（默认 8 MiB，transport/config 可调低），在 JSON
decode 前执行。两者不可互相替代：没有 wire cap，恶意 server 可以在本地
fold 之前用超大响应耗尽内存。

`view` 的普通窗口是 32 KiB，`files[]` 批量聚合窗口是
`viewBatchAggregateBudgetBytes`（64 KiB）；批量窗口必须由工具/参数语义决定，
不能只凭远程 `Meta` 声明。

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
   `trusted_remote`（专用 target，含 SSH stdio / token / TLS）或
   `untrusted_remote`（普通 MCP server），两者都不得绕过上面的硬上限。

必须覆盖的恶意 metadata 测试：

| 输入 | 期望 |
|------|------|
| `skip=true` + 1 MiB body | 忽略 skip，fold 到 cap |
| wire body 超过 `MaxMCPResponseBytes` | `REMOTE_RESPONSE_TOO_LARGE`，不 decode/fold/重试 |
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
8. ID 必须严格匹配 `^art_[0-9a-f]{32}$`；target 必须匹配
   `^[a-z0-9][a-z0-9_.-]{0,63}$`；不匹配直接拒绝，禁止路径拼接；
9. target 名称一旦发布必须稳定；重命名 target 视为不同 scope，旧 ref 失效；
10. 归档失败时：如果结果需要 artifact 才能恢复被折叠内容，返回
    `ARTIFACT_WRITE_FAILED`，不得返回“看似成功但中间丢失”的 body；
11. artifact 过期/不存在返回 `ARTIFACT_EXPIRED` / `NOT_FOUND`，并给出
    “重新执行命令”的 next_action；不得回退到同名本地 artifact。

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
5. env 默认继承目标进程完整环境（凭证类名称过滤）；envAllowlist 改为显式收紧，disableEnvInheritance 可回退平台基线，显式空列表才完全不继承；
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
| 2 | schema validate（含 hook patch 后重跑） | toolargs/surface | `INVALID_ARGUMENT` |
| 3 | permission hook：deny / modify | `policy.Engine` | deny 硬拒；modify 回步骤 2 |
| 4 | static policy：deny → delegation → allowlist → capability scope → read-only → untrusted_remote write deny | `ToolExecutionPolicy.AllowTool` / `AllowToolInfo` | `TOOL_DENIED` / `CAPABILITY_DENIED` |
| 5 | hard safety gates：shell breaker / sensitive-write | `policy.Engine` | HardAsk / deny；不可被 allow rule/callback 放行 |
| 6 | rules（first match wins；deny 硬拒；allow 不得覆盖 safety gate） | `policy.Engine` | `RULE_DENY` |
| 7 | remembered grants（session/project） | `policy.Engine` | allow / 继续 |
| 8 | external-directory gate | `policy.Engine` | ask/deny |
| 9 | read-only auto allow | `policy.Engine` | allow / 继续 |
| 10 | plan-entry gate | `policy.Engine` | ask / allow |
| 11 | permission mode：default / accept_edits / plan / bypass / dont_ask | `policy.Engine` | allow/ask/deny |
| 12 | callback：deny/ask/patch（patch 后重新校验） | host callback | 不能把 policy deny 改为 allow |
| 13 | ask 解析：dont_ask 拒绝；bypass 放行但 HardAsk 除外；无 AskHandler = headless deny | `policy.Engine` | allow/deny |
| 14 | execution preflight：路径 sandbox + command policy + env filter | `ToolExecutionPolicy.AllowToolCallWithContext` + Runtime | `PATH_DENIED` / `COMMAND_DENIED` |
| 15 | execute | Handler | 结果/错误 |
| 16 | output shape：归档 → fold → trust clamp → metadata | OutputShaper | 模型可见结果 |
| 17 | surface map：MCP / CLI / HTTP / builtin | surface | 协议结果 |
| 18 | audit / access / metrics | surface/runtime | 事件 |

固定语义：

- **步骤 0 不是授权**；`profile/tools` 只决定“是否存在”；
- **步骤 4 的 deny 恒优先**；`AllowlistEnabled && empty` 等于全拒；
- **步骤 11 的 bypass 只跳过 mode 的 ask/deny**，不跳过步骤 4、5、14；
- **步骤 13 的 HardAsk 不被 bypass 解决**；`dont_ask` 把 ask 变成 deny；
- **步骤 14 必须检查实际目标参数**，不能只信工具名；
- **direct call / CLI 从步骤 0 走到 14**；`CanDirectCall` 只表示“可被
  非 MCP 表面直接调用”，不是跳过 gate 的开关；
- 远程 `Sandbox.CheckCommand` 是步骤 14 的一部分，不是另一个 allow 表。

#### 10.5.1 复审发现：安全门必须 hard

对照现有 `policy.Engine` 代码后发现两个需要修正的行为：

1. 显式 allow rule 在 `firstMatchingRule` 阶段可以先于
   shell breaker / sensitive-write 返回 allow，从而短路安全门；
2. callback 在 mode/敏感门之后可以把部分 `deny` 改成 `allow`；
   现有代码只对 `HardAsk` 做了回退保护，`dont_ask`/plan deny 仍可能被覆盖。

这与“安全不变量单调”不一致。方案调整为：

- shell breaker、sensitive-write 是 **hard safety gates**，必须先于
  allow rule/grants/callback 判定；
- allow rule 只能放宽普通 mode/allowlist，不能放行 hard safety deny；
- callback 可以 deny、ask、patch；**不能把 policy deny 改为 allow**；
  只能对已经是 `ask` 且非 `HardAsk` 的决策做批准；
- `dont_ask`、plan deny、HardAsk、sandbox/command deny 不可被 callback 覆盖；
- 为兼容旧行为，可提供 `policy_legacy_safety_order` 开关，默认关闭，
  只保留一个发布周期；开启必须记录风险并禁止用于生产 target。

PR3 必须补测试：

- `TestAllowRuleCannotBypassShellBreaker`
- `TestCallbackCannotOverrideDontAsk`
- `TestCallbackCannotOverridePlanDeny`
- `TestCallbackCannotOverrideSandboxDeny`

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

### 10.7 错误、重试与幂等（P1）

#### 10.7.1 错误分类

`ErrorCode` 使用稳定字符串；`ErrorCode` 优先于 `Err` 的文本：

| 类别 | 规范码 |
|------|--------|
| 参数/发现 | `INVALID_ARGUMENT`、`TOOL_NOT_FOUND`、`UNSUPPORTED` |
| 策略/审批 | `TOOL_DENIED`、`CAPABILITY_DENIED`、`PATH_DENIED`、`COMMAND_DENIED`、`APPROVAL_REQUIRED`、`ARTIFACT_SCOPE_DENIED` |
| 执行 | `COMMAND_FAILED`、`TIMEOUT`、`CANCELLED`、`OUTPUT_LIMIT_EXCEEDED`、`NOT_FOUND`、`WORKSPACE_UNAVAILABLE`、`ARTIFACT_WRITE_FAILED`、`ARTIFACT_EXPIRED` |
| 传输 | `REMOTE_UNAVAILABLE`、`RATE_LIMITED`、`PROTOCOL_ERROR`、`REMOTE_RESPONSE_TOO_LARGE` |
| 内部 | `INTERNAL_ERROR` |

旧码 `INVALID_ARGS` 作为 `INVALID_ARGUMENT` 的兼容别名归一；不要新增第二套拼写。

#### 10.7.2 表面映射

| 类别 | MCP | HTTP | CLI exit |
|------|-----|------|----------|
| 参数 | `IsError=true` + `Meta.error_code` | 400 | 2 |
| 工具不存在 | JSON-RPC error | 404 | 3 |
| 策略/审批 | `IsError=true` + `Meta` | 403 | 3 |
| 超时 | `IsError=true` + `Meta` | 408 | 5 |
| 取消 | `IsError=true` + `Meta` | 409 | 5 |
| 输出超限 | `IsError=true` + `Meta` | 413 | 4 |
| 远程响应过大 | JSON-RPC/protocol error | 413 | 6 |
| 限流 | JSON-RPC error / 429 | 429 + `Retry-After` | 6 |
| 传输不可用 | JSON-RPC/protocol error | 503 | 6 |
| 内部 | `IsError=true` + `Meta` | 500 | 4 |

MCP 工具业务失败用 `IsError`，JSON-RPC/HTTP 协议失败用协议错误；
不得把策略拒绝包装成 500，也不得把内部错误伪装成“模型参数错误”。

#### 10.7.3 重试规则

- **只读工具**（view/ls/glob/grep/artifact_read）允许在 transport
  失败后重连重试 **最多一次**；
- **写/编辑**（write/edit/append_write/multiedit/apply_patch）**禁止自动重试**；
- **shell** 禁止自动重放；命令可能有副作用，超时/断连后由调用方决定；
- `job_*` 为 local-only；未来 v2 若远程化，必须有 idempotency key 才能重试；
- `CallTool` 前同步 `ensureClient` 重连是允许的；已经发出的请求不得
  在不知道是否执行的情况下重放；
- 429 必须遵守 `Retry-After`，最多重试一次，禁止紧循环；
- 所有重试计入 audit `retry_count`，并保留同一 `ToolCallID`。

#### 10.7.4 幂等与前置条件

| 操作 | 幂等 | 规则 |
|------|------|------|
| view/ls/glob/grep/artifact_read | ✅ | 可安全重试 |
| write/edit/append_write/multiedit/apply_patch | ❌ | 不自动重试；支持 `expected_sha256` 等前置条件时先校验 |
| shell | ❌ | 不自动重放；非零 exit 是内容结果 |
| fetch/download | ❌/部分 | download 落盘，按写操作处理 |
| job_start（未来 v2） | ❌ | 需要 idempotency key + job 所有权 |

错误判定顺序：**参数 → 策略/审批 → 执行 → 输出整形**。同一错误只能有一个类别，
不得同时返回 `TOOL_DENIED` 和 `INTERNAL_ERROR`。

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
  "schema_version": 1,
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

### 12.4 可观测性接口（P1）

v1 不把 `Logger` / `Tracer` / `Metrics` 放进 toolcore `Handler`；core 只返回
结构化 `Result`/`ErrorCode`。surface 通过统一 sink 记录：

```go
type SurfaceObserver interface {
    Event(ctx context.Context, event Event)
    Metric(ctx context.Context, metric Metric)
}

type Event struct {
    SchemaVersion int
    Surface       string   // builtin | mcp | remote | cli | http
    Origin        string   // local | remote | cli | test
    TrustLevel    string
    Target        string
    Tool          string
    Capabilities  []string
    ToolCallID    string
    TraceID       string
    SessionID     string
    Decision      string   // allow | ask | deny
    Stage         string   // policy stage, e.g. mode / sandbox
    ErrorCode     string
    DurationMS    int64
    BytesIn       int
    BytesOut      int
    ArtifactRef   string
    RetryCount    int
}
```

规则：

- `Event.SchemaVersion` 从 1 开始；新增字段只能追加，不改变已有字段语义；
- audit、access log、metrics 是三个 sink，可以共用事件，但落盘格式独立版本化；
- 不记录原始 token、完整命令、文件内容；命令只记录 preview/hash；
- audit 写入失败不阻塞工具结果，但必须计数并输出到 stderr/health；
- access/audit log 必须有 rotation/retention 配置，禁止无界增长；
- metrics label 必须限制基数（tool/surface/decision），禁止把路径/命令作为 label；
- `Progress` 事件是 local-only（v1 远程无流式）；不进 core 契约；
- 指标名稳定：`tool_calls_total`、`tool_failed_total`、`tool_denied_total`、
  `tool_duration_ms`、`tool_bytes_in/out`、`artifact_scope_denied_total`、
  `tool_metadata_rejected_total`。

---

## 13. 迁移路线

迁移原则：**小步、可回滚、每阶段都能独立构建与验证**。不要一次性重写所有工具。

### 13.0 工具清单与迁移矩阵（P1）

本表以 `internal/toolregistry/registry.go` 与
`internal/tools/manager.go` 的实际注册表为准；它是迁移基线，不是第二份工具注册表。

| 工具 | 别名 | 组 | v1 core | Runtime 依赖 | 表面 | 阶段/说明 |
|------|------|----|---------|--------------|------|-----------|
| `view` | `read` | read | ✅ | Path / Output | builtin/MCP/remote/CLI | P2；别名同实现 |
| `ls` | — | read | ✅ | Path | 全部 | P2 |
| `glob` | — | read | ✅ | Path / ignore | 全部 | P2 |
| `grep` | `rg` | read | ✅ | Path / ripgrep fallback | 全部 | P2 |
| `write` | — | edit | ✅ | Path / Permission / Atomic | 全部 | P3 |
| `edit` | — | edit | ✅ | Path / Permission | 全部 | P3 |
| `append_write` | — | edit | ✅ | Path / Permission | 全部 | P3 |
| `multiedit` | — | edit | ✅ | Path / Permission | 全部 | P3 |
| `apply_patch` | — | edit | ✅ | patch parser / Path / Mutation | 全部 | P3 |
| `shell` | `bash`、`execute_shell_command` | shell | ✅ final | Command / Env / Artifact | 全部 | P4；无 PTY/job |
| `artifact_read` | — | io | ✅ interface | ArtifactRef / ReadScope | 全部 | P1/P2；local/remote |
| `aicli_exec` | — | shell | ❌ local-only | 本地 aicli binary | builtin/CLI | 非远程可移植 |
| `fetch` | — | webfetch | ❌ local-only v1 | HTTP / URL policy | builtin/CLI | 需 SSRF 防护 |
| `download` | — | webfetch | ❌ local-only v1 | HTTP / write | builtin/CLI | write-like |
| `web_search` | — | search | ❌ local-only v1 | providers / network | builtin/CLI | 多 provider |
| `sourcegraph` | — | search | ❌ local-only v1 | network | builtin/CLI | 公网检索 |
| `openai_image_generate` | `image_generate` | io | ❌ local-only v1 | provider / output dir | builtin/CLI | 条件注册 |
| `code_search` / `code_inspect` / `code_navigate` / `code_references` / `code_callers` | — | read | ❌ local-only v1 | knowledge / LSP | builtin/CLI | code gate |
| `todos` | — | control | ❌ local-only v1 | session state | builtin/CLI | stateful |
| `job_start` / `job_output` / `job_list` / `job_search` / `job_cancel` / `job_monitor` / `job_control` / `job_input` | `background_task` 等 | orchestration | ❌ local-only v1 | JobRuntime | builtin/CLI | 不进入 core；远程现有 job_* 仅兼容扩展，默认关闭 |
| `subagent_*` / `team_*` / `supervision_*` / `read_agent_result` | 多个 | orchestration | ❌ not core | control plane | builtin/CLI | 控制面，永不进 core |
| `schedule_*` | — | orchestration | ❌ not core | scheduler | builtin/CLI | 控制面 |
| `get_goal` / `read_goal` / `update_goal` | — | control | ❌ not core | goal state | builtin/CLI | 控制面 |
| `ask_user_question` / `plan_*` | — | orchestration | ❌ not core | UI / plan | builtin/TUI | TUI local |
| `search_tool` | — | search | ❌ not core | agent-side | builtin | 非 toolkit builtin |

**v1 core 的选择**：

- 文件/搜索：`view`、`ls`、`glob`、`grep`；
- 写入：`write`、`edit`、`append_write`、`multiedit`、`apply_patch`；
- 执行：`shell` 的 final-result 形态；
- 产物：`artifact_read` 的接口与 scope 路由。

其余工具保持现有表面，不为了“统一”而搬进 core。特别是：

- `aicli_exec` 依赖本机 aicli，不适合作为跨 target 工具；
- `fetch`/`download`/`web_search`/`sourcegraph` 需要单独的 URL/网络策略；
- `code_*` 依赖 knowledge/LSP 索引；
- `job_*`、subagent/team、schedule、goal、plan 属于控制面，不是文件工具；
- 远程现有 `job_*` 是历史 surface 扩展，v1 不纳入 core，默认不暴露。

### Phase 0：冻结契约与基线

交付：

- `internal/toolcore` 空壳 + `Definition`/`Handler`/`Runtime`/`Result` 接口；
- `toolkit.Tool` → `toolcore.Handler` 的兼容 adapter；
- `ArtifactRef` / `ResultOrigin` / trust clamp 的契约测试骨架；
- §13.0 工具清单与 `toolregistry` / `manager.go` 的双向一致性测试；
- MCP surface 的 auth/roots/name/limit/error 映射测试骨架；
- 错误码/重试/幂等/版本兼容 golden fixtures；
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

### 13.7 版本与兼容策略（P1）

#### 13.7.1 版本维度

| 对象 | 字段 | v1 值 | 规则 |
|------|------|-------|------|
| toolcore 契约 | `ContractVersion` | `1` | 破坏性变更必须升 major |
| 工具 schema | `Definition.Version` | 各工具独立 | 参数/语义变更必须升版本 |
| MCP 协议 | negotiated version | 由 SDK 决定 | 不私下扩展协议 |
| audit/access 事件 | `SchemaVersion` | `1` | 只追加字段 |
| remote 配置 | `config_version` | `1` | 未知字段忽略 |
| artifact ref | `artifact_ref` | `art_...` / `remote:<target>:art_...` | v1 固定 |

#### 13.7.2 兼容规则

- 新增字段只能追加；不改变已有字段语义；
- 工具参数保持 `additionalProperties: false`；新增参数必须升
  `Definition.Version`，旧客户端不传时走默认值；
- 别名至少保留一个大版本；删除别名前先标记 deprecated 并在
  `tools/list` 元数据中暴露；
- 新客户端 + 旧 server：缺少的 metadata 按“无声明”处理；
  不假设 `artifact_ref`、`trust_level` 一定存在；
- 旧客户端 + 新 server：canonical name、`artifact_id` 兼容字段、
  未知 metadata 忽略；
- 新旧版本都必须在 `tools/list` 暴露 `contract_version` /
  `schema_version`，便于诊断；
- 远程 binary 与本地客户端独立升级；不要求版本完全一致，但必须
  通过 capability negotiation 判断是否支持；
- artifact namespace 是加法：旧裸 `art_...` 继续可读，新远程输出写
  `remote:<target>:art_...`。

#### 13.7.3 Rollout 与回滚

- 功能开关：`toolcore_output`、`toolcore_read`、`toolcore_mutate`、
  `toolcore_shell`、`toolcore_cli`；默认按阶段关闭；
- 临时兼容开关 `policy_legacy_safety_order`：默认关闭，只保留一个发布周期；
  开启时不得用于生产 target，并记录风险；
- 远程现有 `job_*` 作为 legacy 开关，默认关闭，不进入 v1 core；
- 先在 TestRuntime + conformance 跑通，再在单个 target（238）canary；
- canary 观察：错误率、p95 延迟、输出字节、429、artifact scope denied；
- 部署由外部 `itsm-deploy-238` / 发布流水线负责；**toolcore 不内置部署脚本**，
  工具只提供 health/status/版本查询；
- 回滚只关 feature flag 或回退 binary，不删除旧 artifact/旧表；
- 破坏性迁移必须提供双写/双读窗口和回滚脚本，且单独评审。

### 13.8 网络类工具迁移（v1 不迁移）

`fetch`、`download`、`web_search`、`sourcegraph`、`openai_image_generate`
在 v1 **不进入 core**，保持 local-only；原因是它们引入了文件工具没有的
网络信任边界：

- URL/域名 allowlist、私网/回环/云 metadata 拒绝、重定向复检、DNS rebinding；
- API key / provider 凭证的注入、脱敏和轮换；
- 响应大小、content-type、超时、限速、重试/缓存；
- `download` 同时是网络 + 写文件，必须走路径/原子写/覆盖策略；
- `web_search` 是多 provider、fallback、成本/速率限制；
- 远程 target 出网策略与本地完全不同，不能默认共享。

进入条件（全部满足才排期）：

1. **NetworkPolicy** 契约：

```go
type NetworkPolicy interface {
    CheckURL(ctx context.Context, rawURL string, op NetworkOp) (URLDecision, error)
    CheckRedirect(ctx context.Context, from, to string) error
}
```

2. **SecretProvider** 契约：按 target/session 取凭证，禁止从进程环境
   任意继承；日志只记录 hash/名称；
3. **HTTPClient** 可注入，测试可替换；DNS/重定向/超时/最大字节可配置；
4. 输出进入共享 output core：fetch body、search 结果、图片走 artifact；
5. surface 策略明确：先 builtin/CLI，MCP local 次之；远程 target 必须
   单独 opt-in 出网 allowlist。

推荐迁移顺序：

| 顺序 | 工具 | 类型 | 关键验收 |
|------|------|------|----------|
| N1 | `fetch` | 只读网络 | SSRF/私网/重定向/DNS、content-type、大小上限 |
| N2 | `sourcegraph` | 只读网络 | 单 provider、query 校验、rate limit、结果截断 |
| N3 | `download` | 网络 + 写 | 路径 jail、原子写、覆盖策略、Content-Length、不自动重试 |
| N4 | `web_search` | 只读网络 | 多 provider、凭证、fallback、缓存、成本/限速 |
| N5 | `openai_image_generate` | 网络 + 图片 | provider、generated-images dir、artifact、prompt 安全 |

远程化门槛：在 N1–N2 完成并通过 SSRF/DNS/重定向测试前，不允许把网络工具
放进远程 profile；`download` 远程化还必须先有远程写入策略和磁盘配额。

验收测试至少覆盖：私网/回环/metadata IP、重定向到私网、DNS rebinding、
超大响应、错误 content-type、401/403/429、取消/超时、凭证不出现在日志。

---

## 14. 文件级改动地图

| 路径 | 动作 |
|------|------|
| `internal/toolcore/contract.go` | 新增 Definition/Handler/Runtime/Result/Capability/ArtifactRef/ResultOrigin/ContractVersion |
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
| `internal/remotetools/workspace.go` | 拆分 `CaptureLimit` 与 `ModelVisibleCap`；MCP roots 只能收窄 |
| `internal/tools/manager.go` | 从 core registry 消费；`formatMCPResult` 增加 `MCPResultContext`、trust 过滤与 artifact 归属 |
| `internal/policy` | gate 顺序、错误码映射、retry/idempotency 的 golden tests |
| `internal/observability` | 统一 Event/Metric schema v1 与脱敏 |
| `internal/mcp/client` | KeepAlive、同步重连、429/Retry-After 处理 |
| `internal/mcp/transport` | `MaxMCPResponseBytes` wire cap（decode 前） |
| `internal/remotetools/server.go` | 非回环 token/TLS、429、health/metrics、access log |
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
   - wire body 超过 `MaxMCPResponseBytes` → `REMOTE_RESPONSE_TOO_LARGE`；
   - 伪造 source 不能改变 `MCPResultContext.TrustLevel`；
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
   - 非法 ID/target 字符 → 拒绝；
   - artifact 归档失败 → `ARTIFACT_WRITE_FAILED`，不返回丢失中间的 body；
   - artifact 过期 → `ARTIFACT_EXPIRED`，不回退本地同名 id。
7. **Gate order tests（P0-3）**
   - deny 优先于 allow/contractual/plan exemption；
   - bypass 仍被 hard deny / HardAsk / sandbox / command policy 拦住；
   - dont_ask 把 ask 变 deny；
   - allow rule 不能绕过 shell breaker/sensitive-write；
   - callback 不能把 dont_ask/plan/sandbox deny 改成 allow；
   - direct call / CLI 与 builtin 走同一顺序；
   - profile/tools 过滤不改变 allow/deny 结果。
8. **Local-only matrix tests（P0-4）**
   - remote schema 不含 `stdin`/`detach`/`pty`/`job_id`；
   - remote 调用这些参数返回 `INVALID_ARGUMENT`/`UNSUPPORTED`；
   - CLI 不暴露 job/PTY 子命令；
   - 本地 interactive/detach/job 回归测试保持通过。
9. **MCP surface tests（P1）**
   - 非回环无 token/TLS 启动失败；
   - roots 只能收窄，越界 root 拒绝；
   - canonical name 64 字符/hash/collision；
   - `tools/list` filter 与 trust level；
   - 429 + `Retry-After`、401/403/404/408/500/503 映射。
10. **Error/retry/idempotency tests（P1）**
    - 只读 transport 失败最多重试一次；
    - 写/编辑/shell 不自动重放；
    - 429 遵守 Retry-After；
    - `ErrorCode` 优先于文本，同一错误只有一个类别。
11. **Observability tests（P1）**
    - 事件 schema 版本与字段；
    - token/命令/文件内容脱敏；
    - audit 写失败不阻塞结果但计数。
12. **Version/compat tests（P1）**
    - 新客户端 + 旧 server、旧客户端 + 新 server；
    - schema/alias deprecation；
    - artifact namespace 双读窗口。
13. **Registry inventory test**
    - §13.0 的 core/local-only/control-plane 分类与
      `toolregistry` / `manager.go` 双向一致。

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

#### 15.2.1 Benchmark 语料

| 语料 | 规模 | 覆盖 |
|------|------|------|
| small-repo | 1k 文件 / 10MB | 基础回归 |
| medium-repo | 10k 文件 / 100MB | grep/glob/view 主路径 |
| large-repo | 100k 文件 / 1GB | 索引/遍历/ignore |
| long-line | 单行 1MB / 文件 100MB | view 长行 clamp |
| shell-capture | 300KiB / 5MB 输出 | 归档、fold、artifact_read |
| mcp-payload | 16/32/64KiB 文本 | MCP 序列化/元数据开销 |

#### 15.2.2 初始目标（provisional）

| 指标 | 目标 | 说明 |
|------|------|------|
| read core p95 开销 | <= 5% | 相对直接调用同实现 |
| MCP 32KiB 序列化 p95 | <= 10% | 相对 in-process |
| grep 峰值内存 | 不随文件总量线性增长 | 流式/有界 buffer |
| shell capture | <= CaptureLimit | 不把完整 capture 放进模型 |
| 工具列表 | 256 tools 下 p95 <= 50ms | 含 schema/filter |
| 回归门槛 | 中位数超出基线 10% 失败 | CI 3 次取中位数 |

上述数字是初始门槛，不是承诺；第一轮 benchmark 后按机器基线校准并记录。
远程网络延迟单列，不混入 core 开销。

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
| MCP 认证误配 | 非回环无 token/TLS 暴露 | §8.7 启动 fail-closed + 配置测试 |
| 错误映射漂移 | 各 surface 返回不同码/状态 | §10.7 映射表 + golden tests |
| 工具清单漂移 | 文档/注册表/构造表不一致 | §13.0 与 toolregistry 双向契约测试 |
| 混合版本不兼容 | 新客户端连旧远程 | §13.7 capability negotiation + 兼容测试 |

### 16.2 开放问题

| # | 问题 | 状态 |
|---|------|------|
| 1 | 包名与 CLI 名：`toolcore` / `toolctl`，或并入 `aicli tool` | 开放 |
| 2 | `toolkit.Tool` 兼容接口保留多久 | 开放 |
| 3 | 远程 stateful/dedup/ledger 是否作为 v2 | 开放 |
| 4 | profile/filter 放 surface 还是 core registry | 已定：surface |
| 5 | 图片/notebook 在 CLI/HTTP 的映射细节 | 开放 |
| 6 | artifact 存储是否统一 | 已定：只统一接口/namespace，不统一实现 |
| 7 | 是否直接 import 整个 `toolkit/tools` | 已定：不；抽 core |
| 8 | 工具版本/schema 演进 | 已定：§13.7 |
| 9 | 审计归属 | 已定：surface 写，core 只发事件（§12.4） |
| 10 | 性能 p95 阈值 | 已给 provisional 目标，待基线校准（§15.2.2） |

### 16.3 威胁模型（P1）

#### 16.3.1 资产与信任边界

资产：workspace 文件、命令执行能力、secrets/env、artifact 原文、
模型上下文、audit/access log、远程 target 凭证。

信任边界：

```text
user/model
  → local runtime (trusted)
  → MCP client/server (local or remote)
  → remote target (trusted_remote / untrusted_remote)
  → subagent / CLI / HTTP client
  → external network (fetch/search)
```

#### 16.3.2 威胁与缓解

| 威胁 | 影响 | 缓解 |
|------|------|------|
| 被攻陷的远程 server 伪造 metadata | 绕过模型预算 | §9.3/§9.6 allowlist + hard cap + tail 校验 |
| 远程/本地 artifact id 混用 | 跨 scope 读原文 | §9.7 `ArtifactRef` + target 校验 + `GetOwned` |
| tool poisoning（描述/schema 注入） | 诱导模型越权 | 描述/schema 上限；untrusted 只读；canonical name；不执行描述中的“指令” |
| 路径穿越 / symlink / TOCTOU | 越界读写 | `PathResolver` + sandbox + symlink 复检；特殊文件拒绝 |
| 命令注入 / 元字符 / 危险命令 | 任意执行 | `CommandPolicy` + `shellrisk`；deny 优先；空 allowlist 全拒 |
| secrets 进入日志/预览 | 凭证泄漏 | env allowlist；token hash；命令 preview 上限；不记录文件内容 |
| SSRF / 恶意 URL | 内网探测 | fetch/download 仅 local-only；URL allowlist/私网拒绝；v1 不远程化 |
| prompt injection（远程文件/网页） | 诱导越权 | 外部内容标注为不可信；工具结果不执行指令；policy 不读内容做授权 |
| subagent 权限放大 | 子代理越权 | read-only boundary、BlockDelegation、工具白名单 |
| 并发/大输出 DoS | 资源耗尽 | maxConcurrentCalls + 429、输出硬上限、Meta 上限、超时/进程组清理 |
| direct CLI 绕过 | 绕过审批/策略 | 同一 `policy.Engine`；`--yes` 只确认 ask；无 bypass 开关 |
| token 泄漏/重放 | 未授权访问 | TLS/mTLS、token 轮换、日志只存 hash、非回环强制 token |
| 构建/部署链被篡改 | 运行恶意 binary | commit/version pinning、发布产物校验、最小权限、canary |

#### 16.3.3 残余风险

- 远程 target 被攻陷后，允许范围内的读写/命令仍可被执行；
- token 被窃取后，在轮换前仍可访问；
- 没有 OS 级容器/沙箱隔离时，workspace jail 不能防御内核/本地提权；
- 多租户共用一个 remote store 时，target 目录隔离必须由部署层保证；
- 这些风险必须在部署文档和审计中显式说明，不能声称“抽象层已解决”。

---

## 17. 第一批落地建议（可独立 PR）

### PR 0：契约冻结（文档，不写代码）

- 工具清单与迁移矩阵（§13.0）；
- MCP surface 规格（§8.7）；
- 错误/重试/幂等（§10.7）；
- 可观测性接口（§12.4）；
- 版本与 rollout（§13.7）；
- 威胁模型（§16.3）。
- CLI/direct 契约（§8.4.1）；
- benchmark 语料/阈值（§15.2.1/§15.2.2）。

验收：P0/P1 design frozen；工具清单与代码注册表一致；本 PR 不产生运行时改动。

### PR 1：Output core + remote 接入

1. 新增 `internal/toolcore/output`：
   - 预算表（复用 `tool_output_budget.go` 数值）；
   - fold/reducer/reserved tail；
   - metadata key 常量；
   - `ArtifactSink` + `ArtifactRef`；
   - `ResultOrigin` + `RemoteOutputHardCap` + trust clamp。
2. `internal/remotetools` 的 `view/grep/glob/ls/shell` 改用共享 output；
3. `internal/tools/manager.go` 的 `formatMCPResult` 增加 `MCPResultContext`、
   远程 trust 过滤与 artifact 归属；裸 `art_...` 按 server name 归到 remote target；
4. `internal/mcp/transport` 增加 `MaxMCPResponseBytes`（decode 前）；
5. `internal/remotetools/artifact.go` 输出 `remote:<target>:art_...`；
6. 删除 `foldRemoteOutput` 的重复逻辑；`boundedWriter` 如仍用于 capture，
   只保留 capture 职责；
7. 补契约测试：
   - remote Result(meta) → MCP Meta → local formatMCPResult → render 不二次 fold；
   - artifact 先归档后折叠；
   - 恶意 skip/budget/tail 被 clamp/忽略；
   - 跨 scope artifact 被拒绝；
   - wire 超限返回 `REMOTE_RESPONSE_TOO_LARGE`；
   - 归档失败返回 `ARTIFACT_WRITE_FAILED`；
   - 本地/远程同输入输出一致。
8. 重建 `aicli-remote-tools`，部署 238，跑 conformance。

验收：

- 输出预算与本地一致；
- 远程 wire 大小下降/稳定；
- 无二次截断；
- 恶意 metadata 不能突破 64 KiB ceiling；
- artifact 不跨 local/remote/target；
- wire cap 在 JSON decode 前生效；
- 归档失败/过期有明确错误码；
- 现有测试全绿。

### PR 2：Read-only core

- view/grep/glob/ls 共享 core；
- 远程复用 `internal/ripgrep` + fallback；
- conformance corpus + benchmark。

### PR 3：CLI surface

- `toolctl list/describe/run`；
- 复用 core + CLIRuntime；
- `--yes` 只确认 ask，不跳过 deny / HardAsk / sandbox；
- hard safety gates 先于 allow rule/callback；callback 不能把 deny 改为 allow；
- `policy_legacy_safety_order` 默认关闭；
- JSON/exit code 契约。

### 17.4 验收矩阵（P0/P1/PR1–3）

#### A. P0/P1 文档门

P0/P1 是**设计门**，不是代码门。文档内容已冻结：PR0 可开始；PR1 需
PR0 + P0 测试；生产/canary 需独立安全评审。

| 门 | 完成条件 | 确认证据 |
|----|----------|----------|
| P0-1 | §9.3/§9.6 的 trust clamp、Meta allowlist、wire cap、`MCPResultContext`、tail 校验 | design freeze；PR1 恶意 metadata/超大响应测试 |
| P0-2 | §9.7 的 `ArtifactRef`、严格 ID/target、归档失败/过期、跨 scope 拒绝 | design freeze；PR1 artifact scope/失败测试 |
| P0-3 | §10.5/§10.6 的 0–18 gate、hard safety、direct/CLI 不绕过 | design freeze；PR3 gate/callback 测试 |
| P0-4 | §11.1/Phase 4 的 final-result only；remote schema 无 PTY/job | design freeze；local-only matrix 测试 |
| P1 工具清单 | §13.0 与 `toolregistry` / `manager.go` 双向一致 | PR0 契约测试；无未登记工具 |
| P1 MCP surface | §8.7 的 auth/roots/name/limits/error | PR0 auth/roots/name/error 测试骨架 |
| P1 错误/重试 | §10.7 的错误码与表面映射 | golden mapping；写/执行不自动重放 |
| P1 可观测性 | §12.4 的 Event schema/脱敏 | schema/redaction 测试 |
| P1 版本/rollout | §13.7 的版本轴/混合版本/feature flag | 新老版本兼容测试 |
| P1 威胁模型 | §16.3 的威胁/缓解/残余风险 | security test map |
| P1 性能 | §15.2.1/§15.2.2 的语料/阈值 | benchmark 脚本与基线报告 |
| P1 CLI/direct | §8.4.1 的 JSON/exit code/`--yes` | CLI golden tests |
| P1 网络工具 | §13.8 明确 v1 deferred；N1–N5 顺序 | 不进入 PR1–3；单独排期 |

**放行规则**：P0 全部 Approved；P1 无未决阻塞项（开放问题必须有 owner/日期）。

#### B. PR1：Output core + remote 接入

必须完成：

- `internal/toolcore/output`：预算、fold/reducer、reserved tail、metadata、
  `ArtifactRef`、`ResultOrigin`、trust clamp；
- 远程 view/grep/glob/ls/shell 改用共享 output；
- `formatMCPResult` 的 Meta allowlist、hard cap、artifact 归属；
- 删除重复 fold 逻辑；`boundedWriter` 只保留 capture 职责；
- 恶意 metadata、artifact scope、先归档后折叠、本地/远程一致性测试。

确认方式：

```powershell
go test ./internal/output ./internal/toolresult ./internal/tools ./internal/remotetools ./internal/toolkit ./internal/mcp/... -count=1
go test ./...
go vet ./...
```

238 canary：

```powershell
aicli.exe mcp test-server 238 -C 'E:\projects\itsm\itsm\.tmp\remote-238\mcp-238.yaml'
pwsh -File 'E:\projects\itsm\itsm\.tmp\remote-238\call.ps1' -Command 'itsm-deploy-238 status'
```

退出条件：

- 恶意 `skip`/budget/tail 不能突破 64 KiB ceiling；
- artifact 不跨 local/remote/target；
- 无二次 fold；
- 238 上 6 个工具调用通过；
- feature flag 可关闭并回退。

#### C. PR2：Read-only core

必须完成：

- `toolcore/viewcore`、`searchcore`、`lscore`；
- 远程复用 `internal/ripgrep` + fallback；
- view/grep/glob/ls 的 conformance 与 benchmark；
- 路径、ignore、hidden、binary、长行、symlink、CRLF 行为一致或显式标注。

确认方式：

```powershell
go test ./internal/toolcore/viewcore ./internal/toolcore/searchcore ./internal/toolcore/lscore ./internal/remotetools ./internal/toolkit/tools -count=1
go test ./...
go test -bench . -benchmem ./internal/toolcore/...
```

退出条件：

- conformance 全绿；
- 10k/100k 文件 benchmark 达到 §15.2.2 的 provisional 阈值；
- 本地/远程输出预算一致；
- 不再新增第三套 view/grep/glob/ls 实现。

#### D. PR3：CLI surface

必须完成：

- `cmd/toolctl list/describe/run/serve`；
- `CLIRuntime`；
- JSON 输出、exit code、`--yes` 语义；
- 不提供 interactive PTY / job 子命令；
- 与 builtin 共用同一 `policy.Engine` 和 `AllowToolCallWithContext`。

确认方式：

```powershell
go test ./cmd/toolctl/... ./internal/policy/... -count=1
go test ./...
```

CLI golden cases：

- 成功：exit 0 + JSON；
- 参数错误：exit 2；
- 策略拒绝：exit 3；
- 执行失败：exit 4；
- 超时：exit 5；
- 限流/不可用：exit 6；
- `--yes` 不能放行 deny / HardAsk / sandbox。

#### E. 总体确认顺序

1. **文档门**：P0/P1 review 通过；
2. **PR1**：unit + trust/artifact + 238 canary；
3. **PR2**：conformance + benchmark；
4. **PR3**：CLI golden + 同 gate；
5. **回归**：`go test ./...`、`go vet ./...`；
6. **证据归档**：测试日志、benchmark JSON、238 调用记录、rollback 记录。

任何一门未通过，不进入下一门；P1 的网络工具迁移不阻塞 PR1–3。

依赖顺序：

```text
PR0 (contracts) → PR1 (output/remote) → PR2 (read-only core)
                                      └→ PR3 (CLI)
```

PR2 与 PR3 只有在 PR1 的 core 接口冻结、write paths 不重叠时才可并行；
否则串行，避免 CLI 先于 read-only core 定型。

### 17.5 最佳实践：验收门、证据与发布

#### 17.5.1 Definition of Ready（进入实现前）

每个 PR 开始前必须满足：

- 行为规格已冻结（P0/P1 对应章节 + design freeze）；
- 输入/输出/golden fixture 已定义；
- 失败路径、错误码、重试/不重试规则已定义；
- 安全边界和威胁模型已评审；
- feature flag、kill switch、回滚路径已定义；
- 测试数据/一次性凭证、目标环境（238）和证据目录已确定；禁止真实 token 入库；
- 没有未关闭的 P0，P1 开放问题有 owner/日期。

#### 17.5.2 Definition of Done（合并/发布前）

- 代码 + 单测 + 契约测试 + conformance 全绿；
- 安全测试和恶意输入测试通过；
- benchmark 达标或明确记录回归原因；
- 238 canary 通过；
- 回滚演练通过；
- 文档、CHANGELOG、运行手册、仪表盘/告警已更新；
- 证据归档（测试日志、benchmark JSON、canary 指标、rollback 记录）。

#### 17.5.3 测试分层与 CI 门

| 层级 | 内容 | 何时跑 | 门禁 |
|------|------|--------|------|
| T0 静态 | `gofmt`、`go vet`、依赖/许可证检查 | 每次提交 | 必须 |
| T1 单元 | output/artifact/policy/工具核心；核心包 `go test -race` | 每次提交 | 必须 |
| T2 契约/兼容 | `toolkit.Tool` ↔ core、新旧版本、artifact ref | 每次提交 | 必须 |
| T3 Conformance | Local/Remote/CLI/MCP 同 fixture | 每次 PR | 必须 |
| T4 安全/对抗 | 恶意 metadata、scope、路径、命令、SSRF、tool poisoning、fuzz/属性测试 | 每次 PR（至少 nightly 全量） | 必须 |
| T5 性能/负载 | 10k/100k 文件、并发/429、长输出、race/负载 | nightly/weekly | 发布前必须 |
| T6 Canary/回滚 | 238 单 target + rollback | 发布前 | 必须 |

不允许“只在本地手工跑过”作为验收证据；T0–T4 必须能在 CI 复现。

#### 17.5.4 追溯矩阵

每个 P0/P1 条目必须映射到具体测试和证据文件：

| 条目 | 测试示例 | 证据 |
|------|----------|------|
| P0-1 | `TestRemoteMetadataTrustBoundary` | `evidence/pr1/metadata.json` |
| P0-2 | `TestArtifactScopeDenied` | `evidence/pr1/artifact.json` |
| P0-3 | `TestGateOrderAcrossSurfaces` | `evidence/pr3/gate.json` |
| P0-4 | `TestRemoteSchemaRejectsPTYAndJobs` | `evidence/pr1/schema.json` |
| P1 MCP | `TestMCPAuthRootsAndNameBudget` | `evidence/pr0/mcp.json` |
| P1 error | `TestErrorMappingGolden` | `evidence/pr1/errors.json` |
| P1 observe | `TestEventSchemaRedaction` | `evidence/pr1/events.json` |
| P1 version | `TestMixedVersionCompatibility` | `evidence/pr1/version.json` |

测试名可以调整，但“条目 → 测试 → 证据”的映射不能缺；没有映射的测试不算验收证据。

#### 17.5.5 独立安全评审

- P0-1（metadata trust）和 P0-2（artifact scope）必须由非实现者评审；
- 威胁模型（§16.3）必须逐条映射到测试；
- 至少覆盖：恶意/超大 metadata、跨 scope artifact、tool poisoning、
  path traversal/symlink、command injection、SSRF、secret redaction；
- 安全测试失败一律 no-go，不能用“已知问题”放行。
- 测试凭证使用一次性 secret；日志/证据中不得出现真实 token。

#### 17.5.6 发布门与 Canary

- 先 T0–T4 全绿，再进入 238 canary；
- 初始 canary 观察窗口：至少 30 分钟或 100 次调用（先到为准）；
- 初始阈值（需按基线校准）：
  - 错误率不高于基线 + 0.5 个百分点；
  - p95 不高于基线 + 10%；
  - 非预期 429、artifact scope denied 为 0；
  - 无数据损坏、无跨 target 读取；
- 必须有 kill switch；回滚目标 <= 10 分钟；
- 发布前完成一次“关 flag / 回退 binary”的演练。
- canary 通过后保留 24 小时观察窗；异常按同一 no-go 条件回滚。

#### 17.5.7 故障注入

至少覆盖：

- 远程断连、半开连接、KeepAlive 失败；
- 429/Retry-After、远程重启；
- 超时/取消/进程组清理；
- artifact store 满、磁盘满、权限变化；
- 旧客户端/新 server、新客户端/旧 server；
- 大输出、长行、二进制、Unicode/CRLF。

#### 17.5.8 No-go 条件

- 任一 P0 未 Approved；
- 安全/对抗测试失败；
- 出现跨 scope artifact 或 metadata 越界；
- canary 超出阈值或出现数据损坏；
- rollback 演练失败；
- 版本兼容测试失败；
- benchmark 回归超过约定阈值且没有批准。

#### 17.5.9 角色与证据

| 角色 | 责任 |
|------|------|
| Design owner | P0/P1 规格、验收标准 |
| Runtime owner | PR1–3 实现、测试、回滚 |
| Security reviewer | P0-1/P0-2/威胁模型独立评审 |
| Release owner | 238 canary、监控、回滚决策 |

证据统一放在 PR 描述或 `evidence/<pr>/`，至少包含：commit、测试命令/日志、
benchmark JSON、canary 指标、rollback 记录。

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
- `docs/tools/unified-tool-registry-plan.md`
- `docs/architecture/unified-tool-surface-registry.md`

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
| 当前 remote registry candidates | 14 个候选（read + write + shell + job_* + artifact）；v1 core 只迁移 read/write/final shell/artifact |
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
| Gate | 参数/策略/审批/执行前检查组成的固定流水线 |
| ArtifactRef | artifact 的 scope + target + id 命名空间 |
| TrustLevel | local / trusted_remote / untrusted_remote |
| OutputShaper | 统一的预算/折叠/reducer/artifact 输出治理 |
