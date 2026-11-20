# 工作区 MCP 配置与会话 MCP 覆盖分层方案

> 文档状态：已评审并批准实施  
> 日期：2026-11-18  
> 范围：在不破坏现有会话级 MCP 覆盖的前提下，支持新会话首轮消息之前管理当前工作区的持久化 MCP 配置。  
> 代码基线：当前工作区（重点涉及 `backend/internal/api/runtimeapi`、`frontend/src/components/workspace`）。

## 0. 摘要

当前 MCP 管理入口主要挂在已有 session 上。对尚未产生 `sessionId` 的 draft thread，右侧 MCP 面板因 `requiresSession` 被禁用，因此用户无法在第一轮消息前为当前工作区新增、删除、启停或重载 MCP。第一轮消息创建 session 后，后端又会依据 workspace 绑定解析 MCP 配置；这造成“配置入口晚于运行时绑定”的体验和语义断层。

本方案将 MCP 明确分为三种作用域，并新增一个以已验证 workspace 为目标的 workspace 管理 API/UI：

1. **全局配置（global/process）**：现有 `/api/runtime/mcps...`，继续管理进程级 MCP。
2. **工作区持久化配置（workspace-persistent）**：新增不依赖 session 的 workspace API，写入该 workspace 的 `.aicli/mcp.yaml`（或既有工作区 MCP 解析链选中的可写目标），第一轮消息前即可使用。
3. **会话临时覆盖（session-only）**：保留现有 `/api/runtime/sessions/{id}/runtime/mcps...` 中不带 `scope=workspace` 的临时启停语义，仅影响当前 session 的内存状态。

已有 session-bound API 中的 workspace 持久化能力继续兼容，但其实现应与新的 workspace service 共用目标解析、校验、授权、写入和缓存失效逻辑，避免两套行为漂移。首版不通过创建空 session 来获得管理入口，也不接受客户端任意原始路径作为写入目标。

## 1. 现状与问题

### 1.1 后端现状

- 全局 MCP 路由已注册在 `handler.go` 的 `/mcps` 路径族，使用 `authorizeMCPAdminMutation` 保护写操作。
- session MCP 路由已注册在 `/sessions/{id}/runtime/mcps` 路径族。
- `session_workspace_mcp_admin.go` 已实现重要的 workspace 解析能力：
  - 读取时优先使用 session 实际生效的 workspace 配置，缺失时回退全局；
  - 写入时 workspace 优先落到 `<workspace>/.aicli/mcp.yaml`，首次写入后下一次解析即可命中；
  - 已有 workspace manager 时复用同一 manager；没有 manager 时可纯写入，并让下一 turn 重新解析；
  - scope payload 已能表达 read/write path、source、workspace fallback 和候选路径。
- 该能力目前由 session 作为入口，无法服务没有 `sessionId` 的 draft thread。
- workspace 目录注册表已有 `NormalizePath` 等能力，可验证绝对路径、目录存在性并与注册目录关联；不得绕过它直接信任请求中的原始路径。

### 1.2 前端现状

- `SessionMcpSurface` 同时包含：session-only toggle、workspace 持久化 toggle、workspace 配置新增/删除、工具清单和 scope 展示。不能简单删除其持久化功能而不做兼容迁移。
- `panel-registry.ts` 将 `sessionMcp` 标记为 `requiresSession: true`；`artifact-panel.tsx` 会在无 session 时禁用该 surface。
- `WorkspaceShell` 已拥有 `runtimeClient.workspacePath`，但 workspace shell → right rail → `ArtifactPanel` 链路尚未将其传给 MCP surface。
- draft thread 没有 session metadata；`Thread` 也没有必须新增的 workspace 字段。当前 workspace 的权威来源应优先使用 `runtimeClient.workspacePath`，不能从 draft thread 猜测。

### 1.3 目标体验

在 workspace 已确定且目录有效时：

1. 用户打开新 chat、尚未发送第一条消息；
2. 右侧 MCP 面板仍可打开；
3. 面板展示该 workspace 的 MCP 配置和来源/目标状态；
4. 用户可新增、更新、删除、启停或重载 workspace MCP；
5. 首次发送消息时，后端使用同一 workspace 配置链，新增的 MCP 可被首轮运行时加载；
6. 若用户切换到已有 session，session-only 覆盖仍只作用于该 session，workspace 持久化操作仍明确标识其共享影响。

## 2. 作用域契约

### 2.1 作用域定义

| 作用域 | 存储 | 影响范围 | 前置条件 | API 入口 |
|---|---|---|---|---|
| global | 进程级 MCP 配置文件 | 当前 runtime 进程/未被 workspace 覆盖的会话 | 无 workspace 必需 | 现有 `/api/runtime/mcps...` |
| workspace | `<workspace>/.aicli/mcp.yaml` 或解析链明确的 workspace 写目标 | 共享该 workspace 的后续/现有 session | 已验证 workspace path | 新增 `/api/runtime/workspaces/mcp...` |
| session | session 内存覆盖/临时连接 | 单个 session | 已存在 session | 现有 session MCP API，不带 `scope=workspace` |

workspace API 不应在缺少 workspace path 时回退写 global。没有 workspace 时，前端隐藏/禁用 workspace 管理入口，后端返回明确的 `400`（缺少 workspace）或 `404/422`（workspace 无效），具体状态码沿用 workspace directory handler 的错误映射。

### 2.2 Workspace 标识与安全边界

- 客户端可以提交 `workspace_path` 作为当前 workspace 标识，但服务端必须调用现有 `workspaceregistry.NormalizePath`（必要时结合 registry `FindByPath`）完成规范化和目录存在性校验。
- API 响应和后续操作使用服务端规范化后的绝对路径；不接受通过 `..`、相对路径、符号链接逃逸或任意配置文件路径改变写入目标。
- 请求不得携带独立的 `config_path` 作为写入目标。workspace 配置文件名和候选顺序由服务器的 MCP path resolver 决定。
- workspace 路径必须限制在当前 runtime 已允许/已注册的 workspace 语义内；若现有 registry 不要求预注册，则至少复用其规范化校验，不新增一套宽松规则。
- 所有 workspace 写操作继续通过 `authorizeMCPAdminMutation`，保留 admin token、可信 admin role、loopback 以及 mutation policy（read-only、禁用 reload/hot reload 等）的既有约束。
- 读取 workspace MCP 状态是否需要 admin auth：沿用现有 MCP admin 读取端点的权限级别；若读取响应会暴露命令、URL、环境变量引用或配置诊断，默认使用与 session MCP 管理读取一致的 admin 保护，不下调权限。

## 3. 后端设计

### 3.1 共享 workspace MCP scope/service

从 `session_workspace_mcp_admin.go` 抽取或扩展无 session 依赖的内部能力，建议形成如下职责边界（名称可按现有代码风格调整）：

- `resolveWorkspaceMCPAdminScope(workspacePath string)`: 
  - 校验并规范化 workspace；
  - 从既有 `workspaceMCP.resolve` 获取候选、命中 source 和读目标；
  - 计算 workspace 写目标；
  - 返回 `ReadPath`、`WritePath`、`ReadSource`、`WriteSource`、是否 fallback、候选和 diagnostics。
- `workspaceMCPAdminService(scope, write bool)`: 
  - 已命中 workspace manager 时复用该 manager；
  - 尚无 manager 且是首次写入时使用 `WithApplyOnMutate(false)` 的纯写模式；
  - 继续复用 `internal/mcp/admin` 的 Add/Update/Remove/SetEnabled/Reload 和 validation。
- 统一的 mutation result/cache invalidation helper：
  - workspace manager 已存在时原地 reload；
  - 首次创建 workspace 文件时清除 workspace manager resolution/cache，使后续 session/turn 能命中新文件；
  - 对现有 session，失效受影响 workspace 的 runtime MCP surface；不能只失效某一个 session 而让同 workspace 的其它 session 使用旧 manager。

session-bound handlers 改为调用共享 resolver/service，但保留其 session-only toggle、session ID 校验、session temp connection 回收和响应字段。

### 3.2 新增 API

新增 workspace MCP 路由族，使用独立路径避免与 global 写语义混淆：

```text
GET    /api/runtime/workspaces/mcps?workspace_path=...
POST   /api/runtime/workspaces/mcps?workspace_path=...
PUT    /api/runtime/workspaces/mcps/{name}?workspace_path=...
DELETE /api/runtime/workspaces/mcps/{name}?workspace_path=...
POST   /api/runtime/workspaces/mcps/{name}/enable?workspace_path=...
POST   /api/runtime/workspaces/mcps/{name}/disable?workspace_path=...
POST   /api/runtime/workspaces/mcps/reload?workspace_path=...
```

workspace 统一由 query 参数 `workspace_path` 指定；所有路由经过同一个规范化解析函数，mutation body 只承载 MCP payload。

统一响应应包含：

```json
{
  "workspace_path": "<server-normalized-absolute-path>",
  "scope": "workspace",
  "config_path": "<server-selected-write-or-read-path>",
  "source": "project|workspace|global",
  "workspace_fallback": true,
  "candidates": [],
  "mcps": [],
  "disabled": [],
  "message": "..."
}
```

具体字段以既有 `RuntimeSessionMcpScope`/`sessionMCPAdminScope.payload()` 兼容为优先，避免复制两套前端类型。响应不得泄露未授权的敏感凭据；MCP admin service 已有的脱敏/validation 行为必须保持。

### 3.3 读取、写入与 reload 语义

- **读取**：workspace 配置存在时返回 workspace 生效配置；不存在时返回空 workspace 配置以及 `workspace_fallback`/候选信息，不把 global 条目伪装成 workspace 持久化条目。若为了展示运行时继承，需要额外字段明确标记 `source=global`，不能混淆可写目标。
- **新增/更新/删除/启停**：只写 server 选定的 workspace `WritePath`。首次新增可以创建 `<workspace>/.aicli/mcp.yaml` 及父目录，但必须沿用现有原子写、权限和配置校验。
- **启停**：workspace enable/disable 是持久化动作，会影响共享 workspace 的其它 session；session-only endpoint 继续只变更当前 session 的临时状态。UI 文案必须明确二者区别。
- **reload**：仅对该 workspace 的配置链执行 reload；禁止借 workspace endpoint 意外 reload 全局 MCP manager。没有 manager 时，reload 应返回明确的“尚未加载/无需 reload”结果或按现有 service 语义安全成功，但不得建立第二套 manager。
- **跨 scope 隔离**：workspace 写入不修改 global 文件；session-only toggle 不修改 workspace/global 文件；global API 不修改 workspace 文件。
- **失败闭合**：workspace 无效、配置 schema 错误、权限/策略拒绝、写入失败或 reload 失败均返回现有错误码/错误结构，不返回“成功但未生效”。首次纯写成功后需明确说明下一 turn/新 session 的生效边界。

### 3.4 路由、兼容与生命周期

- 在 `handler.go` 注册 workspace MCP 路由，和 global/session 路由并列，使用现有 API middleware/context。
- 保留所有现有 session MCP route，尤其是不带 `scope=workspace` 的 session-only 开关。
- 现有带 `scope=workspace` 的 session mutation 继续可用；内部转发到共享 workspace service，必要时保留 session ID 以失效 session runtime surface。
- 不为 draft thread 自动创建空 session；不在首轮 chat request 中追加任意 MCP 配置 payload。
- workspace resolver/cache 的失效范围需要按规范化 workspace path 处理，避免大小写/路径分隔符导致同一 workspace 出现多个 manager。

## 4. 前端设计

### 4.1 Props 与 surface wiring

1. 从 `WorkspaceShell` 将 `runtimeClient.workspacePath` 传到 `WorkspaceRightRailSection`。
2. 从 `WorkspaceRightRailSection` 传到 `ArtifactPanel`；该 props 已存在，可复用，不修改 `Thread` 类型来承载 draft workspace。
3. 从 `ArtifactPanel`/surface mount 将 `workspacePath` 传给 MCP surface。
4. 将 MCP panel 从“仅 requiresSession”拆为：
   - workspace MCP surface：`workspacePath` 存在即可挂载；
   - session-only controls：只有 `sessionId` 存在时显示/启用。

实现可选择保留 `SessionMcpSurface` 作为兼容 surface，通过内部根据 `workspacePath` 和 `sessionId` 选择加载新 workspace API 或旧 session API；若拆出 `WorkspaceMcpSurface`，必须共享行组件、表单和 scope card，避免 UI 行为分叉。首版不应删除现有 session panel 的 workspace 持久化功能。

### 4.2 Draft 与已有 session 的行为

- **无 session、有 workspace**：加载 workspace API；允许新增、编辑、删除、workspace enable/disable、reload；不显示 session-only toggle。
- **有 session、有 workspace**：加载 workspace API 展示持久化配置，并保留 session-only toggle；workspace action 明确标注“影响该 workspace 的其它 session”。
- **有 session、无 workspace**：保留现有 global/session 兼容行为；隐藏 workspace-only action，不把 global action 命名为 workspace action。
- **无 session、无 workspace**：面板可以显示不可用状态和原因，但不能创建空 session，也不能回退到 global 写入。
- workspace 切换或 `runtimeClient.workspacePath` 变化时清除旧 scope/entries，重新加载，避免短暂展示前一 workspace 的配置。
- runtime `mcp.*` 事件仍触发 refresh；workspace mutation 成功后由 API 响应、事件和本地 refresh 共同保证 UI 状态一致。

### 4.3 API client/type/i18n

- 在 `frontend/src/api/runtime/mcp.ts` 增加 workspace list/mutation client，集中处理 workspace path 编码、错误解码和响应兼容。
- 在 `frontend/src/types/runtime/mcp.ts` 增加 workspace response/request 类型，优先复用/泛化已有 scope 类型，避免 `any`。
- 添加清晰的中英文（或项目已支持语言）文案：workspace 持久化、session-only 临时覆盖、共享影响、无 workspace、首次写入后生效边界、失败提示。
- surface 的 add/edit/delete 表单沿用已有 `RuntimeMcpForm`；不要在新面板复制校验逻辑。

## 5. 测试计划

### 5.1 后端单元/HTTP 测试

扩展 `backend/internal/api/runtimeapi/session_workspace_mcp_admin_test.go` 或新增 workspace MCP admin test，覆盖：

1. workspace path 规范化：绝对存在目录成功；相对、文件路径、不存在目录、逃逸路径失败。
2. 未授权请求、read-only policy、禁用 reload 等策略继续拒绝 workspace mutation。
3. 无 workspace API 不会回退 global 写入。
4. 首次 workspace add 创建正确的 `<workspace>/.aicli/mcp.yaml`，且 global 配置未改变。
5. workspace update/delete/enable/disable/reload 使用正确的写目标和 manager。
6. workspace 已命中独立文件时读取不混入 global 写入结果；workspace fallback 的读取/写入状态与 scope 字段准确。
7. session-only toggle 只改变当前 session 覆盖，不改变 workspace 文件；已有 `scope=workspace` 仍改变 workspace 文件。
8. workspace mutation 后同 workspace 的 runtime cache/manager 失效；下一 turn/新 session 能看见新增配置。
9. 两个不同 workspace 的配置相互隔离；同一 workspace 的多个 session 看到共享持久化变化。
10. 响应不泄露原始密钥/敏感配置，错误结构与现有 admin API 一致。

运行目标：`go test ./backend/internal/api/runtimeapi/...`（或仓库实际 package path 对应的 targeted package），必要时再运行相关 MCP/admin 包测试。

### 5.2 前端测试

扩展 `frontend/src/components/workspace/session-mcp-surface.test.tsx`，并为 workspace API/client 增加 targeted tests，覆盖：

1. draft thread + workspace path 可以挂载 MCP surface，不依赖 session ID。
2. draft 状态调用 workspace list/add/toggle/delete/reload API，且不调用 session API。
3. existing session 同时显示 workspace action 和 session-only action；两者调用不同 endpoint/scope。
4. no workspace 时 workspace mutation 不可用并显示原因，不触发 global mutation。
5. workspace 切换会清空旧条目并加载新 workspace。
6. API 错误、首次写入提示、共享影响提示可见且可访问。
7. 不破坏现有 session-only toggle、旧后端响应兼容路径和面板注册行为。

运行目标：前端相关 Vitest 文件、`eslint` 涉及文件、必要的 TypeScript/build 检查；结果中区分本功能失败和工作区既有失败。

### 5.3 静态/差异检查

- `git diff --check` 仅针对本次修改文件。
- 检查新增 route/client/type 的命名和 i18n key 是否完整。
- 检查当前 dirty diff，确保不覆盖用户已有修改；实现前重新读取相关文件和 diff。

## 6. 实施步骤与审查门

### 阶段 A：方案审查（已完成）

- 已确认采用 `/api/runtime/workspaces/mcps` 独立路由族，`workspace_path` 统一放在 query 参数中。
- 已确认 workspace path 经 `workspaceregistry.NormalizePath` 验证；缺失/无效路径 fail closed，不回退 global 写入。
- 已确认读取只展示 workspace 配置，不把 global fallback 项伪装为 workspace 项；首次写入默认创建 `<workspace>/.aicli/mcp.yaml`。
- 已确认保留 session-only 覆盖及旧 session-bound API，不创建空 session。
- 方案已批准，进入阶段 B 实施。

### 阶段 B：后端最小实现

1. 重新检查 dirty diff 和现有 session workspace tests。
2. 抽取共享 workspace scope/service，先保持现有 session route 行为不变。
3. 注册 workspace route，实现 list/mutation/reload。
4. 加入 backend targeted tests，先验证 scope isolation、首次写入和授权。

### 阶段 C：前端接入

1. 贯通 `runtimeClient.workspacePath` 到 panel surface。
2. 增加 workspace client/types，调整 panel requires-session 判据。
3. 在不删除 session controls 的前提下实现 draft/existing-session 分支。
4. 加入 targeted frontend tests、i18n、eslint/type checks。

### 阶段 D：验证与收尾

- 运行后端和前端 targeted tests、lint、diff check。
- 若全量检查受既有 dirty 状态或无关失败影响，单独记录，不通过回滚/清理规避。
- 最终报告列出修改文件、API 契约、测试命令及结果、未解决风险。

## 7. 非目标与风险

### 非目标

- 不创建空 session 以启用 MCP 面板。
- 不允许客户端以任意 `config_path` 写入 MCP 配置。
- 不在首轮 chat 请求中携带任意 MCP 配置并即时执行。
- 不重做 global MCP admin API 的权限模型或配置格式。
- 不移除既有 session-only 覆盖能力。
- 不在本次引入新的 workspace 注册/发现产品流程；仅复用现有 registry/resolver。

### 风险与缓解

- **路由设计与现有 API 风格不一致**：实现前以 `handler.go` 的 route registration 和现有 client 约定为准；若需要改路径，先更新本方案。
- **workspace manager 缓存陈旧**：所有 workspace mutation 统一通过规范化路径失效/重载；首次纯写必须清除 resolution cache。
- **现有 session surface 语义复杂**：先复用共享 service 和组件，再逐步拆分 workspace/session 展示，避免回归。
- **global fallback 被误当 workspace 配置**：response 同时返回 read/write/source/fallback，UI 明确显示；无 workspace 时后端 fail closed。
- **共享配置变更影响其它会话**：workspace action 在 UI 和 API 文案中显式说明共享范围；session-only action 保持独立。
- **dirty workspace 中存在并行修改**：每次编辑前重新查看目标文件和 diff，只做局部补丁，不重置或覆盖无关改动。

## 8. 评审结论

方案审查通过，以上路径、参数位置、workspace 校验、fallback、session 兼容和 draft 行为作为实施约束。workspace 不新增强制预注册要求，具体仍以 `NormalizePath` 与当前 resolver 的行为为准。

---

**评审结论：已批准实施。**
